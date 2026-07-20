package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SyncJob represents a fortlaufende Synchronisation job
type SyncJob struct {
	ID                          string         `json:"id"`
	UserID                      string         `json:"user_id"`
	SourceURL                   string         `json:"source_url"`
	SourceUsername              string         `json:"source_username"`
	SourcePasswordEncrypted     string         `json:"-"`
	SourceRefreshTokenEncrypted sql.NullString `json:"-"`
	SourceTokenExpiresAt        sql.NullTime   `json:"source_token_expires_at,omitempty"`
	TargetURL                   string         `json:"target_url"`
	TargetUsername              string         `json:"target_username"`
	TargetPasswordEncrypted     string         `json:"-"`
	TargetRefreshTokenEncrypted sql.NullString `json:"-"`
	TargetTokenExpiresAt        sql.NullTime   `json:"target_token_expires_at,omitempty"`
	SourceProvider              string         `json:"source_provider"`
	TargetProvider              string         `json:"target_provider"`
	Direction                   string         `json:"direction"`
	ConflictStrategy            string         `json:"conflict_strategy"`
	DeletePropagation          bool           `json:"delete_propagation"`
	IntervalMinutes            int            `json:"interval_minutes"`
	Threads                    int            `json:"threads"`
	Status                     string         `json:"status"` // IDLE, INDEXING, RUNNING, PAUSED, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED
	TargetDir                  string         `json:"target_dir"`
	SelectedPaths               StringArray    `json:"selected_paths,omitempty"`
	LastRunAt                  sql.NullTime   `json:"last_run_at,omitempty"`
	LastRunStatus              sql.NullString `json:"last_run_status,omitempty"`
	ErrorMessage               sql.NullString `json:"error_message,omitempty"`
	TotalFiles                 int            `json:"total_files"`
	ProcessedFiles             int            `json:"processed_files"`
	ChangedFiles               int            `json:"changed_files"`
	DeletedFiles               int            `json:"deleted_files"`
	FailedFiles                int            `json:"failed_files"`
	CreatedAt                  time.Time      `json:"created_at"`
	UpdatedAt                  time.Time      `json:"updated_at"`
}

// MarshalJSON serializes the sync job with nullable columns (sql.NullString,
// sql.NullTime) resolved to plain JSON strings/null so frontend consumers don't
// receive raw driver structs like {"String":"...","Valid":true}.
func (s SyncJob) MarshalJSON() ([]byte, error) {
	type alias SyncJob
	aux := struct {
		*alias
		LastRunStatus string  `json:"last_run_status,omitempty"`
		ErrorMessage  string  `json:"error_message,omitempty"`
		LastRunAt     *string `json:"last_run_at,omitempty"`
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
	return json.Marshal(aux)
}

// SyncState represents the last known state of a file during a sync
type SyncState struct {
	ID         string       `json:"id"`
	SyncJobID  string       `json:"sync_job_id"`
	Side       string       `json:"side"` // source, target
	RelPath    string       `json:"rel_path"`
	Size       int64        `json:"size"`
	Mtime      sql.NullTime `json:"mtime,omitempty"`
	SourceHash string       `json:"source_hash,omitempty"`
	TargetHash string       `json:"target_hash,omitempty"`
}

// CreateSyncJob inserts a new sync job
func CreateSyncJob(db *sql.DB, s *SyncJob) (string, error) {
	query := `
		INSERT INTO sync_jobs (
			user_id, source_url, source_username, source_password_encrypted,
			source_refresh_token_encrypted, source_token_expires_at,
			target_url, target_username, target_password_encrypted,
			target_refresh_token_encrypted, target_token_expires_at,
			source_provider, target_provider, direction, conflict_strategy,
			delete_propagation, interval_minutes, threads, status, target_dir,
			selected_paths
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		s.UserID, s.SourceURL, s.SourceUsername, s.SourcePasswordEncrypted,
		s.SourceRefreshTokenEncrypted, s.SourceTokenExpiresAt,
		s.TargetURL, s.TargetUsername, s.TargetPasswordEncrypted,
		s.TargetRefreshTokenEncrypted, s.TargetTokenExpiresAt,
		s.SourceProvider, s.TargetProvider, s.Direction, s.ConflictStrategy,
		s.DeletePropagation, s.IntervalMinutes, s.Threads, s.Status, s.TargetDir,
		s.SelectedPaths,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)

	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// GetSyncJob retrieves a sync job by ID
func GetSyncJob(db *sql.DB, id string) (*SyncJob, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       source_refresh_token_encrypted, source_token_expires_at,
		       target_url, target_username, target_password_encrypted,
		       target_refresh_token_encrypted, target_token_expires_at,
		       source_provider, target_provider, direction, conflict_strategy,
		       delete_propagation, interval_minutes, threads, status, target_dir,
		       selected_paths, last_run_at, last_run_status, error_message,
		       total_files, processed_files, changed_files, deleted_files, failed_files,
		       created_at, updated_at
		FROM sync_jobs WHERE id = $1
	`
	var s SyncJob
	err := db.QueryRow(query, id).Scan(
		&s.ID, &s.UserID, &s.SourceURL, &s.SourceUsername, &s.SourcePasswordEncrypted,
		&s.SourceRefreshTokenEncrypted, &s.SourceTokenExpiresAt,
		&s.TargetURL, &s.TargetUsername, &s.TargetPasswordEncrypted,
		&s.TargetRefreshTokenEncrypted, &s.TargetTokenExpiresAt,
		&s.SourceProvider, &s.TargetProvider, &s.Direction, &s.ConflictStrategy,
		&s.DeletePropagation, &s.IntervalMinutes, &s.Threads, &s.Status, &s.TargetDir,
		&s.SelectedPaths, &s.LastRunAt, &s.LastRunStatus, &s.ErrorMessage,
		&s.TotalFiles, &s.ProcessedFiles, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSyncJobOwnerID returns the owning user_id for a sync job.
func GetSyncJobOwnerID(database queryExecer, syncJobID string) (string, error) {
	var owner sql.NullString
	err := database.QueryRow(`SELECT user_id FROM sync_jobs WHERE id = $1`, syncJobID).Scan(&owner)
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
	query := `
		SELECT id, user_id, source_url, source_username, source_provider,
		       target_url, target_username, target_provider, direction, conflict_strategy,
		       delete_propagation, interval_minutes, threads, status, target_dir,
		       selected_paths, last_run_at, last_run_status, error_message,
		       total_files, processed_files, changed_files, deleted_files, failed_files,
		       created_at, updated_at
		FROM sync_jobs
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, userID)
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
			&s.DeletePropagation, &s.IntervalMinutes, &s.Threads, &s.Status, &s.TargetDir,
			&s.SelectedPaths, &s.LastRunAt, &s.LastRunStatus, &s.ErrorMessage,
			&s.TotalFiles, &s.ProcessedFiles, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
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
		SET status = $1, error_message = CASE WHEN $2::text IS NOT NULL THEN $2 ELSE error_message END, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := db.Exec(query, status, errVal, id)
	return err
}

// UpdateSyncJobTotals updates the total_files count calculated at index time
func UpdateSyncJobTotals(db *sql.DB, id string, totalFiles int) error {
	query := `
		UPDATE sync_jobs
		SET total_files = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, totalFiles, id)
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
		       total_files, processed_files, changed_files, deleted_files, failed_files,
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
			&s.TotalFiles, &s.ProcessedFiles, &s.ChangedFiles, &s.DeletedFiles, &s.FailedFiles,
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
	query := `SELECT EXISTS(SELECT 1 FROM sync_jobs WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRow(query, syncJobID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// DeleteSyncJobCascade deletes a sync job and all related tasks / state
func DeleteSyncJobCascade(db *sql.DB, syncJobID string) error {
	query := `DELETE FROM sync_jobs WHERE id = $1`
	_, err := db.Exec(query, syncJobID)
	return err
}

// CancelRemainingPendingSyncTasks marks all pending sync tasks as CANCELLED
func CancelRemainingPendingSyncTasks(dbsql *sql.DB, syncJobID string) (int, error) {
	query := `
		UPDATE tasks
		SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP
		WHERE sync_job_id = $1 AND status = 'PENDING'
	`
	res, err := dbsql.Exec(query, syncJobID)
	if err != nil {
		return 0, err
	}
	rows, err := res.RowsAffected()
	return int(rows), err
}

// ReconcileSyncJobProgress repairs progress counter drift for a sync job
func ReconcileSyncJobProgress(dbsql *sql.DB, syncJobID string) error {
	query := `
		SELECT 
			COUNT(*) FILTER (WHERE status = 'COMPLETED') as completed,
			COUNT(*) FILTER (WHERE status = 'SKIPPED') as skipped,
			COUNT(*) FILTER (WHERE status = 'FAILED') as failed,
			COUNT(*) FILTER (WHERE status = 'CANCELLED') as cancelled,
			COUNT(*) FILTER (WHERE status IN ('PENDING', 'RUNNING')) as open
		FROM tasks
		WHERE sync_job_id = $1
	`
	var completed, skipped, failed, cancelled, open int
	err := dbsql.QueryRow(query, syncJobID).Scan(&completed, &skipped, &failed, &cancelled, &open)
	if err != nil {
		return err
	}

	total := completed + skipped + failed + cancelled + open
	if total == 0 {
		return nil
	}

	updateQuery := `
		UPDATE sync_jobs
		SET processed_files = $1,
		    failed_files = $2,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err = dbsql.Exec(updateQuery, completed + skipped, failed + cancelled, syncJobID)
	return err
}

// GetFailedSyncTasksForReport retrieves failed sync tasks for a CSV report
func GetFailedSyncTasksForReport(db *sql.DB, syncJobID string) ([]Task, error) {
	query := `
		SELECT id, sync_job_id, file_path, file_size, source_hash, worker_hash, target_hash,
		       status, error_message, attempts, next_retry_at, created_at, updated_at, resource_type, metadata
		FROM tasks
		WHERE sync_job_id = $1 AND status = 'FAILED'
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, syncJobID)
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

// IncrementSyncJobProgress updates processed/changed/deleted/failed counters
func IncrementSyncJobProgress(db *sql.DB, ctx context.Context, id string, filesDelta, changedDelta, deletedDelta, failedDelta int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		UPDATE sync_jobs
		SET processed_files = processed_files + $1,
		    changed_files = changed_files + $2,
		    deleted_files = deleted_files + $3,
		    failed_files = failed_files + $4,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $5
	`
	_, err = tx.ExecContext(ctx, query, filesDelta, changedDelta, deletedDelta, failedDelta, id)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// UpsertSyncState inserts or updates a sync state row
func UpsertSyncState(db *sql.DB, s *SyncState) error {
	query := `
		INSERT INTO sync_state (sync_job_id, side, rel_path, size, mtime, source_hash, target_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (sync_job_id, side, rel_path) DO UPDATE SET
			size = EXCLUDED.size,
			mtime = EXCLUDED.mtime,
			source_hash = EXCLUDED.source_hash,
			target_hash = EXCLUDED.target_hash
	`
	_, err := db.Exec(query, s.SyncJobID, s.Side, s.RelPath, s.Size, s.Mtime, s.SourceHash, s.TargetHash)
	return err
}

// GetSyncState retrieves a single sync state
func GetSyncState(db *sql.DB, syncJobID, side, relPath string) (*SyncState, error) {
	query := `
		SELECT id, sync_job_id, side, rel_path, size, mtime, source_hash, target_hash
		FROM sync_state
		WHERE sync_job_id = $1 AND side = $2 AND rel_path = $3
	`
	var s SyncState
	err := db.QueryRow(query, syncJobID, side, relPath).Scan(
		&s.ID, &s.SyncJobID, &s.Side, &s.RelPath, &s.Size, &s.Mtime, &s.SourceHash, &s.TargetHash,
	)
	if err != nil {
		return nil, err
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
		SELECT id, sync_job_id, side, rel_path, size, mtime, source_hash, target_hash
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
		err := rows.Scan(&s.ID, &s.SyncJobID, &s.Side, &s.RelPath, &s.Size, &s.Mtime, &s.SourceHash, &s.TargetHash)
		if err != nil {
			return nil, err
		}
		states = append(states, s)
	}
	return states, rows.Err()
}

// BulkCreateSyncTasks inserts sync tasks in batches of batchSize rows per statement.
// This is dramatically faster than N individual INSERTs for large sync passes with
// many files (e.g. 1000 files → 2 DB round-trips instead of 1000).
func BulkCreateSyncTasks(db *sql.DB, tasks []*Task) error {
	if len(tasks) == 0 {
		return nil
	}

	const batchSize = 500

	tx, err := db.Begin()
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

		// Build a multi-row INSERT: VALUES ($1,$2,...), ($9,$10,...), ...
		// Each row has 8 params: migration_id, sync_job_id, file_path, file_size,
		// source_hash, status, resource_type, metadata
		const paramsPerRow = 8
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
				migID, syncID, t.FilePath, t.FileSize, t.SourceHash, t.Status, t.ResourceType, t.Metadata,
			)
			valuesClauses = append(valuesClauses,
				fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
					base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8),
			)
		}

		query := "INSERT INTO tasks (migration_id, sync_job_id, file_path, file_size, source_hash, status, resource_type, metadata) VALUES " +
			strings.Join(valuesClauses, ",")

		if _, err := tx.Exec(query, args...); err != nil {
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
func BulkUpsertSyncStates(db *sql.DB, upserts []*SyncState, deletes []struct{ SyncJobID, Side, RelPath string }) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("bulk upsert sync states: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Prepare the upsert statement once and reuse it for all rows.
	upsertStmt, err := tx.Prepare(`
		INSERT INTO sync_state (sync_job_id, side, rel_path, size, mtime, source_hash, target_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (sync_job_id, side, rel_path) DO UPDATE SET
			size        = EXCLUDED.size,
			mtime       = EXCLUDED.mtime,
			source_hash = EXCLUDED.source_hash,
			target_hash = EXCLUDED.target_hash
	`)
	if err != nil {
		return fmt.Errorf("bulk upsert sync states: prepare upsert: %w", err)
	}
	defer upsertStmt.Close()

	for _, s := range upserts {
		if _, err := upsertStmt.Exec(s.SyncJobID, s.Side, s.RelPath, s.Size, s.Mtime, s.SourceHash, s.TargetHash); err != nil {
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

	return tx.Commit()
}

// UpdateSyncJobThreads updates the thread count for a sync job.
func UpdateSyncJobThreads(db *sql.DB, id string, threads int) error {
	query := `UPDATE sync_jobs SET threads = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, threads, id)
	return err
}
