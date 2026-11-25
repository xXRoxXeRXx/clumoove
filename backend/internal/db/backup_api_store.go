package db

import (
	"context"
	"database/sql"
	"time"
)

// BackupAPIStore is the persistence seam for the owner-facing backup API: job
// CRUD and lifecycle, runs, snapshots and repository checks. Worker-side
// orchestration uses BackupStore instead. It delegates to the package-level
// functions without adding queries.
//
// Job and check calls that take a user id scope their queries on it, so a job
// that does not exist or belongs to someone else is indistinguishable
// (sql.ErrNoRows or false). The snapshot listings, the restorable check and the
// manual run claim are keyed by job id alone: callers must resolve ownership
// first (VerifyBackupJobOwnershipContext). Credentials in CreateBackupJobParams
// are already encrypted by the caller; the store never sees plaintext or the key.
type BackupAPIStore interface {
	// Jobs.
	ListBackupJobsForOwnerContext(ctx context.Context, userID string) ([]BackupJob, error)
	// GetBackupJobForOwnerContext returns sql.ErrNoRows for an absent or foreign job.
	GetBackupJobForOwnerContext(ctx context.Context, jobID, userID string) (*BackupJob, error)
	VerifyBackupJobOwnershipContext(ctx context.Context, jobID, userID string) (bool, error)
	// CreateBackupJobContext inserts the job and its schedule in one transaction.
	CreateBackupJobContext(ctx context.Context, p CreateBackupJobParams) (string, error)
	// UpdateBackupJobContext returns sql.ErrNoRows for an absent, foreign or deleting job.
	UpdateBackupJobContext(ctx context.Context, jobID, userID, cronExpression, timezone string, retentionCount, threads int, nextRun *time.Time) error
	// PauseBackupJobContext and ResumeBackupJobContext report false, changing
	// nothing, when the job is not in a state that allows the transition.
	PauseBackupJobContext(ctx context.Context, jobID, userID string) (bool, error)
	ResumeBackupJobContext(ctx context.Context, jobID, userID string, nextRun func(cronExpression, timezone string) (time.Time, error)) (bool, error)
	ClaimBackupJobPassContext(ctx context.Context, jobID, trigger string, scheduledLocalKey *string) (BackupPassClaim, error)
	// RequestBackupRepositoryDeletionContext reports false when deletion cannot start.
	RequestBackupRepositoryDeletionContext(ctx context.Context, jobID, userID string) (bool, error)

	// Runs and snapshots.
	ListBackupRunsForOwnerContext(ctx context.Context, jobID, userID string) ([]BackupRun, error)
	ListVisibleBackupSnapshotsContext(ctx context.Context, jobID string) ([]BackupSnapshot, error)
	ListBackupSnapshotChildrenContext(ctx context.Context, jobID, snapshotID, directory string) ([]BackupSnapshotItem, error)
	// BackupSnapshotRestorableContext reports whether the snapshot belongs to the
	// job and is published and undamaged.
	BackupSnapshotRestorableContext(ctx context.Context, jobID, snapshotID string) (bool, error)

	// Repository checks (verification).
	CreateBackupVerifyContext(ctx context.Context, jobID, userID, mode string, byteBudget *int64) (string, error)
	GetBackupVerifyForOwnerContext(ctx context.Context, verifyID, userID string) (*BackupMaintenance, error)
	CancelBackupVerifyForOwnerContext(ctx context.Context, verifyID, userID string) (bool, error)
	ListBackupVerifiesForOwnerContext(ctx context.Context, jobID, userID string) ([]BackupMaintenance, error)
}

type postgresBackupAPIStore struct{ db *sql.DB }

// NewBackupAPIStore returns the PostgreSQL-backed BackupAPIStore. It panics on a
// nil handle so a wiring mistake fails at startup instead of on the first query.
func NewBackupAPIStore(database *sql.DB) BackupAPIStore {
	if database == nil {
		panic("db: NewBackupAPIStore requires a database handle")
	}
	return postgresBackupAPIStore{db: database}
}

func (s postgresBackupAPIStore) ListBackupJobsForOwnerContext(ctx context.Context, userID string) ([]BackupJob, error) {
	return listBackupJobsForOwnerContext(ctx, s.db, userID)
}

func (s postgresBackupAPIStore) GetBackupJobForOwnerContext(ctx context.Context, jobID, userID string) (*BackupJob, error) {
	return getBackupJobForOwnerContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupAPIStore) VerifyBackupJobOwnershipContext(ctx context.Context, jobID, userID string) (bool, error) {
	return verifyBackupJobOwnershipContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupAPIStore) CreateBackupJobContext(ctx context.Context, p CreateBackupJobParams) (string, error) {
	return createBackupJobContext(ctx, s.db, p)
}

func (s postgresBackupAPIStore) UpdateBackupJobContext(ctx context.Context, jobID, userID, cronExpression, timezone string, retentionCount, threads int, nextRun *time.Time) error {
	return updateBackupJobContext(ctx, s.db, jobID, userID, cronExpression, timezone, retentionCount, threads, nextRun)
}

func (s postgresBackupAPIStore) PauseBackupJobContext(ctx context.Context, jobID, userID string) (bool, error) {
	return pauseBackupJobContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupAPIStore) ResumeBackupJobContext(ctx context.Context, jobID, userID string, nextRun func(cronExpression, timezone string) (time.Time, error)) (bool, error) {
	return resumeBackupJobContext(ctx, s.db, jobID, userID, nextRun)
}

func (s postgresBackupAPIStore) ClaimBackupJobPassContext(ctx context.Context, jobID, trigger string, scheduledLocalKey *string) (BackupPassClaim, error) {
	return claimBackupJobPassContext(ctx, s.db, jobID, trigger, scheduledLocalKey)
}

func (s postgresBackupAPIStore) RequestBackupRepositoryDeletionContext(ctx context.Context, jobID, userID string) (bool, error) {
	return requestBackupRepositoryDeletionContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupAPIStore) ListBackupRunsForOwnerContext(ctx context.Context, jobID, userID string) ([]BackupRun, error) {
	return listBackupRunsForOwnerContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupAPIStore) ListVisibleBackupSnapshotsContext(ctx context.Context, jobID string) ([]BackupSnapshot, error) {
	return listVisibleBackupSnapshotsContext(ctx, s.db, jobID)
}

func (s postgresBackupAPIStore) ListBackupSnapshotChildrenContext(ctx context.Context, jobID, snapshotID, directory string) ([]BackupSnapshotItem, error) {
	return listBackupSnapshotChildrenContext(ctx, s.db, jobID, snapshotID, directory)
}

func (s postgresBackupAPIStore) BackupSnapshotRestorableContext(ctx context.Context, jobID, snapshotID string) (bool, error) {
	return backupSnapshotRestorableContext(ctx, s.db, jobID, snapshotID)
}

func (s postgresBackupAPIStore) CreateBackupVerifyContext(ctx context.Context, jobID, userID, mode string, byteBudget *int64) (string, error) {
	return createBackupVerifyContext(ctx, s.db, jobID, userID, mode, byteBudget)
}

func (s postgresBackupAPIStore) GetBackupVerifyForOwnerContext(ctx context.Context, verifyID, userID string) (*BackupMaintenance, error) {
	return getBackupVerifyForOwnerContext(ctx, s.db, verifyID, userID)
}

func (s postgresBackupAPIStore) CancelBackupVerifyForOwnerContext(ctx context.Context, verifyID, userID string) (bool, error) {
	return cancelBackupVerifyForOwnerContext(ctx, s.db, verifyID, userID)
}

func (s postgresBackupAPIStore) ListBackupVerifiesForOwnerContext(ctx context.Context, jobID, userID string) ([]BackupMaintenance, error) {
	return listBackupVerifiesForOwnerContext(ctx, s.db, jobID, userID)
}
