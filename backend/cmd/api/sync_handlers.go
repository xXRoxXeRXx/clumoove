package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/storage"
)

// requireSyncOwnership deliberately maps a missing job and a job belonging to
// another account to the same response. This prevents sync IDs being used for
// account enumeration while keeping database failures distinguishable.
func (s *APIServer) requireSyncOwnership(w http.ResponseWriter, r *http.Request, id, userID string) bool {
	owned, err := db.VerifySyncJobOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return false
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return false
	}
	return true
}

type syncBrowseCredentials struct {
	provider string
	url      string
	username string
	password string
}

// syncJobBrowseCredentials decrypts only the credentials needed for a browse
// request and refreshes an expiring OAuth access token before it is used.
func (s *APIServer) syncJobBrowseCredentials(ctx context.Context, id, role string, job *db.SyncJob) (syncBrowseCredentials, error) {
	creds := syncBrowseCredentials{
		provider: job.SourceProvider,
		url:      job.SourceURL,
		username: job.SourceUsername,
	}
	passwordEncrypted := job.SourcePasswordEncrypted
	refreshEncrypted := job.SourceRefreshTokenEncrypted.String
	expiresAt := job.SourceTokenExpiresAt
	if role == "target" {
		creds.provider = job.TargetProvider
		creds.url = job.TargetURL
		creds.username = job.TargetUsername
		passwordEncrypted = job.TargetPasswordEncrypted
		refreshEncrypted = job.TargetRefreshTokenEncrypted.String
		expiresAt = job.TargetTokenExpiresAt
	}

	if oauth.IsProvider(creds.provider) && expiresAt.Valid && !time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) && refreshEncrypted != "" {
		refreshToken, err := crypto.DecryptWithDomain(refreshEncrypted, s.encryptionKey, crypto.DomainOAuthRefreshToken)
		if err != nil {
			return syncBrowseCredentials{}, err
		}
		if refreshToken != "" {
			token, err := oauth.RefreshToken(ctx, creds.provider, refreshToken)
			if err == nil && token != nil && token.AccessToken != "" {
				creds.password = token.AccessToken
				accessEncrypted, accessErr := crypto.EncryptWithDomain(token.AccessToken, s.encryptionKey, crypto.DomainOAuthAccessToken)
				newRefreshEncrypted, refreshErr := crypto.EncryptWithDomain(token.RefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
				if accessErr == nil && refreshErr == nil {
					expiresIn := token.ExpiresIn
					if expiresIn <= 0 {
						expiresIn = 3600
					}
					if err := db.UpdateSyncJobOAuthTokens(s.db, id, role, accessEncrypted, newRefreshEncrypted, time.Now().Add(time.Duration(expiresIn)*time.Second), refreshEncrypted); err != nil {
						return syncBrowseCredentials{}, err
					}
				}
			}
		}
	}

	if creds.password == "" && passwordEncrypted != "" {
		password, err := crypto.DecryptWithDomain(passwordEncrypted, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.provider)))
		if err != nil {
			return syncBrowseCredentials{}, err
		}
		creds.password = password
	}
	return creds, nil
}

type createSyncRequest struct {
	SourceProfileID      string   `json:"source_profile_id,omitempty"`
	TargetProfileID      string   `json:"target_profile_id,omitempty"`
	SourceURL            string   `json:"source_url"`
	SourceUsername       string   `json:"source_username"`
	SourcePassword       string   `json:"source_password"`
	SourceRefreshToken   string   `json:"source_refresh_token,omitempty"`
	SourceTokenExpiresAt *string  `json:"source_token_expires_at,omitempty"`
	TargetURL            string   `json:"target_url"`
	TargetUsername       string   `json:"target_username"`
	TargetPassword       string   `json:"target_password"`
	TargetRefreshToken   string   `json:"target_refresh_token,omitempty"`
	TargetTokenExpiresAt *string  `json:"target_token_expires_at,omitempty"`
	SourceProvider       string   `json:"source_provider"`
	TargetProvider       string   `json:"target_provider"`
	Direction            string   `json:"direction"`
	ConflictStrategy     string   `json:"conflict_strategy"`
	DeletePropagation    bool     `json:"delete_propagation"`
	IntervalMinutes      int      `json:"interval_minutes"`
	Threads              int      `json:"threads"`
	BandwidthLimitMbps   int      `json:"bandwidth_limit_mbps"`
	TargetDir            string   `json:"target_dir"`
	SelectedPaths        []string `json:"selected_paths"`
}

func (s *APIServer) handleListSyncs(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	jobs, err := db.GetSyncJobsForUserContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "Error fetching sync jobs for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if jobs == nil {
		jobs = []db.SyncJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *APIServer) handleCreateSync(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	var req createSyncRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	// Merge any referenced reusable connection profiles into the request.
	src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
		Provider:     req.SourceProvider,
		URL:          req.SourceURL,
		Username:     req.SourceUsername,
		Password:     req.SourcePassword,
		RefreshToken: req.SourceRefreshToken,
	})
	if err != nil {
		s.logf(r, "handleCreateSync: failed to load source profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.SourceProvider = src.Provider
	req.SourceURL = src.URL
	req.SourceUsername = src.Username
	if req.SourcePassword == "" {
		req.SourcePassword = src.Password
	}
	if req.SourceRefreshToken == "" {
		req.SourceRefreshToken = src.RefreshToken
	}

	tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider:     req.TargetProvider,
		URL:          req.TargetURL,
		Username:     req.TargetUsername,
		Password:     req.TargetPassword,
		RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		s.logf(r, "handleCreateSync: failed to load target profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.TargetProvider = tgt.Provider
	req.TargetURL = tgt.URL
	req.TargetUsername = tgt.Username
	if req.TargetPassword == "" {
		req.TargetPassword = tgt.Password
	}
	if req.TargetRefreshToken == "" {
		req.TargetRefreshToken = tgt.RefreshToken
	}

	// Default fallback values
	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if oauth.IsProvider(req.SourceProvider) && req.SourceRefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if oauth.IsProvider(req.TargetProvider) && req.TargetRefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)
	if req.SourceProvider == "immich" || req.TargetProvider == "immich" {
		writeError(w, http.StatusBadRequest, ErrImmichSyncUnsupported)
		return
	}

	if req.Direction == "" {
		req.Direction = "one_way"
	}
	if !validSyncDirection(req.Direction) {
		writeValidationError(w, ErrSyncDirectionInvalid)
		return
	}
	if req.ConflictStrategy == "" {
		req.ConflictStrategy = "OVERWRITE"
	}
	if !db.ValidConflictStrategy(req.ConflictStrategy) {
		writeError(w, http.StatusBadRequest, ErrConflictStrategyInvalid)
		return
	}
	if req.IntervalMinutes <= 0 {
		req.IntervalMinutes = 15
	}
	if req.Threads <= 0 || req.Threads > 16 {
		req.Threads = 8
	}
	if req.BandwidthLimitMbps < 0 {
		req.BandwidthLimitMbps = 0
	} else if req.BandwidthLimitMbps > 1000 {
		req.BandwidthLimitMbps = 1000
	}
	if req.TargetDir == "" {
		req.TargetDir = "/"
	}

	// Validate provider URLs for host-based providers upfront
	if err := storage.ValidateProviderURL(req.SourceProvider, req.SourceURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}
	if err := storage.ValidateProviderURL(req.TargetProvider, req.TargetURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}

	// Encrypt passwords
	sEnc, err := crypto.EncryptWithDomain(req.SourcePassword, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(req.SourceProvider)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	tEnc, err := crypto.EncryptWithDomain(req.TargetPassword, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(req.TargetProvider)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	sourceMegaSessionIDEncrypted, sourceMegaMasterKeyEncrypted, err := s.encryptMegaSession(src.MegaSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	targetMegaSessionIDEncrypted, targetMegaMasterKeyEncrypted, err := s.encryptMegaSession(tgt.MegaSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	// Persist OAuth refresh tokens so the engine can rotate them before expiry.
	// Without this, OAuth-based sync jobs (Dropbox/Google) would fail as soon as
	// the initial access token expires.
	var sourceRefreshEnc sql.NullString
	var sourceTokenExpiresAt sql.NullTime
	if req.SourceRefreshToken != "" {
		enc, eerr := crypto.EncryptWithDomain(req.SourceRefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
		if eerr != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		sourceRefreshEnc = sql.NullString{String: enc, Valid: true}
		sourceTokenExpiresAt = parseSyncTokenExpiry(req.SourceTokenExpiresAt)
	}

	var targetRefreshEnc sql.NullString
	var targetTokenExpiresAt sql.NullTime
	if req.TargetRefreshToken != "" {
		enc, eerr := crypto.EncryptWithDomain(req.TargetRefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
		if eerr != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		targetRefreshEnc = sql.NullString{String: enc, Valid: true}
		targetTokenExpiresAt = parseSyncTokenExpiry(req.TargetTokenExpiresAt)
	}

	job := &db.SyncJob{
		UserID:                       userID,
		SourceURL:                    req.SourceURL,
		SourceUsername:               req.SourceUsername,
		SourcePasswordEncrypted:      sEnc,
		SourceRefreshTokenEncrypted:  sourceRefreshEnc,
		SourceTokenExpiresAt:         sourceTokenExpiresAt,
		SourceMegaSessionIDEncrypted: sourceMegaSessionIDEncrypted,
		SourceMegaMasterKeyEncrypted: sourceMegaMasterKeyEncrypted,
		TargetURL:                    req.TargetURL,
		TargetUsername:               req.TargetUsername,
		TargetPasswordEncrypted:      tEnc,
		TargetRefreshTokenEncrypted:  targetRefreshEnc,
		TargetTokenExpiresAt:         targetTokenExpiresAt,
		TargetMegaSessionIDEncrypted: targetMegaSessionIDEncrypted,
		TargetMegaMasterKeyEncrypted: targetMegaMasterKeyEncrypted,
		SourceProvider:               req.SourceProvider,
		TargetProvider:               req.TargetProvider,
		Direction:                    req.Direction,
		ConflictStrategy:             req.ConflictStrategy,
		DeletePropagation:            req.DeletePropagation,
		IntervalMinutes:              req.IntervalMinutes,
		Threads:                      req.Threads,
		BandwidthLimitMbps:           req.BandwidthLimitMbps,
		Status:                       "IDLE",
		TargetDir:                    req.TargetDir,
		SelectedPaths:                req.SelectedPaths,
	}

	// Create a duration-based linked schedule. Cron's minute field is limited to
	// 0-59, so values such as a 90-minute interval cannot be represented by a
	// cron expression. The scheduler reads interval_minutes from this sync job
	// and advances next_run_at relative to the current time.
	nextRun := time.Now().Add(time.Duration(req.IntervalMinutes) * time.Minute)

	sched := &db.Schedule{
		UserID:         userID,
		TaskType:       "sync",
		CronExpression: sql.NullString{},
		NextRunAt:      sql.NullTime{Time: nextRun, Valid: true},
		IsActive:       true,
	}
	jobID, err := db.CreateSyncJobAndSchedule(s.db, job, sched)
	if err != nil {
		s.logf(r, "Failed to create sync job and schedule: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSyncCreated, jobID, userID, map[string]interface{}{
		"source_provider": req.SourceProvider,
		"target_provider": req.TargetProvider,
		"direction":       req.Direction,
	})

	writeJSON(w, http.StatusOK, map[string]string{"id": jobID})
}

func validSyncDirection(direction string) bool {
	return direction == "one_way" || direction == "two_way"
}

func (s *APIServer) handleGetSyncStatus(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	if job.Status == "RUNNING" || job.Status == "INDEXING" {
		if activeFiles, err := db.GetActiveSyncTaskPaths(s.db, r.Context(), id); err == nil {
			job.ActiveFiles = activeFiles
		}
	}

	writeJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleStartSync(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	claimed, err := s.syncEngine.StartSyncPass(s.backgroundCtx, id)
	if err != nil {
		s.logf(r, "[Sync] Failed to claim sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !claimed {
		job, getErr := db.GetSyncJobContext(r.Context(), s.db, id)
		if getErr != nil {
			writeError(w, http.StatusNotFound, ErrSyncNotFound)
			return
		}
		if job.Status == "RUNNING" || job.Status == "INDEXING" || job.Status == "VERIFYING" {
			writeError(w, http.StatusConflict, ErrSyncAlreadyRunning)
			return
		}
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}

	s.writeAudit(r, db.AuditSyncStarted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handlePauseSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	// A paused sync abandons the current pass. Resume starts a freshly indexed
	// pass; it never attempts to continue a coordinator that was cancelled.
	emptyErr := ""
	paused, err := db.PauseSyncJob(s.db, id, &emptyErr)
	if err != nil {
		s.logf(r, "Failed to pause sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !paused {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}
	if err := db.DeactivateSchedulesForTask(s.db, "sync", id); err != nil {
		s.logf(r, "failed to deactivate schedules for paused sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if _, err := db.CancelOpenSyncTasksForPause(s.db, id); err != nil {
		s.logf(r, "Warning: failed to cancel open tasks for paused sync job %s: %v", id, err)
	}
	s.syncEngine.CancelPass(id)
	if err := s.queue.PublishSyncCancelEvent(r.Context(), id); err != nil {
		s.logf(r, "Warning: failed to publish cancel event for sync job %s: %v", id, err)
	}

	s.writeAudit(r, db.AuditSyncPaused, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleResumeSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	// Resume starts a new full pass. The pass cancelled by pause cannot be
	// continued because its in-memory indexing state was intentionally dropped.
	// Wait for the cancelled coordinator on any API instance to exit before
	// publishing a new INDEXING claim; otherwise it could finish stale writes.
	if err := s.syncEngine.WaitForPassDrain(r.Context(), id); err != nil {
		s.logf(r, "Failed waiting for sync job %s to drain: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	emptyErr := ""
	resumed, err := db.ResumeSyncJob(s.db, id, &emptyErr)
	if err != nil {
		s.logf(r, "Failed to resume sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !resumed {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}
	claimed, startErr := s.syncEngine.StartSyncPass(s.backgroundCtx, id)
	// Always reactivate the schedule. If the immediate claim was lost to an
	// instance race or a transient DB error, the next scheduler tick safely
	// starts the already-resumed IDLE job instead of reporting a false failure.
	nextRun := time.Now()
	if err := db.ReactivateSchedulesForTask(s.db, "sync", id, nextRun); err != nil {
		s.logf(r, "failed to reactivate schedules for resumed sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if startErr != nil {
		s.warnf(r, "Deferred resumed sync job %s after immediate start error: %v", id, startErr)
	} else if !claimed {
		s.warnf(r, "Deferred resumed sync job %s because another instance claimed it", id)
	}

	s.writeAudit(r, db.AuditSyncResumed, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true, "deferred": startErr != nil || !claimed})
}

func (s *APIServer) handleDeleteSync(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	// Cancel any in-flight sync-pass goroutine before deleting the DB rows so
	// the goroutine does not keep operating against a deleted job.
	s.syncEngine.CancelPass(id)
	if err := s.queue.PublishSyncCancelEvent(r.Context(), id); err != nil {
		s.logf(r, "Warning: failed to publish cancel event for sync job %s: %v", id, err)
	}

	err := db.DeleteSyncJobCascade(s.db, id)
	if err != nil {
		s.logf(r, "Failed to delete sync job: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSyncDeleted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleDownloadSyncReport(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")

	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	failedTasks, err := db.GetFailedSyncTasksForReport(s.db, id, job.RunGeneration)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"sync_report_%s.csv\"", id))

	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"ID", "File Path", "Size (Bytes)", "Status", "Error Message", "Created At"})

	for _, task := range failedTasks {
		errMsg := ""
		if task.ErrorMessage.Valid {
			errMsg = sanitizeCSVFormula(task.ErrorMessage.String)
		}
		filePath := sanitizeCSVFormula(task.FilePath)

		_ = writer.Write([]string{
			task.ID,
			filePath,
			fmt.Sprintf("%d", task.FileSize),
			task.Status,
			errMsg,
			task.CreatedAt.Format(time.RFC3339),
		})
	}
	writer.Flush()
}

func (s *APIServer) handleSyncErrors(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}
	limit, offset := parseErrorListPagination(r)
	items, total, err := db.GetSyncErrorsContext(r.Context(), s.db, id, limit, offset)
	if err != nil {
		s.logf(r, "Error fetching sync job %s errors: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	sanitizeErrorListItems(items)
	writeJSON(w, http.StatusOK, map[string]interface{}{"errors": items, "total": total, "limit": limit, "offset": offset})
}

func (s *APIServer) handleSyncStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	if !s.acquireSyncStream(w, r, userID) {
		return
	}
	defer s.releaseSyncStream(userID)

	// Disable the server write deadline for this long-lived connection.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// 3s poll is a good balance: fast enough to feel live, slow enough to not
	// hammer the DB. Change-detection skips the flush when nothing changed.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	// Periodic comment heartbeat keeps the SSE connection alive behind proxies
	// that would otherwise GC an idle connection between data frames.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// lastJSON tracks the previous payload to avoid flushing identical data
	// to the client on every tick when nothing has changed.
	var lastJSON []byte

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			jobs, err := db.GetSyncJobsForUserContext(r.Context(), s.db, userID)
			if err != nil {
				continue
			}
			if jobs == nil {
				jobs = []db.SyncJob{}
			}
			for i := range jobs {
				if jobs[i].Status == "RUNNING" || jobs[i].Status == "INDEXING" {
					if activeFiles, err := db.GetActiveSyncTaskPaths(s.db, r.Context(), jobs[i].ID); err == nil {
						jobs[i].ActiveFiles = activeFiles
					}
				}
			}

			data, err := json.Marshal(jobs)
			if err != nil {
				continue
			}

			// Only push to client when data actually changed
			if bytes.Equal(data, lastJSON) {
				continue
			}
			lastJSON = data

			fmt.Fprintf(w, "event: sync_jobs\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// sanitizeCSVFormula prevents spreadsheet formula injection by prefixing
// cells that start with a trigger character with a single quote.
func sanitizeCSVFormula(input string) string {
	if len(input) == 0 {
		return input
	}
	firstChar := input[0]
	if firstChar == '=' || firstChar == '+' || firstChar == '-' || firstChar == '@' || firstChar == '\t' || firstChar == '\r' {
		return "'" + input
	}
	return input
}

// parseSyncTokenExpiry converts an optional RFC3339 expiry timestamp from the
// request into a sql.NullTime. A missing/invalid value falls back to "now" so
// the engine's ensureFreshToken treats the token as already-expired and refreshes
// on first use (safer than silently assuming a far-future expiry).
func parseSyncTokenExpiry(raw *string) sql.NullTime {
	if raw == nil || *raw == "" {
		return sql.NullTime{Time: time.Now(), Valid: true}
	}
	exp, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return sql.NullTime{Time: time.Now(), Valid: true}
	}
	return sql.NullTime{Time: exp, Valid: true}
}

func (s *APIServer) handleSetSyncThreads(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req ThreadsRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateSyncJobThreads(s.db, id, threads); err != nil {
		s.logf(r, "Error updating threads for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleSetSyncBandwidth(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req BandwidthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if req.LimitMbps < 0 || req.LimitMbps > 1000 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}

	if err := db.UpdateSyncJobBandwidthLimit(s.db, id, req.LimitMbps); err != nil {
		s.logf(r, "Error updating bandwidth limit for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "Error loading sync job %s after bandwidth update: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	// Only active passes have a worker-side throttler to update. The persisted
	// value is used when a later pass starts, so idle and terminal jobs do not
	// need a Redis broadcast.
	if job.Status != "INDEXING" && job.Status != "RUNNING" && job.Status != "VERIFYING" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	if err := s.queue.PublishBandwidthChange(r.Context(), queue.BandwidthEvent{
		SyncJobID:          id,
		BandwidthLimitMbps: req.LimitMbps,
	}); err != nil {
		s.logf(r, "Warning: failed to publish bandwidth change for sync job %s: %v", id, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type updateSyncScheduleRequest struct {
	IntervalMinutes int `json:"interval_minutes"`
}

type updateSyncScopeRequest struct {
	SelectedPaths     []string `json:"selected_paths"`
	TargetDir         string   `json:"target_dir"`
	ConflictStrategy  string   `json:"conflict_strategy,omitempty"`
	Direction         string   `json:"direction,omitempty"`
	DeletePropagation *bool    `json:"delete_propagation,omitempty"`
}

func (s *APIServer) handleUpdateSyncSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	var req updateSyncScheduleRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	validIntervals := map[int]bool{5: true, 15: true, 30: true, 60: true, 360: true, 1440: true}
	if !validIntervals[req.IntervalMinutes] {
		writeValidationError(w, ErrSyncIntervalInvalid)
		return
	}

	if err := db.UpdateSyncJobInterval(s.db, id, req.IntervalMinutes); err != nil {
		s.logf(r, "Error updating sync schedule for job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSyncUpdated, id, userID, map[string]any{"interval_minutes": req.IntervalMinutes})
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleBrowseSyncJob(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "browse", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "Error getting sync job %s for browse: %v", id, err)
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	role := r.URL.Query().Get("role")
	if role != "target" {
		role = "source"
	}
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "/"
	}
	resourceType := r.URL.Query().Get("resource_type")
	if resourceType == "" {
		resourceType = "files"
	}
	if resourceType != "files" && resourceType != "calendars" && resourceType != "contacts" {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	creds, err := s.syncJobBrowseCredentials(r.Context(), id, role, job)
	if err != nil {
		s.logf(r, "handleBrowseSyncJob: failed to load credentials for job %s role %s: %v", id, role, err)
		errCode := ErrSourceConnectionFailed
		if role == "target" {
			errCode = ErrTargetConnectionFailed
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}

	if !storage.IsValidProvider(creds.provider) {
		errCode := ErrSourceUrlInvalid
		if role == "target" {
			errCode = ErrTargetUrlInvalid
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}

	browseCtx := storage.WithLocalUserScope(r.Context(), userID)
	client, err := storage.NewProvider(browseCtx, creds.provider, creds.url, creds.username, creds.password)
	if err != nil {
		s.logf(r, "handleBrowseSyncJob: NewProvider failed for provider %s: %v", creds.provider, err)
		errCode := ErrSourceUrlInvalid
		if role == "target" {
			errCode = ErrTargetUrlInvalid
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	ok, err := client.Connect(ctx)
	if !ok {
		s.logf(r, "handleBrowseSyncJob: connection failed for provider %s: %v", creds.provider, err)
		errCode := ErrSourceConnectionFailed
		if role == "target" {
			errCode = ErrTargetConnectionFailed
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(errCode)})
		return
	}

	items, err := client.GetDirectoryListing(ctx, resourceType, reqPath)
	if err != nil {
		s.logf(r, "handleBrowseSyncJob: failed to list %s for path %s (provider %s): %v", resourceType, reqPath, creds.provider, err)
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": string(ErrListFailed)})
		return
	}

	collections := items

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"items":   collections,
		"files":   collections,
	})
}

func (s *APIServer) handleUpdateSyncScope(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	id := r.PathValue("id")
	if !s.requireSyncOwnership(w, r, id, userID) {
		return
	}

	job, err := db.GetSyncJobContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "Error getting sync job %s for scope update: %v", id, err)
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	if job.Status == "INDEXING" || job.Status == "RUNNING" || job.Status == "VERIFYING" {
		writeError(w, http.StatusConflict, ErrSyncInvalidState)
		return
	}

	if job.SourceProvider == "immich" || job.TargetProvider == "immich" {
		writeError(w, http.StatusBadRequest, ErrImmichSyncUnsupported)
		return
	}

	var req updateSyncScopeRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if len(req.SelectedPaths) == 0 {
		writeValidationError(w, ErrNoSourcePaths)
		return
	}

	if req.TargetDir == "" {
		req.TargetDir = "/"
	}

	if err := db.UpdateSyncJobScope(s.db, id, req.SelectedPaths, req.TargetDir, req.ConflictStrategy, req.Direction, req.DeletePropagation); err != nil {
		if errors.Is(err, db.ErrSyncInvalidState) {
			writeError(w, http.StatusConflict, ErrSyncInvalidState)
			return
		}
		s.logf(r, "Error updating scope for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSyncUpdated, id, userID, map[string]any{
		"selected_paths": req.SelectedPaths,
		"target_dir":     req.TargetDir,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
