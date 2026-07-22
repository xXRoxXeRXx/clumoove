package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/storage"
)

type Engine struct {
	db            *sql.DB
	queue         *queue.Queue
	encryptionKey string

	// cancelMu guards the activePassCancels map which tracks in-progress
	// RunSyncPass goroutines. Entries are added just before the goroutine body
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

// CancelPass cancels any in-progress RunSyncPass for the given sync job.
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

// RunSyncPass performs a single sync pass: scans, computes delta, enqueues tasks, waits, and updates state.
func (e *Engine) RunSyncPass(serverCtx context.Context, syncJobID string) {
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

	// 1. Transition to INDEXING
	err := db.UpdateSyncJobStatus(e.db, syncJobID, "INDEXING", nil)
	if err != nil {
		log.Printf("[SyncEngine] Failed to set INDEXING status for job %s: %v\n", syncJobID, err)
		return
	}

	// 2. Fetch Job configuration
	job, err := db.GetSyncJob(e.db, syncJobID)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to fetch sync job: %v", err))
		return
	}

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
	sourceClient, err := storage.NewProvider(ctx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Failed to connect to source: %v", err))
		return
	}
	defer sourceClient.Close()

	targetClient, err := storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
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

	for _, state := range prevStates {
		if state.Size == -1 {
			if state.Side == "source" {
				prevSourceDirETags[state.RelPath] = state.ETag
			} else {
				prevTargetDirETags[state.RelPath] = state.ETag
			}
		} else {
			fs := fileState{
				Path:         state.RelPath,
				Size:         state.Size,
				LastModified: state.Mtime.Time,
				Hash:         state.SourceHash,
				ETag:         state.ETag,
			}
			if state.Side == "source" {
				prevSource[state.RelPath] = state
				prevSourceFiles[state.RelPath] = fs
			} else {
				prevTarget[state.RelPath] = state
				prevTargetFiles[state.RelPath] = fs
			}
		}
	}

	// 6. Enumerate Source and Target files (using parallel worker pool + ETag skipping)
	log.Printf("[SyncEngine] Listing source files for job %s...\n", syncJobID)
	sourceMap, sourceDirETags, srcErrors, err := e.listFiles(ctx, sourceClient, job.SelectedPaths, prevSourceDirETags, prevSourceFiles)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Source file listing failed: %v", err))
		return
	}
	if len(srcErrors) > 0 {
		log.Printf("[SyncEngine] Warning: encountered %d indexing errors on source for job %s\n", len(srcErrors), syncJobID)
	}

	log.Printf("[SyncEngine] Listing target files for job %s...\n", syncJobID)
	var targetScanPaths []string
	if job.Direction == "two_way" && len(job.SelectedPaths) > 0 {
		for _, sp := range job.SelectedPaths {
			targetScanPaths = append(targetScanPaths, path.Clean(path.Join(job.TargetDir, sp)))
		}
	} else {
		targetScanPaths = []string{job.TargetDir}
	}
	targetRawMap, targetDirETags, _, err := e.listFiles(ctx, targetClient, targetScanPaths, prevTargetDirETags, prevTargetFiles)
	if err != nil {
		e.failSync(syncJobID, fmt.Sprintf("Target file listing failed: %v", err))
		return
	}

	// Map target paths to source-side relative paths
	targetMap := make(map[string]fileState)
	for targetPath, file := range targetRawMap {
		relPath := getSourceRelPath(targetPath, job.TargetDir)
		file.Path = relPath
		targetMap[relPath] = file
	}

	// isFirstPass is true when no sync state exists yet (initial run).
	isFirstPass := len(prevStates) == 0

	// Wait for any tasks that may still be RUNNING from a previous pass before
	// clearing them. This prevents a race where we delete task rows that a worker
	// thread currently holds, causing silent counter drift.
	if err := e.drainRemainingTasks(ctx, job.ID); err != nil {
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
			if hasSrc && srcModified {
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

			// On the very first pass, files that exist on both sides have no
			// previous state.  Treating them as "both modified" would trigger a
			// spurious conflict on every existing file. Instead we treat them as
			// already in-sync so only genuinely new/changed files are acted upon.
			if isFirstPass && hasSrc && hasTgt {
				// Both exist, no prior baseline — record state only, no task.
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
			} else if hasSrc && srcModified && (!hasTgt || !tgtModified) {
				// Modified on source only (upload to target)
				tasks = append(tasks, taskToCreate{
					filePath:     S,
					fileSize:     srcFile.Size,
					sourceHash:   srcFile.Hash,
					resourceType: "files",
					action:       "upload",
				})
			} else if hasTgt && tgtModified && (!hasSrc || !srcModified) {
				// Modified on target only (download back to source)
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

	totalCreatedTasks := len(renameTasks) + len(tasks)
	log.Printf("[SyncEngine] Job %s: calculated %d tasks to run\n", syncJobID, totalCreatedTasks)

	if totalCreatedTasks == 0 {
		// No transfers needed: update stats immediately and complete run
		_ = db.UpdateSyncJobRunStats(e.db, job.ID, "SUCCESS", nil, 0, 0, 0, 0, 0)
		e.updateSyncStates(job.ID, sourceMap, targetMap, sourceDirETags, targetDirETags, nil)
		_ = db.UpdateSyncJobStatus(e.db, job.ID, "IDLE", nil)
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
	// Wake idle worker threads immediately so they start processing without
	// waiting for their next fallback poll cycle.
	e.queue.NotifyTaskAvailable(ctx, e.db)

	// Update totals
	_ = db.UpdateSyncJobTotals(e.db, job.ID, totalCreatedTasks, totalBytes)

	// Transition to RUNNING
	err = db.UpdateSyncJobStatus(e.db, job.ID, "RUNNING", nil)
	if err != nil {
		log.Printf("[SyncEngine] Failed to set RUNNING status for job %s: %v\n", syncJobID, err)
		return
	}

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
	log.Printf("[SyncEngine] All tasks finished for job %s. Writing outcomes...\n", syncJobID)

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

	// Update persistent states
	e.updateSyncStates(job.ID, sourceMap, targetMap, sourceDirETags, targetDirETags, taskOutcomes)

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

	// Persist run stats and set overall status to IDLE (waiting for next run)
	_ = db.UpdateSyncJobRunStats(e.db, job.ID, finalRunStatus, finalErr, total, completed+skipped, changedCount, deletedCount, failed)
	_ = db.UpdateSyncJobStatus(e.db, job.ID, "IDLE", nil)

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
	_ = db.UpdateSyncJobRunStats(e.db, id, "FAILED", &errMsg, 0, 0, 0, 0, 0)
	_ = db.UpdateSyncJobStatus(e.db, id, "IDLE", &errMsg)
}

// updateSyncStates aligns sync_state entries with current listings, preserving the old states of failed files.
// Uses BulkUpsertSyncStates to batch all upserts and deletes into a single transaction instead of N individual
// round-trips (one per file), which is dramatically faster for large directory trees.
func (e *Engine) updateSyncStates(jobID string, sourceMap, targetMap map[string]fileState, sourceDirETags, targetDirETags map[string]string, taskOutcomes map[string]string) {
	allKeys := make(map[string]bool)
	for k := range sourceMap {
		allKeys[k] = true
	}
	for k := range targetMap {
		allKeys[k] = true
	}

	var upserts []*db.SyncState
	var deletes []struct{ SyncJobID, Side, RelPath string }

	for S := range allKeys {
		srcFile, hasSrc := sourceMap[S]
		tgtFile, hasTgt := targetMap[S]
		outcome, hasTask := taskOutcomes[S]

		// If a task ran for this file, and it FAILED, do NOT update states (so it gets retried)
		if hasTask && outcome != "COMPLETED" && outcome != "SKIPPED" {
			continue
		}

		// Source side
		if hasSrc {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "source",
				RelPath:    S,
				Size:       srcFile.Size,
				Mtime:      sql.NullTime{Time: srcFile.LastModified, Valid: !srcFile.LastModified.IsZero()},
				SourceHash: srcFile.Hash,
				TargetHash: srcFile.Hash,
				ETag:       srcFile.ETag,
			})
		} else {
			deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "source", S})
		}

		// Target side
		if hasTgt {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "target",
				RelPath:    S,
				Size:       tgtFile.Size,
				Mtime:      sql.NullTime{Time: tgtFile.LastModified, Valid: !tgtFile.LastModified.IsZero()},
				SourceHash: tgtFile.Hash,
				TargetHash: tgtFile.Hash,
				ETag:       tgtFile.ETag,
			})
		} else {
			deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "target", S})
		}
	}

	// Persist directory ETags with Size: -1
	for dirPath, etag := range sourceDirETags {
		if etag != "" {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "source",
				RelPath:   dirPath,
				Size:      -1,
				ETag:      etag,
			})
		}
	}
	for dirPath, etag := range targetDirETags {
		if etag != "" {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "target",
				RelPath:   dirPath,
				Size:      -1,
				ETag:      etag,
			})
		}
	}

	if err := db.BulkUpsertSyncStates(e.db, upserts, deletes); err != nil {
		log.Printf("[SyncEngine] Warning: BulkUpsertSyncStates for job %s failed: %v\n", jobID, err)
	}
}

// listFiles traverses paths recursively using a parallel worker pool and hierarchical ETag folder skipping.
func (e *Engine) listFiles(
	ctx context.Context,
	client storage.StorageProvider,
	startPaths []string,
	prevDirETags map[string]string,
	prevFileStates map[string]fileState,
) (map[string]fileState, map[string]string, []db.IndexingErrorInput, error) {
	fileMap := make(map[string]fileState)
	dirETagMap := make(map[string]string)
	var indexErrors []db.IndexingErrorInput

	var mu sync.Mutex
	var errsMu sync.Mutex

	addFile := func(fs fileState) {
		mu.Lock()
		fileMap[fs.Path] = fs
		mu.Unlock()
	}

	addDirETag := func(dirPath, etag string) {
		if etag == "" {
			return
		}
		mu.Lock()
		dirETagMap[dirPath] = etag
		mu.Unlock()
	}

	addError := func(path, msg string) {
		errsMu.Lock()
		indexErrors = append(indexErrors, db.IndexingErrorInput{
			Path:         path,
			ResourceType: "files",
			ErrorMessage: msg,
		})
		errsMu.Unlock()
	}

	skipSubtree := func(dirPath string) {
		prefix := dirPath
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		mu.Lock()
		defer mu.Unlock()
		for fp, fs := range prevFileStates {
			if fp == dirPath || strings.HasPrefix(fp, prefix) {
				fileMap[fp] = fs
			}
		}
		for dp, etag := range prevDirETags {
			if dp == dirPath || strings.HasPrefix(dp, prefix) {
				dirETagMap[dp] = etag
			}
		}
	}

	type listJob struct {
		dirPath string
		etag    string
	}

	jobsChan := make(chan listJob, 100000)
	var wg sync.WaitGroup
	visited := make(map[string]bool)
	var visitedMu sync.Mutex

	enqueueDir := func(dirPath, etag string) {
		visitedMu.Lock()
		if visited[dirPath] {
			visitedMu.Unlock()
			return
		}
		visited[dirPath] = true
		visitedMu.Unlock()

		if etag != "" && prevDirETags[dirPath] != "" && etag == prevDirETags[dirPath] {
			log.Printf("[SyncEngine] Hierarchical ETag match for directory %s: skipping subtree scan\n", dirPath)
			skipSubtree(dirPath)
			return
		}

		wg.Add(1)
		jobsChan <- listJob{dirPath: dirPath, etag: etag}
	}

	for _, startPath := range startPaths {
		if startPath == "" {
			continue
		}
		res, err := client.InspectResource(ctx, "files", startPath)
		if err != nil {
			addError(startPath, err.Error())
			continue
		}

		if !res.IsDir {
			addFile(fileState{
				Path:         startPath,
				Size:         res.Size,
				LastModified: res.LastModified,
				Hash:         res.Hash,
				ETag:         res.ETag,
			})
			continue
		}

		addDirETag(startPath, res.ETag)
		enqueueDir(startPath, res.ETag)
	}

	type dirETagItem struct {
		path string
		etag string
	}

	numWorkers := 16
	for i := 0; i < numWorkers; i++ {
		go func() {
			workerFiles := make(map[string]fileState)
			workerDirETags := make(map[string]string)

			for job := range jobsChan {
				func() {
					defer wg.Done()

					if ctx.Err() != nil {
						return
					}

					files, err := client.GetDirectoryListing(ctx, "files", job.dirPath)
					if err != nil {
						addError(job.dirPath, err.Error())
						return
					}

					for _, file := range files {
						if file.IsDir {
							if file.ETag != "" {
								workerDirETags[file.Path] = file.ETag
							}
							enqueueDir(file.Path, file.ETag)
						} else {
							workerFiles[file.Path] = fileState{
								Path:         file.Path,
								Size:         file.Size,
								LastModified: file.LastModified,
								Hash:         file.Hash,
								ETag:         file.ETag,
							}
						}
					}
				}()
			}

			if len(workerFiles) > 0 || len(workerDirETags) > 0 {
				mu.Lock()
				for k, v := range workerFiles {
					fileMap[k] = v
				}
				for k, v := range workerDirETags {
					dirETagMap[k] = v
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	close(jobsChan)

	return fileMap, dirETagMap, indexErrors, nil
}

// isFileModified determines whether a file has changed compared to its stored SyncState.
func isFileModified(curr fileState, prev db.SyncState, isSource bool) bool {
	// Size mismatch -> modified
	if curr.Size != prev.Size {
		return true
	}

	prevHash := prev.SourceHash
	if !isSource {
		prevHash = prev.TargetHash
	}

	// If hashes exist on both current file and previous state -> compare clean hashes
	if curr.Hash != "" && prevHash != "" {
		_, cleanCurr := storage.ParseHashString(curr.Hash)
		_, cleanPrev := storage.ParseHashString(prevHash)
		if cleanCurr != "" && cleanPrev != "" {
			return cleanCurr != cleanPrev
		}
	}

	// If ETags exist on current file and previous state -> compare ETags
	if curr.ETag != "" && prev.ETag != "" {
		cleanCurrETag := strings.Trim(curr.ETag, `"`)
		cleanPrevETag := strings.Trim(prev.ETag, `"`)
		if cleanCurrETag != "" && cleanPrevETag != "" {
			return cleanCurrETag != cleanPrevETag
		}
	}

	// Timestamp comparison with 2-second tolerance (ignores sub-second nanoseconds / FAT32 2s precision)
	if !curr.LastModified.IsZero() && prev.Mtime.Valid {
		diff := curr.LastModified.Sub(prev.Mtime.Time)
		if diff < 0 {
			diff = -diff
		}
		if diff >= 2*time.Second {
			return true
		}
	}

	return false
}

// isFileMatchingTarget determines whether a source file and a target file are identical in content/metadata.
func isFileMatchingTarget(src, tgt fileState) bool {
	// Size mismatch -> not matching
	if src.Size != tgt.Size {
		return false
	}

	// If hashes exist on both source and target -> compare clean hashes
	if src.Hash != "" && tgt.Hash != "" {
		_, cleanSrc := storage.ParseHashString(src.Hash)
		_, cleanTgt := storage.ParseHashString(tgt.Hash)
		if cleanSrc != "" && cleanTgt != "" {
			return cleanSrc == cleanTgt
		}
	}

	// If ETags exist on both source and target -> compare clean ETags
	if src.ETag != "" && tgt.ETag != "" {
		cleanSrcETag := strings.Trim(src.ETag, `"`)
		cleanTgtETag := strings.Trim(tgt.ETag, `"`)
		if cleanSrcETag != "" && cleanTgtETag != "" {
			return cleanSrcETag == cleanTgtETag
		}
	}

	// Timestamp comparison with 2-second tolerance if timestamps are valid on both
	if !src.LastModified.IsZero() && !tgt.LastModified.IsZero() {
		diff := src.LastModified.Sub(tgt.LastModified)
		if diff < 0 {
			diff = -diff
		}
		if diff >= 2*time.Second {
			return false
		}
	}

	return true
}

// conflictNeedsRename reports whether a two-way conflict with the given strategy
// must rename the target copy before uploading the source version.
func conflictNeedsRename(strategy string) bool {
	return strategy == "RENAME"
}

// getSourceRelPath maps a target path back to its source-side relative path by stripping the target dir prefix.
func getSourceRelPath(targetPath, targetDir string) string {
	targetPath = path.Clean("/" + targetPath)
	targetDir = path.Clean("/" + targetDir)

	if targetDir == "/" {
		return targetPath
	}

	prefix := targetDir + "/"
	if strings.HasPrefix(targetPath, prefix) {
		return "/" + targetPath[len(prefix):]
	}
	if targetPath == targetDir {
		return "/"
	}
	return targetPath
}

// shouldRefreshToken reports whether the stored OAuth token should be rotated
// before use. It refreshes only when an expiry is known and the token is within
// 2 minutes of expiry (or already expired). A missing expiry is treated as
// "do not refresh" to preserve the pre-existing behaviour.
func shouldRefreshToken(expiry sql.NullTime) bool {
	return expiry.Valid && !time.Now().Before(expiry.Time.Add(-2*time.Minute))
}

// ensureFreshToken refreshes OAuth credentials for a sync job if they are expired or near expiry.
func (e *Engine) ensureFreshToken(syncJobID string, job *db.SyncJob, role string, currentToken string) (string, error) {
	var expiry sql.NullTime
	var provider, refreshTokenEnc string

	if role == "source" {
		expiry = job.SourceTokenExpiresAt
		provider = job.SourceProvider
		refreshTokenEnc = job.SourceRefreshTokenEncrypted.String
	} else {
		expiry = job.TargetTokenExpiresAt
		provider = job.TargetProvider
		refreshTokenEnc = job.TargetRefreshTokenEncrypted.String
	}

	if !shouldRefreshToken(expiry) {
		return currentToken, nil
	}

	refreshToken, err := crypto.Decrypt(refreshTokenEnc, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	tokenResp, err := oauth.RefreshToken(context.Background(), provider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for %s (%s): %w", role, provider, err)
	}

	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new access token: %w", err)
	}

	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new refresh token: %w", err)
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// Overwrite tokens in database
	var query string
	if role == "source" {
		query = `
			UPDATE sync_jobs
			SET source_password_encrypted = $1,
			    source_refresh_token_encrypted = $2,
			    source_token_expires_at = $3,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = $4
		`
	} else {
		query = `
			UPDATE sync_jobs
			SET target_password_encrypted = $1,
			    target_refresh_token_encrypted = $2,
			    target_token_expires_at = $3,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = $4
		`
	}

	_, err = e.db.Exec(query, newAccessEnc, newRefreshEnc, newExpiresAt, syncJobID)
	if err != nil {
		return "", fmt.Errorf("failed to persist refreshed tokens: %w", err)
	}

	return tokenResp.AccessToken, nil
}
