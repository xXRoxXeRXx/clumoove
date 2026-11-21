package processor

import (
	"context"
	"database/sql"
	"fmt"

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

// executeSyncTransfer copies one file for a sync task through the shared
// transfer core and records the stream hashes on the task. Task lifecycle
// persistence stays with the caller.
//
// Deliberate differences from the former inline implementation: the source
// modification time is read once before the upload and reused for the upload
// options and for metadata; metadata is applied after the target size check;
// size verification uses the upload receipt's resource ID when one is returned;
// and non-fatal metadata failures are logged.
func executeSyncTransfer(ctx context.Context, tr syncTransfer) error {
	task, tgtClient, tgtPath := tr.Task, tr.Target, tr.TargetPath

	// Sync updates may replace an existing destination. Use the same staging
	// decision as migrations so providers whose RenameFile is non-atomic (such
	// as S3's copy-and-delete implementation) upload directly to the final key.
	useTempUpload := useTempThenRename(tgtClient, true)
	uploadPath := tgtPath
	if useTempUpload {
		uploadPath = tgtPath + ".tmp"
	}

	uploadOpts := storage.UploadOptions{Overwrite: true}
	if srcRes, err := tr.Source.InspectResource(ctx, task.ResourceType, tr.SourcePath); err == nil && !srcRes.LastModified.IsZero() {
		uploadOpts.Metadata = storage.FileMetadata{ModifiedTime: srcRes.LastModified}
	}

	req := transferRequest{
		Context:          ctx,
		Source:           tr.Source,
		Target:           tgtClient,
		SourceProvider:   tr.SourceProvider,
		TargetProvider:   tr.TargetProvider,
		ResourceType:     task.ResourceType,
		SourcePath:       tr.SourcePath,
		TargetPath:       uploadPath,
		VerificationPath: tgtPath,
		UploadOptions:    uploadOpts,
		FileSize:         task.FileSize,
		SourceHash:       task.SourceHash.String,
		Throttler:        tr.Throttler,
		Progress:         tr.Progress,
		Metadata:         &syncMetadataPolicy,
	}
	if useTempUpload {
		req.AbortUpload = func(abortCtx context.Context) {
			cleanupStagingUpload(abortCtx, tgtClient, task.ResourceType, uploadPath)
		}
		// Rename the staging object only when the provider guarantees an atomic
		// replacement. Non-atomic providers uploaded directly to tgtPath above.
		req.Finalize = func(finalizeCtx context.Context) error {
			backupPath := overwriteBackupPath(tgtPath, task.ID)
			if err := promoteOverwrite(finalizeCtx, tgtClient, task.ResourceType, tgtPath, uploadPath, backupPath); err != nil {
				return fmt.Errorf("failed to promote temporary upload to target path: %w", err)
			}
			return nil
		}
	}
	transfer, err := runTransferCore(req)
	if err != nil {
		return err
	}

	workerHash := transfer.WorkerHash()
	logNonFatalMetadataError(task.ID, tr.TargetProvider, transfer.MetadataErr)
	task.WorkerHash = sql.NullString{String: workerHash, Valid: true}
	task.TargetHash = sql.NullString{String: workerHash, Valid: true}
	if task.ResourceType != "files" || task.IsDirectory() {
		task.ChecksumVerified = true
	} else if tgtClient.VerificationMode() == storage.VerificationSizeOnly {
		// Size was already verified by the transfer core.
		task.ChecksumVerified = true
	} else if tgtClient.VerificationMode() == storage.VerificationCryptographicHash {
		tryInlineHashVerify(ctx, task, tgtClient, task.ResourceType, tgtPath, "")
	}
	return nil
}
