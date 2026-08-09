package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

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
		`CREATE TEMP TABLE migrations (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'::uuid, status TEXT NOT NULL, verification_generation INT NOT NULL DEFAULT 0, verification_lease_until TIMESTAMPTZ, notification_generation INT NOT NULL DEFAULT 0, total_files INT NOT NULL DEFAULT 0, processed_files INT NOT NULL DEFAULT 0, failed_files INT NOT NULL DEFAULT 0, skipped_files INT NOT NULL DEFAULT 0, processed_bytes BIGINT NOT NULL DEFAULT 0, live_bytes BIGINT NOT NULL DEFAULT 0, error_message TEXT, updated_at TIMESTAMPTZ)`,
		`CREATE TEMP TABLE tasks (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), migration_id UUID NOT NULL, status TEXT NOT NULL, file_size BIGINT NOT NULL DEFAULT 0, checksum_verified BOOLEAN NOT NULL DEFAULT FALSE, target_hash TEXT, error_message TEXT, next_retry_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE TEMP TABLE indexing_errors (migration_id UUID NOT NULL)`,
		`CREATE TEMP TABLE notification_events (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), user_id UUID NOT NULL, kind TEXT NOT NULL, migration_id UUID, run_generation INT NOT NULL, run_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL)`,
		`CREATE UNIQUE INDEX notification_events_migration_generation ON notification_events (migration_id, run_generation) WHERE migration_id IS NOT NULL`,
		`CREATE TEMP TABLE notification_deliveries (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), event_id UUID NOT NULL, channel_type TEXT NOT NULL, config_encrypted TEXT NOT NULL, UNIQUE (event_id, channel_type))`,
		`CREATE TEMP TABLE notification_channels (user_id UUID NOT NULL, type TEXT NOT NULL, enabled BOOLEAN NOT NULL, config_encrypted TEXT NOT NULL)`,
		`CREATE TEMP TABLE instance_smtp_settings (id INT PRIMARY KEY)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestChecksumMismatchRetryKeepsMigrationRunnableUntilRecopyIsVerified(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ($1, 'VERIFYING')`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status, file_size) VALUES ($1, $2, 'COMPLETED', 42)`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}

	generation, claimed, err := ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed {
		t.Fatalf("claim = (%d, %v, %v)", generation, claimed, err)
	}
	retryAt := time.Now().Add(time.Minute)
	wrote, err := MarkMigrationTaskChecksumMismatchWhileVerifying(database, ctx, &Task{
		ID:           testTaskID,
		ErrorMessage: sql.NullString{String: "checksum mismatch", Valid: true},
		NextRetryAt:  sql.NullTime{Time: retryAt, Valid: true},
		TargetHash:   sql.NullString{String: "SHA1:wrong", Valid: true},
	}, generation)
	if err != nil || !wrote {
		t.Fatalf("mark mismatch = (%v, %v), want (true, nil)", wrote, err)
	}

	if got := migrationStatus(t, database, testMigrationID); got != "RUNNING" {
		t.Fatalf("status immediately after scheduling checksum retry = %q, want RUNNING", got)
	}
	if err := ReconcileMigrationProgress(database, testMigrationID); err != nil {
		t.Fatalf("reconcile scheduled retry: %v", err)
	}
	if got := migrationStatus(t, database, testMigrationID); got != "RUNNING" {
		t.Fatalf("status after reconciling checksum retry = %q, want RUNNING", got)
	}
	var notificationCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE migration_id = $1`, testMigrationID).Scan(&notificationCount); err != nil {
		t.Fatal(err)
	}
	if notificationCount != 0 {
		t.Fatalf("notifications after scheduling retry = %d, want 0", notificationCount)
	}

	// This is the retry scheduler's transition, followed by a successful re-copy.
	if _, err := database.Exec(`UPDATE tasks SET status = 'PENDING', next_retry_at = NULL WHERE id = $1`, testTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE tasks SET status = 'RUNNING' WHERE id = $1`, testTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE tasks SET status = 'COMPLETED', checksum_verified = FALSE WHERE id = $1`, testTaskID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileMigrationProgress(database, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if got := migrationStatus(t, database, testMigrationID); got != "VERIFYING" {
		t.Fatalf("status after re-copy = %q, want VERIFYING", got)
	}

	generation, claimed, err = ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed {
		t.Fatalf("claim re-copy verification = (%d, %v, %v)", generation, claimed, err)
	}
	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, testTaskID, "SHA1:correct", generation); err != nil || !wrote {
		t.Fatalf("mark re-copy verified = (%v, %v), want (true, nil)", wrote, err)
	}
	if reconciled, err := ReconcileMigrationProgressWhileVerifying(database, testMigrationID, generation); err != nil || !reconciled {
		t.Fatalf("reconcile verified re-copy = (%v, %v), want (true, nil)", reconciled, err)
	}
	if got := migrationStatus(t, database, testMigrationID); got != "COMPLETED" {
		t.Fatalf("final status = %q, want COMPLETED", got)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE migration_id = $1`, testMigrationID).Scan(&notificationCount); err != nil {
		t.Fatal(err)
	}
	if notificationCount != 1 {
		t.Fatalf("terminal notifications = %d, want 1", notificationCount)
	}
}

func TestChecksumMismatchWithoutRetryStaysVerifyingUntilTerminalReconciliation(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ($1, 'VERIFYING')`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status, file_size) VALUES ($1, $2, 'COMPLETED', 42)`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}

	generation, claimed, err := ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed {
		t.Fatalf("claim = (%d, %v, %v)", generation, claimed, err)
	}
	wrote, err := MarkMigrationTaskChecksumMismatchWhileVerifying(database, ctx, &Task{
		ID:           testTaskID,
		ErrorMessage: sql.NullString{String: "permanent checksum mismatch", Valid: true},
		TargetHash:   sql.NullString{String: "SHA1:wrong", Valid: true},
	}, generation)
	if err != nil || !wrote {
		t.Fatalf("mark permanent mismatch = (%v, %v), want (true, nil)", wrote, err)
	}
	if got := migrationStatus(t, database, testMigrationID); got != "VERIFYING" {
		t.Fatalf("status after non-retryable mismatch = %q, want VERIFYING", got)
	}
	if reconciled, err := ReconcileMigrationProgressWhileVerifying(database, testMigrationID, generation); err != nil || !reconciled {
		t.Fatalf("reconcile permanent mismatch = (%v, %v), want (true, nil)", reconciled, err)
	}
	if got := migrationStatus(t, database, testMigrationID); got != "COMPLETED_WITH_ERRORS" {
		t.Fatalf("terminal status = %q, want COMPLETED_WITH_ERRORS", got)
	}
}

func TestMigrationVerificationClaimFencesStaleTaskWrites(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ($1, 'VERIFYING')`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status) VALUES ($1, $2, 'COMPLETED')`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}

	first, claimed, err := ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed || first != 1 {
		t.Fatalf("first claim = (%d, %v, %v), want (1, true, nil)", first, claimed, err)
	}
	if _, err := database.Exec(`UPDATE migrations SET verification_lease_until = NOW() - INTERVAL '1 second' WHERE id = $1`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	second, claimed, err := ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed || second != 2 {
		t.Fatalf("second claim = (%d, %v, %v), want (2, true, nil)", second, claimed, err)
	}

	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, testTaskID, "SHA1:stale", first); err != nil || wrote {
		t.Fatalf("stale verifier write = (%v, %v), want (false, nil)", wrote, err)
	}
	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, testTaskID, "SHA1:current", second); err != nil || !wrote {
		t.Fatalf("current verifier write = (%v, %v), want (true, nil)", wrote, err)
	}
}

func TestMigrationVerificationWriteRejectsStateChange(t *testing.T) {
	database := setupVerificationClaimTestDB(t)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO migrations (id, status) VALUES ($1, 'VERIFYING')`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO tasks (id, migration_id, status) VALUES ($1, $2, 'COMPLETED')`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}
	generation, claimed, err := ClaimMigrationVerification(database, ctx, testMigrationID)
	if err != nil || !claimed {
		t.Fatalf("claim = (%d, %v, %v)", generation, claimed, err)
	}
	if _, err := database.Exec(`UPDATE migrations SET status = 'CANCELLED' WHERE id = $1`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if wrote, err := MarkMigrationTaskChecksumVerifiedWhileVerifying(database, ctx, testTaskID, "SHA1:late", generation); err != nil || wrote {
		t.Fatalf("write after cancellation = (%v, %v), want (false, nil)", wrote, err)
	}
}
