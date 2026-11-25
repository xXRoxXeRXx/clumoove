package db

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// setupLeaseTestDB creates per-connection temporary tables holding only the
// columns the claim and fenced-write queries touch, so the tests run the real
// production SQL against PostgreSQL without touching application tables.
func setupLeaseTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping lease fencing DB test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Temporary tables belong to one connection.
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE backup_packs (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), last_checked_at TIMESTAMPTZ)`,
		`CREATE TEMP TABLE backup_verify_targets (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), backup_maintenance_id UUID NOT NULL, backup_pack_id UUID, pack_remote_path TEXT NOT NULL, pack_sha256 BYTEA NOT NULL DEFAULT '\x00', pack_size_bytes BIGINT NOT NULL DEFAULT 1, state TEXT NOT NULL DEFAULT 'PENDING', bytes_read BIGINT NOT NULL DEFAULT 0, error_code TEXT, claim_epoch BIGINT NOT NULL DEFAULT 0, claim_deadline TIMESTAMPTZ, worker_hash TEXT, cursor JSONB NOT NULL DEFAULT '{}'::jsonb)`,
		`CREATE TEMP TABLE restore_jobs (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), threads INT NOT NULL DEFAULT 8)`,
		`CREATE TEMP TABLE restore_runs (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), restore_job_id UUID NOT NULL, status TEXT NOT NULL DEFAULT 'RUNNING', processed_files INT NOT NULL DEFAULT 0, processed_bytes BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TEMP TABLE restore_items (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), restore_run_id UUID NOT NULL, parent_item_id UUID, snapshot_relative_path TEXT NOT NULL, is_dir BOOLEAN NOT NULL DEFAULT FALSE, size_bytes BIGINT NOT NULL DEFAULT 0, file_sha256 BYTEA, source_mtime TIMESTAMPTZ, source_metadata JSONB, target_path TEXT NOT NULL DEFAULT '/target', resolved_target_path TEXT, status TEXT NOT NULL DEFAULT 'PENDING', verification_kind TEXT, outcome_code TEXT, attempts INT NOT NULL DEFAULT 0, next_retry_at TIMESTAMPTZ, claim_epoch BIGINT NOT NULL DEFAULT 0, worker_hash TEXT, claim_deadline TIMESTAMPTZ, error_code TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { database.Close() })
	return database
}

const leaseTestMaintenanceID = "00000000-0000-0000-0000-0000000000a1"

func TestBackupVerifyTargetRejectsWritesFromAStaleLeaseAfterReclaim(t *testing.T) {
	database := setupLeaseTestDB(t)
	ctx := context.Background()
	var targetID string
	if err := database.QueryRow(`INSERT INTO backup_verify_targets (backup_maintenance_id, pack_remote_path) VALUES ($1, 'packs/a') RETURNING id`, leaseTestMaintenanceID).Scan(&targetID); err != nil {
		t.Fatal(err)
	}

	first, err := claimNextBackupVerifyTargetContext(ctx, database, leaseTestMaintenanceID, "worker-1")
	if err != nil || first == nil {
		t.Fatalf("first claim = (%v, %v)", first, err)
	}
	// The first worker's claim expires; the next claim recovers the target and
	// grants it to another worker under a higher epoch.
	if _, err := database.Exec(`UPDATE backup_verify_targets SET claim_deadline = CURRENT_TIMESTAMP - INTERVAL '1 minute' WHERE id = $1`, targetID); err != nil {
		t.Fatal(err)
	}
	second, err := claimNextBackupVerifyTargetContext(ctx, database, leaseTestMaintenanceID, "worker-2")
	if err != nil || second == nil {
		t.Fatalf("second claim = (%v, %v)", second, err)
	}
	if second.ClaimEpoch <= first.ClaimEpoch {
		t.Fatalf("epoch after reclaim = %d, want more than %d", second.ClaimEpoch, first.ClaimEpoch)
	}

	stale := first.Lease()
	if _, err := completeBackupVerifyTargetContext(ctx, database, stale, true, 1, 1); err != nil {
		t.Fatalf("stale complete: %v", err)
	}
	if err := markBackupVerifyTargetDamagedContext(ctx, database, stale); err != nil {
		t.Fatalf("stale mark damaged: %v", err)
	}
	if err := markBackupVerifyTargetReadContext(ctx, database, stale, 99); err != nil {
		t.Fatalf("stale mark read: %v", err)
	}
	if err := releaseBackupVerifyTargetClaimContext(ctx, database, stale); err != nil {
		t.Fatalf("stale release: %v", err)
	}

	var state, worker string
	var bytesRead int64
	if err := database.QueryRow(`SELECT state, COALESCE(worker_hash, ''), bytes_read FROM backup_verify_targets WHERE id = $1`, targetID).Scan(&state, &worker, &bytesRead); err != nil {
		t.Fatal(err)
	}
	if state != "RUNNING" || worker != "worker-2" || bytesRead != 0 {
		t.Fatalf("stale lease changed the re-claimed target: state=%s worker=%s bytes_read=%d", state, worker, bytesRead)
	}

	if got, err := completeBackupVerifyTargetContext(ctx, database, second.Lease(), true, 1, 1); err != nil || got != "COMPLETED" {
		t.Fatalf("current lease complete = (%q, %v), want COMPLETED", got, err)
	}
	if err := database.QueryRow(`SELECT state FROM backup_verify_targets WHERE id = $1`, targetID).Scan(&state); err != nil || state != "COMPLETED" {
		t.Fatalf("state after current lease complete = %q, %v", state, err)
	}
}

func TestRestoreItemRejectsWritesFromAStaleLeaseAfterReclaim(t *testing.T) {
	database := setupLeaseTestDB(t)
	ctx := context.Background()
	var jobID, runID, itemID string
	if err := database.QueryRow(`INSERT INTO restore_jobs DEFAULT VALUES RETURNING id`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`INSERT INTO restore_runs (restore_job_id) VALUES ($1) RETURNING id`, jobID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`INSERT INTO restore_items (restore_run_id, snapshot_relative_path, source_metadata) VALUES ($1, 'a.txt', '{}'::jsonb) RETURNING id`, runID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}

	first, err := ClaimNextRestoreItemForWorkerContext(ctx, database, "worker-1")
	if err != nil || first == nil {
		t.Fatalf("first claim = (%v, %v)", first, err)
	}
	// Stale recovery returns the item to PENDING; another worker claims it.
	if _, err := database.Exec(`UPDATE restore_items SET status = 'PENDING', worker_hash = NULL, claim_deadline = NULL WHERE id = $1`, itemID); err != nil {
		t.Fatal(err)
	}
	second, err := ClaimNextRestoreItemForWorkerContext(ctx, database, "worker-2")
	if err != nil || second == nil {
		t.Fatalf("second claim = (%v, %v)", second, err)
	}
	if second.ClaimEpoch <= first.ClaimEpoch {
		t.Fatalf("epoch after reclaim = %d, want more than %d", second.ClaimEpoch, first.ClaimEpoch)
	}

	stale := first.Lease()
	if err := SetRestoreItemTargetPathContext(ctx, database, stale, runID, "/stale"); err != nil {
		t.Fatalf("stale set target path: %v", err)
	}
	if err := HeartbeatRestoreItemContext(ctx, database, stale, runID, "worker-1"); err != nil {
		t.Fatalf("stale heartbeat: %v", err)
	}
	if err := FailRestoreItemContext(ctx, database, stale, runID, "RESTORE_TRANSFER_FAILED"); err != nil {
		t.Fatalf("stale fail: %v", err)
	}
	if completed, err := CompleteRestoreItemContext(ctx, database, stale, runID, 10, "HASH_VERIFIED"); err != nil || completed {
		t.Fatalf("stale complete = (%v, %v), want (false, nil)", completed, err)
	}

	var status, worker string
	var resolved sql.NullString
	var processed int
	if err := database.QueryRow(`SELECT status, COALESCE(worker_hash, ''), resolved_target_path FROM restore_items WHERE id = $1`, itemID).Scan(&status, &worker, &resolved); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" || worker != "worker-2" || resolved.Valid {
		t.Fatalf("stale lease changed the re-claimed item: status=%s worker=%s resolved=%v", status, worker, resolved)
	}
	if err := database.QueryRow(`SELECT processed_files FROM restore_runs WHERE id = $1`, runID).Scan(&processed); err != nil || processed != 0 {
		t.Fatalf("run counters moved for a stale lease: processed_files=%d, %v", processed, err)
	}

	if completed, err := CompleteRestoreItemContext(ctx, database, second.Lease(), runID, 10, "HASH_VERIFIED"); err != nil || !completed {
		t.Fatalf("current lease complete = (%v, %v), want (true, nil)", completed, err)
	}
}
