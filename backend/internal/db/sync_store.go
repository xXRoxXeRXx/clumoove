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

	// Worker side: the task processor and the checksum verifier. Calls that take
	// a run or verification generation are fenced by it, so a superseded pass
	// cannot write progress or status into its successor; claimed task writes
	// are fenced by the dequeue claim epoch carried on the task.
	UpdateSyncJobStatusForGeneration(id string, generation int, status string, errMsg *string) error
	RecoverConnectionLostSyncJob(ctx context.Context, id string) (bool, error)
	ReconcileSyncJobProgress(syncJobID string, generation int) error
	AddSyncJobLiveBytesForGeneration(ctx context.Context, id string, generation int, bytesDelta int64) error
	IncrementSyncJobProgressForGeneration(ctx context.Context, id string, generation, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error

	// Sync task rows: a claimed task's status and the job counters change
	// together so a retry can never count a file twice.
	UpdateClaimedSyncTaskStatus(ctx context.Context, t *Task) error
	UpdateSyncTaskStatusAndIncrementProgress(ctx context.Context, t *Task, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error
	CancelRemainingPendingSyncTasksForGeneration(syncJobID string, generation int) (int, error)

	// Verification pass. A verifier owns a pass through the generation it
	// claimed; renewal and release are fenced by it.
	ClaimSyncJobVerification(ctx context.Context, syncJobID string) (generation int, claimed bool, err error)
	RenewSyncJobVerificationLease(ctx context.Context, syncJobID string, runGeneration, verificationGeneration int) (bool, error)
	ReleaseSyncJobVerificationLease(ctx context.Context, syncJobID string, verificationGeneration int) error
	GetUnverifiedCompletedSyncTasks(ctx context.Context, syncJobID string, generation int) ([]*Task, error)

	// API side: creation, owner-scoped reads, listings, settings and
	// reports. GetOwnedSyncJobContext returns sql.ErrNoRows for a missing or
	// foreign job; VerifySyncJobOwnershipContext is the row-less variant.
	// Every other call is keyed by job id alone, so callers resolve
	// ownership first.
	CreateSyncJobAndSchedule(job *SyncJob, schedule *Schedule) (string, error)
	GetOwnedSyncJobContext(ctx context.Context, id, userID string) (*SyncJob, error)
	VerifySyncJobOwnershipContext(ctx context.Context, syncJobID, userID string) (bool, error)
	GetSyncJobsForUserContext(ctx context.Context, userID string) ([]SyncJob, error)
	ListAllSyncJobsContext(ctx context.Context, p SyncListParams) ([]AdminSyncView, int, error)

	// Settings and status. UpdateSyncJobStatus is an unfenced write for callers
	// that already hold ownership. UpdateSyncJobScope checks the job status inside
	// its transaction and FailSyncJobIfActive only fails a job that is still in
	// flight; the other setting writes are keyed by id alone.
	UpdateSyncJobStatus(id, status string, errMsg *string) error
	FailSyncJobIfActive(ctx context.Context, id, errMsg string) (bool, error)
	UpdateSyncJobBandwidthLimit(id string, limitMbps int) error
	UpdateSyncJobThreads(id string, threads int) error
	UpdateSyncJobInterval(syncJobID string, intervalMinutes int) error
	UpdateSyncJobScope(syncJobID string, selectedPaths []string, targetDir, conflictStrategy, direction string, deletePropagation *bool) error

	// Reports, live file lists and the credential-expiry scan.
	GetFailedSyncTasksForReport(syncJobID string, generation int) ([]Task, error)
	GetActiveSyncTaskPaths(ctx context.Context, syncJobID string) ([]string, error)
	GetVerifyingSyncTaskPaths(ctx context.Context, syncJobID string) ([]string, error)
	GetExpiringOAuthSyncJobs() ([]ExpiringOAuthSyncJob, error)
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
	return getSyncJob(s.db, id)
}

func (s postgresSyncStore) GetSyncJobContext(ctx context.Context, id string) (*SyncJob, error) {
	return getSyncJobContext(ctx, s.db, id)
}

func (s postgresSyncStore) GetSyncJobOwnerID(syncJobID string) (string, error) {
	return getSyncJobOwnerID(s.db, syncJobID)
}

func (s postgresSyncStore) ClaimSyncJobPass(id string) (generation int, claimed bool, err error) {
	return claimSyncJobPass(s.db, id)
}

func (s postgresSyncStore) ReleaseUnstartedSyncPass(id string, generation int) (bool, error) {
	return releaseUnstartedSyncPass(s.db, id, generation)
}

func (s postgresSyncStore) UpdateSyncJobTotals(id string, generation, totalFiles int, totalBytes int64) (bool, error) {
	return updateSyncJobTotals(s.db, id, generation, totalFiles, totalBytes)
}

func (s postgresSyncStore) TransitionSyncJobToRunning(id string, generation int) (bool, error) {
	return transitionSyncJobToRunning(s.db, id, generation)
}

func (s postgresSyncStore) TransitionSyncJobToVerifying(id string, generation int) (bool, error) {
	return transitionSyncJobToVerifying(s.db, id, generation)
}

func (s postgresSyncStore) AbortSyncJobVerification(id string, generation int) (bool, error) {
	return abortSyncJobVerification(s.db, id, generation)
}

func (s postgresSyncStore) FailSyncJobPass(id string, generation int, errMsg string) (bool, error) {
	return failSyncJobPass(s.db, id, generation, errMsg)
}

func (s postgresSyncStore) FinalizeSyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return finalizeRunningSyncPassWithStates(s.db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes)
}

func (s postgresSyncStore) FinalizeEmptySyncJobPassWithStates(id string, generation int, lastRunStatus string, errMsg *string, total, processed, changed, deleted, failed int, upserts []*SyncState, deletes []SyncStateDelete) (bool, error) {
	return finalizeEmptySyncJobPassWithStates(s.db, id, generation, lastRunStatus, errMsg, total, processed, changed, deleted, failed, upserts, deletes)
}

func (s postgresSyncStore) PauseSyncJobAndDeactivateSchedules(ctx context.Context, id string, errMsg *string) (generation int, paused bool, err error) {
	return pauseSyncJobAndDeactivateSchedules(ctx, s.db, id, errMsg)
}

func (s postgresSyncStore) ResumeSyncJobAndReactivateSchedules(ctx context.Context, id string, errMsg *string, nextRunAt time.Time) (bool, error) {
	return resumeSyncJobAndReactivateSchedules(ctx, s.db, id, errMsg, nextRunAt)
}

func (s postgresSyncStore) DeleteSyncJobCascade(syncJobID string) error {
	return deleteSyncJobCascade(s.db, syncJobID)
}

func (s postgresSyncStore) ListSyncStateByJob(syncJobID string) ([]SyncState, error) {
	return listSyncStateByJob(s.db, syncJobID)
}

func (s postgresSyncStore) BulkCreateSyncTasks(ctx context.Context, tasks []*Task) error {
	return bulkCreateSyncTasks(ctx, s.db, tasks)
}

func (s postgresSyncStore) UpdateSyncJobStatusForGeneration(id string, generation int, status string, errMsg *string) error {
	return updateSyncJobStatusForGeneration(s.db, id, generation, status, errMsg)
}

func (s postgresSyncStore) RecoverConnectionLostSyncJob(ctx context.Context, id string) (bool, error) {
	return recoverConnectionLostSyncJob(s.db, ctx, id)
}

func (s postgresSyncStore) ReconcileSyncJobProgress(syncJobID string, generation int) error {
	return reconcileSyncJobProgress(s.db, syncJobID, generation)
}

func (s postgresSyncStore) AddSyncJobLiveBytesForGeneration(ctx context.Context, id string, generation int, bytesDelta int64) error {
	return addSyncJobLiveBytesForGeneration(s.db, ctx, id, generation, bytesDelta)
}

func (s postgresSyncStore) IncrementSyncJobProgressForGeneration(ctx context.Context, id string, generation, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error {
	return incrementSyncJobProgressForGeneration(s.db, ctx, id, generation, filesDelta, changedDelta, deletedDelta, failedDelta, bytesDelta)
}

func (s postgresSyncStore) UpdateClaimedSyncTaskStatus(ctx context.Context, t *Task) error {
	return updateClaimedSyncTaskStatus(s.db, ctx, t)
}

func (s postgresSyncStore) UpdateSyncTaskStatusAndIncrementProgress(ctx context.Context, t *Task, filesDelta, changedDelta, deletedDelta, failedDelta int, bytesDelta int64) error {
	return updateSyncTaskStatusAndIncrementProgress(s.db, ctx, t, filesDelta, changedDelta, deletedDelta, failedDelta, bytesDelta)
}

func (s postgresSyncStore) CancelRemainingPendingSyncTasksForGeneration(syncJobID string, generation int) (int, error) {
	return cancelRemainingPendingSyncTasksForGeneration(s.db, syncJobID, generation)
}

func (s postgresSyncStore) ClaimSyncJobVerification(ctx context.Context, syncJobID string) (int, bool, error) {
	return claimSyncJobVerification(s.db, ctx, syncJobID)
}

func (s postgresSyncStore) RenewSyncJobVerificationLease(ctx context.Context, syncJobID string, runGeneration, verificationGeneration int) (bool, error) {
	return renewSyncJobVerificationLease(s.db, ctx, syncJobID, runGeneration, verificationGeneration)
}

func (s postgresSyncStore) ReleaseSyncJobVerificationLease(ctx context.Context, syncJobID string, verificationGeneration int) error {
	return releaseSyncJobVerificationLease(s.db, ctx, syncJobID, verificationGeneration)
}

func (s postgresSyncStore) GetUnverifiedCompletedSyncTasks(ctx context.Context, syncJobID string, generation int) ([]*Task, error) {
	return getUnverifiedCompletedSyncTasks(s.db, ctx, syncJobID, generation)
}

func (s postgresSyncStore) CreateSyncJobAndSchedule(job *SyncJob, schedule *Schedule) (string, error) {
	return createSyncJobAndSchedule(s.db, job, schedule)
}

func (s postgresSyncStore) GetOwnedSyncJobContext(ctx context.Context, id, userID string) (*SyncJob, error) {
	return getOwnedSyncJobContext(ctx, s.db, id, userID)
}

func (s postgresSyncStore) VerifySyncJobOwnershipContext(ctx context.Context, syncJobID, userID string) (bool, error) {
	return verifySyncJobOwnershipContext(ctx, s.db, syncJobID, userID)
}

func (s postgresSyncStore) GetSyncJobsForUserContext(ctx context.Context, userID string) ([]SyncJob, error) {
	return getSyncJobsForUserContext(ctx, s.db, userID)
}

func (s postgresSyncStore) ListAllSyncJobsContext(ctx context.Context, p SyncListParams) ([]AdminSyncView, int, error) {
	return listAllSyncJobsContext(ctx, s.db, p)
}

func (s postgresSyncStore) UpdateSyncJobStatus(id, status string, errMsg *string) error {
	return updateSyncJobStatus(s.db, id, status, errMsg)
}

func (s postgresSyncStore) FailSyncJobIfActive(ctx context.Context, id, errMsg string) (bool, error) {
	return failSyncJobIfActive(ctx, s.db, id, errMsg)
}

func (s postgresSyncStore) UpdateSyncJobBandwidthLimit(id string, limitMbps int) error {
	return updateSyncJobBandwidthLimit(s.db, id, limitMbps)
}

func (s postgresSyncStore) UpdateSyncJobThreads(id string, threads int) error {
	return updateSyncJobThreads(s.db, id, threads)
}

func (s postgresSyncStore) UpdateSyncJobInterval(syncJobID string, intervalMinutes int) error {
	return updateSyncJobInterval(s.db, syncJobID, intervalMinutes)
}

func (s postgresSyncStore) UpdateSyncJobScope(syncJobID string, selectedPaths []string, targetDir, conflictStrategy, direction string, deletePropagation *bool) error {
	return updateSyncJobScope(s.db, syncJobID, selectedPaths, targetDir, conflictStrategy, direction, deletePropagation)
}

func (s postgresSyncStore) GetFailedSyncTasksForReport(syncJobID string, generation int) ([]Task, error) {
	return getFailedSyncTasksForReport(s.db, syncJobID, generation)
}

func (s postgresSyncStore) GetActiveSyncTaskPaths(ctx context.Context, syncJobID string) ([]string, error) {
	return getActiveSyncTaskPaths(s.db, ctx, syncJobID)
}

func (s postgresSyncStore) GetVerifyingSyncTaskPaths(ctx context.Context, syncJobID string) ([]string, error) {
	return getVerifyingSyncTaskPaths(s.db, ctx, syncJobID)
}

func (s postgresSyncStore) GetExpiringOAuthSyncJobs() ([]ExpiringOAuthSyncJob, error) {
	return getExpiringOAuthSyncJobs(s.db)
}

func (s postgresSyncStore) SyncJobTokenSink(id, role string) OAuthReauthSink {
	return newSyncJobTokenSink(s.db, id, role)
}
