package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

// isComparableHash returns whether we can calculate and compare the algorithm
// during streaming. QuickXor is intentionally included although it is not
// cryptographic: it is OneDrive's provider-specific integrity signal.
func isComparableHash(algo string) bool {
	switch strings.ToUpper(algo) {
	case "SHA1", "SHA256", "MD5", "SHA512", "DROPBOX", "HIDRIVE", "QUICKXOR":
		return true
	default:
		return false
	}
}

// bestSourceHash selects the best available source hash for checksum verification.
// Provider metadata describes the indexed source object. WorkerHash describes
// bytes observed during transfer and can use the target's native algorithm, so
// the verifier promotes it only when that algorithm matches the target hash.
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

func (p *Processor) scheduleVerification(ctx context.Context, entityType, entityID string, run func(context.Context)) {
	key := fmt.Sprintf("%s:%s", entityType, entityID)
	if _, loaded := p.verifyingEntities.LoadOrStore(key, true); loaded {
		return
	}
	work := verificationWork{key: key, run: run}
	queue := p.migrationVerificationQueue
	if entityType == "sync" {
		queue = p.syncVerificationQueue
	}
	select {
	case <-ctx.Done():
		p.verifyingEntities.Delete(key)
	case queue <- work:
	default:
		// Keep discovery non-blocking. The next tick retries a pass when the
		// bounded dispatcher has capacity again.
		p.verifyingEntities.Delete(key)
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
	if err := rows.Err(); err != nil {
		processorLogf("[VERIFIER] Listing verifying migrations: %v", err)
		return
	}

	for _, migID := range migIDs {
		p.scheduleVerification(ctx, "migration", migID, func(passCtx context.Context) {
			p.verifyMigrationChecksums(passCtx, migID, true)
		})
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
	if err := rows.Err(); err != nil {
		processorLogf("[VERIFIER] Listing verifying sync jobs: %v", err)
		return
	}

	for _, syncID := range syncIDs {
		p.scheduleVerification(ctx, "sync", syncID, func(passCtx context.Context) {
			p.verifySyncJobChecksums(passCtx, syncID, true)
		})
	}
}

type verificationPassConfig struct {
	GuardAlreadyHeld bool
	EntityType       string // "Migration" or "Sync job"
	EntityID         string
	UserID           string
	SourceProvider   string
	TargetProvider   string
	TargetURL        string
	TargetUsername   string
	TargetPassword   string
	TargetDir        string
	// Threads is retained for test fixture compatibility. Verification is
	// intentionally serial because one target client is shared for the pass and
	// providers do not promise concurrent safety.
	Threads int
	// TargetClient is test-only injection for a connected target. Production
	// callers leave it nil and construct a scoped provider below.
	TargetClient storage.StorageProvider
	// NewTargetProvider is test-only injection for constructing a provider that
	// still needs Connect before verification. Production callers use newProvider.
	NewTargetProvider func(ctx context.Context, providerType, urlStr, username, password string) (storage.StorageProvider, error)
	GetTasks          func(ctx context.Context) ([]*db.Task, error)
	ReconcileProgress func() error
	MarkVerified      func(ctx context.Context, task *db.Task, targetHash string) (bool, error)
	MarkMismatch      func(ctx context.Context, task *db.Task) (bool, error)
	Release           func(ctx context.Context)
	// RenewLease is the side-effecting heartbeat for a migration verification
	// claim. CanWrite reads its result without issuing a DB query.
	RenewLease func(ctx context.Context) (bool, error)
	CanWrite   func() bool
}

func (p *Processor) runVerificationPass(ctx context.Context, cfg verificationPassConfig) {
	guardKey := fmt.Sprintf("%s:%s", strings.ToLower(cfg.EntityType), cfg.EntityID)
	if !cfg.GuardAlreadyHeld {
		if _, loaded := p.verifyingEntities.LoadOrStore(guardKey, true); loaded {
			processorLogf("[VERIFIER] Verification pass already in progress for %s %s, skipping tick.\n", cfg.EntityType, cfg.EntityID)
			return
		}
	}
	defer p.verifyingEntities.Delete(guardKey)
	if cfg.Release != nil {
		defer func() {
			releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelRelease()
			cfg.Release(releaseCtx)
		}()
	}

	passCtx, cancelPass := context.WithCancel(ctx)
	defer cancelPass()
	if cfg.RenewLease != nil {
		stillVerifying, err := cfg.RenewLease(passCtx)
		if err != nil {
			processorLogf("[VERIFIER] Cannot confirm verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return
		}
		if !stillVerifying {
			return
		}
		stopWatch := make(chan struct{})
		defer close(stopWatch)
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopWatch:
					return
				case <-passCtx.Done():
					return
				case <-ticker.C:
					stillVerifying, err := cfg.RenewLease(passCtx)
					if err != nil {
						processorLogf("[VERIFIER] Cannot refresh verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
						continue
					}
					if !stillVerifying {
						processorLogf("[VERIFIER] %s %s cancelled because it is no longer VERIFYING.\n", cfg.EntityType, cfg.EntityID)
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
		if cfg.CanWrite != nil && !cfg.CanWrite() {
			return false
		}
		return true
	}
	markVerified := func(task *db.Task, targetHash string) bool {
		if !canWrite() {
			return false
		}
		if cfg.MarkVerified != nil {
			ok, err := cfg.MarkVerified(passCtx, task, targetHash)
			if err != nil {
				processorLogf("[VERIFIER] Failed to save verification for task %s: %v\n", task.ID, err)
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
		processorLogf("[VERIFIER] Error fetching unverified tasks for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return
	}

	total := len(unverifiedTasks)
	if total == 0 {
		_ = cfg.ReconcileProgress()
		processorLogf("[VERIFIER] %s %s verification completed (0 unverified remaining).\n", cfg.EntityType, cfg.EntityID)
		return
	}

	if cfg.TargetProvider == "webdav" {
		processorLogf("[VERIFIER] WebDAV target has no checksum API; validating each target size.\n")
	}

	passCtx = storage.WithLocalUserScope(passCtx, cfg.UserID)
	targetClient := cfg.TargetClient
	if targetClient == nil {
		newTargetProvider := cfg.NewTargetProvider
		if newTargetProvider == nil {
			newTargetProvider = newProvider
		}
		targetClient, err = newTargetProvider(passCtx, cfg.TargetProvider, cfg.TargetURL, cfg.TargetUsername, cfg.TargetPassword)
		if err != nil {
			processorLogf("[VERIFIER] Failed to create target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
			return
		}
		defer targetClient.Close()
		if connected, connectErr := targetClient.Connect(passCtx); !connected {
			if connectErr == nil {
				connectErr = errors.New("provider rejected connection")
			}
			processorLogf("[VERIFIER] Failed to connect to target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, connectErr)
			return
		}
	}
	// This is a provider capability, not a per-file property. Resolve it once
	// for the entire pass so size-only targets never enter the hash-query path.
	verificationMode := targetClient.VerificationMode()

	// The dispatcher reserves exactly one process-wide verification slot for a
	// pass. Keep task verification serial to honour that global budget and avoid
	// assuming provider clients are safe for concurrent use.
	numWorkers := 1

	processorLogf("[VERIFIER] Starting checksum verification pass for %d tasks in %s %s (%d workers)\n", total, cfg.EntityType, cfg.EntityID, numWorkers)

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

				targetPath := ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, cfg.TargetDir, cfg.SourceProvider, cfg.TargetProvider)
				taskCtx := passCtx
				if cfg.TargetProvider == "immich" {
					var metadata storage.FileMetadata
					if err := json.Unmarshal(task.Metadata, &metadata); err != nil {
						processorLogf("[VERIFIER] Immich task %s has invalid metadata; leaving it unverified: %v\n", task.ID, err)
						processedCount.Add(1)
						continue
					}
					assetID := metadata.CustomProps["immich_target_asset_id"]
					if strings.TrimSpace(assetID) == "" {
						processorLogf("[VERIFIER] Immich task %s has no persisted target asset ID; leaving it unverified until retransfer.\n", task.ID)
						processedCount.Add(1)
						continue
					}
					taskCtx = storage.WithTargetResourceID(passCtx, assetID)
				}

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
						processorLogf("[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
						return markVerified(task, targetHash)
					}
					exists, targetSize, sizeErr := verifyTargetSize(taskCtx, targetClient, task.ResourceType, targetPath)
					if sizeErr != nil {
						if isDirErr(sizeErr) {
							processorLogf("[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
							return markVerified(task, targetHash)
						}
						processorLogf("[VERIFIER] Cannot recheck target size for %s: %v\n", targetPath, sizeErr)
						return false
					}
					if !exists || (targetSize != task.FileSize && !(task.FileSize == 0 && isDirectoryTask(task))) {
						processorLogf("[VERIFIER] [SIZE_MISMATCH] %s | Got: %d | Expected: %d\n", targetPath, targetSize, task.FileSize)
						markMismatch(task, fmt.Sprintf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize), targetHash)
						return false
					}
					return markVerified(task, targetHash)
				}
				if verificationMode == storage.VerificationSizeOnly {
					processorLogf("[VERIFIER] [SIZE_ONLY] %s | Target does not expose a comparable cryptographic hash — verifying size (%d bytes)\n", targetPath, task.FileSize)
					if !markVerifiedForFile("") {
						return
					}
					current := processedCount.Add(1)
					if current == 1 || current%50 == 0 || current == int64(total) {
						processorLogf("[VERIFIER] %s %s verification progress: %d/%d tasks processed (%.1f%%)\n",
							cfg.EntityType, cfg.EntityID, current, total, float64(current)/float64(total)*100.0)
					}
					continue
				}
				if verificationMode == storage.VerificationNone {
					processorLogf("[VERIFIER] [NO_INDEPENDENT_VERIFICATION] %s | Target does not support verification\n", targetPath)
					continue
				}
				for attempt := 0; attempt < 3; attempt++ {
					hashCtx, taskCancel := context.WithTimeout(taskCtx, 15*time.Second)
					targetHash, errHash = targetClient.GetFileHash(hashCtx, task.ResourceType, targetPath)
					taskCancel()

					if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) ||
						(cfg.TargetProvider == "immich" && errors.Is(errHash, storage.ErrNotFound)) {
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
					// Prefer a streaming hash calculated in the target's native
					// algorithm over a differently-algorithmed source provider hash.
					if task.WorkerHash.Valid {
						workerAlgo, _ := storage.ParseHashString(task.WorkerHash.String)
						sourceAlgo, _ := storage.ParseHashString(srcHash)
						if workerAlgo == targetAlgo && sourceAlgo != targetAlgo {
							srcHash = task.WorkerHash.String
						}
					}

					if srcHash != "" {
						sourceAlgo, cleanSource := storage.ParseHashString(srcHash)

						if isComparableHash(sourceAlgo) && isComparableHash(targetAlgo) && sourceAlgo == targetAlgo {
							if cleanSource == cleanTarget {
								processorLogf("[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, targetAlgo, cleanTarget)
								if cfg.TargetProvider == "immich" {
									// A matching checksum from GET /assets/{id} already
									// confirms the specific target asset exists.
									_ = markVerified(task, targetHash)
								} else {
									_ = markVerifiedForFile(targetHash)
								}
							} else {
								processorLogf("[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s — marking FAILED for automatic re-copy\n",
									targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget)
								markMismatch(task, fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", sourceAlgo, cleanSource, targetAlgo, cleanTarget), targetHash)
							}
						} else if sourceAlgo == "ETAG" || targetAlgo == "ETAG" {
							processorLogf("[VERIFIER] [SIZE_VERIFIED] %s | Source (%s): %s | Target: No cryptographic hash on target (returned ETag: %s) — size (%d bytes) verified [PASSED]\n",
								targetPath, sourceAlgo, cleanSource, cleanTarget, task.FileSize)
							_ = markVerifiedForFile(targetHash)
						} else {
							processorLogf("[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
								targetPath, sourceAlgo, cleanSource, targetAlgo, cleanTarget, task.FileSize)
							_ = markVerifiedForFile(targetHash)
						}
					} else {
						processorLogf("[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, targetAlgo, cleanTarget)
						_ = markVerifiedForFile(targetHash)
					}
				} else {
					reason := "checksum not available"
					if errHash != nil {
						reason = sanitize.SanitizeError(errHash.Error())
					}
					srcHash := bestSourceHash(task)
					if srcHash != "" {
						processorLogf("[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, srcHash, reason, task.FileSize)
					} else {
						processorLogf("[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
							targetPath, reason, task.FileSize)
					}
					if !markVerifiedForFile("") {
						return
					}
				}

				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					processorLogf("[VERIFIER] %s %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						cfg.EntityType, cfg.EntityID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	if passCtx.Err() != nil {
		processorLogf("[VERIFIER] %s %s verification pass aborted.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	_ = cfg.ReconcileProgress()
	processorLogf("[VERIFIER] %s %s checksum verification pass completed.\n", cfg.EntityType, cfg.EntityID)
}

func (p *Processor) verifyMigrationChecksums(ctx context.Context, migrationID string, guardAlreadyHeld bool) {
	generation, claimed, err := db.ClaimMigrationVerification(p.db, ctx, migrationID)
	if err != nil {
		processorLogf("[VERIFIER] Cannot claim verification for migration %s: %v\n", migrationID, err)
		return
	}
	if !claimed {
		return
	}
	mig, err := db.GetMigration(p.db, migrationID)
	if err != nil {
		_ = db.ReleaseMigrationVerificationLease(p.db, context.Background(), migrationID, generation)
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if mig.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(mig.TargetPasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		processorLogf("[VERIFIER] Failed to refresh target OAuth token for migration %s: %v\n", migrationID, err)
		return
	}

	cfg := verificationPassConfig{
		GuardAlreadyHeld: guardAlreadyHeld,
		EntityType:       "Migration",
		EntityID:         migrationID,
		UserID:           mig.UserID.String,
		SourceProvider:   mig.SourceProvider,
		TargetProvider:   mig.TargetProvider,
		TargetURL:        mig.TargetURL,
		TargetUsername:   mig.TargetUsername,
		TargetPassword:   targetPass,
		TargetDir:        mig.TargetDir,
		Threads:          mig.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedTasks(p.db, ctx, migrationID)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkMigrationTaskChecksumVerifiedWhileVerifying(p.db, ctx, task.ID, targetHash, generation)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkMigrationTaskChecksumMismatchWhileVerifying(p.db, ctx, task, generation)
		},
		ReconcileProgress: func() error {
			_, err := db.ReconcileMigrationProgressWhileVerifying(p.db, migrationID, generation)
			return err
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := db.RenewMigrationVerificationLease(p.db, ctx, migrationID, generation)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := db.ReleaseMigrationVerificationLease(p.db, ctx, migrationID, generation); err != nil {
				processorLogf("[VERIFIER] Cannot release verification lease for migration %s: %v\n", migrationID, err)
			}
		},
	}

	p.runVerificationPass(ctx, cfg)
}

func (p *Processor) verifySyncJobChecksums(ctx context.Context, syncJobID string, guardAlreadyHeld bool) {
	verificationGeneration, claimed, err := db.ClaimSyncJobVerification(p.db, ctx, syncJobID)
	if err != nil {
		processorLogf("[VERIFIER] Cannot claim verification for sync job %s: %v", syncJobID, err)
		return
	}
	if !claimed {
		return
	}
	job, err := db.GetSyncJob(p.db, syncJobID)
	if err != nil || job.Status != "VERIFYING" {
		if releaseErr := db.ReleaseSyncJobVerificationLease(p.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			processorLogf("[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if job.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = p.ensureFreshSyncOAuthToken(ctx, job, "target", targetPass)
	if err != nil {
		processorLogf("[VERIFIER] Failed to refresh target OAuth token for sync job %s: %v\n", syncJobID, err)
		if releaseErr := db.ReleaseSyncJobVerificationLease(p.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			processorLogf("[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}

	cfg := verificationPassConfig{
		GuardAlreadyHeld: guardAlreadyHeld,
		EntityType:       "Sync job",
		EntityID:         syncJobID,
		UserID:           job.UserID,
		SourceProvider:   job.SourceProvider,
		TargetProvider:   job.TargetProvider,
		TargetURL:        job.TargetURL,
		TargetUsername:   job.TargetUsername,
		TargetPassword:   targetPass,
		TargetDir:        job.TargetDir,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedSyncTasks(p.db, ctx, syncJobID, job.RunGeneration)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkSyncTaskChecksumVerifiedWhileVerifying(p.db, ctx, task.ID, targetHash, job.RunGeneration, verificationGeneration)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkSyncTaskChecksumMismatchWhileVerifying(p.db, ctx, task, job.RunGeneration, verificationGeneration)
		},
		// The engine applies all durable sync-state changes after it owns the
		// successful finalization, so verification itself only changes tasks.
		ReconcileProgress: func() error {
			// The sync engine owns the pass lifecycle and writes the final stats.
			// The verifier only marks task checksums; changing status here can race
			// the engine and leave an IDLE job without a completed-run record.
			return nil
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := db.RenewSyncJobVerificationLease(p.db, ctx, syncJobID, job.RunGeneration, verificationGeneration)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := db.ReleaseSyncJobVerificationLease(p.db, ctx, syncJobID, verificationGeneration); err != nil {
				processorLogf("[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, err)
			}
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
