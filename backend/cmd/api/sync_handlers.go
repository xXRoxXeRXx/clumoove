package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

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
	TargetDir            string   `json:"target_dir"`
	SelectedPaths        []string `json:"selected_paths"`
}

func (s *APIServer) handleListSyncs(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	jobs, err := db.GetSyncJobsForUser(s.db, userID)
	if err != nil {
		log.Printf("Error fetching sync jobs for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if jobs == nil {
		jobs = []db.SyncJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *APIServer) handleCreateSync(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req createSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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
		log.Printf("handleCreateSync: failed to load source profile: %v", err)
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
		log.Printf("handleCreateSync: failed to load target profile: %v", err)
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
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)

	if req.Direction == "" {
		req.Direction = "one_way"
	}
	if req.ConflictStrategy == "" {
		req.ConflictStrategy = "OVERWRITE"
	}
	if req.IntervalMinutes <= 0 {
		req.IntervalMinutes = 15
	}
	if req.Threads <= 0 || req.Threads > 16 {
		req.Threads = 4
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
	sEnc, err := crypto.Encrypt(req.SourcePassword, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	tEnc, err := crypto.Encrypt(req.TargetPassword, s.encryptionKey)
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
		enc, eerr := crypto.Encrypt(req.SourceRefreshToken, s.encryptionKey)
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
		enc, eerr := crypto.Encrypt(req.TargetRefreshToken, s.encryptionKey)
		if eerr != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		targetRefreshEnc = sql.NullString{String: enc, Valid: true}
		targetTokenExpiresAt = parseSyncTokenExpiry(req.TargetTokenExpiresAt)
	}

	job := &db.SyncJob{
		UserID:                      userID,
		SourceURL:                   req.SourceURL,
		SourceUsername:              req.SourceUsername,
		SourcePasswordEncrypted:     sEnc,
		SourceRefreshTokenEncrypted: sourceRefreshEnc,
		SourceTokenExpiresAt:        sourceTokenExpiresAt,
		TargetURL:                   req.TargetURL,
		TargetUsername:              req.TargetUsername,
		TargetPasswordEncrypted:     tEnc,
		TargetRefreshTokenEncrypted: targetRefreshEnc,
		TargetTokenExpiresAt:        targetTokenExpiresAt,
		SourceProvider:              req.SourceProvider,
		TargetProvider:              req.TargetProvider,
		Direction:                   req.Direction,
		ConflictStrategy:            req.ConflictStrategy,
		DeletePropagation:           req.DeletePropagation,
		IntervalMinutes:             req.IntervalMinutes,
		Threads:                     req.Threads,
		Status:                      "IDLE",
		TargetDir:                   req.TargetDir,
		SelectedPaths:               req.SelectedPaths,
	}

	jobID, err := db.CreateSyncJob(s.db, job)
	if err != nil {
		log.Printf("Failed to create sync job: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Create linked Schedule in schedules table for cron trigger.
	// For intervals that divide evenly into hours, use an hour-based expression
	// so the schedule fires at predictable wall-clock times (e.g. every 2 h at :00).
	// For all other values, use a minute-based */N expression which is correct for
	// any N ≤ 59 and also works for multi-hour non-divisible values (e.g. 90 min
	// fires every 90 minutes regardless of the hour boundary).
	var cronExpr string
	if req.IntervalMinutes >= 60 && req.IntervalMinutes%60 == 0 {
		hours := req.IntervalMinutes / 60
		cronExpr = fmt.Sprintf("0 */%d * * *", hours)
	} else {
		cronExpr = fmt.Sprintf("*/%d * * * *", req.IntervalMinutes)
	}
	nextRun := time.Now().Add(time.Duration(req.IntervalMinutes) * time.Minute)

	sched := &db.Schedule{
		UserID:         userID,
		TaskType:       "sync",
		TaskID:         jobID,
		CronExpression: sql.NullString{String: cronExpr, Valid: true},
		NextRunAt:      sql.NullTime{Time: nextRun, Valid: true},
		IsActive:       true,
	}
	if _, err := db.CreateSchedule(s.db, sched); err != nil {
		log.Printf("[Sync] Warning: sync job %s created but schedule creation failed: %v\n", jobID, err)
	}

	s.writeAudit(r, db.AuditSyncCreated, jobID, userID, map[string]interface{}{
		"source_provider": req.SourceProvider,
		"target_provider": req.TargetProvider,
		"direction":       req.Direction,
	})

	writeJSON(w, http.StatusOK, map[string]string{"id": jobID})
}

func (s *APIServer) handleGetSyncStatus(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	job, err := db.GetSyncJob(s.db, id)
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
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	job, err := db.GetSyncJob(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	if job.Status == "INDEXING" || job.Status == "RUNNING" {
		writeError(w, http.StatusConflict, ErrSyncAlreadyRunning)
		return
	}

	// Asynchronously run pass
	go s.syncEngine.RunSyncPass(s.ctx, id)

	s.writeAudit(r, db.AuditSyncStarted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handlePauseSync(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	job, err := db.GetSyncJob(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}

	// Only allow pausing from states where no active pass controls the lifecycle.
	// RUNNING/INDEXING: the engine's completion path would overwrite PAUSED → IDLE.
	if job.Status == "RUNNING" || job.Status == "INDEXING" {
		writeError(w, http.StatusConflict, ErrSyncAlreadyRunning)
		return
	}

	// Clear any stale error from a previous failed run so the UI doesn't keep
	// showing it while the job is intentionally paused.
	emptyErr := ""
	_ = db.UpdateSyncJobStatus(s.db, id, "PAUSED", &emptyErr)
	// Deactivate schedule
	_, _ = s.db.Exec(`UPDATE schedules SET is_active = FALSE WHERE task_type = 'sync' AND task_id = $1`, id)

	s.writeAudit(r, db.AuditSyncPaused, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleResumeSync(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	// Clear any stale error from a previous failed run when resuming.
	emptyErr := ""
	_ = db.UpdateSyncJobStatus(s.db, id, "IDLE", &emptyErr)
	// Activate schedule
	nextRun := time.Now()
	_, _ = s.db.Exec(`UPDATE schedules SET is_active = TRUE, next_run_at = $1 WHERE task_type = 'sync' AND task_id = $2`, nextRun, id)

	s.writeAudit(r, db.AuditSyncResumed, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleDeleteSync(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	// Cancel any in-flight RunSyncPass goroutine before deleting the DB rows so
	// the goroutine does not keep operating against a deleted job.
	s.syncEngine.CancelPass(id)

	err = db.DeleteSyncJobCascade(s.db, id)
	if err != nil {
		log.Printf("Failed to delete sync job: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSyncDeleted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleDownloadSyncReport(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	id := r.PathValue("id")

	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	failedTasks, err := db.GetFailedSyncTasksForReport(s.db, id)
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

func (s *APIServer) handleSyncStream(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	// Rate-limit connection attempts (mirrors handleMigrationStream).
	if !s.rateLimiter.Allow(s.clientIP(r), streamRateLimit, streamRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	// Cap concurrent streams per user to prevent unlimited DB-polling goroutines.
	s.streamMu.Lock()
	if s.activeStreams[userID] >= maxStreamsPerUser {
		s.streamMu.Unlock()
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	s.activeStreams[userID]++
	s.streamMu.Unlock()
	defer func() {
		s.streamMu.Lock()
		s.activeStreams[userID]--
		if s.activeStreams[userID] <= 0 {
			delete(s.activeStreams, userID)
		}
		s.streamMu.Unlock()
	}()

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
			jobs, err := db.GetSyncJobsForUser(s.db, userID)
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
			if string(data) == string(lastJSON) {
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
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrSyncIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owned, err := db.VerifySyncJobOwnership(s.db, id, userID)
	if err != nil || !owned {
		writeError(w, http.StatusForbidden, ErrSyncNotOwned)
		return
	}

	var req ThreadsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateSyncJobThreads(s.db, id, threads); err != nil {
		log.Printf("Error updating threads for sync job %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

