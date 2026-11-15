package db

import (
	"context"
	"database/sql"
	"time"
)

// BackupStore is the backup domain's persistence seam: the operations the
// backup coordinator, scheduler and worker need for jobs, runs, snapshots, pack
// locators and repository maintenance. Orchestration depends on this interface
// so it can be tested against a fake and the SQL stays in this package.
//
// Method names and semantics deliberately match the package-level functions they
// delegate to; no query, ownership check or fencing predicate lives here. Owner
// facing CRUD used by the API is not part of this seam.
type BackupStore interface {
	BackupRepositoryCredentials
	// Run claiming, liveness and lifecycle. Every transition is fenced by job generation and run ID so a superseded worker cannot overwrite a newer pass.
	ClaimBackupJobPassContext(ctx context.Context, jobID, trigger string, scheduledLocalKey *string) (BackupPassClaim, error)
	ClaimNextQueuedBackupRunContext(ctx context.Context) (*BackupJob, *BackupRun, error)
	RecoverStaleBackupRunsContext(ctx context.Context) (int, error)
	TouchBackupRunContext(ctx context.Context, jobID string, generation int, runID string) error
	BackupRunActiveContext(ctx context.Context, jobID string, generation int, runID string) (bool, error)
	TransitionBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, nextState string) (bool, error)
	FinalizeBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, terminalState string, totalFiles int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, failedFiles int, errorCode *string) (bool, error)
	FailBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, errorCode string) (bool, error)
	GetBackupScheduleInfoContext(ctx context.Context, jobID string) (BackupScheduleInfo, error)
	GetBackupJobForOwnerContext(ctx context.Context, jobID, userID string) (*BackupJob, error)
	UpdateBackupJobOAuthTokens(ctx context.Context, backupJobID, role, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error
	BackupProfilesSameOAuthAccountContext(ctx context.Context, sourceProfileID, targetProfileID sql.NullString) (bool, error)
	DeleteBackupJobAfterRepositoryCleanupContext(ctx context.Context, jobID string) (bool, error)

	// Snapshot catalog and pack/block locators written during a run.
	CreateBackupSnapshotDraftContext(ctx context.Context, jobID, runID string, selectedRoots StringArray) (string, error)
	DiscardBackupSnapshotDraftContext(ctx context.Context, snapshotID, jobID, runID string) error
	GetLatestValidBackupSnapshotIDContext(ctx context.Context, jobID string) (string, error)
	GetBackupSnapshotFileCatalogContext(ctx context.Context, snapshotID string) (map[string]BackupSnapshotCatalogItem, error)
	FindBackupBlockContext(ctx context.Context, jobID string, hash []byte) (string, bool, error)
	RecordBackupPackAndBlocksContext(ctx context.Context, jobID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock) (map[string]string, error)
	BatchCreateBackupSnapshotItemsAndBlocksContext(ctx context.Context, snapshotID string, items []BatchSnapshotItem) error
	PublishBackupSnapshotAndFinalizeContext(ctx context.Context, jobID string, generation int, runID, snapshotID, snapshotState, runState string, totalFiles, totalDirs int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, unstableFiles, failedFiles int) (bool, error)
	ExpireBackupSnapshotsContext(ctx context.Context, jobID string, keep int) error

	// Repository maintenance: retention, compaction, deletion and verification.
	EnqueueBackupMaintenanceContext(ctx context.Context, jobID, kind string) error
	ClaimNextBackupMaintenanceForWorkerContext(ctx context.Context, workerID string) (*BackupMaintenance, error)
	IsBackupMaintenanceCancellingContext(ctx context.Context, maintenanceID string) (bool, error)
	CompleteBackupMaintenanceContext(ctx context.Context, maintenanceID string) error
	RetryBackupMaintenanceContext(ctx context.Context, maintenanceID, errorCode string) error
	ListBackupSnapshotPacksContext(ctx context.Context, jobID, snapshotID string) ([]BackupPack, error)
	ListBackupPacksForDeletionContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error)
	ListBackupReclaimablePacksContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error)
	ListBackupLiveBlocksContext(ctx context.Context, jobID, packID string) ([]BackupLiveBlock, error)
	FindBackupCompactionCandidateContext(ctx context.Context, jobID string) (*BackupPack, error)
	ReplaceBackupPackLocatorsContext(ctx context.Context, jobID, oldPackID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock, blockIDs []string) (string, error)
	MarkBackupPackDeletedContext(ctx context.Context, jobID, packID string) error
	MarkBackupPacksCheckedContext(ctx context.Context, jobID string, packIDs []string) error
	MarkBackupPackDamagedContext(ctx context.Context, jobID, packID string) error
	SnapshotBackupVerifyTargetsContext(ctx context.Context, maintenanceID, backupJobID string) error
	ClaimNextBackupVerifyTargetContext(ctx context.Context, maintenanceID, workerID string) (*BackupVerifyTarget, error)
	ReleaseBackupVerifyTargetClaimContext(ctx context.Context, targetID string, claimEpoch int64) error
	CompleteBackupVerifyTargetContext(ctx context.Context, targetID string, exists bool, expectedSize, actualSize int64, claimEpoch ...int64) (string, error)
	MarkBackupVerifyTargetReadContext(ctx context.Context, targetID string, bytes int64, claimEpoch ...int64) error
	MarkBackupVerifyTargetDamagedContext(ctx context.Context, targetID string, claimEpoch ...int64) error
	AdvanceBackupVerifyCursorContext(ctx context.Context, maintenanceID, targetID string) error
	AddBackupVerifyProcessedBytesContext(ctx context.Context, maintenanceID string, bytes int64) error
	CompleteBackupVerifyContext(ctx context.Context, maintenanceID string) error
}

type postgresBackupStore struct {
	db *sql.DB
	backupRepositoryCredentials
}

// NewBackupStore returns the PostgreSQL-backed BackupStore. It panics on a nil
// handle so a wiring mistake fails at startup instead of on the first query.
func NewBackupStore(database *sql.DB) BackupStore {
	if database == nil {
		panic("db: NewBackupStore requires a database handle")
	}
	return postgresBackupStore{db: database, backupRepositoryCredentials: backupRepositoryCredentials{db: database}}
}

func (s postgresBackupStore) ClaimBackupJobPassContext(ctx context.Context, jobID, trigger string, scheduledLocalKey *string) (BackupPassClaim, error) {
	return ClaimBackupJobPassContext(ctx, s.db, jobID, trigger, scheduledLocalKey)
}

func (s postgresBackupStore) ClaimNextQueuedBackupRunContext(ctx context.Context) (*BackupJob, *BackupRun, error) {
	return ClaimNextQueuedBackupRunContext(ctx, s.db)
}

func (s postgresBackupStore) RecoverStaleBackupRunsContext(ctx context.Context) (int, error) {
	return RecoverStaleBackupRunsContext(ctx, s.db)
}

func (s postgresBackupStore) TouchBackupRunContext(ctx context.Context, jobID string, generation int, runID string) error {
	return TouchBackupRunContext(ctx, s.db, jobID, generation, runID)
}

func (s postgresBackupStore) BackupRunActiveContext(ctx context.Context, jobID string, generation int, runID string) (bool, error) {
	return BackupRunActiveContext(ctx, s.db, jobID, generation, runID)
}

func (s postgresBackupStore) TransitionBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, nextState string) (bool, error) {
	return TransitionBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, nextState)
}

func (s postgresBackupStore) FinalizeBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, terminalState string, totalFiles int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, failedFiles int, errorCode *string) (bool, error) {
	return FinalizeBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, terminalState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, errorCode)
}

func (s postgresBackupStore) FailBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, errorCode string) (bool, error) {
	return FailBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, errorCode)
}

func (s postgresBackupStore) GetBackupScheduleInfoContext(ctx context.Context, jobID string) (BackupScheduleInfo, error) {
	return GetBackupScheduleInfoContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) GetBackupJobForOwnerContext(ctx context.Context, jobID, userID string) (*BackupJob, error) {
	return GetBackupJobForOwnerContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupStore) UpdateBackupJobOAuthTokens(ctx context.Context, backupJobID, role, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error {
	return UpdateBackupJobOAuthTokens(ctx, s.db, backupJobID, role, accessEncrypted, refreshEncrypted, expiresAt, expectedRefreshEncrypted)
}

func (s postgresBackupStore) BackupProfilesSameOAuthAccountContext(ctx context.Context, sourceProfileID, targetProfileID sql.NullString) (bool, error) {
	return BackupProfilesSameOAuthAccountContext(ctx, s.db, sourceProfileID, targetProfileID)
}

func (s postgresBackupStore) DeleteBackupJobAfterRepositoryCleanupContext(ctx context.Context, jobID string) (bool, error) {
	return DeleteBackupJobAfterRepositoryCleanupContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) CreateBackupSnapshotDraftContext(ctx context.Context, jobID, runID string, selectedRoots StringArray) (string, error) {
	return CreateBackupSnapshotDraftContext(ctx, s.db, jobID, runID, selectedRoots)
}

func (s postgresBackupStore) DiscardBackupSnapshotDraftContext(ctx context.Context, snapshotID, jobID, runID string) error {
	return DiscardBackupSnapshotDraftContext(ctx, s.db, snapshotID, jobID, runID)
}

func (s postgresBackupStore) GetLatestValidBackupSnapshotIDContext(ctx context.Context, jobID string) (string, error) {
	return GetLatestValidBackupSnapshotIDContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) GetBackupSnapshotFileCatalogContext(ctx context.Context, snapshotID string) (map[string]BackupSnapshotCatalogItem, error) {
	return GetBackupSnapshotFileCatalogContext(ctx, s.db, snapshotID)
}

func (s postgresBackupStore) FindBackupBlockContext(ctx context.Context, jobID string, hash []byte) (string, bool, error) {
	return FindBackupBlockContext(ctx, s.db, jobID, hash)
}

func (s postgresBackupStore) RecordBackupPackAndBlocksContext(ctx context.Context, jobID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock) (map[string]string, error) {
	return RecordBackupPackAndBlocksContext(ctx, s.db, jobID, remotePath, packHash, packSize, blocks)
}

func (s postgresBackupStore) BatchCreateBackupSnapshotItemsAndBlocksContext(ctx context.Context, snapshotID string, items []BatchSnapshotItem) error {
	return BatchCreateBackupSnapshotItemsAndBlocksContext(ctx, s.db, snapshotID, items)
}

func (s postgresBackupStore) PublishBackupSnapshotAndFinalizeContext(ctx context.Context, jobID string, generation int, runID, snapshotID, snapshotState, runState string, totalFiles, totalDirs int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, unstableFiles, failedFiles int) (bool, error) {
	return PublishBackupSnapshotAndFinalizeContext(ctx, s.db, jobID, generation, runID, snapshotID, snapshotState, runState, totalFiles, totalDirs, totalBytes, processedFiles, processedBytes, deduplicatedBytes, unstableFiles, failedFiles)
}

func (s postgresBackupStore) ExpireBackupSnapshotsContext(ctx context.Context, jobID string, keep int) error {
	return ExpireBackupSnapshotsContext(ctx, s.db, jobID, keep)
}

func (s postgresBackupStore) EnqueueBackupMaintenanceContext(ctx context.Context, jobID, kind string) error {
	return EnqueueBackupMaintenanceContext(ctx, s.db, jobID, kind)
}

func (s postgresBackupStore) ClaimNextBackupMaintenanceForWorkerContext(ctx context.Context, workerID string) (*BackupMaintenance, error) {
	return ClaimNextBackupMaintenanceForWorkerContext(ctx, s.db, workerID)
}

func (s postgresBackupStore) IsBackupMaintenanceCancellingContext(ctx context.Context, maintenanceID string) (bool, error) {
	return IsBackupMaintenanceCancellingContext(ctx, s.db, maintenanceID)
}

func (s postgresBackupStore) CompleteBackupMaintenanceContext(ctx context.Context, maintenanceID string) error {
	return CompleteBackupMaintenanceContext(ctx, s.db, maintenanceID)
}

func (s postgresBackupStore) RetryBackupMaintenanceContext(ctx context.Context, maintenanceID, errorCode string) error {
	return RetryBackupMaintenanceContext(ctx, s.db, maintenanceID, errorCode)
}

func (s postgresBackupStore) ListBackupSnapshotPacksContext(ctx context.Context, jobID, snapshotID string) ([]BackupPack, error) {
	return ListBackupSnapshotPacksContext(ctx, s.db, jobID, snapshotID)
}

func (s postgresBackupStore) ListBackupPacksForDeletionContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error) {
	return ListBackupPacksForDeletionContext(ctx, s.db, jobID, limit)
}

func (s postgresBackupStore) ListBackupReclaimablePacksContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error) {
	return ListBackupReclaimablePacksContext(ctx, s.db, jobID, limit)
}

func (s postgresBackupStore) ListBackupLiveBlocksContext(ctx context.Context, jobID, packID string) ([]BackupLiveBlock, error) {
	return ListBackupLiveBlocksContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) FindBackupCompactionCandidateContext(ctx context.Context, jobID string) (*BackupPack, error) {
	return FindBackupCompactionCandidateContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) ReplaceBackupPackLocatorsContext(ctx context.Context, jobID, oldPackID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock, blockIDs []string) (string, error) {
	return ReplaceBackupPackLocatorsContext(ctx, s.db, jobID, oldPackID, remotePath, packHash, packSize, blocks, blockIDs)
}

func (s postgresBackupStore) MarkBackupPackDeletedContext(ctx context.Context, jobID, packID string) error {
	return MarkBackupPackDeletedContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) MarkBackupPacksCheckedContext(ctx context.Context, jobID string, packIDs []string) error {
	return MarkBackupPacksCheckedContext(ctx, s.db, jobID, packIDs)
}

func (s postgresBackupStore) MarkBackupPackDamagedContext(ctx context.Context, jobID, packID string) error {
	return MarkBackupPackDamagedContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) SnapshotBackupVerifyTargetsContext(ctx context.Context, maintenanceID, backupJobID string) error {
	return SnapshotBackupVerifyTargetsContext(ctx, s.db, maintenanceID, backupJobID)
}

func (s postgresBackupStore) ClaimNextBackupVerifyTargetContext(ctx context.Context, maintenanceID, workerID string) (*BackupVerifyTarget, error) {
	return ClaimNextBackupVerifyTargetContext(ctx, s.db, maintenanceID, workerID)
}

func (s postgresBackupStore) ReleaseBackupVerifyTargetClaimContext(ctx context.Context, targetID string, claimEpoch int64) error {
	return ReleaseBackupVerifyTargetClaimContext(ctx, s.db, targetID, claimEpoch)
}

func (s postgresBackupStore) CompleteBackupVerifyTargetContext(ctx context.Context, targetID string, exists bool, expectedSize, actualSize int64, claimEpoch ...int64) (string, error) {
	return CompleteBackupVerifyTargetContext(ctx, s.db, targetID, exists, expectedSize, actualSize, claimEpoch...)
}

func (s postgresBackupStore) MarkBackupVerifyTargetReadContext(ctx context.Context, targetID string, bytes int64, claimEpoch ...int64) error {
	return MarkBackupVerifyTargetReadContext(ctx, s.db, targetID, bytes, claimEpoch...)
}

func (s postgresBackupStore) MarkBackupVerifyTargetDamagedContext(ctx context.Context, targetID string, claimEpoch ...int64) error {
	return MarkBackupVerifyTargetDamagedContext(ctx, s.db, targetID, claimEpoch...)
}

func (s postgresBackupStore) AdvanceBackupVerifyCursorContext(ctx context.Context, maintenanceID, targetID string) error {
	return AdvanceBackupVerifyCursorContext(ctx, s.db, maintenanceID, targetID)
}

func (s postgresBackupStore) AddBackupVerifyProcessedBytesContext(ctx context.Context, maintenanceID string, bytes int64) error {
	return AddBackupVerifyProcessedBytesContext(ctx, s.db, maintenanceID, bytes)
}

func (s postgresBackupStore) CompleteBackupVerifyContext(ctx context.Context, maintenanceID string) error {
	return CompleteBackupVerifyContext(ctx, s.db, maintenanceID)
}
