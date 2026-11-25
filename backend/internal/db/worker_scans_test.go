package db

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"
)

func TestWorkerMaintenanceScansKeepTheirStatusGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("failed task retry re-enqueues migration and sync tasks in one transaction", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		now := time.Now()
		requeued, err := NewTaskStore(database).RequeueDueFailedTasks(ctx, now)
		if err != nil {
			t.Fatalf("RequeueDueFailedTasks() error: %v", err)
		}
		if requeued != 2 {
			t.Fatalf("requeued = %d, want 1 per statement from the stub", requeued)
		}
		if len(rec.statements) != 2 {
			t.Fatalf("recorded %d statements, want 2: %+v", len(rec.statements), rec.statements)
		}
		for i, fragment := range []string{"FROM migrations m", "t.pass_generation = sj.run_generation"} {
			got := rec.statements[i]
			if !strings.Contains(got.query, fragment) || !strings.Contains(got.query, "t.next_retry_at <= $1") {
				t.Errorf("statement %d = %q, want %q and a due-retry guard", i, got.query, fragment)
			}
			if len(got.args) != 1 || got.args[0] != driver.Value(now) {
				t.Errorf("statement %d args = %v, want the cutoff time", i, got.args)
			}
		}
	})

	t.Run("orphaned RUNNING tasks are only reset for active parents", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		count, err := NewTaskStore(database).RequeueOrphanedRunningTasks(ctx)
		if err != nil {
			t.Fatalf("RequeueOrphanedRunningTasks() error: %v", err)
		}
		if count != 1 {
			t.Fatalf("count = %d, want 1", count)
		}
		requireSingleStatement(t, rec, "t.status = 'RUNNING'")
		for _, fragment := range []string{"INTERVAL '10 minutes'", "m.status IN ('RUNNING', 'INDEXING')", "sj.status IN ('RUNNING', 'INDEXING')"} {
			if !strings.Contains(rec.statements[0].query, fragment) {
				t.Errorf("query is missing guard %q", fragment)
			}
		}
	})

	t.Run("status listings select only their own status", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		ids, err := NewMigrationStore(database).ListRunningMigrationIDs(ctx)
		if err != nil || len(ids) != 1 || ids[0] != "row-id" {
			t.Fatalf("ListRunningMigrationIDs() = %v, %v", ids, err)
		}
		requireSingleStatement(t, rec, "m.status = 'RUNNING'")

		database, rec = openRecordingDB(t)
		if _, err := NewMigrationStore(database).ListVerifyingMigrationIDs(ctx); err != nil {
			t.Fatalf("ListVerifyingMigrationIDs() error: %v", err)
		}
		requireSingleStatement(t, rec, "FROM migrations WHERE status = 'VERIFYING'")

		database, rec = openRecordingDB(t)
		if _, err := NewSyncStore(database).ListVerifyingSyncJobIDs(ctx); err != nil {
			t.Fatalf("ListVerifyingSyncJobIDs() error: %v", err)
		}
		requireSingleStatement(t, rec, "FROM sync_jobs WHERE status = 'VERIFYING'")
	})

	t.Run("running sync passes carry their run generation", func(t *testing.T) {
		database, rec := openRecordingDB(t)
		rec.queryColumns = []string{"id", "run_generation"}
		rec.queryRow = []driver.Value{"sync-1", int64(7)}
		passes, err := NewSyncStore(database).ListRunningSyncPasses(ctx)
		if err != nil {
			t.Fatalf("ListRunningSyncPasses() error: %v", err)
		}
		requireSingleStatement(t, rec, "sj.status = 'RUNNING'")
		if len(passes) != 1 || passes[0] != (SyncPassRef{ID: "sync-1", Generation: 7}) {
			t.Fatalf("passes = %+v", passes)
		}
	})

	t.Run("connection-loss candidates are paged by cursor and limit", func(t *testing.T) {
		cols := []string{"id", "user_id", "su", "sun", "sp", "tu", "tun", "tp", "sprov", "tprov", "updated_at"}
		updated := time.Now()
		row := []driver.Value{"mig-1", "user-1", "https://s", "su", "spw", "https://t", "tu", "tpw", "nextcloud", "webdav", updated}
		want := ConnectionRecoveryCandidate{ID: "mig-1", UserID: "user-1", SourceURL: "https://s", SourceUsername: "su", SourcePassword: "spw", TargetURL: "https://t", TargetUsername: "tu", TargetPassword: "tpw", SourceProvider: "nextcloud", TargetProvider: "webdav", UpdatedAt: updated}

		database, rec := openRecordingDB(t)
		rec.queryColumns, rec.queryRow = cols, row
		got, err := NewMigrationStore(database).ListPausedConnectionLossMigrations(ctx, "cursor-1", 30)
		if err != nil {
			t.Fatalf("ListPausedConnectionLossMigrations() error: %v", err)
		}
		requireSingleStatement(t, rec, "FROM migrations", "cursor-1", int64(30))
		if !strings.Contains(rec.statements[0].query, "status = 'PAUSED_CONNECTION_LOSS'") || !strings.Contains(rec.statements[0].query, "ORDER BY (id::text <= $1), id") {
			t.Errorf("query = %s", rec.statements[0].query)
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("got = %+v, want %+v", got, want)
		}

		database, rec = openRecordingDB(t)
		rec.queryColumns, rec.queryRow = cols, row
		got, err = NewSyncStore(database).ListPausedConnectionLossSyncJobs(ctx, "cursor-2", 10)
		if err != nil {
			t.Fatalf("ListPausedConnectionLossSyncJobs() error: %v", err)
		}
		requireSingleStatement(t, rec, "FROM sync_jobs", "cursor-2", int64(10))
		if len(got) != 1 || got[0] != want {
			t.Fatalf("got = %+v, want %+v", got, want)
		}
	})
}
