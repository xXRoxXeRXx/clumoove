package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"testing"

	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"
)

// failureFakeSyncs records the sync-side writes handleSyncTaskFailure makes;
// any other call panics through the nil embedded interface.
type failureFakeSyncs struct {
	db.SyncStore
	job       *db.SyncJob
	jobErr    error
	cancelled int
	calls     []string
	claimed   []db.Task // snapshots of UpdateClaimedSyncTaskStatus
	terminal  []db.Task // snapshots of UpdateSyncTaskStatusAndIncrementProgress
}

func (f *failureFakeSyncs) GetSyncJob(string) (*db.SyncJob, error) { return f.job, f.jobErr }

func (f *failureFakeSyncs) UpdateClaimedSyncTaskStatus(_ context.Context, t *db.Task) error {
	f.calls = append(f.calls, "claimed:"+t.Status)
	f.claimed = append(f.claimed, *t)
	return nil
}

func (f *failureFakeSyncs) UpdateSyncTaskStatusAndIncrementProgress(_ context.Context, t *db.Task, files, changed, deleted, failed int, bytes int64) error {
	f.calls = append(f.calls, fmt.Sprintf("terminal:%s:%d/%d/%d/%d/%d", t.Status, files, changed, deleted, failed, bytes))
	f.terminal = append(f.terminal, *t)
	return nil
}

func (f *failureFakeSyncs) UpdateSyncJobStatusForGeneration(_ string, generation int, status string, errMsg *string) error {
	msg := ""
	if errMsg != nil {
		msg = "+msg"
	}
	f.calls = append(f.calls, fmt.Sprintf("job:%d:%s%s", generation, status, msg))
	return nil
}

func (f *failureFakeSyncs) CancelRemainingPendingSyncTasksForGeneration(_ string, generation int) (int, error) {
	f.calls = append(f.calls, fmt.Sprintf("cancel-pending:%d", generation))
	return f.cancelled, nil
}

func (f *failureFakeSyncs) IncrementSyncJobProgressForGeneration(_ context.Context, _ string, generation, files, changed, deleted, failed int, bytes int64) error {
	f.calls = append(f.calls, fmt.Sprintf("progress:%d:%d/%d/%d/%d/%d", generation, files, changed, deleted, failed, bytes))
	return nil
}

type failureFakeTasks struct {
	db.TaskStore
	task *db.Task
}

func (f *failureFakeTasks) GetTask(string) (*db.Task, error) {
	t := *f.task
	return &t, nil
}

func syncFailureProcessor(syncs *failureFakeSyncs, task *db.Task) *Processor {
	return &Processor{workerID: "w-test", syncs: syncs, tasks: &failureFakeTasks{task: task}}
}

func syncFailurePayload() *queue.Payload {
	return &queue.Payload{SyncJobID: "sync-1", TaskID: "task-1", ClaimEpoch: 4}
}

func newSyncFailureTask() *db.Task {
	return &db.Task{ID: "task-1", SyncJobID: "sync-1", PassGeneration: 3}
}

func newSyncFailureJob(status string) *db.SyncJob {
	return &db.SyncJob{ID: "sync-1", RunGeneration: 3, Status: status, SourceProvider: "nextcloud", TargetProvider: "nextcloud"}
}

func requireCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("store calls = %v, want %v", got, want)
	}
}

func TestHandleSyncTaskFailureWritesThroughTheSyncStore(t *testing.T) {
	t.Run("a superseded pass generation writes nothing", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: &db.SyncJob{ID: "sync-1", RunGeneration: 4, Status: "RUNNING"}}
		syncFailureProcessor(syncs, newSyncFailureTask()).handleSyncTaskFailure(context.Background(), syncFailurePayload(), errors.New("boom"))
		requireCalls(t, syncs.calls)
	})

	t.Run("a paused job cancels the task durably", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("PAUSED")}
		syncFailureProcessor(syncs, newSyncFailureTask()).handleSyncTaskFailure(context.Background(), syncFailurePayload(), errors.New("boom"))
		requireCalls(t, syncs.calls, "claimed:CANCELLED")
		if syncs.claimed[0].ClaimEpoch != 4 {
			t.Errorf("claim epoch = %d, want the payload's 4", syncs.claimed[0].ClaimEpoch)
		}
	})

	t.Run("a shutdown returns the task to pending", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("RUNNING")}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		syncFailureProcessor(syncs, newSyncFailureTask()).handleSyncTaskFailure(ctx, syncFailurePayload(), context.Canceled)
		requireCalls(t, syncs.calls, "claimed:PENDING")
	})

	t.Run("a first ordinary failure schedules a retry", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("RUNNING")}
		syncFailureProcessor(syncs, newSyncFailureTask()).handleSyncTaskFailure(context.Background(), syncFailurePayload(), errors.New("transient provider hiccup"))
		requireCalls(t, syncs.calls, "claimed:FAILED")
		got := syncs.claimed[0]
		if got.Attempts != 1 || !got.NextRetryAt.Valid {
			t.Errorf("task = attempts %d, next retry valid %v; want 1 attempt and a scheduled retry", got.Attempts, got.NextRetryAt.Valid)
		}
	})

	t.Run("exhausted attempts record a terminal failure and its counter", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("RUNNING")}
		task := newSyncFailureTask()
		task.Attempts = 2
		syncFailureProcessor(syncs, task).handleSyncTaskFailure(context.Background(), syncFailurePayload(), errors.New("still broken"))
		requireCalls(t, syncs.calls, "terminal:FAILED:1/0/0/1/0")
		if syncs.terminal[0].NextRetryAt != (sql.NullTime{}) {
			t.Errorf("terminal failure kept a retry time: %+v", syncs.terminal[0].NextRetryAt)
		}
	})

	t.Run("repeated connection loss pauses the job and requeues the task", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("RUNNING")}
		p := syncFailureProcessor(syncs, newSyncFailureTask())
		// Two earlier losses leave this one at the escalation threshold.
		for i := 1; i < connLossEscalationThreshold; i++ {
			p.recordConnLoss("sync-1")
		}

		p.handleSyncTaskFailure(context.Background(), syncFailurePayload(), &net.OpError{Op: "dial", Err: errors.New("connection refused")})

		requireCalls(t, syncs.calls, "job:3:PAUSED_CONNECTION_LOSS", "claimed:PENDING")
	})

	t.Run("an authentication failure fails the pass and cancels the rest", func(t *testing.T) {
		syncs := &failureFakeSyncs{job: newSyncFailureJob("RUNNING"), cancelled: 5}
		syncFailureProcessor(syncs, newSyncFailureTask()).handleSyncTaskFailure(context.Background(), syncFailurePayload(), fmt.Errorf("login: %w", storage.ErrAuth))
		requireCalls(t, syncs.calls,
			"job:3:FAILED+msg",
			"terminal:FAILED:1/0/0/1/0",
			"cancel-pending:3",
			"progress:3:0/0/0/5/0",
		)
	})
}
