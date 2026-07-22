package db

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
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

// ValidRoles enumerates the roles a user may hold.
var ValidRoles = map[string]bool{
	"USER":  true,
	"ADMIN": true,
}

type queryExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if pqErr, ok := err.(*pq.Error); ok {
		return pqErr.Code == "23505"
	}
	return false
}

func dbHostFromConnStr(connStr string) string {
	if strings.HasPrefix(connStr, "postgres://") || strings.HasPrefix(connStr, "postgresql://") {
		if u, err := url.Parse(connStr); err == nil {
			return u.Hostname()
		}
		return ""
	}
	for _, part := range strings.Fields(connStr) {
		if strings.HasPrefix(part, "host=") {
			host := strings.TrimPrefix(part, "host=")
			if idx := strings.IndexAny(host, "/ "); idx >= 0 {
				host = host[:idx]
			}
			return host
		}
	}
	return ""
}

func isLocalOrPrivateHost(host string) bool {
	if host == "" {
		return false
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		if os.Getenv("ALLOW_UNRESOLVED_DB_HOST") == "1" {
			log.Printf("WARN: could not resolve DB host %q; treating as private per ALLOW_UNRESOLVED_DB_HOST", host)
			return true
		}
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return false
		}
	}
	return true
}

// InitDB initializes the database connection with startup retries and schema DDL setup.
func InitDB(connStr string) (*sql.DB, error) {
	if host := dbHostFromConnStr(connStr); !isLocalOrPrivateHost(host) && strings.Contains(connStr, "postgres:postgres@") {
		return nil, fmt.Errorf("insecure DATABASE_URL: the default 'postgres:postgres' credentials are only permitted for a localhost or private-network database. Set DB_PASSWORD to a strong, unique password for any publicly-reachable deployment.")
	}

	var db *sql.DB
	var err error
	var pingErr error

	for attempt := 1; attempt <= 10; attempt++ {
		db, err = sql.Open("postgres", connStr)
		if err != nil {
			log.Printf("Attempt %d: Failed to open connection to PostgreSQL database: %v\n", attempt, err)
			time.Sleep(2 * time.Second)
			continue
		}

		pingErr = db.Ping()
		if pingErr == nil {
			// Apply inline schema DDL migrations on startup
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				email VARCHAR(255) UNIQUE NOT NULL,
				password_hash VARCHAR(255) NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (users): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS display_name VARCHAR(255) NOT NULL DEFAULT ''`)
			if err != nil {
				log.Printf("Failed schema migration (display_name): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS role VARCHAR(32) NOT NULL DEFAULT 'USER'`)
			if err != nil {
				log.Printf("Failed schema migration (role): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE`)
			if err != nil {
				log.Printf("Failed schema migration (active): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (must_change_password): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar BYTEA`)
			if err != nil {
				log.Printf("Failed schema migration (avatar): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_mime VARCHAR(64)`)
			if err != nil {
				log.Printf("Failed schema migration (avatar_mime): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (totp_enabled): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret_enc TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (totp_secret_enc): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_backup_codes JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (totp_backup_codes): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_failed_attempts INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (totp_failed_attempts): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_locked_until TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (totp_locked_until): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS login_failed_attempts INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (login_failed_attempts): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS login_locked_until TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (login_locked_until): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS user_smtp_settings (
				user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
				smtp_host VARCHAR(255) NOT NULL,
				smtp_port INT NOT NULL DEFAULT 587,
				smtp_username VARCHAR(255) NOT NULL DEFAULT '',
				smtp_password_encrypted TEXT NOT NULL DEFAULT '',
				smtp_from_email VARCHAR(255) NOT NULL DEFAULT '',
				smtp_from_name VARCHAR(255) NOT NULL DEFAULT '',
				smtp_encryption VARCHAR(16) NOT NULL DEFAULT 'tls',
				notify_on_completion BOOLEAN NOT NULL DEFAULT TRUE,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (user_smtp_settings): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS refresh_tokens (
				token_hash VARCHAR(64) PRIMARY KEY,
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (refresh_tokens): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS password_reset_tokens (
				token_hash VARCHAR(64) PRIMARY KEY,
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
				used BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (password_reset_tokens): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS email_change_tokens (
				token_hash VARCHAR(64) PRIMARY KEY,
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				new_email VARCHAR(255) NOT NULL,
				expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
				used BOOLEAN NOT NULL DEFAULT FALSE,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (email_change_tokens): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
				id BIGSERIAL PRIMARY KEY,
				user_id UUID REFERENCES users(id) ON DELETE SET NULL,
				action VARCHAR(64) NOT NULL,
				target VARCHAR(255) NOT NULL DEFAULT '',
				ip VARCHAR(64) NOT NULL DEFAULT '',
				details JSONB,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (audit_log): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_created ON audit_log(created_at DESC)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_audit_log_created): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID REFERENCES users(id) ON DELETE CASCADE,
				source_url VARCHAR(512),
				source_username VARCHAR(255),
				source_password_encrypted TEXT,
				source_provider VARCHAR(64) NOT NULL DEFAULT 'nextcloud',
				target_url VARCHAR(512),
				target_username VARCHAR(255),
				target_password_encrypted TEXT,
				target_provider VARCHAR(64) NOT NULL DEFAULT 'nextcloud',
				target_dir VARCHAR(512) NOT NULL DEFAULT '/',
				status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
				conflict_strategy VARCHAR(32) NOT NULL DEFAULT 'SKIP',
				total_files INT NOT NULL DEFAULT 0,
				total_bytes BIGINT NOT NULL DEFAULT 0,
				processed_files INT NOT NULL DEFAULT 0,
				processed_bytes BIGINT NOT NULL DEFAULT 0,
				live_bytes BIGINT NOT NULL DEFAULT 0,
				skipped_files INT NOT NULL DEFAULT 0,
				failed_files INT NOT NULL DEFAULT 0,
				threads INT NOT NULL DEFAULT 4,
				bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
				email_sent BOOLEAN NOT NULL DEFAULT FALSE,
				error_message TEXT,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (migrations): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_refresh_token_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (source_refresh_token_encrypted): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_token_expires_at TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (source_token_expires_at): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_refresh_token_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (target_refresh_token_encrypted): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_token_expires_at TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (target_token_expires_at): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS picker_session_id TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (picker_session_id): %v\n", err)
			}

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

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS schedules (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				task_type VARCHAR(32) NOT NULL DEFAULT 'migration',
				task_id UUID NOT NULL,
				cron_expression VARCHAR(64),
				run_at TIMESTAMP WITH TIME ZONE,
				next_run_at TIMESTAMP WITH TIME ZONE,
				is_active BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (schedules): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_schedules_next_run ON schedules(next_run_at) WHERE is_active = TRUE`)
			if err != nil {
				log.Printf("Failed schema migration (idx_schedules_next_run): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS connection_profiles (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name VARCHAR(255) NOT NULL,
				provider VARCHAR(64) NOT NULL,
				url VARCHAR(512) NOT NULL DEFAULT '',
				username VARCHAR(255) NOT NULL DEFAULT '',
				password_encrypted TEXT NOT NULL DEFAULT '',
				refresh_token_encrypted TEXT NOT NULL DEFAULT '',
				token_expires_at TIMESTAMP WITH TIME ZONE,
				oauth_user VARCHAR(255) NOT NULL DEFAULT '',
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (user_id, name)
			)`)
			if err != nil {
				log.Printf("Failed schema migration (connection_profiles): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_connection_profiles_user ON connection_profiles(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_connection_profiles_user): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				migration_id UUID REFERENCES migrations(id) ON DELETE CASCADE,
				file_path TEXT NOT NULL,
				file_size BIGINT NOT NULL DEFAULT 0,
				status VARCHAR(32) NOT NULL DEFAULT 'PENDING',
				attempts INT NOT NULL DEFAULT 0,
				error_message TEXT,
				next_retry_at TIMESTAMP WITH TIME ZONE,
				worker_hash VARCHAR(64),
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (tasks): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS resource_type VARCHAR(32) NOT NULL DEFAULT 'files'`)
			if err != nil {
				log.Printf("Failed schema migration (tasks resource_type): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS metadata JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (tasks metadata): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE`)
			if err != nil {
				log.Printf("Failed schema migration (tasks sync_job_id): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS checksum_verified BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (tasks checksum_verified): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS indexing_errors (
				id BIGSERIAL PRIMARY KEY,
				migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
				resource_type VARCHAR(32) NOT NULL DEFAULT 'files',
				path TEXT NOT NULL,
				error_message TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (indexing_errors): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS settings (
				key VARCHAR(128) PRIMARY KEY,
				value TEXT NOT NULL DEFAULT '',
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (settings): %v\n", err)
			}

			_, err = db.Exec(`
				DO $$
				BEGIN
					IF NOT EXISTS (
						SELECT 1 FROM pg_constraint WHERE conname = 'chk_task_job_type'
					) THEN
						ALTER TABLE tasks ADD CONSTRAINT chk_task_job_type
							CHECK (
								(migration_id IS NOT NULL AND sync_job_id IS NULL) OR
								(migration_id IS NULL AND sync_job_id IS NOT NULL)
							);
					END IF;
				END $$;
			`)
			if err != nil {
				log.Printf("Failed schema migration (chk_task_job_type constraint): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_sync_status ON tasks(sync_job_id, status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_sync_status): %v\n", err)
			}

			maxConns := 50
			if envVal := os.Getenv("MAX_THREADS"); envVal != "" {
				if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
					maxConns = val * 2
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
