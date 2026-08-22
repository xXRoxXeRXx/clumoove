package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

const restorePreviewLifetime = 30 * time.Minute

var (
	ErrRestorePreviewNotFound     = errors.New("restore preview not found")
	ErrRestorePreviewInvalidState = errors.New("restore preview invalid state")
	ErrRestorePreviewExpired      = errors.New("restore preview expired")
	ErrRestorePreviewStale        = errors.New("restore preview stale")
	ErrRestoreSnapshotUnavailable = errors.New("restore snapshot unavailable")
	ErrRestoreRetryMismatch       = errors.New("restore retry mismatch")
	ErrRestoreRetryActive         = errors.New("restore retry active")
)

func normalizeRestorePath(p string) string {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" || trimmed == "/" || trimmed == "." {
		return ""
	}
	clean := path.Clean(trimmed)
	clean = strings.Trim(clean, "/")
	if clean == "." {
		return ""
	}
	return clean
}

// RestoreConfigFingerprint is the stable identity of a restore configuration.
// Limits and mutable credentials are deliberately excluded so a retry can use
// fresh credentials while still proving it targets the same immutable data.
func RestoreConfigFingerprint(snapshotID string, selectedPaths StringArray, provider, targetRoot, profileID, conflictStrategy string) ([sha256.Size]byte, error) {
	return RestoreConfigFingerprintWithIdentity(snapshotID, selectedPaths, provider, targetRoot, "profile:"+profileID, conflictStrategy)
}

// RestoreConfigFingerprintWithIdentity is the direct-credential variant of
// RestoreConfigFingerprint. identity is canonical non-secret connection
// identity (for example an owned profile ID or normalized endpoint/user), so
// changing only a password, OAuth access token, or run limits never changes a
// retry's immutable configuration.
func RestoreConfigFingerprintWithIdentity(snapshotID string, selectedPaths StringArray, provider, targetRoot, identity, conflictStrategy string) ([sha256.Size]byte, error) {
	paths := make([]string, 0, len(selectedPaths))
	for _, p := range selectedPaths {
		paths = append(paths, normalizeRestorePath(p))
	}
	sort.Strings(paths)
	payload := struct {
		Format   string   `json:"format"`
		Snapshot string   `json:"snapshot"`
		Paths    []string `json:"paths"`
		Provider string   `json:"provider"`
		Root     string   `json:"root"`
		Identity string   `json:"identity"`
		Conflict string   `json:"conflict"`
	}{
		Format: "restore-config-v1", Snapshot: snapshotID, Paths: paths,
		Provider: strings.ToLower(strings.TrimSpace(provider)),
		Root:     strings.Trim(strings.TrimSpace(targetRoot), "/"),
		Identity: strings.TrimSpace(identity), Conflict: strings.ToUpper(strings.TrimSpace(conflictStrategy)),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

// RestorePreview is the durable, one-time plan for a restore. It deliberately
// stores only non-secret target profile provenance; credentials remain owned by
// the profile and are loaded by the worker immediately before use.
type RestorePreview struct {
	ID                           string          `json:"id"`
	UserID                       string          `json:"-"`
	BackupJobID                  string          `json:"backup_job_id"`
	BackupSnapshotID             string          `json:"backup_snapshot_id"`
	RetryRestoreJobID            sql.NullString  `json:"retry_restore_job_id,omitempty"`
	TargetProfileID              sql.NullString  `json:"target_profile_id,omitempty"`
	SelectedPaths                StringArray     `json:"selected_paths"`
	TargetProvider               string          `json:"target_provider"`
	TargetURL                    string          `json:"-"`
	TargetUsername               string          `json:"-"`
	TargetPasswordEncrypted      sql.NullString  `json:"-"`
	TargetRefreshTokenEncrypted  sql.NullString  `json:"-"`
	TargetTokenExpiresAt         sql.NullTime    `json:"-"`
	TargetMegaSessionIDEncrypted sql.NullString  `json:"-"`
	TargetMegaMasterKeyEncrypted sql.NullString  `json:"-"`
	TargetConnectionIdentity     string          `json:"-"`
	TargetRoot                   string          `json:"target_root"`
	ConflictStrategy             string          `json:"conflict_strategy"`
	Threads                      int             `json:"threads"`
	BandwidthMbps                int             `json:"bandwidth_mbps"`
	ConfigFingerprint            []byte          `json:"-"`
	Status                       string          `json:"status"`
	TotalFiles                   int             `json:"total_files"`
	TotalDirectories             int             `json:"total_directories"`
	TotalBytes                   int64           `json:"total_bytes"`
	ExistingFileConflicts        int             `json:"existing_file_conflicts"`
	MergeableDirectories         int             `json:"mergeable_directories"`
	TypeConflicts                int             `json:"type_conflicts"`
	UnavailableItems             int             `json:"unavailable_items"`
	ExpectedSkips                int             `json:"expected_skips"`
	ExpectedRenames              int             `json:"expected_renames"`
	MetadataWarnings             int             `json:"metadata_warnings"`
	ConflictExamples             json.RawMessage `json:"conflict_examples"`
	ErrorCode                    sql.NullString  `json:"error_code,omitempty"`
	CoordinatorGeneration        int             `json:"-"`
	CoordinatorLeaseUntil        sql.NullTime    `json:"-"`
	WorkerHash                   sql.NullString  `json:"-"`
	ReadyAt                      sql.NullTime    `json:"ready_at,omitempty"`
	ExpiresAt                    sql.NullTime    `json:"expires_at,omitempty"`
	CreatedAt                    time.Time       `json:"created_at"`
	UpdatedAt                    time.Time       `json:"updated_at"`
}

type RestoreJob struct {
	ID                       string         `json:"id"`
	UserID                   string         `json:"-"`
	BackupJobID              sql.NullString `json:"backup_job_id,omitempty"`
	BackupSnapshotID         sql.NullString `json:"backup_snapshot_id,omitempty"`
	TargetProfileID          sql.NullString `json:"target_profile_id,omitempty"`
	SelectedPaths            StringArray    `json:"selected_paths"`
	TargetProvider           string         `json:"target_provider"`
	TargetURL                string         `json:"-"`
	TargetUsername           string         `json:"-"`
	TargetConnectionIdentity string         `json:"-"`
	TargetRoot               string         `json:"target_root"`
	ConflictStrategy         string         `json:"conflict_strategy"`
	Threads                  int            `json:"threads"`
	BandwidthMbps            int            `json:"bandwidth_mbps"`
	ConfigFingerprint        []byte         `json:"-"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

type RestoreRun struct {
	ID                           string         `json:"id"`
	RestoreJobID                 string         `json:"restore_job_id"`
	Generation                   int            `json:"generation"`
	Status                       string         `json:"status"`
	Threads                      int            `json:"threads"`
	BandwidthMbps                int            `json:"bandwidth_mbps"`
	TargetPasswordEncrypted      sql.NullString `json:"-"`
	TargetRefreshTokenEncrypted  sql.NullString `json:"-"`
	TargetTokenExpiresAt         sql.NullTime   `json:"-"`
	TargetMegaSessionIDEncrypted sql.NullString `json:"-"`
	TargetMegaMasterKeyEncrypted sql.NullString `json:"-"`
	CoordinatorGeneration        int            `json:"-"`
	CoordinatorLeaseUntil        sql.NullTime   `json:"-"`
	WorkerHash                   sql.NullString `json:"-"`
	TotalFiles                   int            `json:"total_files"`
	TotalBytes                   int64          `json:"total_bytes"`
	ProcessedFiles               int            `json:"processed_files"`
	ProcessedBytes               int64          `json:"processed_bytes"`
	FailedFiles                  int            `json:"failed_files"`
	ErrorCode                    sql.NullString `json:"error_code,omitempty"`
	StartedAt                    sql.NullTime   `json:"started_at,omitempty"`
	FinishedAt                   sql.NullTime   `json:"finished_at,omitempty"`
	CreatedAt                    time.Time      `json:"created_at"`
	UpdatedAt                    time.Time      `json:"updated_at"`
}

type RestoreItem struct {
	ID                   string          `json:"id"`
	RestoreRunID         string          `json:"restore_run_id"`
	ParentItemID         sql.NullString  `json:"parent_item_id,omitempty"`
	SnapshotRelativePath string          `json:"snapshot_relative_path"`
	IsDir                bool            `json:"is_dir"`
	SizeBytes            int64           `json:"size_bytes"`
	FileSHA256           []byte          `json:"-"`
	SourceMTime          sql.NullTime    `json:"source_mtime,omitempty"`
	SourceMetadata       json.RawMessage `json:"source_metadata,omitempty"`
	TargetPath           string          `json:"target_path"`
	ResolvedTargetPath   sql.NullString  `json:"resolved_target_path,omitempty"`
	Status               string          `json:"status"`
	VerificationKind     sql.NullString  `json:"verification_kind,omitempty"`
	OutcomeCode          sql.NullString  `json:"outcome_code,omitempty"`
	Attempts             int             `json:"attempts"`
	NextRetryAt          sql.NullTime    `json:"next_retry_at,omitempty"`
	ClaimEpoch           int64           `json:"-"`
	WorkerHash           sql.NullString  `json:"-"`
	ClaimDeadline        sql.NullTime    `json:"-"`
	ErrorCode            sql.NullString  `json:"error_code,omitempty"`
}

type RestoreItemBlock struct {
	Ordinal       int
	PackPath      string
	PackSHA256    []byte
	PackSizeBytes int64
	PayloadOffset int64
	PayloadLength int
	BlockSHA256   []byte
	PlaintextSize int
}

// RestorePreviewStats is intentionally aggregate-only: target trees can be
// large and only a bounded, sanitized sample is durable preview output.
type RestorePreviewStats struct {
	Files, Directories, ExistingFiles, MergeableDirectories, TypeConflicts int
	UnavailableItems, ExpectedSkips, ExpectedRenames, MetadataWarnings     int
	Bytes                                                                  int64
	ConflictExamples                                                       json.RawMessage
}

// RestorePreviewItem is the immutable catalog data needed for a read-only
// target preview. It deliberately exposes no pack locators or repository
// credentials to the API layer.
type RestorePreviewItem struct {
	RelativePath string
	IsDir        bool
	SizeBytes    int64
	State        string
	SourceMTime  sql.NullTime
	Metadata     json.RawMessage
}

func restoreClaimEpoch(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func ListRestorePreviewItemsContext(ctx context.Context, database *sql.DB, preview *RestorePreview) ([]RestorePreviewItem, error) {
	if preview == nil {
		return nil, errors.New("restore preview is required")
	}
	selected, err := json.Marshal(preview.SelectedPaths)
	if err != nil {
		return nil, err
	}
	rows, err := database.QueryContext(ctx, `
		SELECT i.relative_path, i.is_dir, i.size_bytes, i.state, i.mtime, i.metadata
		FROM backup_snapshot_items i JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND s.state IN ('READY','PARTIAL') AND s.integrity_state <> 'DAMAGED'
		AND EXISTS (SELECT 1 FROM jsonb_array_elements_text($3::jsonb) AS selected(path) WHERE selected.path = '' OR i.relative_path = selected.path OR i.relative_path LIKE selected.path || '/%')
		ORDER BY i.is_dir DESC, length(i.relative_path), i.relative_path`, preview.BackupSnapshotID, preview.BackupJobID, string(selected))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RestorePreviewItem, 0)
	for rows.Next() {
		var item RestorePreviewItem
		if err := rows.Scan(&item.RelativePath, &item.IsDir, &item.SizeBytes, &item.State, &item.SourceMTime, &item.Metadata); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ClaimNextRestoreItemContext claims one item while enforcing the persisted
// per-run thread cap. Directories are claimed parent-first so empty directories
// are preserved and files never race their required parent creation.
func ClaimNextRestoreItemContext(ctx context.Context, database *sql.DB) (*RestoreItem, error) {
	return ClaimNextRestoreItemForWorkerContext(ctx, database, "")
}

func ClaimNextRestoreItemForWorkerContext(ctx context.Context, database *sql.DB, workerID string) (*RestoreItem, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin restore item claim: %w", err)
	}
	defer tx.Rollback()
	item := &RestoreItem{}
	err = tx.QueryRowContext(ctx, `
		SELECT i.id, i.restore_run_id, i.parent_item_id, i.snapshot_relative_path, i.is_dir, i.size_bytes, i.file_sha256, i.source_mtime, i.source_metadata, i.target_path, i.resolved_target_path, i.status, i.verification_kind, i.outcome_code, i.attempts, i.next_retry_at, i.claim_epoch, i.worker_hash, i.claim_deadline, i.error_code
		FROM restore_items i
		JOIN restore_runs r ON r.id = i.restore_run_id
		JOIN restore_jobs j ON j.id = r.restore_job_id
		WHERE i.status = 'PENDING' AND (i.next_retry_at IS NULL OR i.next_retry_at <= CURRENT_TIMESTAMP) AND r.status = 'RUNNING'
		AND (SELECT COUNT(*) FROM restore_items active WHERE active.restore_run_id = i.restore_run_id AND active.status = 'RUNNING') < j.threads
		AND NOT EXISTS (
			SELECT 1 FROM restore_items parent
			WHERE parent.restore_run_id = i.restore_run_id AND parent.is_dir
			AND parent.status IN ('PENDING','RUNNING')
			AND parent.snapshot_relative_path <> i.snapshot_relative_path
			AND i.snapshot_relative_path LIKE parent.snapshot_relative_path || '/%'
		)
		ORDER BY i.is_dir DESC, length(i.snapshot_relative_path), i.created_at FOR UPDATE OF i, r SKIP LOCKED LIMIT 1`).Scan(
		&item.ID, &item.RestoreRunID, &item.ParentItemID, &item.SnapshotRelativePath, &item.IsDir, &item.SizeBytes, &item.FileSHA256, &item.SourceMTime, &item.SourceMetadata, &item.TargetPath, &item.ResolvedTargetPath, &item.Status, &item.VerificationKind, &item.OutcomeCode, &item.Attempts, &item.NextRetryAt, &item.ClaimEpoch, &item.WorkerHash, &item.ClaimDeadline, &item.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select restore item: %w", err)
	}
	var claimEpoch int64
	err = tx.QueryRowContext(ctx, `UPDATE restore_items SET status = 'RUNNING', next_retry_at = NULL, error_code = NULL, worker_hash = $2, claim_epoch = claim_epoch + 1, claim_deadline = CURRENT_TIMESTAMP + INTERVAL '2 minutes' WHERE id = $1 AND status = 'PENDING' AND (next_retry_at IS NULL OR next_retry_at <= CURRENT_TIMESTAMP) RETURNING claim_epoch`, item.ID, nullableStringValue(workerID)).Scan(&claimEpoch)
	if err != nil {
		return nil, fmt.Errorf("claim restore item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore item claim: %w", err)
	}
	item.Status = "RUNNING"
	item.ClaimEpoch = claimEpoch
	item.WorkerHash = sql.NullString{String: workerID, Valid: workerID != ""}
	return item, nil
}

func ListRestoreItemBlocksContext(ctx context.Context, database *sql.DB, itemID, runID string) ([]RestoreItemBlock, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT ordinal, pack_remote_path, pack_sha256, pack_size_bytes, payload_offset, payload_length, block_sha256, plaintext_size
		FROM restore_item_blocks b JOIN restore_items i ON i.id = b.restore_item_id
		WHERE b.restore_item_id = $1 AND i.restore_run_id = $2 ORDER BY ordinal`, itemID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocks := make([]RestoreItemBlock, 0)
	for rows.Next() {
		var block RestoreItemBlock
		if err := rows.Scan(&block.Ordinal, &block.PackPath, &block.PackSHA256, &block.PackSizeBytes, &block.PayloadOffset, &block.PayloadLength, &block.BlockSHA256, &block.PlaintextSize); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

func CompleteRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID string, sizeBytes int64, verificationKind string, claimEpoch ...int64) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	epoch := restoreClaimEpoch(claimEpoch)
	result, err := tx.ExecContext(ctx, `UPDATE restore_items SET status = 'COMPLETED', verification_kind = $3, error_code = NULL, claim_deadline = NULL WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, verificationKind, epoch)
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE restore_runs SET processed_files = processed_files + 1, processed_bytes = processed_bytes + $2 WHERE id = $1 AND status IN ('RUNNING','CANCELLING')`, runID, sizeBytes)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func CompleteRestoreDirectoryContext(ctx context.Context, database *sql.DB, itemID, runID string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	result, err := database.ExecContext(ctx, `UPDATE restore_items SET status = 'COMPLETED', error_code = NULL, claim_deadline = NULL WHERE id = $1 AND restore_run_id = $2 AND is_dir AND status = 'RUNNING' AND ($3 = 0 OR claim_epoch = $3)`, itemID, runID, epoch)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("restore directory changed while completing")
	}
	return nil
}

func WarnRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID, errorCode string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET status = 'WARNING', error_code = $3 WHERE id = $1 AND restore_run_id = $2 AND status = 'COMPLETED' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, errorCode, epoch)
	return err
}

func FailRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID, errorCode string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET status = 'FAILED', error_code = $3, claim_deadline = NULL WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, errorCode, epoch)
	return err
}

// RetryRestoreItemContext applies the restore retry contract atomically. The
// stored attempt number is incremented for every failed claim. The first three
// failures are rescheduled after 10s, 30s, and 90s; the fourth is terminal.
func RetryRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID, errorCode string, claimEpoch ...int64) (bool, error) {
	epoch := restoreClaimEpoch(claimEpoch)
	result, err := database.ExecContext(ctx, `
		UPDATE restore_items
		SET attempts = attempts + 1,
			status = CASE WHEN attempts + 1 >= 4 THEN 'FAILED' ELSE 'PENDING' END,
			next_retry_at = CASE WHEN attempts + 1 >= 4 THEN NULL ELSE CURRENT_TIMESTAMP + (ARRAY[INTERVAL '10 seconds', INTERVAL '30 seconds', INTERVAL '90 seconds'])[attempts + 1] END,
			error_code = $3, worker_hash = NULL, claim_deadline = NULL
		WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, errorCode, epoch)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, err
	}
	return true, nil
}

func SkipRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID, errorCode string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET status = 'SKIPPED', error_code = $3, claim_deadline = NULL WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, errorCode, epoch)
	return err
}

func SetRestoreItemTargetPathContext(ctx context.Context, database *sql.DB, itemID, runID, targetPath string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET target_path = $3, resolved_target_path = $3 WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, targetPath, epoch)
	return err
}

func SetRestoreItemResolvedTargetPathContext(ctx context.Context, database *sql.DB, itemID, runID, resolvedTargetPath string, claimEpoch ...int64) error {
	epoch := restoreClaimEpoch(claimEpoch)
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET resolved_target_path = $3 WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND ($4 = 0 OR claim_epoch = $4)`, itemID, runID, resolvedTargetPath, epoch)
	return err
}

// ReserveRestoreTargetPathContext is a durable in-run reservation. Paths are
// canonicalized conservatively to lower case, which prevents two concurrent
// workers from selecting aliases on case-insensitive targets. A retry by the
// same item reuses its reservation.
func ReserveRestoreTargetPathContext(ctx context.Context, database *sql.DB, runID, itemID, targetPath string) (bool, error) {
	canonical := strings.ToLower(strings.TrimPrefix(path.Clean(targetPath), "/"))
	if canonical == "." || canonical == "" {
		canonical = "/"
	}
	var reservedID string
	err := database.QueryRowContext(ctx, `
		INSERT INTO restore_path_reservations (restore_run_id, canonical_path, restore_item_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (restore_run_id, canonical_path) DO UPDATE SET canonical_path = EXCLUDED.canonical_path
		WHERE restore_path_reservations.restore_item_id = EXCLUDED.restore_item_id
		RETURNING restore_item_id`, runID, canonical, itemID).Scan(&reservedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return reservedID == itemID, nil
}

func MoveRestoreTargetReservationContext(ctx context.Context, database *sql.DB, runID, itemID, targetPath string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM restore_path_reservations WHERE restore_run_id = $1 AND restore_item_id = $2`, runID, itemID); err != nil {
		return false, err
	}
	canonical := strings.ToLower(strings.TrimPrefix(path.Clean(targetPath), "/"))
	if canonical == "." || canonical == "" {
		canonical = "/"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO restore_path_reservations (restore_run_id, canonical_path, restore_item_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, runID, canonical, itemID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return false, err
	}
	return true, tx.Commit()
}

// SetRestoreDirectoryTargetPathContext persists a directory rename and rewrites
// every selected descendant in the same transaction. This makes retry target
// paths stable even after a coordinator crash.
func SetRestoreDirectoryTargetPathContext(ctx context.Context, database *sql.DB, itemID, runID, snapshotPath, targetPath string, claimEpoch ...int64) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	epoch := restoreClaimEpoch(claimEpoch)
	result, err := tx.ExecContext(ctx, `UPDATE restore_items SET target_path = $4, resolved_target_path = $4 WHERE id = $1 AND restore_run_id = $2 AND snapshot_relative_path = $3 AND is_dir AND status = 'RUNNING' AND ($5 = 0 OR claim_epoch = $5)`, itemID, runID, snapshotPath, targetPath, epoch)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errors.New("restore directory changed while renaming")
	}
	_, err = tx.ExecContext(ctx, `UPDATE restore_items SET target_path = $4 || substring(snapshot_relative_path FROM char_length($3) + 1), resolved_target_path = $4 || substring(snapshot_relative_path FROM char_length($3) + 1) WHERE restore_run_id = $1 AND snapshot_relative_path LIKE $3 || '/%'`, runID, snapshotPath, targetPath)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SkipBlockedRestoreItemsContext propagates a skipped or failed directory to
// its descendants so a terminal run cannot be held open by unreachable files.
func SkipBlockedRestoreItemsContext(ctx context.Context, database *sql.DB) (int64, error) {
	result, err := database.ExecContext(ctx, `
		UPDATE restore_items child SET status = 'SKIPPED', error_code = 'RESTORE_PARENT_UNAVAILABLE'
		WHERE child.status = 'PENDING' AND EXISTS (
			SELECT 1 FROM restore_items parent
			WHERE parent.restore_run_id = child.restore_run_id AND parent.is_dir
			AND parent.status IN ('SKIPPED','FAILED','CANCELLED')
			AND child.snapshot_relative_path LIKE parent.snapshot_relative_path || '/%'
		)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func HeartbeatRestoreItemContext(ctx context.Context, database *sql.DB, itemID, runID, workerID string, claimEpoch int64) error {
	_, err := database.ExecContext(ctx, `UPDATE restore_items SET updated_at = CURRENT_TIMESTAMP, claim_deadline = CURRENT_TIMESTAMP + INTERVAL '2 minutes' WHERE id = $1 AND restore_run_id = $2 AND status = 'RUNNING' AND worker_hash = $3 AND claim_epoch = $4`, itemID, runID, nullableStringValue(workerID), claimEpoch)
	return err
}

// UpdateRestoreRunOAuthTokens conditionally stores a refreshed target token
// snapshot. The encrypted refresh-token compare is the durable CAS fence: a
// stale worker adopts the winner instead of overwriting a newer rotation.
func UpdateRestoreRunOAuthTokens(ctx context.Context, database *sql.DB, runID, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error {
	if expectedRefreshEncrypted == "" {
		return ErrOAuthTokenConflict
	}
	result, err := database.ExecContext(ctx, `
		UPDATE restore_runs
		SET target_password_encrypted = $2, target_refresh_token_encrypted = $3, target_token_expires_at = $4
		WHERE id = $1 AND target_refresh_token_encrypted = $5
			AND status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING')`, runID, accessEncrypted, refreshEncrypted, expiresAt, expectedRefreshEncrypted)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrOAuthTokenConflict
	}
	return nil
}

func GetRestoreRunCredentialSnapshotContext(ctx context.Context, database *sql.DB, runID string) (accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime, err error) {
	err = database.QueryRowContext(ctx, `SELECT target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at FROM restore_runs WHERE id = $1`, runID).Scan(&accessEncrypted, &refreshEncrypted, &expiresAt)
	return
}

// UpdateRestoreRunMegaSessionContext stores a newly issued MEGA session only
// while the same run remains active. It deliberately never writes sessions to
// the durable restore job, because a later retry must be previewed again.
func UpdateRestoreRunMegaSessionContext(ctx context.Context, database *sql.DB, runID, sessionIDEncrypted, masterKeyEncrypted string) error {
	_, err := database.ExecContext(ctx, `
		UPDATE restore_runs SET target_mega_session_id_encrypted = $2, target_mega_master_key_encrypted = $3
		WHERE id = $1 AND status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING')`, runID, nullableStringValue(sessionIDEncrypted), nullableStringValue(masterKeyEncrypted))
	return err
}

func nullableStringValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// RecoverStaleRestoreItemsContext only reclaims claims whose worker heartbeat
// has been absent for ten minutes. CANCELLING runs drain to CANCELLED instead
// of re-queuing target mutations after a worker crash.
func RecoverStaleRestoreItemsContext(ctx context.Context, database *sql.DB) (int64, error) {
	result, err := database.ExecContext(ctx, `
		UPDATE restore_items i
		SET status = CASE WHEN r.status = 'CANCELLING' THEN 'CANCELLED' ELSE 'PENDING' END,
			next_retry_at = NULL,
			error_code = CASE WHEN r.status = 'CANCELLING' THEN 'RESTORE_CANCELLED' ELSE 'RESTORE_WORKER_RECOVERED' END,
			worker_hash = NULL,
			claim_deadline = NULL,
			updated_at = CURRENT_TIMESTAMP
		FROM restore_runs r
		WHERE r.id = i.restore_run_id AND i.status = 'RUNNING'
		AND r.status IN ('RUNNING', 'CANCELLING')
		AND (i.claim_deadline < CURRENT_TIMESTAMP OR (i.claim_deadline IS NULL AND i.updated_at < CURRENT_TIMESTAMP - INTERVAL '10 minutes'))`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// FinalizeCompletedRestoreRunsContext closes drained runs and releases their
// pins in the same transaction. A failed/skipped item yields PARTIAL if any
// file succeeded, otherwise FAILED; no target object is rolled back.
func FinalizeCompletedRestoreRunsContext(ctx context.Context, database *sql.DB) (int, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id, j.user_id FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.status = 'RUNNING' AND NOT EXISTS (SELECT 1 FROM restore_items i WHERE i.restore_run_id = r.id AND i.status IN ('PENDING','RUNNING')) FOR UPDATE OF r SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type summary struct {
		id, userID           string
		completed, imperfect int
		bytes                int64
		status               string
	}
	var summaries []summary
	for rows.Next() {
		var value summary
		if err := rows.Scan(&value.id, &value.userID); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE status IN ('COMPLETED','WARNING')), COUNT(*) FILTER (WHERE status IN ('FAILED','SKIPPED','WARNING','CANCELLED')), COALESCE(SUM(size_bytes) FILTER (WHERE status IN ('COMPLETED','WARNING')), 0) FROM restore_items WHERE restore_run_id = $1`, value.id).Scan(&value.completed, &value.imperfect, &value.bytes); err != nil {
			return 0, err
		}
		summaries = append(summaries, value)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, value := range summaries {
		status := "COMPLETED"
		if value.imperfect > 0 && value.completed > 0 {
			status = "PARTIAL"
		} else if value.imperfect > 0 || value.completed == 0 {
			status = "FAILED"
		}
		value.status = status
		if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET status = $2, processed_files = $3, processed_bytes = $4, failed_files = $5, finished_at = CURRENT_TIMESTAMP WHERE id = $1 AND status = 'RUNNING'`, value.id, status, value.completed, value.bytes, value.imperfect); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM restore_pack_pins WHERE restore_run_id = $1`, value.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, target_token_expires_at = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE id = $1 AND status IN ('COMPLETED','PARTIAL','FAILED')`, value.id); err != nil {
			return 0, err
		}
		if err := createRestoreNotificationEventTx(tx, value.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, value := range summaries {
		action := AuditRestoreCompleted
		if value.status == "PARTIAL" { action = AuditRestorePartial }
		if value.status == "FAILED" { action = AuditRestoreFailed }
		WriteAuditLog(database, AuditEntry{UserID: sql.NullString{String: value.userID, Valid: true}, Action: action, Target: value.id})
	}
	return len(summaries), nil
}

// FinalizeCancellingRestoreRunsContext releases pins only after the final live
// item drains. Pending work was marked CANCELLED by the cancellation request.
func FinalizeCancellingRestoreRunsContext(ctx context.Context, database *sql.DB) (int, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id, j.user_id FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.status = 'CANCELLING' AND NOT EXISTS (SELECT 1 FROM restore_items i WHERE i.restore_run_id = r.id AND i.status = 'RUNNING') FOR UPDATE OF r SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type cancelledRun struct { id, userID string }
	var ids []cancelledRun
	for rows.Next() {
		var value cancelledRun
		if err := rows.Scan(&value.id, &value.userID); err != nil {
			return 0, err
		}
		ids = append(ids, value)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, value := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE id = $1 AND status = 'CANCELLING'`, value.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM restore_pack_pins WHERE restore_run_id = $1`, value.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, target_token_expires_at = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE id = $1 AND status = 'CANCELLED'`, value.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, value := range ids {
		WriteAuditLog(database, AuditEntry{UserID: sql.NullString{String: value.userID, Valid: true}, Action: AuditRestoreCancelled, Target: value.id})
	}
	return len(ids), nil
}

func ListRestoreRunsForOwnerContext(ctx context.Context, database *sql.DB, userID string) ([]RestoreRun, error) {
	rows, err := database.QueryContext(ctx, `SELECT r.id, r.restore_job_id, r.generation, r.status, r.total_files, r.total_bytes, r.processed_files, r.processed_bytes, r.failed_files, r.error_code, r.started_at, r.finished_at, r.created_at, r.updated_at FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE j.user_id = $1 ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := make([]RestoreRun, 0)
	for rows.Next() {
		var run RestoreRun
		if err := scanRestoreRun(rows, &run); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func GetRestoreRunForOwnerContext(ctx context.Context, database *sql.DB, runID, userID string) (*RestoreRun, error) {
	run := &RestoreRun{}
	err := database.QueryRowContext(ctx, `SELECT r.id, r.restore_job_id, r.generation, r.status, r.total_files, r.total_bytes, r.processed_files, r.processed_bytes, r.failed_files, r.error_code, r.started_at, r.finished_at, r.created_at, r.updated_at FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.id = $1 AND j.user_id = $2`, runID, userID).Scan(restoreRunDestinations(run)...)
	if err != nil {
		return nil, err
	}
	return run, nil
}

func ListRestoreItemsForRunOwnerContext(ctx context.Context, database *sql.DB, runID, userID string) ([]RestoreItem, error) {
	rows, err := database.QueryContext(ctx, `SELECT i.id, i.restore_run_id, i.parent_item_id, i.snapshot_relative_path, i.is_dir, i.size_bytes, i.file_sha256, i.source_mtime, i.source_metadata, i.target_path, i.resolved_target_path, i.status, i.verification_kind, i.outcome_code, i.attempts, i.next_retry_at, i.error_code FROM restore_items i JOIN restore_runs r ON r.id = i.restore_run_id JOIN restore_jobs j ON j.id = r.restore_job_id WHERE i.restore_run_id = $1 AND j.user_id = $2 ORDER BY i.snapshot_relative_path`, runID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RestoreItem, 0)
	for rows.Next() {
		var item RestoreItem
		if err := rows.Scan(&item.ID, &item.RestoreRunID, &item.ParentItemID, &item.SnapshotRelativePath, &item.IsDir, &item.SizeBytes, &item.FileSHA256, &item.SourceMTime, &item.SourceMetadata, &item.TargetPath, &item.ResolvedTargetPath, &item.Status, &item.VerificationKind, &item.OutcomeCode, &item.Attempts, &item.NextRetryAt, &item.ErrorCode); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func ListRestoreItemsForOwnerContext(ctx context.Context, database *sql.DB, runID, userID string) ([]RestoreItem, error) {
	return ListRestoreItemsForRunOwnerContext(ctx, database, runID, userID)
}

func CancelRestoreRunForOwnerContext(ctx context.Context, database *sql.DB, runID, userID string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE restore_runs r SET status = 'CANCELLING' FROM restore_jobs j WHERE r.restore_job_id = j.id AND r.id = $1 AND j.user_id = $2 AND r.status IN ('QUEUED','PLANNING','RUNNING')`, runID, userID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE restore_items SET status = 'CANCELLED', error_code = 'RESTORE_CANCELLED' WHERE restore_run_id = $1 AND status = 'PENDING'`, runID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP, target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, target_token_expires_at = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE id = $1 AND status = 'CANCELLING' AND NOT EXISTS (SELECT 1 FROM restore_items WHERE restore_run_id = $1 AND status = 'RUNNING')`, runID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM restore_pack_pins WHERE restore_run_id = $1 AND EXISTS (SELECT 1 FROM restore_runs WHERE id = $1 AND status = 'CANCELLED')`, runID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteRestoreJobForOwnerContext retains active evidence until a run has
// drained. Cascading deletion then removes run items, frozen recipes and any
// already-terminal pin rows together.
func DeleteRestoreJobForOwnerContext(ctx context.Context, database *sql.DB, jobID, userID string) (bool, error) {
	result, err := database.ExecContext(ctx, `
		DELETE FROM restore_jobs j
		WHERE j.id = $1 AND j.user_id = $2
		AND NOT EXISTS (SELECT 1 FROM restore_runs r WHERE r.restore_job_id = j.id AND r.status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'))`, jobID, userID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

type restoreRunScanner interface{ Scan(...any) error }

func scanRestoreRun(row restoreRunScanner, run *RestoreRun) error {
	return row.Scan(restoreRunDestinations(run)...)
}

func restoreRunDestinations(run *RestoreRun) []any {
	return []any{&run.ID, &run.RestoreJobID, &run.Generation, &run.Status, &run.TotalFiles, &run.TotalBytes, &run.ProcessedFiles, &run.ProcessedBytes, &run.FailedFiles, &run.ErrorCode, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt}
}

// ClaimNextQueuedRestoreRunContext reserves a run for planning. Actual content
// workers only see items after PlanRestoreRunContext commits their frozen block
// recipes and pack pins.
func ClaimNextQueuedRestoreRunContext(ctx context.Context, database *sql.DB) (*RestoreJob, *RestoreRun, error) {
	return ClaimNextQueuedRestoreRunForWorkerContext(ctx, database, "")
}

func ClaimNextQueuedRestoreRunForWorkerContext(ctx context.Context, database *sql.DB, workerID string) (*RestoreJob, *RestoreRun, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin restore run claim: %w", err)
	}
	defer tx.Rollback()
	job := &RestoreJob{}
	run := &RestoreRun{}
	err = tx.QueryRowContext(ctx, `
		SELECT j.id, j.user_id, j.backup_job_id, j.backup_snapshot_id, j.target_profile_id, j.selected_paths, j.target_provider, j.target_root, j.conflict_strategy, j.threads, j.bandwidth_mbps, j.created_at, j.updated_at,
			r.id, r.restore_job_id, r.generation, r.status, r.total_files, r.total_bytes, r.processed_files, r.processed_bytes, r.failed_files, r.error_code, r.started_at, r.finished_at, r.created_at, r.updated_at
		FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id
		WHERE r.status = 'QUEUED'
		ORDER BY r.created_at FOR UPDATE OF r SKIP LOCKED LIMIT 1`).Scan(
		&job.ID, &job.UserID, &job.BackupJobID, &job.BackupSnapshotID, &job.TargetProfileID, &job.SelectedPaths, &job.TargetProvider, &job.TargetRoot, &job.ConflictStrategy, &job.Threads, &job.BandwidthMbps, &job.CreatedAt, &job.UpdatedAt,
		&run.ID, &run.RestoreJobID, &run.Generation, &run.Status, &run.TotalFiles, &run.TotalBytes, &run.ProcessedFiles, &run.ProcessedBytes, &run.FailedFiles, &run.ErrorCode, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("select queued restore run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE restore_runs SET status = 'PLANNING', started_at = CURRENT_TIMESTAMP, coordinator_generation = coordinator_generation + 1, coordinator_lease_until = CURRENT_TIMESTAMP + INTERVAL '2 minutes', worker_hash = $2 WHERE id = $1 AND status = 'QUEUED'`, run.ID, nullableStringValue(workerID))
	if err != nil {
		return nil, nil, fmt.Errorf("claim restore run: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, nil, errors.New("restore run changed while claiming")
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit restore run claim: %w", err)
	}
	run.Status = "PLANNING"
	run.CoordinatorGeneration++
	run.WorkerHash = sql.NullString{String: workerID, Valid: workerID != ""}
	return job, run, nil
}

// PlanRestoreRunContext freezes all source block locators while the source
// catalog is locked. Retention and compaction may run after this commit, but
// cannot remove any pinned pack until the restore run terminalizes.
func PlanRestoreRunContext(ctx context.Context, database *sql.DB, job *RestoreJob, run *RestoreRun) (bool, error) {
	if job == nil || run == nil || job.ID == "" || run.ID == "" || !job.BackupJobID.Valid || !job.BackupSnapshotID.Valid {
		return false, errors.New("restore job/run is incomplete")
	}
	selectedPaths, err := json.Marshal(job.SelectedPaths)
	if err != nil {
		return false, fmt.Errorf("encode restore selection: %w", err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin restore planning: %w", err)
	}
	defer tx.Rollback()

	var lockedSnapshotID string
	err = tx.QueryRowContext(ctx, `
		SELECT s.id FROM backup_snapshots s JOIN backup_jobs b ON b.id = s.backup_job_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND s.state IN ('READY','PARTIAL')
		AND s.integrity_state <> 'DAMAGED' AND b.deletion_state = 'ACTIVE' FOR UPDATE OF s, b`, job.BackupSnapshotID.String, job.BackupJobID.String).Scan(&lockedSnapshotID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("restore snapshot is no longer restorable")
	}
	if err != nil {
		return false, fmt.Errorf("lock restore snapshot: %w", err)
	}
	var invalidRecipe bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM backup_snapshot_items i
			WHERE i.backup_snapshot_id = $1 AND NOT i.is_dir AND i.state = 'AVAILABLE' AND i.size_bytes > 0
			AND EXISTS (SELECT 1 FROM jsonb_array_elements_text($2::jsonb) AS selected(path) WHERE selected.path = '' OR i.relative_path = selected.path OR i.relative_path LIKE selected.path || '/%')
			AND (
				NOT EXISTS (SELECT 1 FROM backup_snapshot_item_blocks sib WHERE sib.backup_snapshot_item_id = i.id)
				OR EXISTS (
					SELECT 1 FROM backup_snapshot_item_blocks sib JOIN backup_blocks b ON b.id = sib.backup_block_id JOIN backup_packs p ON p.id = b.backup_pack_id
					WHERE sib.backup_snapshot_item_id = i.id AND (p.backup_job_id <> $3 OR p.state <> 'READY' OR b.payload_offset + b.payload_length > p.size_bytes)
				)
			)
		)`, job.BackupSnapshotID.String, string(selectedPaths), job.BackupJobID.String).Scan(&invalidRecipe)
	if err != nil {
		return false, fmt.Errorf("validate restore block recipes: %w", err)
	}
	if invalidRecipe {
		return false, errors.New("restore snapshot has unavailable block recipes")
	}

	// Snapshot-relative names are appended only after validation at preview
	// creation. Keeping the expression in SQL makes the item+recipe snapshot a
	// single set-based transaction rather than a Go loop under catalog locks.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO restore_items (restore_run_id, snapshot_relative_path, is_dir, size_bytes, file_sha256, source_mtime, source_metadata, target_path, resolved_target_path, status, error_code)
		SELECT $1, i.relative_path, i.is_dir, i.size_bytes, i.file_sha256, i.mtime, i.metadata,
			CASE WHEN $2 = '/' THEN '/' || i.relative_path ELSE $2 || '/' || i.relative_path END,
			CASE WHEN $2 = '/' THEN '/' || i.relative_path ELSE $2 || '/' || i.relative_path END,
			CASE WHEN i.is_dir THEN 'PENDING' WHEN i.state = 'AVAILABLE' THEN 'PENDING' ELSE 'SKIPPED' END,
			CASE WHEN i.is_dir OR i.state = 'AVAILABLE' THEN NULL ELSE COALESCE(i.error_code, 'RESTORE_SOURCE_UNAVAILABLE') END
		FROM backup_snapshot_items i
		WHERE i.backup_snapshot_id = $3
		AND EXISTS (SELECT 1 FROM jsonb_array_elements_text($4::jsonb) AS selected(path) WHERE selected.path = '' OR i.relative_path = selected.path OR i.relative_path LIKE selected.path || '/%')`,
		run.ID, job.TargetRoot, job.BackupSnapshotID.String, string(selectedPaths))
	if err != nil {
		return false, fmt.Errorf("create restore items: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE restore_items child
		SET parent_item_id = parent.id
		FROM restore_items parent
		WHERE child.restore_run_id = $1 AND parent.restore_run_id = $1 AND parent.is_dir
		  AND position('/' in child.snapshot_relative_path) > 0
		  AND parent.snapshot_relative_path = regexp_replace(child.snapshot_relative_path, '/[^/]+$', '')`,
		run.ID); err != nil {
		return false, fmt.Errorf("link restore item directory hierarchy: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO restore_item_blocks (restore_item_id, ordinal, pack_remote_path, pack_sha256, pack_size_bytes, payload_offset, payload_length, block_sha256, plaintext_size)
		SELECT ri.id, sib.ordinal, p.remote_rel_path, p.sha256, p.size_bytes, b.payload_offset, b.payload_length, b.sha256, b.plaintext_size
		FROM restore_items ri
		JOIN backup_snapshot_items si ON si.relative_path = ri.snapshot_relative_path AND si.backup_snapshot_id = $1
		JOIN backup_snapshot_item_blocks sib ON sib.backup_snapshot_item_id = si.id
		JOIN backup_blocks b ON b.id = sib.backup_block_id AND b.backup_job_id = $2
		JOIN backup_packs p ON p.id = b.backup_pack_id AND p.backup_job_id = b.backup_job_id
		WHERE ri.restore_run_id = $3 AND ri.status = 'PENDING' AND NOT ri.is_dir AND p.state = 'READY'`,
		job.BackupSnapshotID.String, job.BackupJobID.String, run.ID)
	if err != nil {
		return false, fmt.Errorf("freeze restore block locators: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO restore_pack_pins (restore_run_id, backup_pack_id)
		SELECT DISTINCT $1, b.backup_pack_id
		FROM restore_items ri
		JOIN backup_snapshot_items si ON si.relative_path = ri.snapshot_relative_path AND si.backup_snapshot_id = $2
		JOIN backup_snapshot_item_blocks sib ON sib.backup_snapshot_item_id = si.id
		JOIN backup_blocks b ON b.id = sib.backup_block_id AND b.backup_job_id = $3
		WHERE ri.restore_run_id = $1 AND ri.status = 'PENDING' AND NOT ri.is_dir`,
		run.ID, job.BackupSnapshotID.String, job.BackupJobID.String)
	if err != nil {
		return false, fmt.Errorf("pin restore packs: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE restore_runs SET status = 'RUNNING', total_files = (SELECT COUNT(*) FROM restore_items WHERE restore_run_id = $1 AND NOT is_dir), total_bytes = (SELECT COALESCE(SUM(size_bytes), 0) FROM restore_items WHERE restore_run_id = $1 AND NOT is_dir), coordinator_lease_until = NULL, worker_hash = NULL
		WHERE id = $1 AND restore_job_id = $2 AND generation = $3 AND status = 'PLANNING'`, run.ID, job.ID, run.Generation)
	if err != nil {
		return false, fmt.Errorf("activate planned restore run: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return false, errors.New("restore run changed during planning")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit restore planning: %w", err)
	}
	return true, nil
}

// FailRestoreRunPlanningContext terminalizes a run that could not freeze a
// restorable source catalog. No pack pins exist until planning commits.
func FailRestoreRunPlanningContext(ctx context.Context, database *sql.DB, jobID, runID string, generation int, errorCode string) (bool, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var userID string
	result, err := tx.ExecContext(ctx, `
		UPDATE restore_runs SET status = 'FAILED', error_code = $4, finished_at = CURRENT_TIMESTAMP,
			target_password_encrypted = NULL, target_refresh_token_encrypted = NULL,
			target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL,
			target_token_expires_at = NULL, coordinator_lease_until = NULL, worker_hash = NULL
		WHERE id = $1 AND restore_job_id = $2 AND generation = $3 AND status = 'PLANNING'`, runID, jobID, generation, errorCode)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM restore_jobs WHERE id = $1`, jobID).Scan(&userID); err != nil {
		return false, err
	}
	if err = createRestoreNotificationEventTx(tx, runID); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	WriteAuditLog(database, AuditEntry{UserID: sql.NullString{String: userID, Valid: true}, Action: AuditRestoreFailed, Target: runID})
	return true, nil
}

// CreateRestorePreviewContext records a pending preview only after the API has
// verified ownership and provider capabilities. The worker owns all target-tree
// enumeration and marks it READY later.
func CreateRestorePreviewContext(ctx context.Context, database *sql.DB, preview *RestorePreview) (string, error) {
	if preview == nil || preview.UserID == "" || preview.BackupJobID == "" || preview.BackupSnapshotID == "" || preview.TargetProvider == "" || preview.TargetRoot == "" {
		return "", errors.New("restore preview is incomplete")
	}
	if len(preview.ConfigFingerprint) == 0 {
		profileID := ""
		if preview.TargetProfileID.Valid {
			profileID = preview.TargetProfileID.String
		}
		identity := preview.TargetConnectionIdentity
		if identity == "" {
			identity = "profile:" + profileID
		}
		fingerprint, err := RestoreConfigFingerprintWithIdentity(preview.BackupSnapshotID, preview.SelectedPaths, preview.TargetProvider, preview.TargetRoot, identity, preview.ConflictStrategy)
		if err != nil {
			return "", fmt.Errorf("fingerprint restore preview: %w", err)
		}
		preview.ConfigFingerprint = fingerprint[:]
	}
	var id string
	err := database.QueryRowContext(ctx, `
		INSERT INTO restore_previews (user_id, backup_job_id, backup_snapshot_id, retry_restore_job_id, target_profile_id, selected_paths, target_provider, target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING id`, preview.UserID, preview.BackupJobID, preview.BackupSnapshotID, preview.RetryRestoreJobID, preview.TargetProfileID, preview.SelectedPaths, preview.TargetProvider, preview.TargetURL, preview.TargetUsername, preview.TargetPasswordEncrypted, preview.TargetRefreshTokenEncrypted, preview.TargetTokenExpiresAt, preview.TargetMegaSessionIDEncrypted, preview.TargetMegaMasterKeyEncrypted, preview.TargetConnectionIdentity, preview.TargetRoot, preview.ConflictStrategy, preview.Threads, preview.BandwidthMbps, preview.ConfigFingerprint).Scan(&id)
	return id, err
}

// ClaimNextRestorePreviewContext assigns one queued preview to a worker. The
// transition itself is the fence; another worker cannot observe it as QUEUED.
func ClaimNextRestorePreviewContext(ctx context.Context, database *sql.DB) (*RestorePreview, error) {
	return ClaimNextRestorePreviewForWorkerContext(ctx, database, "")
}

func ClaimNextRestorePreviewForWorkerContext(ctx context.Context, database *sql.DB, workerID string) (*RestorePreview, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin restore preview claim: %w", err)
	}
	defer tx.Rollback()
	preview := &RestorePreview{}
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, backup_job_id, backup_snapshot_id, retry_restore_job_id, target_profile_id, selected_paths, target_provider, target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint, status, total_files, total_directories, total_bytes, existing_file_conflicts, mergeable_directories, type_conflicts, unavailable_items, expected_skips, expected_renames, metadata_warnings, conflict_examples, error_code, coordinator_generation, coordinator_lease_until, worker_hash, ready_at, expires_at, created_at, updated_at
		FROM restore_previews WHERE status = 'QUEUED' ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(
		&preview.ID, &preview.UserID, &preview.BackupJobID, &preview.BackupSnapshotID, &preview.RetryRestoreJobID, &preview.TargetProfileID, &preview.SelectedPaths, &preview.TargetProvider, &preview.TargetURL, &preview.TargetUsername, &preview.TargetPasswordEncrypted, &preview.TargetRefreshTokenEncrypted, &preview.TargetTokenExpiresAt, &preview.TargetMegaSessionIDEncrypted, &preview.TargetMegaMasterKeyEncrypted, &preview.TargetConnectionIdentity, &preview.TargetRoot, &preview.ConflictStrategy, &preview.Threads, &preview.BandwidthMbps, &preview.ConfigFingerprint, &preview.Status, &preview.TotalFiles, &preview.TotalDirectories, &preview.TotalBytes, &preview.ExistingFileConflicts, &preview.MergeableDirectories, &preview.TypeConflicts, &preview.UnavailableItems, &preview.ExpectedSkips, &preview.ExpectedRenames, &preview.MetadataWarnings, &preview.ConflictExamples, &preview.ErrorCode, &preview.CoordinatorGeneration, &preview.CoordinatorLeaseUntil, &preview.WorkerHash, &preview.ReadyAt, &preview.ExpiresAt, &preview.CreatedAt, &preview.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select restore preview: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE restore_previews SET status = 'RUNNING', coordinator_generation = coordinator_generation + 1, coordinator_lease_until = CURRENT_TIMESTAMP + INTERVAL '2 minutes', worker_hash = $2 WHERE id = $1 AND status = 'QUEUED'`, preview.ID, nullableStringValue(workerID))
	if err != nil {
		return nil, fmt.Errorf("claim restore preview: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, fmt.Errorf("restore preview changed while claiming")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit restore preview claim: %w", err)
	}
	preview.Status = "RUNNING"
	preview.CoordinatorGeneration++
	preview.WorkerHash = sql.NullString{String: workerID, Valid: workerID != ""}
	return preview, nil
}

// CompleteRestorePreviewContext records advisory counts and assigns a fixed
// expiry. The snapshot remains rechecked atomically when it is consumed.
func CompleteRestorePreviewContext(ctx context.Context, database *sql.DB, previewID string, files int, bytes int64) (bool, error) {
	return CompleteRestorePreviewWithStatsContext(ctx, database, previewID, RestorePreviewStats{Files: files, Bytes: bytes})
}

func CompleteRestorePreviewWithStatsContext(ctx context.Context, database *sql.DB, previewID string, stats RestorePreviewStats) (bool, error) {
	return CompleteRestorePreviewWithStatsFencedContext(ctx, database, previewID, 0, "", stats)
}

func CompleteRestorePreviewWithStatsFencedContext(ctx context.Context, database *sql.DB, previewID string, generation int, workerID string, stats RestorePreviewStats) (bool, error) {
	if len(stats.ConflictExamples) == 0 {
		stats.ConflictExamples = json.RawMessage("[]")
	}
	result, err := database.ExecContext(ctx, `
		UPDATE restore_previews SET status = 'READY', total_files = $2, total_directories = $3, total_bytes = $4,
			existing_file_conflicts = $5, mergeable_directories = $6, type_conflicts = $7, unavailable_items = $8,
			expected_skips = $9, expected_renames = $10, metadata_warnings = $11, conflict_examples = $12,
			ready_at = CURRENT_TIMESTAMP, expires_at = CURRENT_TIMESTAMP + $13::interval, error_code = NULL,
			coordinator_lease_until = NULL, worker_hash = NULL
		WHERE id = $1 AND status = 'RUNNING' AND ($14 = 0 OR (coordinator_generation = $14 AND worker_hash IS NOT DISTINCT FROM $15))`, previewID, stats.Files, stats.Directories, stats.Bytes,
		stats.ExistingFiles, stats.MergeableDirectories, stats.TypeConflicts, stats.UnavailableItems,
		stats.ExpectedSkips, stats.ExpectedRenames, stats.MetadataWarnings, stats.ConflictExamples,
		fmt.Sprintf("%d seconds", int(restorePreviewLifetime.Seconds())), generation, nullableStringValue(workerID))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func FailRestorePreviewContext(ctx context.Context, database *sql.DB, previewID, errorCode string) error {
	return FailRestorePreviewFencedContext(ctx, database, previewID, 0, "", errorCode)
}

func FailRestorePreviewFencedContext(ctx context.Context, database *sql.DB, previewID string, generation int, workerID, errorCode string) error {
	_, err := database.ExecContext(ctx, `UPDATE restore_previews SET status = 'FAILED', error_code = $2, target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE id = $1 AND status = 'RUNNING' AND ($3 = 0 OR (coordinator_generation = $3 AND worker_hash IS NOT DISTINCT FROM $4))`, previewID, errorCode, generation, nullableStringValue(workerID))
	return err
}

func RenewRestorePreviewLeaseContext(ctx context.Context, database *sql.DB, previewID string, generation int, workerID string) (bool, error) {
	result, err := database.ExecContext(ctx, `UPDATE restore_previews SET coordinator_lease_until = CURRENT_TIMESTAMP + INTERVAL '2 minutes' WHERE id = $1 AND status = 'RUNNING' AND coordinator_generation = $2 AND worker_hash IS NOT DISTINCT FROM $3`, previewID, generation, nullableStringValue(workerID))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func CancelRestorePreviewForOwnerContext(ctx context.Context, database *sql.DB, previewID, userID string) (bool, error) {
	result, err := database.ExecContext(ctx, `UPDATE restore_previews SET status = 'CANCELLED', error_code = 'RESTORE_PREVIEW_CANCELLED', target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE id = $1 AND user_id = $2 AND status IN ('QUEUED', 'RUNNING', 'READY')`, previewID, userID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

// CountRestorePreviewSelectionContext counts only stable files selected from a
// visible snapshot. A selected path includes the file/directory itself and its
// descendants; callers have already normalized and de-overlapped the paths.
func CountRestorePreviewSelectionContext(ctx context.Context, database *sql.DB, preview *RestorePreview) (int, int64, error) {
	if preview == nil || len(preview.SelectedPaths) == 0 {
		return 0, 0, errors.New("restore selection is empty")
	}
	selectedPaths, err := json.Marshal(preview.SelectedPaths)
	if err != nil {
		return 0, 0, fmt.Errorf("encode restore selection: %w", err)
	}
	var files int
	var bytes int64
	err = database.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(i.size_bytes), 0)
		FROM backup_snapshot_items i
		JOIN backup_snapshots s ON s.id = i.backup_snapshot_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND s.state IN ('READY', 'PARTIAL')
			AND s.integrity_state <> 'DAMAGED' AND NOT i.is_dir AND i.state = 'AVAILABLE'
			AND EXISTS (
				SELECT 1 FROM jsonb_array_elements_text($3::jsonb) AS selected(path)
				WHERE selected.path = '' OR i.relative_path = selected.path OR i.relative_path LIKE selected.path || '/%'
			)`, preview.BackupSnapshotID, preview.BackupJobID, string(selectedPaths)).Scan(&files, &bytes)
	if err != nil {
		return 0, 0, err
	}
	return files, bytes, nil
}

// ExpireRestorePreviewsContext clears previews which can no longer be
// consumed. It is safe to run periodically from every worker.
func ExpireRestorePreviewsContext(ctx context.Context, database *sql.DB) error {
	_, err := database.ExecContext(ctx, `UPDATE restore_previews SET status = 'EXPIRED', target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE status = 'READY' AND expires_at <= CURRENT_TIMESTAMP`)
	return err
}

// RecoverStaleRestorePlanningContext only recovers phases that perform no
// target mutation. Transfer items remain owned until explicit cancellation or
// future lease fencing is available, avoiding duplicate remote writes.
func RecoverStaleRestorePlanningContext(ctx context.Context, database *sql.DB) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE restore_previews SET status = 'QUEUED', error_code = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE status = 'RUNNING' AND (coordinator_lease_until < CURRENT_TIMESTAMP OR (coordinator_lease_until IS NULL AND updated_at < CURRENT_TIMESTAMP - INTERVAL '5 minutes'))`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE restore_runs SET status = 'QUEUED', error_code = NULL, coordinator_lease_until = NULL, worker_hash = NULL WHERE status = 'PLANNING' AND (coordinator_lease_until < CURRENT_TIMESTAMP OR (coordinator_lease_until IS NULL AND updated_at < CURRENT_TIMESTAMP - INTERVAL '5 minutes'))`); err != nil {
		return err
	}
	return tx.Commit()
}

func GetRestorePreviewForOwnerContext(ctx context.Context, database *sql.DB, previewID, userID string) (*RestorePreview, error) {
	preview := &RestorePreview{}
	err := database.QueryRowContext(ctx, `SELECT id, user_id, backup_job_id, backup_snapshot_id, retry_restore_job_id, target_profile_id, selected_paths, target_provider, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint, status, total_files, total_directories, total_bytes, existing_file_conflicts, mergeable_directories, type_conflicts, unavailable_items, expected_skips, expected_renames, metadata_warnings, conflict_examples, error_code, ready_at, expires_at, created_at, updated_at FROM restore_previews WHERE id = $1 AND user_id = $2`, previewID, userID).Scan(
		&preview.ID, &preview.UserID, &preview.BackupJobID, &preview.BackupSnapshotID, &preview.RetryRestoreJobID, &preview.TargetProfileID, &preview.SelectedPaths, &preview.TargetProvider, &preview.TargetRoot, &preview.ConflictStrategy, &preview.Threads, &preview.BandwidthMbps, &preview.ConfigFingerprint, &preview.Status, &preview.TotalFiles, &preview.TotalDirectories, &preview.TotalBytes, &preview.ExistingFileConflicts, &preview.MergeableDirectories, &preview.TypeConflicts, &preview.UnavailableItems, &preview.ExpectedSkips, &preview.ExpectedRenames, &preview.MetadataWarnings, &preview.ConflictExamples, &preview.ErrorCode, &preview.ReadyAt, &preview.ExpiresAt, &preview.CreatedAt, &preview.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return preview, nil
}

// ConsumeRestorePreviewContext turns an unexpired READY preview into the first
// queued run in one transaction. The live snapshot check intentionally happens
// here as well as during preview generation because retention or damage can
// change the repository between the two operations.
func ConsumeRestorePreviewContext(ctx context.Context, database *sql.DB, previewID, userID string) (*RestoreJob, *RestoreRun, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin restore preview consume: %w", err)
	}
	defer tx.Rollback()

	preview := &RestorePreview{}
	err = tx.QueryRowContext(ctx, `
		SELECT id, user_id, backup_job_id, backup_snapshot_id, retry_restore_job_id, target_profile_id, selected_paths, target_provider, target_url, target_username, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint, status, total_files, total_directories, total_bytes, existing_file_conflicts, mergeable_directories, type_conflicts, unavailable_items, expected_skips, expected_renames, metadata_warnings, conflict_examples, error_code, coordinator_generation, coordinator_lease_until, worker_hash, ready_at, expires_at, created_at, updated_at
		FROM restore_previews WHERE id = $1 AND user_id = $2 FOR UPDATE`, previewID, userID).Scan(
		&preview.ID, &preview.UserID, &preview.BackupJobID, &preview.BackupSnapshotID, &preview.RetryRestoreJobID, &preview.TargetProfileID, &preview.SelectedPaths, &preview.TargetProvider, &preview.TargetURL, &preview.TargetUsername, &preview.TargetPasswordEncrypted, &preview.TargetRefreshTokenEncrypted, &preview.TargetTokenExpiresAt, &preview.TargetMegaSessionIDEncrypted, &preview.TargetMegaMasterKeyEncrypted, &preview.TargetConnectionIdentity, &preview.TargetRoot, &preview.ConflictStrategy, &preview.Threads, &preview.BandwidthMbps, &preview.ConfigFingerprint, &preview.Status, &preview.TotalFiles, &preview.TotalDirectories, &preview.TotalBytes, &preview.ExistingFileConflicts, &preview.MergeableDirectories, &preview.TypeConflicts, &preview.UnavailableItems, &preview.ExpectedSkips, &preview.ExpectedRenames, &preview.MetadataWarnings, &preview.ConflictExamples, &preview.ErrorCode, &preview.CoordinatorGeneration, &preview.CoordinatorLeaseUntil, &preview.WorkerHash, &preview.ReadyAt, &preview.ExpiresAt, &preview.CreatedAt, &preview.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrRestorePreviewNotFound
		}
		return nil, nil, err
	}
	if preview.Status == "EXPIRED" || (preview.ExpiresAt.Valid && !preview.ExpiresAt.Time.After(time.Now())) {
		return nil, nil, ErrRestorePreviewExpired
	}
	if preview.Status != "READY" {
		return nil, nil, ErrRestorePreviewInvalidState
	}

	var restorable bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM backup_snapshots s JOIN backup_jobs j ON j.id = s.backup_job_id
		WHERE s.id = $1 AND s.backup_job_id = $2 AND j.user_id = $3 AND j.deletion_state = 'ACTIVE'
		AND s.state IN ('READY','PARTIAL') AND s.integrity_state <> 'DAMAGED'
	)`, preview.BackupSnapshotID, preview.BackupJobID, userID).Scan(&restorable)
	if err != nil {
		return nil, nil, fmt.Errorf("check restore snapshot: %w", err)
	}
	if !restorable {
		return nil, nil, ErrRestoreSnapshotUnavailable
	}

	job := &RestoreJob{}
	if preview.RetryRestoreJobID.Valid {
		var storedFingerprint []byte
		err = tx.QueryRowContext(ctx, `
			SELECT id, user_id, backup_job_id, backup_snapshot_id, target_profile_id, selected_paths, target_provider, target_url, target_username, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint, created_at, updated_at
			FROM restore_jobs WHERE id = $1 AND user_id = $2 FOR UPDATE`, preview.RetryRestoreJobID.String, userID).Scan(
			&job.ID, &job.UserID, &job.BackupJobID, &job.BackupSnapshotID, &job.TargetProfileID, &job.SelectedPaths, &job.TargetProvider, &job.TargetURL, &job.TargetUsername, &job.TargetConnectionIdentity, &job.TargetRoot, &job.ConflictStrategy, &job.Threads, &job.BandwidthMbps, &storedFingerprint, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, nil, fmt.Errorf("load retry restore job: %w", err)
		}
		if !bytes.Equal(storedFingerprint, preview.ConfigFingerprint) {
			return nil, nil, ErrRestoreRetryMismatch
		}
		var terminal bool
		if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM restore_runs WHERE restore_job_id = $1 AND status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING'))`, job.ID).Scan(&terminal); err != nil || !terminal {
			if err != nil {
				return nil, nil, fmt.Errorf("check retry run state: %w", err)
			}
			return nil, nil, ErrRestoreRetryActive
		}
	} else {
		err = tx.QueryRowContext(ctx, `
			INSERT INTO restore_jobs (user_id, backup_job_id, backup_snapshot_id, source_backup_ref, source_snapshot_ref, target_profile_id, selected_paths, target_provider, target_url, target_username, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, config_fingerprint)
			VALUES ($1, $2, $3, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING id, user_id, backup_job_id, backup_snapshot_id, target_profile_id, selected_paths, target_provider, target_url, target_username, target_connection_identity, target_root, conflict_strategy, threads, bandwidth_mbps, created_at, updated_at`,
			userID, preview.BackupJobID, preview.BackupSnapshotID, preview.TargetProfileID, preview.SelectedPaths, preview.TargetProvider, preview.TargetURL, preview.TargetUsername, preview.TargetConnectionIdentity, preview.TargetRoot, preview.ConflictStrategy, preview.Threads, preview.BandwidthMbps, preview.ConfigFingerprint).Scan(
			&job.ID, &job.UserID, &job.BackupJobID, &job.BackupSnapshotID, &job.TargetProfileID, &job.SelectedPaths, &job.TargetProvider, &job.TargetURL, &job.TargetUsername, &job.TargetConnectionIdentity, &job.TargetRoot, &job.ConflictStrategy, &job.Threads, &job.BandwidthMbps, &job.CreatedAt, &job.UpdatedAt)
		if err != nil {
			return nil, nil, fmt.Errorf("create restore job: %w", err)
		}
	}
	run := &RestoreRun{}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO restore_runs (restore_job_id, generation, threads, bandwidth_mbps, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted)
		VALUES ($1, COALESCE((SELECT MAX(generation) + 1 FROM restore_runs WHERE restore_job_id = $1), 1), $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, restore_job_id, generation, status, threads, bandwidth_mbps, target_password_encrypted, target_refresh_token_encrypted, target_token_expires_at, target_mega_session_id_encrypted, target_mega_master_key_encrypted, coordinator_generation, coordinator_lease_until, worker_hash, total_files, total_bytes, processed_files, processed_bytes, failed_files, error_code, started_at, finished_at, created_at, updated_at`,
		job.ID, preview.Threads, preview.BandwidthMbps, preview.TargetPasswordEncrypted, preview.TargetRefreshTokenEncrypted, preview.TargetTokenExpiresAt, preview.TargetMegaSessionIDEncrypted, preview.TargetMegaMasterKeyEncrypted).Scan(
		&run.ID, &run.RestoreJobID, &run.Generation, &run.Status, &run.Threads, &run.BandwidthMbps, &run.TargetPasswordEncrypted, &run.TargetRefreshTokenEncrypted, &run.TargetTokenExpiresAt, &run.TargetMegaSessionIDEncrypted, &run.TargetMegaMasterKeyEncrypted, &run.CoordinatorGeneration, &run.CoordinatorLeaseUntil, &run.WorkerHash, &run.TotalFiles, &run.TotalBytes, &run.ProcessedFiles, &run.ProcessedBytes, &run.FailedFiles, &run.ErrorCode, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return nil, nil, fmt.Errorf("create restore run: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE restore_previews SET status = 'CONSUMED', target_password_encrypted = NULL, target_refresh_token_encrypted = NULL, target_mega_session_id_encrypted = NULL, target_mega_master_key_encrypted = NULL WHERE id = $1 AND status = 'READY'`, preview.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("consume restore preview: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, nil, errors.New("restore preview changed while consuming")
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit restore preview consume: %w", err)
	}
	return job, run, nil
}

// ExpiringOAuthRestoreRun describes an active restore run whose target OAuth token is expiring.
type ExpiringOAuthRestoreRun struct {
	RestoreRunID          string
	Provider              string
	RefreshTokenEncrypted string
}

// GetExpiringOAuthRestoreRuns returns active restore runs whose OAuth access tokens expire within 15 minutes.
func GetExpiringOAuthRestoreRuns(database *sql.DB) ([]ExpiringOAuthRestoreRun, error) {
	rows, err := database.Query(`
		SELECT r.id, j.target_provider, r.target_refresh_token_encrypted
		FROM restore_runs r
		JOIN restore_jobs j ON j.id = r.restore_job_id
		WHERE r.status IN ('RUNNING', 'PLANNING', 'VERIFYING')
		  AND r.target_refresh_token_encrypted IS NOT NULL
		  AND r.target_token_expires_at IS NOT NULL
		  AND r.target_token_expires_at <= CURRENT_TIMESTAMP + INTERVAL '15 minutes'
	`)
	if err != nil {
		return nil, fmt.Errorf("get expiring oauth restore runs: %w", err)
	}
	defer rows.Close()

	var result []ExpiringOAuthRestoreRun
	for rows.Next() {
		var e ExpiringOAuthRestoreRun
		if err := rows.Scan(&e.RestoreRunID, &e.Provider, &e.RefreshTokenEncrypted); err != nil {
			return nil, fmt.Errorf("scan expiring oauth restore run: %w", err)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

