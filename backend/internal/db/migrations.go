package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrMigrationNotFailed = errors.New("migration is not in a failed state")

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
	ID                          string         `json:"id"`
	UserID                      sql.NullString `json:"user_id,omitempty"`
	SourceURL                   string         `json:"source_url"`
	SourceUsername              string         `json:"source_username"`
	SourcePasswordEncrypted     string         `json:"-"`
	SourceProvider              string         `json:"source_provider"`
	SourceRefreshTokenEncrypted sql.NullString `json:"-"`
	SourceTokenExpiresAt        sql.NullTime   `json:"-"`
	TargetURL                   string         `json:"target_url"`
	TargetUsername              string         `json:"target_username"`
	TargetPasswordEncrypted     string         `json:"-"`
	TargetProvider              string         `json:"target_provider"`
	TargetRefreshTokenEncrypted sql.NullString `json:"-"`
	TargetTokenExpiresAt        sql.NullTime   `json:"-"`
	TargetDir                   string         `json:"target_dir"`
	Status                      string         `json:"status"` // PENDING, INDEXING, RUNNING, PAUSED, COMPLETED, FAILED, CANCELLED
	ConflictStrategy            string         `json:"conflict_strategy"`
	TotalFiles                  int            `json:"total_files"`
	TotalBytes                  int64          `json:"total_bytes"`
	ProcessedFiles              int            `json:"processed_files"`
	ProcessedBytes              int64          `json:"processed_bytes"`
	LiveBytes                   int64          `json:"live_bytes"`
	SkippedFiles                int            `json:"skipped_files"`
	FailedFiles                 int            `json:"failed_files"`
	ErrorMessage                sql.NullString `json:"error_message,omitempty"`
	CreatedAt                   time.Time      `json:"created_at"`
	UpdatedAt                   time.Time      `json:"updated_at"`
	Threads                     int            `json:"threads"`
	BandwidthLimitMbps          int            `json:"bandwidth_limit_mbps"`
	PickerSessionID             string         `json:"picker_session_id,omitempty"`
	SelectedPaths               StringArray    `json:"selected_paths,omitempty"`
	SelectedCalendars           StringArray             `json:"selected_calendars,omitempty"`
	SelectedContacts            StringArray             `json:"selected_contacts,omitempty"`
	ResourceStats               *MigrationResourceStats `json:"resource_stats,omitempty"`
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

func CreateMigration(db *sql.DB, m *Migration) (string, error) {
	query := `
		INSERT INTO migrations (
			user_id, source_url, source_username, source_password_encrypted, source_provider,
			target_url, target_username, target_password_encrypted, target_provider,
			status, conflict_strategy, target_dir, threads, bandwidth_limit_mbps,
			picker_session_id, selected_paths, selected_calendars, selected_contacts
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		m.UserID, m.SourceURL, m.SourceUsername, m.SourcePasswordEncrypted, m.SourceProvider,
		m.TargetURL, m.TargetUsername, m.TargetPasswordEncrypted, m.TargetProvider,
		m.Status, m.ConflictStrategy, m.TargetDir, m.Threads, m.BandwidthLimitMbps,
		m.PickerSessionID, m.SelectedPaths, m.SelectedCalendars, m.SelectedContacts,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		return "", err
	}
	return m.ID, nil
}

func GetMigration(db *sql.DB, id string) (*Migration, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted, source_provider,
		       target_url, target_username, target_password_encrypted, target_provider,
		       status, conflict_strategy, total_files, total_bytes, processed_files,
		       processed_bytes, live_bytes, skipped_files, failed_files, error_message,
		       created_at, updated_at, target_dir, threads, bandwidth_limit_mbps,
		       picker_session_id, selected_paths, selected_calendars, selected_contacts
		FROM migrations WHERE id = $1
	`
	var m Migration
	err := db.QueryRow(query, id).Scan(
		&m.ID, &m.UserID, &m.SourceURL, &m.SourceUsername, &m.SourcePasswordEncrypted, &m.SourceProvider,
		&m.TargetURL, &m.TargetUsername, &m.TargetPasswordEncrypted, &m.TargetProvider,
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
		SET status = $1, error_message = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3
	`
	_, err := db.Exec(query, status, errMsg, id)
	return err
}

func UpdateMigrationStatusIfIndexing(db *sql.DB, id string, status string) error {
	query := `
		UPDATE migrations
		SET status = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND status = 'INDEXING'
	`
	_, err := db.Exec(query, status, id)
	return err
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
	query := `
		WITH task_stats AS (
			SELECT
				COUNT(*) FILTER (WHERE status = 'COMPLETED') AS done_files,
				COALESCE(SUM(file_size) FILTER (WHERE status = 'COMPLETED'), 0) AS done_bytes,
				COUNT(*) FILTER (WHERE status = 'SKIPPED') AS skip_files,
				COUNT(*) FILTER (WHERE status = 'FAILED') AS fail_files,
				COUNT(*) FILTER (WHERE status IN ('PENDING', 'RUNNING')) AS active_files,
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
		SET processed_files = t.done_files + t.skip_files + t.fail_files,
		    processed_bytes = t.done_bytes,
		    live_bytes      = t.done_bytes,
		    skipped_files   = t.skip_files,
		    failed_files    = t.fail_files,
		    status = CASE
		        WHEN m.status IN ('CANCELLED', 'PAUSED', 'PAUSED_CONNECTION_LOSS') THEN m.status
		        WHEN t.active_files > 0 THEN 'RUNNING'
		        WHEN t.unverified_files > 0 THEN 'VERIFYING'
		        WHEN (t.fail_files + e.err_files) > 0 THEN 'COMPLETED_WITH_ERRORS'
		        ELSE 'COMPLETED'
		    END,
		    updated_at = CURRENT_TIMESTAMP
		FROM task_stats t, error_stats e
		WHERE m.id = $1
	`
	res, err := dbsql.Exec(query, migrationID)
	if err != nil {
		return fmt.Errorf("ReconcileMigrationProgress exec failed for %s: %w", migrationID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("ReconcileMigrationProgress: migration %s not found", migrationID)
	}
	return nil
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

func UpdateMigrationOAuthTokens(db *sql.DB, u OAuthTokenUpdate) error {
	if u.Role != "source" && u.Role != "target" {
		return fmt.Errorf("invalid oauth token role %q", u.Role)
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
		`
	} else {
		query = `
			UPDATE migrations
			SET target_password_encrypted        = $1,
			    target_refresh_token_encrypted   = $2,
			    target_token_expires_at          = $3,
			    updated_at                       = CURRENT_TIMESTAMP
			WHERE id = $4
		`
	}
	_, err := db.Exec(query, u.AccessTokenEncrypted, u.RefreshTokenEncrypted, u.ExpiresAt, u.MigrationID)
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
