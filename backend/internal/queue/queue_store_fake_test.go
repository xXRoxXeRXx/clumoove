package queue

import (
	"context"
	"errors"
	"testing"

	"backend/internal/db"
)

type fakeQueueTaskStore struct {
	db.TaskStore
	claim         *db.DequeuedTask
	dequeueErr    error
	dequeueWorker string
	recoveredRows int64
	recoverErr    error
	recoverWorker string
	runningHashes []string
	listErr       error
	notifyCalls   int
	notifyErr     error
}

func (f *fakeQueueTaskStore) DequeueTask(_ context.Context, workerID string) (*db.DequeuedTask, error) {
	f.dequeueWorker = workerID
	return f.claim, f.dequeueErr
}

func (f *fakeQueueTaskStore) RecoverAbandonedTasks(_ context.Context, workerID string) (int64, error) {
	f.recoverWorker = workerID
	return f.recoveredRows, f.recoverErr
}

func (f *fakeQueueTaskStore) ListRunningWorkerHashes(context.Context) ([]string, error) {
	return f.runningHashes, f.listErr
}

func (f *fakeQueueTaskStore) NotifyTaskAvailable(context.Context) error {
	f.notifyCalls++
	return f.notifyErr
}

func TestDequeueSQLThroughTaskStore(t *testing.T) {
	q := &Queue{}

	if _, err := q.DequeueSQL(context.Background(), nil, "worker-1"); err == nil {
		t.Fatal("expected error for nil TaskStore")
	}

	store := &fakeQueueTaskStore{
		claim: &db.DequeuedTask{
			MigrationID:    "mig-1",
			SyncJobID:      "sync-1",
			TaskID:         "task-1",
			ClaimEpoch:     7,
			PassGeneration: 3,
		},
	}
	payload, err := q.DequeueSQL(context.Background(), store, "worker-1")
	if err != nil {
		t.Fatalf("DequeueSQL: %v", err)
	}
	if store.dequeueWorker != "worker-1" {
		t.Fatalf("workerID = %q, want worker-1", store.dequeueWorker)
	}
	if payload == nil || payload.TaskID != "task-1" || payload.MigrationID != "mig-1" || payload.SyncJobID != "sync-1" || payload.ClaimEpoch != 7 || payload.PassGeneration != 3 {
		t.Fatalf("payload = %+v, unexpected", payload)
	}

	emptyStore := &fakeQueueTaskStore{}
	payload, err = q.DequeueSQL(context.Background(), emptyStore, "worker-2")
	if err != nil || payload != nil {
		t.Fatalf("empty DequeueSQL = (%+v, %v), want (nil, nil)", payload, err)
	}
}

func TestRecoverAbandonedTasksThroughTaskStore(t *testing.T) {
	q := &Queue{}

	if err := q.RecoverAbandonedTasks(context.Background(), nil, "worker-1"); err == nil {
		t.Fatal("expected error for nil TaskStore")
	}

	store := &fakeQueueTaskStore{recoveredRows: 2}
	if err := q.RecoverAbandonedTasks(context.Background(), store, "dead-worker"); err != nil {
		t.Fatalf("RecoverAbandonedTasks: %v", err)
	}
	if store.recoverWorker != "dead-worker" {
		t.Fatalf("recoverWorker = %q, want dead-worker", store.recoverWorker)
	}

	errStore := &fakeQueueTaskStore{recoverErr: errors.New("db down")}
	if err := q.RecoverAbandonedTasks(context.Background(), errStore, "dead-worker"); err == nil {
		t.Fatal("expected store error to propagate")
	}
}

func TestGetAbandonedWorkerQueuesThroughTaskStore(t *testing.T) {
	q := &Queue{}

	if _, err := q.GetAbandonedWorkerQueues(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil TaskStore")
	}

	errStore := &fakeQueueTaskStore{listErr: errors.New("db down")}
	if _, err := q.GetAbandonedWorkerQueues(context.Background(), errStore); err == nil {
		t.Fatal("expected store error to propagate")
	}
}

func TestNotifyTaskAvailableThroughTaskStore(t *testing.T) {
	q := &Queue{}

	// Nil store is a safe no-op.
	q.NotifyTaskAvailable(context.Background(), nil)

	store := &fakeQueueTaskStore{notifyErr: errors.New("notify failed")}
	q.NotifyTaskAvailable(context.Background(), store)
	if store.notifyCalls != 1 {
		t.Fatalf("notifyCalls = %d, want 1", store.notifyCalls)
	}
}
