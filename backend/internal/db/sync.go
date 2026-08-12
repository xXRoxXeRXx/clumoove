package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// SyncJob represents a continuous synchronization job.
type SyncJob struct {
	ID                           string         `json:"id"`
	UserID                       string         `json:"user_id"`
	SourceURL                    string         `json:"source_url"`
	SourceUsername               string         `json:"source_username"`
	SourcePasswordEncrypted      string         `json:"-"`
	SourceRefreshTokenEncrypted  sql.NullString `json:"-"`
	SourceTokenExpiresAt         sql.NullTime   `json:"source_token_expires_at,omitempty"`
	SourceMegaSessionIDEncrypted string         `json:"-"`
	SourceMegaMasterKeyEncrypted string         `json:"-"`
	TargetURL                    string         `json:"target_url"`
	TargetUsername               string         `json:"target_username"`
	TargetPasswordEncrypted      string         `json:"-"`
	TargetRefreshTokenEncrypted  sql.NullString `json:"-"`
	TargetTokenExpiresAt         sql.NullTime   `json:"target_token_expires_at,omitempty"`
	TargetMegaSessionIDEncrypted string         `json:"-"`
	TargetMegaMasterKeyEncrypted string         `json:"-"`
	SourceProvider               string         `json:"source_provider"`
	TargetProvider               string         `json:"target_provider"`
	Direction                    string         `json:"direction"`
	ConflictStrategy             string         `json:"conflict_strategy"`
	DeletePropagation            bool           `json:"delete_propagation"`
	IntervalMinutes              int            `json:"interval_minutes"`
	Threads                      int            `json:"threads"`
	BandwidthLimitMbps           int            `json:"bandwidth_limit_mbps"`
	Status                       string         `json:"status"` // IDLE, INDEXING, RUNNING, PAUSED, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED
	RunGeneration                int            `json:"run_generation"`
	VerificationGeneration       int            `json:"-"`
	VerificationLeaseUntil       sql.NullTime   `json:"-"`
	TargetDir                    string         `json:"target_dir"`
	SelectedPaths                StringArray    `json:"selected_paths,omitempty"`
	LastRunAt                    sql.NullTime   `json:"last_run_at,omitempty"`
	NextRunAt                    sql.NullTime   `json:"next_run_at,omitempty"`
	LastRunStatus                sql.NullString `json:"last_run_status,omitempty"`
	ErrorMessage                 sql.NullString `json:"error_message,omitempty"`
	TotalFiles                   int            `json:"total_files"`
	TotalBytes                   int64          `json:"total_bytes"`
	ProcessedFiles               int            `json:"processed_files"`
	ProcessedBytes               int64          `json:"processed_bytes"`
	LiveBytes                    int64          `json:"live_bytes"`
	ChangedFiles                 int            `json:"changed_files"`
	DeletedFiles                 int            `json:"deleted_files"`
	FailedFiles                  int            `json:"failed_files"`
	ActiveFiles                  []string       `json:"active_files,omitempty"`
	CreatedAt                    time.Time      `json:"created_at"`
	UpdatedAt                    time.Time      `json:"updated_at"`
}

// MarshalJSON serializes the sync job with nullable columns (sql.NullString,
// sql.NullTime) resolved to plain JSON strings/null so frontend consumers don't
// receive raw driver structs like {"String":"...","Valid":true}.
func (s SyncJob) MarshalJSON() ([]byte, error) {
	type alias SyncJob
	aux := struct {
		*alias
		LastRunStatus        string  `json:"last_run_status,omitempty"`
		ErrorMessage         string  `json:"error_message,omitempty"`
		LastRunAt            *string `json:"last_run_at,omitempty"`
		NextRunAt            *string `json:"next_run_at,omitempty"`
		SourceTokenExpiresAt *string `json:"source_token_expires_at,omitempty"`
		TargetTokenExpiresAt *string `json:"target_token_expires_at,omitempty"`
	}{
		alias: (*alias)(&s),
	}
	if s.LastRunStatus.Valid {
		aux.LastRunStatus = s.LastRunStatus.String
	}
	if s.ErrorMessage.Valid {
		aux.ErrorMessage = s.ErrorMessage.String
	}
	if s.LastRunAt.Valid {
		iso := s.LastRunAt.Time.Format(time.RFC3339)
		aux.LastRunAt = &iso
	}
	if s.NextRunAt.Valid {
		iso := s.NextRunAt.Time.Format(time.RFC3339)
		aux.NextRunAt = &iso
	}
	if s.SourceTokenExpiresAt.Valid {
		iso := s.SourceTokenExpiresAt.Time.Format(time.RFC3339)
		aux.SourceTokenExpiresAt = &iso
	}
	if s.TargetTokenExpiresAt.Valid {
		iso := s.TargetTokenExpiresAt.Time.Format(time.RFC3339)
		aux.TargetTokenExpiresAt = &iso
	}
	return json.Marshal(aux)
}

// SyncState represents the last known state of a file or directory during a sync
type SyncState struct {
	ID         string       `json:"id"`
	SyncJobID  string       `json:"sync_job_id"`
	Side       string       `json:"side"` // source, target
	RelPath    string       `json:"rel_path"`
	Size       int64        `json:"size"`
	Mtime      sql.NullTime `json:"mtime,omitempty"`
	SourceHash string       `json:"source_hash,omitempty"`
	TargetHash string       `json:"target_hash,omitempty"`
	ETag       string       `json:"etag,omitempty"`
}

// SyncStateDelete identifies one baseline row to remove during a sync-state
// reconciliation.
type SyncStateDelete struct {
	SyncJobID string
	Side      string
	RelPath   string
}

const (
	finalizeRunningSyncPass = "status IN ('RUNNING', 'VERIFYING')"
	finalizeEmptySyncPass   = "status = 'INDEXING'"
)

const createSyncJobQuery = `
		INSERT INTO sync_jobs (
			user_id, source_url, source_username, source_password_encrypted,
			source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
			target_url, target_username, target_password_encrypted,
			target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted,
			source_provider, target_provider, direction, conflict_strategy,
			delete_propagation, interval_minutes, threads, bandwidth_limit_mbps, status, target_dir,
			selected_paths
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
		RETURNING id, created_at, updated_at
	`

// CreateSyncJob inserts a sync job without a schedule. Production callers that
// need periodic execution must use CreateSyncJobAndSchedule.
func CreateSyncJob(db *sql.DB, s *SyncJob) (string, error) {
	if err := insertSyncJob(context.Background(), db, s); err != nil {
		return "", err
	}
	return s.ID, nil
}

func insertSyncJob(ctx context.Context, database queryExecerContext, s *SyncJob) error {
	return database.QueryRowContext(ctx,
		createSyncJobQuery,
		s.UserID, s.SourceURL, s.SourceUsername, s.SourcePasswordEncrypted,
		s.SourceRefreshTokenEncrypted, s.SourceTokenExpiresAt, s.SourceMegaSessionIDEncrypted, s.SourceMegaMasterKeyEncrypted,
		s.TargetURL, s.TargetUsername, s.TargetPasswordEncrypted,
		s.TargetRefreshTokenEncrypted, s.TargetTokenExpiresAt, s.TargetMegaSessionIDEncrypted, s.TargetMegaMasterKeyEncrypted,
		s.SourceProvider, s.TargetProvider, s.Direction, s.ConflictStrategy,
		s.DeletePropagation, s.IntervalMinutes, s.Threads, s.BandwidthLimitMbps, s.Status, s.TargetDir,
		s.SelectedPaths,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// CreateSyncJobAndSchedule creates a sync job and its periodic schedule atomically.
// A sync job without a schedule cannot run automatically, so neither record may be
// committed unless both inserts succeed.
func CreateSyncJobAndSchedule(db *sql.DB, job *SyncJob, schedule *Schedule) (string, error) {
	resetSyncJobAndSchedule(job, schedule)
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			resetSyncJobAndSchedule(job, schedule)
		}
	}()

	if err := insertSyncJob(context.Background(), tx, job); err != nil {
		return "", err
	}

	schedule.TaskID = job.ID
	if err := insertSchedule(context.Background(), tx, schedule); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return job.ID, nil
}

func resetSyncJobAndSchedule(job *SyncJob, schedule *Schedule) {
	job.ID = ""
	job.CreatedAt = time.Time{}
	job.UpdatedAt = time.Time{}
	schedule.ID = ""
	schedule.TaskID = ""
	schedule.CreatedAt = time.Time{}
	schedule.UpdatedAt = time.Time{}
}

// GetSyncJob retrieves a sync job by ID
func GetSyncJob(db *sql.DB, id string) (*SyncJob, error) {
	return GetSyncJobContext(context.Background(), db, id)
}

// GetSyncJobContext retrieves a sync job while honoring caller cancellation.
func GetSyncJobContext(ctx context.Context, db *sql.DB, id string) (*SyncJob, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       source_refresh_token_encrypted, source_token_expires_at, COALESCE(source_mega_session_id_encrypted, ''), COALESCE(source_mega_master_key_encrypted, ''),
		       target_url, target_username, target_password_encrypted,
		       target_refresh_token_encrypted, target_token_expires_at, COALESCE(target_mega_session_id_encrypted, ''), COALESCE(target_mega_master_key_encrypted, ''),
		       source_provider, target_provider, direction, conflict_strategy,
		       delete_propagation, interval_minutes, threads, bandwidth_limit_mbps, status, run_generation, verification_generation, verification_lease_until, target_dir,
		       selected_paths, last_run_at, last_run_status, error_message,
		       (SELECT next_run_at FROM schedules WHERE task_type = 'sync' AND task_id = sync_jobs.id AND is_active = TRUE LIMIT 1),
		       total_files, total_bytes, processed_files, processed_bytes, live_bytes, changed_files, deleted_files, failed_files,
		       created_at, updated_at
		FROM sync_jobs WHERE id = $1
	`
	var s SyncJob
	err := db.QueryRowContext(ctx, query, id).Scan(
		&s.ID, &s.UserID, &s.SourceURL, &s.SourceUsername, &s.SourcePasswordEncrypted,
		&s.SourceRefreshTokenEncrypted, &s.SourceTokenExpiresAt, &s.SourceMegaSessionIDEncrypted, &s.SourceMegaMasterKeyEncrypted,
		&s.TargetURL, &s.TargetUsername, &s.TargetPasswordEncrypted,
		&s.TargetRefreshTokenEncrypted, &s.TargetTokenExpiresAt, &s.TargetMegaSessionIDEncrypted, &s.TargetMegaMasterKeyEncrypted,
		&s.SourceProvider, &s.TargetProvider, &s.Direction, &s.ConflictStrategy,
		&s.DeletePropagation, &s.IntervalMinutes, &s.Threads, &s.BandwidthLimitMbps, &s.Status, &s.RunGeneration, &s.VerificationGeneration, &s.VerificationLeaseUntil, &s.TargetDir,
		&s.SelectedPaths, &s.LastRunAt, &s.LastRunStatus, &s.ErrorMessage, &s.NextRunAt,
		&s.TotalFiles, &s.TotalBytes, &s.ProcessedFiles, &s.ProcessedBytes, &s.LiveBytes, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSyncJobOwnerID returns the owning user_id for a sync job.
func GetSyncJobOwnerID(database queryExecerContext, syncJobID string) (string, error) {
	var owner sql.NullString
	err := database.QueryRowContext(context.Background(), `SELECT user_id FROM sync_jobs WHERE id = $1`, syncJobID).Scan(&owner)
	if err != nil {
		return "", err
	}
	if !owner.Valid {
		return "", fmt.Errorf("sync job %s has no owner", syncJobID)
	}
	return owner.String, nil
}

// GetSyncJobsForUser lists all sync jobs for a user
func GetSyncJobsForUser(db *sql.DB, userID string) ([]SyncJob, error) {
	return GetSyncJobsForUserContext(context.Background(), db, userID)
}

// GetSyncJobsForUserContext lists sync jobs while honoring caller cancellation.
func GetSyncJobsForUserContext(ctx context.Context, db *sql.DB, userID string) ([]SyncJob, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_provider,
		       target_url, target_username, target_provider, direction, conflict_strategy,
		       delete_propagation, interval_minutes, threads, bandwidth_limit_mbps, status, target_dir,
		       selected_paths, last_run_at, last_run_status, error_message,
		       (SELECT next_run_at FROM schedules WHERE task_type = 'sync' AND task_id = sync_jobs.id AND is_active = TRUE LIMIT 1),
		       total_files, total_bytes, processed_files, processed_bytes, live_bytes, changed_files, deleted_files, failed_files,
		       created_at, updated_at
		FROM sync_jobs
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []SyncJob
	for rows.Next() {
		var s SyncJob
		err := rows.Scan(
			&s.ID, &s.UserID, &s.SourceURL, &s.SourceUsername, &s.SourceProvider,
			&s.TargetURL, &s.TargetUsername, &s.TargetProvider, &s.Direction, &s.ConflictStrategy,
			&s.DeletePropagation, &s.IntervalMinutes, &s.Threads, &s.BandwidthLimitMbps, &s.Status, &s.TargetDir,
			&s.SelectedPaths, &s.LastRunAt, &s.LastRunStatus, &s.ErrorMessage, &s.NextRunAt,
			&s.TotalFiles, &s.TotalBytes, &s.ProcessedFiles, &s.ProcessedBytes, &s.LiveBytes, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
			&s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// UpdateSyncJobStatus updates the status of a sync job
func UpdateSyncJobStatus(db *sql.DB, id string, status string, errMsg *string) error {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	query := `
		UPDATE sync_jobs
		SET status = $1,
		    verification_lease_until = CASE WHEN $1 = 'VERIFYING' THEN verification_lease_until ELSE NULL END,
		    error_message = CASE WHEN $2::text IS NOT NULL THEN $2 ELSE error_message END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := db.Exec(query, status, errVal, id)
	return err
}

// UpdateSyncJobStatusForGeneration prevents a late worker from changing a
// successor pass's lifecycle state.
func UpdateSyncJobStatusForGeneration(db *sql.DB, id string, generation int, status string, errMsg *string) error {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	_, err := db.Exec(`UPDATE sync_jobs SET status = $1, verification_lease_until = CASE WHEN $1 = 'VERIFYING' THEN verification_lease_until ELSE NULL END, error_message = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3 AND run_generation = $4`, status, errVal, id, generation)
	return err
}

// UpdateSyncJobBandwidthLimit persists a sync job's transfer limit in Mbps.
// A zero value means unlimited.
func UpdateSyncJobBandwidthLimit(db *sql.DB, id string, limitMbps int) error {
	result, err := db.Exec(`
		UPDATE sync_jobs
		SET bandwidth_limit_mbps = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`, limitMbps, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// PauseSyncJob atomically pauses an active job, or an idle job whose schedule
// the user wants to disable. A connection-loss pause is deliberately excluded:
// it remains owned by the automatic recovery flow.
func PauseSyncJob(db *sql.DB, id string, errMsg *string) (bool, error) {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	var pausedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'PAUSED', verification_lease_until = NULL,
		    error_message = CASE WHEN $1::text IS NOT NULL THEN $1 ELSE error_message END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND status IN ('IDLE', 'INDEXING', 'RUNNING', 'VERIFYING')
		RETURNING id
	`, errVal, id).Scan(&pausedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ResumeSyncJob atomically returns a user-paused job to IDLE so a new pass can
// be claimed. It must not overwrite a live or connection-recovery lifecycle.
func ResumeSyncJob(db *sql.DB, id string, errMsg *string) (bool, error) {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	var resumedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'IDLE',
		    error_message = CASE WHEN $1::text IS NOT NULL THEN $1 ELSE error_message END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND status = 'PAUSED'
		RETURNING id
	`, errVal, id).Scan(&resumedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// TransitionSyncJobToRunning atomically starts task processing for an indexed
// pass. It prevents a superseded INDEXING pass from reviving another status.
func TransitionSyncJobToRunning(db *sql.DB, id string, generation int) (bool, error) {
	var transitionedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'RUNNING', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'INDEXING' AND run_generation = $2
		RETURNING id
	`, id, generation).Scan(&transitionedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// TransitionSyncJobToVerifying atomically hands an active pass to checksum
// verification. It must only transition from RUNNING so a task-worker failure
// cannot be revived as VERIFYING.
func TransitionSyncJobToVerifying(db *sql.DB, id string, generation int) (bool, error) {
	var transitionedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'VERIFYING', verification_lease_until = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'RUNNING' AND run_generation = $2
		RETURNING id
	`, id, generation).Scan(&transitionedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AbortSyncJobVerification withdraws a sync pass from the worker verifier while
// keeping the engine as the sole owner of final run statistics and the eventual
// RUNNING -> IDLE transition. RUNNING is safe here because all transfer tasks
// were already observed as terminal before verification began.
func AbortSyncJobVerification(db *sql.DB, id string, generation int) (bool, error) {
	var transitionedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'RUNNING', verification_lease_until = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'VERIFYING' AND run_generation = $2
		RETURNING id
	`, id, generation).Scan(&transitionedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpdateSyncJobOAuthTokens persists a rotated OAuth token pair for a sync job.
// Keeping this update in db makes token rotation identical for the API engine
// and the worker-side checksum verifier. Uses conditional persistence to prevent
// concurrent token rotation from overwriting a newer token pair.
func UpdateSyncJobOAuthTokens(db *sql.DB, id, role, accessTokenEncrypted, refreshTokenEncrypted string, expiresAt time.Time, expectedRefreshTokenEncrypted string) error {
	if role != "source" && role != "target" {
		return fmt.Errorf("invalid oauth token role %q", role)
	}
	if expectedRefreshTokenEncrypted == "" {
		return ErrOAuthTokenConflict
	}
	var query string
	if role == "source" {
		query = `
			UPDATE sync_jobs
			SET source_password_encrypted        = $1,
			    source_refresh_token_encrypted   = $2,
			    source_token_expires_at          = $3,
			    updated_at                       = CURRENT_TIMESTAMP
			WHERE id = $4
			  AND source_refresh_token_encrypted = $5
		`
	} else {
		query = `
			UPDATE sync_jobs
			SET target_password_encrypted        = $1,
			    target_refresh_token_encrypted   = $2,
			    target_token_expires_at          = $3,
			    updated_at                       = CURRENT_TIMESTAMP
			WHERE id = $4
			  AND target_refresh_token_encrypted = $5
		`
	}
	res, err := db.Exec(query, accessTokenEncrypted, refreshTokenEncrypted, expiresAt, id, expectedRefreshTokenEncrypted)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrOAuthTokenConflict
	}
	return nil
}

func UpdateSyncJobOAuthTokensForReauth(db *sql.DB, id, role, accessTokenEncrypted, refreshTokenEncrypted string, expiresAt time.Time) error {
	if role != "source" && role != "target" {
		return fmt.Errorf("invalid oauth token role %q", role)
	}
	columns := "source_password_encrypted = $1, source_refresh_token_encrypted = $2, source_token_expires_at = $3"
	if role == "target" {
		columns = "target_password_encrypted = $1, target_refresh_token_encrypted = $2, target_token_expires_at = $3"
	}
	_, err := db.Exec("UPDATE sync_jobs SET "+columns+", updated_at = CURRENT_TIMESTAMP WHERE id = $4", accessTokenEncrypted, refreshTokenEncrypted, expiresAt, id)
	return err
}

// ExpiringOAuthSyncJob describes a sync job credential near OAuth token expiry.
type ExpiringOAuthSyncJob struct {
	SyncJobID             string
	Role                  string
	Provider              string
	RefreshTokenEncrypted string
}

// GetExpiringOAuthSyncJobs returns active sync jobs whose OAuth access tokens expire within 15 minutes.
func GetExpiringOAuthSyncJobs(db *sql.DB) ([]ExpiringOAuthSyncJob, error) {
	threshold := time.Now().Add(15 * time.Minute)
	query := `
		SELECT id, 'source' AS role, source_provider, source_refresh_token_encrypted
		FROM sync_jobs
		WHERE status IN ('INDEXING', 'RUNNING')
		  AND source_refresh_token_encrypted IS NOT NULL
		  AND source_token_expires_at IS NOT NULL
		  AND source_token_expires_at < $1
		UNION ALL
		SELECT id, 'target' AS role, target_provider, target_refresh_token_encrypted
		FROM sync_jobs
		WHERE status IN ('INDEXING', 'RUNNING')
		  AND target_refresh_token_encrypted IS NOT NULL
		  AND target_token_expires_at IS NOT NULL
		  AND target_token_expires_at < $1
	`
	rows, err := db.Query(query, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ExpiringOAuthSyncJob
	for rows.Next() {
		var e ExpiringOAuthSyncJob
		if err := rows.Scan(&e.SyncJobID, &e.Role, &e.Provider, &e.RefreshTokenEncrypted); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// ClaimSyncJobPass atomically reserves a manually runnable sync job for a new
// pass. A successful claim moves the job to INDEXING before its pass starts.
func ClaimSyncJobPass(database *sql.DB, id string) (generation int, claimed bool, err error) {
	err = database.QueryRow(`
		UPDATE sync_jobs
		SET status = 'INDEXING', run_generation = run_generation + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status IN ('IDLE', 'FAILED')
		RETURNING run_generation
	`, id).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return generation, true, nil
}

// ReleaseUnstartedSyncPass prevents a failed coordinator startup from leaving
// an otherwise runnable job permanently in INDEXING. It deliberately only
// releases INDEXING: a concurrent pause or lifecycle transition is preserved.
func ReleaseUnstartedSyncPass(database *sql.DB, id string, generation int) (bool, error) {
	var releasedID string
	err := database.QueryRow(`
		UPDATE sync_jobs
		SET status = 'IDLE', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'INDEXING' AND run_generation = $2
		RETURNING id
	`, id, generation).Scan(&releasedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RecoverConnectionLostSyncJob atomically releases a connection-loss-paused
// sync job and makes its active schedule due now. Workers intentionally do
// not start a pass: the API scheduler is the sole sync-pass coordinator.
func RecoverConnectionLostSyncJob(database *sql.DB, ctx context.Context, id string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET status = 'IDLE', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'PAUSED_CONNECTION_LOSS'
	`, id)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedules
		SET next_run_at = NOW()
		WHERE task_type = 'sync' AND task_id = $1 AND is_active = TRUE
	`, id); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateSyncJobTotals updates counters for its INDEXING pass. A false result
// means the pass was superseded or otherwise left INDEXING before the update.
func UpdateSyncJobTotals(db *sql.DB, id string, generation, totalFiles int, totalBytes int64) (bool, error) {
	var updatedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET total_files = $1, total_bytes = $2, processed_files = 0, processed_bytes = 0, live_bytes = 0, changed_files = 0, deleted_files = 0, failed_files = 0, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3 AND run_generation = $4 AND status = 'INDEXING'
		RETURNING id
	`, totalFiles, totalBytes, id, generation).Scan(&updatedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AddSyncJobLiveBytes adds bytes to the live_bytes counter of a sync job for real-time speed display
func AddSyncJobLiveBytes(db *sql.DB, ctx context.Context, id string, bytesDelta int64) error {
	if bytesDelta <= 0 {
		return nil
	}
	query := `UPDATE sync_jobs SET live_bytes = live_bytes + $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.ExecContext(ctx, query, bytesDelta, id)
	return err
}

// AddSyncJobLiveBytesForGeneration prevents a late worker from changing the
// live counter after its sync pass has been superseded.
func AddSyncJobLiveBytesForGeneration(db *sql.DB, ctx context.Context, id string, generation int, bytesDelta int64) error {
	if bytesDelta <= 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE sync_jobs SET live_bytes = live_bytes + $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2 AND run_generation = $3 AND status = 'RUNNING'`, bytesDelta, id, generation)
	return err
}

// UpdateSyncJobRunStats updates all statistics and final status at the end of a sync run
func UpdateSyncJobRunStats(db *sql.DB, id string, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int) error {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}
	query := `
		UPDATE sync_jobs
		SET last_run_status = $1,
		    error_message = $2,
		    last_run_at = CURRENT_TIMESTAMP,
		    total_files = $3,
		    processed_files = $4,
		    changed_files = $5,
		    deleted_files = $6,
		    failed_files = $7,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $8
	`
	_, err := db.Exec(query, lastRunStatus, errVal, total, processed, changed, deleted, failed, id)
	return err
}

// FinalizeSyncJobPass records a transfer/verification pass and returns it to
// IDLE only from RUNNING or VERIFYING. It must not accept INDEXING: a newly
// resumed pass can be INDEXING while a cancelled predecessor is winding down.
func FinalizeSyncJobPass(db *sql.DB, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int) (bool, error) {
	return finalizeSyncJobPassWithStates(db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, nil, nil, finalizeRunningSyncPass)
}

// FinalizeSyncJobPassWithStates atomically stores the next delta baseline and
// exposes the completed pass. A state-write failure therefore leaves the job
// active instead of reporting a success that cannot be reconciled next pass.
func FinalizeSyncJobPassWithStates(db *sql.DB, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return finalizeSyncJobPassWithStates(db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes, finalizeRunningSyncPass)
}

// FinalizeEmptySyncJobPass completes an index-only pass that produced no
// tasks. This is deliberately separate from transfer finalization.
func FinalizeEmptySyncJobPass(db *sql.DB, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int) (bool, error) {
	return finalizeSyncJobPassWithStates(db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, nil, nil, finalizeEmptySyncPass)
}

// FinalizeEmptySyncJobPassWithStates is the index-only equivalent of
// FinalizeSyncJobPassWithStates.
func FinalizeEmptySyncJobPassWithStates(db *sql.DB, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return finalizeSyncJobPassWithStates(db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes, finalizeEmptySyncPass)
}

func finalizeSyncJobPassWithStates(database *sql.DB, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete, predicate string) (bool, error) {
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		return false, fmt.Errorf("begin sync finalization: %w", err)
	}
	defer tx.Rollback()

	finalized, err := finalizeSyncJobPassTx(tx, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, predicate)
	if err != nil || !finalized {
		return finalized, err
	}
	if err := bulkUpsertSyncStatesTx(tx, upserts, deletes); err != nil {
		return false, fmt.Errorf("finalize sync state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit sync finalization: %w", err)
	}
	// Notification creation is deliberately best-effort and happens after this
	// transaction. The durable lifecycle and delta baseline are atomic; as with
	// prior finalization, a process crash before this best-effort handoff can
	// omit the completion notification.
	if err := CreateSyncNotificationEvent(database, id); err != nil {
		log.Printf("notification event creation for sync %s failed: %v", id, err)
	}
	return true, nil
}

func finalizeSyncJobPassTx(tx *sql.Tx, id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, predicate string) (bool, error) {
	var errVal sql.NullString
	if errMsg != nil {
		errVal = sql.NullString{String: *errMsg, Valid: true}
	}

	var finalizedID string
	err := tx.QueryRow(`
		UPDATE sync_jobs
		SET status = 'IDLE', verification_lease_until = NULL,
		    last_run_status = $1,
		    error_message = $2,
		    last_run_at = CURRENT_TIMESTAMP,
		    total_files = $3,
		    processed_files = $4,
		    changed_files = $5,
		    deleted_files = $6,
		    failed_files = $7,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $8 AND run_generation = $9 AND `+predicate+`
		RETURNING id
	`, lastRunStatus, errVal, total, processed, changed, deleted, failed, id, generation).Scan(&finalizedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// FailSyncJobPass records an engine failure without overwriting a concurrent
// FAILED or PAUSED_* decision made elsewhere. Engine indexing failures remain
// FAILED so the UI exposes the hard failure until a later pass claims the job.
func FailSyncJobPass(db *sql.DB, id string, generation int, errMsg string) (bool, error) {
	var failedID string
	err := db.QueryRow(`
		UPDATE sync_jobs
		SET status = 'FAILED', verification_lease_until = NULL,
		    last_run_status = 'FAILED',
		    error_message = $1,
		    last_run_at = CURRENT_TIMESTAMP,
		    total_files = 0,
		    processed_files = 0,
		    changed_files = 0,
		    deleted_files = 0,
		    failed_files = 0,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND run_generation = $3 AND status IN ('INDEXING', 'RUNNING', 'VERIFYING')
		RETURNING id
	`, errMsg, id, generation).Scan(&failedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := CreateSyncNotificationEvent(db, id); err != nil {
		log.Printf("notification event creation for sync %s failed: %v", id, err)
	}
	return true, nil
}

// ListActiveSyncJobs lists sync jobs that are active (running or indexing) or enabled (idle)
func ListActiveSyncJobs(db *sql.DB) ([]SyncJob, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       source_refresh_token_encrypted, source_token_expires_at,
		       target_url, target_username, target_password_encrypted,
		       target_refresh_token_encrypted, target_token_expires_at,
		       source_provider, target_provider, direction, conflict_strategy,
		       delete_propagation, interval_minutes, threads, status, target_dir,
		       selected_paths, last_run_at, last_run_status, error_message,
		       total_files, total_bytes, processed_files, processed_bytes, live_bytes, changed_files, deleted_files, failed_files,
		       created_at, updated_at
		FROM sync_jobs
		WHERE status IN ('IDLE', 'INDEXING', 'RUNNING', 'PAUSED_CONNECTION_LOSS')
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []SyncJob
	for rows.Next() {
		var s SyncJob
		err := rows.Scan(
			&s.ID, &s.UserID, &s.SourceURL, &s.SourceUsername, &s.SourcePasswordEncrypted,
			&s.SourceRefreshTokenEncrypted, &s.SourceTokenExpiresAt,
			&s.TargetURL, &s.TargetUsername, &s.TargetPasswordEncrypted,
			&s.TargetRefreshTokenEncrypted, &s.TargetTokenExpiresAt,
			&s.SourceProvider, &s.TargetProvider, &s.Direction, &s.ConflictStrategy,
			&s.DeletePropagation, &s.IntervalMinutes, &s.Threads, &s.Status, &s.TargetDir,
			&s.SelectedPaths, &s.LastRunAt, &s.LastRunStatus, &s.ErrorMessage,
			&s.TotalFiles, &s.TotalBytes, &s.ProcessedFiles, &s.ProcessedBytes, &s.LiveBytes, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
			&s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// VerifySyncJobOwnership checks if a sync job belongs to a specific user
func VerifySyncJobOwnership(db *sql.DB, syncJobID, userID string) (bool, error) {
	return VerifySyncJobOwnershipContext(context.Background(), db, syncJobID, userID)
}

// VerifySyncJobOwnershipContext verifies ownership while honoring caller cancellation.
func VerifySyncJobOwnershipContext(ctx context.Context, db *sql.DB, syncJobID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM sync_jobs WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRowContext(ctx, query, syncJobID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// DeleteSyncJobCascade deletes a sync job and all related tasks / state / schedules
func DeleteSyncJobCascade(db *sql.DB, syncJobID string) error {
	_ = DeleteSchedulesForTask(db, "sync", syncJobID)
	query := `DELETE FROM sync_jobs WHERE id = $1`
	_, err := db.Exec(query, syncJobID)
	return err
}

func CancelRemainingPendingSyncTasksForGeneration(dbsql *sql.DB, syncJobID string, generation int) (int, error) {
	res, err := dbsql.Exec(`UPDATE tasks SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'PENDING'`, syncJobID, generation)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// CancelOpenSyncTasksForPause cancels work that no worker owns. RUNNING rows
// deliberately remain RUNNING: their terminal transition is the durable worker
// acknowledgement that makes it safe to begin the next pass.
func CancelOpenSyncTasksForPause(dbsql *sql.DB, syncJobID string) (int, error) {
	res, err := dbsql.Exec(`
		UPDATE tasks
		SET status = 'CANCELLED', worker_hash = NULL, next_retry_at = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE sync_job_id = $1
		  AND pass_generation = (SELECT run_generation FROM sync_jobs WHERE id = $1)
		  AND (status = 'PENDING' OR (status = 'FAILED' AND next_retry_at IS NOT NULL))
	`, syncJobID)
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	return int(rows), err
}

// ReconcileSyncJobProgress repairs progress counter drift for a sync job
func ReconcileSyncJobProgress(dbsql *sql.DB, syncJobID string, generation int) error {
	tx, err := dbsql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		SELECT 
			COUNT(*) FILTER (WHERE status = 'COMPLETED') as completed,
			COUNT(*) FILTER (WHERE status = 'SKIPPED') as skipped,
			COUNT(*) FILTER (WHERE status = 'FAILED') as failed,
			COUNT(*) FILTER (WHERE status = 'CANCELLED') as cancelled,
			COUNT(*) FILTER (WHERE status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL)) as open
		FROM tasks
		WHERE sync_job_id = $1 AND pass_generation = $2
	`
	var completed, skipped, failed, cancelled, open int
	err = tx.QueryRow(query, syncJobID, generation).Scan(&completed, &skipped, &failed, &cancelled, &open)
	if err != nil {
		return err
	}

	total := completed + skipped + failed + cancelled + open
	if total == 0 {
		return tx.Commit()
	}

	// Always repair cached file counts
	updateQuery := `
		UPDATE sync_jobs
		SET processed_files = $1,
		    failed_files = $2,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $3 AND run_generation = $4 AND status = 'RUNNING'
	`
	if _, err := tx.Exec(updateQuery, completed+skipped, failed+cancelled, syncJobID, generation); err != nil {
		return err
	}

	// Only reconcile a RUNNING pass. INDEXING intentionally has no tasks while
	// listing remote trees and building its delta; the engine alone finalizes it.
	if open == 0 {
		finalRunStatus := "SUCCESS"
		var finalErr *string
		if failed > 0 {
			if failed == total {
				finalRunStatus = "FAILED"
				msg := "All file transfer tasks failed"
				finalErr = &msg
			} else {
				finalRunStatus = "PARTIAL"
				msg := fmt.Sprintf("%d of %d tasks failed", failed, total)
				finalErr = &msg
			}
		}

		statusQuery := `
			UPDATE sync_jobs
			SET status = 'IDLE',
			    last_run_status = $1,
			    error_message = COALESCE($2, error_message),
			    last_run_at = CURRENT_TIMESTAMP,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = $3 AND run_generation = $4 AND status = 'RUNNING'
		`
		var finalizedID string
		err = tx.QueryRow(statusQuery+` RETURNING id`, finalRunStatus, finalErr, syncJobID, generation).Scan(&finalizedID)
		if errors.Is(err, sql.ErrNoRows) {
			return tx.Commit()
		}
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		if err := CreateSyncNotificationEvent(dbsql, syncJobID); err != nil {
			log.Printf("notification event creation for reconciled sync %s failed: %v", syncJobID, err)
		}
		return nil
	}

	return tx.Commit()
}

// GetFailedSyncTasksForReport retrieves failed tasks for one sync pass.
func GetFailedSyncTasksForReport(db *sql.DB, syncJobID string, generation int) ([]Task, error) {
	query := `
		SELECT id, sync_job_id, file_path, file_size, source_hash, worker_hash, target_hash,
		       status, error_message, attempts, next_retry_at, created_at, updated_at, resource_type, metadata
		FROM tasks
		WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'FAILED'
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, syncJobID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		var syncID sql.NullString
		err := rows.Scan(
			&t.ID, &syncID, &t.FilePath, &t.FileSize, &t.SourceHash, &t.WorkerHash, &t.TargetHash,
			&t.Status, &t.ErrorMessage, &t.Attempts, &t.NextRetryAt, &t.CreatedAt, &t.UpdatedAt, &t.ResourceType, &t.Metadata,
		)
		if err != nil {
			return nil, err
		}
		if syncID.Valid {
			t.SyncJobID = syncID.String
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// IncrementSyncJobProgress updates processed/changed/deleted/failed counters and bytes
func IncrementSyncJobProgress(db *sql.DB, ctx context.Context, id string, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		UPDATE sync_jobs
		SET processed_files = processed_files + $1,
		    processed_bytes = processed_bytes + $2,
		    live_bytes = GREATEST(live_bytes, processed_bytes + $2),
		    changed_files = changed_files + $3,
		    deleted_files = deleted_files + $4,
		    failed_files = failed_files + $5,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $6
	`
	result, err := tx.ExecContext(ctx, query, filesDelta, bytesDelta, changedDelta, deletedDelta, failedDelta, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}

	return tx.Commit()
}

func IncrementSyncJobProgressForGeneration(db *sql.DB, ctx context.Context, id string, generation, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error {
	res, err := db.ExecContext(ctx, `UPDATE sync_jobs SET processed_files = processed_files + $1, processed_bytes = processed_bytes + $2, live_bytes = GREATEST(live_bytes, processed_bytes + $2), changed_files = changed_files + $3, deleted_files = deleted_files + $4, failed_files = failed_files + $5, updated_at = CURRENT_TIMESTAMP WHERE id = $6 AND run_generation = $7 AND status = 'RUNNING'`, filesDelta, bytesDelta, changedDelta, deletedDelta, failedDelta, id, generation)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateSyncTaskStatusAndIncrementProgress persists a sync task's terminal
// status and its job counters as one logical transition.  A task must never be
// left RUNNING while its job reports that it has been processed.
func UpdateSyncTaskStatusAndIncrementProgress(db *sql.DB, ctx context.Context, t *Task, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE tasks
		SET status = $1, attempts = $2, error_message = $3, next_retry_at = $4, worker_hash = $5,
		    source_hash = $6, target_hash = $7, checksum_verified = $8, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9 AND sync_job_id = $10 AND status = 'RUNNING' AND claim_epoch = $11 AND pass_generation = $12
	`, t.Status, t.Attempts, t.ErrorMessage, t.NextRetryAt, t.WorkerHash,
		t.SourceHash, t.TargetHash, t.ChecksumVerified, t.ID, t.SyncJobID, t.ClaimEpoch, t.PassGeneration)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}

	result, err = tx.ExecContext(ctx, `
		UPDATE sync_jobs
		SET processed_files = processed_files + $1,
		    processed_bytes = processed_bytes + $2,
		    changed_files = changed_files + $3,
		    deleted_files = deleted_files + $4,
		    failed_files = failed_files + $5,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $6 AND run_generation = $7 AND status = 'RUNNING'
	`, filesDelta, bytesDelta, changedDelta, deletedDelta, failedDelta, t.SyncJobID, t.PassGeneration)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		// A paused pass still records the worker's terminal acknowledgement, but
		// its counters must remain untouched.
		var generation int
		if err := tx.QueryRowContext(ctx, `SELECT run_generation FROM sync_jobs WHERE id = $1`, t.SyncJobID).Scan(&generation); err != nil {
			return err
		}
		if generation != t.PassGeneration {
			return sql.ErrNoRows
		}
	}

	return tx.Commit()
}

// UpdateClaimedSyncTaskStatus records a worker acknowledgement only for the
// coordinator pass that created the task.
func UpdateClaimedSyncTaskStatus(db *sql.DB, ctx context.Context, t *Task) error {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks SET status = $1, attempts = $2, error_message = $3, next_retry_at = $4,
			worker_hash = $5, source_hash = $6, target_hash = $7, checksum_verified = $8, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9 AND sync_job_id = $10 AND status = 'RUNNING' AND claim_epoch = $11 AND pass_generation = $12
	`, t.Status, t.Attempts, t.ErrorMessage, t.NextRetryAt, t.WorkerHash, t.SourceHash, t.TargetHash, t.ChecksumVerified,
		t.ID, t.SyncJobID, t.ClaimEpoch, t.PassGeneration)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// UpsertSyncState inserts or updates a sync state row
func UpsertSyncState(db *sql.DB, s *SyncState) error {
	var etagVal sql.NullString
	if s.ETag != "" {
		etagVal = sql.NullString{String: s.ETag, Valid: true}
	}
	query := `
		INSERT INTO sync_state (sync_job_id, side, rel_path, size, mtime, source_hash, target_hash, etag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (sync_job_id, side, rel_path) DO UPDATE SET
			size = EXCLUDED.size,
			mtime = EXCLUDED.mtime,
			source_hash = EXCLUDED.source_hash,
			target_hash = EXCLUDED.target_hash,
			etag = EXCLUDED.etag
	`
	_, err := db.Exec(query, s.SyncJobID, s.Side, s.RelPath, s.Size, s.Mtime, s.SourceHash, s.TargetHash, etagVal)
	return err
}

// GetSyncState retrieves a single sync state
func GetSyncState(db *sql.DB, syncJobID, side, relPath string) (*SyncState, error) {
	query := `
		SELECT id, sync_job_id, side, rel_path, size, mtime, source_hash, target_hash, etag
		FROM sync_state
		WHERE sync_job_id = $1 AND side = $2 AND rel_path = $3
	`
	var s SyncState
	var etagVal sql.NullString
	err := db.QueryRow(query, syncJobID, side, relPath).Scan(
		&s.ID, &s.SyncJobID, &s.Side, &s.RelPath, &s.Size, &s.Mtime, &s.SourceHash, &s.TargetHash, &etagVal,
	)
	if err != nil {
		return nil, err
	}
	if etagVal.Valid {
		s.ETag = etagVal.String
	}
	return &s, nil
}

// DeleteSyncState deletes a single sync state entry
func DeleteSyncState(db *sql.DB, syncJobID, side, relPath string) error {
	query := `DELETE FROM sync_state WHERE sync_job_id = $1 AND side = $2 AND rel_path = $3`
	_, err := db.Exec(query, syncJobID, side, relPath)
	return err
}

// ListSyncStateByJob lists all sync states for a job
func ListSyncStateByJob(db *sql.DB, syncJobID string) ([]SyncState, error) {
	query := `
		SELECT id, sync_job_id, side, rel_path, size, mtime, source_hash, target_hash, etag
		FROM sync_state WHERE sync_job_id = $1
	`
	rows, err := db.Query(query, syncJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []SyncState
	for rows.Next() {
		var s SyncState
		var etagVal sql.NullString
		err := rows.Scan(&s.ID, &s.SyncJobID, &s.Side, &s.RelPath, &s.Size, &s.Mtime, &s.SourceHash, &s.TargetHash, &etagVal)
		if err != nil {
			return nil, err
		}
		if etagVal.Valid {
			s.ETag = etagVal.String
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

// BulkCreateSyncTasks inserts sync tasks in batches of batchSize rows per statement.
// This is dramatically faster than N individual INSERTs for large sync passes with
// many files (e.g. 1000 files → 2 DB round-trips instead of 1000).
func BulkCreateSyncTasks(ctx context.Context, db *sql.DB, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	if ctx.Err() != nil {
		log.Printf("Warning: flushing bulk task batch (%d tasks) after parent context expired: %v", len(tasks), ctx.Err())
	}

	const batchSize = 2000

	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	tx, err := db.BeginTx(dbCtx, nil)
	if err != nil {
		return fmt.Errorf("bulk create tasks: begin tx: %w", err)
	}
	defer tx.Rollback()

	for start := 0; start < len(tasks); start += batchSize {
		end := start + batchSize
		if end > len(tasks) {
			end = len(tasks)
		}
		batch := tasks[start:end]

		// Each row has 9 params: migration_id, sync_job_id, pass_generation,
		// file_path, file_size, source_hash, status, resource_type, metadata.
		const paramsPerRow = 9
		args := make([]interface{}, 0, len(batch)*paramsPerRow)
		valuesClauses := make([]string, 0, len(batch))

		for i, t := range batch {
			base := i * paramsPerRow
			var migID, syncID sql.NullString
			if t.MigrationID != "" {
				migID = sql.NullString{String: t.MigrationID, Valid: true}
			}
			if t.SyncJobID != "" {
				syncID = sql.NullString{String: t.SyncJobID, Valid: true}
			}
			args = append(args,
				migID, syncID, t.PassGeneration, t.FilePath, t.FileSize, t.SourceHash, t.Status, t.ResourceType, t.Metadata,
			)
			valuesClauses = append(valuesClauses,
				fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
					base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9),
			)
		}

		query := "INSERT INTO tasks (migration_id, sync_job_id, pass_generation, file_path, file_size, source_hash, status, resource_type, metadata) VALUES " +
			strings.Join(valuesClauses, ",")

		if _, err := tx.ExecContext(dbCtx, query, args...); err != nil {
			return fmt.Errorf("bulk create tasks: insert batch [%d:%d]: %w", start, end, err)
		}
	}

	return tx.Commit()
}

// BulkUpsertSyncStates inserts or updates many sync_state rows inside a single
// transaction. For each (sync_job_id, side, rel_path) pair that already exists
// the size/mtime/hash columns are updated; new rows are inserted.
// This replaces the per-file UpsertSyncState loop and is dramatically faster for
// large directory trees (e.g. 1000 files → 1 tx with 1000 statements vs 1000 txs).
func BulkUpsertSyncStates(db *sql.DB, upserts []*SyncState, deletes []SyncStateDelete) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("bulk upsert sync states: begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := bulkUpsertSyncStatesTx(tx, upserts, deletes); err != nil {
		return err
	}
	return tx.Commit()
}

func bulkUpsertSyncStatesTx(tx *sql.Tx, upserts []*SyncState, deletes []SyncStateDelete) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	// Prepare the upsert statement once and reuse it for all rows.
	upsertStmt, err := tx.Prepare(`
		INSERT INTO sync_state (sync_job_id, side, rel_path, size, mtime, source_hash, target_hash, etag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (sync_job_id, side, rel_path) DO UPDATE SET
			size        = EXCLUDED.size,
			mtime       = EXCLUDED.mtime,
			source_hash = EXCLUDED.source_hash,
			target_hash = EXCLUDED.target_hash,
			etag        = EXCLUDED.etag
	`)
	if err != nil {
		return fmt.Errorf("bulk upsert sync states: prepare upsert: %w", err)
	}
	defer upsertStmt.Close()

	for _, s := range upserts {
		var etagVal sql.NullString
		if s.ETag != "" {
			etagVal = sql.NullString{String: s.ETag, Valid: true}
		}
		if _, err := upsertStmt.Exec(s.SyncJobID, s.Side, s.RelPath, s.Size, s.Mtime, s.SourceHash, s.TargetHash, etagVal); err != nil {
			return fmt.Errorf("bulk upsert sync states: exec upsert %s/%s/%s: %w", s.SyncJobID, s.Side, s.RelPath, err)
		}
	}

	// Prepare delete statement once and reuse it.
	if len(deletes) > 0 {
		deleteStmt, err := tx.Prepare(`DELETE FROM sync_state WHERE sync_job_id = $1 AND side = $2 AND rel_path = $3`)
		if err != nil {
			return fmt.Errorf("bulk upsert sync states: prepare delete: %w", err)
		}
		defer deleteStmt.Close()

		for _, d := range deletes {
			if _, err := deleteStmt.Exec(d.SyncJobID, d.Side, d.RelPath); err != nil {
				return fmt.Errorf("bulk upsert sync states: exec delete %s/%s/%s: %w", d.SyncJobID, d.Side, d.RelPath, err)
			}
		}
	}

	return nil
}

// UpdateSyncJobThreads updates the thread count for a sync job.
func UpdateSyncJobThreads(db *sql.DB, id string, threads int) error {
	query := `UPDATE sync_jobs SET threads = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, threads, id)
	return err
}

// GetActiveSyncTaskPaths returns the file_paths of all tasks currently in RUNNING state for the given sync job.
func GetActiveSyncTaskPaths(db *sql.DB, ctx context.Context, syncJobID string) ([]string, error) {
	query := `SELECT file_path, metadata FROM tasks WHERE sync_job_id = $1 AND status = 'RUNNING' ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, query, syncJobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		var meta json.RawMessage
		if err := rows.Scan(&path, &meta); err != nil {
			return nil, err
		}
		paths = append(paths, displayTaskName(path, meta))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return paths, nil
}

// AdminSyncView is a sync job row enriched with the owner's email for the admin view.
type AdminSyncView struct {
	SyncJob
	OwnerEmail string `json:"owner_email"`
}

// SyncListParams filters/paginates the admin-wide sync job listing.
type SyncListParams struct {
	Page  int
	Limit int
}

// ListAllSyncJobs returns every sync job across all users (read-only oversight).
func ListAllSyncJobs(database *sql.DB, p SyncListParams) ([]AdminSyncView, int, error) {
	return ListAllSyncJobsContext(context.Background(), database, p)
}

// ListAllSyncJobsContext lists sync jobs for administration while honoring caller cancellation.
func ListAllSyncJobsContext(ctx context.Context, database *sql.DB, p SyncListParams) ([]AdminSyncView, int, error) {
	var total int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_jobs`).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT s.id, s.user_id, s.source_url, s.source_username, s.source_provider,
		       s.target_url, s.target_username, s.target_provider, s.direction, s.conflict_strategy,
		       s.delete_propagation, s.interval_minutes, s.threads, s.status, s.target_dir,
		       s.selected_paths, s.last_run_at, s.last_run_status, s.error_message,
		       s.total_files, s.total_bytes, s.processed_files, s.processed_bytes, s.live_bytes, s.changed_files, s.deleted_files, s.failed_files,
		       s.created_at, s.updated_at, COALESCE(u.email, '')
		FROM sync_jobs s
		LEFT JOIN users u ON u.id = s.user_id
		ORDER BY s.created_at DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := database.QueryContext(ctx, query, p.Limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	views := []AdminSyncView{}
	for rows.Next() {
		var v AdminSyncView
		if err := rows.Scan(
			&v.ID, &v.UserID, &v.SourceURL, &v.SourceUsername, &v.SourceProvider,
			&v.TargetURL, &v.TargetUsername, &v.TargetProvider, &v.Direction, &v.ConflictStrategy,
			&v.DeletePropagation, &v.IntervalMinutes, &v.Threads, &v.Status, &v.TargetDir,
			&v.SelectedPaths, &v.LastRunAt, &v.LastRunStatus, &v.ErrorMessage,
			&v.TotalFiles, &v.TotalBytes, &v.ProcessedFiles, &v.ProcessedBytes, &v.LiveBytes, &v.ChangedFiles, &v.DeletedFiles, &v.FailedFiles,
			&v.CreatedAt, &v.UpdatedAt, &v.OwnerEmail,
		); err != nil {
			return nil, 0, err
		}
		views = append(views, v)
	}
	return views, total, nil
}

// GetUnverifiedCompletedSyncTasks fetches tasks for a sync job that completed but have checksum_verified = FALSE.
func GetUnverifiedCompletedSyncTasks(db *sql.DB, ctx context.Context, syncJobID string, generation int) ([]*Task, error) {
	query := `
		SELECT id, sync_job_id, pass_generation, resource_type, file_path, file_size, status,
		       attempts, error_message, next_retry_at, worker_hash, source_hash, target_hash,
		       checksum_verified, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM tasks
		WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'COMPLETED' AND checksum_verified = FALSE
	`
	rows, err := db.QueryContext(ctx, query, syncJobID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(
			&t.ID, &t.SyncJobID, &t.PassGeneration, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status,
			&t.Attempts, &t.ErrorMessage, &t.NextRetryAt, &t.WorkerHash, &t.SourceHash, &t.TargetHash,
			&t.ChecksumVerified, &t.Metadata, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, &t)
	}
	return tasks, rows.Err()
}

// UpdateSyncStateTargetHash updates the target_hash in sync_state for a specific file.
func UpdateSyncStateTargetHash(db *sql.DB, ctx context.Context, syncJobID, relPath, targetHash string) error {
	query := `
		UPDATE sync_state
		SET target_hash = $3
		WHERE sync_job_id = $1 AND side = 'target' AND rel_path = $2
	`
	_, err := db.ExecContext(ctx, query, syncJobID, relPath, targetHash)
	return err
}

var ErrSyncInvalidState = errors.New("sync job is in an active state and cannot be modified")

// DeleteSyncStateForJob removes all cached sync_state entries for a sync job (used on scope changes to prevent false deletions).
func DeleteSyncStateForJob(exec queryExecerContext, syncJobID string) error {
	_, err := exec.ExecContext(context.Background(), `DELETE FROM sync_state WHERE sync_job_id = $1`, syncJobID)
	return err
}

// UpdateSyncJobScope updates selected_paths, target_dir, conflict_strategy, direction, and delete_propagation for a sync job and clears its sync_state in a single transaction.
func UpdateSyncJobScope(db *sql.DB, syncJobID string, selectedPaths []string, targetDir string, conflictStrategy string, direction string, deletePropagation *bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM sync_jobs WHERE id = $1 FOR UPDATE`, syncJobID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	if status == "RUNNING" || status == "INDEXING" || status == "VERIFYING" {
		return ErrSyncInvalidState
	}

	pathsJSON, err := json.Marshal(selectedPaths)
	if err != nil {
		return fmt.Errorf("failed to marshal selected_paths: %w", err)
	}

	query := `UPDATE sync_jobs SET selected_paths = $1, target_dir = $2, updated_at = NOW()`
	args := []any{pathsJSON, targetDir}
	argIdx := 3

	if conflictStrategy != "" {
		query += fmt.Sprintf(`, conflict_strategy = $%d`, argIdx)
		args = append(args, conflictStrategy)
		argIdx++
	}
	if direction != "" {
		query += fmt.Sprintf(`, direction = $%d`, argIdx)
		args = append(args, direction)
		argIdx++
	}
	if deletePropagation != nil {
		query += fmt.Sprintf(`, delete_propagation = $%d`, argIdx)
		args = append(args, *deletePropagation)
		argIdx++
	}

	query += fmt.Sprintf(` WHERE id = $%d`, argIdx)
	args = append(args, syncJobID)

	res, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}

	if err := DeleteSyncStateForJob(tx, syncJobID); err != nil {
		return fmt.Errorf("failed to delete sync state: %w", err)
	}

	return tx.Commit()
}

// UpdateSyncJobInterval updates interval_minutes on sync_jobs and recalculates next_run_at on the linked schedule.
func UpdateSyncJobInterval(db *sql.DB, syncJobID string, intervalMinutes int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE sync_jobs SET interval_minutes = $1, updated_at = NOW() WHERE id = $2`,
		intervalMinutes, syncJobID,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}

	nextRun := time.Now().Add(time.Duration(intervalMinutes) * time.Minute)
	schedRes, err := tx.Exec(
		`UPDATE schedules SET next_run_at = $1, updated_at = NOW() WHERE task_type = 'sync' AND task_id = $2`,
		nextRun, syncJobID,
	)
	if err != nil {
		return err
	}
	schedRows, err := schedRes.RowsAffected()
	if err != nil {
		return err
	}
	if schedRows == 0 {
		log.Printf("UpdateSyncJobInterval: warning - no linked schedule found for sync job %s", syncJobID)
	}

	return tx.Commit()
}
