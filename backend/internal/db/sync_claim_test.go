package db

import (
	"database/sql"
	"os"
	"sync"
	"testing"

	_ "github.com/lib/pq"
)

// setupSyncClaimTestDB uses a per-connection temporary table so the claim
// queries exercise PostgreSQL without touching the application's sync_jobs.
func setupSyncClaimTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping sync claim DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// A temporary table belongs to one connection. One connection also makes
	// the concurrent claim test deterministic while still exercising the same
	// atomic UPDATE used in production.
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatalf("ping db: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TEMP TABLE sync_jobs (
			id TEXT PRIMARY KEY,
			status TEXT NOT NULL,
			updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		database.Close()
		t.Fatalf("create temp sync_jobs: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func insertSyncClaimJob(t *testing.T, database *sql.DB, id, status string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO sync_jobs (id, status) VALUES ($1, $2)`, id, status); err != nil {
		t.Fatalf("insert sync job %q: %v", id, err)
	}
}

func syncClaimStatus(t *testing.T, database *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := database.QueryRow(`SELECT status FROM sync_jobs WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("get sync job status: %v", err)
	}
	return status
}

func TestClaimSyncJobPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	for _, status := range []string{"IDLE", "FAILED", "PAUSED"} {
		id := "runnable-" + status
		insertSyncClaimJob(t, database, id, status)
		claimed, err := ClaimSyncJobPass(database, id)
		if err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%v, err=%v", status, claimed, err)
		}
		if got := syncClaimStatus(t, database, id); got != "INDEXING" {
			t.Errorf("claim %s status = %q, want INDEXING", status, got)
		}
	}

	for _, status := range []string{"RUNNING", "INDEXING", "PAUSED_CONNECTION_LOSS", "VERIFYING"} {
		id := "blocked-" + status
		insertSyncClaimJob(t, database, id, status)
		claimed, err := ClaimSyncJobPass(database, id)
		if err != nil || claimed {
			t.Errorf("claim %s: claimed=%v, err=%v; want false, nil", status, claimed, err)
		}
	}
}

func TestClaimPausedSyncJobPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "connection-loss", "PAUSED_CONNECTION_LOSS")
	insertSyncClaimJob(t, database, "manually-paused", "PAUSED")

	claimed, err := ClaimPausedSyncJobPass(database, "connection-loss")
	if err != nil || !claimed {
		t.Fatalf("claim connection-loss job: claimed=%v, err=%v", claimed, err)
	}
	if got := syncClaimStatus(t, database, "connection-loss"); got != "INDEXING" {
		t.Errorf("connection-loss status = %q, want INDEXING", got)
	}

	claimed, err = ClaimPausedSyncJobPass(database, "manually-paused")
	if err != nil || claimed {
		t.Errorf("claim manually paused job: claimed=%v, err=%v; want false, nil", claimed, err)
	}
}

func TestClaimSyncJobPassConcurrent(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "concurrent", "IDLE")

	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := ClaimSyncJobPass(database, "concurrent")
			results <- claimed
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	claims := 0
	for claimed := range results {
		if claimed {
			claims++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	if claims != 1 {
		t.Errorf("successful claims = %d, want 1", claims)
	}
}
