package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"backend/internal/observability"
	"backend/internal/processor"
)

// This file is the worker's single runtime module. It owns the cancellation
// lifecycle of every long-running loop the worker starts (coordinators,
// schedulers, subscribers, notifiers), the policy for unexpected loop exits,
// and the final wait before the process exits. main only builds dependencies,
// wires signals, and calls runWorker.

// loopFailureKind is the error_kind logged for terminal background-loop exits.
const loopFailureKind = "coordinator_failure"

// loopFailure reports that a background loop stopped while the worker
// was still supposed to be running.
type loopFailure struct {
	Name string
	Err  error
}

func (f *loopFailure) Error() string {
	if f.Err == nil {
		return fmt.Sprintf("%s loop stopped unexpectedly", f.Name)
	}
	return fmt.Sprintf("%s loop stopped unexpectedly: %v", f.Name, f.Err)
}

func (f *loopFailure) Unwrap() error { return f.Err }

// loopSupervisor owns the worker-level policy for background loops: any loop
// that exits before shutdown has begun is a terminal failure. The supervisor records the first such failure and cancels
// the worker context, so in-flight tasks drain, the liveness heartbeat stops,
// and the process can exit non-zero for a platform restart instead of running
// on in a partially operational state. A loop stops cleanly only when it
// returns context.Canceled (or nil) because the worker context was cancelled;
// any other return, before or during shutdown, is a failure. The classification
// is by the returned error, not by timing, so a real failure that coincides
// with a shutdown signal is still reported.
type loopSupervisor struct {
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	failure error
}

func newLoopSupervisor(cancel context.CancelFunc) *loopSupervisor {
	return &loopSupervisor{cancel: cancel}
}

// Go runs a background loop under supervision.
func (s *loopSupervisor) Go(ctx context.Context, name string, run func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := run(ctx)
		s.mu.Lock()
		if stoppedForShutdown(ctx, err) || s.failure != nil {
			s.mu.Unlock()
			return
		}
		failure := &loopFailure{Name: name, Err: err}
		s.failure = failure
		s.mu.Unlock()
		slog.Error("coordinator_stopped", slog.String("component", name), observability.Error(failure), slog.String("error_kind", loopFailureKind))
		s.cancel()
	}()
}

func stoppedForShutdown(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled))
}

// Wait blocks until every supervised loop has returned.
func (s *loopSupervisor) Wait() { s.wg.Wait() }

// Err returns the first unexpected background-loop failure, or nil when every loop
// stopped because of shutdown.
func (s *loopSupervisor) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// backgroundLoop is a named background loop that must run until shutdown. It is
// the same type the processor exposes, so no adapter is needed.
type backgroundLoop = processor.BackgroundLoop

// runWorker runs the background loops under supervision while drain (the
// processor) blocks until ctx is cancelled and in-flight tasks finish. Once
// the drain and every loop have stopped, release is called to drop the
// worker's liveness lease: no task is running at that point, so other workers
// can safely reclaim anything left behind instead of waiting for the lease to
// expire. It returns the process exit code: 1 when a background loop failed, so
// the platform restarts the worker, otherwise 0.
func runWorker(ctx context.Context, cancel context.CancelFunc, drain func(context.Context), release func(), loops ...backgroundLoop) int {
	supervisor := newLoopSupervisor(cancel)
	for _, loop := range loops {
		supervisor.Go(ctx, loop.Name, loop.Run)
	}
	drain(ctx)
	// The drain finishing means the worker is done; stop every loop so the
	// final wait below cannot block on a loop that outlives the processor.
	cancel()
	supervisor.Wait()
	release()
	failed := supervisor.Err() != nil
	slog.Info("service_stopped", slog.String("component", "worker"), slog.Bool("failed", failed))
	if failed {
		return 1
	}
	return 0
}
