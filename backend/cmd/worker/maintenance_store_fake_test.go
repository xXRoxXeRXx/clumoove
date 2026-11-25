package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

// Fakes for the stores the worker maintenance loops and the checksum verifier
// poll; any call a test does not override panics through the nil embedded
// interface.

type fakeMaintenanceTaskStore struct {
	db.TaskStore
	requeueCount  int64
	requeueErr    error
	requeueCalls  int
	orphanedCount int64
	orphanedErr   error
	orphanedCalls int
}

func (f *fakeMaintenanceTaskStore) RequeueDueFailedTasks(context.Context, time.Time) (int64, error) {
	f.requeueCalls++
	return f.requeueCount, f.requeueErr
}

func (f *fakeMaintenanceTaskStore) RequeueOrphanedRunningTasks(context.Context) (int64, error) {
	f.orphanedCalls++
	return f.orphanedCount, f.orphanedErr
}

type fakeMaintenanceMigrationStore struct {
	db.MigrationStore
	running       []string
	runningErr    error
	verifying     []string
	verifyingErr  error
	retried       []string
	reconciled    []string
	reconcileErr  map[string]error
	candidates    []db.ConnectionRecoveryCandidate
	candidatesErr error
	gotCursor     string
	gotLimit      int
	recoverCalls  []string
}

func (f *fakeMaintenanceMigrationStore) ListRunningMigrationIDs(context.Context) ([]string, error) {
	return f.running, f.runningErr
}

func (f *fakeMaintenanceMigrationStore) ListVerifyingMigrationIDs(context.Context) ([]string, error) {
	return f.verifying, f.verifyingErr
}

func (f *fakeMaintenanceMigrationStore) MaybeRetryFailedMigrationTasks(_ context.Context, id string) (bool, error) {
	f.retried = append(f.retried, id)
	return false, nil
}

func (f *fakeMaintenanceMigrationStore) ReconcileMigrationProgress(id string) error {
	f.reconciled = append(f.reconciled, id)
	return f.reconcileErr[id]
}

func (f *fakeMaintenanceMigrationStore) ListPausedConnectionLossMigrations(_ context.Context, cursor string, limit int) ([]db.ConnectionRecoveryCandidate, error) {
	f.gotCursor, f.gotLimit = cursor, limit
	return f.candidates, f.candidatesErr
}

func (f *fakeMaintenanceMigrationStore) RecoverConnectionLostMigration(id string) (bool, bool, error) {
	f.recoverCalls = append(f.recoverCalls, id)
	return false, false, nil
}

type fakeMaintenanceSyncStore struct {
	db.SyncStore
	passes        []db.SyncPassRef
	passesErr     error
	verifying     []string
	verifyingErr  error
	reconciled    []db.SyncPassRef
	candidates    []db.ConnectionRecoveryCandidate
	candidatesErr error
	gotCursor     string
	gotLimit      int
}

func (f *fakeMaintenanceSyncStore) ListRunningSyncPasses(context.Context) ([]db.SyncPassRef, error) {
	return f.passes, f.passesErr
}

func (f *fakeMaintenanceSyncStore) ListVerifyingSyncJobIDs(context.Context) ([]string, error) {
	return f.verifying, f.verifyingErr
}

func (f *fakeMaintenanceSyncStore) ReconcileSyncJobProgress(id string, generation int) error {
	f.reconciled = append(f.reconciled, db.SyncPassRef{ID: id, Generation: generation})
	return nil
}

func (f *fakeMaintenanceSyncStore) ListPausedConnectionLossSyncJobs(_ context.Context, cursor string, limit int) ([]db.ConnectionRecoveryCandidate, error) {
	f.gotCursor, f.gotLimit = cursor, limit
	return f.candidates, f.candidatesErr
}

func TestRequeueFailedTasksGoesThroughTheTaskStore(t *testing.T) {
	t.Run("a store failure is logged and never panics", func(t *testing.T) {
		tasks := &fakeMaintenanceTaskStore{requeueErr: errors.New("db down")}
		m := &workerMaintenance{tasks: tasks}
		m.requeueFailedTasks(context.Background())
		if tasks.requeueCalls != 1 {
			t.Fatalf("requeue calls = %d, want 1", tasks.requeueCalls)
		}
	})
	t.Run("nothing due means no wake-up", func(t *testing.T) {
		tasks := &fakeMaintenanceTaskStore{}
		m := &workerMaintenance{tasks: tasks} // a nil queue/db would panic on notify
		m.requeueFailedTasks(context.Background())
		if tasks.requeueCalls != 1 {
			t.Fatalf("requeue calls = %d, want 1", tasks.requeueCalls)
		}
	})
}

func TestRequeueOrphanedRunningTasksGoesThroughTheTaskStore(t *testing.T) {
	tasks := &fakeMaintenanceTaskStore{orphanedErr: errors.New("db down")}
	m := &workerMaintenance{tasks: tasks}
	m.requeueOrphanedRunningTasks(context.Background())
	tasks.orphanedErr = nil
	m.requeueOrphanedRunningTasks(context.Background())
	if tasks.orphanedCalls != 2 {
		t.Fatalf("orphaned calls = %d, want 2", tasks.orphanedCalls)
	}
}

func TestProgressReconcilerUsesTheStoreListings(t *testing.T) {
	t.Run("every running migration is retried and reconciled, even after a failure", func(t *testing.T) {
		migrations := &fakeMaintenanceMigrationStore{
			running:      []string{"mig-1", "mig-2"},
			reconcileErr: map[string]error{"mig-1": errors.New("boom")},
		}
		m := &workerMaintenance{migrations: migrations}
		m.reconcileActiveMigrations(context.Background())
		if len(migrations.reconciled) != 2 || len(migrations.retried) != 2 {
			t.Fatalf("reconciled = %v, retried = %v, want both migrations", migrations.reconciled, migrations.retried)
		}
	})
	t.Run("a failed listing reconciles nothing", func(t *testing.T) {
		migrations := &fakeMaintenanceMigrationStore{runningErr: errors.New("db down")}
		m := &workerMaintenance{migrations: migrations}
		m.reconcileActiveMigrations(context.Background())
		if len(migrations.reconciled) != 0 {
			t.Fatalf("reconciled = %v, want none", migrations.reconciled)
		}
	})
	t.Run("sync passes are reconciled at their run generation", func(t *testing.T) {
		syncs := &fakeMaintenanceSyncStore{passes: []db.SyncPassRef{{ID: "sync-1", Generation: 3}}}
		m := &workerMaintenance{syncs: syncs}
		m.reconcileActiveSyncJobs(context.Background())
		if len(syncs.reconciled) != 1 || syncs.reconciled[0] != (db.SyncPassRef{ID: "sync-1", Generation: 3}) {
			t.Fatalf("reconciled = %+v", syncs.reconciled)
		}
	})
}

func TestConnectionRecoveryPagesCandidatesThroughTheStores(t *testing.T) {
	t.Run("migrations", func(t *testing.T) {
		candidate := db.ConnectionRecoveryCandidate{ID: "mig-1", SourcePassword: "not-encrypted", UpdatedAt: time.Now()}
		migrations := &fakeMaintenanceMigrationStore{candidates: []db.ConnectionRecoveryCandidate{candidate}}
		m := &workerMaintenance{migrations: migrations}
		m.setRecoveryCursor(false, "after-0")

		m.recoverPausedMigrations(context.Background())

		if migrations.gotCursor != "after-0" || migrations.gotLimit != recoveryScanWindow {
			t.Fatalf("store got cursor %q limit %d, want after-0 / %d", migrations.gotCursor, migrations.gotLimit, recoveryScanWindow)
		}
		// The credentials cannot be decrypted, so the probe fails: the failure is
		// recorded for backoff, the cursor moves on and the job stays paused.
		if _, ok := m.recoveryAttempts.Load(recoveryAttemptKey(false, "mig-1")); !ok {
			t.Error("expected a recorded recovery failure for mig-1")
		}
		if got := m.recoveryCursor(false); got != "mig-1" {
			t.Errorf("cursor = %q, want mig-1", got)
		}
		if len(migrations.recoverCalls) != 0 {
			t.Errorf("recover calls = %v, want none for a failed probe", migrations.recoverCalls)
		}
	})

	t.Run("sync jobs", func(t *testing.T) {
		syncs := &fakeMaintenanceSyncStore{candidates: []db.ConnectionRecoveryCandidate{{ID: "sync-1", UpdatedAt: time.Now()}}}
		m := &workerMaintenance{syncs: syncs}

		m.recoverPausedSyncJobs(context.Background())

		if syncs.gotCursor != "" || syncs.gotLimit != recoveryScanWindow {
			t.Fatalf("store got cursor %q limit %d", syncs.gotCursor, syncs.gotLimit)
		}
		if _, ok := m.recoveryAttempts.Load(recoveryAttemptKey(true, "sync-1")); !ok {
			t.Error("expected a recorded recovery failure for sync-1")
		}
	})

	t.Run("a failed listing leaves the cursor alone", func(t *testing.T) {
		migrations := &fakeMaintenanceMigrationStore{candidatesErr: errors.New("db down")}
		m := &workerMaintenance{migrations: migrations}
		m.setRecoveryCursor(false, "keep")
		m.recoverPausedMigrations(context.Background())
		if got := m.recoveryCursor(false); got != "keep" {
			t.Errorf("cursor = %q, want keep", got)
		}
	})

	t.Run("candidates read before an unreadable row are still examined", func(t *testing.T) {
		syncs := &fakeMaintenanceSyncStore{
			candidates:    []db.ConnectionRecoveryCandidate{{ID: "sync-2", UpdatedAt: time.Now()}},
			candidatesErr: errors.New("one row could not be scanned"),
		}
		m := &workerMaintenance{syncs: syncs}

		m.recoverPausedSyncJobs(context.Background())

		if _, ok := m.recoveryAttempts.Load(recoveryAttemptKey(true, "sync-2")); !ok {
			t.Error("the readable candidate must still be probed")
		}
		if got := m.recoveryCursor(true); got != "sync-2" {
			t.Errorf("cursor = %q, want sync-2 so the next page starts past the bad row", got)
		}
	})
}

func TestVerifierDiscoversVerifyingEntitiesThroughTheStores(t *testing.T) {
	newVerifier := func(m db.MigrationStore, s db.SyncStore) *checksumVerifier {
		return &checksumVerifier{
			migrations:     m,
			syncs:          s,
			migrationQueue: make(chan verificationWork, 4),
			syncQueue:      make(chan verificationWork, 4),
		}
	}

	t.Run("each VERIFYING entity is queued once under its guard key", func(t *testing.T) {
		v := newVerifier(&fakeMaintenanceMigrationStore{verifying: []string{"mig-1", "mig-2"}}, &fakeMaintenanceSyncStore{verifying: []string{"sync-1"}})
		v.processVerifyingMigrations(context.Background())
		v.processVerifyingSyncJobs(context.Background())
		v.processVerifyingMigrations(context.Background()) // already guarded

		if got := len(v.migrationQueue); got != 2 {
			t.Errorf("queued migrations = %d, want 2", got)
		}
		if got := len(v.syncQueue); got != 1 {
			t.Errorf("queued sync jobs = %d, want 1", got)
		}
		if _, ok := v.verifyingEntities.Load("sync:sync-1"); !ok {
			t.Error("sync job is not guarded")
		}
	})

	t.Run("a failed listing queues nothing", func(t *testing.T) {
		v := newVerifier(&fakeMaintenanceMigrationStore{verifyingErr: errors.New("db down")}, &fakeMaintenanceSyncStore{verifyingErr: errors.New("db down")})
		v.processVerifyingMigrations(context.Background())
		v.processVerifyingSyncJobs(context.Background())
		if len(v.migrationQueue)+len(v.syncQueue) != 0 {
			t.Error("nothing should be queued when the listing fails")
		}
	})
}
