package processor

import (
	"context"
	"log"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

func (p *Processor) RunWorkerLiveness(ctx context.Context) {
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
	query := `
		SELECT t.id, COALESCE(t.migration_id::text, t.sync_job_id::text, '')
		FROM tasks t
		LEFT JOIN migrations m ON t.migration_id = m.id
		LEFT JOIN sync_jobs sj ON t.sync_job_id = sj.id
		WHERE t.status = 'FAILED' 
		  AND t.next_retry_at <= $1
		  AND (
		    (t.migration_id IS NOT NULL AND m.status IN ('RUNNING', 'INDEXING', 'VERIFYING'))
		    OR
		    (t.sync_job_id IS NOT NULL AND sj.status IN ('RUNNING', 'INDEXING', 'VERIFYING'))
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

		updateQuery := `
			UPDATE tasks
			SET status = 'PENDING', next_retry_at = NULL, checksum_verified = FALSE
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
	if requeued > 0 {
		p.queue.NotifyTaskAvailable(ctx, p.db)
	}
}

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
			p.recoveryAttempts.Delete(id)
		} else {
			p.recoveryAttempts.Store(id, recoveryState{lastAttempt: time.Now(), attempts: ra.attempts + 1})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[RecoveryScheduler] rows error: %v\n", err)
	}
}
