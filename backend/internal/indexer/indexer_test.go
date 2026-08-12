package indexer

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

func TestSanitizeErrorRedactsCredentials(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			"dial tcp 10.0.0.5:443: connect: connection refused https://user:pass@10.0.0.5/remote.php/dav",
			"dial tcp 10.0.0.5:443: connect: connection refused https://***:***@10.0.0.5/remote.php/dav",
		},
		{
			"failed to connect to ftp://alice:secret@host.example.com/path",
			"failed to connect to ftp://***:***@host.example.com/path",
		},
		{
			"no credentials here, just a plain message",
			"no credentials here, just a plain message",
		},
		{
			"https://user:pass@host WITH trailing text and https://a:b@x/y",
			"https://***:***@host WITH trailing text and https://***:***@x/y",
		},
	}
	for _, c := range cases {
		got := sanitize.SanitizeError(c.in)
		if got != c.want {
			t.Errorf("SanitizeError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeErrorLeavesSchemeAndHost(t *testing.T) {
	in := "error contacting https://user:pass@db.internal:8080/dav/files"
	got := sanitize.SanitizeError(in)
	if got == in {
		t.Errorf("expected credentials to be redacted, got %q", got)
	}
	// Scheme and host must be preserved for diagnostics.
	if !contains(got, "https://") || !contains(got, "db.internal:8080") {
		t.Errorf("expected scheme/host preserved, got %q", got)
	}
}

func TestIndexingTimeoutDefault(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Unsetenv("INDEXING_TIMEOUT_MINUTES")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 20*time.Minute {
		t.Errorf("expected default 20m, got %v", d)
	}
}

func TestIndexingTimeoutFromEnv(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Setenv("INDEXING_TIMEOUT_MINUTES", "5")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 5*time.Minute {
		t.Errorf("expected 5m from env, got %v", d)
	}
}

func TestIndexingTimeoutInvalidEnv(t *testing.T) {
	old := os.Getenv("INDEXING_TIMEOUT_MINUTES")
	_ = os.Setenv("INDEXING_TIMEOUT_MINUTES", "not-a-number")
	defer func() { _ = os.Setenv("INDEXING_TIMEOUT_MINUTES", old) }()

	if d := indexingTimeout(); d != 20*time.Minute {
		t.Errorf("expected default 20m for invalid env, got %v", d)
	}
}

func TestDeduplicateSelectedPaths(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "keeps separate paths", in: []string{"/photos", "/documents"}, want: []string{"/photos", "/documents"}},
		{name: "removes child after parent", in: []string{"/photos", "/photos/2026", "/photos/2026"}, want: []string{"/photos"}},
		{name: "replaces earlier child with parent", in: []string{"/photos/2026", "/photos"}, want: []string{"/photos"}},
		{name: "root covers every path", in: []string{"/photos", "/"}, want: []string{"/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deduplicateSelectedPaths(tt.in)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("deduplicateSelectedPaths(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIndexingAuditDetails(t *testing.T) {
	got := string(indexingAuditDetails("hello \"world\""))
	want := `{"phase":"indexing","error":"hello \"world\""}`
	if got != want {
		t.Errorf("indexingAuditDetails = %q, want %q", got, want)
	}
}

func TestIndexFolderFlushesStagedCountersAfterCommit(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	files, dirs, bytes := 0, 0, int64(0)
	err := indexFolder(context.Background(), database, indexFolderTestProvider{listing: []storage.CloudResource{
		{Path: "/report.txt", Name: "report.txt", Size: 42},
		{Path: "/notes.txt", Name: "notes.txt", Size: 8},
	}}, "files", "/", "migration-1", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if files != 2 || dirs != 0 || bytes != 50 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:2 dirs:0 bytes:50", files, dirs, bytes)
	}
	if state.execs != 1 || state.commits != 1 {
		t.Fatalf("database calls = execs:%d commits:%d, want one committed batch", state.execs, state.commits)
	}
}

func TestIndexFolderDoesNotApplyStagedCountersWhenBatchInsertFails(t *testing.T) {
	database, state := newBatchTestDB(t, true)
	files, dirs, bytes := 0, 0, int64(0)
	err := indexFolder(context.Background(), database, indexFolderTestProvider{listing: []storage.CloudResource{{
		Path: "/report.txt", Name: "report.txt", Size: 42,
	}}}, "files", "/", "migration-1", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
	if err == nil {
		t.Fatal("indexFolder() succeeded after batch insert failure")
	}
	if files != 0 || dirs != 0 || bytes != 0 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want all zero after failed batch", files, dirs, bytes)
	}
	if state.execs != 1 || state.commits != 0 {
		t.Fatalf("database calls = execs:%d commits:%d, want one failed uncommitted batch", state.execs, state.commits)
	}
}

func TestIndexFolderStopsWhenMigrationIndexingClaimIsLost(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	state.claimLost = true
	files, dirs, bytes := 0, 0, int64(0)
	err := indexFolder(context.Background(), database, indexFolderTestProvider{listing: []storage.CloudResource{{
		Path: "/report.txt", Name: "report.txt", Size: 42,
	}}}, "files", "/", "migration-1", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
	if !errors.Is(err, db.ErrMigrationIndexingClaimLost) {
		t.Fatalf("indexFolder() error = %v, want ErrMigrationIndexingClaimLost", err)
	}
	if files != 0 || dirs != 0 || bytes != 0 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want all zero after lost claim", files, dirs, bytes)
	}
	if state.execs != 1 || state.commits != 0 {
		t.Fatalf("database calls = execs:%d commits:%d, want one rolled-back batch", state.execs, state.commits)
	}
}

func TestIndexFolderFlushesPartialBatchAfterCancellation(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput
	err := indexFolder(ctx, database, indexFolderTestProvider{
		listing: []storage.CloudResource{
			{Path: "/report.txt", Name: "report.txt", Size: 42},
			{Path: "/later", Name: "later", IsDir: true},
		},
		onListing: cancel,
	}, "files", "/", "migration-1", "local", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if files != 1 || dirs != 1 || bytes != 42 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:1 dirs:1 bytes:42", files, dirs, bytes)
	}
	if state.execs != 1 || state.commits != 1 {
		t.Fatalf("database calls = execs:%d commits:%d, want one committed partial batch", state.execs, state.commits)
	}
	if len(indexErrors) != 1 || indexErrors[0].ErrorMessage != "Indexing interrupted before the folder could be listed." {
		t.Fatalf("indexErrors = %#v, want one cancellation entry", indexErrors)
	}
}

func TestIndexFolderSkipsNonMediaForImmichWithoutError(t *testing.T) {
	database, _ := newBatchTestDB(t, false)
	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput

	listing := []storage.CloudResource{
		{Path: "/photo.jpg", Name: "photo.jpg", Size: 100},
		{Path: "/doc.pdf", Name: "doc.pdf", Size: 200},
		{Path: "/.shards", Name: ".shards", Size: 50},
		{Path: "/vector.svg", Name: "vector.svg", Size: 30},
		{Path: "/readme.md", Name: "readme.md", Size: 10},
		{Path: "/temp.tmp", Name: "temp.tmp", Size: 5},
	}

	err := indexFolder(context.Background(), database, indexFolderTestProvider{listing: listing}, "files", "/", "migration-1", "immich", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if files != 1 || bytes != 100 {
		t.Fatalf("counters = files:%d bytes:%d, want files:1 bytes:100 (only photo.jpg)", files, bytes)
	}
	if len(indexErrors) != 0 {
		t.Fatalf("expected 0 indexing errors for Immich non-media files, got %d: %v", len(indexErrors), indexErrors)
	}
}

func TestIsImmichMedia(t *testing.T) {
	media := []string{"photo.jpg", "PIC.JPEG", "video.mp4", "raw.cr2", "image.heic", "file.png"}
	for _, m := range media {
		if !isImmichMedia(m) {
			t.Errorf("isImmichMedia(%q) = false, want true", m)
		}
	}

	nonMedia := []string{".shards", "file.tmp", "vector.svg", "doc.pdf", "readme.md", "data.docx", "sheet.xlsx", "pres.odp", "file.doc"}
	for _, nm := range nonMedia {
		if isImmichMedia(nm) {
			t.Errorf("isImmichMedia(%q) = true, want false", nm)
		}
	}
}

type indexFolderTestProvider struct {
	listing   []storage.CloudResource
	onListing func()
}

func (p indexFolderTestProvider) Close() error                          { return nil }
func (p indexFolderTestProvider) Connect(context.Context) (bool, error) { return true, nil }
func (p indexFolderTestProvider) GetDirectoryListing(context.Context, string, string) ([]storage.CloudResource, error) {
	if p.onListing != nil {
		p.onListing()
	}
	return p.listing, nil
}
func (p indexFolderTestProvider) InspectResource(context.Context, string, string) (storage.CloudResource, error) {
	return storage.CloudResource{}, errors.New("not implemented")
}
func (p indexFolderTestProvider) StreamDownload(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}
func (p indexFolderTestProvider) StreamUpload(context.Context, string, string, io.Reader, int64) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) StreamUploadChunked(context.Context, string, string, io.Reader, int64, chan<- int64) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	return false, 0, errors.New("not implemented")
}
func (p indexFolderTestProvider) DeleteFile(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) GetFileHash(context.Context, string, string) (string, error) {
	return "", errors.New("not implemented")
}
func (p indexFolderTestProvider) CreateParentDirectories(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) CreateDirectory(context.Context, string, string) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) RenameFile(context.Context, string, string, string) error {
	return errors.New("not implemented")
}
func (p indexFolderTestProvider) SupportsAtomicRename() bool { return true }
func (p indexFolderTestProvider) VerificationMode() storage.VerificationMode {
	// Verification is not exercised by indexer tests; this only satisfies the interface.
	return storage.VerificationSizeOnly
}

var (
	batchTestDriverOnce sync.Once
	batchTestStatesMu   sync.Mutex
	batchTestStates     = make(map[string]*batchDBState)
)

type batchDBState struct {
	failInsert     bool
	claimLost      bool
	execs, commits int
}

func newBatchTestDB(t *testing.T, failInsert bool) (*sql.DB, *batchDBState) {
	t.Helper()
	batchTestDriverOnce.Do(func() { sql.Register("indexer-batch-test", batchTestDriver{}) })
	state := &batchDBState{failInsert: failInsert}
	dsn := t.Name()
	batchTestStatesMu.Lock()
	batchTestStates[dsn] = state
	batchTestStatesMu.Unlock()
	database, err := sql.Open("indexer-batch-test", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		batchTestStatesMu.Lock()
		delete(batchTestStates, dsn)
		batchTestStatesMu.Unlock()
	})
	return database, state
}

type batchTestDriver struct{}

func (batchTestDriver) Open(dsn string) (driver.Conn, error) {
	batchTestStatesMu.Lock()
	state := batchTestStates[dsn]
	batchTestStatesMu.Unlock()
	if state == nil {
		return nil, errors.New("missing batch test state")
	}
	return batchTestConn{state: state}, nil
}

type batchTestConn struct{ state *batchDBState }

func (batchTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (batchTestConn) Close() error                        { return nil }
func (c batchTestConn) Begin() (driver.Tx, error)         { return batchTestTx{state: c.state}, nil }
func (c batchTestConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return batchTestTx{state: c.state}, nil
}
func (c batchTestConn) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.state.execs++
	if c.state.failInsert {
		return nil, errors.New("injected insert failure")
	}
	if c.state.claimLost {
		return driver.RowsAffected(0), nil
	}
	// Each VALUES row ends with "),(" except for the final row.
	return driver.RowsAffected(int64(strings.Count(query, "),(") + 1)), nil
}

type batchTestTx struct{ state *batchDBState }

func (tx batchTestTx) Commit() error { tx.state.commits++; return nil }
func (batchTestTx) Rollback() error  { return nil }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
