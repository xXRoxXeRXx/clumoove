package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLoopSupervisorTreatsLoopErrorAsTerminalFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newLoopSupervisor(cancel)

	supervisor.Go(ctx, "restore", func(context.Context) error { return errors.New("poll exploded") })
	supervisor.Wait()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("worker context was not cancelled after loop failure")
	}
	err := supervisor.Err()
	if err == nil {
		t.Fatal("Err() = nil, want terminal failure")
	}
	var failure *loopFailure
	if !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want loopFailure for restore", err)
	}
}

func TestLoopSupervisorTreatsUnexpectedNilReturnAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newLoopSupervisor(cancel)

	supervisor.Go(ctx, "backup", func(context.Context) error { return nil })
	supervisor.Wait()

	if supervisor.Err() == nil {
		t.Fatal("loop returning nil without shutdown was not reported")
	}
	if ctx.Err() == nil {
		t.Fatal("worker context was not cancelled after loop exit")
	}
}

func TestLoopSupervisorTreatsShutdownCancellationAsCleanStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := newLoopSupervisor(cancel)
	started := make(chan struct{})

	supervisor.Go(ctx, "backup", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	cancel()
	supervisor.Wait()

	if err := supervisor.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil for graceful shutdown", err)
	}
}

func TestLoopSupervisorReportsNonCancellationErrorDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := newLoopSupervisor(cancel)
	started := make(chan struct{})

	supervisor.Go(ctx, "restore", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return errors.New("poll aborted: lost database")
	})
	<-started
	cancel()
	supervisor.Wait()

	var failure *loopFailure
	if err := supervisor.Err(); !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want failure: a real error is not cancellation even during shutdown", err)
	}
}

func TestLoopSupervisorTreatsLoopOwnCancellationAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newLoopSupervisor(cancel)

	supervisor.Go(ctx, "backup", func(context.Context) error { return context.Canceled })
	supervisor.Wait()

	if supervisor.Err() == nil {
		t.Fatal("context.Canceled returned while the worker was running was treated as a clean stop")
	}
}

func TestLoopSupervisorStopsSiblingsAndKeepsFirstFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newLoopSupervisor(cancel)
	siblingRunning := make(chan struct{})

	supervisor.Go(ctx, "backup", func(ctx context.Context) error {
		close(siblingRunning)
		<-ctx.Done()
		return ctx.Err()
	})
	<-siblingRunning
	supervisor.Go(ctx, "restore", func(context.Context) error { return errors.New("poll exploded") })

	done := make(chan struct{})
	go func() { supervisor.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sibling loop was not stopped after failure")
	}

	var failure *loopFailure
	if err := supervisor.Err(); !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want first failure from restore", err)
	}
}

func blockUntilCancelled(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRunWorkerExitsNonZeroAfterDrainAndReleasesLeaseWhenLoopFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	drain := func(ctx context.Context) {
		<-ctx.Done()
		events = append(events, "drained")
	}
	release := func() { events = append(events, "released") }

	code := runWorker(ctx, cancel, drain, release,
		backgroundLoop{name: "backup", run: blockUntilCancelled},
		backgroundLoop{name: "restore", run: func(context.Context) error { return errors.New("poll exploded") }},
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(events) != 2 || events[0] != "drained" || events[1] != "released" {
		t.Fatalf("events = %v, want drain before lease release", events)
	}
}

func TestRunWorkerExitsZeroOnGracefulCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	released := false
	drain := func(ctx context.Context) { <-ctx.Done() }

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	code := runWorker(ctx, cancel, drain, func() { released = true },
		backgroundLoop{name: "backup", run: blockUntilCancelled},
		backgroundLoop{name: "restore", run: blockUntilCancelled},
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for graceful shutdown", code)
	}
	if !released {
		t.Fatal("lease was not released after graceful shutdown")
	}
}

func TestRunWorkerWaitsForSlowLoopBeforeReleasingLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	drain := func(ctx context.Context) { <-ctx.Done() }
	slow := func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		record("slow-loop-stopped")
		return ctx.Err()
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	code := runWorker(ctx, cancel, drain, func() { record("released") },
		backgroundLoop{name: "scheduler", run: slow},
		backgroundLoop{name: "notifier", run: blockUntilCancelled},
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != "slow-loop-stopped" || events[1] != "released" {
		t.Fatalf("events = %v, want slow loop to finish before the lease is released", events)
	}
}

func TestRunWorkerStopsLoopsWhenDrainReturnsWithoutCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopStopped := false

	code := runWorker(ctx, cancel, func(context.Context) {}, func() {},
		backgroundLoop{name: "scheduler", run: func(ctx context.Context) error {
			<-ctx.Done()
			loopStopped = true
			return ctx.Err()
		}},
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0: loops stopping because the drain finished is not a failure", code)
	}
	if !loopStopped {
		t.Fatal("loop was not stopped and awaited after the drain returned")
	}
}
