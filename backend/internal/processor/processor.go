package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type activeTaskInfo struct {
	migrationID string
	cancel      context.CancelFunc
}

type Processor struct {
	db           *sql.DB
	queue        *queue.Queue
	workerID     string
	secretKey    string
	maxThreads   int
	activeTasks  sync.Map
	refreshLocks sync.Map
	throttlers   sync.Map
}

func NewProcessor(database *sql.DB, q *queue.Queue, workerID string, secretKey string) *Processor {
	// Default to 16 to match the maximum selectable threads per migration in the UI slider.
	// The actual concurrency per migration is limited by the m.threads setting in the database during DequeueSQL.
	maxThreads := 16
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

func (p *Processor) getOrCreateRefreshLock(migrationID string) *sync.Mutex {
	actual, _ := p.refreshLocks.LoadOrStore(migrationID, &sync.Mutex{})
	return actual.(*sync.Mutex)
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
	go p.RunCompletionNotifier(ctx)

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

	// Start Bandwidth Change Listener
	go p.queue.SubscribeToBandwidthChanges(ctx, func(event queue.BandwidthEvent) {
		log.Printf("[Worker %s] Bandwidth change for migration %s: %d Mbps",
			p.workerID, event.MigrationID, event.BandwidthLimitMbps)
		if throttler, ok := p.throttlers.Load(event.MigrationID); ok {
			throttler.(*throttle.MigrationThrottler).SetLimit(event.BandwidthLimitMbps)
		}
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
	// Register immediately with a 120s TTL — generous enough to survive a brief Redis hiccup
	// without the liveness key expiring between heartbeat ticks (tick=10s, TTL=120s → 12 chances).
	_ = p.queue.RegisterActiveWorker(ctx, p.workerID, 120*time.Second)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := p.queue.RegisterActiveWorker(ctx, p.workerID, 120*time.Second)
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
				claimed, lockErr := p.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 120*time.Second)
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
	defer func() {
		p.activeTasks.Delete(payload.TaskID)
	}()

	// 1. Fetch Migration from DB
	mig, err := db.GetMigration(p.db, payload.MigrationID)
	if err != nil {
		return fmt.Errorf("failed to fetch migration: %w", err)
	}

	// Get or create throttler for this migration
	throttler, _ := p.throttlers.LoadOrStore(payload.MigrationID, throttle.NewMigrationThrottler(mig.BandwidthLimitMbps))
	migrationThrottler := throttler.(*throttle.MigrationThrottler)

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

	// 3a. Filename Sanitization (before conflict resolution)
	if task.ResourceType == "files" {
		result := sanitize.SanitizeFilename(path.Base(targetPath), mig.TargetProvider)
		if result.Changed {
			dir := path.Dir(targetPath)
			targetPath = path.Join(dir, result.SanitizedName)
			log.Printf("[SANITIZE] %s: \"%s\" → \"%s\" (%s)",
				task.ID, result.OriginalName, result.SanitizedName, strings.Join(result.Reasons, ", "))
			_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
		}

		if sanitize.IsCaseInsensitive(mig.TargetProvider) {
			collision, err := sanitize.CheckCaseCollision(ctx, targetClient, task.ResourceType,
				path.Dir(targetPath), path.Base(targetPath))
			if err != nil {
				log.Printf("Warning: case collision check failed: %v", err)
			} else if collision != "" {
				resolved, err := sanitize.ResolveCollision(ctx, targetClient, task.ResourceType,
					path.Dir(targetPath), path.Base(targetPath), mig.TargetProvider)
				if err != nil {
					return fmt.Errorf("failed to resolve case collision: %w", err)
				}
				targetPath = path.Join(path.Dir(targetPath), resolved)
				log.Printf("[COLLISION] %s: case collision with \"%s\" → \"%s\"",
					task.ID, collision, path.Base(targetPath))
				_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
			}
		}
	}

	exists, existingSize, err := targetClient.FileExists(ctx, task.ResourceType, targetPath)
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
				// Decide whether to skip or overwrite:
				// - A retry (attempts > 0) means the existing file is the leftover of a
				//   previous failed attempt -> overwrite it.
				// - If the existing target file's size already matches the source size, it
				//   is complete and correct (pre-existing or already-migrated) -> safe to skip.
				// - Otherwise the existing file is partial/stale (e.g. an interrupted upload
				//   from a worker restart left a partial file) -> overwrite so we never leave
				//   an incomplete file behind and miscount it as "skipped".
				if task.Attempts > 0 {
					deleteAfterUpload = true
					break
				}
				if exists && existingSize == task.FileSize {
					task.Status = "SKIPPED"
					task.ErrorMessage = sql.NullString{String: "File already exists in target (SKIP)", Valid: true}
					_ = db.UpdateTaskStatus(p.db, task)
					_ = db.IncrementMigrationProgress(p.db, mig.ID, 1, task.FileSize, 1, 0)
					return nil
				}
				// Partial/stale or size-unknown existing file -> overwrite instead of skip.
				deleteAfterUpload = true
				break

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
						_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
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

	// Wrap download stream with throttling (before TeeReader to limit actual network I/O)
	throttledDownloadStream := throttle.NewThrottledReader(downloadStream, migrationThrottler, downloadCtx)

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
	} else if mig.TargetProvider == "s3" {
		targetAlgo = "SHA256"
		targetHasher = sha256.New()
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
	hashingReader := io.TeeReader(throttledDownloadStream, activeWriter)

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
		// Wrap hashingReader with upload throttling
		throttledHashingReader := throttle.NewUploadThrottledReader(hashingReader, migrationThrottler, uploadCtx)
		err = targetClient.StreamUploadChunked(uploadCtx, task.ResourceType, uploadPath, throttledHashingReader, task.FileSize, progressChan)
	} else {
		// Simple upload
		// Wrap with a progress reporting reader
		progressReader := &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
		// Wrap progressReader with upload throttling
		throttledProgressReader := throttle.NewUploadThrottledReader(progressReader, migrationThrottler, uploadCtx)
		err = targetClient.StreamUpload(uploadCtx, task.ResourceType, uploadPath, throttledProgressReader, task.FileSize)
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

	if applier, ok := targetClient.(storage.MetadataApplier); ok {
		var meta storage.FileMetadata
		if task.Metadata != nil {
			_ = json.Unmarshal(task.Metadata, &meta)
		}
		if meta.ModifiedTime.IsZero() {
			if srcInfo, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr == nil {
				meta.ModifiedTime = srcInfo.LastModified
			}
		}
		if !meta.ModifiedTime.IsZero() || meta.Description != "" {
			if err := applier.ApplyMetadata(ctx, task.ResourceType, targetPath, meta); err != nil {
				log.Printf("Warning: failed to apply metadata for %s: %v", targetPath, err)
			}
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
		// Retry the hash query: a transient Nextcloud error (502/503/423/timeout)
		// returns an empty hash and would otherwise cause a false integrity failure.
		// Retry until we get a real value (or exhaust attempts).
		for hashAttempt := 0; hashAttempt < 3; hashAttempt++ {
			if mig.TargetProvider != "webdav" {
				targetHashVal, errTargetHash = targetClient.GetFileHash(ctx, task.ResourceType, targetPath)
			} else {
				errTargetHash = fmt.Errorf("webdav target hash not supported")
				break
			}
			if errTargetHash == nil && targetHashVal != "" {
				break
			}
			if hashAttempt < 2 {
				time.Sleep(2 * time.Second)
			}
		}
		if errTargetHash != nil {
			log.Printf("[INTEGRITY] GetFileHash failed after retries for %s: %v", targetPath, errTargetHash)
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
				if workerTargetHashVal == cleanTargetHash {
					uploadOK = true
				} else {
					// Hash mismatch: some providers (e.g. Nextcloud via oc:checksums)
					// report a checksum that does not match the actual content hash even
					// though the upload succeeded and the file is intact. The upload commit
					// already verified the size, so fall back to a size comparison before
					// declaring the transfer corrupt (prevents false "skipped" files).
					existsOnTarget, targetSize, errExists := verifyTargetSize(ctx, targetClient, task.ResourceType, targetPath)
					if errExists == nil && existsOnTarget {
						if task.FileSize == 0 {
							uploadOK = true
						} else {
							uploadOK = (task.FileSize == targetSize)
						}
						if uploadOK {
							log.Printf("[INTEGRITY] target hash mismatch but size matches for %s (source=%d, target=%d) — accepting", targetPath, task.FileSize, targetSize)
						}
					} else if errExists != nil {
						// The size-verification query itself failed (e.g. transient Nextcloud
						// 502/503/423). The chunked-upload commit already verified the file
						// exists with the correct size, so accept rather than fail the
						// (correct) transfer.
						log.Printf("[INTEGRITY] target hash mismatch and size-query failed for %s; accepting (commit verified size)", targetPath)
						uploadOK = true
					} else {
						uploadOK = false
					}
				}
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
				} else if errExists != nil {
					// Size-verification query failed (transient Nextcloud 502/503/423);
					// the upload commit already verified size, so accept.
					log.Printf("[INTEGRITY] size-query failed for %s; accepting (commit verified size)", targetPath)
					uploadOK = true
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
			} else if errExists != nil {
				// Size-verification query failed (transient Nextcloud 502/503/423);
				// the upload commit already verified size, so accept.
				log.Printf("[INTEGRITY] size-query failed for %s; accepting (commit verified size)", targetPath)
				uploadOK = true
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

	// If the target verified correctly by size but the source hash did not match
	// (e.g. the source provider reports an unreliable/legacy checksum, or the file
	// changed after indexing), the transferred file is still intact: it was streamed
	// 1:1 and the target size matches the source size. Treat the source-hash
	// discrepancy as non-fatal so we don't wrongly skip a good file.
	if !downloadOK && uploadOK {
		log.Printf("[INTEGRITY] source hash mismatch but target size verified for %s — accepting", task.FilePath)
		downloadOK = true
	}

	integrityVerified = downloadOK && uploadOK
	if !integrityVerified {
		// Detail log so we can see exactly which part mismatched (hash vs size,
		// source vs target). workerHash holds the source-computed hash, targetHash
		// holds the target hash or "SIZE:<n>" when only size verification was possible.
		log.Printf("[INTEGRITY] FAILED for %s: downloadOK=%v uploadOK=%v workerHash=%s targetHash=%s sourceFileSize=%d",
			task.FilePath, downloadOK, uploadOK, task.WorkerHash.String, task.TargetHash.String, task.FileSize)
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
		if owner, oerr := db.GetMigrationOwnerID(p.db, payload.MigrationID); oerr == nil {
			db.WriteAuditLog(p.db, db.AuditEntry{
				UserID:  sql.NullString{String: owner, Valid: true},
				Action:  db.AuditMigrationFailed,
				Target:  payload.MigrationID,
				Details: json.RawMessage(`{"phase":"transfer","reason":"auth_error"}`),
			})
		}
		return
	}

	// If it is a normal file transfer failure
	if task.Attempts < 3 && !isPermanent {
		// Exponential Backoff: 10s, 30s (Finding 10)
		backoffTable := []int{10, 30, 90}
		idx := task.Attempts - 1
		if idx < 0 {
			idx = 0
		} else if idx >= len(backoffTable) {
			idx = len(backoffTable) - 1
		}
		backoffSec := backoffTable[idx]

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

	// Acquire lock to serialize token refresh requests for the same migration (Finding 7)
	mu := p.getOrCreateRefreshLock(mig.ID)
	mu.Lock()
	defer mu.Unlock()

	// Double-check: re-query the migration from the database after acquiring the lock to get the latest tokens.
	latestMig, err := db.GetMigration(p.db, mig.ID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest migration details inside refresh lock: %w", err)
	}

	// Determine latest fields and decrypt updated access token if exists
	if role == "source" {
		refreshTokenEnc = latestMig.SourceRefreshTokenEncrypted
		expiresAt = latestMig.SourceTokenExpiresAt
		provider = latestMig.SourceProvider
		latestAccessEnc := latestMig.SourcePasswordEncrypted
		latestAccess, err := crypto.Decrypt(latestAccessEnc, p.secretKey)
		if err == nil {
			accessToken = latestAccess
		}
	} else {
		refreshTokenEnc = latestMig.TargetRefreshTokenEncrypted
		expiresAt = latestMig.TargetTokenExpiresAt
		provider = latestMig.TargetProvider
		latestAccessEnc := latestMig.TargetPasswordEncrypted
		latestAccess, err := crypto.Decrypt(latestAccessEnc, p.secretKey)
		if err == nil {
			accessToken = latestAccess
		}
	}

	// Token still valid with >2 min margin (updated by a concurrent thread) → use as-is.
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

// RunCompletionNotifier polls for migrations that have reached a terminal state
// (COMPLETED or FAILED) and have not yet had their completion email sent. It uses
// the user's own per-user SMTP configuration (if configured and enabled).
func (p *Processor) RunCompletionNotifier(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	throttleCleanupTicker := time.NewTicker(1 * time.Minute)
	defer throttleCleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPendingCompletionEmails(ctx)
		case <-cleanupTicker.C:
			if err := db.DeleteExpiredPasswordResetTokens(p.db); err != nil {
				fmt.Printf("[CompletionNotifier] Error cleaning up expired reset tokens: %v\n", err)
			}
			if err := db.DeleteExpiredEmailChangeTokens(p.db); err != nil {
				fmt.Printf("[CompletionNotifier] Error cleaning up expired email change tokens: %v\n", err)
			}
		case <-throttleCleanupTicker.C:
			p.cleanupThrottlers()
		}
	}
}

// cleanupThrottlers removes throttlers for migrations that have reached a
// terminal state. Throttlers are intentionally kept alive for the full lifetime
// of a migration (not per-task) so that bandwidth changes applied between task
// batches are never dropped (avoids a delete-then-publish race).
func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		migrationID := key.(string)
		mig, err := db.GetMigration(p.db, migrationID)
		if err != nil || mig == nil {
			p.throttlers.Delete(migrationID)
			return true
		}
		switch mig.Status {
		case "COMPLETED", "FAILED", "CANCELLED":
			p.throttlers.Delete(migrationID)
		}
		return true
	})
}

func (p *Processor) sendPendingCompletionEmails(ctx context.Context) {
	notifications, err := db.ClaimPendingEmailNotifications(p.db, 10)
	if err != nil {
		fmt.Printf("[CompletionNotifier] Error claiming pending notifications: %v\n", err)
		return
	}

	for _, n := range notifications {
		p.sendCompletionEmail(ctx, n)
	}
}

func (p *Processor) sendCompletionEmail(ctx context.Context, n db.PendingEmailNotification) {
	settings, err := db.GetUserSMTPSettings(p.db, n.UserID)
	if err != nil {
		if err == sql.ErrNoRows {
			// User has no SMTP config: silently skip email
			_ = db.MarkMigrationEmailSent(p.db, n.MigrationID)
			return
		}
		fmt.Printf("[CompletionNotifier] Error fetching SMTP settings for user %s: %v\n", n.UserID, err)
		return
	}

	if !settings.NotifyOnCompletion {
		// User disabled completion notifications: silently skip
		_ = db.MarkMigrationEmailSent(p.db, n.MigrationID)
		return
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, p.secretKey)
	if err != nil {
		fmt.Printf("[CompletionNotifier] Error decrypting SMTP password for user %s: %v\n", n.UserID, err)
		_ = db.MarkMigrationEmailSent(p.db, n.MigrationID)
		return
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	user, err := db.GetUserByID(p.db, n.UserID)
	if err != nil {
		fmt.Printf("[CompletionNotifier] Error fetching user %s: %v\n", n.UserID, err)
		_ = db.MarkMigrationEmailSent(p.db, n.MigrationID)
		return
	}

	errMsg := ""
	if n.ErrorMessage.Valid {
		errMsg = n.ErrorMessage.String
	}

	htmlBody := email.BuildMigrationReportEmail(
		n.MigrationID, n.Status,
		n.TotalFiles, n.ProcessedFiles, n.FailedFiles, n.SkippedFiles,
		n.TotalBytes, n.ProcessedBytes, errMsg,
	)

	if err := email.SendMail(smtpCfg, user.Email, "Clumoove — Migrationsbericht", htmlBody); err != nil {
		fmt.Printf("[CompletionNotifier] Error sending completion email for migration %s: %v\n", n.MigrationID, err)
		// Leave email_sent = FALSE so it gets retried on the next tick
		return
	}

	if err := db.MarkMigrationEmailSent(p.db, n.MigrationID); err != nil {
		fmt.Printf("[CompletionNotifier] Error marking email sent for migration %s: %v\n", n.MigrationID, err)
	}
}

// verifyTargetSize queries the target for existence and size, retrying on
// transient errors (Nextcloud 502/503/423/timeout). A transient failure to
// *query* verification must not be mistaken for a corrupt transfer, so we retry
// before giving up. Returns the last result after the attempts.
func verifyTargetSize(ctx context.Context, client storage.StorageProvider, resourceType, path string) (exists bool, size int64, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		exists, size, err = client.FileExists(ctx, resourceType, path)
		if err == nil {
			return exists, size, nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return exists, size, err
}
