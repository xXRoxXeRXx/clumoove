package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
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
	"backend/internal/queue"
	"backend/internal/storage"
)

type Processor struct {
	db         *sql.DB
	queue      *queue.Queue
	workerID   string
	secretKey  string
	maxThreads int
}

func NewProcessor(database *sql.DB, q *queue.Queue, workerID string, secretKey string) *Processor {
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
						continue // No task in queue
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
		  AND t.attempts < 3 
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
			continue
		}

		connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
		sOK, _ := sClient.Connect(connCtx)
		tOK, _ := tClient.Connect(connCtx)
		connCancel()

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

func (p *Processor) processTask(ctx context.Context, payload *queue.Payload) error {
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

	// Create storage providers
	sourceClient, err := storage.NewProvider(ctx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		return fmt.Errorf("failed to create source client: %w", err)
	}
	targetClient, err := storage.NewProvider(ctx, mig.TargetProvider, mig.TargetURL, mig.TargetUsername, targetPass)
	if err != nil {
		return fmt.Errorf("failed to create target client: %w", err)
	}

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

	downloadStream, err := sourceClient.StreamDownload(ctx, task.ResourceType, task.FilePath)
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

	// Instantiate source hasher
	if sourceAlgo == "MD5" {
		sourceHasher = md5.New()
	} else if sourceAlgo == "DROPBOX" {
		sourceHasher = storage.NewDropboxHasher()
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
	defer close(progressChan)
	go func() {
		// This goroutine updates progress of migration in the DB
		// Buffer progress updates to reduce database load
		var bufferedBytes int64
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					// Final flush
					if bufferedBytes > 0 {
						_ = db.IncrementMigrationProgress(p.db, mig.ID, 0, bufferedBytes, 0, 0)
					}
					return
				}
				bufferedBytes += bytes
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.IncrementMigrationProgress(p.db, mig.ID, 0, bufferedBytes, 0, 0)
					bufferedBytes = 0
				}
			}
		}
	}()

	// io.TeeReader writes all data read from the download stream to the hasher in-memory
	hashingReader := io.TeeReader(downloadStream, activeWriter)

	// Perform Upload (Zero Data Retention - streamed through RAM buffer)
	// If size > 50MB, do chunked upload
	if task.FileSize > 50*1024*1024 {
		err = targetClient.StreamUploadChunked(ctx, task.ResourceType, uploadPath, hashingReader, task.FileSize, progressChan)
	} else {
		// Simple upload
		// Wrap with a progress reporting reader
		progressReader := &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
		err = targetClient.StreamUpload(ctx, task.ResourceType, uploadPath, progressReader, task.FileSize)
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

	// If it is a normal file transfer failure
	if task.Attempts < 3 && !isPermanent {
		// Exponential Backoff: 10s, 30s, 90s
		backoffSec := int(math.Pow(3, float64(task.Attempts))) * 10
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
		fmt.Printf("[Worker %s] Task %s failed permanently after 3 attempts\n", p.workerID, task.ID)
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
		strings.Contains(errStr, "http2: server sent goaway") ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded)
}
