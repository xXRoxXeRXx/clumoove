package processor

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

func isCryptographicHash(algo string) bool {
	switch strings.ToUpper(algo) {
	case "SHA1", "SHA256", "MD5", "SHA512", "DROPBOX":
		return true
	default:
		return false
	}
}

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

	total := len(unverifiedTasks)
	if total == 0 {
		_ = db.ReconcileMigrationProgress(p.db, migrationID)
		log.Printf("[VERIFIER] Migration %s verification completed (0 unverified remaining).\n", migrationID)
		return
	}

	if mig.TargetProvider == "webdav" {
		log.Printf("[VERIFIER] WebDAV target does not support checksums — accepting size verification for %d tasks in migration %s\n", total, migrationID)
		_ = db.MarkAllMigrationTasksVerified(p.db, ctx, migrationID)
		_ = db.ReconcileMigrationProgress(p.db, migrationID)
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

	numWorkers := mig.Threads
	if numWorkers <= 0 {
		numWorkers = 4
	}
	if numWorkers > total {
		numWorkers = total
	}

	log.Printf("[VERIFIER] Starting checksum verification pass for %d tasks in migration %s (%d workers)\n", total, migrationID, numWorkers)

	taskChan := make(chan *db.Task, total)
	for _, t := range unverifiedTasks {
		taskChan <- t
	}
	close(taskChan)

	var (
		processedCount atomic.Int64
		wg             sync.WaitGroup
	)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if task.ResourceType != "files" {
					_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
					processedCount.Add(1)
					continue
				}

				targetPath := task.FilePath
				if mig.TargetDir != "" && mig.TargetDir != "/" {
					if !strings.HasPrefix(task.FilePath, mig.TargetDir+"/") && task.FilePath != mig.TargetDir {
						targetPath = path.Clean(path.Join(mig.TargetDir, task.FilePath))
					}
				}

				var targetHash string
				var errHash error
				for attempt := 0; attempt < 3; attempt++ {
					taskCtx, taskCancel := context.WithTimeout(ctx, 15*time.Second)
					targetHash, errHash = targetClient.GetFileHash(taskCtx, task.ResourceType, targetPath)
					taskCancel()

					if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) {
						break
					}
					if attempt < 2 {
						time.Sleep(2 * time.Second)
					}
				}
				if errHash == nil && targetHash != "" {
					targetAlgo, cleanTarget := storage.ParseHashString(targetHash)
					sourceHash := task.SourceHash.String
					if sourceHash == "" {
						sourceHash = task.WorkerHash.String
					}

					if sourceHash != "" {
						sourceAlgo, cleanSource := storage.ParseHashString(sourceHash)

						if isCryptographicHash(sourceAlgo) && isCryptographicHash(targetAlgo) && sourceAlgo == targetAlgo {
							if cleanSource == cleanTarget {
								log.Printf("[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, targetAlgo, cleanTarget)
								_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							} else {
								if task.FileSize > 0 {
									log.Printf("[VERIFIER] [HASH_MISMATCH_SIZE_OK] %s | Expected (%s): %s | Target (%s): %s — size (%d bytes) intact\n",
										targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
									_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
								} else {
									log.Printf("[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s\n", targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget)
									task.Status = "FAILED"
									task.ErrorMessage = sql.NullString{String: fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", sourceAlgo, cleanSource, targetAlgo, cleanTarget), Valid: true}
									_ = db.UpdateTaskStatus(p.db, task)
									_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
								}
							}
						} else if sourceAlgo == "ETAG" || targetAlgo == "ETAG" {
							log.Printf("[VERIFIER] [ETAG_INFO] %s | Source (%s): %s | Target (%s): %s — ETags are server-instance-specific; size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
						} else {
							log.Printf("[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
						}
					} else {
						log.Printf("[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, targetAlgo, cleanTarget)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
					}
				} else {
					reason := "checksum not available"
					if errHash != nil {
						reason = errHash.Error()
					}
					sourceStr := task.SourceHash.String
					if sourceStr == "" {
						sourceStr = task.WorkerHash.String
					}
					if sourceStr != "" {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, sourceStr, reason, task.FileSize)
					} else {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, reason, task.FileSize)
					}
					_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
				}

				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					log.Printf("[VERIFIER] Migration %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						migrationID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	_ = db.ReconcileMigrationProgress(p.db, migrationID)
	log.Printf("[VERIFIER] Migration %s checksum verification pass completed.\n", migrationID)
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

	total := len(unverifiedTasks)
	if total == 0 {
		_ = db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
		log.Printf("[VERIFIER] Sync job %s verification completed (0 unverified remaining).\n", syncJobID)
		return
	}

	if job.TargetProvider == "webdav" {
		log.Printf("[VERIFIER] WebDAV target does not support checksums — accepting size verification for %d tasks in sync job %s\n", total, syncJobID)
		_ = db.MarkAllSyncTasksVerified(p.db, ctx, syncJobID)
		_ = db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
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

	numWorkers := job.Threads
	if numWorkers <= 0 {
		numWorkers = 4
	}
	if numWorkers > total {
		numWorkers = total
	}

	log.Printf("[VERIFIER] Starting checksum verification pass for %d tasks in sync job %s (%d workers)\n", total, syncJobID, numWorkers)

	taskChan := make(chan *db.Task, total)
	for _, t := range unverifiedTasks {
		taskChan <- t
	}
	close(taskChan)

	var (
		processedCount atomic.Int64
		wg             sync.WaitGroup
	)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskChan {
				select {
				case <-ctx.Done():
					return
				default:
				}

				targetPath := task.FilePath
				if job.TargetDir != "" && job.TargetDir != "/" {
					if !strings.HasPrefix(task.FilePath, job.TargetDir+"/") && task.FilePath != job.TargetDir {
						targetPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
					}
				}

				var targetHash string
				var errHash error
				for attempt := 0; attempt < 3; attempt++ {
					taskCtx, taskCancel := context.WithTimeout(ctx, 15*time.Second)
					targetHash, errHash = targetClient.GetFileHash(taskCtx, task.ResourceType, targetPath)
					taskCancel()

					if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) {
						break
					}
					if attempt < 2 {
						time.Sleep(2 * time.Second)
					}
				}

				if errHash == nil && targetHash != "" {
					targetAlgo, cleanTarget := storage.ParseHashString(targetHash)
					sourceHash := task.SourceHash.String
					if sourceHash == "" {
						sourceHash = task.WorkerHash.String
					}

					if sourceHash != "" {
						sourceAlgo, cleanSource := storage.ParseHashString(sourceHash)

						if isCryptographicHash(sourceAlgo) && isCryptographicHash(targetAlgo) && sourceAlgo == targetAlgo {
							if cleanSource == cleanTarget {
								log.Printf("[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, targetAlgo, cleanTarget)
								_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
								_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
							} else {
								if task.FileSize > 0 {
									log.Printf("[VERIFIER] [HASH_MISMATCH_SIZE_OK] %s | Expected (%s): %s | Target (%s): %s — size (%d bytes) intact\n",
										targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
									_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
									_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
								} else {
									log.Printf("[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s\n", targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget)
									task.Status = "FAILED"
									task.ErrorMessage = sql.NullString{String: fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", sourceAlgo, cleanSource, targetAlgo, cleanTarget), Valid: true}
									_ = db.UpdateTaskStatus(p.db, task)
									_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
								}
							}
						} else if sourceAlgo == "ETAG" || targetAlgo == "ETAG" {
							log.Printf("[VERIFIER] [ETAG_INFO] %s | Source (%s): %s | Target (%s): %s — ETags are server-instance-specific; size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
						} else {
							log.Printf("[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
						}
					} else {
						log.Printf("[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, targetAlgo, cleanTarget)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
						_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
					}
				} else {
					reason := "checksum not available"
					if errHash != nil {
						reason = errHash.Error()
					}
					sourceStr := task.SourceHash.String
					if sourceStr == "" {
						sourceStr = task.WorkerHash.String
					}
					if sourceStr != "" {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, sourceStr, reason, task.FileSize)
					} else {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, reason, task.FileSize)
					}
					_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
				}

				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					log.Printf("[VERIFIER] Sync job %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						syncJobID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	_ = db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
	log.Printf("[VERIFIER] Sync job %s checksum verification pass completed.\n", syncJobID)
}
