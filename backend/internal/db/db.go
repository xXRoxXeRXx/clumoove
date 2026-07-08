package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/lib/pq"
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
	ID                      string                  `json:"id"`
	SourceURL               string                  `json:"source_url"`
	SourceUsername          string                  `json:"source_username"`
	SourcePasswordEncrypted string                  `json:"-"`
	TargetURL               string                  `json:"target_url"`
	TargetUsername          string                  `json:"target_username"`
	TargetPasswordEncrypted string                  `json:"-"`
	SourceProvider          string                  `json:"source_provider"`
	TargetProvider          string                  `json:"target_provider"`
	TargetDir               string                  `json:"target_dir"`
	Status                  string                  `json:"status"`            // PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED
	ConflictStrategy        string                  `json:"conflict_strategy"` // SKIP, OVERWRITE, RENAME
	TotalFiles              int                     `json:"total_files"`
	TotalBytes              int64                   `json:"total_bytes"`
	ProcessedFiles          int                     `json:"processed_files"`
	ProcessedBytes          int64                   `json:"processed_bytes"`
	SkippedFiles            int                     `json:"skipped_files"`
	FailedFiles             int                     `json:"failed_files"`
	ErrorMessage            sql.NullString          `json:"error_message"`
	CreatedAt               time.Time               `json:"created_at"`
	UpdatedAt               time.Time               `json:"updated_at"`
	ResourceStats           *MigrationResourceStats `json:"resource_stats,omitempty"`
}


type Task struct {
	ID           string         `json:"id"`
	MigrationID  string         `json:"migration_id"`
	FilePath     string         `json:"file_path"`
	FileSize     int64          `json:"file_size"`
	SourceHash   sql.NullString `json:"source_hash"`
	WorkerHash   sql.NullString `json:"worker_hash"`
	TargetHash   sql.NullString `json:"target_hash"`
	Status       string         `json:"status"` // PENDING, RUNNING, COMPLETED, FAILED, SKIPPED
	ResourceType string         `json:"resource_type"` // files, calendars, contacts
	ErrorMessage sql.NullString `json:"error_message"`
	Attempts     int            `json:"attempts"`
	NextRetryAt  sql.NullTime   `json:"next_retry_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// InitDB initializes the database connection with startup retries
func InitDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	// Test connection with retry loop (resilient to Docker startup order)
	var pingErr error
	for attempt := 1; attempt <= 10; attempt++ {
		pingErr = db.Ping()
		if pingErr == nil {
			// Run schema migrations for new columns
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_provider TEXT NOT NULL DEFAULT 'nextcloud'`)
			if err != nil {
				log.Printf("Failed schema migration (source_provider): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_provider TEXT NOT NULL DEFAULT 'nextcloud'`)
			if err != nil {
				log.Printf("Failed schema migration (target_provider): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS resource_type TEXT NOT NULL DEFAULT 'files'`)
			if err != nil {
				log.Printf("Failed schema migration (resource_type): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_dir TEXT NOT NULL DEFAULT '/'`)
			if err != nil {
				log.Printf("Failed schema migration (target_dir): %v\n", err)
			}

			// Set connection pool settings
			db.SetMaxOpenConns(25)
			db.SetMaxIdleConns(5)
			db.SetConnMaxLifetime(5 * time.Minute)
			return db, nil
		}
		log.Printf("Waiting for PostgreSQL database to be ready (attempt %d/10): %v\n", attempt, pingErr)
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("database not ready after 10 attempts: %w", pingErr)
}

// CreateMigration inserts a new migration job and returns the UUID
func CreateMigration(db *sql.DB, m *Migration) (string, error) {
	query := `
		INSERT INTO migrations (
			source_url, source_username, source_password_encrypted,
			target_url, target_username, target_password_encrypted,
			source_provider, target_provider, status, conflict_strategy, target_dir
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		m.SourceURL, m.SourceUsername, m.SourcePasswordEncrypted,
		m.TargetURL, m.TargetUsername, m.TargetPasswordEncrypted,
		m.SourceProvider, m.TargetProvider, m.Status, m.ConflictStrategy, m.TargetDir,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// GetMigration retrieves a migration by ID
func GetMigration(db *sql.DB, id string) (*Migration, error) {
	query := `
		SELECT id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, status, conflict_strategy, total_files, total_bytes,
		       processed_files, processed_bytes, skipped_files, failed_files,
		       error_message, created_at, updated_at, target_dir
		FROM migrations WHERE id = $1
	`
	var m Migration
	err := db.QueryRow(query, id).Scan(
		&m.ID, &m.SourceURL, &m.SourceUsername, &m.SourcePasswordEncrypted,
		&m.TargetURL, &m.TargetUsername, &m.TargetPasswordEncrypted,
		&m.SourceProvider, &m.TargetProvider, &m.Status, &m.ConflictStrategy, &m.TotalFiles, &m.TotalBytes,
		&m.ProcessedFiles, &m.ProcessedBytes, &m.SkippedFiles, &m.FailedFiles,
		&m.ErrorMessage, &m.CreatedAt, &m.UpdatedAt, &m.TargetDir,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMigrationStatus updates the status of a migration.
// If errMsg is non-nil the error_message column is also updated;
// passing nil leaves any previously recorded error intact.
func UpdateMigrationStatus(db *sql.DB, id string, status string, errMsg *string) error {
	if errMsg != nil {
		query := `
			UPDATE migrations
			SET status = $1, error_message = $2
			WHERE id = $3
		`
		_, err := db.Exec(query, status, sql.NullString{String: *errMsg, Valid: true}, id)
		return err
	}
	query := `
		UPDATE migrations
		SET status = $1
		WHERE id = $2
	`
	_, err := db.Exec(query, status, id)
	return err
}

// GetActiveTaskPath returns the file_path of the first task currently in RUNNING state
// for the given migration, or an empty string if none exists.
func GetActiveTaskPath(db *sql.DB, ctx context.Context, migrationID string) (string, error) {
	query := `SELECT file_path FROM tasks WHERE migration_id = $1 AND status = 'RUNNING' LIMIT 1`
	var path string
	err := db.QueryRowContext(ctx, query, migrationID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// IncrementMigrationProgress increments the counters of a migration in the database
// and transitions the migration to COMPLETED or FAILED once all files are processed.
func IncrementMigrationProgress(db *sql.DB, id string, filesDelta int, bytesDelta int64, skippedDelta int, failedDelta int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
		UPDATE migrations
		SET processed_files = processed_files + $1,
		    processed_bytes = processed_bytes + $2,
		    skipped_files = skipped_files + $3,
		    failed_files = failed_files + $4,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $5
		RETURNING processed_files, total_files, failed_files
	`
	var processed, total, failed int
	err = tx.QueryRow(query, filesDelta, bytesDelta, skippedDelta, failedDelta, id).Scan(&processed, &total, &failed)
	if err != nil {
		return err
	}

	if total > 0 && processed >= total {
		finalStatus := "COMPLETED"
		var errMessage sql.NullString
		if failed == total {
			finalStatus = "FAILED"
			errMessage = sql.NullString{String: "All file transfers failed", Valid: true}
		}

		statusQuery := `
			UPDATE migrations
			SET status = $1,
			    error_message = COALESCE($2, error_message)
			WHERE id = $3
		`
		_, err = tx.Exec(statusQuery, finalStatus, errMessage, id)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateMigrationTotals sets the total files and total bytes calculated during indexing
func UpdateMigrationTotals(db *sql.DB, id string, totalFiles int, totalBytes int64) error {
	query := `
		UPDATE migrations
		SET total_files = $1, total_bytes = $2
		WHERE id = $3
	`
	_, err := db.Exec(query, totalFiles, totalBytes, id)
	return err
}

// CreateTask inserts a new task for migration
func CreateTask(db *sql.DB, t *Task) (string, error) {
	query := `
		INSERT INTO tasks (
			migration_id, file_path, file_size, source_hash, status, resource_type
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		t.MigrationID, t.FilePath, t.FileSize, t.SourceHash, t.Status, t.ResourceType,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)

	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// GetTask retrieves a single task by ID
func GetTask(db *sql.DB, id string) (*Task, error) {
	query := `
		SELECT id, migration_id, file_path, file_size, source_hash, worker_hash, target_hash,
		       status, error_message, attempts, next_retry_at, created_at, updated_at, resource_type
		FROM tasks WHERE id = $1
	`
	var t Task
	err := db.QueryRow(query, id).Scan(
		&t.ID, &t.MigrationID, &t.FilePath, &t.FileSize, &t.SourceHash, &t.WorkerHash, &t.TargetHash,
		&t.Status, &t.ErrorMessage, &t.Attempts, &t.NextRetryAt, &t.CreatedAt, &t.UpdatedAt, &t.ResourceType,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateTaskStatus updates a task status, hashes, error message, attempts and next retry time
func UpdateTaskStatus(db *sql.DB, t *Task) error {
	query := `
		UPDATE tasks
		SET status = $1, worker_hash = $2, target_hash = $3, error_message = $4,
		    attempts = $5, next_retry_at = $6
		WHERE id = $7
	`
	_, err := db.Exec(
		query,
		t.Status, t.WorkerHash, t.TargetHash, t.ErrorMessage,
		t.Attempts, t.NextRetryAt, t.ID,
	)
	return err
}

// GetFailedTasksForReport retrieves all failed tasks of a migration for reporting
func GetFailedTasksForReport(db *sql.DB, migrationID string) ([]Task, error) {
	query := `
		SELECT id, file_path, file_size, status, error_message, attempts, updated_at
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
		err := rows.Scan(&t.ID, &t.FilePath, &t.FileSize, &t.Status, &t.ErrorMessage, &t.Attempts, &t.UpdatedAt)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// DeleteOldMigrations deletes migrations (and their tasks via CASCADE) older than 24 hours
func DeleteOldMigrations(db *sql.DB) (int64, error) {
	query := `
		DELETE FROM migrations
		WHERE updated_at < $1
	`
	cutoff := time.Now().Add(-24 * time.Hour)
	res, err := db.Exec(query, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetMigrationResourceStats returns the count statistics grouped by resource_type (files, calendars, contacts)
func GetMigrationResourceStats(db *sql.DB, migrationID string) (*MigrationResourceStats, error) {
	query := `
		SELECT resource_type, status, attempts, COUNT(*)
		FROM tasks
		WHERE migration_id = $1
		GROUP BY resource_type, status, attempts
	`
	rows, err := db.Query(query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &MigrationResourceStats{
		Files:     ResourceStats{},
		Calendars: ResourceStats{},
		Contacts:  ResourceStats{},
	}

	for rows.Next() {
		var resourceType string
		var status string
		var attempts int
		var count int
		if err := rows.Scan(&resourceType, &status, &attempts, &count); err != nil {
			return nil, err
		}

		var r *ResourceStats
		switch resourceType {
		case "files":
			r = &stats.Files
		case "calendars":
			r = &stats.Calendars
		case "contacts":
			r = &stats.Contacts
		default:
			continue
		}

		r.Total += count
		switch status {
		case "COMPLETED":
			r.Processed += count
		case "SKIPPED":
			r.Skipped += count
			r.Processed += count
		case "FAILED":
			if attempts >= 3 {
				r.Failed += count
				r.Processed += count
			}
		}
	}

	return stats, nil
}

