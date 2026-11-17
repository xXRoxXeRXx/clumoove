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
	"time"

	"backend/internal/db"
	"backend/internal/storage"
	"backend/internal/throttle"
)

// syncTransfer is the provider-facing input for one sync upload/download task.
type syncTransfer struct {
	Task           *db.Task
	Source         inspectingTransferSource
	Target         syncTransferTarget
	SourceProvider string
	TargetProvider string
	SourcePath     string
	TargetPath     string
	Throttler      *throttle.MigrationThrottler
	Progress       chan<- int64
}

// executeSyncTransfer copies one file for a sync task and records the stream
// hashes on the task. Task lifecycle persistence stays with the caller.
func executeSyncTransfer(ctx context.Context, tr syncTransfer) error {
	task, srcClient, tgtClient := tr.Task, tr.Source, tr.Target
	srcProvider, tgtProvider := tr.SourceProvider, tr.TargetProvider
	srcPath, tgtPath := tr.SourcePath, tr.TargetPath
	jobThrottler, progressChan := tr.Throttler, tr.Progress

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

	throttledDownloadStream := throttle.NewThrottledReader(downloadCtx, downloadStream, jobThrottler)

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
	} else if srcProvider == "google" || srcProvider == "koofr" {
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
	} else if tgtProvider == "google" || tgtProvider == "koofr" {
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

	sizedReader := newExpectedSizeReader(throttledDownloadStream, task.FileSize)
	hashingReader := io.TeeReader(sizedReader, activeWriter)
	uploadCtx, uploadCancel := context.WithTimeout(ctx, transferDeadline)
	defer uploadCancel()

	var uploadOpts storage.UploadOptions
	if srcRes, err := srcClient.InspectResource(ctx, task.ResourceType, srcPath); err == nil && !srcRes.LastModified.IsZero() {
		uploadOpts.Metadata = storage.FileMetadata{ModifiedTime: srcRes.LastModified}
	}
	if sourceHashStr != "" && sourceAlgo != "ETAG" {
		uploadOpts.Checksum = fmt.Sprintf("%s:%s", sourceAlgo, sourceHashStr)
	}

	uploadReader := io.Reader(hashingReader)
	if task.FileSize <= chunkedUploadThreshold {
		uploadReader = &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
	}
	throttledUploadReader := throttle.NewUploadThrottledReader(uploadCtx, uploadReader, jobThrottler)
	_, err = streamUploadWithOptions(uploadCtx, tgtClient, task.ResourceType, uploadPath, throttledUploadReader, task.FileSize, progressChan, uploadOpts)

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
		exists, targetSize, err := storage.VerifyTargetSizeByResourceID(ctx, tgtClient, task.ResourceType, tgtPath, "")
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

	task.WorkerHash = sql.NullString{String: workerHash, Valid: true}
	task.TargetHash = sql.NullString{String: workerHash, Valid: true}
	if task.ResourceType != "files" || task.IsDirectory() {
		task.ChecksumVerified = true
	} else if tgtClient.VerificationMode() == storage.VerificationSizeOnly {
		// Size was already verified above.
		task.ChecksumVerified = true
	} else if tgtClient.VerificationMode() == storage.VerificationCryptographicHash {
		tryInlineHashVerify(ctx, task, tgtClient, task.ResourceType, tgtPath, "")
	}
	return nil
}
