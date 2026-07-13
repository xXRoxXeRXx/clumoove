package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
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

	// Decrypt source credentials at the last moment (Zero Plaintext rule)
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, idx.encryptionKey)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}

	sourceClient, err := storage.NewProvider(ctx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to create storage provider: %v", err))
		return
	}
	defer sourceClient.Close()

	var totalFiles int
	var totalBytes int64
	var taskIDs []string
	indexErrors := make([]db.IndexingErrorInput, 0)
	indexedPaths := make(map[string]bool)

	paths := mig.SelectedPaths
	calendars := mig.SelectedCalendars
	contacts := mig.SelectedContacts

	// 1. Index files
	for _, p := range paths {
		res, err := sourceClient.InspectResource(ctx, "files", p)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Failed to inspect path %s: %v", p, err))
			return
		}

		if res.IsDir {
			err = indexFolder(ctx, idx.db, sourceClient, "files", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths, &indexErrors)
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
			metaJSON, _ := json.Marshal(storage.FileMetadata{
				ModifiedTime: res.LastModified,
				Description:  res.Metadata.Description,
			})
			task := &db.Task{
				MigrationID:  migID,
				ResourceType: "files",
				FilePath:     p,
				FileSize:     res.Size,
				SourceHash:   sql.NullString{String: hashVal, Valid: hashVal != ""},
				Status:       "PENDING",
				Metadata:     metaJSON,
			}
			taskID, err := db.CreateTask(idx.db, task)
			if err != nil {
				failMigration(idx.db, migID, fmt.Sprintf("Failed to create task in DB: %v", err))
				return
			}
			taskIDs = append(taskIDs, taskID)
			totalFiles++
			totalBytes += res.Size
		}
	}

	// 2. Index calendars
	for _, p := range calendars {
		err = indexFolder(ctx, idx.db, sourceClient, "calendars", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths, &indexErrors)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Indexing calendar %s failed: %v", p, err))
			return
		}
	}

	// 3. Index contacts
	for _, p := range contacts {
		err = indexFolder(ctx, idx.db, sourceClient, "contacts", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths, &indexErrors)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Indexing contacts %s failed: %v", p, err))
			return
		}
	}

	// Persist any per-folder indexing errors that were skipped during traversal.
	// Resilient indexing keeps the migration running (partial success) instead of
	// failing the whole migration on a single bad folder.
	if len(indexErrors) > 0 {
		if err := db.RecordIndexingErrors(idx.db, migID, indexErrors); err != nil {
			log.Printf("Warning: failed to record indexing errors for %s: %v\n", migID, err)
		}
	}

	// If nothing at all was indexed (e.g. root path unreachable or every folder
	// failed), mark the migration FAILED so the user can re-index.
	if totalFiles == 0 && len(indexErrors) > 0 {
		failMigration(idx.db, migID, fmt.Sprintf("Indexing failed: %d folder(s) could not be read. First error: %s", len(indexErrors), indexErrors[0].ErrorMessage))
		return
	}

	// Update Totals and status to RUNNING in PostgreSQL
	err = db.UpdateMigrationTotals(idx.db, migID, totalFiles, totalBytes)
	if err != nil {
		failMigration(idx.db, migID, fmt.Sprintf("Failed to update migration totals: %v", err))
		return
	}

	// Re-evaluate completion: tasks may have all finished before totals were written
	if err := db.IncrementMigrationProgress(idx.db, migID, 0, 0, 0, 0); err != nil {
		log.Printf("Warning: zero-delta progress check after indexing failed for %s: %v\n", migID, err)
	}

	if totalFiles == 0 {
		err = db.UpdateMigrationStatus(idx.db, migID, "COMPLETED", nil)
		if err != nil {
			failMigration(idx.db, migID, fmt.Sprintf("Failed to set migration completed: %v", err))
			return
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

// indexFolder walks a directory/collection recursively using BFS with a visited
// map to prevent infinite loops on symlink cycles or circular DAVs.
//
// Resilient indexing: a failure to list a single folder (e.g. a slow/stalled
// WebDAV PROPFIND that hits the per-request timeout) is recorded in indexErrors
// and skipped, so the rest of the tree keeps being indexed instead of aborting
// the whole migration. If the overall indexing context is cancelled (deadline or
// shutdown) traversal stops gracefully after recording a single interrupted error.
func indexFolder(ctx context.Context, database *sql.DB, client storage.StorageProvider, resourceType string, startPath string, migID string, totalFiles *int, totalBytes *int64, taskIDs *[]string, indexedPaths map[string]bool, indexErrors *[]db.IndexingErrorInput) error {
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
				metaJSON, _ := json.Marshal(storage.FileMetadata{
					ModifiedTime: file.LastModified,
					Description:  file.Metadata.Description,
				})
				task := &db.Task{
					MigrationID:  migID,
					ResourceType: resourceType,
					FilePath:     file.Path,
					FileSize:     file.Size,
					SourceHash:   sql.NullString{String: file.Hash, Valid: file.Hash != ""},
					Status:       "PENDING",
					Metadata:     metaJSON,
				}
				taskID, err := db.CreateTask(database, task)
				if err != nil {
					// A single DB hiccup must not abort the whole index: record and skip.
					*indexErrors = append(*indexErrors, db.IndexingErrorInput{
						Path:         file.Path,
						ResourceType: resourceType,
						ErrorMessage: "failed to create task: " + sanitizeError(err.Error()),
					})
					log.Printf("Indexing: skipping file %s (failed to create task): %v", file.Path, err)
					continue
				}
				*taskIDs = append(*taskIDs, taskID)
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
	return 60 * time.Minute
}

// failMigration marks a migration as FAILED with the given error message.
// The message is sanitized so connection failures cannot leak URLs with embedded
// credentials into the persisted migration state (AGENTS.md: never forward raw
// err.Error() strings for connection failures to API responses).
func failMigration(database *sql.DB, migID string, errMsg string) {
	safe := sanitizeError(errMsg)
	log.Printf("Migration %s failed during indexing: %s\n", migID, safe)
	_ = db.UpdateMigrationStatus(database, migID, "FAILED", &safe)
}

// credURLRe matches the userinfo portion of a URL (scheme://user:pass@host) so it
// can be redacted. Embedded credentials in connection-error strings are stripped
// before being persisted or returned to the client.
var credURLRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.\-]*://)[^/\s:@]+:[^/\s:@]+@`)

// sanitizeError redacts credentials (user:pass) from any URLs embedded in an
// error message. It leaves the rest of the message intact so operators still get
// useful diagnostics (e.g. host/path and the failure type).
func sanitizeError(msg string) string {
	return credURLRe.ReplaceAllString(msg, "${1}***:***@")
}
