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
	migrations              db.MigrationStore
	syncs                   db.SyncStore
	tasks                   db.TaskStore
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
	var tasks db.TaskStore
	var notifications db.NotificationStore
	if database != nil {
		migrations = db.NewMigrationStore(database)
		syncs = db.NewSyncStore(database)
		tasks = db.NewTaskStore(database)
		notifications = db.NewNotificationStore(database)
	}
	return &workerMaintenance{
		migrations:    migrations,
		syncs:         syncs,
		tasks:         tasks,
		notifications: notifications,
		queue:         q,
		indexer:       idx,
		workerID:      workerID,
		secretKey:     secretKey,
	}
}

// RecoverStartupTasks requeues tasks this worker left behind before a restart.
func (m *workerMaintenance) RecoverStartupTasks(ctx context.Context) {
	if err := m.queue.RecoverAbandonedTasks(ctx, m.tasks, m.workerID); err != nil {
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
	deadWorkers, err := m.queue.GetAbandonedWorkerQueues(ctx, m.tasks)
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
		if err := m.queue.RecoverAbandonedTasks(ctx, m.tasks, deadWorkerID); err != nil {
			workerLogf("maintenance", "[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
		} else {
			m.queue.NotifyTaskAvailable(ctx, m.tasks)
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
	requeued, err := m.tasks.RequeueDueFailedTasks(ctx, time.Now())
	if err != nil {
		workerLogf("maintenance", "[RetryScheduler] re-enqueue tasks: %v\n", err)
		return
	}
	if requeued > 0 {
		workerLogf("maintenance", "[RetryScheduler] Re-enqueued %d task(s)\n", requeued)
		m.queue.NotifyTaskAvailable(ctx, m.tasks)
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
	// A read failure still returns the ids read before it; reconciling them is
	// idempotent, so the failure is logged and the sweep carries on.
	ids, err := m.migrations.ListRunningMigrationIDs(ctx)
	if err != nil {
		workerLogf("maintenance", "[ProgressReconciler] DB query error: %v\n", err)
	}

	for _, id := range ids {
		requeued, err := m.migrations.MaybeRetryFailedMigrationTasks(ctx, id)
		if err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error checking failed tasks retry for migration %s: %v\n", id, err)
		}
		if requeued {
			m.queue.NotifyTaskAvailable(ctx, m.tasks)
		}
		if err := m.migrations.ReconcileMigrationProgress(id); err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error reconciling migration %s: %v\n", id, err)
		}
	}
}

func (m *workerMaintenance) reconcileActiveSyncJobs(ctx context.Context) {
	passes, err := m.syncs.ListRunningSyncPasses(ctx)
	if err != nil {
		workerLogf("maintenance", "[ProgressReconciler] Sync DB query error: %v\n", err)
	}

	for _, pass := range passes {
		if err := m.syncs.ReconcileSyncJobProgress(pass.ID, pass.Generation); err != nil {
			workerLogf("maintenance", "[ProgressReconciler] error reconciling sync job %s: %v\n", pass.ID, err)
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
	count, err := m.tasks.RequeueOrphanedRunningTasks(ctx)
	if err != nil {
		workerLogf("maintenance", "[OrphanedTaskRecovery] DB update error: %v\n", err)
		return
	}
	if count > 0 {
		workerLogf("maintenance", "[OrphanedTaskRecovery] Re-enqueued %d orphaned RUNNING tasks\n", count)
		m.queue.NotifyTaskAvailable(ctx, m.tasks)
	}
}
