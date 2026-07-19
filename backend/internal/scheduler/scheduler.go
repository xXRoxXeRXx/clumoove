package scheduler

import (
	"context"
	"database/sql"
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
		if schedule.CronExpression.Valid {
			nextRun, err := NextRun(schedule.CronExpression.String)
			if err == nil {
				_ = db.UpdateNextRunAt(s.db, schedule.ID, nextRun)
			}
		}
		return
	}

	// 2. Trigger the job
	err = s.triggerJob(ctx, schedule)
	if err != nil {
		log.Printf("[Scheduler] Error triggering job for schedule %s: %v",
			schedule.ID, err)
		// Deactivate the schedule to prevent an infinite retry loop (e.g. the
		// linked task was deleted or is in an invalid state). The user can
		// re-create the schedule via the API if needed.
		if deactErr := db.DeactivateSchedule(s.db, schedule.ID); deactErr != nil {
			log.Printf("[Scheduler] Error deactivating failed schedule %s: %v",
				schedule.ID, deactErr)
		} else {
			log.Printf("[Scheduler] Deactivated schedule %s after trigger failure", schedule.ID)
		}
		return
	}

	log.Printf("[Scheduler] Successfully triggered job for schedule %s", schedule.ID)

	// 3. Update schedule lifecycle
	if schedule.CronExpression.Valid {
		// Recurring: calculate next run time
		nextRun, err := NextRun(schedule.CronExpression.String)
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

// isJobActiveStatus reports whether a job with the given status is considered
// still running for overlap-protection purposes. A job is "active" while it is
// RUNNING or INDEXING; any other state (PENDING, SCHEDULED, COMPLETED, FAILED,
// PAUSED_CONNECTION_LOSS) means a new trigger is allowed.
func isJobActiveStatus(status string) bool {
	return status == "RUNNING" || status == "INDEXING"
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
		// Migration is active if it's in RUNNING or INDEXING state
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
	// Fetch migration to verify it exists and is in SCHEDULED state
	mig, err := db.GetMigration(s.db, migrationID)
	if err != nil {
		return fmt.Errorf("failed to fetch migration %s: %w", migrationID, err)
	}

	// Only trigger if migration is in SCHEDULED state
	if mig.Status != "SCHEDULED" {
		return fmt.Errorf("migration %s is not in SCHEDULED state (current: %s)", migrationID, mig.Status)
	}

	// Delegate to the shared indexer in a goroutine. It transitions the migration
	// to INDEXING, walks the persisted selected paths, and creates PENDING tasks.
	// On failure it marks the migration FAILED internally. Spawning asynchronously
	// prevents blocking the scheduler loop (indexing can take up to 20 minutes).
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

	// PAUSED_CONNECTION_LOSS is a transient state managed by the recovery scheduler.
	// Returning an error here would permanently deactivate the schedule, so we
	// skip this trigger silently and let the scheduler advance next_run_at normally.
	if job.Status == "PAUSED_CONNECTION_LOSS" {
		log.Printf("[Scheduler] Skipping sync job %s trigger: job is in PAUSED_CONNECTION_LOSS (recovery pending)", syncJobID)
		return nil
	}

	if job.Status != "IDLE" {
		return fmt.Errorf("sync job %s is not in IDLE state (current: %s)", syncJobID, job.Status)
	}

	go s.syncEngine.RunSyncPass(ctx, syncJobID)
	log.Printf("[Scheduler] Sync job %s pass started", syncJobID)
	return nil
}

// triggerBackup is a placeholder for future backup job implementation
func (s *Scheduler) triggerBackup(ctx context.Context, backupJobID string) error {
	// Future: Implement backup job triggering
	log.Printf("[Scheduler] Backup job triggering not yet implemented (job_id=%s)", backupJobID)
	return nil
}
