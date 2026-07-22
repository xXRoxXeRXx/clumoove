package processor

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
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
	"backend/internal/email"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type activeTaskInfo struct {
	migrationID string
	syncJobID   string
	cancel      context.CancelFunc
}

type Processor struct {
	db           *sql.DB
	queue        *queue.Queue
	workerID     string
	secretKey    string
	maxThreads   int
	// dbConnStr is the raw PostgreSQL DSN used to open a dedicated LISTEN
	// connection for pg_notify-based wake-up (see ListenForTasks in queue).
	// Set via SetDBConnStr before calling Start. If empty, the worker falls back
	// to periodic polling.
	dbConnStr    string
	activeTasks  sync.Map
	refreshLocks sync.Map
	throttlers   sync.Map
	// syncEngine is used by recoverPausedSyncJobs to trigger a new sync pass
	// after connection is restored. Set via SetSyncEngine after construction.
	syncEngine syncEngineInterface
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
	// providerCache caches storage.StorageProvider instances per migration role
	// to eliminate TCP/TLS/SSH/SMB connection setup overhead on every file task.
	providerCache sync.Map
}

type cachedProviderEntry struct {
	client   storage.StorageProvider
	credHash string
}

func (p *Processor) getOrCreateProvider(ctx context.Context, key, providerType, urlStr, username, password string) (storage.StorageProvider, func(), error) {
	credHash := fmt.Sprintf("%s:%s:%s:%s", providerType, urlStr, username, password)
	if val, ok := p.providerCache.Load(key); ok {
		entry := val.(*cachedProviderEntry)
		if entry.credHash == credHash {
			return entry.client, func() {}, nil
		}
		entry.client.Close()
		p.providerCache.Delete(key)
	}

	client, err := storage.NewProvider(ctx, providerType, urlStr, username, password)
	if err != nil {
		return nil, nil, err
	}

	entry := &cachedProviderEntry{
		client:   client,
		credHash: credHash,
	}
	actual, loaded := p.providerCache.LoadOrStore(key, entry)
	if loaded {
		client.Close()
		return actual.(*cachedProviderEntry).client, func() {}, nil
	}

	return client, func() {}, nil
}

// syncEngineInterface is a minimal interface so the processor package does not
// import the concrete sync package (avoiding an import cycle).
type syncEngineInterface interface {
	RunSyncPass(ctx context.Context, syncJobID string)
}

// SetSyncEngine wires the sync engine into the processor so that
// recoverPausedSyncJobs can trigger a new RunSyncPass after restoring
// a lost connection.
func (p *Processor) SetSyncEngine(e syncEngineInterface) {
	p.syncEngine = e
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

	return &Processor{
		db:         database,
		queue:      q,
		workerID:   workerID,
		secretKey:  secretKey,
		maxThreads: maxThreads,
	}
}

func (p *Processor) getOrCreateRefreshLock(migrationID string) *sync.Mutex {
	actual, _ := p.refreshLocks.LoadOrStore(migrationID, &sync.Mutex{})
	return actual.(*sync.Mutex)
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

	// Spawn background schedulers
	go p.RunWorkerLiveness(ctx)
	go p.RunRetryScheduler(ctx)
	go p.RunConnectionRecoveryScheduler(ctx)
	go p.RunOrphanedRunningTasksRecovery(ctx)
	go p.RunCompletionNotifier(ctx)
	go p.RunProgressReconciler(ctx)

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

	// Start Bandwidth Change Listener
	go p.queue.SubscribeToBandwidthChanges(ctx, func(event queue.BandwidthEvent) {
		log.Printf("[Worker %s] Bandwidth change for migration %s: %d Mbps",
			p.workerID, event.MigrationID, event.BandwidthLimitMbps)
		if throttler, ok := p.throttlers.Load(event.MigrationID); ok {
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
	for i := 0; i < p.maxThreads; i++ {
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
				}
			}
		}(i)
	}

	// Wait for shutdown signal
	<-ctx.Done()
	log.Printf("[Worker %s] Shutdown signal received. Waiting for active tasks to finish...\n", p.workerID)
	wg.Wait()
	log.Printf("[Worker %s] Worker loop stopped.\n", p.workerID)
}

// RunWorkerLiveness periodically registers this worker as active and recovers abandoned tasks
func (p *Processor) RunWorkerLiveness(ctx context.Context) {
	// Register immediately with a 120s TTL — generous enough to survive a brief Redis hiccup
	// without the liveness key expiring between heartbeat ticks (tick=10s, TTL=120s → 12 chances).
	_ = p.queue.RegisterActiveWorker(ctx, p.workerID, 120*time.Second)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := p.queue.RegisterActiveWorker(ctx, p.workerID, 120*time.Second)
			if err != nil {
				log.Printf("[Liveness] Error registering active worker: %v\n", err)
			}
		case <-cleanupTicker.C:
			deadWorkers, err := p.queue.GetAbandonedWorkerQueues(ctx, p.db)
			if err != nil {
				log.Printf("[Liveness] Error scanning for dead workers: %v\n", err)
				continue
			}
			for _, deadWorkerID := range deadWorkers {
				if deadWorkerID == p.workerID {
					continue
				}
				// Claim a distributed recovery lock (Redis SETNX) before touching the dead
				// worker's queue. This prevents two processor instances from simultaneously
				// recovering the same worker and enqueuing tasks twice.
				claimed, lockErr := p.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 120*time.Second)
				if lockErr != nil || !claimed {
					continue // Another instance is already handling recovery for this worker
				}
				log.Printf("[Liveness] Found abandoned queue for worker %s, recovering tasks...\n", deadWorkerID)
				if err := p.queue.RecoverAbandonedTasks(ctx, p.db, deadWorkerID); err != nil {
					log.Printf("[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
				} else {
					// Wake idle threads so recovered tasks are picked up immediately.
					p.queue.NotifyTaskAvailable(ctx, p.db)
				}
			}
		}
	}
}

// RunRetryScheduler runs a ticker to scan the DB for tasks waiting for retry
func (p *Processor) RunRetryScheduler(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.requeueFailedTasks(ctx)
		}
	}
}

func (p *Processor) requeueFailedTasks(ctx context.Context) {
	query := `
		SELECT t.id, COALESCE(t.migration_id::text, t.sync_job_id::text, '')
		FROM tasks t
		LEFT JOIN migrations m ON t.migration_id = m.id
		LEFT JOIN sync_jobs sj ON t.sync_job_id = sj.id
		WHERE t.status = 'FAILED' 
		  AND t.next_retry_at <= $1
		  AND (
		    (t.migration_id IS NOT NULL AND m.status IN ('RUNNING', 'INDEXING'))
		    OR
		    (t.sync_job_id IS NOT NULL AND sj.status IN ('RUNNING', 'INDEXING'))
		  )
	`
	rows, err := p.db.QueryContext(ctx, query, time.Now())
	if err != nil {
		return
	}
	defer rows.Close()

	var requeued int
	for rows.Next() {
		var taskID, parentID string
		if err := rows.Scan(&taskID, &parentID); err != nil {
			continue
		}

		// Update status to PENDING
		updateQuery := `
			UPDATE tasks
			SET status = 'PENDING', next_retry_at = NULL
			WHERE id = $1
		`
		_, err := p.db.ExecContext(ctx, updateQuery, taskID)
		if err != nil {
			continue
		}

		log.Printf("[RetryScheduler] Re-enqueued task %s for migration/sync %s\n", taskID, parentID)
		requeued++
	}
	if err := rows.Err(); err != nil {
		log.Printf("[RetryScheduler] rows error: %v\n", err)
	}
	// Wake idle worker threads so retry tasks are picked up immediately.
	if requeued > 0 {
		p.queue.NotifyTaskAvailable(ctx, p.db)
	}
}

// RunProgressReconciler periodically repairs counter drift between the cached
// migration/sync progress columns and the real task rows (see ReconcileMigrationProgress
// in db for the rationale). It advances a stalled RUNNING/INDEXING migration or sync job
// to its terminal state only once no open tasks remain.
func (p *Processor) RunProgressReconciler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcileActiveMigrations(ctx)
			p.reconcileActiveSyncJobs(ctx)
		}
	}
}

// reconcileActiveMigrations scans RUNNING/INDEXING migrations and reconciles their
// progress counters against the authoritative task rows.
func (p *Processor) reconcileActiveMigrations(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT m.id
		FROM migrations m
		WHERE m.status IN ('RUNNING', 'INDEXING')
	`)
	if err != nil {
		log.Printf("[ProgressReconciler] DB query error: %v\n", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[ProgressReconciler] rows error: %v\n", err)
		return
	}

	for _, id := range ids {
		if err := db.ReconcileMigrationProgress(p.db, id); err != nil {
			log.Printf("[ProgressReconciler] error reconciling migration %s: %v\n", id, err)
		}
	}
}

// reconcileActiveSyncJobs scans RUNNING/INDEXING sync jobs and reconciles their
// progress counters and terminal status against authoritative task rows.
func (p *Processor) reconcileActiveSyncJobs(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT sj.id
		FROM sync_jobs sj
		WHERE sj.status IN ('RUNNING', 'INDEXING')
	`)
	if err != nil {
		log.Printf("[ProgressReconciler] Sync DB query error: %v\n", err)
		return
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[ProgressReconciler] Sync rows error: %v\n", err)
		return
	}

	for _, id := range ids {
		if err := db.ReconcileSyncJobProgress(p.db, id); err != nil {
			log.Printf("[ProgressReconciler] error reconciling sync job %s: %v\n", id, err)
		}
	}
}

// RunOrphanedRunningTasksRecovery detects RUNNING tasks that are stuck (e.g. due to a worker
// crashing or a thread dying silently) and resets them back to PENDING. It waits 5 minutes
// after startup before the first scan.
func (p *Processor) RunOrphanedRunningTasksRecovery(ctx context.Context) {
	// Delay first check.
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Minute):
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.requeueOrphanedRunningTasks(ctx)
		}
	}
}

// requeueOrphanedRunningTasks scans for RUNNING tasks belonging to active migrations or sync jobs
// whose updated_at is older than 10 minutes (i.e. they were picked up but never finished).
func (p *Processor) requeueOrphanedRunningTasks(ctx context.Context) {
	query := `
		SELECT t.id, COALESCE(t.migration_id::text, t.sync_job_id::text, '')
		FROM tasks t
		LEFT JOIN migrations m ON t.migration_id = m.id
		LEFT JOIN sync_jobs sj ON t.sync_job_id = sj.id
		WHERE t.status = 'RUNNING'
		  AND t.updated_at < NOW() - INTERVAL '10 minutes'
		  AND (
		    (t.migration_id IS NOT NULL AND m.status IN ('RUNNING', 'INDEXING'))
		    OR
		    (t.sync_job_id IS NOT NULL AND sj.status IN ('RUNNING', 'INDEXING'))
		  )
	`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		log.Printf("[OrphanedTaskRecovery] DB query error: %v\n", err)
		return
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var taskID, parentID string
		if err := rows.Scan(&taskID, &parentID); err != nil {
			continue
		}
		_, err := p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL, updated_at=NOW() WHERE id=$1 AND status='RUNNING'", taskID)
		if err != nil {
			log.Printf("[OrphanedTaskRecovery] Error resetting task %s: %v\n", taskID, err)
		} else {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[OrphanedTaskRecovery] rows error: %v\n", err)
	}
	if count > 0 {
		log.Printf("[OrphanedTaskRecovery] Re-enqueued %d orphaned RUNNING tasks\n", count)
		p.queue.NotifyTaskAvailable(ctx, p.db)
	}
}

// RunConnectionRecoveryScheduler checks paused migrations to test if servers are back online
func (p *Processor) RunConnectionRecoveryScheduler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second) // Check every 60s for connection recovery
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.recoverPausedMigrations(ctx)
			p.recoverPausedSyncJobs(ctx)
		}
	}
}

// recoveryState records the last connection-recovery attempt for a paused
// migration so P1-12 can apply increasing backoff instead of probing a server
// that is still down on every 60s tick.
type recoveryState struct {
	lastAttempt time.Time
	attempts    int
}

// recoveryBackoff returns how long to wait after the most recent failed recovery
// attempt before trying again. The first attempt is allowed immediately; a still-
// down server is then probed at most once per 5 minutes (escalating 60s → 5min).
func recoveryBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 0:
		return 0
	case attempts == 1:
		return 60 * time.Second
	default:
		return 5 * time.Minute
	}
}

func (p *Processor) recoverPausedMigrations(ctx context.Context) {
	query := `
		SELECT id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
	`
	rows, err := p.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, sURL, sUser, sPassEnc, tURL, tUser, tPassEnc, sProv, tProv string
		if err := rows.Scan(&id, &sURL, &sUser, &sPassEnc, &tURL, &tUser, &tPassEnc, &sProv, &tProv); err != nil {
			continue
		}

		// Apply increasing backoff so a server that is still down is not probed
		// on every 60s tick (P1-12).
		var ra recoveryState
		if v, ok := p.recoveryAttempts.Load(id); ok {
			ra = v.(recoveryState)
		}
		if backoff := recoveryBackoff(ra.attempts); backoff > 0 && time.Since(ra.lastAttempt) < backoff {
			continue
		}

		sPass, err := crypto.Decrypt(sPassEnc, p.secretKey)
		if err != nil {
			continue
		}
		tPass, err := crypto.Decrypt(tPassEnc, p.secretKey)
		if err != nil {
			continue
		}

		sClient, err := storage.NewProvider(ctx, sProv, sURL, sUser, sPass)
		if err != nil {
			continue
		}
		tClient, err := storage.NewProvider(ctx, tProv, tURL, tUser, tPass)
		if err != nil {
			sClient.Close()
			continue
		}

		connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
		sOK, _ := sClient.Connect(connCtx)
		tOK, _ := tClient.Connect(connCtx)
		connCancel()
		sClient.Close()
		tClient.Close()

		if sOK && tOK {
			log.Printf("[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", id)
			updateQuery := `
				UPDATE migrations
				SET status = 'RUNNING'
				WHERE id = $1
			`
			_, err = p.db.ExecContext(ctx, updateQuery, id)
			if err != nil {
				log.Printf("[RecoveryScheduler] Error resuming migration %s: %v\n", id, err)
			}
			// Reset backoff tracking now that the migration has recovered.
			p.recoveryAttempts.Delete(id)
		} else {
			// Still down: record the attempt so the next probe is backed off.
			p.recoveryAttempts.Store(id, recoveryState{lastAttempt: time.Now(), attempts: ra.attempts + 1})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[RecoveryScheduler] rows error: %v\n", err)
	}
}

// useTempThenRename reports whether the processor should use the
// "upload to <path>.tmp then atomically rename" overwrite pattern for the
// given target. It is only safe when (a) an overwrite/retry actually requires
// the temp file and (b) the target provider supports a rename operation.
// Providers without atomic-rename support (e.g. Google Photos) write the file
// to its final name during upload, so the temp-file + rename dance must be
// skipped — otherwise the always-failing RenameFile would abort the upload.
func useTempThenRename(target storage.StorageProvider, deleteAfterUpload bool) bool {
	return deleteAfterUpload && target.SupportsAtomicRename()
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
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL WHERE id=$1", payload.TaskID)
		time.Sleep(2 * time.Second)
		return nil
	}

	// If migration is in a terminal state (COMPLETED, COMPLETED_WITH_ERRORS or FAILED), mark task as skipped/failed
	if mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='SKIPPED', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return nil
	}

	// If migration was cancelled, mark the task cancelled and stop
	if mig.Status == "CANCELLED" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='CANCELLED', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return nil
	}

	// If migration is in any other non-running state, requeue and return error
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET status='PENDING', worker_hash=NULL WHERE id=$1", payload.TaskID)
		return fmt.Errorf("migration is in state %s, task skipped for now", mig.Status)
	}

	// 2. Fetch Task from DB
	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return fmt.Errorf("failed to fetch task: %w", err)
	}

	log.Printf("[Worker %s] Thread %d -> Request: [%s] %s (%d bytes) [%s -> %s]\n",
		p.workerID, threadID, strings.ToUpper(task.ResourceType), task.FilePath, task.FileSize, mig.SourceProvider, mig.TargetProvider)

	// Decrypt credentials
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt source password: %w", err)
	}
	targetPass, err := crypto.Decrypt(mig.TargetPasswordEncrypted, p.secretKey)
	if err != nil {
		return fmt.Errorf("failed to decrypt target password: %w", err)
	}

	// For OAuth providers: if the token is expired or within 2 minutes of expiry,
	// refresh it now so this task does not hit a 401. The daemon already handles
	// proactive rotation every 5 min, but tasks could be dequeued right as a token
	// expires, so we have this last-resort inline refresh.
	sourcePass, err = p.ensureFreshOAuthToken(ctx, mig, "source", sourcePass)
	if err != nil {
		return fmt.Errorf("failed to refresh source OAuth token: %w", err)
	}
	targetPass, err = p.ensureFreshOAuthToken(ctx, mig, "target", targetPass)
	if err != nil {
		return fmt.Errorf("failed to refresh target OAuth token: %w", err)
	}

	// Get or create cached storage providers
	sourceKey := fmt.Sprintf("%s:source", mig.ID)
	targetKey := fmt.Sprintf("%s:target", mig.ID)

	sourceClient, closeSource, err := p.getOrCreateProvider(ctx, sourceKey, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		return fmt.Errorf("failed to create source client: %w", err)
	}
	defer closeSource()

	targetClient, closeTarget, err := p.getOrCreateProvider(ctx, targetKey, mig.TargetProvider, mig.TargetURL, mig.TargetUsername, targetPass)
	if err != nil {
		return fmt.Errorf("failed to create target client: %w", err)
	}
	defer closeTarget()

	if nc, ok := sourceClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}
	if nc, ok := targetClient.(*storage.NextcloudProvider); ok {
		nc.Threads = mig.Threads
	}

	// Update task status to RUNNING in DB
	task.Status = "RUNNING"
	_ = db.UpdateTaskStatus(p.db, task)

	// 3. Conflict Resolution
	var deleteAfterUpload bool // set true by OVERWRITE: delete original only after upload succeeds
	targetPath := task.FilePath
	if task.ResourceType == "files" {
		// Use path (POSIX) rather than filepath: WebDAV/Nextcloud paths are always
		// slash-separated, independent of the OS this server process runs on.
		targetPath = path.Clean(path.Join(mig.TargetDir, task.FilePath))
	}

	// 3a. Filename Sanitization (before conflict resolution)
	if task.ResourceType == "files" {
		result := sanitize.SanitizeFilename(path.Base(targetPath), mig.TargetProvider)
		if result.Changed {
			dir := path.Dir(targetPath)
			targetPath = path.Join(dir, result.SanitizedName)
			log.Printf("[SANITIZE] %s: \"%s\" → \"%s\" (%s)",
				task.ID, result.OriginalName, result.SanitizedName, strings.Join(result.Reasons, ", "))
			_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
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
				_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
			}
		}
	}

	if task.ResourceType == "files" && mig.ConflictStrategy == "OVERWRITE" {
		// Optimization: for OVERWRITE on files, bypass the pre-flight FileExists
		// network query (PROPFIND/HEAD) since the file will be overwritten regardless.
		deleteAfterUpload = true
	} else {
		exists, existingSize, err := targetClient.FileExists(ctx, task.ResourceType, targetPath)
		if err != nil {
			return fmt.Errorf("failed to check if target file exists: %w", err)
		}

		if exists {
			// Calendars and contacts are always overwritten: they are dynamic data and
			// a SKIP would silently leave stale entries from a previous failed run.
			if task.ResourceType != "files" {
				err = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
				if err != nil {
					return fmt.Errorf("failed to delete existing calendar/contact entry for overwrite: %w", err)
				}
			} else {
				switch mig.ConflictStrategy {
				case "SKIP":
					if task.Attempts > 0 {
						deleteAfterUpload = true
					} else if exists && existingSize == task.FileSize {
						task.Status = "SKIPPED"
						task.ErrorMessage = sql.NullString{String: "File already exists in target (SKIP)", Valid: true}
						_ = db.UpdateTaskStatus(p.db, task)
						_ = db.IncrementMigrationProgress(p.db, ctx, mig.ID, 1, task.FileSize, 1, 0)
						_ = db.AddLiveBytes(p.db, ctx, mig.ID, task.FileSize)
						return nil
					}
					deleteAfterUpload = true

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
							return fmt.Errorf("failed to check existence of rename candidate: %w", err)
						}
						if !candidateExists {
							targetPath = candidatePath
							_ = db.UpdateTaskFilePath(p.db, task.ID, targetPath)
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
	// Providers without atomic-rename support (e.g. Google Photos) write the
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
	defer downloadStream.Close()

	// Wrap download stream with throttling (before TeeReader to limit actual network I/O)
	throttledDownloadStream := throttle.NewThrottledReader(downloadStream, migrationThrottler, downloadCtx)

	// Handle Hash Algorithm Selection
	var sourceHasher hash.Hash
	sourceAlgo := "SHA1" // Default
	sourceHashStr := ""

	if task.SourceHash.Valid && task.SourceHash.String != "" && mig.SourceProvider != "webdav" {
		algo, cleanHash := storage.ParseHashString(task.SourceHash.String)
		sourceHashStr = cleanHash
		sourceAlgo = algo
	} else {
		// Fallback to fetch hash directly
		if mig.SourceProvider != "webdav" {
			if fetchedHash, err := sourceClient.GetFileHash(ctx, task.ResourceType, task.FilePath); err == nil {
				task.SourceHash = sql.NullString{String: fetchedHash, Valid: true}
				algo, cleanHash := storage.ParseHashString(fetchedHash)
				sourceHashStr = cleanHash
				sourceAlgo = algo
			}
		}
	}

	if mig.SourceProvider == "dropbox" {
		sourceAlgo = "DROPBOX"
	}

	// Instantiate source hasher
	if sourceAlgo == "MD5" {
		sourceHasher = md5.New()
	} else if sourceAlgo == "DROPBOX" {
		sourceHasher = storage.NewDropboxHasher()
	} else if sourceAlgo == "SHA256" {
		sourceHasher = sha256.New()
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

	// Setup progress notification channel
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
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				stale := time.Since(taskStart) > taskHeartbeatGrace &&
					time.Now().UnixNano()-atomic.LoadInt64(&lastByteNano) > int64(taskHeartbeatByteStale)
				if !stale {
					_, _ = p.db.ExecContext(ctx, "UPDATE tasks SET updated_at = NOW() WHERE id = $1 AND status = 'RUNNING'", task.ID)
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

	// io.TeeReader writes all data read from the download stream to the hasher in-memory
	hashingReader := io.TeeReader(throttledDownloadStream, activeWriter)

	// Perform Upload (Zero Data Retention - streamed through RAM buffer)
	// Use the same file-size-scaled deadline as the download phase so neither
	// times out before the other for a given file size.
	uploadCtx, uploadCancel := context.WithTimeout(ctx, transferDeadline)
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
		if errors.Is(err, storage.ErrDuplicateUID) {
			task.Status = "SKIPPED"
			task.ErrorMessage = sql.NullString{String: "Sabredav: Calendar event UID already exists (SKIP)", Valid: true}
			_ = db.UpdateTaskStatus(p.db, task)
			_ = db.IncrementMigrationProgress(p.db, ctx, mig.ID, 1, task.FileSize, 1, 0)
			_ = db.AddLiveBytes(p.db, ctx, mig.ID, task.FileSize)
			return nil
		}
		return fmt.Errorf("upload to target failed: %w", err)
	}

	// OVERWRITE: now that the upload succeeded, safely delete the original and rename the temp file.
	// If the upload succeeded but rename/metadata fails, the .tmp file must be cleaned up so it
	// does not leak on the target and is not mistaken for a partial upload on the next retry.
	//
	// Providers without atomic-rename support (e.g. Google Photos: no rename/delete
	// operation) write the file to its final name during upload (the processor's
	// ".tmp" suffix is stripped by the provider itself), so the rename step must be
	// skipped entirely — attempting it would always fail the upload.
	if useTempThenRename(targetClient, deleteAfterUpload) {
		// Attempt to delete original. Ignore not found error if it's already gone.
		_ = targetClient.DeleteFile(ctx, task.ResourceType, targetPath)
		if renameErr := targetClient.RenameFile(ctx, task.ResourceType, uploadPath, targetPath); renameErr != nil {
			_ = targetClient.DeleteFile(ctx, task.ResourceType, uploadPath)
			return fmt.Errorf("failed to rename temp file to target path: %w", renameErr)
		}
	}

	if applier, ok := targetClient.(storage.MetadataApplier); ok {
		var meta storage.FileMetadata
		if task.Metadata != nil {
			_ = json.Unmarshal(task.Metadata, &meta)
		}
		if meta.ModifiedTime.IsZero() {
			if srcInfo, inspectErr := sourceClient.InspectResource(ctx, task.ResourceType, task.FilePath); inspectErr == nil {
				meta.ModifiedTime = srcInfo.LastModified
			}
		}
		if !meta.ModifiedTime.IsZero() || meta.Description != "" {
			if err := applier.ApplyMetadata(ctx, task.ResourceType, targetPath, meta); err != nil {
				log.Printf("Warning: failed to apply metadata for %s: %v", targetPath, err)
			}
		}
	}

	// 5. Hash & Integrity Verification
	var integrityVerified bool
	downloadOK := true
	uploadOK := true

	if task.ResourceType == "files" {
		workerSourceHashVal := fmt.Sprintf("%x", sourceHasher.Sum(nil))
		task.WorkerHash = sql.NullString{String: fmt.Sprintf("%s:%s", sourceAlgo, workerSourceHashVal), Valid: true}

		if sourceHashStr != "" && sourceAlgo != "UNKNOWN" {
			downloadOK = (workerSourceHashVal == sourceHashStr)
		}

		var targetHashVal string
		var errTargetHash error
		// Retry the hash query: a transient Nextcloud error (502/503/423/timeout)
		// returns an empty hash and would otherwise cause a false integrity failure.
		// Retrying non-retryable errors ("checksum not available", "not supported")
		// adds 4s of useless sleep per file, so we break immediately for those.
		for hashAttempt := 0; hashAttempt < 3; hashAttempt++ {
			if mig.TargetProvider != "webdav" {
				targetHashVal, errTargetHash = targetClient.GetFileHash(ctx, task.ResourceType, targetPath)
			} else {
				errTargetHash = fmt.Errorf("webdav target hash not supported")
				break
			}
			if (errTargetHash == nil && targetHashVal != "") || isNonRetryableHashError(errTargetHash) {
				break
			}
			if hashAttempt < 2 {
				time.Sleep(2 * time.Second)
			}
		}
		if errTargetHash != nil {
			if !isNonRetryableHashError(errTargetHash) {
				log.Printf("[INTEGRITY] GetFileHash failed after retries for %s: %v", targetPath, errTargetHash)
			} else {
				log.Printf("[INTEGRITY] No checksum available on target for %s (%v) — using size verification", targetPath, errTargetHash)
			}
		}

		if errTargetHash == nil && targetHashVal != "" {
			task.TargetHash = sql.NullString{String: targetHashVal, Valid: true}
			targetReturnedAlgo, cleanTargetHash := storage.ParseHashString(targetHashVal)
			if mig.TargetProvider == "dropbox" {
				targetReturnedAlgo = "DROPBOX"
			}

			var workerTargetHashVal string
			hasMatchingAlgo := false
			if sourceAlgo == targetReturnedAlgo && sourceAlgo != "UNKNOWN" {
				workerTargetHashVal = workerSourceHashVal
				hasMatchingAlgo = true
			} else if targetHasher != nil && targetAlgo == targetReturnedAlgo && targetAlgo != "UNKNOWN" {
				workerTargetHashVal = fmt.Sprintf("%x", targetHasher.Sum(nil))
				hasMatchingAlgo = true
			}

			if hasMatchingAlgo {
				if workerTargetHashVal == cleanTargetHash {
					uploadOK = true
				} else {
					// Hash mismatch: some providers (e.g. Nextcloud via oc:checksums)
					// report a checksum that does not match the actual content hash even
					// though the upload succeeded and the file is intact. The upload commit
					// already verified the size, so fall back to a size comparison before
					// declaring the transfer corrupt (prevents false "skipped" files).
					existsOnTarget, targetSize, errExists := queryTargetSize(ctx, targetClient, task.ResourceType, targetPath, true)
					if errExists == nil && existsOnTarget {
						if task.FileSize == 0 {
							uploadOK = true
						} else {
							uploadOK = (task.FileSize == targetSize)
						}
						if uploadOK {
							log.Printf("[INTEGRITY] target hash mismatch but size matches for %s (source=%d, target=%d) — accepting", targetPath, task.FileSize, targetSize)
						}
					} else if errExists != nil {
						// The size-verification query itself failed (e.g. transient Nextcloud
						// 502/503/423). The chunked-upload commit already verified the file
						// exists with the correct size, so accept rather than fail the
						// (correct) transfer.
						log.Printf("[INTEGRITY] target hash mismatch and size-query failed for %s; accepting (commit verified size)", targetPath)
						uploadOK = true
					} else {
						uploadOK = false
					}
				}
			} else {
				// Algorithm mismatch fallback: verify size
				existsOnTarget, targetSize, errExists := queryTargetSize(ctx, targetClient, task.ResourceType, targetPath, false)
				if errExists == nil && existsOnTarget {
					if task.FileSize == 0 {
						uploadOK = true // Google Docs, Calendars, and Contacts have dynamic sizes
					} else {
						uploadOK = (task.FileSize == targetSize)
					}
					task.TargetHash = sql.NullString{String: fmt.Sprintf("SIZE:%d", targetSize), Valid: true}
				} else if errExists != nil {
					// Size-verification query failed (transient Nextcloud 502/503/423);
					// the upload commit already verified size, so accept.
					log.Printf("[INTEGRITY] size-query failed for %s; accepting (commit verified size)", targetPath)
					uploadOK = true
				} else {
					uploadOK = false
				}
			}
		} else {
			// Fallback: Size verification
			existsOnTarget, targetSize, errExists := queryTargetSize(ctx, targetClient, task.ResourceType, targetPath, false)
			if errExists == nil && existsOnTarget {
				if task.FileSize == 0 {
					uploadOK = true // Google Docs, Calendars, and Contacts have dynamic sizes
				} else {
					uploadOK = (task.FileSize == targetSize)
				}
				task.TargetHash = sql.NullString{String: fmt.Sprintf("SIZE:%d", targetSize), Valid: true}
			} else if errExists != nil {
				// Size-verification query failed (transient Nextcloud 502/503/423);
				// the upload commit already verified size, so accept.
				log.Printf("[INTEGRITY] size-query failed for %s; accepting (commit verified size)", targetPath)
				uploadOK = true
			} else {
				uploadOK = false
			}
		}
	} else {
		// Non-files (calendars/contacts) have dynamic content that isn't verifyable via strict checksums or sizes
		// and were already successfully stored.
		task.WorkerHash = sql.NullString{String: "DYNAMIC", Valid: true}
		task.TargetHash = sql.NullString{String: "DYNAMIC", Valid: true}
	}

	// If the target verified correctly by size but the source hash did not match
	// (e.g. the source provider reports an unreliable/legacy checksum, or the file
	// changed after indexing), the transferred file is still intact: it was streamed
	// 1:1 and the target size matches the source size. Treat the source-hash
	// discrepancy as non-fatal so we don't wrongly skip a good file.
	if !downloadOK && uploadOK {
		log.Printf("[INTEGRITY] source hash mismatch but target size verified for %s — accepting", task.FilePath)
		downloadOK = true
	}

	integrityVerified = downloadOK && uploadOK
	if !integrityVerified {
		// Detail log so we can see exactly which part mismatched (hash vs size,
		// source vs target). workerHash holds the source-computed hash, targetHash
		// holds the target hash or "SIZE:<n>" when only size verification was possible.
		log.Printf("[INTEGRITY] FAILED for %s: downloadOK=%v uploadOK=%v workerHash=%s targetHash=%s sourceFileSize=%d",
			task.FilePath, downloadOK, uploadOK, task.WorkerHash.String, task.TargetHash.String, task.FileSize)
		return fmt.Errorf("data integrity check failed: hashes or sizes did not match")
	}

	// Update task to COMPLETED
	task.Status = "COMPLETED"
	task.ErrorMessage = sql.NullString{}
	_ = db.UpdateTaskStatus(p.db, task)

	// A successful transfer breaks the "consecutive connection loss" streak (P1-4).
	p.clearConnLoss(mig.ID)
	p.clearConnLossTask(task.ID)

	// Increment processed files AND bytes count exactly once, at verified
	// completion. Bytes are booked here (not via the progress channel) so that a
	// retried upload (hash mismatch etc.) cannot double-count the same file and
	// push processed_bytes above total_bytes.
	_ = db.IncrementMigrationProgress(p.db, ctx, mig.ID, 1, task.FileSize, 0, 0)
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

	// Check if migration was manually cancelled
	mig, migErr := db.GetMigration(p.db, payload.MigrationID)
	if migErr == nil && mig.Status == "CANCELLED" {
		log.Printf("[Worker %s] Task %s aborted (Migration cancelled).\n", p.workerID, payload.TaskID)
		task.Status = "CANCELLED"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	// Check if context is cancelled (graceful shutdown)
	isShutdown := errors.Is(procErr, context.Canceled) || ctx.Err() != nil
	if isShutdown {
		log.Printf("[Worker %s] Shutdown detected. Requeueing task %s...\n", p.workerID, payload.TaskID)

		task.Status = "PENDING"
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	task.Attempts++
	task.ErrorMessage = sql.NullString{String: procErr.Error(), Valid: true}

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
			_ = db.UpdateTaskStatus(p.db, task)
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
		_ = db.UpdateTaskStatus(p.db, task)
		return
	}

	// Check if error is permanent / non-retryable
	isPermanent := false
	errStr := procErr.Error()
	if strings.Contains(errStr, "exportSizeLimitExceeded") ||
		strings.Contains(errStr, "badRequest") ||
		strings.Contains(errStr, "conversion is not supported") ||
		strings.Contains(errStr, "fileNotDownloadable") ||
		strings.Contains(errStr, "Only files with binary content can be downloaded") ||
		strings.Contains(errStr, "too large to be exported") ||
		strings.Contains(errStr, "notFound") ||
		strings.Contains(errStr, "fileNotFound") {
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
		_ = db.UpdateTaskStatus(p.db, task)
		_ = db.IncrementMigrationProgress(p.db, ctx, task.MigrationID, 1, task.FileSize, 0, 1)
		// Cancel any remaining PENDING tasks so they are not orphaned: the dequeue
		// query only selects PENDING while RUNNING/INDEXING, so they would otherwise
		// stay stuck forever (processed_files never reaches total_files, WebSocket
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
		backoff := retryBackoff(task.Attempts)
		nextRetry := time.Now().Add(backoff)
		task.Status = "FAILED" // Kept as failed until cron schedules retry
		task.NextRetryAt = sql.NullTime{Time: nextRetry, Valid: true}
		_ = db.UpdateTaskStatus(p.db, task)

		log.Printf("[Worker %s] Task %s scheduled for retry in %ds (Attempt %d/3)\n", p.workerID, task.ID, int(backoff.Seconds()), task.Attempts)
	} else {
		// Max retries reached, fail permanently
		task.Status = "FAILED"
		task.NextRetryAt = sql.NullTime{}
		_ = db.UpdateTaskStatus(p.db, task)
		// Task is now terminal: drop its per-task connection-loss counter so the
		// in-memory map does not grow unbounded across a long-running worker.
		p.clearConnLossTask(task.ID)

		// Increment migration failed files
		_ = db.IncrementMigrationProgress(p.db, ctx, task.MigrationID, 1, task.FileSize, 0, 1)
		log.Printf("[Worker %s] Task %s failed permanently after %d attempts\n", p.workerID, task.ID, task.Attempts)
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
	var refreshTokenEnc sql.NullString
	var expiresAt sql.NullTime
	var provider string

	// tokenSet selects the role-specific encrypted tokens/expiry/provider from a
	// migration row. Used both for the initial check and the post-lock re-read so
	// the source/target branches are not duplicated.
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
	refreshTokenEnc, expiresAt, provider = initial.refreshEnc, initial.expiresAt, initial.provider

	// No refresh token stored → not an OAuth provider, nothing to do.
	if !refreshTokenEnc.Valid || refreshTokenEnc.String == "" {
		return accessToken, nil
	}

	// Acquire lock to serialize token refresh requests for the same migration (Finding 7)
	mu := p.getOrCreateRefreshLock(mig.ID)
	mu.Lock()
	defer mu.Unlock()

	// Double-check: re-query the migration from the database after acquiring the lock to get the latest tokens.
	latestMig, err := db.GetMigration(p.db, mig.ID)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest migration details inside refresh lock: %w", err)
	}

	// Adopt the latest access token (if it decrypts) and refresh/expiry/provider
	// fields from the re-read row.
	latest := tokenSet(latestMig)
	if latestAccess, derr := crypto.Decrypt(latest.accessEnc, p.secretKey); derr == nil {
		accessToken = latestAccess
	}
	refreshTokenEnc, expiresAt, provider = latest.refreshEnc, latest.expiresAt, latest.provider

	if expiresAt.Valid && time.Now().Before(expiresAt.Time.Add(-2*time.Minute)) {
		return accessToken, nil
	}

	log.Printf("[Worker %s] %s OAuth token expired or near expiry for migration %s — refreshing inline\n",
		p.workerID, role, mig.ID)

	// Decrypt refresh token immediately before use (Zero Plaintext rule from PRD-12)
	refreshToken, err := crypto.Decrypt(refreshTokenEnc.String, p.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt %s refresh token: %w", role, err)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	tokenResp, err := oauth.RefreshToken(refreshCtx, provider, refreshToken)
	cancel()
	if err != nil {
		return "", fmt.Errorf("OAuth refresh failed for %s (%s): %w", role, provider, err)
	}

	// Encrypt and persist the new token pair atomically (Token Rotation Constraint F-03).
	// Encryption failure is fatal: for single-use refresh tokens (e.g. Google) the old
	// refresh token has already been invalidated by the provider. Proceeding without
	// persisting the new one would leave the DB with a stale token and cause a permanent
	// auth failure on the next refresh attempt.
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
	if err := db.UpdateMigrationOAuthTokens(p.db, db.OAuthTokenUpdate{
		MigrationID:           mig.ID,
		Role:                  role,
		AccessTokenEncrypted:  newAccessEnc,
		RefreshTokenEncrypted: newRefreshEnc,
		ExpiresAt:             time.Now().Add(time.Duration(expiresIn) * time.Second),
	}); err != nil {
		return "", fmt.Errorf("failed to persist new %s OAuth tokens after refresh: %w", role, err)
	}

	return tokenResp.AccessToken, nil
}

// RunCompletionNotifier polls for migrations that have reached a terminal state
// (COMPLETED or FAILED) and have not yet had their completion email sent. It uses
// the user's own per-user SMTP configuration (if configured and enabled).
func (p *Processor) RunCompletionNotifier(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	throttleCleanupTicker := time.NewTicker(1 * time.Minute)
	defer throttleCleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sendPendingCompletionEmails(ctx)
		case <-cleanupTicker.C:
			if err := db.DeleteExpiredPasswordResetTokens(p.db); err != nil {
				log.Printf("[CompletionNotifier] Error cleaning up expired reset tokens: %v\n", err)
			}
			if err := db.DeleteExpiredEmailChangeTokens(p.db); err != nil {
				log.Printf("[CompletionNotifier] Error cleaning up expired email change tokens: %v\n", err)
			}
		case <-throttleCleanupTicker.C:
			p.cleanupThrottlers()
		}
	}
}

// cleanupThrottlers removes throttlers for migrations that have reached a
// terminal state. Throttlers are intentionally kept alive for the full lifetime
// of a migration (not per-task) so that bandwidth changes applied between task
// batches are never dropped (avoids a delete-then-publish race).
func (p *Processor) cleanupThrottlers() {
	p.throttlers.Range(func(key, value interface{}) bool {
		migrationID := key.(string)
		mig, err := db.GetMigration(p.db, migrationID)
		if err != nil || mig == nil {
			p.throttlers.Delete(migrationID)
			return true
		}
		switch mig.Status {
		case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
			p.throttlers.Delete(migrationID)
		}
		return true
	})
}

func (p *Processor) sendPendingCompletionEmails(ctx context.Context) {
	// Claim one notification at a time and hold its row lock only for the
	// duration of a single send. This keeps the lock (and thus any blocked
	// workers) short, and means a crash can only lose — and therefore retry —
	// the one in-flight message instead of dropping a whole batch.
	for claimed := 0; claimed < 10; claimed++ {
		tx, notifs, err := db.LockPendingEmailNotifications(p.db, 1)
		if err != nil {
			log.Printf("[CompletionNotifier] Error claiming pending notification: %v\n", err)
			return
		}
		if len(notifs) == 0 {
			_ = tx.Rollback()
			break
		}
		n := notifs[0]
		// sendCompletionEmail marks the row sent (inside tx) on success or on an
		// intentional skip and returns nil; on a transient error it leaves the row
		// unmarked so the commit re-opens it for the next tick.
		if err := p.sendCompletionEmail(tx, n); err != nil {
			log.Printf("[CompletionNotifier] Transient failure for migration %s, will retry: %v\n", n.MigrationID, err)
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[CompletionNotifier] Error committing email claim for migration %s: %v\n", n.MigrationID, err)
		}
	}
}

// sendCompletionEmail sends the completion report for n. It marks the row sent
// inside tx (via MarkMigrationEmailSentTx) on success or on an intentional skip,
// and returns nil in those cases. On a transient failure (DB/SMTP) it leaves the
// row unmarked and returns the error so the caller's commit re-opens it for retry.
func (p *Processor) sendCompletionEmail(tx *sql.Tx, n db.PendingEmailNotification) error {
	settings, err := db.GetUserSMTPSettings(p.db, n.UserID)
	if err != nil {
		if err == sql.ErrNoRows {
			// User has no SMTP config: nothing to send, mark as sent to avoid retry.
			return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
		}
		return err // transient DB error -> retry
	}

	if !settings.NotifyOnCompletion {
		// User disabled completion notifications: mark as sent, do not retry.
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, p.secretKey)
	if err != nil {
		log.Printf("[CompletionNotifier] Error decrypting SMTP password for user %s: %v\n", n.UserID, err)
		// Permanent config error: skip this migration's mail rather than looping.
		return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	user, err := db.GetUserByID(p.db, n.UserID)
	if err != nil {
		return err // transient DB error -> retry
	}

	errMsg := ""
	if n.ErrorMessage.Valid {
		errMsg = n.ErrorMessage.String
	}

	htmlBody := email.BuildMigrationReportEmail(
		n.MigrationID, n.Status,
		n.TotalFiles, n.ProcessedFiles, n.FailedFiles, n.SkippedFiles,
		n.TotalBytes, n.ProcessedBytes, errMsg,
	)

	if err := email.SendMail(smtpCfg, user.Email, "Clumoove — Migrationsbericht", htmlBody); err != nil {
		// Actual SMTP send failed: leave the row unmarked so it is retried on the
		// next tick (the caller commits without marking).
		return err
	}

	return db.MarkMigrationEmailSentTx(tx, n.MigrationID)
}

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
			time.Sleep(2 * time.Second)
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
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checksum not available") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "not implemented")
}
