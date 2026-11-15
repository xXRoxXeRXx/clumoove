package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

// fakeSyncStore overrides only the sync lookup the scheduler uses; any other
// call panics through the nil embedded interface.
type fakeSyncStore struct {
	db.SyncStore
	job      *db.SyncJob
	jobErr   error
	loadedID string
}

func (f *fakeSyncStore) GetSyncJobContext(_ context.Context, id string) (*db.SyncJob, error) {
	f.loadedID = id
	return f.job, f.jobErr
}

func TestNextRunForSyncScheduleUsesTheJobInterval(t *testing.T) {
	store := &fakeSyncStore{job: &db.SyncJob{IntervalMinutes: 90}}
	s := &Scheduler{syncs: store}

	before := time.Now()
	next, err := s.nextRunForSchedule(context.Background(), &db.Schedule{TaskType: "sync", TaskID: "sync-1"})

	if err != nil {
		t.Fatalf("nextRunForSchedule() error: %v", err)
	}
	if store.loadedID != "sync-1" {
		t.Errorf("loaded job %q, want sync-1", store.loadedID)
	}
	if got := next.Sub(before); got < 89*time.Minute || got > 91*time.Minute {
		t.Errorf("next run in %v, want about 90m", got)
	}
}

func TestNextRunForSyncScheduleWrapsLoadErrors(t *testing.T) {
	loadErr := errors.New("db down")
	s := &Scheduler{syncs: &fakeSyncStore{jobErr: loadErr}}

	_, err := s.nextRunForSchedule(context.Background(), &db.Schedule{TaskType: "sync", TaskID: "sync-1"})

	if !errors.Is(err, loadErr) {
		t.Fatalf("nextRunForSchedule() error = %v, want it to wrap %v", err, loadErr)
	}
}

func TestIsJobActiveForSyncJobs(t *testing.T) {
	tests := []struct {
		name    string
		store   *fakeSyncStore
		want    bool
		wantErr bool
	}{
		{"running job is active", &fakeSyncStore{job: &db.SyncJob{Status: "RUNNING"}}, true, false},
		{"idle job is not active", &fakeSyncStore{job: &db.SyncJob{Status: "IDLE"}}, false, false},
		{"deleted job is not active", &fakeSyncStore{jobErr: sql.ErrNoRows}, false, false},
		{"other load errors propagate", &fakeSyncStore{jobErr: errors.New("db down")}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Scheduler{syncs: tt.store}

			got, err := s.isJobActive(context.Background(), "sync", "sync-1")

			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("isJobActive() = (%v, %v), want (%v, err=%v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
