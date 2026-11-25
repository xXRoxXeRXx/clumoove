// Package backup coordinates Release-1 repository backups in worker processes.
package backup

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"math/big"
	"net"
	"path"
	"strings"
	"time"

	"backend/internal/backuprepo"
	"backend/internal/crypto"
	"backend/internal/coordcfg"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/observability"
	"backend/internal/restore"
	"backend/internal/storage"
)

var errVerifyMoreWork = errors.New("repository check has more bounded work")

// SourceProvider is the read-only storage contract required from a backup source.
type SourceProvider interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	sourceScanner
	StreamDownload(ctx context.Context, resourceType, path string) (io.ReadCloser, error)
}

// TargetProvider is the storage contract required from a backup repository target.
type TargetProvider interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	backupTargetLister
	compactionTarget
}

type sourceScanner interface {
	InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error)
	GetDirectoryListing(ctx context.Context, resourceType, path string) ([]storage.CloudResource, error)
}

type fileBackupSource interface {
	InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error)
	StreamDownload(ctx context.Context, resourceType, path string) (io.ReadCloser, error)
}

type backupTargetLister interface {
	GetDirectoryListing(ctx context.Context, resourceType, path string) ([]storage.CloudResource, error)
}

type packVerifierTarget interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
	StreamDownload(ctx context.Context, resourceType, path string) (io.ReadCloser, error)
}

type packDeleterTarget interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
	DeleteFile(ctx context.Context, resourceType, path string) error
}

type packWriterTarget interface {
	CreateParentDirectories(ctx context.Context, resourceType, path string) error
	StreamUpload(ctx context.Context, resourceType, path string, reader io.Reader, size int64) error
	packVerifierTarget
}

type compactionTarget interface {
	packWriterTarget
	DeleteFile(ctx context.Context, resourceType, path string) error
}

// ProviderFactoryFunc constructs the source and target contracts for a backup job run.
type ProviderFactoryFunc func(ctx context.Context, job *db.BackupJob) (SourceProvider, TargetProvider, error)

// Coordinator owns only backup worker execution. Its pack semaphore is process
// scoped, preventing independently claimed jobs from exhausting memory/network.
type Coordinator struct {
	db                *sql.DB
	store             db.BackupStore
	auditLog          db.AuditStore
	encryptionKey     string
	packWriterSlots   chan struct{}
	packReaderSlots   *restore.PackReaderLimiter
	workerID          string
	providerFactory   ProviderFactoryFunc
	preferMaintenance bool
	oauthLocker       oauth.TokenLocker
}

// backupRunLogger supplies the stable correlation fields used by every
// run-scoped record. Repository paths and credentials deliberately never leave
// the worker through these attributes.
func backupRunLogger(ctx context.Context, job *db.BackupJob, run *db.BackupRun) *slog.Logger {
	return observability.Logger(ctx).With(
		slog.String("component", "backup"),
		slog.String("backup_job_id", job.ID),
		slog.String("backup_run_id", run.ID),
		slog.Int("generation", run.Generation),
	)
}

func backupMaintenanceLogger(ctx context.Context, maintenance *db.BackupMaintenance) *slog.Logger {
	return observability.Logger(ctx).With(
		slog.String("component", "backup"),
		slog.String("backup_job_id", maintenance.BackupJobID),
		slog.String("backup_maintenance_id", maintenance.ID),
		slog.String("maintenance_kind", maintenance.Kind),
	)
}

func backupFailureAttrs(code string, cause error) []slog.Attr {
	attrs := []slog.Attr{slog.String("error_code", code)}
	if cause != nil {
		attrs = append(attrs, observability.Error(cause), slog.String("error_kind", observability.ErrorKind(cause)))
	}
	return attrs
}

// Config is the explicit worker-side configuration of a Coordinator.
type Config struct {
	// Common carries the encryption key, worker ID (blank selects
	// "backup-worker") and required OAuth locker.
	coordcfg.Common
	// MaxPackWriters is the validated value of MAX_BACKUP_PACK_WRITERS (1-4).
	MaxPackWriters int
	// PackReaders optionally shares the restore pack reader limiter.
	PackReaders *restore.PackReaderLimiter
}

// NewCoordinator configures a worker-side coordinator.
func NewCoordinator(database *sql.DB, cfg Config) (*Coordinator, error) {
	if database == nil {
		return nil, errors.New("backup database is required")
	}
	if cfg.MaxPackWriters < 1 || cfg.MaxPackWriters > 4 {
		return nil, fmt.Errorf("MAX_BACKUP_PACK_WRITERS must be between 1 and 4")
	}
	workerID, err := cfg.Resolve("backup-worker")
	if err != nil {
		return nil, fmt.Errorf("backup coordinator: %w", err)
	}
	return &Coordinator{db: database, store: db.NewBackupStore(database), auditLog: db.NewAuditStore(database), encryptionKey: cfg.EncryptionKey, packWriterSlots: make(chan struct{}, cfg.MaxPackWriters), packReaderSlots: cfg.PackReaders, workerID: workerID, oauthLocker: cfg.OAuthLocker}, nil
}

func (c *Coordinator) SetProviderFactory(factory ProviderFactoryFunc) {
	c.providerFactory = factory
}

// PollOnce claims and executes at most one queued run. Callers can invoke it
// from their existing worker poll loop without creating another scheduler.
func (c *Coordinator) PollOnce(ctx context.Context) (bool, error) {
	if _, err := c.store.RecoverStaleBackupRunsContext(ctx); err != nil {
		return false, err
	}
	if c.preferMaintenance {
		c.preferMaintenance = false
		if claimed, err := c.pollMaintenance(ctx); err != nil || claimed {
			return claimed, err
		}
	}
	job, run, err := c.store.ClaimNextQueuedBackupRunContext(ctx)
	if err != nil {
		return false, err
	}
	if run == nil {
		c.preferMaintenance = true
		return c.pollMaintenance(ctx)
	}
	c.preferMaintenance = true
	logger := backupRunLogger(ctx, job, run)
	logger.Info("backup_run_claimed", slog.String("trigger", run.Trigger))
	conn, err := c.db.Conn(ctx)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_LOCK_UNAVAILABLE", err)
		return true, nil
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, job.LockID); err != nil {
		c.fail(ctx, job, run, "BACKUP_LOCK_UNAVAILABLE", err)
		return true, nil
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, job.LockID)
	logger.Info("backup_run_lock_acquired")
	c.execute(ctx, job, run)
	return true, nil
}

func (c *Coordinator) pollMaintenance(ctx context.Context) (bool, error) {
	maintenance, err := c.store.ClaimNextBackupMaintenanceForWorkerContext(ctx, c.workerID)
	if err != nil || maintenance == nil {
		return maintenance != nil, err
	}
	logger := backupMaintenanceLogger(ctx, maintenance)
	logger.Info("backup_maintenance_started")
	conn, err := c.db.Conn(ctx)
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "backup_maintenance_lock_failed", backupFailureAttrs("BACKUP_LOCK_UNAVAILABLE", err)...)
		if retryErr := c.store.RetryBackupMaintenanceContext(context.Background(), maintenance.ID, "BACKUP_LOCK_UNAVAILABLE"); retryErr != nil {
			logger.Error("backup_maintenance_retry_persist_failed", observability.Error(retryErr), slog.String("error_kind", observability.ErrorKind(retryErr)))
		}
		return true, nil
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, maintenance.LockID); err != nil {
		logger.LogAttrs(ctx, slog.LevelError, "backup_maintenance_lock_failed", backupFailureAttrs("BACKUP_LOCK_UNAVAILABLE", err)...)
		if retryErr := c.store.RetryBackupMaintenanceContext(context.Background(), maintenance.ID, "BACKUP_LOCK_UNAVAILABLE"); retryErr != nil {
			logger.Error("backup_maintenance_retry_persist_failed", observability.Error(retryErr), slog.String("error_kind", observability.ErrorKind(retryErr)))
		}
		return true, nil
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, maintenance.LockID)
	if err := c.executeMaintenance(ctx, maintenance); err != nil {
		if errors.Is(err, errVerifyMoreWork) {
			if retryErr := c.store.RetryBackupMaintenanceContext(context.Background(), maintenance.ID, "BACKUP_VERIFY_CONTINUING"); retryErr != nil {
				return true, retryErr
			}
			return true, nil
		}
		logger.LogAttrs(ctx, slog.LevelError, "backup_maintenance_failed", backupFailureAttrs("BACKUP_MAINTENANCE_FAILED", err)...)
		if retryErr := c.store.RetryBackupMaintenanceContext(context.Background(), maintenance.ID, "BACKUP_MAINTENANCE_FAILED"); retryErr != nil {
			logger.Error("backup_maintenance_retry_persist_failed", observability.Error(retryErr), slog.String("error_kind", observability.ErrorKind(retryErr)))
		}
		return true, nil
	}
	if err := c.store.CompleteBackupMaintenanceContext(ctx, maintenance.ID); err != nil {
		logger.Error("backup_maintenance_completion_persist_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return true, err
	}
	logger.Info("backup_maintenance_completed")
	return true, nil
}

func (c *Coordinator) executeMaintenance(ctx context.Context, maintenance *db.BackupMaintenance) error {
	job, err := c.store.GetBackupJobForOwnerContext(ctx, maintenance.BackupJobID, maintenance.UserID)
	if err != nil {
		return err
	}
	ctx = storage.WithLocalUserScope(ctx, job.UserID)
	_, target, err := c.providers(ctx, job)
	if err != nil {
		return err
	}
	defer target.Close()
	if ok, err := target.Connect(ctx); err != nil || !ok {
		return errors.New("backup maintenance connection failed")
	}
	switch maintenance.Kind {
	case "RETENTION":
		return c.applyRetention(ctx, job, target)
	case "COMPACTION":
		return c.compactOnePack(ctx, job, target)
	case "DELETE_REPOSITORY":
		return c.deleteRepository(ctx, maintenance, job, target)
	case "VERIFY":
		return c.verifyRepositoryMetadata(ctx, maintenance, job, target)
	default:
		return fmt.Errorf("unsupported backup maintenance %q", maintenance.Kind)
	}
}

func (c *Coordinator) verifyRepositoryMetadata(ctx context.Context, maintenance *db.BackupMaintenance, job *db.BackupJob, target packVerifierTarget) error {
	if maintenance.VerifyMode.String != db.BackupVerifyMetadata && maintenance.VerifyMode.String != db.BackupVerifyFull && maintenance.VerifyMode.String != db.BackupVerifyBudgeted {
		return errors.New("invalid repository check mode")
	}
	if err := c.store.SnapshotBackupVerifyTargetsContext(ctx, maintenance.ID, job.ID); err != nil {
		return err
	}
	const maxVerifyClaimBytes int64 = 512 << 20
	deadline := time.Now().Add(10 * time.Minute)
	claimRead := int64(0)
	totalRead := maintenance.ProcessedBytes
	for {
		verifyTarget, err := c.store.ClaimNextBackupVerifyTargetContext(ctx, maintenance.ID, c.workerID)
		if err != nil {
			return err
		}
		if verifyTarget == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = c.store.ReleaseBackupVerifyTargetClaimContext(ctx, verifyTarget.Lease())
			return errVerifyMoreWork
		}
		cancelling, err := c.store.IsBackupMaintenanceCancellingContext(ctx, maintenance.ID)
		if err != nil {
			return err
		}
		if cancelling {
			_ = c.store.ReleaseBackupVerifyTargetClaimContext(ctx, verifyTarget.Lease())
			return nil
		}
		exists, size, err := target.FileExists(ctx, "files", verifyTarget.RemotePath)
		if err != nil {
			_ = c.store.ReleaseBackupVerifyTargetClaimContext(context.Background(), verifyTarget.Lease())
			return err
		}
		state := "COMPLETED"
		if !exists || size != verifyTarget.SizeBytes {
			state, err = c.store.CompleteBackupVerifyTargetContext(ctx, verifyTarget.Lease(), exists, verifyTarget.SizeBytes, size)
			if err != nil {
				return err
			}
		} else if maintenance.VerifyMode.String == db.BackupVerifyFull || (maintenance.VerifyMode.String == db.BackupVerifyBudgeted && totalRead < maintenance.ByteBudget.Int64) {
			if claimRead > 0 && claimRead+verifyTarget.SizeBytes > maxVerifyClaimBytes {
				_ = c.store.ReleaseBackupVerifyTargetClaimContext(ctx, verifyTarget.Lease())
				return errVerifyMoreWork
			}
			if err := c.packReaderSlots.Acquire(ctx); err != nil {
				_ = c.store.ReleaseBackupVerifyTargetClaimContext(context.Background(), verifyTarget.Lease())
				return err
			}
			reader, err := target.StreamDownload(ctx, "files", verifyTarget.RemotePath)
			if err != nil {
				c.packReaderSlots.Release()
				_ = c.store.ReleaseBackupVerifyTargetClaimContext(context.Background(), verifyTarget.Lease())
				// A provider/network failure is retryable maintenance work, not
				// evidence that an immutable repository pack is damaged.
				return err
			}
			var expected [sha256.Size]byte
			if len(verifyTarget.SHA256) != sha256.Size {
				_ = reader.Close()
				c.packReaderSlots.Release()
				return errors.New("invalid backup verify pack hash")
			}
			copy(expected[:], verifyTarget.SHA256)
			validationErr := func() error {
				defer reader.Close()
				_, err := backuprepo.ValidatePack(reader, expected, nil)
				return err
			}()
			c.packReaderSlots.Release()
			if validationErr != nil {
				if isRetryableVerifyReadError(validationErr) {
					_ = c.store.ReleaseBackupVerifyTargetClaimContext(context.Background(), verifyTarget.Lease())
					return validationErr
				}
				state = "DAMAGED"
				if err := c.store.MarkBackupVerifyTargetDamagedContext(ctx, verifyTarget.Lease()); err != nil {
					return err
				}
			} else {
				claimRead += verifyTarget.SizeBytes
				totalRead += verifyTarget.SizeBytes
				if err := c.store.AddBackupVerifyProcessedBytesContext(ctx, maintenance.ID, verifyTarget.SizeBytes); err != nil {
					return err
				}
				if err := c.store.MarkBackupVerifyTargetReadContext(ctx, verifyTarget.Lease(), verifyTarget.SizeBytes); err != nil {
					return err
				}
				if verifyTarget.PackID.Valid {
					if err := c.store.MarkBackupPacksCheckedContext(ctx, job.ID, []string{verifyTarget.PackID.String}); err != nil {
						return err
					}
				}
				state, err = c.store.CompleteBackupVerifyTargetContext(ctx, verifyTarget.Lease(), true, verifyTarget.SizeBytes, size)
				if err != nil {
					return err
				}
			}
		} else {
			state, err = c.store.CompleteBackupVerifyTargetContext(ctx, verifyTarget.Lease(), true, verifyTarget.SizeBytes, size)
			if err != nil {
				return err
			}
		}
		if state != "COMPLETED" && verifyTarget.PackID.Valid {
			if err := c.store.MarkBackupPackDamagedContext(ctx, job.ID, verifyTarget.PackID.String); err != nil {
				return err
			}
		}
		if err := c.store.AdvanceBackupVerifyCursorContext(ctx, maintenance.ID, verifyTarget.ID); err != nil {
			return err
		}
	}
	return c.store.CompleteBackupVerifyContext(ctx, maintenance.ID)
}

func isRetryableVerifyReadError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func deleteRemotePack(ctx context.Context, target packDeleterTarget, pack db.BackupPack) error {
	exists, _, err := target.FileExists(ctx, "files", pack.RemotePath)
	if err != nil {
		return err
	}
	if exists {
		return target.DeleteFile(ctx, "files", pack.RemotePath)
	}
	return nil
}

func (c *Coordinator) applyRetention(ctx context.Context, job *db.BackupJob, target packDeleterTarget) error {
	if err := c.store.ExpireBackupSnapshotsContext(ctx, job.ID, job.RetentionCount); err != nil {
		return err
	}
	packs, err := c.store.ListBackupReclaimablePacksContext(ctx, job.ID, 1000)
	if err != nil {
		return err
	}
	for _, pack := range packs {
		if err := deleteRemotePack(ctx, target, pack); err != nil {
			return err
		}
		if err := c.store.MarkBackupPackDeletedContext(ctx, job.ID, pack.ID); err != nil {
			return err
		}
	}
	if len(packs) == 1000 {
		return c.store.EnqueueBackupMaintenanceContext(ctx, job.ID, "RETENTION")
	}
	candidate, err := c.store.FindBackupCompactionCandidateContext(ctx, job.ID)
	if err != nil {
		return err
	}
	if candidate != nil {
		// This maintenance claim holds the job advisory lock. The queued claim
		// runs only after this lock is released, so it cannot overlap retention.
		return c.store.EnqueueBackupMaintenanceContext(ctx, job.ID, "COMPACTION")
	}
	return nil
}

func (c *Coordinator) compactOnePack(ctx context.Context, job *db.BackupJob, target compactionTarget) error {
	pack, err := c.store.FindBackupCompactionCandidateContext(ctx, job.ID)
	if err != nil || pack == nil {
		return err
	}
	liveBlocks, err := c.store.ListBackupLiveBlocksContext(ctx, job.ID, pack.ID)
	if err != nil {
		return err
	}
	live := make(map[string]db.BackupLiveBlock, len(liveBlocks))
	for _, block := range liveBlocks {
		live[string(block.Hash)] = block
	}
	select {
	case c.packWriterSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.packWriterSlots }()
	reader, err := target.StreamDownload(ctx, "files", pack.RemotePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	var expected [sha256.Size]byte
	if len(pack.SHA256) != sha256.Size {
		return errors.New("invalid compacted pack hash")
	}
	copy(expected[:], pack.SHA256)
	entries := make([]backuprepo.Entry, 0, len(liveBlocks))
	blockIDs := make([]string, 0, len(liveBlocks))
	_, err = backuprepo.ValidatePack(reader, expected, func(_ int64, entry backuprepo.Entry) error {
		if block, ok := live[string(entry.Hash[:])]; ok {
			if block.Size != len(entry.Data) {
				return errors.New("backup live block size mismatch")
			}
			entries = append(entries, entry)
			blockIDs = append(blockIDs, block.ID)
		}
		return nil
	})
	if err != nil || len(entries) != len(liveBlocks) {
		return errors.New("backup compaction source pack validation failed")
	}
	var encoded bytes.Buffer
	replacement, err := backuprepo.EncodePack(&encoded, entries)
	if err != nil {
		return err
	}
	remotePath, err := repositoryPath(job.RepositoryRoot, "packs", hex.EncodeToString(replacement.ID[:])+".pack")
	if err != nil {
		return err
	}
	if err := target.CreateParentDirectories(ctx, "files", remotePath); err != nil {
		return err
	}
	if err := target.StreamUpload(ctx, "files", remotePath, bytes.NewReader(encoded.Bytes()), replacement.Size); err != nil {
		return err
	}
	exists, size, err := target.FileExists(ctx, "files", remotePath)
	if err != nil || !exists || size != replacement.Size {
		return errors.New("replacement backup pack size confirmation failed")
	}
	verified, err := target.StreamDownload(ctx, "files", remotePath)
	if err != nil {
		return err
	}
	_, validationErr := backuprepo.ValidatePack(verified, replacement.ID, nil)
	closeErr := verified.Close()
	if validationErr != nil {
		return validationErr
	}
	if closeErr != nil {
		return closeErr
	}
	locators := make([]db.BackupPackBlock, 0, len(entries))
	entryIndex := 0
	_, err = backuprepo.ValidatePack(bytes.NewReader(encoded.Bytes()), replacement.ID, func(offset int64, entry backuprepo.Entry) error {
		if entryIndex >= len(entries) || entry.Hash != entries[entryIndex].Hash || !bytes.Equal(entry.Data, entries[entryIndex].Data) {
			return errors.New("replacement backup pack catalog mismatch")
		}
		locators = append(locators, db.BackupPackBlock{Hash: entry.Hash[:], PlaintextSize: len(entry.Data), PayloadOffset: offset, PayloadLength: len(entry.Data)})
		entryIndex++
		return nil
	})
	if err != nil || entryIndex != len(entries) {
		return errors.New("replacement backup pack validation failed")
	}
	if _, err := c.store.ReplaceBackupPackLocatorsContext(ctx, job.ID, pack.ID, remotePath, replacement.ID[:], replacement.Size, locators, blockIDs); err != nil {
		return err
	}
	if err := deleteRemotePack(ctx, target, *pack); err != nil {
		return err
	}
	if err := c.store.MarkBackupPackDeletedContext(ctx, job.ID, pack.ID); err != nil {
		return err
	}
	if next, err := c.store.FindBackupCompactionCandidateContext(ctx, job.ID); err != nil {
		return err
	} else if next != nil {
		return c.store.EnqueueBackupMaintenanceContext(ctx, job.ID, "COMPACTION")
	}
	return nil
}

func (c *Coordinator) deleteRepository(ctx context.Context, maintenance *db.BackupMaintenance, job *db.BackupJob, target packDeleterTarget) error {
	packs, err := c.store.ListBackupPacksForDeletionContext(ctx, job.ID, 1000)
	if err != nil {
		return err
	}
	for _, pack := range packs {
		if err := deleteRemotePack(ctx, target, pack); err != nil {
			return err
		}
		if err := c.store.MarkBackupPackDeletedContext(ctx, job.ID, pack.ID); err != nil {
			return err
		}
	}
	if len(packs) == 1000 {
		// The claim's object budget is complete. A subsequent request resumes
		// from catalog state instead of relying on a remote directory listing.
		return c.store.RetryBackupMaintenanceContext(ctx, maintenance.ID, "")
	}
	formatPath, err := repositoryPath(job.RepositoryRoot, "format-v1.json")
	if err != nil {
		return err
	}
	exists, _, err := target.FileExists(ctx, "files", formatPath)
	if err != nil {
		return err
	}
	if exists {
		if err := target.DeleteFile(ctx, "files", formatPath); err != nil {
			return err
		}
	}
	deleted, err := c.store.DeleteBackupJobAfterRepositoryCleanupContext(ctx, job.ID)
	if err != nil {
		return err
	}
	if !deleted {
		return errors.New("backup repository cleanup did not finalize")
	}
	return nil
}

// Run polls until ctx is cancelled. It deliberately uses a caller-owned
// context, so stopping a worker stops both polling and any active transfer.
func (c *Coordinator) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("backup poll interval must be positive")
	}
	retryDelay := interval
	if retryDelay > time.Second {
		retryDelay = time.Second
	}
	for {
		if _, err := c.PollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Default().Warn("backup_poll_failed", slog.Any("error", err), slog.Duration("retry_delay", retryDelay))
			if !waitForBackupPoll(ctx, retryDelay) {
				return ctx.Err()
			}
			if retryDelay < 30*time.Second {
				retryDelay *= 2
				if retryDelay > 30*time.Second {
					retryDelay = 30 * time.Second
				}
			}
			continue
		}
		retryDelay = interval
		if retryDelay > time.Second {
			retryDelay = time.Second
		}
		if !waitForBackupPoll(ctx, interval) {
			return ctx.Err()
		}
	}
}

func waitForBackupPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type scannedFile struct {
	sourcePath   string
	relativePath string
	size         int64
	mtime        time.Time
}

type scannedDirectory struct {
	relativePath string
	mtime        time.Time
}

type pendingFile struct {
	file     scannedFile
	fileHash [sha256.Size]byte
	blockIDs []string
}

type runStats struct {
	totalFiles, totalDirs, processedFiles, failedFiles, unstableFiles int
	totalBytes, processedBytes, deduplicatedBytes                     int64
}

func (c *Coordinator) execute(ctx context.Context, job *db.BackupJob, run *db.BackupRun) {
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go c.heartbeatRun(heartbeatCtx, job, run, heartbeatDone)
	defer func() {
		cancelHeartbeat()
		<-heartbeatDone
	}()
	logger := backupRunLogger(ctx, job, run)
	logger.Info("backup_run_started",
		slog.String("source_provider", job.SourceProvider),
		slog.String("target_provider", job.TargetProvider),
		slog.Int("selected_path_count", len(job.SelectedPaths)),
	)
	if job.SourceProvider == "immich" || job.TargetProvider == "immich" ||
		!storage.ProviderSupportsResourceType(job.SourceProvider, "files") ||
		!storage.ProviderSupportsResourceType(job.TargetProvider, "files") || len(job.SelectedPaths) == 0 {
		c.fail(ctx, job, run, "BACKUP_FILES_UNSUPPORTED", nil)
		return
	}
	ctx = storage.WithLocalUserScope(ctx, job.UserID)
	source, target, err := c.providers(ctx, job)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED", err)
		return
	}
	defer source.Close()
	defer target.Close()
	if ok, err := source.Connect(ctx); err != nil || !ok {
		if err == nil {
			err = errors.New("source connection returned unsuccessful")
		}
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED", err)
		return
	}
	logger.Info("backup_source_connected")
	if ok, err := target.Connect(ctx); err != nil || !ok {
		if err == nil {
			err = errors.New("target connection returned unsuccessful")
		}
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED", err)
		return
	}
	logger.Info("backup_target_connected")
	if err := ensureDedicatedTarget(ctx, target, job.TargetDir, job.RepositoryID); err != nil {
		c.fail(ctx, job, run, "BACKUP_TARGET_NOT_EMPTY", err)
		return
	}
	if err := ensureFormat(ctx, target, job.RepositoryRoot, job.RepositoryID); err != nil {
		c.fail(ctx, job, run, "BACKUP_REPOSITORY_FORMAT_INVALID", err)
		return
	}
	logger.Info("backup_repository_validated")
	excludedRoot := ""
	sameConnection, err := c.sameBackupConnection(ctx, job)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED", err)
		return
	}
	if sameConnection {
		excludedRoot = job.RepositoryRoot
	}
	files, directories, stats, err := scanFiles(ctx, source, job.SelectedPaths, excludedRoot)
	if err != nil {
		c.finishOrFail(ctx, job, run, "SCANNING", err, stats)
		return
	}
	logger.Info("backup_scan_completed",
		slog.Int("total_files", stats.totalFiles),
		slog.Int("total_directories", stats.totalDirs),
		slog.Int64("total_bytes", stats.totalBytes),
	)
	// Hold this permit from the first source read through the final flush. This
	// bounds an admitted job's pack buffer rather than merely serializing upload.
	select {
	case c.packWriterSlots <- struct{}{}:
	case <-ctx.Done():
		c.finishOrFail(ctx, job, run, "SCANNING", ctx.Err(), stats)
		return
	}
	defer func() { <-c.packWriterSlots }()
	if ok, err := c.store.TransitionBackupRunContext(ctx, job.ID, run.Generation, run.ID, "SCANNING", "RUNNING"); err != nil || !ok {
		if err == nil {
			err = errors.New("backup run changed state before transfer start")
		}
		c.fail(ctx, job, run, "BACKUP_TRANSITION_FAILED", err)
		return
	}
	run.State = "RUNNING"
	logger.Info("backup_transfer_started")
	snapshotID, err := c.store.CreateBackupSnapshotDraftContext(ctx, job.ID, run.ID, job.SelectedPaths)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED", err)
		return
	}

	var previousCatalog map[string]db.BackupSnapshotCatalogItem
	previousSnapshotID, err := c.store.GetLatestValidBackupSnapshotIDContext(ctx, job.ID)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED", err)
		return
	}
	if previousSnapshotID != "" {
		catalog, err := c.store.GetBackupSnapshotFileCatalogContext(ctx, previousSnapshotID)
		if err != nil {
			c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED", err)
			return
		}
		previousCatalog = catalog
		logger.Info("backup_incremental_catalog_loaded", slog.String("snapshot_id", previousSnapshotID), slog.Int("items_count", len(catalog)))
	}

	builder := newPackBuilder(c, job, target)
	pending := make([]pendingFile, 0, len(files))
	for _, file := range files {
		if !c.runActive(ctx, job, run, snapshotID) {
			return
		}

		// previousCatalog is nil on first-run backups; nil map lookups return false, so
		// this branch is simply skipped and every file is downloaded fresh.
		if prev, ok := previousCatalog[file.relativePath]; ok &&
			!file.mtime.IsZero() &&
			!prev.Mtime.IsZero() &&
			prev.Mtime.Equal(file.mtime) &&
			prev.SizeBytes == file.size &&
			(file.size == 0 || len(prev.BlockIDs) > 0) &&
			prev.FileSHA256 != [sha256.Size]byte{} {
			// Note: InspectResource is called per reusable file to prevent TOCTOU mutations between
			// scan/listing and reuse. For large snapshots with many unchanged files, this incurs O(N)
			// lightweight provider round-trips. Future optimization: batch-stat or conditional bypass
			// when providers guarantee stable mtimes/ETags.
			current, err := source.InspectResource(ctx, "files", file.sourcePath)
			if err == nil && sameSource(file, current) {
				pending = append(pending, pendingFile{
					file:     file,
					fileHash: prev.FileSHA256,
					blockIDs: prev.BlockIDs,
				})
				stats.deduplicatedBytes += file.size
				continue
			}
		}

		item, unstable, err := c.backupFile(ctx, job, source, file, &stats, builder)
		if err != nil {
			c.finishOrFail(ctx, job, run, "RUNNING", err, stats)
			return
		}
		if unstable {
			stats.unstableFiles++
			continue
		}
		pending = append(pending, item)
	}
	if !c.runActive(ctx, job, run, snapshotID) {
		return
	}
	if err := builder.flush(ctx); err != nil {
		c.finishOrFail(ctx, job, run, "RUNNING", err, stats)
		return
	}
	if !c.runActive(ctx, job, run, snapshotID) {
		return
	}

	batchItems := make([]db.BatchSnapshotItem, 0, len(directories)+len(pending))
	for _, directory := range directories {
		batchItems = append(batchItems, db.BatchSnapshotItem{
			RelativePath: directory.relativePath,
			IsDir:        true,
			SizeBytes:    0,
			Mtime:        directory.mtime,
			State:        "AVAILABLE",
		})
	}
	for _, item := range pending {
		resolvedBlockIDs := make([]string, len(item.blockIDs))
		for i, id := range item.blockIDs {
			resolvedBlockIDs[i] = builder.resolveBlockID(id)
		}
		batchItems = append(batchItems, db.BatchSnapshotItem{
			RelativePath: item.file.relativePath,
			IsDir:        false,
			SizeBytes:    item.file.size,
			Mtime:        item.file.mtime,
			FileSHA256:   item.fileHash[:],
			State:        "AVAILABLE",
			BlockIDs:     resolvedBlockIDs,
		})
		stats.processedFiles++
		stats.processedBytes += item.file.size
	}
	if err := c.store.BatchCreateBackupSnapshotItemsAndBlocksContext(ctx, snapshotID, batchItems); err != nil {
		c.finishOrFail(ctx, job, run, "RUNNING", errors.New("backup catalog write failed"), stats)
		return
	}
	if !c.runActive(ctx, job, run, snapshotID) {
		return
	}
	if ok, err := c.store.TransitionBackupRunContext(ctx, job.ID, run.Generation, run.ID, "RUNNING", "VERIFYING"); err != nil || !ok {
		if err == nil {
			err = errors.New("backup run changed state before verification")
		}
		c.fail(ctx, job, run, "BACKUP_TRANSITION_FAILED", err)
		return
	}
	run.State = "VERIFYING"
	logger.Info("backup_verification_started")
	if err := c.verifySnapshotSample(ctx, job, snapshotID, target); err != nil {
		c.fail(ctx, job, run, "BACKUP_VERIFICATION_FAILED", err)
		return
	}
	snapshotState, runState := "READY", "COMPLETED"
	if stats.unstableFiles > 0 {
		snapshotState, runState = "PARTIAL", "PARTIAL"
	}
	published, err := c.store.PublishBackupSnapshotAndFinalizeContext(ctx, job.ID, run.Generation, run.ID, snapshotID, snapshotState, runState, stats.totalFiles, stats.totalDirs, stats.totalBytes, stats.processedFiles, stats.processedBytes, stats.deduplicatedBytes, stats.unstableFiles, stats.failedFiles)
	if err != nil || !published {
		if err == nil {
			err = errors.New("backup snapshot publication lost its state claim")
		}
		c.fail(ctx, job, run, "BACKUP_PUBLICATION_FAILED", err)
		return
	}
	if runState == "PARTIAL" {
		c.audit(job, db.AuditBackupPartial)
	} else {
		c.audit(job, db.AuditBackupCompleted)
	}
	logger.Info("backup_run_completed",
		slog.String("status", runState),
		slog.Int("total_files", stats.totalFiles),
		slog.Int("processed_files", stats.processedFiles),
		slog.Int64("total_bytes", stats.totalBytes),
		slog.Int64("processed_bytes", stats.processedBytes),
		slog.Int64("deduplicated_bytes", stats.deduplicatedBytes),
		slog.Int("unstable_files", stats.unstableFiles),
	)
}

// verifySnapshotSample checks a cryptographically selected subset of packs
// before publication. Each selected pack is fully parsed, which validates its
// framing, pack ID, and every block without requiring range-read support.
func (c *Coordinator) verifySnapshotSample(ctx context.Context, job *db.BackupJob, snapshotID string, target packVerifierTarget) error {
	packs, err := c.store.ListBackupSnapshotPacksContext(ctx, job.ID, snapshotID)
	if err != nil {
		return err
	}
	const sampleBudget int64 = 64 * 1024 * 1024
	if len(packs) == 0 {
		return nil
	}
	start, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(len(packs))))
	if err != nil {
		return fmt.Errorf("choose backup verification sample: %w", err)
	}
	remaining := sampleBudget
	checked := make([]string, 0, len(packs))
	for step := 0; step < len(packs); step++ {
		pack := packs[(int(start.Int64())+step)%len(packs)]
		if pack.SizeBytes > remaining {
			continue
		}
		if len(pack.SHA256) != sha256.Size || pack.State != "READY" {
			c.markPackDamaged(job, pack.ID)
			return errors.New("backup pack catalog is invalid")
		}
		exists, size, err := target.FileExists(ctx, "files", pack.RemotePath)
		if err != nil || !exists || size != pack.SizeBytes {
			c.markPackDamaged(job, pack.ID)
			return errors.New("backup pack size verification failed")
		}
		reader, err := target.StreamDownload(ctx, "files", pack.RemotePath)
		if err != nil {
			c.markPackDamaged(job, pack.ID)
			return err
		}
		var expected [sha256.Size]byte
		copy(expected[:], pack.SHA256)
		_, validateErr := backuprepo.ValidatePack(reader, expected, nil)
		closeErr := reader.Close()
		if validateErr != nil || closeErr != nil {
			c.markPackDamaged(job, pack.ID)
			if validateErr != nil {
				return validateErr
			}
			return closeErr
		}
		checked = append(checked, pack.ID)
		remaining -= pack.SizeBytes
		if remaining == 0 {
			break
		}
	}
	return c.store.MarkBackupPacksCheckedContext(ctx, job.ID, checked)
}

func (c *Coordinator) runActive(ctx context.Context, job *db.BackupJob, run *db.BackupRun, snapshotID string) bool {
	active, err := c.store.BackupRunActiveContext(ctx, job.ID, run.Generation, run.ID)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED", err)
		return false
	}
	if !active {
		logger := backupRunLogger(ctx, job, run)
		logger.Info("backup_run_no_longer_active")
		if err := c.store.DiscardBackupSnapshotDraftContext(context.Background(), snapshotID, job.ID, run.ID); err != nil {
			logger.Warn("backup_snapshot_draft_discard_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		}
		return false
	}
	return true
}

func (c *Coordinator) heartbeatRun(ctx context.Context, job *db.BackupJob, run *db.BackupRun, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.store.TouchBackupRunContext(ctx, job.ID, run.Generation, run.ID); err != nil && ctx.Err() == nil {
				backupRunLogger(ctx, job, run).Warn("backup_run_heartbeat_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			}
		}
	}
}

func ensureDedicatedTarget(ctx context.Context, target backupTargetLister, targetDir, repositoryID string) error {
	entries, err := target.GetDirectoryListing(ctx, "files", targetDir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	container, err := repositoryPath(targetDir, ".clumoove-backup")
	if err != nil {
		return err
	}
	if len(entries) != 1 || path.Clean(entries[0].Path) != path.Clean(container) || !entries[0].IsDir {
		return errors.New("backup target directory is not empty")
	}
	entries, err = target.GetDirectoryListing(ctx, "files", container)
	if err != nil || len(entries) != 1 {
		return errors.New("backup target directory is not dedicated")
	}
	repositoryRoot, err := repositoryPath(container, repositoryID)
	if err != nil || path.Clean(entries[0].Path) != path.Clean(repositoryRoot) || !entries[0].IsDir {
		return errors.New("backup target directory belongs to another repository")
	}
	return nil
}

func (c *Coordinator) providers(ctx context.Context, job *db.BackupJob) (SourceProvider, TargetProvider, error) {
	if c.providerFactory != nil {
		return c.providerFactory(ctx, job)
	}
	return c.defaultProviders(ctx, job)
}

func (c *Coordinator) defaultProviders(ctx context.Context, job *db.BackupJob) (SourceProvider, TargetProvider, error) {
	if err := c.ensureFreshOAuthToken(ctx, job, "source"); err != nil {
		return nil, nil, err
	}
	if err := c.ensureFreshOAuthToken(ctx, job, "target"); err != nil {
		return nil, nil, err
	}
	sourceBytes, err := crypto.DecryptBytesWithDomain(job.SourcePasswordEncrypted, c.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.SourceProvider)))
	if err != nil {
		return nil, nil, err
	}
	defer clear(sourceBytes)
	targetBytes, err := crypto.DecryptBytesWithDomain(job.TargetPasswordEncrypted, c.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
	if err != nil {
		return nil, nil, err
	}
	defer clear(targetBytes)
	sourceMegaSession, err := megasecret.DecodeProviderMegaSession(job.SourceProvider, job.SourceMegaSessionIDEncrypted.String, job.SourceMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return nil, nil, err
	}
	defer clear(sourceMegaSession.MasterKey)
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(job.TargetProvider, job.TargetMegaSessionIDEncrypted.String, job.TargetMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return nil, nil, err
	}
	defer clear(targetMegaSession.MasterKey)
	source, err := storage.NewProvider(ctx, job.SourceProvider, job.SourceURL, job.SourceUsername, string(sourceBytes), storage.WithProviderMegaSession(sourceMegaSession))
	clear(sourceMegaSession.MasterKey)
	if err != nil {
		return nil, nil, err
	}
	target, err := storage.NewProvider(ctx, job.TargetProvider, job.TargetURL, job.TargetUsername, string(targetBytes), storage.WithProviderMegaSession(targetMegaSession))
	clear(targetMegaSession.MasterKey)
	if err != nil {
		source.Close()
		return nil, nil, err
	}
	return source, target, nil
}

func (c *Coordinator) ensureFreshOAuthToken(ctx context.Context, job *db.BackupJob, role string) error {
	provider := job.SourceProvider
	accessEncrypted := &job.SourcePasswordEncrypted
	refreshEncrypted := &job.SourceRefreshTokenEncrypted
	expiresAt := &job.SourceTokenExpiresAt
	if role == "target" {
		provider = job.TargetProvider
		accessEncrypted = &job.TargetPasswordEncrypted
		refreshEncrypted = &job.TargetRefreshTokenEncrypted
		expiresAt = &job.TargetTokenExpiresAt
	}
	if !oauth.IsProvider(provider) || (expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute))) {
		return nil
	}
	if !refreshEncrypted.Valid || refreshEncrypted.String == "" {
		return errors.New("backup OAuth refresh credential is unavailable")
	}
	expectedRefreshEnc := refreshEncrypted.String
	refresh, err := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return fmt.Errorf("decrypt backup OAuth refresh token: %w", err)
	}
	defer crypto.ZeroString(&refresh)

	res, err := c.refresher().Refresh(ctx, oauth.OwnerRefresh{
		UserID:       job.UserID,
		Provider:     provider,
		RefreshToken: refresh,
		Label:        "backup job " + job.ID + " " + role,
		Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
			token, rerr := oauth.RefreshToken(refreshCtx, prov, tok)
			if rerr != nil {
				return nil, fmt.Errorf("refresh backup OAuth token: %w", rerr)
			}
			return token, nil
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			latestJob, lerr := c.store.GetBackupJobForOwnerContext(lockCtx, job.ID, job.UserID)
			if lerr != nil || latestJob == nil {
				return "", false, "", nil
			}
			*job = *latestJob
			if expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
				return *accessEncrypted, true, "", nil
			}
			if refreshEncrypted.Valid && refreshEncrypted.String != "" && refreshEncrypted.String != expectedRefreshEnc {
				expectedRefreshEnc = refreshEncrypted.String
				updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", fmt.Errorf("decrypt backup OAuth refresh token: %w", decErr)
				}
				if updatedPlain != "" && updatedPlain != target {
					refresh = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Sink:            c.store.BackupJobTokenSink(job.ID, role),
		ExpectedRefresh: func() string { return expectedRefreshEnc },
		OnPersisted: func(tokens db.EncryptedOAuthTokens) {
			*accessEncrypted = tokens.Access
			*refreshEncrypted = tokens.NullRefresh()
			*expiresAt = tokens.NullExpiresAt()
		},
	})
	if err != nil {
		return err
	}
	if res.Adopted {
		*accessEncrypted = res.AdoptedAccess
	}
	return nil
}

// refresher binds the coordinator's shared rotation infrastructure.
func (c *Coordinator) refresher() *oauth.Refresher {
	return oauth.NewRefresher(oauth.RefresherConfig{DB: c.db, Locker: c.oauthLocker, EncryptionKey: c.encryptionKey})
}

func (c *Coordinator) backupFile(ctx context.Context, job *db.BackupJob, source fileBackupSource, file scannedFile, stats *runStats, builder *packBuilder) (pendingFile, bool, error) {
	// A file that changes during both attempts is deliberately omitted, rather
	// than publishing content whose recorded metadata is no longer truthful.
	for attempt := 0; attempt < 2; attempt++ {
		current, err := source.InspectResource(ctx, "files", file.sourcePath)
		if err != nil {
			return pendingFile{}, false, err
		}
		if !sameSource(file, current) {
			continue
		}
		reader, err := source.StreamDownload(ctx, "files", file.sourcePath)
		if err != nil {
			return pendingFile{}, false, err
		}
		var sha1Hasher hash.Hash
		var expectedSHA1 string
		var splitReader io.Reader = reader
		if algo, val := storage.ParseHashString(current.Hash); algo == "SHA1" && val != "" {
			sha1Hasher = sha1.New()
			expectedSHA1 = val
			splitReader = io.TeeReader(reader, sha1Hasher)
		}
		blockIDs := make([]string, 0)
		fileHash, err := backuprepo.SplitBlocks(ctx, splitReader, func(entry backuprepo.Entry) error {
			hashKey := string(entry.Hash[:])
			if blockID, found := builder.catalogBlockID(hashKey); found {
				blockIDs = append(blockIDs, blockID)
				stats.deduplicatedBytes += int64(len(entry.Data))
				return nil
			}
			if builder.hasPendingBlock(hashKey) {
				// A block can recur in a file or in another file before the
				// current pack is flushed. Keep its placeholder so every
				// ordinal can reference the one catalog entry after the flush.
				blockIDs = append(blockIDs, hashKey)
				stats.deduplicatedBytes += int64(len(entry.Data))
				return nil
			}
			blockID, found, err := c.store.FindBackupBlockContext(ctx, job.ID, entry.Hash[:])
			if err != nil {
				return err
			}
			if found {
				blockIDs = append(blockIDs, blockID)
				stats.deduplicatedBytes += int64(len(entry.Data))
				return nil
			}
			if err := builder.add(ctx, entry); err != nil {
				return err
			}
			blockIDs = append(blockIDs, hashKey)
			return nil
		})
		closeErr := reader.Close()
		if err != nil {
			return pendingFile{}, false, err
		}
		if closeErr != nil {
			return pendingFile{}, false, closeErr
		}
		if sha1Hasher != nil && !strings.EqualFold(hex.EncodeToString(sha1Hasher.Sum(nil)), expectedSHA1) {
			return pendingFile{}, false, fmt.Errorf("backup source sha1 mismatch for %s", file.relativePath)
		}
		after, err := source.InspectResource(ctx, "files", file.sourcePath)
		if err != nil {
			return pendingFile{}, false, err
		}
		if sameSource(file, after) {
			return pendingFile{file: file, fileHash: fileHash, blockIDs: blockIDs}, false, nil
		}
	}
	return pendingFile{}, true, nil
}

func sameSource(file scannedFile, resource storage.CloudResource) bool {
	return !resource.IsDir && resource.Size == file.size && resource.LastModified.Equal(file.mtime) && resource.Path == file.sourcePath
}

type packBuilder struct {
	coordinator *Coordinator
	job         *db.BackupJob
	target      packWriterTarget
	entries     []backuprepo.Entry
	encodedSize int64
	ids         map[string]string
	pending     map[string]struct{}
}

func newPackBuilder(coordinator *Coordinator, job *db.BackupJob, target packWriterTarget) *packBuilder {
	return &packBuilder{
		coordinator: coordinator,
		job:         job,
		target:      target,
		encodedSize: 16 + 20,
		ids:         make(map[string]string),
		pending:     make(map[string]struct{}),
	}
}

func (b *packBuilder) add(ctx context.Context, entry backuprepo.Entry) error {
	hashKey := string(entry.Hash[:])
	if _, found := b.pending[hashKey]; found {
		return nil
	}
	entrySize := int64(sha256.Size + 4 + len(entry.Data))
	if b.encodedSize+entrySize > backuprepo.MaxPackSize && len(b.entries) > 0 {
		if err := b.flush(ctx); err != nil {
			return err
		}
	}
	b.entries = append(b.entries, entry)
	b.pending[hashKey] = struct{}{}
	b.encodedSize += entrySize
	return nil
}

func (b *packBuilder) catalogBlockID(key string) (string, bool) {
	id, found := b.ids[key]
	return id, found
}

func (b *packBuilder) hasPendingBlock(key string) bool {
	_, found := b.pending[key]
	return found
}

// resolveBlockID replaces an unflushed block-hash placeholder with its catalog
// ID. Existing catalog IDs must pass through unchanged.
func (b *packBuilder) resolveBlockID(reference string) string {
	if id, found := b.catalogBlockID(reference); found {
		return id
	}
	return reference
}

func (b *packBuilder) flush(ctx context.Context) error {
	if len(b.entries) == 0 {
		return nil
	}
	var encoded bytes.Buffer
	pack, err := backuprepo.EncodePack(&encoded, b.entries)
	if err != nil {
		return err
	}
	remotePath, err := repositoryPath(b.job.RepositoryRoot, "packs", hex.EncodeToString(pack.ID[:])+".pack")
	if err != nil {
		return err
	}
	if err := b.target.CreateParentDirectories(ctx, "files", remotePath); err != nil {
		return err
	}
	if err := b.target.StreamUpload(ctx, "files", remotePath, bytes.NewReader(encoded.Bytes()), pack.Size); err != nil {
		return err
	}
	exists, size, err := b.target.FileExists(ctx, "files", remotePath)
	if err != nil || !exists || size != pack.Size {
		return errors.New("remote pack size confirmation failed")
	}
	remote, err := b.target.StreamDownload(ctx, "files", remotePath)
	if err != nil {
		return err
	}
	defer remote.Close()
	blocks := make([]db.BackupPackBlock, 0, len(b.entries))
	entryIndex := 0
	validated, err := backuprepo.ValidatePack(remote, pack.ID, func(offset int64, entry backuprepo.Entry) error {
		if entryIndex >= len(b.entries) || entry.Hash != b.entries[entryIndex].Hash || !bytes.Equal(entry.Data, b.entries[entryIndex].Data) {
			return errors.New("remote pack catalog mismatch")
		}
		blocks = append(blocks, db.BackupPackBlock{Hash: entry.Hash[:], PlaintextSize: len(entry.Data), PayloadOffset: offset, PayloadLength: len(entry.Data)})
		entryIndex++
		return nil
	})
	if err != nil || validated.Size != pack.Size || entryIndex != len(b.entries) {
		return errors.New("remote pack validation failed")
	}
	ids, err := b.coordinator.store.RecordBackupPackAndBlocksContext(ctx, b.job.ID, remotePath, pack.ID[:], pack.Size, blocks)
	if err != nil {
		return err
	}
	for hash, id := range ids {
		b.ids[hash] = id
		delete(b.pending, hash)
	}
	b.entries = nil
	b.encodedSize = 16 + 20
	return nil
}

func ensureFormat(ctx context.Context, target packWriterTarget, root, repositoryID string) error {
	format := fmt.Sprintf("{\"repository_id\":%q,\"format_version\":1,\"block_size\":4194304,\"target_pack_size\":67108864,\"compression\":\"none\"}\n", repositoryID)
	formatPath, err := repositoryPath(root, "format-v1.json")
	if err != nil {
		return err
	}
	exists, size, err := target.FileExists(ctx, "files", formatPath)
	if err != nil {
		return err
	}
	if !exists {
		if err := target.CreateParentDirectories(ctx, "files", formatPath); err != nil {
			return err
		}
		return target.StreamUpload(ctx, "files", formatPath, strings.NewReader(format), int64(len(format)))
	}
	if size != int64(len(format)) {
		return errors.New("unexpected backup repository format size")
	}
	reader, err := target.StreamDownload(ctx, "files", formatPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, int64(len(format))+1))
	if err != nil || string(content) != format {
		return errors.New("unsupported backup repository format")
	}
	return nil
}

func scanFiles(ctx context.Context, source sourceScanner, selected []string, excludedRoot string) ([]scannedFile, []scannedDirectory, runStats, error) {
	files := make([]scannedFile, 0)
	directories := make([]scannedDirectory, 0)
	seen := make(map[string]struct{})
	var dirQueue []string

	for _, root := range selected {
		if err := ctx.Err(); err != nil {
			return nil, nil, runStats{}, err
		}
		if err := safeRemotePath(root); err != nil {
			return nil, nil, runStats{}, err
		}
		if isBackupPathWithin(excludedRoot, root) {
			continue
		}
		resource, err := source.InspectResource(ctx, "files", root)
		if err != nil {
			return nil, nil, runStats{}, err
		}
		if err := safeRemotePath(resource.Path); err != nil {
			return nil, nil, runStats{}, err
		}
		if isBackupPathWithin(excludedRoot, resource.Path) {
			continue
		}
		if _, skip := resource.NonTransferableReason(); skip {
			continue
		}
		if resource.IsDir {
			if _, ok := seen["d:"+resource.Path]; !ok {
				seen["d:"+resource.Path] = struct{}{}
				relative := strings.TrimPrefix(resource.Path, "/")
				if relative != "" {
					normRel, err := backuprepo.NormalizeRelativePath(relative)
					if err != nil {
						return nil, nil, runStats{}, err
					}
					directories = append(directories, scannedDirectory{relativePath: normRel, mtime: resource.LastModified})
				}
				dirQueue = append(dirQueue, resource.Path)
			}
			continue
		}
		relative, err := backuprepo.NormalizeRelativePath(strings.TrimPrefix(resource.Path, "/"))
		if err != nil {
			return nil, nil, runStats{}, err
		}
		if _, ok := seen["f:"+relative]; ok {
			continue
		}
		seen["f:"+relative] = struct{}{}
		files = append(files, scannedFile{
			sourcePath:   resource.Path,
			relativePath: relative,
			size:         resource.Size,
			mtime:        resource.LastModified,
		})
	}

	for len(dirQueue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, runStats{}, err
		}
		currentDir := dirQueue[0]
		dirQueue = dirQueue[1:]

		children, err := source.GetDirectoryListing(ctx, "files", currentDir)
		if err != nil {
			return nil, nil, runStats{}, err
		}
		canonicalCurrentDir := canonicalBackupScanPath(currentDir)
		for _, child := range children {
			if err := safeRemotePath(child.Path); err != nil {
				return nil, nil, runStats{}, err
			}
			canonicalChildPath := canonicalBackupScanPath(child.Path)
			// A DAV Depth:1 response may include the requested collection itself,
			// sometimes with a trailing slash. It is not a descendant and must not
			// be queued again.
			if canonicalChildPath == canonicalCurrentDir {
				continue
			}
			if !isBackupPathWithin(canonicalCurrentDir, canonicalChildPath) {
				return nil, nil, runStats{}, errors.New("backup listing returned a non-child path")
			}
			if isBackupPathWithin(excludedRoot, child.Path) {
				continue
			}
			if _, skip := child.NonTransferableReason(); skip {
				continue
			}
			if child.IsDir {
				if _, ok := seen["d:"+child.Path]; ok {
					continue
				}
				seen["d:"+child.Path] = struct{}{}
				relative := strings.TrimPrefix(child.Path, "/")
				if relative != "" {
					normRel, err := backuprepo.NormalizeRelativePath(relative)
					if err != nil {
						return nil, nil, runStats{}, err
					}
					directories = append(directories, scannedDirectory{relativePath: normRel, mtime: child.LastModified})
				}
				dirQueue = append(dirQueue, child.Path)
				continue
			}
			relative, err := backuprepo.NormalizeRelativePath(strings.TrimPrefix(child.Path, "/"))
			if err != nil {
				return nil, nil, runStats{}, err
			}
			if _, ok := seen["f:"+relative]; ok {
				continue
			}
			seen["f:"+relative] = struct{}{}
			files = append(files, scannedFile{
				sourcePath:   child.Path,
				relativePath: relative,
				size:         child.Size,
				mtime:        child.LastModified,
			})
		}
	}

	stats := runStats{totalFiles: len(files), totalDirs: len(directories)}
	for _, file := range files {
		stats.totalBytes += file.size
	}
	return files, directories, stats, nil
}

func (c *Coordinator) sameBackupConnection(ctx context.Context, job *db.BackupJob) (bool, error) {
	if job.SourceProfileID.Valid && job.TargetProfileID.Valid && job.SourceProfileID.String == job.TargetProfileID.String {
		return true, nil
	}
	if oauth.IsProvider(job.SourceProvider) || oauth.IsProvider(job.TargetProvider) {
		same, err := c.store.BackupProfilesSameOAuthAccountContext(ctx, job.SourceProfileID, job.TargetProfileID)
		if err != nil || same {
			return same, err
		}
	}
	return strings.EqualFold(job.SourceProvider, job.TargetProvider) &&
		strings.EqualFold(strings.TrimRight(job.SourceURL, "/"), strings.TrimRight(job.TargetURL, "/")) &&
		strings.EqualFold(strings.TrimSpace(job.SourceUsername), strings.TrimSpace(job.TargetUsername)), nil
}

func isBackupPathWithin(root, candidate string) bool {
	if root == "" {
		return false
	}
	return root == "/" || candidate == root || strings.HasPrefix(candidate, root+"/")
}

// canonicalBackupScanPath keeps listing validation stable across providers that
// represent collections with or without a trailing slash. It is used only for
// comparisons; the provider-supplied path is retained for subsequent I/O.
func canonicalBackupScanPath(value string) string {
	return path.Clean(value)
}

func repositoryPath(root string, parts ...string) (string, error) {
	if err := safeRemotePath(root); err != nil {
		return "", err
	}
	joined := path.Join(append([]string{root}, parts...)...)
	if err := safeRemotePath(joined); err != nil {
		return "", err
	}
	return joined, nil
}

func safeRemotePath(value string) error {
	if strings.TrimSpace(value) == "" || strings.Contains(value, "\\") {
		return errors.New("invalid backup repository path")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return errors.New("backup path escapes repository")
		}
	}
	return nil
}

func (c *Coordinator) finishOrFail(ctx context.Context, job *db.BackupJob, run *db.BackupRun, state string, err error, stats runStats) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger := backupRunLogger(ctx, job, run)
		logger.LogAttrs(ctx, slog.LevelInfo, "backup_run_cancelled",
			slog.String("state", state),
			observability.Error(err),
			slog.String("error_kind", observability.ErrorKind(err)),
		)
		finalized, finalizeErr := c.store.FinalizeBackupRunContext(context.Background(), job.ID, run.Generation, run.ID, state, "CANCELLED", stats.totalFiles, stats.totalBytes, stats.processedFiles, stats.processedBytes, stats.deduplicatedBytes, stats.failedFiles, nil)
		if finalizeErr != nil {
			logger.Error("backup_cancellation_persist_failed", observability.Error(finalizeErr), slog.String("error_kind", observability.ErrorKind(finalizeErr)))
		} else if !finalized {
			logger.Warn("backup_cancellation_claim_lost")
		}
		return
	}
	c.fail(ctx, job, run, failureCodeForState(state), err)
}

func (c *Coordinator) fail(ctx context.Context, job *db.BackupJob, run *db.BackupRun, code string, cause error) {
	logger := backupRunLogger(ctx, job, run)
	attrs := backupFailureAttrs(code, cause)
	attrs = append(attrs, slog.String("state", run.State))
	logger.LogAttrs(ctx, slog.LevelError, "backup_run_failed", attrs...)
	failed, err := c.store.FailBackupRunContext(context.Background(), job.ID, run.Generation, run.ID, run.State, code)
	if err != nil {
		logger.Error("backup_failure_persist_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	if !failed {
		logger.Warn("backup_failure_claim_lost")
		return
	}
	c.audit(job, db.AuditBackupFailed)
}

func failureCodeForState(state string) string {
	switch state {
	case "SCANNING":
		return "BACKUP_SCAN_FAILED"
	case "VERIFYING":
		return "BACKUP_VERIFICATION_FAILED"
	default:
		return "BACKUP_RUN_FAILED"
	}
}

func (c *Coordinator) markPackDamaged(job *db.BackupJob, packID string) {
	if err := c.store.MarkBackupPackDamagedContext(context.Background(), job.ID, packID); err == nil {
		c.audit(job, db.AuditBackupSnapshotDamaged)
	}
}

func (c *Coordinator) audit(job *db.BackupJob, action db.AuditAction) {
	if c.auditLog == nil {
		return
	}
	c.auditLog.WriteAuditLog(db.AuditEntry{
		UserID: sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		Action: action,
		Target: job.ID,
	})
}
