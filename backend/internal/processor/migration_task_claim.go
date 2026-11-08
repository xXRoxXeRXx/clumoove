package processor

import (
	"context"
	"fmt"

	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type claimedMigrationTask struct {
	migration *db.Migration
	task      *db.Task
	throttler *throttle.MigrationThrottler
}

// claimMigrationTask rechecks the migration lifecycle after SQL dequeue and
// applies the matching fenced task transition before any provider is opened.
// handled reports that the task was deliberately requeued, skipped, or
// cancelled and therefore must not enter the transfer stage.
func (p *Processor) claimMigrationTask(ctx context.Context, payload *queue.Payload) (context.Context, *claimedMigrationTask, bool, error) {
	mig, err := db.GetMigration(p.db, payload.MigrationID)
	if err != nil {
		return ctx, nil, false, fmt.Errorf("failed to fetch migration: %w", err)
	}
	throttler, _ := p.throttlers.LoadOrStore(payload.MigrationID, throttle.NewMigrationThrottler(mig.BandwidthLimitMbps))
	migrationThrottler := throttler.(*throttle.MigrationThrottler)

	if mig.Status == "PAUSED_CONNECTION_LOSS" || mig.Status == "PAUSED" {
		if err := db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "PENDING"); err != nil {
			processorLogf("[Worker %s] Failed to requeue paused task %s: %v", p.workerID, payload.TaskID, err)
		}
		return ctx, nil, true, nil
	}

	ctx = storage.WithLocalUserScope(ctx, mig.UserID.String)
	if mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED" {
		if err := db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "SKIPPED"); err != nil {
			processorLogf("[Worker %s] Failed to skip terminal migration task %s: %v", p.workerID, payload.TaskID, err)
		}
		return ctx, nil, true, nil
	}
	if mig.Status == "CANCELLED" {
		if err := db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "CANCELLED"); err != nil {
			processorLogf("[Worker %s] Failed to cancel task %s: %v", p.workerID, payload.TaskID, err)
		}
		return ctx, nil, true, nil
	}
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		if err := db.TransitionClaimedTask(p.db, ctx, payload.TaskID, payload.ClaimEpoch, "PENDING"); err != nil {
			processorLogf("[Worker %s] Failed to requeue task %s in migration state %s: %v", p.workerID, payload.TaskID, mig.Status, err)
		}
		return ctx, nil, false, fmt.Errorf("migration is in state %s, task skipped for now", mig.Status)
	}

	task, err := db.GetTask(p.db, payload.TaskID)
	if err != nil {
		return ctx, nil, false, fmt.Errorf("failed to fetch task: %w", err)
	}
	task.ClaimEpoch = payload.ClaimEpoch
	return ctx, &claimedMigrationTask{migration: mig, task: task, throttler: migrationThrottler}, false, nil
}
