package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BackupJob struct {
	ID                           string         `json:"id"`
	UserID                       string         `json:"user_id"`
	LockID                       int64          `json:"-"`
	SourceProfileID              sql.NullString `json:"source_profile_id,omitempty"`
	TargetProfileID              sql.NullString `json:"target_profile_id,omitempty"`
	SourceURL                    string         `json:"source_url"`
	SourceUsername               string         `json:"source_username"`
	SourcePasswordEncrypted      string         `json:"-"`
	SourceRefreshTokenEncrypted  sql.NullString `json:"-"`
	SourceTokenExpiresAt         sql.NullTime   `json:"source_token_expires_at,omitempty"`
	SourceMegaSessionIDEncrypted sql.NullString `json:"-"`
	SourceMegaMasterKeyEncrypted sql.NullString `json:"-"`
	TargetURL                    string         `json:"target_url"`
	TargetUsername               string         `json:"target_username"`
	TargetPasswordEncrypted      string         `json:"-"`
	TargetRefreshTokenEncrypted  sql.NullString `json:"-"`
	TargetTokenExpiresAt         sql.NullTime   `json:"target_token_expires_at,omitempty"`
	TargetMegaSessionIDEncrypted sql.NullString `json:"-"`
	TargetMegaMasterKeyEncrypted sql.NullString `json:"-"`
	SourceProvider               string         `json:"source_provider"`
	TargetProvider               string         `json:"target_provider"`
	SelectedPaths                StringArray    `json:"selected_paths"`
	TargetDir                    string         `json:"target_dir"`
	RepositoryID                 string         `json:"-"`
	RepositoryRoot               string         `json:"-"`
	CronExpression               string         `json:"cron_expression"`
	Timezone                     string         `json:"timezone"`
	RetentionCount               int            `json:"retention_count"`
	Threads                      int            `json:"threads"`
	Status                       string         `json:"status"`
	RunGeneration                int            `json:"-"`
	LastRunAt                    sql.NullTime   `json:"last_run_at,omitempty"`
	LastRunStatus                sql.NullString `json:"last_run_status,omitempty"`
	TotalFiles                   int            `json:"total_files"`
	TotalBytes                   int64          `json:"total_bytes"`
	ProcessedFiles               int            `json:"processed_files"`
	ProcessedBytes               int64          `json:"processed_bytes"`
	DeduplicatedBytes            int64          `json:"deduplicated_bytes"`
	FailedFiles                  int            `json:"failed_files"`
	ErrorCode                    sql.NullString `json:"error_code,omitempty"`
	DeletionState                string         `json:"-"`
	CreatedAt                    time.Time      `json:"created_at"`
	UpdatedAt                    time.Time      `json:"updated_at"`
}

type BackupRun struct {
	ID                string         `json:"id"`
	BackupJobID       string         `json:"backup_job_id"`
	Generation        int            `json:"generation"`
	Trigger           string         `json:"trigger"`
	ScheduledLocalKey sql.NullString `json:"scheduled_local_key,omitempty"`
	State             string         `json:"state"`
	TotalFiles        int            `json:"total_files"`
	TotalBytes        int64          `json:"total_bytes"`
	ProcessedFiles    int            `json:"processed_files"`
	ProcessedBytes    int64          `json:"processed_bytes"`
	DeduplicatedBytes int64          `json:"deduplicated_bytes"`
	FailedFiles       int            `json:"failed_files"`
	ErrorCode         sql.NullString `json:"error_code,omitempty"`
	StartedAt         sql.NullTime   `json:"started_at,omitempty"`
	FinishedAt        sql.NullTime   `json:"finished_at,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

// BackupSnapshot is the public, immutable snapshot catalog view. Repository
// paths, pack locators, and block hashes intentionally never leave the worker.
type BackupSnapshot struct {
	ID                   string    `json:"id"`
	State                string    `json:"state"`
	CreatedAt            time.Time `json:"created_at"`
	TotalFiles           int       `json:"total_files"`
	TotalDirs            int       `json:"total_dirs"`
	TotalBytes           int64     `json:"total_bytes"`
	OmittedUnstableCount int       `json:"omitted_unstable_count"`
	OmittedErrorCount    int       `json:"omitted_error_count"`
	IntegrityState       string    `json:"integrity_state"`
}

// BackupSnapshotItem describes one direct child in the read-only snapshot tree.
type BackupSnapshotItem struct {
	RelativePath string     `json:"relative_path"`
	Name         string     `json:"name"`
	IsDir        bool       `json:"is_dir"`
	SizeBytes    int64      `json:"size_bytes"`
	Mtime        *time.Time `json:"mtime,omitempty"`
	State        string     `json:"state"`
	ErrorCode    *string    `json:"error_code,omitempty"`
}

type BackupPack struct {
	ID            string
	RemotePath    string
	SHA256        []byte
	SizeBytes     int64
	State         string
	LastCheckedAt *time.Time
}

type BackupMaintenance struct {
	ID          string
	BackupJobID string
	UserID      string
	LockID      int64
	Kind        string
	State       string
	ByteBudget  sql.NullInt64
}

type BackupLiveBlock struct {
	ID   string
	Hash []byte
	Size int
}

type BackupClaimOutcome string

const (
	BackupClaimed        BackupClaimOutcome = "claimed"
	BackupClaimOverlap   BackupClaimOutcome = "overlap"
	BackupClaimDuplicate BackupClaimOutcome = "duplicate_occurrence"
	BackupClaimBlocked   BackupClaimOutcome = "administratively_blocked"
)

type BackupPassClaim struct {
	Outcome BackupClaimOutcome
	Run     *BackupRun
}

// BackupScheduleInfo is the minimal scheduler-facing view of a backup job. It
// deliberately excludes credentials and repository paths.
type BackupScheduleInfo struct {
	Status         string
	CronExpression string
	Timezone       string
}

// GetBackupScheduleInfoContext loads only scheduling state for a backup job.
func GetBackupScheduleInfoContext(ctx context.Context, database *sql.DB, jobID string) (BackupScheduleInfo, error) {
	var info BackupScheduleInfo
	err := database.QueryRowContext(ctx, `SELECT status, cron_expression, timezone FROM backup_jobs WHERE id = $1`, jobID).Scan(&info.Status, &info.CronExpression, &info.Timezone)
	return info, err
}

// ClaimNextQueuedBackupRunContext claims one queued run without waiting for
// another worker. Claiming also advances the run to SCANNING, so a released row
// lock cannot be picked up by a second worker.
func ClaimNextQueuedBackupRunContext(ctx context.Context, database *sql.DB) (*BackupJob, *BackupRun, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin queued backup claim: %w", err)
	}
	defer tx.Rollback()

	job := &BackupJob{}
	run := &BackupRun{}
	err = tx.QueryRowContext(ctx, `
		SELECT j.id, j.user_id, j.lock_id, j.source_profile_id, j.target_profile_id, j.source_url, j.source_username, j.source_password_encrypted,
			j.source_refresh_token_encrypted, j.source_token_expires_at, j.source_mega_session_id_encrypted, j.source_mega_master_key_encrypted,
			j.target_url, j.target_username, j.target_password_encrypted, j.target_refresh_token_encrypted, j.target_token_expires_at,
			j.target_mega_session_id_encrypted, j.target_mega_master_key_encrypted, j.source_provider, j.target_provider, j.selected_paths,
			j.target_dir, j.repository_id, j.repository_root, j.cron_expression, j.timezone, j.retention_count, j.threads, j.status, j.run_generation,
			j.last_run_at, j.last_run_status, j.total_files, j.total_bytes, j.processed_files, j.processed_bytes, j.deduplicated_bytes, j.failed_files,
			j.error_code, j.deletion_state, j.created_at, j.updated_at,
			r.id, r.backup_job_id, r.generation, r.trigger, r.scheduled_local_key, r.state, r.total_files, r.total_bytes, r.processed_files,
			r.processed_bytes, r.deduplicated_bytes, r.failed_files, r.error_code, r.started_at, r.finished_at, r.created_at, r.updated_at
		FROM backup_runs r
		JOIN backup_jobs j ON j.id = r.backup_job_id
		WHERE r.state = 'QUEUED' AND j.status = 'QUEUED' AND j.run_generation = r.generation AND j.deletion_state = 'ACTIVE'
		ORDER BY r.created_at
		FOR UPDATE OF r SKIP LOCKED
		LIMIT 1`).Scan(
		&job.ID, &job.UserID, &job.LockID, &job.SourceProfileID, &job.TargetProfileID, &job.SourceURL, &job.SourceUsername, &job.SourcePasswordEncrypted,
		&job.SourceRefreshTokenEncrypted, &job.SourceTokenExpiresAt, &job.SourceMegaSessionIDEncrypted, &job.SourceMegaMasterKeyEncrypted,
		&job.TargetURL, &job.TargetUsername, &job.TargetPasswordEncrypted, &job.TargetRefreshTokenEncrypted, &job.TargetTokenExpiresAt,
		&job.TargetMegaSessionIDEncrypted, &job.TargetMegaMasterKeyEncrypted, &job.SourceProvider, &job.TargetProvider, &job.SelectedPaths,
		&job.TargetDir, &job.RepositoryID, &job.RepositoryRoot, &job.CronExpression, &job.Timezone, &job.RetentionCount, &job.Threads, &job.Status, &job.RunGeneration,
		&job.LastRunAt, &job.LastRunStatus, &job.TotalFiles, &job.TotalBytes, &job.ProcessedFiles, &job.ProcessedBytes, &job.DeduplicatedBytes, &job.FailedFiles,
		&job.ErrorCode, &job.DeletionState, &job.CreatedAt, &job.UpdatedAt,
		&run.ID, &run.BackupJobID, &run.Generation, &run.Trigger, &run.ScheduledLocalKey, &run.State, &run.TotalFiles, &run.TotalBytes, &run.ProcessedFiles,
		&run.ProcessedBytes, &run.DeduplicatedBytes, &run.FailedFiles, &run.ErrorCode, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("select queued backup run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_runs SET state = 'SCANNING', started_at = CURRENT_TIMESTAMP WHERE id = $1 AND state = 'QUEUED'`, run.ID); err != nil {
		return nil, nil, fmt.Errorf("mark backup run scanning: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET status = 'SCANNING' WHERE id = $1 AND run_generation = $2 AND status = 'QUEUED'`, job.ID, run.Generation)
	if err != nil {
		return nil, nil, fmt.Errorf("mark backup job scanning: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, nil, fmt.Errorf("read backup job claim result: %w", err)
	}
	if updated != 1 {
		return nil, nil, fmt.Errorf("backup job changed while claiming queued run")
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit queued backup claim: %w", err)
	}
	job.Status, run.State = "SCANNING", "SCANNING"
	return job, run, nil
}

func CreateBackupSnapshotDraftContext(ctx context.Context, database *sql.DB, jobID, runID string, selectedRoots StringArray) (string, error) {
	var id string
	err := database.QueryRowContext(ctx, `INSERT INTO backup_snapshots (backup_job_id, backup_run_id, selected_roots) VALUES ($1, $2, $3) RETURNING id`, jobID, runID, selectedRoots).Scan(&id)
	return id, err
}

// BackupRunActiveContext is a durable cancellation fence: pausing a job changes
// either state, so a worker must not publish or write further catalog entries.
func BackupRunActiveContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID string) (bool, error) {
	var active bool
	err := database.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM backup_runs r JOIN backup_jobs j ON j.id = r.backup_job_id
		WHERE r.id = $1 AND r.backup_job_id = $2 AND r.generation = $3
		AND r.state IN ('SCANNING', 'RUNNING', 'VERIFYING')
		AND j.run_generation = r.generation AND j.status = r.state
	)`, runID, jobID, generation).Scan(&active)
	return active, err
}

func DiscardBackupSnapshotDraftContext(ctx context.Context, database *sql.DB, snapshotID, jobID, runID string) error {
	_, err := database.ExecContext(ctx, `DELETE FROM backup_snapshots WHERE id = $1 AND backup_job_id = $2 AND backup_run_id = $3 AND state = 'PUBLISHING'`, snapshotID, jobID, runID)
	return err
}

func FindBackupBlockContext(ctx context.Context, database *sql.DB, jobID string, hash []byte) (string, bool, error) {
	var id string
	err := database.QueryRowContext(ctx, `
		SELECT b.id FROM backup_blocks b
		JOIN backup_packs p ON p.id = b.backup_pack_id AND p.backup_job_id = b.backup_job_id
		WHERE b.backup_job_id = $1 AND b.sha256 = $2 AND p.state = 'READY'`, jobID, hash).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// RecordBackupPackAndBlockContext records content only after its immutable
// remote object was confirmed at the exact encoded size.
type BackupPackBlock struct {
	Hash          []byte
	PlaintextSize int
	PayloadOffset int64
	PayloadLength int
}

// RecordBackupPackAndBlocksContext records a fully validated immutable pack and
// every block it contains. Payload offsets are offsets in the remote pack, not
// assumptions about a one-entry pack.
func RecordBackupPackAndBlocksContext(ctx context.Context, database *sql.DB, jobID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock) (map[string]string, error) {
	if len(blocks) == 0 {
		return nil, errors.New("backup pack must contain blocks")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var packID string
	err = tx.QueryRowContext(ctx, `INSERT INTO backup_packs (backup_job_id, remote_rel_path, sha256, size_bytes, state) VALUES ($1, $2, $3, $4, 'READY') RETURNING id`, jobID, remotePath, packHash, packSize).Scan(&packID)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(blocks))
	for _, block := range blocks {
		var blockID string
		err = tx.QueryRowContext(ctx, `INSERT INTO backup_blocks (backup_job_id, sha256, plaintext_size, backup_pack_id, payload_offset, payload_length) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, jobID, block.Hash, block.PlaintextSize, packID, block.PayloadOffset, block.PayloadLength).Scan(&blockID)
		if err != nil {
			return nil, err
		}
		ids[string(block.Hash)] = blockID
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func CreateBackupSnapshotItemContext(ctx context.Context, database *sql.DB, snapshotID, relativePath string, size int64, mtime time.Time, fileHash []byte, state, errorCode string) (string, error) {
	var id string
	err := database.QueryRowContext(ctx, `INSERT INTO backup_snapshot_items (backup_snapshot_id, relative_path, is_dir, size_bytes, mtime, file_sha256, state, error_code) VALUES ($1, $2, FALSE, $3, $4, $5, $6, NULLIF($7, '')) RETURNING id`, snapshotID, relativePath, size, nullableTime(mtime), fileHash, state, errorCode).Scan(&id)
	return id, err
}

func CreateBackupSnapshotDirectoryContext(ctx context.Context, database *sql.DB, snapshotID, relativePath string, mtime time.Time) (string, error) {
	var id string
	err := database.QueryRowContext(ctx, `INSERT INTO backup_snapshot_items (backup_snapshot_id, relative_path, is_dir, size_bytes, mtime, state) VALUES ($1, $2, TRUE, 0, $3, 'AVAILABLE') RETURNING id`, snapshotID, relativePath, nullableTime(mtime)).Scan(&id)
	return id, err
}

func LinkBackupSnapshotItemBlocksContext(ctx context.Context, database *sql.DB, itemID string, blockIDs []string) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for ordinal, blockID := range blockIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backup_snapshot_item_blocks (backup_snapshot_item_id, ordinal, backup_block_id) VALUES ($1, $2, $3)`, itemID, ordinal, blockID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableTime(value time.Time) sql.NullTime {
	if value.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: value, Valid: true}
}

// PublishBackupSnapshotAndFinalizeContext makes the visible snapshot and run
// completion one transaction; a failed or cancelled run never exposes READY.
func PublishBackupSnapshotAndFinalizeContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID, snapshotID, snapshotState, runState string, totalFiles, totalDirs int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, unstableFiles, failedFiles int) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_snapshots SET state = $1, total_files = $2, total_dirs = $3, total_bytes = $4, omitted_unstable_count = $5, omitted_error_count = $6, integrity_state = 'VALID' WHERE id = $7 AND backup_job_id = $8 AND backup_run_id = $9 AND state = 'PUBLISHING'`, snapshotState, totalFiles, totalDirs, totalBytes, unstableFiles, failedFiles, snapshotID, jobID, runID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE backup_runs SET state = $1, total_files = $2, total_bytes = $3, processed_files = $4, processed_bytes = $5, deduplicated_bytes = $6, failed_files = $7, finished_at = CURRENT_TIMESTAMP WHERE id = $8 AND backup_job_id = $9 AND generation = $10 AND state = 'VERIFYING'`, runState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, runID, jobID, generation)
	if err != nil {
		return false, err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE backup_jobs SET status = 'IDLE', last_run_at = CURRENT_TIMESTAMP, last_run_status = $1, total_files = $2, total_bytes = $3, processed_files = $4, processed_bytes = $5, deduplicated_bytes = $6, failed_files = $7, error_code = NULL WHERE id = $8 AND run_generation = $9 AND status = 'VERIFYING'`, runState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, jobID, generation)
	if err != nil {
		return false, err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_maintenance (backup_job_id, kind) VALUES ($1, 'RETENTION')`, jobID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ListVisibleBackupSnapshotsContext returns only publish-complete snapshots.
// Draft and retention states are not observable through the user catalog.
func ListVisibleBackupSnapshotsContext(ctx context.Context, database *sql.DB, jobID string) ([]BackupSnapshot, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT id, state, created_at, total_files, total_dirs, total_bytes,
			omitted_unstable_count, omitted_error_count, integrity_state
		FROM backup_snapshots
		WHERE backup_job_id = $1 AND state IN ('READY', 'PARTIAL', 'DAMAGED')
		ORDER BY created_at DESC`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snapshots []BackupSnapshot
	for rows.Next() {
		var snapshot BackupSnapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.State, &snapshot.CreatedAt, &snapshot.TotalFiles, &snapshot.TotalDirs, &snapshot.TotalBytes,
			&snapshot.OmittedUnstableCount, &snapshot.OmittedErrorCount, &snapshot.IntegrityState); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

// ListBackupSnapshotChildrenContext lists exactly one tree level. The snapshot
// is bound to its job so a valid snapshot UUID cannot cross an ownership fence.
func ListBackupSnapshotChildrenContext(ctx context.Context, database *sql.DB, jobID, snapshotID, directory string) ([]BackupSnapshotItem, error) {
	if directory != "" {
		if _, err := NormalizeBackupSnapshotPath(directory); err != nil {
			return nil, err
		}
	}
	prefix := ""
	if directory != "" {
		prefix = directory + "/"
	}
	likePrefix := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
	rows, err := database.QueryContext(ctx, `
		SELECT i.relative_path, i.is_dir, i.size_bytes, i.mtime, i.state, i.error_code
		FROM backup_snapshot_items i
		JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND s.state IN ('READY', 'PARTIAL', 'DAMAGED')
			AND i.relative_path LIKE $3 ESCAPE '\'
		ORDER BY i.is_dir DESC, i.relative_path`, snapshotID, jobID, likePrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	children := make([]BackupSnapshotItem, 0)
	for rows.Next() {
		var item BackupSnapshotItem
		if err := rows.Scan(&item.RelativePath, &item.IsDir, &item.SizeBytes, &item.Mtime, &item.State, &item.ErrorCode); err != nil {
			return nil, err
		}
		remainder := strings.TrimPrefix(item.RelativePath, prefix)
		if remainder == "" || strings.Contains(remainder, "/") {
			continue
		}
		item.Name = remainder
		children = append(children, item)
	}
	return children, rows.Err()
}

// ListBackupSnapshotPacksContext returns every immutable object referenced by a
// draft snapshot. It is used only by the worker verification fence.
func ListBackupSnapshotPacksContext(ctx context.Context, database *sql.DB, jobID, snapshotID string) ([]BackupPack, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT DISTINCT p.id, p.remote_rel_path, p.sha256, p.size_bytes, p.state, p.last_checked_at
		FROM backup_packs p
		JOIN backup_blocks b ON b.backup_pack_id = p.id AND b.backup_job_id = p.backup_job_id
		JOIN backup_snapshot_item_blocks ib ON ib.backup_block_id = b.id
		JOIN backup_snapshot_items i ON i.id = ib.backup_snapshot_item_id
		JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND s.state = 'PUBLISHING'
		ORDER BY p.id`, snapshotID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var packs []BackupPack
	for rows.Next() {
		var pack BackupPack
		if err := rows.Scan(&pack.ID, &pack.RemotePath, &pack.SHA256, &pack.SizeBytes, &pack.State, &pack.LastCheckedAt); err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	}
	return packs, rows.Err()
}

func MarkBackupPacksCheckedContext(ctx context.Context, database *sql.DB, jobID string, packIDs []string) error {
	if len(packIDs) == 0 {
		return nil
	}
	for _, packID := range packIDs {
		if _, err := database.ExecContext(ctx, `UPDATE backup_packs SET last_checked_at = CURRENT_TIMESTAMP WHERE id = $1 AND backup_job_id = $2 AND state = 'READY'`, packID, jobID); err != nil {
			return err
		}
	}
	return nil
}

// MarkBackupPackDamagedContext propagates a verified missing or corrupt pack to
// only the snapshots that reference it. A draft is kept as diagnosis evidence.
func MarkBackupPackDamagedContext(ctx context.Context, database *sql.DB, jobID, packID string) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE backup_packs SET state = 'DAMAGED' WHERE id = $1 AND backup_job_id = $2`, packID, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE backup_snapshots s SET state = 'DAMAGED', integrity_state = 'DAMAGED'
		WHERE s.backup_job_id = $1 AND s.state IN ('PUBLISHING', 'READY', 'PARTIAL')
			AND EXISTS (
				SELECT 1 FROM backup_snapshot_items i
				JOIN backup_snapshot_item_blocks ib ON ib.backup_snapshot_item_id = i.id
				JOIN backup_blocks b ON b.id = ib.backup_block_id
				WHERE i.backup_snapshot_id = s.id AND b.backup_pack_id = $2
			)`, jobID, packID); err != nil {
		return err
	}
	return tx.Commit()
}

// RequestBackupRepositoryDeletionContext blocks all future runs, disables the
// schedule and makes remote cleanup durable before any catalog row is removed.
func RequestBackupRepositoryDeletionContext(ctx context.Context, database *sql.DB, jobID, userID string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET status = 'DELETING', deletion_state = 'DELETING' WHERE id = $1 AND user_id = $2 AND deletion_state = 'ACTIVE'`, jobID, userID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE schedules SET is_active = FALSE WHERE task_type = 'backup' AND task_id = $1`, jobID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_runs SET state = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE backup_job_id = $1 AND state IN ('QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING')`, jobID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_snapshots SET state = 'EXPIRED', expires_at = COALESCE(expires_at, CURRENT_TIMESTAMP) WHERE backup_job_id = $1 AND state IN ('PUBLISHING', 'READY', 'PARTIAL')`, jobID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO backup_maintenance (backup_job_id, kind, byte_budget) VALUES ($1, 'DELETE_REPOSITORY', 1000)`, jobID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ClaimNextBackupMaintenanceContext leases one pending maintenance request.
func ClaimNextBackupMaintenanceContext(ctx context.Context, database *sql.DB) (*BackupMaintenance, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	maintenance := &BackupMaintenance{}
	err = tx.QueryRowContext(ctx, `
		SELECT m.id, m.backup_job_id, j.user_id, j.lock_id, m.kind, m.state, m.byte_budget
		FROM backup_maintenance m JOIN backup_jobs j ON j.id = m.backup_job_id
		WHERE m.state IN ('PENDING', 'RETRY_WAIT') AND (m.next_retry_at IS NULL OR m.next_retry_at <= CURRENT_TIMESTAMP)
		ORDER BY m.created_at FOR UPDATE OF m SKIP LOCKED LIMIT 1`).Scan(&maintenance.ID, &maintenance.BackupJobID, &maintenance.UserID, &maintenance.LockID, &maintenance.Kind, &maintenance.State, &maintenance.ByteBudget)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_maintenance SET state = 'RUNNING', attempts = attempts + 1, started_at = COALESCE(started_at, CURRENT_TIMESTAMP), claim_deadline = CURRENT_TIMESTAMP + INTERVAL '10 minutes' WHERE id = $1`, maintenance.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	maintenance.State = "RUNNING"
	return maintenance, nil
}

func CompleteBackupMaintenanceContext(ctx context.Context, database *sql.DB, maintenanceID string) error {
	_, err := database.ExecContext(ctx, `UPDATE backup_maintenance SET state = 'COMPLETED', finished_at = CURRENT_TIMESTAMP, claim_deadline = NULL, error_code = NULL WHERE id = $1 AND state = 'RUNNING'`, maintenanceID)
	return err
}

func RetryBackupMaintenanceContext(ctx context.Context, database *sql.DB, maintenanceID, errorCode string) error {
	_, err := database.ExecContext(ctx, `UPDATE backup_maintenance SET state = 'RETRY_WAIT', claim_deadline = NULL, next_retry_at = CURRENT_TIMESTAMP + INTERVAL '30 seconds', error_code = $2 WHERE id = $1 AND state = 'RUNNING'`, maintenanceID, errorCode)
	return err
}

func ListBackupPacksForDeletionContext(ctx context.Context, database *sql.DB, jobID string, limit int) ([]BackupPack, error) {
	rows, err := database.QueryContext(ctx, `SELECT id, remote_rel_path, sha256, size_bytes, state, last_checked_at FROM backup_packs WHERE backup_job_id = $1 AND state <> 'DELETED' ORDER BY created_at LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var packs []BackupPack
	for rows.Next() {
		var pack BackupPack
		if err := rows.Scan(&pack.ID, &pack.RemotePath, &pack.SHA256, &pack.SizeBytes, &pack.State, &pack.LastCheckedAt); err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	}
	return packs, rows.Err()
}

func MarkBackupPackDeletedContext(ctx context.Context, database *sql.DB, jobID, packID string) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM backup_snapshot_item_blocks ib
		USING backup_snapshot_items i, backup_snapshots s, backup_blocks b
		WHERE ib.backup_snapshot_item_id = i.id AND i.backup_snapshot_id = s.id
			AND ib.backup_block_id = b.id AND b.backup_job_id = $1 AND b.backup_pack_id = $2
			AND s.state IN ('EXPIRED', 'DELETING')`, jobID, packID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_blocks b WHERE b.backup_job_id = $1 AND b.backup_pack_id = $2 AND NOT EXISTS (SELECT 1 FROM backup_snapshot_item_blocks ib WHERE ib.backup_block_id = b.id)`, jobID, packID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_packs SET state = 'DELETED' WHERE id = $1 AND backup_job_id = $2`, packID, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func EnqueueBackupMaintenanceContext(ctx context.Context, database *sql.DB, jobID, kind string) error {
	_, err := database.ExecContext(ctx, `INSERT INTO backup_maintenance (backup_job_id, kind, byte_budget) VALUES ($1, $2, CASE WHEN $2 = 'COMPACTION' THEN 536870912 ELSE NULL END)`, jobID, kind)
	return err
}

// ExpireBackupSnapshotsContext retains the newest N visible snapshots. It does
// not delete data itself; old objects remain readable to the compactor until
// their last live reference has been determined transactionally.
func ExpireBackupSnapshotsContext(ctx context.Context, database *sql.DB, jobID string, keep int) error {
	_, err := database.ExecContext(ctx, `
		UPDATE backup_snapshots SET state = 'EXPIRED', expires_at = CURRENT_TIMESTAMP
		WHERE id IN (
			SELECT id FROM (
				SELECT id, row_number() OVER (ORDER BY created_at DESC) AS position
				FROM backup_snapshots WHERE backup_job_id = $1 AND state IN ('READY', 'PARTIAL')
			) retained WHERE position > $2
		)`, jobID, keep)
	return err
}

func ListBackupReclaimablePacksContext(ctx context.Context, database *sql.DB, jobID string, limit int) ([]BackupPack, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT p.id, p.remote_rel_path, p.sha256, p.size_bytes, p.state, p.last_checked_at
		FROM backup_packs p
		WHERE p.backup_job_id = $1 AND p.state IN ('READY', 'DELETE_PENDING')
			AND NOT EXISTS (
				SELECT 1 FROM backup_blocks b
				JOIN backup_snapshot_item_blocks ib ON ib.backup_block_id = b.id
				JOIN backup_snapshot_items i ON i.id = ib.backup_snapshot_item_id
				JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
				WHERE b.backup_pack_id = p.id AND s.state IN ('READY', 'PARTIAL')
			)
		ORDER BY p.created_at LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var packs []BackupPack
	for rows.Next() {
		var pack BackupPack
		if err := rows.Scan(&pack.ID, &pack.RemotePath, &pack.SHA256, &pack.SizeBytes, &pack.State, &pack.LastCheckedAt); err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	}
	return packs, rows.Err()
}

func ListBackupLiveBlocksContext(ctx context.Context, database *sql.DB, jobID, packID string) ([]BackupLiveBlock, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT b.id, b.sha256, b.plaintext_size FROM backup_blocks b
		WHERE b.backup_job_id = $1 AND b.backup_pack_id = $2 AND EXISTS (
			SELECT 1 FROM backup_snapshot_item_blocks ib
			JOIN backup_snapshot_items i ON i.id = ib.backup_snapshot_item_id
			JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
			WHERE ib.backup_block_id = b.id AND s.state IN ('READY', 'PARTIAL')
		)
		ORDER BY b.id`, jobID, packID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blocks []BackupLiveBlock
	for rows.Next() {
		var block BackupLiveBlock
		if err := rows.Scan(&block.ID, &block.Hash, &block.Size); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

func FindBackupCompactionCandidateContext(ctx context.Context, database *sql.DB, jobID string) (*BackupPack, error) {
	pack := &BackupPack{}
	err := database.QueryRowContext(ctx, `
		SELECT p.id, p.remote_rel_path, p.sha256, p.size_bytes, p.state, p.last_checked_at
		FROM backup_packs p WHERE p.backup_job_id = $1 AND p.state = 'READY'
			AND EXISTS (SELECT 1 FROM backup_blocks b JOIN backup_snapshot_item_blocks ib ON ib.backup_block_id = b.id JOIN backup_snapshot_items i ON i.id = ib.backup_snapshot_item_id JOIN backup_snapshots s ON s.id = i.backup_snapshot_id WHERE b.backup_pack_id = p.id AND s.state IN ('READY', 'PARTIAL'))
			AND EXISTS (SELECT 1 FROM backup_blocks b WHERE b.backup_pack_id = p.id AND NOT EXISTS (SELECT 1 FROM backup_snapshot_item_blocks ib JOIN backup_snapshot_items i ON i.id = ib.backup_snapshot_item_id JOIN backup_snapshots s ON s.id = i.backup_snapshot_id WHERE ib.backup_block_id = b.id AND s.state IN ('READY', 'PARTIAL')))
		ORDER BY p.created_at LIMIT 1`, jobID).Scan(&pack.ID, &pack.RemotePath, &pack.SHA256, &pack.SizeBytes, &pack.State, &pack.LastCheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pack, nil
}

// ReplaceBackupPackLocatorsContext switches every live locator before the old
// pack may be deleted. The old object is deliberately left DELETE_PENDING.
func ReplaceBackupPackLocatorsContext(ctx context.Context, database *sql.DB, jobID, oldPackID, remotePath string, packHash []byte, packSize int64, blocks []BackupPackBlock, blockIDs []string) (string, error) {
	if len(blocks) == 0 || len(blocks) != len(blockIDs) {
		return "", errors.New("replacement backup pack blocks are invalid")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var replacementID string
	if err := tx.QueryRowContext(ctx, `INSERT INTO backup_packs (backup_job_id, remote_rel_path, sha256, size_bytes, state, generation) VALUES ($1, $2, $3, $4, 'READY', 1) RETURNING id`, jobID, remotePath, packHash, packSize).Scan(&replacementID); err != nil {
		return "", err
	}
	for index, block := range blocks {
		result, err := tx.ExecContext(ctx, `UPDATE backup_blocks SET backup_pack_id = $1, payload_offset = $2, payload_length = $3 WHERE id = $4 AND backup_job_id = $5 AND backup_pack_id = $6 AND sha256 = $7`, replacementID, block.PayloadOffset, block.PayloadLength, blockIDs[index], jobID, oldPackID, block.Hash)
		if err != nil {
			return "", err
		}
		n, err := result.RowsAffected()
		if err != nil || n != 1 {
			return "", errors.New("backup block changed during compaction")
		}
	}
	// Expired snapshots are hidden permanently, so their block mappings can be
	// discarded before reclamation while retaining the snapshot's audit metadata.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM backup_snapshot_item_blocks ib
		USING backup_snapshot_items i, backup_snapshots s, backup_blocks b
		WHERE ib.backup_snapshot_item_id = i.id AND i.backup_snapshot_id = s.id
			AND ib.backup_block_id = b.id AND b.backup_job_id = $1 AND b.backup_pack_id = $2
			AND s.state IN ('EXPIRED', 'DELETING')`, jobID, oldPackID); err != nil {
		return "", err
	}
	// Dead locators must disappear before the old pack is removed. Otherwise a
	// later dedup lookup could reference a pack that no longer exists remotely.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM backup_blocks b
		WHERE b.backup_job_id = $1 AND b.backup_pack_id = $2
			AND NOT EXISTS (SELECT 1 FROM backup_snapshot_item_blocks ib WHERE ib.backup_block_id = b.id)`, jobID, oldPackID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_packs SET state = 'DELETE_PENDING' WHERE id = $1 AND backup_job_id = $2 AND state = 'READY'`, oldPackID, jobID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return replacementID, nil
}

func DeleteBackupJobAfterRepositoryCleanupContext(ctx context.Context, database *sql.DB, jobID string) (bool, error) {
	result, err := database.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id = $1 AND status = 'DELETING' AND NOT EXISTS (SELECT 1 FROM backup_packs WHERE backup_job_id = $1 AND state <> 'DELETED')`, jobID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// NormalizeBackupSnapshotPath accepts the catalog's slash-separated relative
// paths and rejects traversal before it reaches the SQL tree predicate.
func NormalizeBackupSnapshotPath(value string) (string, error) {
	if strings.Contains(value, `\`) || strings.HasPrefix(value, "/") {
		return "", errors.New("invalid backup snapshot path")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid backup snapshot path")
		}
	}
	return value, nil
}

const insertBackupJobQuery = `
	INSERT INTO backup_jobs (
		user_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
		source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
		target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at,
		target_mega_session_id_encrypted, target_mega_master_key_encrypted, source_provider, target_provider,
		selected_paths, target_dir, repository_root, cron_expression, timezone, retention_count, threads, status, deletion_state
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)
	RETURNING id, lock_id, repository_id, created_at, updated_at
`

func CreateBackupJobAndScheduleContext(ctx context.Context, database *sql.DB, job *BackupJob, schedule *Schedule) (string, error) {
	if job == nil || schedule == nil {
		return "", fmt.Errorf("backup job and schedule are required")
	}
	if schedule.UserID != job.UserID || (schedule.TaskType != "" && schedule.TaskType != "backup") || !schedule.NextRunAt.Valid {
		return "", fmt.Errorf("backup schedule does not match job")
	}
	if job.Status == "" {
		job.Status = "IDLE"
	}
	if job.DeletionState == "" {
		job.DeletionState = "ACTIVE"
	}
	if job.RetentionCount == 0 {
		job.RetentionCount = 30
	}
	if job.Threads == 0 {
		job.Threads = 8
	}
	if !schedule.CronExpression.Valid {
		schedule.CronExpression = sql.NullString{String: job.CronExpression, Valid: true}
	}
	if !schedule.CronExpression.Valid || schedule.CronExpression.String != job.CronExpression {
		return "", fmt.Errorf("backup schedule must use the job cron expression")
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin backup job creation: %w", err)
	}
	defer tx.Rollback()

	if err := insertBackupJob(ctx, tx, job); err != nil {
		return "", fmt.Errorf("insert backup job: %w", err)
	}
	schedule.UserID = job.UserID
	schedule.TaskType = "backup"
	schedule.TaskID = job.ID
	if err := insertSchedule(ctx, tx, schedule); err != nil {
		return "", fmt.Errorf("insert backup schedule: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit backup job creation: %w", err)
	}
	return job.ID, nil
}

func insertBackupJob(ctx context.Context, database queryExecerContext, job *BackupJob) error {
	return database.QueryRowContext(ctx, insertBackupJobQuery,
		job.UserID, job.SourceProfileID, job.TargetProfileID, job.SourceURL, job.SourceUsername, job.SourcePasswordEncrypted,
		job.SourceRefreshTokenEncrypted, job.SourceTokenExpiresAt, job.SourceMegaSessionIDEncrypted, job.SourceMegaMasterKeyEncrypted,
		job.TargetURL, job.TargetUsername, job.TargetPasswordEncrypted, job.TargetRefreshTokenEncrypted, job.TargetTokenExpiresAt,
		job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, job.SourceProvider, job.TargetProvider,
		job.SelectedPaths, job.TargetDir, job.RepositoryRoot, job.CronExpression, job.Timezone, job.RetentionCount, job.Threads, job.Status, job.DeletionState,
	).Scan(&job.ID, &job.LockID, &job.RepositoryID, &job.CreatedAt, &job.UpdatedAt)
}

func GetBackupJobForOwnerContext(ctx context.Context, database *sql.DB, jobID, userID string) (*BackupJob, error) {
	job := &BackupJob{}
	err := database.QueryRowContext(ctx, `
		SELECT id, user_id, lock_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
			source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
			target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at,
			target_mega_session_id_encrypted, target_mega_master_key_encrypted, source_provider, target_provider, selected_paths,
			target_dir, repository_id, repository_root, cron_expression, timezone, retention_count, threads, status, run_generation,
			last_run_at, last_run_status, total_files, total_bytes, processed_files, processed_bytes, deduplicated_bytes, failed_files,
			error_code, deletion_state, created_at, updated_at
		FROM backup_jobs WHERE id = $1 AND user_id = $2
	`, jobID, userID).Scan(
		&job.ID, &job.UserID, &job.LockID, &job.SourceProfileID, &job.TargetProfileID, &job.SourceURL, &job.SourceUsername, &job.SourcePasswordEncrypted,
		&job.SourceRefreshTokenEncrypted, &job.SourceTokenExpiresAt, &job.SourceMegaSessionIDEncrypted, &job.SourceMegaMasterKeyEncrypted,
		&job.TargetURL, &job.TargetUsername, &job.TargetPasswordEncrypted, &job.TargetRefreshTokenEncrypted, &job.TargetTokenExpiresAt,
		&job.TargetMegaSessionIDEncrypted, &job.TargetMegaMasterKeyEncrypted, &job.SourceProvider, &job.TargetProvider, &job.SelectedPaths,
		&job.TargetDir, &job.RepositoryID, &job.RepositoryRoot, &job.CronExpression, &job.Timezone, &job.RetentionCount, &job.Threads, &job.Status, &job.RunGeneration,
		&job.LastRunAt, &job.LastRunStatus, &job.TotalFiles, &job.TotalBytes, &job.ProcessedFiles, &job.ProcessedBytes, &job.DeduplicatedBytes, &job.FailedFiles,
		&job.ErrorCode, &job.DeletionState, &job.CreatedAt, &job.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func ListBackupJobsForOwnerContext(ctx context.Context, database *sql.DB, userID string) ([]BackupJob, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT id, user_id, lock_id, source_profile_id, target_profile_id, source_url, source_username, source_password_encrypted,
			source_refresh_token_encrypted, source_token_expires_at, source_mega_session_id_encrypted, source_mega_master_key_encrypted,
			target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at,
			target_mega_session_id_encrypted, target_mega_master_key_encrypted, source_provider, target_provider, selected_paths,
			target_dir, repository_id, repository_root, cron_expression, timezone, retention_count, threads, status, run_generation,
			last_run_at, last_run_status, total_files, total_bytes, processed_files, processed_bytes, deduplicated_bytes, failed_files,
			error_code, deletion_state, created_at, updated_at
		FROM backup_jobs WHERE user_id = $1 ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []BackupJob
	for rows.Next() {
		var job BackupJob
		if err := scanBackupJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

type backupJobScanner interface {
	Scan(dest ...any) error
}

func scanBackupJob(row backupJobScanner, job *BackupJob) error {
	return row.Scan(
		&job.ID, &job.UserID, &job.LockID, &job.SourceProfileID, &job.TargetProfileID, &job.SourceURL, &job.SourceUsername, &job.SourcePasswordEncrypted,
		&job.SourceRefreshTokenEncrypted, &job.SourceTokenExpiresAt, &job.SourceMegaSessionIDEncrypted, &job.SourceMegaMasterKeyEncrypted,
		&job.TargetURL, &job.TargetUsername, &job.TargetPasswordEncrypted, &job.TargetRefreshTokenEncrypted, &job.TargetTokenExpiresAt,
		&job.TargetMegaSessionIDEncrypted, &job.TargetMegaMasterKeyEncrypted, &job.SourceProvider, &job.TargetProvider, &job.SelectedPaths,
		&job.TargetDir, &job.RepositoryID, &job.RepositoryRoot, &job.CronExpression, &job.Timezone, &job.RetentionCount, &job.Threads, &job.Status, &job.RunGeneration,
		&job.LastRunAt, &job.LastRunStatus, &job.TotalFiles, &job.TotalBytes, &job.ProcessedFiles, &job.ProcessedBytes, &job.DeduplicatedBytes, &job.FailedFiles,
		&job.ErrorCode, &job.DeletionState, &job.CreatedAt, &job.UpdatedAt,
	)
}

func VerifyBackupJobOwnershipContext(ctx context.Context, database *sql.DB, jobID, userID string) (bool, error) {
	var owned bool
	err := database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE id = $1 AND user_id = $2)`, jobID, userID).Scan(&owned)
	return owned, err
}

func ClaimBackupJobPassContext(ctx context.Context, database *sql.DB, jobID, trigger string, scheduledLocalKey *string) (BackupPassClaim, error) {
	if trigger != "manual" && trigger != "schedule" && trigger != "catch_up" {
		return BackupPassClaim{}, fmt.Errorf("invalid backup trigger %q", trigger)
	}
	if trigger == "manual" && scheduledLocalKey != nil {
		return BackupPassClaim{}, fmt.Errorf("manual backup run cannot have a scheduled local key")
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return BackupPassClaim{}, fmt.Errorf("begin backup pass claim: %w", err)
	}
	defer tx.Rollback()

	var status string
	var generation int
	err = tx.QueryRowContext(ctx, `SELECT status, run_generation FROM backup_jobs WHERE id = $1 FOR UPDATE`, jobID).Scan(&status, &generation)
	if err != nil {
		return BackupPassClaim{}, err
	}
	if status == "QUEUED" || status == "SCANNING" || status == "RUNNING" || status == "VERIFYING" {
		return BackupPassClaim{Outcome: BackupClaimOverlap}, nil
	}
	if status != "IDLE" && status != "FAILED" {
		return BackupPassClaim{Outcome: BackupClaimBlocked}, nil
	}

	run := &BackupRun{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO backup_runs (backup_job_id, generation, trigger, scheduled_local_key)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (backup_job_id, scheduled_local_key) WHERE scheduled_local_key IS NOT NULL DO NOTHING
		RETURNING id, backup_job_id, generation, trigger, scheduled_local_key, state, created_at, updated_at
	`, jobID, generation+1, trigger, scheduledLocalKey).Scan(
		&run.ID, &run.BackupJobID, &run.Generation, &run.Trigger, &run.ScheduledLocalKey, &run.State, &run.CreatedAt, &run.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupPassClaim{Outcome: BackupClaimDuplicate}, nil
	}
	if err != nil {
		return BackupPassClaim{}, fmt.Errorf("insert backup run: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE backup_jobs SET status = 'QUEUED', run_generation = $1, error_code = NULL, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND run_generation = $3 AND status IN ('IDLE', 'FAILED')
	`, run.Generation, jobID, generation)
	if err != nil {
		return BackupPassClaim{}, fmt.Errorf("reserve backup job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return BackupPassClaim{}, fmt.Errorf("read backup claim result: %w", err)
	}
	if updated != 1 {
		return BackupPassClaim{}, fmt.Errorf("backup job changed while locked")
	}
	if err := tx.Commit(); err != nil {
		return BackupPassClaim{}, fmt.Errorf("commit backup pass claim: %w", err)
	}
	return BackupPassClaim{Outcome: BackupClaimed, Run: run}, nil
}

func TransitionBackupRunContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID, expectedState, nextState string) (bool, error) {
	if !validBackupTransition(expectedState, nextState) {
		return false, fmt.Errorf("invalid backup run transition %s to %s", expectedState, nextState)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin backup run transition: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE backup_runs SET state = $1, started_at = CASE WHEN $1 = 'SCANNING' THEN COALESCE(started_at, CURRENT_TIMESTAMP) ELSE started_at END, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND backup_job_id = $3 AND generation = $4 AND state = $5
	`, nextState, runID, jobID, generation, expectedState)
	if err != nil {
		return false, fmt.Errorf("transition backup run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read backup run transition result: %w", err)
	}
	if updated == 0 {
		return false, nil
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE backup_jobs SET status = $1, updated_at = CURRENT_TIMESTAMP
		WHERE id = $2 AND run_generation = $3 AND status = $4
	`, nextState, jobID, generation, expectedState)
	if err != nil {
		return false, fmt.Errorf("transition backup job: %w", err)
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read backup job transition result: %w", err)
	}
	if updated == 0 {
		return false, fmt.Errorf("backup job state no longer matches run")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit backup run transition: %w", err)
	}
	return true, nil
}

func validBackupTransition(expectedState, nextState string) bool {
	return (expectedState == "QUEUED" && nextState == "SCANNING") ||
		(expectedState == "SCANNING" && nextState == "RUNNING") ||
		(expectedState == "RUNNING" && nextState == "VERIFYING")
}

func FinalizeBackupRunContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID, expectedState, terminalState string, totalFiles int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, failedFiles int, errorCode *string) (bool, error) {
	if (expectedState != "SCANNING" && expectedState != "RUNNING" && expectedState != "VERIFYING") || (terminalState != "COMPLETED" && terminalState != "PARTIAL" && terminalState != "CANCELLED") {
		return false, fmt.Errorf("invalid backup run finalization")
	}
	return completeBackupRunContext(ctx, database, jobID, generation, runID, expectedState, terminalState, "IDLE", totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, errorCode)
}

func FailBackupRunContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID, expectedState string, errorCode string) (bool, error) {
	if expectedState != "QUEUED" && expectedState != "SCANNING" && expectedState != "RUNNING" && expectedState != "VERIFYING" {
		return false, fmt.Errorf("invalid backup run failure state %q", expectedState)
	}
	return completeBackupRunContext(ctx, database, jobID, generation, runID, expectedState, "FAILED", "FAILED", 0, 0, 0, 0, 0, 0, &errorCode)
}

func completeBackupRunContext(ctx context.Context, database *sql.DB, jobID string, generation int, runID, expectedState, terminalState, jobState string, totalFiles int, totalBytes int64, processedFiles int, processedBytes, deduplicatedBytes int64, failedFiles int, errorCode *string) (bool, error) {
	if totalFiles < 0 || totalBytes < 0 || processedFiles < 0 || processedBytes < 0 || deduplicatedBytes < 0 || failedFiles < 0 {
		return false, fmt.Errorf("backup run totals must not be negative")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin backup run completion: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE backup_runs
		SET state = $1, total_files = $2, total_bytes = $3, processed_files = $4, processed_bytes = $5,
			deduplicated_bytes = $6, failed_files = $7, error_code = $8, finished_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE id = $9 AND backup_job_id = $10 AND generation = $11 AND state = $12
	`, terminalState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, nullableString(errorCode), runID, jobID, generation, expectedState)
	if err != nil {
		return false, fmt.Errorf("complete backup run: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read backup run completion result: %w", err)
	}
	if updated == 0 {
		return false, nil
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE backup_jobs
		SET status = $1, last_run_at = CURRENT_TIMESTAMP, last_run_status = $2, total_files = $3, total_bytes = $4,
			processed_files = $5, processed_bytes = $6, deduplicated_bytes = $7, failed_files = $8, error_code = $9, updated_at = CURRENT_TIMESTAMP
		WHERE id = $10 AND run_generation = $11 AND status = $12
	`, jobState, terminalState, totalFiles, totalBytes, processedFiles, processedBytes, deduplicatedBytes, failedFiles, nullableString(errorCode), jobID, generation, expectedState)
	if err != nil {
		return false, fmt.Errorf("complete backup job: %w", err)
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read backup job completion result: %w", err)
	}
	if updated == 0 {
		return false, fmt.Errorf("backup job state no longer matches run")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit backup run completion: %w", err)
	}
	return true, nil
}

func nullableString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}
