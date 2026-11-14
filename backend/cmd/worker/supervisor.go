package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"backend/internal/observability"
)

// coordinatorFailureKind is the error_kind logged for terminal coordinator exits.
const coordinatorFailureKind = "coordinator_failure"

// coordinatorFailure reports that a coordinator loop stopped while the worker
// was still supposed to be running.
type coordinatorFailure struct {
	Name string
	Err  error
}

func (f *coordinatorFailure) Error() string {
	if f.Err == nil {
		return fmt.Sprintf("%s coordinator stopped unexpectedly", f.Name)
	}
	return fmt.Sprintf("%s coordinator stopped unexpectedly: %v", f.Name, f.Err)
}

func (f *coordinatorFailure) Unwrap() error { return f.Err }

// coordinatorSupervisor owns the worker-level policy for background
// coordinator loops: any loop that exits before shutdown has begun is a
// terminal failure. The supervisor records the first such failure and cancels
// the worker context, so in-flight tasks drain, the liveness heartbeat stops,
// and the process can exit non-zero for a platform restart instead of running
// on in a partially operational state. A loop stops cleanly only when it
// returns context.Canceled (or nil) because the worker context was cancelled;
// any other return, before or during shutdown, is a failure. The classification
// is by the returned error, not by timing, so a real failure that coincides
// with a shutdown signal is still reported.
type coordinatorSupervisor struct {
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	failure error
}

func newCoordinatorSupervisor(cancel context.CancelFunc) *coordinatorSupervisor {
	return &coordinatorSupervisor{cancel: cancel}
}

// Go runs a coordinator loop under supervision.
func (s *coordinatorSupervisor) Go(ctx context.Context, name string, run func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := run(ctx)
		s.mu.Lock()
		if stoppedForShutdown(ctx, err) || s.failure != nil {
			s.mu.Unlock()
			return
		}
		failure := &coordinatorFailure{Name: name, Err: err}
		s.failure = failure
		s.mu.Unlock()
		slog.Error("coordinator_stopped", slog.String("component", name), observability.Error(failure), slog.String("error_kind", coordinatorFailureKind))
		s.cancel()
	}()
}

func stoppedForShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled))
}

// Wait blocks until every supervised loop has returned.
func (s *coordinatorSupervisor) Wait() { s.wg.Wait() }

// Err returns the first unexpected coordinator failure, or nil when every loop
// stopped because of shutdown.
func (s *coordinatorSupervisor) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// coordinatorLoop is a named background loop that must run until shutdown.
type coordinatorLoop struct {
	name string
	run  func(context.Context) error
}

// serveWorker runs the coordinator loops under supervision while drain (the
// processor) blocks until ctx is cancelled and in-flight tasks finish. Once
// the drain and every loop have stopped, release is called to drop the
// worker's liveness lease: no task is running at that point, so other workers
// can safely reclaim anything left behind instead of waiting for the lease to
// expire. It returns the process exit code: 1 when a coordinator failed, so
// the platform restarts the worker, otherwise 0.
func serveWorker(ctx context.Context, cancel context.CancelFunc, drain func(context.Context), release func(), loops ...coordinatorLoop) int {
	supervisor := newCoordinatorSupervisor(cancel)
	for _, loop := range loops {
		supervisor.Go(ctx, loop.name, loop.run)
	}
	drain(ctx)
	supervisor.Wait()
	release()
	failed := supervisor.Err() != nil
	slog.Info("service_stopped", slog.String("component", "worker"), slog.Bool("failed", failed))
	if failed {
		return 1
	}
	return 0
}
