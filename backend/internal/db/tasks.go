package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"backend/internal/sanitize"
)

type Task struct {
	ID           string         `json:"id"`
	MigrationID  string         `json:"migration_id,omitempty"`
	SyncJobID    string         `json:"sync_job_id,omitempty"`
	ResourceType string         `json:"resource_type"` // files, calendars, contacts
	FilePath     string         `json:"file_path"`
	FileSize     int64          `json:"file_size"`
	Status       string         `json:"status"` // PENDING, RUNNING, COMPLETED, FAILED, SKIPPED, CANCELLED
	Attempts     int            `json:"attempts"`
	ErrorMessage sql.NullString `json:"error_message,omitempty"`
	NextRetryAt  sql.NullTime   `json:"next_retry_at,omitempty"`
	WorkerHash   sql.NullString `json:"worker_hash,omitempty"`
	// ClaimEpoch is assigned by DequeueSQL and fences a particular worker claim.
	ClaimEpoch       int64           `json:"claim_epoch"`
	PassGeneration   int             `json:"pass_generation"`
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

// ErrorListItem is a safe, display-oriented representation of an error that
// occurred while indexing or transferring a migration/sync resource.
type ErrorListItem struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	ResourceType string          `json:"resource_type"`
	Path         string          `json:"path"`
	Status       string          `json:"status"`
	Attempts     int             `json:"attempts"`
	ErrorMessage string          `json:"error_message"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

func CreateTask(db *sql.DB, t *Task) (string, error) {
	query := `
		INSERT INTO tasks (migration_id, resource_type, file_path, file_size, status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		t.MigrationID, t.ResourceType, t.FilePath, t.FileSize, t.Status, t.Metadata,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)

	if err != nil {
		return "", err
	}
	return t.ID, nil
}

func GetTask(db *sql.DB, id string) (*Task, error) {
	query := `
		SELECT id, COALESCE(migration_id::text, ''), COALESCE(sync_job_id::text, ''), resource_type, file_path, file_size, status,
		       attempts, error_message, next_retry_at, worker_hash, claim_epoch, pass_generation, source_hash, target_hash,
		       checksum_verified, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
		FROM tasks WHERE id = $1
	`
	var t Task
	err := db.QueryRow(query, id).Scan(
		&t.ID, &t.MigrationID, &t.SyncJobID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status,
		&t.Attempts, &t.ErrorMessage, &t.NextRetryAt, &t.WorkerHash, &t.ClaimEpoch, &t.PassGeneration, &t.SourceHash, &t.TargetHash,
		&t.ChecksumVerified, &t.Metadata, &t.CreatedAt, &t.UpdatedAt,
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

// UpdateClaimedTaskStatus changes a task only while this exact dequeue claim
// still owns it. sql.ErrNoRows means the task was recovered or reclaimed and
// the caller must stop work rather than committing stale results.
func UpdateClaimedTaskStatus(db *sql.DB, ctx context.Context, t *Task) error {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks
		SET status = $1, attempts = $2, error_message = $3, next_retry_at = $4, worker_hash = $5,
		    source_hash = $6, target_hash = $7, checksum_verified = $8, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9 AND status = 'RUNNING' AND claim_epoch = $10
	`, t.Status, t.Attempts, t.ErrorMessage, t.NextRetryAt, t.WorkerHash,
		t.SourceHash, t.TargetHash, t.ChecksumVerified, t.ID, t.ClaimEpoch)
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

// HeartbeatTaskClaim refreshes only the active fenced claim.
func HeartbeatTaskClaim(db *sql.DB, ctx context.Context, taskID string, claimEpoch int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE tasks SET updated_at = NOW() WHERE id = $1 AND status = 'RUNNING' AND claim_epoch = $2`, taskID, claimEpoch)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// HeartbeatSyncTaskClaim refreshes only a claim belonging to the active sync
// generation. A superseded worker observes false and cancels its stream.
func HeartbeatSyncTaskClaim(db *sql.DB, ctx context.Context, taskID string, claimEpoch int64, generation int) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE tasks AS t SET updated_at = NOW() WHERE t.id = $1 AND t.status = 'RUNNING' AND t.claim_epoch = $2 AND t.pass_generation = $3 AND EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.run_generation = $3)`, taskID, claimEpoch, generation)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// TransitionClaimedTask applies an early worker transition before a Task has
// been loaded, while still fencing it to the payload's dequeue claim.
func TransitionClaimedTask(db *sql.DB, ctx context.Context, taskID string, claimEpoch int64, status string) error {
	res, err := db.ExecContext(ctx, `UPDATE tasks SET status = $1, worker_hash = NULL, updated_at = NOW() WHERE id = $2 AND status = 'RUNNING' AND claim_epoch = $3`, status, taskID, claimEpoch)
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

func GetUnverifiedCompletedTasks(db *sql.DB, ctx context.Context, migrationID string) ([]*Task, error) {
	query := `
		SELECT id, COALESCE(migration_id::text, ''), COALESCE(sync_job_id::text, ''), resource_type, file_path, file_size, status,
		       attempts, error_message, next_retry_at, worker_hash, source_hash, target_hash,
		       checksum_verified, COALESCE(metadata, '{}'::jsonb), created_at, updated_at
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
			&t.ID, &t.MigrationID, &t.SyncJobID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status,
			&t.Attempts, &t.ErrorMessage, &t.NextRetryAt, &t.WorkerHash, &t.SourceHash, &t.TargetHash,
			&t.ChecksumVerified, &t.Metadata, &t.CreatedAt, &t.UpdatedAt,
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

// MarkSyncTaskChecksumVerifiedWhileVerifying rejects writes after an engine
// timeout has withdrawn the sync pass from VERIFYING.
func MarkSyncTaskChecksumVerifiedWhileVerifying(db *sql.DB, ctx context.Context, taskID, targetHash string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks AS t
		SET checksum_verified = TRUE,
		    target_hash = CASE WHEN $2 <> '' THEN $2 ELSE t.target_hash END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE t.id = $1
		  AND EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status = 'VERIFYING')
	`, taskID, targetHash)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected == 1, err
}

// MarkSyncTaskChecksumMismatchWhileVerifying records a mismatch only while
// the verifier owns VERIFYING. It does not schedule a retry during finalization.
func MarkSyncTaskChecksumMismatchWhileVerifying(db *sql.DB, ctx context.Context, task *Task) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE tasks AS t
		SET status = $1, attempts = $2, error_message = $3, next_retry_at = NULL,
		    worker_hash = $4, source_hash = $5, target_hash = $6,
		    checksum_verified = $7, updated_at = CURRENT_TIMESTAMP
		WHERE t.id = $8
		  AND EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status = 'VERIFYING')
	`, task.Status, task.Attempts, task.ErrorMessage, task.WorkerHash, task.SourceHash,
		task.TargetHash, task.ChecksumVerified, task.ID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected == 1, err
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
		UPDATE tasks AS t
		SET checksum_verified = TRUE, updated_at = CURRENT_TIMESTAMP
		WHERE t.sync_job_id = $1 AND t.checksum_verified = FALSE
		  AND EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status = 'VERIFYING')
	`
	_, err := db.ExecContext(ctx, query, syncJobID)
	return err
}

func UpdateTaskFilePath(db *sql.DB, taskID, newFilePath string) error {
	query := `UPDATE tasks SET file_path = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, newFilePath, taskID)
	return err
}

func UpdateClaimedTaskFilePath(db *sql.DB, ctx context.Context, taskID string, claimEpoch int64, newFilePath string) error {
	res, err := db.ExecContext(ctx, `UPDATE tasks SET file_path = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2 AND status = 'RUNNING' AND claim_epoch = $3`, newFilePath, taskID, claimEpoch)
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

// UpdateMigrationTaskAndProgress commits a fenced terminal task transition and
// its migration counters together, so a crash cannot leave them divergent.
func UpdateMigrationTaskAndProgress(db *sql.DB, ctx context.Context, t *Task, filesDelta int, bytesDelta int64, skippedDelta, failedDelta int, liveBytesDelta int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE tasks SET status = $1, attempts = $2, error_message = $3, next_retry_at = $4, worker_hash = $5,
			source_hash = $6, target_hash = $7, checksum_verified = $8, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9 AND migration_id = $10 AND status = 'RUNNING' AND claim_epoch = $11
	`, t.Status, t.Attempts, t.ErrorMessage, t.NextRetryAt, t.WorkerHash, t.SourceHash, t.TargetHash, t.ChecksumVerified, t.ID, t.MigrationID, t.ClaimEpoch)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	res, err = tx.ExecContext(ctx, `UPDATE migrations SET processed_files = processed_files + $1,
		processed_bytes = processed_bytes + $2, live_bytes = live_bytes + $2 + $3,
		skipped_files = skipped_files + $4, failed_files = failed_files + $5, updated_at = CURRENT_TIMESTAMP WHERE id = $6`,
		filesDelta, bytesDelta, liveBytesDelta, skippedDelta, failedDelta, t.MigrationID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
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
	if len(meta) == 0 {
		return filePath
	}

	var m struct {
		Name        string            `json:"name"`
		CustomProps map[string]string `json:"custom_props"`
	}
	if err := json.Unmarshal(meta, &m); err != nil {
		return filePath
	}
	if filename := m.CustomProps["immich_filename"]; filename != "" {
		return filename
	}
	if strings.HasPrefix(filePath, "/picker/") && m.Name != "" {
		return m.Name
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
		    notification_generation = notification_generation + 1,
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
		    error_message = NULL, email_sent = FALSE, notification_generation = notification_generation + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, migrationID); err != nil {
		return err
	}

	return tx.Commit()
}

func GetFailedTasksForReport(db *sql.DB, migrationID string) ([]Task, error) {
	query := `
		SELECT id, migration_id, resource_type, file_path, file_size, status, attempts, error_message, metadata, created_at, updated_at
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
		if err := rows.Scan(&t.ID, &t.MigrationID, &t.ResourceType, &t.FilePath, &t.FileSize, &t.Status, &t.Attempts, &t.ErrorMessage, &t.Metadata, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func RecordIndexingErrors(ctx context.Context, db *sql.DB, migrationID string, errors []IndexingErrorInput) error {
	if len(errors) == 0 {
		return nil
	}

	if ctx.Err() != nil {
		log.Printf("Warning: recording indexing errors (%d errors) after parent context expired for migration %s: %v", len(errors), migrationID, ctx.Err())
	}

	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()

	tx, err := db.BeginTx(dbCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const batchSize = 500
	const paramsPerRow = 4

	for start := 0; start < len(errors); start += batchSize {
		end := start + batchSize
		if end > len(errors) {
			end = len(errors)
		}
		batch := errors[start:end]

		args := make([]interface{}, 0, len(batch)*paramsPerRow)
		valuesClauses := make([]string, 0, len(batch))

		for i, e := range batch {
			base := i * paramsPerRow
			errMsg := indexingErrorMessage(e)
			resType := e.ResourceType
			if resType == "" {
				resType = "files"
			}
			args = append(args, migrationID, resType, e.Path, errMsg)
			valuesClauses = append(valuesClauses, fmt.Sprintf("($%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4))
		}

		query := "INSERT INTO indexing_errors (migration_id, resource_type, path, error_message) VALUES " +
			strings.Join(valuesClauses, ",")

		if _, err := tx.ExecContext(dbCtx, query, args...); err != nil {
			return fmt.Errorf("bulk record indexing errors [%d:%d]: %w", start, end, err)
		}
	}

	return tx.Commit()
}

func indexingErrorMessage(e IndexingErrorInput) string {
	if e.ErrorMessage != "" {
		return e.ErrorMessage
	}
	if e.Err != nil {
		return sanitize.SanitizeError(e.Err.Error())
	}
	return ""
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

// GetMigrationErrors returns final transfer failures and non-fatal indexing
// errors in one chronologically ordered, paginated list.
func GetMigrationErrors(db *sql.DB, migrationID string, limit, offset int) ([]ErrorListItem, int, error) {
	rows, err := db.Query(`
		WITH errors AS MATERIALIZED (
			SELECT id::text AS id, 'transfer' AS kind, resource_type, file_path AS path, status, attempts,
			       COALESCE(error_message, '') AS error_message, metadata, updated_at AS occurred_at
			FROM tasks
			WHERE migration_id = $1 AND status = 'FAILED'
			UNION ALL
			SELECT id::text AS id, 'indexing' AS kind, resource_type, path, 'INDEXING' AS status, 0 AS attempts,
			       error_message, '{}'::jsonb AS metadata, created_at AS occurred_at
			FROM indexing_errors
			WHERE migration_id = $1
		), counted AS (
			SELECT COUNT(*) AS total FROM errors
		)
		SELECT page.id, page.kind, page.resource_type, page.path, page.status, page.attempts,
		       page.error_message, page.metadata, page.occurred_at, counted.total
		FROM counted
		LEFT JOIN LATERAL (
			SELECT * FROM errors ORDER BY occurred_at DESC, path ASC, id ASC LIMIT $2 OFFSET $3
		) page ON TRUE
		ORDER BY page.occurred_at DESC NULLS LAST, page.path ASC NULLS LAST, page.id ASC NULLS LAST
	`, migrationID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]ErrorListItem, 0)
	total := 0
	for rows.Next() {
		var item ErrorListItem
		var id, kind, resourceType, path, status, errorMessage sql.NullString
		var attempts sql.NullInt32
		var occurredAt sql.NullTime
		var rawMeta []byte
		if err := rows.Scan(&id, &kind, &resourceType, &path, &status, &attempts, &errorMessage, &rawMeta, &occurredAt, &total); err != nil {
			return nil, 0, err
		}
		if !kind.Valid { // The count row remains when the requested page is empty.
			continue
		}
		item.ID, item.Kind, item.ResourceType, item.Path, item.Status = id.String, kind.String, resourceType.String, path.String, status.String
		item.Attempts, item.ErrorMessage, item.OccurredAt = int(attempts.Int32), errorMessage.String, occurredAt.Time
		if len(rawMeta) > 0 {
			item.Metadata = json.RawMessage(rawMeta)
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// GetSyncErrors returns final transfer failures for the current sync job.
func GetSyncErrors(db *sql.DB, syncJobID string, limit, offset int) ([]ErrorListItem, int, error) {
	rows, err := db.Query(`
		WITH errors AS MATERIALIZED (
			SELECT id::text AS id, 'transfer' AS kind, resource_type, file_path AS path, status, attempts,
			       COALESCE(error_message, '') AS error_message, updated_at AS occurred_at
			FROM tasks
			WHERE sync_job_id = $1
			  AND (status = 'FAILED' OR (status = 'SKIPPED' AND error_message IS NOT NULL))
		), counted AS (
			SELECT COUNT(*) AS total FROM errors
		)
		SELECT page.id, page.kind, page.resource_type, page.path, page.status, page.attempts,
		       page.error_message, page.occurred_at, counted.total
		FROM counted
		LEFT JOIN LATERAL (
			SELECT * FROM errors ORDER BY occurred_at DESC, path ASC, id ASC LIMIT $2 OFFSET $3
		) page ON TRUE
		ORDER BY page.occurred_at DESC NULLS LAST, page.path ASC NULLS LAST, page.id ASC NULLS LAST
	`, syncJobID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]ErrorListItem, 0)
	total := 0
	for rows.Next() {
		var item ErrorListItem
		var id, kind, resourceType, path, status, errorMessage sql.NullString
		var attempts sql.NullInt32
		var occurredAt sql.NullTime
		if err := rows.Scan(&id, &kind, &resourceType, &path, &status, &attempts, &errorMessage, &occurredAt, &total); err != nil {
			return nil, 0, err
		}
		if !kind.Valid {
			continue
		}
		item.ID, item.Kind, item.ResourceType, item.Path, item.Status = id.String, kind.String, resourceType.String, path.String, status.String
		item.Attempts, item.ErrorMessage, item.OccurredAt = int(attempts.Int32), errorMessage.String, occurredAt.Time
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func DeleteIndexingErrors(db *sql.DB, migrationID string) error {
	query := `DELETE FROM indexing_errors WHERE migration_id = $1`
	_, err := db.Exec(query, migrationID)
	return err
}
