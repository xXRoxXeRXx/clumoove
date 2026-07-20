package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
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

// ValidRoles enumerates the roles a user may hold. There is no separate
// read-only AUDITOR role; the instance-wide oversight (user list, all
// migrations, audit log) is granted exclusively to ADMIN.
var ValidRoles = map[string]bool{
	"USER":  true,
	"ADMIN": true,
}

type User struct {
	ID                 string         `json:"id"`
	Email              string         `json:"email"`
	PasswordHash       string         `json:"-"`
	DisplayName        string         `json:"display_name"`
	Role               string         `json:"role"`
	Active             bool           `json:"active"`
	MustChangePassword bool           `json:"must_change_password"`
	Avatar             []byte         `json:"-"`
	AvatarMime         string         `json:"-"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	TotpEnabled        bool           `json:"totp_enabled"`
	TotpSecretEnc      string         `json:"-"`
	TotpBackupCodes    StringArray    `json:"-"`
	TotpFailedAttempts int            `json:"-"`
	TotpLockedUntil    sql.NullTime   `json:"-"`
	LoginFailedAttempts int           `json:"-"`
	LoginLockedUntil   sql.NullTime   `json:"-"`
}

// AuditAction enumerates the canonical audit-log event types.
type AuditAction string

const (
	AuditLoginSuccess      AuditAction = "LOGIN_SUCCESS"
	AuditLoginFailed       AuditAction = "LOGIN_FAILED"
	AuditRegistration      AuditAction = "REGISTRATION"
	AuditUserCreated       AuditAction = "USER_CREATED"
	AuditMigrationCreated  AuditAction = "MIGRATION_CREATED"
	AuditMigrationStarted  AuditAction = "MIGRATION_STARTED"
	AuditMigrationCompleted AuditAction = "MIGRATION_COMPLETED"
	AuditMigrationFailed   AuditAction = "MIGRATION_FAILED"
	AuditMigrationPaused   AuditAction = "MIGRATION_PAUSED"
	AuditMigrationResumed  AuditAction = "MIGRATION_RESUMED"
	AuditMigrationCancelled AuditAction = "MIGRATION_CANCELLED"
	AuditMigrationDeleted  AuditAction = "MIGRATION_DELETED"
	AuditSettingUpdated    AuditAction = "SETTING_UPDATED"
	AuditUserSuspended     AuditAction = "USER_SUSPENDED"
	AuditUserReactivated   AuditAction = "USER_REACTIVATED"
	AuditUserDeleted       AuditAction = "USER_DELETED"
	AuditUserRoleChanged   AuditAction = "USER_ROLE_CHANGED"
	Audit2FAEnabled        AuditAction = "2FA_ENABLED"
	Audit2FADisabled       AuditAction = "2FA_DISABLED"
)

// AuditEntry is a single audit-log record. UserID is the acting principal
// (NULL for failed logins). Details is an arbitrary JSONB payload.
type AuditEntry struct {
	UserID sql.NullString
	Action AuditAction
	Target string
	IP     string
	Details json.RawMessage
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
	Status                      string                  `json:"status"`            // PENDING, INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, COMPLETED_WITH_ERRORS, FAILED, SCHEDULED
	ConflictStrategy            string                  `json:"conflict_strategy"` // SKIP, OVERWRITE, RENAME
	SelectedPaths               StringArray             `json:"selected_paths,omitempty"`
	SelectedCalendars           StringArray             `json:"selected_calendars,omitempty"`
	SelectedContacts            StringArray             `json:"selected_contacts,omitempty"`
	PickerSessionID             string                  `json:"picker_session_id,omitempty"`
	TotalFiles                  int                     `json:"total_files"`
	TotalBytes                  int64                   `json:"total_bytes"`
	ProcessedFiles              int                     `json:"processed_files"`
	ProcessedBytes              int64                   `json:"processed_bytes"`
	// LiveBytes is a frequently-updated, non-cumulative counter fed by the
	// streaming progress channel. It is used ONLY for the transfer-speed / ETA
	// display and may transiently exceed TotalBytes (e.g. on a retried upload).
	// The authoritative, never-overflowing progress for the "X / Y" byte display
	// is ProcessedBytes, which is booked exactly once per file at verified
	// completion. See processor.go.
	LiveBytes                   int64                   `json:"live_bytes"`
	SkippedFiles                int                     `json:"skipped_files"`
	FailedFiles                 int                     `json:"failed_files"`
	ErrorMessage                sql.NullString          `json:"error_message"`
	CreatedAt                   time.Time               `json:"created_at"`
	UpdatedAt                   time.Time               `json:"updated_at"`
	ResourceStats               *MigrationResourceStats `json:"resource_stats,omitempty"`
	Threads                     int                     `json:"threads"`
	BandwidthLimitMbps          int                     `json:"bandwidth_limit_mbps"`
}

type Task struct {
	ID           string          `json:"id"`
	MigrationID  string          `json:"migration_id"`
	SyncJobID    string          `json:"sync_job_id,omitempty"`
	FilePath     string          `json:"file_path"`
	FileSize     int64           `json:"file_size"`
	SourceHash   sql.NullString  `json:"source_hash"`
	WorkerHash   sql.NullString  `json:"worker_hash"`
	TargetHash   sql.NullString  `json:"target_hash"`
	Status       string          `json:"status"`        // PENDING, RUNNING, COMPLETED, FAILED, SKIPPED
	ResourceType string          `json:"resource_type"` // files, calendars, contacts
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	ErrorMessage sql.NullString  `json:"error_message"`
	Attempts     int             `json:"attempts"`
	NextRetryAt  sql.NullTime    `json:"next_retry_at"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// dbHostFromConnStr extracts the host from either a postgres:// URL or a
// keyword/value connection string, used to exempt localhost/dev databases
// from the default-credential rejection in InitDB.
func dbHostFromConnStr(connStr string) string {
	if strings.HasPrefix(connStr, "postgres://") || strings.HasPrefix(connStr, "postgresql://") {
		if u, err := url.Parse(connStr); err == nil {
			return u.Hostname()
		}
		return ""
	}
	// keyword/value form: host=... or host=.../dbname
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

// isLocalOrPrivateHost reports whether the database host is loopback or a
// private (RFC1918/ULA) address. The default-credential rejection must not
// fire for these: local dev (localhost) and Docker-internal services (e.g.
// the "postgres-db" container, which resolves to a 172.x/10.x private IP) are
// not exposed to the public internet, so the well-known postgres/postgres
// credentials are acceptable there. Only a *publicly reachable* database on
// default credentials is the real risk.
func isLocalOrPrivateHost(host string) bool {
	if host == "" {
		return false
	}
	// Tolerate bracketed IPv6 literals (e.g. "[::1]") from URL-style DSNs.
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	// Hostname: resolve and inspect every address. If any resolved address is
	// public, treat the host as public (do not exempt). If the name cannot be
	// resolved, exempt it rather than refusing to start — a connection to an
	// unresolvable host would fail at Ping() regardless, so there is no
	// credential-exposure risk, and failing open here avoids a boot-time
	// regression on transient DNS outages.
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		// Fail closed: an unresolvable DB host is treated as public so the
		// default-credential refusal triggers, rather than being silently
		// exempted. The connection would fail at Ping() anyway, so there is no
		// credential-exposure risk — only the boot-time default-credential guard
		// is made stricter. Restore the old fail-open behaviour for known-broken
		// DNS setups with ALLOW_UNRESOLVED_DB_HOST=1.
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

// InitDB initializes the database connection with startup retries
func InitDB(connStr string) (*sql.DB, error) {
	// Reject the well-known postgres/postgres default credentials when the
	// database is publicly reachable (M-4). A deployment that forgets to set
	// DB_PASSWORD would otherwise run with a publicly-known password. Local
	// dev (localhost) and Docker-internal services (which resolve to private
	// RFC1918/ULA addresses) are exempted — they are not exposed to the public
	// internet, so the default credentials are acceptable there. Only a
	// publicly-reachable database on default credentials is the real risk.
	if host := dbHostFromConnStr(connStr); !isLocalOrPrivateHost(host) && strings.Contains(connStr, "postgres:postgres@") {
		return nil, fmt.Errorf("insecure DATABASE_URL: the default 'postgres:postgres' credentials are only permitted for a localhost or private-network database. Set DB_PASSWORD to a strong, unique password for any publicly-reachable deployment.")
	}

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
			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS metadata JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (metadata): %v\n", err)
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
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS picker_session_id TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (picker_session_id): %v\n", err)
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

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_pending ON tasks(status, created_at) WHERE status = 'PENDING'`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_pending): %v\n", err)
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

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS email_sent BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (email_sent): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS bandwidth_limit_mbps INT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (bandwidth_limit_mbps): %v\n", err)
			}

			// live_bytes: non-cumulative counter for transfer-speed/ETA display.
			_, err = db.Exec(`ALTER TABLE migrations ADD COLUMN IF NOT EXISTS live_bytes BIGINT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (live_bytes): %v\n", err)
			}

			// TOTP 2FA columns
			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret_encrypted TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (totp_secret_encrypted): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE`)
			if err != nil {
				log.Printf("Failed schema migration (totp_enabled): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_backup_codes JSONB`)
			if err != nil {
				log.Printf("Failed schema migration (totp_backup_codes): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_failed_attempts INTEGER NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (totp_failed_attempts): %v\n", err)
			}
		_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_locked_until TIMESTAMP WITH TIME ZONE`)
		if err != nil {
			log.Printf("Failed schema migration (totp_locked_until): %v\n", err)
		}

		_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS login_failed_attempts INTEGER NOT NULL DEFAULT 0`)
		if err != nil {
			log.Printf("Failed schema migration (login_failed_attempts): %v\n", err)
		}
		_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS login_locked_until TIMESTAMP WITH TIME ZONE`)
		if err != nil {
			log.Printf("Failed schema migration (login_locked_until): %v\n", err)
		}

		_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE`)
		if err != nil {
			log.Printf("Failed schema migration (active): %v\n", err)
		}

		_, err = db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password BOOLEAN NOT NULL DEFAULT FALSE`)
		if err != nil {
			log.Printf("Failed schema migration (must_change_password): %v\n", err)
		}

		// Audit Log table
		_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS audit_log (
				id BIGSERIAL PRIMARY KEY,
				user_id UUID,
				action TEXT NOT NULL,
				target TEXT,
				ip TEXT,
				details JSONB,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
			)
		`)
		if err != nil {
			log.Printf("Failed schema migration (create audit_log table): %v\n", err)
		}

		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_created_at ON audit_log(created_at)`)
		if err != nil {
			log.Printf("Failed schema migration (idx_audit_log_created_at): %v\n", err)
		}
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action)`)
		if err != nil {
			log.Printf("Failed schema migration (idx_audit_log_action): %v\n", err)
		}
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_log_user_id ON audit_log(user_id)`)
		if err != nil {
			log.Printf("Failed schema migration (idx_audit_log_user_id): %v\n", err)
		}

		// Reusable connection profiles (usable as either source or target)
		_, err = db.Exec(`
			CREATE TABLE IF NOT EXISTS connection_profiles (
				id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
				user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				provider TEXT NOT NULL,
				url TEXT,
				username TEXT,
				password_encrypted TEXT,
				refresh_token_encrypted TEXT,
				token_expires_at TIMESTAMP WITH TIME ZONE,
				oauth_user TEXT,
				created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
				UNIQUE (user_id, name)
			)
		`)
		if err != nil {
			log.Printf("Failed schema migration (create connection_profiles table): %v\n", err)
		}

		// Drop the legacy role column/constraint/index on existing deployments.
		_, _ = db.Exec(`ALTER TABLE connection_profiles DROP CONSTRAINT IF EXISTS connection_profiles_role_check`)
		_, _ = db.Exec(`ALTER TABLE connection_profiles DROP COLUMN IF EXISTS role`)
		_, _ = db.Exec(`DROP INDEX IF EXISTS idx_conn_profiles_user_role`)
		_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_profiles_user ON connection_profiles(user_id)`)
		if err != nil {
			log.Printf("Failed schema migration (idx_conn_profiles_user): %v\n", err)
		}

		_, err = db.Exec(`
			CREATE OR REPLACE TRIGGER update_connection_profiles_updated_at
				BEFORE UPDATE ON connection_profiles
				FOR EACH ROW
				EXECUTE FUNCTION update_updated_at_column()
		`)
		if err != nil {
			log.Printf("Failed schema migration (trigger connection_profiles_updated_at): %v\n", err)
		}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS user_smtp_settings (
					user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
					smtp_host TEXT NOT NULL,
					smtp_port INT NOT NULL DEFAULT 587,
					smtp_username TEXT NOT NULL,
					smtp_password_encrypted TEXT NOT NULL,
					smtp_from_email TEXT NOT NULL,
					smtp_from_name TEXT NOT NULL DEFAULT '',
					smtp_encryption TEXT NOT NULL DEFAULT 'tls',
					notify_on_completion BOOLEAN NOT NULL DEFAULT TRUE,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create user_smtp_settings table): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS password_reset_tokens (
					token_hash TEXT PRIMARY KEY,
					user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
					used BOOLEAN NOT NULL DEFAULT FALSE,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create password_reset_tokens table): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS email_change_tokens (
					token_hash TEXT PRIMARY KEY,
					user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
					new_email TEXT NOT NULL,
					expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
					used BOOLEAN NOT NULL DEFAULT FALSE,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create email_change_tokens table): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS indexing_errors (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
					path TEXT NOT NULL,
					resource_type TEXT NOT NULL DEFAULT 'files',
					error_message TEXT NOT NULL,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create indexing_errors table): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_indexing_errors_migration_id ON indexing_errors(migration_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_indexing_errors_migration_id): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE OR REPLACE TRIGGER update_user_smtp_settings_updated_at
				    BEFORE UPDATE ON user_smtp_settings
				    FOR EACH ROW
				    EXECUTE FUNCTION update_updated_at_column()
			`)
			if err != nil {
				log.Printf("Failed schema migration (trigger user_smtp_settings_updated_at): %v\n", err)
			}

			// Sync Engine migrations
			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS sync_jobs (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
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
					direction TEXT NOT NULL DEFAULT 'one_way' CHECK (direction IN ('one_way','two_way')),
					conflict_strategy TEXT NOT NULL DEFAULT 'OVERWRITE' CHECK (conflict_strategy IN ('OVERWRITE','SKIP','RENAME')),
					delete_propagation BOOLEAN NOT NULL DEFAULT FALSE,
					interval_minutes INT NOT NULL DEFAULT 15,
					threads INT NOT NULL DEFAULT 4,
					status TEXT NOT NULL DEFAULT 'IDLE',
					target_dir TEXT NOT NULL DEFAULT '/',
					selected_paths JSONB,
					last_run_at TIMESTAMP WITH TIME ZONE,
					last_run_status TEXT,
					error_message TEXT,
					total_files INT NOT NULL DEFAULT 0,
					processed_files INT NOT NULL DEFAULT 0,
					changed_files INT NOT NULL DEFAULT 0,
					deleted_files INT NOT NULL DEFAULT 0,
					failed_files INT NOT NULL DEFAULT 0,
					created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
					updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create sync_jobs table): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_user_id ON sync_jobs(user_id)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_sync_jobs_user_id): %v\n", err)
			}
			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_jobs_status ON sync_jobs(status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_sync_jobs_status): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS total_bytes BIGINT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs total_bytes): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS processed_bytes BIGINT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs processed_bytes): %v\n", err)
			}
			_, err = db.Exec(`ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS live_bytes BIGINT NOT NULL DEFAULT 0`)
			if err != nil {
				log.Printf("Failed schema migration (sync_jobs live_bytes): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE OR REPLACE TRIGGER update_sync_jobs_updated_at
				    BEFORE UPDATE ON sync_jobs
				    FOR EACH ROW
				    EXECUTE FUNCTION update_updated_at_column()
			`)
			if err != nil {
				log.Printf("Failed schema migration (trigger sync_jobs_updated_at): %v\n", err)
			}

			_, err = db.Exec(`
				CREATE TABLE IF NOT EXISTS sync_state (
					id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
					sync_job_id UUID NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
					side TEXT NOT NULL CHECK (side IN ('source','target')),
					rel_path TEXT NOT NULL,
					size BIGINT NOT NULL DEFAULT 0,
					mtime TIMESTAMP WITH TIME ZONE,
					source_hash TEXT,
					target_hash TEXT,
					etag TEXT,
					UNIQUE (sync_job_id, side, rel_path)
				)
			`)
			if err != nil {
				log.Printf("Failed schema migration (create sync_state table): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE sync_state ADD COLUMN IF NOT EXISTS etag TEXT`)
			if err != nil {
				log.Printf("Failed schema migration (alter sync_state add etag): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_sync_state_job ON sync_state(sync_job_id, side)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_sync_state_job): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ALTER COLUMN migration_id DROP NOT NULL`)
			if err != nil {
				log.Printf("Failed schema migration (alter tasks migration_id drop not null): %v\n", err)
			}

			_, err = db.Exec(`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE`)
			if err != nil {
				log.Printf("Failed schema migration (alter tasks add sync_job_id): %v\n", err)
			}

			_, _ = db.Exec(`ALTER TABLE tasks DROP CONSTRAINT IF EXISTS chk_task_job_type`)
			_, err = db.Exec(`ALTER TABLE tasks ADD CONSTRAINT chk_task_job_type CHECK ((migration_id IS NOT NULL AND sync_job_id IS NULL) OR (migration_id IS NULL AND sync_job_id IS NOT NULL))`)
			if err != nil {
				log.Printf("Failed schema migration (chk_task_job_type constraint): %v\n", err)
			}

			_, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_tasks_sync_status ON tasks(sync_job_id, status)`)
			if err != nil {
				log.Printf("Failed schema migration (idx_tasks_sync_status): %v\n", err)
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
			selected_paths, selected_calendars, selected_contacts, threads, bandwidth_limit_mbps,
			picker_session_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		m.UserID, m.SourceURL, m.SourceUsername, m.SourcePasswordEncrypted,
		m.SourceRefreshTokenEncrypted, m.SourceTokenExpiresAt,
		m.TargetURL, m.TargetUsername, m.TargetPasswordEncrypted,
		m.TargetRefreshTokenEncrypted, m.TargetTokenExpiresAt,
		m.SourceProvider, m.TargetProvider, m.Status, m.ConflictStrategy, m.TargetDir,
		m.SelectedPaths, m.SelectedCalendars, m.SelectedContacts, m.Threads, m.BandwidthLimitMbps,
		m.PickerSessionID,
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
		       processed_files, processed_bytes, live_bytes, skipped_files, failed_files,
		       error_message, created_at, updated_at, target_dir, threads,
		       selected_paths, selected_calendars, selected_contacts, bandwidth_limit_mbps,
		       picker_session_id
		FROM migrations WHERE id = $1
	`
	var m Migration
	err := db.QueryRow(query, id).Scan(
		&m.ID, &m.UserID, &m.SourceURL, &m.SourceUsername, &m.SourcePasswordEncrypted,
		&m.SourceRefreshTokenEncrypted, &m.SourceTokenExpiresAt,
		&m.TargetURL, &m.TargetUsername, &m.TargetPasswordEncrypted,
		&m.TargetRefreshTokenEncrypted, &m.TargetTokenExpiresAt,
		&m.SourceProvider, &m.TargetProvider, &m.Status, &m.ConflictStrategy, &m.TotalFiles, &m.TotalBytes,
		&m.ProcessedFiles, &m.ProcessedBytes, &m.LiveBytes, &m.SkippedFiles, &m.FailedFiles,
		&m.ErrorMessage, &m.CreatedAt, &m.UpdatedAt, &m.TargetDir, &m.Threads,
		&m.SelectedPaths, &m.SelectedCalendars, &m.SelectedContacts, &m.BandwidthLimitMbps,
		&m.PickerSessionID,
	)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// UpdateMigrationBandwidthLimit updates the bandwidth limit for a migration
func UpdateMigrationBandwidthLimit(db *sql.DB, id string, limitMbps int) error {
	query := `
		UPDATE migrations
		SET bandwidth_limit_mbps = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, limitMbps, id)
	return err
}

// UpdateMigrationThreads updates the thread count for a migration.
func UpdateMigrationThreads(db *sql.DB, id string, threads int) error {
	query := `
		UPDATE migrations
		SET threads = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, threads, id)
	return err
}

// UpdateMigrationPickerSession persists the Google Photos Picker session id
// for a migration (used by the deferred/scheduled flow where the picker session
// is created separately from the migration row).
func UpdateMigrationPickerSession(db *sql.DB, id string, pickerSessionID string) error {
	query := `
		UPDATE migrations
		SET picker_session_id = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := db.Exec(query, pickerSessionID, id)
	return err
}
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
	query := `SELECT file_path, metadata FROM tasks WHERE migration_id = $1 AND status = 'RUNNING' ORDER BY updated_at DESC`
	rows, err := db.QueryContext(ctx, query, migrationID)
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

// displayTaskName returns a user-visible name for a task. Google Photos Picker
// tasks carry a self-describing transport handle in file_path (a "/picker/<id>
// ?base_url=…" path); the real media name lives in the task metadata, so we
// surface that instead of the raw transport path. For all other tasks we fall
// back to the path basename.
func displayTaskName(filePath string, meta json.RawMessage) string {
	base := path.Base(filePath)
	if base == "." || base == "/" || base == "" {
		return filePath
	}
	return base
}

// IncrementMigrationProgress increments the counters of a migration in the database
// and transitions the migration to COMPLETED, COMPLETED_WITH_ERRORS or FAILED once all files are processed.
// CancelRemainingPendingTasks marks all still-PENDING tasks of a migration as
// CANCELLED (terminal) and returns the number cancelled. This is used when a
// migration transitions to a terminal state (e.g. auth failure) so that
// dequeue can never pick up those tasks again — otherwise they would stay
// PENDING forever (the dequeue query only releases PENDING tasks while the
// migration is RUNNING/INDEXING), leaving processed_files < total_files and
// preventing the WebSocket stream / report from completing.
func CancelRemainingPendingTasks(dbsql *sql.DB, migrationID string) (int, error) {
	res, err := dbsql.Exec(
		`UPDATE tasks SET status = 'CANCELLED', updated_at = CURRENT_TIMESTAMP WHERE migration_id = $1 AND status = 'PENDING'`,
		migrationID,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func IncrementMigrationProgress(db *sql.DB, ctx context.Context, id string, filesDelta int, bytesDelta int64, skippedDelta int, failedDelta int) error {
	tx, err := db.BeginTx(ctx, nil)
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
		RETURNING processed_files, total_files, failed_files, skipped_files
	`
	var processed, total, failed, skipped int
	err = tx.QueryRow(query, filesDelta, bytesDelta, skippedDelta, failedDelta, id).Scan(&processed, &total, &failed, &skipped)
	if err != nil {
		return err
	}

	// A migration is only finished once every task is in a terminal state
	// (success / skip / fail / cancelled). Counting processed alone misses
	// skipped/failed/cancelled tasks, which would otherwise leave the
	// migration stuck in RUNNING with processed < total (see CancelRemaining-
	// PendingTasks, which records cancelled tasks as failed).
	//
	// To avoid marking a migration COMPLETED/FAILED while open (PENDING/RUNNING)
	// tasks still exist — which previously produced a "99% but FERTIG" state —
	// we additionally require that no task is left in an open state. This is a
	// belt-and-suspenders guard; ReconcileMigrationProgress is the authoritative
	// repair path, but checking here prevents the race at the source.
	if total > 0 && processed+failed+skipped >= total {
		var openTasks int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM tasks WHERE migration_id = $1 AND status IN ('PENDING','RUNNING')`,
			id,
		).Scan(&openTasks); err != nil {
			return err
		}
		if openTasks == 0 {
			finalStatus := "COMPLETED"
			var errMessage sql.NullString
			if failed == total {
				finalStatus = "FAILED"
				errMessage = sql.NullString{String: "All file transfers failed", Valid: true}
			} else if failed > 0 {
				finalStatus = "COMPLETED_WITH_ERRORS"
			}

			statusQuery := `
				UPDATE migrations
				SET status = $1,
				    error_message = COALESCE($2, error_message)
				WHERE id = $3
				  AND status IN ('RUNNING', 'INDEXING')
			`
			res, err := tx.Exec(statusQuery, finalStatus, errMessage, id)
			if err != nil {
				return err
			}
			if rows, rerr := res.RowsAffected(); rerr == nil && rows > 0 {
				// Migration just transitioned to a terminal state: record it in the
				// audit log. Best-effort; ignore lookup failures.
				if owner, oerr := GetMigrationOwnerID(tx, id); oerr == nil {
					action := AuditMigrationCompleted
					if finalStatus == "FAILED" {
						action = AuditMigrationFailed
					}
					WriteAuditLog(tx, AuditEntry{
						UserID: sql.NullString{String: owner, Valid: true},
						Action: action,
						Target: id,
						Details: json.RawMessage(fmt.Sprintf(`{"phase":"transfer","all_failed":%t,"partial":%t}`, failed == total, failed > 0 && failed < total)),
					})
				}
			}
		}
	}

	return tx.Commit()
}

// AddLiveBytes adds bytes to the non-cumulative live_bytes counter used only
// for the transfer-speed / ETA display. It deliberately does NOT touch
// processed_bytes (which is booked exactly once per file at verified
// completion) so that processed_bytes can never exceed total_bytes due to
// retried uploads. See processor.go.
func AddLiveBytes(db *sql.DB, ctx context.Context, id string, bytesDelta int64) error {
	if bytesDelta == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE migrations SET live_bytes = live_bytes + $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`,
		bytesDelta, id)
	return err
}

// ResetLiveBytes sets live_bytes back to the authoritative processed_bytes
// value. Called when a migration reaches a terminal state so the speed display
// does not keep showing stale/transient values.
func ResetLiveBytes(db *sql.DB, ctx context.Context, id string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE migrations SET live_bytes = GREATEST(live_bytes, processed_bytes) WHERE id = $1`, id)
	return err
}

// ReconcileMigrationProgress repairs counter drift between the cached
// processed_files/skipped_files/failed_files/total_files columns on a migration
// and the actual task rows. It is idempotent and safe to call repeatedly.
//
// Why this exists: IncrementMigrationProgress updates the counters with delta
// increments that are best-effort (callers ignore the returned error). Under
// load, worker crashes between a task's status write and its counter increment,
// or tasks flipped to SKIPPED after the migration already transitioned to a
// terminal state, can leave the counters out of sync with the real task rows.
// That drift previously caused two visible bugs:
//   - A migration stuck in RUNNING forever with 100% byte progress because
//     processed+skipped+failed never reached total_files.
//   - A migration marked COMPLETED while PENDING tasks were still left behind,
//     so total_files > processed+skipped+failed.
//
// Reconciliation counts the real terminal task states and only transitions the
// migration to a terminal status when there are genuinely no open (PENDING/
// RUNNING) tasks left. It never resets or re-queues already-finished migrations
// — it only fixes the cached counters and, if appropriate, advances a stalled
// RUNNING/INDEXING migration to its correct terminal state.
func ReconcileMigrationProgress(dbsql *sql.DB, migrationID string) error {
	tx, err := dbsql.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Count real task states and capture the cached migration columns.
	var (
		completedTasks, skippedTasks, failedTasks, cancelledTasks int
		openTasks                                                 int
		totalFiles, processedFiles, skippedFiles, failedFiles    int
		status                                                    string
	)
	countQuery := `
		SELECT
			COUNT(*) FILTER (WHERE t.status = 'COMPLETED')  AS completed,
			COUNT(*) FILTER (WHERE t.status = 'SKIPPED')    AS skipped,
			COUNT(*) FILTER (WHERE t.status = 'FAILED')     AS failed,
			COUNT(*) FILTER (WHERE t.status = 'CANCELLED')  AS cancelled,
			COUNT(*) FILTER (WHERE t.status IN ('PENDING','RUNNING')) AS open,
			m.total_files, m.processed_files, m.skipped_files, m.failed_files, m.status
		FROM migrations m
		JOIN tasks t ON t.migration_id = m.id
		WHERE m.id = $1
		GROUP BY m.total_files, m.processed_files, m.skipped_files, m.failed_files, m.status
	`
	err = tx.QueryRow(countQuery, migrationID).Scan(
		&completedTasks, &skippedTasks, &failedTasks, &cancelledTasks, &openTasks,
		&totalFiles, &processedFiles, &skippedFiles, &failedFiles, &status,
	)
	if err == sql.ErrNoRows {
		return nil // no such migration; nothing to do
	}
	if err != nil {
		return err
	}

	// Recompute the cached counters from the authoritative task rows. Tasks that
	// were CANCELLED during a terminal transition are counted as failed (matching
	// CancelRemainingPendingTasks' behaviour so reports stay consistent).
	newProcessed := completedTasks
	newSkipped := skippedTasks
	newFailed := failedTasks + cancelledTasks
	newTotal := completedTasks + skippedTasks + failedTasks + cancelledTasks + openTasks

	// Only write when something actually drifted, to avoid needless writes/lock
	// contention on every tick.
	if newProcessed != processedFiles || newSkipped != skippedFiles ||
		newFailed != failedFiles || newTotal != totalFiles {
		if _, err := tx.Exec(`
			UPDATE migrations
			SET processed_files = $1,
			    skipped_files   = $2,
			    failed_files    = $3,
			    total_files     = $4,
			    updated_at      = CURRENT_TIMESTAMP
			WHERE id = $5
		`, newProcessed, newSkipped, newFailed, newTotal, migrationID); err != nil {
			return err
		}
	}

	// Advance a stalled active migration to its terminal state only when no open
	// tasks remain. This is the guard that prevents both bugs above: a migration
	// can never become COMPLETED/FAILED while PENDING/RUNNING tasks still exist.
	if (status == "RUNNING" || status == "INDEXING") && openTasks == 0 && newTotal > 0 {
		finalStatus := "COMPLETED"
		var errMessage sql.NullString
		if newFailed == newTotal {
			finalStatus = "FAILED"
			errMessage = sql.NullString{String: "All file transfers failed", Valid: true}
		} else if newFailed > 0 {
			finalStatus = "COMPLETED_WITH_ERRORS"
		}
		res, err := tx.Exec(`
			UPDATE migrations
			SET status = $1,
			    error_message = COALESCE($2, error_message)
			WHERE id = $3
			  AND status IN ('RUNNING', 'INDEXING')
		`, finalStatus, errMessage, migrationID)
		if err != nil {
			return err
		}
		if rows, rerr := res.RowsAffected(); rerr == nil && rows > 0 {
			if owner, oerr := GetMigrationOwnerID(tx, migrationID); oerr == nil {
				action := AuditMigrationCompleted
				if finalStatus == "FAILED" {
					action = AuditMigrationFailed
				}
				WriteAuditLog(tx, AuditEntry{
					UserID: sql.NullString{String: owner, Valid: true},
					Action: action,
					Target: migrationID,
					Details: json.RawMessage(fmt.Sprintf(`{"phase":"transfer","reconciled":true,"all_failed":%t,"partial":%t}`, newFailed == newTotal, newFailed > 0 && newFailed < newTotal)),
				})
			}
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

// CreateTask inserts a new task for migration or sync job
func CreateTask(db *sql.DB, t *Task) (string, error) {
	var migID, syncID sql.NullString
	if t.MigrationID != "" {
		migID = sql.NullString{String: t.MigrationID, Valid: true}
	}
	if t.SyncJobID != "" {
		syncID = sql.NullString{String: t.SyncJobID, Valid: true}
	}

	query := `
		INSERT INTO tasks (
			migration_id, sync_job_id, file_path, file_size, source_hash, status, resource_type, metadata
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at
	`
	err := db.QueryRow(
		query,
		migID, syncID, t.FilePath, t.FileSize, t.SourceHash, t.Status, t.ResourceType, t.Metadata,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)

	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// GetTask retrieves a single task by ID
func GetTask(db *sql.DB, id string) (*Task, error) {
	query := `
		SELECT id, migration_id, sync_job_id, file_path, file_size, source_hash, worker_hash, target_hash,
		       status, error_message, attempts, next_retry_at, created_at, updated_at, resource_type, metadata
		FROM tasks WHERE id = $1
	`
	var t Task
	var migID, syncID sql.NullString
	err := db.QueryRow(query, id).Scan(
		&t.ID, &migID, &syncID, &t.FilePath, &t.FileSize, &t.SourceHash, &t.WorkerHash, &t.TargetHash,
		&t.Status, &t.ErrorMessage, &t.Attempts, &t.NextRetryAt, &t.CreatedAt, &t.UpdatedAt, &t.ResourceType, &t.Metadata,
	)
	if err != nil {
		return nil, err
	}
	if migID.Valid {
		t.MigrationID = migID.String
	}
	if syncID.Valid {
		t.SyncJobID = syncID.String
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

// UpdateTaskFilePath updates a task's file_path (used when sanitization changes the target name)
func UpdateTaskFilePath(db *sql.DB, taskID, newFilePath string) error {
	_, err := db.Exec(`UPDATE tasks SET file_path = $1 WHERE id = $2`, newFilePath, taskID)
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

// IndexingError represents a per-folder error encountered during the resilient
// indexing phase. These are recorded (not fatal) so the migration can proceed and
// the skipped paths can be surfaced in the report.
type IndexingError struct {
	Path         string
	ResourceType string
	ErrorMessage string
	CreatedAt    time.Time
}

// IndexingErrorInput is the payload used to persist indexing errors.
type IndexingErrorInput struct {
	Path         string
	ResourceType string
	ErrorMessage string
}

// RecordIndexingErrors persists a batch of indexing errors for a migration in a
// single transaction so a mid-batch failure cannot leave a partial error set.
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
		INSERT INTO indexing_errors (migration_id, path, resource_type, error_message)
		VALUES ($1, $2, $3, $4)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, e := range errors {
		if _, err := stmt.Exec(migrationID, e.Path, e.ResourceType, e.ErrorMessage); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetIndexingErrorsForReport retrieves all indexing errors of a migration for reporting.
func GetIndexingErrorsForReport(db *sql.DB, migrationID string) ([]IndexingError, error) {
	query := `
		SELECT path, resource_type, error_message, created_at
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
		var e IndexingError
		if err := rows.Scan(&e.Path, &e.ResourceType, &e.ErrorMessage, &e.CreatedAt); err != nil {
			return nil, err
		}
		errs = append(errs, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return errs, nil
}

// DeleteIndexingErrors removes all indexing errors for a migration (used on re-index).
func DeleteIndexingErrors(db *sql.DB, migrationID string) error {
	_, err := db.Exec(`DELETE FROM indexing_errors WHERE migration_id = $1`, migrationID)
	return err
}

// ErrMigrationNotFailed is returned by ResetMigrationForReindex when the migration
// is not in a re-indexable terminal state (FAILED or COMPLETED_WITH_ERRORS, e.g. already re-indexing, or finished). It lets the API
// distinguish a benign concurrent re-trigger from a real error.
var ErrMigrationNotFailed = errors.New("migration is not in FAILED state")

// ResetMigrationForReindex clears tasks and indexing errors and resets counters so the
// shared indexer can re-run indexing for an existing FAILED or COMPLETED_WITH_ERRORS
// migration. The status flip to INDEXING is guarded by
// `WHERE status IN ('FAILED','COMPLETED_WITH_ERRORS')`, which also prevents a second
// concurrent re-index request from spawning a duplicate indexer (TOCTOU safe).
func ResetMigrationForReindex(db *sql.DB, ctx context.Context, migrationID string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(`DELETE FROM tasks WHERE migration_id = $1`, migrationID); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM indexing_errors WHERE migration_id = $1`, migrationID); err != nil {
		return err
	}
	res, err := tx.Exec(`
		UPDATE migrations
		SET status = 'INDEXING',
		    total_files = 0, total_bytes = 0,
		    processed_files = 0, processed_bytes = 0,
		    live_bytes = 0,
		    skipped_files = 0, failed_files = 0,
		    error_message = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND status IN ('FAILED', 'COMPLETED_WITH_ERRORS')
	`, migrationID)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrMigrationNotFailed
	}
	return tx.Commit()
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
// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (SQLSTATE 23505). Used to treat a duplicate-email insert as a
// benign "already registered" case instead of a server error.
func IsUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

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
		SELECT id, email, password_hash, display_name, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
			totp_enabled, totp_secret_encrypted, totp_backup_codes, totp_failed_attempts, totp_locked_until
		FROM users WHERE email = $1
	`
	var u User
	var mime sql.NullString
	var secret sql.NullString
	err := db.QueryRow(query, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &secret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil)
	if err != nil {
		return nil, err
	}
	u.AvatarMime = mime.String
	u.TotpSecretEnc = secret.String
	return &u, nil
}

// GetUserByID retrieves a user by UUID
func GetUserByID(db *sql.DB, id string) (*User, error) {
	query := `
		SELECT id, email, password_hash, display_name, role, active, must_change_password, avatar, avatar_mime, created_at, updated_at,
			totp_enabled, totp_secret_encrypted, totp_backup_codes, totp_failed_attempts, totp_locked_until,
			login_failed_attempts, login_locked_until
		FROM users WHERE id = $1
	`
	var u User
	var mime sql.NullString
	var secret sql.NullString
	err := db.QueryRow(query, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.Avatar, &mime, &u.CreatedAt, &u.UpdatedAt,
		&u.TotpEnabled, &secret, &u.TotpBackupCodes, &u.TotpFailedAttempts, &u.TotpLockedUntil,
		&u.LoginFailedAttempts, &u.LoginLockedUntil)
	if err != nil {
		return nil, err
	}
	u.AvatarMime = mime.String
	u.TotpSecretEnc = secret.String
	return &u, nil
}

// SetUserTOTPSecret stores the encrypted TOTP secret and resets 2FA state
// (disabled, no backup codes, no failed attempts, no lockout). Used by setup
// before the user verifies their first code.
func SetUserTOTPSecret(database *sql.DB, userID, encryptedSecret string) error {
	query := `
		UPDATE users
		SET totp_secret_encrypted = $1,
		    totp_enabled = FALSE,
		    totp_backup_codes = NULL,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, encryptedSecret, userID)
	return err
}

// EnableUserTOTP marks 2FA as enabled and stores the backup code hashes.
func EnableUserTOTP(database *sql.DB, userID string, backupCodeHashes StringArray) error {
	query := `
		UPDATE users
		SET totp_enabled = TRUE,
		    totp_backup_codes = $1,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, backupCodeHashes, userID)
	return err
}

// DisableUserTOTP disables 2FA and clears the secret and backup codes.
func DisableUserTOTP(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET totp_enabled = FALSE,
		    totp_secret_encrypted = NULL,
		    totp_backup_codes = NULL,
		    totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
}

// ReplaceUsedBackupCode overwrites the stored backup code hashes with the
// remaining set after one code has been consumed.
func ReplaceUsedBackupCode(database *sql.DB, userID string, remainingHashes StringArray) error {
	query := `
		UPDATE users
		SET totp_backup_codes = $1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $2
	`
	_, err := database.Exec(query, remainingHashes, userID)
	return err
}

// IncrementTOTPFailed atomically increments the failed-attempt counter. Once it
// reaches maxAttempts (5) the account is locked for lockDuration (15 min) and the
// counter is reset. Returns whether the account is now locked. The increment and
// lock decision happen in a single UPDATE ... RETURNING to avoid a read-then-write
// race under concurrent failed attempts.
func IncrementTOTPFailed(database *sql.DB, userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	lockUntil := time.Now().Add(lockDuration)
	query := `
		UPDATE users
		SET totp_failed_attempts = CASE
		        WHEN totp_failed_attempts + 1 >= $2 THEN 0
		        ELSE totp_failed_attempts + 1
		    END,
		    totp_locked_until = CASE
		        WHEN totp_failed_attempts + 1 >= $2 THEN $3
		        ELSE totp_locked_until
		    END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING (totp_locked_until IS NOT NULL AND totp_locked_until > CURRENT_TIMESTAMP)
	`
	var locked bool
	if err := database.QueryRow(query, userID, maxAttempts, lockUntil).Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

// ResetTOTPFailed clears the failed-attempt counter and lockout.
func ResetTOTPFailed(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET totp_failed_attempts = 0,
		    totp_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
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

// generateRandomPassword returns a cryptographically random, URL-safe password of
// the requested length (used for the bootstrap admin account).
func generateRandomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

// queryExecer is satisfied by both *sql.DB and *sql.Tx, letting audit helpers run
// inside or outside a transaction.
type queryExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// WriteAuditLog appends a single audit record. It is best-effort: failures are
// logged but never propagated, so auditing can never break the primary request.
func WriteAuditLog(database queryExecer, e AuditEntry) {
	details := "null"
	if len(e.Details) > 0 {
		details = string(e.Details)
	}
	_, err := database.Exec(
		`INSERT INTO audit_log (user_id, action, target, ip, details) VALUES ($1, $2, $3, $4, $5::jsonb)`,
		e.UserID, e.Action, e.Target, e.IP, details,
	)
	if err != nil {
		log.Printf("WriteAuditLog failed (action=%s): %v\n", e.Action, err)
	}
}

// GetMigrationOwnerID returns the owning user_id for a migration.
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

// CreateUserWithRole creates a user with an explicit role and optional
// must-change-password flag (used by the admin provisioning endpoint and the
// bootstrap admin). The caller is responsible for hashing the password.
func CreateUserWithRole(database *sql.DB, email, passwordHash, displayName, role string, mustChangePassword bool) (*User, error) {
	if !ValidRoles[role] {
		role = "USER"
	}
	query := `
		INSERT INTO users (email, password_hash, display_name, role, active, must_change_password)
		VALUES ($1, $2, $3, $4, TRUE, $5)
		RETURNING id, role, active, must_change_password, created_at, updated_at
	`
	var u User
	u.Email = email
	u.DisplayName = displayName
	err := database.QueryRow(query, email, passwordHash, displayName, role, mustChangePassword).
		Scan(&u.ID, &u.Role, &u.Active, &u.MustChangePassword, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserActive soft-activates or deactivates a user. On deactivation, any
// RUNNING/INDEXING migrations are paused and all of the user's schedules are
// disabled. On re-activation, the user's schedules are re-enabled.
func UpdateUserActive(database *sql.DB, id string, active bool) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE users SET active = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, active, id); err != nil {
		return err
	}

	if !active {
		// Pause in-flight migrations (the worker halts them on its next status check).
		if _, err := tx.Exec(
			`UPDATE migrations SET status = 'PAUSED', updated_at = CURRENT_TIMESTAMP WHERE user_id = $1 AND status IN ('RUNNING', 'INDEXING')`,
			id,
		); err != nil {
			return err
		}
		// Disable schedules so deferred jobs stop firing.
		if _, err := tx.Exec(`UPDATE schedules SET is_active = FALSE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id); err != nil {
			return err
		}
	} else {
		// Re-enable schedules on reactivation.
		if _, err := tx.Exec(`UPDATE schedules SET is_active = TRUE, updated_at = CURRENT_TIMESTAMP WHERE user_id = $1`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateUserRole changes a user's role (must be a valid role).
func UpdateUserRole(database *sql.DB, id, role string) error {
	if !ValidRoles[role] {
		return fmt.Errorf("invalid role %q", role)
	}
	_, err := database.Exec(`UPDATE users SET role = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, role, id)
	return err
}

// DeleteUser hard-deletes a user; dependent rows (migrations, tasks, schedules,
// tokens) cascade via ON DELETE CASCADE.
func DeleteUser(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM users WHERE id = $1`, id)
	return err
}

// CountActiveAdmins returns the number of currently active ADMIN users.
func CountActiveAdmins(database *sql.DB) (int, error) {
	var n int
	err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'ADMIN' AND active = TRUE`).Scan(&n)
	return n, err
}

// EnsureAdminUser idempotently guarantees an ADMIN account exists for the given
// email. If the email is absent, an ADMIN is created with a system-generated
// random password and must_change_password = TRUE; the generated password is
// returned so the caller can surface it exactly once (e.g. to stdout). If the
// account already exists, its role is promoted to ADMIN (password untouched).
func EnsureAdminUser(database *sql.DB, email, displayName string) (created bool, generatedPassword string, err error) {
	var existingID string
	var existingRole string
	err = database.QueryRow(`SELECT id, role FROM users WHERE email = $1`, email).Scan(&existingID, &existingRole)
	if err == sql.ErrNoRows {
		// Generate a strong random password (>= 24 chars).
		pass, genErr := generateRandomPassword(24)
		if genErr != nil {
			return false, "", genErr
		}
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(pass), 12)
		if hashErr != nil {
			return false, "", hashErr
		}
		dn := displayName
		if dn == "" {
			dn = email
		}
		u, createErr := CreateUserWithRole(database, email, string(hash), dn, "ADMIN", true)
		if createErr != nil {
			return false, "", createErr
		}
		if u.ID == "" {
			return false, "", fmt.Errorf("admin user creation returned empty id")
		}
		return true, pass, nil
	}
	if err != nil {
		return false, "", err
	}

	// Security: never silently promote an existing non-ADMIN account to ADMIN.
	// Otherwise an attacker who pre-registers the configured ADMIN_EMAIL via open
	// registration would be auto-escalated to ADMIN on the next bootstrap.
	// Idempotency across restarts is preserved only for an account that is
	// already an ADMIN (we just ensure it stays active).
	if existingRole != "ADMIN" {
		log.Printf("WARNING: bootstrap admin email %q already exists with role %q; refusing to auto-promote (possible pre-registration collision)", email, existingRole)
		return false, "", nil
	}
	if _, err := database.Exec(`UPDATE users SET active = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, existingID); err != nil {
		return false, "", err
	}
	return false, "", nil
}

// UserListParams filters/paginates the admin user listing.
type UserListParams struct {
	Page    int
	Limit   int
	Role    string
	Active  *bool
	Query   string
}

// ListUsers returns a paginated, password-free view of all users.
func ListUsers(database *sql.DB, p UserListParams) ([]User, int, error) {
	where := "TRUE"
	args := []interface{}{}
	idx := 1
	if p.Role != "" {
		where += fmt.Sprintf(" AND role = $%d", idx)
		args = append(args, p.Role)
		idx++
	}
	if p.Active != nil {
		where += fmt.Sprintf(" AND active = $%d", idx)
		args = append(args, *p.Active)
		idx++
	}
	if p.Query != "" {
		where += fmt.Sprintf(" AND (email ILIKE $%d OR display_name ILIKE $%d)", idx, idx+1)
		like := "%" + p.Query + "%"
		args = append(args, like, like)
		idx += 2
	}

	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	listArgs := append(append([]interface{}{}, args...), p.Limit, offset)
	query := `
		SELECT id, email, display_name, role, active, must_change_password, totp_enabled, created_at, updated_at
		FROM users WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	rows, err := database.Query(query, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Active, &u.MustChangePassword, &u.TotpEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, nil
}

// GlobalStats aggregates instance-wide counts for the admin overview.
type GlobalStats struct {
	TotalUsers         int            `json:"total_users"`
	ActiveUsers        int            `json:"active_users"`
	MigrationsByStatus map[string]int `json:"migrations_by_status"`
	TasksByStatus      map[string]int `json:"tasks_by_status"`
}

// GetGlobalStats computes the counts shown on the admin stats panel.
func GetGlobalStats(database *sql.DB) (*GlobalStats, error) {
	stats := &GlobalStats{
		MigrationsByStatus: map[string]int{},
		TasksByStatus:      map[string]int{},
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&stats.TotalUsers); err != nil {
		return nil, err
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM users WHERE active = TRUE`).Scan(&stats.ActiveUsers); err != nil {
		return nil, err
	}
	rows, err := database.Query(`SELECT status, COUNT(*) FROM migrations GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.MigrationsByStatus[status] = n
	}
	rows.Close()
	rows, err = database.Query(`SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stats.TasksByStatus[status] = n
	}
	rows.Close()
	return stats, nil
}

// AdminMigrationView is a migration row enriched with the owner's email for the
// admin-wide oversight view.
type AdminMigrationView struct {
	Migration
	OwnerEmail string `json:"owner_email"`
}

// MigrationListParams filters/paginates the admin-wide migration listing.
type MigrationListParams struct {
	Page  int
	Limit int
}

// ListAllMigrations returns every migration across all users (read-only oversight).
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

// AuditLogRow is a serialized audit-log entry for listing responses.
type AuditLogRow struct {
	ID        int64           `json:"id"`
	UserID    string          `json:"user_id"`
	Action    AuditAction     `json:"action"`
	Target    string          `json:"target"`
	IP        string          `json:"ip"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

// AuditLogParams filters/paginates the audit-log listing.
type AuditLogParams struct {
	Page    int
	Limit   int
	Action  string
	UserID  string
	Target  string
	From    string
	To      string
}

// ListAuditLog returns a paginated, filtered view of the audit log.
func ListAuditLog(database *sql.DB, p AuditLogParams) ([]AuditLogRow, int, error) {
	where := "TRUE"
	args := []interface{}{}
	idx := 1
	if p.Action != "" {
		where += fmt.Sprintf(" AND action = $%d", idx)
		args = append(args, p.Action)
		idx++
	}
	if p.UserID != "" {
		where += fmt.Sprintf(" AND user_id = $%d", idx)
		args = append(args, p.UserID)
		idx++
	}
	if p.Target != "" {
		where += fmt.Sprintf(" AND target = $%d", idx)
		args = append(args, p.Target)
		idx++
	}
	if p.From != "" {
		ft, ferr := parseAuditTime(p.From)
		if ferr != nil {
			return nil, 0, ferr
		}
		where += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, ft)
		idx++
	}
	if p.To != "" {
		tt, terr := parseAuditTime(p.To)
		if terr != nil {
			return nil, 0, terr
		}
		where += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, tt)
		idx++
	}

	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	listArgs := append(append([]interface{}{}, args...), p.Limit, offset)
	query := `
		SELECT id, user_id, action, target, ip, details, created_at
		FROM audit_log WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	rows, err := database.Query(query, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	entries := []AuditLogRow{}
	for rows.Next() {
		var e AuditLogRow
		var uid sql.NullString
		var details []byte
		if err := rows.Scan(&e.ID, &uid, &e.Action, &e.Target, &e.IP, &details, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		if uid.Valid {
			e.UserID = uid.String
		}
		if len(details) > 0 {
			e.Details = details
		} else {
			e.Details = json.RawMessage("null")
		}
		entries = append(entries, e)
	}
	return entries, total, nil
}

// parseAuditTime normalizes a caller-supplied timestamp filter into an RFC3339
// string suitable for a Postgres timestamptz comparison. It accepts RFC3339 as
// well as a plain date (YYYY-MM-DD) from the audit-log date inputs. Invalid
// values are rejected so we never pass an unchecked string to the query.
func parseAuditTime(s string) (string, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid time filter value %q", s)
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

// DeleteAllRefreshTokensForUser revokes every active session for a user. Used
// after a password or email change so a previously compromised session can no
// longer mint new access tokens.
func DeleteAllRefreshTokensForUser(db *sql.DB, userID string) error {
	query := `DELETE FROM refresh_tokens WHERE user_id = $1`
	_, err := db.Exec(query, userID)
	return err
}

// IncrementLoginFailed atomically increments the failed-login counter. Once it
// reaches maxAttempts the account is locked for lockDuration and the counter is
// reset. Returns whether the account is now locked. Mirrors IncrementTOTPFailed.
func IncrementLoginFailed(database *sql.DB, userID string, maxAttempts int, lockDuration time.Duration) (bool, error) {
	lockUntil := time.Now().Add(lockDuration)
	query := `
		UPDATE users
		SET login_failed_attempts = CASE
		        WHEN login_failed_attempts + 1 >= $2 THEN 0
		        ELSE login_failed_attempts + 1
		    END,
		    login_locked_until = CASE
		        WHEN login_failed_attempts + 1 >= $2 THEN $3
		        ELSE login_locked_until
		    END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
		RETURNING (login_locked_until IS NOT NULL AND login_locked_until > CURRENT_TIMESTAMP)
	`
	var locked bool
	if err := database.QueryRow(query, userID, maxAttempts, lockUntil).Scan(&locked); err != nil {
		return false, err
	}
	return locked, nil
}

// ResetLoginFailed clears the failed-login counter and lockout.
func ResetLoginFailed(database *sql.DB, userID string) error {
	query := `
		UPDATE users
		SET login_failed_attempts = 0,
		    login_locked_until = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`
	_, err := database.Exec(query, userID)
	return err
}

// CountActiveMigrationsForUser returns the number of non-terminal migrations a
// user currently has (INDEXING, RUNNING, PAUSED, PAUSED_CONNECTION_LOSS). Used
// to enforce a per-user concurrency quota and prevent resource exhaustion.
func CountActiveMigrationsForUser(db *sql.DB, userID string) (int, error) {
	query := `
		SELECT COUNT(*) FROM migrations
		WHERE user_id = $1
		  AND status IN ('INDEXING', 'RUNNING', 'PAUSED', 'PAUSED_CONNECTION_LOSS')
	`
	var count int
	err := db.QueryRow(query, userID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
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
func ResetFailedTasksForRetry(db *sql.DB, ctx context.Context, migrationID string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 1. Count FAILED tasks and sum their file sizes
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

	// 2. Reset these tasks
	_, err = tx.Exec(`
		UPDATE tasks 
		SET status = 'PENDING', attempts = 0, next_retry_at = NULL, worker_hash = NULL, error_message = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE migration_id = $1 AND status = 'FAILED'
	`, migrationID)
	if err != nil {
		return 0, err
	}

	// 3. Adjust the migration (only in a terminal state)
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
type UserSMTPSettings struct {
	UserID             string    `json:"user_id"`
	SMTPHost           string    `json:"smtp_host"`
	SMTPPort           int       `json:"smtp_port"`
	SMTPUsername       string    `json:"smtp_username"`
	SMTPPasswordEnc    string    `json:"-"`
	SMTPFromEmail      string    `json:"smtp_from_email"`
	SMTPFromName       string    `json:"smtp_from_name"`
	SMTPEncryption     string    `json:"smtp_encryption"`
	NotifyOnCompletion bool      `json:"notify_on_completion"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type PasswordResetToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
}

type EmailChangeToken struct {
	TokenHash string    `json:"token_hash"`
	UserID    string    `json:"user_id"`
	NewEmail  string    `json:"new_email"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
}

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

// ============================================================================
// Connection Profiles (reusable source/target credentials per user)
// ============================================================================

// ConnectionProfile describes a reusable connection (usable as source OR target).
// OAuth-based profiles store only the (encrypted) refresh token + oauth_user;
// password-based profiles store username + encrypted password; 'local' stores neither.
type ConnectionProfile struct {
	ID                     string         `json:"id"`
	UserID                 string         `json:"user_id"`
	Name                   string         `json:"name"`
	Provider               string         `json:"provider"`
	URL                    string         `json:"url,omitempty"`
	Username               string         `json:"username,omitempty"`
	PasswordEncrypted     string         `json:"-"`
	RefreshTokenEncrypted string         `json:"-"`
	TokenExpiresAt        sql.NullTime   `json:"token_expires_at,omitempty"`
	OAuthUser             string         `json:"oauth_user,omitempty"`
	CreatedAt             time.Time      `json:"created_at"`
	UpdatedAt             time.Time      `json:"updated_at"`
}

// ConnectionProfilePublic is the subset of ConnectionProfile that may be sent to
// the client: secrets are never serialized, and we expose only whether a
// password is stored (so the UI can render the right fields).
type ConnectionProfilePublic struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Provider        string       `json:"provider"`
	URL             string       `json:"url,omitempty"`
	Username        string       `json:"username,omitempty"`
	HasPassword     bool         `json:"has_password"`
	TokenExpiresAt sql.NullTime `json:"token_expires_at,omitempty"`
	OAuthUser      string       `json:"oauth_user,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// ToPublic strips secret fields and derives HasPassword.
func (p *ConnectionProfile) ToPublic() ConnectionProfilePublic {
	return ConnectionProfilePublic{
		ID:              p.ID,
		Name:            p.Name,
		Provider:        p.Provider,
		URL:             p.URL,
		Username:        p.Username,
		HasPassword:     p.PasswordEncrypted != "",
		TokenExpiresAt: p.TokenExpiresAt,
		OAuthUser:      p.OAuthUser,
		CreatedAt:       p.CreatedAt,
		UpdatedAt:       p.UpdatedAt,
	}
}

// CreateConnectionProfile inserts a new profile and returns its UUID.
func CreateConnectionProfile(database *sql.DB, p *ConnectionProfile) (string, error) {
	query := `
		INSERT INTO connection_profiles (
			user_id, name, provider, url, username,
			password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`
	err := database.QueryRow(
		query,
		p.UserID, p.Name, p.Provider, p.URL, p.Username,
		p.PasswordEncrypted, p.RefreshTokenEncrypted, p.TokenExpiresAt, p.OAuthUser,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// GetConnectionProfile loads a single profile by ID (regardless of owner; the
// caller must enforce ownership via VerifyProfileOwnership before use).
func GetConnectionProfile(database *sql.DB, id string) (*ConnectionProfile, error) {
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user,
		       created_at, updated_at
		FROM connection_profiles WHERE id = $1
	`
	var p ConnectionProfile
	err := database.QueryRow(query, id).Scan(
		&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
		&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetConnectionProfiles lists the user's profiles.
func GetConnectionProfiles(database *sql.DB, userID, _ string) ([]ConnectionProfile, error) {
	args := []interface{}{userID}
	query := `
		SELECT id, user_id, name, provider, url, username,
		       password_encrypted, refresh_token_encrypted, token_expires_at, oauth_user,
		       created_at, updated_at
		FROM connection_profiles
		WHERE user_id = $1
	`
	query += ` ORDER BY name ASC`

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []ConnectionProfile
	for rows.Next() {
		var p ConnectionProfile
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Name, &p.Provider, &p.URL, &p.Username,
			&p.PasswordEncrypted, &p.RefreshTokenEncrypted, &p.TokenExpiresAt, &p.OAuthUser,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// UpdateConnectionProfile applies a partial update to an existing profile.
// Empty-string fields are left unchanged; pass a pointer sentinel via the
// UpdateConnectionProfileInput wrapper so callers can distinguish "set to empty"
// from "do not change". For the password/refresh token, nil means "do not
// change", while a non-nil *string changes (or clears) the value.
type UpdateConnectionProfileInput struct {
	Name                   *string
	Provider               *string
	URL                    *string
	Username               *string
	PasswordEncrypted      *string // nil = unchanged; "" = clear
	RefreshTokenEncrypted  *string // nil = unchanged; "" = clear
	TokenExpiresAt         *time.Time
	OAuthUser              *string
}

func UpdateConnectionProfile(database *sql.DB, id string, in UpdateConnectionProfileInput) error {
	setClauses := []string{}
	args := []interface{}{}
	idx := 1

	if in.Name != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", idx))
		args = append(args, *in.Name)
	}
	if in.Provider != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("provider = $%d", idx))
		args = append(args, *in.Provider)
	}
	if in.URL != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("url = $%d", idx))
		args = append(args, *in.URL)
	}
	if in.Username != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("username = $%d", idx))
		args = append(args, *in.Username)
	}
	if in.PasswordEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("password_encrypted = $%d", idx))
		args = append(args, *in.PasswordEncrypted)
	}
	if in.RefreshTokenEncrypted != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("refresh_token_encrypted = $%d", idx))
		args = append(args, *in.RefreshTokenEncrypted)
	}
	if in.TokenExpiresAt != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("token_expires_at = $%d", idx))
		args = append(args, *in.TokenExpiresAt)
	}
	if in.OAuthUser != nil {
		idx++
		setClauses = append(setClauses, fmt.Sprintf("oauth_user = $%d", idx))
		args = append(args, *in.OAuthUser)
	}

	if len(setClauses) == 0 {
		return nil
	}

	query := `UPDATE connection_profiles SET ` + strings.Join(setClauses, ", ") + ` WHERE id = $1`
	args = append([]interface{}{id}, args...)
	_, err := database.Exec(query, args...)
	return err
}

// DeleteConnectionProfile removes a profile by ID.
func DeleteConnectionProfile(database *sql.DB, id string) error {
	_, err := database.Exec(`DELETE FROM connection_profiles WHERE id = $1`, id)
	return err
}

// VerifyProfileOwnership reports whether the profile belongs to the user.
// Returns EXISTS semantics so a non-owned ID yields false (not an error),
// letting callers return 404 Not Found without leaking existence.
func VerifyProfileOwnership(database *sql.DB, profileID, userID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM connection_profiles WHERE id = $1 AND user_id = $2)`
	var exists bool
	err := database.QueryRow(query, profileID, userID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// ============================================================================
// User SMTP Settings
// ============================================================================

func GetUserSMTPSettings(db *sql.DB, userID string) (*UserSMTPSettings, error) {
	query := `
		SELECT user_id, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
		       smtp_from_email, smtp_from_name, smtp_encryption, notify_on_completion, updated_at
		FROM user_smtp_settings WHERE user_id = $1
	`
	var s UserSMTPSettings
	err := db.QueryRow(query, userID).Scan(
		&s.UserID, &s.SMTPHost, &s.SMTPPort, &s.SMTPUsername, &s.SMTPPasswordEnc,
		&s.SMTPFromEmail, &s.SMTPFromName, &s.SMTPEncryption, &s.NotifyOnCompletion, &s.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func UpsertUserSMTPSettings(db *sql.DB, s *UserSMTPSettings) error {
	query := `
		INSERT INTO user_smtp_settings (
			user_id, smtp_host, smtp_port, smtp_username, smtp_password_encrypted,
			smtp_from_email, smtp_from_name, smtp_encryption, notify_on_completion
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id) DO UPDATE SET
			smtp_host = EXCLUDED.smtp_host,
			smtp_port = EXCLUDED.smtp_port,
			smtp_username = EXCLUDED.smtp_username,
			smtp_password_encrypted = EXCLUDED.smtp_password_encrypted,
			smtp_from_email = EXCLUDED.smtp_from_email,
			smtp_from_name = EXCLUDED.smtp_from_name,
			smtp_encryption = EXCLUDED.smtp_encryption,
			notify_on_completion = EXCLUDED.notify_on_completion,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(query,
		s.UserID, s.SMTPHost, s.SMTPPort, s.SMTPUsername, s.SMTPPasswordEnc,
		s.SMTPFromEmail, s.SMTPFromName, s.SMTPEncryption, s.NotifyOnCompletion,
	)
	return err
}

// ============================================================================
// Password Reset Tokens
// ============================================================================

func CreatePasswordResetToken(db *sql.DB, tokenHash, userID string, expiresAt time.Time) error {
	query := `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	_, err := db.Exec(query, tokenHash, userID, expiresAt)
	return err
}

func GetPasswordResetToken(db *sql.DB, tokenHash string) (*PasswordResetToken, error) {
	query := `
		SELECT token_hash, user_id, expires_at, used, created_at
		FROM password_reset_tokens WHERE token_hash = $1
	`
	var t PasswordResetToken
	err := db.QueryRow(query, tokenHash).Scan(&t.TokenHash, &t.UserID, &t.ExpiresAt, &t.Used, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ClaimPasswordResetToken atomically validates and claims a password reset token.
// It checks that the token exists, is not used, and is not expired, then marks it used
// and updates the user's password in a single transaction to prevent TOCTOU races.
// Returns the user ID on success, or sql.ErrNoRows if the token is invalid/expired/used.
func ClaimPasswordResetToken(db *sql.DB, ctx context.Context, tokenHash, newPasswordHash string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	// Atomically claim the token: must be unused and not expired
	var userID string
	err = tx.QueryRow(`
		UPDATE password_reset_tokens
		SET used = TRUE
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
		RETURNING user_id
	`, tokenHash).Scan(&userID)
	if err != nil {
		return "", err // sql.ErrNoRows if invalid/expired/used
	}

	// Update the password within the same transaction
	if _, err := tx.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, newPasswordHash, userID); err != nil {
		return "", err
	}

	// Invalidate all refresh tokens for this user within the same transaction
	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = $1`, userID); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

func DeleteExpiredPasswordResetTokens(db *sql.DB) error {
	query := `DELETE FROM password_reset_tokens WHERE expires_at < NOW() OR used = TRUE`
	_, err := db.Exec(query)
	return err
}

// ============================================================================
// Email Change Tokens
// ============================================================================

// CreateEmailChangeToken stores a new email-change token. Any previously open
// token for the same user is removed first so a user can only have one pending
// email-change request at a time.
func CreateEmailChangeToken(db *sql.DB, tokenHash, userID, newEmail string, expiresAt time.Time) error {
	if _, err := db.Exec(`DELETE FROM email_change_tokens WHERE user_id = $1`, userID); err != nil {
		return err
	}
	query := `
		INSERT INTO email_change_tokens (token_hash, user_id, new_email, expires_at)
		VALUES ($1, $2, $3, $4)
	`
	_, err := db.Exec(query, tokenHash, userID, newEmail, expiresAt)
	return err
}

// ErrEmailTaken is returned by ClaimEmailChangeToken when the requested new
// email address is already in use by a different user at confirm time.
var ErrEmailTaken = errors.New("email already taken")

// ClaimEmailChangeToken atomically validates and claims an email-change token,
// then updates the user's email address inside the same transaction. The
// availability of the new email is checked BEFORE the token is marked used, so a
// taken address does not silently consume the token. On success it invalidates
// all refresh tokens for the user (forces re-login, like password reset).
// Returns the new email on success, ErrEmailTaken if the address is taken, or
// sql.ErrNoRows if the token is invalid/expired/used.
func ClaimEmailChangeToken(db *sql.DB, ctx context.Context, tokenHash string) (userID, newEmail string, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	// Read the token without consuming it yet, so we can reject a taken address
	// without marking the token used.
	var uid, newMail string
	err = tx.QueryRow(`
		SELECT user_id, new_email
		FROM email_change_tokens
		WHERE token_hash = $1 AND used = FALSE AND expires_at > NOW()
	`, tokenHash).Scan(&uid, &newMail)
	if err != nil {
		return "", "", err // sql.ErrNoRows if invalid/expired/used
	}

	// Ensure the new email is still free for this user BEFORE consuming the token.
	var taken string
	err = tx.QueryRow(`SELECT id FROM users WHERE email = $1 AND id <> $2 LIMIT 1`, newMail, uid).Scan(&taken)
	if err == nil {
		return "", "", ErrEmailTaken
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}

	// Atomically claim the token now that the address is confirmed free.
	if _, err := tx.Exec(`
		UPDATE email_change_tokens
		SET used = TRUE
		WHERE token_hash = $1
	`, tokenHash); err != nil {
		return "", "", err
	}

	if _, err := tx.Exec(`UPDATE users SET email = $1 WHERE id = $2`, newMail, uid); err != nil {
		return "", "", err
	}

	if _, err := tx.Exec(`DELETE FROM refresh_tokens WHERE user_id = $1`, uid); err != nil {
		return "", "", err
	}

	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return uid, newMail, nil
}

func DeleteExpiredEmailChangeTokens(db *sql.DB) error {
	query := `DELETE FROM email_change_tokens WHERE expires_at < NOW() OR used = TRUE`
	_, err := db.Exec(query)
	return err
}

// ============================================================================
// Migration Email Sent Flag
// ============================================================================

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

// LockPendingEmailNotifications claims a single pending completion-email
// notification and returns it together with the open *sql.Tx that holds the
// SELECT ... FOR UPDATE SKIP LOCKED row lock. The caller is responsible for
// sending the mail and then either marking it sent (MarkMigrationEmailSentTx)
// and committing, or rolling back on a transient failure so the row is retried
// on the next tick.
//
// Holding the row lock across the claim→send window gives two guarantees:
//   - No two workers can ever claim the same migration's mail (SKIP LOCKED),
//     so there are no duplicate sends.
//   - If this worker crashes after the claim but before the send completes,
//     the database releases the lock on connection drop and the row stays
//     email_sent = FALSE, so it is retried instead of being silently lost
//     (the previous design marked the row sent inside the claim transaction
//     and could lose a mail on a crash between commit and SMTP).
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

// MarkMigrationEmailSentTx marks a migration's completion email as sent inside
// the transaction returned by LockPendingEmailNotifications, so the mark is
// atomic with the (successful) send that the caller performs beforehand.
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
