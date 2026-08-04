package db

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func setupFinalRetryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping final retry DB test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'::uuid,
			status TEXT NOT NULL DEFAULT 'RUNNING',
			verification_generation INT NOT NULL DEFAULT 0,
			verification_lease_until TIMESTAMPTZ,
			notification_generation INT NOT NULL DEFAULT 0,
			total_files INT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			skipped_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			live_bytes BIGINT NOT NULL DEFAULT 0,
			failed_retry_done BOOLEAN NOT NULL DEFAULT FALSE,
			error_message TEXT,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TEMP TABLE tasks (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			migration_id UUID NOT NULL,
			status TEXT NOT NULL,
			attempts INT NOT NULL DEFAULT 0,
			file_size BIGINT NOT NULL DEFAULT 100,
			checksum_verified BOOLEAN NOT NULL DEFAULT FALSE,
			worker_hash TEXT,
			target_hash TEXT,
			error_message TEXT,
			next_retry_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TEMP TABLE indexing_errors (migration_id UUID NOT NULL)`,
		`CREATE TEMP TABLE notification_events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			kind TEXT NOT NULL,
			migration_id UUID,
			run_generation INT NOT NULL,
			run_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL
		)`,
		`CREATE UNIQUE INDEX notification_events_migration_generation ON notification_events (migration_id, run_generation) WHERE migration_id IS NOT NULL`,
		`CREATE TEMP TABLE notification_deliveries (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			event_id UUID NOT NULL,
			channel_type TEXT NOT NULL,
			config_encrypted TEXT NOT NULL,
			UNIQUE (event_id, channel_type)
		)`,
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

func TestMaybeRetryFailedMigrationTasks(t *testing.T) {
	database := setupFinalRetryTestDB(t)
	ctx := context.Background()

	var migID string
	err := database.QueryRow(`
		INSERT INTO migrations (status, failed_retry_done)
		VALUES ('RUNNING', FALSE)
		RETURNING id::text
	`).Scan(&migID)
	if err != nil {
		t.Fatalf("failed to insert test migration: %v", err)
	}

	// Insert tasks: 1 COMPLETED, 2 terminally FAILED (next_retry_at NULL), 1 retryable FAILED (next_retry_at NOT NULL)
	_, err = database.Exec(`
		INSERT INTO tasks (migration_id, status, attempts, error_message, next_retry_at) VALUES
		($1, 'COMPLETED', 1, NULL, NULL),
		($1, 'FAILED', 3, 'network error', NULL),
		($1, 'FAILED', 3, 'timeout error', NULL),
		($1, 'FAILED', 1, 'temporary error', NOW() + INTERVAL '10 seconds')
	`, migID)
	if err != nil {
		t.Fatalf("failed to insert test tasks: %v", err)
	}

	// First call: while an active task (next_retry_at IS NOT NULL) exists,
	// MaybeRetryFailedMigrationTasks MUST NOT claim final retry.
	requeuedActive, err := MaybeRetryFailedMigrationTasks(database, ctx, migID)
	if err != nil {
		t.Fatalf("MaybeRetryFailedMigrationTasks call with active task returned error: %v", err)
	}
	if requeuedActive {
		t.Fatalf("expected requeuedActive=false while active task exists, got true")
	}

	var flagDoneBefore bool
	if err := database.QueryRow(`SELECT failed_retry_done FROM migrations WHERE id = $1`, migID).Scan(&flagDoneBefore); err != nil {
		t.Fatalf("failed to read failed_retry_done: %v", err)
	}
	if flagDoneBefore {
		t.Fatalf("expected failed_retry_done=false while active task exists")
	}

	// Mark the active task as COMPLETED so no active tasks remain.
	_, err = database.Exec(`UPDATE tasks SET status = 'COMPLETED', next_retry_at = NULL WHERE migration_id = $1 AND error_message = 'temporary error'`, migID)
	if err != nil {
		t.Fatalf("failed to complete active task: %v", err)
	}

	// Second call: no active tasks remain -> should claim final retry and requeue 2 terminal failed tasks
	requeued, err := MaybeRetryFailedMigrationTasks(database, ctx, migID)
	if err != nil {
		t.Fatalf("MaybeRetryFailedMigrationTasks call returned error: %v", err)
	}
	if !requeued {
		t.Fatalf("expected requeued=true, got false")
	}

	// Verify migration flag failed_retry_done is now true
	var flagDone bool
	if err := database.QueryRow(`SELECT failed_retry_done FROM migrations WHERE id = $1`, migID).Scan(&flagDone); err != nil {
		t.Fatalf("failed to read failed_retry_done: %v", err)
	}
	if !flagDone {
		t.Fatalf("expected failed_retry_done=true")
	}

	// Verify task statuses
	var pendingCount, failedCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM tasks WHERE migration_id = $1 AND status = 'PENDING' AND attempts = 0`, migID).Scan(&pendingCount); err != nil {
		t.Fatalf("failed to count PENDING tasks: %v", err)
	}
	if pendingCount != 2 {
		t.Fatalf("expected 2 PENDING tasks with attempts=0, got %d", pendingCount)
	}

	if err := database.QueryRow(`SELECT COUNT(*) FROM tasks WHERE migration_id = $1 AND status = 'FAILED'`, migID).Scan(&failedCount); err != nil {
		t.Fatalf("failed to count FAILED tasks: %v", err)
	}
	if failedCount != 0 {
		t.Fatalf("expected 0 remaining FAILED tasks, got %d", failedCount)
	}

	// Third call: should return requeued=false (already claimed, no infinite loop)
	requeuedAgain, err := MaybeRetryFailedMigrationTasks(database, ctx, migID)
	if err != nil {
		t.Fatalf("MaybeRetryFailedMigrationTasks subsequent call returned error: %v", err)
	}
	if requeuedAgain {
		t.Fatalf("expected requeuedAgain=false, got true")
	}
}

func TestTransitionResetsFailedRetryDoneFlag(t *testing.T) {
	database := setupFinalRetryTestDB(t)
	ctx := context.Background()

	var migID string
	err := database.QueryRow(`
		INSERT INTO migrations (status, failed_retry_done)
		VALUES ('INDEXING', TRUE)
		RETURNING id::text
	`).Scan(&migID)
	if err != nil {
		t.Fatalf("failed to insert test migration: %v", err)
	}

	// Transition INDEXING -> RUNNING should reset failed_retry_done to false
	if err := TransitionMigrationIndexingToRunning(database, migID); err != nil {
		t.Fatalf("TransitionMigrationIndexingToRunning error: %v", err)
	}

	var flagDone bool
	if err := database.QueryRow(`SELECT failed_retry_done FROM migrations WHERE id = $1`, migID).Scan(&flagDone); err != nil {
		t.Fatalf("failed to read failed_retry_done: %v", err)
	}
	if flagDone {
		t.Fatalf("expected failed_retry_done=false after transition to RUNNING")
	}

	// Now set status to COMPLETED and failed_retry_done to true
	_, err = database.Exec(`UPDATE migrations SET status = 'COMPLETED', failed_retry_done = TRUE WHERE id = $1`, migID)
	if err != nil {
		t.Fatalf("failed to update migration status: %v", err)
	}

	// ResetFailedTasksForRetry should also reset failed_retry_done to false
	_, err = database.Exec(`INSERT INTO tasks (migration_id, status) VALUES ($1, 'FAILED')`, migID)
	if err != nil {
		t.Fatalf("failed to insert task: %v", err)
	}

	if _, err := ResetFailedTasksForRetry(database, ctx, migID); err != nil {
		t.Fatalf("ResetFailedTasksForRetry error: %v", err)
	}

	if err := database.QueryRow(`SELECT failed_retry_done FROM migrations WHERE id = $1`, migID).Scan(&flagDone); err != nil {
		t.Fatalf("failed to read failed_retry_done: %v", err)
	}
	if flagDone {
		t.Fatalf("expected failed_retry_done=false after ResetFailedTasksForRetry")
	}
}
