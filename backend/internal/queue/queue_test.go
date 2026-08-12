package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
)

const (
	migrationBID = "00000000-0000-0000-0000-000000000002"
	migrationID  = "00000000-0000-0000-0000-000000000003"
	syncJobID    = "00000000-0000-0000-0000-000000000004"
)

// setupDequeueTestDB creates a connection-local temporary schema used by
// DequeueSQL. It mirrors the queue-relevant production constraints.
func setupDequeueTestDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Parallel()
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
		CREATE TEMP TABLE migrations (id UUID PRIMARY KEY, status TEXT NOT NULL, threads INTEGER NOT NULL);
		CREATE TEMP TABLE sync_jobs (id UUID PRIMARY KEY, status TEXT NOT NULL, threads INTEGER NOT NULL, run_generation INTEGER NOT NULL DEFAULT 0);
		CREATE TEMP TABLE tasks (
			id UUID PRIMARY KEY,
			migration_id UUID REFERENCES migrations(id) ON DELETE CASCADE,
			sync_job_id UUID REFERENCES sync_jobs(id) ON DELETE CASCADE,
			file_path TEXT NOT NULL DEFAULT '',
			file_size BIGINT NOT NULL DEFAULT 0,
			source_hash TEXT,
			target_hash TEXT,
			resource_type TEXT NOT NULL DEFAULT 'files',
			status TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			error_message TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			checksum_verified BOOLEAN NOT NULL DEFAULT FALSE,
			next_retry_at TIMESTAMP WITH TIME ZONE,
			created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE,
			worker_hash TEXT,
			claim_epoch BIGINT NOT NULL DEFAULT 0,
			pass_generation INTEGER NOT NULL DEFAULT 0,
			CONSTRAINT chk_task_job_type CHECK (
				(migration_id IS NOT NULL AND sync_job_id IS NULL) OR
				(migration_id IS NULL AND sync_job_id IS NOT NULL)
			)
		);
	`); err != nil {
		_ = database.Close()
		t.Fatalf("create temp queue tables: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestDequeueSQLSkipsSaturatedJobToClaimNext(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO migrations (id, status, threads) VALUES
			('00000000-0000-0000-0000-000000000001', 'RUNNING', 1),
			('00000000-0000-0000-0000-000000000002', 'RUNNING', 1);
		INSERT INTO tasks (id, migration_id, status, metadata, created_at) VALUES
			('00000000-0000-0000-0000-000000000101', '00000000-0000-0000-0000-000000000001', 'RUNNING', '{}', '2020-01-01'),
			('00000000-0000-0000-0000-000000000102', '00000000-0000-0000-0000-000000000001', 'PENDING', '{}', '2020-01-02'),
			('00000000-0000-0000-0000-000000000103', '00000000-0000-0000-0000-000000000002', 'PENDING', '{}', '2020-01-03');
	`); err != nil {
		t.Fatalf("create saturated and eligible migrations: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue after saturated migration: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000103" || payload.MigrationID != migrationBID {
		t.Fatalf("dequeue = %+v, want pending task from migration-b", payload)
	}
}

// TestDequeueSQLSerializesMigrationCapacity uses separate PostgreSQL
// connections.  A thread cap of one must permit exactly one concurrent claim.
func TestDequeueSQLSerializesMigrationCapacity(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping dequeue concurrency DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = database.Close() })

	testEmail := fmt.Sprintf("queue-migration-capacity-test-%d@example.invalid", time.Now().UnixNano())
	var userID, createdMigrationID string
	if err := database.QueryRow(`
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, 'unused', 'Queue test')
		RETURNING id
	`, testEmail).Scan(&userID); err != nil {
		t.Fatalf("create migration test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM users WHERE id = $1`, userID)
	})
	if err := database.QueryRow(`
		INSERT INTO migrations (
			user_id, source_url, source_username, source_password_encrypted,
			target_url, target_username, target_password_encrypted, status, threads
		) VALUES ($1, 'https://source.example', 'source', 'secret', 'https://target.example', 'target', 'secret', 'RUNNING', 1)
		RETURNING id
	`, userID).Scan(&createdMigrationID); err != nil {
		t.Fatalf("create migration: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO tasks (migration_id, file_path, file_size, status, metadata)
		VALUES ($1, '/one', 1, 'PENDING', '{}'::jsonb), ($1, '/two', 1, 'PENDING', '{}'::jsonb)
	`, createdMigrationID); err != nil {
		t.Fatalf("create tasks: %v", err)
	}

	start := make(chan struct{})
	results := make(chan *Payload, 2)
	errs := make(chan error, 2)
	q := &Queue{}
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(workerID string) {
			defer workers.Done()
			<-start
			payload, err := q.DequeueSQL(context.Background(), database, workerID)
			if err != nil {
				errs <- err
				return
			}
			results <- payload
		}(fmt.Sprintf("concurrent-migration-worker-%d", i))
	}
	close(start)
	workers.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Errorf("concurrent dequeue: %v", err)
	}
	claims := 0
	nilPayloads := 0
	for payload := range results {
		if payload != nil {
			claims++
			if payload.MigrationID != createdMigrationID || (payload.TaskID == "" || payload.ClaimEpoch != 1) {
				t.Errorf("migration claim = %+v, want a fresh task for migration %s", payload, createdMigrationID)
			}
		} else {
			nilPayloads++
		}
	}
	if claims != 1 || nilPayloads != 1 {
		t.Fatalf("concurrent results = %d claims, %d empty, want 1 claim and 1 empty result", claims, nilPayloads)
	}
	var running int
	if err := database.QueryRow(`SELECT COUNT(*) FROM tasks WHERE migration_id = $1 AND status = 'RUNNING'`, createdMigrationID).Scan(&running); err != nil {
		t.Fatalf("count running tasks: %v", err)
	}
	if running != 1 {
		t.Fatalf("running tasks = %d, want 1", running)
	}
}

// TestDequeueSQLSerializesSyncCapacity verifies that concurrent workers cannot
// exceed a sync pass's thread cap. The generation is part of the capacity key,
// so tasks from a different pass must not affect this assertion.
func TestDequeueSQLSerializesSyncCapacity(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping dequeue concurrency DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = database.Close() })

	testEmail := fmt.Sprintf("queue-sync-capacity-test-%d@example.invalid", time.Now().UnixNano())
	var userID, createdSyncJobID string
	if err := database.QueryRow(`
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, 'unused', 'Queue test')
		RETURNING id
	`, testEmail).Scan(&userID); err != nil {
		t.Fatalf("create sync test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM users WHERE id = $1`, userID)
	})
	if err := database.QueryRow(`
		INSERT INTO sync_jobs (
			user_id, source_url, source_username, source_password_encrypted,
			target_url, target_username, target_password_encrypted, status, threads, run_generation
		) VALUES ($1, 'https://source.example', 'source', 'secret', 'https://target.example', 'target', 'secret', 'RUNNING', 1, 1)
		RETURNING id
	`, userID).Scan(&createdSyncJobID); err != nil {
		t.Fatalf("create sync job: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO tasks (sync_job_id, file_path, file_size, status, metadata, pass_generation)
		VALUES ($1, '/one', 1, 'PENDING', '{}'::jsonb, 1), ($1, '/two', 1, 'PENDING', '{}'::jsonb, 1)
	`, createdSyncJobID); err != nil {
		t.Fatalf("create sync tasks: %v", err)
	}

	start := make(chan struct{})
	results := make(chan *Payload, 2)
	errs := make(chan error, 2)
	q := &Queue{}
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(workerID string) {
			defer workers.Done()
			<-start
			payload, err := q.DequeueSQL(context.Background(), database, workerID)
			if err != nil {
				errs <- err
				return
			}
			results <- payload
		}(fmt.Sprintf("concurrent-sync-worker-%d", i))
	}
	close(start)
	workers.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Errorf("concurrent dequeue: %v", err)
	}
	claims := 0
	nilPayloads := 0
	for payload := range results {
		if payload != nil {
			claims++
			if payload.SyncJobID != createdSyncJobID || (payload.TaskID == "" || payload.ClaimEpoch != 1) {
				t.Errorf("sync claim = %+v, want a fresh task for sync job %s", payload, createdSyncJobID)
			}
		} else {
			nilPayloads++
		}
	}
	if claims != 1 || nilPayloads != 1 {
		t.Fatalf("concurrent results = %d claims, %d empty, want 1 claim and 1 empty result", claims, nilPayloads)
	}
	var running int
	if err := database.QueryRow(`SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND pass_generation = 1 AND status = 'RUNNING'`, createdSyncJobID).Scan(&running); err != nil {
		t.Fatalf("count running tasks: %v", err)
	}
	if running != 1 {
		t.Fatalf("running tasks = %d, want 1", running)
	}
}

// TestDequeueSQLCommitsEmptyWhenCapacitySaturatesAfterCandidateLock forces a
// candidate to pass the optimistic filter, then reduces its parent capacity
// while DequeueSQL waits to acquire the authoritative parent lock.
func TestDequeueSQLCommitsEmptyWhenCapacitySaturatesAfterCandidateLock(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping dequeue capacity DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	database.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = database.Close() })

	testEmail := fmt.Sprintf("queue-capacity-race-test-%d@example.invalid", time.Now().UnixNano())
	var userID, createdMigrationID, taskID string
	if err := database.QueryRow(`
		INSERT INTO users (email, password_hash, display_name)
		VALUES ($1, 'unused', 'Queue test')
		RETURNING id
	`, testEmail).Scan(&userID); err != nil {
		t.Fatalf("create capacity test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM users WHERE id = $1`, userID)
	})
	if err := database.QueryRow(`
		INSERT INTO migrations (
			user_id, source_url, source_username, source_password_encrypted,
			target_url, target_username, target_password_encrypted, status, threads
		) VALUES ($1, 'https://source.example', 'source', 'secret', 'https://target.example', 'target', 'secret', 'RUNNING', 1)
		RETURNING id
	`, userID).Scan(&createdMigrationID); err != nil {
		t.Fatalf("create capacity test migration: %v", err)
	}
	if err := database.QueryRow(`
		INSERT INTO tasks (migration_id, file_path, file_size, status, metadata)
		VALUES ($1, '/capacity-race', 1, 'PENDING', '{}'::jsonb)
		RETURNING id
	`, createdMigrationID).Scan(&taskID); err != nil {
		t.Fatalf("create capacity test task: %v", err)
	}

	parentTx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin parent lock transaction: %v", err)
	}
	defer func() { _ = parentTx.Rollback() }()
	var lockedMigrationID string
	if err := parentTx.QueryRow(`SELECT id FROM migrations WHERE id = $1 FOR UPDATE`, createdMigrationID).Scan(&lockedMigrationID); err != nil {
		t.Fatalf("lock migration parent: %v", err)
	}

	type dequeueResult struct {
		payload *Payload
		err     error
	}
	result := make(chan dequeueResult, 1)
	q := &Queue{}
	go func() {
		payload, err := q.DequeueSQL(context.Background(), database, "capacity-race-worker")
		result <- dequeueResult{payload: payload, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		lockTx, err := database.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin task lock probe: %v", err)
		}
		var lockedTaskID string
		err = lockTx.QueryRow(`SELECT id FROM tasks WHERE id = $1 FOR UPDATE NOWAIT`, taskID).Scan(&lockedTaskID)
		_ = lockTx.Rollback()
		if err != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code == "55P03" {
				break
			}
			t.Fatalf("probe candidate task lock: %v", err)
		}
		select {
		case outcome := <-result:
			t.Fatalf("dequeue returned before parent capacity changed: payload=%+v err=%v", outcome.payload, outcome.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("dequeue did not lock the candidate task before the timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := parentTx.Exec(`UPDATE migrations SET threads = 0 WHERE id = $1`, createdMigrationID); err != nil {
		t.Fatalf("saturate migration while parent is locked: %v", err)
	}
	if err := parentTx.Commit(); err != nil {
		t.Fatalf("commit saturated migration: %v", err)
	}

	outcome := <-result
	if outcome.err != nil {
		t.Fatalf("dequeue after capacity saturation: %v", outcome.err)
	}
	if outcome.payload != nil {
		t.Fatalf("dequeue after capacity saturation = %+v, want nil", outcome.payload)
	}
	var status string
	var claimEpoch int64
	if err := database.QueryRow(`SELECT status, claim_epoch FROM tasks WHERE id = $1`, taskID).Scan(&status, &claimEpoch); err != nil {
		t.Fatalf("read unclaimed task: %v", err)
	}
	if status != "PENDING" || claimEpoch != 0 {
		t.Fatalf("task after empty dequeue = (%q, %d), want (PENDING, 0)", status, claimEpoch)
	}
}

func TestDequeueSQLConflictCopyDependency(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('00000000-0000-0000-0000-000000000004', 'RUNNING', 1);
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata, created_at)
		VALUES ('00000000-0000-0000-0000-000000000104', '00000000-0000-0000-0000-000000000004', '/file.txt', 'files', 'PENDING', '{"action":"conflict_copy"}', '2020-01-01');
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata, created_at)
		VALUES ('00000000-0000-0000-0000-000000000105', '00000000-0000-0000-0000-000000000004', '/file.txt', 'files', 'PENDING', '{"action":"upload","wait_for_conflict_copy":true}', '2020-01-02');
	`); err != nil {
		t.Fatalf("insert conflict dependency tasks: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue conflict copy: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000104" {
		t.Fatalf("first dequeue = %+v, want conflict copy", payload)
	}

	payload, err = q.DequeueSQL(context.Background(), database, "worker-2")
	if err != nil {
		t.Fatalf("dequeue while conflict copy running: %v", err)
	}
	if payload != nil {
		t.Fatalf("dequeue while conflict copy running = %+v, want nil", payload)
	}

	if _, err := database.Exec(`UPDATE tasks SET status = 'COMPLETED' WHERE id = '00000000-0000-0000-0000-000000000104'`); err != nil {
		t.Fatalf("complete conflict copy: %v", err)
	}
	payload, err = q.DequeueSQL(context.Background(), database, "worker-2")
	if err != nil {
		t.Fatalf("dequeue after conflict copy completion: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000105" {
		t.Fatalf("dequeue after conflict copy completion = %+v, want upload", payload)
	}
}

func TestDequeueSQLSkipsUploadWhenConflictCopyFails(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('00000000-0000-0000-0000-000000000004', 'RUNNING', 1);
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata)
		VALUES ('00000000-0000-0000-0000-000000000104', '00000000-0000-0000-0000-000000000004', '/file.txt', 'files', 'FAILED', '{"action":"conflict_copy"}');
		INSERT INTO tasks (id, sync_job_id, file_path, resource_type, status, metadata)
		VALUES ('00000000-0000-0000-0000-000000000105', '00000000-0000-0000-0000-000000000004', '/file.txt', 'files', 'PENDING', '{"action":"upload","wait_for_conflict_copy":true}');
	`); err != nil {
		t.Fatalf("insert failed conflict dependency tasks: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue failed conflict dependency: %v", err)
	}
	if payload != nil {
		t.Fatalf("dequeue failed conflict dependency = %+v, want nil", payload)
	}

	var status, errorMessage string
	if err := database.QueryRow(`SELECT status, error_message FROM tasks WHERE id = '00000000-0000-0000-0000-000000000105'`).Scan(&status, &errorMessage); err != nil {
		t.Fatalf("read skipped upload: %v", err)
	}
	if status != "SKIPPED" || errorMessage != "conflict_copy prerequisite failed; upload skipped" {
		t.Fatalf("skipped upload = (%q, %q), want auditable skipped status", status, errorMessage)
	}
}

func TestDequeueSQLClaimsTaskWithoutConflictDependency(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO migrations (id, status, threads) VALUES ('00000000-0000-0000-0000-000000000003', 'RUNNING', 1);
		INSERT INTO tasks (id, migration_id, status, metadata)
		VALUES ('00000000-0000-0000-0000-000000000106', '00000000-0000-0000-0000-000000000003', 'PENDING', '{"action":"upload"}');
	`); err != nil {
		t.Fatalf("insert ordinary upload task: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue ordinary upload: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000106" || payload.ClaimEpoch != 1 {
		t.Fatalf("dequeue ordinary upload = %+v, want ordinary-upload", payload)
	}
	if _, err := database.Exec(`UPDATE tasks SET status = 'PENDING', worker_hash = NULL WHERE id = '00000000-0000-0000-0000-000000000106'`); err != nil {
		t.Fatalf("reset task for reclaim: %v", err)
	}
	payload, err = q.DequeueSQL(context.Background(), database, "worker-2")
	if err != nil {
		t.Fatalf("reclaim ordinary upload: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000106" || payload.ClaimEpoch != 2 {
		t.Fatalf("reclaimed ordinary upload = %+v, want claim epoch 2", payload)
	}
}

func TestDequeueSQLSyncTasksWaitForRunning(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO sync_jobs (id, status, threads) VALUES ('00000000-0000-0000-0000-000000000004', 'INDEXING', 1);
		INSERT INTO tasks (id, sync_job_id, status) VALUES ('00000000-0000-0000-0000-000000000107', '00000000-0000-0000-0000-000000000004', 'PENDING');
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
	if err := database.QueryRow(`SELECT status FROM tasks WHERE id = '00000000-0000-0000-0000-000000000107'`).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != "PENDING" {
		t.Fatalf("task status while indexing = %q, want PENDING", status)
	}

	if _, err := database.Exec(`UPDATE sync_jobs SET status = 'RUNNING' WHERE id = '00000000-0000-0000-0000-000000000004'`); err != nil {
		t.Fatalf("set sync job running: %v", err)
	}
	payload, err = q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue while running: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000107" || payload.SyncJobID != syncJobID {
		t.Fatalf("dequeue while running = %+v, want sync task", payload)
	}
}

func TestDequeueSQLMigrationTasksMayRunWhileIndexing(t *testing.T) {
	database := setupDequeueTestDB(t)
	if _, err := database.Exec(`
		INSERT INTO migrations (id, status, threads) VALUES ('00000000-0000-0000-0000-000000000003', 'INDEXING', 1);
		INSERT INTO tasks (id, migration_id, status) VALUES ('00000000-0000-0000-0000-000000000108', '00000000-0000-0000-0000-000000000003', 'PENDING');
	`); err != nil {
		t.Fatalf("insert indexing migration task: %v", err)
	}

	q := &Queue{}
	payload, err := q.DequeueSQL(context.Background(), database, "worker-1")
	if err != nil {
		t.Fatalf("dequeue while indexing: %v", err)
	}
	if payload == nil || payload.TaskID != "00000000-0000-0000-0000-000000000108" || payload.MigrationID != migrationID {
		t.Fatalf("dequeue while indexing = %+v, want migration task", payload)
	}
}
