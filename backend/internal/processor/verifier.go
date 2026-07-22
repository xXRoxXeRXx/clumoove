package processor

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

// RunChecksumVerifier periodically checks for migrations in VERIFYING state
// and performs post-migration cryptographic checksum validation.
func (p *Processor) RunChecksumVerifier(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.processVerifyingMigrations(ctx)
			p.processVerifyingSyncJobs(ctx)
		}
	}
}

func (p *Processor) processVerifyingMigrations(ctx context.Context) {
	query := `SELECT id FROM migrations WHERE status = 'VERIFYING'`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	var migIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			migIDs = append(migIDs, id)
		}
	}

	for _, migID := range migIDs {
		p.verifyMigrationChecksums(ctx, migID)
	}
}

func (p *Processor) verifyMigrationChecksums(ctx context.Context, migrationID string) {
	mig, err := db.GetMigration(p.db, migrationID)
	if err != nil || mig.Status != "VERIFYING" {
		return
	}

	unverifiedTasks, err := db.GetUnverifiedCompletedTasks(p.db, ctx, migrationID)
	if err != nil {
		log.Printf("[VERIFIER] Error fetching unverified tasks for migration %s: %v\n", migrationID, err)
		return
	}

	if len(unverifiedTasks) == 0 {
		_ = db.ReconcileMigrationProgress(p.db, migrationID)
		log.Printf("[VERIFIER] Migration %s verification completed (0 unverified remaining).\n", migrationID)
		return
	}

	targetPass := ""
	if mig.TargetPasswordEncrypted != "" {
		dec, err := crypto.Decrypt(mig.TargetPasswordEncrypted, p.secretKey)
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		log.Printf("[VERIFIER] Failed to refresh target OAuth token for migration %s: %v\n", migrationID, err)
		return
	}

	targetKey := fmt.Sprintf("%s:target", mig.ID)
	targetClient, cleanup, err := p.getOrCreateProvider(ctx, targetKey, mig.TargetProvider, mig.TargetURL, mig.TargetUsername, targetPass)
	if err != nil {
		log.Printf("[VERIFIER] Failed to connect to target provider for verification on migration %s: %v\n", migrationID, err)
		return
	}
	defer cleanup()

	log.Printf("[VERIFIER] Starting checksum verification pass for %d tasks in migration %s\n", len(unverifiedTasks), migrationID)

	for _, task := range unverifiedTasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if task.ResourceType != "files" {
			_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
			continue
		}

		targetHash, errHash := targetClient.GetFileHash(ctx, task.ResourceType, task.FilePath)
		if errHash == nil && targetHash != "" {
			sourceHash := task.SourceHash.String
			if sourceHash == "" {
				sourceHash = task.WorkerHash.String
			}

			if sourceHash != "" {
				sourceAlgo, cleanSource := storage.ParseHashString(sourceHash)
				targetAlgo, cleanTarget := storage.ParseHashString(targetHash)

				if (sourceAlgo == targetAlgo || sourceAlgo == "UNKNOWN" || targetAlgo == "UNKNOWN") && cleanSource != "" && cleanTarget != "" {
					if cleanSource == cleanTarget {
						log.Printf("[VERIFIER] Checksum match confirmed for %s (hash=%s)\n", task.FilePath, targetHash)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
					} else {
						log.Printf("[VERIFIER] Checksum MISMATCH detected for %s: source=%s target=%s\n", task.FilePath, cleanSource, cleanTarget)
						task.Status = "FAILED"
						task.ErrorMessage = sql.NullString{String: "checksum mismatch during post-verification", Valid: true}
						_ = db.UpdateTaskStatus(p.db, task)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
					}
					continue
				}
			}

			// If algorithms differ or source hash was missing, mark verified with retrieved target hash
			_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
		} else {
			// Target checksum not available or unsupported; accept size verification
			_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
		}
	}

	// Re-reconcile migration progress after verification pass
	_ = db.ReconcileMigrationProgress(p.db, migrationID)
}

func (p *Processor) processVerifyingSyncJobs(ctx context.Context) {
	query := `SELECT id FROM sync_jobs WHERE status = 'VERIFYING'`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	var syncIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			syncIDs = append(syncIDs, id)
		}
	}

	for _, syncID := range syncIDs {
		p.verifySyncJobChecksums(ctx, syncID)
	}
}

func (p *Processor) verifySyncJobChecksums(ctx context.Context, syncJobID string) {
	job, err := db.GetSyncJob(p.db, syncJobID)
	if err != nil || job.Status != "VERIFYING" {
		return
	}

	unverifiedTasks, err := db.GetUnverifiedCompletedSyncTasks(p.db, ctx, syncJobID)
	if err != nil {
		log.Printf("[VERIFIER] Error fetching unverified tasks for sync job %s: %v\n", syncJobID, err)
		return
	}

	if len(unverifiedTasks) == 0 {
		_ = db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
		log.Printf("[VERIFIER] Sync job %s verification completed (0 unverified remaining).\n", syncJobID)
		return
	}

	targetPass := ""
	if job.TargetPasswordEncrypted != "" {
		dec, err := crypto.Decrypt(job.TargetPasswordEncrypted, p.secretKey)
		if err == nil {
			targetPass = dec
		}
	}

	targetKey := fmt.Sprintf("%s:target", job.ID)
	targetClient, cleanup, err := p.getOrCreateProvider(ctx, targetKey, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
	if err != nil {
		log.Printf("[VERIFIER] Failed to connect to target provider for verification on sync job %s: %v\n", syncJobID, err)
		return
	}
	defer cleanup()

	log.Printf("[VERIFIER] Starting checksum verification pass for %d tasks in sync job %s\n", len(unverifiedTasks), syncJobID)

	for _, task := range unverifiedTasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		targetHash, errHash := targetClient.GetFileHash(ctx, task.ResourceType, task.FilePath)
		if errHash == nil && targetHash != "" {
			sourceHash := task.SourceHash.String
			if sourceHash == "" {
				sourceHash = task.WorkerHash.String
			}

			if sourceHash != "" {
				sourceAlgo, cleanSource := storage.ParseHashString(sourceHash)
				targetAlgo, cleanTarget := storage.ParseHashString(targetHash)

				if (sourceAlgo == targetAlgo || sourceAlgo == "UNKNOWN" || targetAlgo == "UNKNOWN") && cleanSource != "" && cleanTarget != "" {
					if cleanSource == cleanTarget {
						log.Printf("[VERIFIER] Checksum match confirmed for sync file %s (hash=%s)\n", task.FilePath, targetHash)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
						_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, task.FilePath, targetHash)
					} else {
						log.Printf("[VERIFIER] Checksum MISMATCH detected for sync file %s: source=%s target=%s\n", task.FilePath, cleanSource, cleanTarget)
						task.Status = "FAILED"
						task.ErrorMessage = sql.NullString{String: "checksum mismatch during post-verification", Valid: true}
						_ = db.UpdateTaskStatus(p.db, task)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
					}
					continue
				}
			}

			_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
			_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, task.FilePath, targetHash)
		} else {
			_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
		}
	}

	// Return job status to IDLE
	_ = db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
}
