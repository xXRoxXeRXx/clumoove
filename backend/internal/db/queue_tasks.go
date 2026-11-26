package db

import (
	"context"
	"database/sql"
	"fmt"
)

// DequeuedTask is the task claim returned by TaskStore.DequeueTask.
type DequeuedTask struct {
	MigrationID    string
	SyncJobID      string
	TaskID         string
	ClaimEpoch     int64
	PassGeneration int
}

// dequeueTask pops a PENDING task from the database queue while respecting
// migration and sync-job thread limits and conflict-copy prerequisites.
func dequeueTask(ctx context.Context, database *sql.DB, workerID string) (*DequeuedTask, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin dequeue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	commitEmpty := func() (*DequeuedTask, error) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty dequeue: %w", err)
		}
		return nil, nil
	}

	// Skip only direct dependents whose prerequisite reached a terminal failure.
	if _, err := tx.ExecContext(ctx, `
		-- A direct prerequisite fences a RENAME conflict-copy before its paired
		-- upload, preventing another worker from overwriting the unrenamed target.
		UPDATE tasks AS dependent
			SET status = 'SKIPPED',
			    worker_hash = NULL,
			    error_message = 'conflict_copy prerequisite failed; upload skipped',
			    updated_at = CURRENT_TIMESTAMP
			FROM tasks AS prerequisite
			WHERE dependent.status = 'PENDING'
			  AND dependent.prerequisite_task_id = prerequisite.id
			  AND (
				prerequisite.status IN ('CANCELLED', 'SKIPPED')
				OR (prerequisite.status = 'FAILED' AND prerequisite.next_retry_at IS NULL)
			  )`); err != nil {
		return nil, fmt.Errorf("skip failed conflict dependents: %w", err)
	}

	// Lock a candidate task first, then lock its parent below. A task lock alone
	// is insufficient: different workers can lock different tasks for the same
	// job. The parent lock serializes their capacity check and claim.
	candidateQuery := `
		SELECT t.id, t.migration_id, t.sync_job_id, t.pass_generation
		FROM tasks t
		LEFT JOIN migrations m ON t.migration_id = m.id
		LEFT JOIN sync_jobs sj ON t.sync_job_id = sj.id
		WHERE t.status = 'PENDING'
		AND (t.prerequisite_task_id IS NULL OR EXISTS (
			SELECT 1 FROM tasks AS prerequisite
			WHERE prerequisite.id = t.prerequisite_task_id AND prerequisite.status = 'COMPLETED'
		))
		-- This is an optimistic filter to avoid head-of-line blocking on jobs
		-- that are inactive or already saturated. The parent lock and count
		-- below remain authoritative for concurrent claims.
		AND (
			(t.migration_id IS NOT NULL AND m.status IN ('RUNNING', 'INDEXING') AND (
				SELECT COUNT(*) FROM tasks t2
				WHERE t2.migration_id = m.id AND t2.status = 'RUNNING'
			) < m.threads)
			OR
			(t.sync_job_id IS NOT NULL AND sj.status = 'RUNNING' AND t.pass_generation = sj.run_generation AND (
				SELECT COUNT(*) FROM tasks t2
				WHERE t2.sync_job_id = sj.id AND t2.pass_generation = sj.run_generation AND t2.status = 'RUNNING'
			) < sj.threads)
		)
		ORDER BY t.created_at ASC
		LIMIT 1
		FOR UPDATE OF t SKIP LOCKED
	`
	var taskID string
	var migID, syncID sql.NullString
	var taskGeneration int
	if err := tx.QueryRowContext(ctx, candidateQuery).Scan(&taskID, &migID, &syncID, &taskGeneration); err != nil {
		if err == sql.ErrNoRows {
			return commitEmpty()
		}
		return nil, fmt.Errorf("select dequeue candidate: %w", err)
	}

	var capacity int
	if migID.Valid {
		var status string
		var threads int
		if err := tx.QueryRowContext(ctx, `SELECT status, threads FROM migrations WHERE id = $1 FOR UPDATE`, migID.String).Scan(&status, &threads); err != nil {
			return nil, fmt.Errorf("lock migration dequeue parent: %w", err)
		}
		if status != "RUNNING" && status != "INDEXING" {
			return commitEmpty()
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE migration_id = $1 AND status = 'RUNNING'`, migID.String).Scan(&capacity); err != nil {
			return nil, fmt.Errorf("count running migration tasks: %w", err)
		}
		if capacity >= threads {
			return commitEmpty()
		}
	} else if syncID.Valid {
		var status string
		var generation int
		var threads int
		if err := tx.QueryRowContext(ctx, `SELECT status, threads, run_generation FROM sync_jobs WHERE id = $1 FOR UPDATE`, syncID.String).Scan(&status, &threads, &generation); err != nil {
			return nil, fmt.Errorf("lock sync dequeue parent: %w", err)
		}
		if status != "RUNNING" {
			return commitEmpty()
		}
		if taskGeneration != generation {
			return commitEmpty()
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE sync_job_id = $1 AND pass_generation = $2 AND status = 'RUNNING'`, syncID.String, generation).Scan(&capacity); err != nil {
			return nil, fmt.Errorf("count running sync tasks: %w", err)
		}
		if capacity >= threads {
			return commitEmpty()
		}
	} else {
		return nil, fmt.Errorf("dequeue candidate %s has no parent", taskID)
	}

	query := `
		WITH candidate AS (
			SELECT t.id
			FROM tasks t
			WHERE t.id = $2 AND t.status = 'PENDING'
		)
		UPDATE tasks
		SET status = 'RUNNING', updated_at = CURRENT_TIMESTAMP, worker_hash = $1,
		    claim_epoch = claim_epoch + 1
		WHERE id = (SELECT id FROM candidate)
		RETURNING id, migration_id, sync_job_id, claim_epoch, pass_generation
	`
	var claim DequeuedTask
	err = tx.QueryRowContext(ctx, query, workerID, taskID).Scan(&claim.TaskID, &migID, &syncID, &claim.ClaimEpoch, &claim.PassGeneration)
	if err != nil {
		if err == sql.ErrNoRows {
			return commitEmpty()
		}
		return nil, fmt.Errorf("failed to dequeue task from sql: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit dequeue task: %w", err)
	}
	if migID.Valid {
		claim.MigrationID = migID.String
	}
	if syncID.Valid {
		claim.SyncJobID = syncID.String
	}
	return &claim, nil
}

// recoverAbandonedTasks resets RUNNING tasks for a dead worker back to PENDING
// and returns the number of recovered rows.
func recoverAbandonedTasks(ctx context.Context, database *sql.DB, workerID string) (int64, error) {
	query := `
		UPDATE tasks
		SET status = 'PENDING', worker_hash = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE status = 'RUNNING' AND worker_hash = $1
		  AND updated_at < CURRENT_TIMESTAMP - INTERVAL '2 minutes'
	`
	res, err := database.ExecContext(ctx, query, workerID)
	if err != nil {
		return 0, err
	}
	rowsAffected, _ := res.RowsAffected()
	return rowsAffected, nil
}

// listRunningWorkerHashes returns the distinct non-null worker_hash values for
// tasks currently in RUNNING state.
func listRunningWorkerHashes(ctx context.Context, database *sql.DB) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT DISTINCT worker_hash FROM tasks WHERE status = 'RUNNING' AND worker_hash IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []string
	for rows.Next() {
		var workerID string
		if err := rows.Scan(&workerID); err != nil {
			continue
		}
		workers = append(workers, workerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return workers, nil
}

// notifyTaskAvailable sends a PostgreSQL NOTIFY on the 'task_available' channel.
func notifyTaskAvailable(ctx context.Context, database *sql.DB) error {
	_, err := database.ExecContext(ctx, "SELECT pg_notify('task_available', '')")
	return err
}
