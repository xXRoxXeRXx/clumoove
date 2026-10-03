package processor

import (
	"context"
	"database/sql"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

func (p *Processor) RunWorkerLiveness(ctx context.Context) {
	p.refreshWorkerLease(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refreshWorkerLease(ctx)
		}
	}
}

func (p *Processor) refreshWorkerLease(ctx context.Context) {
	opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := p.queue.RegisterActiveWorker(opCtx, p.workerID, 120*time.Second); err != nil {
		processorLogf("[Liveness] Error registering active worker: %v\n", err)
	}
}

func (p *Processor) RunAbandonedWorkerCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			passCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			p.cleanupAbandonedWorkers(passCtx)
			cancel()
		}
	}
}

func (p *Processor) cleanupAbandonedWorkers(ctx context.Context) {
	deadWorkers, err := p.queue.GetAbandonedWorkerQueues(ctx, p.db)
	if err != nil {
		processorLogf("[Liveness] Error scanning for dead workers: %v\n", err)
		return
	}
	for _, deadWorkerID := range deadWorkers {
		if ctx.Err() != nil {
			return
		}
		if deadWorkerID == p.workerID {
			continue
		}
		claimed, err := p.queue.TryClaimWorkerRecoveryLock(ctx, deadWorkerID, 120*time.Second)
		if err != nil || !claimed {
			continue
		}
		if err := p.queue.RecoverAbandonedTasks(ctx, p.db, deadWorkerID); err != nil {
			processorLogf("[Liveness] Error recovering tasks for worker %s: %v\n", deadWorkerID, err)
		} else {
			p.queue.NotifyTaskAvailable(ctx, p.db)
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
		processorLogf("[RetryScheduler] begin transaction: %v\n", err)
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
			processorLogf("[RetryScheduler] re-enqueue tasks: %v\n", err)
			return
		}
		count, _ := result.RowsAffected()
		requeued += count
	}
	if err := tx.Commit(); err != nil {
		processorLogf("[RetryScheduler] commit re-enqueue: %v\n", err)
		return
	}
	if requeued > 0 {
		processorLogf("[RetryScheduler] Re-enqueued %d task(s)\n", requeued)
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
		processorLogf("[NotificationRepair] migration outbox repair: %v\n", err)
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
		processorLogf("[ProgressReconciler] DB query error: %v\n", err)
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
		processorLogf("[ProgressReconciler] rows error: %v\n", err)
		return
	}

	for _, id := range ids {
		requeued, err := db.MaybeRetryFailedMigrationTasks(p.db, ctx, id)
		if err != nil {
			processorLogf("[ProgressReconciler] error checking failed tasks retry for migration %s: %v\n", id, err)
		}
		if requeued {
			p.queue.NotifyTaskAvailable(ctx, p.db)
		}
		if err := db.ReconcileMigrationProgress(p.db, id); err != nil {
			processorLogf("[ProgressReconciler] error reconciling migration %s: %v\n", id, err)
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
		processorLogf("[ProgressReconciler] Sync DB query error: %v\n", err)
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
		processorLogf("[ProgressReconciler] Sync rows error: %v\n", err)
		return
	}

	for _, pass := range passes {
		if err := db.ReconcileSyncJobProgress(p.db, pass.id, pass.generation); err != nil {
			processorLogf("[ProgressReconciler] error reconciling sync job %s: %v\n", pass.id, err)
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
		UPDATE tasks t
		SET status = 'PENDING', worker_hash = NULL, updated_at = NOW()
		WHERE t.status = 'RUNNING'
		  AND t.updated_at < NOW() - INTERVAL '10 minutes'
		  AND (
			EXISTS (SELECT 1 FROM migrations m WHERE m.id = t.migration_id AND m.status IN ('RUNNING', 'INDEXING'))
			OR EXISTS (SELECT 1 FROM sync_jobs sj WHERE sj.id = t.sync_job_id AND sj.status IN ('RUNNING', 'INDEXING'))
		  )
	`
	result, err := p.db.ExecContext(ctx, query)
	if err != nil {
		processorLogf("[OrphanedTaskRecovery] DB update error: %v\n", err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		processorLogf("[OrphanedTaskRecovery] Cannot count reset tasks: %v\n", err)
		return
	}
	if count > 0 {
		processorLogf("[OrphanedTaskRecovery] Re-enqueued %d orphaned RUNNING tasks\n", count)
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
const recoveryScanWindow = 30

type migrationRecoveryCandidate struct {
	id, sourceURL, sourceUsername, sourcePassword, targetURL, targetUsername, targetPassword, sourceProvider, targetProvider string
	userID                                                                                                                   sql.NullString
}

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

func recoveryAttemptKey(syncJob bool, id string) string {
	if syncJob {
		return "sync:" + id
	}
	return "migration:" + id
}

func (p *Processor) recordRecoveryFailure(syncJob bool, id string, attempts int) {
	p.recoveryAttempts.Store(recoveryAttemptKey(syncJob, id), recoveryState{lastAttempt: time.Now(), attempts: attempts + 1})
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

// examineRecoveryCandidate records cursor progress only for candidates the
// probe loop reached. This prevents a scan window larger than the probe budget
// from permanently skipping the tail of each window.
func examineRecoveryCandidate(lastExamined *string, probes, budget int, id string) bool {
	if probes >= budget {
		return false
	}
	*lastExamined = id
	return true
}

func (p *Processor) recoverPausedMigrations(ctx context.Context) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`
	rows, err := p.db.QueryContext(ctx, query, p.recoveryCursor(false), recoveryScanWindow)
	if err != nil {
		return
	}
	var candidates []migrationRecoveryCandidate
	for rows.Next() {
		var candidate migrationRecoveryCandidate
		if err := rows.Scan(&candidate.id, &candidate.userID, &candidate.sourceURL, &candidate.sourceUsername, &candidate.sourcePassword, &candidate.targetURL, &candidate.targetUsername, &candidate.targetPassword, &candidate.sourceProvider, &candidate.targetProvider); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		processorLogf("[RecoveryScheduler] rows error: %v\n", err)
		return
	}
	if err := rows.Close(); err != nil {
		processorLogf("[RecoveryScheduler] close rows: %v\n", err)
		return
	}

	probes := 0
	lastExamined := ""
	for _, candidate := range candidates {
		if !examineRecoveryCandidate(&lastExamined, probes, maxMigrationRecoveryProbesPerTick, candidate.id) {
			break
		}
		id, sURL, sUser, sPassEnc := candidate.id, candidate.sourceURL, candidate.sourceUsername, candidate.sourcePassword
		tURL, tUser, tPassEnc, sProv, tProv, userID := candidate.targetURL, candidate.targetUsername, candidate.targetPassword, candidate.sourceProvider, candidate.targetProvider, candidate.userID

		var ra recoveryState
		if v, ok := p.recoveryAttempts.Load(recoveryAttemptKey(false, id)); ok {
			ra = v.(recoveryState)
		}
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++

		sPass, err := crypto.DecryptWithDomain(sPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(sProv)))
		if err != nil {
			p.recordRecoveryFailure(false, id, ra.attempts)
			continue
		}
		tPass, err := crypto.DecryptWithDomain(tPassEnc, p.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(tProv)))
		if err != nil {
			p.recordRecoveryFailure(false, id, ra.attempts)
			continue
		}

		userCtx := storage.WithLocalUserScope(ctx, userID.String)
		sClient, err := storage.NewProvider(userCtx, sProv, sURL, sUser, sPass)
		if err != nil {
			p.recordRecoveryFailure(false, id, ra.attempts)
			continue
		}
		tClient, err := storage.NewProvider(userCtx, tProv, tURL, tUser, tPass)
		if err != nil {
			sClient.Close()
			p.recordRecoveryFailure(false, id, ra.attempts)
			continue
		}

		connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
		sOK, _ := sClient.Connect(connCtx)
		tOK, _ := tClient.Connect(connCtx)
		connCancel()
		sClient.Close()
		tClient.Close()

		if sOK && tOK {
			processorLogf("[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", id)
			recovered, needsDiscovery, recoverErr := db.RecoverConnectionLostMigration(p.db, id)
			if recoverErr != nil {
				processorLogf("[RecoveryScheduler] Error resuming migration %s: %v\n", id, recoverErr)
				continue
			}
			if recovered {
				p.recoveryAttempts.Delete(recoveryAttemptKey(false, id))
				if needsDiscovery {
					// Recovery owns the execution transition, but incomplete discovery
					// must be restarted by a fenced indexer before reconciliation can
					// complete the migration.
					go p.indexer.Start(ctx, id)
				}
			} else {
				processorLogf("[RecoveryScheduler] Did not resume migration %s because its status changed", id)
			}
		} else {
			p.recordRecoveryFailure(false, id, ra.attempts)
		}
	}
	if lastExamined != "" {
		p.setRecoveryCursor(false, lastExamined)
	}
}
