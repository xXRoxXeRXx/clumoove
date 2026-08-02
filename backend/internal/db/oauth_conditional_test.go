package db

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func setupOAuthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping OAuth conditional DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatalf("ping db: %v", err)
	}

	if _, err := database.Exec(`
		CREATE TEMP TABLE migrations (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			source_provider TEXT NOT NULL,
			target_provider TEXT NOT NULL,
			source_password_encrypted TEXT,
			source_refresh_token_encrypted TEXT,
			source_token_expires_at TIMESTAMP WITH TIME ZONE,
			target_password_encrypted TEXT,
			target_refresh_token_encrypted TEXT,
			target_token_expires_at TIMESTAMP WITH TIME ZONE,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TEMP TABLE sync_jobs (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			source_provider TEXT NOT NULL,
			target_provider TEXT NOT NULL,
			source_password_encrypted TEXT,
			source_refresh_token_encrypted TEXT,
			source_token_expires_at TIMESTAMP WITH TIME ZONE,
			target_password_encrypted TEXT,
			target_refresh_token_encrypted TEXT,
			target_token_expires_at TIMESTAMP WITH TIME ZONE,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TEMP TABLE connection_profiles (
			id TEXT PRIMARY KEY,
			password_encrypted TEXT,
			refresh_token_encrypted TEXT,
			token_expires_at TIMESTAMP WITH TIME ZONE,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		);
	`); err != nil {
		database.Close()
		t.Fatalf("create temp tables: %v", err)
	}

	t.Cleanup(func() {
		database.Close()
	})

	return database
}

func TestErrOAuthTokenConflictSentinel(t *testing.T) {
	if ErrOAuthTokenConflict == nil {
		t.Fatal("ErrOAuthTokenConflict sentinel error is nil")
	}
	if ErrOAuthTokenConflict.Error() != "oauth token update conflict: persisted token changed concurrently" {
		t.Errorf("unexpected error string: %v", ErrOAuthTokenConflict)
	}
}

// A migration must retain OAuth credentials from creation through the worker's
// GetMigration lookup. The worker refreshes after that lookup, so dropping
// either set of columns makes every OAuth provider fail at its first 401.
func TestCreateMigrationPersistsOAuthTokens(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping migration OAuth persistence test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TEMP TABLE migrations (
			id TEXT PRIMARY KEY DEFAULT 'migration-oauth-persistence', user_id TEXT,
			source_url TEXT, source_username TEXT, source_password_encrypted TEXT, source_provider TEXT,
			source_refresh_token_encrypted TEXT, source_token_expires_at TIMESTAMP WITH TIME ZONE,
			target_url TEXT, target_username TEXT, target_password_encrypted TEXT, target_provider TEXT,
			target_refresh_token_encrypted TEXT, target_token_expires_at TIMESTAMP WITH TIME ZONE,
			status TEXT, conflict_strategy TEXT, total_files INT DEFAULT 0, total_bytes BIGINT DEFAULT 0,
			processed_files INT DEFAULT 0, processed_bytes BIGINT DEFAULT 0, live_bytes BIGINT DEFAULT 0,
			skipped_files INT DEFAULT 0, failed_files INT DEFAULT 0, error_message TEXT,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			target_dir TEXT, threads INT, bandwidth_limit_mbps INT, picker_session_id TEXT,
			selected_paths JSONB, selected_calendars JSONB, selected_contacts JSONB
		)`); err != nil {
		t.Fatalf("create temp migrations table: %v", err)
	}

	expiresAt := time.Now().Add(time.Hour).Truncate(time.Second)
	migration := &Migration{
		UserID:                      sql.NullString{String: "user-1", Valid: true},
		SourceURL:                   "oauth://onedrive",
		SourceUsername:              "source-user",
		SourcePasswordEncrypted:     "source-access",
		SourceProvider:              "onedrive",
		SourceRefreshTokenEncrypted: sql.NullString{String: "source-refresh", Valid: true},
		SourceTokenExpiresAt:        sql.NullTime{Time: expiresAt, Valid: true},
		TargetURL:                   "https://target.example",
		TargetUsername:              "target-user",
		TargetPasswordEncrypted:     "target-access",
		TargetProvider:              "google",
		TargetRefreshTokenEncrypted: sql.NullString{String: "target-refresh", Valid: true},
		TargetTokenExpiresAt:        sql.NullTime{Time: expiresAt, Valid: true},
		Status:                      "INDEXING",
		ConflictStrategy:            "SKIP",
		TargetDir:                   "/",
		Threads:                     1,
	}
	if _, err := CreateMigration(database, migration); err != nil {
		t.Fatalf("create migration: %v", err)
	}

	stored, err := GetMigration(database, migration.ID)
	if err != nil {
		t.Fatalf("get migration: %v", err)
	}
	if !stored.SourceRefreshTokenEncrypted.Valid || stored.SourceRefreshTokenEncrypted.String != "source-refresh" ||
		!stored.TargetRefreshTokenEncrypted.Valid || stored.TargetRefreshTokenEncrypted.String != "target-refresh" {
		t.Fatalf("OAuth refresh tokens were not retained: source=%+v target=%+v", stored.SourceRefreshTokenEncrypted, stored.TargetRefreshTokenEncrypted)
	}
	if !stored.SourceTokenExpiresAt.Valid || !stored.TargetTokenExpiresAt.Valid {
		t.Fatalf("OAuth token expiry was not retained: source=%+v target=%+v", stored.SourceTokenExpiresAt, stored.TargetTokenExpiresAt)
	}
}

func TestConditionalOAuthTokenUpdate(t *testing.T) {
	database := setupOAuthTestDB(t)

	now := time.Now().Truncate(time.Second)

	// Seed sync job
	_, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, source_provider, target_provider, source_password_encrypted, source_refresh_token_encrypted, source_token_expires_at)
		VALUES ('sync-1', 'RUNNING', 'google', 'nextcloud', 'old-pass', 'old-refresh', $1)
	`, now.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("seed sync job: %v", err)
	}

	// 1. Updating with matching expected refresh token should succeed and overwrite tokens
	laterExp := now.Add(2 * time.Hour)
	err = UpdateSyncJobOAuthTokens(database, "sync-1", "source", "new-pass-1", "new-refresh-1", laterExp, "old-refresh")
	if err != nil {
		t.Fatalf("UpdateSyncJobOAuthTokens failed: %v", err)
	}

	var curPass, curRefresh string
	var curExp time.Time
	err = database.QueryRow(`SELECT source_password_encrypted, source_refresh_token_encrypted, source_token_expires_at FROM sync_jobs WHERE id = 'sync-1'`).Scan(&curPass, &curRefresh, &curExp)
	if err != nil {
		t.Fatalf("query sync job: %v", err)
	}

	if curPass != "new-pass-1" || curRefresh != "new-refresh-1" {
		t.Errorf("expected new-pass-1 / new-refresh-1, got pass=%q refresh=%q", curPass, curRefresh)
	}

	// 2. Updating with mismatched expected refresh token (concurrent update occurred) must return ErrOAuthTokenConflict
	earlierExp := now.Add(30 * time.Minute)
	err = UpdateSyncJobOAuthTokens(database, "sync-1", "source", "stale-pass", "stale-refresh", earlierExp, "stale-old-token")
	if err == nil {
		t.Fatalf("expected ErrOAuthTokenConflict when expectedRefreshTokenEncrypted mismatches, got nil")
	}
	if err != ErrOAuthTokenConflict {
		t.Fatalf("expected ErrOAuthTokenConflict, got %v", err)
	}

	// Ensure DB row was NOT modified by the mismatched call
	err = database.QueryRow(`SELECT source_password_encrypted, source_refresh_token_encrypted FROM sync_jobs WHERE id = 'sync-1'`).Scan(&curPass, &curRefresh)
	if err != nil {
		t.Fatalf("query sync job after stale: %v", err)
	}

	if curPass != "new-pass-1" || curRefresh != "new-refresh-1" {
		t.Errorf("CAS failed: mismatched token update overwrote DB tokens")
	}
}

func TestConditionalConnectionProfileOAuthTokenUpdate(t *testing.T) {
	database := setupOAuthTestDB(t)
	now := time.Now().Truncate(time.Second)
	if _, err := database.Exec(`
		INSERT INTO connection_profiles (id, password_encrypted, refresh_token_encrypted, token_expires_at)
		VALUES ('onedrive-profile', 'old-access', 'old-refresh', $1)
	`, now); err != nil {
		t.Fatal(err)
	}

	expiresAt := now.Add(time.Hour)
	if err := UpdateConnectionProfileOAuthTokens(database, "onedrive-profile", "new-access", "new-refresh", expiresAt, "old-refresh"); err != nil {
		t.Fatal(err)
	}
	var access, refresh string
	if err := database.QueryRow(`SELECT password_encrypted, refresh_token_encrypted FROM connection_profiles WHERE id = 'onedrive-profile'`).Scan(&access, &refresh); err != nil {
		t.Fatal(err)
	}
	if access != "new-access" || refresh != "new-refresh" {
		t.Fatalf("persisted tokens = %q, %q; want new values", access, refresh)
	}
	if err := UpdateConnectionProfileOAuthTokens(database, "onedrive-profile", "stale-access", "stale-refresh", expiresAt, "old-refresh"); err != ErrOAuthTokenConflict {
		t.Fatalf("stale update error = %v, want ErrOAuthTokenConflict", err)
	}
}

func TestGetExpiringOAuthSyncJobs(t *testing.T) {
	database := setupOAuthTestDB(t)

	now := time.Now()

	// Seed one expiring sync job (expires in 5 minutes) and one non-expiring sync job (expires in 2 hours)
	_, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, source_provider, target_provider, source_refresh_token_encrypted, source_token_expires_at)
		VALUES
		  ('expiring-sync', 'RUNNING', 'google', 'nextcloud', 'refresh-token-1', $1),
		  ('fresh-sync', 'RUNNING', 'dropbox', 'nextcloud', 'refresh-token-2', $2)
	`, now.Add(5*time.Minute), now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("seed sync jobs: %v", err)
	}

	expiring, err := GetExpiringOAuthSyncJobs(database)
	if err != nil {
		t.Fatalf("GetExpiringOAuthSyncJobs failed: %v", err)
	}

	if len(expiring) != 1 {
		t.Fatalf("expected 1 expiring sync job, got %d", len(expiring))
	}

	if expiring[0].SyncJobID != "expiring-sync" || expiring[0].Role != "source" || expiring[0].Provider != "google" {
		t.Errorf("unexpected expiring entry: %+v", expiring[0])
	}
}
