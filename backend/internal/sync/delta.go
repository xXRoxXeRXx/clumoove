package sync

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

// cleanRelPath normalizes a relative path so that it always starts with a single leading slash
// and has no trailing slash (unless it is the root "/").
func cleanRelPath(p string) string {
	cleaned := path.Clean("/" + p)
	if cleaned == "." || cleaned == "" {
		return "/"
	}
	return cleaned
}

// updateSyncStates aligns sync_state entries with current listings, preserving the old states of failed files.
// Uses BulkUpsertSyncStates to batch all upserts and deletes into a single transaction instead of N individual
// round-trips (one per file), which is dramatically faster for large directory trees.
func (e *Engine) updateSyncStates(
	jobID string,
	sourceMap, targetMap map[string]fileState,
	prevSource, prevTarget map[string]db.SyncState,
	sourceDirETags, targetDirETags map[string]string,
	taskOutcomes map[string]string,
) {
	allKeys := make(map[string]bool)
	for k := range sourceMap {
		allKeys[cleanRelPath(k)] = true
	}
	for k := range targetMap {
		allKeys[cleanRelPath(k)] = true
	}
	for k := range prevSource {
		allKeys[cleanRelPath(k)] = true
	}
	for k := range prevTarget {
		allKeys[cleanRelPath(k)] = true
	}

	var upserts []*db.SyncState
	var deletes []struct{ SyncJobID, Side, RelPath string }

	for S := range allKeys {
		srcFile, hasSrc := sourceMap[S]
		tgtFile, hasTgt := targetMap[S]
		outcome, hasTask := taskOutcomes[S]

		// If a task ran for this file, and it FAILED, do NOT update states (so it gets retried)
		if hasTask && outcome != "COMPLETED" && outcome != "SKIPPED" {
			continue
		}

		cleanKey := cleanRelPath(S)

		// Source side
		if hasSrc {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "source",
				RelPath:    cleanKey,
				Size:       srcFile.Size,
				Mtime:      sql.NullTime{Time: srcFile.LastModified, Valid: !srcFile.LastModified.IsZero()},
				SourceHash: srcFile.Hash,
				TargetHash: srcFile.Hash,
				ETag:       srcFile.ETag,
			})
		} else {
			deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "source", cleanKey})
		}

		// Target side
		if hasTgt {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "target",
				RelPath:    cleanKey,
				Size:       tgtFile.Size,
				Mtime:      sql.NullTime{Time: tgtFile.LastModified, Valid: !tgtFile.LastModified.IsZero()},
				SourceHash: tgtFile.Hash,
				TargetHash: tgtFile.Hash,
				ETag:       tgtFile.ETag,
			})
		} else {
			deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "target", cleanKey})
		}
	}

	// Persist directory ETags with Size: -1
	for dirPath, etag := range sourceDirETags {
		if etag != "" {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "source",
				RelPath:   cleanRelPath(dirPath),
				Size:      -1,
				ETag:      etag,
			})
		}
	}
	for dirPath, etag := range targetDirETags {
		if etag != "" {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "target",
				RelPath:   cleanRelPath(dirPath),
				Size:      -1,
				ETag:      etag,
			})
		}
	}

	if e.db == nil {
		return
	}

	if err := db.BulkUpsertSyncStates(e.db, upserts, deletes); err != nil {
		log.Printf("[SyncEngine] Warning: BulkUpsertSyncStates for job %s failed: %v\n", jobID, err)
	}
}

// listFiles traverses paths recursively using a parallel worker pool and hierarchical ETag folder skipping.
func (e *Engine) listFiles(
	ctx context.Context,
	client storage.StorageProvider,
	startPaths []string,
	prevDirETags map[string]string,
	prevFileStates map[string]fileState,
) (map[string]fileState, map[string]string, []db.IndexingErrorInput, error) {
	fileMap := make(map[string]fileState)
	dirETagMap := make(map[string]string)
	var indexErrors []db.IndexingErrorInput

	var mu sync.Mutex
	var errsMu sync.Mutex

	addFile := func(fs fileState) {
		fs.Path = cleanRelPath(fs.Path)
		mu.Lock()
		fileMap[fs.Path] = fs
		mu.Unlock()
	}

	addDirETag := func(dirPath, etag string) {
		if etag == "" {
			return
		}
		cdir := cleanRelPath(dirPath)
		mu.Lock()
		dirETagMap[cdir] = etag
		mu.Unlock()
	}

	addError := func(path, msg string) {
		errsMu.Lock()
		indexErrors = append(indexErrors, db.IndexingErrorInput{
			Path:         path,
			ResourceType: "files",
			ErrorMessage: msg,
		})
		errsMu.Unlock()
	}

	type listJob struct {
		dirPath string
		etag    string
	}

	jobsChan := make(chan listJob, 100000)
	var wg sync.WaitGroup
	visited := make(map[string]bool)
	var visitedMu sync.Mutex

	enqueueDir := func(dirPath, etag string) {
		cdir := cleanRelPath(dirPath)
		visitedMu.Lock()
		if visited[cdir] {
			visitedMu.Unlock()
			return
		}
		visited[cdir] = true
		visitedMu.Unlock()

		wg.Add(1)
		jobsChan <- listJob{dirPath: dirPath, etag: etag}
	}

	for _, startPath := range startPaths {
		if startPath == "" {
			continue
		}
		res, err := client.InspectResource(ctx, "files", startPath)
		if err != nil {
			addError(startPath, err.Error())
			continue
		}

		if !res.IsDir {
			addFile(fileState{
				Path:         startPath,
				Size:         res.Size,
				LastModified: res.LastModified,
				Hash:         res.Hash,
				ETag:         res.ETag,
			})
			continue
		}

		addDirETag(startPath, res.ETag)
		enqueueDir(startPath, res.ETag)
	}

	type dirETagItem struct {
		path string
		etag string
	}

	numWorkers := 16
	for i := 0; i < numWorkers; i++ {
		go func() {
			for job := range jobsChan {
				func() {
					defer wg.Done()

					if ctx.Err() != nil {
						return
					}

					files, err := client.GetDirectoryListing(ctx, "files", job.dirPath)
					if err != nil {
						addError(job.dirPath, err.Error())
						return
					}

					for _, file := range files {
						cpath := cleanRelPath(file.Path)
						if file.IsDir {
							addDirETag(cpath, file.ETag)
							enqueueDir(file.Path, file.ETag)
						} else {
							addFile(fileState{
								Path:         cpath,
								Size:         file.Size,
								LastModified: file.LastModified,
								Hash:         file.Hash,
								ETag:         file.ETag,
							})
						}
					}
				}()
			}
		}()
	}

	wg.Wait()
	close(jobsChan)

	return fileMap, dirETagMap, indexErrors, nil
}

// isFileModified determines whether a file has changed compared to its stored SyncState.
func isFileModified(curr fileState, prev db.SyncState, isSource bool) bool {
	if curr.Size != prev.Size {
		return true
	}

	prevHash := prev.SourceHash
	if !isSource {
		prevHash = prev.TargetHash
	}

	if curr.Hash != "" && prevHash != "" {
		_, cleanCurr := storage.ParseHashString(curr.Hash)
		_, cleanPrev := storage.ParseHashString(prevHash)
		if cleanCurr != "" && cleanPrev != "" {
			return cleanCurr != cleanPrev
		}
	}

	if curr.ETag != "" && prev.ETag != "" {
		cleanCurrETag := strings.Trim(curr.ETag, `"`)
		cleanPrevETag := strings.Trim(prev.ETag, `"`)
		if cleanCurrETag != "" && cleanPrevETag != "" {
			return cleanCurrETag != cleanPrevETag
		}
	}

	if !curr.LastModified.IsZero() && prev.Mtime.Valid {
		diff := curr.LastModified.Sub(prev.Mtime.Time)
		if diff < 0 {
			diff = -diff
		}
		if diff >= 2*time.Second {
			return true
		}
	}

	return false
}

// isFileMatchingTarget determines whether a source file and a target file are identical in content/metadata.
func isFileMatchingTarget(src, tgt fileState) bool {
	if src.Size != tgt.Size {
		return false
	}

	if src.Hash != "" && tgt.Hash != "" {
		_, cleanSrc := storage.ParseHashString(src.Hash)
		_, cleanTgt := storage.ParseHashString(tgt.Hash)
		if cleanSrc != "" && cleanTgt != "" {
			return cleanSrc == cleanTgt
		}
	}

	// When sizes match down to the byte and hashes do not conflict, treat as matching
	// (WebDAV targets update getlastmodified to HTTP upload time instead of preserving source mtime).
	return true
}

// conflictNeedsRename reports whether a two-way conflict with the given strategy
// must rename the target copy before uploading the source version.
func conflictNeedsRename(strategy string) bool {
	return strategy == "RENAME"
}

// getSourceRelPath maps a target path back to its source-side relative path by stripping the target dir prefix.
func getSourceRelPath(targetPath, targetDir string) string {
	targetPath = cleanRelPath(targetPath)
	targetDir = cleanRelPath(targetDir)

	if targetDir == "/" {
		return targetPath
	}

	prefix := targetDir + "/"
	if strings.HasPrefix(targetPath, prefix) {
		return cleanRelPath(targetPath[len(prefix):])
	}
	if targetPath == targetDir {
		return "/"
	}
	return targetPath
}

// shouldRefreshToken reports whether the stored OAuth token should be rotated
// before use. It refreshes only when an expiry is known and the token is within
// 2 minutes of expiry (or already expired). A missing expiry is treated as
// "do not refresh" to preserve the pre-existing behaviour.
func shouldRefreshToken(expiry sql.NullTime) bool {
	return expiry.Valid && !time.Now().Before(expiry.Time.Add(-2*time.Minute))
}

// ensureFreshToken refreshes OAuth credentials for a sync job if they are expired or near expiry.
func (e *Engine) ensureFreshToken(syncJobID string, job *db.SyncJob, role string, currentToken string) (string, error) {
	var expiry sql.NullTime
	var provider, refreshTokenEnc string

	if role == "source" {
		expiry = job.SourceTokenExpiresAt
		provider = job.SourceProvider
		refreshTokenEnc = job.SourceRefreshTokenEncrypted.String
	} else {
		expiry = job.TargetTokenExpiresAt
		provider = job.TargetProvider
		refreshTokenEnc = job.TargetRefreshTokenEncrypted.String
	}

	if !shouldRefreshToken(expiry) {
		return currentToken, nil
	}

	refreshToken, err := crypto.Decrypt(refreshTokenEnc, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	tokenResp, err := oauth.RefreshToken(context.Background(), provider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for %s (%s): %w", role, provider, err)
	}

	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new access token: %w", err)
	}

	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new refresh token: %w", err)
	}

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// Overwrite tokens in database
	var query string
	if role == "source" {
		query = `
			UPDATE sync_jobs
			SET source_password_encrypted = $1,
			    source_refresh_token_encrypted = $2,
			    source_token_expires_at = $3,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = $4
		`
	} else {
		query = `
			UPDATE sync_jobs
			SET target_password_encrypted = $1,
			    target_refresh_token_encrypted = $2,
			    target_token_expires_at = $3,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = $4
		`
	}

	_, err = e.db.Exec(query, newAccessEnc, newRefreshEnc, newExpiresAt, syncJobID)
	if err != nil {
		return "", fmt.Errorf("failed to persist refreshed tokens: %w", err)
	}

	return tokenResp.AccessToken, nil
}
