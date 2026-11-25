package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ConnectionRecoveryCandidate is a migration or sync job paused by connection
// loss, with the encrypted endpoint credentials the worker needs to probe both
// sides before resuming it. UserID is empty for rows without an owner.
type ConnectionRecoveryCandidate struct {
	ID             string
	UserID         string
	SourceURL      string
	SourceUsername string
	SourcePassword string
	TargetURL      string
	TargetUsername string
	TargetPassword string
	SourceProvider string
	TargetProvider string
	UpdatedAt      time.Time
}

// SyncPassRef names a sync job together with the run generation of its current
// pass, which fences every write made on that pass's behalf.
type SyncPassRef struct {
	ID         string
	Generation int
}

// requeueDueFailedTasks puts FAILED tasks whose retry time has passed back to
// PENDING, for migrations and for the current pass of sync jobs that are still
// active. Both statements share one transaction so a retry round is all or
// nothing. It returns the number of re-enqueued tasks.
func requeueDueFailedTasks(ctx context.Context, database *sql.DB, now time.Time) (int64, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var requeued int64
	for _, updateQuery := range []string{
		`UPDATE tasks AS t SET status = 'PENDING', next_retry_at = NULL, checksum_verified = FALSE FROM migrations m WHERE t.migration_id = m.id AND t.status = 'FAILED' AND t.next_retry_at <= $1 AND m.status IN ('RUNNING', 'INDEXING', 'VERIFYING')`,
		`UPDATE tasks AS t SET status = 'PENDING', next_retry_at = NULL, checksum_verified = FALSE FROM sync_jobs sj WHERE t.sync_job_id = sj.id AND t.pass_generation = sj.run_generation AND t.status = 'FAILED' AND t.next_retry_at <= $1 AND sj.status IN ('RUNNING', 'INDEXING', 'VERIFYING')`,
	} {
		result, err := tx.ExecContext(ctx, updateQuery, now)
		if err != nil {
			return 0, err
		}
		count, _ := result.RowsAffected()
		requeued += count
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return requeued, nil
}

// requeueOrphanedRunningTasks resets RUNNING tasks that have not been touched
// for ten minutes while their migration or sync job is still active. It returns
// the number of reset tasks.
func requeueOrphanedRunningTasks(ctx context.Context, database *sql.DB) (int64, error) {
	result, err := database.ExecContext(ctx, `
		UPDATE tasks t
		SET status = 'PENDING', worker_hash = NULL, updated_at = NOW()
		WHERE t.status = 'RUNNING'
		  AND t.updated_at < NOW() - INTERVAL '10 minutes'
		  AND (
			EXISTS (SELECT 1 FROM migrations m WHERE m.id = t.migration_id AND m.status IN ('RUNNING', 'INDEXING'))
			OR EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status IN ('RUNNING', 'INDEXING'))
		  )
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func listRunningMigrationIDs(ctx context.Context, database *sql.DB) ([]string, error) {
	return queryIDs(ctx, database, `
		SELECT DISTINCT m.id
		FROM migrations m
		WHERE m.status = 'RUNNING'
	`)
}

func listVerifyingMigrationIDs(ctx context.Context, database *sql.DB) ([]string, error) {
	return queryIDs(ctx, database, `SELECT id FROM migrations WHERE status = 'VERIFYING'`)
}

func listVerifyingSyncJobIDs(ctx context.Context, database *sql.DB) ([]string, error) {
	return queryIDs(ctx, database, `SELECT id FROM sync_jobs WHERE status = 'VERIFYING'`)
}

// queryIDs reads a single id column. Like the other scanners here it skips an
// unreadable row, keeps reading, and reports the failure through the returned
// error next to the rows it did get.
func queryIDs(ctx context.Context, database *sql.DB, query string) ([]string, error) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	var scanErr error
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		ids = append(ids, id)
	}
	return ids, errors.Join(scanErr, rows.Err())
}

func listRunningSyncPasses(ctx context.Context, database *sql.DB) ([]SyncPassRef, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT DISTINCT sj.id, sj.run_generation
		FROM sync_jobs sj
		WHERE sj.status = 'RUNNING'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var passes []SyncPassRef
	var scanErr error
	for rows.Next() {
		var pass SyncPassRef
		if err := rows.Scan(&pass.ID, &pass.Generation); err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		passes = append(passes, pass)
	}
	return passes, errors.Join(scanErr, rows.Err())
}

// listPausedConnectionLossMigrations returns up to limit migrations paused by
// connection loss. Ordering starts after cursor and wraps around, so repeated
// scans with the last examined id walk the whole set fairly.
func listPausedConnectionLossMigrations(ctx context.Context, database *sql.DB, cursor string, limit int) ([]ConnectionRecoveryCandidate, error) {
	return queryRecoveryCandidates(ctx, database, `
		SELECT id, COALESCE(user_id, ''), source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`, cursor, limit)
}

// listPausedConnectionLossSyncJobs is the sync-job twin of
// listPausedConnectionLossMigrations.
func listPausedConnectionLossSyncJobs(ctx context.Context, database *sql.DB, cursor string, limit int) ([]ConnectionRecoveryCandidate, error) {
	return queryRecoveryCandidates(ctx, database, `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM sync_jobs
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`, cursor, limit)
}

func queryRecoveryCandidates(ctx context.Context, database *sql.DB, query, cursor string, limit int) ([]ConnectionRecoveryCandidate, error) {
	rows, err := database.QueryContext(ctx, query, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []ConnectionRecoveryCandidate
	var scanErr error
	for rows.Next() {
		var c ConnectionRecoveryCandidate
		if err := rows.Scan(
			&c.ID, &c.UserID,
			&c.SourceURL, &c.SourceUsername, &c.SourcePassword,
			&c.TargetURL, &c.TargetUsername, &c.TargetPassword,
			&c.SourceProvider, &c.TargetProvider, &c.UpdatedAt,
		); err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		candidates = append(candidates, c)
	}
	return candidates, errors.Join(scanErr, rows.Err())
}
