package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/scheduler"
	"backend/internal/storage"
)

type createBackupRequest struct {
	SourceProfileID string   `json:"source_profile_id"`
	TargetProfileID string   `json:"target_profile_id"`
	SelectedPaths   []string `json:"selected_paths"`
	TargetDir       string   `json:"target_dir"`
	CronExpression  string   `json:"cron_expression"`
	Timezone        string   `json:"timezone"`
	RetentionCount  int      `json:"retention_count"`
	Threads         int      `json:"threads"`
}

type createRestorePreviewRequest struct {
	RetryRestoreJobID    string   `json:"retry_restore_job_id"`
	TargetProfileID      string   `json:"target_profile_id"`
	TargetProvider       string   `json:"target_provider"`
	TargetURL            string   `json:"target_url"`
	TargetUsername       string   `json:"target_username"`
	TargetPassword       string   `json:"target_password"`
	TargetRefreshToken   string   `json:"target_refresh_token"`
	TargetTokenExpiresIn int      `json:"target_token_expires_in"`
	SelectedPaths        []string `json:"selected_paths"`
	TargetRoot           string   `json:"target_root"`
	ConflictStrategy     string   `json:"conflict_strategy"`
	Threads              int      `json:"threads"`
	BandwidthMbps        int      `json:"bandwidth_mbps"`
}

type createBackupVerifyRequest struct {
	Mode        string `json:"mode"`
	ByteBudget  *int64 `json:"byte_budget"`
	ConfirmFull bool   `json:"confirm_full"`
}

func (s *APIServer) requireBackupOwnership(w http.ResponseWriter, r *http.Request, id, userID string) bool {
	owned, err := db.VerifyBackupJobOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return false
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return false
	}
	return true
}

func (s *APIServer) handleListBackups(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	jobs, err := db.ListBackupJobsForOwnerContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "list backup jobs failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if jobs == nil {
		jobs = []db.BackupJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *APIServer) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	job, err := db.GetBackupJobForOwnerContext(r.Context(), s.db, id, userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		s.logf(r, "get backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleListBackupSnapshots(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	snapshots, err := db.ListVisibleBackupSnapshotsContext(r.Context(), s.db, id)
	if err != nil {
		s.logf(r, "list backup snapshots failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if snapshots == nil {
		snapshots = []db.BackupSnapshot{}
	}
	writeJSON(w, http.StatusOK, snapshots)
}

func (s *APIServer) handleListBackupSnapshotItems(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	id, snapshotID := r.PathValue("id"), r.PathValue("snapshotID")
	if id == "" || snapshotID == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	directory := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	items, err := db.ListBackupSnapshotChildrenContext(r.Context(), s.db, id, snapshotID, directory)
	if err != nil {
		// Invalid paths and a snapshot outside this backup are deliberately not
		// distinguishable from an absent catalog item.
		if _, pathErr := db.NormalizeBackupSnapshotPath(directory); directory != "" && pathErr != nil {
			writeValidationError(w, ErrFolderPathInvalid)
			return
		}
		s.logf(r, "list backup snapshot items failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if items == nil {
		items = []db.BackupSnapshotItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *APIServer) handleCreateRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "restore-preview", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, snapshotID := r.PathValue("id"), r.PathValue("snapshotID")
	if backupID == "" || snapshotID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	var req createRestorePreviewRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	var targetProfileID sql.NullString
	targetCreds, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider: req.TargetProvider, URL: req.TargetURL, Username: req.TargetUsername,
		Password: req.TargetPassword, RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	if req.TargetProfileID != "" {
		targetProfileID = sql.NullString{String: req.TargetProfileID, Valid: true}
	}
	if targetCreds.Provider == "" {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	targetCreds.URL = normalizeProviderURL(targetCreds.Provider, targetCreds.URL)
	if targetCreds.Provider == "immich" {
		writeValidationError(w, ErrImmichBackupUnsupported)
		return
	}
	if !storage.IsValidProvider(targetCreds.Provider) || !storage.ProviderSupportsResourceType(targetCreds.Provider, "files") || storage.ValidateProviderURL(targetCreds.Provider, targetCreds.URL) != nil {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	if oauth.IsProvider(targetCreds.Provider) && targetCreds.RefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if req.TargetProfileID == "" && targetCreds.Provider != "local" && targetCreds.Password == "" {
		writeValidationError(w, ErrMissingRequiredFields)
		return
	}
	paths, valid := normalizeRestoreSnapshotPaths(req.SelectedPaths)
	if !valid {
		writeValidationError(w, ErrBackupPathsInvalid)
		return
	}
	targetRoot, valid := normalizeBackupPath(req.TargetRoot)
	if !valid {
		writeValidationError(w, ErrFolderPathInvalid)
		return
	}
	if strings.Contains("/"+strings.Trim(targetRoot, "/")+"/", "/.clumoove-backup/") {
		writeValidationError(w, ErrRestoreRepositoryOverlap)
		return
	}
	backupJob, err := db.GetBackupJobForOwnerContext(r.Context(), s.db, backupID, userID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	sameRepositoryAccount := backupJob.TargetProfileID.Valid && targetProfileID.Valid && backupJob.TargetProfileID.String == targetProfileID.String
	if !sameRepositoryAccount {
		sameRepositoryAccount = strings.EqualFold(backupJob.TargetProvider, targetCreds.Provider) && strings.EqualFold(strings.TrimSpace(backupJob.TargetURL), strings.TrimSpace(targetCreds.URL)) && strings.EqualFold(strings.TrimSpace(backupJob.TargetUsername), strings.TrimSpace(targetCreds.Username))
	}
	if sameRepositoryAccount && (targetRoot == backupJob.RepositoryRoot || strings.HasPrefix(targetRoot, backupJob.RepositoryRoot+"/") || strings.HasPrefix(backupJob.RepositoryRoot, targetRoot+"/")) {
		writeValidationError(w, ErrRestoreRepositoryOverlap)
		return
	}
	if req.ConflictStrategy == "" {
		req.ConflictStrategy = "RENAME"
	}
	if req.ConflictStrategy != "SKIP" && req.ConflictStrategy != "OVERWRITE" && req.ConflictStrategy != "RENAME" {
		writeValidationError(w, ErrConflictStrategyInvalid)
		return
	}
	if req.Threads == 0 {
		req.Threads = 8
	}
	if req.Threads < 1 || req.Threads > 16 {
		writeValidationError(w, ErrThreadsOutOfRange)
		return
	}
	if req.BandwidthMbps < 0 || req.BandwidthMbps > 1000 {
		writeValidationError(w, ErrBandwidthOutOfRange)
		return
	}
	var exists bool
	err = s.db.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM backup_snapshots WHERE id = $1 AND backup_job_id = $2 AND state IN ('READY','PARTIAL') AND integrity_state <> 'DAMAGED')`, snapshotID, backupID).Scan(&exists)
	if err != nil {
		s.logf(r, "check restore snapshot failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, ErrRestoreSnapshotUnavailable)
		return
	}
	passwordEncrypted, err := crypto.EncryptWithDomain(targetCreds.Password, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(targetCreds.Provider)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	var refreshEncrypted sql.NullString
	var expiresAt sql.NullTime
	if targetCreds.RefreshToken != "" {
		value, encryptErr := crypto.EncryptWithDomain(targetCreds.RefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
		if encryptErr != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		refreshEncrypted = sql.NullString{String: value, Valid: true}
		expiresIn := req.TargetTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		expiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}
	megaSessionID, megaMasterKey, err := s.encryptMegaSession(targetCreds.MegaSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	identity := "direct:" + strings.ToLower(targetCreds.Provider) + ":" + strings.ToLower(strings.TrimSpace(targetCreds.URL)) + ":" + strings.ToLower(strings.TrimSpace(targetCreds.Username))
	if targetProfileID.Valid {
		identity = "profile:" + targetProfileID.String + ":" + strings.ToLower(targetCreds.Provider) + ":" + strings.ToLower(strings.TrimSpace(targetCreds.URL)) + ":" + strings.ToLower(strings.TrimSpace(targetCreds.Username))
	}
	retryJobID := sql.NullString{}
	if req.RetryRestoreJobID != "" {
		var existingFingerprint []byte
		var retryBackupID, retrySnapshotID string
		var active bool
		err = s.db.QueryRowContext(r.Context(), `
			SELECT j.config_fingerprint, j.source_backup_ref, j.source_snapshot_ref,
				EXISTS (SELECT 1 FROM restore_runs r WHERE r.restore_job_id = j.id AND r.status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'))
			FROM restore_jobs j WHERE j.id = $1 AND j.user_id = $2`, req.RetryRestoreJobID, userID).Scan(&existingFingerprint, &retryBackupID, &retrySnapshotID, &active)
		if err != nil || active || retryBackupID != backupID || retrySnapshotID != snapshotID {
			writeConflictError(w, ErrRestorePreviewInvalidState)
			return
		}
		fingerprint, fingerprintErr := db.RestoreConfigFingerprintWithIdentity(snapshotID, db.StringArray(paths), targetCreds.Provider, targetRoot, identity, req.ConflictStrategy)
		if fingerprintErr != nil || !bytes.Equal(fingerprint[:], existingFingerprint) {
			writeConflictError(w, ErrRestorePreviewInvalidState)
			return
		}
		retryJobID = sql.NullString{String: req.RetryRestoreJobID, Valid: true}
	}
	previewID, err := db.CreateRestorePreviewContext(r.Context(), s.db, &db.RestorePreview{UserID: userID, BackupJobID: backupID, BackupSnapshotID: snapshotID, RetryRestoreJobID: retryJobID, TargetProfileID: targetProfileID, SelectedPaths: db.StringArray(paths), TargetProvider: targetCreds.Provider, TargetURL: targetCreds.URL, TargetUsername: targetCreds.Username, TargetPasswordEncrypted: sql.NullString{String: passwordEncrypted, Valid: passwordEncrypted != ""}, TargetRefreshTokenEncrypted: refreshEncrypted, TargetTokenExpiresAt: expiresAt, TargetMegaSessionIDEncrypted: sql.NullString{String: megaSessionID, Valid: megaSessionID != ""}, TargetMegaMasterKeyEncrypted: sql.NullString{String: megaMasterKey, Valid: megaMasterKey != ""}, TargetConnectionIdentity: identity, TargetRoot: targetRoot, ConflictStrategy: req.ConflictStrategy, Threads: req.Threads, BandwidthMbps: req.BandwidthMbps})
	if err != nil {
		s.logf(r, "create restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditRestorePreviewCreated, previewID, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": previewID})
}

func (s *APIServer) handleGetRestorePreview(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	preview, err := db.GetRestorePreviewForOwnerContext(r.Context(), s.db, r.PathValue("previewID"), userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	if err != nil {
		s.logf(r, "get restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *APIServer) handleCancelRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "restore-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	cancelled, err := db.CancelRestorePreviewForOwnerContext(r.Context(), s.db, r.PathValue("previewID"), userID)
	if err != nil {
		s.logf(r, "cancel restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	s.writeAudit(r, db.AuditRestorePreviewCancelled, r.PathValue("previewID"), userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleConsumeRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "restore-consume", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	job, run, err := db.ConsumeRestorePreviewContext(r.Context(), s.db, r.PathValue("previewID"), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, db.ErrRestorePreviewNotFound) {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		} else if errors.Is(err, db.ErrRestorePreviewExpired) {
			writeConflictError(w, ErrRestorePreviewExpired)
		} else if errors.Is(err, db.ErrRestoreSnapshotUnavailable) || errors.Is(err, db.ErrRestorePreviewStale) || errors.Is(err, db.ErrRestoreRetryMismatch) {
			writeConflictError(w, ErrRestorePreviewStale)
		} else {
			writeConflictError(w, ErrRestorePreviewInvalidState)
		}
		return
	}
	s.writeAudit(r, db.AuditRestoreCreated, job.ID, userID, nil)
	s.writeAudit(r, db.AuditRestoreStarted, run.ID, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"restore_job_id": job.ID, "restore_run_id": run.ID})
}

func (s *APIServer) handleListRestoreRuns(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	runs, err := db.ListRestoreRunsForOwnerContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "list restore runs failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *APIServer) handleGetRestoreRun(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), s.db, r.PathValue("runID"), userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *APIServer) handleRestoreRunStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !s.acquireStream(w, r, userID, "restore-stream") {
		return
	}
	defer s.releaseBackupStream(userID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	runID := r.PathValue("runID")
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), s.db, runID, userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		return
	}
	write := func(value *db.RestoreRun) ([]byte, error) {
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(w, "event: restore\ndata: %s\n\n", payload); err != nil {
			return nil, err
		}
		flusher.Flush()
		return payload, nil
	}
	previous, err := write(run)
	if err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			current, err := db.GetRestoreRunForOwnerContext(r.Context(), s.db, runID, userID)
			if err != nil {
				return
			}
			payload, err := json.Marshal(current)
			if err != nil {
				return
			}
			if !bytes.Equal(payload, previous) {
				if _, err := fmt.Fprintf(w, "event: restore\ndata: %s\n\n", payload); err != nil {
					return
				}
				flusher.Flush()
				previous = payload
			}
		}
	}
}

func (s *APIServer) handleListRestoreRunItems(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	runID := r.PathValue("runID")
	if _, err := db.GetRestoreRunForOwnerContext(r.Context(), s.db, runID, userID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
			return
		}
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	items, err := db.ListRestoreItemsForOwnerContext(r.Context(), s.db, runID, userID)
	if err != nil {
		s.logf(r, "list restore items failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *APIServer) handleCancelRestoreRun(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "restore-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	cancelled, err := db.CancelRestoreRunForOwnerContext(r.Context(), s.db, r.PathValue("runID"), userID)
	if err != nil {
		s.logf(r, "cancel restore run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeConflictError(w, ErrRestorePreviewInvalidState)
		return
	}
	s.writeAudit(r, db.AuditRestoreCancelled, r.PathValue("runID"), userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"success": true})
}

func (s *APIServer) handleDeleteRestoreJob(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "restore-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	deleted, err := db.DeleteRestoreJobForOwnerContext(r.Context(), s.db, r.PathValue("jobID"), userID)
	if err != nil {
		s.logf(r, "delete restore job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	s.writeAudit(r, db.AuditRestoreDeleted, r.PathValue("jobID"), userID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *APIServer) handleDownloadRestoreReport(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	runID := r.PathValue("runID")
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), s.db, runID, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
			return
		}
		s.logf(r, "load restore report run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if run.Status != "COMPLETED" && run.Status != "PARTIAL" && run.Status != "FAILED" && run.Status != "CANCELLED" {
		writeConflictError(w, ErrRestorePreviewInvalidState)
		return
	}
	items, err := db.ListRestoreItemsForRunOwnerContext(r.Context(), s.db, runID, userID)
	if err != nil {
		s.logf(r, "create restore report failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=restore-report.csv")
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"source_path", "target_path", "status", "verification", "size_bytes", "error_code"}); err != nil {
		return
	}
	for _, item := range items {
		if err := writer.Write([]string{neutralizeCSV(item.SnapshotRelativePath), neutralizeCSV(item.TargetPath), item.Status, neutralizeCSV(item.VerificationKind.String), fmt.Sprintf("%d", item.SizeBytes), neutralizeCSV(item.ErrorCode.String)}); err != nil {
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		s.logf(r, "write restore report failed: %v", err)
	}
}

func neutralizeCSV(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

func (s *APIServer) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	var req createBackupRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if req.SourceProfileID == "" || req.TargetProfileID == "" {
		writeValidationError(w, ErrMissingRequiredFields)
		return
	}
	source, err := db.GetConnectionProfile(r.Context(), s.db, req.SourceProfileID)
	if err != nil || source.UserID != userID {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	target, err := db.GetConnectionProfile(r.Context(), s.db, req.TargetProfileID)
	if err != nil || target.UserID != userID {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	if source.Provider == "immich" || target.Provider == "immich" {
		writeValidationError(w, ErrImmichBackupUnsupported)
		return
	}
	if !storage.IsValidProvider(source.Provider) || !storage.IsValidProvider(target.Provider) ||
		!storage.ProviderSupportsResourceType(source.Provider, "files") || !storage.ProviderSupportsResourceType(target.Provider, "files") {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	if err := storage.ValidateProviderURL(source.Provider, source.URL); err != nil {
		writeValidationError(w, ErrProfileURLRequired)
		return
	}
	if err := storage.ValidateProviderURL(target.Provider, target.URL); err != nil {
		writeValidationError(w, ErrProfileURLRequired)
		return
	}
	paths, valid := normalizeBackupPaths(req.SelectedPaths)
	if !valid {
		writeValidationError(w, ErrBackupPathsInvalid)
		return
	}
	targetDir, valid := normalizeBackupPath(req.TargetDir)
	if !valid {
		writeValidationError(w, ErrFolderPathInvalid)
		return
	}
	if sameBackupConnection(source, target) && anyBackupPathOverlaps(paths, targetDir) {
		writeValidationError(w, ErrBackupSourceTargetOverlap)
		return
	}
	if err := scheduler.ValidateCronExpression(req.CronExpression); err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeValidationError(w, ErrBackupTimezoneInvalid)
		return
	}
	if req.RetentionCount < 1 || req.RetentionCount > 365 {
		writeValidationError(w, ErrBackupRetentionInvalid)
		return
	}
	if req.Threads < 1 || req.Threads > 16 {
		writeValidationError(w, ErrThreadsOutOfRange)
		return
	}
	nextRun, err := scheduler.NextRunInLocation(req.CronExpression, req.Timezone, time.Now())
	if err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return
	}
	// The repository UUID is generated once by PostgreSQL and used in the same
	// statement to make the immutable root unguessable and collision-free.
	var id string
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err == nil {
		err = tx.QueryRowContext(r.Context(), `WITH repository AS (SELECT gen_random_uuid() AS id)
			INSERT INTO backup_jobs (user_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
				source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
				target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted,
				target_mega_master_key_encrypted, source_provider, target_provider, selected_paths, target_dir, repository_id, repository_root,
				cron_expression, timezone, retention_count, threads)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, repository.id,
				$21 || '/.clumoove-backup/' || repository.id::text, $22, $23, $24, $25 FROM repository RETURNING id`,
			userID, req.SourceProfileID, req.TargetProfileID, source.URL, source.Username, source.PasswordEncrypted, nullableEncrypted(source.RefreshTokenEncrypted), source.TokenExpiresAt, nullableEncrypted(source.MegaSessionIDEncrypted), nullableEncrypted(source.MegaMasterKeyEncrypted),
			target.URL, target.Username, target.PasswordEncrypted, nullableEncrypted(target.RefreshTokenEncrypted), target.TokenExpiresAt, nullableEncrypted(target.MegaSessionIDEncrypted), nullableEncrypted(target.MegaMasterKeyEncrypted), source.Provider, target.Provider, db.StringArray(paths), targetDir,
			req.CronExpression, req.Timezone, req.RetentionCount, req.Threads).Scan(&id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO schedules (user_id, task_type, task_id, cron_expression, next_run_at, is_active) VALUES ($1, 'backup', $2, $3, $4, TRUE)`, userID, id, req.CronExpression, nextRun)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		s.logf(r, "create backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditBackupCreated, id, userID, map[string]interface{}{"source_provider": source.Provider, "target_provider": target.Provider})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func nullableEncrypted(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// sameBackupConnection conservatively identifies two saved profiles that point
// at the same account. Credentials are deliberately not used for this check.
func sameBackupConnection(source, target *db.ConnectionProfile) bool {
	if source.ID != "" && source.ID == target.ID {
		return true
	}
	if oauth.IsProvider(source.Provider) || oauth.IsProvider(target.Provider) {
		return strings.EqualFold(source.Provider, target.Provider) && source.OAuthUser != "" &&
			strings.EqualFold(source.OAuthUser, target.OAuthUser)
	}
	return strings.EqualFold(source.Provider, target.Provider) &&
		strings.EqualFold(strings.TrimRight(source.URL, "/"), strings.TrimRight(target.URL, "/")) &&
		strings.EqualFold(strings.TrimSpace(source.Username), strings.TrimSpace(target.Username))
}

func anyBackupPathOverlaps(selected []string, targetDir string) bool {
	for _, selectedPath := range selected {
		if backupPathContains(selectedPath, targetDir) || backupPathContains(targetDir, selectedPath) {
			return true
		}
	}
	return false
}

func backupPathContains(parent, candidate string) bool {
	return parent == "/" || candidate == parent || strings.HasPrefix(candidate, parent+"/")
}

func (s *APIServer) handleCreateBackupVerify(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "backup-verify", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	var req createBackupVerifyRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if req.Mode != db.BackupVerifyMetadata && req.Mode != db.BackupVerifyBudgeted && req.Mode != db.BackupVerifyFull {
		writeValidationError(w, ErrBackupVerifyInvalid)
		return
	}
	if req.Mode == db.BackupVerifyBudgeted && (req.ByteBudget == nil || *req.ByteBudget < 64<<20 || *req.ByteBudget > 1<<40) {
		writeValidationError(w, ErrBackupVerifyInvalid)
		return
	}
	if req.Mode != db.BackupVerifyBudgeted && req.ByteBudget != nil {
		writeValidationError(w, ErrBackupVerifyInvalid)
		return
	}
	if req.Mode == db.BackupVerifyFull && !req.ConfirmFull {
		writeValidationError(w, ErrBackupVerifyInvalid)
		return
	}
	id, err := db.CreateBackupVerifyContext(r.Context(), s.db, backupID, userID, req.Mode, req.ByteBudget)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		writeValidationError(w, ErrBackupVerifyInvalid)
		return
	}
	s.writeAudit(r, db.AuditRepositoryCheckCreated, id, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *APIServer) handleCancelBackupVerify(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "backup-verify", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), s.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		s.logf(r, "verify check lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	cancelled, err := db.CancelBackupVerifyForOwnerContext(r.Context(), s.db, verifyID, userID)
	if err != nil {
		s.logf(r, "cancel repository check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	s.writeAudit(r, db.AuditRepositoryCheckCancelled, verifyID, userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *APIServer) handleListBackupVerifies(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	checks, err := db.ListBackupVerifiesForOwnerContext(r.Context(), s.db, backupID, userID)
	if err != nil {
		s.logf(r, "list repository checks failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, checks)
}

func (s *APIServer) handleGetBackupVerify(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), s.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		s.logf(r, "get repository check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, check)
}

func (s *APIServer) handleBackupVerifyStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !s.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if !s.acquireStream(w, r, userID, "backup-verify-stream") {
		return
	}
	defer s.releaseBackupStream(userID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), s.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		return
	}
	write := func(value *db.BackupMaintenance) ([]byte, error) {
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(w, "event: repository-check\ndata: %s\n\n", payload); err != nil {
			return nil, err
		}
		flusher.Flush()
		return payload, nil
	}
	previous, err := write(check)
	if err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			current, err := db.GetBackupVerifyForOwnerContext(r.Context(), s.db, verifyID, userID)
			if err != nil {
				return
			}
			payload, err := json.Marshal(current)
			if err != nil {
				return
			}
			if !bytes.Equal(payload, previous) {
				if _, err := fmt.Fprintf(w, "event: repository-check\ndata: %s\n\n", payload); err != nil {
					return
				}
				flusher.Flush()
				previous = payload
			}
		}
	}
}

func (s *APIServer) handleRunBackup(w http.ResponseWriter, r *http.Request) {
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
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	claim, err := db.ClaimBackupJobPassContext(r.Context(), s.db, id, "manual", nil)
	if err != nil {
		s.logf(r, "claim backup run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if claim.Outcome != db.BackupClaimed {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	s.writeAudit(r, db.AuditBackupStarted, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// handleDeleteBackup starts durable repository cleanup. It intentionally does
// not remove the catalog synchronously: worker-side cleanup owns decrypted
// target credentials and can retry provider failures safely.
func (s *APIServer) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
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
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	started, err := db.RequestBackupRepositoryDeletionContext(r.Context(), s.db, id, userID)
	if err != nil {
		s.logf(r, "request backup repository deletion failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !started {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	s.writeAudit(r, db.AuditBackupDeleted, id, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"success": true})
}

func (s *APIServer) handlePauseBackup(w http.ResponseWriter, r *http.Request) {
	s.handleBackupStateChange(w, r, true)
}

func (s *APIServer) handleResumeBackup(w http.ResponseWriter, r *http.Request) {
	s.handleBackupStateChange(w, r, false)
}

func (s *APIServer) handleBackupStateChange(w http.ResponseWriter, r *http.Request, pause bool) {
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
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		s.logf(r, "begin backup state change failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	defer tx.Rollback()

	var result sql.Result
	if pause {
		result, err = tx.ExecContext(r.Context(), `UPDATE backup_jobs SET status = 'PAUSED' WHERE id = $1 AND user_id = $2 AND status IN ('IDLE','FAILED','QUEUED','SCANNING','RUNNING','VERIFYING')`, id, userID)
		if err == nil {
			updated, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				err = rowsErr
			} else if updated != 1 {
				writeConflictError(w, ErrBackupInvalidState)
				return
			}
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE backup_runs SET state = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE backup_job_id = $1 AND state IN ('QUEUED','SCANNING','RUNNING','VERIFYING')`, id)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE schedules SET is_active = FALSE WHERE task_type = 'backup' AND task_id = $1`, id)
		}
	} else {
		var cronExpression, timezone string
		err = tx.QueryRowContext(r.Context(), `SELECT cron_expression, timezone FROM backup_jobs WHERE id = $1 AND user_id = $2 AND status = 'PAUSED' FOR UPDATE`, id, userID).Scan(&cronExpression, &timezone)
		if err == sql.ErrNoRows {
			writeConflictError(w, ErrBackupInvalidState)
			return
		}
		if err == nil {
			result, err = tx.ExecContext(r.Context(), `UPDATE backup_jobs SET status = 'IDLE' WHERE id = $1 AND user_id = $2 AND status = 'PAUSED'`, id, userID)
		}
		if err == nil {
			nextRun, nextErr := scheduler.NextRunInLocation(cronExpression, timezone, time.Now())
			if nextErr != nil {
				err = nextErr
			} else {
				_, err = tx.ExecContext(r.Context(), `UPDATE schedules SET is_active = TRUE, next_run_at = $1 WHERE task_type = 'backup' AND task_id = $2`, nextRun, id)
			}
		}
	}
	if err != nil {
		s.logf(r, "change backup state failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	if err := tx.Commit(); err != nil {
		s.logf(r, "commit backup state change failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if pause {
		s.writeAudit(r, db.AuditBackupPaused, id, userID, nil)
	} else {
		s.writeAudit(r, db.AuditBackupResumed, id, userID, nil)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func normalizeBackupPaths(paths []string) ([]string, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, candidate := range paths {
		clean, valid := normalizeBackupPath(candidate)
		if !valid {
			return nil, false
		}
		if _, exists := seen[clean]; exists {
			return nil, false
		}
		seen[clean] = struct{}{}
		normalized = append(normalized, clean)
	}
	for _, candidate := range normalized {
		for _, other := range normalized {
			if candidate != other && (candidate == "/" || strings.HasPrefix(other, candidate+"/")) {
				return nil, false
			}
		}
	}
	return normalized, true
}

func normalizeBackupPath(value string) (string, bool) {
	if value == "" || !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", false
		}
	}
	clean := path.Clean(value)
	if clean == "." || !strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

func normalizeRestoreSnapshotPaths(paths []string) ([]string, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		clean := strings.TrimPrefix(value, "/")
		if clean != "" {
			var err error
			clean, err = db.NormalizeBackupSnapshotPath(clean)
			if err != nil {
				return nil, false
			}
		}
		if _, exists := seen[clean]; exists {
			return nil, false
		}
		seen[clean] = struct{}{}
	}
	normalized := make([]string, 0, len(seen))
	for value := range seen {
		for other := range seen {
			if value != other && strings.HasPrefix(other, value+"/") {
				return nil, false
			}
		}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, true
}

func (s *APIServer) handleListBackupRuns(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}
	runs, err := db.ListBackupRunsForOwnerContext(r.Context(), s.db, id, userID)
	if err != nil {
		s.logf(r, "list backup runs failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if runs == nil {
		runs = []db.BackupRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

type updateBackupRequest struct {
	CronExpression string `json:"cron_expression"`
	Timezone       string `json:"timezone"`
	RetentionCount int    `json:"retention_count"`
	Threads        int    `json:"threads"`
}

func (s *APIServer) handleUpdateBackup(w http.ResponseWriter, r *http.Request) {
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
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !s.requireBackupOwnership(w, r, id, userID) {
		return
	}

	var req updateBackupRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if err := scheduler.ValidateCronExpression(req.CronExpression); err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil {
		writeValidationError(w, ErrBackupTimezoneInvalid)
		return
	}
	if req.RetentionCount < 1 || req.RetentionCount > 365 {
		writeValidationError(w, ErrBackupRetentionInvalid)
		return
	}
	if req.Threads < 1 || req.Threads > 16 {
		writeValidationError(w, ErrThreadsOutOfRange)
		return
	}

	nextRun, err := scheduler.NextRunInLocation(req.CronExpression, req.Timezone, time.Now())
	if err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return
	}

	err = db.UpdateBackupJobContext(r.Context(), s.db, id, userID, req.CronExpression, req.Timezone, req.RetentionCount, req.Threads, &nextRun)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		s.logf(r, "update backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	job, err := db.GetBackupJobForOwnerContext(r.Context(), s.db, id, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *APIServer) handleBackupStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	if !s.acquireStream(w, r, userID, "backup-stream") {
		return
	}
	defer s.releaseBackupStream(userID)

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

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	var lastJSON []byte

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			jobs, err := db.ListBackupJobsForOwnerContext(r.Context(), s.db, userID)
			if err != nil {
				continue
			}
			if jobs == nil {
				jobs = []db.BackupJob{}
			}

			data, err := json.Marshal(jobs)
			if err != nil {
				continue
			}

			if bytes.Equal(data, lastJSON) {
				continue
			}

			lastJSON = data
			fmt.Fprintf(w, "event: backup_jobs\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
