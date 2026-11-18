package db

import (
	"context"
	"database/sql"
	"time"
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
	UpdateRestoreRunOAuthTokens(ctx context.Context, runID, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error
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
	return ClaimNextQueuedRestoreRunForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) PlanRestoreRunContext(ctx context.Context, job *RestoreJob, run *RestoreRun) (bool, error) {
	return PlanRestoreRunContext(ctx, s.db, job, run)
}

func (s postgresRestoreStore) FailRestoreRunPlanningContext(ctx context.Context, jobID, runID string, generation int, errorCode string) (bool, error) {
	return FailRestoreRunPlanningContext(ctx, s.db, jobID, runID, generation, errorCode)
}

func (s postgresRestoreStore) RecoverStaleRestorePlanningContext(ctx context.Context) error {
	return RecoverStaleRestorePlanningContext(ctx, s.db)
}

func (s postgresRestoreStore) ClaimNextRestorePreviewForWorkerContext(ctx context.Context, workerID string) (*RestorePreview, error) {
	return ClaimNextRestorePreviewForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) RenewRestorePreviewLeaseContext(ctx context.Context, previewID string, generation int, workerID string) (bool, error) {
	return RenewRestorePreviewLeaseContext(ctx, s.db, previewID, generation, workerID)
}

func (s postgresRestoreStore) CompleteRestorePreviewWithStatsFencedContext(ctx context.Context, previewID string, generation int, workerID string, stats RestorePreviewStats) (bool, error) {
	return CompleteRestorePreviewWithStatsFencedContext(ctx, s.db, previewID, generation, workerID, stats)
}

func (s postgresRestoreStore) FailRestorePreviewFencedContext(ctx context.Context, previewID string, generation int, workerID, errorCode string) error {
	return FailRestorePreviewFencedContext(ctx, s.db, previewID, generation, workerID, errorCode)
}

func (s postgresRestoreStore) ExpireRestorePreviewsContext(ctx context.Context) error {
	return ExpireRestorePreviewsContext(ctx, s.db)
}

func (s postgresRestoreStore) ListRestorePreviewItemsContext(ctx context.Context, preview *RestorePreview) ([]RestorePreviewItem, error) {
	return ListRestorePreviewItemsContext(ctx, s.db, preview)
}

func (s postgresRestoreStore) FinalizeCompletedRestoreRunsContext(ctx context.Context) (int, error) {
	return FinalizeCompletedRestoreRunsContext(ctx, s.db)
}

func (s postgresRestoreStore) FinalizeCancellingRestoreRunsContext(ctx context.Context) (int, error) {
	return FinalizeCancellingRestoreRunsContext(ctx, s.db)
}

func (s postgresRestoreStore) ClaimNextRestoreItemForWorkerContext(ctx context.Context, workerID string) (*RestoreItem, error) {
	return ClaimNextRestoreItemForWorkerContext(ctx, s.db, workerID)
}

func (s postgresRestoreStore) HeartbeatRestoreItemContext(ctx context.Context, lease Lease, runID, workerID string) error {
	return HeartbeatRestoreItemContext(ctx, s.db, lease, runID, workerID)
}

func (s postgresRestoreStore) RecoverStaleRestoreItemsContext(ctx context.Context) (int64, error) {
	return RecoverStaleRestoreItemsContext(ctx, s.db)
}

func (s postgresRestoreStore) SkipBlockedRestoreItemsContext(ctx context.Context) (int64, error) {
	return SkipBlockedRestoreItemsContext(ctx, s.db)
}

func (s postgresRestoreStore) ListRestoreItemBlocksContext(ctx context.Context, itemID, runID string) ([]RestoreItemBlock, error) {
	return ListRestoreItemBlocksContext(ctx, s.db, itemID, runID)
}

func (s postgresRestoreStore) SetRestoreItemTargetPathContext(ctx context.Context, lease Lease, runID, targetPath string) error {
	return SetRestoreItemTargetPathContext(ctx, s.db, lease, runID, targetPath)
}

func (s postgresRestoreStore) SetRestoreDirectoryTargetPathContext(ctx context.Context, lease Lease, runID, snapshotPath, targetPath string) error {
	return SetRestoreDirectoryTargetPathContext(ctx, s.db, lease, runID, snapshotPath, targetPath)
}

func (s postgresRestoreStore) ReserveRestoreTargetPathContext(ctx context.Context, runID, itemID, targetPath string) (bool, error) {
	return ReserveRestoreTargetPathContext(ctx, s.db, runID, itemID, targetPath)
}

func (s postgresRestoreStore) MoveRestoreTargetReservationContext(ctx context.Context, runID, itemID, targetPath string) (bool, error) {
	return MoveRestoreTargetReservationContext(ctx, s.db, runID, itemID, targetPath)
}

func (s postgresRestoreStore) CompleteRestoreItemContext(ctx context.Context, lease Lease, runID string, sizeBytes int64, verificationKind string) (bool, error) {
	return CompleteRestoreItemContext(ctx, s.db, lease, runID, sizeBytes, verificationKind)
}

func (s postgresRestoreStore) CompleteRestoreDirectoryContext(ctx context.Context, lease Lease, runID string) error {
	return CompleteRestoreDirectoryContext(ctx, s.db, lease, runID)
}

func (s postgresRestoreStore) WarnRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return WarnRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) RetryRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) (bool, error) {
	return RetryRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) FailRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return FailRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) SkipRestoreItemContext(ctx context.Context, lease Lease, runID, errorCode string) error {
	return SkipRestoreItemContext(ctx, s.db, lease, runID, errorCode)
}

func (s postgresRestoreStore) GetRestoreRunTargetConnectionContext(ctx context.Context, runID string) (*RestoreRunTargetConnection, error) {
	return GetRestoreRunTargetConnectionContext(ctx, s.db, runID)
}

func (s postgresRestoreStore) GetRestoreRunCredentialSnapshotContext(ctx context.Context, runID string) (accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime, err error) {
	return GetRestoreRunCredentialSnapshotContext(ctx, s.db, runID)
}

func (s postgresRestoreStore) UpdateRestoreRunOAuthTokens(ctx context.Context, runID, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error {
	return UpdateRestoreRunOAuthTokens(ctx, s.db, runID, accessEncrypted, refreshEncrypted, expiresAt, expectedRefreshEncrypted)
}

func (s postgresRestoreStore) UpdateRestoreRunMegaSessionContext(ctx context.Context, runID, sessionIDEncrypted, masterKeyEncrypted string) error {
	return UpdateRestoreRunMegaSessionContext(ctx, s.db, runID, sessionIDEncrypted, masterKeyEncrypted)
}
