// Package backup coordinates Release-1 repository backups in worker processes.
package backup

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path"
	"strings"
	"time"

	"backend/internal/backuprepo"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/storage"
)

// Coordinator owns only backup worker execution. Its pack semaphore is process
// scoped, preventing independently claimed jobs from exhausting memory/network.
type Coordinator struct {
	db              *sql.DB
	encryptionKey   string
	packWriterSlots chan struct{}
}

// NewCoordinator configures a worker-side coordinator. maxPackWriters is the
// validated value of MAX_BACKUP_PACK_WRITERS supplied by worker configuration.
func NewCoordinator(database *sql.DB, encryptionKey string, maxPackWriters int) (*Coordinator, error) {
	if database == nil {
		return nil, errors.New("backup database is required")
	}
	if maxPackWriters < 1 || maxPackWriters > 4 {
		return nil, fmt.Errorf("MAX_BACKUP_PACK_WRITERS must be between 1 and 4")
	}
	return &Coordinator{db: database, encryptionKey: encryptionKey, packWriterSlots: make(chan struct{}, maxPackWriters)}, nil
}

// PollOnce claims and executes at most one queued run. Callers can invoke it
// from their existing worker poll loop without creating another scheduler.
func (c *Coordinator) PollOnce(ctx context.Context) (bool, error) {
	job, run, err := db.ClaimNextQueuedBackupRunContext(ctx, c.db)
	if err != nil {
		return false, err
	}
	if run == nil {
		return c.pollMaintenance(ctx)
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_LOCK_UNAVAILABLE")
		return true, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, job.LockID); err != nil {
		c.fail(ctx, job, run, "BACKUP_LOCK_UNAVAILABLE")
		return true, err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, job.LockID)
	c.execute(ctx, job, run)
	return true, nil
}

func (c *Coordinator) pollMaintenance(ctx context.Context) (bool, error) {
	maintenance, err := db.ClaimNextBackupMaintenanceContext(ctx, c.db)
	if err != nil || maintenance == nil {
		return maintenance != nil, err
	}
	conn, err := c.db.Conn(ctx)
	if err != nil {
		_ = db.RetryBackupMaintenanceContext(context.Background(), c.db, maintenance.ID, "BACKUP_LOCK_UNAVAILABLE")
		return true, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, maintenance.LockID); err != nil {
		_ = db.RetryBackupMaintenanceContext(context.Background(), c.db, maintenance.ID, "BACKUP_LOCK_UNAVAILABLE")
		return true, err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, maintenance.LockID)
	if err := c.executeMaintenance(ctx, maintenance); err != nil {
		_ = db.RetryBackupMaintenanceContext(context.Background(), c.db, maintenance.ID, "BACKUP_MAINTENANCE_FAILED")
		return true, err
	}
	return true, db.CompleteBackupMaintenanceContext(ctx, c.db, maintenance.ID)
}

func (c *Coordinator) executeMaintenance(ctx context.Context, maintenance *db.BackupMaintenance) error {
	job, err := db.GetBackupJobForOwnerContext(ctx, c.db, maintenance.BackupJobID, maintenance.UserID)
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
	default:
		return fmt.Errorf("unsupported backup maintenance %q", maintenance.Kind)
	}
}

func deleteRemotePack(ctx context.Context, target storage.StorageProvider, pack db.BackupPack) error {
	exists, _, err := target.FileExists(ctx, "files", pack.RemotePath)
	if err != nil {
		return err
	}
	if exists {
		return target.DeleteFile(ctx, "files", pack.RemotePath)
	}
	return nil
}

func (c *Coordinator) applyRetention(ctx context.Context, job *db.BackupJob, target storage.StorageProvider) error {
	if err := db.ExpireBackupSnapshotsContext(ctx, c.db, job.ID, job.RetentionCount); err != nil {
		return err
	}
	packs, err := db.ListBackupReclaimablePacksContext(ctx, c.db, job.ID, 1000)
	if err != nil {
		return err
	}
	for _, pack := range packs {
		if err := deleteRemotePack(ctx, target, pack); err != nil {
			return err
		}
		if err := db.MarkBackupPackDeletedContext(ctx, c.db, job.ID, pack.ID); err != nil {
			return err
		}
	}
	candidate, err := db.FindBackupCompactionCandidateContext(ctx, c.db, job.ID)
	if err != nil {
		return err
	}
	if candidate != nil {
		// This maintenance claim holds the job advisory lock. The queued claim
		// runs only after this lock is released, so it cannot overlap retention.
		return db.EnqueueBackupMaintenanceContext(ctx, c.db, job.ID, "COMPACTION")
	}
	return nil
}

func (c *Coordinator) compactOnePack(ctx context.Context, job *db.BackupJob, target storage.StorageProvider) error {
	pack, err := db.FindBackupCompactionCandidateContext(ctx, c.db, job.ID)
	if err != nil || pack == nil {
		return err
	}
	liveBlocks, err := db.ListBackupLiveBlocksContext(ctx, c.db, job.ID, pack.ID)
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
	if _, err := db.ReplaceBackupPackLocatorsContext(ctx, c.db, job.ID, pack.ID, remotePath, replacement.ID[:], replacement.Size, locators, blockIDs); err != nil {
		return err
	}
	if err := deleteRemotePack(ctx, target, *pack); err != nil {
		return err
	}
	if err := db.MarkBackupPackDeletedContext(ctx, c.db, job.ID, pack.ID); err != nil {
		return err
	}
	if next, err := db.FindBackupCompactionCandidateContext(ctx, c.db, job.ID); err != nil {
		return err
	} else if next != nil {
		return db.EnqueueBackupMaintenanceContext(ctx, c.db, job.ID, "COMPACTION")
	}
	return nil
}

func (c *Coordinator) deleteRepository(ctx context.Context, maintenance *db.BackupMaintenance, job *db.BackupJob, target storage.StorageProvider) error {
	packs, err := db.ListBackupPacksForDeletionContext(ctx, c.db, job.ID, 1000)
	if err != nil {
		return err
	}
	for _, pack := range packs {
		if err := deleteRemotePack(ctx, target, pack); err != nil {
			return err
		}
		if err := db.MarkBackupPackDeletedContext(ctx, c.db, job.ID, pack.ID); err != nil {
			return err
		}
	}
	if len(packs) == 1000 {
		// The claim's object budget is complete. A subsequent request resumes
		// from catalog state instead of relying on a remote directory listing.
		return db.RetryBackupMaintenanceContext(ctx, c.db, maintenance.ID, "")
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
	deleted, err := db.DeleteBackupJobAfterRepositoryCleanupContext(ctx, c.db, job.ID)
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := c.PollOnce(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
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
	if job.SourceProvider == "immich" || job.TargetProvider == "immich" ||
		!storage.ProviderSupportsResourceType(job.SourceProvider, "files") ||
		!storage.ProviderSupportsResourceType(job.TargetProvider, "files") || len(job.SelectedPaths) == 0 {
		c.fail(ctx, job, run, "BACKUP_FILES_UNSUPPORTED")
		return
	}
	ctx = storage.WithLocalUserScope(ctx, job.UserID)
	source, target, err := c.providers(ctx, job)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED")
		return
	}
	defer source.Close()
	defer target.Close()
	if ok, err := source.Connect(ctx); err != nil || !ok {
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED")
		return
	}
	if ok, err := target.Connect(ctx); err != nil || !ok {
		c.fail(ctx, job, run, "BACKUP_CONNECTION_FAILED")
		return
	}
	if err := ensureDedicatedTarget(ctx, target, job.TargetDir, job.RepositoryID); err != nil {
		c.fail(ctx, job, run, "BACKUP_TARGET_NOT_EMPTY")
		return
	}
	if err := ensureFormat(ctx, target, job.RepositoryRoot, job.RepositoryID); err != nil {
		c.fail(ctx, job, run, "BACKUP_REPOSITORY_FORMAT_INVALID")
		return
	}
	files, directories, stats, err := scanFiles(ctx, source, job.SelectedPaths)
	if err != nil {
		c.finishOrFail(ctx, job, run, "SCANNING", err, stats)
		return
	}
	// Hold this permit from the first source read through the final flush. This
	// bounds an admitted job's pack buffer rather than merely serializing upload.
	select {
	case c.packWriterSlots <- struct{}{}:
	case <-ctx.Done():
		c.finishOrFail(ctx, job, run, "SCANNING", ctx.Err(), stats)
		return
	}
	defer func() { <-c.packWriterSlots }()
	if ok, err := db.TransitionBackupRunContext(ctx, c.db, job.ID, run.Generation, run.ID, "SCANNING", "RUNNING"); err != nil || !ok {
		c.fail(ctx, job, run, "BACKUP_TRANSITION_FAILED")
		return
	}
	run.State = "RUNNING"
	snapshotID, err := db.CreateBackupSnapshotDraftContext(ctx, c.db, job.ID, run.ID, job.SelectedPaths)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED")
		return
	}
	for _, directory := range directories {
		if _, err := db.CreateBackupSnapshotDirectoryContext(ctx, c.db, snapshotID, directory.relativePath, directory.mtime); err != nil {
			c.finishOrFail(ctx, job, run, "RUNNING", err, stats)
			return
		}
	}
	builder := newPackBuilder(c, job, target)
	pending := make([]pendingFile, 0, len(files))
	for _, file := range files {
		if !c.runActive(ctx, job, run, snapshotID) {
			return
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
	for _, item := range pending {
		for i, id := range item.blockIDs {
			item.blockIDs[i] = builder.blockID(id)
		}
		itemID, err := db.CreateBackupSnapshotItemContext(ctx, c.db, snapshotID, item.file.relativePath, item.file.size, item.file.mtime, item.fileHash[:], "AVAILABLE", "")
		if err != nil || db.LinkBackupSnapshotItemBlocksContext(ctx, c.db, itemID, item.blockIDs) != nil {
			c.finishOrFail(ctx, job, run, "RUNNING", errors.New("backup catalog write failed"), stats)
			return
		}
		stats.processedFiles++
		stats.processedBytes += item.file.size
	}
	if !c.runActive(ctx, job, run, snapshotID) {
		return
	}
	if ok, err := db.TransitionBackupRunContext(ctx, c.db, job.ID, run.Generation, run.ID, "RUNNING", "VERIFYING"); err != nil || !ok {
		c.fail(ctx, job, run, "BACKUP_TRANSITION_FAILED")
		return
	}
	run.State = "VERIFYING"
	if err := c.verifySnapshotSample(ctx, job, snapshotID, target); err != nil {
		c.fail(ctx, job, run, "BACKUP_VERIFICATION_FAILED")
		return
	}
	snapshotState, runState := "READY", "COMPLETED"
	if stats.unstableFiles > 0 {
		snapshotState, runState = "PARTIAL", "PARTIAL"
	}
	published, err := db.PublishBackupSnapshotAndFinalizeContext(ctx, c.db, job.ID, run.Generation, run.ID, snapshotID, snapshotState, runState, stats.totalFiles, stats.totalDirs, stats.totalBytes, stats.processedFiles, stats.processedBytes, stats.deduplicatedBytes, stats.unstableFiles, stats.failedFiles)
	if err != nil || !published {
		c.fail(ctx, job, run, "BACKUP_PUBLICATION_FAILED")
		return
	}
	if runState == "PARTIAL" {
		c.audit(job, db.AuditBackupPartial)
	} else {
		c.audit(job, db.AuditBackupCompleted)
	}
}

// verifySnapshotSample checks a cryptographically selected subset of packs
// before publication. Each selected pack is fully parsed, which validates its
// framing, pack ID, and every block without requiring range-read support.
func (c *Coordinator) verifySnapshotSample(ctx context.Context, job *db.BackupJob, snapshotID string, target storage.StorageProvider) error {
	packs, err := db.ListBackupSnapshotPacksContext(ctx, c.db, job.ID, snapshotID)
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
	return db.MarkBackupPacksCheckedContext(ctx, c.db, job.ID, checked)
}

func (c *Coordinator) runActive(ctx context.Context, job *db.BackupJob, run *db.BackupRun, snapshotID string) bool {
	active, err := db.BackupRunActiveContext(ctx, c.db, job.ID, run.Generation, run.ID)
	if err != nil {
		c.fail(ctx, job, run, "BACKUP_CATALOG_FAILED")
		return false
	}
	if !active {
		_ = db.DiscardBackupSnapshotDraftContext(context.Background(), c.db, snapshotID, job.ID, run.ID)
		return false
	}
	return true
}

func ensureDedicatedTarget(ctx context.Context, target storage.StorageProvider, targetDir, repositoryID string) error {
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
	if len(entries) != 1 || entries[0].Path != container || !entries[0].IsDir {
		return errors.New("backup target directory is not empty")
	}
	entries, err = target.GetDirectoryListing(ctx, "files", container)
	if err != nil || len(entries) != 1 {
		return errors.New("backup target directory is not dedicated")
	}
	repositoryRoot, err := repositoryPath(container, repositoryID)
	if err != nil || entries[0].Path != repositoryRoot || !entries[0].IsDir {
		return errors.New("backup target directory belongs to another repository")
	}
	return nil
}

func (c *Coordinator) providers(ctx context.Context, job *db.BackupJob) (storage.StorageProvider, storage.StorageProvider, error) {
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
	sourceCtx, err := megasecret.WithMegaSession(ctx, job.SourceProvider, job.SourceMegaSessionIDEncrypted.String, job.SourceMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return nil, nil, err
	}
	targetCtx, err := megasecret.WithMegaSession(ctx, job.TargetProvider, job.TargetMegaSessionIDEncrypted.String, job.TargetMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return nil, nil, err
	}
	source, err := storage.NewProvider(sourceCtx, job.SourceProvider, job.SourceURL, job.SourceUsername, string(sourceBytes))
	if err != nil {
		return nil, nil, err
	}
	target, err := storage.NewProvider(targetCtx, job.TargetProvider, job.TargetURL, job.TargetUsername, string(targetBytes))
	if err != nil {
		source.Close()
		return nil, nil, err
	}
	return source, target, nil
}

func (c *Coordinator) backupFile(ctx context.Context, job *db.BackupJob, source storage.StorageProvider, file scannedFile, stats *runStats, builder *packBuilder) (pendingFile, bool, error) {
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
		blockIDs := make([]string, 0)
		fileHash, err := backuprepo.SplitBlocks(ctx, reader, func(entry backuprepo.Entry) error {
			blockID, found, err := db.FindBackupBlockContext(ctx, c.db, job.ID, entry.Hash[:])
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
			blockIDs = append(blockIDs, string(entry.Hash[:]))
			return nil
		})
		closeErr := reader.Close()
		if err != nil {
			return pendingFile{}, false, err
		}
		if closeErr != nil {
			return pendingFile{}, false, closeErr
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
	target      storage.StorageProvider
	entries     []backuprepo.Entry
	encodedSize int64
	ids         map[string]string
}

func newPackBuilder(coordinator *Coordinator, job *db.BackupJob, target storage.StorageProvider) *packBuilder {
	return &packBuilder{coordinator: coordinator, job: job, target: target, encodedSize: 16 + 20, ids: make(map[string]string)}
}

func (b *packBuilder) add(ctx context.Context, entry backuprepo.Entry) error {
	entrySize := int64(sha256.Size + 4 + len(entry.Data))
	if b.encodedSize+entrySize > backuprepo.MaxPackSize && len(b.entries) > 0 {
		if err := b.flush(ctx); err != nil {
			return err
		}
	}
	b.entries = append(b.entries, entry)
	b.encodedSize += entrySize
	return nil
}

func (b *packBuilder) blockID(key string) string { return b.ids[key] }

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
	ids, err := db.RecordBackupPackAndBlocksContext(ctx, b.coordinator.db, b.job.ID, remotePath, pack.ID[:], pack.Size, blocks)
	if err != nil {
		return err
	}
	for hash, id := range ids {
		b.ids[hash] = id
	}
	b.entries = nil
	b.encodedSize = 16 + 20
	return nil
}

func ensureFormat(ctx context.Context, target storage.StorageProvider, root, repositoryID string) error {
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

func scanFiles(ctx context.Context, source storage.StorageProvider, selected []string) ([]scannedFile, []scannedDirectory, runStats, error) {
	files := make([]scannedFile, 0)
	directories := make([]scannedDirectory, 0)
	seen := make(map[string]struct{})
	queue := append([]string(nil), selected...)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, runStats{}, err
		}
		current := queue[0]
		queue = queue[1:]
		if err := safeRemotePath(current); err != nil {
			return nil, nil, runStats{}, err
		}
		resource, err := source.InspectResource(ctx, "files", current)
		if err != nil {
			return nil, nil, runStats{}, err
		}
		if err := safeRemotePath(resource.Path); err != nil {
			return nil, nil, runStats{}, err
		}
		if resource.IsDir {
			if _, ok := seen["d:"+resource.Path]; ok {
				continue
			}
			seen["d:"+resource.Path] = struct{}{}
			relative := strings.TrimPrefix(resource.Path, "/")
			if relative != "" {
				relative, err = backuprepo.NormalizeRelativePath(relative)
				if err != nil {
					return nil, nil, runStats{}, err
				}
				directories = append(directories, scannedDirectory{relativePath: relative, mtime: resource.LastModified})
			}
			children, err := source.GetDirectoryListing(ctx, "files", resource.Path)
			if err != nil {
				return nil, nil, runStats{}, err
			}
			for _, child := range children {
				queue = append(queue, child.Path)
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
		files = append(files, scannedFile{sourcePath: resource.Path, relativePath: relative, size: resource.Size, mtime: resource.LastModified})
	}
	stats := runStats{totalFiles: len(files), totalDirs: len(directories)}
	for _, file := range files {
		stats.totalBytes += file.size
	}
	return files, directories, stats, nil
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
		_, _ = db.FinalizeBackupRunContext(context.Background(), c.db, job.ID, run.Generation, run.ID, state, "CANCELLED", stats.totalFiles, stats.totalBytes, stats.processedFiles, stats.processedBytes, stats.deduplicatedBytes, stats.failedFiles, nil)
		return
	}
	c.fail(ctx, job, run, failureCodeForState(state))
}

func (c *Coordinator) fail(ctx context.Context, job *db.BackupJob, run *db.BackupRun, code string) {
	failed, err := db.FailBackupRunContext(context.Background(), c.db, job.ID, run.Generation, run.ID, run.State, code)
	if err == nil && failed {
		c.audit(job, db.AuditBackupFailed)
	}
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
	if err := db.MarkBackupPackDamagedContext(context.Background(), c.db, job.ID, packID); err == nil {
		c.audit(job, db.AuditBackupSnapshotDamaged)
	}
}

func (c *Coordinator) audit(job *db.BackupJob, action db.AuditAction) {
	db.WriteAuditLog(c.db, db.AuditEntry{
		UserID: sql.NullString{String: job.UserID, Valid: job.UserID != ""},
		Action: action,
		Target: job.ID,
	})
}
