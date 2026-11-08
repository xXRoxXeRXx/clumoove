package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"backend/internal/db"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

// executeMigrationTransfer owns the destination preparation, provider transfer,
// integrity bookkeeping for one claimed task. Terminal persistence belongs to
// the outcome stage.
func (p *Processor) executeMigrationTransfer(ctx context.Context, cancel context.CancelFunc, mig *db.Migration, task *db.Task, sourceClient storage.StorageProvider, targetClient storage.StorageProvider, migrationThrottler *throttle.MigrationThrottler) error {
	// Update task status to RUNNING in DB.
	task.Status = "RUNNING"
	if err := db.UpdateClaimedTaskStatus(p.db, ctx, task); err != nil {
		processorLogf("[Worker %s] Failed to mark task %s RUNNING: %v", p.workerID, task.ID, err)
		return fmt.Errorf("failed to mark task running: %w", err)
	}

	// Skip read-only system or app-generated calendar/contact collections
	if task.ResourceType != "files" && storage.IsSystemOrAppGeneratedPath(task.FilePath) {
		return p.skipMigrationTask(ctx, task, "Skipped read-only system or app-generated collection (SKIP)")
	}
	// Existing jobs may have been indexed before Personal Vault exclusion was
	// introduced. When Graph still exposes its metadata, turn such a task into
	// a deliberate skip instead of retrying an interactive-only vault download.
	if mig.SourceProvider == "onedrive" && task.ResourceType == "files" {
		if resource, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr == nil && resource.IsPersonalVault() {
			return p.skipMigrationTask(ctx, task, "OneDrive Personal Vault cannot be migrated through the API")
		}
	}

	// Handle directory creation tasks (action == "mkdir").
	// These are emitted by the indexer for every directory encountered so that
	// empty directories are created on the target. We skip the full
	// download/upload pipeline and just call CreateDirectory.
	var taskMeta map[string]interface{}
	if task.Metadata != nil {
		if err := json.Unmarshal(task.Metadata, &taskMeta); err != nil {
			processorLogf("[Worker] Failed to parse task metadata for task %s: %v", task.ID, err)
			return fmt.Errorf("failed to parse task metadata: %w", err)
		}
	}
	action, _ := taskMeta["action"].(string)
	if action == "mkdir" || strings.HasSuffix(task.FilePath, "/") {
		targetPath := task.FilePath
		if task.ResourceType == "files" {
			targetPath = ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)

			// Check for case collisions on case-insensitive providers
			if sanitize.IsCaseInsensitive(mig.TargetProvider) {
				dirPath := path.Dir(targetPath)
				dirName := path.Base(targetPath)
				if collision, _ := sanitize.CheckCaseCollision(ctx, targetClient, task.ResourceType, dirPath, dirName); collision != "" {
					processorLogf("[Worker] Directory case collision detected: %s conflicts with %s", targetPath, collision)
					return p.skipMigrationDirectoryTask(ctx, task, fmt.Sprintf("Directory skipped due to case collision with %s", collision))
				}
			}
		}
		if err := targetClient.CreateDirectory(ctx, task.ResourceType, targetPath); err != nil {
			return fmt.Errorf("failed to create directory %s on target: %w", targetPath, err)
		}
		return p.completeMigrationDirectoryTask(ctx, mig, task)
	}

	// 3. Conflict Resolution
	var deleteAfterUpload bool // set true by OVERWRITE: delete original only after upload succeeds
	targetPath := task.FilePath
	targetPathPersisted := task.ResolvedTargetPath != ""
	if task.ResourceType == "files" && targetPathPersisted {
		targetPath = task.ResolvedTargetPath
	} else if task.ResourceType == "files" {
		targetPath = effectiveTargetPath(task, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
	}

	// Synchronize parallel worker threads operating on the exact same target path
	unlockTarget := p.lockTargetFile(targetPath)
	defer unlockTarget()

	// 3a. Filename Sanitization (before conflict resolution)
	if task.ResourceType == "files" && !targetPathPersisted && mig.TargetProvider != "immich" {
		result := sanitize.SanitizeFilename(path.Base(targetPath), mig.TargetProvider)
		if result.Changed {
			dir := path.Dir(targetPath)
			targetPath = path.Join(dir, result.SanitizedName)
			processorLogf("[SANITIZE] %s: \"%s\" → \"%s\" (%s)",
				task.ID, result.OriginalName, result.SanitizedName, strings.Join(result.Reasons, ", "))
		}

		if sanitize.IsCaseInsensitive(mig.TargetProvider) {
			collision, err := sanitize.CheckCaseCollision(ctx, targetClient, task.ResourceType,
				path.Dir(targetPath), path.Base(targetPath))
			if err != nil {
				processorLogf("Warning: case collision check failed: %v", err)
			} else if collision != "" {
				resolved, err := sanitize.ResolveCollision(ctx, targetClient, task.ResourceType,
					path.Dir(targetPath), path.Base(targetPath), mig.TargetProvider)
				if err != nil {
					return fmt.Errorf("failed to resolve case collision: %w", err)
				}
				targetPath = path.Join(path.Dir(targetPath), resolved)
				processorLogf("[COLLISION] %s: case collision with \"%s\" → \"%s\"",
					task.ID, collision, path.Base(targetPath))
			}
		}
	}

	// Directory tasks are queued independently and can run after a file task.
	// Ensure the destination hierarchy here as well, so parallel scheduling
	// cannot make an upload fail merely because its parent has not been created.
	if task.ResourceType == "files" {
		if err := targetClient.CreateParentDirectories(ctx, task.ResourceType, targetPath); err != nil {
			return fmt.Errorf("failed to create target directories: %w", err)
		}
	}

	// The database and API allow only SKIP, OVERWRITE, and RENAME, but retain
	// this guard for legacy rows and direct database writes. Falling through to
	// an upload would overwrite an existing WebDAV target via PUT.
	if !db.ValidConflictStrategy(mig.ConflictStrategy) {
		return fmt.Errorf("invalid migration conflict strategy %q", mig.ConflictStrategy)
	}

	_, nativeDuplicates := targetClient.(storage.NativeDuplicateDetector)
	if targetPathPersisted {
		// A prior attempt selected this exact destination. Re-running conflict
		// resolution would turn a retry into another rename once the first
		// attempt has created the target.
	} else if nativeDuplicates {
		// Immich determines duplicates from its native asset identity/checksum;
		// filename preflight would be both inaccurate and unsafe.
	} else if task.ResourceType == "files" && mig.ConflictStrategy == "OVERWRITE" {
		// Optimization: for OVERWRITE on files, bypass the pre-flight FileExists
		// network query (PROPFIND/HEAD) since the file will be overwritten regardless.
		deleteAfterUpload = true
	} else {
		exists, _, err := targetClient.FileExists(ctx, task.ResourceType, targetPath)
		if err != nil {
			if isWebDAVSystemConflict(err) {
				return p.skipMigrationTask(ctx, task, fmt.Sprintf("Target file existence check skipped (WebDAV system conflict): %v", err))
			}
			return fmt.Errorf("failed to check if target file exists: %w", err)
		}

		if exists {
			// Calendars and contacts are always overwritten: they are dynamic data and
			// a SKIP would silently leave stale entries from a previous failed run.
			if task.ResourceType != "files" {
				err = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
				if err != nil {
					if isWebDAVSystemConflict(err) {
						return p.skipMigrationTask(ctx, task, fmt.Sprintf("Skipped calendar/contact entry: %v", err))
					}
					return fmt.Errorf("failed to delete existing calendar/contact entry for overwrite: %w", err)
				}
			} else {
				switch mig.ConflictStrategy {
				case "SKIP":
					// An existing target must never be overwritten under SKIP,
					// including after a create-only HiDrive POST reports a race.
					return p.skipMigrationTask(ctx, task, "File already exists in target (SKIP)")

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
							if isWebDAVSystemConflict(err) {
								return p.skipMigrationTask(ctx, task, fmt.Sprintf("Target rename candidate check skipped (WebDAV system conflict): %v", err))
							}
							return fmt.Errorf("failed to check existence of rename candidate: %w", err)
						}
						if !candidateExists {
							targetPath = candidatePath
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
	}

	if task.ResourceType == "files" && !targetPathPersisted {
		// Persist the fully resolved destination before any upload. file_path is
		// deliberately immutable: retries must continue to download it from the
		// source while reusing this chosen target, and deferred verification must
		// not apply TargetDir a second time.
		if err := db.UpdateClaimedTaskResolvedTargetPath(p.db, ctx, task.ID, task.ClaimEpoch, targetPath); err != nil {
			return fmt.Errorf("persist resolved target path: %w", err)
		}
		task.ResolvedTargetPath = targetPath
	}

	// 4. Download and Upload stream. The provider-facing pipeline is isolated in
	// runTransferCore below; lifecycle, conflict handling, and persistence stay here.
	// Providers without atomic-rename support (e.g. S3, Immich) write the
	// file to its final name during upload, so the ".tmp" suffix must never be
	// applied — otherwise the provider has to strip it itself and the rename
	// step below is skipped anyway. Centralising this here avoids leaking the
	// ".tmp" artefact into logs/task bookkeeping for those providers.
	uploadPath := targetPath
	if useTempThenRename(targetClient, deleteAfterUpload) {
		uploadPath = targetPath + ".tmp"
	}

	progressChan, stopProgress := p.startMigrationProgressReporter(ctx, cancel, mig.ID, task.ID, task.ClaimEpoch)
	defer stopProgress()

	var transferMeta storage.FileMetadata
	if task.Metadata != nil {
		_ = json.Unmarshal(task.Metadata, &transferMeta)
	}
	if transferMeta.ModifiedTime.IsZero() {
		if srcInfo, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr != nil {
			processorLogf("Warning: could not fetch source mtime for %s: %v (timestamp may be inaccurate)", task.FilePath, inspectErr)
		} else {
			transferMeta.ModifiedTime = srcInfo.LastModified
		}
	}
	uploadCtx := storage.WithTransferMetadata(ctx, transferMeta)
	var uploadReceipt *storage.UploadReceipt
	if mig.TargetProvider == "immich" {
		uploadReceipt = &storage.UploadReceipt{}
		uploadCtx = storage.WithUploadReceipt(uploadCtx, uploadReceipt)
	}
	transfer, err := runTransferCore(transferRequest{
		Context:          ctx,
		UploadContext:    uploadCtx,
		Source:           sourceClient,
		Target:           targetClient,
		SourceProvider:   mig.SourceProvider,
		TargetProvider:   mig.TargetProvider,
		ResourceType:     task.ResourceType,
		SourcePath:       task.FilePath,
		TargetPath:       uploadPath,
		VerificationPath: targetPath,
		FileSize:         task.FileSize,
		SourceHash:       task.SourceHash.String,
		Throttler:        migrationThrottler,
		Progress:         progressChan,
		Finalize: func(finalizeCtx context.Context) error {
			if !useTempThenRename(targetClient, deleteAfterUpload) {
				return nil
			}
			return promoteOverwrite(finalizeCtx, targetClient, task.ResourceType, targetPath, uploadPath, overwriteBackupPath(targetPath, task.ID))
		},
		VerificationContext: func() context.Context {
			if uploadReceipt != nil && uploadReceipt.TargetResourceID != "" {
				return storage.WithTargetResourceID(ctx, uploadReceipt.TargetResourceID)
			}
			return ctx
		},
	})
	if err != nil {
		if errors.Is(err, storage.ErrNativeDuplicate) {
			return p.skipMigrationTask(ctx, task, "Asset already exists in Immich; duplicate handled natively (SKIP)")
		}
		if errors.Is(err, storage.ErrDuplicateUID) || isWebDAVSystemConflict(err) {
			return p.skipMigrationTask(ctx, task, fmt.Sprintf("Sabredav/WebDAV target entry skipped (system conflict/forbidden): %v", err))
		}
		return err
	}
	if uploadReceipt != nil {
		if uploadReceipt.TargetResourceID == "" {
			return fmt.Errorf("immich upload completed without target asset ID")
		}
		storage.SetImmichTargetAssetID(&transferMeta, uploadReceipt.TargetResourceID)
		metadata, err := json.Marshal(transferMeta)
		if err != nil {
			return fmt.Errorf("marshal immich target asset metadata: %w", err)
		}
		task.Metadata = metadata
		if err := db.UpdateClaimedTaskMetadata(p.db, ctx, task.ID, task.ClaimEpoch, task.Metadata); err != nil {
			return fmt.Errorf("persist immich target asset ID: %w", err)
		}
	}

	if applier, ok := targetClient.(storage.MetadataApplier); ok {
		if !transferMeta.ModifiedTime.IsZero() || transferMeta.Description != "" {
			if err := applier.ApplyMetadata(ctx, task.ResourceType, targetPath, transferMeta); err != nil {
				if !errors.Is(err, storage.ErrUnsupportedOnPlatform) {
					// Metadata propagation is non-fatal, but must remain visible to
					// operators rather than allowing the task to appear silently complete.
					processorLogf("Warning: metadata propagation failed for task %s (target provider %s): %v", task.ID, mig.TargetProvider, err)
				}
			}
		}
	}

	// File-manager moves reuse the transfer pipeline but must never remove the
	// source until the destination stream has completed successfully. Directory
	// entries are intentionally not removed here: their content may still be
	// queued and an empty directory is safer than a partially removed tree.
	if fileManagerTransferOperation(mig.PickerSessionID) == "move" && task.ResourceType == "files" {
		if err := sourceClient.DeleteFile(ctx, task.ResourceType, task.FilePath); err != nil {
			return fmt.Errorf("delete source after file-manager move: %w", err)
		}
	}

	// 5. Stream Hash Registration & Fast Task Completion
	// Network hash-verification queries against the target provider are omitted during
	// the transfer phase for maximum speed. Full checksum verification is performed
	// post-transfer by the Verifier daemon.
	if task.ResourceType == "files" {
		workerHashAlgo := transfer.SourceAlgorithm
		workerHashVal := formatWorkerHashValue(transfer.SourceAlgorithm, transfer.SourceHasher)
		// When source and target use different algorithms, retain the hash
		// calculated for the bytes written to the target. The verifier promotes
		// this value when it matches the target provider's native algorithm.
		if transfer.TargetHasher != nil {
			workerHashAlgo = transfer.TargetAlgorithm
			workerHashVal = formatWorkerHashValue(transfer.TargetAlgorithm, transfer.TargetHasher)
		}
		workerHash := fmt.Sprintf("%s:%s", workerHashAlgo, workerHashVal)
		task.WorkerHash = sql.NullString{String: workerHash, Valid: true}
		task.TargetHash = sql.NullString{String: workerHash, Valid: true}

		if isDirectoryTask(task) {
			task.ChecksumVerified = true
		} else if targetClient.VerificationMode() == storage.VerificationSizeOnly {
			// Existence and size were already verified by runTransferCore immediately after upload.
			task.ChecksumVerified = true
		} else if targetClient.VerificationMode() == storage.VerificationCryptographicHash {
			// Fast inline verification using the existing connected targetClient
			tryInlineHashVerify(ctx, task, targetClient, task.ResourceType, targetPath)
		}
	} else {
		task.WorkerHash = sql.NullString{String: "DYNAMIC", Valid: true}
		task.TargetHash = sql.NullString{String: "DYNAMIC", Valid: true}
		task.ChecksumVerified = true
	}

	return p.completeMigrationTask(ctx, mig, task)
}
