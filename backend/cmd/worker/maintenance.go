package main

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/queue"
)

type workerMaintenance struct {
	db                      *sql.DB
	migrations              db.MigrationStore
	syncs                   db.SyncStore
	notifications           db.NotificationStore
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
	var migrations db.MigrationStore
	var syncs db.SyncStore
	var notifications db.NotificationStore
	if database != nil {
		migrations = db.NewMigrationStore(database)
		syncs = db.NewSyncStore(database)
		notifications = db.NewNotificationStore(database)
	}
	return &workerMaintenance{
		migrations:    migrations,
		syncs:         syncs,
		notifications: notifications,
		db:            database,
		queue:         q,
		indexer:       idx,
		workerID:      workerID,
		secretKey:     secretKey,
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
	if _, err := m.notifications.RepairMissingMigrationNotificationEvents(100); err != nil {
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
		requeued, err := m.migrations.MaybeRetryFailedMigrationTasks(ctx, id)
		if err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error checking failed tasks retry for migration %s: %v\n", id, err)
		}
		if requeued {
			m.queue.NotifyTaskAvailable(ctx, m.db)
		}
		if err := m.migrations.ReconcileMigrationProgress(id); err != nil {
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
		if err := m.syncs.ReconcileSyncJobProgress(pass.id, pass.generation); err != nil {
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
