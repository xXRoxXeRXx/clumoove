package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

type Task struct {
	ID           string          `json:"id"`
	MigrationID  string          `json:"migration_id,omitempty"`
	SyncJobID    string          `json:"sync_job_id,omitempty"`
	ResourceType string          `json:"resource_type"` // files, calendars, contacts
	FilePath     string          `json:"file_path"`
	FileSize     int64           `json:"file_size"`
	Status       string          `json:"status"` // PENDING, RUNNING, COMPLETED, FAILED, SKIPPED, CANCELLED
	Attempts     int             `json:"attempts"`
	ErrorMessage sql.NullString  `json:"error_message,omitempty"`
	NextRetryAt  sql.NullTime    `json:"next_retry_at,omitempty"`
	WorkerHash   sql.NullString  `json:"worker_hash,omitempty"`
	SourceHash       sql.NullString  `json:"source_hash,omitempty"`
	TargetHash       sql.NullString  `json:"target_hash,omitempty"`
	ChecksumVerified bool            `json:"checksum_verified"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type TaskInput struct {
	ResourceType string
	FilePath     string
	FileSize     int64
	Metadata     json.RawMessage
}

type IndexingError struct {
	ID           int64     `json:"id"`
	MigrationID  string    `json:"migration_id"`
	ResourceType string    `json:"resource_type"`
	Path         string    `json:"path"`
	ErrorMessage string    `json:"error_message"`
	CreatedAt    time.Time `json:"created_at"`
}

type IndexingErrorInput struct {
	ResourceType string
	Path         string
	ErrorMessage string
	Err          error
}

func CreateTask(db *sql.DB, t *Task) (string, error) {
	query := `
		INSERT INTO tasks (migration_id, resource_type, file_path, file_size, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		t.MigrationID, t.ResourceType, t.FilePath, t.FileSize, t.Status,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)

	if err != nil {
		return "", err
	}
	return t.ID, nil
}

func GetTask(db *sql.DB, id string) (*Task, error) {
	query := `
		SELECT id, migration_id, resource_type, file_path, file_size, status,
		       attempts, error_message, next_retry_at, worker_hash, source_hash, target_hash,
		       checksum_verified, created_at, updated_at
		FROM tasks WHERE id = $1
	`
	var t Task
	err := db.QueryRow(query, id).Scan(
		&t.ID, &t.MigrationID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status,
		&t.Attempts, &t.ErrorMessage, &t.NextRetryAt, &t.WorkerHash, &t.SourceHash, &t.TargetHash,
		&t.ChecksumVerified, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func UpdateTaskStatus(db *sql.DB, t *Task) error {
	query := `
		UPDATE tasks
		SET status = $1, attempts = $2, error_message = $3, next_retry_at = $4, worker_hash = $5,
		    source_hash = $6, target_hash = $7, checksum_verified = $8, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9
	`
	_, err := db.Exec(query, t.Status, t.Attempts, t.ErrorMessage, t.NextRetryAt, t.WorkerHash,
		t.SourceHash, t.TargetHash, t.ChecksumVerified, t.ID)
	return err
}

func GetUnverifiedCompletedTasks(db *sql.DB, ctx context.Context, migrationID string) ([]*Task, error) {
	query := `
		SELECT id, migration_id, resource_type, file_path, file_size, status,
		       attempts, error_message, next_retry_at, worker_hash, source_hash, target_hash,
		       checksum_verified, created_at, updated_at
		FROM tasks
		WHERE migration_id = $1 AND status = 'COMPLETED' AND checksum_verified = FALSE
	`
	rows, err := db.QueryContext(ctx, query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(
			&t.ID, &t.MigrationID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status,
			&t.Attempts, &t.ErrorMessage, &t.NextRetryAt, &t.WorkerHash, &t.SourceHash, &t.TargetHash,
			&t.ChecksumVerified, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, &t)
	}
	return tasks, rows.Err()
}

func MarkTaskChecksumVerified(db *sql.DB, ctx context.Context, taskID, targetHash string) error {
	query := `
		UPDATE tasks
		SET checksum_verified = TRUE,
		    target_hash = CASE WHEN $2 <> '' THEN $2 ELSE target_hash END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := db.ExecContext(ctx, query, taskID, targetHash)
	return err
}

func MarkAllMigrationTasksVerified(db *sql.DB, ctx context.Context, migrationID string) error {
	query := `
		UPDATE tasks
		SET checksum_verified = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND checksum_verified = FALSE
	`
	_, err := db.ExecContext(ctx, query, migrationID)
	return err
}

func MarkAllSyncTasksVerified(db *sql.DB, ctx context.Context, syncJobID string) error {
	query := `
		UPDATE sync_tasks
		SET checksum_verified = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE sync_job_id = $1 AND checksum_verified = FALSE
	`
	_, err := db.ExecContext(ctx, query, syncJobID)
	return err
}

func UpdateTaskFilePath(db *sql.DB, taskID, newFilePath string) error {
	query := `UPDATE tasks SET file_path = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, newFilePath, taskID)
	return err
}

func GetActiveTaskPath(db *sql.DB, ctx context.Context, migrationID string) (string, error) {
	paths, err := GetActiveTaskPaths(db, ctx, migrationID)
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

func GetActiveTaskPaths(db *sql.DB, ctx context.Context, migrationID string) ([]string, error) {
	query := `
		SELECT file_path, metadata
		FROM tasks
		WHERE migration_id = $1 AND status = 'RUNNING'
		ORDER BY updated_at DESC
	`
	rows, err := db.QueryContext(ctx, query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var fp string
		var meta json.RawMessage
		if err := rows.Scan(&fp, &meta); err != nil {
			return nil, err
		}
		paths = append(paths, displayTaskName(fp, meta))
	}
	return paths, rows.Err()
}

func displayTaskName(filePath string, meta json.RawMessage) string {
	if strings.HasPrefix(filePath, "/picker/") && len(meta) > 0 {
		var m struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(meta, &m); err == nil && m.Name != "" {
			return m.Name
		}
	}
	return filePath
}

func CancelRemainingPendingTasks(dbsql *sql.DB, migrationID string) (int, error) {
	query := `
		UPDATE tasks
		SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status IN ('PENDING', 'RUNNING')
	`
	res, err := dbsql.Exec(query, migrationID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func CancelPendingTasks(db *sql.DB, migrationID string) error {
	query := `
		UPDATE tasks
		SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status = 'PENDING'
	`
	_, err := db.Exec(query, migrationID)
	return err
}

func ResetFailedTasksForRetry(db *sql.DB, ctx context.Context, migrationID string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var count int
	var bytesSum int64
	err = tx.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(file_size), 0)
		FROM tasks
		WHERE migration_id = $1 AND status = 'FAILED'
	`, migrationID).Scan(&count, &bytesSum)
	if err != nil {
		return 0, err
	}

	if count == 0 {
		return 0, nil
	}

	_, err = tx.Exec(`
		UPDATE tasks
		SET status = 'PENDING', attempts = 0, next_retry_at = NULL, worker_hash = NULL, error_message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status = 'FAILED'
	`, migrationID)
	if err != nil {
		return 0, err
	}

	res, err := tx.Exec(`
		UPDATE migrations
		SET failed_files = failed_files - $1,
		    processed_files = processed_files - $1,
		    processed_bytes = processed_bytes - $2,
		    live_bytes = processed_bytes,
		    status = 'RUNNING',
		    error_message = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $3 AND status IN ('COMPLETED', 'FAILED', 'COMPLETED_WITH_ERRORS')
	`, count, bytesSum, migrationID)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if rowsAffected == 0 {
		return 0, nil
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return count, nil
}

func ResetMigrationForReindex(db *sql.DB, ctx context.Context, migrationID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM tasks WHERE migration_id = $1`, migrationID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM indexing_errors WHERE migration_id = $1`, migrationID); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		UPDATE migrations
		SET total_files = 0, total_bytes = 0, processed_files = 0, processed_bytes = 0,
		    live_bytes = 0, skipped_files = 0, failed_files = 0, status = 'INDEXING',
		    error_message = NULL, email_sent = FALSE, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, migrationID); err != nil {
		return err
	}

	return tx.Commit()
}

func GetFailedTasksForReport(db *sql.DB, migrationID string) ([]Task, error) {
	query := `
		SELECT id, migration_id, resource_type, file_path, file_size, status, attempts, error_message, created_at, updated_at
		FROM tasks
		WHERE migration_id = $1 AND status = 'FAILED'
		ORDER BY file_path ASC
	`
	rows, err := db.Query(query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.MigrationID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status, &t.Attempts, &t.ErrorMessage, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func RecordIndexingErrors(db *sql.DB, ctx context.Context, migrationID string, errors []IndexingErrorInput) error {
	if len(errors) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO indexing_errors (migration_id, resource_type, path, error_message)
		VALUES ($1, $2, $3, $4)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range errors {
		errMsg := ""
		if e.Err != nil {
			errMsg = e.Err.Error()
		}
		resType := e.ResourceType
		if resType == "" {
			resType = "files"
		}
		if _, err := stmt.Exec(migrationID, resType, e.Path, errMsg); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func GetIndexingErrorsForReport(db *sql.DB, migrationID string) ([]IndexingError, error) {
	query := `
		SELECT id, migration_id, resource_type, path, error_message, created_at
		FROM indexing_errors
		WHERE migration_id = $1
		ORDER BY path ASC
	`
	rows, err := db.Query(query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var errs []IndexingError
	for rows.Next() {
		var ie IndexingError
		if err := rows.Scan(&ie.ID, &ie.MigrationID, &ie.ResourceType, &ie.Path, &ie.ErrorMessage, &ie.CreatedAt); err != nil {
			return nil, err
		}
		errs = append(errs, ie)
	}
	return errs, rows.Err()
}

func DeleteIndexingErrors(db *sql.DB, migrationID string) error {
	query := `DELETE FROM indexing_errors WHERE migration_id = $1`
	_, err := db.Exec(query, migrationID)
	return err
}
