package main

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/storage"
)

const (
	maxMigrationRecoveryProbesPerTick = 10
	maxSyncRecoveryProbesPerTick      = 10
	recoveryScanWindow                = 30
)

type workerMaintenance struct {
	db                      *sql.DB
	queue                   *queue.Queue
	indexer                 *indexer.Indexer
	workerID                string
	secretKey               string
	recoveredIndexers       sync.WaitGroup
	recoveryAttempts        sync.Map
	recoveryCursorMu        sync.Mutex
	migrationRecoveryCursor string
	syncRecoveryCursor      string
}

func newWorkerMaintenance(database *sql.DB, q *queue.Queue, idx *indexer.Indexer, workerID, secretKey string) *workerMaintenance {
	return &workerMaintenance{
		db:        database,
		queue:     q,
		indexer:   idx,
		workerID:  workerID,
		secretKey: secretKey,
	}
}

// RecoverStartupTasks requeues tasks this worker left behind before a restart.
func (m *workerMaintenance) RecoverStartupTasks(ctx context.Context) {
	if err := m.queue.RecoverAbandonedTasks(ctx, m.db, m.workerID); err != nil {
		workerLogf("maintenance", "[Worker %s] Error recovering abandoned tasks: %v\n", m.workerID, err)
	}
}

// Loops returns the background maintenance schedulers owned by the worker runtime.
func (m *workerMaintenance) Loops() []backgroundLoop {
	loop := func(name string, run func(ctx context.Context)) backgroundLoop {
		return backgroundLoop{Name: name, Run: func(ctx context.Context) error {
			run(ctx)
			return nil
		}}
	}
	return []backgroundLoop{
		loop("worker_liveness", m.runWorkerLiveness),
		loop("abandoned_worker_cleanup", m.runAbandonedWorkerCleanup),
		loop("retry_scheduler", m.runRetryScheduler),
		loop("connection_recovery", m.awaitRecoveredIndexers(m.runConnectionRecoveryScheduler)),
		loop("orphaned_task_recovery", m.runOrphanedRunningTasksRecovery),
		loop("progress_reconciler", m.runProgressReconciler),
	}
}

func (m *workerMaintenance) awaitRecoveredIndexers(run func(ctx context.Context)) func(ctx context.Context) {
	return func(ctx context.Context) {
		run(ctx)
		m.recoveredIndexers.Wait()
	}
}

func (m *workerMaintenance) runWorkerLiveness(ctx context.Context) {
	m.refreshWorkerLease(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshWorkerLease(ctx)
		}
	}
}

func (m *workerMaintenance) refreshWorkerLease(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := m.queue.RegisterActiveWorker(opCtx, m.workerID, 120*time.Second); err != nil {
		workerLogf("maintenance", "[Liveness] Error registering active worker: %v\n", err)
	}
}

func (m *workerMaintenance) runAbandonedWorkerCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			passCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			m.cleanupAbandonedWorkers(passCtx)
			cancel()
		}
	}
}

func (m *workerMaintenance) cleanupAbandonedWorkers(ctx context.Context) {
	deadWorkers, err := m.queue.GetAbandonedWorkerQueues(ctx, m.db)
	if err != nil {
		workerLogf("maintenance", "[Liveness] Error scanning for dead workers: %v\n", err)
		return
	}
	for _, deadWorkerID := range deadWorkers {
		if ctx.Err() != nil {
			return
		}
		if deadWorkerID == m.workerID {
			continue
		}
		claimed, err := m.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 120*time.Second)
		if err != nil || !claimed {
			continue
		}
		if err := m.queue.RecoverAbandonedTasks(ctx, m.db, deadWorkerID); err != nil {
			workerLogf("maintenance", "[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
		} else {
			m.queue.NotifyTaskAvailable(ctx, m.db)
		}
	}
}

func (m *workerMaintenance) runRetryScheduler(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.requeueFailedTasks(ctx)
		}
	}
}

func (m *workerMaintenance) requeueFailedTasks(ctx context.Context) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		workerLogf("maintenance", "[RetryScheduler] begin transaction: %v\n", err)
		return
	}
	defer tx.Rollback()

	now := time.Now()
	var requeued int64
	for _, updateQuery := range []string{
		`UPDATE tasks AS t SET status = 'PENDING', next_retry_at = NULL, checksum_verified = FALSE FROM migrations m WHERE t.migration_id = m.id AND t.status = 'FAILED' AND t.next_retry_at <= $1 AND m.status IN ('RUNNING', 'INDEXING', 'VERIFYING')`,
		`UPDATE tasks AS t SET status = 'PENDING', next_retry_at = NULL, checksum_verified = FALSE FROM sync_jobs sj WHERE t.sync_job_id = sj.id AND t.pass_generation = sj.run_generation AND t.status = 'FAILED' AND t.next_retry_at <= $1 AND sj.status IN ('RUNNING', 'INDEXING', 'VERIFYING')`,
	} {
		result, err := tx.ExecContext(ctx, updateQuery, now)
		if err != nil {
			workerLogf("maintenance", "[RetryScheduler] re-enqueue tasks: %v\n", err)
			return
		}
		count, _ := result.RowsAffected()
		requeued += count
	}
	if err := tx.Commit(); err != nil {
		workerLogf("maintenance", "[RetryScheduler] commit re-enqueue: %v\n", err)
		return
	}
	if requeued > 0 {
		workerLogf("maintenance", "[RetryScheduler] Re-enqueued %d task(s)\n", requeued)
		m.queue.NotifyTaskAvailable(ctx, m.db)
	}
}

func (m *workerMaintenance) runProgressReconciler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	repairTicker := time.NewTicker(5 * time.Minute)
	defer repairTicker.Stop()
	m.repairMissingMigrationNotifications()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcileActiveMigrations(ctx)
			m.reconcileActiveSyncJobs(ctx)
		case <-repairTicker.C:
			m.repairMissingMigrationNotifications()
		}
	}
}

func (m *workerMaintenance) repairMissingMigrationNotifications() {
	if _, err := db.RepairMissingMigrationNotificationEvents(m.db, 100); err != nil {
		workerLogf("maintenance", "[NotificationRepair] migration outbox repair: %v\n", err)
	}
}

func (m *workerMaintenance) reconcileActiveMigrations(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT DISTINCT m.id
		FROM migrations m
		WHERE m.status = 'RUNNING'
	`)
	if err != nil {
		workerLogf("maintenance", "[ProgressReconciler] DB query error: %v\n", err)
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
		workerLogf("maintenance", "[ProgressReconciler] rows error: %v\n", err)
		return
	}

	for _, id := range ids {
		requeued, err := db.MaybeRetryFailedMigrationTasks(m.db, ctx, id)
		if err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error checking failed tasks retry for migration %s: %v\n", id, err)
		}
		if requeued {
			m.queue.NotifyTaskAvailable(ctx, m.db)
		}
		if err := db.ReconcileMigrationProgress(m.db, id); err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error reconciling migration %s: %v\n", id, err)
		}
	}
}

func (m *workerMaintenance) reconcileActiveSyncJobs(ctx context.Context) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT DISTINCT sj.id, sj.run_generation
		FROM sync_jobs sj
		WHERE sj.status = 'RUNNING'
	`)
	if err != nil {
		workerLogf("maintenance", "[ProgressReconciler] Sync DB query error: %v\n", err)
		return
	}
	defer rows.Close()

	type syncPass struct {
		id         string
		generation int
	}
	var passes []syncPass
	for rows.Next() {
		var pass syncPass
		if err := rows.Scan(&pass.id, &pass.generation); err != nil {
			continue
		}
		passes = append(passes, pass)
	}
	if err := rows.Err(); err != nil {
		workerLogf("maintenance", "[ProgressReconciler] Sync rows error: %v\n", err)
		return
	}

	for _, pass := range passes {
		if err := db.ReconcileSyncJobProgress(m.db, pass.id, pass.generation); err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error reconciling sync job %s: %v\n", pass.id, err)
		}
	}
}

func (m *workerMaintenance) runOrphanedRunningTasksRecovery(ctx context.Context) {
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
			m.requeueOrphanedRunningTasks(ctx)
		}
	}
}

func (m *workerMaintenance) requeueOrphanedRunningTasks(ctx context.Context) {
	query := `
		UPDATE tasks t
		SET status = 'PENDING', worker_hash = NULL, updated_at = NOW()
		WHERE t.status = 'RUNNING'
		  AND t.updated_at < NOW() - INTERVAL '10 minutes'
		  AND (
			EXISTS (SELECT 1 FROM migrations m WHERE m.id = t.migration_id AND m.status IN ('RUNNING', 'INDEXING'))
			OR EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status IN ('RUNNING', 'INDEXING'))
		  )
	`
	result, err := m.db.ExecContext(ctx, query)
	if err != nil {
		workerLogf("maintenance", "[OrphanedTaskRecovery] DB update error: %v\n", err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		workerLogf("maintenance", "[OrphanedTaskRecovery] Cannot count reset tasks: %v\n", err)
		return
	}
	if count > 0 {
		workerLogf("maintenance", "[OrphanedTaskRecovery] Re-enqueued %d orphaned RUNNING tasks\n", count)
		m.queue.NotifyTaskAvailable(ctx, m.db)
	}
}

func (m *workerMaintenance) runConnectionRecoveryScheduler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.recoverPausedMigrations(ctx)
			m.recoverPausedSyncJobs(ctx)
		}
	}
}

type recoveryState struct {
	lastAttempt     time.Time
	attempts        int
	pausedUpdatedAt time.Time
}

type recoveryCandidate struct {
	id, userID, sourceURL, sourceUsername, sourcePassword, targetURL, targetUsername, targetPassword, sourceProvider, targetProvider string
	updatedAt                                                                                                                        time.Time
}

func (m *workerMaintenance) recoveryCursor(syncJob bool) string {
	m.recoveryCursorMu.Lock()
	defer m.recoveryCursorMu.Unlock()
	if syncJob {
		return m.syncRecoveryCursor
	}
	return m.migrationRecoveryCursor
}

func (m *workerMaintenance) setRecoveryCursor(syncJob bool, id string) {
	m.recoveryCursorMu.Lock()
	defer m.recoveryCursorMu.Unlock()
	if syncJob {
		m.syncRecoveryCursor = id
		return
	}
	m.migrationRecoveryCursor = id
}

func recoveryAttemptKey(syncJob bool, id string) string {
	if syncJob {
		return "sync:" + id
	}
	return "migration:" + id
}

func (m *workerMaintenance) effectiveRecoveryState(key string, updatedAt time.Time) recoveryState {
	if v, ok := m.recoveryAttempts.Load(key); ok {
		state := v.(recoveryState)
		if state.pausedUpdatedAt.Equal(updatedAt) {
			return state
		}
	}
	return recoveryState{pausedUpdatedAt: updatedAt}
}

func (m *workerMaintenance) recordRecoveryFailure(syncJob bool, id string, attempts int, pausedUpdatedAt time.Time) {
	m.recoveryAttempts.Store(recoveryAttemptKey(syncJob, id), recoveryState{
		lastAttempt:     time.Now(),
		attempts:        attempts + 1,
		pausedUpdatedAt: pausedUpdatedAt,
	})
}

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

func shouldProbeRecovery(state recoveryState, now time.Time) bool {
	backoff := recoveryBackoff(state.attempts)
	return backoff == 0 || now.Sub(state.lastAttempt) >= backoff
}

func examineRecoveryCandidate(lastExamined *string, probes, budget int, id string) bool {
	if probes >= budget {
		return false
	}
	*lastExamined = id
	return true
}

func (m *workerMaintenance) probeCandidateConnections(ctx context.Context, c recoveryCandidate) bool {
	sPass, err := crypto.DecryptWithDomain(c.sourcePassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.sourceProvider)))
	if err != nil {
		return false
	}
	tPass, err := crypto.DecryptWithDomain(c.targetPassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.targetProvider)))
	if err != nil {
		return false
	}

	userCtx := storage.WithLocalUserScope(ctx, c.userID)
	sClient, err := storage.NewProvider(userCtx, c.sourceProvider, c.sourceURL, c.sourceUsername, sPass)
	if err != nil {
		return false
	}
	tClient, err := storage.NewProvider(userCtx, c.targetProvider, c.targetURL, c.targetUsername, tPass)
	if err != nil {
		sClient.Close()
		return false
	}

	connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
	sOK, _ := sClient.Connect(connCtx)
	tOK, _ := tClient.Connect(connCtx)
	connCancel()
	sClient.Close()
	tClient.Close()
	return sOK && tOK
}

func (m *workerMaintenance) recoverPausedMigrations(ctx context.Context) {
	query := `
		SELECT id, COALESCE(user_id, ''), source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`
	m.recoverPausedEntities(ctx, false, query, maxMigrationRecoveryProbesPerTick, func(candidate recoveryCandidate) bool {
		workerLogf("maintenance", "[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", candidate.id)
		recovered, needsDiscovery, recoverErr := db.RecoverConnectionLostMigration(m.db, candidate.id)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error resuming migration %s: %v\n", candidate.id, recoverErr)
			return false
		}
		if !recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Did not resume migration %s because its status changed", candidate.id)
			return false
		}
		if needsDiscovery && m.indexer != nil {
			m.recoveredIndexers.Add(1)
			id := candidate.id
			go func() {
				defer m.recoveredIndexers.Done()
				m.indexer.Start(ctx, id)
			}()
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedSyncJobs(ctx context.Context) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM sync_jobs
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`
	m.recoverPausedEntities(ctx, true, query, maxSyncRecoveryProbesPerTick, func(candidate recoveryCandidate) bool {
		recovered, recoverErr := db.RecoverConnectionLostSyncJob(m.db, ctx, candidate.id)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error recovering sync job %s: %v\n", candidate.id, recoverErr)
			return false
		}
		if recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Connection restored for sync job %s; scheduled API retry\n", candidate.id)
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedEntities(
	ctx context.Context,
	syncJob bool,
	query string,
	maxProbes int,
	recoverFn func(candidate recoveryCandidate) bool,
) {
	candidates, err := m.queryRecoveryCandidates(ctx, query, m.recoveryCursor(syncJob))
	if err != nil {
		return
	}

	probes := 0
	lastExamined := ""
	for _, candidate := range candidates {
		if !examineRecoveryCandidate(&lastExamined, probes, maxProbes, candidate.id) {
			break
		}
		key := recoveryAttemptKey(syncJob, candidate.id)
		ra := m.effectiveRecoveryState(key, candidate.updatedAt)
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++

		if !m.probeCandidateConnections(ctx, candidate) {
			m.recordRecoveryFailure(syncJob, candidate.id, ra.attempts, candidate.updatedAt)
			continue
		}

		if recoverFn(candidate) {
			m.recoveryAttempts.Delete(key)
		}
	}
	if lastExamined != "" {
		m.setRecoveryCursor(syncJob, lastExamined)
	}
}

func (m *workerMaintenance) queryRecoveryCandidates(ctx context.Context, query, cursor string) ([]recoveryCandidate, error) {
	rows, err := m.db.QueryContext(ctx, query, cursor, recoveryScanWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []recoveryCandidate
	for rows.Next() {
		var candidate recoveryCandidate
		if err := rows.Scan(
			&candidate.id, &candidate.userID,
			&candidate.sourceURL, &candidate.sourceUsername, &candidate.sourcePassword,
			&candidate.targetURL, &candidate.targetUsername, &candidate.targetPassword,
			&candidate.sourceProvider, &candidate.targetProvider, &candidate.updatedAt,
		); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		workerLogf("maintenance", "[RecoveryScheduler] rows error: %v\n", err)
		return nil, err
	}
	return candidates, nil
}
