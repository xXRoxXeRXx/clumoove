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
	"path"
	"sort"
	"strings"
	"time"

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

func (api *backupAPI) handleListBackups(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	jobs, err := db.ListBackupJobsForOwnerContext(r.Context(), api.db, userID)
	if err != nil {
		api.logf(r, "list backup jobs failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if jobs == nil {
		jobs = []db.BackupJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (api *backupAPI) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	job, err := db.GetBackupJobForOwnerContext(r.Context(), api.db, id, userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "get backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (api *backupAPI) handleListBackupSnapshots(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	snapshots, err := db.ListVisibleBackupSnapshotsContext(r.Context(), api.db, id)
	if err != nil {
		api.logf(r, "list backup snapshots failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if snapshots == nil {
		snapshots = []db.BackupSnapshot{}
	}
	writeJSON(w, http.StatusOK, snapshots)
}

func (api *backupAPI) handleListBackupSnapshotItems(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id, snapshotID := r.PathValue("id"), r.PathValue("snapshotID")
	if id == "" || snapshotID == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	directory := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	items, err := db.ListBackupSnapshotChildrenContext(r.Context(), api.db, id, snapshotID, directory)
	if err != nil {
		// Invalid paths and a snapshot outside this backup are deliberately not
		// distinguishable from an absent catalog item.
		if _, pathErr := db.NormalizeBackupSnapshotPath(directory); directory != "" && pathErr != nil {
			writeValidationError(w, ErrFolderPathInvalid)
			return
		}
		api.logf(r, "list backup snapshot items failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if items == nil {
		items = []db.BackupSnapshotItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (api *backupAPI) handleCreateRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "restore-preview", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, ok := api.requireOwnedBackupID(w, r, userID)
	if !ok {
		return
	}
	snapshotID := r.PathValue("snapshotID")
	if snapshotID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	var req createRestorePreviewRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	targetSnap, err := api.profiles.SnapshotRequest(r.Context(), userID, requestConnectionInput{
		ProfileID:      req.TargetProfileID,
		NormalizeURL:   true,
		Provider:       req.TargetProvider,
		URL:            req.TargetURL,
		Username:       req.TargetUsername,
		Password:       req.TargetPassword,
		RefreshToken:   req.TargetRefreshToken,
		TokenExpiresIn: req.TargetTokenExpiresIn,
	})
	if err != nil {
		writeRequestSnapshotErrorWith(w, r, api.logf, "resolve restore target connection", err)
		return
	}
	targetProfileID := targetSnap.ProfileID
	if targetSnap.Provider == "" {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	if targetSnap.Provider == "immich" {
		writeValidationError(w, ErrImmichBackupUnsupported)
		return
	}
	if !storage.IsValidProvider(targetSnap.Provider) || !storage.ProviderSupportsResourceType(targetSnap.Provider, "files") || storage.ValidateProviderURL(targetSnap.Provider, targetSnap.URL) != nil {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	if oauth.IsProvider(targetSnap.Provider) && !targetSnap.HasRefreshToken {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if req.TargetProfileID == "" && targetSnap.Provider != "local" && !targetSnap.HasPassword {
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
	backupJob, err := db.GetBackupJobForOwnerContext(r.Context(), api.db, backupID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "load backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	sameRepositoryAccount := backupJob.TargetProfileID.Valid && targetProfileID.Valid && backupJob.TargetProfileID.String == targetProfileID.String
	if !sameRepositoryAccount {
		sameRepositoryAccount = strings.EqualFold(backupJob.TargetProvider, targetSnap.Provider) && strings.EqualFold(strings.TrimSpace(backupJob.TargetURL), strings.TrimSpace(targetSnap.URL)) && strings.EqualFold(strings.TrimSpace(backupJob.TargetUsername), strings.TrimSpace(targetSnap.Username))
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
	err = api.db.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM backup_snapshots WHERE id = $1 AND backup_job_id = $2 AND state IN ('READY','PARTIAL') AND integrity_state <> 'DAMAGED')`, snapshotID, backupID).Scan(&exists)
	if err != nil {
		api.logf(r, "check restore snapshot failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, ErrRestoreSnapshotUnavailable)
		return
	}
	passwordEncrypted := targetSnap.Creds.PasswordEncrypted
	refreshEncrypted := targetSnap.Creds.RefreshTokenEncrypted
	expiresAt := targetSnap.TokenExpiresAt
	megaSessionID := targetSnap.Creds.MegaSessionIDEncrypted
	megaMasterKey := targetSnap.Creds.MegaMasterKeyEncrypted
	identity := "direct:" + strings.ToLower(targetSnap.Provider) + ":" + strings.ToLower(strings.TrimSpace(targetSnap.URL)) + ":" + strings.ToLower(strings.TrimSpace(targetSnap.Username))
	if targetProfileID.Valid {
		identity = "profile:" + targetProfileID.String + ":" + strings.ToLower(targetSnap.Provider) + ":" + strings.ToLower(strings.TrimSpace(targetSnap.URL)) + ":" + strings.ToLower(strings.TrimSpace(targetSnap.Username))
	}
	retryJobID := sql.NullString{}
	if req.RetryRestoreJobID != "" {
		var existingFingerprint []byte
		var retryBackupID, retrySnapshotID string
		var active bool
		err = api.db.QueryRowContext(r.Context(), `
			SELECT j.config_fingerprint, j.source_backup_ref, j.source_snapshot_ref,
				EXISTS (SELECT 1 FROM restore_runs r WHERE r.restore_job_id = j.id AND r.status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'))
			FROM restore_jobs j WHERE j.id = $1 AND j.user_id = $2`, req.RetryRestoreJobID, userID).Scan(&existingFingerprint, &retryBackupID, &retrySnapshotID, &active)
		if err != nil || active || retryBackupID != backupID || retrySnapshotID != snapshotID {
			writeConflictError(w, ErrRestorePreviewInvalidState)
			return
		}
		fingerprint, fingerprintErr := db.RestoreConfigFingerprintWithIdentity(snapshotID, db.StringArray(paths), targetSnap.Provider, targetRoot, identity, req.ConflictStrategy)
		if fingerprintErr != nil || !bytes.Equal(fingerprint[:], existingFingerprint) {
			writeConflictError(w, ErrRestorePreviewInvalidState)
			return
		}
		retryJobID = sql.NullString{String: req.RetryRestoreJobID, Valid: true}
	}
	previewID, err := db.CreateRestorePreviewContext(r.Context(), api.db, &db.RestorePreview{UserID: userID, BackupJobID: backupID, BackupSnapshotID: snapshotID, RetryRestoreJobID: retryJobID, TargetProfileID: targetProfileID, SelectedPaths: db.StringArray(paths), TargetProvider: targetSnap.Provider, TargetURL: targetSnap.URL, TargetUsername: targetSnap.Username, TargetPasswordEncrypted: sql.NullString{String: passwordEncrypted, Valid: passwordEncrypted != ""}, TargetRefreshTokenEncrypted: refreshEncrypted, TargetTokenExpiresAt: expiresAt, TargetMegaSessionIDEncrypted: sql.NullString{String: megaSessionID, Valid: megaSessionID != ""}, TargetMegaMasterKeyEncrypted: sql.NullString{String: megaMasterKey, Valid: megaMasterKey != ""}, TargetConnectionIdentity: identity, TargetRoot: targetRoot, ConflictStrategy: req.ConflictStrategy, Threads: req.Threads, BandwidthMbps: req.BandwidthMbps})
	if err != nil {
		api.logf(r, "create restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditRestorePreviewCreated, previewID, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": previewID})
}

func (api *backupAPI) handleGetRestorePreview(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	preview, err := db.GetRestorePreviewForOwnerContext(r.Context(), api.db, r.PathValue("previewID"), userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	if err != nil {
		api.logf(r, "get restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (api *backupAPI) handleCancelRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "restore-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	cancelled, err := db.CancelRestorePreviewForOwnerContext(r.Context(), api.db, r.PathValue("previewID"), userID)
	if err != nil {
		api.logf(r, "cancel restore preview failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	api.writeAudit(r, db.AuditRestorePreviewCancelled, r.PathValue("previewID"), userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *backupAPI) handleConsumeRestorePreview(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "restore-consume", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	job, run, err := db.ConsumeRestorePreviewContext(r.Context(), api.db, r.PathValue("previewID"), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, db.ErrRestorePreviewNotFound) {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		} else if errors.Is(err, db.ErrRestorePreviewExpired) {
			writeConflictError(w, ErrRestorePreviewExpired)
		} else if errors.Is(err, db.ErrRestoreSnapshotUnavailable) || errors.Is(err, db.ErrRestorePreviewStale) || errors.Is(err, db.ErrRestoreRetryMismatch) {
			writeConflictError(w, ErrRestorePreviewStale)
		} else if errors.Is(err, db.ErrRestorePreviewInvalidState) || errors.Is(err, db.ErrRestoreRetryActive) {
			writeConflictError(w, ErrRestorePreviewInvalidState)
		} else {
			api.logf(r, "consume restore preview failed: %v", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}
	api.writeAudit(r, db.AuditRestoreCreated, job.ID, userID, nil)
	api.writeAudit(r, db.AuditRestoreStarted, run.ID, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"restore_job_id": job.ID, "restore_run_id": run.ID})
}

func (api *backupAPI) handleListRestoreRuns(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	runs, err := db.ListRestoreRunsForOwnerContext(r.Context(), api.db, userID)
	if err != nil {
		api.logf(r, "list restore runs failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (api *backupAPI) handleGetRestoreRun(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), api.db, r.PathValue("runID"), userID)
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

func (api *backupAPI) handleRestoreRunStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !api.acquireBackupStream(w, r, userID, "restore-stream") {
		return
	}
	defer api.releaseBackupStream(userID)
	runID := r.PathValue("runID")
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), api.db, runID, userID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	if err != nil {
		api.logf(r, "stream restore run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.serveBackupDetailSSE(w, r, "restore", run, func(ctx context.Context) (any, error) {
		return db.GetRestoreRunForOwnerContext(ctx, api.db, runID, userID)
	})
}

func (api *backupAPI) handleListRestoreRunItems(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	runID := r.PathValue("runID")
	if _, err := db.GetRestoreRunForOwnerContext(r.Context(), api.db, runID, userID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
			return
		}
		api.logf(r, "list restore run items ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	items, err := db.ListRestoreItemsForOwnerContext(r.Context(), api.db, runID, userID)
	if err != nil {
		api.logf(r, "list restore items failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (api *backupAPI) handleCancelRestoreRun(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "restore-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	cancelled, err := db.CancelRestoreRunForOwnerContext(r.Context(), api.db, r.PathValue("runID"), userID)
	if err != nil {
		api.logf(r, "cancel restore run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeConflictError(w, ErrRestorePreviewInvalidState)
		return
	}
	api.writeAudit(r, db.AuditRestoreCancelled, r.PathValue("runID"), userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"success": true})
}

func (api *backupAPI) handleDeleteRestoreJob(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "restore-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	deleted, err := db.DeleteRestoreJobForOwnerContext(r.Context(), api.db, r.PathValue("jobID"), userID)
	if err != nil {
		api.logf(r, "delete restore job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, ErrRestoreNotFound)
		return
	}
	api.writeAudit(r, db.AuditRestoreDeleted, r.PathValue("jobID"), userID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (api *backupAPI) handleDownloadRestoreReport(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	runID := r.PathValue("runID")
	run, err := db.GetRestoreRunForOwnerContext(r.Context(), api.db, runID, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrRestoreNotFound)
			return
		}
		api.logf(r, "load restore report run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if run.Status != "COMPLETED" && run.Status != "PARTIAL" && run.Status != "FAILED" && run.Status != "CANCELLED" {
		writeConflictError(w, ErrRestorePreviewInvalidState)
		return
	}
	items, err := db.ListRestoreItemsForRunOwnerContext(r.Context(), api.db, runID, userID)
	if err != nil {
		api.logf(r, "create restore report failed: %v", err)
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
		if err := writer.Write([]string{csvCell(item.SnapshotRelativePath), csvCell(item.TargetPath), item.Status, csvCell(item.VerificationKind.String), fmt.Sprintf("%d", item.SizeBytes), csvCell(item.ErrorCode.String)}); err != nil {
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		api.logf(r, "write restore report failed: %v", err)
	}
}

func (api *backupAPI) requireOwnedBackupID(w http.ResponseWriter, r *http.Request, userID string) (string, bool) {
	backupID := r.PathValue("id")
	if backupID == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return "", false
	}
	if !api.requireBackupOwnership(w, r, backupID, userID) {
		return "", false
	}
	return backupID, true
}

func validateBackupScheduleAndTuning(w http.ResponseWriter, cronExpr, timezone string, retentionCount, threads int) (time.Time, bool) {
	if err := scheduler.ValidateCronExpression(cronExpr); err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return time.Time{}, false
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		writeValidationError(w, ErrBackupTimezoneInvalid)
		return time.Time{}, false
	}
	if retentionCount < 1 || retentionCount > 365 {
		writeValidationError(w, ErrBackupRetentionInvalid)
		return time.Time{}, false
	}
	if threads < 1 || threads > 16 {
		writeValidationError(w, ErrThreadsOutOfRange)
		return time.Time{}, false
	}
	nextRun, err := scheduler.NextRunInLocation(cronExpr, timezone, time.Now())
	if err != nil {
		writeValidationError(w, ErrBackupCronInvalid)
		return time.Time{}, false
	}
	return nextRun, true
}

func (api *backupAPI) prepareBackupSSE(w http.ResponseWriter, r *http.Request) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return nil, false
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		api.logf(r, "clear sse write deadline failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	return flusher, true
}

func (api *backupAPI) serveBackupDetailSSE(w http.ResponseWriter, r *http.Request, eventName string, initial any, fetch func(context.Context) (any, error)) {
	flusher, ok := api.prepareBackupSSE(w, r)
	if !ok {
		return
	}
	writeEvent := func(value any) ([]byte, error) {
		payload, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, payload); err != nil {
			return nil, err
		}
		flusher.Flush()
		return payload, nil
	}
	previous, err := writeEvent(initial)
	if err != nil {
		return
	}
	ticker := time.NewTicker(api.detailStreamPollInterval())
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
			current, err := fetch(r.Context())
			if err != nil {
				return
			}
			payload, err := json.Marshal(current)
			if err != nil {
				return
			}
			if !bytes.Equal(payload, previous) {
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, payload); err != nil {
					return
				}
				flusher.Flush()
				previous = payload
			}
		}
	}
}

func (api *backupAPI) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
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
	sourceSnap, err := api.profiles.SnapshotOwned(r.Context(), userID, req.SourceProfileID)
	if err != nil {
		api.writeBackupProfileResolutionError(w, err)
		return
	}
	targetSnap, err := api.profiles.SnapshotOwned(r.Context(), userID, req.TargetProfileID)
	if err != nil {
		api.writeBackupProfileResolutionError(w, err)
		return
	}
	source, sourcePersisted := sourceSnap.Profile, sourceSnap.Creds
	target, targetPersisted := targetSnap.Profile, targetSnap.Creds
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
	nextRun, valid := validateBackupScheduleAndTuning(w, req.CronExpression, req.Timezone, req.RetentionCount, req.Threads)
	if !valid {
		return
	}
	// The repository UUID is generated once by PostgreSQL and used in the same
	// statement to make the immutable root unguessable and collision-free.
	var id string
	tx, err := api.db.BeginTx(r.Context(), nil)
	if err == nil {
		err = tx.QueryRowContext(r.Context(), `WITH repository AS (SELECT gen_random_uuid() AS id)
			INSERT INTO backup_jobs (user_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
				source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
				target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted,
				target_mega_master_key_encrypted, source_provider, target_provider, selected_paths, target_dir, repository_id, repository_root,
				cron_expression, timezone, retention_count, threads)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, repository.id,
				$21 || '/.clumoove-backup/' || repository.id::text, $22, $23, $24, $25 FROM repository RETURNING id`,
			userID, req.SourceProfileID, req.TargetProfileID, source.URL, source.Username, sourcePersisted.PasswordEncrypted, sourcePersisted.RefreshTokenEncrypted, source.TokenExpiresAt, nullableEncrypted(sourcePersisted.MegaSessionIDEncrypted), nullableEncrypted(sourcePersisted.MegaMasterKeyEncrypted),
			target.URL, target.Username, targetPersisted.PasswordEncrypted, targetPersisted.RefreshTokenEncrypted, target.TokenExpiresAt, nullableEncrypted(targetPersisted.MegaSessionIDEncrypted), nullableEncrypted(targetPersisted.MegaMasterKeyEncrypted), source.Provider, target.Provider, db.StringArray(paths), targetDir,
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
		api.logf(r, "create backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditBackupCreated, id, userID, map[string]interface{}{"source_provider": source.Provider, "target_provider": target.Provider})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (api *backupAPI) writeBackupProfileResolutionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errProfileNotFound) || errors.Is(err, errProfileNotOwned) {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	writeError(w, http.StatusInternalServerError, ErrBackupConnectionFailed)
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

func (api *backupAPI) handleCreateBackupVerify(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "backup-verify", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, ok := api.requireOwnedBackupID(w, r, userID)
	if !ok {
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
	id, err := db.CreateBackupVerifyContext(r.Context(), api.db, backupID, userID, req.Mode, req.ByteBudget)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "create backup verify failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditRepositoryCheckCreated, id, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (api *backupAPI) handleCancelBackupVerify(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "backup-verify", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, ok := api.requireOwnedBackupID(w, r, userID)
	if !ok {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), api.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "verify check lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	cancelled, err := db.CancelBackupVerifyForOwnerContext(r.Context(), api.db, verifyID, userID)
	if err != nil {
		api.logf(r, "cancel repository check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !cancelled {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	api.writeAudit(r, db.AuditRepositoryCheckCancelled, verifyID, userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (api *backupAPI) handleListBackupVerifies(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID := r.PathValue("id")
	if backupID == "" || !api.requireBackupOwnership(w, r, backupID, userID) {
		return
	}
	checks, err := db.ListBackupVerifiesForOwnerContext(r.Context(), api.db, backupID, userID)
	if err != nil {
		api.logf(r, "list repository checks failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, checks)
}

func (api *backupAPI) handleGetBackupVerify(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, ok := api.requireOwnedBackupID(w, r, userID)
	if !ok {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), api.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "get repository check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, check)
}

func (api *backupAPI) handleBackupVerifyStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	backupID, ok := api.requireOwnedBackupID(w, r, userID)
	if !ok {
		return
	}
	verifyID := r.PathValue("verifyID")
	if verifyID == "" {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if !api.acquireBackupStream(w, r, userID, "backup-verify-stream") {
		return
	}
	defer api.releaseBackupStream(userID)
	check, err := db.GetBackupVerifyForOwnerContext(r.Context(), api.db, verifyID, userID)
	if err == sql.ErrNoRows || (check != nil && check.BackupJobID != backupID) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "stream repository check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.serveBackupDetailSSE(w, r, "repository-check", check, func(ctx context.Context) (any, error) {
		return db.GetBackupVerifyForOwnerContext(ctx, api.db, verifyID, userID)
	})
}

func (api *backupAPI) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	claim, err := db.ClaimBackupJobPassContext(r.Context(), api.db, id, "manual", nil)
	if err != nil {
		api.logf(r, "claim backup run failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if claim.Outcome != db.BackupClaimed {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	api.writeAudit(r, db.AuditBackupStarted, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// handleDeleteBackup starts durable repository cleanup. It intentionally does
// not remove the catalog synchronously: worker-side cleanup owns decrypted
// target credentials and can retry provider failures safely.
func (api *backupAPI) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	started, err := db.RequestBackupRepositoryDeletionContext(r.Context(), api.db, id, userID)
	if err != nil {
		api.logf(r, "request backup repository deletion failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !started {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	api.writeAudit(r, db.AuditBackupDeleted, id, userID, nil)
	writeJSON(w, http.StatusAccepted, map[string]bool{"success": true})
}

func (api *backupAPI) handlePauseBackup(w http.ResponseWriter, r *http.Request) {
	api.handleBackupStateChange(w, r, true)
}

func (api *backupAPI) handleResumeBackup(w http.ResponseWriter, r *http.Request) {
	api.handleBackupStateChange(w, r, false)
}

func (api *backupAPI) handleBackupStateChange(w http.ResponseWriter, r *http.Request, pause bool) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	tx, err := api.db.BeginTx(r.Context(), nil)
	if err != nil {
		api.logf(r, "begin backup state change failed: %v", err)
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
		api.logf(r, "change backup state failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		writeConflictError(w, ErrBackupInvalidState)
		return
	}
	if err := tx.Commit(); err != nil {
		api.logf(r, "commit backup state change failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if pause {
		api.writeAudit(r, db.AuditBackupPaused, id, userID, nil)
	} else {
		api.writeAudit(r, db.AuditBackupResumed, id, userID, nil)
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

func (api *backupAPI) handleListBackupRuns(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}
	runs, err := db.ListBackupRunsForOwnerContext(r.Context(), api.db, id, userID)
	if err != nil {
		api.logf(r, "list backup runs failed: %v", err)
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

func (api *backupAPI) handleUpdateBackup(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "migration-sync-mutation", jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeValidationError(w, ErrBackupIDMissing)
		return
	}
	if !api.requireBackupOwnership(w, r, id, userID) {
		return
	}

	var req updateBackupRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	nextRun, valid := validateBackupScheduleAndTuning(w, req.CronExpression, req.Timezone, req.RetentionCount, req.Threads)
	if !valid {
		return
	}

	err := db.UpdateBackupJobContext(r.Context(), api.db, id, userID, req.CronExpression, req.Timezone, req.RetentionCount, req.Threads, &nextRun)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return
	}
	if err != nil {
		api.logf(r, "update backup job failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	job, err := db.GetBackupJobForOwnerContext(r.Context(), api.db, id, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (api *backupAPI) handleBackupStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	if !api.acquireBackupStream(w, r, userID, "backup-stream") {
		return
	}
	defer api.releaseBackupStream(userID)

	flusher, ok := api.prepareBackupSSE(w, r)
	if !ok {
		return
	}

	ticker := time.NewTicker(api.listStreamPollInterval())
	defer ticker.Stop()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	var lastJSON []byte

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			jobs, err := db.ListBackupJobsForOwnerContext(r.Context(), api.db, userID)
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

			if _, err := fmt.Fprintf(w, "event: backup_jobs\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
			lastJSON = data
		}
	}
}
