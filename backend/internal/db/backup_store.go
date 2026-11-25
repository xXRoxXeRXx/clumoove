package db

import (
	"context"
	"database/sql"
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
	BackupJobTokenSink(backupJobID, role string) OAuthTokenSink
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
	ReleaseBackupVerifyTargetClaimContext(ctx context.Context, lease Lease) error
	CompleteBackupVerifyTargetContext(ctx context.Context, lease Lease, exists bool, expectedSize, actualSize int64) (string, error)
	MarkBackupVerifyTargetReadContext(ctx context.Context, lease Lease, bytes int64) error
	MarkBackupVerifyTargetDamagedContext(ctx context.Context, lease Lease) error
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
	return claimBackupJobPassContext(ctx, s.db, jobID, trigger, scheduledLocalKey)
}

func (s postgresBackupStore) ClaimNextQueuedBackupRunContext(ctx context.Context) (*BackupJob, *BackupRun, error) {
	return claimNextQueuedBackupRunContext(ctx, s.db)
}

func (s postgresBackupStore) RecoverStaleBackupRunsContext(ctx context.Context) (int, error) {
	return recoverStaleBackupRunsContext(ctx, s.db)
}

func (s postgresBackupStore) TouchBackupRunContext(ctx context.Context, jobID string, generation int, runID string) error {
	return touchBackupRunContext(ctx, s.db, jobID, generation, runID)
}

func (s postgresBackupStore) BackupRunActiveContext(ctx context.Context, jobID string, generation int, runID string) (bool, error) {
	return backupRunActiveContext(ctx, s.db, jobID, generation, runID)
}

func (s postgresBackupStore) TransitionBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, nextState string) (bool, error) {
	return transitionBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, nextState)
}

func (s postgresBackupStore) FinalizeBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, terminalState string, totalFiles int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, failedFiles int, errorCode *string) (bool, error) {
	return finalizeBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, terminalState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, errorCode)
}

func (s postgresBackupStore) FailBackupRunContext(ctx context.Context, jobID string, generation int, runID, expectedState, errorCode string) (bool, error) {
	return failBackupRunContext(ctx, s.db, jobID, generation, runID, expectedState, errorCode)
}

func (s postgresBackupStore) GetBackupScheduleInfoContext(ctx context.Context, jobID string) (BackupScheduleInfo, error) {
	return getBackupScheduleInfoContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) GetBackupJobForOwnerContext(ctx context.Context, jobID, userID string) (*BackupJob, error) {
	return getBackupJobForOwnerContext(ctx, s.db, jobID, userID)
}

func (s postgresBackupStore) BackupJobTokenSink(backupJobID, role string) OAuthTokenSink {
	return NewBackupJobTokenSink(s.db, backupJobID, role)
}

func (s postgresBackupStore) BackupProfilesSameOAuthAccountContext(ctx context.Context, sourceProfileID, targetProfileID sql.NullString) (bool, error) {
	return backupProfilesSameOAuthAccountContext(ctx, s.db, sourceProfileID, targetProfileID)
}

func (s postgresBackupStore) DeleteBackupJobAfterRepositoryCleanupContext(ctx context.Context, jobID string) (bool, error) {
	return deleteBackupJobAfterRepositoryCleanupContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) CreateBackupSnapshotDraftContext(ctx context.Context, jobID, runID string, selectedRoots StringArray) (string, error) {
	return createBackupSnapshotDraftContext(ctx, s.db, jobID, runID, selectedRoots)
}

func (s postgresBackupStore) DiscardBackupSnapshotDraftContext(ctx context.Context, snapshotID, jobID, runID string) error {
	return discardBackupSnapshotDraftContext(ctx, s.db, snapshotID, jobID, runID)
}

func (s postgresBackupStore) GetLatestValidBackupSnapshotIDContext(ctx context.Context, jobID string) (string, error) {
	return getLatestValidBackupSnapshotIDContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) GetBackupSnapshotFileCatalogContext(ctx context.Context, snapshotID string) (map[string]BackupSnapshotCatalogItem, error) {
	return getBackupSnapshotFileCatalogContext(ctx, s.db, snapshotID)
}

func (s postgresBackupStore) FindBackupBlockContext(ctx context.Context, jobID string, hash []byte) (string, bool, error) {
	return findBackupBlockContext(ctx, s.db, jobID, hash)
}

func (s postgresBackupStore) RecordBackupPackAndBlocksContext(ctx context.Context, jobID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock) (map[string]string, error) {
	return recordBackupPackAndBlocksContext(ctx, s.db, jobID, remotePath, packHash, packSize, blocks)
}

func (s postgresBackupStore) BatchCreateBackupSnapshotItemsAndBlocksContext(ctx context.Context, snapshotID string, items []BatchSnapshotItem) error {
	return batchCreateBackupSnapshotItemsAndBlocksContext(ctx, s.db, snapshotID, items)
}

func (s postgresBackupStore) PublishBackupSnapshotAndFinalizeContext(ctx context.Context, jobID string, generation int, runID, snapshotID, snapshotState, runState string, totalFiles, totalDirs int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, unstableFiles, failedFiles int) (bool, error) {
	return publishBackupSnapshotAndFinalizeContext(ctx, s.db, jobID, generation, runID, snapshotID, snapshotState, runState, totalFiles, totalDirs, totalBytes, processedFiles, processedBytes, deduplicatedBytes, unstableFiles, failedFiles)
}

func (s postgresBackupStore) ExpireBackupSnapshotsContext(ctx context.Context, jobID string, keep int) error {
	return expireBackupSnapshotsContext(ctx, s.db, jobID, keep)
}

func (s postgresBackupStore) EnqueueBackupMaintenanceContext(ctx context.Context, jobID, kind string) error {
	return enqueueBackupMaintenanceContext(ctx, s.db, jobID, kind)
}

func (s postgresBackupStore) ClaimNextBackupMaintenanceForWorkerContext(ctx context.Context, workerID string) (*BackupMaintenance, error) {
	return claimNextBackupMaintenanceForWorkerContext(ctx, s.db, workerID)
}

func (s postgresBackupStore) IsBackupMaintenanceCancellingContext(ctx context.Context, maintenanceID string) (bool, error) {
	return isBackupMaintenanceCancellingContext(ctx, s.db, maintenanceID)
}

func (s postgresBackupStore) CompleteBackupMaintenanceContext(ctx context.Context, maintenanceID string) error {
	return completeBackupMaintenanceContext(ctx, s.db, maintenanceID)
}

func (s postgresBackupStore) RetryBackupMaintenanceContext(ctx context.Context, maintenanceID, errorCode string) error {
	return retryBackupMaintenanceContext(ctx, s.db, maintenanceID, errorCode)
}

func (s postgresBackupStore) ListBackupSnapshotPacksContext(ctx context.Context, jobID, snapshotID string) ([]BackupPack, error) {
	return listBackupSnapshotPacksContext(ctx, s.db, jobID, snapshotID)
}

func (s postgresBackupStore) ListBackupPacksForDeletionContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error) {
	return listBackupPacksForDeletionContext(ctx, s.db, jobID, limit)
}

func (s postgresBackupStore) ListBackupReclaimablePacksContext(ctx context.Context, jobID string, limit int) ([]BackupPack, error) {
	return listBackupReclaimablePacksContext(ctx, s.db, jobID, limit)
}

func (s postgresBackupStore) ListBackupLiveBlocksContext(ctx context.Context, jobID, packID string) ([]BackupLiveBlock, error) {
	return listBackupLiveBlocksContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) FindBackupCompactionCandidateContext(ctx context.Context, jobID string) (*BackupPack, error) {
	return findBackupCompactionCandidateContext(ctx, s.db, jobID)
}

func (s postgresBackupStore) ReplaceBackupPackLocatorsContext(ctx context.Context, jobID, oldPackID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock, blockIDs []string) (string, error) {
	return replaceBackupPackLocatorsContext(ctx, s.db, jobID, oldPackID, remotePath, packHash, packSize, blocks, blockIDs)
}

func (s postgresBackupStore) MarkBackupPackDeletedContext(ctx context.Context, jobID, packID string) error {
	return markBackupPackDeletedContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) MarkBackupPacksCheckedContext(ctx context.Context, jobID string, packIDs []string) error {
	return markBackupPacksCheckedContext(ctx, s.db, jobID, packIDs)
}

func (s postgresBackupStore) MarkBackupPackDamagedContext(ctx context.Context, jobID, packID string) error {
	return markBackupPackDamagedContext(ctx, s.db, jobID, packID)
}

func (s postgresBackupStore) SnapshotBackupVerifyTargetsContext(ctx context.Context, maintenanceID, backupJobID string) error {
	return snapshotBackupVerifyTargetsContext(ctx, s.db, maintenanceID, backupJobID)
}

func (s postgresBackupStore) ClaimNextBackupVerifyTargetContext(ctx context.Context, maintenanceID, workerID string) (*BackupVerifyTarget, error) {
	return claimNextBackupVerifyTargetContext(ctx, s.db, maintenanceID, workerID)
}

func (s postgresBackupStore) ReleaseBackupVerifyTargetClaimContext(ctx context.Context, lease Lease) error {
	return releaseBackupVerifyTargetClaimContext(ctx, s.db, lease)
}

func (s postgresBackupStore) CompleteBackupVerifyTargetContext(ctx context.Context, lease Lease, exists bool, expectedSize, actualSize int64) (string, error) {
	return completeBackupVerifyTargetContext(ctx, s.db, lease, exists, expectedSize, actualSize)
}

func (s postgresBackupStore) MarkBackupVerifyTargetReadContext(ctx context.Context, lease Lease, bytes int64) error {
	return markBackupVerifyTargetReadContext(ctx, s.db, lease, bytes)
}

func (s postgresBackupStore) MarkBackupVerifyTargetDamagedContext(ctx context.Context, lease Lease) error {
	return markBackupVerifyTargetDamagedContext(ctx, s.db, lease)
}

func (s postgresBackupStore) AdvanceBackupVerifyCursorContext(ctx context.Context, maintenanceID, targetID string) error {
	return advanceBackupVerifyCursorContext(ctx, s.db, maintenanceID, targetID)
}

func (s postgresBackupStore) AddBackupVerifyProcessedBytesContext(ctx context.Context, maintenanceID string, bytes int64) error {
	return addBackupVerifyProcessedBytesContext(ctx, s.db, maintenanceID, bytes)
}

func (s postgresBackupStore) CompleteBackupVerifyContext(ctx context.Context, maintenanceID string) error {
	return completeBackupVerifyContext(ctx, s.db, maintenanceID)
}
