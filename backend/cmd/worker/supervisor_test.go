package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCoordinatorSupervisorTreatsLoopErrorAsTerminalFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newCoordinatorSupervisor(cancel)

	supervisor.Go(ctx, "restore", func(context.Context) error { return errors.New("poll exploded") })
	supervisor.Wait()

	select {
	case <-ctx.Done():
	default:
		t.Fatal("worker context was not cancelled after coordinator failure")
	}
	err := supervisor.Err()
	if err == nil {
		t.Fatal("Err() = nil, want terminal failure")
	}
	var failure *coordinatorFailure
	if !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want coordinatorFailure for restore", err)
	}
}

func TestCoordinatorSupervisorTreatsUnexpectedNilReturnAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newCoordinatorSupervisor(cancel)

	supervisor.Go(ctx, "backup", func(context.Context) error { return nil })
	supervisor.Wait()

	if supervisor.Err() == nil {
		t.Fatal("coordinator returning nil without shutdown was not reported")
	}
	if ctx.Err() == nil {
		t.Fatal("worker context was not cancelled after coordinator exit")
	}
}

func TestCoordinatorSupervisorTreatsShutdownCancellationAsCleanStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := newCoordinatorSupervisor(cancel)
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

func TestCoordinatorSupervisorReportsNonCancellationErrorDuringShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := newCoordinatorSupervisor(cancel)
	started := make(chan struct{})

	supervisor.Go(ctx, "restore", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return errors.New("poll aborted: lost database")
	})
	<-started
	cancel()
	supervisor.Wait()

	var failure *coordinatorFailure
	if err := supervisor.Err(); !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want failure: a real error is not cancellation even during shutdown", err)
	}
}

func TestCoordinatorSupervisorTreatsLoopOwnCancellationAsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newCoordinatorSupervisor(cancel)

	supervisor.Go(ctx, "backup", func(context.Context) error { return context.Canceled })
	supervisor.Wait()

	if supervisor.Err() == nil {
		t.Fatal("context.Canceled returned while the worker was running was treated as a clean stop")
	}
}

func TestCoordinatorSupervisorStopsSiblingsAndKeepsFirstFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	supervisor := newCoordinatorSupervisor(cancel)
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
		t.Fatal("sibling coordinator was not stopped after failure")
	}

	var failure *coordinatorFailure
	if err := supervisor.Err(); !errors.As(err, &failure) || failure.Name != "restore" {
		t.Fatalf("Err() = %v, want first failure from restore", err)
	}
}

func blockUntilCancelled(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestServeWorkerExitsNonZeroAfterDrainAndReleasesLeaseWhenCoordinatorFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events []string
	drain := func(ctx context.Context) {
		<-ctx.Done()
		events = append(events, "drained")
	}
	release := func() { events = append(events, "released") }

	code := serveWorker(ctx, cancel, drain, release,
		coordinatorLoop{name: "backup", run: blockUntilCancelled},
		coordinatorLoop{name: "restore", run: func(context.Context) error { return errors.New("poll exploded") }},
	)

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(events) != 2 || events[0] != "drained" || events[1] != "released" {
		t.Fatalf("events = %v, want drain before lease release", events)
	}
}

func TestServeWorkerExitsZeroOnGracefulCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	released := false
	drain := func(ctx context.Context) { <-ctx.Done() }

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	code := serveWorker(ctx, cancel, drain, func() { released = true },
		coordinatorLoop{name: "backup", run: blockUntilCancelled},
		coordinatorLoop{name: "restore", run: blockUntilCancelled},
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for graceful shutdown", code)
	}
	if !released {
		t.Fatal("lease was not released after graceful shutdown")
	}
}
