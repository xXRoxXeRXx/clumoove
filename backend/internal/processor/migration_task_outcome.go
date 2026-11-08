package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

// handleTaskFailure owns all terminal and retry outcomes for a claimed
// migration task. Transfer execution returns an error; this stage decides the
// durable task and migration state while preserving the claim-epoch fence.
func (p *Processor) handleTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		processorLogf("Error fetching task on failure handler: %v\n", err)
		return
	}
	task.ClaimEpoch = payload.ClaimEpoch

	mig, migErr := db.GetMigration(p.db, payload.MigrationID)
	if migErr == nil && mig.Status == "CANCELLED" {
		processorLogf("[Worker %s] Task %s aborted (Migration cancelled).\n", p.workerID, payload.TaskID)
		task.Status = "CANCELLED"
		if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("[Worker %s] Failed to mark cancelled task %s: %v", p.workerID, task.ID, err)
		}
		return
	}

	if errors.Is(procErr, context.Canceled) || ctx.Err() != nil {
		processorLogf("[Worker %s] Shutdown detected. Requeueing task %s...\n", p.workerID, payload.TaskID)
		task.Status = "PENDING"
		if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("[Worker %s] Failed to requeue shutdown task %s: %v", p.workerID, task.ID, err)
		}
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(procErr.Error()), Valid: true}
	if p.handleMigrationConnectionLoss(ctx, payload, task, procErr) {
		return
	}

	isPermanent := isPermanentTransferError(procErr)
	errStr := procErr.Error()
	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials") ||
		strings.Contains(errStr, "invalid authentication credentials")
	if isAuthError {
		p.handleMigrationAuthenticationFailure(ctx, payload, task, mig, errStr)
		return
	}

	if task.Attempts < 3 && !isPermanent {
		backoff := retryDelay(procErr, task.Attempts)
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
		if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 0, 1, 0); err != nil {
			processorLogf("[Worker %s] Failed to record retry state for task %s: %v", p.workerID, task.ID, err)
			return
		}
		processorLogf("[Worker %s] Task %s scheduled for retry in %ds (Attempt %d/3)\n", p.workerID, task.ID, int(backoff.Seconds()), task.Attempts)
		return
	}

	task.Status = "FAILED"
	task.NextRetryAt = sql.NullTime{}
	if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
		processorLogf("[Worker %s] Failed to record terminal failure for task %s: %v", p.workerID, task.ID, err)
	}
	p.clearConnLossTask(task.ID)
	if migErr == nil && mig != nil {
		p.reconcileFileManagerTransferIfDone(mig)
	}
	processorLogf("[Worker %s] Task %s failed permanently after %d attempts\n", p.workerID, task.ID, task.Attempts)
}

func (p *Processor) handleMigrationConnectionLoss(ctx context.Context, payload *queue.Payload, task *db.Task, procErr error) bool {
	if !isNetworkError(procErr) {
		return false
	}
	processorLogf("[Worker %s] Connection loss detected: %v\n", p.workerID, procErr)
	lossCount := p.recordConnLoss(payload.MigrationID)
	taskConnLoss := p.recordConnLossTask(task.ID)
	if lossCount < connLossEscalationThreshold && taskConnLoss < maxConnLossTaskAttempts {
		backoff := retryBackoff(taskConnLoss)
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
		if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("[Worker %s] Failed to schedule connection-loss retry for task %s: %v", p.workerID, task.ID, err)
		}
		processorLogf("[Worker %s] Connection loss on task %s (migration %s): retrying in %ds (consecutive losses %d/%d, task conn-loss attempts %d)\n", p.workerID, payload.TaskID, payload.MigrationID, int(backoff.Seconds()), lossCount, connLossEscalationThreshold, taskConnLoss)
		return true
	}
	paused, pauseErr := db.PauseMigrationForConnectionLoss(p.db, payload.MigrationID)
	if pauseErr != nil {
		processorLogf("[Worker %s] Failed to pause migration %s after connection loss: %v", p.workerID, payload.MigrationID, pauseErr)
		return true
	}
	if !paused {
		processorLogf("[Worker %s] Did not pause migration %s after connection loss because its status changed", p.workerID, payload.MigrationID)
		return true
	}
	p.clearConnLoss(payload.MigrationID)
	p.clearConnLossTask(task.ID)
	p.recoveryAttempts.Delete(recoveryAttemptKey(false, payload.MigrationID))
	task.Status = "PENDING"
	if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
		processorLogf("[Worker %s] Failed to requeue task %s after pausing migration: %v", p.workerID, task.ID, err)
	}
	return true
}

func (p *Processor) handleMigrationAuthenticationFailure(ctx context.Context, payload *queue.Payload, task *db.Task, mig *db.Migration, errStr string) {
	if role := oauthAuthFailureRole(mig, errStr); role != "" && task.Attempts <= 3 {
		_, refreshErr := p.refreshOAuthToken(ctx, mig, role)
		if refreshErr == nil {
			backoff := retryBackoff(task.Attempts)
			task.Status = "FAILED"
			task.ErrorMessage = sql.NullString{String: "OAuth access token rejected; refreshed token scheduled for retry", Valid: true}
			task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
			if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
				processorLogf("[Worker %s] Failed to schedule OAuth retry for task %s: %v", p.workerID, task.ID, err)
			}
			processorLogf("[Worker %s] OAuth 401 for task %s (migration %s, %s) — refreshed token and retrying in %ds\n", p.workerID, payload.TaskID, payload.MigrationID, role, int(backoff.Seconds()))
			return
		}
		processorLogf("[Worker %s] OAuth 401 recovery refresh failed for task %s (migration %s, %s): %v\n", p.workerID, payload.TaskID, payload.MigrationID, role, refreshErr)
	}
	processorLogf("[Worker %s] Auth error detected for task %s (migration %s) — stopping migration immediately\n", p.workerID, payload.TaskID, payload.MigrationID)
	finalized, finalizeErr := db.FailMigrationForAuthentication(p.db, ctx, task, "Authentication failed — please check your credentials and start a new migration")
	p.clearConnLoss(payload.MigrationID)
	p.clearConnLossTask(payload.TaskID)
	p.recoveryAttempts.Delete(recoveryAttemptKey(false, payload.MigrationID))
	if finalizeErr != nil || !finalized {
		if finalizeErr != nil {
			processorLogf("[Worker %s] atomically finalizing auth failure for migration %s: %v\n", p.workerID, payload.MigrationID, finalizeErr)
		}
		return
	}
	if owner, oerr := db.GetMigrationOwnerID(p.db, payload.MigrationID); oerr == nil {
		db.WriteAuditLog(p.db, db.AuditEntry{UserID: sql.NullString{String: owner, Valid: true}, Action: db.AuditMigrationFailed, Target: payload.MigrationID, Details: json.RawMessage(`{"phase":"transfer","reason":"auth_error"}`)})
	}
}

// completeMigrationTask owns the durable success outcome after transfer and
// integrity work have succeeded. Keeping it alongside failure handling makes
// all terminal task state transitions local to one lifecycle stage.
func (p *Processor) completeMigrationTask(ctx context.Context, migration *db.Migration, task *db.Task) error {
	if err := p.finishMigrationTask(ctx, migration, task, task.FileSize); err != nil {
		return err
	}
	_ = db.ResetLiveBytes(p.db, ctx, migration.ID)
	p.reconcileFileManagerTransferIfDone(migration)
	return nil
}

func (p *Processor) completeMigrationDirectoryTask(ctx context.Context, migration *db.Migration, task *db.Task) error {
	if err := p.finishMigrationTask(ctx, migration, task, 0); err != nil {
		return err
	}
	p.reconcileFileManagerTransferIfDone(migration)
	return nil
}

// finishMigrationTask persists COMPLETED with the given transferred bytes and
// clears connection-loss tracking. Files and directories differ only in bytes.
func (p *Processor) finishMigrationTask(ctx context.Context, migration *db.Migration, task *db.Task, bytes int64) error {
	task.Status = "COMPLETED"
	task.ErrorMessage = sql.NullString{}
	if fileManagerTransferOperation(migration.PickerSessionID) != "" {
		task.ChecksumVerified = true
	}
	if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, bytes, 0, 0, 0); err != nil {
		return err
	}
	p.clearConnLoss(migration.ID)
	p.clearConnLossTask(task.ID)
	return nil
}

func (p *Processor) skipMigrationTask(ctx context.Context, task *db.Task, reason string) error {
	return p.skipMigrationTaskWithBytes(ctx, task, reason, task.FileSize, 1, task.FileSize)
}

func (p *Processor) skipMigrationDirectoryTask(ctx context.Context, task *db.Task, reason string) error {
	return p.skipMigrationTaskWithBytes(ctx, task, reason, 0, 0, 0)
}

func (p *Processor) skipMigrationTaskWithBytes(ctx context.Context, task *db.Task, reason string, bytes int64, skipped int, skippedBytes int64) error {
	task.Status = "SKIPPED"
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(reason), Valid: true}
	return db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, bytes, skipped, 0, skippedBytes)
}
