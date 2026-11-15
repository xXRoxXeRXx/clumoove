package processor

import (
	"context"
	"sort"
	"testing"
	"time"
)

func TestBackgroundLoopsExposeEverySchedulerAndSubscriber(t *testing.T) {
	loops := (&Processor{}).BackgroundLoops()

	var names []string
	for _, loop := range loops {
		if loop.Run == nil {
			t.Fatalf("loop %q has no Run function", loop.Name)
		}
		names = append(names, loop.Name)
	}
	sort.Strings(names)
	want := []string{
		"abandoned_worker_cleanup",
		"bandwidth_events",
		"checksum_verifier",
		"connection_recovery",
		"migration_cancel_events",
		"notifier",
		"orphaned_task_recovery",
		"progress_reconciler",
		"retry_scheduler",
		"sync_cancel_events",
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

func TestStartWaitsForTaskListenerToReleaseItsConnectionAfterCancellation(t *testing.T) {
	listenerClosed := make(chan struct{})
	p := &Processor{
		workerID:  "worker-test",
		notifyDSN: "postgres://listener",
		listenForTasks: func(ctx context.Context, _ string) (<-chan struct{}, error) {
			ch := make(chan struct{})
			go func() {
				<-ctx.Done()
				time.Sleep(50 * time.Millisecond)
				close(listenerClosed)
				close(ch)
			}()
			return ch, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		p.Start(ctx)
		close(returned)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
	select {
	case <-listenerClosed:
	default:
		t.Fatal("Start returned before the task listener released its connection")
	}
}

func TestConnectionRecoveryLoopWaitsForIndexersItRestarted(t *testing.T) {
	p := &Processor{}
	releaseIndexer := make(chan struct{})
	scheduler := func(context.Context) {
		p.recoveredIndexers.Add(1)
		go func() {
			defer p.recoveredIndexers.Done()
			<-releaseIndexer
		}()
	}

	returned := make(chan struct{})
	go func() {
		p.awaitRecoveredIndexers(scheduler)(context.Background())
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
