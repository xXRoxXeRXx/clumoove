package main

import (
	"sort"
	"testing"

	"backend/internal/processor"
	"backend/internal/queue"
)

func TestControlLoopsRegisterExpectedSubscriptions(t *testing.T) {
	loops := controlLoops(nil, nil)
	var names []string
	for _, loop := range loops {
		if loop.Run == nil {
			t.Fatalf("loop %q has nil Run", loop.Name)
		}
		names = append(names, loop.Name)
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
}

func TestBandwidthTargetResolvesJobAndDispatchesCleanly(t *testing.T) {
	migEvent := queue.BandwidthEvent{MigrationID: "mig-1", BandwidthLimitMbps: 25}
	if id, kind := bandwidthTarget(migEvent); id != "mig-1" || kind != "migration" {
		t.Fatalf("bandwidthTarget(migration) = (%q, %q), want (mig-1, migration)", id, kind)
	}

	syncEvent := queue.BandwidthEvent{MigrationID: "mig-ignored", SyncJobID: "sync-1", BandwidthLimitMbps: 10}
	if id, kind := bandwidthTarget(syncEvent); id != "sync-1" || kind != "sync job" {
		t.Fatalf("bandwidthTarget(sync) = (%q, %q), want (sync-1, sync job)", id, kind)
	}

	applyBandwidthEvent(&processor.Processor{}, migEvent)
	applyBandwidthEvent(&processor.Processor{}, syncEvent)
}
