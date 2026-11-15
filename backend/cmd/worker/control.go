package main

import (
	"context"

	"backend/internal/queue"
)

type controlSubscriber interface {
	SubscribeToCancelEvents(ctx context.Context, callback func(migrationID string))
	SubscribeToSyncCancelEvents(ctx context.Context, callback func(event queue.SyncCancelEvent))
	SubscribeToBandwidthChanges(ctx context.Context, callback func(event queue.BandwidthEvent))
}

type transferControl interface {
	CancelMigration(migrationID string)
	CancelSyncJob(syncJobID string, passGeneration int, allGenerations bool)
	SetBandwidthLimit(jobID string, mbps int)
}

func controlLoops(workerID string, sub controlSubscriber, ctrl transferControl) []backgroundLoop {
	return []backgroundLoop{
		{
			Name: "migration_cancel_events",
			Run: func(ctx context.Context) error {
				sub.SubscribeToCancelEvents(ctx, ctrl.CancelMigration)
				return nil
			},
		},
		{
			Name: "sync_cancel_events",
			Run: func(ctx context.Context) error {
				sub.SubscribeToSyncCancelEvents(ctx, func(event queue.SyncCancelEvent) {
					ctrl.CancelSyncJob(event.SyncJobID, event.PassGeneration, event.AllGenerations)
				})
				return nil
			},
		},
		{
			Name: "bandwidth_events",
			Run: func(ctx context.Context) error {
				sub.SubscribeToBandwidthChanges(ctx, func(event queue.BandwidthEvent) {
					jobID := event.MigrationID
					jobType := "migration"
					if event.SyncJobID != "" {
						jobID = event.SyncJobID
						jobType = "sync job"
					}
					workerLogf("control", "[Worker %s] Bandwidth change for %s %s: %d Mbps",
						workerID, jobType, jobID, event.BandwidthLimitMbps)
					ctrl.SetBandwidthLimit(jobID, event.BandwidthLimitMbps)
				})
				return nil
			},
		},
	}
}
