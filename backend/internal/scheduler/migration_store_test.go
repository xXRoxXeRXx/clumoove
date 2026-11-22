package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"backend/internal/db"
)

// fakeMigrationStore overrides only the migration calls the scheduler makes;
// any other call panics through the nil embedded interface.
type fakeMigrationStore struct {
	db.MigrationStore
	migration  *db.Migration
	getErr     error
	claimed    bool
	claimErr   error
	claimedIDs []string
}

func (f *fakeMigrationStore) GetMigrationContext(context.Context, string) (*db.Migration, error) {
	return f.migration, f.getErr
}

func (f *fakeMigrationStore) ClaimScheduledMigrationForIndexingContext(_ context.Context, id string) (bool, error) {
	f.claimedIDs = append(f.claimedIDs, id)
	return f.claimed, f.claimErr
}

func TestIsJobActiveForMigrationsUsesTheMigrationStore(t *testing.T) {
	tests := []struct {
		name    string
		store   *fakeMigrationStore
		want    bool
		wantErr bool
	}{
		{"running migration is active", &fakeMigrationStore{migration: &db.Migration{Status: "RUNNING"}}, true, false},
		{"completed migration is not active", &fakeMigrationStore{migration: &db.Migration{Status: "COMPLETED"}}, false, false},
		{"missing migration is not active", &fakeMigrationStore{getErr: sql.ErrNoRows}, false, false},
		{"lookup failure is reported", &fakeMigrationStore{getErr: errors.New("db down")}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Scheduler{migrations: tt.store}
			got, err := s.isJobActive(context.Background(), "migration", "mig-1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("isJobActive() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("isJobActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTriggerMigrationReportsAnUnclaimableMigration(t *testing.T) {
	t.Run("a lost claim retires the schedule via sql.ErrNoRows", func(t *testing.T) {
		store := &fakeMigrationStore{claimed: false}
		s := &Scheduler{migrations: store}

		err := s.triggerMigration(context.Background(), "mig-1")

		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("triggerMigration() error = %v, want it to wrap sql.ErrNoRows", err)
		}
		if len(store.claimedIDs) != 1 || store.claimedIDs[0] != "mig-1" {
			t.Errorf("claimed %v, want [mig-1]", store.claimedIDs)
		}
	})

	t.Run("a claim failure is wrapped, not mistaken for a lost claim", func(t *testing.T) {
		claimErr := errors.New("db down")
		s := &Scheduler{migrations: &fakeMigrationStore{claimErr: claimErr}}

		err := s.triggerMigration(context.Background(), "mig-1")

		if !errors.Is(err, claimErr) || errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("triggerMigration() error = %v, want it to wrap only %v", err, claimErr)
		}
	})
}
