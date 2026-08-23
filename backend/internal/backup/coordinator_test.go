package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/backuprepo"
	"backend/internal/db"
	"backend/internal/observability"
	"backend/internal/storage"
)

func TestNewCoordinatorValidatesPackWriterLimit(t *testing.T) {
	for _, limit := range []int{0, 5} {
		if _, err := NewCoordinator(&sql.DB{}, "key", limit); err == nil {
			t.Errorf("NewCoordinator(limit=%d) succeeded", limit)
		}
	}
	coordinator, err := NewCoordinator(&sql.DB{}, "key", 2)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if cap(coordinator.packWriterSlots) != 2 {
		t.Fatalf("pack writer capacity = %d, want 2", cap(coordinator.packWriterSlots))
	}
}

func TestRepositoryPath(t *testing.T) {
	tests := []struct {
		name string
		root string
		want string
		err  bool
	}{
		{name: "nested repository", root: "clumoove/repositories/123", want: "clumoove/repositories/123/packs/a.pack"},
		{name: "absolute repository", root: "/backups/123", want: "/backups/123/packs/a.pack"},
		{name: "reject traversal", root: "backups/../other", err: true},
		{name: "reject backslash", root: `backups\other`, err: true},
		{name: "reject empty", root: "", err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositoryPath(tt.root, "packs", "a.pack")
			if (err != nil) != tt.err {
				t.Fatalf("repositoryPath() error = %v, want error=%v", err, tt.err)
			}
			if got != tt.want {
				t.Fatalf("repositoryPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeRemotePath(t *testing.T) {
	for _, value := range []string{"backup/../outside", `backup\outside`, ""} {
		if err := safeRemotePath(value); err == nil {
			t.Errorf("safeRemotePath(%q) succeeded", value)
		}
	}
}

func TestFailureCodeForState(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  string
	}{
		{name: "scan", state: "SCANNING", want: "BACKUP_SCAN_FAILED"},
		{name: "run", state: "RUNNING", want: "BACKUP_RUN_FAILED"},
		{name: "verify", state: "VERIFYING", want: "BACKUP_VERIFICATION_FAILED"},
		{name: "unknown defaults to run", state: "UNKNOWN", want: "BACKUP_RUN_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := failureCodeForState(test.state); got != test.want {
				t.Fatalf("failureCodeForState(%q) = %q, want %q", test.state, got, test.want)
			}
		})
	}
}

func TestPackBuilderDeduplicatesPendingBlocksAndPreservesCatalogIDs(t *testing.T) {
	builder := newPackBuilder(nil, nil, nil)
	entry := backuprepo.Entry{Data: []byte("repeated backup data")}
	entry.Hash = sha256.Sum256(entry.Data)
	hashKey := string(entry.Hash[:])

	if err := builder.add(context.Background(), entry); err != nil {
		t.Fatalf("first add() error = %v", err)
	}
	if err := builder.add(context.Background(), entry); err != nil {
		t.Fatalf("duplicate add() error = %v", err)
	}
	if len(builder.entries) != 1 {
		t.Fatalf("pending entries = %d, want 1", len(builder.entries))
	}
	if !builder.hasPendingBlock(hashKey) {
		t.Fatal("pending block was not tracked")
	}

	builder.ids[hashKey] = "catalog-block-id"
	if got := builder.resolveBlockID(hashKey); got != "catalog-block-id" {
		t.Fatalf("resolveBlockID(hash) = %q, want catalog ID", got)
	}
	if got := builder.resolveBlockID("existing-catalog-block-id"); got != "existing-catalog-block-id" {
		t.Fatalf("resolveBlockID(existing ID) = %q, want unchanged ID", got)
	}
}

func TestBackupRunLoggerIncludesCorrelationFields(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&output, nil)))
	job := &db.BackupJob{ID: "backup-job"}
	run := &db.BackupRun{ID: "backup-run", Generation: 7}

	backupRunLogger(ctx, job, run).Info("backup_run_started")

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log record: %v", err)
	}
	for key, want := range map[string]any{
		"component":     "backup",
		"backup_job_id": "backup-job",
		"backup_run_id": "backup-run",
		"generation":    float64(7),
	} {
		if got := record[key]; got != want {
			t.Errorf("log %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestBackupFailureAttrsIncludesCodeAndClassifiedCause(t *testing.T) {
	attrs := backupFailureAttrs("BACKUP_CONNECTION_FAILED", errors.New("connection refused"))
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		values[attr.Key] = attr.Value.String()
	}
	if values["error_code"] != "BACKUP_CONNECTION_FAILED" {
		t.Errorf("error_code = %q", values["error_code"])
	}
	if values["error"] == "" {
		t.Error("error attribute is missing")
	}
	if values["error_kind"] != "network" {
		t.Errorf("error_kind = %q, want network", values["error_kind"])
	}
}

type mockBackupTarget struct {
	listings map[string][]storage.CloudResource
}

func (m *mockBackupTarget) Close() error                              { return nil }
func (m *mockBackupTarget) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockBackupTarget) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	return storage.CloudResource{}, errors.New("not implemented")
}
func (m *mockBackupTarget) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (m *mockBackupTarget) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", errors.New("not implemented")
}
func (m *mockBackupTarget) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return errors.New("not implemented")
}
func (m *mockBackupTarget) SupportsAtomicRename() bool { return true }
func (m *mockBackupTarget) VerificationMode() storage.VerificationMode {
	return storage.VerificationSizeOnly
}
func (m *mockBackupTarget) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	return m.listings[dirPath], nil
}
func (m *mockBackupTarget) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	return false, 0, nil
}

func TestEnsureDedicatedTarget(t *testing.T) {
	const (
		targetDir    = "/backups"
		repoID       = "38256494-2dce-416a-b4fd-46f5b25de01c"
		container    = "/backups/.clumoove-backup"
		repoRoot     = "/backups/.clumoove-backup/38256494-2dce-416a-b4fd-46f5b25de01c"
	)

	t.Run("empty target directory succeeds", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() error = %v, want nil", err)
		}
	})

	t.Run("webdav trailing slashes on directories succeed", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container + "/", Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot + "/", Name: repoID, IsDir: true},
				},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() with trailing slashes error = %v, want nil", err)
		}
	})

	t.Run("clean paths without trailing slashes succeed", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot, Name: repoID, IsDir: true},
				},
			},
		}
		if err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID); err != nil {
			t.Fatalf("ensureDedicatedTarget() clean paths error = %v, want nil", err)
		}
	})

	t.Run("non-empty directory with unexpected file fails", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: "/backups/existing.docx", Name: "existing.docx", IsDir: false},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory is not empty" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory is not empty'", err)
		}
	})

	t.Run("container with multiple repos fails dedicated check", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: repoRoot, Name: repoID, IsDir: true},
					{Path: container + "/other-repo", Name: "other-repo", IsDir: true},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory is not dedicated" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory is not dedicated'", err)
		}
	})

	t.Run("container belonging to another repo fails", func(t *testing.T) {
		target := &mockBackupTarget{
			listings: map[string][]storage.CloudResource{
				targetDir: {
					{Path: container, Name: ".clumoove-backup", IsDir: true},
				},
				container: {
					{Path: container + "/other-repo-id", Name: "other-repo-id", IsDir: true},
				},
			},
		}
		err := ensureDedicatedTarget(context.Background(), target, targetDir, repoID)
		if err == nil || err.Error() != "backup target directory belongs to another repository" {
			t.Fatalf("ensureDedicatedTarget() error = %v, want 'backup target directory belongs to another repository'", err)
		}
	})
}

func TestIsRetryableVerifyReadError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "EOF is retryable", err: io.EOF, want: true},
		{name: "UnexpectedEOF is retryable", err: io.ErrUnexpectedEOF, want: true},
		{name: "Corrupt data error is permanent", err: errors.New("invalid pack checksum"), want: false},
		{name: "Nil error is not retryable", err: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableVerifyReadError(tt.err); got != tt.want {
				t.Fatalf("isRetryableVerifyReadError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type mockFileRecord struct {
	data  []byte
	mtime time.Time
	isDir bool
}

type mockTrackedSource struct {
	files          map[string]mockFileRecord
	downloadCounts map[string]int
	inspectCounts  map[string]int
}

func newMockTrackedSource() *mockTrackedSource {
	return &mockTrackedSource{
		files:          make(map[string]mockFileRecord),
		downloadCounts: make(map[string]int),
		inspectCounts:  make(map[string]int),
	}
}

func (m *mockTrackedSource) Close() error                              { return nil }
func (m *mockTrackedSource) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockTrackedSource) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	m.inspectCounts[path]++
	rec, ok := m.files[path]
	if !ok {
		return storage.CloudResource{}, errors.New("not found")
	}
	return storage.CloudResource{
		Path:         path,
		IsDir:        rec.isDir,
		Size:         int64(len(rec.data)),
		LastModified: rec.mtime,
	}, nil
}
func (m *mockTrackedSource) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	m.downloadCounts[filePath]++
	rec, ok := m.files[filePath]
	if !ok {
		return nil, errors.New("file not found")
	}
	return io.NopCloser(bytes.NewReader(rec.data)), nil
}
func (m *mockTrackedSource) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return errors.New("not supported")
}
func (m *mockTrackedSource) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return errors.New("not supported")
}
func (m *mockTrackedSource) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	delete(m.files, filePath)
	return nil
}
func (m *mockTrackedSource) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	rec, ok := m.files[filePath]
	if !ok {
		return "", errors.New("not found")
	}
	h := sha256.Sum256(rec.data)
	return fmt.Sprintf("SHA256:%x", h), nil
}
func (m *mockTrackedSource) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return nil
}
func (m *mockTrackedSource) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	m.files[dirPath] = mockFileRecord{isDir: true}
	return nil
}
func (m *mockTrackedSource) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return errors.New("not supported")
}
func (m *mockTrackedSource) SupportsAtomicRename() bool { return true }
func (m *mockTrackedSource) VerificationMode() storage.VerificationMode {
	return storage.VerificationCryptographicHash
}
func (m *mockTrackedSource) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	var results []storage.CloudResource
	cleanDir := path.Clean(dirPath)
	for p, rec := range m.files {
		cleanP := path.Clean(p)
		if path.Dir(cleanP) == cleanDir && cleanP != cleanDir {
			results = append(results, storage.CloudResource{
				Path:         p,
				Name:         path.Base(p),
				IsDir:        rec.isDir,
				Size:         int64(len(rec.data)),
				LastModified: rec.mtime,
			})
		}
	}
	return results, nil
}
func (m *mockTrackedSource) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if rec, ok := m.files[filePath]; ok {
		return true, int64(len(rec.data)), nil
	}
	return false, 0, nil
}

func canReuseFromCatalog(file scannedFile, prev db.BackupSnapshotCatalogItem) bool {
	return !file.mtime.IsZero() &&
		!prev.Mtime.IsZero() &&
		prev.Mtime.Equal(file.mtime) &&
		prev.SizeBytes == file.size &&
		(file.size == 0 || len(prev.BlockIDs) > 0) &&
		len(prev.FileSHA256) == sha256.Size
}

func TestIncrementalReuseDecisionLogic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	var validSHA [32]byte
	validSHA[0] = 0xab

	source := newMockTrackedSource()
	source.files["/docs/readme.txt"] = mockFileRecord{
		data:  []byte("hello world"),
		mtime: now,
	}
	source.files["/docs/zero.txt"] = mockFileRecord{
		data:  []byte(""),
		mtime: now,
	}
	source.files["/docs/nomtime.txt"] = mockFileRecord{
		data:  []byte("no mtime"),
		mtime: time.Time{}, // zero mtime
	}
	source.files["/docs/modified.txt"] = mockFileRecord{
		data:  []byte("modified content"),
		mtime: now.Add(time.Hour),
	}

	catalog := map[string]db.BackupSnapshotCatalogItem{
		"docs/readme.txt": {
			RelativePath: "docs/readme.txt",
			SizeBytes:    11,
			Mtime:        now,
			FileSHA256:   validSHA[:],
			BlockIDs:     []string{"block-1"},
		},
		"docs/zero.txt": {
			RelativePath: "docs/zero.txt",
			SizeBytes:    0,
			Mtime:        now,
			FileSHA256:   validSHA[:],
			BlockIDs:     nil, // 0 blocks for 0-byte file
		},
		"docs/nomtime.txt": {
			RelativePath: "docs/nomtime.txt",
			SizeBytes:    8,
			Mtime:        time.Time{}, // missing mtime
			FileSHA256:   validSHA[:],
			BlockIDs:     []string{"block-2"},
		},
		"docs/modified.txt": {
			RelativePath: "docs/modified.txt",
			SizeBytes:    16,
			Mtime:        now, // old mtime
			FileSHA256:   validSHA[:],
			BlockIDs:     []string{"block-3"},
		},
	}

	t.Run("identical file with valid mtime is reused without download", func(t *testing.T) {
		file := scannedFile{
			sourcePath:   "/docs/readme.txt",
			relativePath: "docs/readme.txt",
			size:         11,
			mtime:        now,
		}
		prev, ok := catalog[file.relativePath]
		if !ok {
			t.Fatal("expected item in catalog")
		}
		if !canReuseFromCatalog(file, prev) {
			t.Fatal("expected file to be eligible for reuse")
		}

		current, err := source.InspectResource(ctx, "files", file.sourcePath)
		if err != nil || !sameSource(file, current) {
			t.Fatalf("InspectResource stability check failed: %v", err)
		}
		if source.downloadCounts["/docs/readme.txt"] != 0 {
			t.Fatalf("StreamDownload was called %d times, want 0", source.downloadCounts["/docs/readme.txt"])
		}
	})

	t.Run("0-byte file with matching mtime is reused without block IDs", func(t *testing.T) {
		file := scannedFile{
			sourcePath:   "/docs/zero.txt",
			relativePath: "docs/zero.txt",
			size:         0,
			mtime:        now,
		}
		prev, ok := catalog[file.relativePath]
		if !ok {
			t.Fatal("expected item in catalog")
		}
		if !canReuseFromCatalog(file, prev) {
			t.Fatal("expected 0-byte file to be eligible for reuse")
		}
	})

	t.Run("missing or zero mtime forces fresh download", func(t *testing.T) {
		file := scannedFile{
			sourcePath:   "/docs/nomtime.txt",
			relativePath: "docs/nomtime.txt",
			size:         8,
			mtime:        time.Time{},
		}
		prev, ok := catalog[file.relativePath]
		if !ok {
			t.Fatal("expected item in catalog")
		}
		if canReuseFromCatalog(file, prev) {
			t.Fatal("zero mtime must not be reused")
		}
	})

	t.Run("modified mtime forces fresh download", func(t *testing.T) {
		file := scannedFile{
			sourcePath:   "/docs/modified.txt",
			relativePath: "docs/modified.txt",
			size:         16,
			mtime:        now.Add(time.Hour),
		}
		prev, ok := catalog[file.relativePath]
		if !ok {
			t.Fatal("expected item in catalog")
		}
		if canReuseFromCatalog(file, prev) {
			t.Fatal("modified mtime must not be reused")
		}
	})
}

var mockTestDriverID atomic.Uint64
var (
	mockTestStatesMu sync.Mutex
	mockTestStates   = make(map[string]*mockCatalogDriverState)
)

type catalogMockRow struct {
	relPath   string
	size      int64
	mtime     time.Time
	fileSHA   []byte
	ordinal   int
	blockID   string
	packState string
}

type mockCatalogDriverState struct {
	rows            []catalogMockRow
	queryExecuted   string
	beginCount      int
	commitCount     int
	rollbackCount   int
	prepStatements  []string
	execCount       int
}

func newMockCatalogTestDB(t *testing.T, rows []catalogMockRow) (*sql.DB, *mockCatalogDriverState) {
	t.Helper()
	driverName := "backup-mock-" + strconv.FormatUint(mockTestDriverID.Add(1), 10)
	state := &mockCatalogDriverState{rows: rows}
	sql.Register(driverName, mockCatalogDriver{state: state})
	database, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database, state
}

type mockCatalogDriver struct {
	state *mockCatalogDriverState
}

func (d mockCatalogDriver) Open(string) (driver.Conn, error) {
	return &mockCatalogConn{state: d.state}, nil
}

type mockCatalogConn struct {
	state *mockCatalogDriverState
}

func (c *mockCatalogConn) Prepare(query string) (driver.Stmt, error) {
	c.state.prepStatements = append(c.state.prepStatements, query)
	return &mockCatalogStmt{state: c.state, query: query}, nil
}
func (c *mockCatalogConn) Close() error { return nil }
func (c *mockCatalogConn) Begin() (driver.Tx, error) {
	c.state.beginCount++
	return &mockCatalogTx{state: c.state}, nil
}
func (c *mockCatalogConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	c.state.beginCount++
	return &mockCatalogTx{state: c.state}, nil
}
func (c *mockCatalogConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.queryExecuted = query
	return &mockCatalogRowsReader{rows: c.state.rows}, nil
}
func (c *mockCatalogConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.execCount++
	return driver.RowsAffected(1), nil
}

type mockCatalogStmt struct {
	state *mockCatalogDriverState
	query string
}

func (s *mockCatalogStmt) Close() error { return nil }
func (s *mockCatalogStmt) NumInput() int { return -1 }
func (s *mockCatalogStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.state.execCount++
	return driver.RowsAffected(1), nil
}
func (s *mockCatalogStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &mockIDRowsReader{id: "generated-item-id"}, nil
}

type mockIDRowsReader struct {
	id   string
	read bool
}

func (r *mockIDRowsReader) Columns() []string { return []string{"id"} }
func (r *mockIDRowsReader) Close() error      { return nil }
func (r *mockIDRowsReader) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = r.id
	return nil
}

type mockCatalogTx struct {
	state *mockCatalogDriverState
}

func (tx *mockCatalogTx) Commit() error {
	tx.state.commitCount++
	return nil
}
func (tx *mockCatalogTx) Rollback() error {
	tx.state.rollbackCount++
	return nil
}

type mockCatalogRowsReader struct {
	rows []catalogMockRow
	pos  int
}

func (r *mockCatalogRowsReader) Columns() []string {
	return []string{"relative_path", "size_bytes", "mtime", "file_sha256", "ordinal", "backup_block_id", "state"}
}
func (r *mockCatalogRowsReader) Close() error { return nil }
func (r *mockCatalogRowsReader) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.pos]
	r.pos++
	dest[0] = row.relPath
	dest[1] = row.size
	if row.mtime.IsZero() {
		dest[2] = nil
	} else {
		dest[2] = row.mtime
	}
	dest[3] = row.fileSHA
	dest[4] = int64(row.ordinal)
	dest[5] = row.blockID
	dest[6] = row.packState
	return nil
}

func TestCatalogIncompleteBlockChainRejection(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	validSHA := make([]byte, 32)
	validSHA[0] = 0x55

	rows := []catalogMockRow{
		// 1. Incomplete pack state: block 1 is PENDING (not READY)
		{relPath: "incomplete_pack.txt", size: 100, mtime: now, fileSHA: validSHA, ordinal: 0, blockID: "b1", packState: "READY"},
		{relPath: "incomplete_pack.txt", size: 100, mtime: now, fileSHA: validSHA, ordinal: 1, blockID: "b2", packState: "PENDING"},
		{relPath: "incomplete_pack.txt", size: 100, mtime: now, fileSHA: validSHA, ordinal: 2, blockID: "b3", packState: "READY"},

		// 2. Ordinal gap: ordinal 1 is missing
		{relPath: "ordinal_gap.txt", size: 100, mtime: now, fileSHA: validSHA, ordinal: 0, blockID: "b1", packState: "READY"},
		{relPath: "ordinal_gap.txt", size: 100, mtime: now, fileSHA: validSHA, ordinal: 2, blockID: "b3", packState: "READY"},

		// 3. 0-byte file: 0 blocks
		{relPath: "zero_byte.txt", size: 0, mtime: now, fileSHA: validSHA, ordinal: -1, blockID: "", packState: ""},

		// 4. Fully valid file: ordinals 0, 1 with READY packs
		{relPath: "valid_file.txt", size: 200, mtime: now, fileSHA: validSHA, ordinal: 0, blockID: "b1", packState: "READY"},
		{relPath: "valid_file.txt", size: 200, mtime: now, fileSHA: validSHA, ordinal: 1, blockID: "b2", packState: "READY"},
	}

	dbConn, _ := newMockCatalogTestDB(t, rows)
	catalog, err := db.GetBackupSnapshotFileCatalogContext(context.Background(), dbConn, "snapshot-1")
	if err != nil {
		t.Fatalf("GetBackupSnapshotFileCatalogContext() error = %v", err)
	}

	if _, ok := catalog["incomplete_pack.txt"]; ok {
		t.Error("file with non-READY pack block must be rejected")
	}
	if _, ok := catalog["ordinal_gap.txt"]; ok {
		t.Error("file with ordinal gap must be rejected")
	}
	if item, ok := catalog["zero_byte.txt"]; !ok {
		t.Error("0-byte file must be present in catalog")
	} else if len(item.BlockIDs) != 0 {
		t.Errorf("0-byte file block count = %d, want 0", len(item.BlockIDs))
	}
	if item, ok := catalog["valid_file.txt"]; !ok {
		t.Error("valid_file.txt must be present in catalog")
	} else if len(item.BlockIDs) != 2 || item.BlockIDs[0] != "b1" || item.BlockIDs[1] != "b2" {
		t.Errorf("valid_file.txt blocks = %v, want [b1, b2]", item.BlockIDs)
	}
}

func TestBatchCreateBackupSnapshotItemsAndBlocksContextSingleTransaction(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	validSHA := make([]byte, 32)

	items := []db.BatchSnapshotItem{
		{RelativePath: "dir1", IsDir: true, State: "AVAILABLE", Mtime: now},
		{RelativePath: "dir1/file1.txt", IsDir: false, SizeBytes: 100, Mtime: now, FileSHA256: validSHA, State: "AVAILABLE", BlockIDs: []string{"b1", "b2"}},
		{RelativePath: "dir1/file2.txt", IsDir: false, SizeBytes: 50, Mtime: now, FileSHA256: validSHA, State: "AVAILABLE", BlockIDs: []string{"b3"}},
	}

	dbConn, state := newMockCatalogTestDB(t, nil)
	err := db.BatchCreateBackupSnapshotItemsAndBlocksContext(context.Background(), dbConn, "snapshot-1", items)
	if err != nil {
		t.Fatalf("BatchCreateBackupSnapshotItemsAndBlocksContext() error = %v", err)
	}

	if state.beginCount != 1 {
		t.Errorf("beginCount = %d, want exactly 1 transaction", state.beginCount)
	}
	if state.commitCount != 1 {
		t.Errorf("commitCount = %d, want exactly 1 commit", state.commitCount)
	}
	if state.rollbackCount != 0 {
		t.Errorf("rollbackCount = %d, want 0", state.rollbackCount)
	}
}
