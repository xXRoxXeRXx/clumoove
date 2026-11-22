package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type syncDirCreator interface {
	sanitize.DirectoryLister
	CreateDirectory(ctx context.Context, resourceType, path string) error
}

type syncTransferTarget interface {
	CreateParentDirectories(ctx context.Context, resourceType, path string) error
	overwritePromoter
	transferTarget
	storage.FileHasher
	atomicRenameChecker
	VerificationMode() storage.VerificationMode
}

// processSyncTask handles execution of a single task belonging to a sync job.
func (p *Processor) processSyncTask(ctx context.Context, payload *queue.Payload, threadID int, pool *providerPool) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.activeTasks.Store(payload.TaskID, activeTaskInfo{
		syncJobID:      payload.SyncJobID,
		syncGeneration: payload.PassGeneration,
		cancel:         cancel,
	})
	defer func() {
		p.activeTasks.Delete(payload.TaskID)
	}()

	// 1. Fetch Sync Job
	job, err := p.syncs.GetSyncJob(payload.SyncJobID)
	if err != nil {
		return fmt.Errorf("failed to fetch sync job: %w", err)
	}
	ctx = storage.WithLocalUserScope(ctx, job.UserID)
	task, err := p.tasks.GetTask(payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}
	task.ClaimEpoch = payload.ClaimEpoch
	if task.SyncJobID != payload.SyncJobID || task.PassGeneration != job.RunGeneration {
		return nil
	}

	// A paused worker must acknowledge cancellation durably; do not return it to
	// PENDING where a resumed pass could claim it.
	if job.Status == "PAUSED_CONNECTION_LOSS" || job.Status == "PAUSED" {
		task.Status = "CANCELLED"
		_ = p.syncs.UpdateClaimedSyncTaskStatus(ctx, task)
		return nil
	}

	if job.Status == "COMPLETED" || job.Status == "FAILED" {
		task.Status = "SKIPPED"
		_ = p.syncs.UpdateClaimedSyncTaskStatus(ctx, task)
		return nil
	}

	// Parse action/metadata
	var meta map[string]interface{}
	if err := json.Unmarshal(task.Metadata, &meta); err != nil {
		processorLogf("[Worker] Failed to parse task metadata for task %s: %v", task.ID, err)
		return fmt.Errorf("failed to parse task metadata: %w", err)
	}
	action, _ := meta["action"].(string)
	side, _ := meta["side"].(string)

	processorLogf("[Worker %s] Thread %d -> Request: [%s] %s (%d bytes) [%s -> %s]\n",
		p.workerID, threadID, strings.ToUpper(action), task.FilePath, task.FileSize, job.SourceProvider, job.TargetProvider)

	// Decrypt credentials
	sourcePass, err := crypto.DecryptWithDomain(job.SourcePasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.SourceProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}

	targetPass, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}

	sourceProviderPass, err := p.tokenRefresher().EnsureFreshSyncToken(ctx, job, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}

	targetProviderPass, err := p.tokenRefresher().EnsureFreshSyncToken(ctx, job, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}

	sourceMegaSession, err := megasecret.DecodeProviderMegaSession(job.SourceProvider, job.SourceMegaSessionIDEncrypted, job.SourceMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source MEGA session: %w", err)
	}
	defer clear(sourceMegaSession.MasterKey)
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(job.TargetProvider, job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target MEGA session: %w", err)
	}
	defer clear(targetMegaSession.MasterKey)
	poolKey := syncProviderPoolKey(job.ID, task.PassGeneration)
	sourceSpec := providerPoolSpec{
		providerType: job.SourceProvider, url: job.SourceURL, username: job.SourceUsername,
		password: sourceProviderPass, passwordEncrypted: job.SourcePasswordEncrypted,
		megaSession:            sourceMegaSession,
		megaSessionIDEncrypted: job.SourceMegaSessionIDEncrypted, megaMasterKeyEncrypted: job.SourceMegaMasterKeyEncrypted,
		threads: job.Threads,
	}
	targetSpec := providerPoolSpec{
		providerType: job.TargetProvider, url: job.TargetURL, username: job.TargetUsername,
		password: targetProviderPass, passwordEncrypted: job.TargetPasswordEncrypted,
		megaSession:            targetMegaSession,
		megaSessionIDEncrypted: job.TargetMegaSessionIDEncrypted, megaMasterKeyEncrypted: job.TargetMegaMasterKeyEncrypted,
		threads: job.Threads,
	}

	// Handle directory creation tasks (action == "mkdir").
	// Enqueued by the sync engine for directories present on one side but
	// missing on the other. We create the directory on the appropriate client
	// and skip the full file-transfer pipeline.
	if action == "mkdir" {
		// Determine which client and path to create the directory on.
		// side == "source" means create on source (two-way: target has the dir, source doesn't).
		// side == "" or "target" means create on target (one-way or two-way source->target).
		var mkClient syncDirCreator
		var mkPath string
		var mkProvider string
		if side == "source" {
			unlockMegaTarget := p.lockMegaTarget(job.SourceProvider, job.SourceURL, job.SourceUsername)
			defer unlockMegaTarget()
			mkClient, err = acquirePoolRole[syncDirCreator](pool, ctx, poolKey, "source", sourceSpec)
			if err != nil {
				return fmt.Errorf("failed to connect to source provider for mkdir: %w", err)
			}
			mkPath = task.FilePath
			mkProvider = job.SourceProvider
		} else {
			unlockMegaTarget := p.lockMegaTarget(job.TargetProvider, job.TargetURL, job.TargetUsername)
			defer unlockMegaTarget()
			mkClient, err = acquirePoolRole[syncDirCreator](pool, ctx, poolKey, "target", targetSpec)
			if err != nil {
				return fmt.Errorf("failed to connect to target provider for mkdir: %w", err)
			}
			mkPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
			mkProvider = job.TargetProvider
		}

		// Sanitize directory name
		dirName := path.Base(mkPath)
		sanitized := sanitize.SanitizeFilename(dirName, mkProvider)
		if sanitized.Changed {
			mkPath = path.Join(path.Dir(mkPath), sanitized.SanitizedName)
			processorLogf("[Worker] Sanitized directory name: %s -> %s (%s)",
				dirName, sanitized.SanitizedName, strings.Join(sanitized.Reasons, ", "))
		}

		// Check for case collisions on case-insensitive providers
		if sanitize.IsCaseInsensitive(mkProvider) {
			dirPath := path.Dir(mkPath)
			dirName := path.Base(mkPath)
			if collision, _ := sanitize.CheckCaseCollision(ctx, mkClient, task.ResourceType, dirPath, dirName); collision != "" {
				processorLogf("[Worker] Directory case collision detected: %s conflicts with %s", mkPath, collision)
				task.Status = "SKIPPED"
				task.ErrorMessage = sql.NullString{String: fmt.Sprintf("Directory skipped due to case collision with %s", collision), Valid: true}
				return p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 1, 0, 0, 0)
			}
		}

		if err := mkClient.CreateDirectory(ctx, task.ResourceType, mkPath); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", mkPath, err)
		}
		task.Status = "COMPLETED"
		task.ChecksumVerified = true
		return p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 1, 0, 0, 0) // count as 1 changed item, 0 bytes
	}

	// Setup clients depending on action
	if action == "delete" {
		var delClient fileDeleter
		var delPath string
		if side == "source" {
			delClient, err = acquirePoolRole[fileDeleter](pool, ctx, poolKey, "source", sourceSpec)
			if err != nil {
				return fmt.Errorf("failed to connect to source provider: %w", err)
			}
			delPath = task.FilePath
		} else {
			delClient, err = acquirePoolRole[fileDeleter](pool, ctx, poolKey, "target", targetSpec)
			if err != nil {
				return fmt.Errorf("failed to connect to target provider: %w", err)
			}
			delPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		}
		if err := delClient.DeleteFile(ctx, task.ResourceType, delPath); err != nil {
			if side == "source" {
				return fmt.Errorf("failed to delete source file: %w", err)
			}
			return fmt.Errorf("failed to delete target file: %w", err)
		}

		// Success
		task.Status = "COMPLETED"
		task.ChecksumVerified = true
		return p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 0, 1, 0, 0) // filesDelta=1, deletedDelta=1
	}

	if action == "conflict_copy" {
		var renClient fileRenamer
		renClient, err = acquirePoolRole[fileRenamer](pool, ctx, poolKey, "target", targetSpec)
		if err != nil {
			return fmt.Errorf("failed to connect to target provider for conflict: %w", err)
		}

		tgtPath := path.Clean(path.Join(job.TargetDir, task.FilePath))
		dir := path.Dir(tgtPath)
		base := path.Base(tgtPath)
		ext := path.Ext(base)
		nameWithoutExt := strings.TrimSuffix(base, ext)
		ts := time.Now().Format("20060102150405")
		conflictName := fmt.Sprintf("%s.conflict-%s%s", nameWithoutExt, ts, ext)
		newPath := path.Clean(path.Join(dir, conflictName))

		err = renClient.RenameFile(ctx, task.ResourceType, tgtPath, newPath)
		if err != nil {
			return fmt.Errorf("failed to rename target to conflict copy: %w", err)
		}

		// Success
		task.Status = "COMPLETED"
		task.ChecksumVerified = true
		return p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 1, 0, 0, 0) // filesDelta=1, changedDelta=1
	}

	// Handle upload and download
	var srcClient inspectingTransferSource
	var tgtClient syncTransferTarget
	var srcPath, tgtPath string
	var srcProvider, tgtProvider string

	if action == "download" {
		// Download: Target -> Source (Two-Way pull)
		srcClient, err = acquirePoolRole[inspectingTransferSource](pool, ctx, poolKey, "target", targetSpec)
		if err != nil {
			return fmt.Errorf("failed to connect to target (source) provider: %w", err)
		}

		srcPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		tgtPath = task.FilePath
		srcProvider = job.TargetProvider
		tgtProvider = job.SourceProvider
	} else {
		// Upload: Source -> Target (Standard migration style)
		srcClient, err = acquirePoolRole[inspectingTransferSource](pool, ctx, poolKey, "source", sourceSpec)
		if err != nil {
			return fmt.Errorf("failed to connect to source provider: %w", err)
		}

		srcPath = task.FilePath
		tgtPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		srcProvider = job.SourceProvider
		tgtProvider = job.TargetProvider
	}
	targetURL, targetUsername := job.TargetURL, job.TargetUsername
	if tgtProvider == job.SourceProvider {
		targetURL, targetUsername = job.SourceURL, job.SourceUsername
	}
	unlockMegaTarget := p.lockMegaTarget(tgtProvider, targetURL, targetUsername)
	defer unlockMegaTarget()
	if action == "download" {
		tgtClient, err = acquirePoolRole[syncTransferTarget](pool, ctx, poolKey, "source", sourceSpec)
		if err != nil {
			return fmt.Errorf("failed to connect to source (target) provider: %w", err)
		}
	} else {
		tgtClient, err = acquirePoolRole[syncTransferTarget](pool, ctx, poolKey, "target", targetSpec)
		if err != nil {
			return fmt.Errorf("failed to connect to target provider: %w", err)
		}
	}
	clear(sourceMegaSession.MasterKey)
	clear(targetMegaSession.MasterKey)
	// Create directories if needed
	if err := tgtClient.CreateParentDirectories(ctx, task.ResourceType, tgtPath); err != nil {
		return fmt.Errorf("failed to create target directories: %w", err)
	}

	// The stored limit initializes the throttler for a new pass. Set it again
	// for a reused throttler because mid-pass changes arrive via Pub/Sub, while
	// the map keeps the throttler for subsequent passes.
	throttler, _ := p.throttlers.LoadOrStore(job.ID, throttle.NewMigrationThrottler(job.BandwidthLimitMbps))
	jobThrottler := throttler.(*throttle.MigrationThrottler)
	jobThrottler.SetLimit(job.BandwidthLimitMbps)

	progressChan := make(chan int64, 10)
	progressDone := make(chan struct{})
	var lastByteNano = time.Now().UnixNano()
	taskStart := time.Now()

	go func() {
		defer close(progressDone)
		var bufferedBytes int64
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					if bufferedBytes > 0 {
						_ = p.syncs.AddSyncJobLiveBytesForGeneration(ctx, job.ID, task.PassGeneration, bufferedBytes)
						bufferedBytes = 0
					}
					return
				}
				bufferedBytes += bytes
				atomic.StoreInt64(&lastByteNano, time.Now().UnixNano())
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = p.syncs.AddSyncJobLiveBytesForGeneration(ctx, job.ID, task.PassGeneration, bufferedBytes)
					bufferedBytes = 0
				}
			}
		}
	}()

	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		consecutiveFailures := 0
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace &&
					time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if !stale {
					owned, err := p.tasks.HeartbeatSyncTaskClaim(ctx, task.Lease(), task.PassGeneration)
					if err == nil && !owned {
						cancel() // recovery or a new claim fenced this worker off
						return
					}
					if err != nil {
						consecutiveFailures++
						processorLogf("[Worker %s] heartbeat error for task %s (failure %d/5): %v", p.workerID, task.ID, consecutiveFailures, err)
						if consecutiveFailures >= 5 {
							cancel()
							return
						}
					} else {
						consecutiveFailures = 0
					}
				}
			}
		}
	}()

	defer func() {
		close(progressChan)
		<-progressDone
		close(heartbeatStop)
	}()

	if err := executeSyncTransfer(ctx, syncTransfer{
		Task:           task,
		Source:         srcClient,
		Target:         tgtClient,
		SourceProvider: srcProvider,
		TargetProvider: tgtProvider,
		SourcePath:     srcPath,
		TargetPath:     tgtPath,
		Throttler:      jobThrottler,
		Progress:       progressChan,
	}); err != nil {
		return err
	}
	task.Status = "COMPLETED"
	return p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 1, 0, 0, task.FileSize) // filesDelta=1, changedDelta=1, bytesDelta=task.FileSize
}

// handleSyncTaskFailure registers failure and checks retry count for sync tasks.
func (p *Processor) handleSyncTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	task, err := p.tasks.GetTask(payload.TaskID)
	if err != nil {
		processorLogf("Error fetching task on sync failure handler: %v\n", err)
		return
	}
	task.ClaimEpoch = payload.ClaimEpoch

	job, jobErr := p.syncs.GetSyncJob(payload.SyncJobID)
	if jobErr == nil && task.PassGeneration != job.RunGeneration {
		return
	}
	if jobErr == nil && (job.Status == "PAUSED" || job.Status == "PAUSED_CONNECTION_LOSS" || job.Status == "COMPLETED") {
		task.Status = "CANCELLED"
		_ = p.syncs.UpdateClaimedSyncTaskStatus(ctx, task)
		return
	}

	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		task.Status = "PENDING"
		if err := p.syncs.UpdateClaimedSyncTaskStatus(ctx, task); err != nil {
			processorLogf("Error returning cancelled sync task %s to pending: %v", task.ID, err)
		}
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(procErr.Error()), Valid: true}

	isConnLoss := isNetworkError(procErr)
	if isConnLoss {
		lossCount := p.recordConnLoss(payload.SyncJobID)
		taskConnLoss := p.recordConnLossTask(task.ID)

		if lossCount < connLossEscalationThreshold && taskConnLoss < maxConnLossTaskAttempts {
			backoff := retryBackoff(taskConnLoss)
			nextRetry := time.Now().Add(backoff)
			task.Status = "FAILED"
			task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
			if err := p.syncs.UpdateClaimedSyncTaskStatus(ctx, task); err != nil {
				processorLogf("Error scheduling sync task %s retry after connection loss: %v", task.ID, err)
			}
			return
		}

		// Connection loss escalation: pause the sync job
		_ = p.syncs.UpdateSyncJobStatusForGeneration(payload.SyncJobID, task.PassGeneration, "PAUSED_CONNECTION_LOSS", nil)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(task.ID)

		task.Status = "PENDING"
		if err := p.syncs.UpdateClaimedSyncTaskStatus(ctx, task); err != nil {
			processorLogf("Error re-queueing sync task %s after connection loss: %v", task.ID, err)
		}
		return
	}

	isPermanent := isPermanentTransferError(procErr)
	errStr := procErr.Error()

	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials")

	if isAuthError {
		if role := oauthAuthFailureRoleForProviders(job.SourceProvider, job.TargetProvider, errStr); role != "" && task.Attempts <= 3 {
			if _, refreshErr := p.tokenRefresher().ForceRefreshSyncToken(ctx, job, role); refreshErr == nil {
				backoff := retryBackoff(task.Attempts)
				task.Status = "FAILED"
				task.ErrorMessage = sql.NullString{String: "OAuth access token rejected; refreshed token scheduled for retry", Valid: true}
				task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
				if err := p.syncs.UpdateClaimedSyncTaskStatus(ctx, task); err != nil {
					processorLogf("Error scheduling sync OAuth retry for task %s: %v", task.ID, err)
				}
				processorLogf("[Worker %s] OAuth 401 for sync task %s (job %s, %s) — refreshed token and retrying in %ds\n",
					p.workerID, payload.TaskID, payload.SyncJobID, role, int(backoff.Seconds()))
				return
			} else {
				processorLogf("[Worker %s] OAuth 401 recovery refresh failed for sync task %s (job %s, %s): %v\n",
					p.workerID, payload.TaskID, payload.SyncJobID, role, refreshErr)
			}
		}
		authErrMsg := "Authentication failed - please check your credentials"
		_ = p.syncs.UpdateSyncJobStatusForGeneration(payload.SyncJobID, task.PassGeneration, "FAILED", &authErrMsg)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(payload.TaskID)

		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		if err := p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 0, 0, 1, 0); err != nil {
			processorLogf("Error recording terminal sync task failure for %s: %v", task.ID, err)
			return
		}

		// Cancel other pending tasks
		cancelled, cerr := p.syncs.CancelRemainingPendingSyncTasksForGeneration(task.SyncJobID, task.PassGeneration)
		if cerr == nil && cancelled > 0 {
			if err := p.syncs.IncrementSyncJobProgressForGeneration(ctx, task.SyncJobID, task.PassGeneration, 0, 0, 0, cancelled, 0); err != nil {
				processorLogf("Error recording %d cancelled sync tasks for job %s: %v", cancelled, task.SyncJobID, err)
			}
		} else if cerr != nil {
			processorLogf("Error cancelling pending sync tasks for job %s: %v", task.SyncJobID, cerr)
		}
		return
	}

	if task.Attempts < 3 && !isPermanent {
		backoff := retryDelay(procErr, task.Attempts)
		nextRetry := time.Now().Add(backoff)
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		if err := p.syncs.UpdateClaimedSyncTaskStatus(ctx, task); err != nil {
			processorLogf("Error scheduling sync task %s retry: %v", task.ID, err)
		}
	} else {
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		if err := p.syncs.UpdateSyncTaskStatusAndIncrementProgress(ctx, task, 1, 0, 0, 1, 0); err != nil {
			processorLogf("Error recording exhausted sync task failure for %s: %v", task.ID, err)
			return
		}
		p.clearConnLossTask(task.ID)
	}
}
