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

var (
	ErrMigrationNotFailed         = errors.New("migration is not in a failed state")
	ErrMigrationIndexingClaimLost = errors.New("migration indexing claim lost")
)

type ResourceStats struct {
	Total     int `json:"total"`
	Processed int `json:"processed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

type MigrationResourceStats struct {
	Files     ResourceStats `json:"files"`
	Calendars ResourceStats `json:"calendars"`
	Contacts  ResourceStats `json:"contacts"`
}

type Migration struct {
	ID                          string                  `json:"id"`
	UserID                      sql.NullString          `json:"user_id,omitempty"`
	SourceURL                   string                  `json:"source_url"`
	SourceUsername              string                  `json:"source_username"`
	SourcePasswordEncrypted     string                  `json:"-"`
	SourceProvider              string                  `json:"source_provider"`
	SourceRefreshTokenEncrypted sql.NullString          `json:"-"`
	SourceTokenExpiresAt        sql.NullTime            `json:"-"`
	TargetURL                   string                  `json:"target_url"`
	TargetUsername              string                  `json:"target_username"`
	TargetPasswordEncrypted     string                  `json:"-"`
	TargetProvider              string                  `json:"target_provider"`
	TargetRefreshTokenEncrypted sql.NullString          `json:"-"`
	TargetTokenExpiresAt        sql.NullTime            `json:"-"`
	TargetDir                   string                  `json:"target_dir"`
	Status                      string                  `json:"status"` // PENDING, INDEXING, RUNNING, PAUSED, COMPLETED, FAILED, CANCELLED
	ConflictStrategy            string                  `json:"conflict_strategy"`
	TotalFiles                  int                     `json:"total_files"`
	TotalBytes                  int64                   `json:"total_bytes"`
	ProcessedFiles              int                     `json:"processed_files"`
	ProcessedBytes              int64                   `json:"processed_bytes"`
	LiveBytes                   int64                   `json:"live_bytes"`
	SkippedFiles                int                     `json:"skipped_files"`
	FailedFiles                 int                     `json:"failed_files"`
	ErrorMessage                sql.NullString          `json:"error_message,omitempty"`
	CreatedAt                   time.Time               `json:"created_at"`
	UpdatedAt                   time.Time               `json:"updated_at"`
	Threads                     int                     `json:"threads"`
	BandwidthLimitMbps          int                     `json:"bandwidth_limit_mbps"`
	PickerSessionID             string                  `json:"picker_session_id,omitempty"`
	SelectedPaths               StringArray             `json:"selected_paths,omitempty"`
	SelectedCalendars           StringArray             `json:"selected_calendars,omitempty"`
	SelectedContacts            StringArray             `json:"selected_contacts,omitempty"`
	ResourceStats               *MigrationResourceStats `json:"resource_stats,omitempty"`
}

// ValidConflictStrategy reports whether strategy is safe for a target write.
// Keep this allowlist shared by request validation and the transfer processor.
func ValidConflictStrategy(strategy string) bool {
	switch strategy {
	case "SKIP", "OVERWRITE", "RENAME":
		return true
	default:
		return false
	}
}

func (m Migration) MarshalJSON() ([]byte, error) {
	type Alias Migration
	return json.Marshal(&struct {
		Alias
		UserID       *string `json:"user_id"`
		ErrorMessage *string `json:"error_message"`
	}{
		Alias:        Alias(m),
		UserID:       nullStringPtr(m.UserID),
		ErrorMessage: nullStringPtr(m.ErrorMessage),
	})
}

func nullStringPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

type AdminMigrationView struct {
	Migration
	OwnerEmail string `json:"owner_email"`
}

type MigrationListParams struct {
	Page  int
	Limit int
}

type OAuthTokenUpdate struct {
	MigrationID           string
	Role                  string
	AccessTokenEncrypted  string
	RefreshTokenEncrypted string
	ExpiresAt             time.Time
}

type ExpiringOAuthMigration struct {
	MigrationID           string
	Role                  string
	Provider              string
	RefreshTokenEncrypted string
}

type PendingEmailNotification struct {
	MigrationID    string
	UserID         string
	Status         string
	TotalFiles     int
	ProcessedFiles int
	FailedFiles    int
	SkippedFiles   int
	TotalBytes     int64
	ProcessedBytes int64
	ErrorMessage   sql.NullString
}

const createMigrationQuery = `
		INSERT INTO migrations (
			user_id, source_url, source_username, source_password_encrypted, source_provider,
			source_refresh_token_encrypted, source_token_expires_at,
			target_url, target_username, target_password_encrypted, target_provider,
			target_refresh_token_encrypted, target_token_expires_at,
			status, conflict_strategy, target_dir, threads, bandwidth_limit_mbps,
			picker_session_id, selected_paths, selected_calendars, selected_contacts
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		RETURNING id, created_at, updated_at
	`

func CreateMigration(db *sql.DB, m *Migration) (string, error) {
	if err := insertMigration(db, m); err != nil {
		return "", err
	}
	return m.ID, nil
}

func insertMigration(database queryExecer, m *Migration) error {
	return database.QueryRow(
		createMigrationQuery,
		m.UserID, m.SourceURL, m.SourceUsername, m.SourcePasswordEncrypted, m.SourceProvider,
		m.SourceRefreshTokenEncrypted, m.SourceTokenExpiresAt,
		m.TargetURL, m.TargetUsername, m.TargetPasswordEncrypted, m.TargetProvider,
		m.TargetRefreshTokenEncrypted, m.TargetTokenExpiresAt,
		m.Status, m.ConflictStrategy, m.TargetDir, m.Threads, m.BandwidthLimitMbps,
		m.PickerSessionID, m.SelectedPaths, m.SelectedCalendars, m.SelectedContacts,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
}

// CreateMigrationAndSchedule creates a scheduled migration and its one-shot
// schedule atomically, preventing scheduled migrations without a trigger.
func CreateMigrationAndSchedule(db *sql.DB, migration *Migration, schedule *Schedule) (string, error) {
	resetMigrationAndSchedule(migration, schedule)
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			resetMigrationAndSchedule(migration, schedule)
		}
	}()

	if err := insertMigration(tx, migration); err != nil {
		return "", err
	}
	schedule.TaskID = migration.ID
	if err := insertSchedule(tx, schedule); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	committed = true
	return migration.ID, nil
}

func resetMigrationAndSchedule(migration *Migration, schedule *Schedule) {
	migration.ID = ""
	migration.CreatedAt = time.Time{}
	migration.UpdatedAt = time.Time{}
	schedule.ID = ""
	schedule.TaskID = ""
	schedule.CreatedAt = time.Time{}
	schedule.UpdatedAt = time.Time{}
}

func GetMigration(db *sql.DB, id string) (*Migration, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted, source_provider,
		       source_refresh_token_encrypted, source_token_expires_at,
		       target_url, target_username, target_password_encrypted, target_provider,
		       target_refresh_token_encrypted, target_token_expires_at,
		       status, conflict_strategy, total_files, total_bytes, processed_files,
		       processed_bytes, live_bytes, skipped_files, failed_files, error_message,
		       created_at, updated_at, target_dir, threads, bandwidth_limit_mbps,
		       picker_session_id, selected_paths, selected_calendars, selected_contacts
		FROM migrations WHERE id = $1
	`
	var m Migration
	err := db.QueryRow(query, id).Scan(
		&m.ID, &m.UserID, &m.SourceURL, &m.SourceUsername, &m.SourcePasswordEncrypted, &m.SourceProvider,
		&m.SourceRefreshTokenEncrypted, &m.SourceTokenExpiresAt,
		&m.TargetURL, &m.TargetUsername, &m.TargetPasswordEncrypted, &m.TargetProvider,
		&m.TargetRefreshTokenEncrypted, &m.TargetTokenExpiresAt,
		&m.Status, &m.ConflictStrategy, &m.TotalFiles, &m.TotalBytes, &m.ProcessedFiles,
		&m.ProcessedBytes, &m.LiveBytes, &m.SkippedFiles, &m.FailedFiles, &m.ErrorMessage,
		&m.CreatedAt, &m.UpdatedAt, &m.TargetDir, &m.Threads, &m.BandwidthLimitMbps,
		&m.PickerSessionID, &m.SelectedPaths, &m.SelectedCalendars, &m.SelectedContacts,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func GetMigrationsForUser(db *sql.DB, userID string) ([]Migration, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_provider,
		       target_url, target_username, target_provider, status,
		       conflict_strategy, total_files, total_bytes, processed_files,
		       processed_bytes, live_bytes, skipped_files, failed_files, error_message,
		       created_at, updated_at, target_dir, threads
		FROM migrations
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Migration
	for rows.Next() {
		var m Migration
		err := rows.Scan(
			&m.ID, &m.UserID, &m.SourceURL, &m.SourceUsername, &m.SourceProvider,
			&m.TargetURL, &m.TargetUsername, &m.TargetProvider, &m.Status,
			&m.ConflictStrategy, &m.TotalFiles, &m.TotalBytes, &m.ProcessedFiles,
			&m.ProcessedBytes, &m.LiveBytes, &m.SkippedFiles, &m.FailedFiles, &m.ErrorMessage,
			&m.CreatedAt, &m.UpdatedAt, &m.TargetDir, &m.Threads,
		)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func ListAllMigrations(database *sql.DB, p MigrationListParams) ([]AdminMigrationView, int, error) {
	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM migrations`).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	query := `
		SELECT m.id, m.user_id, m.source_url, m.source_username, m.source_provider, m.target_url,
		       m.target_username, m.target_provider, m.target_dir, m.status, m.conflict_strategy,
		       m.total_files, m.total_bytes, m.processed_files, m.processed_bytes, m.skipped_files,
		       m.failed_files, m.error_message, m.created_at, m.updated_at, m.threads,
		       m.bandwidth_limit_mbps, COALESCE(u.email, ''),
		       m.selected_paths, m.selected_calendars, m.selected_contacts
		FROM migrations m
		LEFT JOIN users u ON u.id = m.user_id
		ORDER BY m.created_at DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := database.Query(query, p.Limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	views := []AdminMigrationView{}
	for rows.Next() {
		var v AdminMigrationView
		var uid sql.NullString
		var errMsg sql.NullString
		if err := rows.Scan(
			&v.ID, &uid, &v.SourceURL, &v.SourceUsername, &v.SourceProvider, &v.TargetURL,
			&v.TargetUsername, &v.TargetProvider, &v.TargetDir, &v.Status, &v.ConflictStrategy,
			&v.TotalFiles, &v.TotalBytes, &v.ProcessedFiles, &v.ProcessedBytes, &v.SkippedFiles,
			&v.FailedFiles, &errMsg, &v.CreatedAt, &v.UpdatedAt, &v.Threads,
			&v.BandwidthLimitMbps, &v.OwnerEmail,
			&v.SelectedPaths, &v.SelectedCalendars, &v.SelectedContacts,
		); err != nil {
			return nil, 0, err
		}
		v.UserID = uid
		v.ErrorMessage = errMsg
		views = append(views, v)
	}
	return views, total, nil
}

func UpdateMigrationStatus(db *sql.DB, id string, status string, errMsg *string) error {
	query := `
		UPDATE migrations
		SET notification_generation = CASE WHEN status IN ('COMPLETED','COMPLETED_WITH_ERRORS','FAILED') AND $1 NOT IN ('COMPLETED','COMPLETED_WITH_ERRORS','FAILED') THEN notification_generation + 1 ELSE notification_generation END,
		    status = $1, error_message = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := db.Exec(query, status, errMsg, id)
	if err != nil {
		return err
	}
	if status == "COMPLETED" || status == "COMPLETED_WITH_ERRORS" || status == "FAILED" {
		if notifyErr := CreateMigrationNotificationEvent(db, id); notifyErr != nil {
			log.Printf("notification event creation for migration %s failed: %v", id, notifyErr)
		}
	}
	return nil
}

// FailMigrationWhileIndexing records an indexing failure only if the indexer
// still owns the migration lifecycle. It prevents a late provider error from
// replacing a user's CANCELLED status.
func FailMigrationWhileIndexing(db *sql.DB, id string, errMsg *string) (bool, error) {
	result, err := db.Exec(`
		UPDATE migrations
		SET status = 'FAILED', error_message = $1, updated_at = CURRENT_TIMESTAMP,
		    notification_generation = notification_generation + 1
		WHERE id = $2 AND status = 'INDEXING'
	`, errMsg, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rowsAffected != 1 {
		return false, nil
	}
	if err := CreateMigrationNotificationEvent(db, id); err != nil {
		log.Printf("notification event creation for migration %s failed: %v", id, err)
	}
	return true, nil
}

// ClaimScheduledMigrationForIndexing atomically claims a scheduled migration for
// indexing. A false result means the row either does not exist or is no longer
// in SCHEDULED state; both cases are intentionally treated as an invalid claim.
func ClaimScheduledMigrationForIndexing(db *sql.DB, id string) (bool, error) {
	query := `
		UPDATE migrations
		SET status = 'INDEXING', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'SCHEDULED'
	`
	result, err := db.Exec(query, id)
	if err != nil {
		return false, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return claimSucceeded(rowsAffected), nil
}

func claimSucceeded(rowsAffected int64) bool {
	return rowsAffected == 1
}

// TransitionMigrationIndexingToRunning completes indexing. It rejects a stale
// transition instead of silently leaving newly-created tasks unrunnable.
func TransitionMigrationIndexingToRunning(db *sql.DB, id string) error {
	query := `
		UPDATE migrations
		SET status = 'RUNNING', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status = 'INDEXING'
	`
	result, err := db.Exec(query, id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected != 1 {
		return ErrMigrationIndexingClaimLost
	}
	// No notification event is created on RUNNING: notifications are emitted
	// only for terminal migration states.
	return nil
}

// TransitionMigrationIndexingToCompleted completes an empty migration without
// allowing an indexer that lost its lifecycle claim to overwrite cancellation.
func TransitionMigrationIndexingToCompleted(db *sql.DB, id string) error {
	result, err := db.Exec(`
		UPDATE migrations
		SET status = 'COMPLETED', updated_at = CURRENT_TIMESTAMP,
		    notification_generation = notification_generation + 1
		WHERE id = $1 AND status = 'INDEXING'
	`, id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected != 1 {
		return ErrMigrationIndexingClaimLost
	}
	if err := CreateMigrationNotificationEvent(db, id); err != nil {
		log.Printf("notification event creation for migration %s failed: %v", id, err)
	}
	return nil
}

// CreateMigrationTaskWhileIndexing creates a task only while the migration's
// INDEXING claim is still active. The status predicate and insert are one SQL
// statement, so cancellation cannot leave a task inserted after its pending
// task sweep has completed.
func CreateMigrationTaskWhileIndexing(db *sql.DB, t *Task) (string, error) {
	query := `
		INSERT INTO tasks (migration_id, resource_type, file_path, file_size, status, metadata, source_hash)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE EXISTS (SELECT 1 FROM migrations WHERE id = $1 AND status = 'INDEXING')
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(query, t.MigrationID, t.ResourceType, t.FilePath, t.FileSize, t.Status, t.Metadata, t.SourceHash).
		Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrMigrationIndexingClaimLost
	}
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// BulkCreateMigrationTasksWhileIndexing persists indexing batches only while
// the migration still has the INDEXING lifecycle claim. All batches share one
// transaction, so a lost claim rolls back every earlier batch in this call.
func BulkCreateMigrationTasksWhileIndexing(ctx context.Context, db *sql.DB, migrationID string, tasks []*Task) (bool, error) {
	if len(tasks) == 0 {
		return true, nil
	}

	const batchSize = 2000
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(dbCtx, nil)
	if err != nil {
		return false, fmt.Errorf("bulk create migration tasks: begin tx: %w", err)
	}
	defer tx.Rollback()

	for start := 0; start < len(tasks); start += batchSize {
		end := start + batchSize
		if end > len(tasks) {
			end = len(tasks)
		}
		batch := tasks[start:end]
		const paramsPerRow = 6
		args := make([]interface{}, 0, 1+len(batch)*paramsPerRow)
		args = append(args, migrationID)
		valuesClauses := make([]string, 0, len(batch))
		for i, t := range batch {
			base := 2 + i*paramsPerRow
			args = append(args, t.ResourceType, t.FilePath, t.FileSize, t.SourceHash, t.Status, t.Metadata)
			valuesClauses = append(valuesClauses, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)", base, base+1, base+2, base+3, base+4, base+5))
		}
		query := `INSERT INTO tasks (migration_id, resource_type, file_path, file_size, source_hash, status, metadata)
			SELECT $1, v.resource_type, v.file_path, v.file_size, v.source_hash, v.status, v.metadata
			FROM (VALUES ` + strings.Join(valuesClauses, ",") + `) AS v(resource_type, file_path, file_size, source_hash, status, metadata)
			WHERE EXISTS (SELECT 1 FROM migrations WHERE id = $1 AND status = 'INDEXING')`
		res, err := tx.ExecContext(dbCtx, query, args...)
		if err != nil {
			return false, fmt.Errorf("bulk create migration tasks: insert batch [%d:%d]: %w", start, end, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		// All batches share this transaction; a lost claim rolls back every
		// batch inserted so far before this function returns.
		if n != int64(len(batch)) {
			return false, ErrMigrationIndexingClaimLost
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func UpdateMigrationBandwidthLimit(db *sql.DB, id string, limitMbps int) error {
	query := `
		UPDATE migrations
		SET bandwidth_limit_mbps = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, limitMbps, id)
	return err
}

func UpdateMigrationThreads(db *sql.DB, id string, threads int) error {
	query := `
		UPDATE migrations
		SET threads = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, threads, id)
	return err
}

func UpdateMigrationPickerSession(db *sql.DB, id string, pickerSessionID string) error {
	query := `
		UPDATE migrations
		SET picker_session_id = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, pickerSessionID, id)
	return err
}

func UpdateMigrationTotals(db *sql.DB, id string, totalFiles int, totalBytes int64) error {
	query := `
		UPDATE migrations
		SET total_files = $1, total_bytes = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := db.Exec(query, totalFiles, totalBytes, id)
	return err
}

func IncrementMigrationProgress(db *sql.DB, ctx context.Context, id string, filesDelta int, bytesDelta int64, skippedDelta int, failedDelta int) error {
	query := `
		UPDATE migrations
		SET processed_files = processed_files + $1,
		    processed_bytes = processed_bytes + $2,
		    live_bytes      = live_bytes + $2,
		    skipped_files   = skipped_files + $3,
		    failed_files    = failed_files + $4,
		    updated_at      = CURRENT_TIMESTAMP
		WHERE id = $5
	`
	_, err := db.ExecContext(ctx, query, filesDelta, bytesDelta, skippedDelta, failedDelta, id)
	return err
}

func AddLiveBytes(db *sql.DB, ctx context.Context, id string, bytesDelta int64) error {
	query := `
		UPDATE migrations
		SET live_bytes = live_bytes + $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.ExecContext(ctx, query, bytesDelta, id)
	return err
}

func ResetLiveBytes(db *sql.DB, ctx context.Context, id string) error {
	query := `
		UPDATE migrations
		SET live_bytes = processed_bytes, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := db.ExecContext(ctx, query, id)
	return err
}

func ReconcileMigrationProgress(dbsql *sql.DB, migrationID string) error {
	_, err := reconcileMigrationProgress(dbsql, migrationID, nil)
	return err
}

// ReconcileMigrationProgressWhileVerifying finalizes a verification pass only
// while its generation still owns the active lease.
func ReconcileMigrationProgressWhileVerifying(dbsql *sql.DB, migrationID string, generation int) (bool, error) {
	return reconcileMigrationProgress(dbsql, migrationID, &generation)
}

func reconcileMigrationProgress(dbsql *sql.DB, migrationID string, generation *int) (bool, error) {
	// A false, nil result for a fenced caller means its lease expired or was
	// superseded; another verifier owns finalization and this caller must no-op.
	var verificationGeneration sql.NullInt64
	if generation != nil {
		verificationGeneration = sql.NullInt64{Int64: int64(*generation), Valid: true}
	}
	query := `
		WITH task_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE status = 'COMPLETED') AS done_files,
				COALESCE(SUM(file_size) FILTER (WHERE status = 'COMPLETED'), 0) AS done_bytes,
				COUNT(*) FILTER (WHERE status = 'SKIPPED') AS skip_files,
				COUNT(*) FILTER (WHERE status = 'FAILED') AS fail_files,
				COUNT(*) FILTER (WHERE status = 'CANCELLED') AS cancelled_files,
				-- A FAILED task with a retry time is still transfer work.  In
				-- particular, an OAuth refresh deliberately parks the task in FAILED
				-- until the retry scheduler recreates its provider.  Treating that
				-- state as terminal can start checksum verification before the retry
				-- is dequeued, after which workers are intentionally barred from
				-- claiming the task.
				COUNT(*) FILTER (WHERE status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL)) AS active_files,
				COUNT(*) FILTER (WHERE status = 'COMPLETED' AND checksum_verified = FALSE) AS unverified_files
			FROM tasks
			WHERE migration_id = $1
		),
		error_stats AS (
			SELECT COUNT(*) AS err_files
			FROM indexing_errors
			WHERE migration_id = $1
		)
		UPDATE migrations m
		SET processed_files = t.done_files + t.skip_files + t.fail_files + t.cancelled_files,
		    processed_bytes = t.done_bytes,
		    live_bytes      = t.done_bytes,
		    skipped_files   = t.skip_files,
			failed_files    = t.fail_files + t.cancelled_files,
		    status = CASE
		        WHEN m.status IN ('CANCELLED', 'PAUSED', 'PAUSED_CONNECTION_LOSS') THEN m.status
		        WHEN t.active_files > 0 THEN 'RUNNING'
		        WHEN t.unverified_files > 0 THEN 'VERIFYING'
				WHEN (t.fail_files + t.cancelled_files + e.err_files) > 0 THEN 'COMPLETED_WITH_ERRORS'
		        ELSE 'COMPLETED'
		    END,
		    updated_at = CURRENT_TIMESTAMP
		FROM task_stats t, error_stats e
		WHERE m.id = $1
		  AND ($2::bigint IS NULL OR (m.status = 'VERIFYING' AND m.verification_generation = $2 AND m.verification_lease_until > NOW()))
	`
	res, err := dbsql.Exec(query, migrationID, verificationGeneration)
	if err != nil {
		return false, fmt.Errorf("ReconcileMigrationProgress exec failed for %s: %w", migrationID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if generation != nil {
			return false, nil
		}
		return false, fmt.Errorf("ReconcileMigrationProgress: migration %s not found", migrationID)
	}
	var status string
	if err := dbsql.QueryRow(`SELECT status FROM migrations WHERE id = $1`, migrationID).Scan(&status); err != nil {
		return false, err
	}
	if status == "COMPLETED" || status == "COMPLETED_WITH_ERRORS" || status == "FAILED" {
		if notifyErr := CreateMigrationNotificationEvent(dbsql, migrationID); notifyErr != nil {
			log.Printf("notification event creation for reconciled migration %s failed: %v", migrationID, notifyErr)
		}
	}
	return true, nil
}

func CountActiveMigrationsForUser(db *sql.DB, userID string) (int, error) {
	query := `
		SELECT COUNT(*) FROM migrations
		WHERE user_id = $1
		  AND status IN ('INDEXING', 'RUNNING', 'PAUSED', 'PAUSED_CONNECTION_LOSS')
	`
	var count int
	err := db.QueryRow(query, userID).Scan(&count)
	return count, err
}

func VerifyMigrationOwnership(db *sql.DB, migrationID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM migrations WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRow(query, migrationID, userID).Scan(&exists)
	return exists, err
}

func GetMigrationOwnerID(database queryExecer, migrationID string) (string, error) {
	var owner sql.NullString
	err := database.QueryRow(`SELECT user_id FROM migrations WHERE id = $1`, migrationID).Scan(&owner)
	if err != nil {
		return "", err
	}
	if !owner.Valid {
		return "", fmt.Errorf("migration %s has no owner", migrationID)
	}
	return owner.String, nil
}

func DeleteMigrationCascade(db *sql.DB, migrationID string) error {
	_ = DeleteSchedulesForTask(db, "migration", migrationID)
	query := `DELETE FROM migrations WHERE id = $1`
	_, err := db.Exec(query, migrationID)
	return err
}

func DeleteOldMigrations(db *sql.DB) (int64, error) {
	query := `
		DELETE FROM migrations
		WHERE status IN ('COMPLETED', 'FAILED', 'CANCELLED', 'COMPLETED_WITH_ERRORS')
		  AND updated_at < NOW() - INTERVAL '30 days'
	`
	res, err := db.Exec(query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func GetMigrationResourceStats(db *sql.DB, migrationID string) (*MigrationResourceStats, error) {
	query := `
		SELECT resource_type, status, COUNT(*)
		FROM tasks
		WHERE migration_id = $1
		GROUP BY resource_type, status
	`
	rows, err := db.Query(query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &MigrationResourceStats{}
	for rows.Next() {
		var resType, status string
		var count int
		if err := rows.Scan(&resType, &status, &count); err != nil {
			return nil, err
		}

		var target *ResourceStats
		switch resType {
		case "calendars":
			target = &stats.Calendars
		case "contacts":
			target = &stats.Contacts
		default:
			target = &stats.Files
		}

		target.Total += count
		switch status {
		case "COMPLETED":
			target.Processed += count
		case "FAILED":
			target.Failed += count
		case "SKIPPED":
			target.Skipped += count
		}
	}
	return stats, rows.Err()
}

var ErrOAuthTokenConflict = errors.New("oauth token update conflict: persisted token changed concurrently")

func UpdateMigrationOAuthTokens(db *sql.DB, u OAuthTokenUpdate, expectedRefreshTokenEncrypted string) error {
	if u.Role != "source" && u.Role != "target" {
		return fmt.Errorf("invalid oauth token role %q", u.Role)
	}
	// OAuth rotation must always be anchored to an existing encrypted refresh
	// token. An empty expected value would turn the SQL NULL/empty branch into
	// an unconditional credential upsert for non-OAuth migrations.
	if expectedRefreshTokenEncrypted == "" {
		return ErrOAuthTokenConflict
	}
	var query string
	if u.Role == "source" {
		query = `
			UPDATE migrations
			SET source_password_encrypted        = $1,
			    source_refresh_token_encrypted   = $2,
			    source_token_expires_at          = $3,
			    updated_at                       = CURRENT_TIMESTAMP
			WHERE id = $4
			  AND source_refresh_token_encrypted = $5
		`
	} else {
		query = `
			UPDATE migrations
			SET target_password_encrypted        = $1,
			    target_refresh_token_encrypted   = $2,
			    target_token_expires_at          = $3,
			    updated_at                       = CURRENT_TIMESTAMP
			WHERE id = $4
			  AND target_refresh_token_encrypted = $5
		`
	}
	res, err := db.Exec(query, u.AccessTokenEncrypted, u.RefreshTokenEncrypted, u.ExpiresAt, u.MigrationID, expectedRefreshTokenEncrypted)
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

func UpdateMigrationOAuthTokensForReauth(db *sql.DB, u OAuthTokenUpdate) error {
	if u.Role != "source" && u.Role != "target" {
		return fmt.Errorf("invalid oauth token role %q", u.Role)
	}
	columns := "source_password_encrypted = $1, source_refresh_token_encrypted = $2, source_token_expires_at = $3"
	if u.Role == "target" {
		columns = "target_password_encrypted = $1, target_refresh_token_encrypted = $2, target_token_expires_at = $3"
	}
	_, err := db.Exec("UPDATE migrations SET "+columns+", updated_at = CURRENT_TIMESTAMP WHERE id = $4", u.AccessTokenEncrypted, u.RefreshTokenEncrypted, u.ExpiresAt, u.MigrationID)
	return err
}

func GetExpiringOAuthMigrations(db *sql.DB) ([]ExpiringOAuthMigration, error) {
	threshold := time.Now().Add(15 * time.Minute)
	query := `
		SELECT id, 'source' AS role, source_provider, source_refresh_token_encrypted
		FROM migrations
		WHERE status IN ('INDEXING', 'RUNNING')
		  AND source_refresh_token_encrypted IS NOT NULL
		  AND source_token_expires_at IS NOT NULL
		  AND source_token_expires_at < $1
		UNION ALL
		SELECT id, 'target' AS role, target_provider, target_refresh_token_encrypted
		FROM migrations
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

	var result []ExpiringOAuthMigration
	for rows.Next() {
		var e ExpiringOAuthMigration
		if err := rows.Scan(&e.MigrationID, &e.Role, &e.Provider, &e.RefreshTokenEncrypted); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func LockPendingEmailNotifications(dbsql *sql.DB, limit int) (*sql.Tx, []PendingEmailNotification, error) {
	tx, err := dbsql.Begin()
	if err != nil {
		return nil, nil, err
	}

	query := `
		SELECT m.id, m.user_id, m.status, m.total_files, m.processed_files,
		       m.failed_files, m.skipped_files, m.total_bytes, m.processed_bytes, m.error_message
		FROM migrations m
		WHERE m.status IN ('COMPLETED', 'FAILED', 'COMPLETED_WITH_ERRORS')
		  AND m.email_sent = FALSE
		  AND m.user_id IS NOT NULL
		ORDER BY m.id
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(query, limit)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}

	var notifications []PendingEmailNotification
	for rows.Next() {
		var n PendingEmailNotification
		err := rows.Scan(&n.MigrationID, &n.UserID, &n.Status, &n.TotalFiles, &n.ProcessedFiles,
			&n.FailedFiles, &n.SkippedFiles, &n.TotalBytes, &n.ProcessedBytes, &n.ErrorMessage)
		if err != nil {
			rows.Close()
			_ = tx.Rollback()
			return nil, nil, err
		}
		notifications = append(notifications, n)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		_ = tx.Rollback()
		return nil, nil, err
	}
	rows.Close()

	return tx, notifications, nil
}

func MarkMigrationEmailSentTx(tx *sql.Tx, migrationID string) error {
	_, err := tx.Exec(
		`UPDATE migrations SET email_sent = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
		migrationID,
	)
	return err
}

func MarkMigrationEmailSent(db *sql.DB, migrationID string) error {
	query := `UPDATE migrations SET email_sent = TRUE WHERE id = $1`
	_, err := db.Exec(query, migrationID)
	return err
}
