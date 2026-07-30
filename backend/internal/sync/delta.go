package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/sanitize"
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
	sourceDirMap, targetDirMap map[string]bool,
	prevSourceDirETags, prevTargetDirETags map[string]string,
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

	// Persist directory presence with Size: -1 (no ETag) for dirs that have no
	// ETag, so we can detect new/deleted directories across sync passes.
	for dirPath := range sourceDirMap {
		cdir := cleanRelPath(dirPath)
		if _, hasETag := sourceDirETags[cdir]; !hasETag {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "source",
				RelPath:   cdir,
				Size:      -1,
			})
		}
	}
	for dirPath := range targetDirMap {
		cdir := cleanRelPath(dirPath)
		if _, hasETag := targetDirETags[cdir]; !hasETag {
			upserts = append(upserts, &db.SyncState{
				SyncJobID: jobID,
				Side:      "target",
				RelPath:   cdir,
				Size:      -1,
			})
		}
	}

	// Clean up stale directory entries: directories that were in the previous
	// sync_state but no longer appear in the current scan (neither in dirMap
	// nor dirETags) must be deleted to prevent unbounded table growth and
	// spurious delete-propagation for already-removed directories.
	for dirPath := range prevSourceDirETags {
		cdir := cleanRelPath(dirPath)
		if !sourceDirMap[cdir] {
			if _, hasETag := sourceDirETags[cdir]; !hasETag {
				deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "source", cdir})
			}
		}
	}
	for dirPath := range prevTargetDirETags {
		cdir := cleanRelPath(dirPath)
		if !targetDirMap[cdir] {
			if _, hasETag := targetDirETags[cdir]; !hasETag {
				deletes = append(deletes, struct{ SyncJobID, Side, RelPath string }{jobID, "target", cdir})
			}
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
) (map[string]fileState, map[string]bool, map[string]string, []db.IndexingErrorInput, error) {
	fileMap := make(map[string]fileState)
	dirMap := make(map[string]bool) // all directory relative paths seen
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

	addDir := func(dirPath string) {
		cdir := cleanRelPath(dirPath)
		mu.Lock()
		dirMap[cdir] = true
		mu.Unlock()
	}

	// A directory whose contents could not be listed is not part of a
	// complete snapshot. Keep it out of the maps used for directory state and
	// ETag skipping; callers must treat the accompanying indexing error as a
	// failed scan rather than persist these partial results.
	removeDir := func(dirPath string) {
		cdir := cleanRelPath(dirPath)
		mu.Lock()
		delete(dirMap, cdir)
		delete(dirETagMap, cdir)
		mu.Unlock()
	}

	addError := func(path, msg string) {
		errsMu.Lock()
		indexErrors = append(indexErrors, db.IndexingErrorInput{
			Path:         path,
			ResourceType: "files",
			ErrorMessage: sanitize.SanitizeError(msg),
		})
		errsMu.Unlock()
	}

	type listJob struct {
		dirPath string
		etag    string
	}

	// The coordinator owns pending work. Workers only report completed listings;
	// they never submit child directories directly. This is important for wide
	// trees: if every worker were blocked sending children to a full jobs channel,
	// no worker would remain to receive work and drain that channel.
	const numWorkers = 16
	jobsChan := make(chan listJob)
	type listResult struct {
		job   listJob
		files []storage.CloudResource
		err   error
	}
	resultsChan := make(chan listResult, numWorkers*2)
	var pending []listJob
	pendingHead := 0
	visited := make(map[string]bool)

	enqueueDir := func(dirPath, etag string) {
		cdir := cleanRelPath(dirPath)
		if visited[cdir] {
			return
		}
		visited[cdir] = true
		pending = append(pending, listJob{dirPath: dirPath, etag: etag})
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
		addDir(startPath)
		enqueueDir(startPath, res.ETag)
	}

	var workers sync.WaitGroup
	workers.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer workers.Done()
			for job := range jobsChan {
				files, err := client.GetDirectoryListing(ctx, "files", job.dirPath)
				select {
				case resultsChan <- listResult{job: job, files: files, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	inFlight := 0
	for pendingHead < len(pending) || inFlight > 0 {
		var nextJob listJob
		var sendJob chan listJob
		if pendingHead < len(pending) {
			nextJob = pending[pendingHead]
			sendJob = jobsChan
		}
		// sendJob is nil (and therefore disabled in select) when no work is pending.

		select {
		case sendJob <- nextJob:
			pending[pendingHead] = listJob{} // release references held by completed work
			pendingHead++
			if pendingHead >= 1024 && pendingHead*2 >= len(pending) {
				copy(pending, pending[pendingHead:])
				pending = pending[:len(pending)-pendingHead]
				pendingHead = 0
			}
			inFlight++
		case result := <-resultsChan:
			inFlight--
			if result.err != nil {
				removeDir(result.job.dirPath)
				addError(result.job.dirPath, result.err.Error())
				continue
			}
			for _, file := range result.files {
				cpath := cleanRelPath(file.Path)
				if file.IsDir {
					addDirETag(cpath, file.ETag)
					addDir(cpath)
					enqueueDir(file.Path, file.ETag)
					continue
				}
				addFile(fileState{Path: cpath, Size: file.Size, LastModified: file.LastModified, Hash: file.Hash, ETag: file.ETag})
			}
		case <-ctx.Done():
			close(jobsChan)
			workers.Wait()
			return fileMap, dirMap, dirETagMap, indexErrors, ctx.Err()
		}
	}
	close(jobsChan)
	workers.Wait()

	if err := ctx.Err(); err != nil {
		return fileMap, dirMap, dirETagMap, indexErrors, err
	}
	return fileMap, dirMap, dirETagMap, indexErrors, nil
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

	if !src.LastModified.IsZero() && !tgt.LastModified.IsZero() {
		diff := src.LastModified.Sub(tgt.LastModified)
		if diff < 0 {
			diff = -diff
		}
		if diff >= 2*time.Second {
			return false
		}
	}

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
func (e *Engine) ensureFreshToken(ctx context.Context, syncJobID string, job *db.SyncJob, role string, currentToken string) (string, error) {
	tokenSet := func(j *db.SyncJob) (sql.NullTime, string, string, string) {
		if role == "source" {
			return j.SourceTokenExpiresAt, j.SourceProvider, j.SourceRefreshTokenEncrypted.String, j.SourcePasswordEncrypted
		}
		return j.TargetTokenExpiresAt, j.TargetProvider, j.TargetRefreshTokenEncrypted.String, j.TargetPasswordEncrypted
	}

	expiry, provider, refreshTokenEnc, _ := tokenSet(job)

	if !shouldRefreshToken(expiry) {
		return currentToken, nil
	}

	var lockToken string
	if e.queue != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = e.queue.TryClaimOAuthLock(ctx, "sync", syncJobID, role, 30*time.Second)
			if err == nil && claimed {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if latestJob, lerr := db.GetSyncJob(e.db, syncJobID); lerr == nil {
				latestExpiry, _, _, latestAccessEnc := tokenSet(latestJob)
				if !shouldRefreshToken(latestExpiry) {
					if latestAccess, derr := crypto.Decrypt(latestAccessEnc, e.encryptionKey); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for sync job %s (%s)", syncJobID, role)
		}
		defer e.queue.ReleaseOAuthLock(ctx, "sync", syncJobID, role, lockToken)
	}

	// Re-fetch latest sync job details inside lock
	if latestJob, err := db.GetSyncJob(e.db, syncJobID); err == nil {
		latestExpiry, latestProvider, latestRefreshEnc, latestAccessEnc := tokenSet(latestJob)
		if latestAccess, derr := crypto.Decrypt(latestAccessEnc, e.encryptionKey); derr == nil {
			currentToken = latestAccess
		}
		expiry, provider, refreshTokenEnc = latestExpiry, latestProvider, latestRefreshEnc
	}

	if !shouldRefreshToken(expiry) {
		return currentToken, nil
	}

	if refreshTokenEnc == "" {
		return currentToken, nil
	}

	refreshToken, err := crypto.Decrypt(refreshTokenEnc, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt refresh token: %w", err)
	}
	defer crypto.ZeroString(&refreshToken)

	tokenResp, err := oauth.RefreshToken(ctx, provider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for %s (%s): %w", role, provider, err)
	}
	defer crypto.ZeroString(&tokenResp.RefreshToken)

	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new access token: %w", err)
	}

	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, e.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new refresh token: %w", err)
	}
	// The returned access token remains live for the caller to construct the
	// provider; runSyncPass defers zeroing that returned credential. Clear the
	// response's refresh-token reference as soon as it has been encrypted.

	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	expectedRefreshEnc := refreshTokenEnc
	err = db.UpdateSyncJobOAuthTokens(e.db, syncJobID, role, newAccessEnc, newRefreshEnc, newExpiresAt, expectedRefreshEnc)
	if errors.Is(err, db.ErrOAuthTokenConflict) {
		log.Printf("[SyncEngine] Token update conflict for sync job %s (%s) — adopting winner token from DB\n", syncJobID, role)
		if latestJob, lerr := db.GetSyncJob(e.db, syncJobID); lerr == nil {
			_, _, _, latestAccessEnc := tokenSet(latestJob)
			if latestAccess, derr := crypto.Decrypt(latestAccessEnc, e.encryptionKey); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for sync job %s (%s): %w", syncJobID, role, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist refreshed tokens: %w", err)
	}

	return tokenResp.AccessToken, nil
}
