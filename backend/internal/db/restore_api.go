package db

import (
	"context"
	"database/sql"
)

// RestoreRetrySource is what a retried restore must still match: the config
// fingerprint and source backup/snapshot of an earlier restore job, and whether
// that job still has a run in flight.
type RestoreRetrySource struct {
	ConfigFingerprint []byte
	SourceBackupRef   string
	SourceSnapshotRef string
	// Active is true while any run of the job is queued, planning, running,
	// verifying or cancelling; such a job cannot be retried.
	Active bool
}

// getRestoreRetrySourceContext loads the retry source of an owned restore job.
// It returns sql.ErrNoRows for an absent or foreign job.
func getRestoreRetrySourceContext(ctx context.Context, database *sql.DB, restoreJobID, userID string) (*RestoreRetrySource, error) {
	var source RestoreRetrySource
	err := database.QueryRowContext(ctx, `
		SELECT j.config_fingerprint, j.source_backup_ref, j.source_snapshot_ref,
			EXISTS (SELECT 1 FROM restore_runs r WHERE r.restore_job_id = j.id AND r.status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'))
		FROM restore_jobs j WHERE j.id = $1 AND j.user_id = $2`, restoreJobID, userID).Scan(&source.ConfigFingerprint, &source.SourceBackupRef, &source.SourceSnapshotRef, &source.Active)
	if err != nil {
		return nil, err
	}
	return &source, nil
}
