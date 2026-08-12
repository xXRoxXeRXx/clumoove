package processor

import (
	"context"
	"database/sql"
	"log"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

func (p *Processor) RunWorkerLiveness(ctx context.Context) {
	_ = p.queue.RegisterActiveWorker(ctx, p.workerID, 120*time.Second)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	cleanupTicker := time.NewTicker(60 * time.Second)
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
				claimed, lockErr := p.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 120*time.Second)
				if lockErr != nil || !claimed {
					continue
				}
				log.Printf("[Liveness] Found abandoned queue for worker %s, recovering tasks...\n", deadWorkerID)
				if err := p.queue.RecoverAbandonedTasks(ctx, p.db, deadWorkerID); err != nil {
					log.Printf("[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
				} else {
					p.queue.NotifyTaskAvailable(ctx, p.db)
				}
			}
		}
	}
}

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
	// Use two set-based updates in one transaction instead of one UPDATE per task.
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("[RetryScheduler] begin transaction: %v\n", err)
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
			log.Printf("[RetryScheduler] re-enqueue tasks: %v\n", err)
			return
		}
		count, _ := result.RowsAffected()
		requeued += count
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[RetryScheduler] commit re-enqueue: %v\n", err)
		return
	}
	if requeued > 0 {
		log.Printf("[RetryScheduler] Re-enqueued %d task(s)\n", requeued)
		p.queue.NotifyTaskAvailable(ctx, p.db)
	}
}

func (p *Processor) RunProgressReconciler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	repairTicker := time.NewTicker(5 * time.Minute)
	defer repairTicker.Stop()
	p.repairMissingMigrationNotifications()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcileActiveMigrations(ctx)
			p.reconcileActiveSyncJobs(ctx)
		case <-repairTicker.C:
			p.repairMissingMigrationNotifications()
		}
	}
}

func (p *Processor) repairMissingMigrationNotifications() {
	if _, err := db.RepairMissingMigrationNotificationEvents(p.db, 100); err != nil {
		log.Printf("[NotificationRepair] migration outbox repair: %v\n", err)
	}
}

func (p *Processor) reconcileActiveMigrations(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT m.id
		FROM migrations m
		-- INDEXING migrations may be actively adding tasks while workers drain an
		-- earlier batch. Reconciling them here could see a transient empty queue
		-- and incorrectly mark the migration terminal before the indexer performs
		-- its guarded INDEXING -> RUNNING transition.
		WHERE m.status = 'RUNNING'
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
		requeued, err := db.MaybeRetryFailedMigrationTasks(p.db, ctx, id)
		if err != nil {
			log.Printf("[ProgressReconciler] error checking failed tasks retry for migration %s: %v\n", id, err)
		}
		if requeued {
			p.queue.NotifyTaskAvailable(ctx, p.db)
		}
		if err := db.ReconcileMigrationProgress(p.db, id); err != nil {
			log.Printf("[ProgressReconciler] error reconciling migration %s: %v\n", id, err)
		}
	}
}

func (p *Processor) reconcileActiveSyncJobs(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT DISTINCT sj.id, sj.run_generation
		FROM sync_jobs sj
		WHERE sj.status = 'RUNNING'
	`)
	if err != nil {
		log.Printf("[ProgressReconciler] Sync DB query error: %v\n", err)
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
		log.Printf("[ProgressReconciler] Sync rows error: %v\n", err)
		return
	}

	for _, pass := range passes {
		if err := db.ReconcileSyncJobProgress(p.db, pass.id, pass.generation); err != nil {
			log.Printf("[ProgressReconciler] error reconciling sync job %s: %v\n", pass.id, err)
		}
	}
}

func (p *Processor) RunOrphanedRunningTasksRecovery(ctx context.Context) {
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

func (p *Processor) RunConnectionRecoveryScheduler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
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

type recoveryState struct {
	lastAttempt time.Time
	attempts    int
}

// maxMigrationRecoveryProbesPerTick bounds recovery attempts, including
// credential decryptions and provider connection probes, triggered by a shared
// connection outage. Entries currently in backoff do not consume the budget.
const maxMigrationRecoveryProbesPerTick = 10

func (p *Processor) recoveryCursor(syncJob bool) string {
	p.recoveryCursorMu.Lock()
	defer p.recoveryCursorMu.Unlock()
	if syncJob {
		return p.syncRecoveryCursor
	}
	return p.migrationRecoveryCursor
}

func (p *Processor) setRecoveryCursor(syncJob bool, id string) {
	p.recoveryCursorMu.Lock()
	defer p.recoveryCursorMu.Unlock()
	if syncJob {
		p.syncRecoveryCursor = id
		return
	}
	p.migrationRecoveryCursor = id
}

func (p *Processor) recordRecoveryFailure(id string, attempts int) {
	p.recoveryAttempts.Store(id, recoveryState{lastAttempt: time.Now(), attempts: attempts + 1})
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

func (p *Processor) recoverPausedMigrations(ctx context.Context) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
	`
	rows, err := p.db.QueryContext(ctx, query, p.recoveryCursor(false))
	if err != nil {
		return
	}
	defer rows.Close()

	probes := 0
	for rows.Next() {
		if probes >= maxMigrationRecoveryProbesPerTick {
			break
		}

		var id, sURL, sUser, sPassEnc, tURL, tUser, tPassEnc, sProv, tProv string
		var userID sql.NullString
		if err := rows.Scan(&id, &userID, &sURL, &sUser, &sPassEnc, &tURL, &tUser, &tPassEnc, &sProv, &tProv); err != nil {
			continue
		}

		var ra recoveryState
		if v, ok := p.recoveryAttempts.Load(id); ok {
			ra = v.(recoveryState)
		}
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++
		p.setRecoveryCursor(false, id)

		sPass, err := crypto.DecryptWithDomain(sPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(sProv)))
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}
		tPass, err := crypto.DecryptWithDomain(tPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(tProv)))
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}

		userCtx := storage.WithLocalUserScope(ctx, userID.String)
		sClient, err := storage.NewProvider(userCtx, sProv, sURL, sUser, sPass)
		if err != nil {
			p.recordRecoveryFailure(id, ra.attempts)
			continue
		}
		tClient, err := storage.NewProvider(userCtx, tProv, tURL, tUser, tPass)
		if err != nil {
			sClient.Close()
			p.recordRecoveryFailure(id, ra.attempts)
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
			p.recoveryAttempts.Delete(id)
		} else {
			p.recordRecoveryFailure(id, ra.attempts)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[RecoveryScheduler] rows error: %v\n", err)
	}
}
