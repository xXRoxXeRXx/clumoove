package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

// Indexer performs the indexing phase of a migration: it connects to the source,
// walks the selected paths/calendars/contacts, and creates PENDING tasks in the DB.
// It is safe to call from both the API (immediate start) and the scheduler (deferred start).
type Indexer struct {
	db            *sql.DB
	encryptionKey string
}

// NewIndexer creates a new Indexer instance
func NewIndexer(database *sql.DB, encryptionKey string) *Indexer {
	return &Indexer{
		db:            database,
		encryptionKey: encryptionKey,
	}
}

// Start indexes the migration identified by migID. It reads the persisted
// selected_paths/calendars/contacts from the migration row, decrypts the source
// credentials at the last moment, and creates PENDING tasks. On any failure it
// marks the migration FAILED with a descriptive error message.
func (idx *Indexer) Start(serverCtx context.Context, migID string) {
	ctx, cancel := context.WithTimeout(serverCtx, indexingTimeout())
	defer cancel()

	// Transition status to INDEXING before starting work. This is essential for
	// scheduled migrations (created as SCHEDULED) so the UI and overlap protection
	// correctly reflect that indexing is actively in progress. For immediate starts
	// the migration is already INDEXING, so this is a no-op.
	if err := db.UpdateMigrationStatusIfIndexing(idx.db, migID, "INDEXING"); err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to set indexing status: %v", err))
		return
	}

	// Load migration from DB (includes persisted selected paths)
	mig, err := db.GetMigration(idx.db, migID)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to fetch migration: %v", err))
		return
	}

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
	defer crypto.ZeroString(&sourcePass)

	// Reuse the Google Photos Picker session the user selected their media in, so
	// the indexer enumerates exactly the items the user picked. Picker sessions
	// are short-lived, so when indexing runs much later than the selection (e.g.
	// for a scheduled migration) the session may have expired; indexGooglePhotos
	// transparently creates a fresh session with the current token in that case.
	var gpSource *storage.GooglePhotosProvider
	if mig.SourceProvider == "googlephotos" {
		if gp, ok := sourceClient.(*storage.GooglePhotosProvider); ok {
			gpSource = gp
			if mig.PickerSessionID != "" {
				gp.SetPickerSession(mig.PickerSessionID)
			}
		}
	}

	var totalFiles int
	var totalBytes int64
	indexErrors := make([]db.IndexingErrorInput, 0)
	indexedPaths := make(map[string]bool)

	paths := mig.SelectedPaths
	calendars := mig.SelectedCalendars
	contacts := mig.SelectedContacts

	// 1. Index files
	for _, p := range paths {
		// Google Photos as a Picker source: the user selected individual media
		// items in the Picker UI, so we enumerate the Picker session directly
		// instead of walking a folder tree (the Library API no longer exposes a
		// read scope for the whole library). The session may have expired by the
		// time we index, so indexGooglePhotosPicker retries with a fresh session.
		if mig.SourceProvider == "googlephotos" && gpSource != nil {
			if err := idx.indexGooglePhotosPicker(migID, gpSource, paths, ctx, sourceClient, &totalFiles, &totalBytes, indexedPaths, &indexErrors); err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Indexing Google Photos selection failed: %v", err))
				return
			}
			continue
		}

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
			err = indexFolder(ctx, idx.db, sourceClient, "files", p, migID, &totalFiles, &totalBytes, indexedPaths, &indexErrors)
			if err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Indexing folder %s failed: %v", p, err))
				return
			}
		} else {
			// Single file
			key := fmt.Sprintf("files:%s", p)
			if indexedPaths[key] {
				continue
			}
			indexedPaths[key] = true
			hashVal := res.Hash
			metaJSON, err := json.Marshal(storage.FileMetadata{
				ModifiedTime: res.LastModified,
				Description:  res.Metadata.Description,
			})
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
	for _, p := range calendars {
		err = indexFolder(ctx, idx.db, sourceClient, "calendars", p, migID, &totalFiles, &totalBytes, indexedPaths, &indexErrors)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Indexing calendar %s failed: %v", p, err))
			return
		}
	}

	// 3. Index contacts
	for _, p := range contacts {
		err = indexFolder(ctx, idx.db, sourceClient, "contacts", p, migID, &totalFiles, &totalBytes, indexedPaths, &indexErrors)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Indexing contacts %s failed: %v", p, err))
			return
		}
	}

	// Persist any per-folder indexing errors that were skipped during traversal.
	// Resilient indexing keeps the migration running (partial success) instead of
	// failing the whole migration on a single bad folder.
	if len(indexErrors) > 0 {
		if err := db.RecordIndexingErrors(idx.db, ctx, migID, indexErrors); err != nil {
			log.Printf("Warning: failed to record indexing errors for %s: %v\n", migID, err)
		}
	}

	// Terminal decision: write totals, then decide the final outcome in one place.
	// This avoids two separate totalFiles == 0 branches split by the totals write.
	if err := db.UpdateMigrationTotals(idx.db, migID, totalFiles, totalBytes); err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to update migration totals: %v", err))
		return
	}

	// Re-evaluate completion: tasks may have all finished before totals were written
	if err := db.IncrementMigrationProgress(idx.db, ctx, migID, 0, 0, 0, 0); err != nil {
		log.Printf("Warning: zero-delta progress check after indexing failed for %s: %v\n", migID, err)
	}

	switch {
	case totalFiles == 0 && len(indexErrors) > 0:
		// Nothing was indexed but some folders/paths failed: mark FAILED so the
		// user can re-index (orphaned PENDING tasks are not possible here since
		// none were created; the worker dequeue also filters on migration status).
		failMigration(idx.db, migID, fmt.Sprintf("Indexing failed: %d path(s) could not be read. First error: %s", len(indexErrors), indexErrors[0].ErrorMessage))
		return
	case totalFiles == 0:
		// Every selected path was an empty folder / empty calendar / skipped file.
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

	err = db.UpdateMigrationStatusIfIndexing(idx.db, migID, "RUNNING")
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to set migration running: %v", err))
		return
	}

	log.Printf("Finished indexing migration %s. Total files: %d, Total size: %d bytes.\n", migID, totalFiles, totalBytes)
	if len(indexErrors) > 0 {
		log.Printf("Indexing migration %s completed with %d skipped folder error(s) (see report).\n", migID, len(indexErrors))
	}
}

// indexGooglePhotosPicker enumerates the user's Google Photos Picker selection.
// It first tries the session id persisted on the migration row. Picker sessions
// are short-lived and single-use, so for a deferred/scheduled migration the
// stored session is very likely expired or already consumed by index time. We
// therefore treat BOTH an expired session (ErrPickerSessionExpired) AND an empty
// selection from a *stored* session as suspicious and transparently retry with a
// freshly minted session exactly once. This keeps immediate migrations fast while
// making scheduled/retried migrations robust without requiring the user to
// re-open the Picker UI.
func (idx *Indexer) indexGooglePhotosPicker(migID string, gp *storage.GooglePhotosProvider, selectedPaths []string, ctx context.Context, client storage.StorageProvider, totalFiles *int, totalBytes *int64, indexedPaths map[string]bool, indexErrors *[]db.IndexingErrorInput) error {
	sessionID := gp.PickerSessionID()
	if sessionID == "" {
		return fmt.Errorf("google photos picker session id is missing from the migration")
	}

	err := indexPickerSource(idx.db, ctx, client, migID, sessionID, selectedPaths, totalFiles, totalBytes, indexedPaths, indexErrors)
	if err == nil {
		return nil
	}
	if !errors.Is(err, storage.ErrPickerSessionExpired) && !errors.Is(err, errEmptyPickerSelection) {
		return err
	}

	// Stored session expired or returned nothing (likely consumed) — mint a
	// fresh session with the current (already-refreshed) OAuth token and retry
	// exactly once. A genuinely empty fresh selection is reported as an error.
	fresh, cerr := gp.CreatePickerSession(ctx)
	if cerr != nil {
		return fmt.Errorf("failed to refresh expired google photos picker session: %w", cerr)
	}
	refreshAndUseFresh(idx.db, migID, gp, fresh)

	if rerr := indexPickerSource(idx.db, ctx, client, migID, fresh, selectedPaths, totalFiles, totalBytes, indexedPaths, indexErrors); rerr != nil {
		if errors.Is(rerr, errEmptyPickerSelection) {
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         "/picker",
				ResourceType: "files",
				ErrorMessage: "no media items were selected in the Google Photos Picker",
			})
			return nil
		}
		return rerr
	}
	return nil
}

// refreshAndUseFresh records a freshly minted Picker session on the provider and
// (best-effort) persists it on the migration row so a deferred re-index can find
// it. Persistence failures are non-fatal: the in-memory provider already uses the
// new session for the current run.
func refreshAndUseFresh(database *sql.DB, migID string, gp *storage.GooglePhotosProvider, fresh string) {
	gp.SetPickerSession(fresh)
	if perr := db.UpdateMigrationPickerSession(database, migID, fresh); perr != nil {
		log.Printf("Indexing: could not persist refreshed picker session for %s: %v", migID, perr)
	}
}

// errEmptyPickerSelection is returned by indexPickerSource when the Picker
// session yields no media items. It is a sentinel the caller uses to decide
// whether to retry with a freshly minted session before concluding the
// selection is genuinely empty.
var errEmptyPickerSelection = errors.New("google photos picker selection is empty")

// indexPickerSource enumerates the media items the user selected in a Google
// Photos Picker session and creates one PENDING task per item. Each task's
// FilePath is a self-describing Picker path (`/picker/<id><ext>?base_url=...`)
// so the processor can recover the download URL at transfer time. It mirrors
// the resilient dedup/error behaviour of indexFolder.
func indexPickerSource(database *sql.DB, ctx context.Context, client storage.StorageProvider, migID, sessionID string, selectedPaths []string, totalFiles *int, totalBytes *int64, indexedPaths map[string]bool, indexErrors *[]db.IndexingErrorInput) error {
	// When the migration carries explicit selected_paths (the PickerPaths the
	// user ticked in the UI), those paths are self-describing transport handles
	// that already embed the download base_url + mime, so we build the tasks
	// directly from them. We do NOT re-enumerate the Picker session: a session is
	// short-lived and single-use, so by index time it is usually expired or was
	// already consumed — re-enumerating would yield a fresh, EMPTY session and
	// migrate nothing. The selected_paths are the only reliable source of truth.
	if len(selectedPaths) > 0 {
		for _, filePath := range selectedPaths {
			if !storage.IsPickerPath(filePath) {
				*indexErrors = append(*indexErrors, db.IndexingErrorInput{
					Path:         filePath,
					ResourceType: "files",
					ErrorMessage: "skipped non-picker path in google photos selection",
				})
				continue
			}
			key := "files:" + filePath
			if indexedPaths[key] {
				continue
			}
			indexedPaths[key] = true
			mediaID, baseURL, perr := storage.ParsePickerPath(filePath)
			if perr != nil {
				*indexErrors = append(*indexErrors, db.IndexingErrorInput{
					Path:         filePath,
					ResourceType: "files",
					ErrorMessage: "failed to parse picker path: " + sanitizeError(perr.Error()),
				})
				log.Printf("Indexing: skipping unparseable picker path %s: %v", filePath, perr)
				continue
			}
			handle := storage.PickerHandle{
				ID:      mediaID,
				BaseURL: baseURL,
				Mime:    storage.PickerMimeFromPath(filePath),
				Name:    storage.PickerTargetName(filePath),
			}
			metaJSON, err := json.Marshal(handle)
			if err != nil {
				metaJSON = []byte("{}")
			}
			task := &db.Task{
				MigrationID:  migID,
				ResourceType: "files",
				FilePath:     filePath,
				Status:       "PENDING",
				Metadata:     metaJSON,
			}
			if _, err := db.CreateTask(database, task); err != nil {
				*indexErrors = append(*indexErrors, db.IndexingErrorInput{
					Path:         filePath,
					ResourceType: "files",
					ErrorMessage: "failed to create task: " + sanitizeError(err.Error()),
				})
				log.Printf("Indexing: skipping picker item %s (failed to create task): %v", filePath, err)
				continue
			}
			*totalFiles++
		}
		return nil
	}

	// No explicit selection: migrate the whole session (legacy behaviour for
	// migrations started before per-item selection existed).
	items, err := storage.GetPickerMediaItems(ctx, client, sessionID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errEmptyPickerSelection
	}

	for _, item := range items {
		filePath := storage.PickerPath(item)
		key := "files:" + filePath
		if indexedPaths[key] {
			continue
		}
		indexedPaths[key] = true
		handle := storage.PickerHandle{
			ID:      item.ID,
			BaseURL: item.BaseURL,
			Mime:    item.MimeType,
			Name:    item.Name,
		}
		metaJSON, err := json.Marshal(handle)
		if err != nil {
			metaJSON = []byte("{}")
		}
		task := &db.Task{
			MigrationID:  migID,
			ResourceType: "files",
			FilePath:     filePath,
			FileSize:     item.Size,
			Status:       "PENDING",
			Metadata:     metaJSON,
		}
		if _, err := db.CreateTask(database, task); err != nil {
			*indexErrors = append(*indexErrors, db.IndexingErrorInput{
				Path:         filePath,
				ResourceType: "files",
				ErrorMessage: "failed to create task: " + sanitizeError(err.Error()),
			})
			log.Printf("Indexing: skipping picker item %s (failed to create task): %v", filePath, err)
			continue
		}
		*totalFiles++
		*totalBytes += item.Size
	}
	return nil
}

// ensureFreshSourceToken refreshes an OAuth source access token if it is expired
// or near expiry (mirroring the worker's inline refresh). It returns the freshly
// decrypted access token and persists the new token pair atomically.
func (idx *Indexer) ensureFreshSourceToken(migID string, mig *db.Migration, accessToken string) (string, error) {
	if !mig.SourceTokenExpiresAt.Valid || time.Now().Before(mig.SourceTokenExpiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}
	refreshToken, err := crypto.Decrypt(mig.SourceRefreshTokenEncrypted.String, idx.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt source refresh token: %w", err)
	}
	tokenResp, err := oauth.RefreshToken(context.Background(), mig.SourceProvider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for source (%s): %w", mig.SourceProvider, err)
	}
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
	if err := db.UpdateMigrationOAuthTokens(idx.db, db.OAuthTokenUpdate{
		MigrationID:           migID,
		Role:                  "source",
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             time.Now().Add(time.Duration(expiresIn) * time.Second),
	}); err != nil {
		return "", fmt.Errorf("failed to persist refreshed source tokens: %w", err)
	}
	return tokenResp.AccessToken, nil
}
//
// Resilient indexing: a failure to list a single folder (e.g. a slow/stalled
// WebDAV PROPFIND that hits the per-request timeout) is recorded in indexErrors
// and skipped, so the rest of the tree keeps being indexed instead of aborting
// the whole migration. If the overall indexing context is cancelled (deadline or
// shutdown) traversal stops gracefully after recording a single interrupted error.
func indexFolder(ctx context.Context, database *sql.DB, client storage.StorageProvider, resourceType string, startPath string, migID string, totalFiles *int, totalBytes *int64, indexedPaths map[string]bool, indexErrors *[]db.IndexingErrorInput) error {
	queue := []string{startPath}
	visited := make(map[string]bool)
	visited[startPath] = true

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
				if !visited[file.Path] {
					visited[file.Path] = true
					queue = append(queue, file.Path)
				}
			} else {
				key := fmt.Sprintf("%s:%s", resourceType, file.Path)
				if indexedPaths[key] {
					continue
				}
				indexedPaths[key] = true
				metaJSON, err := json.Marshal(storage.FileMetadata{
					ModifiedTime: file.LastModified,
					Description:  file.Metadata.Description,
				})
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
				if _, err := db.CreateTask(database, task); err != nil {
					// A single DB hiccup must not abort the whole index: record and skip.
					*indexErrors = append(*indexErrors, db.IndexingErrorInput{
						Path:         file.Path,
						ResourceType: resourceType,
						ErrorMessage: "failed to create task: " + sanitizeError(err.Error()),
					})
					log.Printf("Indexing: skipping file %s (failed to create task): %v", file.Path, err)
					continue
				}
				*totalFiles++
				*totalBytes += file.Size
			}
		}
	}
	return nil
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
// before being persisted or returned to the client.
var credURLRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s:@]+:[^/\s:@]+@`)

// credQueryRe matches credential-bearing URL query values so they are redacted
// before being persisted or returned to the client. This covers:
//   - base_url=… : the short-lived, bearer-authenticated Google Photos Picker
//     download URL that is embedded verbatim into task FilePath values;
//   - access_token=… / token=… : OAuth tokens that may leak into error strings.
// The value (everything up to the next & or end of string) is replaced with a
// redaction marker so the host/path diagnostics remain useful.
var credQueryRe = regexp.MustCompile(`(?i)((?:base_url|access_token|token)=)[^&\s]+`)

// sanitizeError redacts credentials from any URLs embedded in an error message.
// It strips user:pass userinfo (credURLRe) and credential-bearing query values
// (credQueryRe, e.g. the Google Photos Picker base_url) before the message is
// persisted or returned to the client. The rest of the message is left intact so
// operators still get useful diagnostics (host/path and the failure type).
func sanitizeError(msg string) string {
	msg = credURLRe.ReplaceAllString(msg, "${1}***:***@")
	return credQueryRe.ReplaceAllString(msg, "${1}***")
}
