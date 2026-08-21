package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/observability"
	"backend/internal/queue"
	"backend/internal/sync"
)

// Scheduler is the core daemon that manages scheduled tasks
type Scheduler struct {
	db         *sql.DB
	queue      *queue.Queue
	indexer    *indexer.Indexer
	syncEngine atomic.Pointer[sync.Engine]
}

// SetSyncEngine registers the sync engine with the scheduler
func (s *Scheduler) SetSyncEngine(se *sync.Engine) {
	s.syncEngine.Store(se)
}

func schedulerLogger(ctx context.Context) *slog.Logger {
	return observability.Logger(ctx).With(slog.String("component", "scheduler"))
}

// NewScheduler creates a new Scheduler instance
func NewScheduler(database *sql.DB, q *queue.Queue, idx *indexer.Indexer) *Scheduler {
	return &Scheduler{
		db:      database,
		queue:   q,
		indexer: idx,
	}
}

// Run starts the scheduler daemon that checks for due schedules every minute
func (s *Scheduler) Run(ctx context.Context) {
	schedulerLogger(ctx).InfoContext(ctx, "scheduler_started", slog.Duration("interval", time.Minute))
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Process immediately on startup to catch any overdue schedules
	s.processDueSchedules(ctx)

	for {
		select {
		case <-ctx.Done():
			schedulerLogger(ctx).Info("scheduler_stopped")
			return
		case <-ticker.C:
			s.processDueSchedules(ctx)
		}
	}
}

// processDueSchedules queries and processes all due schedules
func (s *Scheduler) processDueSchedules(ctx context.Context) {
	logger := schedulerLogger(ctx)
	schedules, err := db.GetDueSchedulesContext(ctx, s.db)
	if err != nil {
		logger.ErrorContext(ctx, "due_schedule_query_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}

	if len(schedules) == 0 {
		return
	}

	logger.InfoContext(ctx, "due_schedules_found", slog.Int("count", len(schedules)))

	for _, schedule := range schedules {
		// Distributed claim: only one API instance may process a given schedule.
		// The lock TTL (2 min) exceeds the 1-min tick so a crashed instance cannot
		// immediately re-trigger the same schedule, while a stale lock eventually expires.
		claimed, err := s.queue.TryClaimScheduleLock(ctx, schedule.ID, 2*time.Minute)
		if err != nil {
			logger.ErrorContext(ctx, "schedule_lock_claim_failed", slog.String("schedule_id", schedule.ID), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		if !claimed {
			logger.DebugContext(ctx, "schedule_lock_unavailable", slog.String("schedule_id", schedule.ID))
			continue
		}
		s.processSchedule(ctx, &schedule)
	}
}

// processSchedule handles a single due schedule with overlap protection
func (s *Scheduler) processSchedule(ctx context.Context, schedule *db.Schedule) {
	logger := schedulerLogger(ctx).With(slog.String("schedule_id", schedule.ID), slog.String("task_type", schedule.TaskType), slog.String("task_id", schedule.TaskID))
	logger.InfoContext(ctx, "schedule_processing")

	// 1. Check overlap protection - skip if job is already running
	isActive, err := s.isJobActive(ctx, schedule.TaskType, schedule.TaskID)
	if err != nil {
		logger.ErrorContext(ctx, "schedule_job_status_check_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}

	if isActive {
		logger.InfoContext(ctx, "schedule_overlap_skipped")
		// For recurring jobs, still update next_run_at even if skipped
		if schedule.CronExpression.Valid || schedule.TaskType == "sync" || schedule.TaskType == "backup" {
			nextRun, err := s.nextRunForSchedule(ctx, schedule)
			if err == nil {
				if err := db.UpdateNextRunAtContext(ctx, s.db, schedule.ID, nextRun); err != nil {
					logger.ErrorContext(ctx, "schedule_next_run_update_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				}
			} else {
				logger.ErrorContext(ctx, "schedule_next_run_calculation_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			}
		} else if err := db.DeactivateScheduleContext(ctx, s.db, schedule.ID); err != nil {
			logger.ErrorContext(ctx, "one_shot_overlap_deactivation_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		} else {
			logger.InfoContext(ctx, "one_shot_overlap_deactivated")
		}
		return
	}

	// 2. Trigger the job
	err = s.triggerJob(ctx, schedule)
	if err != nil {
		logger.ErrorContext(ctx, "schedule_trigger_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)), slog.Bool("linked_job_missing", errors.Is(err, sql.ErrNoRows)))
		// A trigger failure is not safe to retry blindly: the linked job may have
		// been deleted or moved to a non-runnable state. Deactivate every schedule
		// type so an operator can make the recovery decision explicitly.
		if deactErr := db.DeactivateScheduleContext(ctx, s.db, schedule.ID); deactErr != nil {
			logger.ErrorContext(ctx, "failed_schedule_deactivation_failed", observability.Error(deactErr), slog.String("error_kind", observability.ErrorKind(deactErr)))
		} else {
			logger.InfoContext(ctx, "failed_schedule_deactivated")
		}
		return
	}

	logger.InfoContext(ctx, "schedule_triggered")

	// 3. Update schedule lifecycle
	if schedule.CronExpression.Valid || schedule.TaskType == "sync" || schedule.TaskType == "backup" {
		// Recurring: calculate next run time
		nextRun, err := s.nextRunForSchedule(ctx, schedule)
		if err != nil {
			logger.ErrorContext(ctx, "schedule_next_run_calculation_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			return
		}
		err = db.UpdateNextRunAtContext(ctx, s.db, schedule.ID, nextRun)
		if err != nil {
			logger.ErrorContext(ctx, "schedule_next_run_update_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		} else {
			logger.InfoContext(ctx, "schedule_next_run_updated", slog.Time("next_run_at", nextRun))
		}
	} else {
		// One-shot: deactivate the schedule
		err = db.DeactivateScheduleContext(ctx, s.db, schedule.ID)
		if err != nil {
			logger.ErrorContext(ctx, "one_shot_schedule_deactivation_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		} else {
			logger.InfoContext(ctx, "one_shot_schedule_deactivated")
		}
	}
}

// nextRunForSchedule calculates the next occurrence for a recurring schedule.
// Sync schedules use their persisted interval_minutes rather than cron because
// intervals above 59 minutes (for example 90 minutes) are not representable in
// cron's minute field. Checking task type first also repairs existing sync
// schedules that may still contain an old invalid cron expression.
func (s *Scheduler) nextRunForSchedule(ctx context.Context, schedule *db.Schedule) (time.Time, error) {
	if schedule.TaskType == "sync" {
		job, err := db.GetSyncJobContext(ctx, s.db, schedule.TaskID)
		if err != nil {
			return time.Time{}, fmt.Errorf("get sync job interval: %w", err)
		}
		return nextSyncRunAt(job.IntervalMinutes, time.Now())
	}
	if schedule.TaskType == "backup" {
		job, err := db.GetBackupScheduleInfoContext(ctx, s.db, schedule.TaskID)
		if err != nil {
			return time.Time{}, fmt.Errorf("get backup job schedule: %w", err)
		}
		return NextRunInLocation(job.CronExpression, job.Timezone, time.Now())
	}
	if !schedule.CronExpression.Valid {
		return time.Time{}, fmt.Errorf("schedule %s has no recurrence", schedule.ID)
	}
	return NextRun(schedule.CronExpression.String)
}

func nextSyncRunAt(intervalMinutes int, from time.Time) (time.Time, error) {
	if intervalMinutes <= 0 {
		return time.Time{}, fmt.Errorf("invalid sync interval: %d minutes", intervalMinutes)
	}
	return from.Add(time.Duration(intervalMinutes) * time.Minute), nil
}

// isJobActiveStatus reports whether a job with the given status is considered
// still running for overlap-protection purposes. A job is "active" while it is
// RUNNING, INDEXING, VERIFYING, or PAUSED_CONNECTION_LOSS. VERIFYING owns the
// completion of the current pass, while PAUSED_CONNECTION_LOSS must remain
// reserved for the connection-recovery scheduler; neither may be overlapped.
func isJobActiveStatus(status string) bool {
	return status == "RUNNING" ||
		status == "INDEXING" ||
		status == "VERIFYING" ||
		status == "PAUSED_CONNECTION_LOSS"
}

// isJobActive checks if the linked job is currently running (overlap protection)
func (s *Scheduler) isJobActive(ctx context.Context, taskType, taskID string) (bool, error) {
	switch taskType {
	case "migration":
		mig, err := db.GetMigrationContext(ctx, s.db, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil // Migration doesn't exist, not active
			}
			return false, err
		}
		// Keep migration overlap protection aligned with isJobActiveStatus.
		return isJobActiveStatus(mig.Status), nil

	case "sync":
		job, err := db.GetSyncJobContext(ctx, s.db, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		return isJobActiveStatus(job.Status), nil

	case "backup":
		job, err := db.GetBackupScheduleInfoContext(ctx, s.db, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		return job.Status == "QUEUED" || job.Status == "SCANNING" || job.Status == "RUNNING" || job.Status == "VERIFYING", nil

	default:
		return false, fmt.Errorf("unknown task type: %s", taskType)
	}
}

// triggerJob starts the appropriate job based on task type
func (s *Scheduler) triggerJob(ctx context.Context, schedule *db.Schedule) error {
	switch schedule.TaskType {
	case "migration":
		return s.triggerMigration(ctx, schedule.TaskID)
	case "sync":
		return s.triggerSync(ctx, schedule.TaskID)
	case "backup":
		return s.triggerBackup(ctx, schedule)
	default:
		return fmt.Errorf("unknown task type: %s", schedule.TaskType)
	}
}

// triggerMigration starts the indexing phase for a scheduled migration.
// It verifies the migration is in SCHEDULED state, then delegates to the shared
// indexer which reads the persisted selected paths/calendars/contacts, decrypts
// credentials at the last moment, and creates PENDING tasks.
// The indexer is spawned in a goroutine to avoid blocking the scheduler loop
// (indexing can take up to 20 minutes for large migrations).
func (s *Scheduler) triggerMigration(ctx context.Context, migrationID string) error {
	// Atomically claim the migration before starting the asynchronous indexer.
	// A prior status read cannot prevent a competing cancel/trigger from changing
	// the row before this point.
	claimed, err := db.ClaimScheduledMigrationForIndexingContext(ctx, s.db, migrationID)
	if err != nil {
		return fmt.Errorf("claim scheduled migration %s: %w", migrationID, err)
	}
	if !claimed {
		// This is intentionally an error: processSchedule deactivates a one-shot
		// schedule after an invalid claim, including a user cancellation or delete.
		return fmt.Errorf("migration %s is no longer scheduled: %w", migrationID, sql.ErrNoRows)
	}

	// Delegate to the shared indexer in a goroutine. The migration is already
	// INDEXING because the CAS above succeeded; on failure the indexer marks it
	// FAILED internally. Spawning asynchronously prevents blocking the scheduler
	// loop (indexing can take up to 20 minutes).
	go s.indexer.Start(ctx, migrationID)
	schedulerLogger(ctx).InfoContext(ctx, "migration_indexing_started", slog.String("migration_id", migrationID))
	return nil
}

// triggerSync triggers a sync pass for a scheduled sync job.
func (s *Scheduler) triggerSync(ctx context.Context, syncJobID string) error {
	syncEngine := s.syncEngine.Load()
	if syncEngine == nil {
		return fmt.Errorf("sync engine not initialized in scheduler")
	}

	job, err := db.GetSyncJobContext(ctx, s.db, syncJobID)
	if err != nil {
		return fmt.Errorf("failed to fetch sync job %s: %w", syncJobID, err)
	}

	// Skip an already active pass without deactivating the recurring schedule.
	// The conditional UPDATE below closes the race after this status read.
	if isJobActiveStatus(job.Status) {
		schedulerLogger(ctx).InfoContext(ctx, "sync_trigger_overlap_skipped", slog.String("sync_job_id", syncJobID), slog.String("status", job.Status))
		return nil
	}

	if job.Status != "IDLE" && job.Status != "FAILED" {
		return fmt.Errorf("sync job %s is in a non-runnable state (current: %s)", syncJobID, job.Status)
	}

	claimed, err := syncEngine.StartSyncPass(ctx, syncJobID)
	if err != nil {
		return fmt.Errorf("failed to claim sync job %s: %w", syncJobID, err)
	}
	if !claimed {
		// A competing API/scheduler trigger claimed it between the status read
		// above and this atomic update. Skip without deactivating the schedule.
		schedulerLogger(ctx).InfoContext(ctx, "sync_trigger_claim_unavailable", slog.String("sync_job_id", syncJobID))
		return nil
	}

	schedulerLogger(ctx).InfoContext(ctx, "sync_pass_started", slog.String("sync_job_id", syncJobID))
	return nil
}

// triggerBackup atomically queues a backup run. Execution is intentionally
// worker-owned so the API scheduler never handles decrypted credentials.
func (s *Scheduler) triggerBackup(ctx context.Context, schedule *db.Schedule) error {
	job, err := db.GetBackupScheduleInfoContext(ctx, s.db, schedule.TaskID)
	if err != nil {
		return fmt.Errorf("get backup job %s: %w", schedule.TaskID, err)
	}
	if job.Status == "PAUSED" || job.Status == "DELETING" {
		return fmt.Errorf("backup job %s is administratively blocked", schedule.TaskID)
	}

	dueAt := time.Now().UTC()
	if schedule.NextRunAt.Valid {
		dueAt = schedule.NextRunAt.Time.UTC()
	}
	location, err := time.LoadLocation(job.Timezone)
	if err != nil {
		return fmt.Errorf("load backup timezone: %w", err)
	}
	localKey := dueAt.In(location).Format("2006-01-02T15:04")
	claim, err := db.ClaimBackupJobPassContext(ctx, s.db, schedule.TaskID, "schedule", &localKey)
	if err != nil {
		return fmt.Errorf("claim backup job %s: %w", schedule.TaskID, err)
	}
	switch claim.Outcome {
	case db.BackupClaimed, db.BackupClaimOverlap, db.BackupClaimDuplicate:
		return nil
	case db.BackupClaimBlocked:
		return fmt.Errorf("backup job %s is administratively blocked", schedule.TaskID)
	default:
		return fmt.Errorf("unknown backup claim outcome %q", claim.Outcome)
	}
}

// RunOrphanedSyncJobRecovery periodically frees sync jobs whose API-side
// coordinator goroutine died (deploy, crash, OOM) while status remained
// INDEXING/RUNNING. Overlap protection then skips every future trigger and the
// job wedges permanently — workers cannot rescue a stuck parent status.
//
// Recovery resets eligible jobs to IDLE, records a recovery error_message, and
// advances the linked active schedule so the next scheduler tick re-triggers.
func (s *Scheduler) RunOrphanedSyncJobRecovery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run once shortly after startup so jobs orphaned by a restart that happened
	// while passes were in flight are freed without waiting for the first tick.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		s.recoverOrphanedSyncJobs(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverOrphanedSyncJobs(ctx)
		}
	}
}

// recoverOrphanedSyncJobs resets sync jobs that look truly orphaned.
//
// Safety rules (avoid killing a healthy live pass):
//   - INDEXING: job.updated_at stale > 30m (listing should finish well under that;
//     indexCtx also hard-caps listing at 30m and fails cleanly).
//   - RUNNING: job.updated_at stale > 30m AND no non-terminal task with
//     task.updated_at within the last 10m (worker progress refreshes both task
//     and job updated_at; a live transfer must not be reset).
//   - Multi-instance: Redis SET NX lock so only one API replica runs recovery.
func (s *Scheduler) recoverOrphanedSyncJobs(ctx context.Context) {
	logger := schedulerLogger(ctx).With(slog.String("recovery_type", "orphaned_sync"))
	if s.queue != nil {
		claimed, err := s.queue.TryClaimOrphanedSyncRecoveryLock(ctx, 4*time.Minute)
		if err != nil {
			logger.ErrorContext(ctx, "recovery_lock_claim_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			return
		}
		if !claimed {
			return
		}
	}

	const recoveryMsg = "Recovered from stale INDEXING/RUNNING (coordinator lost)"

	// (INDEXING AND stale) OR (RUNNING AND stale AND no fresh open work).
	// Open work matches the engine poll predicate: PENDING/RUNNING, or FAILED
	// awaiting retry (next_retry_at set).
	query := `
		UPDATE sync_jobs sj
		SET status = 'IDLE',
		    error_message = $1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE (
		        sj.status = 'INDEXING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		  )
		   OR (
		        sj.status = 'RUNNING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		    AND NOT EXISTS (
		        SELECT 1 FROM tasks t
		        WHERE t.sync_job_id = sj.id
		          AND (
		                t.status IN ('PENDING', 'RUNNING')
		             OR (t.status = 'FAILED' AND t.next_retry_at IS NOT NULL)
		          )
		          AND t.updated_at > NOW() - INTERVAL '10 minutes'
		    )
		  )
		RETURNING sj.id, sj.user_id
	`

	rows, err := s.db.QueryContext(ctx, query, recoveryMsg)
	if err != nil {
		logger.ErrorContext(ctx, "orphaned_sync_query_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	defer rows.Close()

	type recoveredJob struct {
		id     string
		userID string
	}
	var recovered []recoveredJob
	for rows.Next() {
		var id string
		var userID sql.NullString
		if err := rows.Scan(&id, &userID); err != nil {
			logger.ErrorContext(ctx, "orphaned_sync_scan_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		recovered = append(recovered, recoveredJob{id: id, userID: userID.String})
	}
	if err := rows.Err(); err != nil {
		logger.ErrorContext(ctx, "orphaned_sync_rows_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}

	for _, job := range recovered {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE schedules SET next_run_at = NOW() WHERE task_type = 'sync' AND task_id = $1 AND is_active = TRUE`,
			job.id); err != nil {
			logger.ErrorContext(ctx, "orphaned_sync_schedule_advance_failed", slog.String("sync_job_id", job.id), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		}
		db.WriteAuditLogContext(ctx, s.db, db.AuditEntry{
			UserID: sql.NullString{String: job.userID, Valid: job.userID != ""},
			Action: db.AuditSyncRecovered,
			Target: job.id,
		})
		logger.InfoContext(ctx, "orphaned_sync_recovered", slog.String("sync_job_id", job.id))
	}
}

// RunOrphanedMigrationIndexingRecovery recovers migrations left in INDEXING
// when the API process hosting the indexer exits unexpectedly. Scheduled
// migrations are returned to SCHEDULED and their schedule is made due again;
// unscheduled migrations are failed visibly because there is no safe automatic
// trigger for them.
func (s *Scheduler) RunOrphanedMigrationIndexingRecovery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
		s.recoverOrphanedMigrationIndexing(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverOrphanedMigrationIndexing(ctx)
		}
	}
}

func (s *Scheduler) recoverOrphanedMigrationIndexing(ctx context.Context) {
	logger := schedulerLogger(ctx).With(slog.String("recovery_type", "orphaned_migration_indexing"))
	if s.queue != nil {
		claimed, err := s.queue.TryClaimOrphanedMigrationRecoveryLock(ctx, 4*time.Minute)
		if err != nil {
			logger.ErrorContext(ctx, "recovery_lock_claim_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			return
		}
		if !claimed {
			return
		}
	}

	const recoveryMsg = "Recovered from stale INDEXING (indexer lost)"
	s.recoverScheduledMigrations(ctx, logger, recoveryMsg)
	s.failOrphanedImmediateMigrations(ctx, logger, recoveryMsg)
}

// recoverScheduledMigrations makes stale scheduled migrations due again. A
// migration is transitioned first; its linked schedules are then reactivated
// before the next scheduler tick can claim them.
func (s *Scheduler) recoverScheduledMigrations(ctx context.Context, logger *slog.Logger, recoveryMsg string) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE migrations m
		SET status = 'SCHEDULED', error_message = $1, updated_at = CURRENT_TIMESTAMP
		WHERE m.status = 'INDEXING'
		  AND m.updated_at < NOW() - INTERVAL '30 minutes'
		  AND EXISTS (
			SELECT 1 FROM schedules s
			WHERE s.task_type = 'migration' AND s.task_id = m.id
		)
		RETURNING m.id, m.user_id
	`, recoveryMsg)
	if err != nil {
		logger.ErrorContext(ctx, "orphaned_migration_query_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	defer rows.Close()

	type recoveredMigration struct{ id, userID string }
	var scheduled []recoveredMigration
	for rows.Next() {
		var migrationID string
		var userID sql.NullString
		if err := rows.Scan(&migrationID, &userID); err != nil {
			logger.ErrorContext(ctx, "orphaned_migration_scan_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		scheduled = append(scheduled, recoveredMigration{id: migrationID, userID: userID.String})
	}
	if err := rows.Err(); err != nil {
		logger.ErrorContext(ctx, "orphaned_migration_rows_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}

	for _, migration := range scheduled {
		if _, err := s.db.ExecContext(ctx, `
			UPDATE schedules SET is_active = TRUE, next_run_at = NOW(), updated_at = CURRENT_TIMESTAMP
			WHERE task_type = 'migration' AND task_id = $1
		`, migration.id); err != nil {
			logger.ErrorContext(ctx, "orphaned_migration_schedule_reactivation_failed", slog.String("migration_id", migration.id), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		db.WriteAuditLogContext(ctx, s.db, db.AuditEntry{UserID: sql.NullString{String: migration.userID, Valid: migration.userID != ""}, Action: db.AuditMigrationRecovered, Target: migration.id})
		logger.InfoContext(ctx, "orphaned_migration_rescheduled", slog.String("migration_id", migration.id))
	}
}

// failOrphanedImmediateMigrations makes stale immediate migrations visible to
// their owner. Without a schedule there is no safe automatic restart path.
func (s *Scheduler) failOrphanedImmediateMigrations(ctx context.Context, logger *slog.Logger, recoveryMsg string) {
	unscheduledRows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.user_id FROM migrations m
		WHERE m.status = 'INDEXING' AND m.updated_at < NOW() - INTERVAL '30 minutes'
		  AND NOT EXISTS (SELECT 1 FROM schedules s WHERE s.task_type = 'migration' AND s.task_id = m.id)
	`)
	if err != nil {
		logger.ErrorContext(ctx, "orphaned_unscheduled_migration_query_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	defer unscheduledRows.Close()
	for unscheduledRows.Next() {
		var migrationID string
		var userID sql.NullString
		if err := unscheduledRows.Scan(&migrationID, &userID); err != nil {
			logger.ErrorContext(ctx, "orphaned_unscheduled_migration_scan_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		failed, err := db.FailStaleIndexingMigration(ctx, s.db, migrationID, stringPtr(recoveryMsg))
		if err != nil {
			logger.ErrorContext(ctx, "orphaned_unscheduled_migration_failure_persist_failed", slog.String("migration_id", migrationID), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
			continue
		}
		if failed {
			db.WriteAuditLogContext(ctx, s.db, db.AuditEntry{UserID: userID, Action: db.AuditMigrationRecovered, Target: migrationID})
			logger.InfoContext(ctx, "orphaned_unscheduled_migration_failed", slog.String("migration_id", migrationID))
		}
	}
	if err := unscheduledRows.Err(); err != nil {
		logger.ErrorContext(ctx, "orphaned_unscheduled_migration_rows_failed", observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
	}
}

func stringPtr(value string) *string { return &value }
