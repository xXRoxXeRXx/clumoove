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
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/indexer"
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
	db                         *sql.DB
	queue                      *queue.Queue
	indexer                    *indexer.Indexer
	workerID                   string
	secretKey                  string
	maxThreads                 int
	transferWorkers            int
	verificationWorkers        int
	migrationVerificationQueue chan verificationWork
	syncVerificationQueue      chan verificationWork
	verificationWG             sync.WaitGroup
	providerSlots              chan struct{}
	// dbConnStr is the raw PostgreSQL DSN used to open a dedicated LISTEN
	// connection for pg_notify-based wake-up (see ListenForTasks in queue).
	// Set via SetDBConnStr before calling Start. If empty, the worker falls back
	// to periodic polling.
	dbConnStr    string
	activeTasks  sync.Map
	refreshLocks keyedMutexes
	throttlers   sync.Map
	// connLossCounts tracks consecutive connection-loss events per migration so
	// a single flaky task does not immediately pause the whole migration (P1-4).
	connLossCounts sync.Map
	// recoveryAttempts tracks, per paused migration, how many times connection
	// recovery has been attempted and when, so P1-12 can apply increasing backoff
	// instead of probing a server that is still down on every 60s tick. Keyed by
	// migration id.
	recoveryAttempts sync.Map
	// recoveryCursorMu guards the round-robin cursors used to spread bounded
	// connection-recovery probes across paused migrations and sync jobs.
	recoveryCursorMu        sync.Mutex
	migrationRecoveryCursor string
	syncRecoveryCursor      string
	// connLossTaskAttempts tracks, per task, how many consecutive connection-loss
	// failures it has seen. This lets the per-task connection-loss cap
	// (maxConnLossTaskAttempts) count only network errors, not unrelated failures,
	// so a task that failed twice for non-network reasons is not wrongly escalated
	// to a full migration pause on its next (first) network loss (P1-4).
	connLossTaskAttempts sync.Map
	// verifyingEntities tracks currently running checksum verification passes
	// (keyed by "mig:<id>" or "sync:<id>") to prevent concurrent ticks from
	// spawning duplicate verification passes for the same entity.
	verifyingEntities sync.Map
	// targetFileLocks synchronizes parallel worker threads processing tasks that
	// resolve to the same target file path. Entries are reference counted and
	// released automatically, so a long-lived worker holds a mutex only for the
	// target paths it is actively transferring to.
	targetFileLocks keyedMutexes
	// megaTargetLocks serializes access to an individual MEGA account within this
	// worker. MEGA permits duplicate same-name folders, and independent clients
	// with stale filesystem trees can otherwise race while creating parents.
	megaTargetLocks keyedMutexes
}

type verificationWork struct {
	key string
	run func(context.Context)
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

// effectiveTargetPath returns the destination selected for this task. Once a
// transfer has resolved its destination, that value must be reused instead of
// deriving a new path from the immutable source file_path.
func effectiveTargetPath(task *db.Task, targetDir, sourceProvider, targetProvider string) string {
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

// newProvider creates a provider scoped to a single operation. Providers retain
// their credentials internally, so retaining them in a shared cache would keep
// decrypted credentials alive beyond the task that needs them.
func newProvider(ctx context.Context, providerType, urlStr, username, password string) (storage.StorageProvider, error) {
	return storage.NewProvider(ctx, providerType, urlStr, username, password)
}

// SetDBConnStr sets the PostgreSQL DSN used to open a dedicated LISTEN
// connection for immediate wake-up when new tasks are inserted.
// Must be called before Start(). Falls back to polling if not set.
func (p *Processor) SetDBConnStr(connStr string) {
	p.dbConnStr = connStr
}

func NewProcessor(database *sql.DB, q *queue.Queue, workerID string, secretKey string) *Processor {
	// Default to 16 to match the maximum selectable threads per migration in the UI slider.
	// The actual concurrency per migration is limited by the m.threads setting in the database during DequeueSQL.
	maxThreads := 16
	if envVal := os.Getenv("MAX_THREADS"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
			maxThreads = val
		}
	}

	transferWorkers, verificationWorkers := workerCapacity(maxThreads)
	return &Processor{
		db:                         database,
		queue:                      q,
		indexer:                    indexer.NewIndexer(database, secretKey, q),
		workerID:                   workerID,
		secretKey:                  secretKey,
		maxThreads:                 maxThreads,
		transferWorkers:            transferWorkers,
		verificationWorkers:        verificationWorkers,
		migrationVerificationQueue: make(chan verificationWork, maxThreads*2),
		syncVerificationQueue:      make(chan verificationWork, maxThreads*2),
		providerSlots:              make(chan struct{}, maxThreads),
	}
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

// workerCapacity starts enough dequeue loops to use every configured provider
// slot. Verification workers share those slots, so idle verification capacity
// never reduces transfer throughput.
func workerCapacity(maxThreads int) (transferWorkers, verificationWorkers int) {
	if maxThreads < 1 {
		maxThreads = 1
	}
	return maxThreads, min(4, maxThreads)
}

// dequeueVerificationWork alternates preference when both entity queues have
// work. Separate queues prevent a long migration backlog from starving sync
// passes whose coordinator is waiting in VERIFYING.
func (p *Processor) dequeueVerificationWork(ctx context.Context, preferSync bool) (verificationWork, bool) {
	preferred, alternate := p.migrationVerificationQueue, p.syncVerificationQueue
	if preferSync {
		preferred, alternate = alternate, preferred
	}
	select {
	case work := <-preferred:
		return work, true
	default:
	}
	select {
	case work := <-alternate:
		return work, true
	default:
	}
	select {
	case <-ctx.Done():
		return verificationWork{}, false
	case work := <-preferred:
		return work, true
	case work := <-alternate:
		return work, true
	}
}

func (p *Processor) startVerificationDispatcher(ctx context.Context) {
	for i := 0; i < p.verificationWorkers; i++ {
		p.verificationWG.Add(1)
		go func() {
			defer p.verificationWG.Done()
			preferSync := false
			for {
				work, ok := p.dequeueVerificationWork(ctx, preferSync)
				if !ok {
					return
				}
				preferSync = !preferSync
				if !p.acquireProviderSlot(ctx) {
					p.verifyingEntities.Delete(work.key)
					return
				}
				work.run(ctx)
				p.releaseProviderSlot()
				p.verifyingEntities.Delete(work.key)
			}
		}()
	}
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

// Start runs the worker dequeue loop and background schedulers
func (p *Processor) Start(ctx context.Context) {
	processorLogf("[Worker %s] Started and waiting for tasks with max %d threads...\n", p.workerID, p.maxThreads)

	// Recover any abandoned tasks on startup
	if err := p.queue.RecoverAbandonedTasks(ctx, p.db, p.workerID); err != nil {
		processorLogf("[Worker %s] Error recovering abandoned tasks: %v\n", p.workerID, err)
	}

	p.startVerificationDispatcher(ctx)

	// Spawn background schedulers
	go p.RunWorkerLiveness(ctx)
	go p.RunAbandonedWorkerCleanup(ctx)
	go p.RunRetryScheduler(ctx)
	go p.RunConnectionRecoveryScheduler(ctx)
	go p.RunOrphanedRunningTasksRecovery(ctx)
	go p.RunNotifier(ctx)
	go p.RunProgressReconciler(ctx)
	go p.RunChecksumVerifier(ctx)

	// Start Cancel Listener
	go p.queue.SubscribeToCancelEvents(ctx, func(migrationID string) {
		processorLogf("[Worker %s] Received Cancel Event for Migration: %s\n", p.workerID, migrationID)
		p.activeTasks.Range(func(key, value interface{}) bool {
			info, ok := value.(activeTaskInfo)
			if ok && info.migrationID == migrationID {
				processorLogf("[Worker %s] Cancelling active stream for task: %s\n", p.workerID, key)
				info.cancel()
			}
			return true
		})
	})

	// Sync transfers have their own control channel because their lifecycle is
	// coordinated by the sync engine rather than the migration processor.
	go p.queue.SubscribeToSyncCancelEvents(ctx, func(event queue.SyncCancelEvent) {
		processorLogf("[Worker %s] Received Cancel Event for Sync Job: %s generation %d\n", p.workerID, event.SyncJobID, event.PassGeneration)
		p.activeTasks.Range(func(key, value interface{}) bool {
			info, ok := value.(activeTaskInfo)
			if ok && info.syncJobID == event.SyncJobID && (event.AllGenerations || info.syncGeneration == event.PassGeneration) {
				processorLogf("[Worker %s] Cancelling active sync stream for task: %s\n", p.workerID, key)
				info.cancel()
			}
			return true
		})
	})

	// Start Bandwidth Change Listener
	go p.queue.SubscribeToBandwidthChanges(ctx, func(event queue.BandwidthEvent) {
		jobID := event.MigrationID
		jobType := "migration"
		if event.SyncJobID != "" {
			jobID = event.SyncJobID
			jobType = "sync job"
		}
		processorLogf("[Worker %s] Bandwidth change for %s %s: %d Mbps",
			p.workerID, jobType, jobID, event.BandwidthLimitMbps)
		if throttler, ok := p.throttlers.Load(jobID); ok {
			throttler.(*throttle.MigrationThrottler).SetLimit(event.BandwidthLimitMbps)
		}
	})

	// Start a PostgreSQL LISTEN watcher so idle threads can be woken up
	// immediately when new tasks are inserted (pg_notify 'task_available').
	// Falls back to periodic polling if the listener cannot be established.
	var notifyTasksCh <-chan struct{}
	if p.dbConnStr != "" {
		ch, err := queue.ListenForTasks(ctx, p.dbConnStr)
		if err != nil {
			processorLogf("[Worker %s] LISTEN task_available unavailable (falling back to polling): %v\n", p.workerID, err)
		} else {
			notifyTasksCh = ch
			processorLogf("[Worker %s] LISTEN task_available active — idle threads will wake immediately on new tasks\n", p.workerID)
		}
	}

	p.runTransferWorkers(ctx, notifyTasksCh)
	return

}

// useTempThenRename reports whether the processor should use the
// "upload to <path>.tmp then atomically rename" overwrite pattern for the
// given target. It is only safe when (a) an overwrite/retry actually requires
// the temp file and (b) the target provider supports a rename operation.
// Providers without atomic-rename support write the file directly to its final
// name during upload. This includes S3, whose copy-and-delete "rename" would
// otherwise require deleting an existing object before a potentially failing
// copy completes.
func useTempThenRename(target storage.StorageProvider, deleteAfterUpload bool) bool {
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

func cleanupStagingUpload(ctx context.Context, target storage.StorageProvider, resourceType, uploadPath string) {
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
func promoteOverwrite(ctx context.Context, target storage.StorageProvider, resourceType, targetPath, uploadPath, backupPath string) error {
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

// oauthSyncAuthFailureRole remains as a small compatibility wrapper for the
// focused role-selection tests; both lifecycle paths use the shared resolver.
func oauthSyncAuthFailureRole(job *db.SyncJob, errText string) string {
	return oauthAuthFailureRoleForProviders(job.SourceProvider, job.TargetProvider, errText)
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

// ensureFreshOAuthToken checks whether a migration's OAuth access token is expired
// (or within 2 minutes of expiry) and, if so, performs an inline token refresh before
// the storage provider is constructed. The freshly decrypted access token is returned.
// For non-OAuth providers (no refresh token stored) the original accessToken is returned
// unchanged, making this a safe no-op for Nextcloud/WebDAV.
func (p *Processor) ensureFreshOAuthToken(ctx context.Context, mig *db.Migration, role string, accessToken string) (string, error) {
	return p.refreshOAuthTokenIfNeeded(ctx, mig, role, accessToken, false)
}

// refreshOAuthToken forces a refresh after an OAuth provider has returned 401,
// even if the stored expiry timestamp still lies in the future.
func (p *Processor) refreshOAuthToken(ctx context.Context, mig *db.Migration, role string) (string, error) {
	return p.refreshOAuthTokenIfNeeded(ctx, mig, role, "", true)
}

func applyOAuthTokensToMigration(mig *db.Migration, role string, accessEnc string, refreshEnc sql.NullString, expiresAt sql.NullTime) {
	if role == "source" {
		mig.SourcePasswordEncrypted = accessEnc
		mig.SourceRefreshTokenEncrypted = refreshEnc
		mig.SourceTokenExpiresAt = expiresAt
	} else {
		mig.TargetPasswordEncrypted = accessEnc
		mig.TargetRefreshTokenEncrypted = refreshEnc
		mig.TargetTokenExpiresAt = expiresAt
	}
}

func applyOAuthTokensToSyncJob(job *db.SyncJob, role string, accessEnc string, refreshEnc sql.NullString, expiresAt sql.NullTime) {
	if role == "source" {
		job.SourcePasswordEncrypted = accessEnc
		job.SourceRefreshTokenEncrypted = refreshEnc
		job.SourceTokenExpiresAt = expiresAt
	} else {
		job.TargetPasswordEncrypted = accessEnc
		job.TargetRefreshTokenEncrypted = refreshEnc
		job.TargetTokenExpiresAt = expiresAt
	}
}

func (p *Processor) refreshOAuthTokenIfNeeded(ctx context.Context, mig *db.Migration, role string, accessToken string, force bool) (string, error) {
	tokenSet := func(m *db.Migration) struct {
		refreshEnc sql.NullString
		expiresAt  sql.NullTime
		provider   string
		accessEnc  string
	} {
		if role == "source" {
			return struct {
				refreshEnc sql.NullString
				expiresAt  sql.NullTime
				provider   string
				accessEnc  string
			}{m.SourceRefreshTokenEncrypted, m.SourceTokenExpiresAt, m.SourceProvider, m.SourcePasswordEncrypted}
		}
		return struct {
			refreshEnc sql.NullString
			expiresAt  sql.NullTime
			provider   string
			accessEnc  string
		}{m.TargetRefreshTokenEncrypted, m.TargetTokenExpiresAt, m.TargetProvider, m.TargetPasswordEncrypted}
	}

	initial := tokenSet(mig)
	refreshTokenEnc, expiresAt, provider := initial.refreshEnc, initial.expiresAt, initial.provider

	if !refreshTokenEnc.Valid || refreshTokenEnc.String == "" {
		if force {
			return "", fmt.Errorf("no OAuth refresh token is stored for %s", role)
		}
		return accessToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	// Acquire lock to serialize token refresh requests for the same migration
	lockKey := fmt.Sprintf("migration:%s:%s", mig.ID, role)
	releaseRefreshLock := p.refreshLocks.lock(lockKey)
	defer releaseRefreshLock()

	var lockToken string
	if p.queue != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = p.queue.TryClaimOAuthLock(ctx, "migration", mig.ID, role, 30*time.Second)
			if err == nil && claimed {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if latestMig, lerr := db.GetMigration(p.db, mig.ID); lerr == nil {
				latest := tokenSet(latestMig)
				if latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
					if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for migration %s (%s)", mig.ID, role)
		}
		defer p.queue.ReleaseOAuthLock(ctx, "migration", mig.ID, role, lockToken)
	}

	// Double-check: re-query the migration from the database after acquiring the lock to get the latest tokens.
	latestMig, err := db.GetMigration(p.db, mig.ID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest migration details inside refresh lock: %w", err)
	}

	latest := tokenSet(latestMig)
	if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
		accessToken = latestAccess
	}
	refreshTokenEnc, expiresAt, provider = latest.refreshEnc, latest.expiresAt, latest.provider

	// Another task or the rotation daemon may already have recovered this
	// credential while this task waited for the distributed refresh lock. Reuse
	// the newer access token instead of rotating a freshly issued refresh token
	// again (Microsoft can invalidate older refresh tokens during rotation).
	if force && latestMig.UpdatedAt.After(mig.UpdatedAt) {
		applyOAuthTokensToMigration(mig, role, latest.accessEnc, latest.refreshEnc, latest.expiresAt)
		mig.UpdatedAt = latestMig.UpdatedAt
		return accessToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	processorLogf("[Worker %s] %s OAuth token expired or near expiry for migration %s — refreshing inline\n",
		p.workerID, role, mig.ID)

	refreshToken, err := crypto.DecryptWithDomain(refreshTokenEnc.String, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token: %w", role, err)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for %s (%s): %w", role, provider, err)
	}

	newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, p.secretKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s access token after refresh: %w", role, err)
	}
	newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s refresh token after refresh: %w", role, err)
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	expectedRefreshEnc := refreshTokenEnc.String
	err = db.UpdateMigrationOAuthTokens(p.db, db.OAuthTokenUpdate{
		MigrationID:           mig.ID,
		Role:                  role,
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             newExpiresAt,
	}, expectedRefreshEnc)

	if errors.Is(err, db.ErrOAuthTokenConflict) {
		processorLogf("[Worker %s] Token update conflict for migration %s (%s) — adopting winner token from DB\n", p.workerID, mig.ID, role)
		if latestMig, lerr := db.GetMigration(p.db, mig.ID); lerr == nil {
			latest := tokenSet(latestMig)
			applyOAuthTokensToMigration(mig, role, latest.accessEnc, latest.refreshEnc, latest.expiresAt)
			mig.UpdatedAt = latestMig.UpdatedAt
			if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for migration %s (%s): %w", mig.ID, role, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

	applyOAuthTokensToMigration(mig, role, newAccessEnc, sql.NullString{String: newRefreshEnc, Valid: true}, sql.NullTime{Time: newExpiresAt, Valid: true})

	return tokenResp.AccessToken, nil
}

// ensureFreshSyncOAuthToken checks whether a sync job's OAuth access token is expired
// (or within 2 minutes of expiry) and, if so, performs an inline token refresh under
// a per-job/role distributed lock before provider construction.
func (p *Processor) ensureFreshSyncOAuthToken(ctx context.Context, job *db.SyncJob, role string, currentToken string) (string, error) {
	return p.refreshSyncOAuthTokenIfNeeded(ctx, job, role, currentToken, false)
}

func (p *Processor) refreshSyncOAuthToken(ctx context.Context, job *db.SyncJob, role string) (string, error) {
	return p.refreshSyncOAuthTokenIfNeeded(ctx, job, role, "", true)
}

func (p *Processor) refreshSyncOAuthTokenIfNeeded(ctx context.Context, job *db.SyncJob, role string, currentToken string, force bool) (string, error) {
	tokenSet := func(j *db.SyncJob) struct {
		refreshEnc sql.NullString
		expiresAt  sql.NullTime
		provider   string
		accessEnc  string
	} {
		if role == "source" {
			return struct {
				refreshEnc sql.NullString
				expiresAt  sql.NullTime
				provider   string
				accessEnc  string
			}{j.SourceRefreshTokenEncrypted, j.SourceTokenExpiresAt, j.SourceProvider, j.SourcePasswordEncrypted}
		}
		return struct {
			refreshEnc sql.NullString
			expiresAt  sql.NullTime
			provider   string
			accessEnc  string
		}{j.TargetRefreshTokenEncrypted, j.TargetTokenExpiresAt, j.TargetProvider, j.TargetPasswordEncrypted}
	}

	initial := tokenSet(job)
	refreshTokenEnc, expiresAt, provider := initial.refreshEnc, initial.expiresAt, initial.provider

	if !refreshTokenEnc.Valid || refreshTokenEnc.String == "" {
		if force {
			return "", fmt.Errorf("no OAuth refresh token is stored for %s", role)
		}
		return currentToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return currentToken, nil
	}

	lockKey := fmt.Sprintf("sync:%s:%s", job.ID, role)
	releaseRefreshLock := p.refreshLocks.lock(lockKey)
	defer releaseRefreshLock()

	var lockToken string
	if p.queue != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = p.queue.TryClaimOAuthLock(ctx, "sync", job.ID, role, 30*time.Second)
			if err == nil && claimed {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if latestJob, lerr := db.GetSyncJob(p.db, job.ID); lerr == nil {
				latest := tokenSet(latestJob)
				if latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
					if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
						return latestAccess, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for sync job %s (%s)", job.ID, role)
		}
		defer p.queue.ReleaseOAuthLock(ctx, "sync", job.ID, role, lockToken)
	}

	latestJob, err := db.GetSyncJob(p.db, job.ID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest sync job details inside refresh lock: %w", err)
	}

	latest := tokenSet(latestJob)
	if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
		currentToken = latestAccess
	}
	refreshTokenEnc, expiresAt, provider = latest.refreshEnc, latest.expiresAt, latest.provider

	if force && latestJob.UpdatedAt.After(job.UpdatedAt) {
		applyOAuthTokensToSyncJob(job, role, latest.accessEnc, latest.refreshEnc, latest.expiresAt)
		job.UpdatedAt = latestJob.UpdatedAt
		return currentToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return currentToken, nil
	}

	processorLogf("[Worker %s] %s OAuth token expired or near expiry for sync job %s — refreshing inline\n",
		p.workerID, role, job.ID)

	refreshToken, err := crypto.DecryptWithDomain(refreshTokenEnc.String, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token for sync job: %w", role, err)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for sync job %s (%s): %w", role, provider, err)
	}

	newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, p.secretKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s access token after refresh: %w", role, err)
	}
	newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s refresh token after refresh: %w", role, err)
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	expectedRefreshEnc := refreshTokenEnc.String
	err = db.UpdateSyncJobOAuthTokens(p.db, job.ID, role, newAccessEnc, newRefreshEnc, newExpiresAt, expectedRefreshEnc)
	if errors.Is(err, db.ErrOAuthTokenConflict) {
		processorLogf("[Worker %s] Token update conflict for sync job %s (%s) — adopting winner token from DB\n", p.workerID, job.ID, role)
		if latestJob, lerr := db.GetSyncJob(p.db, job.ID); lerr == nil {
			latest := tokenSet(latestJob)
			applyOAuthTokensToSyncJob(job, role, latest.accessEnc, latest.refreshEnc, latest.expiresAt)
			job.UpdatedAt = latestJob.UpdatedAt
			if latestAccess, derr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for sync job %s (%s): %w", job.ID, role, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

	applyOAuthTokensToSyncJob(job, role, newAccessEnc, sql.NullString{String: newRefreshEnc, Valid: true}, sql.NullTime{Time: newExpiresAt, Valid: true})

	return tokenResp.AccessToken, nil
}

// RunCompletionNotifier and email reporting functions are located in notifier.go.

// verifyTargetSize queries the target for existence and size, retrying on
// transient errors (Nextcloud 502/503/423/timeout). A transient failure to
// *query* verification must not be mistaken for a corrupt transfer, so we retry
// before giving up. Returns the last result after the attempts.
func verifyTargetSize(ctx context.Context, client storage.StorageProvider, resourceType, path string) (exists bool, size int64, err error) {
	for attempt := 0; attempt < 3; attempt++ {
		exists, size, err = client.FileExists(ctx, resourceType, path)
		if err == nil {
			return exists, size, nil
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return exists, size, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return exists, size, err
}

// isNonRetryableHashError reports whether a GetFileHash error (or nil error) indicates
// that file hashes are permanently unsupported or unavailable for the file/provider,
// meaning retries will not yield a hash and should be skipped immediately.
func isNonRetryableHashError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, storage.ErrHashNotSupported) || errors.Is(err, storage.ErrChecksumNotAvailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checksum not available") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "not implemented") ||
		strings.Contains(msg, "is a directory") ||
		strings.Contains(msg, "is a folder")
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
