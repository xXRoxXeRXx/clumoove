package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

// fakeScheduleStore records the schedule writes processSchedule makes; any
// other call panics through the nil embedded interface.
type fakeScheduleStore struct {
	db.ScheduleStore
	advanceApplied bool
	advanced       []time.Time // expected next_run_at values passed to the compare-and-swap
	advancedTo     []time.Time
	deactivated    []string
}

func (f *fakeScheduleStore) UpdateNextRunAtIfUnchangedContext(_ context.Context, _ string, expected, next time.Time) (bool, error) {
	f.advanced = append(f.advanced, expected)
	f.advancedTo = append(f.advancedTo, next)
	return f.advanceApplied, nil
}

func (f *fakeScheduleStore) DeactivateScheduleContext(_ context.Context, id string) error {
	f.deactivated = append(f.deactivated, id)
	return nil
}

func scheduleDue() time.Time { return time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC) }

func migrationSchedule(cron string) *db.Schedule {
	s := &db.Schedule{
		ID:        "sched-1",
		TaskType:  "migration",
		TaskID:    "mig-1",
		NextRunAt: sql.NullTime{Time: scheduleDue(), Valid: true},
	}
	if cron != "" {
		s.CronExpression = sql.NullString{String: cron, Valid: true}
	}
	return s
}

func TestProcessScheduleWritesThroughTheScheduleStore(t *testing.T) {
	t.Run("an overlapping recurring run only advances next_run_at, guarded by the due time", func(t *testing.T) {
		schedules := &fakeScheduleStore{advanceApplied: true}
		s := &Scheduler{
			schedules:  schedules,
			migrations: &fakeMigrationStore{migration: &db.Migration{Status: "RUNNING"}},
		}

		s.processSchedule(context.Background(), migrationSchedule("*/30 * * * *"))

		if len(schedules.advanced) != 1 || !schedules.advanced[0].Equal(scheduleDue()) {
			t.Fatalf("advanced with %v, want one compare-and-swap on the due time %v", schedules.advanced, scheduleDue())
		}
		if len(schedules.deactivated) != 0 {
			t.Errorf("deactivated %v, want none for a recurring schedule", schedules.deactivated)
		}
		if len(schedules.advancedTo) != 1 || !schedules.advancedTo[0].After(scheduleDue()) {
			t.Errorf("next_run_at moved to %v, want a time after the due time %v", schedules.advancedTo, scheduleDue())
		}
	})

	t.Run("an overlapping one-shot run is deactivated", func(t *testing.T) {
		schedules := &fakeScheduleStore{}
		s := &Scheduler{
			schedules:  schedules,
			migrations: &fakeMigrationStore{migration: &db.Migration{Status: "INDEXING"}},
		}

		s.processSchedule(context.Background(), migrationSchedule(""))

		if len(schedules.deactivated) != 1 || schedules.deactivated[0] != "sched-1" {
			t.Fatalf("deactivated %v, want [sched-1]", schedules.deactivated)
		}
		if len(schedules.advanced) != 0 {
			t.Errorf("advanced %v, want none for a one-shot schedule", schedules.advanced)
		}
	})

	t.Run("a one-shot whose migration is no longer scheduled is retired", func(t *testing.T) {
		schedules := &fakeScheduleStore{}
		s := &Scheduler{
			schedules:  schedules,
			migrations: &fakeMigrationStore{migration: &db.Migration{Status: "SCHEDULED"}, claimed: false},
		}

		s.processSchedule(context.Background(), migrationSchedule(""))

		if len(schedules.deactivated) != 1 {
			t.Fatalf("deactivated %v, want the schedule retired after a lost claim", schedules.deactivated)
		}
	})

	t.Run("a transient trigger failure leaves the schedule active and due", func(t *testing.T) {
		schedules := &fakeScheduleStore{}
		s := &Scheduler{
			schedules: schedules,
			migrations: &fakeMigrationStore{
				migration: &db.Migration{Status: "SCHEDULED"},
				claimErr:  errors.New("database unavailable"),
			},
		}

		s.processSchedule(context.Background(), migrationSchedule("*/30 * * * *"))

		if len(schedules.deactivated) != 0 || len(schedules.advanced) != 0 {
			t.Fatalf("schedule was written (deactivated %v, advanced %v); it must stay due for the next tick", schedules.deactivated, schedules.advanced)
		}
	})
}
