package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func setupSchedulerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping scheduler database integration test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.Ping(); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

// TestTriggerMigrationInvalidClaimIsNotFound protects the schedule lifecycle:
// an unsuccessful CAS must identify the missing/non-SCHEDULED migration as a
// non-retryable condition rather than allowing a recurring schedule to loop.
func TestTriggerMigrationInvalidClaimIsNotFound(t *testing.T) {
	database := setupSchedulerTestDB(t)
	// ClaimScheduledMigrationForIndexing intentionally targets the production
	// migrations table, so only run this assertion when that table is available.
	if _, err := database.Exec(`SELECT 1 FROM migrations LIMIT 0`); err != nil {
		t.Skip("production migrations table unavailable")
	}
	s := NewScheduler(database, nil, nil)
	err := s.triggerMigration(context.Background(), "00000000-0000-0000-0000-000000000099")
	if err == nil {
		t.Fatal("triggerMigration unexpectedly claimed a missing migration")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("triggerMigration error = %v, want errors.Is(sql.ErrNoRows)", err)
	}
}
