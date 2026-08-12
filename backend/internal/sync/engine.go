package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

type Engine struct {
	db            *sql.DB
	queue         *queue.Queue
	encryptionKey string

	// cancelMu guards the activePassCancels map which tracks in-progress
	// sync-pass goroutines. Entries are added just before the goroutine body
	// runs and removed when it returns, allowing CancelPass to interrupt them.
	cancelMu          sync.Mutex
	activePassCancels map[string]context.CancelFunc
}

func NewEngine(database *sql.DB, q *queue.Queue, encryptionKey string) *Engine {
	return &Engine{
		db:                database,
		queue:             q,
		encryptionKey:     encryptionKey,
		activePassCancels: make(map[string]context.CancelFunc),
	}
}

// CancelPass cancels any in-progress sync pass for the given sync job.
// It is a no-op if no pass is running for the job.
func (e *Engine) CancelPass(syncJobID string) {
	e.cancelMu.Lock()
	cancel, ok := e.activePassCancels[syncJobID]
	e.cancelMu.Unlock()
	if ok {
		cancel()
	}
}

// SubscribeToCancelEvents stops locally owned sync-pass coordinators when a
// pause or deletion was requested through another API/worker process.
func (e *Engine) SubscribeToCancelEvents(ctx context.Context) {
	e.queue.SubscribeToSyncCancelEvents(ctx, e.CancelPass)
}

type fileState struct {
	Path         string
	Size         int64
	LastModified time.Time
	Hash         string
	ETag         string
}

// StartSyncPass claims a manually runnable sync job and, if successful, starts
// exactly one asynchronous pass. The boolean is false when another pass owns
// the job or its status is not runnable.
func (e *Engine) StartSyncPass(serverCtx context.Context, syncJobID string) (bool, error) {
	generation, claimed, err := db.ClaimSyncJobPass(e.db, syncJobID)
	if err != nil || !claimed {
		return claimed, err
	}
	go e.runSyncPass(serverCtx, syncJobID, generation)
	return true, nil
}

// WaitForPassDrain serializes a manual resume with a cancelled predecessor on
// every API instance. A PostgreSQL advisory lock is used rather than a local
// mutex because the predecessor may be running in another process.
func (e *Engine) WaitForPassDrain(ctx context.Context, syncJobID string) error {
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('sync-pass:' || $1))`, syncJobID); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext('sync-pass:' || $1))`, syncJobID)
	return nil
}

func (e *Engine) lockPass(ctx context.Context, syncJobID string) (func(), error) {
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('sync-pass:' || $1))`, syncJobID); err != nil {
		conn.Close()
		return nil, err
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext('sync-pass:' || $1))`, syncJobID)
		_ = conn.Close()
	}, nil
}

// runSyncPass performs a previously claimed sync pass: scans, computes delta,
// enqueues tasks, waits, and updates state.
func (e *Engine) runSyncPass(serverCtx context.Context, syncJobID string, generation int) {
	slog.Info("sync pass started", "sync_job_id", syncJobID, "generation", generation)

	ctx, cancel := context.WithCancel(serverCtx)
	defer cancel()

	// Hold the lock through coordinator completion. A resume waits here until a
	// cancelled predecessor and every worker it owns have acknowledged drain.
	unlockPass, err := e.lockPass(ctx, syncJobID)
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("failed to lock sync pass", "sync_job_id", syncJobID, "error", err)
			if released, releaseErr := db.ReleaseUnstartedSyncPass(e.db, syncJobID, generation); releaseErr != nil {
				slog.Error("failed to release unstarted sync pass", "sync_job_id", syncJobID, "error", releaseErr)
			} else if !released {
				slog.Warn("sync pass changed state before lock failure recovery", "sync_job_id", syncJobID)
			}
		}
		return
	}
	defer unlockPass()

	// Register only after acquiring the cross-instance pass lock. This avoids a
	// successor overwriting the predecessor's cancel entry while it is draining.
	e.cancelMu.Lock()
	e.activePassCancels[syncJobID] = cancel
	e.cancelMu.Unlock()
	defer func() {
		e.cancelMu.Lock()
		delete(e.activePassCancels, syncJobID)
		e.cancelMu.Unlock()
	}()

	// Bound the indexing/listing/delta phase so a stalled provider call cannot
	// wedge the job in INDEXING forever (which the scheduler's overlap
	// protection would then skip indefinitely). Transfers run on the worker;
	// once the pass transitions to RUNNING the original ctx (without this
	// timeout) takes over, so legitimately long syncs are not cut short.
	indexCtx, indexCancel := context.WithTimeout(ctx, 30*time.Minute)
	defer indexCancel()

	// Refuse to run if the claim was superseded (for example by deletion or an
	// administrative status transition) before this goroutine was scheduled.
	// Only the engine's Start* methods can reach this private method.
	job, err := db.GetSyncJob(e.db, syncJobID)
	if err != nil {
		slog.Error("failed to load claimed sync job", "sync_job_id", syncJobID, "error", err)
		return
	}
	indexCtx = storage.WithLocalUserScope(indexCtx, job.UserID)
	if job.Status != "INDEXING" || job.RunGeneration != generation {
		slog.Warn("refusing stale sync pass", "sync_job_id", syncJobID, "generation", generation, "status", job.Status, "run_generation", job.RunGeneration)
		return
	}

	// A predecessor can outlive its cancelled coordinator while it is blocked in
	// provider I/O. Drain before decrypting credentials or rotating OAuth tokens:
	// a long drain must neither retain plaintext secrets nor rotate credentials
	// for a pass that cannot proceed. It must also precede both provider scans so
	// the predecessor cannot change the target after this pass observes it.
	if err := e.drainRemainingTasks(indexCtx, job.ID); err != nil {
		if indexCtx.Err() != nil {
			// A cancelled coordinator must preserve a concurrent pause or deletion.
			if errors.Is(indexCtx.Err(), context.DeadlineExceeded) {
				e.failSync(syncJobID, generation, fmt.Sprintf("Indexing phase timed out while draining previous tasks: %v", err))
			} else {
				slog.Warn("sync pass drain interrupted", "sync_job_id", syncJobID, "error", err)
			}
			return
		}
		// The database could not establish that no prior worker owns a task.
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to determine whether previous tasks drained: %v", err))
		return
	}

	// 1. Fetch the claimed job configuration.

	// 3. Decrypt credentials
	sourcePass, err := crypto.DecryptWithDomain(job.SourcePasswordEncrypted, e.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.SourceProvider)))
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}

	targetPass, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, e.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to decrypt target password: %v", err))
		return
	}

	// Refresh OAuth tokens if necessary
	if job.SourceRefreshTokenEncrypted.Valid && job.SourceRefreshTokenEncrypted.String != "" {
		sourcePass, err = e.ensureFreshToken(indexCtx, syncJobID, job, "source", sourcePass)
		if err != nil {
			e.failSync(syncJobID, generation, fmt.Sprintf("Failed to refresh source OAuth token: %v", err))
			return
		}
	}
	if job.TargetRefreshTokenEncrypted.Valid && job.TargetRefreshTokenEncrypted.String != "" {
		targetPass, err = e.ensureFreshToken(indexCtx, syncJobID, job, "target", targetPass)
		if err != nil {
			e.failSync(syncJobID, generation, fmt.Sprintf("Failed to refresh target OAuth token: %v", err))
			return
		}
	}

	// 4. Create storage provider clients
	sourceCtx, err := megasecret.WithMegaSession(indexCtx, job.SourceProvider, job.SourceMegaSessionIDEncrypted, job.SourceMegaMasterKeyEncrypted, e.encryptionKey)
	if err != nil {
		e.failSync(syncJobID, generation, "Failed to decrypt source connection session.")
		return
	}
	targetCtx, err := megasecret.WithMegaSession(indexCtx, job.TargetProvider, job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, e.encryptionKey)
	if err != nil {
		e.failSync(syncJobID, generation, "Failed to decrypt target connection session.")
		return
	}
	sourceClient, err := storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to connect to source: %v", err))
		return
	}
	defer sourceClient.Close()

	targetClient, err := storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to connect to target: %v", err))
		return
	}
	defer targetClient.Close()

	// Each provider gets its own connection budget; a slow source must not
	// consume the target's deadline.
	if err := func() error {
		sourceConnCtx, sourceConnCancel := context.WithTimeout(indexCtx, 15*time.Second)
		defer sourceConnCancel()
		ok, err := sourceClient.Connect(sourceConnCtx)
		if !ok {
			if err == nil {
				return errors.New("source provider rejected connection")
			}
			return err
		}
		return nil
	}(); err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Source connection failed: %v", err))
		return
	}
	if err := func() error {
		targetConnCtx, targetConnCancel := context.WithTimeout(indexCtx, 15*time.Second)
		defer targetConnCancel()
		ok, err := targetClient.Connect(targetConnCtx)
		if !ok {
			if err == nil {
				return errors.New("target provider rejected connection")
			}
			return err
		}
		return nil
	}(); err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Target connection failed: %v", err))
		return
	}

	// 5. Load previous state from DB to enable ETag folder skipping and fast delta checks
	prevStates, err := db.ListSyncStateByJob(e.db, job.ID)
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to load sync state: %v", err))
		return
	}

	prevSource := make(map[string]db.SyncState)
	prevTarget := make(map[string]db.SyncState)
	prevSourceDirETags := make(map[string]string)
	prevTargetDirETags := make(map[string]string)
	prevSourceFiles := make(map[string]fileState)
	prevTargetFiles := make(map[string]fileState)
	prevSourceDirs := make(map[string]bool) // directories seen in previous source pass (Size == -1)
	prevTargetDirs := make(map[string]bool) // directories seen in previous target pass (Size == -1)

	for _, state := range prevStates {
		cPath := cleanRelPath(state.RelPath)
		if state.Size == -1 {
			if state.Side == "source" {
				prevSourceDirETags[cPath] = state.ETag
				prevSourceDirs[cPath] = true
			} else {
				prevTargetDirETags[cPath] = state.ETag
				prevTargetDirs[cPath] = true
			}
		} else {
			stateHash := state.SourceHash
			if state.Side == "target" {
				stateHash = state.TargetHash
			}
			fs := fileState{
				Path:         cPath,
				Size:         state.Size,
				LastModified: state.Mtime.Time,
				Hash:         stateHash,
				ETag:         state.ETag,
			}
			if state.Side == "source" {
				prevSource[cPath] = state
				prevSourceFiles[cPath] = fs
			} else {
				prevTarget[cPath] = state
				prevTargetFiles[cPath] = fs
			}
		}
	}

	// 6. Enumerate Source and Target files (using parallel worker pool + ETag skipping)
	slog.Info("listing sync source", "sync_job_id", syncJobID)
	var sourceStartPaths []string
	if len(job.SelectedPaths) > 0 {
		for _, sp := range job.SelectedPaths {
			csp := cleanRelPath(sp)
			if csp != "" {
				sourceStartPaths = append(sourceStartPaths, csp)
			}
		}
	}
	if len(sourceStartPaths) == 0 {
		sourceStartPaths = []string{"/"}
	}

	sourceMap, sourceDirMap, sourceDirETags, srcErrors, err := e.listFiles(indexCtx, sourceClient, sourceStartPaths, prevSourceDirETags, prevSourceFiles)
	if err != nil {
		// Parent cancel (pause/delete/shutdown) shares indexCtx; only hard-fail on the 30m deadline.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(indexCtx.Err(), context.DeadlineExceeded) {
			e.failSync(syncJobID, generation, fmt.Sprintf("Source file listing timed out: %v", err))
			return
		}
		if ctx.Err() != nil {
			slog.Info("sync source listing interrupted", "sync_job_id", syncJobID)
			return
		}
		e.failSync(syncJobID, generation, fmt.Sprintf("Source file listing failed: %v", err))
		return
	}
	if len(srcErrors) > 0 {
		// listFiles returns the successfully enumerated portion of a tree along
		// with per-directory failures. That portion is not an authoritative
		// snapshot: treating missing entries as deletions could delete valid
		// target files or overwrite a changed target. Do not continue to delta
		// calculation or state persistence from an incomplete source scan.
		e.failSync(syncJobID, generation, fmt.Sprintf("Source file listing incomplete: %d traversal errors (first: %s)", len(srcErrors), srcErrors[0].ErrorMessage))
		return
	}

	slog.Info("listing sync target", "sync_job_id", syncJobID)
	cleanTargetDir := cleanRelPath(job.TargetDir)
	targetScanPaths := []string{cleanTargetDir}

	targetRawMap, targetDirMap, targetDirETags, tgtErrors, err := e.listFiles(indexCtx, targetClient, targetScanPaths, prevTargetDirETags, prevTargetFiles)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(indexCtx.Err(), context.DeadlineExceeded) {
			e.failSync(syncJobID, generation, fmt.Sprintf("Target file listing timed out: %v", err))
			return
		}
		if ctx.Err() != nil {
			slog.Info("sync target listing interrupted", "sync_job_id", syncJobID)
			return
		}
		e.failSync(syncJobID, generation, fmt.Sprintf("Target file listing failed: %v", err))
		return
	}
	if len(tgtErrors) > 0 {
		// As above, an incomplete target scan must not be used to infer that a
		// target file disappeared. In particular, doing so can enqueue uploads
		// that overwrite a target-side change and can erase durable sync state.
		e.failSync(syncJobID, generation, fmt.Sprintf("Target file listing incomplete: %d traversal errors (first: %s)", len(tgtErrors), tgtErrors[0].ErrorMessage))
		return
	}
	slog.Info("sync target listed", "sync_job_id", syncJobID, "file_count", len(targetRawMap))

	// Map target paths to source-side relative paths and ensure cleanRelPath
	targetMap := make(map[string]fileState)
	for targetPath, file := range targetRawMap {
		relPath := cleanRelPath(getSourceRelPath(targetPath, job.TargetDir))
		file.Path = relPath
		targetMap[relPath] = file
	}

	// Remap targetDirMap from raw target paths to source-relative paths for consistent
	// comparison against sourceDirMap during the directory delta step.
	srcRelTargetDirMap := make(map[string]bool, len(targetDirMap))
	for rawDir := range targetDirMap {
		relDir := cleanRelPath(getSourceRelPath(rawDir, job.TargetDir))
		srcRelTargetDirMap[relDir] = true
	}

	// isFirstPass is true when no sync state exists yet (initial run).
	isFirstPass := len(prevStates) == 0

	// Only delete terminal tasks from the previous pass. PENDING tasks that
	// survived the drain (e.g. from a prior incomplete pass) are also cleared
	// now since we are about to re-enqueue a fresh delta.
	_, err = e.db.Exec(`
		DELETE FROM tasks
		WHERE sync_job_id = $1 AND status IN ('COMPLETED','FAILED','CANCELLED','SKIPPED','PENDING')
	`, job.ID)
	if err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to clear old tasks: %v", err))
		return
	}

	// 7. Delta Calculation and Task Creation
	slog.Info("computing sync delta", "sync_job_id", syncJobID)
	allKeys := make(map[string]bool)
	for k := range sourceMap {
		allKeys[k] = true
	}
	for k := range targetMap {
		allKeys[k] = true
	}
	for k := range prevSource {
		allKeys[k] = true
	}
	for k := range prevTarget {
		allKeys[k] = true
	}

	slog.Info("sync delta input", "sync_job_id", syncJobID, "source_files", len(sourceMap), "target_files", len(targetMap), "previous_source_files", len(prevSource), "previous_target_files", len(prevTarget), "paths", len(allKeys), "first_pass", isFirstPass)

	type taskToCreate struct {
		filePath     string
		fileSize     int64
		sourceHash   string
		resourceType string
		action       string
		side         string // source or target
		// waitForConflictCopy prevents a source upload from racing the target
		// rename that preserves the target version of a two-way conflict.
		waitForConflictCopy bool
	}

	var tasks []taskToCreate
	var renameTasks []taskToCreate // Run renames before uploads to prevent overwrite of renamed files

	for S := range allKeys {
		srcFile, hasSrc := sourceMap[S]
		tgtFile, hasTgt := targetMap[S]
		pSrc, hasPrevSrc := prevSource[S]
		pTgt, hasPrevTgt := prevTarget[S]

		// Direct equality check between source and target file
		inSyncDirectMatch := hasSrc && hasTgt && isFileMatchingTarget(srcFile, tgtFile)

		// Source modified check
		srcModified := false
		if hasSrc {
			if inSyncDirectMatch {
				srcModified = false
			} else if hasPrevSrc {
				srcModified = isFileModified(srcFile, pSrc, true)
			} else {
				srcModified = true
			}
		}

		// Target modified check
		tgtModified := false
		if hasTgt {
			if inSyncDirectMatch {
				tgtModified = false
			} else if hasPrevTgt {
				tgtModified = isFileModified(tgtFile, pTgt, false)
			} else {
				tgtModified = true
			}
		}

		if job.Direction == "one_way" {
			// One-Way: Source -> Target
			if hasSrc && (srcModified || !hasTgt) {
				// Upload / Update
				tasks = append(tasks, taskToCreate{
					filePath:     S,
					fileSize:     srcFile.Size,
					sourceHash:   srcFile.Hash,
					resourceType: "files",
					action:       "upload",
				})
			} else if !hasSrc && hasPrevSrc {
				// Deleted on source
				if job.DeletePropagation && hasTgt {
					tasks = append(tasks, taskToCreate{
						filePath:     S,
						fileSize:     0,
						resourceType: "files",
						action:       "delete",
						side:         "target",
					})
				}
			}
		} else {
			// Two-Way: Bidirectional
			srcDeleted := !hasSrc && hasPrevSrc
			tgtDeleted := !hasTgt && hasPrevTgt

			// If files exist on both sides and match in content/size/hash, no action is needed.
			if hasSrc && hasTgt && inSyncDirectMatch {
				// Both exist and match — record state only, no task needed.
				continue
			}

			if hasSrc && srcModified && hasTgt && tgtModified {
				// Conflict! Both modified
				switch job.ConflictStrategy {
				case "OVERWRITE":
					// Source wins, overwrite target
					tasks = append(tasks, taskToCreate{
						filePath:     S,
						fileSize:     srcFile.Size,
						sourceHash:   srcFile.Hash,
						resourceType: "files",
						action:       "upload",
					})
				case "SKIP":
					// Do nothing.
				case "RENAME":
					// Rename target first, then upload source
					needsRename := conflictNeedsRename(job.ConflictStrategy)
					if needsRename {
						renameTasks = append(renameTasks, taskToCreate{
							filePath:     S,
							fileSize:     0,
							resourceType: "files",
							action:       "conflict_copy",
							side:         "target",
						})
					}
					tasks = append(tasks, taskToCreate{
						filePath:            S,
						fileSize:            srcFile.Size,
						sourceHash:          srcFile.Hash,
						resourceType:        "files",
						action:              "upload",
						waitForConflictCopy: needsRename,
					})
				default:
					// Creation validates this value, but a corrupt legacy row must
					// not silently choose a destructive conflict action.
					slog.Warn("ignoring conflict with unsupported strategy", "sync_job_id", syncJobID, "conflict_strategy", job.ConflictStrategy)
				}
			} else if hasSrc && (srcModified || (!hasTgt && !tgtDeleted)) {
				// Present on source, and (modified OR missing from target and not deleted on target) -> upload to target
				tasks = append(tasks, taskToCreate{
					filePath:     S,
					fileSize:     srcFile.Size,
					sourceHash:   srcFile.Hash,
					resourceType: "files",
					action:       "upload",
				})
			} else if hasTgt && (tgtModified || (!hasSrc && !srcDeleted)) {
				// Present on target, and (modified OR missing from source and not deleted on source) -> download to source
				tasks = append(tasks, taskToCreate{
					filePath:     S,
					fileSize:     tgtFile.Size,
					sourceHash:   tgtFile.Hash,
					resourceType: "files",
					action:       "download",
				})
			} else if srcDeleted && (!hasTgt || !tgtModified) {
				// Deleted on source, propagate to target
				if job.DeletePropagation && hasTgt {
					tasks = append(tasks, taskToCreate{
						filePath:     S,
						fileSize:     0,
						resourceType: "files",
						action:       "delete",
						side:         "target",
					})
				}
			} else if tgtDeleted && (!hasSrc || !srcModified) {
				// Deleted on target, propagate to source
				if job.DeletePropagation && hasSrc {
					tasks = append(tasks, taskToCreate{
						filePath:     S,
						fileSize:     0,
						resourceType: "files",
						action:       "delete",
						side:         "source",
					})
				}
			}
		}

	}

	// Directory delta: create missing directories on target (or source for two-way).
	// We only emit mkdir tasks for directories discovered in the *current* scan that
	// are absent on the other side. We skip root paths ("/") and the target root itself.
	for dirPath := range sourceDirMap {
		if dirPath == "/" {
			continue
		}
		// Does this directory already exist on the target?
		if srcRelTargetDirMap[dirPath] {
			continue
		}
		// One-Way or Two-Way: source dir missing from target -> mkdir on target
		tasks = append(tasks, taskToCreate{
			filePath:     dirPath,
			fileSize:     0,
			resourceType: "files",
			action:       "mkdir",
			side:         "target",
		})
		// Also handle delete propagation: if dir existed before on source but now
		// it's gone AND delete propagation is enabled, we delete it on target.
		// (Handled in the file deletion loop for now; directories are pruned by
		// pruneEmptyParentDirectories after all files are deleted.)
	}

	// Two-Way only: target dir missing from source -> mkdir on source
	if job.Direction == "two_way" {
		for dirPath := range srcRelTargetDirMap {
			if dirPath == "/" {
				continue
			}
			if sourceDirMap[dirPath] {
				continue
			}
			// Target has the dir but source doesn't.
			if !prevSourceDirs[dirPath] {
				// Dir is new on target (not previously known on source): create on source.
				tasks = append(tasks, taskToCreate{
					filePath:     dirPath,
					fileSize:     0,
					resourceType: "files",
					action:       "mkdir",
					side:         "source",
				})
			} else if job.DeletePropagation {
				// Dir was previously on source, now gone from source but still on target:
				// propagate deletion to target (delete the directory on target).
				// Only safe if dir is empty; pruneEmptyParentDirectories will handle cleanup.
				tasks = append(tasks, taskToCreate{
					filePath:     dirPath,
					fileSize:     0,
					resourceType: "files",
					action:       "delete",
					side:         "target",
				})
			}
		}
	}

	// One-Way: delete propagation for source dirs no longer present
	if job.Direction == "one_way" && job.DeletePropagation {
		for dirPath := range prevSourceDirs {
			if dirPath == "/" {
				continue
			}
			if sourceDirMap[dirPath] {
				continue // still present
			}
			if srcRelTargetDirMap[dirPath] {
				// Was on source before, now gone, but exists on target: delete it.
				tasks = append(tasks, taskToCreate{
					filePath:     dirPath,
					fileSize:     0,
					resourceType: "files",
					action:       "delete",
					side:         "target",
				})
			}
		}
	}

	totalCreatedTasks := len(renameTasks) + len(tasks)
	slog.Info("sync tasks calculated", "sync_job_id", syncJobID, "task_count", totalCreatedTasks)

	if totalCreatedTasks == 0 {
		// The baseline and lifecycle result must commit together. Otherwise a
		// successful empty pass can lose deletion/conflict history.
		upserts, deletes := syncStateChanges(job.ID, sourceMap, targetMap, prevSource, prevTarget, sourceDirETags, targetDirETags, sourceDirMap, srcRelTargetDirMap, prevSourceDirETags, prevTargetDirETags, prevSourceDirs, prevTargetDirs, nil)
		finalized, err := db.FinalizeEmptySyncJobPassWithStates(e.db, job.ID, generation, "SUCCESS", nil, 0, 0, 0, 0, 0, upserts, deletes)
		if err != nil {
			slog.Error("failed to finalize empty sync pass", "sync_job_id", syncJobID, "error", err)
			e.failSync(syncJobID, generation, "Failed to persist sync state")
			return
		}
		if !finalized {
			slog.Warn("empty sync pass status changed before finalization", "sync_job_id", syncJobID)
			return
		}
		return
	}

	// Insert tasks into database — use bulk insert to reduce DB round-trips from
	// N (one per task) to ceil(N/500) (one batch statement per 500 rows).
	allTasksToEnqueue := make([]taskToCreate, 0, len(renameTasks)+len(tasks))
	allTasksToEnqueue = append(allTasksToEnqueue, renameTasks...)
	allTasksToEnqueue = append(allTasksToEnqueue, tasks...)
	dbTasks := make([]*db.Task, 0, len(allTasksToEnqueue))
	var totalBytes int64
	for _, tc := range allTasksToEnqueue {
		totalBytes += tc.fileSize
		meta := map[string]interface{}{
			"action": tc.action,
		}
		if tc.side != "" {
			meta["side"] = tc.side
		}
		if tc.waitForConflictCopy {
			meta["wait_for_conflict_copy"] = true
		}
		metaJSON, _ := json.Marshal(meta)

		dbTasks = append(dbTasks, &db.Task{
			SyncJobID:      job.ID,
			PassGeneration: generation,
			FilePath:       tc.filePath,
			FileSize:       tc.fileSize,
			SourceHash:     sql.NullString{String: tc.sourceHash, Valid: tc.sourceHash != ""},
			Status:         "PENDING",
			ResourceType:   tc.resourceType,
			Metadata:       metaJSON,
		})
	}
	if err := db.BulkCreateSyncTasks(ctx, e.db, dbTasks); err != nil {
		e.failSync(syncJobID, generation, fmt.Sprintf("Failed to create tasks in DB: %v", err))
		return
	}
	// Update totals
	updated, err := db.UpdateSyncJobTotals(e.db, job.ID, generation, totalCreatedTasks, totalBytes)
	if err != nil {
		slog.Error("failed to update sync totals", "sync_job_id", syncJobID, "error", err)
		return
	}
	if !updated {
		slog.Warn("sync pass superseded before totals update", "sync_job_id", syncJobID)
		return
	}

	// Transition to RUNNING
	running, err := db.TransitionSyncJobToRunning(e.db, job.ID, generation)
	if err != nil {
		slog.Error("failed to set sync job running", "sync_job_id", syncJobID, "error", err)
		return
	}
	if !running {
		slog.Warn("sync job status changed during indexing", "sync_job_id", syncJobID)
		return
	}
	// Keep the cross-instance lock while workers drain. Releasing it here would
	// let a resume start a successor while this coordinator still owns live
	// transfers.
	// Wake idle worker threads only after the queue predicate permits them to
	// claim this pass's tasks, avoiding a fallback-poll delay after indexing.
	e.queue.NotifyTaskAvailable(ctx, e.db)

	// 8. Poll database until all tasks finish (or context is cancelled)
	// Poll every 1s: tight enough to react quickly when the last task finishes
	// without adding noticeable DB load (only runs while the job is RUNNING).
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

SyncTaskPoll:
	for {
		select {
		case <-ctx.Done():
			// Context was cancelled (server shutdown or explicit cancel from delete/pause).
			// Do not mark as FAILED — leave status to the caller (delete removes the row;
			// pause has already set the status via handlePauseSync).
			// Pause leaves RUNNING task rows in place until their worker reaches a
			// terminal state. Give cancellation a bounded grace period so a crashed
			// worker or unresponsive provider cannot indefinitely hold the pass lock
			// or prevent API shutdown.
			drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(serverCtx), 30*time.Second)
			if err := e.drainGenerationTasks(drainCtx, job.ID, generation); err != nil {
				slog.Warn("timed out draining cancelled sync pass", "sync_job_id", syncJobID, "error", err)
			}
			drainCancel()
			slog.Info("sync pass interrupted", "sync_job_id", syncJobID)
			return
		case <-ticker.C:
			var openCount int
			err := e.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM tasks 
				WHERE sync_job_id = $1 AND pass_generation = $2
				  AND (status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL))
			`, job.ID, generation).Scan(&openCount)
			if err != nil {
				slog.Warn("failed to query sync task progress", "sync_job_id", syncJobID, "error", err)
				continue
			}

			if openCount == 0 {
				break SyncTaskPoll
			}
		}
	}

	slog.Info("sync tasks finished; checking verification", "sync_job_id", syncJobID)

	var unverifiedCount int
	if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'COMPLETED' AND checksum_verified = FALSE`, job.ID, generation).Scan(&unverifiedCount); err != nil {
		e.failSync(syncJobID, generation, "Failed to determine checksum verification requirements")
		return
	}
	if unverifiedCount > 0 {
		slog.Info("transitioning sync job to verification", "sync_job_id", syncJobID, "unverified_tasks", unverifiedCount)
		verifying, err := db.TransitionSyncJobToVerifying(e.db, job.ID, generation)
		if err != nil {
			slog.Error("failed to set sync job verifying", "sync_job_id", syncJobID, "error", err)
			return
		}
		if !verifying {
			slog.Warn("sync job status changed before verification", "sync_job_id", syncJobID)
			return
		}

		verifyTicker := time.NewTicker(1 * time.Second)
		defer verifyTicker.Stop()

		for verifying := true; verifying; {
			select {
			case <-ctx.Done():
				_, _ = db.AbortSyncJobVerification(e.db, job.ID, generation)
				verifying = false
			case <-verifyTicker.C:
				var currentStatus string
				var currentGeneration int
				if err := e.db.QueryRow(`SELECT status, run_generation FROM sync_jobs WHERE id = $1`, job.ID).Scan(&currentStatus, &currentGeneration); err == nil {
					if currentStatus != "VERIFYING" || currentGeneration != generation {
						verifying = false
						continue
					}
					var remaining int
					if err := e.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'COMPLETED' AND checksum_verified = FALSE`, job.ID, generation).Scan(&remaining); err == nil && remaining == 0 {
						slog.Info("sync verification completed", "sync_job_id", syncJobID)
						verifying = false
					}
				}
			}
		}
	}

	// A task worker may have stopped this pass while the engine was polling or
	// verifying (for example after an authentication failure). Do not derive
	// outcomes or update sync state for an interrupted pass.
	currentJob, err := db.GetSyncJob(e.db, job.ID)
	if err != nil {
		slog.Error("failed to read final sync job status", "sync_job_id", syncJobID, "error", err)
		return
	}
	if currentJob.RunGeneration != generation || (currentJob.Status != "RUNNING" && currentJob.Status != "VERIFYING") {
		slog.Warn("stopping sync completion after status change", "sync_job_id", syncJobID, "status", currentJob.Status)
		return
	}

	slog.Info("writing sync outcomes", "sync_job_id", syncJobID)

	// 9. The baseline must only advance from a complete task snapshot.
	stats, err := e.readFinalTaskOutcomes(ctx, job.ID, generation)
	if err != nil {
		e.failSync(syncJobID, generation, "Failed to read final task outcomes")
		return
	}
	total, completed, skipped, failed := stats.total, stats.completed, stats.skipped, stats.failed
	changedCount, deletedCount := stats.changed, stats.deleted
	taskOutcomes := stats.outcomes

	// Determine final outcome status
	finalRunStatus := "SUCCESS"
	var finalErr *string
	if failed > 0 {
		if failed == total {
			finalRunStatus = "FAILED"
			msg := "All file transfer tasks failed"
			finalErr = &msg
		} else {
			finalRunStatus = "PARTIAL"
			msg := fmt.Sprintf("%d of %d tasks failed", failed, total)
			finalErr = &msg
		}
	}

	// Persist the durable delta baseline and return to IDLE in one transaction.
	// The predicate excludes FAILED/PAUSED_*, so a concurrent task-worker
	// failure is not overwritten.
	upserts, deletes := syncStateChanges(job.ID, sourceMap, targetMap, prevSource, prevTarget, sourceDirETags, targetDirETags, sourceDirMap, srcRelTargetDirMap, prevSourceDirETags, prevTargetDirETags, prevSourceDirs, prevTargetDirs, taskOutcomes)
	finalized, err := db.FinalizeSyncJobPassWithStates(e.db, job.ID, generation, finalRunStatus, finalErr, total, completed+skipped, changedCount, deletedCount, failed, upserts, deletes)
	if err != nil {
		slog.Error("failed to finalize sync pass", "sync_job_id", syncJobID, "outcome", finalRunStatus, "total", total, "processed", completed+skipped, "changed", changedCount, "deleted", deletedCount, "failed", failed, "error", err)
		e.failSync(syncJobID, generation, "Failed to persist sync state")
		return
	}
	if !finalized {
		slog.Warn("sync job status changed before finalization", "sync_job_id", syncJobID)
		return
	}

	auditAction := db.AuditSyncCompleted
	if finalRunStatus == "FAILED" {
		auditAction = db.AuditSyncFailed
	}
	db.WriteAuditLog(e.db, db.AuditEntry{
		UserID: sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		Action: auditAction,
		Target: job.ID,
	})

	slog.Info("sync pass completed", "sync_job_id", syncJobID, "status", finalRunStatus, "processed", completed+skipped, "changed", changedCount, "deleted", deletedCount, "failed", failed)
}

// drainRemainingTasks waits for any RUNNING tasks from a previous pass to reach
// a terminal state before their rows are deleted. It deliberately has no
// independent success deadline: a worker still in provider I/O owns the task
// and may mutate the target, so starting a successor pass would allow two
// passes to write, rename, or delete the same path concurrently. The caller's
// indexing context supplies the bounded failure path. On its deadline the pass
// fails without changing the old RUNNING task; its worker retains ownership and
// must reach a terminal state before a later pass can begin.
func (e *Engine) drainRemainingTasks(ctx context.Context, jobID string) error {
	return waitForNoRunningTasks(ctx, 3*time.Second, func() (int, error) {
		var running bool
		err := e.db.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM tasks WHERE sync_job_id = $1 AND status = 'RUNNING')
		`, jobID).Scan(&running)
		if err != nil {
			return 0, fmt.Errorf("check running tasks for sync job %s: %w", jobID, err)
		}
		if running {
			return 1, nil
		}
		return 0, nil
	})
}

func (e *Engine) drainGenerationTasks(ctx context.Context, jobID string, generation int) error {
	return waitForNoRunningTasks(ctx, 3*time.Second, func() (int, error) {
		var running bool
		err := e.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'RUNNING')`, jobID, generation).Scan(&running)
		if err != nil {
			return 0, fmt.Errorf("check running tasks for sync job %s generation %d: %w", jobID, generation, err)
		}
		if running {
			return 1, nil
		}
		return 0, nil
	})
}

// waitForNoRunningTasks checks immediately, then polls until task ownership
// ends or the caller's context expires. Keeping the polling mechanics separate
// makes the safety behavior testable without a database.
func waitForNoRunningTasks(ctx context.Context, interval time.Duration, countRunning func() (int, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := countRunning()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			count, err := countRunning()
			if err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
		}
	}
}

func (e *Engine) failSync(id string, generation int, errMsg string) {
	errMsg = sanitize.SanitizeError(errMsg)
	slog.Error("sync pass failed", "sync_job_id", id, "error", errMsg)
	failed, err := db.FailSyncJobPass(e.db, id, generation, errMsg)
	if err != nil {
		slog.Error("failed to record failed sync pass", "sync_job_id", id, "error", err)
		return
	}
	if !failed {
		slog.Warn("sync job status changed before failure recording", "sync_job_id", id)
		return
	}
	if ownerID, err := db.GetSyncJobOwnerID(e.db, id); err == nil {
		db.WriteAuditLog(e.db, db.AuditEntry{
			UserID: sql.NullString{String: ownerID, Valid: ownerID != ""},
			Action: db.AuditSyncFailed,
			Target: id,
		})
	}
}

// Delta calculation, state update, and listing helpers (updateSyncStates, listFiles,
// isFileModified, isFileMatchingTarget, ensureFreshToken) are located in delta.go.
