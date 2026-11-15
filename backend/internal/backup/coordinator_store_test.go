package backup

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"backend/internal/db"
)

// fakeBackupStore implements only the persistence PollOnce touches when no work
// is queued. Any other store call panics via the nil embedded interface, so the
// tests also pin which persistence an idle poll is allowed to use.
type fakeBackupStore struct {
	db.BackupStore
	calls          []string
	recoverErr     error
	claimRunErr    error
	claimMaintErr  error
	maintenanceFor string
}

func (f *fakeBackupStore) RecoverStaleBackupRunsContext(context.Context) (int, error) {
	f.calls = append(f.calls, "recover")
	return 0, f.recoverErr
}

func (f *fakeBackupStore) ClaimNextQueuedBackupRunContext(context.Context) (*db.BackupJob, *db.BackupRun, error) {
	f.calls = append(f.calls, "claim-run")
	return nil, nil, f.claimRunErr
}

func (f *fakeBackupStore) ClaimNextBackupMaintenanceForWorkerContext(_ context.Context, workerID string) (*db.BackupMaintenance, error) {
	f.calls = append(f.calls, "claim-maintenance")
	f.maintenanceFor = workerID
	return nil, f.claimMaintErr
}

func TestPollOnceIdleClaimsRunThenMaintenanceForThisWorker(t *testing.T) {
	store := &fakeBackupStore{}
	c := &Coordinator{store: store, workerID: "backup-worker-7"}

	worked, err := c.PollOnce(context.Background())

	if err != nil || worked {
		t.Fatalf("PollOnce() = (%v, %v), want (false, nil)", worked, err)
	}
	want := []string{"recover", "claim-run", "claim-maintenance"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Errorf("store calls = %v, want %v", store.calls, want)
	}
	if store.maintenanceFor != "backup-worker-7" {
		t.Errorf("maintenance claimed for %q, want the coordinator worker ID", store.maintenanceFor)
	}
	if !c.preferMaintenance {
		t.Error("an idle run poll must prefer maintenance on the next poll")
	}
}

func TestPollOncePrefersMaintenanceAfterARunPoll(t *testing.T) {
	store := &fakeBackupStore{}
	c := &Coordinator{store: store, workerID: "w", preferMaintenance: true}

	if _, err := c.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce() error: %v", err)
	}

	want := []string{"recover", "claim-maintenance", "claim-run", "claim-maintenance"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Errorf("store calls = %v, want %v", store.calls, want)
	}
}

func TestPollOnceStopsWhenStaleRunRecoveryFails(t *testing.T) {
	boom := errors.New("recovery unavailable")
	store := &fakeBackupStore{recoverErr: boom}
	c := &Coordinator{store: store, workerID: "w"}

	worked, err := c.PollOnce(context.Background())

	if !errors.Is(err, boom) || worked {
		t.Fatalf("PollOnce() = (%v, %v), want (false, recovery error)", worked, err)
	}
	if !reflect.DeepEqual(store.calls, []string{"recover"}) {
		t.Errorf("store calls = %v, want only recovery", store.calls)
	}
}

func TestPollOncePropagatesClaimErrors(t *testing.T) {
	boom := errors.New("claim failed")
	for name, store := range map[string]*fakeBackupStore{
		"run":         {claimRunErr: boom},
		"maintenance": {claimMaintErr: boom},
	} {
		c := &Coordinator{store: store, workerID: "w"}

		worked, err := c.PollOnce(context.Background())

		if !errors.Is(err, boom) || worked {
			t.Errorf("%s: PollOnce() = (%v, %v), want (false, claim error)", name, worked, err)
		}
	}
}
