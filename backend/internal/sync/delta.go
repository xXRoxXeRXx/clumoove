package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// taskToCreate is the provider-independent result of delta calculation.
type taskToCreate struct {
	filePath            string
	fileSize            int64
	sourceHash          string
	resourceType        string
	action              string
	side                string // source or target
	waitForConflictCopy bool
}

// cleanRelPath normalizes a relative path so that it always starts with a single leading slash
// and has no trailing slash (unless it is the root "/").
func cleanRelPath(p string) string {
	cleaned := path.Clean("/" + p)
	if cleaned == "." || cleaned == "" {
		return "/"
	}
	return cleaned
}

// syncStateChanges aligns sync_state entries with current listings, preserving
// the old states of failed files. The caller persists the returned changes with
// lifecycle finalization in one transaction.
func syncStateChanges(
	jobID string,
	sourceMap, targetMap map[string]fileState,
	prevSource, prevTarget map[string]db.SyncState,
	sourceDirETags, targetDirETags map[string]string,
	sourceDirMap, targetDirMap map[string]bool,
	prevSourceDirs, prevTargetDirs map[string]bool,
	taskOutcomes map[string]string,
) ([]*db.SyncState, []db.SyncStateDelete) {
	allPaths := make(map[string]bool)
	for k := range sourceMap {
		allPaths[cleanRelPath(k)] = true
	}
	for k := range targetMap {
		allPaths[cleanRelPath(k)] = true
	}
	for k := range prevSource {
		allPaths[cleanRelPath(k)] = true
	}
	for k := range prevTarget {
		allPaths[cleanRelPath(k)] = true
	}

	var upserts []*db.SyncState
	var deletes []db.SyncStateDelete

	for relPath := range allPaths {
		sourceFile, hasSource := sourceMap[relPath]
		targetFile, hasTarget := targetMap[relPath]
		outcome, hasTask := taskOutcomes[relPath]

		// Keep the old baseline for any task that did not finish successfully so
		// the next pass retries it.
		if hasTask && outcome != "COMPLETED" && outcome != "SKIPPED" {
			continue
		}

		cleanPath := cleanRelPath(relPath)

		// Source side
		if hasSource {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "source",
				RelPath:    cleanPath,
				Size:       sourceFile.Size,
				Mtime:      sql.NullTime{Time: sourceFile.LastModified, Valid: !sourceFile.LastModified.IsZero()},
				SourceHash: sourceFile.Hash,
				ETag:       sourceFile.ETag,
			})
		} else {
			deletes = append(deletes, db.SyncStateDelete{SyncJobID: jobID, Side: "source", RelPath: cleanPath})
		}

		// Target side
		if hasTarget {
			upserts = append(upserts, &db.SyncState{
				SyncJobID:  jobID,
				Side:       "target",
				RelPath:    cleanPath,
				Size:       targetFile.Size,
				Mtime:      sql.NullTime{Time: targetFile.LastModified, Valid: !targetFile.LastModified.IsZero()},
				TargetHash: targetFile.Hash,
				ETag:       targetFile.ETag,
			})
		} else {
			deletes = append(deletes, db.SyncStateDelete{SyncJobID: jobID, Side: "target", RelPath: cleanPath})
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
	for dirPath := range prevSourceDirs {
		cdir := cleanRelPath(dirPath)
		if !sourceDirMap[cdir] {
			if _, hasETag := sourceDirETags[cdir]; !hasETag {
				deletes = append(deletes, db.SyncStateDelete{SyncJobID: jobID, Side: "source", RelPath: cdir})
			}
		}
	}
	for dirPath := range prevTargetDirs {
		cdir := cleanRelPath(dirPath)
		if !targetDirMap[cdir] {
			if _, hasETag := targetDirETags[cdir]; !hasETag {
				deletes = append(deletes, db.SyncStateDelete{SyncJobID: jobID, Side: "target", RelPath: cdir})
			}
		}
	}

	return upserts, deletes
}

// listFiles traverses paths recursively using a parallel worker pool. When a
// directory ETag is unchanged, its previous subtree is retained without a
// provider listing.
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

	copyPreviousSubtree := func(dirPath string) {
		// prevFileStates is populated only from Size != -1 rows; directory
		// entries live exclusively in prevDirETags.
		cdir := cleanRelPath(dirPath)
		prefix := cdir
		if prefix != "/" {
			prefix += "/"
		}
		for filePath, fs := range prevFileStates {
			if cdir == "/" || filePath == cdir || strings.HasPrefix(filePath, prefix) {
				addFile(fs)
			}
		}
		for previousDir, etag := range prevDirETags {
			if cdir == "/" || previousDir == cdir || strings.HasPrefix(previousDir, prefix) {
				addDir(previousDir)
				addDirETag(previousDir, etag)
			}
		}
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
		if etag != "" && prevDirETags[cdir] == etag {
			copyPreviousSubtree(cdir)
			return
		}
		pending = append(pending, listJob{dirPath: dirPath, etag: etag})
	}

	for _, startPath := range startPaths {
		if startPath == "" {
			continue
		}
		res, err := client.InspectResource(ctx, "files", startPath)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				slog.Debug("sync start path not found; treating as empty")
				continue
			}
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
		// Older rows stored the target hash in source_hash. Preserve their
		// change-detection behavior until the next successful reconciliation
		// rewrites them with the side-correct field.
		if prevHash == "" {
			prevHash = prev.SourceHash
		}
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

type finalTaskStats struct {
	total, completed, skipped, failed int
	changed, deleted                  int
	outcomes                          map[string]string
}

// readFinalTaskOutcomes collects statistics and the durable state outcome map
// in one cancellable query, so finalization cannot observe mismatched task
// snapshots.
func (e *Engine) readFinalTaskOutcomes(ctx context.Context, jobID string, generation int) (finalTaskStats, error) {
	stats := finalTaskStats{outcomes: make(map[string]string)}
	rows, err := e.db.QueryContext(ctx, `SELECT file_path, status, metadata FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2`, jobID, generation)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var filePath, status string
		var metadata []byte
		if err := rows.Scan(&filePath, &status, &metadata); err != nil {
			return stats, err
		}
		stats.total++
		stats.outcomes[filePath] = status
		switch status {
		case "COMPLETED":
			stats.completed++
		case "SKIPPED":
			stats.skipped++
		case "FAILED", "CANCELLED":
			stats.failed++
		}
		if status != "COMPLETED" && status != "SKIPPED" {
			continue
		}
		var meta struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(metadata, &meta) != nil {
			continue
		}
		switch meta.Action {
		case "delete":
			stats.deleted++
		case "upload", "download", "conflict_copy":
			stats.changed++
		}
	}
	return stats, rows.Err()
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

// getTargetAbsPath is the inverse of getSourceRelPath: it maps a source-relative
// path to the physical target path by prepending targetDir. This is used to
// convert prevTargetFiles / prevTargetDirETags (stored source-relative in
// sync_state) back to raw target paths before passing them to listFiles, so
// that copyPreviousSubtree can match against provider-returned paths correctly.
func getTargetAbsPath(relPath, targetDir string) string {
	relPath = cleanRelPath(relPath)
	targetDir = cleanRelPath(targetDir)
	if targetDir == "/" {
		return relPath
	}
	if relPath == "/" {
		return targetDir
	}
	return targetDir + relPath
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
					if latestAccess, derr := crypto.DecryptWithDomain(latestAccessEnc, e.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for sync job %s (%s)", syncJobID, role)
		}
		defer e.queue.ReleaseOAuthLock(context.Background(), "sync", syncJobID, role, lockToken)
	}

	// Re-fetch latest sync job details inside lock
	if latestJob, err := db.GetSyncJob(e.db, syncJobID); err == nil {
		latestExpiry, latestProvider, latestRefreshEnc, latestAccessEnc := tokenSet(latestJob)
		if latestAccess, derr := crypto.DecryptWithDomain(latestAccessEnc, e.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
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

	refreshToken, err := crypto.DecryptWithDomain(refreshTokenEnc, e.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt refresh token: %w", err)
	}

	tokenResp, err := oauth.RefreshToken(ctx, provider, refreshToken)
	if err != nil {
		return "", fmt.Errorf("oauth refresh failed for %s (%s): %w", role, provider, err)
	}

	newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, e.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new access token: %w", err)
	}

	newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, e.encryptionKey, crypto.DomainOAuthRefreshToken)
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
		slog.Info("sync OAuth token update conflicted; adopting stored winner", "sync_job_id", syncJobID, "role", role)
		if latestJob, lerr := db.GetSyncJob(e.db, syncJobID); lerr == nil {
			_, _, _, latestAccessEnc := tokenSet(latestJob)
			if latestAccess, derr := crypto.DecryptWithDomain(latestAccessEnc, e.encryptionKey, crypto.DomainOAuthAccessToken); derr == nil {
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
