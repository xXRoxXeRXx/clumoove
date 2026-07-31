package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/sanitize"
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

// bestSourceHash selects the best available source hash for checksum verification.
// Provider metadata describes the indexed source object, while WorkerHash only
// describes bytes observed during the transfer; therefore a cryptographic
// SourceHash is the stronger reference when both are available.
func bestSourceHash(task *db.Task) string {
	workerHash := ""
	if task.WorkerHash.Valid {
		workerHash = task.WorkerHash.String
	}
	sourceHash := ""
	if task.SourceHash.Valid {
		sourceHash = task.SourceHash.String
	}

	// 1. Prefer cryptographic SourceHash
	if sourceHash != "" && !strings.HasPrefix(strings.ToUpper(sourceHash), "ETAG:") {
		return sourceHash
	}
	// 2. Fall back to cryptographic WorkerHash
	if workerHash != "" && !strings.HasPrefix(strings.ToUpper(workerHash), "ETAG:") {
		return workerHash
	}
	// 3. Fall back to the source ETag, which is tied to the indexed object.
	if sourceHash != "" {
		return sourceHash
	}
	// 4. Finally fall back to WorkerHash ETag.
	if workerHash != "" {
		return workerHash
	}
	return ""
}

// RunChecksumVerifier periodically checks for migrations and sync jobs in VERIFYING state
// and performs post-transfer cryptographic checksum validation.
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

type verificationPassConfig struct {
	EntityType     string // "Migration" or "Sync job"
	EntityID       string
	UserID         string
	SourceProvider string
	TargetProvider string
	TargetURL      string
	TargetUsername string
	TargetPassword string
	TargetDir      string
	Threads        int
	// TargetClient is test-only injection for a connected target. Production
	// callers leave it nil and construct a scoped provider below.
	TargetClient      storage.StorageProvider
	GetTasks          func(ctx context.Context) ([]*db.Task, error)
	OnVerified        func(task *db.Task, targetPath string, targetHash string)
	ReconcileProgress func() error
	MarkVerified      func(ctx context.Context, task *db.Task, targetHash string) (bool, error)
	MarkMismatch      func(ctx context.Context, task *db.Task) (bool, error)
	// IsStillVerifying is used for sync jobs, whose engine can cancel a
	// cross-process verifier by moving the persisted status out of VERIFYING.
	IsStillVerifying func(ctx context.Context) (bool, error)
}

func (p *Processor) runVerificationPass(ctx context.Context, cfg verificationPassConfig) {
	guardKey := fmt.Sprintf("%s:%s", strings.ToLower(cfg.EntityType), cfg.EntityID)
	if _, loaded := p.verifyingEntities.LoadOrStore(guardKey, true); loaded {
		log.Printf("[VERIFIER] Verification pass already in progress for %s %s, skipping tick.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	defer p.verifyingEntities.Delete(guardKey)

	passCtx, cancelPass := context.WithCancel(ctx)
	defer cancelPass()
	if cfg.IsStillVerifying != nil {
		stillVerifying, err := cfg.IsStillVerifying(passCtx)
		if err != nil {
			log.Printf("[VERIFIER] Cannot confirm verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return
		}
		if !stillVerifying {
			return
		}
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopWatch:
					return
				case <-passCtx.Done():
					return
				case <-ticker.C:
					stillVerifying, err := cfg.IsStillVerifying(passCtx)
					if err != nil {
						log.Printf("[VERIFIER] Cannot refresh verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
						continue
					}
					if !stillVerifying {
						log.Printf("[VERIFIER] %s %s cancelled because it is no longer VERIFYING.\n", cfg.EntityType, cfg.EntityID)
						cancelPass()
						return
					}
				}
			}
		}()
	}
	canWrite := func() bool {
		if passCtx.Err() != nil {
			return false
		}
		if cfg.IsStillVerifying == nil {
			return true
		}
		stillVerifying, err := cfg.IsStillVerifying(passCtx)
		if err != nil {
			log.Printf("[VERIFIER] Cannot confirm write ownership for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return false
		}
		return stillVerifying
	}
	markVerified := func(task *db.Task, targetHash string) bool {
		if !canWrite() {
			return false
		}
		if cfg.MarkVerified != nil {
			ok, err := cfg.MarkVerified(passCtx, task, targetHash)
			if err != nil {
				log.Printf("[VERIFIER] Failed to save verification for task %s: %v\n", task.ID, err)
			}
			return ok && err == nil
		}
		return db.MarkTaskChecksumVerified(p.db, passCtx, task.ID, targetHash) == nil
	}
	markMismatch := func(task *db.Task, message, targetHash string) {
		task.Status = "FAILED"
		task.ErrorMessage = sql.NullString{String: message, Valid: true}
		// Keep the provider-computed target hash even when it does not match.
		// This preserves both sides of the verification evidence: SourceHash (or
		// WorkerHash) remains the source-side value and TargetHash records the
		// target-side value that was actually compared.
		if targetHash != "" {
			task.TargetHash = sql.NullString{String: targetHash, Valid: true}
		}
		if !canWrite() {
			return
		}
		if cfg.MarkMismatch != nil {
			// A sync pass is being finalized by the engine; do not create new
			// runnable work during that finalization.
			task.NextRetryAt = sql.NullTime{}
			_, _ = cfg.MarkMismatch(passCtx, task)
			return
		}
		// Migrations keep their established automatic re-copy behavior.
		task.NextRetryAt = sql.NullTime{Time: time.Now(), Valid: true}
		_ = db.UpdateTaskStatus(p.db, task)
	}

	unverifiedTasks, err := cfg.GetTasks(passCtx)
	if err != nil {
		log.Printf("[VERIFIER] Error fetching unverified tasks for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return
	}

	total := len(unverifiedTasks)
	if total == 0 {
		_ = cfg.ReconcileProgress()
		log.Printf("[VERIFIER] %s %s verification completed (0 unverified remaining).\n", cfg.EntityType, cfg.EntityID)
		return
	}

	if cfg.TargetProvider == "webdav" {
		log.Printf("[VERIFIER] WebDAV target has no checksum API; validating each target size.\n")
	}

	passCtx = storage.WithLocalUserScope(passCtx, cfg.UserID)
	targetClient := cfg.TargetClient
	if targetClient == nil {
		targetClient, err = newProvider(passCtx, cfg.TargetProvider, cfg.TargetURL, cfg.TargetUsername, cfg.TargetPassword)
		if err != nil {
			log.Printf("[VERIFIER] Failed to connect to target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return
		}
		defer targetClient.Close()
	}

	numWorkers := cfg.Threads
	if numWorkers <= 0 {
		numWorkers = 4
	}
	if numWorkers > total {
		numWorkers = total
	}

	log.Printf("[VERIFIER] Starting checksum verification pass for %d tasks in %s %s (%d workers)\n", total, cfg.EntityType, cfg.EntityID, numWorkers)

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
				case <-passCtx.Done():
					return
				default:
				}

				if task.ResourceType != "files" || isDirectoryTask(task) {
					if !markVerified(task, "") {
						return
					}
					processedCount.Add(1)
					continue
				}

				targetPath := resolveTargetPath(task, cfg.TargetDir, cfg.SourceProvider, cfg.TargetProvider)

				var targetHash string
				var errHash error

				isDirErr := func(err error) bool {
					if err == nil {
						return false
					}
					msg := strings.ToLower(err.Error())
					return strings.Contains(msg, "is a directory") || strings.Contains(msg, "is a folder")
				}

				// Check size immediately before committing verification. This covers
				// checksum-unavailable and differing-algorithm fallbacks without an
				// extra provider round trip before the hash lookup.
				markVerifiedForFile := func(targetHash string) bool {
					if isDirErr(errHash) {
						log.Printf("[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
						return markVerified(task, targetHash)
					}
					exists, targetSize, sizeErr := verifyTargetSize(passCtx, targetClient, task.ResourceType, targetPath)
					if sizeErr != nil {
						if isDirErr(sizeErr) {
							log.Printf("[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
							return markVerified(task, targetHash)
						}
						log.Printf("[VERIFIER] Cannot recheck target size for %s: %v\n", targetPath, sizeErr)
						return false
					}
					if !exists || (targetSize != task.FileSize && !(task.FileSize == 0 && isDirectoryTask(task))) {
						log.Printf("[VERIFIER] [SIZE_MISMATCH] %s | Got: %d | Expected: %d\n", targetPath, targetSize, task.FileSize)
						markMismatch(task, fmt.Sprintf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize), targetHash)
						return false
					}
					return markVerified(task, targetHash)
				}
				for attempt := 0; attempt < 3; attempt++ {
					taskCtx, taskCancel := context.WithTimeout(passCtx, 15*time.Second)
					targetHash, errHash = targetClient.GetFileHash(taskCtx, task.ResourceType, targetPath)
					taskCancel()

					if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) {
						break
					}
					if attempt < 2 {
						select {
						case <-passCtx.Done():
							return
						case <-time.After(2 * time.Second):
						}
					}
				}

				if errHash == nil && targetHash != "" {
					targetAlgo, cleanTarget := storage.ParseHashString(targetHash)
					srcHash := bestSourceHash(task)

					if srcHash != "" {
						sourceAlgo, cleanSource := storage.ParseHashString(srcHash)

						if isCryptographicHash(sourceAlgo) && isCryptographicHash(targetAlgo) && sourceAlgo == targetAlgo {
							if cleanSource == cleanTarget {
								log.Printf("[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, targetAlgo, cleanTarget)
								if markVerifiedForFile(targetHash) && cfg.OnVerified != nil {
									cfg.OnVerified(task, targetPath, targetHash)
								}
							} else {
								log.Printf("[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s — marking FAILED for automatic re-copy\n",
									targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget)
								markMismatch(task, fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", sourceAlgo, cleanSource, targetAlgo, cleanTarget), targetHash)
							}
						} else if sourceAlgo == "ETAG" || targetAlgo == "ETAG" {
							log.Printf("[VERIFIER] [SIZE_VERIFIED] %s | Source (%s): %s | Target: No cryptographic hash on target (returned ETag: %s) — size (%d bytes) verified [PASSED]\n",
								targetPath, sourceAlgo, cleanSource, cleanTarget, task.FileSize)
							if markVerifiedForFile(targetHash) && cfg.OnVerified != nil {
								cfg.OnVerified(task, targetPath, targetHash)
							}
						} else {
							log.Printf("[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							if markVerifiedForFile(targetHash) && cfg.OnVerified != nil {
								cfg.OnVerified(task, targetPath, targetHash)
							}
						}
					} else {
						log.Printf("[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, targetAlgo, cleanTarget)
						if markVerifiedForFile(targetHash) && cfg.OnVerified != nil {
							cfg.OnVerified(task, targetPath, targetHash)
						}
					}
				} else {
					reason := "checksum not available"
					if errHash != nil {
						reason = sanitize.SanitizeError(errHash.Error())
					}
					srcHash := bestSourceHash(task)
					if srcHash != "" {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, srcHash, reason, task.FileSize)
					} else {
						log.Printf("[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, reason, task.FileSize)
					}
					if !markVerifiedForFile("") {
						return
					}
				}

				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					log.Printf("[VERIFIER] %s %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						cfg.EntityType, cfg.EntityID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	if passCtx.Err() != nil {
		log.Printf("[VERIFIER] %s %s verification pass aborted.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	_ = cfg.ReconcileProgress()
	log.Printf("[VERIFIER] %s %s checksum verification pass completed.\n", cfg.EntityType, cfg.EntityID)
}

func (p *Processor) verifyMigrationChecksums(ctx context.Context, migrationID string) {
	mig, err := db.GetMigration(p.db, migrationID)
	if err != nil || mig.Status != "VERIFYING" {
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

	cfg := verificationPassConfig{
		EntityType:     "Migration",
		EntityID:       migrationID,
		UserID:         mig.UserID.String,
		SourceProvider: mig.SourceProvider,
		TargetProvider: mig.TargetProvider,
		TargetURL:      mig.TargetURL,
		TargetUsername: mig.TargetUsername,
		TargetPassword: targetPass,
		TargetDir:      mig.TargetDir,
		Threads:        mig.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedTasks(p.db, ctx, migrationID)
		},
		OnVerified: nil,
		ReconcileProgress: func() error {
			return db.ReconcileMigrationProgress(p.db, migrationID)
		},
	}

	p.runVerificationPass(ctx, cfg)
}

func (p *Processor) verifySyncJobChecksums(ctx context.Context, syncJobID string) {
	job, err := db.GetSyncJob(p.db, syncJobID)
	if err != nil || job.Status != "VERIFYING" {
		return
	}

	targetPass := ""
	if job.TargetPasswordEncrypted != "" {
		dec, err := crypto.Decrypt(job.TargetPasswordEncrypted, p.secretKey)
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = p.ensureFreshSyncOAuthToken(ctx, job, "target", targetPass)
	if err != nil {
		log.Printf("[VERIFIER] Failed to refresh target OAuth token for sync job %s: %v\n", syncJobID, err)
		return
	}

	cfg := verificationPassConfig{
		EntityType:     "Sync job",
		EntityID:       syncJobID,
		UserID:         job.UserID,
		SourceProvider: job.SourceProvider,
		TargetProvider: job.TargetProvider,
		TargetURL:      job.TargetURL,
		TargetUsername: job.TargetUsername,
		TargetPassword: targetPass,
		TargetDir:      job.TargetDir,
		Threads:        job.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedSyncTasks(p.db, ctx, syncJobID)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkSyncTaskChecksumVerifiedWhileVerifying(p.db, ctx, task.ID, targetHash)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkSyncTaskChecksumMismatchWhileVerifying(p.db, ctx, task)
		},
		// The engine applies all durable sync-state changes after it owns the
		// successful finalization, so verification itself only changes tasks.
		OnVerified: nil,
		ReconcileProgress: func() error {
			// The sync engine owns the pass lifecycle and writes the final stats.
			// The verifier only marks task checksums; changing status here can race
			// the engine and leave an IDLE job without a completed-run record.
			return nil
		},
		IsStillVerifying: func(ctx context.Context) (bool, error) {
			var status string
			if err := p.db.QueryRowContext(ctx, `SELECT status FROM sync_jobs WHERE id = $1`, syncJobID).Scan(&status); err != nil {
				return false, err
			}
			return status == "VERIFYING", nil
		},
	}

	p.runVerificationPass(ctx, cfg)
}

func isDirectoryTask(task *db.Task) bool {
	if strings.HasSuffix(task.FilePath, "/") {
		return true
	}
	if len(task.Metadata) > 0 {
		var meta struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(task.Metadata, &meta); err == nil {
			return meta.Action == "mkdir"
		}
	}
	return false
}
