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
			file_path TEXT NOT NULL DEFAULT '',
			resource_type TEXT NOT NULL DEFAULT 'files',
			status TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			error_message TEXT,
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

func TestDequeueSQLConflictCopyDependency(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('sync-1', 'RUNNING', 1);
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata, created_at)
		VALUES ('conflict-copy', 'sync-1', '/file.txt', 'files', 'PENDING', '{"action":"conflict_copy"}', '2020-01-01');
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata, created_at)
		VALUES ('upload', 'sync-1', '/file.txt', 'files', 'PENDING', '{"action":"upload","wait_for_conflict_copy":true}', '2020-01-02');
	`); err != nil {
		t.Fatalf("insert conflict dependency tasks: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue conflict copy: %v", err)
	}
	if payload == nil || payload.TaskID != "conflict-copy" {
		t.Fatalf("first dequeue = %+v, want conflict copy", payload)
	}

	payload, err = q.DequeueSQL(context.Background(), database, "worker-2")
	if err != nil {
		t.Fatalf("dequeue while conflict copy running: %v", err)
	}
	if payload != nil {
		t.Fatalf("dequeue while conflict copy running = %+v, want nil", payload)
	}

	if _, err := database.Exec(`UPDATE tasks SET status = 'COMPLETED' WHERE id = 'conflict-copy'`); err != nil {
		t.Fatalf("complete conflict copy: %v", err)
	}
	payload, err = q.DequeueSQL(context.Background(), database, "worker-2")
	if err != nil {
		t.Fatalf("dequeue after conflict copy completion: %v", err)
	}
	if payload == nil || payload.TaskID != "upload" {
		t.Fatalf("dequeue after conflict copy completion = %+v, want upload", payload)
	}
}

func TestDequeueSQLSkipsUploadWhenConflictCopyFails(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('sync-1', 'RUNNING', 1);
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata)
		VALUES ('conflict-copy', 'sync-1', '/file.txt', 'files', 'FAILED', '{"action":"conflict_copy"}');
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata)
		VALUES ('upload', 'sync-1', '/file.txt', 'files', 'PENDING', '{"action":"upload","wait_for_conflict_copy":true}');
	`); err != nil {
		t.Fatalf("insert failed conflict dependency tasks: %v", err)
	}

	payload, err := (&Queue{}).DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue failed conflict dependency: %v", err)
	}
	if payload != nil {
		t.Fatalf("dequeue failed conflict dependency = %+v, want nil", payload)
	}

	var status, errorMessage string
	if err := database.QueryRow(`SELECT status, error_message FROM tasks WHERE id = 'upload'`).Scan(&status, &errorMessage); err != nil {
		t.Fatalf("read skipped upload: %v", err)
	}
	if status != "SKIPPED" || errorMessage != "conflict_copy prerequisite failed; upload skipped" {
		t.Fatalf("skipped upload = (%q, %q), want auditable skipped status", status, errorMessage)
	}
}

func TestDequeueSQLClaimsTaskWithoutConflictDependency(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO migrations (id, status, threads) VALUES ('migration-1', 'RUNNING', 1);
		INSERT INTO tasks (id, migration_id, status, metadata)
		VALUES ('ordinary-upload', 'migration-1', 'PENDING', '{"action":"upload"}');
	`); err != nil {
		t.Fatalf("insert ordinary upload task: %v", err)
	}

	payload, err := (&Queue{}).DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue ordinary upload: %v", err)
	}
	if payload == nil || payload.TaskID != "ordinary-upload" {
		t.Fatalf("dequeue ordinary upload = %+v, want ordinary-upload", payload)
	}
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

func TestDequeueSQLMigrationTasksMayRunWhileIndexing(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO migrations (id, status, threads) VALUES ('migration-1', 'INDEXING', 1);
		INSERT INTO tasks (id, migration_id, status) VALUES ('task-1', 'migration-1', 'PENDING');
	`); err != nil {
		t.Fatalf("insert indexing migration task: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue while indexing: %v", err)
	}
	if payload == nil || payload.TaskID != "task-1" || payload.MigrationID != "migration-1" {
		t.Fatalf("dequeue while indexing = %+v, want migration task", payload)
	}
}
