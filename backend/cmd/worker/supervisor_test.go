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

func TestCoordinatorSupervisorIgnoresLoopErrorsCausedByShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := newCoordinatorSupervisor(cancel)
	started := make(chan struct{})

	supervisor.Go(ctx, "restore", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return errors.New("poll aborted: connection closed during shutdown")
	})
	<-started
	cancel()
	supervisor.Wait()

	if err := supervisor.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil: error arrived after shutdown began", err)
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
