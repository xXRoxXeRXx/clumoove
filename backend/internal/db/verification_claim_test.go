package db

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func setupVerificationClaimTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping verification claim DB test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Temporary tables are scoped to one PostgreSQL connection, so this helper
	// deliberately serializes these focused fencing tests.
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE migrations (id TEXT PRIMARY KEY, status TEXT NOT NULL, verification_generation INT NOT NULL DEFAULT 0, verification_lease_until TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE TEMP TABLE tasks (id TEXT PRIMARY KEY, migration_id TEXT NOT NULL, status TEXT NOT NULL, checksum_verified BOOLEAN NOT NULL DEFAULT FALSE, target_hash TEXT, error_message TEXT, next_retry_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestMigrationVerificationClaimFencesStaleTaskWrites(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ('migration', 'VERIFYING')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status) VALUES ('task', 'migration', 'COMPLETED')`); err != nil {
		t.Fatal(err)
	}

	first, claimed, err := ClaimMigrationVerification(database, ctx, "migration")
	if err != nil || !claimed || first != 1 {
		t.Fatalf("first claim = (%d, %v, %v), want (1, true, nil)", first, claimed, err)
	}
	if _, err := database.Exec(`UPDATE migrations SET verification_lease_until = NOW() - INTERVAL '1 second' WHERE id = 'migration'`); err != nil {
		t.Fatal(err)
	}
	second, claimed, err := ClaimMigrationVerification(database, ctx, "migration")
	if err != nil || !claimed || second != 2 {
		t.Fatalf("second claim = (%d, %v, %v), want (2, true, nil)", second, claimed, err)
	}

	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, "task", "SHA1:stale", first); err != nil || wrote {
		t.Fatalf("stale verifier write = (%v, %v), want (false, nil)", wrote, err)
	}
	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, "task", "SHA1:current", second); err != nil || !wrote {
		t.Fatalf("current verifier write = (%v, %v), want (true, nil)", wrote, err)
	}
}

func TestMigrationVerificationWriteRejectsStateChange(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ('migration', 'VERIFYING')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status) VALUES ('task', 'migration', 'COMPLETED')`); err != nil {
		t.Fatal(err)
	}
	generation, claimed, err := ClaimMigrationVerification(database, ctx, "migration")
	if err != nil || !claimed {
		t.Fatalf("claim = (%d, %v, %v)", generation, claimed, err)
	}
	if _, err := database.Exec(`UPDATE migrations SET status = 'CANCELLED' WHERE id = 'migration'`); err != nil {
		t.Fatal(err)
	}
	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, "task", "SHA1:late", generation); err != nil || wrote {
		t.Fatalf("write after cancellation = (%v, %v), want (false, nil)", wrote, err)
	}
}
