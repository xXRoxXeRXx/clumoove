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
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"
	"backend/internal/throttle"
)

// syncErrorRe redacts user:pass@ from error strings before they are persisted.
var syncErrorRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s:@]+:[^/\s:@]+@`)

func sanitizeSyncError(msg string) string {
	return syncErrorRe.ReplaceAllString(msg, "${1}***:***@")
}

// processSyncTask handles execution of a single task belonging to a sync job.
func (p *Processor) processSyncTask(ctx context.Context, payload *queue.Payload) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.activeTasks.Store(payload.TaskID, activeTaskInfo{
		syncJobID: payload.SyncJobID,
		cancel:    cancel,
	})
	defer func() {
		p.activeTasks.Delete(payload.TaskID)
	}()

	// 1. Fetch Sync Job
	job, err := db.GetSyncJob(p.db, payload.SyncJobID)
	if err != nil {
		return fmt.Errorf("failed to fetch sync job: %w", err)
	}

	// Re-route or requeue if paused or connection loss
	if job.Status == "PAUSED_CONNECTION_LOSS" || job.Status == "PAUSED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL WHERE id=$1", payload.TaskID)
		time.Sleep(2 * time.Second)
		return nil
	}

	if job.Status == "COMPLETED" || job.Status == "FAILED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='SKIPPED', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return nil
	}

	// 2. Fetch Task
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}

	// Parse action/metadata
	var meta map[string]interface{}
	_ = json.Unmarshal(task.Metadata, &meta)
	action, _ := meta["action"].(string)
	side, _ := meta["side"].(string)

	// Decrypt credentials
	sourcePass, err := crypto.Decrypt(job.SourcePasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}
	defer crypto.ZeroString(&sourcePass)

	targetPass, err := crypto.Decrypt(job.TargetPasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}
	defer crypto.ZeroString(&targetPass)

	// Setup clients depending on action
	if action == "delete" {
		if side == "source" {
			sourceClient, err := storage.NewProvider(ctx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
			if err != nil {
				return fmt.Errorf("failed to create source client: %w", err)
			}
			defer sourceClient.Close()
			if ok, err := sourceClient.Connect(ctx); !ok {
				return fmt.Errorf("failed to connect to source for delete: %w", err)
			}
			err = sourceClient.DeleteFile(ctx, task.ResourceType, task.FilePath)
			if err != nil {
				return fmt.Errorf("failed to delete source file: %w", err)
			}
		} else {
			targetClient, err := storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
			if err != nil {
				return fmt.Errorf("failed to create target client: %w", err)
			}
			defer targetClient.Close()
			if ok, err := targetClient.Connect(ctx); !ok {
				return fmt.Errorf("failed to connect to target for delete: %w", err)
			}
			tgtPath := path.Clean(path.Join(job.TargetDir, task.FilePath))
			err = targetClient.DeleteFile(ctx, task.ResourceType, tgtPath)
			if err != nil {
				return fmt.Errorf("failed to delete target file: %w", err)
			}
		}

		// Success
		task.Status = "COMPLETED"
		_ = db.UpdateTaskStatus(p.db, task)
		_ = db.IncrementSyncJobProgress(p.db, ctx, job.ID, 1, 0, 1, 0, 0) // filesDelta=1, deletedDelta=1
		return nil
	}

	if action == "conflict_copy" {
		targetClient, err := storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
		if err != nil {
			return fmt.Errorf("failed to create target client for conflict: %w", err)
		}
		defer targetClient.Close()
		if ok, err := targetClient.Connect(ctx); !ok {
			return fmt.Errorf("failed to connect to target for conflict: %w", err)
		}

		tgtPath := path.Clean(path.Join(job.TargetDir, task.FilePath))
		dir := path.Dir(tgtPath)
		base := path.Base(tgtPath)
		ext := path.Ext(base)
		nameWithoutExt := strings.TrimSuffix(base, ext)
		ts := time.Now().Format("20060102150405")
		conflictName := fmt.Sprintf("%s.conflict-%s%s", nameWithoutExt, ts, ext)
		newPath := path.Clean(path.Join(dir, conflictName))

		err = targetClient.RenameFile(ctx, task.ResourceType, tgtPath, newPath)
		if err != nil {
			return fmt.Errorf("failed to rename target to conflict copy: %w", err)
		}

		// Success
		task.Status = "COMPLETED"
		_ = db.UpdateTaskStatus(p.db, task)
		_ = db.IncrementSyncJobProgress(p.db, ctx, job.ID, 1, 1, 0, 0, 0) // filesDelta=1, changedDelta=1
		return nil
	}

	// Handle upload and download
	var srcClient, tgtClient storage.StorageProvider
	var srcPath, tgtPath string
	var srcProvider, tgtProvider string

	if action == "download" {
		// Download: Target -> Source (Two-Way pull)
		srcClient, err = storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
		if err != nil {
			return fmt.Errorf("failed to create target (source) client: %w", err)
		}
		defer srcClient.Close()

		tgtClient, err = storage.NewProvider(ctx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
		if err != nil {
			return fmt.Errorf("failed to create source (target) client: %w", err)
		}
		defer tgtClient.Close()

		srcPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		tgtPath = task.FilePath
		srcProvider = job.TargetProvider
		tgtProvider = job.SourceProvider
	} else {
		// Upload: Source -> Target (Standard migration style)
		srcClient, err = storage.NewProvider(ctx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourcePass)
		if err != nil {
			return fmt.Errorf("failed to create source client: %w", err)
		}
		defer srcClient.Close()

		tgtClient, err = storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetPass)
		if err != nil {
			return fmt.Errorf("failed to create target client: %w", err)
		}
		defer tgtClient.Close()

		srcPath = task.FilePath
		tgtPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		srcProvider = job.SourceProvider
		tgtProvider = job.TargetProvider
	}

	if ok, err := srcClient.Connect(ctx); !ok {
		return fmt.Errorf("source connection failed: %w", err)
	}
	if ok, err := tgtClient.Connect(ctx); !ok {
		return fmt.Errorf("target connection failed: %w", err)
	}

	// Create directories if needed
	if err := tgtClient.CreateParentDirectories(ctx, task.ResourceType, tgtPath); err != nil {
		return fmt.Errorf("failed to create target directories: %w", err)
	}

	// Use temporary path if atomic rename is supported
	uploadPath := tgtPath
	if tgtClient.SupportsAtomicRename() {
		uploadPath = tgtPath + ".tmp"
	}

	transferDeadline := transferTimeout(task.FileSize)
	downloadCtx, downloadCancel := context.WithTimeout(ctx, transferDeadline)
	defer downloadCancel()

	downloadStream, err := srcClient.StreamDownload(downloadCtx, task.ResourceType, srcPath)
	if err != nil {
		return fmt.Errorf("failed to download from source: %w", err)
	}
	defer downloadStream.Close()

	// Wrap throttler (0 bandwidth limit is unlimited)
	throttler, _ := p.throttlers.LoadOrStore(job.ID, throttle.NewMigrationThrottler(0))
	jobThrottler := throttler.(*throttle.MigrationThrottler)
	throttledDownloadStream := throttle.NewThrottledReader(downloadStream, jobThrottler, downloadCtx)

	// Hash calculations
	var sourceHasher hash.Hash
	sourceAlgo := "SHA1"

	if task.SourceHash.Valid && task.SourceHash.String != "" && srcProvider != "webdav" {
		algo, _ := storage.ParseHashString(task.SourceHash.String)
		sourceAlgo = algo
	} else {
		if srcProvider != "webdav" {
			if fetchedHash, err := srcClient.GetFileHash(ctx, task.ResourceType, srcPath); err == nil {
				task.SourceHash = sql.NullString{String: fetchedHash, Valid: true}
				algo, _ := storage.ParseHashString(fetchedHash)
				sourceAlgo = algo
			}
		}
	}

	if srcProvider == "dropbox" {
		sourceAlgo = "DROPBOX"
	}

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

	var targetHasher hash.Hash
	targetAlgo := "SHA1"
	if tgtProvider == "dropbox" {
		targetAlgo = "DROPBOX"
		targetHasher = storage.NewDropboxHasher()
	} else if tgtProvider == "s3" {
		targetAlgo = "SHA256"
		targetHasher = sha256.New()
	} else {
		targetAlgo = "SHA1"
		targetHasher = sha1.New()
	}

	var activeWriter io.Writer
	if sourceAlgo == targetAlgo {
		activeWriter = sourceHasher
		targetHasher = nil
	} else {
		activeWriter = io.MultiWriter(sourceHasher, targetHasher)
	}

	progressChan := make(chan int64, 10)
	progressDone := make(chan struct{})
	var lastByteNano = time.Now().UnixNano()
	taskStart := time.Now()

	go func() {
		defer close(progressDone)
		var bufferedBytes int64
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					if bufferedBytes > 0 {
						_ = db.AddSyncJobLiveBytes(p.db, ctx, job.ID, bufferedBytes)
						bufferedBytes = 0
					}
					return
				}
				bufferedBytes += bytes
				atomic.StoreInt64(&lastByteNano, time.Now().UnixNano())
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.AddSyncJobLiveBytes(p.db, ctx, job.ID, bufferedBytes)
					bufferedBytes = 0
				}
			}
		}
	}()

	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace &&
					time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if !stale {
					_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET updated_at = NOW() WHERE id = $1 AND status = 'RUNNING'", task.ID)
				}
			}
		}
	}()

	defer func() {
		close(progressChan)
		<-progressDone
		close(heartbeatStop)
	}()

	hashingReader := io.TeeReader(throttledDownloadStream, activeWriter)
	uploadCtx, uploadCancel := context.WithTimeout(ctx, transferDeadline)
	defer uploadCancel()

	if task.FileSize > chunkedUploadThreshold {
		throttledHashingReader := throttle.NewUploadThrottledReader(hashingReader, jobThrottler, uploadCtx)
		err = tgtClient.StreamUploadChunked(uploadCtx, task.ResourceType, uploadPath, throttledHashingReader, task.FileSize, progressChan)
	} else {
		progressReader := &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
		throttledProgressReader := throttle.NewUploadThrottledReader(progressReader, jobThrottler, uploadCtx)
		err = tgtClient.StreamUpload(uploadCtx, task.ResourceType, uploadPath, throttledProgressReader, task.FileSize)
	}

	if err != nil {
		return fmt.Errorf("failed to upload: %w", err)
	}

	// Rename temp file if necessary
	if tgtClient.SupportsAtomicRename() {
		renameCtx, renameCancel := context.WithTimeout(ctx, 30*time.Second)
		err = tgtClient.RenameFile(renameCtx, task.ResourceType, uploadPath, tgtPath)
		renameCancel()
		if err != nil {
			return fmt.Errorf("failed to rename temp file to target path: %w", err)
		}
	}

	// Verify Target Hash
	var finalTargetHash string
	if targetHasher != nil {
		finalTargetHash = fmt.Sprintf("%x", targetHasher.Sum(nil))
	} else if sourceHasher != nil {
		finalTargetHash = fmt.Sprintf("%x", sourceHasher.Sum(nil))
	}

	if tgtProvider != "webdav" {
		targetVerifyHash, terr := tgtClient.GetFileHash(ctx, task.ResourceType, tgtPath)
		if terr == nil && targetVerifyHash != "" {
			_, cleanTgtHash := storage.ParseHashString(targetVerifyHash)
			_, cleanExpected := storage.ParseHashString(finalTargetHash)
			if cleanTgtHash != cleanExpected {
				return fmt.Errorf("hash verification failed on target: expected %s, got %s", cleanExpected, cleanTgtHash)
			}
		}
	}

	// Success
	task.Status = "COMPLETED"
	task.WorkerHash = sql.NullString{String: finalTargetHash, Valid: true}
	task.TargetHash = sql.NullString{String: finalTargetHash, Valid: true}
	_ = db.UpdateTaskStatus(p.db, task)
	_ = db.IncrementSyncJobProgress(p.db, ctx, job.ID, 1, 1, 0, 0, task.FileSize) // filesDelta=1, changedDelta=1, bytesDelta=task.FileSize
	return nil
}

// handleSyncTaskFailure registers failure and checks retry count for sync tasks.
func (p *Processor) handleSyncTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		log.Printf("Error fetching task on sync failure handler: %v\n", err)
		return
	}

	job, jobErr := db.GetSyncJob(p.db, payload.SyncJobID)
	if jobErr == nil && (job.Status == "PAUSED" || job.Status == "COMPLETED") {
		task.Status = "CANCELLED"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		task.Status = "PENDING"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: sanitizeSyncError(procErr.Error()), Valid: true}

	isConnLoss := isNetworkError(procErr)
	if isConnLoss {
		lossCount := p.recordConnLoss(payload.SyncJobID)
		taskConnLoss := p.recordConnLossTask(task.ID)

		if lossCount < connLossEscalationThreshold && taskConnLoss < maxConnLossTaskAttempts {
			backoff := retryBackoff(taskConnLoss)
			nextRetry := time.Now().Add(backoff)
			task.Status = "FAILED"
			task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
			_ = db.UpdateTaskStatus(p.db, task)
			return
		}

		// Connection loss escalation: pause the sync job
		_ = db.UpdateSyncJobStatus(p.db, payload.SyncJobID, "PAUSED_CONNECTION_LOSS", nil)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(task.ID)
		p.recoveryAttempts.Delete(payload.SyncJobID)

		task.Status = "PENDING"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	isPermanent := false
	errStr := procErr.Error()
	if strings.Contains(errStr, "exportSizeLimitExceeded") ||
		strings.Contains(errStr, "badRequest") ||
		strings.Contains(errStr, "notFound") ||
		strings.Contains(errStr, "fileNotFound") {
		isPermanent = true
	}

	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials")

	if isAuthError {
		authErrMsg := "Authentication failed - please check your credentials"
		_ = db.UpdateSyncJobStatus(p.db, payload.SyncJobID, "FAILED", &authErrMsg)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(payload.TaskID)
		p.recoveryAttempts.Delete(payload.SyncJobID)

		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateTaskStatus(p.db, task)
		_ = db.IncrementSyncJobProgress(p.db, ctx, task.SyncJobID, 1, 0, 0, 1, 0)

		// Cancel other pending tasks
		cancelled, cerr := db.CancelRemainingPendingSyncTasks(p.db, task.SyncJobID)
		if cerr == nil && cancelled > 0 {
			_ = db.IncrementSyncJobProgress(p.db, ctx, task.SyncJobID, 0, 0, 0, cancelled, 0)
		}
		return
	}

	if task.Attempts < 3 && !isPermanent {
		backoff := retryBackoff(task.Attempts)
		nextRetry := time.Now().Add(backoff)
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		_ = db.UpdateTaskStatus(p.db, task)
	} else {
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateTaskStatus(p.db, task)
		p.clearConnLossTask(task.ID)
		_ = db.IncrementSyncJobProgress(p.db, ctx, task.SyncJobID, 1, 0, 0, 1, 0)
	}
}

// recoverPausedSyncJobs checks connection-loss paused sync jobs and restores connection.
func (p *Processor) recoverPausedSyncJobs(ctx context.Context) {
	query := `
		SELECT id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM sync_jobs
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

		var ra recoveryState
		if v, ok := p.recoveryAttempts.Load(id); ok {
			ra = v.(recoveryState)
		}
		if backoff := recoveryBackoff(ra.attempts); backoff > 0 && time.Since(ra.lastAttempt) < backoff {
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
			log.Printf("[RecoveryScheduler] Connection restored for sync job %s! Resuming...\n", id)
			// Restore to IDLE (not RUNNING) so the engine's completion notifier
			// can track the pass. Then launch a fresh RunSyncPass which handles
			// the INDEXING→RUNNING→IDLE lifecycle.
			_, err = p.db.ExecContext(ctx, `UPDATE sync_jobs SET status = 'IDLE' WHERE id = $1`, id)
			if err != nil {
				log.Printf("[RecoveryScheduler] Error resuming sync job %s: %v\n", id, err)
			} else if p.syncEngine != nil {
				go p.syncEngine.RunSyncPass(ctx, id)
			}
			p.recoveryAttempts.Delete(id)
		} else {
			p.recoveryAttempts.Store(id, recoveryState{lastAttempt: time.Now(), attempts: ra.attempts + 1})
		}
	}
}
