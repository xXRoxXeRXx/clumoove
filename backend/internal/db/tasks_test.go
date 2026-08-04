package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateTaskPersistsDirectSelectionSourceHash(t *testing.T) {
	database, state := newCreateTaskTestDB(t)
	task := &Task{
		MigrationID:  "migration-1",
		ResourceType: "files",
		FilePath:     "/important.iso",
		FileSize:     1024,
		SourceHash:   sql.NullString{String: "sha256:provider-checksum", Valid: true},
		Status:       "PENDING",
		Metadata:     json.RawMessage(`{}`),
	}

	if _, err := CreateTask(database, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	const wantInsert = "INSERT INTO tasks (migration_id, resource_type, file_path, file_size, status, metadata, source_hash) VALUES"
	if normalized := strings.Join(strings.Fields(state.query), " "); !strings.Contains(normalized, wantInsert) {
		t.Fatalf("CreateTask() INSERT columns = %q, want ordered source_hash column", normalized)
	}
	if len(state.args) != 7 {
		t.Fatalf("CreateTask() argument count = %d, want 7", len(state.args))
	}
	if got := state.args[6].Value; got != task.SourceHash.String {
		t.Fatalf("CreateTask() source_hash argument = %#v, want %#v", got, task.SourceHash.String)
	}
}

func TestCreateTaskWithoutSourceHashPersistsNull(t *testing.T) {
	database, state := newCreateTaskTestDB(t)
	task := &Task{
		MigrationID:  "migration-2",
		ResourceType: "files",
		FilePath:     "/other.txt",
		FileSize:     512,
		Status:       "PENDING",
		Metadata:     json.RawMessage(`{}`),
	}

	if _, err := CreateTask(database, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}
	if len(state.args) != 7 {
		t.Fatalf("CreateTask() argument count = %d, want 7", len(state.args))
	}
	if got := state.args[6].Value; got != nil {
		t.Fatalf("CreateTask() source_hash argument = %#v, want nil", got)
	}
}

func TestCreateMigrationTaskWhileIndexingPersistsSourceHash(t *testing.T) {
	database, state := newCreateTaskTestDB(t)
	task := &Task{
		MigrationID:  "migration-3",
		ResourceType: "files",
		FilePath:     "/important.iso",
		FileSize:     2048,
		SourceHash:   sql.NullString{String: "sha256:provider-checksum", Valid: true},
		Status:       "PENDING",
		Metadata:     json.RawMessage(`{}`),
	}

	if _, err := CreateMigrationTaskWhileIndexing(database, task); err != nil {
		t.Fatalf("CreateMigrationTaskWhileIndexing() error = %v", err)
	}
	const wantInsert = "INSERT INTO tasks (migration_id, resource_type, file_path, file_size, status, metadata, source_hash) SELECT"
	if normalized := strings.Join(strings.Fields(state.query), " "); !strings.Contains(normalized, wantInsert) {
		t.Fatalf("CreateMigrationTaskWhileIndexing() INSERT columns = %q, want ordered source_hash column", normalized)
	}
	if len(state.args) != 7 {
		t.Fatalf("CreateMigrationTaskWhileIndexing() argument count = %d, want 7", len(state.args))
	}
	if got := state.args[6].Value; got != task.SourceHash.String {
		t.Fatalf("CreateMigrationTaskWhileIndexing() source_hash argument = %#v, want %#v", got, task.SourceHash.String)
	}
}

func TestBulkCreateMigrationTasksWhileIndexingPersistsSourceHash(t *testing.T) {
	database, state := newCreateTaskTestDB(t)
	task := &Task{
		MigrationID:  "migration-3",
		ResourceType: "files",
		FilePath:     "/folder/important.iso",
		FileSize:     2048,
		SourceHash:   sql.NullString{String: "sha256:provider-checksum", Valid: true},
		Status:       "PENDING",
		Metadata:     json.RawMessage(`{}`),
	}

	created, err := BulkCreateMigrationTasksWhileIndexing(context.Background(), database, task.MigrationID, []*Task{task})
	if err != nil {
		t.Fatalf("BulkCreateMigrationTasksWhileIndexing() error = %v", err)
	}
	if !created {
		t.Fatal("BulkCreateMigrationTasksWhileIndexing() created = false, want true")
	}
	const wantInsert = "INSERT INTO tasks (migration_id, resource_type, file_path, file_size, source_hash, status, metadata)"
	if normalized := strings.Join(strings.Fields(state.execQuery), " "); !strings.Contains(normalized, wantInsert) {
		t.Fatalf("BulkCreateMigrationTasksWhileIndexing() INSERT columns = %q, want ordered source_hash column", normalized)
	}
	if len(state.execArgs) != 7 {
		t.Fatalf("BulkCreateMigrationTasksWhileIndexing() argument count = %d, want 7", len(state.execArgs))
	}
	if got := state.execArgs[4].Value; got != task.SourceHash.String {
		t.Fatalf("BulkCreateMigrationTasksWhileIndexing() source_hash argument = %#v, want %#v", got, task.SourceHash.String)
	}
}

var createTaskTestDriverID atomic.Uint64

type createTaskDBState struct {
	query      string
	args       []driver.NamedValue
	execQuery  string
	execArgs   []driver.NamedValue
	execResult driver.Result
}

func newCreateTaskTestDB(t *testing.T) (*sql.DB, *createTaskDBState) {
	t.Helper()
	state := &createTaskDBState{}
	driverName := "create-task-test-" + strconv.FormatUint(createTaskTestDriverID.Add(1), 10)
	sql.Register(driverName, createTaskDriver{state: state})
	database, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database, state
}

type createTaskDriver struct{ state *createTaskDBState }

func (d createTaskDriver) Open(string) (driver.Conn, error) {
	return createTaskConn{state: d.state}, nil
}

type createTaskConn struct{ state *createTaskDBState }

func (createTaskConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (createTaskConn) Close() error                        { return nil }
func (createTaskConn) Begin() (driver.Tx, error)           { return createTaskTx{}, nil }
func (createTaskConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return createTaskTx{}, nil
}
func (c createTaskConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.query = query
	c.state.args = args
	return &createTaskRows{}, nil
}
func (c createTaskConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.execQuery = query
	c.state.execArgs = args
	if c.state.execResult != nil {
		return c.state.execResult, nil
	}
	return driver.RowsAffected(1), nil
}

func TestUpdateClaimedTaskMetadataFencesClaimAndPersistsMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		wantErr  error
	}{
		{name: "active claim", affected: 1},
		{name: "wrong claim epoch", affected: 0, wantErr: sql.ErrNoRows},
		{name: "task already terminal", affected: 0, wantErr: sql.ErrNoRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, state := newCreateTaskTestDB(t)
			state.execResult = driver.RowsAffected(tc.affected)
			metadata := json.RawMessage(`{"custom_props":{"immich_asset_id":"source","immich_target_asset_id":"target"}}`)
			err := UpdateClaimedTaskMetadata(database, context.Background(), "task-1", 7, metadata)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("UpdateClaimedTaskMetadata() error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil {
				if !strings.Contains(state.execQuery, "status = 'RUNNING' AND claim_epoch = $3") {
					t.Fatalf("fenced query = %q", state.execQuery)
				}
				if got := string(state.execArgs[0].Value.([]byte)); got != string(metadata) {
					t.Fatalf("metadata argument = %q, want %q", got, metadata)
				}
			}
		})
	}
}

type createTaskTx struct{}

func (createTaskTx) Commit() error   { return nil }
func (createTaskTx) Rollback() error { return nil }

type createTaskRows struct{ returned bool }

func (r *createTaskRows) Columns() []string { return []string{"id", "created_at", "updated_at"} }
func (r *createTaskRows) Close() error      { return nil }
func (r *createTaskRows) Next(dest []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	r.returned = true
	now := time.Now()
	dest[0], dest[1], dest[2] = "task-1", now, now
	return nil
}

func TestIndexingErrorMessage(t *testing.T) {
	tests := []struct {
		name  string
		input IndexingErrorInput
		want  string
	}{
		{name: "persists explicit message", input: IndexingErrorInput{ErrorMessage: "failed to inspect path"}, want: "failed to inspect path"},
		{name: "falls back to error", input: IndexingErrorInput{Err: errors.New("listing failed")}, want: "listing failed"},
		{name: "prefers explicit message", input: IndexingErrorInput{ErrorMessage: "safe message", Err: errors.New("raw error")}, want: "safe message"},
		{name: "sanitizes fallback error", input: IndexingErrorInput{Err: errors.New("failed: https://user:password@example.com")}, want: "failed: https://***:***@example.com"},
		{name: "empty input returns empty string", input: IndexingErrorInput{}, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := indexingErrorMessage(test.input); got != test.want {
				t.Errorf("indexingErrorMessage() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDisplayTaskName(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		metadata string
		want     string
	}{
		{
			name:     "uses Immich original filename",
			filePath: "/Timeline/eaf7957e-0601-428a-895d-55a376194d5a",
			metadata: `{"custom_props":{"immich_filename":"2026-07-urlaub.jpg"}}`,
			want:     "2026-07-urlaub.jpg",
		},
		{
			name:     "uses picker name",
			filePath: "/picker/opaque-id",
			metadata: `{"name":"document.pdf"}`,
			want:     "document.pdf",
		},
		{
			name:     "falls back to path",
			filePath: "/files/document.pdf",
			metadata: `{}`,
			want:     "/files/document.pdf",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := displayTaskName(test.filePath, json.RawMessage(test.metadata))
			if got != test.want {
				t.Errorf("displayTaskName(%q, %s) = %q, want %q", test.filePath, test.metadata, got, test.want)
			}
		})
	}
}
