package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
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

	// Both callers establish INDEXING before starting this goroutine: immediate
	// migrations are created in INDEXING, and the scheduler atomically claims
	// SCHEDULED migrations first. Keeping that claim outside the goroutine makes
	// a lost scheduled claim observable before its one-shot schedule is retired.

	// Load migration from DB (includes persisted selected paths).
	mig, err := db.GetMigration(idx.db, migID)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to fetch migration: %v", err))
		return
	}
	ctx = storage.WithLocalUserScope(ctx, mig.UserID.String)

	// Decrypt source credentials at the last moment (Zero Plaintext rule).
	// The plaintext is scoped to this block and zeroed immediately after the
	// provider is constructed so it does not linger in memory during the
	// (possibly long) BFS traversal.
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, idx.encryptionKey)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}

	// For OAuth providers (e.g. googlephotos) the access token may have expired
	// by the time indexing runs (especially for scheduled migrations). Refresh it
	// now so the provider can authenticate at index time. The refreshed token is
	// persisted so the worker does not need to refresh again.
	if mig.SourceRefreshTokenEncrypted.Valid && mig.SourceRefreshTokenEncrypted.String != "" {
		sourcePass, err = idx.ensureFreshSourceToken(migID, mig, sourcePass)
		if err != nil {
			crypto.ZeroString(&sourcePass)
			failMigration(idx.db, migID, fmt.Sprintf("Failed to refresh source OAuth token: %v", err))
			return
		}
	}

	sourceClient, err := storage.NewProvider(ctx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		crypto.ZeroString(&sourcePass)
		// Log the detailed (sanitized) error server-side for diagnostics, but do
		// not persist/leak the raw Go error string to the client (Security ->
		// Error messages). Surface a neutral, user-safe message instead.
		log.Printf("Migration %s: failed to create source storage provider: %s", migID, sanitizeError(err.Error()))
		failMigration(idx.db, migID, "Failed to connect to the source. Please verify the source connection settings.")
		return
	}
	defer sourceClient.Close()
	var totalFiles int
	var totalDirs int
	var totalBytes int64
	indexErrors := make([]db.IndexingErrorInput, 0)
	indexedPaths := make(map[string]bool)

	paths := mig.SelectedPaths
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
				ErrorMessage: "failed to inspect path: " + sanitizeError(err.Error()),
			})
			log.Printf("Indexing: skipping path %s (failed to inspect): %v", p, err)
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
					mkdirMeta, _ := json.Marshal(directoryTaskMetadata(res.Metadata))
					mkdirTask := &db.Task{
						MigrationID:  migID,
						ResourceType: "files",
						FilePath:     p,
						FileSize:     0,
						Status:       "PENDING",
						Metadata:     mkdirMeta,
					}
					if _, err := db.CreateTask(idx.db, mkdirTask); err != nil {
						failMigration(idx.db, migID, fmt.Sprintf("Failed to create mkdir task for %s: %v", p, err))
						return
					}
					totalDirs++
				}
			}
			err = indexFolder(ctx, idx.db, sourceClient, "files", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Indexing folder %s failed: %v", p, err))
				return
			}
		} else {
			// Single file
			if mig.TargetProvider == "immich" && !isImmichMedia(res.Name) {
				indexErrors = append(indexErrors, db.IndexingErrorInput{Path: p, ResourceType: "files", ErrorMessage: "unsupported Immich media extension: " + path.Ext(res.Name)})
				continue
			}
			key := resourceIndexKey("files", p, res.Metadata)
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
			if _, err := db.CreateTask(idx.db, task); err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Failed to create task in DB: %v", err))
				return
			}
			totalFiles++
			totalBytes += res.Size
		}
	}

	// 2. Index calendars
	if len(calendars) > 0 && storage.ProviderSupportsResourceType(mig.SourceProvider, "calendars") && storage.ProviderSupportsResourceType(mig.TargetProvider, "calendars") {
		for _, p := range calendars {
			// Emit a mkdir task for the root calendar directory itself (unless it
			// is the root "/", which every provider already has). This ensures
			// the directory is created on the target even when it is empty.
			if p != "/" {
				dirKey := fmt.Sprintf("dir:calendars:%s", p)
				if !indexedPaths[dirKey] {
					indexedPaths[dirKey] = true
					mkdirMeta, _ := json.Marshal(map[string]interface{}{"action": "mkdir"})
					mkdirTask := &db.Task{
						MigrationID:  migID,
						ResourceType: "calendars",
						FilePath:     p,
						FileSize:     0,
						Status:       "PENDING",
						Metadata:     mkdirMeta,
					}
					if _, err := db.CreateTask(idx.db, mkdirTask); err != nil {
						failMigration(idx.db, migID, fmt.Sprintf("Failed to create mkdir task for calendar %s: %v", p, err))
						return
					}
					totalDirs++
				}
			}
			err = indexFolder(ctx, idx.db, sourceClient, "calendars", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Indexing calendar %s failed: %v", p, err))
				return
			}
		}
	} else if len(calendars) > 0 {
		log.Printf("Indexing: skipping calendars for migration %s (not supported by source %s or target %s)", migID, mig.SourceProvider, mig.TargetProvider)
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
			// Emit a mkdir task for the root contacts directory itself (unless it
			// is the root "/", which every provider already has). This ensures
			// the directory is created on the target even when it is empty.
			if p != "/" {
				dirKey := fmt.Sprintf("dir:contacts:%s", p)
				if !indexedPaths[dirKey] {
					indexedPaths[dirKey] = true
					mkdirMeta, _ := json.Marshal(map[string]interface{}{"action": "mkdir"})
					mkdirTask := &db.Task{
						MigrationID:  migID,
						ResourceType: "contacts",
						FilePath:     p,
						FileSize:     0,
						Status:       "PENDING",
						Metadata:     mkdirMeta,
					}
					if _, err := db.CreateTask(idx.db, mkdirTask); err != nil {
						failMigration(idx.db, migID, fmt.Sprintf("Failed to create mkdir task for contacts %s: %v", p, err))
						return
					}
					totalDirs++
				}
			}
			err = indexFolder(ctx, idx.db, sourceClient, "contacts", p, migID, mig.TargetProvider, &totalFiles, &totalDirs, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Indexing contacts %s failed: %v", p, err))
				return
			}
		}
	} else if len(contacts) > 0 {
		log.Printf("Indexing: skipping contacts for migration %s (not supported by source %s or target %s)", migID, mig.SourceProvider, mig.TargetProvider)
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
			log.Printf("Warning: failed to record indexing errors for %s: %v\n", migID, err)
		}
	}

	// Terminal decision: write totals, then decide the final outcome in one place.
	// total_files includes both file tasks AND mkdir tasks so the progress bar
	// correctly counts directory creation as work items.
	totalItems := totalFiles + totalDirs
	if err := db.UpdateMigrationTotals(idx.db, migID, totalItems, totalBytes); err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to update migration totals: %v", err))
		return
	}

	switch {
	case totalItems == 0 && len(indexErrors) > 0:
		// Nothing was indexed but some folders/paths failed: mark FAILED so the
		// user can re-index (orphaned PENDING tasks are not possible here since
		// none were created; the worker dequeue also filters on migration status).
		failMigration(idx.db, migID, fmt.Sprintf("Indexing failed: %d path(s) could not be read. First error: %s", len(indexErrors), indexErrors[0].ErrorMessage))
		return
	case totalItems == 0:
		// Every selected path was an empty folder / empty calendar / skipped file
		// AND no mkdir tasks were created (e.g. root "/" was the only selection).
		if err := db.UpdateMigrationStatus(idx.db, migID, "COMPLETED", nil); err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Failed to set migration completed: %v", err))
			return
		}
		if owner, oerr := db.GetMigrationOwnerID(idx.db, migID); oerr == nil {
			db.WriteAuditLog(idx.db, db.AuditEntry{
				UserID:  sql.NullString{String: owner, Valid: true},
				Action:  db.AuditMigrationCompleted,
				Target:  migID,
				Details: json.RawMessage(`{"phase":"indexing","files":0}`),
			})
		}
		log.Printf("Finished indexing migration %s. 0 files to migrate. Marked COMPLETED.\n", migID)
		return
	}

	err = db.TransitionMigrationIndexingToRunning(idx.db, migID)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to set migration running: %v", err))
		return
	}
	// Workers may have completed every task while indexing was still producing
	// the migration's task list. Reconcile only after the guarded transition so
	// that this final check may safely advance RUNNING to a terminal state.
	if err := db.ReconcileMigrationProgress(idx.db, migID); err != nil {
		log.Printf("Warning: final progress reconciliation after indexing failed for %s: %v\n", migID, err)
	}

	// Wake idle worker threads immediately so they start picking up the freshly
	// created PENDING tasks without waiting for the fallback poll cycle.
	if idx.queue != nil {
		idx.queue.NotifyTaskAvailable(ctx, idx.db)
	}

	log.Printf("Finished indexing migration %s. Total files: %d, Total dirs: %d, Total size: %d bytes.\n", migID, totalFiles, totalDirs, totalBytes)
	if len(indexErrors) > 0 {
		log.Printf("Indexing migration %s completed with %d skipped folder error(s) (see report).\n", migID, len(indexErrors))
	}
}

func directoryTaskMetadata(metadata storage.FileMetadata) map[string]interface{} {
	result := map[string]interface{}{"action": "mkdir"}
	if albumName := metadata.CustomProps["immich_album_name"]; albumName != "" {
		result["immich_album_name"] = albumName
	}
	return result
}

// ensureFreshSourceToken refreshes an OAuth source access token if it is expired
// or near expiry (mirroring the worker's inline refresh). It returns the freshly
// decrypted access token and persists the new token pair atomically under a
// per-migration distributed lock with CAS update validation.
func (idx *Indexer) ensureFreshSourceToken(migID string, mig *db.Migration, accessToken string) (string, error) {
	if !mig.SourceTokenExpiresAt.Valid || time.Now().Before(mig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	ctx := context.Background()
	var lockToken string
	if idx.queue != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = idx.queue.TryClaimOAuthLock(ctx, "migration", migID, "source", 30*time.Second)
			if err == nil && claimed {
				break
			}
			time.Sleep(200 * time.Millisecond)
			if latestMig, lerr := db.GetMigration(idx.db, migID); lerr == nil {
				if latestMig.SourceTokenExpiresAt.Valid && time.Now().Before(latestMig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
					if latestAccess, derr := crypto.Decrypt(latestMig.SourcePasswordEncrypted, idx.encryptionKey); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for migration %s (source)", migID)
		}
		defer idx.queue.ReleaseOAuthLock(ctx, "migration", migID, "source", lockToken)
	}

	// Re-fetch latest migration details inside lock
	if latestMig, err := db.GetMigration(idx.db, migID); err == nil {
		if latestMig.SourceTokenExpiresAt.Valid && time.Now().Before(latestMig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
			if latestAccess, derr := crypto.Decrypt(latestMig.SourcePasswordEncrypted, idx.encryptionKey); derr == nil {
				return latestAccess, nil
			}
		}
		mig = latestMig
	}

	if !mig.SourceRefreshTokenEncrypted.Valid || mig.SourceRefreshTokenEncrypted.String == "" {
		return accessToken, nil
	}

	refreshToken, err := crypto.Decrypt(mig.SourceRefreshTokenEncrypted.String, idx.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt source refresh token: %w", err)
	}
	defer crypto.ZeroString(&refreshToken)

	tokenResp, err := oauth.RefreshToken(ctx, mig.SourceProvider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for source (%s): %w", mig.SourceProvider, err)
	}
	defer crypto.ZeroString(&tokenResp.RefreshToken)
	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, idx.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt refreshed source access token: %w", err)
	}
	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, idx.encryptionKey)
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
		log.Printf("[Indexer] Token update conflict for migration %s (source) — adopting winner token from DB\n", migID)
		if latestMig, lerr := db.GetMigration(idx.db, migID); lerr == nil {
			if latestAccess, derr := crypto.Decrypt(latestMig.SourcePasswordEncrypted, idx.encryptionKey); derr == nil {
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
	visited := make(map[string]bool)
	visited[startPath] = true

	var taskBatch []*db.Task
	var batchFiles, batchDirs int
	var batchBytes int64
	flushBatch := func() error {
		if len(taskBatch) == 0 {
			return nil
		}
		if err := db.BulkCreateSyncTasks(ctx, database, taskBatch); err != nil {
			return fmt.Errorf("create task batch: %w", err)
		}
		*totalFiles += batchFiles
		*totalDirs += batchDirs
		*totalBytes += batchBytes
		taskBatch = taskBatch[:0]
		batchFiles = 0
		batchDirs = 0
		batchBytes = 0
		return nil
	}

	for len(queue) > 0 {
		currentPath := queue[0]
		queue = queue[1:]

		// Stop gracefully if the overall indexing deadline/context was cancelled.
		// Keep whatever was already indexed (partial success) rather than failing.
		// Attribute the interruption to the folder we were about to list.
		if ctx.Err() != nil {
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         currentPath,
				ResourceType: resourceType,
				ErrorMessage: "indexing interrupted: " + sanitizeError(ctx.Err().Error()),
			})
			break
		}

		files, err := client.GetDirectoryListing(ctx, resourceType, currentPath)
		if err != nil {
			// Skip this folder (and its subtree) but keep indexing siblings.
			// Sanitize the error so connection failures cannot leak URLs with
			// embedded credentials into the DB / report (AGENTS.md).
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         currentPath,
				ResourceType: resourceType,
				ErrorMessage: sanitizeError(err.Error()),
			})
			log.Printf("Indexing: skipping folder %s (resource=%s): %v", currentPath, resourceType, err)
			continue
		}

		for _, file := range files {
			if file.IsDir {
				// Emit a mkdir task for every sub-directory encountered so that
				// empty directories (no files inside) are created on the target.
				dirKey := fmt.Sprintf("dir:%s:%s", resourceType, file.Path)
				if targetProvider != "immich" && !indexedPaths[dirKey] {
					indexedPaths[dirKey] = true
					mkdirMeta, _ := json.Marshal(directoryTaskMetadata(file.Metadata))
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
					*indexErrors = append(*indexErrors, db.IndexingErrorInput{Path: file.Path, ResourceType: resourceType, ErrorMessage: "unsupported Immich media extension: " + path.Ext(file.Name)})
					continue
				}
				key := resourceIndexKey(resourceType, file.Path, file.Metadata)
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
	return flushBatch()
}

// resourceIndexKey prevents Immich's All Assets and album virtual paths from
// producing duplicate tasks for the same stable asset UUID.
func resourceIndexKey(resourceType, filePath string, meta storage.FileMetadata) string {
	if id := meta.CustomProps["immich_asset_id"]; id != "" {
		return resourceType + ":immich:" + id
	}
	return fmt.Sprintf("%s:%s", resourceType, filePath)
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
func failMigration(database *sql.DB, migID string, errMsg string) {
	safe := sanitizeError(errMsg)
	log.Printf("Migration %s failed during indexing: %s\n", migID, safe)
	_ = db.UpdateMigrationStatus(database, migID, "FAILED", &safe)
	if owner, oerr := db.GetMigrationOwnerID(database, migID); oerr == nil {
		db.WriteAuditLog(database, db.AuditEntry{
			UserID:  sql.NullString{String: owner, Valid: true},
			Action:  db.AuditMigrationFailed,
			Target:  migID,
			Details: json.RawMessage(fmt.Sprintf(`{"phase":"indexing","error":%s}`, marshalString(safe))),
		})
	}
}

// marshalString returns a JSON-encoded string literal (with quotes) so it can be
// inlined into a hand-built JSON detail object.
func marshalString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// credURLRe matches the userinfo portion of a URL (scheme://user:pass@host) so it
// can be redacted. Embedded credentials in connection-error strings are stripped
// sanitizeError redacts credentials from any URLs embedded in an error message.
func sanitizeError(msg string) string {
	return sanitize.SanitizeError(msg)
}


