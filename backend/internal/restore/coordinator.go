// Package restore owns durable restore preview work. File reconstruction is
// deliberately separate so preview workers never mutate a target provider.
package restore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type Coordinator struct {
	db            *sql.DB
	encryptionKey string
	packReaders   *PackReaderLimiter
	workerID      string
}

var ErrRestoreTypeConflict = errors.New("restore target type conflict")

func NewCoordinator(database *sql.DB, encryptionKey string, packReaders ...*PackReaderLimiter) (*Coordinator, error) {
	if database == nil || encryptionKey == "" {
		return nil, errors.New("restore database is required")
	}
	var limiter *PackReaderLimiter
	if len(packReaders) > 1 {
		return nil, errors.New("only one restore pack reader limiter may be configured")
	}
	if len(packReaders) == 1 {
		limiter = packReaders[0]
	}
	return &Coordinator{db: database, encryptionKey: encryptionKey, packReaders: limiter, workerID: "restore-worker"}, nil
}

// SetWorkerID binds claims and heartbeats to the process liveness identity.
// It is optional for narrow unit tests, but production workers always set it.
func (c *Coordinator) SetWorkerID(workerID string) {
	if strings.TrimSpace(workerID) != "" {
		c.workerID = workerID
	}
}

// PollOnce claims a single preview. The durable status transition is the
// worker fence; a crash leaves RUNNING work for the upcoming lease recovery.
func (c *Coordinator) PollOnce(ctx context.Context) (bool, error) {
	if recovered, err := db.RecoverStaleRestoreItemsContext(ctx, c.db); err != nil {
		return false, fmt.Errorf("recover stale restore items: %w", err)
	} else if recovered > 0 {
		return true, nil
	}
	if err := db.ExpireRestorePreviewsContext(ctx, c.db); err != nil {
		return false, fmt.Errorf("expire restore previews: %w", err)
	}
	if err := db.RecoverStaleRestorePlanningContext(ctx, c.db); err != nil {
		return false, fmt.Errorf("recover stale restore planning: %w", err)
	}
	preview, err := db.ClaimNextRestorePreviewForWorkerContext(ctx, c.db, c.workerID)
	if err != nil {
		return false, err
	}
	if preview != nil {
		previewCtx, cancelPreview := context.WithCancel(ctx)
		heartbeatDone := make(chan struct{})
		go c.heartbeatPreview(previewCtx, preview, heartbeatDone, cancelPreview)
		stats, err := c.previewTarget(previewCtx, preview)
		cancelPreview()
		<-heartbeatDone
		if err != nil {
			if failErr := db.FailRestorePreviewFencedContext(context.Background(), c.db, preview.ID, preview.CoordinatorGeneration, c.workerID, "RESTORE_PREVIEW_FAILED"); failErr != nil {
				return true, fmt.Errorf("count restore preview: %w; persist failure: %v", err, failErr)
			}
			db.WriteAuditLog(c.db, db.AuditEntry{UserID: sql.NullString{String: preview.UserID, Valid: true}, Action: db.AuditRestorePreviewFailed, Target: preview.ID})
			return true, nil
		}
		if _, err := db.CompleteRestorePreviewWithStatsFencedContext(ctx, c.db, preview.ID, preview.CoordinatorGeneration, c.workerID, stats); err != nil {
			return true, fmt.Errorf("complete restore preview: %w", err)
		}
		db.WriteAuditLog(c.db, db.AuditEntry{UserID: sql.NullString{String: preview.UserID, Valid: true}, Action: db.AuditRestorePreviewReady, Target: preview.ID})
		return true, nil
	}

	job, run, err := db.ClaimNextQueuedRestoreRunForWorkerContext(ctx, c.db, c.workerID)
	if err != nil {
		return false, err
	}
	if run != nil {
		if _, err := db.PlanRestoreRunContext(ctx, c.db, job, run); err != nil {
			if _, failErr := db.FailRestoreRunPlanningContext(context.Background(), c.db, job.ID, run.ID, run.Generation, "RESTORE_PLANNING_FAILED"); failErr != nil {
				return true, fmt.Errorf("plan restore run: %w; persist failure: %v", err, failErr)
			}
			return true, nil
		}
		return true, nil
	}
	if skipped, err := db.SkipBlockedRestoreItemsContext(ctx, c.db); err != nil {
		return false, err
	} else if skipped > 0 {
		return true, nil
	}
	item, err := db.ClaimNextRestoreItemForWorkerContext(ctx, c.db, c.workerID)
	if err != nil {
		return false, err
	}
	if item == nil {
		finalized, err := db.FinalizeCompletedRestoreRunsContext(ctx, c.db)
		if err != nil {
			return false, err
		}
		cancelled, err := db.FinalizeCancellingRestoreRunsContext(ctx, c.db)
		return finalized > 0 || cancelled > 0, err
	}
	if err := c.restoreItem(ctx, item); err != nil {
		failureCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		errorCode := "RESTORE_TRANSFER_FAILED"
		if errors.Is(err, ErrRestoreTypeConflict) {
			errorCode = "RESTORE_TYPE_CONFLICT"
		}
		if isPermanentRestoreError(err) {
			_ = db.FailRestoreItemContext(failureCtx, c.db, item.ID, item.RestoreRunID, errorCode, item.ClaimEpoch)
		} else {
			_, _ = db.RetryRestoreItemContext(failureCtx, c.db, item.ID, item.RestoreRunID, errorCode, item.ClaimEpoch)
		}
	}
	return true, nil
}

// previewTarget enumerates the selected immutable source catalog and the
// target tree without mutating it. The output remains bounded: only aggregate
// counters plus the first 100 deterministic conflict examples are stored.
func (c *Coordinator) previewTarget(ctx context.Context, preview *db.RestorePreview) (db.RestorePreviewStats, error) {
	items, err := db.ListRestorePreviewItemsContext(ctx, c.db, preview)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	if preview.TargetPasswordEncrypted.String == "" && preview.TargetProvider != "local" {
		return db.RestorePreviewStats{}, errors.New("restore preview target credential is unavailable")
	}
	providerCtx := storage.WithLocalUserScope(ctx, preview.UserID)
	providerCtx, err = megasecret.WithMegaSession(providerCtx, preview.TargetProvider, preview.TargetMegaSessionIDEncrypted.String, preview.TargetMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	target, err := c.newProvider(providerCtx, preview.TargetProvider, preview.TargetURL, preview.TargetUsername, preview.TargetPasswordEncrypted.String)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	defer target.Close()
	if ok, err := target.Connect(ctx); err != nil || !ok {
		return db.RestorePreviewStats{}, errors.New("restore preview target connection failed")
	}

	expected := make(map[string]db.RestorePreviewItem, len(items))
	for _, item := range items {
		resolved := previewTargetPath(preview.TargetRoot, item.RelativePath)
		expected[resolved] = item
	}
	targetTree, err := enumeratePreviewTargetTree(ctx, target, preview.TargetRoot)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	stats := db.RestorePreviewStats{}
	examples := make([]map[string]string, 0, 100)
	paths := make([]string, 0, len(expected))
	for targetPath := range expected {
		paths = append(paths, targetPath)
	}
	sort.Strings(paths)
	_, metadataSupported := target.(storage.MetadataApplier)
	for _, targetPath := range paths {
		item := expected[targetPath]
		if item.IsDir {
			stats.Directories++
		} else {
			stats.Files++
			stats.Bytes += item.SizeBytes
		}
		if !item.IsDir && item.State != "AVAILABLE" {
			stats.UnavailableItems++
			continue
		}
		if !metadataSupported && (item.SourceMTime.Valid || len(item.Metadata) > 0 && string(item.Metadata) != "null") {
			stats.MetadataWarnings++
		}
		resource, exists := targetTree[targetPath]
		if !exists {
			continue
		}
		outcome := ""
		if item.IsDir && resource.IsDir {
			stats.MergeableDirectories++
			continue
		}
		if !item.IsDir && !resource.IsDir {
			stats.ExistingFiles++
			if preview.ConflictStrategy == "SKIP" {
				stats.ExpectedSkips++
				outcome = "SKIP"
			}
			if preview.ConflictStrategy == "RENAME" {
				stats.ExpectedRenames++
				outcome = "RENAME"
			}
		} else {
			stats.TypeConflicts++
			switch preview.ConflictStrategy {
			case "SKIP":
				stats.ExpectedSkips++
				outcome = "TYPE_SKIP"
			case "RENAME":
				stats.ExpectedRenames++
				outcome = "TYPE_RENAME"
			default:
				outcome = "TYPE_OVERWRITE"
			}
		}
		if outcome != "" && len(examples) < 100 {
			examples = append(examples, map[string]string{"path": strings.TrimPrefix(targetPath, "/"), "outcome": outcome})
		}
	}
	encoded, err := json.Marshal(examples)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	stats.ConflictExamples = encoded
	return stats, nil
}

func previewTargetPath(root, relative string) string {
	if relative == "" {
		return path.Clean(root)
	}
	if root == "/" {
		return "/" + strings.TrimPrefix(relative, "/")
	}
	return path.Join(root, relative)
}

func enumeratePreviewTargetTree(ctx context.Context, target storage.StorageProvider, root string) (map[string]storage.CloudResource, error) {
	result := make(map[string]storage.CloudResource)
	root = path.Clean(root)
	if root == "." {
		root = "/"
	}
	if root != "/" {
		exists, _, err := target.FileExists(ctx, "files", root)
		if err != nil {
			return nil, err
		}
		if !exists {
			return result, nil
		}
	}
	queue := []string{root}
	visited := make(map[string]struct{}, 16)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}
		entries, err := target.GetDirectoryListing(ctx, "files", current)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			clean := path.Clean(entry.Path)
			if !strings.HasPrefix(clean, "/") {
				clean = "/" + clean
			}
			result[clean] = entry
			if entry.IsDir {
				queue = append(queue, clean)
			}
		}
	}
	return result, nil
}

func (c *Coordinator) heartbeatPreview(ctx context.Context, preview *db.RestorePreview, done chan<- struct{}, cancel context.CancelFunc) {
	defer close(done)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, heartbeatCancel := context.WithTimeout(context.Background(), 5*time.Second)
			owned, err := db.RenewRestorePreviewLeaseContext(heartbeatCtx, c.db, preview.ID, preview.CoordinatorGeneration, c.workerID)
			heartbeatCancel()
			if err != nil || !owned {
				cancel()
				return
			}
		}
	}
}

func isPermanentRestoreError(err error) bool {
	return errors.Is(err, ErrRepositoryCorrupt) ||
		errors.Is(err, ErrRestoreTypeConflict) ||
		errors.Is(err, storage.ErrAuth) ||
		errors.Is(err, storage.ErrPermanentTransfer) ||
		errors.Is(err, storage.ErrUnsupportedResourceType) ||
		errors.Is(err, storage.ErrPathEscapesRoot) ||
		errors.Is(err, storage.ErrUnsupportedOnPlatform)
}

// ensureFreshRestoreTargetOAuthToken refreshes only the active run's snapshot.
// A retry always receives a fresh preview, and terminalization clears this
// snapshot, so neither a profile nor a historical restore job retains a
// runnable direct credential.
func (c *Coordinator) ensureFreshRestoreTargetOAuthToken(ctx context.Context, runID, provider, accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime) (string, error) {
	if !oauth.IsProvider(provider) {
		return accessEncrypted, nil
	}
	if !refreshEncrypted.Valid || refreshEncrypted.String == "" {
		return "", errors.New("restore OAuth refresh credential is unavailable")
	}
	if expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessEncrypted, nil
	}
	refresh, err := crypto.DecryptWithDomain(refreshEncrypted.String, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt restore OAuth refresh token: %w", err)
	}
	defer crypto.ZeroString(&refresh)
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	token, err := oauth.RefreshToken(refreshCtx, provider, refresh)
	cancel()
	if err != nil {
		return "", fmt.Errorf("refresh restore OAuth token: %w", err)
	}
	access, err := crypto.EncryptWithDomain(token.AccessToken, c.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", err
	}
	rotatedRefresh, err := crypto.EncryptWithDomain(token.RefreshToken, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", err
	}
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	if err := db.UpdateRestoreRunOAuthTokens(ctx, c.db, runID, access, rotatedRefresh, time.Now().Add(time.Duration(expiresIn)*time.Second), refreshEncrypted.String); err != nil {
		if !errors.Is(err, db.ErrOAuthTokenConflict) {
			return "", fmt.Errorf("persist restore OAuth token: %w", err)
		}
		latestAccess, _, latestExpires, latestErr := db.GetRestoreRunCredentialSnapshotContext(ctx, c.db, runID)
		if latestErr != nil || latestAccess == "" || !latestExpires.Valid {
			return "", fmt.Errorf("adopt concurrent restore OAuth token: %w", latestErr)
		}
		return latestAccess, nil
	}
	return access, nil
}

func (c *Coordinator) ensureFreshRepositoryOAuthToken(ctx context.Context, connection *db.BackupRepositoryConnection) (string, error) {
	if connection == nil || !oauth.IsProvider(connection.Provider) {
		if connection == nil { return "", errors.New("restore repository connection is unavailable") }
		return connection.PasswordEncrypted, nil
	}
	if !connection.RefreshTokenEncrypted.Valid || connection.RefreshTokenEncrypted.String == "" {
		return "", errors.New("backup repository OAuth refresh credential is unavailable")
	}
	if connection.TokenExpiresAt.Valid && time.Now().Before(connection.TokenExpiresAt.Time.Add(-2*time.Minute)) {
		return connection.PasswordEncrypted, nil
	}
	refresh, err := crypto.DecryptWithDomain(connection.RefreshTokenEncrypted.String, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil { return "", err }
	defer crypto.ZeroString(&refresh)
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	token, err := oauth.RefreshToken(refreshCtx, connection.Provider, refresh)
	cancel()
	if err != nil { return "", fmt.Errorf("refresh backup repository OAuth token: %w", err) }
	accessEncrypted, err := crypto.EncryptWithDomain(token.AccessToken, c.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil { return "", err }
	refreshEncrypted, err := crypto.EncryptWithDomain(token.RefreshToken, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil { return "", err }
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 { expiresIn = 3600 }
	if err := db.UpdateBackupRepositoryOAuthTokens(ctx, c.db, connection.BackupJobID, accessEncrypted, refreshEncrypted, time.Now().Add(time.Duration(expiresIn)*time.Second), connection.RefreshTokenEncrypted.String); err != nil {
		if !errors.Is(err, db.ErrOAuthTokenConflict) { return "", err }
		latest, loadErr := db.GetBackupRepositoryConnectionContext(ctx, c.db, connection.BackupJobID)
		if loadErr != nil || latest.PasswordEncrypted == "" { return "", fmt.Errorf("adopt concurrent backup repository OAuth token: %w", loadErr) }
		return latest.PasswordEncrypted, nil
	}
	return accessEncrypted, nil
}

func (c *Coordinator) restoreItem(ctx context.Context, item *db.RestoreItem) error {
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go c.heartbeatItem(heartbeatCtx, item, heartbeatDone)
	defer func() {
		cancelHeartbeat()
		<-heartbeatDone
	}()

	var backupJobID, targetProviderType, targetURL, targetUsername, conflictStrategy, targetPasswordEncrypted string
	var bandwidthMbps int
	var targetRefreshEncrypted, targetMegaSessionEncrypted, targetMegaMasterKeyEncrypted sql.NullString
	var targetTokenExpiresAt sql.NullTime
	err := c.db.QueryRowContext(ctx, `
		SELECT j.backup_job_id, j.target_provider, j.target_url, j.target_username, j.conflict_strategy,
			r.bandwidth_mbps, COALESCE(r.target_password_encrypted, ''), r.target_refresh_token_encrypted,
			r.target_token_expires_at, r.target_mega_session_id_encrypted, r.target_mega_master_key_encrypted
		FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.id = $1`, item.RestoreRunID).Scan(
		&backupJobID, &targetProviderType, &targetURL, &targetUsername, &conflictStrategy, &bandwidthMbps,
		&targetPasswordEncrypted, &targetRefreshEncrypted, &targetTokenExpiresAt, &targetMegaSessionEncrypted, &targetMegaMasterKeyEncrypted)
	if err != nil {
		return fmt.Errorf("load restore connections: %w", err)
	}
	repository, err := db.GetBackupRepositoryConnectionContext(ctx, c.db, backupJobID)
	if err != nil {
		return err
	}
	repository.PasswordEncrypted, err = c.ensureFreshRepositoryOAuthToken(ctx, repository)
	if err != nil { return err }
	if targetPasswordEncrypted == "" && targetProviderType != "local" {
		return errors.New("restore target credential snapshot is unavailable")
	}
	targetPasswordEncrypted, err = c.ensureFreshRestoreTargetOAuthToken(ctx, item.RestoreRunID, targetProviderType, targetPasswordEncrypted, targetRefreshEncrypted, targetTokenExpiresAt)
	if err != nil {
		return err
	}
	ctx = storage.WithLocalUserScope(ctx, repository.UserID)
	repositoryCtx, err := megasecret.WithMegaSession(ctx, repository.Provider, repository.MegaSessionIDEncrypted.String, repository.MegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return fmt.Errorf("load repository mega session: %w", err)
	}
	repositoryProvider, err := c.newProvider(repositoryCtx, repository.Provider, repository.URL, repository.Username, repository.PasswordEncrypted)
	if err != nil {
		return err
	}
	defer repositoryProvider.Close()
	targetCtx, err := megasecret.WithMegaSession(ctx, targetProviderType, targetMegaSessionEncrypted.String, targetMegaMasterKeyEncrypted.String, c.encryptionKey)
	if err != nil {
		return fmt.Errorf("load restore target mega session: %w", err)
	}
	targetProvider, err := c.newProvider(targetCtx, targetProviderType, targetURL, targetUsername, targetPasswordEncrypted)
	if err != nil {
		return err
	}
	defer targetProvider.Close()
	if ok, err := repositoryProvider.Connect(ctx); err != nil || !ok {
		return errors.New("restore repository connection failed")
	}
	if megaProvider, ok := repositoryProvider.(*storage.MegaProvider); ok {
		session := megaProvider.Session()
		if session.ID != "" && len(session.MasterKey) > 0 {
			sessionIDEncrypted, encryptErr := crypto.EncryptWithDomain(session.ID, c.encryptionKey, crypto.DomainMegaSessionID)
			masterKeyEncrypted := ""
			if encryptErr == nil { masterKeyEncrypted, encryptErr = crypto.EncryptWithDomain(base64.StdEncoding.EncodeToString(session.MasterKey), c.encryptionKey, crypto.DomainMegaMasterKey) }
			for i := range session.MasterKey { session.MasterKey[i] = 0 }
			crypto.ZeroString(&session.ID)
			if encryptErr != nil { return fmt.Errorf("encrypt backup repository mega session: %w", encryptErr) }
			if err := db.UpdateBackupRepositoryMegaSessionContext(ctx, c.db, repository.BackupJobID, sessionIDEncrypted, masterKeyEncrypted); err != nil { return fmt.Errorf("persist backup repository mega session: %w", err) }
		}
	}
	if ok, err := targetProvider.Connect(ctx); err != nil || !ok {
		return errors.New("restore target connection failed")
	}
	if megaProvider, ok := targetProvider.(*storage.MegaProvider); ok {
		session := megaProvider.Session()
		if session.ID != "" && len(session.MasterKey) > 0 {
			sessionIDEncrypted, encryptErr := crypto.EncryptWithDomain(session.ID, c.encryptionKey, crypto.DomainMegaSessionID)
			masterKeyEncrypted := ""
			if encryptErr == nil {
				masterKeyEncrypted, encryptErr = crypto.EncryptWithDomain(base64.StdEncoding.EncodeToString(session.MasterKey), c.encryptionKey, crypto.DomainMegaMasterKey)
			}
			for i := range session.MasterKey {
				session.MasterKey[i] = 0
			}
			crypto.ZeroString(&session.ID)
			if encryptErr != nil {
				return fmt.Errorf("encrypt restore mega session: %w", encryptErr)
			}
			if err := db.UpdateRestoreRunMegaSessionContext(ctx, c.db, item.RestoreRunID, sessionIDEncrypted, masterKeyEncrypted); err != nil {
				return fmt.Errorf("persist restore mega session: %w", err)
			}
		}
	}
	throttler := throttle.NewMigrationThrottler(bandwidthMbps)
	if sanitized := sanitize.SanitizeFilename(path.Base(item.TargetPath), targetProviderType); sanitized.Changed {
		resolvedPath := path.Join(path.Dir(item.TargetPath), sanitized.SanitizedName)
		if item.IsDir {
			if err := db.SetRestoreDirectoryTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, item.SnapshotRelativePath, resolvedPath, item.ClaimEpoch); err != nil {
				return err
			}
		} else if err := db.SetRestoreItemTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, resolvedPath, item.ClaimEpoch); err != nil {
			return err
		}
		item.TargetPath = resolvedPath
	}
	if err := c.reserveRestoreTargetPath(ctx, item, item.IsDir); err != nil {
		return err
	}
	if item.IsDir {
		return c.restoreDirectory(ctx, targetProvider, item, conflictStrategy)
	}
	exists, _, err := targetProvider.FileExists(ctx, "files", item.TargetPath)
	if err != nil {
		return err
	}
	overwriteExisting := false
	if exists {
		resource, err := targetProvider.InspectResource(ctx, "files", item.TargetPath)
		if err != nil {
			return err
		}
		if resource.IsDir {
			// A restore must never recursively delete target-only directory
			// content. RENAME/SKIP are handled below; OVERWRITE is a hard error.
			if conflictStrategy == "OVERWRITE" {
				return ErrRestoreTypeConflict
			}
		}
		switch conflictStrategy {
		case "SKIP":
			return db.SkipRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, "RESTORE_TARGET_EXISTS", item.ClaimEpoch)
		case "OVERWRITE":
			overwriteExisting = true
			if !targetProvider.SupportsAtomicRename() {
				if err := targetProvider.DeleteFile(ctx, "files", item.TargetPath); err != nil {
					return err
				}
			}
		case "RENAME":
			original := item.TargetPath
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := path.Join(path.Dir(original), fmt.Sprintf("%s (%d)%s", trimExtension(path.Base(original)), suffix, path.Ext(original)))
				exists, _, err = targetProvider.FileExists(ctx, "files", candidate)
				if err != nil {
					return err
				}
				if !exists {
					reserved, reserveErr := c.moveRestoreTargetReservation(ctx, item, candidate, false)
					if reserveErr != nil {
						return reserveErr
					}
					if !reserved {
						continue
					}
					break
				}
			}
			if item.TargetPath == original {
				return errors.New("restore rename candidates exhausted")
			}
		default:
			return errors.New("invalid restore conflict strategy")
		}
	}
	if err := targetProvider.CreateParentDirectories(ctx, "files", item.TargetPath); err != nil {
		return err
	}
	blocks, err := db.ListRestoreItemBlocksContext(ctx, c.db, item.ID, item.RestoreRunID)
	if err != nil {
		return err
	}
	recipes := make([]BlockRecipe, len(blocks))
	for i, block := range blocks {
		if len(block.PackSHA256) != sha256.Size || len(block.BlockSHA256) != sha256.Size {
			return errors.New("invalid restore block hash")
		}
		copy(recipes[i].PackSHA256[:], block.PackSHA256)
		copy(recipes[i].BlockSHA256[:], block.BlockSHA256)
		recipes[i].PackPath, recipes[i].PayloadOffset, recipes[i].PayloadLength, recipes[i].PlaintextSize = block.PackPath, block.PayloadOffset, block.PayloadLength, block.PlaintextSize
	}
	var expected [sha256.Size]byte
	if len(item.FileSHA256) != sha256.Size {
		return errors.New("invalid restore file hash")
	}
	copy(expected[:], item.FileSHA256)
	uploadPath := item.TargetPath
	backupPath := ""
	if targetProvider.SupportsAtomicRename() {
		uploadPath = fmt.Sprintf("%s.clumoove-restore-%s", item.TargetPath, shortRestoreID(item.ID))
		backupPath = fmt.Sprintf("%s.clumoove-restore-backup-%s", item.TargetPath, shortRestoreID(item.ID))
	}
	reader, writer := io.Pipe()
	reconstructed := make(chan error, 1)
	go func() {
		var openRange RangeOpener
		if rangeDownloader, ok := repositoryProvider.(storage.RangeDownloader); ok {
			openRange = func(ctx context.Context, packPath string, offset, length int64) (io.ReadCloser, error) {
				reader, err := rangeDownloader.StreamDownloadRange(ctx, "files", packPath, offset, length)
				if err != nil {
					return nil, err
				}
				return &throttledReadCloser{ReadCloser: reader, reader: throttle.NewThrottledReader(ctx, reader, throttler)}, nil
			}
		}
		err := ReconstructFileWithRanges(ctx, writer, recipes, item.SizeBytes, expected, openRange, func(ctx context.Context, packPath string) (io.ReadCloser, error) {
			if err := c.packReaders.Acquire(ctx); err != nil {
				return nil, err
			}
			reader, err := repositoryProvider.StreamDownload(ctx, "files", packPath)
			if err != nil {
				c.packReaders.Release()
				return nil, err
			}
			return &limitedReadCloser{ReadCloser: &throttledReadCloser{ReadCloser: reader, reader: throttle.NewThrottledReader(ctx, reader, throttler)}, release: c.packReaders.Release}, nil
		})
		reconstructed <- err
		_ = writer.CloseWithError(err)
	}()
	uploadErr := targetProvider.StreamUpload(ctx, "files", uploadPath, throttle.NewUploadThrottledReader(ctx, reader, throttler), item.SizeBytes)
	reader.Close()
	if reconstructionErr := <-reconstructed; reconstructionErr != nil {
		if targetProvider.SupportsAtomicRename() {
			_ = targetProvider.DeleteFile(ctx, "files", uploadPath)
		}
		return reconstructionErr
	}
	if uploadErr != nil {
		if targetProvider.SupportsAtomicRename() {
			_ = targetProvider.DeleteFile(ctx, "files", uploadPath)
		}
		return uploadErr
	}
	if targetProvider.SupportsAtomicRename() {
		if err := promoteRestoreUpload(ctx, targetProvider, uploadPath, item.TargetPath, backupPath, overwriteExisting); err != nil {
			return err
		}
	}
	_, size, err := targetProvider.FileExists(ctx, "files", item.TargetPath)
	if err != nil || size != item.SizeBytes {
		return errors.New("restore target size verification failed")
	}
	verificationKind := "SIZE_VERIFIED"
	if targetProvider.VerificationMode() == storage.VerificationCryptographicHash {
		targetHash, err := targetProvider.GetFileHash(ctx, "files", item.TargetPath)
		if err != nil && !errors.Is(err, storage.ErrHashNotSupported) && !errors.Is(err, storage.ErrChecksumNotAvailable) {
			return fmt.Errorf("verify restore target hash: %w", err)
		}
		if err == nil && targetHash != "" {
			algorithm, actual := storage.ParseHashString(targetHash)
			if algorithm == "SHA256" && actual != fmt.Sprintf("%x", expected) {
				return errors.New("restore target hash verification failed")
			}
			if algorithm == "SHA256" {
				verificationKind = "HASH_VERIFIED"
			}
		}
	}
	if _, err = db.CompleteRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, item.SizeBytes, verificationKind, item.ClaimEpoch); err != nil {
		return err
	}
	return c.applyItemMetadata(ctx, targetProvider, item)
}

func (c *Coordinator) applyItemMetadata(ctx context.Context, target storage.StorageProvider, item *db.RestoreItem) error {
	if !item.SourceMTime.Valid && len(item.SourceMetadata) == 0 {
		return nil
	}
	metadata := storage.FileMetadata{}
	if len(item.SourceMetadata) > 0 && string(item.SourceMetadata) != "null" {
		if err := json.Unmarshal(item.SourceMetadata, &metadata); err != nil {
			return db.WarnRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, "RESTORE_METADATA_INVALID", item.ClaimEpoch)
		}
	}
	if item.SourceMTime.Valid {
		metadata.ModifiedTime = item.SourceMTime.Time
	}
	applier, ok := target.(storage.MetadataApplier)
	if !ok {
		return db.WarnRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, "RESTORE_METADATA_UNSUPPORTED", item.ClaimEpoch)
	}
	if err := applier.ApplyMetadata(ctx, "files", item.TargetPath, metadata); err != nil {
		return db.WarnRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, "RESTORE_METADATA_FAILED", item.ClaimEpoch)
	}
	return nil
}

func (c *Coordinator) heartbeatItem(ctx context.Context, item *db.RestoreItem, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = db.HeartbeatRestoreItemContext(heartbeatCtx, c.db, item.ID, item.RestoreRunID, c.workerID, item.ClaimEpoch)
			cancel()
		}
	}
}

func shortRestoreID(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func (c *Coordinator) reserveRestoreTargetPath(ctx context.Context, item *db.RestoreItem, isDir bool) error {
	candidate := item.TargetPath
	for suffix := 0; suffix <= 100; suffix++ {
		reserved, err := db.ReserveRestoreTargetPathContext(ctx, c.db, item.RestoreRunID, item.ID, candidate)
		if err != nil {
			return err
		}
		if reserved {
			if candidate != item.TargetPath {
				if isDir {
					err = db.SetRestoreDirectoryTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, item.SnapshotRelativePath, candidate, item.ClaimEpoch)
				} else {
					err = db.SetRestoreItemTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, candidate, item.ClaimEpoch)
				}
				if err != nil {
					return err
				}
				item.TargetPath = candidate
			}
			return nil
		}
		if isDir {
			candidate = path.Join(path.Dir(item.TargetPath), fmt.Sprintf("%s (%d)", path.Base(item.TargetPath), suffix+1))
		} else {
			candidate = path.Join(path.Dir(item.TargetPath), fmt.Sprintf("%s (%d)%s", trimExtension(path.Base(item.TargetPath)), suffix+1, path.Ext(item.TargetPath)))
		}
	}
	return errors.New("restore target path reservations exhausted")
}

func (c *Coordinator) moveRestoreTargetReservation(ctx context.Context, item *db.RestoreItem, candidate string, isDir bool) (bool, error) {
	reserved, err := db.MoveRestoreTargetReservationContext(ctx, c.db, item.RestoreRunID, item.ID, candidate)
	if err != nil || !reserved {
		return reserved, err
	}
	if isDir {
		err = db.SetRestoreDirectoryTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, item.SnapshotRelativePath, candidate, item.ClaimEpoch)
	} else {
		err = db.SetRestoreItemTargetPathContext(ctx, c.db, item.ID, item.RestoreRunID, candidate, item.ClaimEpoch)
	}
	if err != nil {
		return false, err
	}
	item.TargetPath = candidate
	return true, nil
}

// promoteRestoreUpload never deletes an existing target until the staged file
// is complete. The backup is retained if cleanup fails so a transient provider
// error cannot turn an overwrite into data loss.
func promoteRestoreUpload(ctx context.Context, target storage.StorageProvider, uploadPath, targetPath, backupPath string, overwriteExisting bool) error {
	if overwriteExisting {
		backupExists, _, err := target.FileExists(ctx, "files", backupPath)
		if err != nil {
			return err
		}
		if backupExists {
			if err := target.DeleteFile(ctx, "files", backupPath); err != nil {
				return err
			}
		}
		if err := target.RenameFile(ctx, "files", targetPath, backupPath); err != nil {
			return err
		}
	}
	if err := target.RenameFile(ctx, "files", uploadPath, targetPath); err != nil {
		_ = target.DeleteFile(ctx, "files", uploadPath)
		if overwriteExisting {
			_ = target.RenameFile(ctx, "files", backupPath, targetPath)
		}
		return err
	}
	if overwriteExisting {
		if err := target.DeleteFile(ctx, "files", backupPath); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) restoreDirectory(ctx context.Context, target storage.StorageProvider, item *db.RestoreItem, conflictStrategy string) error {
	exists, _, err := target.FileExists(ctx, "files", item.TargetPath)
	if err != nil {
		return err
	}
	if exists {
		resource, err := target.InspectResource(ctx, "files", item.TargetPath)
		if err != nil {
			return err
		}
		if resource.IsDir {
			return db.CompleteRestoreDirectoryContext(ctx, c.db, item.ID, item.RestoreRunID, item.ClaimEpoch)
		}
		switch conflictStrategy {
		case "SKIP":
			return db.SkipRestoreItemContext(ctx, c.db, item.ID, item.RestoreRunID, "RESTORE_TARGET_FILE_CONFLICT", item.ClaimEpoch)
		case "OVERWRITE":
			if err := target.DeleteFile(ctx, "files", item.TargetPath); err != nil {
				return err
			}
		case "RENAME":
			original := item.TargetPath
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := path.Join(path.Dir(original), fmt.Sprintf("%s (%d)", path.Base(original), suffix))
				candidateExists, _, err := target.FileExists(ctx, "files", candidate)
				if err != nil {
					return err
				}
				if !candidateExists {
					reserved, reserveErr := c.moveRestoreTargetReservation(ctx, item, candidate, true)
					if reserveErr != nil {
						return reserveErr
					}
					if !reserved {
						continue
					}
					break
				}
			}
			if item.TargetPath == original {
				return errors.New("restore directory rename candidates exhausted")
			}
		default:
			return errors.New("invalid restore conflict strategy")
		}
	}
	if err := target.CreateParentDirectories(ctx, "files", item.TargetPath); err != nil {
		return err
	}
	if err := target.CreateDirectory(ctx, "files", item.TargetPath); err != nil {
		return err
	}
	return db.CompleteRestoreDirectoryContext(ctx, c.db, item.ID, item.RestoreRunID, item.ClaimEpoch)
}

type limitedReadCloser struct {
	io.ReadCloser
	release func()
}

type throttledReadCloser struct {
	io.ReadCloser
	reader io.Reader
}

func (r *throttledReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r *limitedReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.release()
	return err
}

func trimExtension(name string) string {
	extension := path.Ext(name)
	return name[:len(name)-len(extension)]
}

func (c *Coordinator) newProvider(ctx context.Context, provider, endpoint, username, encryptedPassword string) (storage.StorageProvider, error) {
	password, err := crypto.DecryptWithDomain(encryptedPassword, c.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(provider)))
	if err != nil {
		return nil, err
	}
	defer crypto.ZeroString(&password)
	return storage.NewProvider(ctx, provider, endpoint, username, password)
}

func (c *Coordinator) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("restore poll interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, err := c.PollOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
