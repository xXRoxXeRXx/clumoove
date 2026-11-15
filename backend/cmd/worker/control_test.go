package main

import (
	"context"
	"sort"
	"testing"

	"backend/internal/queue"
)

type stubControlSubscriber struct {
	migrationIDs    []string
	syncEvents      []queue.SyncCancelEvent
	bandwidthEvents []queue.BandwidthEvent
}

func (s *stubControlSubscriber) SubscribeToCancelEvents(_ context.Context, cb func(string)) {
	for _, id := range s.migrationIDs {
		cb(id)
	}
}

func (s *stubControlSubscriber) SubscribeToSyncCancelEvents(_ context.Context, cb func(queue.SyncCancelEvent)) {
	for _, evt := range s.syncEvents {
		cb(evt)
	}
}

func (s *stubControlSubscriber) SubscribeToBandwidthChanges(_ context.Context, cb func(queue.BandwidthEvent)) {
	for _, evt := range s.bandwidthEvents {
		cb(evt)
	}
}

type stubTransferControl struct {
	cancelledMigrations []string
	cancelledSyncs      []queue.SyncCancelEvent
	bandwidthUpdates    []struct {
		jobID string
		mbps  int
	}
}

func (c *stubTransferControl) CancelMigration(migrationID string) {
	c.cancelledMigrations = append(c.cancelledMigrations, migrationID)
}

func (c *stubTransferControl) CancelSyncJob(syncJobID string, passGeneration int, allGenerations bool) {
	c.cancelledSyncs = append(c.cancelledSyncs, queue.SyncCancelEvent{
		SyncJobID:      syncJobID,
		PassGeneration: passGeneration,
		AllGenerations: allGenerations,
	})
}

func (c *stubTransferControl) SetBandwidthLimit(jobID string, mbps int) {
	c.bandwidthUpdates = append(c.bandwidthUpdates, struct {
		jobID string
		mbps  int
	}{jobID: jobID, mbps: mbps})
}

func TestControlLoopsSubscribeAndDispatch(t *testing.T) {
	sub := &stubControlSubscriber{
		migrationIDs: []string{"mig-1"},
		syncEvents: []queue.SyncCancelEvent{
			{SyncJobID: "sync-1", PassGeneration: 3, AllGenerations: false},
			{SyncJobID: "sync-2", PassGeneration: 0, AllGenerations: true},
		},
		bandwidthEvents: []queue.BandwidthEvent{
			{MigrationID: "mig-1", BandwidthLimitMbps: 25},
			{MigrationID: "mig-ignored", SyncJobID: "sync-1", BandwidthLimitMbps: 10},
		},
	}
	ctrl := &stubTransferControl{}

	loops := controlLoops("w-test", sub, ctrl)
	var names []string
	for _, loop := range loops {
		if loop.Run == nil {
			t.Fatalf("loop %q has nil Run", loop.Name)
		}
		names = append(names, loop.Name)
		if err := loop.Run(context.Background()); err != nil {
			t.Fatalf("loop %q returned unexpected error: %v", loop.Name, err)
		}
	}

	sort.Strings(names)
	want := []string{"bandwidth_events", "migration_cancel_events", "sync_cancel_events"}
	if len(names) != len(want) {
		t.Fatalf("loop names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("loop names = %v, want %v", names, want)
		}
	}

	if len(ctrl.cancelledMigrations) != 1 || ctrl.cancelledMigrations[0] != "mig-1" {
		t.Fatalf("cancelledMigrations = %v, want [mig-1]", ctrl.cancelledMigrations)
	}
	if len(ctrl.cancelledSyncs) != 2 || ctrl.cancelledSyncs[0].SyncJobID != "sync-1" || ctrl.cancelledSyncs[0].PassGeneration != 3 || ctrl.cancelledSyncs[0].AllGenerations ||
		ctrl.cancelledSyncs[1].SyncJobID != "sync-2" || !ctrl.cancelledSyncs[1].AllGenerations {
		t.Fatalf("cancelledSyncs = %+v", ctrl.cancelledSyncs)
	}
	if len(ctrl.bandwidthUpdates) != 2 ||
		ctrl.bandwidthUpdates[0].jobID != "mig-1" || ctrl.bandwidthUpdates[0].mbps != 25 ||
		ctrl.bandwidthUpdates[1].jobID != "sync-1" || ctrl.bandwidthUpdates[1].mbps != 10 {
		t.Fatalf("bandwidthUpdates = %+v", ctrl.bandwidthUpdates)
	}
}
