package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
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

// queryExecerContext is implemented by both *sql.DB and *sql.Tx. It keeps
// shared helpers cancellation-aware without forcing them to depend on a
// particular database handle.
type queryExecerContext interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

// schemaErrorLogger records a failed bootstrap DDL statement while preserving
// the existing diagnostic log. InitDB checks the recorded error before handing
// the database to callers, so a partially applied schema never starts a usable
// service.
type schemaErrorLogger struct {
	logger *log.Logger
	err    *error
}

func (l schemaErrorLogger) Printf(format string, v ...interface{}) {
	l.logger.Printf(format, v...)
	// This logger is used only for bootstrap migration failures. Capture the
	// first error independently of the log message wording so changing a
	// diagnostic string can never let InitDB continue with partial DDL.
	if *l.err == nil {
		*l.err = fmt.Errorf("%s", strings.TrimSpace(fmt.Sprintf(format, v...)))
	}
}

func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
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
			// A session-scoped advisory lock must stay on one dedicated connection.
			// Calling db.Exec for lock/unlock can use different pool connections,
			// leaving the lock held after startup or failing to serialize DDL.
			lockConn, lockErr := db.Conn(context.Background())
			if lockErr != nil {
				db.Close()
				return nil, fmt.Errorf("acquire schema migration connection: %w", lockErr)
			}
			lockHeld := false
			lockClosed := false
			releaseLock := func() {
				if lockClosed {
					return
				}
				if lockHeld {
					if _, err := lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(84736291)`); err != nil {
						log.Printf("Failed to release schema migration advisory lock: %v", err)
					}
					lockHeld = false
				}
				if err := lockConn.Close(); err != nil {
					log.Printf("Failed to close schema migration connection: %v", err)
				}
				lockClosed = true
			}
			if _, lockErr = lockConn.ExecContext(context.Background(), `SELECT pg_advisory_lock(84736291)`); lockErr != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("acquire schema migration advisory lock: %w", lockErr)
			}
			lockHeld = true
			defer releaseLock()

			// Apply inline schema DDL migrations on startup. Shadow the package
			// logger in this scope so every existing migration failure is retained
			// and can fail startup after the DDL sequence completes.
			var schemaErr error
			log := schemaErrorLogger{logger: log.Default(), err: &schemaErr}

			_, err = db.Exec(`CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql'`)
			if err != nil {
				log.Printf("Failed schema migration (update_updated_at_column): %v\n", err)
			}

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

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS language VARCHAR(8) NOT NULL DEFAULT 'en'`)
			if err != nil {
				log.Printf("Failed schema migration (user language): %v\n", err)
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

			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (last_login_at): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_last_login_at ON users(last_login_at)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_users_last_login_at): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS instance_smtp_settings (
				id SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
				smtp_host VARCHAR(255) NOT NULL,
				smtp_port INT NOT NULL DEFAULT 587,
				smtp_username VARCHAR(255) NOT NULL DEFAULT '',
				smtp_password_encrypted TEXT NOT NULL DEFAULT '',
				smtp_from_email VARCHAR(255) NOT NULL DEFAULT '',
				smtp_from_name VARCHAR(255) NOT NULL DEFAULT '',
				smtp_encryption VARCHAR(16) NOT NULL DEFAULT 'tls' CHECK (smtp_encryption IN ('tls', 'starttls')),
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (instance_smtp_settings): %v\n", err)
			}
			_, err = db.Exec(`DROP TRIGGER IF EXISTS update_instance_smtp_settings_updated_at ON instance_smtp_settings;
				CREATE TRIGGER update_instance_smtp_settings_updated_at BEFORE UPDATE ON instance_smtp_settings FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`)
			if err != nil {
				log.Printf("Failed schema migration (instance_smtp_settings trigger): %v\n", err)
			}

			// Administrator-managed OAuth2 client credentials. The provider whitelist is
			// enforced in Go (oauth.IsProvider), not by a CHECK constraint.
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS instance_oauth_providers (
				provider VARCHAR(32) PRIMARY KEY,
				client_id TEXT NOT NULL,
				client_secret_encrypted TEXT NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (instance_oauth_providers): %v\n", err)
			}
			_, err = db.Exec(`DROP TRIGGER IF EXISTS update_instance_oauth_providers_updated_at ON instance_oauth_providers;
				CREATE TRIGGER update_instance_oauth_providers_updated_at BEFORE UPDATE ON instance_oauth_providers FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`)
			if err != nil {
				log.Printf("Failed schema migration (instance_oauth_providers trigger): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS refresh_tokens (
				token_hash VARCHAR(64) PRIMARY KEY,
				id UUID NOT NULL DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				user_agent TEXT NOT NULL DEFAULT '',
				expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (refresh_tokens): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS id UUID DEFAULT gen_random_uuid();
				ALTER TABLE refresh_tokens ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '';
				UPDATE refresh_tokens SET id = gen_random_uuid() WHERE id IS NULL;
				ALTER TABLE refresh_tokens ALTER COLUMN id SET NOT NULL;
				CREATE UNIQUE INDEX IF NOT EXISTS idx_refresh_tokens_id ON refresh_tokens(id);
				CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_expires_at ON refresh_tokens(user_id, expires_at DESC)`)
			if err != nil {
				log.Printf("Failed schema migration (refresh_tokens session metadata): %v\n", err)
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
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_audit_log_action): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_user_id ON audit_log(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_audit_log_user_id): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE audit_log ALTER COLUMN target SET DEFAULT '', ALTER COLUMN ip SET DEFAULT ''`)
			if err != nil {
				log.Printf("Failed schema migration (audit_log column defaults): %v\n", err)
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
				conflict_strategy VARCHAR(32) NOT NULL DEFAULT 'SKIP' CONSTRAINT chk_migrations_conflict_strategy CHECK (conflict_strategy IN ('SKIP', 'OVERWRITE', 'RENAME')),
				total_files INT NOT NULL DEFAULT 0,
				total_bytes BIGINT NOT NULL DEFAULT 0,
				processed_files INT NOT NULL DEFAULT 0,
				processed_bytes BIGINT NOT NULL DEFAULT 0,
				live_bytes BIGINT NOT NULL DEFAULT 0,
				skipped_files INT NOT NULL DEFAULT 0,
				failed_files INT NOT NULL DEFAULT 0,
				threads INT NOT NULL DEFAULT 8,
				bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
				email_sent BOOLEAN NOT NULL DEFAULT FALSE,
				error_message TEXT,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (migrations): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ALTER COLUMN source_url SET DEFAULT '', ALTER COLUMN source_username SET DEFAULT '', ALTER COLUMN source_password_encrypted SET DEFAULT ''`)
			if err != nil {
				log.Printf("Failed schema migration (migrations column defaults): %v\n", err)
			}

			// Repair legacy invalid values before adding the constraint. This makes
			// the startup migration safe for databases created before validation.
			_, err = db.Exec(`UPDATE migrations SET conflict_strategy = 'SKIP' WHERE conflict_strategy IS NULL OR conflict_strategy NOT IN ('SKIP', 'OVERWRITE', 'RENAME')`)
			if err != nil {
				log.Printf("Failed data migration (migration conflict strategy): %v\n", err)
			}
			_, err = db.Exec(`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'migrations'::regclass AND conname = 'chk_migrations_conflict_strategy') THEN
					ALTER TABLE migrations ADD CONSTRAINT chk_migrations_conflict_strategy CHECK (conflict_strategy IN ('SKIP', 'OVERWRITE', 'RENAME'));
				END IF;
			END $$`)
			if err != nil {
				// schemaErrorLogger records this as schemaErr, causing InitDB to
				// close the connection and fail startup below.
				log.Printf("Failed schema migration (migration conflict strategy constraint): %v\n", err)
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
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS source_mega_master_key_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_master_key_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (migrations MEGA sessions): %v\n", err)
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

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS verification_generation INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (migrations verification_generation): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS verification_lease_until TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (migrations verification_lease_until): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS failed_retry_done BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (migrations failed_retry_done): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_migrations_status ON migrations(status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_migrations_status): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_migrations_user_id ON migrations(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_migrations_user_id): %v\n", err)
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

			// Add task_type CHECK constraint idempotently (missing from the original CREATE TABLE).
			_, err = db.Exec(`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'schedules'::regclass AND conname = 'chk_schedules_task_type') THEN
					ALTER TABLE schedules ADD CONSTRAINT chk_schedules_task_type
						CHECK (task_type IN ('migration', 'sync', 'backup'));
				END IF;
			END $$`)
			if err != nil {
				log.Printf("Failed schema migration (schedules task_type constraint): %v\n", err)
			}

			// Sync schedules are duration-based. Clear legacy cron expressions so
			// consumers can reliably distinguish them from cron-based schedules.
			_, err = db.Exec(`UPDATE schedules SET cron_expression = NULL
				WHERE task_type = 'sync' AND cron_expression IS NOT NULL`)
			if err != nil {
				log.Printf("Failed data migration (clear sync cron expressions): %v\n", err)
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

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_profiles_user ON connection_profiles(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_conn_profiles_user): %v\n", err)
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration idx_conn_profiles_user: %w", err)
			}
			// The previous inline migration used a different name for the same
			// index. Keep one canonical index to avoid duplicate storage.
			_, err = db.Exec(`DROP INDEX IF EXISTS idx_connection_profiles_user`)
			if err != nil {
				log.Printf("Failed schema migration (drop idx_connection_profiles_user): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE connection_profiles ALTER COLUMN url SET DEFAULT '', ALTER COLUMN username SET DEFAULT '', ALTER COLUMN password_encrypted SET DEFAULT '', ALTER COLUMN refresh_token_encrypted SET DEFAULT ''`)
			if err != nil {
				log.Printf("Failed schema migration (connection_profiles column defaults): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE connection_profiles ADD COLUMN IF NOT EXISTS mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS mega_master_key_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (connection_profiles MEGA sessions): %v\n", err)
			}
			// connection_profiles is initialized after migrations. Keep these foreign
			// keys here so fresh bootstraps and legacy upgrades both see the referenced
			// table before the constraints are added.
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL`)
			if err != nil {
				log.Printf("Failed schema migration (migration profile references): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_migrations_source_profile_id ON migrations(source_profile_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_migrations_source_profile_id): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_migrations_target_profile_id ON migrations(target_profile_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_migrations_target_profile_id): %v\n", err)
			}

			// Keep this bootstrap DDL in sync with db/schema.sql. It must precede
			// the tasks.sync_job_id foreign key below.
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sync_jobs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
				target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
				source_url TEXT NOT NULL,
				source_username TEXT NOT NULL,
				source_password_encrypted TEXT NOT NULL,
				source_refresh_token_encrypted TEXT,
				source_token_expires_at TIMESTAMP WITH TIME ZONE,
				target_url TEXT NOT NULL,
				target_username TEXT NOT NULL,
				target_password_encrypted TEXT NOT NULL,
				target_refresh_token_encrypted TEXT,
				target_token_expires_at TIMESTAMP WITH TIME ZONE,
				source_provider TEXT NOT NULL DEFAULT 'nextcloud',
				target_provider TEXT NOT NULL DEFAULT 'nextcloud',
				direction TEXT NOT NULL DEFAULT 'one_way' CHECK (direction IN ('one_way', 'two_way')),
				conflict_strategy TEXT NOT NULL DEFAULT 'OVERWRITE' CHECK (conflict_strategy IN ('OVERWRITE', 'SKIP', 'RENAME')),
				delete_propagation BOOLEAN NOT NULL DEFAULT FALSE,
				interval_minutes INT NOT NULL DEFAULT 15,
				threads INT NOT NULL DEFAULT 8,
				bandwidth_limit_mbps INT NOT NULL DEFAULT 0,
				status TEXT NOT NULL DEFAULT 'IDLE',
				run_generation INT NOT NULL DEFAULT 0,
				verification_generation INT NOT NULL DEFAULT 0,
				verification_lease_until TIMESTAMP WITH TIME ZONE,
				target_dir TEXT NOT NULL DEFAULT '/',
				selected_paths JSONB,
				last_run_at TIMESTAMP WITH TIME ZONE,
				last_run_status TEXT,
				error_message TEXT,
				total_files INT NOT NULL DEFAULT 0,
				total_bytes BIGINT NOT NULL DEFAULT 0,
				processed_files INT NOT NULL DEFAULT 0,
				processed_bytes BIGINT NOT NULL DEFAULT 0,
				live_bytes BIGINT NOT NULL DEFAULT 0,
				changed_files INT NOT NULL DEFAULT 0,
				deleted_files INT NOT NULL DEFAULT 0,
				failed_files INT NOT NULL DEFAULT 0,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration sync_jobs: %w", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS bandwidth_limit_mbps INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs bandwidth_limit_mbps): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS run_generation INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs run_generation): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS verification_generation INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS verification_lease_until TIMESTAMP WITH TIME ZONE`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs verification lease): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS source_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS source_mega_master_key_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_master_key_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs MEGA sessions): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL, ADD COLUMN IF NOT EXISTS target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL`)
			if err != nil {
				log.Printf("Failed schema migration (sync job profile references): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_user_id ON sync_jobs(user_id)`)
			if err != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration idx_sync_jobs_user_id: %w", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_status ON sync_jobs(status)`)
			if err != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration idx_sync_jobs_status: %w", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_source_profile_id ON sync_jobs(source_profile_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_sync_jobs_source_profile_id): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_target_profile_id ON sync_jobs(target_profile_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_sync_jobs_target_profile_id): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS sync_state (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				sync_job_id UUID NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
				side TEXT NOT NULL CHECK (side IN ('source', 'target')),
				rel_path TEXT NOT NULL,
				size BIGINT NOT NULL DEFAULT 0,
				mtime TIMESTAMP WITH TIME ZONE,
				source_hash TEXT,
				target_hash TEXT,
				etag TEXT,
				UNIQUE (sync_job_id, side, rel_path)
			)`)
			if err != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration sync_state: %w", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_state_job ON sync_state(sync_job_id, side)`)
			if err != nil {
				releaseLock()
				db.Close()
				return nil, fmt.Errorf("schema migration idx_sync_state_job: %w", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				migration_id UUID REFERENCES migrations(id) ON DELETE CASCADE,
				file_path TEXT NOT NULL,
				file_size BIGINT NOT NULL DEFAULT 0,
				source_hash TEXT,
				target_hash TEXT,
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
			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS source_hash TEXT, ADD COLUMN IF NOT EXISTS target_hash TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (tasks hashes): %v\n", err)
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

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS claim_epoch BIGINT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (tasks claim_epoch): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS pass_generation INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (tasks pass_generation): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_migration_id ON tasks(migration_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_migration_id): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_status): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_migration_status ON tasks(migration_id, status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_migration_status): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_retry ON tasks(status, next_retry_at) WHERE status = 'FAILED' AND next_retry_at IS NOT NULL`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_retry): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_pending ON tasks(status, created_at) WHERE status = 'PENDING'`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_pending): %v\n", err)
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
			_, err = db.Exec(`DO $$ BEGIN
				IF EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'indexing_errors' AND column_name = 'id' AND data_type = 'uuid'
				) THEN
					ALTER TABLE indexing_errors DROP COLUMN id;
					ALTER TABLE indexing_errors ADD COLUMN id BIGSERIAL PRIMARY KEY;
				END IF;
			END $$`)
			if err != nil {
				log.Printf("Failed schema migration (indexing_errors id type migration): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_indexing_errors_migration_id ON indexing_errors(migration_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_indexing_errors_migration_id): %v\n", err)
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

			_, err = db.Exec(`DROP INDEX IF EXISTS idx_tasks_sync_status`)
			if err != nil {
				log.Printf("Failed schema migration (drop idx_tasks_sync_status): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_sync_gen_status ON tasks(sync_job_id, pass_generation, status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_sync_gen_status): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_wait_conflict_copy ON tasks ((metadata->>'wait_for_conflict_copy')) WHERE status = 'PENDING' AND metadata->>'wait_for_conflict_copy' = 'true'`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_wait_conflict_copy): %v\n", err)
			}

			// Notification tables intentionally follow migrations and sync_jobs because
			// their event foreign keys reference both tables.
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS notification_channels (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				type TEXT NOT NULL CHECK (type IN ('email','gotify','ntfy','telegram','discord')), enabled BOOLEAN NOT NULL DEFAULT FALSE,
				config_encrypted TEXT NOT NULL, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (user_id, type))`)
			if err != nil {
				log.Printf("Failed schema migration (notification_channels): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_channels_user ON notification_channels(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_notification_channels_user): %v\n", err)
			}
			var legacySMTPTabExists bool
			err = db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'user_smtp_settings')`).Scan(&legacySMTPTabExists)
			if err != nil {
				log.Printf("Failed legacy SMTP migration check: %v\n", err)
			} else if legacySMTPTabExists {
				if _, err = db.Exec(`UPDATE notification_channels SET config_encrypted = '' WHERE type = 'email'`); err != nil {
					log.Printf("Failed email notification migration: %v\n", err)
				} else if _, err = db.Exec(`DROP TABLE user_smtp_settings`); err != nil {
					log.Printf("Failed schema cleanup (user_smtp_settings): %v\n", err)
				}
			}
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS notification_events (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				kind TEXT NOT NULL CHECK (kind IN ('migration','sync','restore','backup')), migration_id UUID REFERENCES migrations(id) ON DELETE CASCADE, run_generation INT NOT NULL DEFAULT 0,
				sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE, restore_run_id UUID REFERENCES restore_runs(id) ON DELETE CASCADE, backup_run_id UUID REFERENCES backup_runs(id) ON DELETE CASCADE, run_at TIMESTAMP WITH TIME ZONE NOT NULL, payload JSONB NOT NULL,
				created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK (
					(kind = 'migration' AND migration_id IS NOT NULL AND sync_job_id IS NULL AND restore_run_id IS NULL AND backup_run_id IS NULL) OR
					(kind = 'sync'      AND sync_job_id  IS NOT NULL AND migration_id IS NULL AND restore_run_id IS NULL AND backup_run_id IS NULL) OR
					(kind = 'restore'   AND restore_run_id IS NOT NULL AND migration_id IS NULL AND sync_job_id IS NULL AND backup_run_id IS NULL) OR
					(kind = 'backup'    AND backup_run_id IS NOT NULL AND migration_id IS NULL AND sync_job_id IS NULL AND restore_run_id IS NULL)
				),
				UNIQUE (sync_job_id, run_at))`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS notification_generation INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (migrations notification_generation): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS run_generation INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events run_generation): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_migration_id_key`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events old migration uniqueness): %v\n", err)
			}
			_, err = db.Exec(`DROP INDEX IF EXISTS idx_notification_events_migration_generation; DROP INDEX IF EXISTS notification_events_migration_generation;`)
			if err != nil {
				log.Printf("Failed schema migration (drop legacy notification_events indexes): %v\n", err)
			}
			// Partial unique indexes replace the old table-level UNIQUE (sync_job_id, run_at).
			// NULL != NULL in SQL unique constraints, so the table constraint was toothless
			// for migration-kind rows where sync_job_id IS NULL. Partial indexes are precise.
			_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_migration_uniq ON notification_events(migration_id, run_generation) WHERE migration_id IS NOT NULL`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events migration uniqueness): %v\n", err)
			}
			_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_sync_uniq ON notification_events(sync_job_id, run_at) WHERE sync_job_id IS NOT NULL`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events sync uniqueness): %v\n", err)
			}
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS notification_deliveries (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), event_id UUID NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
				channel_type TEXT NOT NULL CHECK (channel_type IN ('email','gotify','ntfy','telegram','discord')), config_encrypted TEXT NOT NULL,
				state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','SENT','FAILED')), attempts INT NOT NULL DEFAULT 0,
				next_retry_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, last_error_code TEXT, sent_at TIMESTAMP WITH TIME ZONE,
				created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (event_id, channel_type))`)
			if err != nil {
				log.Printf("Failed schema migration (notification_deliveries): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_notification_deliveries_pending ON notification_deliveries(state, next_retry_at)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_notification_deliveries_pending): %v\n", err)
			}

			// Keep this backup repository catalog DDL in sync with db/schema.sql.
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_jobs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				lock_id BIGSERIAL UNIQUE NOT NULL, source_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL, target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
				source_url TEXT NOT NULL, source_username TEXT NOT NULL, source_password_encrypted TEXT NOT NULL, source_refresh_token_encrypted TEXT, source_token_expires_at TIMESTAMP WITH TIME ZONE, source_mega_session_id_encrypted TEXT, source_mega_master_key_encrypted TEXT,
				target_url TEXT NOT NULL, target_username TEXT NOT NULL, target_password_encrypted TEXT NOT NULL, target_refresh_token_encrypted TEXT, target_token_expires_at TIMESTAMP WITH TIME ZONE, target_mega_session_id_encrypted TEXT, target_mega_master_key_encrypted TEXT,
				source_provider TEXT NOT NULL, target_provider TEXT NOT NULL, selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb, target_dir TEXT NOT NULL,
				repository_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE, repository_root TEXT NOT NULL, cron_expression TEXT NOT NULL, timezone TEXT NOT NULL,
				retention_count INT NOT NULL DEFAULT 30 CHECK (retention_count >= 1), threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16),
				status TEXT NOT NULL DEFAULT 'IDLE' CHECK (status IN ('IDLE','QUEUED','SCANNING','RUNNING','VERIFYING','PAUSED','FAILED','DELETING')), run_generation INT NOT NULL DEFAULT 0 CHECK (run_generation >= 0),
				last_run_at TIMESTAMP WITH TIME ZONE, last_run_status TEXT, total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0), total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0), processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0), processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0), deduplicated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (deduplicated_bytes >= 0), failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0), error_code TEXT,
				deletion_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (deletion_state IN ('ACTIVE','REQUESTED','DELETING','DELETED')), created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_jobs): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_jobs_user_created ON backup_jobs(user_id, created_at DESC); CREATE INDEX IF NOT EXISTS idx_backup_jobs_status ON backup_jobs(status)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_jobs indexes): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_runs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE, generation INT NOT NULL CHECK (generation > 0),
				trigger TEXT NOT NULL CHECK (trigger IN ('manual','schedule','catch_up')), scheduled_local_key TEXT,
				state TEXT NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','SCANNING','RUNNING','VERIFYING','COMPLETED','PARTIAL','FAILED','CANCELLED')),
				total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0), total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0), processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0), processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0), deduplicated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (deduplicated_bytes >= 0), failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0), error_code TEXT,
				started_at TIMESTAMP WITH TIME ZONE, finished_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (backup_job_id, generation), UNIQUE (backup_job_id, id)
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_runs): %v\n", err)
			}
			_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_runs_scheduled_local_key ON backup_runs(backup_job_id, scheduled_local_key) WHERE scheduled_local_key IS NOT NULL; CREATE INDEX IF NOT EXISTS idx_backup_runs_queued ON backup_runs(created_at) WHERE state = 'QUEUED'; CREATE INDEX IF NOT EXISTS idx_backup_runs_job_created ON backup_runs(backup_job_id, created_at DESC)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_runs indexes): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_snapshots (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE, backup_run_id UUID NOT NULL,
				state TEXT NOT NULL DEFAULT 'PUBLISHING' CHECK (state IN ('PUBLISHING','READY','PARTIAL','EXPIRED','DELETING','DAMAGED')), selected_roots JSONB NOT NULL DEFAULT '[]'::jsonb,
				total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0), total_dirs INT NOT NULL DEFAULT 0 CHECK (total_dirs >= 0), total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0), omitted_unstable_count INT NOT NULL DEFAULT 0 CHECK (omitted_unstable_count >= 0), omitted_error_count INT NOT NULL DEFAULT 0 CHECK (omitted_error_count >= 0), integrity_state TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (integrity_state IN ('UNKNOWN','VALID','DAMAGED')),
				expires_at TIMESTAMP WITH TIME ZONE, deletion_started_at TIMESTAMP WITH TIME ZONE, deleted_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (backup_job_id, backup_run_id), FOREIGN KEY (backup_job_id, backup_run_id) REFERENCES backup_runs(backup_job_id, id) ON DELETE CASCADE
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshots): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_snapshots_job_created ON backup_snapshots(backup_job_id, created_at DESC); CREATE INDEX IF NOT EXISTS idx_backup_snapshots_visible ON backup_snapshots(backup_job_id, created_at DESC) WHERE state IN ('READY','PARTIAL','DAMAGED')`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshots indexes): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_packs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE, remote_rel_path TEXT NOT NULL, sha256 BYTEA NOT NULL CHECK (octet_length(sha256) = 32), size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
				state TEXT NOT NULL DEFAULT 'UPLOADING' CHECK (state IN ('UPLOADING','READY','REPLACING','DELETE_PENDING','DELETED','DAMAGED')), generation INT NOT NULL DEFAULT 0 CHECK (generation >= 0), last_checked_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (backup_job_id, id), UNIQUE (backup_job_id, remote_rel_path), UNIQUE (backup_job_id, sha256)
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_packs): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_packs_job_state ON backup_packs(backup_job_id, state)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_packs index): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_blocks (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_job_id UUID NOT NULL, sha256 BYTEA NOT NULL CHECK (octet_length(sha256) = 32), plaintext_size INT NOT NULL CHECK (plaintext_size BETWEEN 1 AND 4194304), backup_pack_id UUID NOT NULL, payload_offset BIGINT NOT NULL CHECK (payload_offset >= 0), payload_length INT NOT NULL CHECK (payload_length BETWEEN 1 AND 4194304), created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (backup_job_id, sha256), UNIQUE (backup_job_id, id), FOREIGN KEY (backup_job_id, backup_pack_id) REFERENCES backup_packs(backup_job_id, id) ON DELETE RESTRICT
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_blocks): %v\n", err)
			}
			// v1 payloads match plaintext, but future compressed formats may not.
			_, err = db.Exec(`ALTER TABLE backup_blocks DROP CONSTRAINT IF EXISTS backup_blocks_check`)
			if err != nil {
				log.Printf("Failed schema migration (backup_blocks payload constraint): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_blocks_job_pack ON backup_blocks(backup_job_id, backup_pack_id)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_blocks index): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_snapshot_items (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_snapshot_id UUID NOT NULL REFERENCES backup_snapshots(id) ON DELETE CASCADE, relative_path TEXT NOT NULL, is_dir BOOLEAN NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0), mtime TIMESTAMP WITH TIME ZONE, metadata JSONB, file_sha256 BYTEA CHECK (file_sha256 IS NULL OR octet_length(file_sha256) = 32), state TEXT NOT NULL DEFAULT 'AVAILABLE' CHECK (state IN ('AVAILABLE','UNSTABLE','ERROR')), error_code TEXT, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (backup_snapshot_id, relative_path), CHECK ((is_dir AND size_bytes = 0 AND file_sha256 IS NULL) OR (NOT is_dir AND file_sha256 IS NOT NULL))
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshot_items): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_snapshot_items_snapshot ON backup_snapshot_items(backup_snapshot_id, relative_path)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshot_items index): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_snapshot_item_blocks (
				backup_snapshot_item_id UUID NOT NULL REFERENCES backup_snapshot_items(id) ON DELETE CASCADE, ordinal INT NOT NULL CHECK (ordinal >= 0), backup_block_id UUID NOT NULL REFERENCES backup_blocks(id) ON DELETE RESTRICT,
				PRIMARY KEY (backup_snapshot_item_id, ordinal)
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshot_item_blocks): %v\n", err)
			}
			// A content-addressed block may occur at several offsets within the
			// same file. Ordinal is the only uniqueness requirement here.
			_, err = db.Exec(`ALTER TABLE backup_snapshot_item_blocks DROP CONSTRAINT IF EXISTS backup_snapshot_item_blocks_backup_snapshot_item_id_backup_block_id_key`)
			if err != nil {
				log.Printf("Failed schema migration (backup snapshot repeated blocks): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_snapshot_item_blocks_block ON backup_snapshot_item_blocks(backup_block_id)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_snapshot_item_blocks index): %v\n", err)
			}

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS backup_maintenance (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE, kind TEXT NOT NULL CHECK (kind IN ('RETENTION','COMPACTION','DELETE_REPOSITORY','VERIFY')), state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','RETRY_WAIT','CANCELLING','CANCELLED','COMPLETED','FAILED')), byte_budget BIGINT CHECK (byte_budget IS NULL OR byte_budget > 0), claim_deadline TIMESTAMP WITH TIME ZONE, cursor JSONB NOT NULL DEFAULT '{}'::jsonb, attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0), next_retry_at TIMESTAMP WITH TIME ZONE, error_code TEXT, verify_mode TEXT CHECK (verify_mode IS NULL OR verify_mode IN ('METADATA','BUDGETED','FULL')), processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0), total_packs INT NOT NULL DEFAULT 0 CHECK (total_packs >= 0), checked_packs INT NOT NULL DEFAULT 0 CHECK (checked_packs >= 0), missing_packs INT NOT NULL DEFAULT 0 CHECK (missing_packs >= 0), damaged_packs INT NOT NULL DEFAULT 0 CHECK (damaged_packs >= 0), started_at TIMESTAMP WITH TIME ZONE, finished_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`)
			if err != nil {
				log.Printf("Failed schema migration (backup_maintenance): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE backup_maintenance ADD COLUMN IF NOT EXISTS verify_mode TEXT, ADD COLUMN IF NOT EXISTS processed_bytes BIGINT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS total_packs INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS checked_packs INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS missing_packs INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS damaged_packs INT NOT NULL DEFAULT 0; ALTER TABLE backup_maintenance DROP CONSTRAINT IF EXISTS backup_maintenance_state_check, DROP CONSTRAINT IF EXISTS chk_backup_maintenance_state, DROP CONSTRAINT IF EXISTS chk_backup_maintenance_verify_mode; ALTER TABLE backup_maintenance ADD CONSTRAINT backup_maintenance_state_check CHECK (state IN ('PENDING','RUNNING','RETRY_WAIT','CANCELLING','CANCELLED','COMPLETED','FAILED')), ADD CONSTRAINT chk_backup_maintenance_verify_mode CHECK ((kind = 'VERIFY' AND ((verify_mode IN ('METADATA','FULL') AND byte_budget IS NULL) OR (verify_mode = 'BUDGETED' AND byte_budget BETWEEN 67108864 AND 1099511627776))) OR (kind <> 'VERIFY' AND verify_mode IS NULL)); CREATE TABLE IF NOT EXISTS backup_verify_targets (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_maintenance_id UUID NOT NULL REFERENCES backup_maintenance(id) ON DELETE CASCADE, backup_pack_id UUID CONSTRAINT fk_backup_verify_targets_live_pack REFERENCES backup_packs(id) ON DELETE RESTRICT, pack_remote_path TEXT NOT NULL, pack_sha256 BYTEA NOT NULL CHECK (octet_length(pack_sha256) = 32), pack_size_bytes BIGINT NOT NULL CHECK (pack_size_bytes > 0), state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING','RUNNING','COMPLETED','MISSING','DAMAGED','FAILED','CANCELLED')), bytes_read BIGINT NOT NULL DEFAULT 0 CHECK (bytes_read >= 0), error_code TEXT, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (backup_maintenance_id, pack_remote_path)); ALTER TABLE backup_verify_targets DROP CONSTRAINT IF EXISTS backup_verify_targets_backup_pack_id_fkey, DROP CONSTRAINT IF EXISTS fk_backup_verify_targets_live_pack; ALTER TABLE backup_verify_targets ADD CONSTRAINT fk_backup_verify_targets_live_pack FOREIGN KEY (backup_pack_id) REFERENCES backup_packs(id) ON DELETE RESTRICT; CREATE INDEX IF NOT EXISTS idx_backup_verify_targets_claim ON backup_verify_targets(backup_maintenance_id, state)`)
			if err != nil {
				log.Printf("Failed schema migration (backup verify targets): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_backup_maintenance_pending ON backup_maintenance(next_retry_at, created_at) WHERE state IN ('PENDING','RETRY_WAIT') AND kind <> 'VERIFY'; CREATE INDEX IF NOT EXISTS idx_backup_maintenance_job ON backup_maintenance(backup_job_id, created_at DESC); CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_maintenance_active_verify ON backup_maintenance(backup_job_id) WHERE kind = 'VERIFY' AND state IN ('PENDING','RUNNING','RETRY_WAIT','CANCELLING')`)
			if err != nil {
				log.Printf("Failed schema migration (backup_maintenance indexes): %v\n", err)
			}
			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS restore_previews (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE, backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE, backup_snapshot_id UUID NOT NULL REFERENCES backup_snapshots(id) ON DELETE CASCADE, target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL,
				selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb, target_provider TEXT NOT NULL, target_root TEXT NOT NULL, conflict_strategy TEXT NOT NULL DEFAULT 'RENAME' CHECK (conflict_strategy IN ('SKIP','OVERWRITE','RENAME')), threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16), bandwidth_mbps INT NOT NULL DEFAULT 0 CHECK (bandwidth_mbps BETWEEN 0 AND 1000), config_fingerprint BYTEA NOT NULL CHECK (octet_length(config_fingerprint) = 32), status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','RUNNING','READY','FAILED','EXPIRED','CONSUMED','CANCELLED')), total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0), total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0), error_code TEXT, ready_at TIMESTAMP WITH TIME ZONE, expires_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
			); CREATE INDEX IF NOT EXISTS idx_restore_previews_claim ON restore_previews(created_at) WHERE status = 'QUEUED'; CREATE INDEX IF NOT EXISTS idx_restore_previews_owner ON restore_previews(user_id, created_at DESC);
			CREATE TABLE IF NOT EXISTS restore_jobs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE, backup_job_id UUID REFERENCES backup_jobs(id) ON DELETE SET NULL, backup_snapshot_id UUID REFERENCES backup_snapshots(id) ON DELETE SET NULL, source_backup_ref UUID NOT NULL, source_snapshot_ref UUID NOT NULL, target_profile_id UUID REFERENCES connection_profiles(id) ON DELETE SET NULL, selected_paths JSONB NOT NULL DEFAULT '[]'::jsonb, target_provider TEXT NOT NULL, target_root TEXT NOT NULL, conflict_strategy TEXT NOT NULL CHECK (conflict_strategy IN ('SKIP','OVERWRITE','RENAME')), threads INT NOT NULL DEFAULT 8 CHECK (threads BETWEEN 1 AND 16), bandwidth_mbps INT NOT NULL DEFAULT 0 CHECK (bandwidth_mbps BETWEEN 0 AND 1000), config_fingerprint BYTEA NOT NULL CHECK (octet_length(config_fingerprint) = 32), created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
			); ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS config_fingerprint BYTEA; CREATE INDEX IF NOT EXISTS idx_restore_jobs_owner ON restore_jobs(user_id, created_at DESC);
			CREATE TABLE IF NOT EXISTS restore_runs (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), restore_job_id UUID NOT NULL REFERENCES restore_jobs(id) ON DELETE CASCADE, generation INT NOT NULL CHECK (generation > 0), status TEXT NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING','COMPLETED','PARTIAL','FAILED','CANCELLED')), total_files INT NOT NULL DEFAULT 0 CHECK (total_files >= 0), total_bytes BIGINT NOT NULL DEFAULT 0 CHECK (total_bytes >= 0), processed_files INT NOT NULL DEFAULT 0 CHECK (processed_files >= 0), processed_bytes BIGINT NOT NULL DEFAULT 0 CHECK (processed_bytes >= 0), failed_files INT NOT NULL DEFAULT 0 CHECK (failed_files >= 0), error_code TEXT, started_at TIMESTAMP WITH TIME ZONE, finished_at TIMESTAMP WITH TIME ZONE, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (restore_job_id, generation)
			); CREATE UNIQUE INDEX IF NOT EXISTS idx_restore_runs_active ON restore_runs(restore_job_id) WHERE status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'); CREATE INDEX IF NOT EXISTS idx_restore_runs_claim ON restore_runs(created_at) WHERE status = 'QUEUED';
			CREATE TABLE IF NOT EXISTS restore_items (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(), restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE, parent_item_id UUID, snapshot_relative_path TEXT NOT NULL, is_dir BOOLEAN NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0), file_sha256 BYTEA CHECK (file_sha256 IS NULL OR octet_length(file_sha256) = 32), source_mtime TIMESTAMP WITH TIME ZONE, source_metadata JSONB, target_path TEXT NOT NULL, resolved_target_path TEXT, status TEXT NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING','RUNNING','COMPLETED','SKIPPED','WARNING','FAILED','CANCELLED')), verification_kind TEXT CHECK (verification_kind IS NULL OR verification_kind IN ('HASH_VERIFIED','SIZE_VERIFIED')), outcome_code TEXT, attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0), next_retry_at TIMESTAMP WITH TIME ZONE, error_code TEXT, created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (restore_run_id, snapshot_relative_path), UNIQUE (restore_run_id, id), FOREIGN KEY (restore_run_id, parent_item_id) REFERENCES restore_items(restore_run_id, id) ON DELETE CASCADE
			); ALTER TABLE restore_items ADD COLUMN IF NOT EXISTS parent_item_id UUID, ADD COLUMN IF NOT EXISTS resolved_target_path TEXT, ADD COLUMN IF NOT EXISTS outcome_code TEXT, ADD COLUMN IF NOT EXISTS source_mtime TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS source_metadata JSONB, ADD COLUMN IF NOT EXISTS verification_kind TEXT, ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS next_retry_at TIMESTAMP WITH TIME ZONE; CREATE INDEX IF NOT EXISTS idx_restore_items_run_status ON restore_items(restore_run_id, status, created_at); CREATE INDEX IF NOT EXISTS idx_restore_items_retry ON restore_items(next_retry_at) WHERE status = 'PENDING' AND next_retry_at IS NOT NULL;
			CREATE TABLE IF NOT EXISTS restore_item_blocks (restore_item_id UUID NOT NULL REFERENCES restore_items(id) ON DELETE CASCADE, ordinal INT NOT NULL CHECK (ordinal >= 0), pack_remote_path TEXT NOT NULL, pack_sha256 BYTEA NOT NULL CHECK (octet_length(pack_sha256) = 32), pack_size_bytes BIGINT NOT NULL CHECK (pack_size_bytes > 0), payload_offset BIGINT NOT NULL CHECK (payload_offset >= 0), payload_length INT NOT NULL CHECK (payload_length BETWEEN 1 AND 4194304), block_sha256 BYTEA NOT NULL CHECK (octet_length(block_sha256) = 32), plaintext_size INT NOT NULL CHECK (plaintext_size BETWEEN 1 AND 4194304), PRIMARY KEY (restore_item_id, ordinal));
			CREATE TABLE IF NOT EXISTS restore_pack_pins (restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE, backup_pack_id UUID NOT NULL REFERENCES backup_packs(id) ON DELETE RESTRICT, PRIMARY KEY (restore_run_id, backup_pack_id))`)
			if err != nil {
				log.Printf("Failed schema migration (restore): %v\n", err)
			}
			// Release-2 additions are kept as idempotent upgrade DDL so an active
			// installation retains its restore history and pack pins.
			_, err = db.Exec(`
				ALTER TABLE backup_maintenance ADD COLUMN IF NOT EXISTS coordinator_generation INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS coordinator_lease_until TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS worker_hash TEXT;
				ALTER TABLE backup_verify_targets ADD COLUMN IF NOT EXISTS claim_epoch BIGINT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS claim_deadline TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS worker_hash TEXT, ADD COLUMN IF NOT EXISTS cursor JSONB NOT NULL DEFAULT '{}'::jsonb;
				ALTER TABLE restore_previews ADD COLUMN IF NOT EXISTS retry_restore_job_id UUID, ADD COLUMN IF NOT EXISTS target_url TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS target_username TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS target_password_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_refresh_token_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_token_expires_at TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS target_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_master_key_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_connection_identity TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS total_directories INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS existing_file_conflicts INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS mergeable_directories INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS type_conflicts INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS unavailable_items INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS expected_skips INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS expected_renames INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS metadata_warnings INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS conflict_examples JSONB NOT NULL DEFAULT '[]'::jsonb, ADD COLUMN IF NOT EXISTS coordinator_generation INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS coordinator_lease_until TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS worker_hash TEXT;
				ALTER TABLE restore_previews DROP CONSTRAINT IF EXISTS fk_restore_previews_retry_job;
				ALTER TABLE restore_previews ADD CONSTRAINT fk_restore_previews_retry_job FOREIGN KEY (retry_restore_job_id) REFERENCES restore_jobs(id) ON DELETE SET NULL;
				ALTER TABLE restore_jobs ADD COLUMN IF NOT EXISTS target_url TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS target_username TEXT NOT NULL DEFAULT '', ADD COLUMN IF NOT EXISTS target_connection_identity TEXT NOT NULL DEFAULT '';
				ALTER TABLE restore_runs ADD COLUMN IF NOT EXISTS threads INT NOT NULL DEFAULT 8, ADD COLUMN IF NOT EXISTS bandwidth_mbps INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS target_password_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_refresh_token_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_token_expires_at TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS target_mega_session_id_encrypted TEXT, ADD COLUMN IF NOT EXISTS target_mega_master_key_encrypted TEXT, ADD COLUMN IF NOT EXISTS coordinator_generation INT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS coordinator_lease_until TIMESTAMP WITH TIME ZONE, ADD COLUMN IF NOT EXISTS worker_hash TEXT;
				ALTER TABLE restore_items ADD COLUMN IF NOT EXISTS parent_item_id UUID, ADD COLUMN IF NOT EXISTS resolved_target_path TEXT, ADD COLUMN IF NOT EXISTS outcome_code TEXT, ADD COLUMN IF NOT EXISTS claim_epoch BIGINT NOT NULL DEFAULT 0, ADD COLUMN IF NOT EXISTS worker_hash TEXT, ADD COLUMN IF NOT EXISTS claim_deadline TIMESTAMP WITH TIME ZONE;
				ALTER TABLE restore_items DROP CONSTRAINT IF EXISTS restore_items_run_id_uniq, DROP CONSTRAINT IF EXISTS fk_restore_items_parent;
				ALTER TABLE restore_items ADD CONSTRAINT restore_items_run_id_uniq UNIQUE (restore_run_id, id);
				ALTER TABLE restore_items ADD CONSTRAINT fk_restore_items_parent FOREIGN KEY (restore_run_id, parent_item_id) REFERENCES restore_items(restore_run_id, id) ON DELETE CASCADE;
				CREATE TABLE IF NOT EXISTS restore_path_reservations (restore_run_id UUID NOT NULL REFERENCES restore_runs(id) ON DELETE CASCADE, canonical_path TEXT NOT NULL, restore_item_id UUID NOT NULL REFERENCES restore_items(id) ON DELETE CASCADE, PRIMARY KEY (restore_run_id, canonical_path), UNIQUE (restore_run_id, restore_item_id));
				ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS restore_run_id UUID REFERENCES restore_runs(id) ON DELETE CASCADE;
				ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS backup_run_id UUID REFERENCES backup_runs(id) ON DELETE CASCADE;
				ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_kind_check, DROP CONSTRAINT IF EXISTS notification_events_check, DROP CONSTRAINT IF EXISTS chk_notification_events_kind, DROP CONSTRAINT IF EXISTS chk_notification_events_parent;
				ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_kind CHECK (kind IN ('migration','sync','restore','backup'));
				ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_parent CHECK (num_nonnulls(migration_id, sync_job_id, restore_run_id, backup_run_id) = 1 AND ((kind = 'migration' AND migration_id IS NOT NULL) OR (kind = 'sync' AND sync_job_id IS NOT NULL) OR (kind = 'restore' AND restore_run_id IS NOT NULL) OR (kind = 'backup' AND backup_run_id IS NOT NULL)));
				CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_restore_uniq ON notification_events(restore_run_id) WHERE restore_run_id IS NOT NULL;
			`)
			if err != nil {
				log.Printf("Failed schema migration (restore fencing): %v\n", err)
			}

			_, err = db.Exec(`DROP TRIGGER IF EXISTS update_backup_jobs_updated_at ON backup_jobs; CREATE TRIGGER update_backup_jobs_updated_at BEFORE UPDATE ON backup_jobs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_runs_updated_at ON backup_runs; CREATE TRIGGER update_backup_runs_updated_at BEFORE UPDATE ON backup_runs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_snapshots_updated_at ON backup_snapshots; CREATE TRIGGER update_backup_snapshots_updated_at BEFORE UPDATE ON backup_snapshots FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_packs_updated_at ON backup_packs; CREATE TRIGGER update_backup_packs_updated_at BEFORE UPDATE ON backup_packs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_blocks_updated_at ON backup_blocks; CREATE TRIGGER update_backup_blocks_updated_at BEFORE UPDATE ON backup_blocks FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_snapshot_items_updated_at ON backup_snapshot_items; CREATE TRIGGER update_backup_snapshot_items_updated_at BEFORE UPDATE ON backup_snapshot_items FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_backup_maintenance_updated_at ON backup_maintenance; CREATE TRIGGER update_backup_maintenance_updated_at BEFORE UPDATE ON backup_maintenance FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_restore_previews_updated_at ON restore_previews; CREATE TRIGGER update_restore_previews_updated_at BEFORE UPDATE ON restore_previews FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_restore_jobs_updated_at ON restore_jobs; CREATE TRIGGER update_restore_jobs_updated_at BEFORE UPDATE ON restore_jobs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_restore_runs_updated_at ON restore_runs; CREATE TRIGGER update_restore_runs_updated_at BEFORE UPDATE ON restore_runs FOR EACH ROW EXECUTE FUNCTION update_updated_at_column(); DROP TRIGGER IF EXISTS update_restore_items_updated_at ON restore_items; CREATE TRIGGER update_restore_items_updated_at BEFORE UPDATE ON restore_items FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`)
			if err != nil {
				log.Printf("Failed schema migration (backup triggers): %v\n", err)
			}
			// notification_deliveries has an updated_at column: add the trigger that was
			// missing from the original schema so every UPDATE reflects actual modification time.
			_, err = db.Exec(`DROP TRIGGER IF EXISTS update_notification_deliveries_updated_at ON notification_deliveries;
				CREATE TRIGGER update_notification_deliveries_updated_at BEFORE UPDATE ON notification_deliveries FOR EACH ROW EXECUTE FUNCTION update_updated_at_column()`)
			if err != nil {
				log.Printf("Failed schema migration (notification_deliveries trigger): %v\n", err)
			}

			_, err = db.Exec(`
				ALTER TABLE notification_events ADD COLUMN IF NOT EXISTS backup_run_id UUID REFERENCES backup_runs(id) ON DELETE CASCADE;
				ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notification_events_kind_check, DROP CONSTRAINT IF EXISTS notification_events_check, DROP CONSTRAINT IF EXISTS chk_notification_events_kind, DROP CONSTRAINT IF EXISTS chk_notification_events_parent;
				ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_kind CHECK (kind IN ('migration','sync','restore','backup'));
				ALTER TABLE notification_events ADD CONSTRAINT chk_notification_events_parent CHECK (num_nonnulls(migration_id, sync_job_id, restore_run_id, backup_run_id) = 1 AND ((kind = 'migration' AND migration_id IS NOT NULL) OR (kind = 'sync' AND sync_job_id IS NOT NULL) OR (kind = 'restore' AND restore_run_id IS NOT NULL) OR (kind = 'backup' AND backup_run_id IS NOT NULL)));
				CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_events_backup_uniq ON notification_events(backup_run_id) WHERE backup_run_id IS NOT NULL;
			`)
			if err != nil {
				log.Printf("Failed schema migration (notification_events backup): %v\n", err)
			}

			if schemaErr != nil {
				releaseLock()
				db.Close()
				return nil, schemaErr
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
			db.SetConnMaxIdleTime(5 * time.Minute)
			return db, nil
		}
		_ = db.Close()
		log.Printf("Waiting for PostgreSQL database to be ready (attempt %d/10): %v\n", attempt, pingErr)
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("database not ready after 10 attempts: %w", pingErr)
}
