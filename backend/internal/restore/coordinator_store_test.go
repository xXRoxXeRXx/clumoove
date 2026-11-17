package restore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"backend/internal/db"
)

// fakeRestoreStore implements the persistence PollOnce uses on the paths under
// test. Any other store call panics through the nil embedded interface, which
// also pins which persistence a path is allowed to touch.
type fakeRestoreStore struct {
	db.RestoreStore
	calls []string

	recovered   int64
	recoverErr  error
	expireErr   error
	planningErr error
	preview     *db.RestorePreview
	job         *db.RestoreJob
	run         *db.RestoreRun
	planErr     error
	failPlanErr error
	skipped     int64
	item        *db.RestoreItem
	finalized   int
	cancelled   int
	connectErr  error
	failedPlan  struct {
		jobID, runID, code string
		generation         int
	}
	failedItem  itemOutcome
	retriedItem itemOutcome
	claimedAs   []string
}

type itemOutcome struct {
	called     bool
	lease db.Lease
	runID string
	code  string
}

func (f *fakeRestoreStore) note(name string) { f.calls = append(f.calls, name) }

func (f *fakeRestoreStore) RecoverStaleRestoreItemsContext(context.Context) (int64, error) {
	f.note("recover-items")
	return f.recovered, f.recoverErr
}
func (f *fakeRestoreStore) ExpireRestorePreviewsContext(context.Context) error {
	f.note("expire-previews")
	return f.expireErr
}
func (f *fakeRestoreStore) RecoverStaleRestorePlanningContext(context.Context) error {
	f.note("recover-planning")
	return f.planningErr
}
func (f *fakeRestoreStore) ClaimNextRestorePreviewForWorkerContext(_ context.Context, workerID string) (*db.RestorePreview, error) {
	f.note("claim-preview")
	f.claimedAs = append(f.claimedAs, workerID)
	return f.preview, nil
}
func (f *fakeRestoreStore) ClaimNextQueuedRestoreRunForWorkerContext(_ context.Context, workerID string) (*db.RestoreJob, *db.RestoreRun, error) {
	f.note("claim-run")
	f.claimedAs = append(f.claimedAs, workerID)
	return f.job, f.run, nil
}
func (f *fakeRestoreStore) PlanRestoreRunContext(context.Context, *db.RestoreJob, *db.RestoreRun) (bool, error) {
	f.note("plan-run")
	return f.planErr == nil, f.planErr
}
func (f *fakeRestoreStore) FailRestoreRunPlanningContext(_ context.Context, jobID, runID string, generation int, code string) (bool, error) {
	f.note("fail-planning")
	f.failedPlan.jobID, f.failedPlan.runID, f.failedPlan.generation, f.failedPlan.code = jobID, runID, generation, code
	return f.failPlanErr == nil, f.failPlanErr
}
func (f *fakeRestoreStore) SkipBlockedRestoreItemsContext(context.Context) (int64, error) {
	f.note("skip-blocked")
	return f.skipped, nil
}
func (f *fakeRestoreStore) ClaimNextRestoreItemForWorkerContext(_ context.Context, workerID string) (*db.RestoreItem, error) {
	f.note("claim-item")
	f.claimedAs = append(f.claimedAs, workerID)
	return f.item, nil
}
func (f *fakeRestoreStore) FinalizeCompletedRestoreRunsContext(context.Context) (int, error) {
	f.note("finalize-completed")
	return f.finalized, nil
}
func (f *fakeRestoreStore) FinalizeCancellingRestoreRunsContext(context.Context) (int, error) {
	f.note("finalize-cancelling")
	return f.cancelled, nil
}
func (f *fakeRestoreStore) GetRestoreRunTargetConnectionContext(context.Context, string) (*db.RestoreRunTargetConnection, error) {
	f.note("load-target-connection")
	return nil, f.connectErr
}
func (f *fakeRestoreStore) FailRestoreItemContext(_ context.Context, lease db.Lease, runID, code string) error {
	f.note("fail-item")
	f.failedItem = itemOutcome{called: true, lease: lease, runID: runID, code: code}
	return nil
}
func (f *fakeRestoreStore) RetryRestoreItemContext(_ context.Context, lease db.Lease, runID, code string) (bool, error) {
	f.note("retry-item")
	f.retriedItem = itemOutcome{called: true, lease: lease, runID: runID, code: code}
	return true, nil
}

func newFakeStoreCoordinator(store *fakeRestoreStore) *Coordinator {
	return &Coordinator{store: store, workerID: "restore-worker-3"}
}

func TestPollOnceReportsWorkWhenStaleItemsWereRecovered(t *testing.T) {
	store := &fakeRestoreStore{recovered: 2}

	worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if !reflect.DeepEqual(store.calls, []string{"recover-items"}) {
		t.Errorf("store calls = %v, want recovery only", store.calls)
	}
}

func TestPollOnceWrapsHousekeepingFailuresWithoutClaimingWork(t *testing.T) {
	boom := errors.New("db down")
	cases := map[string]struct {
		store   *fakeRestoreStore
		message string
		calls   []string
	}{
		"recover items":    {&fakeRestoreStore{recoverErr: boom}, "recover stale restore items", []string{"recover-items"}},
		"expire previews":  {&fakeRestoreStore{expireErr: boom}, "expire restore previews", []string{"recover-items", "expire-previews"}},
		"recover planning": {&fakeRestoreStore{planningErr: boom}, "recover stale restore planning", []string{"recover-items", "expire-previews", "recover-planning"}},
	}
	for name, tc := range cases {
		worked, err := newFakeStoreCoordinator(tc.store).PollOnce(context.Background())

		if worked || !errors.Is(err, boom) || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: PollOnce() = (%v, %v), want (false, error wrapping %q)", name, worked, err, tc.message)
		}
		if !reflect.DeepEqual(tc.store.calls, tc.calls) {
			t.Errorf("%s: store calls = %v, want %v", name, tc.store.calls, tc.calls)
		}
	}
}

func TestPollOnceFailsPlanningWithRunGenerationFence(t *testing.T) {
	store := &fakeRestoreStore{
		job:     &db.RestoreJob{ID: "job-1"},
		run:     &db.RestoreRun{ID: "run-1", Generation: 6},
		planErr: errors.New("catalog unavailable"),
	}

	worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if got := store.failedPlan; got.jobID != "job-1" || got.runID != "run-1" || got.generation != 6 || got.code != "RESTORE_PLANNING_FAILED" {
		t.Errorf("planning failure = %+v, want job-1/run-1/generation 6/RESTORE_PLANNING_FAILED", got)
	}
	if store.claimedAs[0] != "restore-worker-3" {
		t.Errorf("claimed as %q, want the coordinator worker ID", store.claimedAs[0])
	}
}

func TestPollOnceSurfacesBothErrorsWhenPlanningFailureCannotBePersisted(t *testing.T) {
	store := &fakeRestoreStore{
		job:         &db.RestoreJob{ID: "job-1"},
		run:         &db.RestoreRun{ID: "run-1", Generation: 1},
		planErr:     errors.New("plan boom"),
		failPlanErr: errors.New("persist boom"),
	}

	worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

	if !worked || err == nil || !strings.Contains(err.Error(), "plan boom") || !strings.Contains(err.Error(), "persist boom") {
		t.Fatalf("PollOnce() = (%v, %v), want (true, error naming both failures)", worked, err)
	}
}

func TestPollOnceStopsAfterSkippingBlockedItems(t *testing.T) {
	store := &fakeRestoreStore{skipped: 3}

	worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	for _, call := range store.calls {
		if call == "claim-item" {
			t.Errorf("an item was claimed in the same poll that skipped blocked items: %v", store.calls)
		}
	}
}

func TestPollOnceFinalizesRunsOnlyWhenNoItemIsRunnable(t *testing.T) {
	for name, tc := range map[string]struct {
		finalized, cancelled int
		want                 bool
	}{
		"nothing to finalize": {0, 0, false},
		"completed run":       {1, 0, true},
		"cancelled run":       {0, 2, true},
	} {
		store := &fakeRestoreStore{finalized: tc.finalized, cancelled: tc.cancelled}

		worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

		if err != nil || worked != tc.want {
			t.Errorf("%s: PollOnce() = (%v, %v), want (%v, nil)", name, worked, err, tc.want)
		}
		tail := store.calls[len(store.calls)-3:]
		if !reflect.DeepEqual(tail, []string{"claim-item", "finalize-completed", "finalize-cancelling"}) {
			t.Errorf("%s: tail of store calls = %v", name, tail)
		}
	}
}

func TestPollOnceRetriesTransientItemFailuresWithClaimEpoch(t *testing.T) {
	store := &fakeRestoreStore{
		item:       &db.RestoreItem{ID: "item-1", RestoreRunID: "run-1", ClaimEpoch: 9},
		connectErr: errors.New("connection reset"),
	}

	worked, err := newFakeStoreCoordinator(store).PollOnce(context.Background())

	if err != nil || !worked {
		t.Fatalf("PollOnce() = (%v, %v), want (true, nil)", worked, err)
	}
	if store.failedItem.called {
		t.Error("transient failure must not fail the item permanently")
	}
	got := store.retriedItem
	if !got.called || got.lease != (db.Lease{ID: "item-1", Epoch: 9}) || got.runID != "run-1" || got.code != "RESTORE_TRANSFER_FAILED" {
		t.Errorf("retry = %+v, want item-1/run-1/RESTORE_TRANSFER_FAILED/epoch 9", got)
	}
}

func TestPollOnceFailsPermanentItemErrorsWithoutRetry(t *testing.T) {
	for name, tc := range map[string]struct {
		cause error
		code  string
	}{
		"corrupt repository": {ErrRepositoryCorrupt, "RESTORE_TRANSFER_FAILED"},
		"type conflict":      {ErrRestoreTypeConflict, "RESTORE_TYPE_CONFLICT"},
	} {
		store := &fakeRestoreStore{
			item:       &db.RestoreItem{ID: "item-2", RestoreRunID: "run-2", ClaimEpoch: 4},
			connectErr: tc.cause,
		}

		if _, err := newFakeStoreCoordinator(store).PollOnce(context.Background()); err != nil {
			t.Fatalf("%s: PollOnce() error: %v", name, err)
		}

		if store.retriedItem.called {
			t.Errorf("%s: permanent failure must not be retried", name)
		}
		got := store.failedItem
		if !got.called || got.lease != (db.Lease{ID: "item-2", Epoch: 4}) || got.code != tc.code {
			t.Errorf("%s: failure = %+v, want item-2/%s/epoch 4", name, got, tc.code)
		}
	}
}
