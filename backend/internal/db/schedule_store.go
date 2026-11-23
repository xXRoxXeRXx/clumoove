package db

import (
	"context"
	"database/sql"
	"time"
)

// ScheduleStore is the persistence seam for the schedules table as the API and
// the scheduler daemon see it: owner-scoped reads and deletion for the schedule
// endpoints, and the due-schedule loop's claim, advance and retire writes. It
// delegates to package-level functions without adding queries.
//
// Creation, reactivation and the status-coupled changes stay inside the
// migration, sync and backup stores, which run them in the same transaction as
// the job they belong to.
//
// Ownership is enforced only by VerifyScheduleOwnershipContext and
// GetSchedulesForUserContext; every other call is keyed by schedule or task id
// alone, so API callers verify ownership first.
type ScheduleStore interface {
	GetScheduleContext(ctx context.Context, id string) (*Schedule, error)
	GetSchedulesForUserContext(ctx context.Context, userID string) ([]Schedule, error)
	VerifyScheduleOwnershipContext(ctx context.Context, scheduleID, userID string) (bool, error)
	DeleteSchedule(id string) error
	DeleteSchedulesForTask(taskType, taskID string) error

	// Scheduler loop. UpdateNextRunAtIfUnchangedContext is a compare-and-swap on
	// the next_run_at the scheduler read, so a schedule edited meanwhile is not
	// overwritten; it reports false when the schedule changed.
	GetDueSchedulesContext(ctx context.Context) ([]Schedule, error)
	UpdateNextRunAtIfUnchangedContext(ctx context.Context, id string, expectedNextRunAt, nextRunAt time.Time) (bool, error)
	DeactivateScheduleContext(ctx context.Context, id string) error
}

type postgresScheduleStore struct{ db *sql.DB }

// NewScheduleStore returns the PostgreSQL-backed ScheduleStore. It panics on a
// nil handle so a wiring mistake fails at startup instead of on the first query.
func NewScheduleStore(database *sql.DB) ScheduleStore {
	if database == nil {
		panic("db: NewScheduleStore requires a database handle")
	}
	return postgresScheduleStore{db: database}
}

func (s postgresScheduleStore) GetScheduleContext(ctx context.Context, id string) (*Schedule, error) {
	return getScheduleContext(ctx, s.db, id)
}

func (s postgresScheduleStore) GetSchedulesForUserContext(ctx context.Context, userID string) ([]Schedule, error) {
	return getSchedulesForUserContext(ctx, s.db, userID)
}

func (s postgresScheduleStore) VerifyScheduleOwnershipContext(ctx context.Context, scheduleID, userID string) (bool, error) {
	return verifyScheduleOwnershipContext(ctx, s.db, scheduleID, userID)
}

func (s postgresScheduleStore) DeleteSchedule(id string) error {
	return deleteSchedule(s.db, id)
}

func (s postgresScheduleStore) DeleteSchedulesForTask(taskType, taskID string) error {
	return deleteSchedulesForTask(s.db, taskType, taskID)
}

func (s postgresScheduleStore) GetDueSchedulesContext(ctx context.Context) ([]Schedule, error) {
	return getDueSchedulesContext(ctx, s.db)
}

func (s postgresScheduleStore) UpdateNextRunAtIfUnchangedContext(ctx context.Context, id string, expectedNextRunAt, nextRunAt time.Time) (bool, error) {
	return updateNextRunAtIfUnchangedContext(ctx, s.db, id, expectedNextRunAt, nextRunAt)
}

func (s postgresScheduleStore) DeactivateScheduleContext(ctx context.Context, id string) error {
	return deactivateScheduleContext(ctx, s.db, id)
}
