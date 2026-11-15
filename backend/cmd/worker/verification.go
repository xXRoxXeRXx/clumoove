package main

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
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/processor"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

type verificationWork struct {
	key string
	run func(context.Context)
}

// checksumVerifier schedules and executes post-transfer checksum/size
// verification passes for migrations and sync jobs in VERIFYING state.
type checksumVerifier struct {
	db                  *sql.DB
	secretKey           string
	oauthRefresher      *oauth.JobTokenRefresher
	providerSlots       chan struct{}
	verificationWorkers int
	migrationQueue      chan verificationWork
	syncQueue           chan verificationWork
	verifyingEntities   sync.Map
	verificationWG      sync.WaitGroup
}

func verificationWorkerCount(maxThreads int) int {
	if maxThreads < 1 {
		maxThreads = 1
	}
	return min(4, maxThreads)
}

func newChecksumVerifier(database *sql.DB, secretKey string, maxThreads int, providerSlots chan struct{}, refresher *oauth.JobTokenRefresher) *checksumVerifier {
	if maxThreads < 1 {
		maxThreads = 1
	}
	if refresher == nil && database != nil {
		refresher = oauth.NewJobTokenRefresher(database, secretKey, nil)
	}
	return &checksumVerifier{
		db:                  database,
		secretKey:           secretKey,
		oauthRefresher:      refresher,
		providerSlots:       providerSlots,
		verificationWorkers: verificationWorkerCount(maxThreads),
		migrationQueue:      make(chan verificationWork, maxThreads*2),
		syncQueue:           make(chan verificationWork, maxThreads*2),
	}
}

func (v *checksumVerifier) tokenRefresher() *oauth.JobTokenRefresher {
	if v.oauthRefresher != nil {
		return v.oauthRefresher
	}
	return oauth.NewJobTokenRefresher(v.db, v.secretKey, nil)
}

func (v *checksumVerifier) acquireProviderSlot(ctx context.Context) bool {
	if v.providerSlots == nil {
		return true
	}
	select {
	case v.providerSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (v *checksumVerifier) releaseProviderSlot() {
	if v.providerSlots == nil {
		return
	}
	<-v.providerSlots
}

// Run starts the bounded verification dispatcher, polls for VERIFYING
// migrations and sync jobs until ctx is cancelled, and waits for all in-flight
// verification passes to finish before returning.
func (v *checksumVerifier) Run(ctx context.Context) error {
	v.startDispatcher(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			v.verificationWG.Wait()
			return nil
		case <-ticker.C:
			if v.db != nil {
				v.processVerifyingMigrations(ctx)
				v.processVerifyingSyncJobs(ctx)
			}
		}
	}
}

// dequeue alternates preference when both entity queues have work so a long
// migration verification backlog cannot starve waiting sync passes.
func (v *checksumVerifier) dequeue(ctx context.Context, preferSync bool) (verificationWork, bool) {
	preferred, alternate := v.migrationQueue, v.syncQueue
	if preferSync {
		preferred, alternate = alternate, preferred
	}
	select {
	case work := <-preferred:
		return work, true
	default:
	}
	select {
	case work := <-alternate:
		return work, true
	default:
	}
	select {
	case <-ctx.Done():
		return verificationWork{}, false
	case work := <-preferred:
		return work, true
	case work := <-alternate:
		return work, true
	}
}

func (v *checksumVerifier) startDispatcher(ctx context.Context) {
	for i := 0; i < v.verificationWorkers; i++ {
		v.verificationWG.Add(1)
		go func() {
			defer v.verificationWG.Done()
			preferSync := false
			for {
				work, ok := v.dequeue(ctx, preferSync)
				if !ok {
					return
				}
				preferSync = !preferSync
				if !v.acquireProviderSlot(ctx) {
					v.verifyingEntities.Delete(work.key)
					return
				}
				work.run(ctx)
				v.releaseProviderSlot()
				v.verifyingEntities.Delete(work.key)
			}
		}()
	}
}

func verificationGuardKey(entityType, entityID string) string {
	normalized := strings.ToLower(strings.TrimSpace(entityType))
	if normalized == "sync job" {
		normalized = "sync"
	}
	return fmt.Sprintf("%s:%s", normalized, entityID)
}

func (v *checksumVerifier) schedule(ctx context.Context, entityType, entityID string, run func(context.Context)) {
	key := verificationGuardKey(entityType, entityID)
	if _, loaded := v.verifyingEntities.LoadOrStore(key, true); loaded {
		return
	}
	work := verificationWork{key: key, run: run}
	queue := v.migrationQueue
	if entityType == "sync" {
		queue = v.syncQueue
	}
	select {
	case <-ctx.Done():
		v.verifyingEntities.Delete(key)
	case queue <- work:
	default:
		// Keep discovery non-blocking; the next tick retries when capacity frees.
		v.verifyingEntities.Delete(key)
	}
}

func (v *checksumVerifier) processVerifyingMigrations(ctx context.Context) {
	query := `SELECT id FROM migrations WHERE status = 'VERIFYING'`
	rows, err := v.db.QueryContext(ctx, query)
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
		workerLogf("verifier", "[VERIFIER] Listing verifying migrations: %v", err)
		return
	}

	for _, migID := range migIDs {
		v.schedule(ctx, "migration", migID, func(passCtx context.Context) {
			v.verifyMigrationChecksums(passCtx, migID, true)
		})
	}
}

func (v *checksumVerifier) processVerifyingSyncJobs(ctx context.Context) {
	query := `SELECT id FROM sync_jobs WHERE status = 'VERIFYING'`
	rows, err := v.db.QueryContext(ctx, query)
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
		workerLogf("verifier", "[VERIFIER] Listing verifying sync jobs: %v", err)
		return
	}

	for _, syncID := range syncIDs {
		v.schedule(ctx, "sync", syncID, func(passCtx context.Context) {
			v.verifySyncJobChecksums(passCtx, syncID, true)
		})
	}
}

type verificationPassConfig struct {
	GuardAlreadyHeld  bool
	EntityType        string
	EntityID          string
	UserID            string
	SourceProvider    string
	TargetProvider    string
	TargetURL         string
	TargetUsername    string
	TargetPassword    string
	MegaSession       storage.MegaSession
	TargetDir         string
	Threads           int
	TargetClient      verificationTarget
	NewTargetProvider func(ctx context.Context, providerType, urlStr, username, password string) (connectableVerificationTarget, error)
	GetTasks          func(ctx context.Context) ([]*db.Task, error)
	ReconcileProgress func() error
	MarkVerified      func(ctx context.Context, task *db.Task, targetHash string) (bool, error)
	MarkMismatch      func(ctx context.Context, task *db.Task) (bool, error)
	Release           func(ctx context.Context)
	RenewLease        func(ctx context.Context) (bool, error)
	CanWrite          func() bool
}

type verificationTarget interface {
	storage.FileExister
	storage.FileHasher
	VerificationMode() storage.VerificationMode
}

type connectableVerificationTarget interface {
	verificationTarget
	Connect(ctx context.Context) (bool, error)
	Close() error
}

func (v *checksumVerifier) runPass(ctx context.Context, cfg verificationPassConfig) {
	guardKey := verificationGuardKey(cfg.EntityType, cfg.EntityID)
	if !cfg.GuardAlreadyHeld {
		if _, loaded := v.verifyingEntities.LoadOrStore(guardKey, true); loaded {
			workerLogf("verifier", "[VERIFIER] Verification pass already in progress for %s %s, skipping tick.\n", cfg.EntityType, cfg.EntityID)
			return
		}
		defer v.verifyingEntities.Delete(guardKey)
	}
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
			workerLogf("verifier", "[VERIFIER] Cannot confirm verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
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
						workerLogf("verifier", "[VERIFIER] Cannot refresh verification state for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
						continue
					}
					if !stillVerifying {
						workerLogf("verifier", "[VERIFIER] %s %s cancelled because it is no longer VERIFYING.\n", cfg.EntityType, cfg.EntityID)
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
				workerLogf("verifier", "[VERIFIER] Failed to save verification for task %s: %v\n", task.ID, err)
			}
			return ok && err == nil
		}
		return db.MarkTaskChecksumVerified(v.db, passCtx, task.ID, targetHash) == nil
	}
	markMismatch := func(task *db.Task, message, targetHash string) {
		task.Status = "FAILED"
		task.ErrorMessage = sql.NullString{String: message, Valid: true}
		if targetHash != "" {
			task.TargetHash = sql.NullString{String: targetHash, Valid: true}
		}
		if !canWrite() {
			return
		}
		if cfg.MarkMismatch != nil {
			task.NextRetryAt = sql.NullTime{}
			_, _ = cfg.MarkMismatch(passCtx, task)
			return
		}
		task.NextRetryAt = sql.NullTime{Time: time.Now(), Valid: true}
		_ = db.UpdateTaskStatus(v.db, task)
	}

	unverifiedTasks, err := cfg.GetTasks(passCtx)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Error fetching unverified tasks for %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return
	}

	total := len(unverifiedTasks)
	if total == 0 {
		_ = cfg.ReconcileProgress()
		workerLogf("verifier", "[VERIFIER] %s %s verification completed (0 unverified remaining).\n", cfg.EntityType, cfg.EntityID)
		return
	}

	if cfg.TargetProvider == "webdav" {
		workerLogf("verifier", "[VERIFIER] WebDAV target has no checksum API; validating each target size.\n")
	}

	passCtx = storage.WithLocalUserScope(passCtx, cfg.UserID)
	targetClient := cfg.TargetClient
	if targetClient == nil {
		createdTarget, ok := v.connectTarget(passCtx, cfg)
		if !ok {
			return
		}
		defer createdTarget.Close()
		targetClient = createdTarget
	}

	verificationMode := targetClient.VerificationMode()
	numWorkers := 1
	if verificationMode == storage.VerificationCryptographicHash {
		numWorkers = cfg.Threads
		if numWorkers <= 0 {
			numWorkers = 4
		}
		if numWorkers > 8 {
			numWorkers = 8
		}
		if numWorkers > total {
			numWorkers = total
		}
	}

	workerLogf("verifier", "[VERIFIER] Starting checksum verification pass for %d tasks in %s %s (%d workers)\n", total, cfg.EntityType, cfg.EntityID, numWorkers)

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
				if passCtx.Err() != nil {
					return
				}
				if !v.verifyTask(passCtx, cfg, targetClient, verificationMode, task, markVerified, markMismatch) {
					return
				}
				current := processedCount.Add(1)
				if current == 1 || current%50 == 0 || current == int64(total) {
					workerLogf("verifier", "[VERIFIER] %s %s verification progress: %d/%d tasks processed (%.1f%%)\n",
						cfg.EntityType, cfg.EntityID, current, total, float64(current)/float64(total)*100.0)
				}
			}
		}()
	}

	wg.Wait()
	if passCtx.Err() != nil {
		workerLogf("verifier", "[VERIFIER] %s %s verification pass aborted.\n", cfg.EntityType, cfg.EntityID)
		return
	}
	_ = cfg.ReconcileProgress()
	workerLogf("verifier", "[VERIFIER] %s %s checksum verification pass completed.\n", cfg.EntityType, cfg.EntityID)
}

func (v *checksumVerifier) connectTarget(passCtx context.Context, cfg verificationPassConfig) (connectableVerificationTarget, bool) {
	newTargetProvider := cfg.NewTargetProvider
	if newTargetProvider == nil {
		newTargetProvider = func(ctx context.Context, providerType, urlStr, username, password string) (connectableVerificationTarget, error) {
			return storage.NewProvider(ctx, providerType, urlStr, username, password, storage.WithProviderMegaSession(cfg.MegaSession))
		}
	}
	createdTarget, err := newTargetProvider(passCtx, cfg.TargetProvider, cfg.TargetURL, cfg.TargetUsername, cfg.TargetPassword)
	clear(cfg.MegaSession.MasterKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to create target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, err)
		return nil, false
	}
	connected, connectErr := createdTarget.Connect(passCtx)
	if !connected {
		_ = createdTarget.Close()
		if connectErr == nil {
			connectErr = errors.New("provider rejected connection")
		}
		workerLogf("verifier", "[VERIFIER] Failed to connect to target provider for verification on %s %s: %v\n", cfg.EntityType, cfg.EntityID, connectErr)
		return nil, false
	}
	return createdTarget, true
}

func (v *checksumVerifier) verifyTask(
	passCtx context.Context,
	cfg verificationPassConfig,
	targetClient verificationTarget,
	verificationMode storage.VerificationMode,
	task *db.Task,
	markVerified func(*db.Task, string) bool,
	markMismatch func(*db.Task, string, string),
) bool {
	if task.ResourceType != "files" || task.IsDirectory() {
		return markVerified(task, "")
	}

	targetPath := processor.EffectiveTargetPath(task, cfg.TargetDir, cfg.SourceProvider, cfg.TargetProvider)
	targetResourceID := ""
	if cfg.TargetProvider == "immich" {
		var metadata storage.FileMetadata
		if err := json.Unmarshal(task.Metadata, &metadata); err != nil {
			workerLogf("verifier", "[VERIFIER] Immich task %s has invalid metadata; leaving it unverified: %v\n", task.ID, err)
			return true
		}
		assetID := metadata.CustomProps["immich_target_asset_id"]
		if strings.TrimSpace(assetID) == "" {
			workerLogf("verifier", "[VERIFIER] Immich task %s has no persisted target asset ID; leaving it unverified until retransfer.\n", task.ID)
			return true
		}
		targetResourceID = assetID
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
	verifySizeAndMark := func(hash string) bool {
		if isDirErr(errHash) {
			workerLogf("verifier", "[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
			return markVerified(task, hash)
		}
		exists, targetSize, sizeErr := storage.VerifyTargetSizeByResourceID(passCtx, targetClient, task.ResourceType, targetPath, targetResourceID)
		if sizeErr != nil {
			if isDirErr(sizeErr) {
				workerLogf("verifier", "[VERIFIER] [DIRECTORY_VERIFIED] %s — target directory confirmed [PASSED]\n", targetPath)
				return markVerified(task, hash)
			}
			workerLogf("verifier", "[VERIFIER] Cannot recheck target size for %s: %v\n", targetPath, sizeErr)
			return false
		}
		if !exists || (targetSize != task.FileSize && !(task.FileSize == 0 && task.IsDirectory())) {
			workerLogf("verifier", "[VERIFIER] [SIZE_MISMATCH] %s | Got: %d | Expected: %d\n", targetPath, targetSize, task.FileSize)
			markMismatch(task, fmt.Sprintf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize), hash)
			return false
		}
		return markVerified(task, hash)
	}

	if verificationMode == storage.VerificationSizeOnly {
		workerLogf("verifier", "[VERIFIER] [SIZE_ONLY] %s | Target does not expose a comparable cryptographic hash — verifying size (%d bytes)\n", targetPath, task.FileSize)
		return verifySizeAndMark("")
	}
	if verificationMode == storage.VerificationNone {
		workerLogf("verifier", "[VERIFIER] [NO_INDEPENDENT_VERIFICATION] %s | Target does not support verification\n", targetPath)
		return true
	}

	for attempt := 0; attempt < 3; attempt++ {
		hashCtx, taskCancel := context.WithTimeout(passCtx, 15*time.Second)
		targetHash, errHash = storage.GetFileHashByResourceID(hashCtx, targetClient, task.ResourceType, targetPath, targetResourceID)
		taskCancel()

		if (errHash == nil && targetHash != "") || isNonRetryableHashError(errHash) ||
			(cfg.TargetProvider == "immich" && errors.Is(errHash, storage.ErrNotFound)) {
			break
		}
		if attempt < 2 {
			select {
			case <-passCtx.Done():
				return false
			case <-time.After(2 * time.Second):
			}
		}
	}

	if errHash == nil && targetHash != "" {
		cmp := storage.CompareTargetHash(task.SourceHash.String, task.WorkerHash.String, targetHash)
		if cmp.HasSource {
			if cmp.Comparable {
				if cmp.Matched {
					workerLogf("verifier", "[VERIFIER] [MATCH] %s | Algo: %s | Hash: %s\n", targetPath, cmp.TargetAlgo, cmp.TargetValue)
					_ = markVerified(task, targetHash)
				} else {
					workerLogf("verifier", "[VERIFIER] [MISMATCH] %s | Expected (%s): %s | Received (%s): %s — marking FAILED for automatic re-copy\n",
						targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue)
					markMismatch(task, fmt.Sprintf("checksum mismatch: expected (%s) %s, got (%s) %s", cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue), targetHash)
				}
			} else if cmp.SourceAlgo == "ETAG" || cmp.TargetAlgo == "ETAG" {
				workerLogf("verifier", "[VERIFIER] [SIZE_VERIFIED] %s | Source (%s): %s | Target: No cryptographic hash on target (returned ETag: %s) — size (%d bytes) verified [PASSED]\n",
					targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetValue, task.FileSize)
				_ = verifySizeAndMark(targetHash)
			} else {
				workerLogf("verifier", "[VERIFIER] [ALGO_DIFF] %s | Source (%s): %s | Target (%s): %s — size (%d bytes) verified\n",
					targetPath, cmp.SourceAlgo, cmp.SourceValue, cmp.TargetAlgo, cmp.TargetValue, task.FileSize)
				_ = verifySizeAndMark(targetHash)
			}
		} else {
			workerLogf("verifier", "[VERIFIER] [NO_SOURCE_HASH] %s | Target (%s): %s — registered target hash\n", targetPath, cmp.TargetAlgo, cmp.TargetValue)
			_ = verifySizeAndMark(targetHash)
		}
		return true
	}

	reason := "checksum not available"
	if errHash != nil {
		reason = sanitize.SanitizeError(errHash.Error())
	}
	if srcHash := storage.BestSourceHash(task.SourceHash.String, task.WorkerHash.String); srcHash != "" {
		workerLogf("verifier", "[VERIFIER] [NO_TARGET_HASH] %s | Expected: %s | Reason: %s — falling back to size verification (%d bytes)\n",
			targetPath, srcHash, reason, task.FileSize)
	} else {
		workerLogf("verifier", "[VERIFIER] [NO_TARGET_HASH] %s | Reason: %s — falling back to size verification (%d bytes)\n",
			targetPath, reason, task.FileSize)
	}
	return verifySizeAndMark("")
}

func (v *checksumVerifier) verifyMigrationChecksums(ctx context.Context, migrationID string, guardAlreadyHeld bool) {
	generation, claimed, err := db.ClaimMigrationVerification(v.db, ctx, migrationID)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Cannot claim verification for migration %s: %v\n", migrationID, err)
		return
	}
	if !claimed {
		return
	}
	mig, err := db.GetMigration(v.db, migrationID)
	if err != nil {
		_ = db.ReleaseMigrationVerificationLease(v.db, context.Background(), migrationID, generation)
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if mig.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(mig.TargetPasswordEncrypted, v.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = v.tokenRefresher().EnsureFreshMigrationToken(ctx, mig, "target", targetPass)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to refresh target OAuth token for migration %s: %v\n", migrationID, err)
		_ = db.ReleaseMigrationVerificationLease(v.db, context.Background(), migrationID, generation)
		return
	}
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(mig.TargetProvider, mig.TargetMegaSessionIDEncrypted, mig.TargetMegaMasterKeyEncrypted, v.secretKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to decode target MEGA session for migration %s: %v\n", migrationID, err)
		_ = db.ReleaseMigrationVerificationLease(v.db, context.Background(), migrationID, generation)
		return
	}
	defer clear(targetMegaSession.MasterKey)

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
		MegaSession:      targetMegaSession,
		TargetDir:        mig.TargetDir,
		Threads:          mig.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedTasks(v.db, ctx, migrationID)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkMigrationTaskChecksumVerifiedWhileVerifying(v.db, ctx, task.ID, targetHash, generation)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkMigrationTaskChecksumMismatchWhileVerifying(v.db, ctx, task, generation)
		},
		ReconcileProgress: func() error {
			_, err := db.ReconcileMigrationProgressWhileVerifying(v.db, migrationID, generation)
			return err
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := db.RenewMigrationVerificationLease(v.db, ctx, migrationID, generation)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := db.ReleaseMigrationVerificationLease(v.db, ctx, migrationID, generation); err != nil {
				workerLogf("verifier", "[VERIFIER] Cannot release verification lease for migration %s: %v\n", migrationID, err)
			}
		},
	}

	v.runPass(ctx, cfg)
}

func (v *checksumVerifier) verifySyncJobChecksums(ctx context.Context, syncJobID string, guardAlreadyHeld bool) {
	verificationGeneration, claimed, err := db.ClaimSyncJobVerification(v.db, ctx, syncJobID)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Cannot claim verification for sync job %s: %v", syncJobID, err)
		return
	}
	if !claimed {
		return
	}
	job, err := db.GetSyncJob(v.db, syncJobID)
	if err != nil || job.Status != "VERIFYING" {
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if job.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, v.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = v.tokenRefresher().EnsureFreshSyncToken(ctx, job, "target", targetPass)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to refresh target OAuth token for sync job %s: %v\n", syncJobID, err)
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(job.TargetProvider, job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, v.secretKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to decode target MEGA session for sync job %s: %v\n", syncJobID, err)
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	defer clear(targetMegaSession.MasterKey)

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
		MegaSession:      targetMegaSession,
		TargetDir:        job.TargetDir,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedSyncTasks(v.db, ctx, syncJobID, job.RunGeneration)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkSyncTaskChecksumVerifiedWhileVerifying(v.db, ctx, task.ID, targetHash, job.RunGeneration, verificationGeneration)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkSyncTaskChecksumMismatchWhileVerifying(v.db, ctx, task, job.RunGeneration, verificationGeneration)
		},
		ReconcileProgress: func() error {
			return nil
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := db.RenewSyncJobVerificationLease(v.db, ctx, syncJobID, job.RunGeneration, verificationGeneration)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := db.ReleaseSyncJobVerificationLease(v.db, ctx, syncJobID, verificationGeneration); err != nil {
				workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, err)
			}
		},
	}

	v.runPass(ctx, cfg)
}

func isNonRetryableHashError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, storage.ErrHashNotSupported) || errors.Is(err, storage.ErrChecksumNotAvailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checksum not available") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "not implemented") ||
		strings.Contains(msg, "is a directory") ||
		strings.Contains(msg, "is a folder")
}
