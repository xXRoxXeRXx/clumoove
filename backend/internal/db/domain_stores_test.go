package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// Recording driver: a stand-in for PostgreSQL that captures statements so the
// domain stores can be checked for faithful delegation without a database.

type recordedStatement struct {
	query string
	args  []driver.Value
}

type recordingDriver struct{ statements []recordedStatement }

type recordingConn struct{ owner *recordingDriver }

type recordingStmt struct {
	conn  *recordingConn
	query string
}

type singleIDRows struct{ done bool }

var recordingDriverSeq atomic.Int64

func (d *recordingDriver) Open(string) (driver.Conn, error) { return &recordingConn{owner: d}, nil }

func (c *recordingConn) Prepare(query string) (driver.Stmt, error) {
	return &recordingStmt{conn: c, query: query}, nil
}
func (c *recordingConn) Close() error              { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) { return nil, driver.ErrSkip }

func (s *recordingStmt) Close() error  { return nil }
func (s *recordingStmt) NumInput() int { return -1 }
func (s *recordingStmt) record(args []driver.Value) {
	s.conn.owner.statements = append(s.conn.owner.statements, recordedStatement{query: s.query, args: args})
}
func (s *recordingStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.record(args)
	return driver.RowsAffected(1), nil
}
func (s *recordingStmt) Query(args []driver.Value) (driver.Rows, error) {
	s.record(args)
	return &singleIDRows{}, nil
}

func (r *singleIDRows) Columns() []string { return []string{"id"} }
func (r *singleIDRows) Close() error      { return nil }
func (r *singleIDRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = "row-id"
	return nil
}

func openRecordingDB(t *testing.T) (*sql.DB, *recordingDriver) {
	t.Helper()
	rec := &recordingDriver{}
	name := "recording-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + string(rune('a'+recordingDriverSeq.Add(1)%26))
	sql.Register(name, rec)
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("open recording db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, rec
}

func requireSingleStatement(t *testing.T, rec *recordingDriver, wantQuery string, wantArgs ...any) {
	t.Helper()
	if len(rec.statements) != 1 {
		t.Fatalf("recorded %d statements, want 1: %+v", len(rec.statements), rec.statements)
	}
	got := rec.statements[0]
	if !strings.Contains(got.query, wantQuery) {
		t.Errorf("query = %q, want it to contain %q", got.query, wantQuery)
	}
	if len(got.args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", got.args, wantArgs)
	}
	for i, want := range wantArgs {
		if got.args[i] != driver.Value(want) {
			t.Errorf("arg[%d] = %#v, want %#v", i, got.args[i], want)
		}
	}
}

func TestBackupStoreDelegatesToBackupPersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store BackupStore = NewBackupStore(database)

	if err := store.TouchBackupRunContext(context.Background(), "job-1", 7, "run-1"); err != nil {
		t.Fatalf("TouchBackupRunContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "UPDATE backup_runs", "run-1", "job-1", int64(7))
}

func TestRestoreStoreDelegatesToRestorePersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store RestoreStore = NewRestoreStore(database)

	if err := store.ExpireRestorePreviewsContext(context.Background()); err != nil {
		t.Fatalf("ExpireRestorePreviewsContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "UPDATE restore_previews")
}

func TestRestoreStoreForwardsClaimEpoch(t *testing.T) {
	database, rec := openRecordingDB(t)
	store := NewRestoreStore(database)

	if err := store.HeartbeatRestoreItemContext(context.Background(), "item-1", "run-1", "worker-1", 4); err != nil {
		t.Fatalf("HeartbeatRestoreItemContext() error: %v", err)
	}

	if len(rec.statements) != 1 {
		t.Fatalf("recorded %d statements, want 1", len(rec.statements))
	}
	found := false
	for _, arg := range rec.statements[0].args {
		if arg == driver.Value(int64(4)) {
			found = true
		}
	}
	if !found {
		t.Errorf("claim epoch 4 not forwarded to SQL, args = %v", rec.statements[0].args)
	}
}

func TestSyncStoreDelegatesToSyncPersistence(t *testing.T) {
	database, rec := openRecordingDB(t)
	var store SyncStore = NewSyncStore(database)

	updated, err := store.UpdateSyncJobTotals("sync-1", 3, 10, 2048)
	if err != nil {
		t.Fatalf("UpdateSyncJobTotals() error: %v", err)
	}
	if !updated {
		t.Error("UpdateSyncJobTotals() = false, want true when the row is returned")
	}

	requireSingleStatement(t, rec, "UPDATE sync_jobs", int64(10), int64(2048), "sync-1", int64(3))
}

func TestSharedBackupRepositoryCredentialsAreReachableFromBothDomains(t *testing.T) {
	database, _ := openRecordingDB(t)

	var _ BackupRepositoryCredentials = NewBackupStore(database)
	var _ BackupRepositoryCredentials = NewRestoreStore(database)
}

func TestDomainStoreConstructorsRejectNilDatabase(t *testing.T) {
	for name, construct := range map[string]func(){
		"backup":  func() { NewBackupStore(nil) },
		"restore": func() { NewRestoreStore(nil) },
		"sync":    func() { NewSyncStore(nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("New%sStore(nil) did not panic", name)
				}
			}()
			construct()
		})
	}
}
