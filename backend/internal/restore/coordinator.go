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
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"backend/internal/coordcfg"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type previewTreeReader interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
	GetDirectoryListing(ctx context.Context, resourceType, path string) ([]storage.CloudResource, error)
}

type uploadPromoter interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
	DeleteFile(ctx context.Context, resourceType, path string) error
	RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error
}

type directoryRestorer interface {
	FileExists(ctx context.Context, resourceType, path string) (bool, int64, error)
	InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error)
	DeleteFile(ctx context.Context, resourceType, path string) error
	CreateParentDirectories(ctx context.Context, resourceType, path string) error
	CreateDirectory(ctx context.Context, resourceType, path string) error
}

// RepositorySource is the read-only storage contract required from a backup repository during restore.
type RepositorySource interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	StreamDownload(ctx context.Context, resourceType, path string) (io.ReadCloser, error)
}

// PreviewTargetClient is the read-only target contract required by restore previews.
type PreviewTargetClient interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	previewTreeReader
}

// RestoreTarget is the destination storage contract required to apply restore items.
type RestoreTarget interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	previewTreeReader
	directoryRestorer
	uploadPromoter
	StreamUpload(ctx context.Context, resourceType, path string, reader io.Reader, size int64) error
	SupportsAtomicRename() bool
	VerificationMode() storage.VerificationMode
	GetFileHash(ctx context.Context, resourceType, path string) (string, error)
}

// PreviewTargetFactoryFunc constructs the read-only target client for restore previews.
type PreviewTargetFactoryFunc func(ctx context.Context, provider, endpoint, username, password string, opts ...storage.ProviderOption) (PreviewTargetClient, error)

// RepositorySourceFactoryFunc constructs the backup repository reader for restores.
type RepositorySourceFactoryFunc func(ctx context.Context, provider, endpoint, username, password string, opts ...storage.ProviderOption) (RepositorySource, error)

// RestoreTargetFactoryFunc constructs the target client for restore execution.
type RestoreTargetFactoryFunc func(ctx context.Context, provider, endpoint, username, password string, opts ...storage.ProviderOption) (RestoreTarget, error)

type Coordinator struct {
	db                      *sql.DB
	store                   db.RestoreStore
	encryptionKey           string
	packReaders             *PackReaderLimiter
	workerID                string
	previewTargetFactory    PreviewTargetFactoryFunc
	repositorySourceFactory RepositorySourceFactoryFunc
	restoreTargetFactory    RestoreTargetFactoryFunc
	oauthLocker             oauth.TokenLocker
}

// SetPreviewTargetFactory overrides preview target creation for tests or read-only adapters.
func (c *Coordinator) SetPreviewTargetFactory(factory PreviewTargetFactoryFunc) {
	c.previewTargetFactory = factory
}

// SetRepositorySourceFactory overrides backup repository reader creation for tests or read-only adapters.
func (c *Coordinator) SetRepositorySourceFactory(factory RepositorySourceFactoryFunc) {
	c.repositorySourceFactory = factory
}

// SetRestoreTargetFactory overrides restore destination creation for tests or target-only adapters.
func (c *Coordinator) SetRestoreTargetFactory(factory RestoreTargetFactoryFunc) {
	c.restoreTargetFactory = factory
}

var ErrRestoreTypeConflict = errors.New("restore target type conflict")

// Config is the explicit worker-side configuration of a Coordinator.
type Config struct {
	// Common carries the encryption key, worker ID (blank selects
	// "restore-worker") and required OAuth locker.
	coordcfg.Common
	// PackReaders optionally bounds concurrent full-pack memory buffers.
	PackReaders *PackReaderLimiter
}

func NewCoordinator(database *sql.DB, cfg Config) (*Coordinator, error) {
	if database == nil || cfg.EncryptionKey == "" {
		return nil, errors.New("restore database is required")
	}
	workerID, err := cfg.Resolve("restore-worker")
	if err != nil {
		return nil, fmt.Errorf("restore coordinator: %w", err)
	}
	return &Coordinator{db: database, store: db.NewRestoreStore(database), encryptionKey: cfg.EncryptionKey, packReaders: cfg.PackReaders, workerID: workerID, oauthLocker: cfg.OAuthLocker}, nil
}

// PollOnce claims a single preview. The durable status transition is the
// worker fence; a crash leaves RUNNING work for the upcoming lease recovery.
func (c *Coordinator) PollOnce(ctx context.Context) (bool, error) {
	if recovered, err := c.store.RecoverStaleRestoreItemsContext(ctx); err != nil {
		return false, fmt.Errorf("recover stale restore items: %w", err)
	} else if recovered > 0 {
		return true, nil
	}
	if err := c.store.ExpireRestorePreviewsContext(ctx); err != nil {
		return false, fmt.Errorf("expire restore previews: %w", err)
	}
	if err := c.store.RecoverStaleRestorePlanningContext(ctx); err != nil {
		return false, fmt.Errorf("recover stale restore planning: %w", err)
	}
	preview, err := c.store.ClaimNextRestorePreviewForWorkerContext(ctx, c.workerID)
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
			if failErr := c.store.FailRestorePreviewFencedContext(context.Background(), preview.ID, preview.CoordinatorGeneration, c.workerID, "RESTORE_PREVIEW_FAILED"); failErr != nil {
				return true, fmt.Errorf("count restore preview: %w; persist failure: %v", err, failErr)
			}
			db.WriteAuditLog(c.db, db.AuditEntry{UserID: sql.NullString{String: preview.UserID, Valid: true}, Action: db.AuditRestorePreviewFailed, Target: preview.ID})
			return true, nil
		}
		if _, err := c.store.CompleteRestorePreviewWithStatsFencedContext(ctx, preview.ID, preview.CoordinatorGeneration, c.workerID, stats); err != nil {
			return true, fmt.Errorf("complete restore preview: %w", err)
		}
		db.WriteAuditLog(c.db, db.AuditEntry{UserID: sql.NullString{String: preview.UserID, Valid: true}, Action: db.AuditRestorePreviewReady, Target: preview.ID})
		return true, nil
	}

	job, run, err := c.store.ClaimNextQueuedRestoreRunForWorkerContext(ctx, c.workerID)
	if err != nil {
		return false, err
	}
	if run != nil {
		if _, err := c.store.PlanRestoreRunContext(ctx, job, run); err != nil {
			if _, failErr := c.store.FailRestoreRunPlanningContext(context.Background(), job.ID, run.ID, run.Generation, "RESTORE_PLANNING_FAILED"); failErr != nil {
				return true, fmt.Errorf("plan restore run: %w; persist failure: %v", err, failErr)
			}
			return true, nil
		}
		return true, nil
	}
	if skipped, err := c.store.SkipBlockedRestoreItemsContext(ctx); err != nil {
		return false, err
	} else if skipped > 0 {
		return true, nil
	}
	item, err := c.store.ClaimNextRestoreItemForWorkerContext(ctx, c.workerID)
	if err != nil {
		return false, err
	}
	if item == nil {
		finalized, err := c.store.FinalizeCompletedRestoreRunsContext(ctx)
		if err != nil {
			return false, err
		}
		cancelled, err := c.store.FinalizeCancellingRestoreRunsContext(ctx)
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
			_ = c.store.FailRestoreItemContext(failureCtx, item.Lease(), item.RestoreRunID, errorCode)
		} else {
			_, _ = c.store.RetryRestoreItemContext(failureCtx, item.Lease(), item.RestoreRunID, errorCode)
		}
	}
	return true, nil
}

// previewTarget enumerates the selected immutable source catalog and the
// target tree without mutating it. The output remains bounded: only aggregate
// counters plus the first 100 deterministic conflict examples are stored.
func (c *Coordinator) previewTarget(ctx context.Context, preview *db.RestorePreview) (db.RestorePreviewStats, error) {
	items, err := c.store.ListRestorePreviewItemsContext(ctx, preview)
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	return c.previewTargetWithItems(ctx, preview, items)
}

func (c *Coordinator) previewTargetWithItems(ctx context.Context, preview *db.RestorePreview, items []db.RestorePreviewItem) (db.RestorePreviewStats, error) {
	if preview.TargetPasswordEncrypted.String == "" && preview.TargetProvider != "local" {
		return db.RestorePreviewStats{}, errors.New("restore preview target credential is unavailable")
	}
	providerCtx := storage.WithLocalUserScope(ctx, preview.UserID)
	target, err := c.newPreviewTarget(providerCtx, preview.TargetProvider, preview.TargetURL, preview.TargetUsername, preview.TargetPasswordEncrypted.String, encryptedMegaSession{
		id:        preview.TargetMegaSessionIDEncrypted.String,
		masterKey: preview.TargetMegaMasterKeyEncrypted.String,
	})
	if err != nil {
		return db.RestorePreviewStats{}, err
	}
	defer target.Close()
	if ok, err := target.Connect(ctx); err != nil || !ok {
		return db.RestorePreviewStats{}, errors.New("restore preview target connection failed")
	}

	expected := make(map[string]db.RestorePreviewItem, len(items))
	for _, item := range items {
		resolved, err := previewTargetPath(preview.TargetRoot, item.RelativePath)
		if err != nil {
			return db.RestorePreviewStats{}, err
		}
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

func resolveWithinRoot(root, relative string) (string, error) {
	root = path.Clean(root)
	if root == "." || root == "" {
		root = "/"
	}
	if relative == "" {
		return root, nil
	}
	relative = strings.ReplaceAll(relative, "\\", "/")
	joined := path.Clean(path.Join(root, relative))
	if root == "/" {
		if !strings.HasPrefix(joined, "/") {
			return "", storage.ErrPathEscapesRoot
		}
		return joined, nil
	}
	if joined != root && !strings.HasPrefix(joined, root+"/") {
		return "", storage.ErrPathEscapesRoot
	}
	return joined, nil
}

func previewTargetPath(root, relative string) (string, error) {
	return resolveWithinRoot(root, relative)
}

func enumeratePreviewTargetTree(ctx context.Context, target previewTreeReader, root string) (map[string]storage.CloudResource, error) {
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
			owned, err := c.store.RenewRestorePreviewLeaseContext(heartbeatCtx, preview.ID, preview.CoordinatorGeneration, c.workerID)
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
func (c *Coordinator) ensureFreshRestoreTargetOAuthToken(ctx context.Context, userID, runID, provider, accessEncrypted string, refreshEncrypted sql.NullString, expiresAt sql.NullTime) (string, error) {
	if !oauth.IsProvider(provider) {
		return accessEncrypted, nil
	}
	if !refreshEncrypted.Valid || refreshEncrypted.String == "" {
		return "", errors.New("restore OAuth refresh credential is unavailable")
	}
	if expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessEncrypted, nil
	}
	expectedRefreshEnc := refreshEncrypted.String
	refresh, err := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt restore OAuth refresh token: %w", err)
	}
	defer crypto.ZeroString(&refresh)

	var persistedAccess string
	res, err := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
		DB:            c.db,
		Locker:        c.oauthLocker,
		EncryptionKey: c.encryptionKey,
		UserID:        userID,
		Provider:      provider,
		RefreshToken:  refresh,
		Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
			tCtx, cancel := context.WithTimeout(refreshCtx, 15*time.Second)
			defer cancel()
			token, rerr := oauth.RefreshToken(tCtx, prov, tok)
			if rerr != nil {
				return nil, fmt.Errorf("refresh restore OAuth token: %w", rerr)
			}
			return token, nil
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			latestAccess, latestRefreshEnc, latestExpires, loadErr := c.store.GetRestoreRunCredentialSnapshotContext(lockCtx, runID)
			if loadErr != nil {
				return "", false, "", nil
			}
			if latestExpires.Valid && time.Now().Before(latestExpires.Time.Add(-2*time.Minute)) && latestAccess != "" {
				return latestAccess, true, "", nil
			}
			if latestRefreshEnc.Valid && latestRefreshEnc.String != "" && latestRefreshEnc.String != expectedRefreshEnc {
				expectedRefreshEnc = latestRefreshEnc.String
				updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", fmt.Errorf("decrypt restore OAuth refresh token: %w", decErr)
				}
				if updatedPlain != "" && updatedPlain != target {
					refresh = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Persist: func(persistCtx context.Context, _ string, token *oauth.TokenResponse) (string, bool, error) {
			access, err := crypto.EncryptWithDomain(token.AccessToken, c.encryptionKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				return "", false, err
			}
			rotatedRefresh, err := crypto.EncryptWithDomain(token.RefreshToken, c.encryptionKey, crypto.DomainOAuthRefreshToken)
			if err != nil {
				return "", false, err
			}
			expiresIn := token.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			if err := c.store.UpdateRestoreRunOAuthTokens(persistCtx, runID, access, rotatedRefresh, time.Now().Add(time.Duration(expiresIn)*time.Second), expectedRefreshEnc); err != nil {
				if !errors.Is(err, db.ErrOAuthTokenConflict) {
					return "", false, fmt.Errorf("persist restore OAuth token: %w", err)
				}
				latestAccess, _, latestExpires, latestErr := c.store.GetRestoreRunCredentialSnapshotContext(persistCtx, runID)
				if latestErr != nil || latestAccess == "" || !latestExpires.Valid {
					return "", false, fmt.Errorf("adopt concurrent restore OAuth token: %w", latestErr)
				}
				return latestAccess, true, nil
			}
			persistedAccess = access
			return "", false, nil
		},
	})
	if err != nil {
		return "", err
	}
	if res.Adopted {
		return res.AdoptedAccess, nil
	}
	return persistedAccess, nil
}

func (c *Coordinator) ensureFreshRepositoryOAuthToken(ctx context.Context, connection *db.BackupRepositoryConnection) (string, error) {
	if connection == nil || !oauth.IsProvider(connection.Provider) {
		if connection == nil {
			return "", errors.New("restore repository connection is unavailable")
		}
		return connection.PasswordEncrypted, nil
	}
	if !connection.RefreshTokenEncrypted.Valid || connection.RefreshTokenEncrypted.String == "" {
		return "", errors.New("backup repository OAuth refresh credential is unavailable")
	}
	if connection.TokenExpiresAt.Valid && time.Now().Before(connection.TokenExpiresAt.Time.Add(-2*time.Minute)) {
		return connection.PasswordEncrypted, nil
	}
	expectedRefreshEnc := connection.RefreshTokenEncrypted.String
	refresh, err := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", err
	}
	defer crypto.ZeroString(&refresh)

	var persistedAccess string
	res, err := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
		DB:            c.db,
		Locker:        c.oauthLocker,
		EncryptionKey: c.encryptionKey,
		UserID:        connection.UserID,
		Provider:      connection.Provider,
		RefreshToken:  refresh,
		Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
			tCtx, cancel := context.WithTimeout(refreshCtx, 15*time.Second)
			defer cancel()
			token, rerr := oauth.RefreshToken(tCtx, prov, tok)
			if rerr != nil {
				return nil, fmt.Errorf("refresh backup repository OAuth token: %w", rerr)
			}
			return token, nil
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			latest, loadErr := c.store.GetBackupRepositoryConnectionContext(lockCtx, connection.BackupJobID)
			if loadErr != nil {
				return "", false, "", nil
			}
			if latest.TokenExpiresAt.Valid && time.Now().Before(latest.TokenExpiresAt.Time.Add(-2*time.Minute)) && latest.PasswordEncrypted != "" {
				return latest.PasswordEncrypted, true, "", nil
			}
			if latest.RefreshTokenEncrypted.Valid && latest.RefreshTokenEncrypted.String != "" && latest.RefreshTokenEncrypted.String != expectedRefreshEnc {
				expectedRefreshEnc = latest.RefreshTokenEncrypted.String
				updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, c.encryptionKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", decErr
				}
				if updatedPlain != "" && updatedPlain != target {
					refresh = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Persist: func(persistCtx context.Context, _ string, token *oauth.TokenResponse) (string, bool, error) {
			accessEncrypted, err := crypto.EncryptWithDomain(token.AccessToken, c.encryptionKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				return "", false, err
			}
			refreshEncrypted, err := crypto.EncryptWithDomain(token.RefreshToken, c.encryptionKey, crypto.DomainOAuthRefreshToken)
			if err != nil {
				return "", false, err
			}
			expiresIn := token.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			if err := c.store.UpdateBackupRepositoryOAuthTokens(persistCtx, connection.BackupJobID, accessEncrypted, refreshEncrypted, time.Now().Add(time.Duration(expiresIn)*time.Second), expectedRefreshEnc); err != nil {
				if !errors.Is(err, db.ErrOAuthTokenConflict) {
					return "", false, err
				}
				latest, loadErr := c.store.GetBackupRepositoryConnectionContext(persistCtx, connection.BackupJobID)
				if loadErr != nil || latest.PasswordEncrypted == "" {
					return "", false, fmt.Errorf("adopt concurrent backup repository OAuth token: %w", loadErr)
				}
				return latest.PasswordEncrypted, true, nil
			}
			persistedAccess = accessEncrypted
			return "", false, nil
		},
	})
	if err != nil {
		return "", err
	}
	if res.Adopted {
		return res.AdoptedAccess, nil
	}
	return persistedAccess, nil
}

func (c *Coordinator) restoreItem(ctx context.Context, item *db.RestoreItem) error {
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go c.heartbeatItem(heartbeatCtx, item, heartbeatDone)
	defer func() {
		cancelHeartbeat()
		<-heartbeatDone
	}()

	runTarget, err := c.store.GetRestoreRunTargetConnectionContext(ctx, item.RestoreRunID)
	if err != nil {
		return fmt.Errorf("load restore connections: %w", err)
	}
	targetPasswordEncrypted := runTarget.TargetPasswordEncrypted
	repository, err := c.store.GetBackupRepositoryConnectionContext(ctx, runTarget.BackupJobID)
	if err != nil {
		return err
	}
	repository.PasswordEncrypted, err = c.ensureFreshRepositoryOAuthToken(ctx, repository)
	if err != nil {
		return err
	}
	if targetPasswordEncrypted == "" && runTarget.TargetProvider != "local" {
		return errors.New("restore target credential snapshot is unavailable")
	}
	targetPasswordEncrypted, err = c.ensureFreshRestoreTargetOAuthToken(ctx, repository.UserID, item.RestoreRunID, runTarget.TargetProvider, targetPasswordEncrypted, runTarget.TargetRefreshEncrypted, runTarget.TargetTokenExpiresAt)
	if err != nil {
		return err
	}
	ctx = storage.WithLocalUserScope(ctx, repository.UserID)
	repositoryProvider, err := c.newRepositorySource(ctx, repository.Provider, repository.URL, repository.Username, repository.PasswordEncrypted, encryptedMegaSession{
		id:        repository.MegaSessionIDEncrypted.String,
		masterKey: repository.MegaMasterKeyEncrypted.String,
		errLabel:  "load repository mega session",
	})
	if err != nil {
		return err
	}
	defer repositoryProvider.Close()
	targetProvider, err := c.newRestoreTarget(ctx, runTarget.TargetProvider, runTarget.TargetURL, runTarget.TargetUsername, targetPasswordEncrypted, encryptedMegaSession{
		id:        runTarget.TargetMegaSessionEncrypted.String,
		masterKey: runTarget.TargetMegaMasterKeyEncrypted.String,
		errLabel:  "load restore target mega session",
	})
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
			if encryptErr == nil {
				masterKeyEncrypted, encryptErr = crypto.EncryptWithDomain(base64.StdEncoding.EncodeToString(session.MasterKey), c.encryptionKey, crypto.DomainMegaMasterKey)
			}
			for i := range session.MasterKey {
				session.MasterKey[i] = 0
			}
			crypto.ZeroString(&session.ID)
			if encryptErr != nil {
				return fmt.Errorf("encrypt backup repository mega session: %w", encryptErr)
			}
			if err := c.store.UpdateBackupRepositoryMegaSessionContext(ctx, repository.BackupJobID, sessionIDEncrypted, masterKeyEncrypted); err != nil {
				return fmt.Errorf("persist backup repository mega session: %w", err)
			}
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
			if err := c.store.UpdateRestoreRunMegaSessionContext(ctx, item.RestoreRunID, sessionIDEncrypted, masterKeyEncrypted); err != nil {
				return fmt.Errorf("persist restore mega session: %w", err)
			}
		}
	}
	throttler := throttle.NewMigrationThrottler(runTarget.BandwidthMbps)
	if sanitized := sanitize.SanitizeFilename(path.Base(item.TargetPath), runTarget.TargetProvider); sanitized.Changed {
		resolvedPath := path.Join(path.Dir(item.TargetPath), sanitized.SanitizedName)
		if item.IsDir {
			if err := c.store.SetRestoreDirectoryTargetPathContext(ctx, item.Lease(), item.RestoreRunID, item.SnapshotRelativePath, resolvedPath); err != nil {
				return err
			}
		} else if err := c.store.SetRestoreItemTargetPathContext(ctx, item.Lease(), item.RestoreRunID, resolvedPath); err != nil {
			return err
		}
		item.TargetPath = resolvedPath
	}
	if err := c.reserveRestoreTargetPath(ctx, item, item.IsDir); err != nil {
		return err
	}
	if item.IsDir {
		return c.restoreDirectory(ctx, targetProvider, item, runTarget.ConflictStrategy)
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
			if runTarget.ConflictStrategy == "OVERWRITE" {
				return ErrRestoreTypeConflict
			}
		}
		switch runTarget.ConflictStrategy {
		case "SKIP":
			return c.store.SkipRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, "RESTORE_TARGET_EXISTS")
		case "OVERWRITE":
			overwriteExisting = true
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
	blocks, err := c.store.ListRestoreItemBlocksContext(ctx, item.ID, item.RestoreRunID)
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
	exists, size, err := targetProvider.FileExists(ctx, "files", item.TargetPath)
	if err != nil || !exists || size != item.SizeBytes {
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
	if _, err = c.store.CompleteRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, item.SizeBytes, verificationKind); err != nil {
		return err
	}
	return c.applyItemMetadata(ctx, targetProvider, item)
}

func (c *Coordinator) applyItemMetadata(ctx context.Context, target any, item *db.RestoreItem) error {
	if !item.SourceMTime.Valid && len(item.SourceMetadata) == 0 {
		return nil
	}
	metadata := storage.FileMetadata{}
	if len(item.SourceMetadata) > 0 && string(item.SourceMetadata) != "null" {
		if err := json.Unmarshal(item.SourceMetadata, &metadata); err != nil {
			return c.store.WarnRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, "RESTORE_METADATA_INVALID")
		}
	}
	if item.SourceMTime.Valid {
		metadata.ModifiedTime = item.SourceMTime.Time
	}
	applier, ok := target.(storage.MetadataApplier)
	if !ok {
		return c.store.WarnRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, "RESTORE_METADATA_UNSUPPORTED")
	}
	if err := applier.ApplyMetadata(ctx, "files", item.TargetPath, metadata); err != nil {
		return c.store.WarnRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, "RESTORE_METADATA_FAILED")
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
			_ = c.store.HeartbeatRestoreItemContext(heartbeatCtx, item.ID, item.RestoreRunID, c.workerID, item.ClaimEpoch)
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
		reserved, err := c.store.ReserveRestoreTargetPathContext(ctx, item.RestoreRunID, item.ID, candidate)
		if err != nil {
			return err
		}
		if reserved {
			if candidate != item.TargetPath {
				if isDir {
					err = c.store.SetRestoreDirectoryTargetPathContext(ctx, item.Lease(), item.RestoreRunID, item.SnapshotRelativePath, candidate)
				} else {
					err = c.store.SetRestoreItemTargetPathContext(ctx, item.Lease(), item.RestoreRunID, candidate)
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
	reserved, err := c.store.MoveRestoreTargetReservationContext(ctx, item.RestoreRunID, item.ID, candidate)
	if err != nil || !reserved {
		return reserved, err
	}
	if isDir {
		err = c.store.SetRestoreDirectoryTargetPathContext(ctx, item.Lease(), item.RestoreRunID, item.SnapshotRelativePath, candidate)
	} else {
		err = c.store.SetRestoreItemTargetPathContext(ctx, item.Lease(), item.RestoreRunID, candidate)
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
func promoteRestoreUpload(ctx context.Context, target uploadPromoter, uploadPath, targetPath, backupPath string, overwriteExisting bool) error {
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

func (c *Coordinator) restoreDirectory(ctx context.Context, target directoryRestorer, item *db.RestoreItem, conflictStrategy string) error {
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
			return c.store.CompleteRestoreDirectoryContext(ctx, item.Lease(), item.RestoreRunID)
		}
		switch conflictStrategy {
		case "SKIP":
			return c.store.SkipRestoreItemContext(ctx, item.Lease(), item.RestoreRunID, "RESTORE_TARGET_FILE_CONFLICT")
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
	return c.store.CompleteRestoreDirectoryContext(ctx, item.Lease(), item.RestoreRunID)
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

type encryptedMegaSession struct {
	id        string
	masterKey string
	errLabel  string
}

func (c *Coordinator) newRepositorySource(ctx context.Context, provider, endpoint, username, encryptedPassword string, mega encryptedMegaSession) (RepositorySource, error) {
	return openWithDecryptedCredentials(c, ctx, provider, endpoint, username, encryptedPassword, mega,
		c.repositorySourceFactory)
}

func (c *Coordinator) newPreviewTarget(ctx context.Context, provider, endpoint, username, encryptedPassword string, mega encryptedMegaSession) (PreviewTargetClient, error) {
	return openWithDecryptedCredentials(c, ctx, provider, endpoint, username, encryptedPassword, mega,
		c.previewTargetFactory)
}

func (c *Coordinator) newRestoreTarget(ctx context.Context, provider, endpoint, username, encryptedPassword string, mega encryptedMegaSession) (RestoreTarget, error) {
	return openWithDecryptedCredentials(c, ctx, provider, endpoint, username, encryptedPassword, mega,
		c.restoreTargetFactory)
}

func openWithDecryptedCredentials[T any](
	c *Coordinator,
	ctx context.Context,
	provider, endpoint, username, encryptedPassword string,
	mega encryptedMegaSession,
	factory func(ctx context.Context, provider, endpoint, username, password string, opts ...storage.ProviderOption) (T, error),
) (T, error) {
	password, err := crypto.DecryptWithDomain(encryptedPassword, c.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(provider)))
	if err != nil {
		var zero T
		return zero, err
	}
	defer crypto.ZeroString(&password)
	// Decrypt the password before decoding MEGA session material so failed
	// password decryption never creates or retains a decrypted master-key buffer.
	session, err := megasecret.DecodeProviderMegaSession(provider, mega.id, mega.masterKey, c.encryptionKey)
	if err != nil {
		var zero T
		if mega.errLabel != "" {
			return zero, fmt.Errorf("%s: %w", mega.errLabel, err)
		}
		return zero, err
	}
	defer clear(session.MasterKey)
	option := storage.WithProviderMegaSession(session)
	if factory != nil {
		return factory(ctx, provider, endpoint, username, password, option)
	}
	client, err := storage.NewProvider(ctx, provider, endpoint, username, password, option)
	if err != nil {
		var zero T
		return zero, err
	}
	result, ok := any(client).(T)
	if !ok {
		_ = client.Close()
		var zero T
		return zero, fmt.Errorf("provider %q does not support the requested restore operations", provider)
	}
	return result, nil
}

// Run polls until ctx is cancelled. Poll errors are treated as transient and
// retried with capped exponential backoff, so a brief database outage does not
// end the loop (an exit before shutdown is a terminal worker failure).
func (c *Coordinator) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("restore poll interval must be positive")
	}
	const maxRetryDelay = 30 * time.Second
	baseDelay := interval
	if baseDelay > time.Second {
		baseDelay = time.Second
	}
	retryDelay := baseDelay
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	wait := func(delay time.Duration) error {
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	for {
		processed, err := c.PollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Default().Warn("restore_poll_failed", slog.Any("error", err), slog.Duration("retry_delay", retryDelay))
			if werr := wait(retryDelay); werr != nil {
				return werr
			}
			if retryDelay *= 2; retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
			continue
		}
		retryDelay = baseDelay
		if processed {
			continue
		}
		if werr := wait(interval); werr != nil {
			return werr
		}
	}
}
