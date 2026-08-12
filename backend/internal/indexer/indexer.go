package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/observability"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

// Indexer performs the indexing phase of a migration: it connects to the source,
// walks the selected paths/calendars/contacts, and creates PENDING tasks in the DB.
// It is safe to call from both the API (immediate start) and the scheduler (deferred start).
type Indexer struct {
	db            *sql.DB
	encryptionKey string
	queue         *queue.Queue
}

// NewIndexer creates a new Indexer instance
func NewIndexer(database *sql.DB, encryptionKey string, q *queue.Queue) *Indexer {
	return &Indexer{
		db:            database,
		encryptionKey: encryptionKey,
		queue:         q,
	}
}

// Start indexes the migration identified by migID. It reads the persisted
// selected_paths/calendars/contacts from the migration row, decrypts the source
// credentials at the last moment, and creates PENDING tasks. On any failure it
// marks the migration FAILED with a descriptive error message.
func (idx *Indexer) Start(serverCtx context.Context, migID string) {
	ctx, cancel := context.WithTimeout(serverCtx, indexingTimeout())
	defer cancel()
	logger := observability.Logger(ctx).With(
		slog.String("component", "indexer"),
		slog.String("migration_id", migID),
	)
	ctx = observability.WithLogger(ctx, logger)
	claimLost := func(err error) bool {
		if !errors.Is(err, db.ErrMigrationIndexingClaimLost) {
			return false
		}
		// If cancellation won a race with an earlier insert, sweep once more.
		// Guarded inserts ensure this cannot create new orphaned PENDING tasks.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if cancelErr := db.CancelPendingTasksCtx(cleanupCtx, idx.db, migID); cancelErr != nil {
			logger.Warn("indexing_claim_loss_cleanup_failed", observability.Error(cancelErr), slog.String("error_kind", observability.ErrorKind(cancelErr)))
		}
		logger.Info("indexing_claim_lost")
		return true
	}

	// Both callers establish INDEXING before starting this goroutine: immediate
	// migrations are created in INDEXING, and the scheduler atomically claims
	// SCHEDULED migrations first. Keeping that claim outside the goroutine makes
	// a lost scheduled claim observable before its one-shot schedule is retired.

	// Load migration from DB (includes persisted selected paths).
	mig, err := db.GetMigration(idx.db, migID)
	if err != nil {
		logger.Error("indexing_migration_load_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Unable to load migration details.")
		return
	}
	ctx = storage.WithLocalUserScope(ctx, mig.UserID.String)

	// Decrypt source credentials at the last moment. The temporary GCM plaintext
	// buffer is cleared before DecryptWithDomain returns. Release this caller's
	// string reference immediately after provider construction; providers retain
	// only what they require for their own authenticated session.
	sourcePass, err := crypto.DecryptWithDomain(mig.SourcePasswordEncrypted, idx.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.SourceProvider)))
	if err != nil {
		logger.Error("indexing_source_credential_decrypt_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Unable to decrypt source credentials.")
		return
	}
	defer crypto.ZeroString(&sourcePass)

	// For OAuth providers (e.g. googlephotos) the access token may have expired
	// by the time indexing runs (especially for scheduled migrations). Refresh it
	// now so the provider can authenticate at index time. The refreshed token is
	// persisted so the worker does not need to refresh again.
	if mig.SourceRefreshTokenEncrypted.Valid && mig.SourceRefreshTokenEncrypted.String != "" {
		sourcePass, err = idx.ensureFreshSourceToken(ctx, migID, mig, sourcePass)
		if err != nil {
			logger.Error("indexing_source_token_refresh_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			failMigration(ctx, idx.db, migID, "Unable to refresh source credentials.")
			return
		}
	}

	sourceCtx, err := megasecret.WithSession(ctx, mig.SourceProvider, mig.SourceMegaSessionIDEncrypted, mig.SourceMegaMasterKeyEncrypted, idx.encryptionKey)
	if err != nil {
		logger.Error("indexing_source_session_decrypt_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Failed to decrypt source connection session.")
		return
	}
	sourceClient, err := storage.NewProvider(sourceCtx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	crypto.ZeroString(&sourcePass)
	if err != nil {
		// Log the detailed (sanitized) error server-side for diagnostics, but do
		// not persist/leak the raw Go error string to the client (Security ->
		// Error messages). Surface a neutral, user-safe message instead.
		logger.Error("indexing_source_provider_create_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Failed to connect to the source. Please verify the source connection settings.")
		return
	}
	defer sourceClient.Close()
	// Providers whose resource APIs depend on a session-backed filesystem tree
	// (notably MEGA) must be connected before any InspectResource or directory
	// listing call. Some HTTP providers can perform those calls lazily, which
	// previously hid this missing lifecycle step until MEGA was used as source.
	connected, err := sourceClient.Connect(ctx)
	if err != nil || !connected {
		if err != nil {
			logger.Error("indexing_source_connect_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		} else {
			logger.Warn("indexing_source_connect_unsuccessful")
		}
		failMigration(ctx, idx.db, migID, "Failed to connect to the source. Please verify the source connection settings.")
		return
	}
	var totalFiles int
	var totalDirs int
	var totalBytes int64
	indexErrors := make([]db.IndexingErrorInput, 0)
	indexedPaths := make(map[string]bool)

	paths := deduplicateSelectedPaths(mig.SelectedPaths)
	if len(paths) != len(mig.SelectedPaths) {
		logger.Debug("indexing_overlapping_paths_deduplicated", slog.Int("selected_paths", len(mig.SelectedPaths)), slog.Int("paths_to_index", len(paths)))
	}
	calendars := mig.SelectedCalendars
	contacts := mig.SelectedContacts

	// 1. Index files
	for _, p := range paths {
		res, err := sourceClient.InspectResource(ctx, "files", p)
		if err != nil {
			// A single bad file path must not abort the whole migration.
			// Record it as a skipped indexing error and continue, consistent
			// with the resilient-indexing philosophy used in indexFolder.
			indexErrors = append(indexErrors, db.IndexingErrorInput{
				Path:         p,
				ResourceType: "files",
				ErrorMessage: "Unable to inspect selected path.",
			})
			logger.Debug("indexing_path_inspection_skipped", slog.String("path", p), observability.ErrorAttr(err, true), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		if res.IsPersonalVault() {
			indexErrors = append(indexErrors, db.IndexingErrorInput{Path: p, ResourceType: "files", ErrorMessage: "OneDrive Personal Vault cannot be migrated through the API"})
			continue
		}

		if res.IsDir {
			// Emit a mkdir task for the root selected directory itself (unless it
			// is the root "/", which every provider already has). This ensures
			// the directory is created on the target even when it is empty.
			if p != "/" && mig.TargetProvider != "immich" {
				dirKey := fmt.Sprintf("dir:files:%s", p)
				if !indexedPaths[dirKey] {
					indexedPaths[dirKey] = true
					mkdirMeta, _ := json.Marshal(directoryTaskMetadata())
					mkdirTask := &db.Task{
						MigrationID:  migID,
						ResourceType: "files",
						FilePath:     p,
						FileSize:     0,
						Status:       "PENDING",
						Metadata:     mkdirMeta,
					}
					if _, err := db.CreateMigrationTaskWhileIndexing(idx.db, mkdirTask); err != nil {
						if claimLost(err) {
							return
						}
						logger.Error("indexing_directory_task_create_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
						failMigration(ctx, idx.db, migID, "Unable to create indexing tasks.")
						return
					}
					totalDirs++
				}
			}
			err = indexFolder(ctx, idx.db, sourceClient, "files", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				if claimLost(err) {
					return
				}
				logger.Error("indexing_folder_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				failMigration(ctx, idx.db, migID, "Unable to index selected resources.")
				return
			}
		} else {
			// Single file
			if mig.TargetProvider == "immich" && !isImmichMedia(res.Name) {
				continue
			}
			key := resourceIndexKey("files", p)
			if indexedPaths[key] {
				continue
			}
			indexedPaths[key] = true
			hashVal := res.Hash
			meta := res.Metadata
			if meta.ModifiedTime.IsZero() {
				meta.ModifiedTime = res.LastModified
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				metaJSON = []byte("{}")
			}
			task := &db.Task{
				MigrationID:  migID,
				ResourceType: "files",
				FilePath:     p,
				FileSize:     res.Size,
				SourceHash:   sql.NullString{String: hashVal, Valid: hashVal != ""},
				Status:       "PENDING",
				Metadata:     metaJSON,
			}
			if _, err := db.CreateMigrationTaskWhileIndexing(idx.db, task); err != nil {
				if claimLost(err) {
					return
				}
				logger.Error("indexing_task_create_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				failMigration(ctx, idx.db, migID, "Unable to create indexing tasks.")
				return
			}
			totalFiles++
			totalBytes += res.Size
		}
	}

	// 2. Index calendars
	if len(calendars) > 0 && storage.ProviderSupportsResourceType(mig.SourceProvider, "calendars") && storage.ProviderSupportsResourceType(mig.TargetProvider, "calendars") {
		for _, p := range calendars {
			err = indexFolder(ctx, idx.db, sourceClient, "calendars", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				if claimLost(err) {
					return
				}
				logger.Error("indexing_calendar_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				failMigration(ctx, idx.db, migID, "Unable to index selected resources.")
				return
			}
		}
	} else if len(calendars) > 0 {
		logger.Info("indexing_calendars_skipped_unsupported", slog.String("source_provider", mig.SourceProvider), slog.String("target_provider", mig.TargetProvider))
		for _, p := range calendars {
			indexErrors = append(indexErrors, db.IndexingErrorInput{
				Path:         p,
				ResourceType: "calendars",
				ErrorMessage: fmt.Sprintf("resource type calendars not supported by source %s or target %s", mig.SourceProvider, mig.TargetProvider),
			})
		}
	}

	// 3. Index contacts
	if len(contacts) > 0 && storage.ProviderSupportsResourceType(mig.SourceProvider, "contacts") && storage.ProviderSupportsResourceType(mig.TargetProvider, "contacts") {
		for _, p := range contacts {
			err = indexFolder(ctx, idx.db, sourceClient, "contacts", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				if claimLost(err) {
					return
				}
				logger.Error("indexing_contacts_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				failMigration(ctx, idx.db, migID, "Unable to index selected resources.")
				return
			}
		}
	} else if len(contacts) > 0 {
		logger.Info("indexing_contacts_skipped_unsupported", slog.String("source_provider", mig.SourceProvider), slog.String("target_provider", mig.TargetProvider))
		for _, p := range contacts {
			indexErrors = append(indexErrors, db.IndexingErrorInput{
				Path:         p,
				ResourceType: "contacts",
				ErrorMessage: fmt.Sprintf("resource type contacts not supported by source %s or target %s", mig.SourceProvider, mig.TargetProvider),
			})
		}
	}

	// Persist any per-folder indexing errors that were skipped during traversal.
	// Resilient indexing keeps the migration running (partial success) instead of
	// failing the whole migration on a single bad folder.
	if len(indexErrors) > 0 {
		if err := db.RecordIndexingErrors(ctx, idx.db, migID, indexErrors); err != nil {
			logger.Warn("indexing_errors_record_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		}
	}

	// Terminal decision: write totals, then decide the final outcome in one place.
	// total_files includes both file tasks AND mkdir tasks so the progress bar
	// correctly counts directory creation as work items.
	totalItems := totalFiles + totalDirs
	if err := db.UpdateMigrationTotals(idx.db, migID, totalItems, totalBytes); err != nil {
		logger.Error("indexing_totals_update_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Unable to finalize indexing totals.")
		return
	}

	switch {
	case totalItems == 0 && len(indexErrors) > 0:
		// Nothing was indexed but some folders/paths failed: mark FAILED so the
		// user can re-index (orphaned PENDING tasks are not possible here since
		// none were created; the worker dequeue also filters on migration status).
		failMigration(ctx, idx.db, migID, "No selected resources could be indexed.")
		return
	case totalItems == 0:
		// Every selected path was an empty folder / empty calendar / skipped file
		// AND no mkdir tasks were created (e.g. root "/" was the only selection).
		if err := db.TransitionMigrationIndexingToCompleted(idx.db, migID); err != nil {
			if claimLost(err) {
				return
			}
			logger.Error("indexing_completion_transition_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			failMigration(ctx, idx.db, migID, "Unable to finalize migration.")
			return
		}
		if owner, oerr := db.GetMigrationOwnerID(idx.db, migID); oerr == nil {
			db.WriteAuditLog(idx.db, db.AuditEntry{
				UserID:  sql.NullString{String: owner, Valid: true},
				Action:  db.AuditMigrationCompleted,
				Target:  migID,
				Details: json.RawMessage(`{"phase":"indexing","files":0}`),
			})
		} else {
			logger.Warn("indexing_completion_audit_owner_load_failed", observability.Error(oerr), slog.String("error_kind", observability.ErrorKind(oerr)))
		}
		logger.Info("indexing_completed_empty")
		return
	}

	err = db.TransitionMigrationIndexingToRunning(idx.db, migID)
	if err != nil {
		if claimLost(err) {
			return
		}
		logger.Error("indexing_running_transition_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		failMigration(ctx, idx.db, migID, "Unable to finalize migration.")
		return
	}
	// Workers may have completed every task while indexing was still producing
	// the migration's task list. Reconcile only after the guarded transition so
	// that this final check may safely advance RUNNING to a terminal state.
	if err := db.ReconcileMigrationProgress(idx.db, migID); err != nil {
		logger.Warn("indexing_progress_reconcile_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
	}

	// Wake idle worker threads immediately so they start picking up the freshly
	// created PENDING tasks without waiting for the fallback poll cycle.
	if idx.queue != nil {
		idx.queue.NotifyTaskAvailable(ctx, idx.db)
	}

	logger.Info("indexing_completed", slog.Int("total_files", totalFiles), slog.Int("total_directories", totalDirs), slog.Int64("total_bytes", totalBytes))
	if len(indexErrors) > 0 {
		logger.Info("indexing_completed_with_skips", slog.Int("skipped_resources", len(indexErrors)))
	}
}

func directoryTaskMetadata() map[string]interface{} {
	return map[string]interface{}{"action": "mkdir"}
}

// ensureFreshSourceToken refreshes an OAuth source access token if it is expired
// or near expiry (mirroring the worker's inline refresh). It returns the freshly
// decrypted access token and persists the new token pair atomically under a
// per-migration distributed lock with CAS update validation.
func (idx *Indexer) ensureFreshSourceToken(parentCtx context.Context, migID string, mig *db.Migration, accessToken string) (string, error) {
	if !mig.SourceTokenExpiresAt.Valid || time.Now().Before(mig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()
	logger := observability.Logger(ctx)
	var lockToken string
	if idx.queue != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = idx.queue.TryClaimOAuthLock(ctx, "migration", migID, "source", 30*time.Second)
			if err == nil && claimed {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if latestMig, lerr := db.GetMigration(idx.db, migID); lerr == nil {
				if latestMig.SourceTokenExpiresAt.Valid && time.Now().Before(latestMig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
					if latestAccess, derr := crypto.DecryptWithDomain(latestMig.SourcePasswordEncrypted, idx.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for migration %s (source)", migID)
		}
		defer func() {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer releaseCancel()
			if err := idx.queue.ReleaseOAuthLock(releaseCtx, "migration", migID, "source", lockToken); err != nil {
				logger.Warn("indexing_source_token_lock_release_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			}
		}()
	}

	// Re-fetch latest migration details inside lock
	if latestMig, err := db.GetMigration(idx.db, migID); err == nil {
		if latestMig.SourceTokenExpiresAt.Valid && time.Now().Before(latestMig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
			if latestAccess, derr := crypto.DecryptWithDomain(latestMig.SourcePasswordEncrypted, idx.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
				return latestAccess, nil
			}
		}
		mig = latestMig
	}

	if !mig.SourceRefreshTokenEncrypted.Valid || mig.SourceRefreshTokenEncrypted.String == "" {
		return accessToken, nil
	}

	refreshToken, err := crypto.DecryptWithDomain(mig.SourceRefreshTokenEncrypted.String, idx.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt source refresh token: %w", err)
	}

	tokenResp, err := oauth.RefreshToken(ctx, mig.SourceProvider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for source (%s): %w", mig.SourceProvider, err)
	}
	newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, idx.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt refreshed source access token: %w", err)
	}
	newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, idx.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt refreshed source refresh token: %w", err)
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	expectedRefreshEnc := mig.SourceRefreshTokenEncrypted.String
	err = db.UpdateMigrationOAuthTokens(idx.db, db.OAuthTokenUpdate{
		MigrationID:           migID,
		Role:                  "source",
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, expectedRefreshEnc)

	if errors.Is(err, db.ErrOAuthTokenConflict) {
		logger.Info("indexing_source_token_update_conflict")
		if latestMig, lerr := db.GetMigration(idx.db, migID); lerr == nil {
			if latestAccess, derr := crypto.DecryptWithDomain(latestMig.SourcePasswordEncrypted, idx.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for migration %s (source): %w", migID, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist refreshed source tokens: %w", err)
	}

	return tokenResp.AccessToken, nil
}

// Resilient indexing: a failure to list a single folder (e.g. a slow/stalled
// WebDAV PROPFIND that hits the per-request timeout) is recorded in indexErrors
// and skipped, so the rest of the tree keeps being indexed instead of aborting
// the whole migration. Database insertion failures are not recoverable partial
// successes: they are returned so the migration is failed rather than receiving
// totals for tasks that were never committed.
func indexFolder(ctx context.Context, database *sql.DB, client storage.StorageProvider, resourceType string, startPath string, migID, targetProvider string, totalFiles *int, totalDirs *int, totalBytes *int64, indexedPaths map[string]bool, indexErrors *[]db.IndexingErrorInput) error {
	queue := []string{startPath}
	head := 0
	visited := make(map[string]bool)
	visited[startPath] = true

	var taskBatch []*db.Task
	var batchFiles, batchDirs int
	var batchBytes int64
	flushBatch := func() error {
		if len(taskBatch) == 0 {
			return nil
		}
		created, err := db.BulkCreateMigrationTasksWhileIndexing(ctx, database, migID, taskBatch)
		if err != nil {
			return fmt.Errorf("create task batch: %w", err)
		}
		if !created {
			return db.ErrMigrationIndexingClaimLost
		}
		*totalFiles += batchFiles
		*totalDirs += batchDirs
		*totalBytes += batchBytes
		clear(taskBatch)
		taskBatch = taskBatch[:0]
		batchFiles = 0
		batchDirs = 0
		batchBytes = 0
		return nil
	}

	for head < len(queue) {
		currentPath := queue[head]
		queue[head] = ""
		head++

		// Stop gracefully if the overall indexing deadline/context was cancelled.
		// Keep whatever was already indexed (partial success) rather than failing.
		// Attribute the interruption to the folder we were about to list.
		if ctx.Err() != nil {
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         currentPath,
				ResourceType: resourceType,
				ErrorMessage: "Indexing interrupted before the folder could be listed.",
			})
			break
		}

		files, err := client.GetDirectoryListing(ctx, resourceType, currentPath)
		if err != nil {
			// Skip this folder (and its subtree) but keep indexing siblings.
			// Persist a neutral, user-safe message. Provider errors can contain
			// credentials or implementation details, so they stay in logs only.
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         currentPath,
				ResourceType: resourceType,
				ErrorMessage: "Unable to list folder.",
			})
			observability.Logger(ctx).Debug("indexing_folder_skipped", slog.String("component", "indexer"), slog.String("migration_id", migID), slog.String("path", currentPath), slog.String("resource_type", resourceType), observability.ErrorAttr(err, true), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}

		for _, file := range files {
			if file.IsPersonalVault() {
				// A vault found while traversing a selected parent is expected and
				// intentionally excluded. Only an explicitly selected vault is
				// reported above, so ordinary root migrations stay clean.
				continue
			}
			if file.IsDir {
				// Emit a mkdir task for every sub-directory encountered so that
				// empty directories (no files inside) are created on the target.
				dirKey := fmt.Sprintf("dir:%s:%s", resourceType, file.Path)
				if targetProvider != "immich" && !indexedPaths[dirKey] {
					indexedPaths[dirKey] = true
					mkdirMeta, _ := json.Marshal(directoryTaskMetadata())
					taskBatch = append(taskBatch, &db.Task{
						MigrationID:  migID,
						ResourceType: resourceType,
						FilePath:     file.Path,
						FileSize:     0,
						Status:       "PENDING",
						Metadata:     mkdirMeta,
					})
					batchDirs++
					if len(taskBatch) >= 500 {
						if err := flushBatch(); err != nil {
							return err
						}
					}
				}
				if !visited[file.Path] {
					visited[file.Path] = true
					queue = append(queue, file.Path)
				}
			} else {
				if targetProvider == "immich" && resourceType == "files" && !isImmichMedia(file.Name) {
					continue
				}
				key := resourceIndexKey(resourceType, file.Path)
				if indexedPaths[key] {
					continue
				}
				indexedPaths[key] = true
				meta := file.Metadata
				if meta.ModifiedTime.IsZero() {
					meta.ModifiedTime = file.LastModified
				}
				metaJSON, err := json.Marshal(meta)
				if err != nil {
					metaJSON = []byte("{}")
				}
				task := &db.Task{
					MigrationID:  migID,
					ResourceType: resourceType,
					FilePath:     file.Path,
					FileSize:     file.Size,
					SourceHash:   sql.NullString{String: file.Hash, Valid: file.Hash != ""},
					Status:       "PENDING",
					Metadata:     metaJSON,
				}
				taskBatch = append(taskBatch, task)
				batchFiles++
				batchBytes += file.Size
				if len(taskBatch) >= 500 {
					if err := flushBatch(); err != nil {
						return err
					}
				}
			}
		}
	}
	// BulkCreateMigrationTasksWhileIndexing deliberately detaches cancellation
	// while applying this final batch, preserving the partial work accumulated
	// before the traversal deadline was reached.
	return flushBatch()
}

// resourceIndexKey returns a unique key for deduplicating resources during indexing.
// Keying by resourceType and filePath ensures each selected virtual folder/album gets its files.
func resourceIndexKey(resourceType, filePath string) string {
	return fmt.Sprintf("%s:%s", resourceType, filePath)
}

// deduplicateSelectedPaths removes repeated and nested file selections before
// traversal. It applies only to files: calendar and contact selections are
// provider-specific collections rather than hierarchical filesystem paths. A
// parent walk already indexes every descendant, so retaining both causes
// avoidable provider listing calls without adding work items.
func deduplicateSelectedPaths(paths []string) []string {
	selected := make([]string, 0, len(paths))
	for _, candidate := range paths {
		candidate = path.Clean(candidate)
		if candidate == "." {
			candidate = "/"
		}

		covered := false
		filtered := selected[:0]
		for _, existing := range selected {
			switch {
			case isSameOrDescendantPath(candidate, existing):
				covered = true
				filtered = append(filtered, existing)
			case isSameOrDescendantPath(existing, candidate):
				// The new parent selection subsumes an earlier child selection.
			default:
				filtered = append(filtered, existing)
			}
		}
		if covered {
			selected = filtered
			continue
		}
		selected = append(filtered, candidate)
	}
	return selected
}

func isSameOrDescendantPath(candidate, parent string) bool {
	if candidate == parent || parent == "/" {
		return true
	}
	return strings.HasPrefix(candidate, strings.TrimSuffix(parent, "/")+"/")
}

// Immich accepts media formats supported by its current MIME registry, including
// common RAW camera formats, but intentionally excludes sidecars and documents.
func isImmichMedia(name string) bool {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	switch ext {
	case "3fr", "3g2", "3gp", "ari", "arw", "avif", "avi", "bay", "bmp", "cap", "cine", "cr2", "cr3", "crw", "dcr", "dcs", "dng", "drf", "eip", "erf", "fff", "flv", "gif", "heic", "heif", "iiq", "jpeg", "jpg", "k25", "kdc", "m4v", "mef", "mjpeg", "mkv", "mos", "mov", "mp4", "mpeg", "mpg", "mrw", "nef", "nrw", "orf", "ori", "pef", "png", "ptx", "pxn", "raf", "raw", "rwl", "rw2", "r3d", "sr2", "srf", "srw", "tif", "tiff", "vob", "webm", "webp", "wmv", "x3f":
		return true
	}
	return false
}

// indexingTimeout returns the maximum allowed duration for a single indexing run.
// Configurable via INDEXING_TIMEOUT_MINUTES (default 20) so large trees are not
// killed by the global deadline.
func indexingTimeout() time.Duration {
	if v := os.Getenv("INDEXING_TIMEOUT_MINUTES"); v != "" {
		if mins, err := strconv.Atoi(v); err == nil && mins > 0 {
			return time.Duration(mins) * time.Minute
		}
	}
	return 20 * time.Minute
}

// failMigration marks a migration as FAILED with the given error message.
// The message is sanitized so connection failures cannot leak URLs with embedded
// credentials into the persisted migration state (AGENTS.md: never forward raw
// err.Error() strings for connection failures to API responses).
func failMigration(ctx context.Context, database *sql.DB, migID string, errMsg string) {
	safe := sanitize.SanitizeError(errMsg)
	logger := observability.Logger(ctx)
	logger.Error("indexing_failed", slog.String("reason", safe))
	failed, err := db.FailMigrationWhileIndexing(database, migID, &safe)
	if err != nil {
		logger.Error("indexing_failure_persist_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	if !failed {
		logger.Info("indexing_failure_claim_lost")
		return
	}
	if owner, oerr := db.GetMigrationOwnerID(database, migID); oerr == nil {
		db.WriteAuditLog(database, db.AuditEntry{
			UserID:  sql.NullString{String: owner, Valid: true},
			Action:  db.AuditMigrationFailed,
			Target:  migID,
			Details: indexingAuditDetails(safe),
		})
	} else {
		logger.Warn("indexing_failure_audit_owner_load_failed", observability.Error(oerr), slog.String("error_kind", observability.ErrorKind(oerr)))
	}
}

func indexingAuditDetails(errMsg string) json.RawMessage {
	details, err := json.Marshal(struct {
		Phase string `json:"phase"`
		Error string `json:"error"`
	}{
		Phase: "indexing",
		Error: errMsg,
	})
	if err != nil {
		return json.RawMessage(`{"phase":"indexing"}`)
	}
	return details
}
