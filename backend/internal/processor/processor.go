package processor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

func formatWorkerHashValue(algo string, hasher hash.Hash) string {
	sum := hasher.Sum(nil)
	if algo == "QUICKXOR" {
		return base64.StdEncoding.EncodeToString(sum)
	}
	return fmt.Sprintf("%x", sum)
}

type activeTaskInfo struct {
	migrationID    string
	syncJobID      string
	syncGeneration int
	cancel         context.CancelFunc
}

type Processor struct {
	db                 *sql.DB
	queue              *queue.Queue
	oauthRefresher     *oauth.JobTokenRefresher
	oauthRefresherOnce sync.Once
	workerID           string
	secretKey          string
	maxThreads         int
	transferWorkers    int
	providerSlots      chan struct{}
	// notifyDSN is the raw PostgreSQL DSN used to open a dedicated LISTEN
	// connection for pg_notify-based wake-up (see ListenForTasks in queue).
	// Supplied via Config.NotifyDSN. If empty, the worker falls back to periodic
	// polling.
	notifyDSN string
	// listenForTasks opens the LISTEN watcher; tests replace it.
	listenForTasks func(ctx context.Context, dsn string) (<-chan struct{}, error)
	activeTasks    sync.Map
	throttlers     sync.Map
	// connLossCounts tracks consecutive connection-loss events per migration so
	// a single flaky task does not immediately pause the whole migration (P1-4).
	connLossCounts sync.Map
	// connLossTaskAttempts tracks, per task, how many consecutive connection-loss
	// failures it has seen. This lets the per-task connection-loss cap
	// (maxConnLossTaskAttempts) count only network errors, not unrelated failures,
	// so a task that failed twice for non-network reasons is not wrongly escalated
	// to a full migration pause on its next (first) network loss (P1-4).
	connLossTaskAttempts sync.Map
	// targetFileLocks synchronizes parallel worker threads processing tasks that
	// resolve to the same target file path. Entries are reference counted and
	// released automatically, so a long-lived worker holds a mutex only for the
	// target paths it is actively transferring to.
	targetFileLocks keyedMutexes
	// megaTargetLocks serializes access to an individual MEGA account within this
	// worker. MEGA permits duplicate same-name folders, and independent clients
	// with stale filesystem trees can otherwise race while creating parents.
	megaTargetLocks keyedMutexes
	providerFactory ProviderFactoryFunc
}

// SetProviderFactory overrides provider construction for tests or focused
// source-only/target-only transfer adapters.
func (p *Processor) SetProviderFactory(factory ProviderFactoryFunc) {
	p.providerFactory = factory
}

func (p *Processor) newWorkerPool() *providerPool {
	pool := newProviderPool()
	if p.providerFactory != nil {
		pool.factory = p.providerFactory
	}
	return pool
}

type refMutex struct {
	sync.Mutex
	count int
}

// noCopy may be added to a struct to make go vet give a warning if the struct is
// copied. See https://golang.org/issues/8005#issuecomment-190753527.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// keyedMutexes provides per-key mutual exclusion with reference counting, so an
// entry is removed as soon as no goroutine holds or waits for it. This keeps the
// map bounded by concurrent in-flight work instead of by the number of distinct
// keys the process has ever seen.
type keyedMutexes struct {
	_  noCopy
	mu sync.Mutex
	m  map[string]*refMutex
}

// lock acquires the mutex for key and returns a release function that must be
// called exactly once. The reference count is incremented before the mutex is
// acquired, so a waiter can never have its entry deleted out from under it.
func (k *keyedMutexes) lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = make(map[string]*refMutex)
	}
	m, ok := k.m[key]
	if !ok {
		m = &refMutex{}
		k.m[key] = m
	}
	m.count++
	k.mu.Unlock()

	m.Lock()
	return func() {
		m.Unlock()

		k.mu.Lock()
		defer k.mu.Unlock()
		m.count--
		if m.count == 0 {
			delete(k.m, key)
		}
	}
}

func (p *Processor) lockTargetFile(targetPath string) func() {
	if targetPath == "" || targetPath == "/" {
		return func() {}
	}
	return p.targetFileLocks.lock(targetPath)
}

func (p *Processor) lockMegaTarget(providerType, urlStr, username string) func() {
	if providerType != "mega" || username == "" {
		return func() {}
	}
	return p.megaTargetLocks.lock(providerType + "\x00" + urlStr + "\x00" + strings.ToLower(username))
}

// ResolveTargetPath computes the destination path for a migrated file,
// considering target directory and target filename/directory sanitization.
// For Immich sources, assets from an album are organized into Target/<AlbumName>/<filename>,
// while assets from the entire library are organized by date into Target/<YYYY>/<MM>/<filename>.
func ResolveTargetPath(resourceType, filePath string, metadata []byte, targetDir, sourceProvider, targetProvider string) string {
	if resourceType != "files" {
		return filePath
	}

	relativePath := filePath

	if sourceProvider == "immich" {
		filename := immichFilenameFromMetadata(metadata)
		if filename == "" {
			filename = path.Base(filePath)
		}

		albumName := immichAlbumNameFromMetadata(metadata)
		year, month := immichYearMonthFromMetadata(metadata)

		if albumName != "" {
			relativePath = path.Join(albumName, filename)
		} else if year != "" && month != "" {
			relativePath = path.Join(year, month, filename)
		} else {
			relativePath = filename
		}
	}

	if !storage.IsVirtualProvider(targetProvider) {
		parts := strings.Split(relativePath, "/")
		for i, part := range parts {
			if part != "" && part != "." && part != ".." {
				sanitized := sanitize.SanitizeFilename(part, targetProvider)
				parts[i] = sanitized.SanitizedName
			}
		}
		relativePath = strings.Join(parts, "/")
	}

	if targetDir != "" && targetDir != "/" {
		return path.Clean(path.Join(targetDir, relativePath))
	}
	return path.Clean(relativePath)
}

// EffectiveTargetPath returns the destination selected for this task. Once a
// transfer has resolved its destination, that value must be reused instead of
// deriving a new path from the immutable source file_path.
func EffectiveTargetPath(task *db.Task, targetDir, sourceProvider, targetProvider string) string {
	if task.ResolvedTargetPath != "" {
		return task.ResolvedTargetPath
	}
	return ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, targetDir, sourceProvider, targetProvider)
}

func fileManagerTransferOperation(pickerSessionID string) string {
	const marker = "file-manager-transfer:"
	if !strings.HasPrefix(pickerSessionID, marker) {
		return ""
	}
	return strings.TrimPrefix(pickerSessionID, marker)
}

func (p *Processor) reconcileFileManagerTransferIfDone(mig *db.Migration) {
	if p.db != nil && mig != nil && fileManagerTransferOperation(mig.PickerSessionID) != "" {
		_ = db.ReconcileMigrationProgress(p.db, mig.ID)
	}
}

// immichFilenameFromMetadata extracts the original filename that an Immich
// asset was indexed with. It is stored either as a top-level immich_filename
// key or inside custom_props.immich_filename.
func immichFilenameFromMetadata(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var meta struct {
		CustomProps map[string]string `json:"custom_props"`
		Filename    string            `json:"immich_filename"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return ""
	}
	if meta.Filename != "" {
		return meta.Filename
	}
	if meta.CustomProps != nil {
		return meta.CustomProps["immich_filename"]
	}
	return ""
}

func immichAlbumNameFromMetadata(metadata []byte) string {
	if len(metadata) == 0 {
		return ""
	}
	var meta struct {
		CustomProps map[string]string `json:"custom_props"`
		AlbumName   string            `json:"immich_album_name"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return ""
	}
	// CustomProps is the canonical path; direct key is a legacy fallback.
	if meta.AlbumName != "" {
		return meta.AlbumName
	}
	if meta.CustomProps != nil {
		return meta.CustomProps["immich_album_name"]
	}
	return ""
}

func immichYearMonthFromMetadata(metadata []byte) (string, string) {
	if len(metadata) == 0 {
		return "", ""
	}
	var meta struct {
		CustomProps map[string]string `json:"custom_props"`
		Year        string            `json:"immich_year"`
		Month       string            `json:"immich_month"`
		CreatedAt   string            `json:"immich_file_created_at"`
		ModifiedAt  string            `json:"immich_file_modified_at"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return "", ""
	}
	year := meta.Year
	month := meta.Month
	if year == "" && meta.CustomProps != nil {
		year = meta.CustomProps["immich_year"]
	}
	if month == "" && meta.CustomProps != nil {
		month = meta.CustomProps["immich_month"]
	}
	if year != "" && month != "" {
		return year, month
	}

	createdAtStr := meta.CreatedAt
	if createdAtStr == "" && meta.CustomProps != nil {
		createdAtStr = meta.CustomProps["immich_file_created_at"]
	}
	if createdAtStr == "" {
		createdAtStr = meta.ModifiedAt
		if createdAtStr == "" && meta.CustomProps != nil {
			createdAtStr = meta.CustomProps["immich_file_modified_at"]
		}
	}
	if createdAtStr != "" {
		if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil && !t.IsZero() {
			return t.Format("2006"), t.Format("01")
		}
	}
	return "", ""
}

// Config is the explicit execution configuration of a Processor. The worker
// loads and validates it once at startup; the processor never reads the
// environment itself.
type Config struct {
	WorkerID  string
	SecretKey string
	// MaxThreads is the global parallelism of this worker process. It must be
	// at least 1.
	MaxThreads int
	// NotifyDSN is the PostgreSQL DSN used to open a dedicated LISTEN connection
	// for pg_notify-based wake-up of idle worker threads. If empty, the worker
	// falls back to periodic polling.
	NotifyDSN string
	// ProviderSlots supplies the shared concurrency semaphore across transfer
	// workers and runtime verification dispatchers. Its capacity must be at least 1.
	ProviderSlots chan struct{}
	// TokenRefresher optionally supplies a shared OAuth job token refresher so
	// concurrent transfer and verification refreshes for the same job and role
	// serialize on the same in-process lock set. When nil, NewProcessor builds one.
	TokenRefresher *oauth.JobTokenRefresher
}

func queueTokenLocker(q *queue.Queue) oauth.TokenLocker {
	if q == nil {
		return nil
	}
	return q
}

// NewProcessor builds a Processor from validated configuration.
func NewProcessor(database *sql.DB, q *queue.Queue, cfg Config) (*Processor, error) {
	if cfg.MaxThreads < 1 {
		return nil, errors.New("processor MaxThreads must be at least 1")
	}
	if cap(cfg.ProviderSlots) < 1 {
		return nil, errors.New("processor ProviderSlots must have capacity of at least 1")
	}
	slots := cfg.ProviderSlots
	refresher := cfg.TokenRefresher
	if refresher == nil {
		refresher = oauth.NewJobTokenRefresher(database, cfg.SecretKey, queueTokenLocker(q))
	}
	return &Processor{
		db:              database,
		queue:           q,
		oauthRefresher:  refresher,
		workerID:        cfg.WorkerID,
		secretKey:       cfg.SecretKey,
		maxThreads:      cfg.MaxThreads,
		transferWorkers: cfg.MaxThreads,
		providerSlots:   slots,
		notifyDSN:       cfg.NotifyDSN,
		listenForTasks:  queue.ListenForTasks,
	}, nil
}

func (p *Processor) tokenRefresher() *oauth.JobTokenRefresher {
	p.oauthRefresherOnce.Do(func() {
		if p.oauthRefresher == nil {
			p.oauthRefresher = oauth.NewJobTokenRefresher(p.db, p.secretKey, queueTokenLocker(p.queue))
		}
	})
	return p.oauthRefresher
}

func (p *Processor) acquireProviderSlot(ctx context.Context) bool {
	select {
	case p.providerSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *Processor) releaseProviderSlot() {
	<-p.providerSlots
}

func (p *Processor) pruneProviderPool(ctx context.Context, pool *providerPool) {
	if p.db == nil {
		return
	}
	pool.prune(func(key providerPoolKey) bool {
		switch key.entityType {
		case "migration":
			mig, err := db.GetMigration(p.db, key.entityID)
			return err == nil && (mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED" || mig.Status == "CANCELLED")
		case "sync":
			job, err := db.GetSyncJob(p.db, key.entityID)
			return err == nil && (job.RunGeneration != key.generation || job.Status == "COMPLETED" || job.Status == "FAILED")
		default:
			return false
		}
	})
}

// connLossEscalationThreshold is the number of consecutive connection-loss
// events for a migration before we escalate from per-task retry to a full
// PAUSED_CONNECTION_LOSS pause (which triggers the connection-recovery
// scheduler). This prevents one flaky endpoint from pausing every other task
// in flight (P1-4).
const connLossEscalationThreshold = 3

// maxConnLossTaskAttempts caps how many times a single task may be retried on
// connection loss before the migration is paused. Without this cap a poisoned
// endpoint whose transfers keep classifying as network errors would retry
// forever, because every *other* successful task resets the migration-wide
// connLossCounts streak (P1-4).
const maxConnLossTaskAttempts = 3

// taskHeartbeatGrace is the initial window during which a RUNNING task's
// updated_at is heartbeated unconditionally, so a slow-starting or briefly
// throttled large-file transfer is never reclaimed by the orphan watchdog.
var taskHeartbeatGrace = 10 * time.Minute

// taskHeartbeatByteStale is how long a past-grace task may go without moving any
// bytes before its heartbeat is suppressed, allowing the orphan-recovery
// watchdog to reclaim a genuinely hung transfer.
var taskHeartbeatByteStale = 2 * time.Minute

// chunkedUploadThreshold is the file size (50 MiB) above which transfers use
// chunked upload. Kept as a single source of truth so the download/upload
// timeout policy below stays in sync with the chunking decision.
const chunkedUploadThreshold int64 = 50 * 1024 * 1024

// transferTimeoutBase / transferTimeoutPerChunk scale the per-request timeout by
// file size: every 50 MiB of content adds one minute, capped at 12h. Applied
// identically to the download and upload phases so neither side times out before
// the other for a given file size.
const (
	transferTimeoutBase     = 5 * time.Minute
	transferTimeoutPerChunk = 1 * time.Minute
	transferTimeoutMax      = 12 * time.Hour
)

// transferTimeout returns a file-size-scaled transfer timeout. It is deterministic
// (no clock dependency) so the download and upload phases use the same deadline.
func transferTimeout(fileSize int64) time.Duration {
	if fileSize <= 0 {
		return transferTimeoutBase
	}
	timeout := transferTimeoutBase + time.Duration(fileSize/chunkedUploadThreshold)*transferTimeoutPerChunk
	if timeout > transferTimeoutMax {
		return transferTimeoutMax
	}
	return timeout
}

// retryBackoff returns the exponential-backoff delay for the given 1-based attempt,
// using the standard 10×3^(attempt-1) schedule (10s, 30s, 90s), capped at 90s.
// Centralising the schedule keeps the connection-loss and normal-failure retry
// paths consistent (both previously inlined the same [10,30,90] table + clamp).
func retryBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 10 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 90 * time.Second
	}
}

// retryDelay respects a provider's explicit rate-limit window while retaining
// the standard migration retry floor for all other transient failures.
func retryDelay(err error, attempt int) time.Duration {
	delay := retryBackoff(attempt)
	var retryAfterErr *storage.RetryAfterError
	if errors.As(err, &retryAfterErr) && retryAfterErr.After > delay {
		return retryAfterErr.After
	}
	return delay
}

func (p *Processor) recordConnLoss(migrationID string) int {
	actual, _ := p.connLossCounts.LoadOrStore(migrationID, new(int32))
	return int(atomic.AddInt32(actual.(*int32), 1))
}

func (p *Processor) clearConnLoss(migrationID string) {
	p.connLossCounts.Delete(migrationID)
}

// recordConnLossTask increments and returns the per-task connection-loss attempt
// count. It is reset via clearConnLossTask whenever a task succeeds or the
// migration-wide streak is cleared, so it only reflects consecutive network
// failures for that specific task (P1-4).
func (p *Processor) recordConnLossTask(taskID string) int {
	actual, _ := p.connLossTaskAttempts.LoadOrStore(taskID, new(int32))
	return int(atomic.AddInt32(actual.(*int32), 1))
}

func (p *Processor) clearConnLossTask(taskID string) {
	p.connLossTaskAttempts.Delete(taskID)
}

// CancelMigration cancels active transfer streams belonging to migrationID.
func (p *Processor) CancelMigration(migrationID string) {
	processorLogf("[Worker %s] Received Cancel Event for Migration: %s\n", p.workerID, migrationID)
	p.cancelActiveTasks("stream", func(info activeTaskInfo) bool { return info.migrationID == migrationID })
}

// cancelActiveTasks cancels the stream of every active task matching the filter.
func (p *Processor) cancelActiveTasks(kind string, match func(activeTaskInfo) bool) {
	p.activeTasks.Range(func(key, value interface{}) bool {
		info, ok := value.(activeTaskInfo)
		if ok && match(info) {
			processorLogf("[Worker %s] Cancelling active %s for task: %s\n", p.workerID, kind, key)
			info.cancel()
		}
		return true
	})
}

// CancelSyncJob cancels active sync transfer streams matching event.
func (p *Processor) CancelSyncJob(event queue.SyncCancelEvent) {
	processorLogf("[Worker %s] Received Cancel Event for Sync Job: %s generation %d\n", p.workerID, event.SyncJobID, event.PassGeneration)
	p.cancelActiveTasks("sync stream", func(info activeTaskInfo) bool {
		return info.syncJobID == event.SyncJobID && (event.AllGenerations || info.syncGeneration == event.PassGeneration)
	})
}

// SetBandwidthLimit updates the active throttler rate limit for jobID, if any.
func (p *Processor) SetBandwidthLimit(jobID string, mbps int) {
	if throttler, ok := p.throttlers.Load(jobID); ok {
		throttler.(*throttle.MigrationThrottler).SetLimit(mbps)
	}
}

// Start runs the worker dequeue loop and blocks until ctx is cancelled and all
// in-flight tasks have finished.
func (p *Processor) Start(ctx context.Context) {
	processorLogf("[Worker %s] Started and waiting for tasks with max %d threads...\n", p.workerID, p.maxThreads)

	var throttleWG sync.WaitGroup
	throttleWG.Add(1)
	go func() {
		defer throttleWG.Done()
		p.runThrottlerCleanup(ctx)
	}()

	// Start a PostgreSQL LISTEN watcher so idle threads can be woken up
	// immediately when new tasks are inserted (pg_notify 'task_available').
	// Falls back to periodic polling if the listener cannot be established.
	var notifyTasksCh <-chan struct{}
	if p.notifyDSN != "" {
		ch, err := p.listenForTasks(ctx, p.notifyDSN)
		if err != nil {
			processorLogf("[Worker %s] LISTEN task_available unavailable (falling back to polling): %v\n", p.workerID, err)
		} else {
			notifyTasksCh = ch
			processorLogf("[Worker %s] LISTEN task_available active — idle threads will wake immediately on new tasks\n", p.workerID)
		}
	}

	p.runTransferWorkers(ctx, notifyTasksCh)
	throttleWG.Wait()
	// ctx is cancelled here; wait for the LISTEN goroutine to release its
	// connection so no worker-owned goroutine outlives the drain.
	if notifyTasksCh != nil {
		for range notifyTasksCh {
		}
	}
}

func (p *Processor) runThrottlerCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.db != nil {
				p.cleanupThrottlers()
			}
		}
	}
}

func isTerminalStatus(status string) bool {
	switch status {
	case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
		return true
	}
	return false
}

// shouldEvictThrottler decides whether a throttler entry may be dropped. The
// throttlers map is shared by migrations and sync jobs, so a migration miss must
// fall back to a sync-job lookup before the entry is considered orphaned. A
// transient lookup error must never evict live throttler state.
func shouldEvictThrottler(migStatus string, migErr error, lookupJob func() (string, error)) bool {
	if migErr == nil {
		return isTerminalStatus(migStatus)
	}
	if !errors.Is(migErr, sql.ErrNoRows) {
		return false
	}
	jobStatus, jobErr := lookupJob()
	if jobErr == nil {
		return isTerminalStatus(jobStatus)
	}
	// Neither a migration nor a sync job: the owning entity was deleted.
	return errors.Is(jobErr, sql.ErrNoRows)
}

func migStatusOrEmpty(mig *db.Migration, err error) string {
	if err != nil || mig == nil {
		return ""
	}
	return mig.Status
}

func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		id := key.(string)
		mig, err := db.GetMigration(p.db, id)
		if shouldEvictThrottler(migStatusOrEmpty(mig, err), err, func() (string, error) {
			job, jerr := db.GetSyncJob(p.db, id)
			if jerr != nil {
				return "", jerr
			}
			return job.Status, nil
		}) {
			p.throttlers.Delete(id)
		}
		return true
	})
}

type atomicRenameChecker interface {
	SupportsAtomicRename() bool
}

type fileDeleter interface {
	DeleteFile(ctx context.Context, resourceType, path string) error
}

type fileRenamer interface {
	RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error
}

type overwritePromoter interface {
	storage.FileExister
	fileDeleter
	fileRenamer
}

// useTempThenRename reports whether the processor should use the
// "upload to <path>.tmp then atomically rename" overwrite pattern for the
// given target. It is only safe when (a) an overwrite/retry actually requires
// the temp file and (b) the target provider supports a rename operation.
// Providers without atomic-rename support write the file directly to its final
// name during upload. This includes S3, whose copy-and-delete "rename" would
// otherwise require deleting an existing object before a potentially failing
// copy completes.
func useTempThenRename(target atomicRenameChecker, deleteAfterUpload bool) bool {
	return deleteAfterUpload && target.SupportsAtomicRename()
}

// overwriteBackupPath is stable across retries of the same task, allowing an
// interrupted promotion to be recovered. Keep the suffix short because many
// filesystems limit an individual filename to 255 bytes.
func overwriteBackupPath(targetPath, taskID string) string {
	if len(taskID) > 8 {
		taskID = taskID[:8]
	}
	return fmt.Sprintf("%s.bak-%s", targetPath, taskID)
}

func cleanupStagingUpload(ctx context.Context, target fileDeleter, resourceType, uploadPath string) {
	if err := target.DeleteFile(ctx, resourceType, uploadPath); err != nil {
		processorLogf("Warning: failed to clean up staging upload %s: %v", uploadPath, err)
	}
}

// promoteOverwrite replaces targetPath with uploadPath without deleting the
// previous target until the replacement is in place. Providers that return
// SupportsAtomicRename use a same-provider rename for each move; if promoting
// the temporary upload fails, the original is moved back immediately. A backup
// is intentionally left in place when its cleanup fails so a transient error
// can never turn into data loss.
func promoteOverwrite(ctx context.Context, target overwritePromoter, resourceType, targetPath, uploadPath, backupPath string) error {
	exists, _, err := target.FileExists(ctx, resourceType, targetPath)
	if err != nil {
		return fmt.Errorf("failed to check target before overwrite promotion: %w", err)
	}

	originalBackedUp := false
	if exists {
		backupExists, _, err := target.FileExists(ctx, resourceType, backupPath)
		if err != nil {
			return fmt.Errorf("failed to check overwrite backup path: %w", err)
		}
		if backupExists {
			// A backup with this task's stable identifier is an artifact from an
			// earlier attempt. The live target is still present, so remove that
			// older recovery copy before preserving the current target again.
			if err := target.DeleteFile(ctx, resourceType, backupPath); err != nil {
				return fmt.Errorf("recovery backup already exists at %q and cleanup failed: %w", backupPath, err)
			}
		}
		if err := target.RenameFile(ctx, resourceType, targetPath, backupPath); err != nil {
			return fmt.Errorf("failed to preserve existing target before overwrite promotion: %w", err)
		}
		originalBackedUp = true
	}

	if err := target.RenameFile(ctx, resourceType, uploadPath, targetPath); err != nil {
		cleanupStagingUpload(ctx, target, resourceType, uploadPath)
		if originalBackedUp {
			if rollbackErr := target.RenameFile(ctx, resourceType, backupPath, targetPath); rollbackErr != nil {
				return fmt.Errorf("failed to promote temporary upload: %w; failed to restore preserved target from %q: %v", err, backupPath, rollbackErr)
			}
			return fmt.Errorf("failed to promote temporary upload; preserved target restored: %w", err)
		}
		return fmt.Errorf("failed to promote temporary upload: %w", err)
	}

	if originalBackedUp {
		if err := target.DeleteFile(ctx, resourceType, backupPath); err != nil {
			// The new target is confirmed in place and the old target remains
			// recoverable at backupPath. Surface the failure rather than silently
			// accepting an unexpected retained copy.
			return fmt.Errorf("overwrite promoted target but failed to remove preserved backup %q: %w", backupPath, err)
		}
	}

	return nil
}

// oauthAuthFailureRole identifies which OAuth side rejected the transfer. The
// wrapped transfer errors retain source/target context, so a valid credential
// on the other side is never rotated unnecessarily.
func oauthAuthFailureRole(mig *db.Migration, errText string) string {
	return oauthAuthFailureRoleForProviders(mig.SourceProvider, mig.TargetProvider, errText)
}

func oauthAuthFailureRoleForProviders(sourceProvider, targetProvider, errText string) string {
	errText = strings.ToLower(errText)
	sourceOAuth := oauth.IsProvider(sourceProvider)
	targetOAuth := oauth.IsProvider(targetProvider)
	switch {
	case sourceOAuth && strings.Contains(errText, "source"):
		return "source"
	case targetOAuth && strings.Contains(errText, "target"):
		return "target"
	case sourceOAuth && !targetOAuth:
		return "source"
	case targetOAuth && !sourceOAuth:
		return "target"
	default:
		return ""
	}
}

// ProgressReader wraps io.Reader to notify bytes read
type ProgressReader struct {
	Reader       io.Reader
	ProgressChan chan<- int64
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 && pr.ProgressChan != nil {
		select {
		case pr.ProgressChan <- int64(n):
		default:
			// Live progress is approximate; never let a slow database update stall
			// the source stream or upload.
		}
	}
	return n, err
}

// isPermanentTransferError centralizes provider errors which cannot succeed on
// retry. Prefer typed errors where providers expose them; the remaining legacy
// API strings are kept in one place until those providers gain sentinels.
func isPermanentTransferError(err error) bool {
	if errors.Is(err, oauth.ErrRefreshTokenInvalid) ||
		errors.Is(err, storage.ErrUnsupportedResourceType) ||
		errors.Is(err, storage.ErrPathEscapesRoot) ||
		errors.Is(err, storage.ErrPermanentTransfer) {
		return true
	}
	errText := strings.ToLower(err.Error())
	for _, marker := range []string{
		"exportsizelimitexceeded", "badrequest", "conversion is not supported",
		"filenotdownloadable", "only files with binary content can be downloaded",
		"too large to be exported", "notfound", "filenotfound", "not supported by",
		"path escapes storage root",
	} {
		if strings.Contains(errText, marker) {
			return true
		}
	}
	return false
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Direct type assertions
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "handshake failure") ||
		strings.Contains(errStr, "http2: server sent goaway")
}

func shouldInvalidateProviderPool(err error) bool {
	if isNetworkError(err) || errors.Is(err, storage.ErrAuth) {
		return true
	}
	// Google API errors expose authError text rather than storage.ErrAuth; the
	// credential-message fallbacks cover legacy provider clients with the same gap.
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "autherror") ||
		strings.Contains(errText, "invalid credentials") ||
		strings.Contains(errText, "invalid authentication credentials")
}

// isWebDAVSystemConflict returns true if err represents a WebDAV/SabreDAV server
// policy rejection or HTTP status failure (e.g. HTTP 400/403/404/409 or SabreDAV exception)
// that prevents operating on protected or blacklisted files (such as .htaccess).
//
// String matching is tightly anchored to WebDAV/SabreDAV error formats to prevent
// false positives on numeric ports (e.g. :4003), byte counts (e.g. 400 bytes), or
// non-WebDAV provider errors (such as SMB sharing conflicts or S3 request errors).
func isWebDAVSystemConflict(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())

	// Match SabreDAV exceptions in PHP collapsed format ("sabredav") or backslash-escaped format ("sabre\dav")
	if strings.Contains(s, "sabredav") || strings.Contains(s, "sabre\\dav") || strings.Contains(s, "sabre/dav") {
		return true
	}

	// Match WebDAV PROPFIND status failures (e.g. "PROPFIND check failed with status: 400")
	if strings.Contains(s, "propfind check failed with status:") || strings.Contains(s, "propfind for hash failed: status") {
		return true
	}

	// Match WebDAV HTTP status code responses anchored to status prefixes
	if strings.Contains(s, "status: 400") || strings.Contains(s, "status 400") ||
		strings.Contains(s, "status: 403") || strings.Contains(s, "status 403") ||
		strings.Contains(s, "status: 404") || strings.Contains(s, "status 404") ||
		strings.Contains(s, "status: 409") || strings.Contains(s, "status 409") {
		return true
	}

	// Match specific WebDAV error phrases
	if strings.Contains(s, "delete failed with status: 403") ||
		strings.Contains(s, "could not be found") {
		return true
	}

	return false
}
