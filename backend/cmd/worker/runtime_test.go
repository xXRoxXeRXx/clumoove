package main

import (
	"context"
	"errors"
	"fmt"
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

	code := superviseLoops(ctx, cancel, drain, release,
		backgroundLoop{Name: "backup", Run: blockUntilCancelled},
		backgroundLoop{Name: "restore", Run: func(context.Context) error { return errors.New("poll exploded") }},
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
	code := superviseLoops(ctx, cancel, drain, func() { released = true },
		backgroundLoop{Name: "backup", Run: blockUntilCancelled},
		backgroundLoop{Name: "restore", Run: blockUntilCancelled},
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
	code := superviseLoops(ctx, cancel, drain, func() { record("released") },
		backgroundLoop{Name: "scheduler", Run: slow},
		backgroundLoop{Name: "notifier", Run: blockUntilCancelled},
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

	code := superviseLoops(ctx, cancel, func(context.Context) {}, func() {},
		backgroundLoop{Name: "scheduler", Run: func(ctx context.Context) error {
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

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) record(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fakeProcessor struct {
	log     *eventLog
	started chan struct{}
	loops   []backgroundLoop
}

func (f *fakeProcessor) RecoverStartupTasks(context.Context) { f.log.record("recovered") }
func (f *fakeProcessor) Start(ctx context.Context) {
	f.log.record("processor-started")
	close(f.started)
	<-ctx.Done()
	f.log.record("processor-stopped")
}
func (f *fakeProcessor) BackgroundLoops() []backgroundLoop { return f.loops }

type fakeCoordinator struct {
	name    string
	log     *eventLog
	started chan struct{}
}

func (f *fakeCoordinator) Run(ctx context.Context, _ time.Duration) error {
	f.log.record(f.name + "-started")
	close(f.started)
	<-ctx.Done()
	f.log.record(f.name + "-stopped")
	return ctx.Err()
}

type fakeLeases struct {
	log *eventLog
	err error
}

func (f *fakeLeases) DeregisterActiveWorker(_ context.Context, workerID string) error {
	f.log.record("released:" + workerID)
	return f.err
}

func newFakeRuntime(log *eventLog, leaseErr error) (workerRuntime, []chan struct{}) {
	processorStarted := make(chan struct{})
	backupStarted := make(chan struct{})
	restoreStarted := make(chan struct{})
	schedulerStarted := make(chan struct{})
	scheduler := backgroundLoop{Name: "scheduler", Run: func(ctx context.Context) error {
		log.record("scheduler-started")
		close(schedulerStarted)
		<-ctx.Done()
		log.record("scheduler-stopped")
		return ctx.Err()
	}}
	return workerRuntime{
		WorkerID:  "worker-1",
		Processor: &fakeProcessor{log: log, started: processorStarted, loops: []backgroundLoop{scheduler}},
		Backup:    &fakeCoordinator{name: "backup", log: log, started: backupStarted},
		Restore:   &fakeCoordinator{name: "restore", log: log, started: restoreStarted},
		Leases:    &fakeLeases{log: log, err: leaseErr},
	}, []chan struct{}{processorStarted, backupStarted, restoreStarted, schedulerStarted}
}

func runFakeRuntimeUntilAllStarted(t *testing.T, rt workerRuntime, started []chan struct{}) int {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for _, ch := range started {
			<-ch
		}
		cancel()
	}()
	return runWorker(ctx, cancel, rt)
}

func TestRunWorkerRecoversThenRunsEveryLoopAndReleasesLeaseLast(t *testing.T) {
	log := &eventLog{}
	rt, started := newFakeRuntime(log, nil)

	code := runFakeRuntimeUntilAllStarted(t, rt, started)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	events := log.snapshot()
	if len(events) != 10 {
		t.Fatalf("events = %v, want recovery, 4 starts, 4 stops and the lease release", events)
	}
	if events[0] != "recovered" {
		t.Fatalf("events = %v, want startup recovery before any loop starts", events)
	}
	if events[len(events)-1] != "released:worker-1" {
		t.Fatalf("events = %v, want lease released after every loop stopped", events)
	}
	for _, name := range []string{"processor", "backup", "restore", "scheduler"} {
		if !contains(events, name+"-started") || !contains(events, name+"-stopped") {
			t.Fatalf("events = %v, want %s started and stopped", events, name)
		}
	}
}

func TestRunWorkerIgnoresLeaseReleaseFailure(t *testing.T) {
	log := &eventLog{}
	rt, started := newFakeRuntime(log, fmt.Errorf("redis unavailable"))

	if code := runFakeRuntimeUntilAllStarted(t, rt, started); code != 0 {
		t.Fatalf("exit code = %d, want 0: a failed lease release must not fail a clean shutdown", code)
	}
}

func contains(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}
