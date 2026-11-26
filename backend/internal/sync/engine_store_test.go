package sync

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"backend/internal/db"
)

// fakeSyncStore overrides only the persistence the engine and its pass store
// touch in these tests; any other call panics through the nil embedded
// interface, which pins the persistence each path is allowed to use.
type fakeSyncStore struct {
	db.SyncStore
	calls []string

	job        *db.SyncJob
	jobErr     error
	claimGen   int
	claimed    bool
	claimErr   error
	claimedJob string

	pauseGen    int
	pauseOK     bool
	pauseErrMsg *string
	resumeOK    bool
	resumeErr   *string
	resumeAt    time.Time
	deletedID   string

	finalizeOK  bool
	finalizeErr error
	finalized   Finalization

	recoveredPasses []db.RecoveredSyncPass
	recoverErr      error
	advancedID      string
	advanceErr      error
}

func (f *fakeSyncStore) GetSyncJobContext(_ context.Context, id string) (*db.SyncJob, error) {
	f.calls = append(f.calls, "get:"+id)
	return f.job, f.jobErr
}

func (f *fakeSyncStore) ClaimSyncJobPass(id string) (int, bool, error) {
	f.calls = append(f.calls, "claim")
	f.claimedJob = id
	return f.claimGen, f.claimed, f.claimErr
}

func (f *fakeSyncStore) PauseSyncJobAndDeactivateSchedules(_ context.Context, _ string, errMsg *string) (int, bool, error) {
	f.pauseErrMsg = errMsg
	return f.pauseGen, f.pauseOK, nil
}

func (f *fakeSyncStore) ResumeSyncJobAndReactivateSchedules(_ context.Context, _ string, errMsg *string, nextRunAt time.Time) (bool, error) {
	f.resumeErr, f.resumeAt = errMsg, nextRunAt
	return f.resumeOK, nil
}

func (f *fakeSyncStore) DeleteSyncJobCascade(id string) error {
	f.deletedID = id
	return nil
}

func (f *fakeSyncStore) FinalizeSyncJobPassWithStates(id string, generation int, status string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*db.SyncState, deletes []db.SyncStateDelete) (bool, error) {
	f.calls = append(f.calls, "finalize")
	f.finalized = Finalization{JobID: id, Generation: generation, Status: status, Error: errMsg, Total: total, Processed: processed, Changed: changed, Deleted: deleted, Failed: failed, StateUpserts: upserts, StateDeletes: deletes}
	return f.finalizeOK, f.finalizeErr
}

func (f *fakeSyncStore) FinalizeEmptySyncJobPassWithStates(id string, generation int, status string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*db.SyncState, deletes []db.SyncStateDelete) (bool, error) {
	f.calls = append(f.calls, "finalize-empty")
	f.finalized = Finalization{JobID: id, Generation: generation, Status: status, Error: errMsg, Total: total, Processed: processed, Changed: changed, Deleted: deleted, Failed: failed, StateUpserts: upserts, StateDeletes: deletes}
	return f.finalizeOK, f.finalizeErr
}

func (f *fakeSyncStore) RecoverStaleSyncPasses(context.Context) ([]db.RecoveredSyncPass, error) {
	f.calls = append(f.calls, "recover")
	return f.recoveredPasses, f.recoverErr
}

func (f *fakeSyncStore) AdvanceRecoveredSyncSchedule(_ context.Context, syncJobID string) error {
	f.calls = append(f.calls, "advance:"+syncJobID)
	f.advancedID = syncJobID
	return f.advanceErr
}

type fakeEngineTaskStore struct {
	db.TaskStore
	hasRunning     bool
	hasRunningErr  error
	hasPassRunning bool
	hasPassErr     error
	outcomes       []db.SyncTaskOutcome
	outcomesErr    error
}

func (f *fakeEngineTaskStore) HasRunningSyncTasks(context.Context, string) (bool, error) {
	return f.hasRunning, f.hasRunningErr
}

func (f *fakeEngineTaskStore) HasRunningSyncPassTasks(context.Context, string, int) (bool, error) {
	return f.hasPassRunning, f.hasPassErr
}

func (f *fakeEngineTaskStore) ListSyncPassTaskOutcomes(context.Context, string, int) ([]db.SyncTaskOutcome, error) {
	return f.outcomes, f.outcomesErr
}

func TestStartSyncPassReportsAnUnclaimedJobWithoutStartingAPass(t *testing.T) {
	store := &fakeSyncStore{claimed: false}
	e := &Engine{syncs: store, activePassCancels: map[string]activePassCancel{}}

	started, err := e.startSyncPass(context.Background(), "sync-1")

	if err != nil || started {
		t.Fatalf("startSyncPass() = (%v, %v), want (false, nil)", started, err)
	}
	if store.claimedJob != "sync-1" {
		t.Errorf("claimed job = %q, want sync-1", store.claimedJob)
	}
}

func TestStartSyncPassPropagatesClaimErrors(t *testing.T) {
	claimErr := errors.New("claim failed")
	e := &Engine{syncs: &fakeSyncStore{claimErr: claimErr}}

	started, err := e.startSyncPass(context.Background(), "sync-1")

	if !errors.Is(err, claimErr) || started {
		t.Fatalf("startSyncPass() = (%v, %v), want (false, %v)", started, err, claimErr)
	}
}

func TestPostgresPassStoreReadsStatusThroughSyncStore(t *testing.T) {
	store := &fakeSyncStore{job: &db.SyncJob{Status: "PAUSED_CONNECTION_LOSS"}}
	ps := postgresPassStore{syncs: store}

	status, err := ps.Status(context.Background(), "sync-1")

	if err != nil || status != "PAUSED_CONNECTION_LOSS" {
		t.Fatalf("Status() = (%q, %v), want PAUSED_CONNECTION_LOSS", status, err)
	}
	if want := []string{"get:sync-1"}; !reflect.DeepEqual(store.calls, want) {
		t.Errorf("store calls = %v, want %v", store.calls, want)
	}
}

func TestPostgresPassStoreStatusPropagatesLoadError(t *testing.T) {
	loadErr := errors.New("boom")
	ps := postgresPassStore{syncs: &fakeSyncStore{jobErr: loadErr}}

	if _, err := ps.Status(context.Background(), "sync-1"); !errors.Is(err, loadErr) {
		t.Fatalf("Status() error = %v, want %v", err, loadErr)
	}
}

func TestPostgresPassStorePauseResumeAndDeleteClearTheStoredError(t *testing.T) {
	store := &fakeSyncStore{pauseGen: 4, pauseOK: true, resumeOK: true}
	ps := postgresPassStore{syncs: store}
	ctx := context.Background()

	gen, paused, err := ps.Pause(ctx, "sync-1")
	if err != nil || !paused || gen != 4 {
		t.Fatalf("Pause() = (%d, %v, %v), want (4, true, nil)", gen, paused, err)
	}
	if store.pauseErrMsg == nil || *store.pauseErrMsg != "" {
		t.Errorf("pause error message = %v, want pointer to empty string", store.pauseErrMsg)
	}

	next := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	resumed, err := ps.Resume(ctx, "sync-1", next)
	if err != nil || !resumed {
		t.Fatalf("Resume() = (%v, %v), want (true, nil)", resumed, err)
	}
	if store.resumeErr == nil || *store.resumeErr != "" || !store.resumeAt.Equal(next) {
		t.Errorf("resume args = (%v, %v), want (empty string, %v)", store.resumeErr, store.resumeAt, next)
	}

	if err := ps.Delete(ctx, "sync-1"); err != nil || store.deletedID != "sync-1" {
		t.Errorf("Delete() deleted %q (err %v), want sync-1", store.deletedID, err)
	}
}

func TestPostgresPassStoreFinalizeRoutesEmptyPassesToTheEmptyTransition(t *testing.T) {
	failMsg := "x"
	tests := []struct {
		name  string
		empty bool
		call  string
	}{
		{"populated pass", false, "finalize"},
		{"empty pass", true, "finalize-empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeSyncStore{finalizeOK: true}
			ps := postgresPassStore{syncs: store}
			in := Finalization{JobID: "sync-1", Generation: 3, Status: "COMPLETED", Error: &failMsg, Total: 5, Processed: 4, Changed: 3, Deleted: 2, Failed: 1, Empty: tt.empty}

			ok, err := ps.Finalize(in)

			if err != nil || !ok {
				t.Fatalf("Finalize() = (%v, %v), want (true, nil)", ok, err)
			}
			if !reflect.DeepEqual(store.calls, []string{tt.call}) {
				t.Errorf("store calls = %v, want [%s]", store.calls, tt.call)
			}
			in.Empty = false
			if !reflect.DeepEqual(store.finalized, in) {
				t.Errorf("finalized = %+v, want %+v", store.finalized, in)
			}
		})
	}
}

func TestPostgresPassStoreFinalizeReportsASupersededPass(t *testing.T) {
	ps := postgresPassStore{syncs: &fakeSyncStore{finalizeOK: false}}

	ok, err := ps.Finalize(Finalization{JobID: "sync-1", Generation: 1})

	if err != nil || ok {
		t.Fatalf("Finalize() = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestPostgresPassStoreRecoverAndAdvanceThroughSyncStore(t *testing.T) {
	store := &fakeSyncStore{
		recoveredPasses: []db.RecoveredSyncPass{{ID: "sync-1", UserID: "user-1"}},
	}
	ps := postgresPassStore{syncs: store}

	recovered, err := ps.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(recovered) != 1 || recovered[0].ID != "sync-1" || recovered[0].UserID != "user-1" {
		t.Fatalf("Recover() = %+v, want [{ID:sync-1 UserID:user-1}]", recovered)
	}
	if err := ps.AdvanceRecovered(context.Background(), "sync-1"); err != nil {
		t.Fatalf("AdvanceRecovered() error = %v", err)
	}
	if store.advancedID != "sync-1" {
		t.Fatalf("advancedID = %q, want sync-1", store.advancedID)
	}
}

func TestEngineDrainTasksThroughTaskStore(t *testing.T) {
	tasks := &fakeEngineTaskStore{}
	e := &Engine{tasks: tasks}

	if err := e.drainRemainingTasks(context.Background(), "sync-1"); err != nil {
		t.Fatalf("drainRemainingTasks() error = %v", err)
	}
	if err := e.drainGenerationTasks(context.Background(), "sync-1", 2); err != nil {
		t.Fatalf("drainGenerationTasks() error = %v", err)
	}

	tasks.hasRunningErr = errors.New("db down")
	if err := e.drainRemainingTasks(context.Background(), "sync-1"); err == nil {
		t.Fatal("expected error from drainRemainingTasks")
	}
}

func TestEngineReadFinalTaskOutcomesThroughTaskStore(t *testing.T) {
	tasks := &fakeEngineTaskStore{
		outcomes: []db.SyncTaskOutcome{
			{FilePath: "/a.txt", Status: "COMPLETED", Metadata: []byte(`{"action":"UPLOAD","side":"target"}`)},
			{FilePath: "/b.txt", Status: "SKIPPED", Metadata: []byte(`{}`)},
			{FilePath: "/c.txt", Status: "FAILED", Metadata: []byte(`{}`)},
		},
	}
	e := &Engine{tasks: tasks}

	stats, err := e.readFinalTaskOutcomes(context.Background(), "sync-1", 1)
	if err != nil {
		t.Fatalf("readFinalTaskOutcomes() error = %v", err)
	}
	if stats.total != 3 || stats.completed != 1 || stats.skipped != 1 || stats.failed != 1 {
		t.Fatalf("stats = %+v, unexpected counts", stats)
	}
	if !stats.protectedPaths["/b.txt"] || !stats.protectedPaths["/c.txt"] {
		t.Fatalf("protectedPaths = %v, want /b.txt and /c.txt", stats.protectedPaths)
	}
	if len(stats.completedOperations) != 1 || stats.completedOperations[0].filePath != "/a.txt" {
		t.Fatalf("completedOperations = %+v, want /a.txt", stats.completedOperations)
	}
}
