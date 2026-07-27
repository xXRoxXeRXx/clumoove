package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
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
	claimed, err := db.ClaimSyncJobPass(e.db, syncJobID)
	if err != nil || !claimed {
		return claimed, err
	}
	go e.runSyncPass(serverCtx, syncJobID)
	return true, nil
}

// ResumePausedSyncPass claims a connection-loss-paused job and starts its pass.
func (e *Engine) ResumePausedSyncPass(serverCtx context.Context, syncJobID string) (bool, error) {
	claimed, err := db.ClaimPausedSyncJobPass(e.db, syncJobID)
	if err != nil || !claimed {
		return claimed, err
	}
	go e.runSyncPass(serverCtx, syncJobID)
	return true, nil
}

// runSyncPass performs a previously claimed sync pass: scans, computes delta,
// enqueues tasks, waits, and updates state.
func (e *Engine) runSyncPass(serverCtx context.Context, syncJobID string) {
	log.Printf("[SyncEngine] Starting sync pass for job %s\n", syncJobID)

	// Register a cancellable child context so handleDeleteSync / handlePauseSync
	// can interrupt this goroutine without having to wait for the poll timeout.
	ctx, cancel := context.WithCancel(serverCtx)
	e.cancelMu.Lock()
	e.activePassCancels[syncJobID] = cancel
	e.cancelMu.Unlock()
	defer func() {
		cancel()
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
		log.Printf("[SyncEngine] Refusing pass for %s: failed to load claimed job: %v\n", syncJobID, err)
		return
	}
	if job.Status != "INDEXING" {
		log.Printf("[SyncEngine] Refusing pass for %s: expected INDEXING, got %s\n", syncJobID, job.Status)
		return
	}

	// 1. Fetch the claimed job configuration.

	// 3. Decrypt credentials
	sourcePass, err := crypto.Decrypt(job.SourcePasswordEncrypted, e.encryptionKey)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}
	defer crypto.ZeroString(&sourcePass)

	targetPass, err := crypto.Decrypt(job.TargetPasswordEncrypted, e.encryptionKey)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to decrypt target password: %v", err))
		return
	}
	defer crypto.ZeroString(&targetPass)

	// Refresh OAuth tokens if necessary
	if job.SourceRefreshTokenEncrypted.Valid && job.SourceRefreshTokenEncrypted.String != "" {
		sourcePass, err = e.ensureFreshToken(syncJobID, job, "source", sourcePass)
		if err != nil {
			e.failSync(syncJobID, fmt.Sprintf("Failed to refresh source OAuth token: %v", err))
			return
		}
	}
	if job.TargetRefreshTokenEncrypted.Valid && job.TargetRefreshTokenEncrypted.String != "" {
		targetPass, err = e.ensureFreshToken(syncJobID, job, "target", targetPass)
		if err != nil {
			e.failSync(syncJobID, fmt.Sprintf("Failed to refresh target OAuth token: %v", err))
			return
		}
	}

	// 4. Create storage provider clients
	sourceClient, err := storage.NewProvider(indexCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to connect to source: %v", err))
		return
	}
	defer sourceClient.Close()

	targetClient, err := storage.NewProvider(indexCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to connect to target: %v", err))
		return
	}
	defer targetClient.Close()

	// Connect to both providers
	connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
	defer connCancel()
	if ok, err := sourceClient.Connect(connCtx); !ok {
		e.failSync(syncJobID, fmt.Sprintf("Source connection failed: %v", err))
		return
	}
	if ok, err := targetClient.Connect(connCtx); !ok {
		e.failSync(syncJobID, fmt.Sprintf("Target connection failed: %v", err))
		return
	}

	// 5. Load previous state from DB to enable ETag folder skipping and fast delta checks
	prevStates, err := db.ListSyncStateByJob(e.db, job.ID)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to load sync state: %v", err))
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
			fs := fileState{
				Path:         cPath,
				Size:         state.Size,
				LastModified: state.Mtime.Time,
				Hash:         state.SourceHash,
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
	log.Printf("[SyncEngine] Listing source files for job %s...\n", syncJobID)
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
			e.failSync(syncJobID, fmt.Sprintf("Source file listing timed out: %v", err))
			return
		}
		if ctx.Err() != nil {
			log.Printf("[SyncEngine] Source listing interrupted for job %s: %v\n", syncJobID, ctx.Err())
			return
		}
		e.failSync(syncJobID, fmt.Sprintf("Source file listing failed: %v", err))
		return
	}
	if len(srcErrors) > 0 {
		log.Printf("[SyncEngine] Warning: encountered %d indexing errors on source for job %s\n", len(srcErrors), syncJobID)
	}

	log.Printf("[SyncEngine] Listing target files for job %s...\n", syncJobID)
	cleanTargetDir := cleanRelPath(job.TargetDir)
	targetScanPaths := []string{cleanTargetDir}

	targetRawMap, targetDirMap, targetDirETags, tgtErrors, err := e.listFiles(indexCtx, targetClient, targetScanPaths, prevTargetDirETags, prevTargetFiles)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(indexCtx.Err(), context.DeadlineExceeded) {
			e.failSync(syncJobID, fmt.Sprintf("Target file listing timed out: %v", err))
			return
		}
		if ctx.Err() != nil {
			log.Printf("[SyncEngine] Target listing interrupted for job %s: %v\n", syncJobID, ctx.Err())
			return
		}
		e.failSync(syncJobID, fmt.Sprintf("Target file listing failed: %v", err))
		return
	}
	if len(tgtErrors) > 0 {
		log.Printf("[SyncEngine] Warning: encountered %d indexing errors on target for job %s (e.g. %v)\n", len(tgtErrors), syncJobID, tgtErrors[0])
	}
	log.Printf("[SyncEngine] Job %s target raw files listed: %d (targetScanPaths=%v)\n", syncJobID, len(targetRawMap), targetScanPaths)

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

	// Wait for any tasks that may still be RUNNING from a previous pass before
	// clearing them. This prevents a race where we delete task rows that a worker
	// thread currently holds, causing silent counter drift.
	if err := e.drainRemainingTasks(indexCtx, job.ID); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(indexCtx.Err(), context.DeadlineExceeded) {
			e.failSync(syncJobID, fmt.Sprintf("Indexing phase timed out while draining previous tasks: %v", err))
			return
		}
		log.Printf("[SyncEngine] Drain interrupted for job %s: %v\n", syncJobID, err)
		return
	}

	// Only delete terminal tasks from the previous pass. PENDING tasks that
	// survived the drain (e.g. from a prior incomplete pass) are also cleared
	// now since we are about to re-enqueue a fresh delta.
	_, err = e.db.Exec(`
		DELETE FROM tasks
		WHERE sync_job_id = $1 AND status IN ('COMPLETED','FAILED','CANCELLED','SKIPPED','PENDING')
	`, job.ID)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to clear old tasks: %v", err))
		return
	}

	// 7. Delta Calculation and Task Creation
	log.Printf("[SyncEngine] Computing delta and enqueuing tasks for job %s...\n", syncJobID)
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

	log.Printf("[SyncEngine] Job %s delta breakdown: sourceMap=%d, targetMap=%d, prevSource=%d, prevTarget=%d, allKeys=%d, isFirstPass=%v\n",
		syncJobID, len(sourceMap), len(targetMap), len(prevSource), len(prevTarget), len(allKeys), isFirstPass)

	type taskToCreate struct {
		filePath     string
		fileSize     int64
		sourceHash   string
		resourceType string
		action       string
		side         string // source or target
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
					// Do nothing
					log.Printf("[SyncEngine] Sync conflict for %s: skipping due to strategy SKIP\n", S)
				case "RENAME":
					// Rename target first, then upload source
					if conflictNeedsRename(job.ConflictStrategy) {
						renameTasks = append(renameTasks, taskToCreate{
							filePath:     S,
							fileSize:     0,
							resourceType: "files",
							action:       "conflict_copy",
							side:         "target",
						})
					}
					tasks = append(tasks, taskToCreate{
						filePath:     S,
						fileSize:     srcFile.Size,
						sourceHash:   srcFile.Hash,
						resourceType: "files",
						action:       "upload",
					})
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

		if len(tasks) <= 5 && len(tasks) > 0 {
			last := tasks[len(tasks)-1]
			log.Printf("[SyncEngine] Delta task #%d: path=%s action=%s side=%s (hasSrc=%v, hasTgt=%v, srcSize=%d, tgtSize=%d, srcMtime=%v, tgtMtime=%v)\n",
				len(tasks), last.filePath, last.action, last.side, hasSrc, hasTgt, srcFile.Size, tgtFile.Size, srcFile.LastModified, tgtFile.LastModified)
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
	log.Printf("[SyncEngine] Job %s: calculated %d tasks to run\n", syncJobID, totalCreatedTasks)

	if totalCreatedTasks == 0 {
		// No transfers needed: update stats immediately and complete run
		finalized, err := db.FinalizeSyncJobPass(e.db, job.ID, "SUCCESS", nil, 0, 0, 0, 0, 0)
		if err != nil {
			log.Printf("[SyncEngine] Failed to finalize empty sync pass for job %s: %v\n", syncJobID, err)
			return
		}
		if !finalized {
			log.Printf("[SyncEngine] Not finalizing empty sync pass for job %s; status changed during execution\n", syncJobID)
			return
		}
		e.updateSyncStates(job.ID, sourceMap, targetMap, prevSource, prevTarget, sourceDirETags, targetDirETags, sourceDirMap, srcRelTargetDirMap, prevSourceDirETags, prevTargetDirETags, nil)
		return
	}

	// Insert tasks into database — use bulk insert to reduce DB round-trips from
	// N (one per task) to ceil(N/500) (one batch statement per 500 rows).
	allTasksToEnqueue := append(renameTasks, tasks...)
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
		metaJSON, _ := json.Marshal(meta)

		dbTasks = append(dbTasks, &db.Task{
			SyncJobID:    job.ID,
			FilePath:     tc.filePath,
			FileSize:     tc.fileSize,
			SourceHash:   sql.NullString{String: tc.sourceHash, Valid: tc.sourceHash != ""},
			Status:       "PENDING",
			ResourceType: tc.resourceType,
			Metadata:     metaJSON,
		})
	}
	if err := db.BulkCreateSyncTasks(e.db, dbTasks); err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to create tasks in DB: %v", err))
		return
	}
	// Update totals
	_ = db.UpdateSyncJobTotals(e.db, job.ID, totalCreatedTasks, totalBytes)

	// Transition to RUNNING
	running, err := db.TransitionSyncJobToRunning(e.db, job.ID)
	if err != nil {
		log.Printf("[SyncEngine] Failed to set RUNNING status for job %s: %v\n", syncJobID, err)
		return
	}
	if !running {
		log.Printf("[SyncEngine] Not starting task processing for job %s; status changed during indexing\n", syncJobID)
		return
	}
	// Wake idle worker threads only after the queue predicate permits them to
	// claim this pass's tasks, avoiding a fallback-poll delay after indexing.
	e.queue.NotifyTaskAvailable(ctx, e.db)

	// 8. Poll database until all tasks finish (or context is cancelled)
	// Poll every 1s: tight enough to react quickly when the last task finishes
	// without adding noticeable DB load (only runs while the job is RUNNING).
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context was cancelled (server shutdown or explicit cancel from delete/pause).
			// Do not mark as FAILED — leave status to the caller (delete removes the row;
			// pause has already set the status via handlePauseSync).
			log.Printf("[SyncEngine] Sync pass for job %s interrupted: %v\n", syncJobID, ctx.Err())
			return
		case <-ticker.C:
			var openCount int
			err := e.db.QueryRow(`
				SELECT COUNT(*) FROM tasks 
				WHERE sync_job_id = $1 
				  AND (status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL))
			`, job.ID).Scan(&openCount)
			if err != nil {
				log.Printf("[SyncEngine] Error querying task progress for job %s: %v\n", syncJobID, err)
				continue
			}

			if openCount == 0 {
				goto SyncTasksDone
			}
		}
	}

SyncTasksDone:
	log.Printf("[SyncEngine] All tasks finished for job %s. Checking verification requirements...\n", syncJobID)

	var unverifiedCount int
	_ = e.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND status = 'COMPLETED' AND checksum_verified = FALSE`, job.ID).Scan(&unverifiedCount)
	if unverifiedCount > 0 {
		log.Printf("[SyncEngine] Transitioning job %s to VERIFYING status (%d unverified tasks)...\n", syncJobID, unverifiedCount)
		verifying, err := db.TransitionSyncJobToVerifying(e.db, job.ID)
		if err != nil {
			log.Printf("[SyncEngine] Failed to set VERIFYING status for job %s: %v\n", syncJobID, err)
			return
		}
		if !verifying {
			log.Printf("[SyncEngine] Not verifying job %s; status changed during task completion\n", syncJobID)
			return
		}

		verifyTimeout := time.After(2 * time.Minute)
		verifyTicker := time.NewTicker(1 * time.Second)

		for verifying := true; verifying; {
			select {
			case <-ctx.Done():
				verifying = false
			case <-verifyTimeout:
				log.Printf("[SyncEngine] Verification timeout reached for job %s\n", syncJobID)
				verifying = false
			case <-verifyTicker.C:
				var currentStatus string
				if err := e.db.QueryRow(`SELECT status FROM sync_jobs WHERE id = $1`, job.ID).Scan(&currentStatus); err == nil {
					if currentStatus != "VERIFYING" {
						verifying = false
					}
				}
			}
		}
		verifyTicker.Stop()
	}

	// A task worker may have stopped this pass while the engine was polling or
	// verifying (for example after an authentication failure). Do not derive
	// outcomes or update sync state for an interrupted pass.
	currentJob, err := db.GetSyncJob(e.db, job.ID)
	if err != nil {
		log.Printf("[SyncEngine] Failed to read final status for job %s: %v\n", syncJobID, err)
		return
	}
	if currentJob.Status != "RUNNING" && currentJob.Status != "VERIFYING" {
		log.Printf("[SyncEngine] Stopping completion for job %s; current status is %s\n", syncJobID, currentJob.Status)
		return
	}

	log.Printf("[SyncEngine] Writing outcomes for job %s...\n", syncJobID)

	// 9. Process final statistics and state updates
	var total, completed, skipped, failed int
	query := `
		SELECT 
			COUNT(*) as total,
			COUNT(*) FILTER (WHERE status = 'COMPLETED') as completed,
			COUNT(*) FILTER (WHERE status = 'SKIPPED') as skipped,
			COUNT(*) FILTER (WHERE status = 'FAILED' OR status = 'CANCELLED') as failed
		FROM tasks
		WHERE sync_job_id = $1
	`
	err = e.db.QueryRow(query, job.ID).Scan(&total, &completed, &skipped, &failed)
	if err != nil {
		log.Printf("[SyncEngine] Error querying task statistics for job %s: %v\n", syncJobID, err)
		// Fallback to defaults
	}

	// Query task statuses to build success map for sync states
	taskOutcomes := make(map[string]string) // filePath -> status (COMPLETED, SKIPPED, FAILED)
	rows, err := e.db.Query(`SELECT file_path, status FROM tasks WHERE sync_job_id = $1`, job.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var fp, st string
			if err := rows.Scan(&fp, &st); err == nil {
				taskOutcomes[fp] = st
			}
		}
	}

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

	// We count uploads/renames/conflict copies as changed files, and propagates as deleted
	var changedCount, deletedCount int
	taskRows, err := e.db.Query(`SELECT file_path, status, metadata FROM tasks WHERE sync_job_id = $1`, job.ID)
	if err == nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var fp, st string
			var metaBytes []byte
			if err := taskRows.Scan(&fp, &st, &metaBytes); err == nil && (st == "COMPLETED" || st == "SKIPPED") {
				var meta map[string]interface{}
				_ = json.Unmarshal(metaBytes, &meta)
				action, _ := meta["action"].(string)
				if action == "delete" {
					deletedCount++
				} else if action == "upload" || action == "download" || action == "conflict_copy" {
					changedCount++
				}
			}
		}
	}

	// Persist the run outcome and return to IDLE only if this engine still owns
	// an active pass. In particular, do not overwrite a FAILED/PAUSED_* state
	// written by a task worker after an auth or connection failure.
	finalized, err := db.FinalizeSyncJobPass(e.db, job.ID, finalRunStatus, finalErr, total, completed+skipped, changedCount, deletedCount, failed)
	if err != nil {
		log.Printf("[SyncEngine] Failed to finalize job %s: %v\n", syncJobID, err)
		return
	}
	if !finalized {
		log.Printf("[SyncEngine] Not finalizing job %s; status changed during execution\n", syncJobID)
		return
	}

	// Apply durable sync state only after the lifecycle CAS succeeds. A late
	// task-worker failure must leave both the job status and its delta state
	// untouched so the next pass can retry correctly.
	e.updateSyncStates(job.ID, sourceMap, targetMap, prevSource, prevTarget, sourceDirETags, targetDirETags, sourceDirMap, srcRelTargetDirMap, prevSourceDirETags, prevTargetDirETags, taskOutcomes)

	auditAction := db.AuditSyncCompleted
	if finalRunStatus == "FAILED" {
		auditAction = db.AuditSyncFailed
	}
	db.WriteAuditLog(e.db, db.AuditEntry{
		UserID: sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		Action: auditAction,
		Target: job.ID,
	})

	log.Printf("[SyncEngine] Sync pass completed for job %s. Status: %s, Processed: %d, Changed: %d, Deleted: %d, Failed: %d\n",
		syncJobID, finalRunStatus, completed+skipped, changedCount, deletedCount, failed)
}

// drainRemainingTasks waits (up to 2 minutes) for any RUNNING tasks from a
// previous pass to reach a terminal state before we delete the task rows.
// This prevents counter drift caused by deleting task rows that a worker thread
// is still operating on. If the context is cancelled first, it returns an error.
func (e *Engine) drainRemainingTasks(ctx context.Context, jobID string) error {
	deadline := time.Now().Add(2 * time.Minute)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var runningCount int
			err := e.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND status = 'RUNNING'
			`, jobID).Scan(&runningCount)
			if err != nil || runningCount == 0 {
				return nil
			}
			if time.Now().After(deadline) {
				log.Printf("[SyncEngine] Drain deadline exceeded for job %s (%d tasks still RUNNING); proceeding anyway\n", jobID, runningCount)
				return nil
			}
		}
	}
}

func (e *Engine) failSync(id string, errMsg string) {
	log.Printf("[SyncEngine] Job %s failed pass: %s\n", id, errMsg)
	failed, err := db.FailSyncJobPass(e.db, id, errMsg)
	if err != nil {
		log.Printf("[SyncEngine] Failed to record failed pass for job %s: %v\n", id, err)
		return
	}
	if !failed {
		log.Printf("[SyncEngine] Not recording failed pass for job %s; status changed during execution\n", id)
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
