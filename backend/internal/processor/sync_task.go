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
	"path"
	"strings"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

// processSyncTask handles execution of a single task belonging to a sync job.
func (p *Processor) processSyncTask(ctx context.Context, payload *queue.Payload, threadID int) (err error) {
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
	ctx = storage.WithLocalUserScope(ctx, job.UserID)
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}
	task.ClaimEpoch = payload.ClaimEpoch
	if task.SyncJobID != payload.SyncJobID || task.PassGeneration != job.RunGeneration {
		return nil
	}

	// A paused worker must acknowledge cancellation durably; do not return it to
	// PENDING where a resumed pass could claim it.
	if job.Status == "PAUSED_CONNECTION_LOSS" || job.Status == "PAUSED" {
		task.Status = "CANCELLED"
		_ = db.UpdateClaimedSyncTaskStatus(p.db, ctx, task)
		return nil
	}

	if job.Status == "COMPLETED" || job.Status == "FAILED" {
		task.Status = "SKIPPED"
		_ = db.UpdateClaimedSyncTaskStatus(p.db, ctx, task)
		return nil
	}

	// Parse action/metadata
	var meta map[string]interface{}
	if err := json.Unmarshal(task.Metadata, &meta); err != nil {
		processorLogf("[Worker] Failed to parse task metadata for task %s: %v", task.ID, err)
		return fmt.Errorf("failed to parse task metadata: %w", err)
	}
	action, _ := meta["action"].(string)
	side, _ := meta["side"].(string)

	processorLogf("[Worker %s] Thread %d -> Request: [%s] %s (%d bytes) [%s -> %s]\n",
		p.workerID, threadID, strings.ToUpper(action), task.FilePath, task.FileSize, job.SourceProvider, job.TargetProvider)

	// Decrypt credentials
	sourcePass, err := crypto.DecryptWithDomain(job.SourcePasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.SourceProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}

	targetPass, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}

	sourceProviderPass, err := p.ensureFreshSyncOAuthToken(ctx, job, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}

	targetProviderPass, err := p.ensureFreshSyncOAuthToken(ctx, job, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}

	sourceCtx, err := megasecret.WithMegaSession(ctx, job.SourceProvider, job.SourceMegaSessionIDEncrypted, job.SourceMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source MEGA session: %w", err)
	}
	targetCtx, err := megasecret.WithMegaSession(ctx, job.TargetProvider, job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target MEGA session: %w", err)
	}

	// Handle directory creation tasks (action == "mkdir").
	// Enqueued by the sync engine for directories present on one side but
	// missing on the other. We create the directory on the appropriate client
	// and skip the full file-transfer pipeline.
	if action == "mkdir" {
		// Determine which client and path to create the directory on.
		// side == "source" means create on source (two-way: target has the dir, source doesn't).
		// side == "" or "target" means create on target (one-way or two-way source->target).
		var mkClient storage.StorageProvider
		var mkPath string
		var mkProvider string
		var mkURL, mkUsername string
		if side == "source" {
			mkClient, err = storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourceProviderPass)
			if err != nil {
				return fmt.Errorf("failed to create source client for mkdir: %w", err)
			}
			defer mkClient.Close()
			mkPath = task.FilePath
			mkProvider = job.SourceProvider
			mkURL, mkUsername = job.SourceURL, job.SourceUsername
		} else {
			mkClient, err = storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetProviderPass)
			if err != nil {
				return fmt.Errorf("failed to create target client for mkdir: %w", err)
			}
			defer mkClient.Close()
			mkPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
			mkProvider = job.TargetProvider
			mkURL, mkUsername = job.TargetURL, job.TargetUsername
		}
		unlockMegaTarget := p.lockMegaTarget(mkProvider, mkURL, mkUsername)
		defer unlockMegaTarget()
		if ok, err := mkClient.Connect(ctx); !ok {
			if err == nil {
				err = errors.New("provider rejected connection")
			}
			return fmt.Errorf("failed to connect to %s provider for mkdir: %w", mkProvider, err)
		}

		// Sanitize directory name
		dirName := path.Base(mkPath)
		sanitized := sanitize.SanitizeFilename(dirName, mkProvider)
		if sanitized.Changed {
			mkPath = path.Join(path.Dir(mkPath), sanitized.SanitizedName)
			processorLogf("[Worker] Sanitized directory name: %s -> %s (%s)",
				dirName, sanitized.SanitizedName, strings.Join(sanitized.Reasons, ", "))
		}

		// Check for case collisions on case-insensitive providers
		if sanitize.IsCaseInsensitive(mkProvider) {
			dirPath := path.Dir(mkPath)
			dirName := path.Base(mkPath)
			if collision, _ := sanitize.CheckCaseCollision(ctx, mkClient, task.ResourceType, dirPath, dirName); collision != "" {
				processorLogf("[Worker] Directory case collision detected: %s conflicts with %s", mkPath, collision)
				task.Status = "SKIPPED"
				task.ErrorMessage = sql.NullString{String: fmt.Sprintf("Directory skipped due to case collision with %s", collision), Valid: true}
				return db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 1, 0, 0, 0)
			}
		}

		if err := mkClient.CreateDirectory(ctx, task.ResourceType, mkPath); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", mkPath, err)
		}
		task.Status = "COMPLETED"
		return db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 1, 0, 0, 0) // count as 1 changed item, 0 bytes
	}

	// Setup clients depending on action
	if action == "delete" {
		sourceClient, err := storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourceProviderPass)
		if err != nil {
			return fmt.Errorf("failed to create source client: %w", err)
		}
		defer sourceClient.Close()

		targetClient, err := storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetProviderPass)
		if err != nil {
			return fmt.Errorf("failed to create target client: %w", err)
		}
		defer targetClient.Close()

		if side == "source" {
			if ok, err := sourceClient.Connect(ctx); !ok {
				return fmt.Errorf("failed to connect to source for delete: %w", err)
			}
			err = sourceClient.DeleteFile(ctx, task.ResourceType, task.FilePath)
			if err != nil {
				return fmt.Errorf("failed to delete source file: %w", err)
			}
			_, _ = targetClient.Connect(ctx)
			pruneEmptyParentDirectories(ctx, sourceClient, targetClient, task.ResourceType, task.FilePath, "/", job.TargetDir)
		} else {
			if ok, err := targetClient.Connect(ctx); !ok {
				return fmt.Errorf("failed to connect to target for delete: %w", err)
			}
			tgtPath := path.Clean(path.Join(job.TargetDir, task.FilePath))
			err = targetClient.DeleteFile(ctx, task.ResourceType, tgtPath)
			if err != nil {
				return fmt.Errorf("failed to delete target file: %w", err)
			}
			_, _ = sourceClient.Connect(ctx)
			pruneEmptyParentDirectories(ctx, targetClient, sourceClient, task.ResourceType, tgtPath, job.TargetDir, "/")
		}

		// Success
		task.Status = "COMPLETED"
		return db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 0, 1, 0, 0) // filesDelta=1, deletedDelta=1
	}

	if action == "conflict_copy" {
		targetClient, err := storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetProviderPass)
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
		return db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 1, 0, 0, 0) // filesDelta=1, changedDelta=1
	}

	// Handle upload and download
	var srcClient, tgtClient storage.StorageProvider
	var srcPath, tgtPath string
	var srcProvider, tgtProvider string

	if action == "download" {
		// Download: Target -> Source (Two-Way pull)
		srcClient, err = storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetProviderPass)
		if err != nil {
			return fmt.Errorf("failed to create target (source) client: %w", err)
		}
		defer srcClient.Close()

		tgtClient, err = storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourceProviderPass)
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
		srcClient, err = storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, sourceProviderPass)
		if err != nil {
			return fmt.Errorf("failed to create source client: %w", err)
		}
		defer srcClient.Close()

		tgtClient, err = storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, targetProviderPass)
		if err != nil {
			return fmt.Errorf("failed to create target client: %w", err)
		}
		defer tgtClient.Close()

		srcPath = task.FilePath
		tgtPath = path.Clean(path.Join(job.TargetDir, task.FilePath))
		srcProvider = job.SourceProvider
		tgtProvider = job.TargetProvider
	}
	targetURL, targetUsername := job.TargetURL, job.TargetUsername
	if tgtProvider == job.SourceProvider {
		targetURL, targetUsername = job.SourceURL, job.SourceUsername
	}
	unlockMegaTarget := p.lockMegaTarget(tgtProvider, targetURL, targetUsername)
	defer unlockMegaTarget()
	if ok, err := srcClient.Connect(ctx); !ok {
		if err == nil {
			err = errors.New("provider rejected connection")
		}
		return fmt.Errorf("failed to connect to source provider: %w", err)
	}
	if ok, err := tgtClient.Connect(ctx); !ok {
		if err == nil {
			err = errors.New("provider rejected connection")
		}
		return fmt.Errorf("failed to connect to target provider: %w", err)
	}
	// Create directories if needed
	if err := tgtClient.CreateParentDirectories(ctx, task.ResourceType, tgtPath); err != nil {
		return fmt.Errorf("failed to create target directories: %w", err)
	}

	// Sync updates may replace an existing destination. Use the same staging
	// decision as migrations so providers whose RenameFile is non-atomic (such
	// as S3's copy-and-delete implementation) upload directly to the final key.
	useTempUpload := useTempThenRename(tgtClient, true)
	uploadPath := tgtPath
	if useTempUpload {
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

	// The stored limit initializes the throttler for a new pass. Set it again
	// for a reused throttler because mid-pass changes arrive via Pub/Sub, while
	// the map keeps the throttler for subsequent passes.
	throttler, _ := p.throttlers.LoadOrStore(job.ID, throttle.NewMigrationThrottler(job.BandwidthLimitMbps))
	jobThrottler := throttler.(*throttle.MigrationThrottler)
	jobThrottler.SetLimit(job.BandwidthLimitMbps)
	throttledDownloadStream := throttle.NewThrottledReader(downloadStream, jobThrottler, downloadCtx)

	var sourceHasher hash.Hash
	sourceAlgo := "SHA1"
	sourceHashStr := ""

	if task.SourceHash.Valid && task.SourceHash.String != "" && srcProvider != "webdav" {
		algo, cleanHash := storage.ParseHashString(task.SourceHash.String)
		// HiDrive's native chash cannot be recomputed by the streaming hasher.
		if algo == "SHA1" || algo == "SHA256" || algo == "MD5" || algo == "DROPBOX" {
			sourceHashStr = cleanHash
			sourceAlgo = algo
		}
	}

	if srcProvider == "dropbox" {
		sourceAlgo = "DROPBOX"
	} else if srcProvider == "google" {
		sourceAlgo = "MD5"
	} else if srcProvider == "onedrive" {
		sourceAlgo = "QUICKXOR"
	}

	if sourceAlgo == "MD5" {
		sourceHasher = md5.New()
	} else if sourceAlgo == "DROPBOX" {
		sourceHasher = storage.NewDropboxHasher()
	} else if sourceAlgo == "SHA256" {
		sourceHasher = sha256.New()
	} else if sourceAlgo == "QUICKXOR" {
		sourceHasher = storage.NewQuickXorHasher()
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
	} else if tgtProvider == "google" {
		targetAlgo = "MD5"
		targetHasher = md5.New()
	} else if tgtProvider == "hidrive" {
		targetAlgo = "HIDRIVE"
		targetHasher = storage.NewHiDriveHasher()
	} else if tgtProvider == "onedrive" {
		targetAlgo = "QUICKXOR"
		targetHasher = storage.NewQuickXorHasher()
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
						_ = db.AddSyncJobLiveBytesForGeneration(p.db, ctx, job.ID, task.PassGeneration, bufferedBytes)
						bufferedBytes = 0
					}
					return
				}
				bufferedBytes += bytes
				atomic.StoreInt64(&lastByteNano, time.Now().UnixNano())
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.AddSyncJobLiveBytesForGeneration(p.db, ctx, job.ID, task.PassGeneration, bufferedBytes)
					bufferedBytes = 0
				}
			}
		}
	}()

	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		consecutiveFailures := 0
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace &&
					time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if !stale {
					owned, err := db.HeartbeatSyncTaskClaim(p.db, ctx, task.ID, task.ClaimEpoch, task.PassGeneration)
					if err == nil && !owned {
						cancel() // recovery or a new claim fenced this worker off
						return
					}
					if err != nil {
						consecutiveFailures++
						processorLogf("[Worker %s] heartbeat error for task %s (failure %d/5): %v", p.workerID, task.ID, consecutiveFailures, err)
						if consecutiveFailures >= 5 {
							cancel()
							return
						}
					} else {
						consecutiveFailures = 0
					}
				}
			}
		}
	}()

	defer func() {
		close(progressChan)
		<-progressDone
		close(heartbeatStop)
	}()

	sizedReader := newExpectedSizeReader(throttledDownloadStream, task.FileSize)
	hashingReader := io.TeeReader(sizedReader, activeWriter)
	uploadCtx, uploadCancel := context.WithTimeout(ctx, transferDeadline)
	if sourceHashStr != "" && sourceAlgo != "ETAG" {
		uploadCtx = storage.WithUploadChecksum(uploadCtx, fmt.Sprintf("%s:%s", sourceAlgo, sourceHashStr))
	}
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
		if useTempUpload {
			cleanupStagingUpload(ctx, tgtClient, task.ResourceType, uploadPath)
		}
		return fmt.Errorf("failed to upload: %w", err)
	}
	if err := sizedReader.VerifyComplete(); err != nil {
		if useTempUpload {
			cleanupStagingUpload(ctx, tgtClient, task.ResourceType, uploadPath)
		}
		return err
	}

	// Rename the staging object only when the provider guarantees an atomic
	// replacement. Non-atomic providers uploaded directly to tgtPath above.
	if useTempUpload {
		backupPath := overwriteBackupPath(tgtPath, task.ID)
		if err := promoteOverwrite(ctx, tgtClient, task.ResourceType, tgtPath, uploadPath, backupPath); err != nil {
			return fmt.Errorf("failed to promote temporary upload to target path: %w", err)
		}
	}

	// Preserve source modification time on target if supported
	if srcRes, err := srcClient.InspectResource(ctx, task.ResourceType, srcPath); err == nil && !srcRes.LastModified.IsZero() {
		if applier, ok := tgtClient.(storage.MetadataApplier); ok {
			metaCtx, metaCancel := context.WithTimeout(ctx, 15*time.Second)
			err := applier.ApplyMetadata(metaCtx, task.ResourceType, tgtPath, storage.FileMetadata{
				ModifiedTime: srcRes.LastModified,
			})
			metaCancel()
			if errors.Is(err, storage.ErrAuth) {
				return err
			}
		}
	}

	if task.ResourceType == "files" {
		exists, targetSize, err := verifyTargetSize(ctx, tgtClient, task.ResourceType, tgtPath)
		if err != nil {
			return fmt.Errorf("failed to verify target size: %w", err)
		}
		if !exists || targetSize != task.FileSize {
			return fmt.Errorf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize)
		}
	}

	// Stream Hash Registration & Fast Task Completion
	var finalTargetHashVal string
	if targetHasher != nil {
		finalTargetHashVal = formatWorkerHashValue(targetAlgo, targetHasher)
	} else if sourceHasher != nil {
		finalTargetHashVal = formatWorkerHashValue(sourceAlgo, sourceHasher)
	}
	workerHashAlgo := sourceAlgo
	if targetHasher != nil {
		workerHashAlgo = targetAlgo
	}
	workerHash := fmt.Sprintf("%s:%s", workerHashAlgo, finalTargetHashVal)

	// Success
	task.Status = "COMPLETED"
	task.WorkerHash = sql.NullString{String: workerHash, Valid: true}
	task.TargetHash = sql.NullString{String: workerHash, Valid: true}
	return db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 1, 0, 0, task.FileSize) // filesDelta=1, changedDelta=1, bytesDelta=task.FileSize
}

// handleSyncTaskFailure registers failure and checks retry count for sync tasks.
func (p *Processor) handleSyncTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		processorLogf("Error fetching task on sync failure handler: %v\n", err)
		return
	}
	task.ClaimEpoch = payload.ClaimEpoch

	job, jobErr := db.GetSyncJob(p.db, payload.SyncJobID)
	if jobErr == nil && task.PassGeneration != job.RunGeneration {
		return
	}
	if jobErr == nil && (job.Status == "PAUSED" || job.Status == "PAUSED_CONNECTION_LOSS" || job.Status == "COMPLETED") {
		task.Status = "CANCELLED"
		_ = db.UpdateClaimedSyncTaskStatus(p.db, ctx, task)
		return
	}

	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		task.Status = "PENDING"
		if err := db.UpdateClaimedSyncTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("Error returning cancelled sync task %s to pending: %v", task.ID, err)
		}
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(procErr.Error()), Valid: true}

	isConnLoss := isNetworkError(procErr)
	if isConnLoss {
		lossCount := p.recordConnLoss(payload.SyncJobID)
		taskConnLoss := p.recordConnLossTask(task.ID)

		if lossCount < connLossEscalationThreshold && taskConnLoss < maxConnLossTaskAttempts {
			backoff := retryBackoff(taskConnLoss)
			nextRetry := time.Now().Add(backoff)
			task.Status = "FAILED"
			task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
			if err := db.UpdateClaimedSyncTaskStatus(p.db, ctx, task); err != nil {
				processorLogf("Error scheduling sync task %s retry after connection loss: %v", task.ID, err)
			}
			return
		}

		// Connection loss escalation: pause the sync job
		_ = db.UpdateSyncJobStatusForGeneration(p.db, payload.SyncJobID, task.PassGeneration, "PAUSED_CONNECTION_LOSS", nil)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(task.ID)
		p.recoveryAttempts.Delete(payload.SyncJobID)

		task.Status = "PENDING"
		if err := db.UpdateClaimedSyncTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("Error re-queueing sync task %s after connection loss: %v", task.ID, err)
		}
		return
	}

	isPermanent := isPermanentTransferError(procErr)
	errStr := procErr.Error()

	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials")

	if isAuthError {
		if role := oauthAuthFailureRoleForProviders(job.SourceProvider, job.TargetProvider, errStr); role != "" && task.Attempts <= 3 {
			if _, refreshErr := p.refreshSyncOAuthToken(ctx, job, role); refreshErr == nil {
				backoff := retryBackoff(task.Attempts)
				task.Status = "FAILED"
				task.ErrorMessage = sql.NullString{String: "OAuth access token rejected; refreshed token scheduled for retry", Valid: true}
				task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
				if err := db.UpdateClaimedSyncTaskStatus(p.db, ctx, task); err != nil {
					processorLogf("Error scheduling sync OAuth retry for task %s: %v", task.ID, err)
				}
				processorLogf("[Worker %s] OAuth 401 for sync task %s (job %s, %s) — refreshed token and retrying in %ds\n",
					p.workerID, payload.TaskID, payload.SyncJobID, role, int(backoff.Seconds()))
				return
			} else {
				processorLogf("[Worker %s] OAuth 401 recovery refresh failed for sync task %s (job %s, %s): %v\n",
					p.workerID, payload.TaskID, payload.SyncJobID, role, refreshErr)
			}
		}
		authErrMsg := "Authentication failed - please check your credentials"
		_ = db.UpdateSyncJobStatusForGeneration(p.db, payload.SyncJobID, task.PassGeneration, "FAILED", &authErrMsg)
		p.clearConnLoss(payload.SyncJobID)
		p.clearConnLossTask(payload.TaskID)
		p.recoveryAttempts.Delete(payload.SyncJobID)

		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		if err := db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 0, 0, 1, 0); err != nil {
			processorLogf("Error recording terminal sync task failure for %s: %v", task.ID, err)
			return
		}

		// Cancel other pending tasks
		cancelled, cerr := db.CancelRemainingPendingSyncTasksForGeneration(p.db, task.SyncJobID, task.PassGeneration)
		if cerr == nil && cancelled > 0 {
			if err := db.IncrementSyncJobProgressForGeneration(p.db, ctx, task.SyncJobID, task.PassGeneration, 0, 0, 0, cancelled, 0); err != nil {
				processorLogf("Error recording %d cancelled sync tasks for job %s: %v", cancelled, task.SyncJobID, err)
			}
		} else if cerr != nil {
			processorLogf("Error cancelling pending sync tasks for job %s: %v", task.SyncJobID, cerr)
		}
		return
	}

	if task.Attempts < 3 && !isPermanent {
		backoff := retryDelay(procErr, task.Attempts)
		nextRetry := time.Now().Add(backoff)
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		if err := db.UpdateClaimedSyncTaskStatus(p.db, ctx, task); err != nil {
			processorLogf("Error scheduling sync task %s retry: %v", task.ID, err)
		}
	} else {
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		if err := db.UpdateSyncTaskStatusAndIncrementProgress(p.db, ctx, task, 1, 0, 0, 1, 0); err != nil {
			processorLogf("Error recording exhausted sync task failure for %s: %v", task.ID, err)
			return
		}
		p.clearConnLossTask(task.ID)
	}
}

const maxSyncRecoveryProbesPerTick = 10

// recoverPausedSyncJobs checks connection-loss paused sync jobs and restores connection.
func (p *Processor) recoverPausedSyncJobs(ctx context.Context) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM sync_jobs
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
	`
	rows, err := p.db.QueryContext(ctx, query, p.recoveryCursor(true))
	if err != nil {
		return
	}
	defer rows.Close()

	probes := 0
	for rows.Next() {
		if probes >= maxSyncRecoveryProbesPerTick {
			break
		}

		var id, userID, sURL, sUser, sPassEnc, tURL, tUser, tPassEnc, sProv, tProv string
		if err := rows.Scan(&id, &userID, &sURL, &sUser, &sPassEnc, &tURL, &tUser, &tPassEnc, &sProv, &tProv); err != nil {
			continue
		}

		var ra recoveryState
		if v, ok := p.recoveryAttempts.Load(id); ok {
			ra = v.(recoveryState)
		}
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++
		p.setRecoveryCursor(true, id)

		sPass, err := crypto.DecryptWithDomain(sPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(sProv)))
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}
		tPass, err := crypto.DecryptWithDomain(tPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(tProv)))
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}

		userCtx := storage.WithLocalUserScope(ctx, userID)
		sClient, err := storage.NewProvider(userCtx, sProv, sURL, sUser, sPass)
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}
		tClient, err := storage.NewProvider(userCtx, tProv, tURL, tUser, tPass)
		if err != nil {
			sClient.Close()
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}

		connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
		sOK, _ := sClient.Connect(connCtx)
		tOK, _ := tClient.Connect(connCtx)
		connCancel()
		sClient.Close()
		tClient.Close()

		if sOK && tOK {
			// Workers only release the job. The API scheduler is the sole owner of
			// sync-pass coordinators, avoiding one engine per worker and duplicate
			// recovery passes across workers/API replicas.
			recovered, recoverErr := db.RecoverConnectionLostSyncJob(p.db, ctx, id)
			if recoverErr != nil {
				processorLogf("[RecoveryScheduler] Error recovering sync job %s: %v\n", id, recoverErr)
				continue
			}
			if recovered {
				processorLogf("[RecoveryScheduler] Connection restored for sync job %s; scheduled API retry\n", id)
			}
			p.recoveryAttempts.Delete(id)
		} else {
			p.recordRecoveryFailure(id, ra.attempts)
		}
	}
}

// pruneEmptyParentDirectories recursively checks parent directories of a deleted file and removes any that are empty,
// provided they also no longer exist on the other storage provider (e.g. after a directory rename/delete).
func pruneEmptyParentDirectories(ctx context.Context, client, otherClient storage.StorageProvider, resourceType, filePath, stopDir, otherStopDir string) {
	stopDir = path.Clean(stopDir)
	otherStopDir = path.Clean(otherStopDir)
	currDir := path.Clean(path.Dir(filePath))

	for currDir != "/" && currDir != "." && currDir != stopDir {
		items, err := client.GetDirectoryListing(ctx, resourceType, currDir)
		if err != nil || len(items) > 0 {
			// Stop pruning if directory still contains items or listing fails
			break
		}

		// Check if this directory still exists on the opposing storage provider
		if otherClient != nil {
			relDir := currDir
			if stopDir != "/" && stopDir != "." {
				relDir = strings.TrimPrefix(currDir, stopDir)
			}
			otherDir := path.Clean(path.Join(otherStopDir, relDir))
			if res, err := otherClient.InspectResource(ctx, resourceType, otherDir); err == nil && res.IsDir {
				// The directory STILL EXISTS on the other side (user only deleted files, kept folder)! Do not prune!
				break
			}
		}

		// Directory is completely empty AND does not exist on the other side! Prune it.
		if err := client.DeleteFile(ctx, resourceType, currDir); err != nil {
			break
		}
		processorLogf("[SyncTask] Pruned empty parent directory %s (no longer on other side)\n", currDir)
		currDir = path.Clean(path.Dir(currDir))
	}
}
