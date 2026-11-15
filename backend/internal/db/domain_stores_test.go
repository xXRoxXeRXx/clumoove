package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Recording driver: a stand-in for PostgreSQL that captures statements so the
// domain stores can be checked for faithful delegation without a database.

type recordedStatement struct {
	query string
	args  []driver.Value
}

type recordingDriver struct {
	statements []recordedStatement
	// queryColumns/queryRow override the default single-column row returned by queries.
	queryColumns []string
	queryRow     []driver.Value
}

type recordingConn struct{ owner *recordingDriver }

type recordingStmt struct {
	conn  *recordingConn
	query string
}

type recordedRows struct {
	done    bool
	columns []string
	row     []driver.Value
}

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
	if d := s.conn.owner; d.queryColumns != nil {
		return &recordedRows{columns: d.queryColumns, row: d.queryRow}, nil
	}
	return &recordedRows{}, nil
}

func (r *recordedRows) Columns() []string {
	if r.columns != nil {
		return r.columns
	}
	return []string{"id"}
}
func (r *recordedRows) Close() error { return nil }
func (r *recordedRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if r.row != nil {
		copy(dest, r.row)
		return nil
	}
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

func TestRestoreStoreLoadsRestoreRunTargetConnection(t *testing.T) {
	database, rec := openRecordingDB(t)
	rec.queryColumns = []string{"backup_job_id", "target_provider", "target_url", "target_username", "conflict_strategy", "bandwidth_mbps", "target_password", "refresh", "expires", "mega_session", "mega_master"}
	expires := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rec.queryRow = []driver.Value{"backup-1", "nextcloud", "https://cloud.example", "alice", "RENAME", int64(25), "enc-pass", "enc-refresh", expires, nil, "enc-master"}
	var store RestoreStore = NewRestoreStore(database)

	got, err := store.GetRestoreRunTargetConnectionContext(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRestoreRunTargetConnectionContext() error: %v", err)
	}

	requireSingleStatement(t, rec, "FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.id = $1", "run-1")
	want := &RestoreRunTargetConnection{
		BackupJobID: "backup-1", TargetProvider: "nextcloud", TargetURL: "https://cloud.example", TargetUsername: "alice",
		ConflictStrategy: "RENAME", BandwidthMbps: 25, TargetPasswordEncrypted: "enc-pass",
		TargetRefreshEncrypted:       sql.NullString{String: "enc-refresh", Valid: true},
		TargetTokenExpiresAt:         sql.NullTime{Time: expires, Valid: true},
		TargetMegaSessionEncrypted:   sql.NullString{},
		TargetMegaMasterKeyEncrypted: sql.NullString{String: "enc-master", Valid: true},
	}
	if *got != *want {
		t.Errorf("connection = %+v, want %+v", *got, *want)
	}
}
