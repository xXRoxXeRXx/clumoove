package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// TaskStore is the persistence seam for rows of the tasks table as the
// workers and the API see them: loading a task, fenced status changes,
// claim heartbeats, checksum verification marks and the sync error list. It
// delegates to package-level functions without adding queries.
//
// Migration and sync tasks share the table, so the interface carries both
// flavours; the Sync-named calls are additionally fenced by a pass
// generation. Dequeueing itself (SKIP LOCKED) stays in internal/queue.
//
// Every write that takes a Lease is fenced by the dequeue claim epoch: a task
// that was recovered or reclaimed reports sql.ErrNoRows (or false) and the
// caller must stop instead of committing stale results. The unfenced
// UpdateTaskStatus is only for callers that own the task by other means.
type TaskStore interface {
	GetTask(id string) (*Task, error)
	UpdateTaskStatus(t *Task) error
	UpdateClaimedTaskStatus(ctx context.Context, t *Task) error
	TransitionClaimedTask(ctx context.Context, lease Lease, status string) error
	UpdateClaimedTaskResolvedTargetPath(ctx context.Context, lease Lease, targetPath string) error
	UpdateClaimedTaskMetadata(ctx context.Context, lease Lease, metadata json.RawMessage) error

	// Claim heartbeats report false once the claim (and, for sync, the pass
	// generation) no longer owns the task.
	HeartbeatTaskClaim(ctx context.Context, lease Lease) (bool, error)
	HeartbeatSyncTaskClaim(ctx context.Context, lease Lease, generation int) (bool, error)

	// Checksum verification. The sync marks are fenced by the run and the
	// verification generation and report false when either was superseded.
	GetUnverifiedCompletedTasks(ctx context.Context, migrationID string) ([]*Task, error)
	MarkTaskChecksumVerified(ctx context.Context, taskID, targetHash string) error
	MarkSyncTaskChecksumVerifiedWhileVerifying(ctx context.Context, taskID, targetHash string, runGeneration, verificationGeneration int) (bool, error)
	MarkSyncTaskChecksumMismatchWhileVerifying(ctx context.Context, task *Task, runGeneration, verificationGeneration int) (bool, error)

	GetSyncErrorsContext(ctx context.Context, syncJobID string, limit, offset int) ([]ErrorListItem, int, error)

	// Worker maintenance sweeps over both flavours of task. Each reports how
	// many tasks went back to PENDING so the caller can wake idle workers.
	// RequeueDueFailedTasks re-enqueues FAILED tasks whose retry time (against
	// now) has passed, for active migrations and the current pass of active sync
	// jobs, in one transaction. RequeueOrphanedRunningTasks resets RUNNING tasks
	// untouched for ten minutes whose parent is still active.
	RequeueDueFailedTasks(ctx context.Context, now time.Time) (int64, error)
	RequeueOrphanedRunningTasks(ctx context.Context) (int64, error)
}

func (s postgresTaskStore) RequeueDueFailedTasks(ctx context.Context, now time.Time) (int64, error) {
	return requeueDueFailedTasks(ctx, s.db, now)
}

func (s postgresTaskStore) RequeueOrphanedRunningTasks(ctx context.Context) (int64, error) {
	return requeueOrphanedRunningTasks(ctx, s.db)
}

type postgresTaskStore struct{ db *sql.DB }

// NewTaskStore returns the PostgreSQL-backed TaskStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewTaskStore(database *sql.DB) TaskStore {
	if database == nil {
		panic("db: NewTaskStore requires a database handle")
	}
	return postgresTaskStore{db: database}
}

func (s postgresTaskStore) GetTask(id string) (*Task, error) {
	return getTask(s.db, id)
}

func (s postgresTaskStore) UpdateTaskStatus(t *Task) error {
	return updateTaskStatus(s.db, t)
}

func (s postgresTaskStore) UpdateClaimedTaskStatus(ctx context.Context, t *Task) error {
	return updateClaimedTaskStatus(s.db, ctx, t)
}

func (s postgresTaskStore) TransitionClaimedTask(ctx context.Context, lease Lease, status string) error {
	return transitionClaimedTask(s.db, ctx, lease, status)
}

func (s postgresTaskStore) UpdateClaimedTaskResolvedTargetPath(ctx context.Context, lease Lease, targetPath string) error {
	return updateClaimedTaskResolvedTargetPath(s.db, ctx, lease, targetPath)
}

func (s postgresTaskStore) UpdateClaimedTaskMetadata(ctx context.Context, lease Lease, metadata json.RawMessage) error {
	return updateClaimedTaskMetadata(s.db, ctx, lease, metadata)
}

func (s postgresTaskStore) HeartbeatTaskClaim(ctx context.Context, lease Lease) (bool, error) {
	return heartbeatTaskClaim(s.db, ctx, lease)
}

func (s postgresTaskStore) HeartbeatSyncTaskClaim(ctx context.Context, lease Lease, generation int) (bool, error) {
	return heartbeatSyncTaskClaim(s.db, ctx, lease, generation)
}

func (s postgresTaskStore) GetUnverifiedCompletedTasks(ctx context.Context, migrationID string) ([]*Task, error) {
	return getUnverifiedCompletedTasks(s.db, ctx, migrationID)
}

func (s postgresTaskStore) MarkTaskChecksumVerified(ctx context.Context, taskID, targetHash string) error {
	return markTaskChecksumVerified(s.db, ctx, taskID, targetHash)
}

func (s postgresTaskStore) MarkSyncTaskChecksumVerifiedWhileVerifying(ctx context.Context, taskID, targetHash string, runGeneration, verificationGeneration int) (bool, error) {
	return markSyncTaskChecksumVerifiedWhileVerifying(s.db, ctx, taskID, targetHash, runGeneration, verificationGeneration)
}

func (s postgresTaskStore) MarkSyncTaskChecksumMismatchWhileVerifying(ctx context.Context, task *Task, runGeneration, verificationGeneration int) (bool, error) {
	return markSyncTaskChecksumMismatchWhileVerifying(s.db, ctx, task, runGeneration, verificationGeneration)
}

func (s postgresTaskStore) GetSyncErrorsContext(ctx context.Context, syncJobID string, limit, offset int) ([]ErrorListItem, int, error) {
	return getSyncErrorsContext(ctx, s.db, syncJobID, limit, offset)
}
