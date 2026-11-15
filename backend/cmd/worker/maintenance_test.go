package main

import (
	"context"
	"sort"
	"testing"
	"time"
)

func TestMaintenanceLoopsExposeEveryMaintenanceLoop(t *testing.T) {
	m := newWorkerMaintenance(nil, nil, nil, "worker-test", "")
	loops := m.Loops()

	var names []string
	for _, loop := range loops {
		if loop.Run == nil {
			t.Fatalf("loop %q has nil Run", loop.Name)
		}
		names = append(names, loop.Name)
	}
	sort.Strings(names)
	want := []string{
		"abandoned_worker_cleanup",
		"connection_recovery",
		"orphaned_task_recovery",
		"progress_reconciler",
		"retry_scheduler",
		"worker_liveness",
	}
	if len(names) != len(want) {
		t.Fatalf("loop names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("loop names = %v, want %v", names, want)
		}
	}
}

func TestExamineRecoveryCandidateStopsCursorAtProbeBudget(t *testing.T) {
	last := ""
	probes := 0
	for i := 1; i <= maxMigrationRecoveryProbesPerTick+5; i++ {
		id := string(rune('a' + i - 1))
		if !examineRecoveryCandidate(&last, probes, maxMigrationRecoveryProbesPerTick, id) {
			break
		}
		probes++
	}
	if last != "j" {
		t.Fatalf("cursor after probe budget = %q, want %q", last, "j")
	}
}

func TestRecoveryBackoffAndProbeEligibility(t *testing.T) {
	now := time.Now()
	cases := []struct {
		attempts int
		age      time.Duration
		want     bool
	}{
		{0, 0, true},
		{1, 59 * time.Second, false},
		{1, time.Minute, true},
		{2, 4*time.Minute + 59*time.Second, false},
		{2, 5 * time.Minute, true},
	}
	for _, tc := range cases {
		if got := shouldProbeRecovery(recoveryState{attempts: tc.attempts, lastAttempt: now.Add(-tc.age)}, now); got != tc.want {
			t.Errorf("shouldProbeRecovery(attempts=%d, age=%s) = %v, want %v", tc.attempts, tc.age, got, tc.want)
		}
	}
}

func TestRecoveryCursorKeepsMigrationAndSyncQueuesIndependent(t *testing.T) {
	m := &workerMaintenance{}
	m.setRecoveryCursor(false, "migration-1")
	m.setRecoveryCursor(true, "sync-1")
	if got := m.recoveryCursor(false); got != "migration-1" {
		t.Fatalf("migration recovery cursor = %q", got)
	}
	if got := m.recoveryCursor(true); got != "sync-1" {
		t.Fatalf("sync recovery cursor = %q", got)
	}
}

func TestConnectionRecoveryLoopWaitsForIndexersItRestarted(t *testing.T) {
	m := &workerMaintenance{}
	releaseIndexer := make(chan struct{})
	scheduler := func(context.Context) {
		m.recoveredIndexers.Add(1)
		go func() {
			defer m.recoveredIndexers.Done()
			<-releaseIndexer
		}()
	}

	returned := make(chan struct{})
	go func() {
		m.awaitRecoveredIndexers(scheduler)(context.Background())
		close(returned)
	}()

	select {
	case <-returned:
		t.Fatal("recovery loop returned while a restarted indexer was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseIndexer)
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery loop did not return after the indexer finished")
	}
}

func TestEffectiveRecoveryStateResetsBackoffWhenEntityUpdatedAtChanges(t *testing.T) {
	m := &workerMaintenance{}
	firstPaused := time.Now().Add(-10 * time.Minute)
	secondPaused := firstPaused.Add(5 * time.Minute)
	key := recoveryAttemptKey(false, "mig-1")

	m.recordRecoveryFailure(false, "mig-1", 2, firstPaused)

	// Same paused timestamp preserves accumulated backoff attempts.
	same := m.effectiveRecoveryState(key, firstPaused)
	if same.attempts != 3 {
		t.Fatalf("same pausedUpdatedAt attempts = %d, want 3", same.attempts)
	}

	// New updated_at (e.g. migration resumed and later re-paused) resets attempts to 0.
	reset := m.effectiveRecoveryState(key, secondPaused)
	if reset.attempts != 0 || !reset.pausedUpdatedAt.Equal(secondPaused) {
		t.Fatalf("changed pausedUpdatedAt state = %+v, want attempts=0 and updatedAt=%v", reset, secondPaused)
	}
}
