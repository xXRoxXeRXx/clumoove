package db

import (
	"context"
	"database/sql"
	"fmt"
)

// RecoveredSyncPass identifies a stale sync pass reset to IDLE during startup
// or periodic recovery.
type RecoveredSyncPass struct {
	ID     string
	UserID string
}

// SyncTaskOutcome is the minimal task projection needed to compute final sync
// pass statistics and protected paths.
type SyncTaskOutcome struct {
	FilePath string
	Status   string
	Metadata []byte
}

func recoverStaleSyncPasses(ctx context.Context, database *sql.DB) ([]RecoveredSyncPass, error) {
	const recoveryMsg = "Recovered from stale INDEXING/RUNNING/VERIFYING (coordinator lost)"
	rows, err := database.QueryContext(ctx, `
		UPDATE sync_jobs sj
		SET status = 'IDLE', verification_lease_until = NULL, error_message = $1, updated_at = CURRENT_TIMESTAMP
		WHERE (
		        sj.status = 'INDEXING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		  )
		   OR (
		        sj.status = 'RUNNING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		    AND NOT EXISTS (
		        SELECT 1 FROM tasks t
		        WHERE t.sync_job_id = sj.id
		          AND (t.status IN ('PENDING', 'RUNNING') OR (t.status = 'FAILED' AND t.next_retry_at IS NOT NULL))
		          AND t.updated_at > NOW() - INTERVAL '10 minutes'
		    )
		  )
		   OR (
		        sj.status = 'VERIFYING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		    AND (sj.verification_lease_until IS NULL OR sj.verification_lease_until <= NOW())
		  )
		RETURNING sj.id, sj.user_id
	`, recoveryMsg)
	if err != nil {
		return nil, fmt.Errorf("recover orphaned sync passes: %w", err)
	}
	defer rows.Close()

	var recovered []RecoveredSyncPass
	for rows.Next() {
		var pass RecoveredSyncPass
		var userID sql.NullString
		if err := rows.Scan(&pass.ID, &userID); err != nil {
			return nil, fmt.Errorf("scan recovered sync pass: %w", err)
		}
		pass.UserID = userID.String
		recovered = append(recovered, pass)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovered sync passes: %w", err)
	}
	return recovered, nil
}

func advanceRecoveredSyncSchedule(ctx context.Context, database *sql.DB, syncJobID string) error {
	_, err := database.ExecContext(ctx, `UPDATE schedules SET next_run_at = NOW() WHERE task_type = 'sync' AND task_id = $1 AND is_active = TRUE`, syncJobID)
	return err
}

func getSyncJobStatusAndGeneration(ctx context.Context, database *sql.DB, syncJobID string) (string, int, error) {
	var status string
	var generation int
	err := database.QueryRowContext(ctx, `SELECT status, run_generation FROM sync_jobs WHERE id = $1`, syncJobID).Scan(&status, &generation)
	return status, generation, err
}

func deleteNonRunningSyncTasks(ctx context.Context, database *sql.DB, syncJobID string) error {
	_, err := database.ExecContext(ctx, `
		DELETE FROM tasks
		WHERE sync_job_id = $1 AND status IN ('COMPLETED','FAILED','CANCELLED','SKIPPED','PENDING')
	`, syncJobID)
	return err
}

func countOpenSyncTasks(ctx context.Context, database *sql.DB, syncJobID string, generation int) (int, error) {
	var openCount int
	err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE sync_job_id = $1 AND pass_generation = $2
		  AND (status IN ('PENDING', 'RUNNING') OR (status = 'FAILED' AND next_retry_at IS NOT NULL))
	`, syncJobID, generation).Scan(&openCount)
	return openCount, err
}

func countUnverifiedSyncTasks(ctx context.Context, database *sql.DB, syncJobID string, generation int) (int, error) {
	var unverifiedCount int
	err := database.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'COMPLETED' AND checksum_verified = FALSE
	`, syncJobID, generation).Scan(&unverifiedCount)
	return unverifiedCount, err
}

func hasRunningSyncTasks(ctx context.Context, database *sql.DB, syncJobID string) (bool, error) {
	var running bool
	err := database.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM tasks WHERE sync_job_id = $1 AND status = 'RUNNING')
	`, syncJobID).Scan(&running)
	return running, err
}

func hasRunningSyncPassTasks(ctx context.Context, database *sql.DB, syncJobID string, generation int) (bool, error) {
	var running bool
	err := database.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'RUNNING')
	`, syncJobID, generation).Scan(&running)
	return running, err
}

func listSyncPassTaskOutcomes(ctx context.Context, database *sql.DB, syncJobID string, generation int) ([]SyncTaskOutcome, error) {
	rows, err := database.QueryContext(ctx, `SELECT file_path, status, metadata FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2`, syncJobID, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var outcomes []SyncTaskOutcome
	for rows.Next() {
		var outcome SyncTaskOutcome
		if err := rows.Scan(&outcome.FilePath, &outcome.Status, &outcome.Metadata); err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return outcomes, nil
}
