package queue

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// setupDequeueTestDB creates the minimal temporary schema used by DequeueSQL.
func setupDequeueTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping dequeue DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(1) // Temporary tables are connection-local.
	if err := database.Ping(); err != nil {
		_ = database.Close()
		t.Fatalf("ping db: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TEMP TABLE migrations (id TEXT PRIMARY KEY, status TEXT NOT NULL, threads INTEGER NOT NULL);
		CREATE TEMP TABLE sync_jobs (id TEXT PRIMARY KEY, status TEXT NOT NULL, threads INTEGER NOT NULL);
		CREATE TEMP TABLE tasks (
			id TEXT PRIMARY KEY,
			migration_id TEXT,
			sync_job_id TEXT,
			status TEXT NOT NULL,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE,
			worker_hash TEXT
		);
	`); err != nil {
		_ = database.Close()
		t.Fatalf("create temp queue tables: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestDequeueSQLSyncTasksWaitForRunning(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('sync-1', 'INDEXING', 1);
		INSERT INTO tasks (id, sync_job_id, status) VALUES ('task-1', 'sync-1', 'PENDING');
	`); err != nil {
		t.Fatalf("insert indexing sync task: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue while indexing: %v", err)
	}
	if payload != nil {
		t.Fatalf("dequeue while indexing = %+v, want nil", payload)
	}

	var status string
	if err := database.QueryRow(`SELECT status FROM tasks WHERE id = 'task-1'`).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "PENDING" {
		t.Fatalf("task status while indexing = %q, want PENDING", status)
	}

	if _, err := database.Exec(`UPDATE sync_jobs SET status = 'RUNNING' WHERE id = 'sync-1'`); err != nil {
		t.Fatalf("set sync job running: %v", err)
	}
	payload, err = q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue while running: %v", err)
	}
	if payload == nil || payload.TaskID != "task-1" || payload.SyncJobID != "sync-1" {
		t.Fatalf("dequeue while running = %+v, want sync task", payload)
	}
}
