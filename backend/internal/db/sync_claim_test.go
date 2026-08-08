package db

import (
	"context"
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
			run_generation INTEGER NOT NULL DEFAULT 0,
			verification_generation INTEGER NOT NULL DEFAULT 0,
			verification_lease_until TIMESTAMP WITH TIME ZONE,
			last_run_status TEXT,
			error_message TEXT,
			last_run_at TIMESTAMP WITH TIME ZONE,
			total_files INTEGER NOT NULL DEFAULT 0,
			processed_files INTEGER NOT NULL DEFAULT 0,
			changed_files INTEGER NOT NULL DEFAULT 0,
			deleted_files INTEGER NOT NULL DEFAULT 0,
			failed_files INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		database.Close()
		t.Fatalf("create temp sync_jobs: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TEMP TABLE schedules (
			task_type TEXT NOT NULL,
			task_id TEXT NOT NULL,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			next_run_at TIMESTAMP WITH TIME ZONE
		)
	`); err != nil {
		database.Close()
		t.Fatalf("create temp schedules: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestFinalizeSyncJobPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	insertSyncClaimJob(t, database, "empty-indexing", "INDEXING")
	finalized, err := FinalizeEmptySyncJobPass(database, "empty-indexing", 0, "SUCCESS", nil, 0, 0, 0, 0, 0)
	if err != nil || !finalized || syncClaimStatus(t, database, "empty-indexing") != "IDLE" {
		t.Fatalf("finalize empty INDEXING pass: finalized=%v, err=%v", finalized, err)
	}

	for _, status := range []string{"RUNNING", "VERIFYING"} {
		id := "finalize-" + status
		insertSyncClaimJob(t, database, id, status)
		finalized, err := FinalizeSyncJobPass(database, id, 0, "SUCCESS", nil, 2, 2, 1, 0, 0)
		if err != nil || !finalized {
			t.Fatalf("finalize %s: finalized=%v, err=%v", status, finalized, err)
		}
		if got := syncClaimStatus(t, database, id); got != "IDLE" {
			t.Errorf("finalize %s status = %q, want IDLE", status, got)
		}
		var lastRunStatus string
		var total, processed, changed, deleted, failed int
		if err := database.QueryRow(`
			SELECT last_run_status, total_files, processed_files, changed_files, deleted_files, failed_files
			FROM sync_jobs WHERE id = $1
		`, id).Scan(&lastRunStatus, &total, &processed, &changed, &deleted, &failed); err != nil {
			t.Fatalf("read finalized stats for %s: %v", status, err)
		}
		if lastRunStatus != "SUCCESS" || total != 2 || processed != 2 || changed != 1 || deleted != 0 || failed != 0 {
			t.Errorf("finalize %s stats = (%q, %d, %d, %d, %d, %d), want (SUCCESS, 2, 2, 1, 0, 0)", status, lastRunStatus, total, processed, changed, deleted, failed)
		}
	}

	for _, status := range []string{"INDEXING", "FAILED", "PAUSED", "PAUSED_CONNECTION_LOSS", "IDLE"} {
		id := "preserve-" + status
		insertSyncClaimJob(t, database, id, status)
		finalized, err := FinalizeSyncJobPass(database, id, 0, "SUCCESS", nil, 2, 2, 1, 0, 0)
		if err != nil || finalized {
			t.Errorf("finalize %s: finalized=%v, err=%v; want false, nil", status, finalized, err)
		}
		if got := syncClaimStatus(t, database, id); got != status {
			t.Errorf("finalize %s status = %q, want unchanged", status, got)
		}
	}

	insertSyncClaimJob(t, database, "superseded", "RUNNING")
	if _, err := database.Exec(`UPDATE sync_jobs SET run_generation = 2 WHERE id = 'superseded'`); err != nil {
		t.Fatalf("set superseded pass generation: %v", err)
	}
	finalized, err = FinalizeSyncJobPass(database, "superseded", 1, "SUCCESS", nil, 2, 2, 1, 0, 0)
	if err != nil || finalized || syncClaimStatus(t, database, "superseded") != "RUNNING" {
		t.Errorf("superseded pass finalization: finalized=%v, err=%v; want false and unchanged", finalized, err)
	}
}

func TestFailSyncJobPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	for _, status := range []string{"INDEXING", "RUNNING", "VERIFYING"} {
		id := "fail-" + status
		insertSyncClaimJob(t, database, id, status)
		failed, err := FailSyncJobPass(database, id, 0, "indexing failed")
		if err != nil || !failed {
			t.Fatalf("fail %s: failed=%v, err=%v", status, failed, err)
		}
		var currentStatus, lastRunStatus, errorMessage string
		if err := database.QueryRow(`SELECT status, last_run_status, error_message FROM sync_jobs WHERE id = $1`, id).Scan(&currentStatus, &lastRunStatus, &errorMessage); err != nil {
			t.Fatalf("read failed pass for %s: %v", status, err)
		}
		if currentStatus != "FAILED" || lastRunStatus != "FAILED" || errorMessage != "indexing failed" {
			t.Errorf("fail %s = (%q, %q, %q), want (FAILED, FAILED, indexing failed)", status, currentStatus, lastRunStatus, errorMessage)
		}
	}

	insertSyncClaimJob(t, database, "preserve-paused", "PAUSED")
	failed, err := FailSyncJobPass(database, "preserve-paused", 0, "indexing failed")
	if err != nil || failed {
		t.Errorf("fail paused: failed=%v, err=%v; want false, nil", failed, err)
	}

	insertSyncClaimJob(t, database, "preserve-newer-pass", "INDEXING")
	if _, err := database.Exec(`UPDATE sync_jobs SET run_generation = 2 WHERE id = 'preserve-newer-pass'`); err != nil {
		t.Fatalf("set newer pass generation: %v", err)
	}
	failed, err = FailSyncJobPass(database, "preserve-newer-pass", 1, "stale failure")
	if err != nil || failed || syncClaimStatus(t, database, "preserve-newer-pass") != "INDEXING" {
		t.Errorf("stale failure: failed=%v, err=%v; want false and unchanged", failed, err)
	}
}

func TestUpdateSyncJobTotals(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "totals", "INDEXING")

	updated, err := UpdateSyncJobTotals(database, "totals", 0, 4, 99)
	if err != nil || !updated {
		t.Fatalf("update matching pass totals: updated=%v, err=%v", updated, err)
	}
	updated, err = UpdateSyncJobTotals(database, "totals", 1, 8, 199)
	if err != nil || updated {
		t.Errorf("update stale pass totals: updated=%v, err=%v; want false, nil", updated, err)
	}
}

func TestSyncJobLifecycleTransitions(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	insertSyncClaimJob(t, database, "indexing", "INDEXING")
	transitioned, err := TransitionSyncJobToRunning(database, "indexing", 0)
	if err != nil || !transitioned || syncClaimStatus(t, database, "indexing") != "RUNNING" {
		t.Fatalf("INDEXING -> RUNNING: transitioned=%v, err=%v", transitioned, err)
	}

	transitioned, err = TransitionSyncJobToVerifying(database, "indexing", 0)
	if err != nil || !transitioned || syncClaimStatus(t, database, "indexing") != "VERIFYING" {
		t.Fatalf("RUNNING -> VERIFYING: transitioned=%v, err=%v", transitioned, err)
	}

	for _, status := range []string{"FAILED", "PAUSED", "PAUSED_CONNECTION_LOSS"} {
		id := "do-not-revive-" + status
		insertSyncClaimJob(t, database, id, status)
		transitioned, err := TransitionSyncJobToVerifying(database, id, 0)
		if err != nil || transitioned {
			t.Errorf("%s -> VERIFYING: transitioned=%v, err=%v; want false, nil", status, transitioned, err)
		}
		if got := syncClaimStatus(t, database, id); got != status {
			t.Errorf("%s -> VERIFYING changed status to %q", status, got)
		}
	}
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

func TestAbortSyncJobVerification(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "verifying", "VERIFYING")

	aborted, err := AbortSyncJobVerification(database, "verifying", 0)
	if err != nil || !aborted || syncClaimStatus(t, database, "verifying") != "RUNNING" {
		t.Fatalf("VERIFYING -> RUNNING abort: aborted=%v, err=%v", aborted, err)
	}

	insertSyncClaimJob(t, database, "idle", "IDLE")
	aborted, err = AbortSyncJobVerification(database, "idle", 0)
	if err != nil || aborted || syncClaimStatus(t, database, "idle") != "IDLE" {
		t.Fatalf("IDLE abort: aborted=%v, err=%v", aborted, err)
	}
}

func TestSyncVerificationClaimLeaseAndGeneration(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "verifying-lease", "VERIFYING")

	first, claimed, err := ClaimSyncJobVerification(database, context.Background(), "verifying-lease")
	if err != nil || !claimed || first != 1 {
		t.Fatalf("first claim = (%d, %v, %v), want (1, true, nil)", first, claimed, err)
	}
	if _, claimed, err = ClaimSyncJobVerification(database, context.Background(), "verifying-lease"); err != nil || claimed {
		t.Fatalf("live lease second claim = (%v, %v), want (false, nil)", claimed, err)
	}
	if renewed, err := RenewSyncJobVerificationLease(database, context.Background(), "verifying-lease", 0, first); err != nil || !renewed {
		t.Fatalf("renew = (%v, %v), want (true, nil)", renewed, err)
	}
	if _, err := database.Exec(`UPDATE sync_jobs SET verification_lease_until = NOW() - INTERVAL '1 second' WHERE id = 'verifying-lease'`); err != nil {
		t.Fatal(err)
	}
	second, claimed, err := ClaimSyncJobVerification(database, context.Background(), "verifying-lease")
	if err != nil || !claimed || second != 2 {
		t.Fatalf("expired lease claim = (%d, %v, %v), want (2, true, nil)", second, claimed, err)
	}
	if renewed, err := RenewSyncJobVerificationLease(database, context.Background(), "verifying-lease", 0, first); err != nil || renewed {
		t.Fatalf("stale renew = (%v, %v), want (false, nil)", renewed, err)
	}
	if aborted, err := AbortSyncJobVerification(database, "verifying-lease", 0); err != nil || !aborted {
		t.Fatalf("abort = (%v, %v), want (true, nil)", aborted, err)
	}
	var lease sql.NullTime
	if err := database.QueryRow(`SELECT verification_lease_until FROM sync_jobs WHERE id = 'verifying-lease'`).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.Valid {
		t.Fatal("abort must clear the verification lease")
	}
}

func TestClaimSyncJobPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	for _, status := range []string{"IDLE", "FAILED"} {
		id := "runnable-" + status
		insertSyncClaimJob(t, database, id, status)
		generation, claimed, err := ClaimSyncJobPass(database, id)
		if err != nil || !claimed {
			t.Fatalf("claim %s: claimed=%v, err=%v", status, claimed, err)
		}
		if generation != 1 {
			t.Errorf("claim %s generation = %d, want 1", status, generation)
		}
		if got := syncClaimStatus(t, database, id); got != "INDEXING" {
			t.Errorf("claim %s status = %q, want INDEXING", status, got)
		}
	}

	for _, status := range []string{"RUNNING", "INDEXING", "PAUSED", "PAUSED_CONNECTION_LOSS", "VERIFYING"} {
		id := "blocked-" + status
		insertSyncClaimJob(t, database, id, status)
		_, claimed, err := ClaimSyncJobPass(database, id)
		if err != nil || claimed {
			t.Errorf("claim %s: claimed=%v, err=%v; want false, nil", status, claimed, err)
		}
	}
}

func TestManualSyncPauseResumeTransitions(t *testing.T) {
	database := setupSyncClaimTestDB(t)

	for _, status := range []string{"IDLE", "INDEXING", "RUNNING", "VERIFYING"} {
		id := "pause-" + status
		insertSyncClaimJob(t, database, id, status)
		paused, err := PauseSyncJob(database, id, nil)
		if err != nil || !paused || syncClaimStatus(t, database, id) != "PAUSED" {
			t.Errorf("pause %s: paused=%v, err=%v, status=%s", status, paused, err, syncClaimStatus(t, database, id))
		}
	}

	for _, status := range []string{"PAUSED", "PAUSED_CONNECTION_LOSS", "FAILED"} {
		id := "pause-blocked-" + status
		insertSyncClaimJob(t, database, id, status)
		paused, err := PauseSyncJob(database, id, nil)
		if err != nil || paused || syncClaimStatus(t, database, id) != status {
			t.Errorf("pause %s: paused=%v, err=%v; want false and unchanged", status, paused, err)
		}
	}

	insertSyncClaimJob(t, database, "resume-paused", "PAUSED")
	resumed, err := ResumeSyncJob(database, "resume-paused", nil)
	if err != nil || !resumed || syncClaimStatus(t, database, "resume-paused") != "IDLE" {
		t.Errorf("resume PAUSED: resumed=%v, err=%v", resumed, err)
	}

	for _, status := range []string{"IDLE", "RUNNING", "PAUSED_CONNECTION_LOSS"} {
		id := "resume-blocked-" + status
		insertSyncClaimJob(t, database, id, status)
		resumed, err := ResumeSyncJob(database, id, nil)
		if err != nil || resumed || syncClaimStatus(t, database, id) != status {
			t.Errorf("resume %s: resumed=%v, err=%v; want false and unchanged", status, resumed, err)
		}
	}
}

func TestReleaseUnstartedSyncPass(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "stuck-indexing", "INDEXING")
	released, err := ReleaseUnstartedSyncPass(database, "stuck-indexing", 0)
	if err != nil || !released || syncClaimStatus(t, database, "stuck-indexing") != "IDLE" {
		t.Errorf("release INDEXING: released=%v, err=%v", released, err)
	}

	insertSyncClaimJob(t, database, "running", "RUNNING")
	released, err = ReleaseUnstartedSyncPass(database, "running", 0)
	if err != nil || released || syncClaimStatus(t, database, "running") != "RUNNING" {
		t.Errorf("release RUNNING: released=%v, err=%v; want false and unchanged", released, err)
	}

	insertSyncClaimJob(t, database, "release-stale", "INDEXING")
	if _, err := database.Exec(`UPDATE sync_jobs SET run_generation = 3 WHERE id = 'release-stale'`); err != nil {
		t.Fatalf("set stale release generation: %v", err)
	}
	released, err = ReleaseUnstartedSyncPass(database, "release-stale", 2)
	if err != nil || released || syncClaimStatus(t, database, "release-stale") != "INDEXING" {
		t.Errorf("stale release: released=%v, err=%v; want false and unchanged", released, err)
	}
}

func TestRecoverConnectionLostSyncJob(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "connection-loss", "PAUSED_CONNECTION_LOSS")
	insertSyncClaimJob(t, database, "manually-paused", "PAUSED")
	if _, err := database.Exec(`INSERT INTO schedules (task_type, task_id, is_active, next_run_at) VALUES
		('sync', 'connection-loss', TRUE, NOW() + INTERVAL '1 hour'),
		('sync', 'manually-paused', TRUE, NOW() + INTERVAL '1 hour')`); err != nil {
		t.Fatalf("insert schedules: %v", err)
	}

	recovered, err := RecoverConnectionLostSyncJob(database, context.Background(), "connection-loss")
	if err != nil || !recovered {
		t.Fatalf("recover connection-loss job: recovered=%v, err=%v", recovered, err)
	}
	if got := syncClaimStatus(t, database, "connection-loss"); got != "IDLE" {
		t.Errorf("connection-loss status = %q, want IDLE", got)
	}
	var due bool
	if err := database.QueryRow(`SELECT next_run_at <= NOW() FROM schedules WHERE task_id = 'connection-loss'`).Scan(&due); err != nil || !due {
		t.Errorf("connection-loss schedule not made due: due=%v, err=%v", due, err)
	}
	// The compare-and-set recovery is idempotent: a second worker that probes
	// the same restored connection must not claim it again.
	recovered, err = RecoverConnectionLostSyncJob(database, context.Background(), "connection-loss")
	if err != nil || recovered {
		t.Errorf("second recovery: recovered=%v, err=%v; want false, nil", recovered, err)
	}

	insertSyncClaimJob(t, database, "without-schedule", "PAUSED_CONNECTION_LOSS")
	recovered, err = RecoverConnectionLostSyncJob(database, context.Background(), "without-schedule")
	if err != nil || !recovered || syncClaimStatus(t, database, "without-schedule") != "IDLE" {
		t.Errorf("recover without active schedule: recovered=%v, err=%v, status=%s", recovered, err, syncClaimStatus(t, database, "without-schedule"))
	}

	insertSyncClaimJob(t, database, "inactive-schedule", "PAUSED_CONNECTION_LOSS")
	if _, err := database.Exec(`INSERT INTO schedules (task_type, task_id, is_active, next_run_at)
		VALUES ('sync', 'inactive-schedule', FALSE, NOW() + INTERVAL '1 hour')`); err != nil {
		t.Fatalf("insert inactive schedule: %v", err)
	}
	recovered, err = RecoverConnectionLostSyncJob(database, context.Background(), "inactive-schedule")
	if err != nil || !recovered {
		t.Fatalf("recover inactive schedule: recovered=%v, err=%v", recovered, err)
	}
	if err := database.QueryRow(`SELECT next_run_at > NOW() FROM schedules WHERE task_id = 'inactive-schedule'`).Scan(&due); err != nil || !due {
		t.Errorf("inactive schedule was made due: remainsFuture=%v, err=%v", due, err)
	}

	recovered, err = RecoverConnectionLostSyncJob(database, context.Background(), "manually-paused")
	if err != nil || recovered {
		t.Errorf("recover manually paused job: recovered=%v, err=%v; want false, nil", recovered, err)
	}
	if got := syncClaimStatus(t, database, "manually-paused"); got != "PAUSED" {
		t.Errorf("manually paused status = %q, want PAUSED", got)
	}
	if err := database.QueryRow(`SELECT next_run_at > NOW() FROM schedules WHERE task_id = 'manually-paused'`).Scan(&due); err != nil || !due {
		t.Errorf("manually paused schedule was made due: remainsFuture=%v, err=%v", due, err)
	}
}

func TestRecoverConnectionLostSyncJobConcurrent(t *testing.T) {
	database := setupSyncClaimTestDB(t)
	insertSyncClaimJob(t, database, "concurrent-recovery", "PAUSED_CONNECTION_LOSS")
	if _, err := database.Exec(`INSERT INTO schedules (task_type, task_id, is_active, next_run_at)
		VALUES ('sync', 'concurrent-recovery', TRUE, NOW() + INTERVAL '1 hour')`); err != nil {
		t.Fatalf("insert schedule: %v", err)
	}

	results := make(chan bool, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recovered, err := RecoverConnectionLostSyncJob(database, context.Background(), "concurrent-recovery")
			results <- recovered
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	recoveries := 0
	for recovered := range results {
		if recovered {
			recoveries++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent recovery: %v", err)
		}
	}
	if recoveries != 1 {
		t.Errorf("recoveries = %d, want 1", recoveries)
	}
	if got := syncClaimStatus(t, database, "concurrent-recovery"); got != "IDLE" {
		t.Errorf("concurrent-recovery status = %q, want IDLE", got)
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
			_, claimed, err := ClaimSyncJobPass(database, "concurrent")
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
