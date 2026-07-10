package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/storage"
)

type activeTaskInfo struct {
	migrationID string
	cancel      context.CancelFunc
}

type Processor struct {
	db         *sql.DB
	queue      *queue.Queue
	workerID   string
	secretKey  string
	maxThreads int
	activeTasks sync.Map
}

func NewProcessor(database *sql.DB, q *queue.Queue, workerID string, secretKey string) *Processor {
	// Default to a conservative worker pool (4) as documented in AGENTS.md.
	// The actual concurrency per migration is limited by the m.threads setting in the database during DequeueSQL.
	maxThreads := 4
	if envVal := os.Getenv("MAX_THREADS"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
			maxThreads = val
		}
	}

	return &Processor{
		db:         database,
		queue:      q,
		workerID:   workerID,
		secretKey:  secretKey,
		maxThreads: maxThreads,
	}
}

// Start runs the worker dequeue loop and background schedulers
func (p *Processor) Start(ctx context.Context) {
	fmt.Printf("[Worker %s] Started and waiting for tasks with max %d threads...\n", p.workerID, p.maxThreads)

	// Recover any abandoned tasks on startup
	if err := p.queue.RecoverAbandonedTasks(ctx, p.db, p.workerID); err != nil {
		fmt.Printf("[Worker %s] Error recovering abandoned tasks: %v\n", p.workerID, err)
	}

	// Spawn background schedulers
	go p.RunWorkerLiveness(ctx)
	go p.RunRetryScheduler(ctx)
	go p.RunConnectionRecoveryScheduler(ctx)
	go p.RunOrphanedRunningTasksRecovery(ctx)

	// Start Cancel Listener
	go p.queue.SubscribeToCancelEvents(ctx, func(migrationID string) {
		fmt.Printf("[Worker %s] Received Cancel Event for Migration: %s\n", p.workerID, migrationID)
		p.activeTasks.Range(func(key, value interface{}) bool {
			info, ok := value.(activeTaskInfo)
			if ok && info.migrationID == migrationID {
				fmt.Printf("[Worker %s] Cancelling active stream for task: %s\n", p.workerID, key)
				info.cancel()
			}
			return true
		})
	})

	var wg sync.WaitGroup
	for i := 0; i < p.maxThreads; i++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Dequeue task from PostgreSQL
					payload, err := p.queue.DequeueSQL(ctx, p.db, p.workerID)
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						fmt.Printf("[Worker %s] Thread %d dequeue error: %v. Sleeping...\n", p.workerID, threadID, err)
						time.Sleep(2 * time.Second)
						continue
					}

					if payload == nil {
						time.Sleep(2 * time.Second) // Sleep to avoid busy loop
						continue                    // No task in queue
					}

					fmt.Printf("[Worker %s] Thread %d processing task %s for migration %s\n", p.workerID, threadID, payload.TaskID, payload.MigrationID)

					err = p.processTask(ctx, payload)
					if err != nil {
						fmt.Printf("[Worker %s] Thread %d error processing task %s: %v\n", p.workerID, threadID, payload.TaskID, err)
						p.handleTaskFailure(ctx, payload, err)
					} else {
						fmt.Printf("[Worker %s] Thread %d successfully processed task %s\n", p.workerID, threadID, payload.TaskID)
					}
				}
			}
		}(i)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	fmt.Printf("[Worker %s] Shutdown signal received. Waiting for active tasks to finish...\n", p.workerID)
	wg.Wait()
	fmt.Printf("[Worker %s] Worker loop stopped.\n", p.workerID)
}

// RunWorkerLiveness periodically registers this worker as active and recovers abandoned tasks
func (p *Processor) RunWorkerLiveness(ctx context.Context) {
	// Register immediately with a 60s TTL — generous enough to survive a brief Redis hiccup
	// without the liveness key expiring between heartbeat ticks (tick=10s, TTL=60s → 6 chances).
	_ = p.queue.RegisterActiveWorker(ctx, p.workerID, 60*time.Second)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := p.queue.RegisterActiveWorker(ctx, p.workerID, 60*time.Second)
			if err != nil {
				fmt.Printf("[Liveness] Error registering active worker: %v\n", err)
			}
		case <-cleanupTicker.C:
			deadWorkers, err := p.queue.GetAbandonedWorkerQueues(ctx, p.db)
			if err != nil {
				fmt.Printf("[Liveness] Error scanning for dead workers: %v\n", err)
				continue
			}
			for _, deadWorkerID := range deadWorkers {
				if deadWorkerID == p.workerID {
					continue
				}
				// Claim a distributed recovery lock (Redis SETNX) before touching the dead
				// worker's queue. This prevents two processor instances from simultaneously
				// recovering the same worker and enqueuing tasks twice.
				claimed, lockErr := p.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 60*time.Second)
				if lockErr != nil || !claimed {
					continue // Another instance is already handling recovery for this worker
				}
				fmt.Printf("[Liveness] Found abandoned queue for worker %s, recovering tasks...\n", deadWorkerID)
				if err := p.queue.RecoverAbandonedTasks(ctx, p.db, deadWorkerID); err != nil {
					fmt.Printf("[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
				}
			}
		}
	}
}

// RunRetryScheduler runs a ticker to scan the DB for tasks waiting for retry
func (p *Processor) RunRetryScheduler(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.requeueFailedTasks(ctx)
		}
	}
}

func (p *Processor) requeueFailedTasks(ctx context.Context) {
	query := `
		SELECT t.id, t.migration_id
		FROM tasks t
		JOIN migrations m ON t.migration_id = m.id
		WHERE t.status = 'FAILED' 
		  AND t.next_retry_at <= $1
		  AND m.status IN ('RUNNING', 'INDEXING')
	`
	rows, err := p.db.QueryContext(ctx, query, time.Now())
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var taskID, migrationID string
		if err := rows.Scan(&taskID, &migrationID); err != nil {
			continue
		}

		// Update status to PENDING
		updateQuery := `
			UPDATE tasks
			SET status = 'PENDING', next_retry_at = NULL
			WHERE id = $1
		`
		_, err := p.db.ExecContext(ctx, updateQuery, taskID)
		if err != nil {
			continue
		}

		fmt.Printf("[RetryScheduler] Re-enqueued task %s for migration %s\n", taskID, migrationID)
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("[RetryScheduler] rows error: %v\n", err)
	}
}

// RunOrphanedRunningTasksRecovery detects RUNNING tasks that are stuck (e.g. due to a worker
// crashing or a thread dying silently) and resets them back to PENDING. It waits 5 minutes
// after startup before the first scan.
func (p *Processor) RunOrphanedRunningTasksRecovery(ctx context.Context) {
	// Delay first check.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.requeueOrphanedRunningTasks(ctx)
		}
	}
}

// requeueOrphanedRunningTasks scans for RUNNING tasks belonging to active migrations whose
// updated_at is older than 10 minutes (i.e. they were picked up but never finished).
func (p *Processor) requeueOrphanedRunningTasks(ctx context.Context) {
	query := `
		SELECT t.id, t.migration_id
		FROM tasks t
		JOIN migrations m ON t.migration_id = m.id
		WHERE t.status = 'RUNNING'
		  AND t.updated_at < NOW() - INTERVAL '10 minutes'
		  AND m.status IN ('RUNNING', 'INDEXING')
	`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		fmt.Printf("[OrphanedTaskRecovery] DB query error: %v\n", err)
		return
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var taskID, migrationID string
		if err := rows.Scan(&taskID, &migrationID); err != nil {
			continue
		}
		_, err := p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL, updated_at=NOW() WHERE id=$1 AND status='RUNNING'", taskID)
		if err != nil {
			fmt.Printf("[OrphanedTaskRecovery] Error resetting task %s: %v\n", taskID, err)
		} else {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("[OrphanedTaskRecovery] rows error: %v\n", err)
	}
	if count > 0 {
		fmt.Printf("[OrphanedTaskRecovery] Re-enqueued %d orphaned RUNNING tasks\n", count)
	}
}

// RunConnectionRecoveryScheduler checks paused migrations to test if servers are back online
func (p *Processor) RunConnectionRecoveryScheduler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second) // Check every 60s for connection recovery
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.recoverPausedMigrations(ctx)
		}
	}
}

func (p *Processor) recoverPausedMigrations(ctx context.Context) {
	query := `
		SELECT id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
	`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, sURL, sUser, sPassEnc, tURL, tUser, tPassEnc, sProv, tProv string
		if err := rows.Scan(&id, &sURL, &sUser, &sPassEnc, &tURL, &tUser, &tPassEnc, &sProv, &tProv); err != nil {
			continue
		}

		sPass, err := crypto.Decrypt(sPassEnc, p.secretKey)
		if err != nil {
			continue
		}
		tPass, err := crypto.Decrypt(tPassEnc, p.secretKey)
		if err != nil {
			continue
		}

		sClient, err := storage.NewProvider(ctx, sProv, sURL, sUser, sPass)
		if err != nil {
			continue
		}
		tClient, err := storage.NewProvider(ctx, tProv, tURL, tUser, tPass)
		if err != nil {
			sClient.Close()
			continue
		}

		connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
		sOK, _ := sClient.Connect(connCtx)
		tOK, _ := tClient.Connect(connCtx)
		connCancel()
		sClient.Close()
		tClient.Close()

		if sOK && tOK {
			fmt.Printf("[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", id)
			updateQuery := `
				UPDATE migrations
				SET status = 'RUNNING'
				WHERE id = $1
			`
			_, err = p.db.ExecContext(ctx, updateQuery, id)
			if err != nil {
				fmt.Printf("[RecoveryScheduler] Error resuming migration %s: %v\n", id, err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("[RecoveryScheduler] rows error: %v\n", err)
	}
}

func (p *Processor) processTask(ctx context.Context, payload *queue.Payload) (err error) {
	// Shadow ctx with a cancelable one
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.activeTasks.Store(payload.TaskID, activeTaskInfo{
		migrationID: payload.MigrationID,
		cancel:      cancel,
	})
	defer p.activeTasks.Delete(payload.TaskID)

	// 1. Fetch Migration from DB
	mig, err := db.GetMigration(p.db, payload.MigrationID)
	if err != nil {
		return fmt.Errorf("failed to fetch migration: %w", err)
	}

	// If migration is paused or in connection loss, return nil (task stays in RUNNING, but we want it PENDING)
	// Actually, DequeueSQL only picks PENDING. If migration is paused, DequeueSQL won't pick it!
	// But just in case status changed right after dequeue:
	if mig.Status == "PAUSED_CONNECTION_LOSS" || mig.Status == "PAUSED" {
		// Set back to pending
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL WHERE id=$1", payload.TaskID)
		time.Sleep(2 * time.Second)
		return nil
	}

	// If migration is in a terminal state (COMPLETED or FAILED), mark task as skipped/failed
	if mig.Status == "COMPLETED" || mig.Status == "FAILED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='SKIPPED', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return nil
	}

	// If migration was cancelled, mark the task cancelled and stop
	if mig.Status == "CANCELLED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='CANCELLED', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return nil
	}

	// If migration is in any other non-running state, requeue and return error
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return fmt.Errorf("migration is in state %s, task skipped for now", mig.Status)
	}

	// 2. Fetch Task from DB
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}

	// Decrypt credentials
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}
	targetPass, err := crypto.Decrypt(mig.TargetPasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}

	// For OAuth providers: if the token is expired or within 2 minutes of expiry,
	// refresh it now so this task does not hit a 401. The daemon already handles
	// proactive rotation every 5 min, but tasks could be dequeued right as a token
	// expires, so we have this last-resort inline refresh.
	sourcePass, err = p.ensureFreshOAuthToken(ctx, mig, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}
	targetPass, err = p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}

	// Create storage providers
	sourceClient, err := storage.NewProvider(ctx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		return fmt.Errorf("failed to create source client: %w", err)
	}
	defer sourceClient.Close()
	targetClient, err := storage.NewProvider(ctx, mig.TargetProvider, mig.TargetURL, mig.TargetUsername, targetPass)
	if err != nil {
		return fmt.Errorf("failed to create target client: %w", err)
	}
	defer targetClient.Close()

	if nc, ok := sourceClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}
	if nc, ok := targetClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}

	// Update task status to RUNNING in DB
	task.Status = "RUNNING"
	_ = db.UpdateTaskStatus(p.db, task)

	// 3. Conflict Resolution
	var deleteAfterUpload bool // set true by OVERWRITE: delete original only after upload succeeds
	targetPath := task.FilePath
	if task.ResourceType == "files" {
		// Use path (POSIX) rather than filepath: WebDAV/Nextcloud paths are always
		// slash-separated, independent of the OS this server process runs on.
		targetPath = path.Clean(path.Join(mig.TargetDir, task.FilePath))
	}
	exists, _, err := targetClient.FileExists(ctx, task.ResourceType, targetPath)
	if err != nil {
		return fmt.Errorf("failed to check if target file exists: %w", err)
	}

	if exists {
		// Calendars and contacts are always overwritten: they are dynamic data and
		// a SKIP would silently leave stale entries from a previous failed run.
		if task.ResourceType != "files" {
			err = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
			if err != nil {
				return fmt.Errorf("failed to delete existing calendar/contact entry for overwrite: %w", err)
			}
		} else {
			switch mig.ConflictStrategy {
			case "SKIP":
				task.Status = "SKIPPED"
				task.ErrorMessage = sql.NullString{String: "File already exists in target (SKIP)", Valid: true}
				_ = db.UpdateTaskStatus(p.db, task)
				_ = db.IncrementMigrationProgress(p.db, mig.ID, 1, task.FileSize, 1, 0)
				return nil

			case "OVERWRITE":
				// Do NOT delete before upload — if upload fails, the original would be lost.
				// Instead, mark that we should delete after a successful upload.
				deleteAfterUpload = true

			case "RENAME":
				// Generate new target name
				dir := path.Dir(targetPath)
				ext := path.Ext(targetPath)
				base := strings.TrimSuffix(path.Base(targetPath), ext)

				counter := 1
				for {
					candidatePath := path.Join(dir, fmt.Sprintf("%s_copy%d%s", base, counter, ext))
					candidateExists, _, err := targetClient.FileExists(ctx, task.ResourceType, candidatePath)
					if err != nil {
						return fmt.Errorf("failed to check existence of rename candidate: %w", err)
					}
					if !candidateExists {
						targetPath = candidatePath
						task.FilePath = targetPath
						break
					}
					counter++
					if counter > 100 {
						return fmt.Errorf("failed to rename target file after 100 attempts")
					}
				}
			}
		}
	}

	// 4. Download and Upload stream
	uploadPath := targetPath
	if deleteAfterUpload {
		uploadPath = targetPath + ".tmp"
	}

	// Apply a dynamic per-request timeout scaled by file size (same policy as uploads)
	downloadTimeout := 5 * time.Minute
	if task.FileSize > 0 {
		downloadTimeout += time.Duration(task.FileSize/(50*1024*1024)) * time.Minute
	}
	if downloadTimeout > 12*time.Hour {
		downloadTimeout = 12 * time.Hour
	}
	downloadCtx, downloadCancel := context.WithTimeout(ctx, downloadTimeout)
	defer downloadCancel()

	downloadStream, err := sourceClient.StreamDownload(downloadCtx, task.ResourceType, task.FilePath)
	if err != nil {
		return fmt.Errorf("failed to download from source: %w", err)
	}
	defer downloadStream.Close()

	// Handle Hash Algorithm Selection
	var sourceHasher hash.Hash
	sourceAlgo := "SHA1" // Default
	sourceHashStr := ""

	if task.SourceHash.Valid && task.SourceHash.String != "" && mig.SourceProvider != "webdav" {
		algo, cleanHash := storage.ParseHashString(task.SourceHash.String)
		sourceHashStr = cleanHash
		sourceAlgo = algo
	} else {
		// Fallback to fetch hash directly
		if mig.SourceProvider != "webdav" {
			if fetchedHash, err := sourceClient.GetFileHash(ctx, task.ResourceType, task.FilePath); err == nil {
				task.SourceHash = sql.NullString{String: fetchedHash, Valid: true}
				algo, cleanHash := storage.ParseHashString(fetchedHash)
				sourceHashStr = cleanHash
				sourceAlgo = algo
			}
		}
	}

	if mig.SourceProvider == "dropbox" {
		sourceAlgo = "DROPBOX"
	}

	// Instantiate source hasher
	if sourceAlgo == "MD5" {
		sourceHasher = md5.New()
	} else if sourceAlgo == "DROPBOX" {
		sourceHasher = storage.NewDropboxHasher()
	} else if sourceAlgo == "SHA256" {
		sourceHasher = sha256.New()
	} else {
		sourceHasher = sha1.New()
		sourceAlgo = "SHA1"
	}

	// Determine target hasher algorithm
	var targetHasher hash.Hash
	targetAlgo := "SHA1" // Default
	if mig.TargetProvider == "dropbox" {
		targetAlgo = "DROPBOX"
		targetHasher = storage.NewDropboxHasher()
	} else {
		targetAlgo = "SHA1"
		targetHasher = sha1.New()
	}

	// We only need two hashers if the algorithms differ
	var activeWriter io.Writer
	if sourceAlgo == targetAlgo {
		activeWriter = sourceHasher
		targetHasher = nil // Disable target hasher to save CPU cycles
	} else {
		activeWriter = io.MultiWriter(sourceHasher, targetHasher)
	}

	// Setup progress notification channel
	progressChan := make(chan int64, 10)
	progressDone := make(chan struct{})
	var totalBytesReported int64

	go func() {
		defer close(progressDone)
		// This goroutine updates progress of migration in the DB
		// Buffer progress updates to reduce database load
		var bufferedBytes int64
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		lastHeartbeat := time.Now()
		var bytesSinceLastHeartbeat int64

		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					// Final flush
					if bufferedBytes > 0 {
						_ = db.IncrementMigrationProgress(p.db, mig.ID, 0, bufferedBytes, 0, 0)
						totalBytesReported += bufferedBytes
					}
					return
				}
				bufferedBytes += bytes
				bytesSinceLastHeartbeat += bytes
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.IncrementMigrationProgress(p.db, mig.ID, 0, bufferedBytes, 0, 0)
					totalBytesReported += bufferedBytes
					bufferedBytes = 0
				}
				// Heartbeat updated_at for this task to avoid triggering orphan recovery
				// Only heartbeat if progress was made (data was actively moving)
				if time.Since(lastHeartbeat) >= 30*time.Second {
					if bytesSinceLastHeartbeat > 0 {
						_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET updated_at = NOW() WHERE id = $1 AND status = 'RUNNING'", task.ID)
						bytesSinceLastHeartbeat = 0
					}
					lastHeartbeat = time.Now()
				}
			}
		}
	}()

	// Defer cleanup of progress channel and progress rollback on failure (Nitpick fix)
	defer func() {
		close(progressChan)
		<-progressDone
		if err != nil {
			// Rollback progress reported to DB during this failed run
			if totalBytesReported > 0 {
				_ = db.IncrementMigrationProgress(p.db, mig.ID, 0, -totalBytesReported, 0, 0)
			}
		}
	}()

	// io.TeeReader writes all data read from the download stream to the hasher in-memory
	hashingReader := io.TeeReader(downloadStream, activeWriter)

	// Perform Upload (Zero Data Retention - streamed through RAM buffer)
	// Apply a dynamic per-request timeout scaled by file size (same policy as downloads)
	uploadTimeout := 5 * time.Minute
	if task.FileSize > 0 {
		uploadTimeout += time.Duration(task.FileSize/(50*1024*1024)) * time.Minute
	}
	if uploadTimeout > 12*time.Hour {
		uploadTimeout = 12 * time.Hour
	}
	uploadCtx, uploadCancel := context.WithTimeout(ctx, uploadTimeout)
	defer uploadCancel()

	// If size > 50MB, do chunked upload
	if task.FileSize > 50*1024*1024 {
		err = targetClient.StreamUploadChunked(uploadCtx, task.ResourceType, uploadPath, hashingReader, task.FileSize, progressChan)
	} else {
		// Simple upload
		// Wrap with a progress reporting reader
		progressReader := &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
		err = targetClient.StreamUpload(uploadCtx, task.ResourceType, uploadPath, progressReader, task.FileSize)
	}

	if err != nil {
		if errors.Is(err, storage.ErrDuplicateUID) {
			task.Status = "SKIPPED"
			task.ErrorMessage = sql.NullString{String: "Sabredav: Calendar event UID already exists (SKIP)", Valid: true}
			_ = db.UpdateTaskStatus(p.db, task)
			_ = db.IncrementMigrationProgress(p.db, mig.ID, 1, task.FileSize, 1, 0)
			return nil
		}
		return fmt.Errorf("upload to target failed: %w", err)
	}

	// OVERWRITE: now that the upload succeeded, safely delete the original and rename the temp file.
	if deleteAfterUpload {
		// Attempt to delete original. Ignore not found error if it's already gone.
		_ = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
		if renameErr := targetClient.RenameFile(ctx, task.ResourceType, uploadPath, targetPath); renameErr != nil {
			return fmt.Errorf("failed to rename temp file to target path: %w", renameErr)
		}
	}

	// 5. Hash & Integrity Verification
	var integrityVerified bool
	downloadOK := true
	uploadOK := true

	if task.ResourceType == "files" {
		workerSourceHashVal := fmt.Sprintf("%x", sourceHasher.Sum(nil))
		task.WorkerHash = sql.NullString{String: fmt.Sprintf("%s:%s", sourceAlgo, workerSourceHashVal), Valid: true}

		if sourceHashStr != "" && sourceAlgo != "UNKNOWN" {
			downloadOK = (workerSourceHashVal == sourceHashStr)
		}

		var targetHashVal string
		var errTargetHash error
		if mig.TargetProvider != "webdav" {
			targetHashVal, errTargetHash = targetClient.GetFileHash(ctx, task.ResourceType, targetPath)
		} else {
			errTargetHash = fmt.Errorf("webdav target hash not supported")
		}

		if errTargetHash == nil {
			task.TargetHash = sql.NullString{String: targetHashVal, Valid: true}
			targetReturnedAlgo, cleanTargetHash := storage.ParseHashString(targetHashVal)
			if mig.TargetProvider == "dropbox" {
				targetReturnedAlgo = "DROPBOX"
			}

			var workerTargetHashVal string
			hasMatchingAlgo := false
			if sourceAlgo == targetReturnedAlgo && sourceAlgo != "UNKNOWN" {
				workerTargetHashVal = workerSourceHashVal
				hasMatchingAlgo = true
			} else if targetHasher != nil && targetAlgo == targetReturnedAlgo && targetAlgo != "UNKNOWN" {
				workerTargetHashVal = fmt.Sprintf("%x", targetHasher.Sum(nil))
				hasMatchingAlgo = true
			}

			if hasMatchingAlgo {
				uploadOK = (workerTargetHashVal == cleanTargetHash)
			} else {
				// Algorithm mismatch fallback: verify size
				existsOnTarget, targetSize, errExists := targetClient.FileExists(ctx, task.ResourceType, targetPath)
				if errExists == nil && existsOnTarget {
					if task.FileSize == 0 {
						uploadOK = true // Google Docs, Calendars, and Contacts have dynamic sizes
					} else {
						uploadOK = (task.FileSize == targetSize)
					}
					task.TargetHash = sql.NullString{String: fmt.Sprintf("SIZE:%d", targetSize), Valid: true}
				} else {
					uploadOK = false
				}
			}
		} else {
			// Fallback: Size verification
			existsOnTarget, targetSize, errExists := targetClient.FileExists(ctx, task.ResourceType, targetPath)
			if errExists == nil && existsOnTarget {
				if task.FileSize == 0 {
					uploadOK = true // Google Docs, Calendars, and Contacts have dynamic sizes
				} else {
					uploadOK = (task.FileSize == targetSize)
				}
				task.TargetHash = sql.NullString{String: fmt.Sprintf("SIZE:%d", targetSize), Valid: true}
			} else {
				uploadOK = false
			}
		}
	} else {
		// Non-files (calendars/contacts) have dynamic content that isn't verifyable via strict checksums or sizes
		// and were already successfully stored.
		task.WorkerHash = sql.NullString{String: "DYNAMIC", Valid: true}
		task.TargetHash = sql.NullString{String: "DYNAMIC", Valid: true}
	}

	integrityVerified = downloadOK && uploadOK
	if !integrityVerified {
		return fmt.Errorf("data integrity check failed: hashes or sizes did not match")
	}

	// Update task to COMPLETED
	task.Status = "COMPLETED"
	task.ErrorMessage = sql.NullString{}
	_ = db.UpdateTaskStatus(p.db, task)

	// Increment processed files count (processed bytes already incremented by progress channel)
	_ = db.IncrementMigrationProgress(p.db, mig.ID, 1, 0, 0, 0)

	return nil
}

func (p *Processor) handleTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	// 1. Fetch Task
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		fmt.Printf("Error fetching task on failure handler: %v\n", err)
		return
	}

	// Check if migration was manually cancelled
	mig, migErr := db.GetMigration(p.db, payload.MigrationID)
	if migErr == nil && mig.Status == "CANCELLED" {
		fmt.Printf("[Worker %s] Task %s aborted (Migration cancelled).\n", p.workerID, payload.TaskID)
		task.Status = "CANCELLED"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	// Check if context is cancelled (graceful shutdown)
	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		fmt.Printf("[Worker %s] Shutdown detected. Requeueing task %s...\n", p.workerID, payload.TaskID)

		task.Status = "PENDING"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: procErr.Error(), Valid: true}

	// Check if this error is a network connection loss
	isConnLoss := isNetworkError(procErr)

	if isConnLoss {
		fmt.Printf("[Worker %s] Connection loss detected: %v\n", p.workerID, procErr)
		// Pause the migration
		_ = db.UpdateMigrationStatus(p.db, payload.MigrationID, "PAUSED_CONNECTION_LOSS", nil)

		// Task is set back to PENDING so it can be retried immediately upon resume
		task.Status = "PENDING"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	// Check if error is permanent / non-retryable
	isPermanent := false
	errStr := procErr.Error()
	if strings.Contains(errStr, "exportSizeLimitExceeded") ||
		strings.Contains(errStr, "badRequest") ||
		strings.Contains(errStr, "conversion is not supported") ||
		strings.Contains(errStr, "fileNotDownloadable") ||
		strings.Contains(errStr, "Only files with binary content can be downloaded") ||
		strings.Contains(errStr, "too large to be exported") ||
		strings.Contains(errStr, "notFound") ||
		strings.Contains(errStr, "fileNotFound") {
		isPermanent = true
	}

	// Detect authentication errors that mean the stored credentials are invalid.
	// All provider methods (Connect and every transfer method) wrap HTTP 401
	// responses with storage.ErrAuth so errors.Is detects them via the error chain.
	// For Google (OAuth), the Google API client returns *googleapi.Error whose
	// message contains these distinctive strings — they do not appear in other paths.
	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials") ||
		strings.Contains(errStr, "invalid authentication credentials")

	if isAuthError {
		fmt.Printf("[Worker %s] Auth error detected for task %s (migration %s) — stopping migration immediately\n",
			p.workerID, payload.TaskID, payload.MigrationID)
		authErrMsg := fmt.Sprintf("Authentication failed — please check your credentials and start a new migration")
		_ = db.UpdateMigrationStatus(p.db, payload.MigrationID, "FAILED", &authErrMsg)
		// Mark this individual task failed too so progress counters stay accurate
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateTaskStatus(p.db, task)
		_ = db.IncrementMigrationProgress(p.db, task.MigrationID, 1, task.FileSize, 0, 1)
		return
	}

	// If it is a normal file transfer failure
	if task.Attempts < 3 && !isPermanent {
		// Exponential Backoff: 10s, 30s
		backoffSec := int(math.Pow(3, float64(task.Attempts-1))) * 10
		if backoffSec > 90 {
			backoffSec = 90
		}

		nextRetry := time.Now().Add(time.Duration(backoffSec) * time.Second)
		task.Status = "FAILED" // Kept as failed until cron schedules retry
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		_ = db.UpdateTaskStatus(p.db, task)

		fmt.Printf("[Worker %s] Task %s scheduled for retry in %ds (Attempt %d/3)\n", p.workerID, task.ID, backoffSec, task.Attempts)
	} else {
		// Max retries reached, fail permanently
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateTaskStatus(p.db, task)

		// Increment migration failed files
		_ = db.IncrementMigrationProgress(p.db, task.MigrationID, 1, task.FileSize, 0, 1)
		fmt.Printf("[Worker %s] Task %s failed permanently after %d attempts\n", p.workerID, task.ID, task.Attempts)
	}
}

// ProgressReader wraps io.Reader to notify bytes read
type ProgressReader struct {
	Reader       io.Reader
	ProgressChan chan<- int64
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 && pr.ProgressChan != nil {
		pr.ProgressChan <- int64(n)
	}
	return n, err
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Direct type assertions
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "handshake failure") ||
		strings.Contains(errStr, "http2: server sent goaway")
}

// ensureFreshOAuthToken checks whether a migration's OAuth access token is expired
// (or within 2 minutes of expiry) and, if so, performs an inline token refresh before
// the storage provider is constructed. The freshly decrypted access token is returned.
// For non-OAuth providers (no refresh token stored) the original accessToken is returned
// unchanged, making this a safe no-op for Nextcloud/WebDAV.
func (p *Processor) ensureFreshOAuthToken(ctx context.Context, mig *db.Migration, role string, accessToken string) (string, error) {
	var refreshTokenEnc sql.NullString
	var expiresAt sql.NullTime
	var provider string

	if role == "source" {
		refreshTokenEnc = mig.SourceRefreshTokenEncrypted
		expiresAt = mig.SourceTokenExpiresAt
		provider = mig.SourceProvider
	} else {
		refreshTokenEnc = mig.TargetRefreshTokenEncrypted
		expiresAt = mig.TargetTokenExpiresAt
		provider = mig.TargetProvider
	}

	// No refresh token stored → not an OAuth provider, nothing to do.
	if !refreshTokenEnc.Valid || refreshTokenEnc.String == "" {
		return accessToken, nil
	}

	// Token still valid with >2 min margin → use as-is.
	if expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	fmt.Printf("[Worker %s] %s OAuth token expired or near expiry for migration %s — refreshing inline\n",
		p.workerID, role, mig.ID)

	// Decrypt refresh token immediately before use (Zero Plaintext rule from PRD-12)
	refreshToken, err := crypto.Decrypt(refreshTokenEnc.String, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token: %w", role, err)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for %s (%s): %w", role, provider, err)
	}

	// Encrypt and persist the new token pair atomically (Token Rotation Constraint F-03).
	// Encryption failure is fatal: for single-use refresh tokens (e.g. Google) the old
	// refresh token has already been invalidated by the provider. Proceeding without
	// persisting the new one would leave the DB with a stale token and cause a permanent
	// auth failure on the next refresh attempt.
	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s access token after refresh: %w", role, err)
	}
	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s refresh token after refresh: %w", role, err)
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	if err := db.UpdateMigrationOAuthTokens(p.db, db.OAuthTokenUpdate{
		MigrationID:           mig.ID,
		Role:                  role,
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             time.Now().Add(time.Duration(expiresIn) * time.Second),
	}); err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

	return tokenResp.AccessToken, nil
}
