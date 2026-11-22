package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"backend/internal/db"
	"backend/internal/queue"
)

// claimFakeMigrations overrides only the migration lookup claimMigrationTask
// performs; any other call panics through the nil embedded interface.
type claimFakeMigrations struct {
	db.MigrationStore
	migration *db.Migration
	err       error
}

func (f *claimFakeMigrations) GetMigration(string) (*db.Migration, error) { return f.migration, f.err }

// claimFakeTasks records the fenced task transitions and serves one task row.
type claimFakeTasks struct {
	db.TaskStore
	task        *db.Task
	getErr      error
	transitions []string
	leases      []db.Lease
}

func (f *claimFakeTasks) GetTask(string) (*db.Task, error) { return f.task, f.getErr }

func (f *claimFakeTasks) TransitionClaimedTask(_ context.Context, lease db.Lease, status string) error {
	f.transitions = append(f.transitions, status)
	f.leases = append(f.leases, lease)
	return nil
}

func claimPayload() *queue.Payload {
	return &queue.Payload{MigrationID: "mig-1", TaskID: "task-1", ClaimEpoch: 7}
}

func TestClaimMigrationTaskAppliesTheMatchingFencedTransition(t *testing.T) {
	tests := []struct {
		status         string
		wantTransition string
		wantHandled    bool
		wantErr        bool
	}{
		{"PAUSED", "PENDING", true, false},
		{"PAUSED_CONNECTION_LOSS", "PENDING", true, false},
		{"COMPLETED", "SKIPPED", true, false},
		{"COMPLETED_WITH_ERRORS", "SKIPPED", true, false},
		{"FAILED", "SKIPPED", true, false},
		{"CANCELLED", "CANCELLED", true, false},
		{"VERIFYING", "PENDING", false, true},
		{"RUNNING", "", false, false},
		{"INDEXING", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			tasks := &claimFakeTasks{task: &db.Task{ID: "task-1"}}
			p := &Processor{
				workerID:   "w-test",
				migrations: &claimFakeMigrations{migration: &db.Migration{ID: "mig-1", Status: tt.status}},
				tasks:      tasks,
			}

			_, claimed, handled, err := p.claimMigrationTask(context.Background(), claimPayload())

			if (err != nil) != tt.wantErr {
				t.Fatalf("claimMigrationTask() error = %v, wantErr %v", err, tt.wantErr)
			}
			if handled != tt.wantHandled {
				t.Errorf("handled = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantTransition == "" {
				if len(tasks.transitions) != 0 {
					t.Errorf("transitions = %v, want none for a runnable migration", tasks.transitions)
				}
				if claimed == nil || claimed.task.ClaimEpoch != 7 {
					t.Fatalf("claimed task = %+v, want the task stamped with claim epoch 7", claimed)
				}
				return
			}
			if len(tasks.transitions) != 1 || tasks.transitions[0] != tt.wantTransition {
				t.Fatalf("transitions = %v, want [%s]", tasks.transitions, tt.wantTransition)
			}
			if tasks.leases[0] != (db.Lease{ID: "task-1", Epoch: 7}) {
				t.Errorf("lease = %+v, want the payload's task and claim epoch", tasks.leases[0])
			}
			if claimed != nil {
				t.Errorf("claimed = %+v, want nil when the task was not run", claimed)
			}
		})
	}
}

func TestClaimMigrationTaskReportsLookupFailures(t *testing.T) {
	t.Run("missing migration", func(t *testing.T) {
		p := &Processor{migrations: &claimFakeMigrations{err: sql.ErrNoRows}, tasks: &claimFakeTasks{}}
		if _, _, _, err := p.claimMigrationTask(context.Background(), claimPayload()); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("claimMigrationTask() error = %v, want it to wrap sql.ErrNoRows", err)
		}
	})
	t.Run("missing task row", func(t *testing.T) {
		taskErr := errors.New("task gone")
		p := &Processor{
			migrations: &claimFakeMigrations{migration: &db.Migration{ID: "mig-1", Status: "RUNNING"}},
			tasks:      &claimFakeTasks{getErr: taskErr},
		}
		if _, _, _, err := p.claimMigrationTask(context.Background(), claimPayload()); !errors.Is(err, taskErr) {
			t.Fatalf("claimMigrationTask() error = %v, want it to wrap %v", err, taskErr)
		}
	})
}
