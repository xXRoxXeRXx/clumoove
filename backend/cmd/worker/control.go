package main

import (
	"context"

	"backend/internal/processor"
	"backend/internal/queue"
)

func controlLoops(q *queue.Queue, proc *processor.Processor) []backgroundLoop {
	return []backgroundLoop{
		{
			Name: "migration_cancel_events",
			Run: func(ctx context.Context) error {
				q.SubscribeToCancelEvents(ctx, proc.CancelMigration)
				return nil
			},
		},
		{
			Name: "sync_cancel_events",
			Run: func(ctx context.Context) error {
				q.SubscribeToSyncCancelEvents(ctx, proc.CancelSyncJob)
				return nil
			},
		},
		{
			Name: "bandwidth_events",
			Run: func(ctx context.Context) error {
				q.SubscribeToBandwidthChanges(ctx, func(event queue.BandwidthEvent) {
					applyBandwidthEvent(proc, event)
				})
				return nil
			},
		},
	}
}

func bandwidthTarget(event queue.BandwidthEvent) (jobID, jobType string) {
	if event.SyncJobID != "" {
		return event.SyncJobID, "sync job"
	}
	return event.MigrationID, "migration"
}

func applyBandwidthEvent(proc *processor.Processor, event queue.BandwidthEvent) {
	jobID, jobType := bandwidthTarget(event)
	workerLogf("control", "Bandwidth change for %s %s: %d Mbps", jobType, jobID, event.BandwidthLimitMbps)
	proc.SetBandwidthLimit(jobID, event.BandwidthLimitMbps)
}
