package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// BackupConnectionSnapshot is one endpoint of a new backup job as persisted with
// the job: the resolved connection plus its already-encrypted credentials. No
// plaintext secret ever reaches this package.
type BackupConnectionSnapshot struct {
	// ProfileID is the saved connection profile the endpoint was resolved from.
	ProfileID              string
	Provider               string
	URL                    string
	Username               string
	PasswordEncrypted      string
	RefreshTokenEncrypted  sql.NullString
	TokenExpiresAt         sql.NullTime
	MegaSessionIDEncrypted sql.NullString
	MegaMasterKeyEncrypted sql.NullString
}

// CreateBackupJobParams describes a new backup job and its first schedule row.
type CreateBackupJobParams struct {
	UserID         string
	Source         BackupConnectionSnapshot
	Target         BackupConnectionSnapshot
	SelectedPaths  StringArray
	TargetDir      string
	CronExpression string
	Timezone       string
	RetentionCount int
	Threads        int
	// NextRun is the first fire time of the job's schedule.
	NextRun time.Time
}

// createBackupJobContext inserts the job and its active schedule in one
// transaction and returns the job id. The repository UUID is generated once by
// PostgreSQL and used in the same statement to make the immutable root
// unguessable and collision-free.
func createBackupJobContext(ctx context.Context, database *sql.DB, p CreateBackupJobParams) (string, error) {
	var id string
	tx, err := database.BeginTx(ctx, nil)
	if err == nil {
		err = tx.QueryRowContext(ctx, `WITH repository AS (SELECT gen_random_uuid() AS id)
			INSERT INTO backup_jobs (user_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
				source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
				target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted,
				target_mega_master_key_encrypted, source_provider, target_provider, selected_paths, target_dir, repository_id, repository_root,
				cron_expression, timezone, retention_count, threads)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, repository.id,
				$21 || '/.clumoove-backup/' || repository.id::text, $22, $23, $24, $25 FROM repository RETURNING id`,
			p.UserID, p.Source.ProfileID, p.Target.ProfileID, p.Source.URL, p.Source.Username, p.Source.PasswordEncrypted, p.Source.RefreshTokenEncrypted, p.Source.TokenExpiresAt, p.Source.MegaSessionIDEncrypted, p.Source.MegaMasterKeyEncrypted,
			p.Target.URL, p.Target.Username, p.Target.PasswordEncrypted, p.Target.RefreshTokenEncrypted, p.Target.TokenExpiresAt, p.Target.MegaSessionIDEncrypted, p.Target.MegaMasterKeyEncrypted, p.Source.Provider, p.Target.Provider, p.SelectedPaths, p.TargetDir,
			p.CronExpression, p.Timezone, p.RetentionCount, p.Threads).Scan(&id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO schedules (user_id, task_type, task_id, cron_expression, next_run_at, is_active) VALUES ($1, 'backup', $2, $3, $4, TRUE)`, p.UserID, id, p.CronExpression, p.NextRun)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// pauseBackupJobContext pauses an owned job in a pausable state, cancels its
// in-flight runs and deactivates its schedule in one transaction. It reports
// false, with nothing changed, when the job is absent, not owned or not in a
// state that can pause.
func pauseBackupJobContext(ctx context.Context, database *sql.DB, jobID, userID string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET status = 'PAUSED' WHERE id = $1 AND user_id = $2 AND status = ANY($3::text[])`, jobID, userID, pq.Array(BackupActionSources(JobActionPause)))
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated != 1 {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE backup_runs SET state = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE backup_job_id = $1 AND state IN ('QUEUED','SCANNING','RUNNING','VERIFYING')`, jobID); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET is_active = FALSE WHERE task_type = 'backup' AND task_id = $1`, jobID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// resumeBackupJobContext resumes an owned job in a resumable state and
// reactivates its schedule at the next fire time. The job row is locked while
// nextRun runs, so the schedule it computes from is the one that is persisted.
// It reports false, with nothing changed, when the job is absent, not owned or
// not in a state that can resume.
func resumeBackupJobContext(ctx context.Context, database *sql.DB, jobID, userID string, nextRun func(cronExpression, timezone string) (time.Time, error)) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var cronExpression, timezone string
	err = tx.QueryRowContext(ctx, `SELECT cron_expression, timezone FROM backup_jobs WHERE id = $1 AND user_id = $2 AND status = ANY($3::text[]) FOR UPDATE`, jobID, userID, pq.Array(BackupActionSources(JobActionResume))).Scan(&cronExpression, &timezone)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET status = 'IDLE' WHERE id = $1 AND user_id = $2 AND status = ANY($3::text[])`, jobID, userID, pq.Array(BackupActionSources(JobActionResume)))
	if err != nil {
		return false, err
	}
	next, err := nextRun(cronExpression, timezone)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE schedules SET is_active = TRUE, next_run_at = $1 WHERE task_type = 'backup' AND task_id = $2`, next, jobID); err != nil {
		return false, err
	}
	if updated, err := result.RowsAffected(); err != nil || updated != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// backupSnapshotRestorableContext reports whether snapshotID belongs to jobID
// and can be restored from: it is published and not known to be damaged.
func backupSnapshotRestorableContext(ctx context.Context, database *sql.DB, jobID, snapshotID string) (bool, error) {
	var exists bool
	err := database.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM backup_snapshots WHERE id = $1 AND backup_job_id = $2 AND state IN ('READY','PARTIAL') AND integrity_state <> 'DAMAGED')`, snapshotID, jobID).Scan(&exists)
	return exists, err
}
