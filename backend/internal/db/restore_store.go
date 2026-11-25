package db

import (
	"context"
	"database/sql"
)

// RestoreStore is the restore domain's persistence seam for previews, runs and
// items: the operations restore planning and execution need. Safeguards (claim-epoch fencing, target-path reservation, preview lease
// generations) remain in the delegated functions and are not reimplemented here.
type RestoreStore interface {
	BackupRepositoryCredentials
	// Run and preview claiming, planning and recovery.
	ClaimNextQueuedRestoreRunForWorkerContext(ctx context.Context, workerID string) (*RestoreJob, *RestoreRun, error)
	PlanRestoreRunContext(ctx context.Context, job *RestoreJob, run *RestoreRun) (bool, error)
	FailRestoreRunPlanningContext(ctx context.Context, jobID, runID string, generation int, errorCode string) (bool, error)
	RecoverStaleRestorePlanningContext(ctx context.Context) error
	ClaimNextRestorePreviewForWorkerContext(ctx context.Context, workerID string) (*RestorePreview, error)
	RenewRestorePreviewLeaseContext(ctx context.Context, previewID string, generation int, workerID string) (bool, error)
	CompleteRestorePreviewWithStatsFencedContext(ctx context.Context, previewID string, generation int, workerID string, stats RestorePreviewStats) (bool, error)
	FailRestorePreviewFencedContext(ctx context.Context, previewID string, generation int, workerID, errorCode string) error
	ExpireRestorePreviewsContext(ctx context.Context) error
	ListRestorePreviewItemsContext(ctx context.Context, preview *RestorePreview) ([]RestorePreviewItem, error)
	FinalizeCompletedRestoreRunsContext(ctx context.Context) (int, error)
	FinalizeCancellingRestoreRunsContext(ctx context.Context) (int, error)

	// Item execution. Item mutations carry the claim epoch that fences a lost lease.
	ClaimNextRestoreItemForWorkerContext(ctx context.Context, workerID string) (*RestoreItem, error)
	HeartbeatRestoreItemContext(ctx context.Context, lease Lease, runID, workerID string) error
	RecoverStaleRestoreItemsContext(ctx context.Context) (int64, error)
	SkipBlockedRestoreItemsContext(ctx context.Context) (int64, error)
	ListRestoreItemBlocksContext(ctx context.Context, itemID, runID string) ([]RestoreItemBlock, error)
	SetRestoreItemTargetPathContext(ctx context.Context, lease Lease, runID, targetPath string) error
	SetRestoreDirectoryTargetPathContext(ctx context.Context, lease Lease, runID, snapshotPath, targetPath string) error
	ReserveRestoreTargetPathContext(ctx context.Context, runID, itemID, targetPath string) (bool, error)
	MoveRestoreTargetReservationContext(ctx context.Context, runID, itemID, targetPath string) (bool, error)
	CompleteRestoreItemContext(ctx context.Context, lease Lease, runID string, sizeBytes int64, verificationKind string) (bool, error)
	CompleteRestoreDirectoryContext(ctx context.Context, lease Lease, runID string) error
	WarnRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error
	RetryRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) (bool, error)
	FailRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error
	SkipRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error

	// Target credential snapshot owned by the restore run.
	GetRestoreRunTargetConnectionContext(ctx context.Context, runID string) (*RestoreRunTargetConnection, error)
	GetRestoreRunCredentialSnapshotContext(ctx context.Context, runID string) (accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime, err error)
	RestoreRunTokenSink(runID string) OAuthTokenSink
	// GetExpiringOAuthRestoreRuns lists active runs whose OAuth access token expires within 15 minutes, for the proactive rotation daemon.
	GetExpiringOAuthRestoreRuns() ([]ExpiringOAuthRestoreRun, error)
	UpdateRestoreRunMegaSessionContext(ctx context.Context, runID, sessionIDEncrypted, masterKeyEncrypted string) error
}

type postgresRestoreStore struct {
	db *sql.DB
	backupRepositoryCredentials
}

// NewRestoreStore returns the PostgreSQL-backed RestoreStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewRestoreStore(database *sql.DB) RestoreStore {
	if database == nil {
		panic("db: NewRestoreStore requires a database handle")
	}
	return postgresRestoreStore{db: database, backupRepositoryCredentials: backupRepositoryCredentials{db: database}}
}

func (s postgresRestoreStore) ClaimNextQueuedRestoreRunForWorkerContext(ctx context.Context, workerID string) (*RestoreJob, *RestoreRun, error) {
	return claimNextQueuedRestoreRunForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) PlanRestoreRunContext(ctx context.Context, job *RestoreJob, run *RestoreRun) (bool, error) {
	return planRestoreRunContext(ctx, s.db, job, run)
}

func (s postgresRestoreStore) FailRestoreRunPlanningContext(ctx context.Context, jobID, runID string, generation int, errorCode string) (bool, error) {
	return failRestoreRunPlanningContext(ctx, s.db, jobID, runID, generation, errorCode)
}

func (s postgresRestoreStore) RecoverStaleRestorePlanningContext(ctx context.Context) error {
	return recoverStaleRestorePlanningContext(ctx, s.db)
}

func (s postgresRestoreStore) ClaimNextRestorePreviewForWorkerContext(ctx context.Context, workerID string) (*RestorePreview, error) {
	return claimNextRestorePreviewForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) RenewRestorePreviewLeaseContext(ctx context.Context, previewID string, generation int, workerID string) (bool, error) {
	return renewRestorePreviewLeaseContext(ctx, s.db, previewID, generation, workerID)
}

func (s postgresRestoreStore) CompleteRestorePreviewWithStatsFencedContext(ctx context.Context, previewID string, generation int, workerID string, stats RestorePreviewStats) (bool, error) {
	return completeRestorePreviewWithStatsFencedContext(ctx, s.db, previewID, generation, workerID, stats)
}

func (s postgresRestoreStore) FailRestorePreviewFencedContext(ctx context.Context, previewID string, generation int, workerID, errorCode string) error {
	return failRestorePreviewFencedContext(ctx, s.db, previewID, generation, workerID, errorCode)
}

func (s postgresRestoreStore) ExpireRestorePreviewsContext(ctx context.Context) error {
	return expireRestorePreviewsContext(ctx, s.db)
}

func (s postgresRestoreStore) ListRestorePreviewItemsContext(ctx context.Context, preview *RestorePreview) ([]RestorePreviewItem, error) {
	return listRestorePreviewItemsContext(ctx, s.db, preview)
}

func (s postgresRestoreStore) FinalizeCompletedRestoreRunsContext(ctx context.Context) (int, error) {
	return finalizeCompletedRestoreRunsContext(ctx, s.db)
}

func (s postgresRestoreStore) FinalizeCancellingRestoreRunsContext(ctx context.Context) (int, error) {
	return finalizeCancellingRestoreRunsContext(ctx, s.db)
}

func (s postgresRestoreStore) ClaimNextRestoreItemForWorkerContext(ctx context.Context, workerID string) (*RestoreItem, error) {
	return claimNextRestoreItemForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) HeartbeatRestoreItemContext(ctx context.Context, lease Lease, runID, workerID string) error {
	return heartbeatRestoreItemContext(ctx, s.db, lease, runID, workerID)
}

func (s postgresRestoreStore) RecoverStaleRestoreItemsContext(ctx context.Context) (int64, error) {
	return recoverStaleRestoreItemsContext(ctx, s.db)
}

func (s postgresRestoreStore) SkipBlockedRestoreItemsContext(ctx context.Context) (int64, error) {
	return skipBlockedRestoreItemsContext(ctx, s.db)
}

func (s postgresRestoreStore) ListRestoreItemBlocksContext(ctx context.Context, itemID, runID string) ([]RestoreItemBlock, error) {
	return listRestoreItemBlocksContext(ctx, s.db, itemID, runID)
}

func (s postgresRestoreStore) SetRestoreItemTargetPathContext(ctx context.Context, lease Lease, runID, targetPath string) error {
	return setRestoreItemTargetPathContext(ctx, s.db, lease, runID, targetPath)
}

func (s postgresRestoreStore) SetRestoreDirectoryTargetPathContext(ctx context.Context, lease Lease, runID, snapshotPath, targetPath string) error {
	return setRestoreDirectoryTargetPathContext(ctx, s.db, lease, runID, snapshotPath, targetPath)
}

func (s postgresRestoreStore) ReserveRestoreTargetPathContext(ctx context.Context, runID, itemID, targetPath string) (bool, error) {
	return reserveRestoreTargetPathContext(ctx, s.db, runID, itemID, targetPath)
}

func (s postgresRestoreStore) MoveRestoreTargetReservationContext(ctx context.Context, runID, itemID, targetPath string) (bool, error) {
	return moveRestoreTargetReservationContext(ctx, s.db, runID, itemID, targetPath)
}

func (s postgresRestoreStore) CompleteRestoreItemContext(ctx context.Context, lease Lease, runID string, sizeBytes int64, verificationKind string) (bool, error) {
	return completeRestoreItemContext(ctx, s.db, lease, runID, sizeBytes, verificationKind)
}

func (s postgresRestoreStore) CompleteRestoreDirectoryContext(ctx context.Context, lease Lease, runID string) error {
	return completeRestoreDirectoryContext(ctx, s.db, lease, runID)
}

func (s postgresRestoreStore) WarnRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return warnRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) RetryRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) (bool, error) {
	return retryRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) FailRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return failRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) SkipRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return skipRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) GetRestoreRunTargetConnectionContext(ctx context.Context, runID string) (*RestoreRunTargetConnection, error) {
	return getRestoreRunTargetConnectionContext(ctx, s.db, runID)
}

func (s postgresRestoreStore) GetRestoreRunCredentialSnapshotContext(ctx context.Context, runID string) (accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime, err error) {
	return getRestoreRunCredentialSnapshotContext(ctx, s.db, runID)
}

func (s postgresRestoreStore) RestoreRunTokenSink(runID string) OAuthTokenSink {
	return NewRestoreRunTokenSink(s.db, runID)
}

func (s postgresRestoreStore) UpdateRestoreRunMegaSessionContext(ctx context.Context, runID, sessionIDEncrypted, masterKeyEncrypted string) error {
	return updateRestoreRunMegaSessionContext(ctx, s.db, runID, sessionIDEncrypted, masterKeyEncrypted)
}

func (s postgresRestoreStore) GetExpiringOAuthRestoreRuns() ([]ExpiringOAuthRestoreRun, error) {
	return getExpiringOAuthRestoreRuns(s.db)
}
