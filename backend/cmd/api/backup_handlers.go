package main

import (
	"database/sql"
	"net/http"
	"path"
	"strings"
	"time"

	"backend/internal/db"
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
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
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
	if !storage.IsValidProvider(source.Provider) || !storage.IsValidProvider(target.Provider) {
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
				source_refresh_token_encrypted, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
				target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_mega_session_id_encrypted,
				target_mega_master_key_encrypted, source_provider, target_provider, selected_paths, target_dir, repository_id, repository_root,
				cron_expression, timezone, retention_count, threads)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, repository.id,
				$19 || '/.clumoove-backup/' || repository.id::text, $20, $21, $22, $23 FROM repository RETURNING id`,
			userID, req.SourceProfileID, req.TargetProfileID, source.URL, source.Username, source.PasswordEncrypted, nullableEncrypted(source.RefreshTokenEncrypted), nullableEncrypted(source.MegaSessionIDEncrypted), nullableEncrypted(source.MegaMasterKeyEncrypted),
			target.URL, target.Username, target.PasswordEncrypted, nullableEncrypted(target.RefreshTokenEncrypted), nullableEncrypted(target.MegaSessionIDEncrypted), nullableEncrypted(target.MegaMasterKeyEncrypted), source.Provider, target.Provider, db.StringArray(paths), targetDir,
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
