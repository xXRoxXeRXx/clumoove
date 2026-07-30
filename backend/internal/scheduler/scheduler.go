package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/queue"
	"backend/internal/sync"
)

// Scheduler is the core daemon that manages scheduled tasks
type Scheduler struct {
	db         *sql.DB
	queue      *queue.Queue
	indexer    *indexer.Indexer
	syncEngine *sync.Engine
}

// SetSyncEngine registers the sync engine with the scheduler
func (s *Scheduler) SetSyncEngine(se *sync.Engine) {
	s.syncEngine = se
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
	log.Println("[Scheduler] Started. Checking for due schedules every minute...")
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Process immediately on startup to catch any overdue schedules
	s.processDueSchedules(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Scheduler] Shutting down.")
			return
		case <-ticker.C:
			s.processDueSchedules(ctx)
		}
	}
}

// processDueSchedules queries and processes all due schedules
func (s *Scheduler) processDueSchedules(ctx context.Context) {
	schedules, err := db.GetDueSchedules(s.db)
	if err != nil {
		log.Printf("[Scheduler] Error querying due schedules: %v", err)
		return
	}

	if len(schedules) == 0 {
		return
	}

	log.Printf("[Scheduler] Found %d due schedule(s) to process", len(schedules))

	for _, schedule := range schedules {
		// Distributed claim: only one API instance may process a given schedule.
		// The lock TTL (2 min) exceeds the 1-min tick so a crashed instance cannot
		// immediately re-trigger the same schedule, while a stale lock eventually expires.
		claimed, err := s.queue.TryClaimScheduleLock(ctx, schedule.ID, 2*time.Minute)
		if err != nil {
			log.Printf("[Scheduler] Error claiming lock for schedule %s: %v", schedule.ID, err)
			continue
		}
		if !claimed {
			log.Printf("[Scheduler] Schedule %s already claimed by another instance, skipping", schedule.ID)
			continue
		}
		s.processSchedule(ctx, &schedule)
	}
}

// processSchedule handles a single due schedule with overlap protection
func (s *Scheduler) processSchedule(ctx context.Context, schedule *db.Schedule) {
	log.Printf("[Scheduler] Processing schedule %s (type=%s, task_id=%s)",
		schedule.ID, schedule.TaskType, schedule.TaskID)

	// 1. Check overlap protection - skip if job is already running
	isActive, err := s.isJobActive(schedule.TaskType, schedule.TaskID)
	if err != nil {
		log.Printf("[Scheduler] Error checking job status for %s/%s: %v",
			schedule.TaskType, schedule.TaskID, err)
		return
	}

	if isActive {
		log.Printf("[Scheduler] Skipping schedule %s: job %s/%s is still running (overlap protection)",
			schedule.ID, schedule.TaskType, schedule.TaskID)
		// For recurring jobs, still update next_run_at even if skipped
		if schedule.CronExpression.Valid || schedule.TaskType == "sync" {
			nextRun, err := s.nextRunForSchedule(schedule)
			if err == nil {
				_ = db.UpdateNextRunAt(s.db, schedule.ID, nextRun)
			}
		}
		return
	}

	// 2. Trigger the job
	err = s.triggerJob(ctx, schedule)
	if err != nil {
		log.Printf("[Scheduler] Error triggering job for schedule %s: %v", schedule.ID, err)
		if errors.Is(err, sql.ErrNoRows) {
			if deactErr := db.DeactivateSchedule(s.db, schedule.ID); deactErr != nil {
				log.Printf("[Scheduler] Error deactivating schedule %s for missing job: %v", schedule.ID, deactErr)
			} else {
				log.Printf("[Scheduler] Deactivated schedule %s (linked %s %s no longer exists)", schedule.ID, schedule.TaskType, schedule.TaskID)
			}
			return
		}
		// For recurring jobs, DO NOT deactivate on transient trigger failure!
		// Advance next_run_at so it retries automatically on the next interval.
		if schedule.CronExpression.Valid || schedule.TaskType == "sync" {
			nextRun, nErr := s.nextRunForSchedule(schedule)
			if nErr == nil {
				_ = db.UpdateNextRunAt(s.db, schedule.ID, nextRun)
				log.Printf("[Scheduler] Recurring schedule %s trigger failed; next retry scheduled at %s",
					schedule.ID, nextRun.Format(time.RFC3339))
			}
		} else {
			// One-shot job: deactivate after trigger failure
			if deactErr := db.DeactivateSchedule(s.db, schedule.ID); deactErr != nil {
				log.Printf("[Scheduler] Error deactivating failed schedule %s: %v", schedule.ID, deactErr)
			} else {
				log.Printf("[Scheduler] Deactivated one-shot schedule %s after trigger failure", schedule.ID)
			}
		}
		return
	}

	log.Printf("[Scheduler] Successfully triggered job for schedule %s", schedule.ID)

	// 3. Update schedule lifecycle
	if schedule.CronExpression.Valid || schedule.TaskType == "sync" {
		// Recurring: calculate next run time
		nextRun, err := s.nextRunForSchedule(schedule)
		if err != nil {
			log.Printf("[Scheduler] Error calculating next run for schedule %s: %v",
				schedule.ID, err)
			return
		}
		err = db.UpdateNextRunAt(s.db, schedule.ID, nextRun)
		if err != nil {
			log.Printf("[Scheduler] Error updating next_run_at for schedule %s: %v",
				schedule.ID, err)
		} else {
			log.Printf("[Scheduler] Updated next_run_at for schedule %s to %s",
				schedule.ID, nextRun.Format(time.RFC3339))
		}
	} else {
		// One-shot: deactivate the schedule
		err = db.DeactivateSchedule(s.db, schedule.ID)
		if err != nil {
			log.Printf("[Scheduler] Error deactivating schedule %s: %v",
				schedule.ID, err)
		} else {
			log.Printf("[Scheduler] Deactivated one-shot schedule %s", schedule.ID)
		}
	}
}

// nextRunForSchedule calculates the next occurrence for a recurring schedule.
// Sync schedules use their persisted interval_minutes rather than cron because
// intervals above 59 minutes (for example 90 minutes) are not representable in
// cron's minute field. Checking task type first also repairs existing sync
// schedules that may still contain an old invalid cron expression.
func (s *Scheduler) nextRunForSchedule(schedule *db.Schedule) (time.Time, error) {
	if schedule.TaskType == "sync" {
		job, err := db.GetSyncJob(s.db, schedule.TaskID)
		if err != nil {
			return time.Time{}, fmt.Errorf("get sync job interval: %w", err)
		}
		return nextSyncRunAt(job.IntervalMinutes, time.Now())
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
func (s *Scheduler) isJobActive(taskType, taskID string) (bool, error) {
	switch taskType {
	case "migration":
		mig, err := db.GetMigration(s.db, taskID)
		if err != nil {
			if err == sql.ErrNoRows {
				return false, nil // Migration doesn't exist, not active
			}
			return false, err
		}
		// Keep migration overlap protection aligned with isJobActiveStatus.
		return isJobActiveStatus(mig.Status), nil

	case "sync":
		job, err := db.GetSyncJob(s.db, taskID)
		if err != nil {
			if err == sql.ErrNoRows {
				return false, nil
			}
			return false, err
		}
		return isJobActiveStatus(job.Status), nil

	case "backup":
		// Future: Check backup_jobs table when implemented
		// For now, return false to allow scheduling
		return false, nil

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
		return s.triggerBackup(ctx, schedule.TaskID)
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
	claimed, err := db.ClaimScheduledMigrationForIndexing(s.db, migrationID)
	if err != nil {
		return fmt.Errorf("claim scheduled migration %s: %w", migrationID, err)
	}
	if !claimed {
		// This is intentionally an error: processSchedule deactivates a one-shot
		// schedule after an invalid claim, including a user cancellation or delete.
		return fmt.Errorf("migration %s is no longer scheduled", migrationID)
	}

	// Delegate to the shared indexer in a goroutine. The migration is already
	// INDEXING because the CAS above succeeded; on failure the indexer marks it
	// FAILED internally. Spawning asynchronously prevents blocking the scheduler
	// loop (indexing can take up to 20 minutes).
	go s.indexer.Start(ctx, migrationID)
	log.Printf("[Scheduler] Migration %s indexing started", migrationID)
	return nil
}

// triggerSync triggers a sync pass for a scheduled sync job.
func (s *Scheduler) triggerSync(ctx context.Context, syncJobID string) error {
	if s.syncEngine == nil {
		return fmt.Errorf("sync engine not initialized in scheduler")
	}

	job, err := db.GetSyncJob(s.db, syncJobID)
	if err != nil {
		return fmt.Errorf("failed to fetch sync job %s: %w", syncJobID, err)
	}

	// Skip an already active pass without deactivating the recurring schedule.
	// The conditional UPDATE below closes the race after this status read.
	if isJobActiveStatus(job.Status) {
		log.Printf("[Scheduler] Skipping sync job %s trigger: job is already %s (overlap protection)", syncJobID, job.Status)
		return nil
	}

	if job.Status != "IDLE" && job.Status != "FAILED" {
		return fmt.Errorf("sync job %s is in a non-runnable state (current: %s)", syncJobID, job.Status)
	}

	claimed, err := s.syncEngine.StartSyncPass(ctx, syncJobID)
	if err != nil {
		return fmt.Errorf("failed to claim sync job %s: %w", syncJobID, err)
	}
	if !claimed {
		// A competing API/scheduler trigger claimed it between the status read
		// above and this atomic update. Skip without deactivating the schedule.
		log.Printf("[Scheduler] Skipping sync job %s trigger: pass was claimed by another starter", syncJobID)
		return nil
	}

	log.Printf("[Scheduler] Sync job %s pass started", syncJobID)
	return nil
}

// triggerBackup is a placeholder for future backup job implementation
func (s *Scheduler) triggerBackup(ctx context.Context, backupJobID string) error {
	// Future: Implement backup job triggering
	log.Printf("[Scheduler] Backup job triggering not yet implemented (job_id=%s)", backupJobID)
	return nil
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
	if s.queue != nil {
		claimed, err := s.queue.TryClaimOrphanedSyncRecoveryLock(ctx, 4*time.Minute)
		if err != nil {
			log.Printf("[OrphanedSyncJobRecovery] Error claiming recovery lock: %v", err)
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
		log.Printf("[OrphanedSyncJobRecovery] DB query error: %v", err)
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
			log.Printf("[OrphanedSyncJobRecovery] Scan error: %v", err)
			continue
		}
		recovered = append(recovered, recoveredJob{id: id, userID: userID.String})
	}
	if err := rows.Err(); err != nil {
		log.Printf("[OrphanedSyncJobRecovery] rows error: %v", err)
		return
	}

	for _, job := range recovered {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE schedules SET next_run_at = NOW() WHERE task_type = 'sync' AND task_id = $1 AND is_active = TRUE`,
			job.id); err != nil {
			log.Printf("[OrphanedSyncJobRecovery] Error advancing schedule for job %s: %v", job.id, err)
		}
		db.WriteAuditLog(s.db, db.AuditEntry{
			UserID: sql.NullString{String: job.userID, Valid: job.userID != ""},
			Action: db.AuditSyncRecovered,
			Target: job.id,
		})
		log.Printf("[OrphanedSyncJobRecovery] Recovered orphaned sync job %s (was stuck in INDEXING/RUNNING)", job.id)
	}
}
