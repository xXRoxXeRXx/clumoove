package db

import (
	"context"
	"database/sql"
	"time"
)

// FileManagerTransfer is one ad-hoc file-manager transfer, stored as a
// migration whose picker_session_id starts with a caller-chosen marker.
type FileManagerTransfer struct {
	ID                string
	Marker            string // full picker_session_id, marker included
	Status            string
	SourceProfileName string
	TargetProfileName string
	TotalFiles        int
	ProcessedFiles    int
	FailedFiles       int
	SkippedFiles      int
}

// MigrationStore is the persistence seam for migrations and their task
// queue as seen by the API: creation, owner-scoped reads, guarded lifecycle
// transitions, recovery actions, reports and the periodic scans. It delegates
// to package-level functions without adding queries.
//
// The worker, indexer, processor and scheduler still reach the claim-fenced
// and progress functions directly; those join this interface when they move
// off *sql.DB.
//
// Ownership is enforced only where a method says so (the Owned and ForUser
// reads). Every other call is keyed by migration id alone, so callers resolve
// ownership first. Status guards live inside the UPDATE statements, never in
// a preceding read.
type MigrationStore interface {
	// Creation.
	CreateMigration(m *Migration) (string, error)
	CreateMigrationAndSchedule(m *Migration, s *Schedule) (string, error)

	// Reads. GetOwnedMigrationContext returns sql.ErrNoRows for a missing or
	// foreign migration; MigrationExistsContext tells the two apart.
	GetMigrationContext(ctx context.Context, id string) (*Migration, error)
	GetOwnedMigrationContext(ctx context.Context, id, userID string) (*Migration, error)
	MigrationExistsContext(ctx context.Context, id string) (bool, error)
	GetMigrationsForUserContext(ctx context.Context, userID string) ([]Migration, error)
	ListAllMigrationsContext(ctx context.Context, p MigrationListParams) ([]AdminMigrationView, int, error)
	CountActiveMigrationsForUser(userID string) (int, error)
	GetMigrationResourceStats(id string) (*MigrationResourceStats, error)
	ListFileManagerTransfers(ctx context.Context, userID, profileID, markerPrefix string) ([]FileManagerTransfer, error)

	// Lifecycle. TransitionMigration and ResumeMigration report false when the
	// migration was not in a state that allows the change.
	TransitionMigration(ctx context.Context, id string, event MigrationEvent, errMsg *string) (bool, error)
	ResumeMigration(id string) (resumed, needsDiscovery bool, err error)
	UpdateMigrationBandwidthLimit(id string, limitMbps int) error
	UpdateMigrationThreads(id string, threads int) error
	DeleteMigrationCascade(id string) error
	DeleteOldMigrations() (int64, error)

	// Task queue actions and reports for one migration.
	CancelPendingTasks(id string) error
	ResetFailedTasksForRetry(ctx context.Context, id string) (int, error)
	ResetMigrationForReindex(ctx context.Context, id string) error
	GetFailedTasksForReport(id string) ([]Task, error)
	GetIndexingErrorsForReport(id string) ([]IndexingError, error)
	GetMigrationErrorsContext(ctx context.Context, id string, limit, offset int) ([]ErrorListItem, int, error)
	GetActiveTaskPaths(ctx context.Context, id string) ([]string, error)
	GetVerifyingTaskPaths(ctx context.Context, id string) ([]string, error)

	// Credential rotation.
	GetExpiringOAuthMigrations() ([]ExpiringOAuthMigration, error)
	TokenSink(id, role string) OAuthReauthSink

	// Worker side: the indexer, scheduler, processor and verification
	// workers. Claim-fenced calls take the discovery generation captured when
	// the indexing pass started and report ErrMigrationIndexingClaimLost (or
	// false) when a pause/resume successor owns the migration.
	GetMigration(id string) (*Migration, error)
	GetMigrationOwnerID(id string) (string, error)

	// Scheduler claim and stale-indexing recovery.
	ClaimScheduledMigrationForIndexingContext(ctx context.Context, id string) (bool, error)
	FailStaleIndexingMigration(ctx context.Context, id string, errMsg *string) (bool, error)
	// RecoverStaleScheduledIndexingMigrations moves expired INDEXING migrations
	// that have a schedule back to SCHEDULED and returns them (on a read
	// failure, those read so far plus the error). The caller then calls
	// ReactivateMigrationSchedules for each. ListStaleUnscheduledIndexingMigrations
	// lists the expired ones without a schedule, to be failed one by one with
	// FailStaleIndexingMigration, which re-checks the lease.
	RecoverStaleScheduledIndexingMigrations(ctx context.Context, errMsg string) ([]StaleIndexingMigration, error)
	ReactivateMigrationSchedules(ctx context.Context, migrationID string) error
	ListStaleUnscheduledIndexingMigrations(ctx context.Context) ([]StaleIndexingMigration, error)

	// Indexing pass.
	RenewMigrationIndexingLease(ctx context.Context, id string, generation int64, duration time.Duration) (bool, error)
	MigrationIndexedTaskKeys(id string) (map[string]bool, error)
	MigrationTaskTotals(id string) (int, int64, error)
	CreateMigrationTaskWhileIndexingClaim(t *Task, generation int64) (string, error)
	BulkCreateMigrationTasksWhileIndexing(ctx context.Context, id string, tasks []*Task) (bool, error)
	BulkCreateMigrationTasksWhileIndexingClaim(ctx context.Context, id string, generation int64, tasks []*Task) (bool, error)
	UpdateMigrationTotalsWhileIndexingClaim(id string, generation int64, totalFiles int, totalBytes int64) error
	TransitionMigrationIndexingToRunningClaim(id string, generation int64) error
	TransitionMigrationIndexingToCompletedClaim(id string, generation int64) error
	FailMigrationWhileIndexingClaim(id string, generation int64, errMsg *string) (bool, error)

	// Task processing and verification.
	PauseMigrationForConnectionLoss(id string) (bool, error)
	RecoverConnectionLostMigration(id string) (recovered, needsDiscovery bool, err error)
	MaybeRetryFailedMigrationTasks(ctx context.Context, id string) (bool, error)
	AddLiveBytes(ctx context.Context, id string, bytesDelta int64) error
	ResetLiveBytes(ctx context.Context, id string) error
	ReconcileMigrationProgress(id string) error
	ReconcileMigrationProgressWhileVerifying(id string, generation int) (bool, error)

	// Task outcome: one atomic write of the task row and the migration counters.
	UpdateMigrationTaskAndProgress(ctx context.Context, t *Task, filesDelta int, bytesDelta int64, skippedDelta, failedDelta int, liveBytesDelta int64) error
	FailMigrationForAuthentication(ctx context.Context, t *Task, errMsg string) (bool, error)
	RecordIndexingErrors(ctx context.Context, id string, errs []IndexingErrorInput) error

	// Verification pass. A verifier owns a pass through the generation it
	// claimed; every write is fenced by it and reports false once superseded.
	ClaimMigrationVerification(ctx context.Context, id string) (generation int, claimed bool, err error)
	RenewMigrationVerificationLease(ctx context.Context, id string, generation int) (bool, error)
	ReleaseMigrationVerificationLease(ctx context.Context, id string, generation int) error
	MarkMigrationTaskChecksumVerifiedWhileVerifying(ctx context.Context, taskID, targetHash string, generation int) (bool, error)
	MarkMigrationTaskChecksumMismatchWhileVerifying(ctx context.Context, t *Task, generation int) (bool, error)
}

type postgresMigrationStore struct{ db *sql.DB }

// NewMigrationStore returns the PostgreSQL-backed MigrationStore. It panics on
// a nil handle so a wiring mistake fails at startup instead of on the first
// query.
func NewMigrationStore(database *sql.DB) MigrationStore {
	if database == nil {
		panic("db: NewMigrationStore requires a database handle")
	}
	return postgresMigrationStore{db: database}
}

func (s postgresMigrationStore) CreateMigration(m *Migration) (string, error) {
	return createMigration(s.db, m)
}

func (s postgresMigrationStore) CreateMigrationAndSchedule(m *Migration, sched *Schedule) (string, error) {
	return createMigrationAndSchedule(s.db, m, sched)
}

func (s postgresMigrationStore) GetMigrationContext(ctx context.Context, id string) (*Migration, error) {
	return getMigrationContext(ctx, s.db, id)
}

func (s postgresMigrationStore) GetOwnedMigrationContext(ctx context.Context, id, userID string) (*Migration, error) {
	return getOwnedMigrationContext(ctx, s.db, id, userID)
}

func (s postgresMigrationStore) MigrationExistsContext(ctx context.Context, id string) (bool, error) {
	return migrationExistsContext(ctx, s.db, id)
}

func (s postgresMigrationStore) GetMigrationsForUserContext(ctx context.Context, userID string) ([]Migration, error) {
	return getMigrationsForUserContext(ctx, s.db, userID)
}

func (s postgresMigrationStore) ListAllMigrationsContext(ctx context.Context, p MigrationListParams) ([]AdminMigrationView, int, error) {
	return listAllMigrationsContext(ctx, s.db, p)
}

func (s postgresMigrationStore) CountActiveMigrationsForUser(userID string) (int, error) {
	return countActiveMigrationsForUser(s.db, userID)
}

func (s postgresMigrationStore) GetMigrationResourceStats(id string) (*MigrationResourceStats, error) {
	return getMigrationResourceStats(s.db, id)
}

func (s postgresMigrationStore) ListFileManagerTransfers(ctx context.Context, userID, profileID, markerPrefix string) ([]FileManagerTransfer, error) {
	return listFileManagerTransfers(ctx, s.db, userID, profileID, markerPrefix)
}

func (s postgresMigrationStore) TransitionMigration(ctx context.Context, id string, event MigrationEvent, errMsg *string) (bool, error) {
	return transitionMigration(ctx, s.db, id, event, errMsg)
}

func (s postgresMigrationStore) ResumeMigration(id string) (bool, bool, error) {
	return resumeMigration(s.db, id)
}

func (s postgresMigrationStore) UpdateMigrationBandwidthLimit(id string, limitMbps int) error {
	return updateMigrationBandwidthLimit(s.db, id, limitMbps)
}

func (s postgresMigrationStore) UpdateMigrationThreads(id string, threads int) error {
	return updateMigrationThreads(s.db, id, threads)
}

func (s postgresMigrationStore) DeleteMigrationCascade(id string) error {
	return deleteMigrationCascade(s.db, id)
}

func (s postgresMigrationStore) DeleteOldMigrations() (int64, error) {
	return deleteOldMigrations(s.db)
}

func (s postgresMigrationStore) CancelPendingTasks(id string) error {
	return cancelPendingTasks(s.db, id)
}

func (s postgresMigrationStore) ResetFailedTasksForRetry(ctx context.Context, id string) (int, error) {
	return resetFailedTasksForRetry(s.db, ctx, id)
}

func (s postgresMigrationStore) ResetMigrationForReindex(ctx context.Context, id string) error {
	return resetMigrationForReindex(s.db, ctx, id)
}

func (s postgresMigrationStore) GetFailedTasksForReport(id string) ([]Task, error) {
	return getFailedTasksForReport(s.db, id)
}

func (s postgresMigrationStore) GetIndexingErrorsForReport(id string) ([]IndexingError, error) {
	return getIndexingErrorsForReport(s.db, id)
}

func (s postgresMigrationStore) GetMigrationErrorsContext(ctx context.Context, id string, limit, offset int) ([]ErrorListItem, int, error) {
	return getMigrationErrorsContext(ctx, s.db, id, limit, offset)
}

func (s postgresMigrationStore) GetActiveTaskPaths(ctx context.Context, id string) ([]string, error) {
	return getActiveTaskPaths(s.db, ctx, id)
}

func (s postgresMigrationStore) GetVerifyingTaskPaths(ctx context.Context, id string) ([]string, error) {
	return getVerifyingTaskPaths(s.db, ctx, id)
}

func (s postgresMigrationStore) GetExpiringOAuthMigrations() ([]ExpiringOAuthMigration, error) {
	return getExpiringOAuthMigrations(s.db)
}

func (s postgresMigrationStore) GetMigration(id string) (*Migration, error) {
	return getMigration(s.db, id)
}

func (s postgresMigrationStore) GetMigrationOwnerID(id string) (string, error) {
	return getMigrationOwnerID(s.db, id)
}

func (s postgresMigrationStore) ClaimScheduledMigrationForIndexingContext(ctx context.Context, id string) (bool, error) {
	return claimScheduledMigrationForIndexingContext(ctx, s.db, id)
}

func (s postgresMigrationStore) FailStaleIndexingMigration(ctx context.Context, id string, errMsg *string) (bool, error) {
	return failStaleIndexingMigration(ctx, s.db, id, errMsg)
}

func (s postgresMigrationStore) RecoverStaleScheduledIndexingMigrations(ctx context.Context, errMsg string) ([]StaleIndexingMigration, error) {
	return recoverStaleScheduledIndexingMigrations(ctx, s.db, errMsg)
}

func (s postgresMigrationStore) ReactivateMigrationSchedules(ctx context.Context, migrationID string) error {
	return reactivateMigrationSchedules(ctx, s.db, migrationID)
}

func (s postgresMigrationStore) ListStaleUnscheduledIndexingMigrations(ctx context.Context) ([]StaleIndexingMigration, error) {
	return listStaleUnscheduledIndexingMigrations(ctx, s.db)
}

func (s postgresMigrationStore) RenewMigrationIndexingLease(ctx context.Context, id string, generation int64, duration time.Duration) (bool, error) {
	return renewMigrationIndexingLease(ctx, s.db, id, generation, duration)
}

func (s postgresMigrationStore) MigrationIndexedTaskKeys(id string) (map[string]bool, error) {
	return migrationIndexedTaskKeys(s.db, id)
}

func (s postgresMigrationStore) MigrationTaskTotals(id string) (int, int64, error) {
	return migrationTaskTotals(s.db, id)
}

func (s postgresMigrationStore) CreateMigrationTaskWhileIndexingClaim(t *Task, generation int64) (string, error) {
	return createMigrationTaskWhileIndexingClaim(s.db, t, generation)
}

func (s postgresMigrationStore) BulkCreateMigrationTasksWhileIndexing(ctx context.Context, id string, tasks []*Task) (bool, error) {
	return bulkCreateMigrationTasksUnfenced(ctx, s.db, id, tasks)
}

func (s postgresMigrationStore) BulkCreateMigrationTasksWhileIndexingClaim(ctx context.Context, id string, generation int64, tasks []*Task) (bool, error) {
	return bulkCreateMigrationTasksWhileIndexingClaim(ctx, s.db, id, generation, tasks)
}

func (s postgresMigrationStore) UpdateMigrationTotalsWhileIndexingClaim(id string, generation int64, totalFiles int, totalBytes int64) error {
	return updateMigrationTotalsWhileIndexingClaim(s.db, id, generation, totalFiles, totalBytes)
}

func (s postgresMigrationStore) TransitionMigrationIndexingToRunningClaim(id string, generation int64) error {
	return transitionMigrationIndexingToRunningClaim(s.db, id, generation)
}

func (s postgresMigrationStore) TransitionMigrationIndexingToCompletedClaim(id string, generation int64) error {
	return transitionMigrationIndexingToCompletedClaim(s.db, id, generation)
}

func (s postgresMigrationStore) FailMigrationWhileIndexingClaim(id string, generation int64, errMsg *string) (bool, error) {
	return failMigrationWhileIndexingClaim(s.db, id, generation, errMsg)
}

func (s postgresMigrationStore) PauseMigrationForConnectionLoss(id string) (bool, error) {
	return pauseMigrationForConnectionLoss(s.db, id)
}

func (s postgresMigrationStore) RecoverConnectionLostMigration(id string) (bool, bool, error) {
	return recoverConnectionLostMigration(s.db, id)
}

func (s postgresMigrationStore) MaybeRetryFailedMigrationTasks(ctx context.Context, id string) (bool, error) {
	return maybeRetryFailedMigrationTasks(s.db, ctx, id)
}

func (s postgresMigrationStore) AddLiveBytes(ctx context.Context, id string, bytesDelta int64) error {
	return addLiveBytes(s.db, ctx, id, bytesDelta)
}

func (s postgresMigrationStore) ResetLiveBytes(ctx context.Context, id string) error {
	return resetLiveBytes(s.db, ctx, id)
}

func (s postgresMigrationStore) ReconcileMigrationProgress(id string) error {
	return reconcileMigrationProgressUnfenced(s.db, id)
}

func (s postgresMigrationStore) ReconcileMigrationProgressWhileVerifying(id string, generation int) (bool, error) {
	return reconcileMigrationProgressWhileVerifying(s.db, id, generation)
}

func (s postgresMigrationStore) UpdateMigrationTaskAndProgress(ctx context.Context, t *Task, filesDelta int, bytesDelta int64, skippedDelta, failedDelta int, liveBytesDelta int64) error {
	return updateMigrationTaskAndProgress(s.db, ctx, t, filesDelta, bytesDelta, skippedDelta, failedDelta, liveBytesDelta)
}

func (s postgresMigrationStore) FailMigrationForAuthentication(ctx context.Context, t *Task, errMsg string) (bool, error) {
	return failMigrationForAuthentication(s.db, ctx, t, errMsg)
}

func (s postgresMigrationStore) RecordIndexingErrors(ctx context.Context, id string, errs []IndexingErrorInput) error {
	return recordIndexingErrors(ctx, s.db, id, errs)
}

func (s postgresMigrationStore) ClaimMigrationVerification(ctx context.Context, id string) (int, bool, error) {
	return claimMigrationVerification(s.db, ctx, id)
}

func (s postgresMigrationStore) RenewMigrationVerificationLease(ctx context.Context, id string, generation int) (bool, error) {
	return renewMigrationVerificationLease(s.db, ctx, id, generation)
}

func (s postgresMigrationStore) ReleaseMigrationVerificationLease(ctx context.Context, id string, generation int) error {
	return releaseMigrationVerificationLease(s.db, ctx, id, generation)
}

func (s postgresMigrationStore) MarkMigrationTaskChecksumVerifiedWhileVerifying(ctx context.Context, taskID, targetHash string, generation int) (bool, error) {
	return markMigrationTaskChecksumVerifiedWhileVerifying(s.db, ctx, taskID, targetHash, generation)
}

func (s postgresMigrationStore) MarkMigrationTaskChecksumMismatchWhileVerifying(ctx context.Context, t *Task, generation int) (bool, error) {
	return markMigrationTaskChecksumMismatchWhileVerifying(s.db, ctx, t, generation)
}

func (s postgresMigrationStore) TokenSink(id, role string) OAuthReauthSink {
	return newMigrationTokenSink(s.db, id, role)
}

// listFileManagerTransfers returns the user's recent file-manager transfers
// touching profileID: unfinished ones and ones that finished in the last
// minute. markerPrefix selects them by picker_session_id and is matched with LIKE,
// so it must be a literal prefix without % or _ wildcards.
func listFileManagerTransfers(ctx context.Context, database *sql.DB, userID, profileID, markerPrefix string) ([]FileManagerTransfer, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT m.id::text, m.picker_session_id, m.status, COALESCE(source.name, ''), COALESCE(target.name, ''),
		       m.total_files, m.processed_files, m.failed_files, m.skipped_files
		FROM migrations m
		LEFT JOIN connection_profiles source ON source.id = m.source_profile_id
		LEFT JOIN connection_profiles target ON target.id = m.target_profile_id
		WHERE m.user_id = $1 AND (m.source_profile_id = $2 OR m.target_profile_id = $2)
		  AND m.picker_session_id LIKE $3 || '%'
		  AND (
		       m.status IN ('PENDING', 'INDEXING', 'RUNNING', 'VERIFYING', 'PAUSED', 'PAUSED_CONNECTION_LOSS')
		       OR (m.status IN ('COMPLETED', 'COMPLETED_WITH_ERRORS', 'FAILED') AND m.updated_at > NOW() - INTERVAL '1 minute')
		  )
		ORDER BY m.created_at DESC LIMIT 20`, userID, profileID, markerPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	transfers := make([]FileManagerTransfer, 0)
	for rows.Next() {
		var t FileManagerTransfer
		if err := rows.Scan(&t.ID, &t.Marker, &t.Status, &t.SourceProfileName, &t.TargetProfileName, &t.TotalFiles, &t.ProcessedFiles, &t.FailedFiles, &t.SkippedFiles); err != nil {
			return nil, err
		}
		transfers = append(transfers, t)
	}
	return transfers, rows.Err()
}
