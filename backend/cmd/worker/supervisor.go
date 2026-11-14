package main

import (
	"context"
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
// on in a partially operational state. Exits after shutdown has begun are a
// clean stop, whatever error the loop returns while unwinding.
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
		if ctx.Err() != nil || s.failure != nil {
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

// Wait blocks until every supervised loop has returned.
func (s *coordinatorSupervisor) Wait() { s.wg.Wait() }

// Err returns the first unexpected coordinator failure, or nil when every loop
// stopped because of shutdown.
func (s *coordinatorSupervisor) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}
