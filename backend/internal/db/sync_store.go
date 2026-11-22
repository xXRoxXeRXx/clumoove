package db

import (
	"context"
	"database/sql"
	"time"
)

// SyncStore is the sync domain's persistence seam for sync job passes: pass
// state, delta baselines and sync task fan-out, delegating to the package-level
// functions without adding queries. Schedule recovery and the engine's inline
// queries are not part of this seam yet.
type SyncStore interface {
	// Pass claiming and lifecycle. Generation-fenced transitions return false when the pass was superseded.
	GetSyncJob(id string) (*SyncJob, error)
	GetSyncJobContext(ctx context.Context, id string) (*SyncJob, error)
	GetSyncJobOwnerID(syncJobID string) (string, error)
	ClaimSyncJobPass(id string) (generation int, claimed bool, err error)
	ReleaseUnstartedSyncPass(id string, generation int) (bool, error)
	UpdateSyncJobTotals(id string, generation, totalFiles int, totalBytes int64) (bool, error)
	TransitionSyncJobToRunning(id string, generation int) (bool, error)
	TransitionSyncJobToVerifying(id string, generation int) (bool, error)
	AbortSyncJobVerification(id string, generation int) (bool, error)
	FailSyncJobPass(id string, generation int, errMsg string) (bool, error)
	FinalizeSyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error)
	FinalizeEmptySyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error)

	// Pause, resume and deletion keep the job and its schedules consistent.
	PauseSyncJobAndDeactivateSchedules(ctx context.Context, id string, errMsg *string) (generation int, paused bool, err error)
	ResumeSyncJobAndReactivateSchedules(ctx context.Context, id string, errMsg *string, nextRunAt time.Time) (bool, error)
	DeleteSyncJobCascade(syncJobID string) error

	// Delta baseline, task fan-out and credential rotation.
	ListSyncStateByJob(syncJobID string) ([]SyncState, error)
	BulkCreateSyncTasks(ctx context.Context, tasks []*Task) error
	SyncJobTokenSink(id, role string) OAuthReauthSink
}

type postgresSyncStore struct{ db *sql.DB }

// NewSyncStore returns the PostgreSQL-backed SyncStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewSyncStore(database *sql.DB) SyncStore {
	if database == nil {
		panic("db: NewSyncStore requires a database handle")
	}
	return postgresSyncStore{db: database}
}

func (s postgresSyncStore) GetSyncJob(id string) (*SyncJob, error) {
	return GetSyncJob(s.db, id)
}

func (s postgresSyncStore) GetSyncJobContext(ctx context.Context, id string) (*SyncJob, error) {
	return GetSyncJobContext(ctx, s.db, id)
}

func (s postgresSyncStore) GetSyncJobOwnerID(syncJobID string) (string, error) {
	return GetSyncJobOwnerID(s.db, syncJobID)
}

func (s postgresSyncStore) ClaimSyncJobPass(id string) (generation int, claimed bool, err error) {
	return ClaimSyncJobPass(s.db, id)
}

func (s postgresSyncStore) ReleaseUnstartedSyncPass(id string, generation int) (bool, error) {
	return ReleaseUnstartedSyncPass(s.db, id, generation)
}

func (s postgresSyncStore) UpdateSyncJobTotals(id string, generation, totalFiles int, totalBytes int64) (bool, error) {
	return UpdateSyncJobTotals(s.db, id, generation, totalFiles, totalBytes)
}

func (s postgresSyncStore) TransitionSyncJobToRunning(id string, generation int) (bool, error) {
	return TransitionSyncJobToRunning(s.db, id, generation)
}

func (s postgresSyncStore) TransitionSyncJobToVerifying(id string, generation int) (bool, error) {
	return TransitionSyncJobToVerifying(s.db, id, generation)
}

func (s postgresSyncStore) AbortSyncJobVerification(id string, generation int) (bool, error) {
	return AbortSyncJobVerification(s.db, id, generation)
}

func (s postgresSyncStore) FailSyncJobPass(id string, generation int, errMsg string) (bool, error) {
	return FailSyncJobPass(s.db, id, generation, errMsg)
}

func (s postgresSyncStore) FinalizeSyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return FinalizeSyncJobPassWithStates(s.db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes)
}

func (s postgresSyncStore) FinalizeEmptySyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return FinalizeEmptySyncJobPassWithStates(s.db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes)
}

func (s postgresSyncStore) PauseSyncJobAndDeactivateSchedules(ctx context.Context, id string, errMsg *string) (generation int, paused bool, err error) {
	return PauseSyncJobAndDeactivateSchedules(ctx, s.db, id, errMsg)
}

func (s postgresSyncStore) ResumeSyncJobAndReactivateSchedules(ctx context.Context, id string, errMsg *string, nextRunAt time.Time) (bool, error) {
	return ResumeSyncJobAndReactivateSchedules(ctx, s.db, id, errMsg, nextRunAt)
}

func (s postgresSyncStore) DeleteSyncJobCascade(syncJobID string) error {
	return DeleteSyncJobCascade(s.db, syncJobID)
}

func (s postgresSyncStore) ListSyncStateByJob(syncJobID string) ([]SyncState, error) {
	return ListSyncStateByJob(s.db, syncJobID)
}

func (s postgresSyncStore) BulkCreateSyncTasks(ctx context.Context, tasks []*Task) error {
	return BulkCreateSyncTasks(ctx, s.db, tasks)
}

func (s postgresSyncStore) SyncJobTokenSink(id, role string) OAuthReauthSink {
	return NewSyncJobTokenSink(s.db, id, role)
}
