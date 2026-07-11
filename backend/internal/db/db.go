package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
)

// StringArray is a []string that implements sql.Scanner and driver.Valuer
// for seamless JSONB <-> Go string slice conversion with lib/pq.
type StringArray []string

// Value implements driver.Valuer, encoding the slice as a JSON byte slice
// suitable for PostgreSQL JSONB columns.
func (s StringArray) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("StringArray.Value: %w", err)
	}
	return b, nil
}

// Scan implements sql.Scanner, decoding a JSONB column value into the string slice.
func (s *StringArray) Scan(src interface{}) error {
	if src == nil {
		*s = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("StringArray.Scan: unsupported type %T", src)
	}
	return json.Unmarshal(b, s)
}

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

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	Avatar       []byte    `json:"-"`
	AvatarMime   string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type RefreshToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type Migration struct {
	ID                          string                  `json:"id"`
	UserID                      sql.NullString          `json:"user_id"`
	SourceURL                   string                  `json:"source_url"`
	SourceUsername              string                  `json:"source_username"`
	SourcePasswordEncrypted     string                  `json:"-"`
	SourceRefreshTokenEncrypted sql.NullString          `json:"-"`
	SourceTokenExpiresAt        sql.NullTime            `json:"source_token_expires_at,omitempty"`
	TargetURL                   string                  `json:"target_url"`
	TargetUsername              string                  `json:"target_username"`
	TargetPasswordEncrypted     string                  `json:"-"`
	TargetRefreshTokenEncrypted sql.NullString          `json:"-"`
	TargetTokenExpiresAt        sql.NullTime            `json:"target_token_expires_at,omitempty"`
	SourceProvider              string                  `json:"source_provider"`
	TargetProvider              string                  `json:"target_provider"`
	TargetDir                   string                  `json:"target_dir"`
	Status                      string                  `json:"status"`            // PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED, SCHEDULED
	ConflictStrategy            string                  `json:"conflict_strategy"` // SKIP, OVERWRITE, RENAME
	SelectedPaths               StringArray             `json:"selected_paths,omitempty"`
	SelectedCalendars           StringArray             `json:"selected_calendars,omitempty"`
	SelectedContacts            StringArray             `json:"selected_contacts,omitempty"`
	TotalFiles                  int                     `json:"total_files"`
	TotalBytes                  int64                   `json:"total_bytes"`
	ProcessedFiles              int                     `json:"processed_files"`
	ProcessedBytes              int64                   `json:"processed_bytes"`
	SkippedFiles                int                     `json:"skipped_files"`
	FailedFiles                 int                     `json:"failed_files"`
	ErrorMessage                sql.NullString          `json:"error_message"`
	CreatedAt                   time.Time               `json:"created_at"`
	UpdatedAt                   time.Time               `json:"updated_at"`
	ResourceStats               *MigrationResourceStats `json:"resource_stats,omitempty"`
	Threads                     int                     `json:"threads"`
}

type Task struct {
	ID           string         `json:"id"`
	MigrationID  string         `json:"migration_id"`
	FilePath     string         `json:"file_path"`
	FileSize     int64          `json:"file_size"`
	SourceHash   sql.NullString `json:"source_hash"`
	WorkerHash   sql.NullString `json:"worker_hash"`
	TargetHash   sql.NullString `json:"target_hash"`
	Status       string         `json:"status"`        // PENDING, RUNNING, COMPLETED, FAILED, SKIPPED
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
			// Run schema migrations for new columns and tables
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS users (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					email TEXT UNIQUE NOT NULL,
					password_hash TEXT NOT NULL,
					display_name TEXT NOT NULL,
					role TEXT NOT NULL DEFAULT 'USER',
					avatar BYTEA,
					avatar_mime TEXT,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				// Fatal: all other migrations depend on this table
				return nil, fmt.Errorf("fatal schema migration (create users table): %w", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS refresh_tokens (
					token_hash TEXT PRIMARY KEY,
					user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				// Fatal: authentication depends on this table
				return nil, fmt.Errorf("fatal schema migration (create refresh_tokens table): %w", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS settings (
					key TEXT PRIMARY KEY,
					value TEXT NOT NULL,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				return nil, fmt.Errorf("fatal schema migration (create settings table): %w", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar BYTEA`)
			if err != nil {
				log.Printf("Failed schema migration (user avatar): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_mime TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (user avatar_mime): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE`)
			if err != nil {
				log.Printf("Failed schema migration (user_id): %v\n", err)
			}

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
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS threads INT NOT NULL DEFAULT 4`)
			if err != nil {
				log.Printf("Failed schema migration (threads): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_refresh_token_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (source_refresh_token_encrypted): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_refresh_token_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (target_refresh_token_encrypted): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_token_expires_at TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (source_token_expires_at): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_token_expires_at TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (target_token_expires_at): %v\n", err)
			}

			// Scheduler: persist selected paths/calendars/contacts for deferred re-indexing
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS selected_paths JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (selected_paths): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS selected_calendars JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (selected_calendars): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS selected_contacts JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (selected_contacts): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_migrations_user_id ON migrations(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_migrations_user_id): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_migration_status ON tasks(migration_id, status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_migration_status): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_retry ON tasks(status, next_retry_at) WHERE status = 'FAILED' AND next_retry_at IS NOT NULL`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_retry): %v\n", err)
			}

			// Schedules table for Core Scheduler Engine
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS schedules (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					task_type TEXT NOT NULL CHECK (task_type IN ('migration', 'sync', 'backup')),
					task_id UUID NOT NULL,
					cron_expression TEXT,
					run_at TIMESTAMP WITH TIME ZONE,
					next_run_at TIMESTAMP WITH TIME ZONE,
					is_active BOOLEAN NOT NULL DEFAULT TRUE,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create schedules table): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(next_run_at) WHERE is_active = TRUE`)
			if err != nil {
				log.Printf("Failed schema migration (idx_schedules_next_run): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_schedules_user_id ON schedules(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_schedules_user_id): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_schedules_task ON schedules(task_type, task_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_schedules_task): %v\n", err)
			}

			// Set connection pool settings
			maxConns := 50
			if envVal := os.Getenv("MAX_THREADS"); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					maxConns = val * 2 // ensure pool has enough connections for main threads + helper queries
					if maxConns < 50 {
						maxConns = 50
					}
				}
			}
			db.SetMaxOpenConns(maxConns)
			db.SetMaxIdleConns(10)
			db.SetConnMaxLifetime(time.Hour)
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
			user_id, source_url, source_username, source_password_encrypted,
			source_refresh_token_encrypted, source_token_expires_at,
			target_url, target_username, target_password_encrypted,
			target_refresh_token_encrypted, target_token_expires_at,
			source_provider, target_provider, status, conflict_strategy, target_dir,
			selected_paths, selected_calendars, selected_contacts, threads
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		m.UserID, m.SourceURL, m.SourceUsername, m.SourcePasswordEncrypted,
		m.SourceRefreshTokenEncrypted, m.SourceTokenExpiresAt,
		m.TargetURL, m.TargetUsername, m.TargetPasswordEncrypted,
		m.TargetRefreshTokenEncrypted, m.TargetTokenExpiresAt,
		m.SourceProvider, m.TargetProvider, m.Status, m.ConflictStrategy, m.TargetDir,
		m.SelectedPaths, m.SelectedCalendars, m.SelectedContacts, m.Threads,
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		return "", err
	}
	return m.ID, nil
}

// GetMigration retrieves a migration by ID
func GetMigration(db *sql.DB, id string) (*Migration, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       source_refresh_token_encrypted, source_token_expires_at,
		       target_url, target_username, target_password_encrypted,
		       target_refresh_token_encrypted, target_token_expires_at,
		       source_provider, target_provider, status, conflict_strategy, total_files, total_bytes,
		       processed_files, processed_bytes, skipped_files, failed_files,
		       error_message, created_at, updated_at, target_dir, threads,
		       selected_paths, selected_calendars, selected_contacts
		FROM migrations WHERE id = $1
	`
	var m Migration
	err := db.QueryRow(query, id).Scan(
		&m.ID, &m.UserID, &m.SourceURL, &m.SourceUsername, &m.SourcePasswordEncrypted,
		&m.SourceRefreshTokenEncrypted, &m.SourceTokenExpiresAt,
		&m.TargetURL, &m.TargetUsername, &m.TargetPasswordEncrypted,
		&m.TargetRefreshTokenEncrypted, &m.TargetTokenExpiresAt,
		&m.SourceProvider, &m.TargetProvider, &m.Status, &m.ConflictStrategy, &m.TotalFiles, &m.TotalBytes,
		&m.ProcessedFiles, &m.ProcessedBytes, &m.SkippedFiles, &m.FailedFiles,
		&m.ErrorMessage, &m.CreatedAt, &m.UpdatedAt, &m.TargetDir, &m.Threads,
		&m.SelectedPaths, &m.SelectedCalendars, &m.SelectedContacts,
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

// UpdateMigrationStatusIfIndexing updates the migration status to the new status
// only if it is currently 'INDEXING' or 'SCHEDULED'. This allows the shared
// indexer to finalize both the immediate path (migration created as INDEXING)
// and the deferred/scheduled path (migration created as SCHEDULED and triggered
// by the scheduler) into RUNNING once tasks have been created.
func UpdateMigrationStatusIfIndexing(db *sql.DB, id string, status string) error {
	query := `
		UPDATE migrations
		SET status = $1
		WHERE id = $2 AND status IN ('INDEXING', 'SCHEDULED')
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

// GetActiveTaskPaths returns the file_paths of all tasks currently in RUNNING state
// for the given migration.
func GetActiveTaskPaths(db *sql.DB, ctx context.Context, migrationID string) ([]string, error) {
	query := `SELECT file_path FROM tasks WHERE migration_id = $1 AND status = 'RUNNING' ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, query, migrationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return paths, nil
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
			  AND status IN ('RUNNING', 'INDEXING')
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
	if err := rows.Err(); err != nil {
		return nil, err
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
		SELECT resource_type, status, (next_retry_at IS NULL) AS permanent_fail, COUNT(*)
		FROM tasks
		WHERE migration_id = $1
		GROUP BY resource_type, status, (next_retry_at IS NULL)
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
		var permanentFail bool
		var count int
		if err := rows.Scan(&resourceType, &status, &permanentFail, &count); err != nil {
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
			if permanentFail {
				r.Failed += count
				r.Processed += count
			}
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// CreateUser inserts a new user into the database
func CreateUser(db *sql.DB, email, passwordHash, displayName string) (*User, error) {
	query := `
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, role, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := db.QueryRow(query, email, passwordHash, displayName).Scan(&u.ID, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByEmail retrieves a user by email
func GetUserByEmail(db *sql.DB, email string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, role, avatar, avatar_mime, created_at, updated_at
		FROM users WHERE email = $1
	`
	var u User
	var mime sql.NullString
	err := db.QueryRow(query, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.AvatarMime = mime.String
	return &u, nil
}

// GetUserByID retrieves a user by UUID
func GetUserByID(db *sql.DB, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, role, avatar, avatar_mime, created_at, updated_at
		FROM users WHERE id = $1
	`
	var u User
	var mime sql.NullString
	err := db.QueryRow(query, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.AvatarMime = mime.String
	return &u, nil
}

// UpdateUserDisplayName updates the user's display name
func UpdateUserDisplayName(db *sql.DB, id, name string) error {
	query := `UPDATE users SET display_name = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, name, id)
	return err
}

// UpdateUserPassword updates the user's password hash
func UpdateUserPassword(db *sql.DB, id, newHash string) error {
	query := `UPDATE users SET password_hash = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`
	_, err := db.Exec(query, newHash, id)
	return err
}

// UpdateUserAvatar updates the user's avatar image bytes and mime type
func UpdateUserAvatar(db *sql.DB, id string, data []byte, mime string) error {
	query := `UPDATE users SET avatar = $1, avatar_mime = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $3`
	_, err := db.Exec(query, data, mime, id)
	return err
}

// DeleteUserAvatar clears the user's avatar image
func DeleteUserAvatar(db *sql.DB, id string) error {
	query := `UPDATE users SET avatar = NULL, avatar_mime = NULL, updated_at = CURRENT_TIMESTAMP WHERE id = $1`
	_, err := db.Exec(query, id)
	return err
}

// GetSetting retrieves the setting value for the given key, returning empty string if it does not exist
func GetSetting(db *sql.DB, key string) (string, error) {
	var val string
	query := `SELECT value FROM settings WHERE key = $1`
	err := db.QueryRow(query, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSetting sets or updates a setting key-value pair
func SetSetting(db *sql.DB, key, value string) error {
	query := `
		INSERT INTO settings (key, value, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE
		SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(query, key, value)
	return err
}

// StoreRefreshToken saves a hashed refresh token mapped to a user
func StoreRefreshToken(db *sql.DB, tokenHash string, userID string, expiresAt time.Time) error {
	query := `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	_, err := db.Exec(query, tokenHash, userID, expiresAt)
	return err
}

// DeleteRefreshToken removes a refresh token on logout
func DeleteRefreshToken(db *sql.DB, tokenHash string) error {
	query := `DELETE FROM refresh_tokens WHERE token_hash = $1`
	_, err := db.Exec(query, tokenHash)
	return err
}

// GetUserIDByRefreshToken validates a refresh token and returns the owner's userID
func GetUserIDByRefreshToken(db *sql.DB, tokenHash string) (string, error) {
	query := `
		SELECT user_id FROM refresh_tokens 
		WHERE token_hash = $1 AND expires_at > $2
	`
	var userID string
	err := db.QueryRow(query, tokenHash, time.Now()).Scan(&userID)
	if err != nil {
		return "", err
	}
	return userID, nil
}

// VerifyMigrationOwnership checks if a migration belongs to a specific user
func VerifyMigrationOwnership(db *sql.DB, migrationID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM migrations WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRow(query, migrationID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// DeleteMigrationCascade deletes a migration record (and task history via CASCADE)
func DeleteMigrationCascade(db *sql.DB, migrationID string) error {
	query := `DELETE FROM migrations WHERE id = $1`
	_, err := db.Exec(query, migrationID)
	return err
}

// OAuthTokenUpdate holds new token data for UpdateMigrationOAuthTokens.
type OAuthTokenUpdate struct {
	MigrationID           string
	Role                  string // "source" or "target"
	AccessTokenEncrypted  string
	RefreshTokenEncrypted string
	ExpiresAt             time.Time
}

// UpdateMigrationOAuthTokens atomically overwrites the access+refresh token pair
// for either the source or target credential of a migration.
// Implements the Token Rotation Constraint from PRD-12 (F-03): the old refresh
// token is overwritten in the same transaction that writes the new token pair.
func UpdateMigrationOAuthTokens(db *sql.DB, u OAuthTokenUpdate) error {
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

// ExpiringOAuthMigration is a lightweight row returned by GetExpiringOAuthMigrations.
type ExpiringOAuthMigration struct {
	MigrationID           string
	Role                  string // "source" or "target"
	Provider              string
	RefreshTokenEncrypted string
}

// GetExpiringOAuthMigrations returns all (migration_id, role, provider, refresh_token_encrypted)
// rows where the OAuth access token expires within the next 15 minutes and the migration
// is still active. Only rows that actually have a stored refresh token are returned.
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

// GetMigrationsForUser lists all migrations belonging to a specific user
func GetMigrationsForUser(db *sql.DB, userID string) ([]Migration, error) {
	query := `
		SELECT id, user_id, source_url, source_username, source_provider,
		       target_url, target_username, target_provider, status,
		       conflict_strategy, total_files, total_bytes, processed_files,
		       processed_bytes, skipped_files, failed_files, error_message,
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
			&m.ProcessedBytes, &m.SkippedFiles, &m.FailedFiles, &m.ErrorMessage,
			&m.CreatedAt, &m.UpdatedAt, &m.TargetDir, &m.Threads,
		)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
}

// CancelPendingTasks marks all pending tasks of a migration as CANCELLED
func CancelPendingTasks(db *sql.DB, migrationID string) error {
	query := `
		UPDATE tasks 
		SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status = 'PENDING'
	`
	_, err := db.Exec(query, migrationID)
	return err
}

// ResetFailedTasksForRetry resets failed tasks for a migration to PENDING, resets their attempts,
// and updates the migration status back to RUNNING, returning the number of reset tasks.
func ResetFailedTasksForRetry(db *sql.DB, migrationID string) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 1. Zähle FAILED Tasks und summiere ihre Dateigröße
	// In terminal states, status='FAILED' tasks are permanent failures and match failed_files.
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

	// 2. Setze diese Tasks zurück
	_, err = tx.Exec(`
		UPDATE tasks 
		SET status = 'PENDING', attempts = 0, next_retry_at = NULL, worker_hash = NULL, error_message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status = 'FAILED'
	`, migrationID)
	if err != nil {
		return 0, err
	}

	// 3. Passe die Migration an (nur in terminalem Zustand)
	res, err := tx.Exec(`
		UPDATE migrations 
		SET failed_files = failed_files - $1, 
		    processed_files = processed_files - $1, 
		    processed_bytes = processed_bytes - $2, 
		    status = 'RUNNING', 
		    error_message = NULL, 
		    updated_at = CURRENT_TIMESTAMP 
		WHERE id = $3 AND status IN ('COMPLETED', 'FAILED')
	`, count, bytesSum, migrationID)
	if err != nil {
		return 0, err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	// If no migration rows were updated (e.g. invalid state or ID), rollback
	if rowsAffected == 0 {
		return 0, nil
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return count, nil
}

// ============================================================================
// Core Scheduler Engine - Schedule Types and Functions
// ============================================================================

// Schedule represents a scheduled task (migration, sync, or backup)
type Schedule struct {
	ID             string         `json:"id"`
	UserID         string         `json:"user_id"`
	TaskType       string         `json:"task_type"` // migration, sync, backup
	TaskID         string         `json:"task_id"`
	CronExpression sql.NullString `json:"cron_expression"`
	RunAt          sql.NullTime   `json:"run_at"`
	NextRunAt      sql.NullTime   `json:"next_run_at"`
	IsActive       bool           `json:"is_active"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateSchedule inserts a new schedule and returns the UUID
func CreateSchedule(db *sql.DB, s *Schedule) (string, error) {
	query := `
		INSERT INTO schedules (
			user_id, task_type, task_id, cron_expression, run_at, next_run_at, is_active
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		s.UserID, s.TaskType, s.TaskID, s.CronExpression, s.RunAt, s.NextRunAt, s.IsActive,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)

	if err != nil {
		return "", err
	}
	return s.ID, nil
}

// GetSchedule retrieves a schedule by ID
func GetSchedule(db *sql.DB, id string) (*Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules WHERE id = $1
	`
	var s Schedule
	err := db.QueryRow(query, id).Scan(
		&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
		&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetSchedulesForUser lists all schedules belonging to a specific user
func GetSchedulesForUser(db *sql.DB, userID string) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		err := rows.Scan(
			&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
			&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}

// GetDueSchedules returns all active schedules where next_run_at <= NOW()
func GetDueSchedules(db *sql.DB) ([]Schedule, error) {
	query := `
		SELECT id, user_id, task_type, task_id, cron_expression, run_at, next_run_at,
		       is_active, created_at, updated_at
		FROM schedules
		WHERE is_active = TRUE
		  AND next_run_at <= NOW()
		ORDER BY next_run_at ASC
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		var s Schedule
		err := rows.Scan(
			&s.ID, &s.UserID, &s.TaskType, &s.TaskID, &s.CronExpression, &s.RunAt, &s.NextRunAt,
			&s.IsActive, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return schedules, nil
}

// UpdateNextRunAt updates the next_run_at timestamp for a schedule
func UpdateNextRunAt(db *sql.DB, id string, nextRunAt time.Time) error {
	query := `
		UPDATE schedules
		SET next_run_at = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, nextRunAt, id)
	return err
}

// DeactivateSchedule sets is_active = FALSE for a schedule
func DeactivateSchedule(db *sql.DB, id string) error {
	query := `
		UPDATE schedules
		SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := db.Exec(query, id)
	return err
}

// DeleteSchedule deletes a schedule by ID
func DeleteSchedule(db *sql.DB, id string) error {
	query := `DELETE FROM schedules WHERE id = $1`
	_, err := db.Exec(query, id)
	return err
}

// DeleteSchedulesForTask deletes all schedules for a specific task
func DeleteSchedulesForTask(db *sql.DB, taskType string, taskID string) error {
	query := `DELETE FROM schedules WHERE task_type = $1 AND task_id = $2`
	_, err := db.Exec(query, taskType, taskID)
	return err
}

// VerifyScheduleOwnership checks if a schedule belongs to a specific user
func VerifyScheduleOwnership(db *sql.DB, scheduleID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM schedules WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := db.QueryRow(query, scheduleID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// UpdateSchedule updates a schedule's cron_expression, run_at, next_run_at, and is_active
func UpdateSchedule(db *sql.DB, s *Schedule) error {
	query := `
		UPDATE schedules
		SET cron_expression = $1, run_at = $2, next_run_at = $3, is_active = $4,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $5
	`
	_, err := db.Exec(query, s.CronExpression, s.RunAt, s.NextRunAt, s.IsActive, s.ID)
	return err
}
