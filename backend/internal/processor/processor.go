package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math"
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
	"backend/internal/megasecret"
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
	migrationID string
	syncJobID   string
	cancel      context.CancelFunc
}

type Processor struct {
	db                         *sql.DB
	queue                      *queue.Queue
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

// ResolveTargetPath computes the target file or directory path for a task
// considering target directory and target filename/directory sanitization.
// Immich sources/targets are always flat (the library root), but an Immich
// source's asset UUID is renamed to its original filename from task metadata so
// the destination keeps human-readable names. No virtual album/path translation
// is applied.
func ResolveTargetPath(resourceType, filePath string, metadata []byte, targetDir, sourceProvider, targetProvider string) string {
	if resourceType != "files" {
		return filePath
	}

	relativePath := filePath

	// Immich sources store the original filename in metadata; replace the asset
	// UUID with it while staying flat (no album structure).
	if sourceProvider == "immich" {
		if filename := immichFilenameFromMetadata(metadata); filename != "" {
			relativePath = path.Join(path.Dir(relativePath), path.Base(filename))
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
	sec := 10 * int(math.Pow(3, float64(attempt-1)))
	if sec > 90 {
		sec = 90
	}
	return time.Duration(sec) * time.Second
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

// queryTargetSize reports whether the target file exists and its size. When retry
// is true, transient query errors are retried (used for integrity checks where a
// transient Nextcloud 502/503/423 must not be mistaken for a corrupt transfer).
func queryTargetSize(ctx context.Context, client storage.StorageProvider, resourceType, p string, retry bool) (exists bool, size int64, err error) {
	if retry {
		return verifyTargetSize(ctx, client, resourceType, p)
	}
	return client.FileExists(ctx, resourceType, p)
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
	log.Printf("[Worker %s] Started and waiting for tasks with max %d threads...\n", p.workerID, p.maxThreads)

	// Recover any abandoned tasks on startup
	if err := p.queue.RecoverAbandonedTasks(ctx, p.db, p.workerID); err != nil {
		log.Printf("[Worker %s] Error recovering abandoned tasks: %v\n", p.workerID, err)
	}

	p.startVerificationDispatcher(ctx)

	// Spawn background schedulers
	go p.RunWorkerLiveness(ctx)
	go p.RunRetryScheduler(ctx)
	go p.RunConnectionRecoveryScheduler(ctx)
	go p.RunOrphanedRunningTasksRecovery(ctx)
	go p.RunNotifier(ctx)
	go p.RunProgressReconciler(ctx)
	go p.RunChecksumVerifier(ctx)

	// Start Cancel Listener
	go p.queue.SubscribeToCancelEvents(ctx, func(migrationID string) {
		log.Printf("[Worker %s] Received Cancel Event for Migration: %s\n", p.workerID, migrationID)
		p.activeTasks.Range(func(key, value interface{}) bool {
			info, ok := value.(activeTaskInfo)
			if ok && info.migrationID == migrationID {
				log.Printf("[Worker %s] Cancelling active stream for task: %s\n", p.workerID, key)
				info.cancel()
			}
			return true
		})
	})

	// Sync transfers have their own control channel because their lifecycle is
	// coordinated by the sync engine rather than the migration processor.
	go p.queue.SubscribeToSyncCancelEvents(ctx, func(syncJobID string) {
		log.Printf("[Worker %s] Received Cancel Event for Sync Job: %s\n", p.workerID, syncJobID)
		p.activeTasks.Range(func(key, value interface{}) bool {
			info, ok := value.(activeTaskInfo)
			if ok && info.syncJobID == syncJobID {
				log.Printf("[Worker %s] Cancelling active sync stream for task: %s\n", p.workerID, key)
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
		log.Printf("[Worker %s] Bandwidth change for %s %s: %d Mbps",
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
			log.Printf("[Worker %s] LISTEN task_available unavailable (falling back to polling): %v\n", p.workerID, err)
		} else {
			notifyTasksCh = ch
			log.Printf("[Worker %s] LISTEN task_available active — idle threads will wake immediately on new tasks\n", p.workerID)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < p.transferWorkers; i++ {
		wg.Add(1)
		go func(threadID int) {
			defer wg.Done()
			// fallbackPoll is the maximum time an idle thread waits before
			// re-polling even without a notify signal. 5s is fine because
			// pg_notify delivers the wake-up immediately in the common case.
			// Without LISTEN it falls back to the old 2s behaviour so
			// throughput is not affected when LISTEN is unavailable.
			fallbackInterval := 5 * time.Second
			if notifyTasksCh == nil {
				// LISTEN unavailable: use the original 2s poll to maintain
				// latency parity with the pre-optimisation behaviour.
				fallbackInterval = 2 * time.Second
			}
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Dequeue task from PostgreSQL
					payload, err := p.queue.DequeueSQL(ctx, p.db, p.workerID)
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						log.Printf("[Worker %s] Thread %d dequeue error: %v. Sleeping...\n", p.workerID, threadID, err)
						time.Sleep(2 * time.Second)
						continue
					}

					if payload == nil {
						// No task: wait for a pg_notify signal or fallback timeout.
						// This eliminates the busy-poll while still reacting quickly.
						select {
						case <-ctx.Done():
							return
						case <-notifyTasksCh:
							// Woken by pg_notify — go straight back to DequeueSQL
						case <-time.After(fallbackInterval):
							// Periodic fallback poll
						}
						continue
					}
					if !p.acquireProviderSlot(ctx) {
						return
					}

					if payload.SyncJobID != "" {
						log.Printf("[Worker %s] Thread %d processing sync task %s for job %s\n", p.workerID, threadID, payload.TaskID, payload.SyncJobID)
						err = p.processSyncTask(ctx, payload, threadID)
						if err != nil {
							log.Printf("[Worker %s] Thread %d error processing sync task %s: %v\n", p.workerID, threadID, payload.TaskID, err)
							p.handleSyncTaskFailure(ctx, payload, err)
						} else {
							log.Printf("[Worker %s] Thread %d successfully processed sync task %s\n", p.workerID, threadID, payload.TaskID)
						}
					} else {
						log.Printf("[Worker %s] Thread %d processing migration task %s for migration %s\n", p.workerID, threadID, payload.TaskID, payload.MigrationID)
						err = p.processTask(ctx, payload, threadID)
						if err != nil {
							log.Printf("[Worker %s] Thread %d error processing task %s: %v\n", p.workerID, threadID, payload.TaskID, err)
							p.handleTaskFailure(ctx, payload, err)
						} else {
							log.Printf("[Worker %s] Thread %d successfully processed task %s\n", p.workerID, threadID, payload.TaskID)
						}
					}
					p.releaseProviderSlot()
				}
			}
		}(i)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	log.Printf("[Worker %s] Shutdown signal received. Waiting for active tasks to finish...\n", p.workerID)
	wg.Wait()
	p.verificationWG.Wait()
	log.Printf("[Worker %s] Worker loop stopped.\n", p.workerID)
	// Background schedulers (RunWorkerLiveness, RunRetryScheduler, RunProgressReconciler,
	// RunOrphanedRunningTasksRecovery, RunConnectionRecoveryScheduler) are located in schedulers.go.
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
		log.Printf("Warning: failed to clean up staging upload %s: %v", uploadPath, err)
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

func (p *Processor) processTask(ctx context.Context, payload *queue.Payload, threadID int) (err error) {
	// Shadow ctx with a cancelable one
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	p.activeTasks.Store(payload.TaskID, activeTaskInfo{
		migrationID: payload.MigrationID,
		cancel:      cancel,
	})
	defer func() {
		p.activeTasks.Delete(payload.TaskID)
	}()

	// 1. Fetch Migration from DB
	mig, err := db.GetMigration(p.db, payload.MigrationID)
	if err != nil {
		return fmt.Errorf("failed to fetch migration: %w", err)
	}

	// Get or create throttler for this migration
	throttler, _ := p.throttlers.LoadOrStore(payload.MigrationID, throttle.NewMigrationThrottler(mig.BandwidthLimitMbps))
	migrationThrottler := throttler.(*throttle.MigrationThrottler)

	// If migration is paused or in connection loss, return nil (task stays in RUNNING, but we want it PENDING)
	// Actually, DequeueSQL only picks PENDING. If migration is paused, DequeueSQL won't pick it!
	// But just in case status changed right after dequeue:
	if mig.Status == "PAUSED_CONNECTION_LOSS" || mig.Status == "PAUSED" {
		// Set back to pending
		_ = db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "PENDING")
		time.Sleep(2 * time.Second)
		return nil
	}
	ctx = storage.WithLocalUserScope(ctx, mig.UserID.String)

	// If migration is in a terminal state (COMPLETED, COMPLETED_WITH_ERRORS or FAILED), mark task as skipped/failed
	if mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED" {
		_ = db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "SKIPPED")
		return nil
	}

	// If migration was cancelled, mark the task cancelled and stop
	if mig.Status == "CANCELLED" {
		_ = db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "CANCELLED")
		return nil
	}

	// If migration is in any other non-running state, requeue and return error
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		_ = db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "PENDING")
		return fmt.Errorf("migration is in state %s, task skipped for now", mig.Status)
	}

	// 2. Fetch Task from DB
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}
	task.ClaimEpoch = payload.ClaimEpoch

	logPath := task.FilePath
	if task.ResourceType == "files" {
		logPath = ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
	}

	log.Printf("[Worker %s] Thread %d -> Request: [%s] %s (%d bytes) [%s -> %s]\n",
		p.workerID, threadID, strings.ToUpper(task.ResourceType), logPath, task.FileSize, mig.SourceProvider, mig.TargetProvider)

	// Decrypt credentials
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}
	defer crypto.ZeroString(&sourcePass)

	targetPass, err := crypto.Decrypt(mig.TargetPasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}
	defer crypto.ZeroString(&targetPass)

	// For OAuth providers: if the token is expired or within 2 minutes of expiry,
	// refresh it now so this task does not hit a 401. The daemon already handles
	// proactive rotation every 5 min, but tasks could be dequeued right as a token
	// expires, so we have this last-resort inline refresh.
	sourceProviderPass, err := p.ensureFreshOAuthToken(ctx, mig, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}
	defer crypto.ZeroString(&sourceProviderPass)

	targetProviderPass, err := p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}
	defer crypto.ZeroString(&targetProviderPass)

	// Providers are task-scoped because they retain credentials internally.
	sourceCtx, err := megasecret.WithSession(ctx, mig.SourceProvider, mig.SourceMegaSessionIDEncrypted, mig.SourceMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source MEGA session: %w", err)
	}
	targetCtx, err := megasecret.WithSession(ctx, mig.TargetProvider, mig.TargetMegaSessionIDEncrypted, mig.TargetMegaMasterKeyEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target MEGA session: %w", err)
	}
	sourceClient, err := newProvider(sourceCtx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourceProviderPass)
	if err != nil {
		return fmt.Errorf("failed to create source client: %w", err)
	}
	defer sourceClient.Close()

	targetClient, err := newProvider(targetCtx, mig.TargetProvider, mig.TargetURL, mig.TargetUsername, targetProviderPass)
	if err != nil {
		return fmt.Errorf("failed to create target client: %w", err)
	}
	defer targetClient.Close()

	if nc, ok := sourceClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}
	if nc, ok := targetClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}

	// Update task status to RUNNING in DB
	task.Status = "RUNNING"
	_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)

	// Skip read-only system or app-generated calendar/contact collections
	if task.ResourceType != "files" && storage.IsSystemOrAppGeneratedPath(task.FilePath) {
		task.Status = "SKIPPED"
		task.ErrorMessage = sql.NullString{String: "Skipped read-only system or app-generated collection (SKIP)", Valid: true}
		if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 1, 0, task.FileSize); err != nil {
			return err
		}
		return nil
	}
	// Existing jobs may have been indexed before Personal Vault exclusion was
	// introduced. When Graph still exposes its metadata, turn such a task into
	// a deliberate skip instead of retrying an interactive-only vault download.
	if mig.SourceProvider == "onedrive" && task.ResourceType == "files" {
		if resource, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr == nil && resource.IsPersonalVault() {
			task.Status = "SKIPPED"
			task.ErrorMessage = sql.NullString{String: "OneDrive Personal Vault cannot be migrated through the API", Valid: true}
			if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 1, 0, task.FileSize); err != nil {
				return err
			}
			return nil
		}
	}

	// Handle directory creation tasks (action == "mkdir").
	// These are emitted by the indexer for every directory encountered so that
	// empty directories are created on the target. We skip the full
	// download/upload pipeline and just call CreateDirectory.
	var taskMeta map[string]interface{}
	if task.Metadata != nil {
		if err := json.Unmarshal(task.Metadata, &taskMeta); err != nil {
			log.Printf("[Worker] Failed to parse task metadata for task %s: %v", task.ID, err)
			return fmt.Errorf("failed to parse task metadata: %w", err)
		}
	}
	action, _ := taskMeta["action"].(string)
	if action == "mkdir" || strings.HasSuffix(task.FilePath, "/") {
		targetPath := task.FilePath
		if task.ResourceType == "files" {
			targetPath = ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)

			// Check for case collisions on case-insensitive providers
			if sanitize.IsCaseInsensitive(mig.TargetProvider) {
				dirPath := path.Dir(targetPath)
				dirName := path.Base(targetPath)
				if collision, _ := sanitize.CheckCaseCollision(ctx, targetClient, task.ResourceType, dirPath, dirName); collision != "" {
					log.Printf("[Worker] Directory case collision detected: %s conflicts with %s", targetPath, collision)
					// Skip this directory to avoid conflicts
					task.Status = "SKIPPED"
					task.ErrorMessage = sql.NullString{String: fmt.Sprintf("Directory skipped due to case collision with %s", collision), Valid: true}
					if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, 0, 0, 0, 0); err != nil {
						return err
					}
					return nil
				}
			}
		}
		if err := targetClient.CreateDirectory(ctx, task.ResourceType, targetPath); err != nil {
			return fmt.Errorf("failed to create directory %s on target: %w", targetPath, err)
		}
		task.Status = "COMPLETED"
		task.ErrorMessage = sql.NullString{}
		if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, 0, 0, 0, 0); err != nil {
			return err
		}
		p.clearConnLoss(mig.ID)
		p.clearConnLossTask(task.ID)
		return nil
	}

	// 3. Conflict Resolution
	var deleteAfterUpload bool // set true by OVERWRITE: delete original only after upload succeeds
	targetPath := task.FilePath
	if task.ResourceType == "files" {
		targetPath = ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
	}

	// Synchronize parallel worker threads operating on the exact same target path
	unlockTarget := p.lockTargetFile(targetPath)
	defer unlockTarget()

	// 3a. Filename Sanitization (before conflict resolution)
	if task.ResourceType == "files" && mig.TargetProvider != "immich" {
		result := sanitize.SanitizeFilename(path.Base(targetPath), mig.TargetProvider)
		if result.Changed {
			dir := path.Dir(targetPath)
			targetPath = path.Join(dir, result.SanitizedName)
			log.Printf("[SANITIZE] %s: \"%s\" → \"%s\" (%s)",
				task.ID, result.OriginalName, result.SanitizedName, strings.Join(result.Reasons, ", "))
			_ = db.UpdateClaimedTaskFilePath(p.db, ctx, task.ID, task.ClaimEpoch, targetPath)
		}

		if sanitize.IsCaseInsensitive(mig.TargetProvider) {
			collision, err := sanitize.CheckCaseCollision(ctx, targetClient, task.ResourceType,
				path.Dir(targetPath), path.Base(targetPath))
			if err != nil {
				log.Printf("Warning: case collision check failed: %v", err)
			} else if collision != "" {
				resolved, err := sanitize.ResolveCollision(ctx, targetClient, task.ResourceType,
					path.Dir(targetPath), path.Base(targetPath), mig.TargetProvider)
				if err != nil {
					return fmt.Errorf("failed to resolve case collision: %w", err)
				}
				targetPath = path.Join(path.Dir(targetPath), resolved)
				log.Printf("[COLLISION] %s: case collision with \"%s\" → \"%s\"",
					task.ID, collision, path.Base(targetPath))
				_ = db.UpdateClaimedTaskFilePath(p.db, ctx, task.ID, task.ClaimEpoch, targetPath)
			}
		}
	}

	// The database and API allow only SKIP, OVERWRITE, and RENAME, but retain
	// this guard for legacy rows and direct database writes. Falling through to
	// an upload would overwrite an existing WebDAV target via PUT.
	if !db.ValidConflictStrategy(mig.ConflictStrategy) {
		return fmt.Errorf("invalid migration conflict strategy %q", mig.ConflictStrategy)
	}

	_, nativeDuplicates := targetClient.(storage.NativeDuplicateDetector)
	if nativeDuplicates {
		// Immich determines duplicates from its native asset identity/checksum;
		// filename preflight would be both inaccurate and unsafe.
	} else if task.ResourceType == "files" && mig.ConflictStrategy == "OVERWRITE" {
		// Optimization: for OVERWRITE on files, bypass the pre-flight FileExists
		// network query (PROPFIND/HEAD) since the file will be overwritten regardless.
		deleteAfterUpload = true
	} else {
		exists, _, err := targetClient.FileExists(ctx, task.ResourceType, targetPath)
		if err != nil {
			if isWebDAVSystemConflict(err) {
				return p.skipTask(ctx, task, fmt.Sprintf("Target file existence check skipped (WebDAV system conflict): %v", err))
			}
			return fmt.Errorf("failed to check if target file exists: %w", err)
		}

		if exists {
			// Calendars and contacts are always overwritten: they are dynamic data and
			// a SKIP would silently leave stale entries from a previous failed run.
			if task.ResourceType != "files" {
				err = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
				if err != nil {
					if isWebDAVSystemConflict(err) {
						return p.skipTask(ctx, task, fmt.Sprintf("Skipped calendar/contact entry: %v", err))
					}
					return fmt.Errorf("failed to delete existing calendar/contact entry for overwrite: %w", err)
				}
			} else {
				switch mig.ConflictStrategy {
				case "SKIP":
					// An existing target must never be overwritten under SKIP,
					// including after a create-only HiDrive POST reports a race.
					if exists {
						return p.skipTask(ctx, task, "File already exists in target (SKIP)")
					}

				case "RENAME":
					// Generate new target name
					dir := path.Dir(targetPath)
					ext := path.Ext(targetPath)
					base := strings.TrimSuffix(path.Base(targetPath), ext)

					counter := 1
					for {
						candidatePath := path.Join(dir, fmt.Sprintf("%s_copy%d%s", base, counter, ext))
						candidateExists, _, err := targetClient.FileExists(ctx, task.ResourceType, candidatePath)
						if err != nil {
							if isWebDAVSystemConflict(err) {
								return p.skipTask(ctx, task, fmt.Sprintf("Target rename candidate check skipped (WebDAV system conflict): %v", err))
							}
							return fmt.Errorf("failed to check existence of rename candidate: %w", err)
						}
						if !candidateExists {
							targetPath = candidatePath
							_ = db.UpdateClaimedTaskFilePath(p.db, ctx, task.ID, task.ClaimEpoch, targetPath)
							break
						}
						counter++
						if counter > 100 {
							return fmt.Errorf("failed to rename target file after 100 attempts")
						}
					}
				}
			}
		}
	}

	// 4. Download and Upload stream
	// Providers without atomic-rename support (e.g. S3, Immich) write the
	// file to its final name during upload, so the ".tmp" suffix must never be
	// applied — otherwise the provider has to strip it itself and the rename
	// step below is skipped anyway. Centralising this here avoids leaking the
	// ".tmp" artefact into logs/task bookkeeping for those providers.
	uploadPath := targetPath
	if useTempThenRename(targetClient, deleteAfterUpload) {
		uploadPath = targetPath + ".tmp"
	}

	// Per-request timeout scaled by file size (same policy as uploads, see
	// transferTimeout). Computed once and applied to both the download and
	// upload phases so the two phases share a single, consistent deadline.
	transferDeadline := transferTimeout(task.FileSize)
	downloadCtx, downloadCancel := context.WithTimeout(ctx, transferDeadline)
	defer downloadCancel()

	downloadStream, err := sourceClient.StreamDownload(downloadCtx, task.ResourceType, task.FilePath)
	if err != nil {
		return fmt.Errorf("failed to download from source: %w", err)
	}
	// Wrap download stream with throttling (before TeeReader to limit actual network I/O)
	defer downloadStream.Close()
	throttledDownloadStream := throttle.NewThrottledReader(downloadStream, migrationThrottler, downloadCtx)

	// Handle Hash Algorithm Selection
	var sourceHasher hash.Hash
	sourceAlgo := "SHA1" // Default
	sourceHashStr := ""

	if task.SourceHash.Valid && task.SourceHash.String != "" && mig.SourceProvider != "webdav" {
		algo, cleanHash := storage.ParseHashString(task.SourceHash.String)
		// HiDrive's native chash is only comparable with another HiDrive
		// chash; it cannot be recreated while streaming to another provider.
		if algo == "SHA1" || algo == "SHA256" || algo == "MD5" || algo == "DROPBOX" {
			sourceHashStr = cleanHash
			sourceAlgo = algo
		}
	}

	if mig.SourceProvider == "dropbox" {
		sourceAlgo = "DROPBOX"
	} else if mig.SourceProvider == "google" {
		sourceAlgo = "MD5"
	} else if mig.SourceProvider == "onedrive" {
		sourceAlgo = "QUICKXOR"
	}

	// Instantiate source hasher
	if sourceAlgo == "MD5" {
		sourceHasher = md5.New()
	} else if sourceAlgo == "DROPBOX" {
		sourceHasher = storage.NewDropboxHasher()
	} else if sourceAlgo == "SHA256" {
		sourceHasher = sha256.New()
	} else if sourceAlgo == "QUICKXOR" {
		sourceHasher = storage.NewQuickXorHasher()
	} else {
		sourceHasher = sha1.New()
		sourceAlgo = "SHA1"
	}

	// Determine target hasher algorithm
	var targetHasher hash.Hash
	targetAlgo := "SHA1" // Default
	if mig.TargetProvider == "dropbox" {
		targetAlgo = "DROPBOX"
		targetHasher = storage.NewDropboxHasher()
	} else if mig.TargetProvider == "s3" {
		targetAlgo = "SHA256"
		targetHasher = sha256.New()
	} else if mig.TargetProvider == "google" {
		targetAlgo = "MD5"
		targetHasher = md5.New()
	} else if mig.TargetProvider == "hidrive" {
		targetAlgo = "HIDRIVE"
		targetHasher = storage.NewHiDriveHasher()
	} else if mig.TargetProvider == "onedrive" {
		targetAlgo = "QUICKXOR"
		targetHasher = storage.NewQuickXorHasher()
	} else {
		targetAlgo = "SHA1"
		targetHasher = sha1.New()
	}

	// We only need two hashers if the algorithms differ
	var activeWriter io.Writer
	if sourceAlgo == targetAlgo {
		activeWriter = sourceHasher
		targetHasher = nil // Disable target hasher to save CPU cycles
	} else {
		activeWriter = io.MultiWriter(sourceHasher, targetHasher)
	}

	progressChan := make(chan int64, 10)
	progressDone := make(chan struct{})

	// lastByteNano tracks the most recent monotonic clock time that any byte was
	// reported for this task. It is shared with the heartbeat goroutine via the
	// atomic so the heartbeat can tell whether the transfer is genuinely
	// progressing or hung (no bytes for a long stretch).
	var lastByteNano = time.Now().UnixNano()
	taskStart := time.Now()

	go func() {
		defer close(progressDone)
		// This goroutine drains the progress channel and feeds the non-cumulative
		// live_bytes counter (used only for the transfer-speed / ETA display).
		// Cumulative processed_bytes are booked exactly once at verified
		// completion (see below), so we must NOT add streamed bytes to it here —
		// doing so previously caused processed_bytes to exceed total_bytes when a
		// file was retried (e.g. after a hash mismatch re-ran the whole upload).
		var bufferedBytes int64
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case bytes, ok := <-progressChan:
				if !ok {
					// Final flush of any buffered live bytes.
					if bufferedBytes > 0 {
						_ = db.AddLiveBytes(p.db, ctx, mig.ID, bufferedBytes)
						bufferedBytes = 0
					}
					return
				}
				bufferedBytes += bytes
				atomic.StoreInt64(&lastByteNano, time.Now().UnixNano())
			case <-ticker.C:
				if bufferedBytes > 0 {
					_ = db.AddLiveBytes(p.db, ctx, mig.ID, bufferedBytes)
					bufferedBytes = 0
				}
			}
		}
	}()

	// Heartbeat goroutine: keeps the task's updated_at fresh so the orphan-recovery
	// watchdog does not reclaim an in-flight transfer. It runs for the *entire*
	// task lifetime — including the post-upload verification/hash-query phase,
	// during which no bytes flow on the progress channel but the task is still
	// legitimately working. A truly hung transfer (no bytes for longer than
	// taskHeartbeatByteStale once past the initial grace period) stops
	// heartbeating and is then reclaimed by the watchdog.
	heartbeatStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		consecutiveFailures := 0
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace &&
					time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if !stale {
					owned, err := db.HeartbeatTaskClaim(p.db, ctx, task.ID, task.ClaimEpoch)
					if err == nil && !owned {
						cancel() // recovery or a new claim fenced this worker off
						return
					}
					if err != nil {
						consecutiveFailures++
						log.Printf("[Worker %s] heartbeat error for task %s (failure %d/5): %v", p.workerID, task.ID, consecutiveFailures, err)
						if consecutiveFailures >= 5 {
							cancel()
							return
						}
					} else {
						consecutiveFailures = 0
					}
				}
			}
		}
	}()

	// Defer cleanup of progress channel.
	//
	// IMPORTANT: The progress channel is used ONLY for the live transfer-speed
	// display (the "5,5 MB/s" value). It does NOT book cumulative bytes into the
	// DB. Cumulative processed_bytes are incremented exactly once, when the file
	// is verified COMPLETED (see the IncrementMigrationProgress call below).
	//
	// Previously the channel added every streamed byte to processed_bytes here,
	// and a failed run rolled it back via a defer. That produced two bugs:
	//   1. On a retry (e.g. a post-upload hash mismatch that re-runs the whole
	//      upload) the same file's bytes were streamed and counted a second time,
	//      while total_bytes stayed at the single indexed value -> "transferred
	//      > total" (44,8 GB / 42,9 GB).
	//   2. Any non-zero rounding left processed_bytes permanently above total.
	// Booking once at verified completion keeps processed_bytes <= total_bytes
	// and in lockstep with processed_files.
	defer func() {
		close(progressChan)
		<-progressDone
		close(heartbeatStop)
	}()

	// Enforce the indexed size before hashing. A clean early EOF must fail instead
	// of becoming a valid hash for a truncated source stream.
	sizedReader := newExpectedSizeReader(throttledDownloadStream, task.FileSize)
	// io.TeeReader writes all data read from the download stream to the hasher in-memory
	hashingReader := io.TeeReader(sizedReader, activeWriter)

	// Perform Upload (Zero Data Retention - streamed through RAM buffer)
	// Use the same file-size-scaled deadline as the download phase so neither
	// times out before the other for a given file size.
	uploadCtx, uploadCancel := context.WithTimeout(ctx, transferDeadline)
	var transferMeta storage.FileMetadata
	if task.Metadata != nil {
		_ = json.Unmarshal(task.Metadata, &transferMeta)
	}
	if transferMeta.ModifiedTime.IsZero() {
		if srcInfo, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr != nil {
			log.Printf("Warning: could not fetch source mtime for %s: %v (timestamp may be inaccurate)", task.FilePath, inspectErr)
		} else {
			transferMeta.ModifiedTime = srcInfo.LastModified
		}
	}
	uploadCtx = storage.WithTransferMetadata(uploadCtx, transferMeta)
	verificationCtx := ctx
	var uploadReceipt *storage.UploadReceipt
	if mig.TargetProvider == "immich" {
		uploadReceipt = &storage.UploadReceipt{}
		uploadCtx = storage.WithUploadReceipt(uploadCtx, uploadReceipt)
	}
	if sourceHashStr != "" && sourceAlgo != "ETAG" {
		uploadCtx = context.WithValue(uploadCtx, "oc-checksum", fmt.Sprintf("%s:%s", sourceAlgo, sourceHashStr))
	}
	defer uploadCancel()

	// If size > chunkedUploadThreshold (50 MiB), do chunked upload
	if task.FileSize > chunkedUploadThreshold {
		// Wrap hashingReader with upload throttling
		throttledHashingReader := throttle.NewUploadThrottledReader(hashingReader, migrationThrottler, uploadCtx)
		err = targetClient.StreamUploadChunked(uploadCtx, task.ResourceType, uploadPath, throttledHashingReader, task.FileSize, progressChan)
	} else {
		// Simple upload
		// Wrap with a progress reporting reader
		progressReader := &ProgressReader{
			Reader:       hashingReader,
			ProgressChan: progressChan,
		}
		// Wrap progressReader with upload throttling
		throttledProgressReader := throttle.NewUploadThrottledReader(progressReader, migrationThrottler, uploadCtx)
		err = targetClient.StreamUpload(uploadCtx, task.ResourceType, uploadPath, throttledProgressReader, task.FileSize)
	}

	if err != nil {
		if errors.Is(err, storage.ErrNativeDuplicate) {
			return p.skipTask(ctx, task, "Asset already exists in Immich; duplicate handled natively (SKIP)")
		}
		if errors.Is(err, storage.ErrDuplicateUID) || isWebDAVSystemConflict(err) {
			return p.skipTask(ctx, task, fmt.Sprintf("Sabredav/WebDAV target entry skipped (system conflict/forbidden): %v", err))
		}
		return fmt.Errorf("upload to target failed: %w", err)
	}
	if err := sizedReader.VerifyComplete(); err != nil {
		return err
	}
	if uploadReceipt != nil {
		if uploadReceipt.TargetResourceID == "" {
			return fmt.Errorf("immich upload completed without target asset ID")
		}
		storage.SetImmichTargetAssetID(&transferMeta, uploadReceipt.TargetResourceID)
		metadata, err := json.Marshal(transferMeta)
		if err != nil {
			return fmt.Errorf("marshal immich target asset metadata: %w", err)
		}
		task.Metadata = metadata
		if err := db.UpdateClaimedTaskMetadata(p.db, ctx, task.ID, task.ClaimEpoch, task.Metadata); err != nil {
			return fmt.Errorf("persist immich target asset ID: %w", err)
		}
		verificationCtx = storage.WithTargetResourceID(ctx, uploadReceipt.TargetResourceID)
	}

	// OVERWRITE: now that the upload succeeded, promote the temp file without
	// deleting the original first. A failed promotion restores the original.
	//
	// Providers without atomic-rename support (e.g. S3, Immich) write the file
	// directly to its final name during upload. S3 rename is copy-and-delete, so
	// the rename step must be skipped entirely to avoid copy-and-delete loss.
	if useTempThenRename(targetClient, deleteAfterUpload) {
		backupPath := overwriteBackupPath(targetPath, task.ID)
		if err := promoteOverwrite(ctx, targetClient, task.ResourceType, targetPath, uploadPath, backupPath); err != nil {
			return err
		}
	}

	if applier, ok := targetClient.(storage.MetadataApplier); ok {
		if !transferMeta.ModifiedTime.IsZero() || transferMeta.Description != "" {
			if err := applier.ApplyMetadata(ctx, task.ResourceType, targetPath, transferMeta); err != nil && !errors.Is(err, storage.ErrUnsupportedOnPlatform) {
				log.Printf("Warning: failed to apply metadata for %s: %v", targetPath, err)
			}
		}
	}

	if task.ResourceType == "files" {
		exists, targetSize, err := verifyTargetSize(verificationCtx, targetClient, task.ResourceType, targetPath)
		if err != nil {
			return fmt.Errorf("failed to verify target size: %w", err)
		}
		if !exists || targetSize != task.FileSize {
			return fmt.Errorf("target size mismatch: got %d bytes, expected %d", targetSize, task.FileSize)
		}
	}

	// 5. Stream Hash Registration & Fast Task Completion
	// Network hash-verification queries against the target provider are omitted during
	// the transfer phase for maximum speed. Full checksum verification is performed
	// post-transfer by the Verifier daemon.
	if task.ResourceType == "files" {
		workerHashAlgo := sourceAlgo
		workerHashVal := formatWorkerHashValue(sourceAlgo, sourceHasher)
		// When source and target use different algorithms, retain the hash
		// calculated for the bytes written to the target. The verifier promotes
		// this value when it matches the target provider's native algorithm.
		if targetHasher != nil {
			workerHashAlgo = targetAlgo
			workerHashVal = formatWorkerHashValue(targetAlgo, targetHasher)
		}
		workerHash := fmt.Sprintf("%s:%s", workerHashAlgo, workerHashVal)
		task.WorkerHash = sql.NullString{String: workerHash, Valid: true}
		task.TargetHash = sql.NullString{String: workerHash, Valid: true}
	} else {
		task.WorkerHash = sql.NullString{String: "DYNAMIC", Valid: true}
		task.TargetHash = sql.NullString{String: "DYNAMIC", Valid: true}
	}

	// Update task to COMPLETED
	task.Status = "COMPLETED"
	task.ErrorMessage = sql.NullString{}
	if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 0, 0, 0); err != nil {
		return err
	}

	// A successful transfer breaks the "consecutive connection loss" streak (P1-4).
	p.clearConnLoss(mig.ID)
	p.clearConnLossTask(task.ID)

	// Increment processed files AND bytes count exactly once, at verified
	// completion. Bytes are booked here (not via the progress channel) so that a
	// retried upload (hash mismatch etc.) cannot double-count the same file and
	// push processed_bytes above total_bytes.
	// Re-sync the live counter to the now-authoritative processed_bytes so the
	// speed/ETA display cannot stay above total_bytes after a retried upload.
	_ = db.ResetLiveBytes(p.db, ctx, mig.ID)

	return nil
}

func (p *Processor) handleTaskFailure(ctx context.Context, payload *queue.Payload, procErr error) {
	// 1. Fetch Task
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		log.Printf("Error fetching task on failure handler: %v\n", err)
		return
	}
	task.ClaimEpoch = payload.ClaimEpoch

	// Check if migration was manually cancelled
	mig, migErr := db.GetMigration(p.db, payload.MigrationID)
	if migErr == nil && mig.Status == "CANCELLED" {
		log.Printf("[Worker %s] Task %s aborted (Migration cancelled).\n", p.workerID, payload.TaskID)
		task.Status = "CANCELLED"
		_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
		return
	}

	// Check if context is cancelled (graceful shutdown)
	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		log.Printf("[Worker %s] Shutdown detected. Requeueing task %s...\n", p.workerID, payload.TaskID)

		task.Status = "PENDING"
		_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(procErr.Error()), Valid: true}

	// Check if this error is a network connection loss
	isConnLoss := isNetworkError(procErr)

	if isConnLoss {
		log.Printf("[Worker %s] Connection loss detected: %v\n", p.workerID, procErr)
		// Prefer per-task backoff: retry just this task instead of pausing the
		// whole migration. Only escalate to PAUSED_CONNECTION_LOSS after several
		// consecutive connection losses for the migration, so a single flaky task
		// (e.g. one bad endpoint) does not stall every other task in flight (P1-4).
		lossCount := p.recordConnLoss(payload.MigrationID)
		// Per-task cap: a single task must never retry on connection loss forever.
		// We count only this task's connection-loss attempts (not total attempts,
		// which also include unrelated failures) so a task that previously failed
		// for non-network reasons is not wrongly escalated to a full migration
		// pause on its first connection loss. If the migration-level streak is
		// still below the escalation threshold but THIS task has exhausted its own
		// connection-loss retries, escalate to a pause so the connection-recovery
		// scheduler can retry the whole migration — otherwise a poisoned endpoint
		// would loop indefinitely even though other tasks keep resetting the
		// migration-wide streak (P1-4).
		taskConnLoss := p.recordConnLossTask(task.ID)
		if lossCount < connLossEscalationThreshold && taskConnLoss < maxConnLossTaskAttempts {
			backoff := retryBackoff(taskConnLoss)
			nextRetry := time.Now().Add(backoff)
			task.Status = "FAILED"
			task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
			_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
			log.Printf("[Worker %s] Connection loss on task %s (migration %s): retrying in %ds (consecutive losses %d/%d, task conn-loss attempts %d)\n",
				p.workerID, payload.TaskID, payload.MigrationID, int(backoff.Seconds()),
				lossCount, connLossEscalationThreshold, taskConnLoss)
			return
		}
		// Escalation: too many consecutive connection losses for the migration,
		// or this single task exhausted its connection-loss retries — pause the
		// migration so the connection-recovery scheduler can retry it.
		_ = db.UpdateMigrationStatus(p.db, payload.MigrationID, "PAUSED_CONNECTION_LOSS", nil)
		p.clearConnLoss(payload.MigrationID)
		p.clearConnLossTask(task.ID)
		p.recoveryAttempts.Delete(payload.MigrationID)
		// Task is set back to PENDING so it can be retried immediately upon resume
		task.Status = "PENDING"
		_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
		return
	}

	// Check if error is permanent / non-retryable
	isPermanent := errors.Is(procErr, storage.ErrUnsupportedResourceType) ||
		errors.Is(procErr, storage.ErrPathEscapesRoot)
	errStr := procErr.Error()
	if !isPermanent && (strings.Contains(errStr, "exportSizeLimitExceeded") ||
		strings.Contains(errStr, "badRequest") ||
		strings.Contains(errStr, "conversion is not supported") ||
		strings.Contains(errStr, "fileNotDownloadable") ||
		strings.Contains(errStr, "Only files with binary content can be downloaded") ||
		strings.Contains(errStr, "too large to be exported") ||
		strings.Contains(errStr, "notFound") ||
		strings.Contains(errStr, "fileNotFound") ||
		strings.Contains(errStr, "not supported by") ||
		strings.Contains(errStr, "path escapes storage root")) {
		isPermanent = true
	}

	// Detect authentication errors that mean the stored credentials are invalid.
	// All provider methods (Connect and every transfer method) wrap HTTP 401
	// responses with storage.ErrAuth so errors.Is detects them via the error chain.
	// For Google (OAuth), the Google API client returns *googleapi.Error whose
	// message contains these distinctive strings — they do not appear in other paths.
	isAuthError := errors.Is(procErr, storage.ErrAuth) ||
		strings.Contains(errStr, "authError") ||
		strings.Contains(errStr, "Invalid Credentials") ||
		strings.Contains(errStr, "invalid authentication credentials")

	if isAuthError {
		// Providers may revoke an access token before its persisted expiry. Refresh
		// the affected OAuth side once and let the normal retry scheduler recreate
		// a provider with the new token instead of failing the whole migration.
		if role := oauthAuthFailureRole(mig, errStr); role != "" && task.Attempts <= 3 {
			if _, refreshErr := p.refreshOAuthToken(ctx, mig, role); refreshErr == nil {
				backoff := retryBackoff(task.Attempts)
				task.Status = "FAILED"
				task.ErrorMessage = sql.NullString{String: "OAuth access token rejected; refreshed token scheduled for retry", Valid: true}
				task.NextRetryAt = sql.NullTime{Time: time.Now().Add(backoff), Valid: true}
				_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
				log.Printf("[Worker %s] OAuth 401 for task %s (migration %s, %s) — refreshed token and retrying in %ds\n",
					p.workerID, payload.TaskID, payload.MigrationID, role, int(backoff.Seconds()))
				return
			} else {
				log.Printf("[Worker %s] OAuth 401 recovery refresh failed for task %s (migration %s, %s): %v\n",
					p.workerID, payload.TaskID, payload.MigrationID, role, refreshErr)
			}
		}
		log.Printf("[Worker %s] Auth error detected for task %s (migration %s) — stopping migration immediately\n",
			p.workerID, payload.TaskID, payload.MigrationID)
		authErrMsg := "Authentication failed — please check your credentials and start a new migration"
		_ = db.UpdateMigrationStatus(p.db, payload.MigrationID, "FAILED", &authErrMsg)
		// Drop any connection-loss / recovery tracking for this migration now that
		// it is terminal, so the in-memory maps do not leak across migrations.
		p.clearConnLoss(payload.MigrationID)
		p.clearConnLossTask(payload.TaskID)
		p.recoveryAttempts.Delete(payload.MigrationID)
		// Mark this individual task failed too so progress counters stay accurate
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 0, 1, 0); err != nil {
			return
		}
		// Cancel any remaining PENDING tasks so they are not orphaned: the dequeue
		// query only selects PENDING while RUNNING/INDEXING, so they would otherwise
		// stay stuck forever (processed_files never reaches total_files, live stream
		// never closes, CSV report incomplete). Count them as FAILED (not processed)
		// so the report does not understate how many files were not migrated.
		cancelled, cerr := db.CancelRemainingPendingTasks(p.db, task.MigrationID)
		if cerr != nil {
			log.Printf("[Worker %s] Error cancelling remaining pending tasks for migration %s: %v\n", p.workerID, task.MigrationID, cerr)
		} else if cancelled > 0 {
			_ = db.IncrementMigrationProgress(p.db, ctx, task.MigrationID, 0, 0, 0, cancelled)
		}
		if owner, oerr := db.GetMigrationOwnerID(p.db, payload.MigrationID); oerr == nil {
			db.WriteAuditLog(p.db, db.AuditEntry{
				UserID:  sql.NullString{String: owner, Valid: true},
				Action:  db.AuditMigrationFailed,
				Target:  payload.MigrationID,
				Details: json.RawMessage(`{"phase":"transfer","reason":"auth_error"}`),
			})
		}
		return
	}

	// If it is a normal file transfer failure
	if task.Attempts < 3 && !isPermanent {
		backoff := retryDelay(procErr, task.Attempts)
		nextRetry := time.Now().Add(backoff)
		task.Status = "FAILED" // Kept as failed until cron schedules retry
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		if err := db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 0, 1, 0); err != nil {
			return
		}

		log.Printf("[Worker %s] Task %s scheduled for retry in %ds (Attempt %d/3)\n", p.workerID, task.ID, int(backoff.Seconds()), task.Attempts)
	} else {
		// Max retries reached, fail permanently
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateClaimedTaskStatus(p.db, ctx, task)
		// Task is now terminal: drop its per-task connection-loss counter so the
		// in-memory map does not grow unbounded across a long-running worker.
		p.clearConnLossTask(task.ID)

		log.Printf("[Worker %s] Task %s failed permanently after %d attempts\n", p.workerID, task.ID, task.Attempts)
	}
}

// oauthAuthFailureRole identifies which OAuth side rejected the transfer. The
// wrapped transfer errors retain source/target context, so a valid credential
// on the other side is never rotated unnecessarily.
func oauthAuthFailureRole(mig *db.Migration, errText string) string {
	errText = strings.ToLower(errText)
	sourceOAuth := oauth.IsProvider(mig.SourceProvider)
	targetOAuth := oauth.IsProvider(mig.TargetProvider)
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

func oauthSyncAuthFailureRole(job *db.SyncJob, errText string) string {
	errText = strings.ToLower(errText)
	sourceOAuth := oauth.IsProvider(job.SourceProvider)
	targetOAuth := oauth.IsProvider(job.TargetProvider)
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
		pr.ProgressChan <- int64(n)
	}
	return n, err
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
					if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
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
	if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
		accessToken = latestAccess
	}
	refreshTokenEnc, expiresAt, provider = latest.refreshEnc, latest.expiresAt, latest.provider

	// Another task or the rotation daemon may already have recovered this
	// credential while this task waited for the distributed refresh lock. Reuse
	// the newer access token instead of rotating a freshly issued refresh token
	// again (Microsoft can invalidate older refresh tokens during rotation).
	if force && latestMig.UpdatedAt.After(mig.UpdatedAt) {
		return accessToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	log.Printf("[Worker %s] %s OAuth token expired or near expiry for migration %s — refreshing inline\n",
		p.workerID, role, mig.ID)

	refreshToken, err := crypto.Decrypt(refreshTokenEnc.String, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token: %w", role, err)
	}
	defer crypto.ZeroString(&refreshToken)

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for %s (%s): %w", role, provider, err)
	}
	defer crypto.ZeroString(&tokenResp.RefreshToken)

	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s access token after refresh: %w", role, err)
	}
	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s refresh token after refresh: %w", role, err)
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	expectedRefreshEnc := refreshTokenEnc.String
	err = db.UpdateMigrationOAuthTokens(p.db, db.OAuthTokenUpdate{
		MigrationID:           mig.ID,
		Role:                  role,
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, expectedRefreshEnc)

	if errors.Is(err, db.ErrOAuthTokenConflict) {
		log.Printf("[Worker %s] Token update conflict for migration %s (%s) — adopting winner token from DB\n", p.workerID, mig.ID, role)
		if latestMig, lerr := db.GetMigration(p.db, mig.ID); lerr == nil {
			latest := tokenSet(latestMig)
			if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for migration %s (%s): %w", mig.ID, role, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

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
					if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
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
	if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
		currentToken = latestAccess
	}
	refreshTokenEnc, expiresAt, provider = latest.refreshEnc, latest.expiresAt, latest.provider

	if force && latestJob.UpdatedAt.After(job.UpdatedAt) {
		return currentToken, nil
	}

	if !force && expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return currentToken, nil
	}

	log.Printf("[Worker %s] %s OAuth token expired or near expiry for sync job %s — refreshing inline\n",
		p.workerID, role, job.ID)

	refreshToken, err := crypto.Decrypt(refreshTokenEnc.String, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token for sync job: %w", role, err)
	}
	defer crypto.ZeroString(&refreshToken)

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for sync job %s (%s): %w", role, provider, err)
	}
	defer crypto.ZeroString(&tokenResp.RefreshToken)

	newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt new %s access token after refresh: %w", role, err)
	}
	newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, p.secretKey)
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
		log.Printf("[Worker %s] Token update conflict for sync job %s (%s) — adopting winner token from DB\n", p.workerID, job.ID, role)
		if latestJob, lerr := db.GetSyncJob(p.db, job.ID); lerr == nil {
			latest := tokenSet(latestJob)
			if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
				return latestAccess, nil
			}
		}
		return "", fmt.Errorf("token update conflict for sync job %s (%s): %w", job.ID, role, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

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

func (p *Processor) skipTask(ctx context.Context, task *db.Task, reason string) error {
	task.Status = "SKIPPED"
	task.ErrorMessage = sql.NullString{String: sanitize.SanitizeError(reason), Valid: true}
	return db.UpdateMigrationTaskAndProgress(p.db, ctx, task, 1, task.FileSize, 1, 0, task.FileSize)
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
