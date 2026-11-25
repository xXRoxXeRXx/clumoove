package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"backend/internal/db"
)

// fakeRecoveryMigrationStore records the stale-indexing recovery calls the
// scheduler makes; any other call panics through the nil embedded interface.
type fakeRecoveryMigrationStore struct {
	db.MigrationStore
	recovered      []db.StaleIndexingMigration
	recoveredErr   error
	recoverMsg     string
	unscheduled    []db.StaleIndexingMigration
	unscheduledErr error
	reactivateErr  map[string]error
	reactivated    []string
	failed         []string
	failResult     bool
	failErr        error
}

func (f *fakeRecoveryMigrationStore) RecoverStaleScheduledIndexingMigrations(_ context.Context, msg string) ([]db.StaleIndexingMigration, error) {
	f.recoverMsg = msg
	return f.recovered, f.recoveredErr
}

func (f *fakeRecoveryMigrationStore) ReactivateMigrationSchedules(_ context.Context, id string) error {
	if err := f.reactivateErr[id]; err != nil {
		return err
	}
	f.reactivated = append(f.reactivated, id)
	return nil
}

func (f *fakeRecoveryMigrationStore) ListStaleUnscheduledIndexingMigrations(context.Context) ([]db.StaleIndexingMigration, error) {
	return f.unscheduled, f.unscheduledErr
}

func (f *fakeRecoveryMigrationStore) FailStaleIndexingMigration(_ context.Context, id string, _ *string) (bool, error) {
	f.failed = append(f.failed, id)
	return f.failResult, f.failErr
}

func TestOrphanedMigrationIndexingRecoveryUsesTheMigrationStore(t *testing.T) {
	t.Run("recovered scheduled migrations get their schedules reactivated", func(t *testing.T) {
		store := &fakeRecoveryMigrationStore{
			recovered: []db.StaleIndexingMigration{{ID: "mig-1"}, {ID: "mig-2"}},
		}
		s := &Scheduler{migrations: store}

		s.recoverOrphanedMigrationIndexing(context.Background())

		if store.recoverMsg != "Recovered from stale INDEXING (indexer lost)" {
			t.Errorf("recovery message = %q", store.recoverMsg)
		}
		if len(store.reactivated) != 2 || store.reactivated[0] != "mig-1" || store.reactivated[1] != "mig-2" {
			t.Errorf("reactivated = %v, want [mig-1 mig-2]", store.reactivated)
		}
	})

	t.Run("a failed reactivation does not stop the remaining migrations", func(t *testing.T) {
		store := &fakeRecoveryMigrationStore{
			recovered:     []db.StaleIndexingMigration{{ID: "mig-1"}, {ID: "mig-2"}},
			reactivateErr: map[string]error{"mig-1": errors.New("db down")},
		}
		s := &Scheduler{migrations: store}

		s.recoverOrphanedMigrationIndexing(context.Background())

		if len(store.reactivated) != 1 || store.reactivated[0] != "mig-2" {
			t.Errorf("reactivated = %v, want [mig-2]", store.reactivated)
		}
	})

	t.Run("migrations read before a recovery failure are still reactivated", func(t *testing.T) {
		store := &fakeRecoveryMigrationStore{
			recovered:    []db.StaleIndexingMigration{{ID: "mig-1"}},
			recoveredErr: errors.New("rows failed"),
		}
		s := &Scheduler{migrations: store}

		s.recoverOrphanedMigrationIndexing(context.Background())

		if len(store.reactivated) != 1 || store.reactivated[0] != "mig-1" {
			t.Errorf("reactivated = %v, want [mig-1]; they were already moved to SCHEDULED", store.reactivated)
		}
	})

	t.Run("unscheduled stale migrations are failed through the guarded transition", func(t *testing.T) {
		store := &fakeRecoveryMigrationStore{
			unscheduled: []db.StaleIndexingMigration{
				{ID: "mig-3", UserID: sql.NullString{String: "user-1", Valid: true}},
				{ID: "mig-4"},
			},
			failResult: true,
		}
		s := &Scheduler{migrations: store}

		s.recoverOrphanedMigrationIndexing(context.Background())

		if len(store.failed) != 2 || store.failed[0] != "mig-3" || store.failed[1] != "mig-4" {
			t.Errorf("failed = %v, want [mig-3 mig-4]", store.failed)
		}
		if len(store.reactivated) != 0 {
			t.Errorf("unscheduled migrations must not touch schedules, reactivated = %v", store.reactivated)
		}
	})

	t.Run("a failing scan is survivable and the unscheduled pass still runs", func(t *testing.T) {
		store := &fakeRecoveryMigrationStore{
			recoveredErr: errors.New("db down"),
			unscheduled:  []db.StaleIndexingMigration{{ID: "mig-5"}},
		}
		s := &Scheduler{migrations: store}

		s.recoverOrphanedMigrationIndexing(context.Background())

		if len(store.failed) != 1 || store.failed[0] != "mig-5" {
			t.Errorf("failed = %v, want [mig-5]", store.failed)
		}
	})
}
