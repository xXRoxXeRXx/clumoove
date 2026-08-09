package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// setupTestDB connects to a real PostgreSQL (via DATABASE_URL) and creates the
// shared schema baseline for database integration tests. It skips tests when no
// DATABASE_URL is configured, so environments without a database still pass.
func setupTestDB(t *testing.T) *sql.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping database integration test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			language TEXT NOT NULL DEFAULT 'en',
			role TEXT NOT NULL DEFAULT 'USER',
			active BOOLEAN NOT NULL DEFAULT TRUE,
			must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS refresh_tokens (
			token_hash TEXT PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS email_change_tokens (
			token_hash TEXT PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			new_email TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			used BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'IDLE',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS sync_jobs (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'IDLE',
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE,
			status TEXT NOT NULL DEFAULT 'PENDING',
			worker_hash TEXT,
			next_retry_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS schedules (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}
	for _, statement := range []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_backup_codes JSONB`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_failed_attempts INT NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_locked_until TIMESTAMPTZ`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("add TOTP test column: %v", err)
		}
	}

	// Ensure a clean slate for the test.
	if _, err := db.Exec(`DELETE FROM email_change_tokens; DELETE FROM refresh_tokens; DELETE FROM tasks; DELETE FROM schedules; DELETE FROM sync_jobs; DELETE FROM migrations; DELETE FROM users;`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM email_change_tokens; DELETE FROM refresh_tokens; DELETE FROM tasks; DELETE FROM schedules; DELETE FROM sync_jobs; DELETE FROM migrations; DELETE FROM users;`)
		_ = db.Close()
	})
	return db
}

func createTestUser(t *testing.T, db *sql.DB, email string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`INSERT INTO users (email) VALUES ($1) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatalf("create user %q: %v", email, err)
	}
	return id
}

func insertChangeToken(t *testing.T, db *sql.DB, hash, userID, newEmail string, expiresAt time.Time) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO email_change_tokens (token_hash, user_id, new_email, expires_at) VALUES ($1, $2, $3, $4)`,
		hash, userID, newEmail, expiresAt,
	)
	if err != nil {
		t.Fatalf("insert token %q: %v", hash, err)
	}
}

func userEmail(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id = $1`, userID).Scan(&email); err != nil {
		t.Fatalf("get email: %v", err)
	}
	return email
}

func tokenUsed(t *testing.T, db *sql.DB, hash string) bool {
	t.Helper()
	var used bool
	err := db.QueryRow(`SELECT used FROM email_change_tokens WHERE token_hash = $1`, hash).Scan(&used)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("get token used: %v", err)
	}
	return used
}

func TestClaimEmailChangeToken(t *testing.T) {
	db := setupTestDB(t)

	t.Run("valid claim updates email and invalidates refresh tokens", func(t *testing.T) {
		uid := createTestUser(t, db, "old@example.com")
		// Pre-existing session that must be wiped on email change.
		_, err := db.Exec(`INSERT INTO refresh_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, NOW() + INTERVAL '1 hour')`, "rt-old", uid)
		if err != nil {
			t.Fatalf("insert refresh token: %v", err)
		}
		insertChangeToken(t, db, "tok-valid", uid, "new@example.com", time.Now().Add(4*time.Hour))

		gotUID, gotEmail, err := ClaimEmailChangeToken(db, context.Background(), "tok-valid")
		if err != nil {
			t.Fatalf("expected success, got error: %v", err)
		}
		if gotUID != uid {
			t.Errorf("userID = %q, want %q", gotUID, uid)
		}
		if gotEmail != "new@example.com" {
			t.Errorf("newEmail = %q, want %q", gotEmail, "new@example.com")
		}
		if email := userEmail(t, db, uid); email != "new@example.com" {
			t.Errorf("user email = %q, want %q", email, "new@example.com")
		}
		if !tokenUsed(t, db, "tok-valid") {
			t.Error("token should be marked used")
		}
		var rtCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`, uid).Scan(&rtCount); err != nil {
			t.Fatalf("count refresh tokens: %v", err)
		}
		if rtCount != 0 {
			t.Errorf("expected refresh tokens to be deleted, got %d", rtCount)
		}
	})

	t.Run("expired token returns ErrNoRows", func(t *testing.T) {
		uid := createTestUser(t, db, "expired-user@example.com")
		insertChangeToken(t, db, "tok-expired", uid, "fresh@example.com", time.Now().Add(-1*time.Hour))

		_, _, err := ClaimEmailChangeToken(db, context.Background(), "tok-expired")
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
		if email := userEmail(t, db, uid); email != "expired-user@example.com" {
			t.Errorf("email changed despite expired token: %q", email)
		}
	})

	t.Run("reused token returns ErrNoRows and does not change email again", func(t *testing.T) {
		uid := createTestUser(t, db, "reuse-user@example.com")
		insertChangeToken(t, db, "tok-reuse", uid, "reuse-new@example.com", time.Now().Add(4*time.Hour))

		if _, _, err := ClaimEmailChangeToken(db, context.Background(), "tok-reuse"); err != nil {
			t.Fatalf("first claim failed: %v", err)
		}

		// Second attempt with the same (now used) token.
		_, _, err := ClaimEmailChangeToken(db, context.Background(), "tok-reuse")
		if err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows on reuse, got %v", err)
		}
		if email := userEmail(t, db, uid); email != "reuse-new@example.com" {
			t.Errorf("email unexpectedly changed on reuse: %q", email)
		}
	})

	t.Run("email already taken returns ErrEmailTaken and leaves token reusable", func(t *testing.T) {
		owner := createTestUser(t, db, "owner@example.com")
		// A second user already holding the target address.
		createTestUser(t, db, "taken@example.com")
		insertChangeToken(t, db, "tok-taken", owner, "taken@example.com", time.Now().Add(4*time.Hour))

		_, _, err := ClaimEmailChangeToken(db, context.Background(), "tok-taken")
		if err == nil {
			t.Fatal("expected ErrEmailTaken, got nil")
		}
		if err != ErrEmailTaken {
			t.Fatalf("expected ErrEmailTaken, got %v", err)
		}
		// The token must NOT be consumed, so the user can retry with another address.
		if tokenUsed(t, db, "tok-taken") {
			t.Error("token must remain unused when email is taken")
		}
		if email := userEmail(t, db, owner); email != "owner@example.com" {
			t.Errorf("owner email changed despite conflict: %q", email)
		}
	})

	t.Run("unknown token returns ErrNoRows", func(t *testing.T) {
		if _, _, err := ClaimEmailChangeToken(db, context.Background(), "tok-does-not-exist"); err != sql.ErrNoRows {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
	})
}

func TestCreateEmailChangeToken_ReplacesPriorOpenToken(t *testing.T) {
	db := setupTestDB(t)

	uid := createTestUser(t, db, "single@example.com")
	insertChangeToken(t, db, "tok-first", uid, "a@example.com", time.Now().Add(4*time.Hour))
	if err := CreateEmailChangeToken(db, "tok-second", uid, "b@example.com", time.Now().Add(4*time.Hour)); err != nil {
		t.Fatalf("CreateEmailChangeToken: %v", err)
	}

	var firstUsed, secondExists bool
	_ = db.QueryRow(`SELECT COALESCE(used, FALSE) FROM email_change_tokens WHERE token_hash = $1`, "tok-first").Scan(&firstUsed)
	err := db.QueryRow(`SELECT TRUE FROM email_change_tokens WHERE token_hash = $1`, "tok-second").Scan(&secondExists)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("check second token: %v", err)
	}
	if firstUsed {
		t.Error("first token should have been removed, not just marked used")
	}
	if !secondExists {
		t.Error("second token should exist")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_change_tokens WHERE user_id = $1`, uid).Scan(&count); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one open token per user, got %d", count)
	}
}
