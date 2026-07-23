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
// It prefers a cryptographic WorkerHash (computed on the fly during streaming download)
// over SourceHash (from provider metadata). Non-cryptographic ETags are used as fallback.
func bestSourceHash(task *db.Task) string {
	workerHash := ""
	if task.WorkerHash.Valid {
		workerHash = task.WorkerHash.String
	}
	sourceHash := ""
	if task.SourceHash.Valid {
		sourceHash = task.SourceHash.String
	}

	// 1. Prefer cryptographic WorkerHash
	if workerHash != "" && !strings.HasPrefix(strings.ToUpper(workerHash), "ETAG:") {
		return workerHash
	}
	// 2. Fall back to cryptographic SourceHash
	if sourceHash != "" && !strings.HasPrefix(strings.ToUpper(sourceHash), "ETAG:") {
		return sourceHash
	}
	// 3. Fall back to ETag (preferring WorkerHash ETag)
	if workerHash != "" {
		return workerHash
	}
	return sourceHash
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
	EntityType        string // "Migration" or "Sync job"
	EntityID          string
	TargetProvider    string
	TargetURL         string
	TargetUsername    string
	TargetPassword    string
	TargetDir         string
	Threads           int
	GetTasks          func(ctx context.Context) ([]*db.Task, error)
	MarkAllVerified   func(ctx context.Context) error
	OnVerified        func(task *db.Task, targetPath string, targetHash string)
	ReconcileProgress func() error
}

func (p *Processor) runVerificationPass(ctx context.Context, cfg verificationPassConfig) {
	guardKey := fmt.Sprintf("%s:%s", strings.ToLower(cfg.EntityType), cfg.EntityID)
	if _, loaded := p.verifyingEntities.LoadOrStore(guardKey, true); loaded {
		log.Printf("[VERIFIER] Verification pass already in progress for %s %s, skipping tick.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	defer p.verifyingEntities.Delete(guardKey)

	unverifiedTasks, err := cfg.GetTasks(ctx)
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
		log.Printf("[VERIFIER] WebDAV target does not support checksums — accepting size verification for %d tasks in %s %s\n", total, cfg.EntityType, cfg.EntityID)
		_ = cfg.MarkAllVerified(ctx)
		_ = cfg.ReconcileProgress()
		return
	}

	targetKey := fmt.Sprintf("%s:target", cfg.EntityID)
	targetClient, cleanup, err := p.getOrCreateProvider(ctx, targetKey, cfg.TargetProvider, cfg.TargetURL, cfg.TargetUsername, cfg.TargetPassword)
	if err != nil {
		log.Printf("[VERIFIER] Failed to connect to target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return
	}
	defer cleanup()

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
				if cfg.TargetDir != "" && cfg.TargetDir != "/" {
					if !strings.HasPrefix(task.FilePath, cfg.TargetDir+"/") && task.FilePath != cfg.TargetDir {
						targetPath = path.Clean(path.Join(cfg.TargetDir, task.FilePath))
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
					srcHash := bestSourceHash(task)

					if srcHash != "" {
						sourceAlgo, cleanSource := storage.ParseHashString(srcHash)

						if isCryptographicHash(sourceAlgo) && isCryptographicHash(targetAlgo) && sourceAlgo == targetAlgo {
							if cleanSource == cleanTarget {
								log.Printf("[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, targetAlgo, cleanTarget)
								_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
								if cfg.OnVerified != nil {
									cfg.OnVerified(task, targetPath, targetHash)
								}
							} else {
								log.Printf("[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s\n",
									targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget)
								task.Status = "FAILED"
								task.ErrorMessage = sql.NullString{
									String: fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", sourceAlgo, cleanSource, targetAlgo, cleanTarget),
									Valid:  true,
								}
								_ = db.UpdateTaskStatus(p.db, task)
								_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							}
						} else if sourceAlgo == "ETAG" || targetAlgo == "ETAG" {
							log.Printf("[VERIFIER] [SIZE_VERIFIED] %s | Source (%s): %s | Target: No cryptographic hash on target (returned ETag: %s) — size (%d bytes) verified [PASSED]\n",
								targetPath, sourceAlgo, cleanSource, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							if cfg.OnVerified != nil {
								cfg.OnVerified(task, targetPath, targetHash)
							}
						} else {
							log.Printf("[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
							if cfg.OnVerified != nil {
								cfg.OnVerified(task, targetPath, targetHash)
							}
						}
					} else {
						log.Printf("[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, targetAlgo, cleanTarget)
						_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, targetHash)
						if cfg.OnVerified != nil {
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
					_ = db.MarkTaskChecksumVerified(p.db, ctx, task.ID, "")
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
		TargetProvider: mig.TargetProvider,
		TargetURL:      mig.TargetURL,
		TargetUsername: mig.TargetUsername,
		TargetPassword: targetPass,
		TargetDir:      mig.TargetDir,
		Threads:        mig.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedTasks(p.db, ctx, migrationID)
		},
		MarkAllVerified: func(ctx context.Context) error {
			return db.MarkAllMigrationTasksVerified(p.db, ctx, migrationID)
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

	cfg := verificationPassConfig{
		EntityType:     "Sync job",
		EntityID:       syncJobID,
		TargetProvider: job.TargetProvider,
		TargetURL:      job.TargetURL,
		TargetUsername: job.TargetUsername,
		TargetPassword: targetPass,
		TargetDir:      job.TargetDir,
		Threads:        job.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedSyncTasks(p.db, ctx, syncJobID)
		},
		MarkAllVerified: func(ctx context.Context) error {
			return db.MarkAllSyncTasksVerified(p.db, ctx, syncJobID)
		},
		OnVerified: func(task *db.Task, targetPath string, targetHash string) {
			_ = db.UpdateSyncStateTargetHash(p.db, ctx, syncJobID, targetPath, targetHash)
		},
		ReconcileProgress: func() error {
			return db.UpdateSyncJobStatus(p.db, syncJobID, "IDLE", nil)
		},
	}

	p.runVerificationPass(ctx, cfg)
}
