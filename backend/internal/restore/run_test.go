package restore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

type failingConnector struct{}

func (failingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("database unavailable")
}
func (failingConnector) Driver() driver.Driver { return nil }

func TestRunRetriesTransientPollErrorsUntilCancelled(t *testing.T) {
	failingDB := sql.OpenDB(failingConnector{})
	coordinator := &Coordinator{db: failingDB, store: db.NewRestoreStore(failingDB), workerID: "restore-test"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx, time.Millisecond) }()

	select {
	case err := <-done:
		t.Fatalf("Run returned %v on a transient poll error, want it to keep retrying", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v after cancel, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
