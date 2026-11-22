package indexer

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"backend/internal/crypto"
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

func TestResolveIndexedFileSize(t *testing.T) {
	workspaceResource := storage.CloudResource{
		Path: "/document.docx",
		Size: 0,
		Metadata: storage.FileMetadata{CustomProps: map[string]string{
			"google_workspace_export": "true",
		}},
	}

	t.Run("uses listed size without workspace marker", func(t *testing.T) {
		resource := storage.CloudResource{Path: "/report.txt", Size: 42}
		size, err := resolveIndexedFileSize(context.Background(), indexFolderTestProvider{}, "files", resource.Path, resource)
		if err != nil || size != 42 {
			t.Fatalf("resolveIndexedFileSize() = %d, %v; want 42, nil", size, err)
		}
	})

	t.Run("rejects workspace marker without resolver", func(t *testing.T) {
		_, err := resolveIndexedFileSize(context.Background(), indexFolderTestProvider{}, "files", workspaceResource.Path, workspaceResource)
		if err == nil {
			t.Fatal("resolveIndexedFileSize() error = nil, want missing resolver error")
		}
	})

	t.Run("uses optional resolver", func(t *testing.T) {
		provider := &resolvingIndexFolderProvider{size: 123}
		size, err := resolveIndexedFileSize(context.Background(), provider, "files", workspaceResource.Path, workspaceResource)
		if err != nil || size != 123 {
			t.Fatalf("resolveIndexedFileSize() = %d, %v; want 123, nil", size, err)
		}
		if provider.resourcePath != workspaceResource.Path {
			t.Fatalf("resolver path = %q, want %q", provider.resourcePath, workspaceResource.Path)
		}
	})
}

func TestIndexFolderFlushesStagedCountersAfterCommit(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	files, dirs, bytes := 0, 0, int64(0)
	err := indexFolder(context.Background(), db.NewMigrationStore(database), indexFolderTestProvider{listing: []storage.CloudResource{
		{Path: "/report.txt", Name: "report.txt", Size: 42},
		{Path: "/notes.txt", Name: "notes.txt", Size: 8},
	}}, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
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
	err := indexFolder(context.Background(), db.NewMigrationStore(database), indexFolderTestProvider{listing: []storage.CloudResource{{
		Path: "/report.txt", Name: "report.txt", Size: 42,
	}}}, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
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
	err := indexFolder(context.Background(), db.NewMigrationStore(database), indexFolderTestProvider{listing: []storage.CloudResource{{
		Path: "/report.txt", Name: "report.txt", Size: 42,
	}}}, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
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
	err := indexFolder(ctx, db.NewMigrationStore(database), indexFolderTestProvider{
		listing: []storage.CloudResource{
			{Path: "/report.txt", Name: "report.txt", Size: 42},
			{Path: "/later", Name: "later", IsDir: true},
		},
		onListing: cancel,
	}, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
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

	err := indexFolder(context.Background(), db.NewMigrationStore(database), indexFolderTestProvider{listing: listing}, "files", "/", "migration-1", "local", "immich", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
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

func TestIndexFolderLimitsConcurrentListingsAndIndexesEveryPathOnce(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	const folderCount = 8
	releaseListings := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseListings:
		default:
			close(releaseListings)
		}
	})
	provider := &parallelIndexFolderProvider{
		listings:   make(map[string][]storage.CloudResource),
		blockPaths: make(map[string]bool),
		started:    make(chan string, folderCount),
		release:    releaseListings,
	}

	for i := range folderCount {
		folderPath := fmt.Sprintf("/folder-%d", i)
		filePath := fmt.Sprintf("%s/file-%d.txt", folderPath, i)
		provider.listings["/"] = append(provider.listings["/"], storage.CloudResource{Path: folderPath, Name: fmt.Sprintf("folder-%d", i), IsDir: true})
		provider.listings[folderPath] = []storage.CloudResource{{Path: filePath, Name: fmt.Sprintf("file-%d.txt", i), Size: int64(i + 1)}}
		provider.blockPaths[folderPath] = true
	}

	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput
	done := make(chan error, 1)
	go func() {
		done <- indexFolder(context.Background(), db.NewMigrationStore(database), provider, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	}()

	for range indexFolderListingWorkers {
		select {
		case <-provider.started:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for concurrent directory listings")
		}
	}
	provider.mu.Lock()
	peakListings := provider.peakListings
	provider.mu.Unlock()
	if peakListings != indexFolderListingWorkers {
		t.Fatalf("peak concurrent listings = %d, want %d", peakListings, indexFolderListingWorkers)
	}
	close(releaseListings)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("indexFolder() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("indexFolder() did not finish after listings were released")
	}

	if len(indexErrors) != 0 {
		t.Fatalf("indexErrors = %#v, want none", indexErrors)
	}
	if files != folderCount || dirs != folderCount || bytes != 36 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:%d dirs:%d bytes:36", files, dirs, bytes, folderCount, folderCount)
	}
	if len(state.taskPaths) != folderCount*2 {
		t.Fatalf("created task paths = %d, want %d", len(state.taskPaths), folderCount*2)
	}
	expectedPaths := make(map[string]bool, folderCount*2)
	for i := range folderCount {
		expectedPaths[fmt.Sprintf("/folder-%d", i)] = true
		expectedPaths[fmt.Sprintf("/folder-%d/file-%d.txt", i, i)] = true
	}
	for _, taskPath := range state.taskPaths {
		if !expectedPaths[taskPath] {
			t.Fatalf("unexpected or duplicate task path %q", taskPath)
		}
		delete(expectedPaths, taskPath)
	}
	if len(expectedPaths) != 0 {
		t.Fatalf("missing task paths: %#v", expectedPaths)
	}
}

func TestIndexFolderSkipsFailedListingAndContinuesSibling(t *testing.T) {
	database, _ := newBatchTestDB(t, false)
	provider := &parallelIndexFolderProvider{
		listings: map[string][]storage.CloudResource{
			"/": {
				{Path: "/unavailable", Name: "unavailable", IsDir: true},
				{Path: "/available", Name: "available", IsDir: true},
			},
			"/available": {{Path: "/available/report.txt", Name: "report.txt", Size: 42}},
		},
		errs: map[string]error{"/unavailable": errors.New("injected listing failure")},
	}
	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput

	err := indexFolder(context.Background(), db.NewMigrationStore(database), provider, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if files != 1 || dirs != 2 || bytes != 42 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:1 dirs:2 bytes:42", files, dirs, bytes)
	}
	if len(indexErrors) != 1 || indexErrors[0].Path != "/unavailable" || indexErrors[0].ErrorMessage != "Unable to list folder." {
		t.Fatalf("indexErrors = %#v, want neutral error for unavailable folder", indexErrors)
	}
}

func TestIndexFolderFlushesPartialBatchAfterInFlightListingCancellation(t *testing.T) {
	database, state := newBatchTestDB(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &parallelIndexFolderProvider{
		listings: map[string][]storage.CloudResource{
			"/": {
				{Path: "/first", Name: "first", IsDir: true},
				{Path: "/second", Name: "second", IsDir: true},
				{Path: "/third", Name: "third", IsDir: true},
				{Path: "/fourth", Name: "fourth", IsDir: true},
			},
		},
		blockPaths: map[string]bool{
			"/first":  true,
			"/second": true,
			"/third":  true,
			"/fourth": true,
		},
		started: make(chan string, indexFolderListingWorkers),
		release: make(chan struct{}),
	}
	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput
	done := make(chan error, 1)
	go func() {
		done <- indexFolder(ctx, db.NewMigrationStore(database), provider, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	}()

	for range indexFolderListingWorkers {
		select {
		case <-provider.started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for in-flight directory listings")
		}
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("indexFolder() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("indexFolder() did not stop after cancellation")
	}

	if files != 0 || dirs != indexFolderListingWorkers || bytes != 0 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:0 dirs:%d bytes:0", files, dirs, bytes, indexFolderListingWorkers)
	}
	if len(state.taskPaths) != indexFolderListingWorkers {
		t.Fatalf("created task paths = %d, want %d", len(state.taskPaths), indexFolderListingWorkers)
	}
	if len(indexErrors) != indexFolderListingWorkers {
		t.Fatalf("indexErrors = %#v, want one listing error per in-flight folder", indexErrors)
	}
	for _, indexError := range indexErrors {
		if indexError.ErrorMessage != "Unable to list folder." {
			t.Fatalf("index error = %#v, want neutral listing error", indexError)
		}
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

type resolvingIndexFolderProvider struct {
	indexFolderTestProvider
	size         int64
	err          error
	resourcePath string
}

type parallelIndexFolderProvider struct {
	// Embed the index source stub; only directory listing behaviour is
	// relevant to these concurrency tests.
	indexFolderTestProvider
	listings   map[string][]storage.CloudResource
	errs       map[string]error
	blockPaths map[string]bool
	started    chan string
	release    <-chan struct{}

	mu             sync.Mutex
	activeListings int
	peakListings   int
}

func (p *parallelIndexFolderProvider) GetDirectoryListing(ctx context.Context, _ string, folderPath string) ([]storage.CloudResource, error) {
	p.mu.Lock()
	p.activeListings++
	if p.activeListings > p.peakListings {
		p.peakListings = p.activeListings
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.activeListings--
		p.mu.Unlock()
	}()

	if p.blockPaths[folderPath] {
		select {
		case p.started <- folderPath:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := p.errs[folderPath]; err != nil {
		return nil, err
	}
	return p.listings[folderPath], nil
}

func (p *resolvingIndexFolderProvider) ResolveResourceSize(_ context.Context, _ string, resourcePath string) (int64, error) {
	p.resourcePath = resourcePath
	return p.size, p.err
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

var (
	batchTestDriverOnce sync.Once
	batchTestStatesMu   sync.Mutex
	batchTestStates     = make(map[string]*batchDBState)
)

type recordedBatchTask struct {
	FilePath     string
	FileSize     int64
	Status       string
	ErrorMessage string
}

type batchDBState struct {
	failInsert     bool
	claimLost      bool
	execs, commits int
	taskPaths      []string
	tasks          []recordedBatchTask
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
func (c batchTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
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
	for i := 2; i < len(args); i += 7 {
		filePath, ok := args[i].Value.(string)
		if !ok {
			return nil, fmt.Errorf("task path argument %d = %T, want string", i, args[i].Value)
		}
		c.state.taskPaths = append(c.state.taskPaths, filePath)
		rec := recordedBatchTask{FilePath: filePath}
		if i+1 < len(args) {
			if sz, ok := args[i+1].Value.(int64); ok {
				rec.FileSize = sz
			}
		}
		if i+3 < len(args) {
			if st, ok := args[i+3].Value.(string); ok {
				rec.Status = st
			}
		}
		if i+5 < len(args) {
			if msg, ok := args[i+5].Value.(string); ok {
				rec.ErrorMessage = msg
			}
		}
		c.state.tasks = append(c.state.tasks, rec)
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

type listingOnlySource struct {
	listing []storage.CloudResource
}

func (s listingOnlySource) GetDirectoryListing(context.Context, string, string) ([]storage.CloudResource, error) {
	return s.listing, nil
}

var (
	_ folderLister = (storage.StorageProvider)(nil)
	_ IndexSource  = (storage.StorageProvider)(nil)
)

type minimalIndexSource struct {
	connected bool
	closed    bool
	resources map[string]storage.CloudResource
	listing   map[string][]storage.CloudResource
}

func (s *minimalIndexSource) Close() error {
	s.closed = true
	return nil
}

func (s *minimalIndexSource) Connect(context.Context) (bool, error) {
	s.connected = true
	return true, nil
}

func (s *minimalIndexSource) InspectResource(_ context.Context, _, resourcePath string) (storage.CloudResource, error) {
	res, ok := s.resources[resourcePath]
	if !ok {
		return storage.CloudResource{}, errors.New("missing")
	}
	return res, nil
}

func (s *minimalIndexSource) GetDirectoryListing(_ context.Context, _, dirPath string) ([]storage.CloudResource, error) {
	return s.listing[dirPath], nil
}

func TestIndexerSetSourceFactoryAcceptsMinimalIndexSource(t *testing.T) {
	database, _ := newBatchTestDB(t, false)
	idx, err := NewIndexer(database, "", nil, time.Minute)
	if err != nil {
		t.Fatalf("NewIndexer() error = %v", err)
	}
	src := &minimalIndexSource{
		resources: map[string]storage.CloudResource{
			"/root": {Path: "/root", Name: "root", IsDir: true},
		},
		listing: map[string][]storage.CloudResource{
			"/root": {{Path: "/root/doc.txt", Name: "doc.txt", Size: 11}},
		},
	}
	idx.SetSourceFactory(func(context.Context, string, string, string, string, ...storage.ProviderOption) (IndexSource, error) {
		return src, nil
	})

	client, err := idx.newSource(context.Background(), "nextcloud", "https://cloud.example.test", "user", "pass")
	if err != nil {
		t.Fatalf("newSource() error = %v", err)
	}
	defer client.Close()
	if ok, err := client.Connect(context.Background()); err != nil || !ok {
		t.Fatalf("Connect() = (%v, %v), want (true, nil)", ok, err)
	}
	res, err := client.InspectResource(context.Background(), "files", "/root")
	if err != nil || !res.IsDir {
		t.Fatalf("InspectResource() = (%+v, %v), want directory", res, err)
	}
	files, dirs, bytes := 0, 0, int64(0)
	if err := indexFolder(context.Background(), db.NewMigrationStore(database), client, "files", "/root", "mig-1", "nextcloud", "google", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{}); err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	_ = client.Close()
	if !src.connected || !src.closed || files != 1 || bytes != 11 {
		t.Fatalf("connected=%v closed=%v files=%d bytes=%d", src.connected, src.closed, files, bytes)
	}
}

func TestIndexFolderAcceptsListingOnlySource(t *testing.T) {
	database, _ := newBatchTestDB(t, false)
	files, dirs, bytes := 0, 0, int64(0)
	err := indexFolder(context.Background(), db.NewMigrationStore(database), listingOnlySource{listing: []storage.CloudResource{
		{Path: "/only-listed.txt", Name: "only-listed.txt", Size: 19},
	}}, "files", "/", "migration-1", "local", "local", &files, &dirs, &bytes, map[string]bool{}, &[]db.IndexingErrorInput{})
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if files != 1 || dirs != 0 || bytes != 19 {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:1 dirs:0 bytes:19", files, dirs, bytes)
	}
}

func newFakeBoxIndexerServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	normalBody := []byte("hello box")
	normalSum := sha1.Sum(normalBody)
	normalSHA1 := hex.EncodeToString(normalSum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/2.0/users/me":
			_, _ = w.Write([]byte(`{"id":"1","type":"user","name":"Box User","login":"box@example.test"}`))
		case "/2.0/folders/0/items":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{"id": "10", "type": "folder", "name": "Docs", "modified_at": "2026-01-15T10:00:00Z"},
				},
			})
		case "/2.0/folders/10/items":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{"id": "11", "type": "file", "name": "Meeting.boxnote", "size": 128, "sha1": "", "modified_at": "2026-01-16T10:00:00Z"},
					{"id": "12", "type": "file", "name": "Board.boxcanvas", "size": 256, "sha1": "", "modified_at": "2026-01-16T10:00:00Z"},
					{"id": "13", "type": "web_link", "name": "Portal", "size": 0, "url": "https://example.test/portal", "modified_at": "2026-01-16T10:00:00Z"},
					{"id": "14", "type": "file", "name": "normal.txt", "size": len(normalBody), "sha1": normalSHA1, "modified_at": "2026-01-16T10:00:00Z"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIndexFolderRecordsBoxNonTransferableItemsAsSkippedWithReason(t *testing.T) {
	srv := newFakeBoxIndexerServer(t, "box-token")
	boxClient, err := storage.NewBoxTestProvider("box-token", srv.URL+"/2.0", srv.URL+"/api/2.0", srv.Client())
	if err != nil {
		t.Fatalf("NewBoxTestProvider: %v", err)
	}
	defer boxClient.Close()

	database, state := newBatchTestDB(t, false)
	files, dirs, bytes := 0, 0, int64(0)
	var indexErrors []db.IndexingErrorInput
	err = indexFolder(context.Background(), db.NewMigrationStore(database), boxClient, "files", "/Docs", "mig-box-1", "box", "nextcloud", &files, &dirs, &bytes, map[string]bool{}, &indexErrors)
	if err != nil {
		t.Fatalf("indexFolder() error = %v", err)
	}
	if len(indexErrors) != 0 {
		t.Fatalf("indexErrors = %#v, want none during parent traversal", indexErrors)
	}
	if files != 4 || dirs != 0 || bytes != int64(len("hello box")) {
		t.Fatalf("counters = files:%d dirs:%d bytes:%d, want files:4 dirs:0 bytes:%d", files, dirs, bytes, len("hello box"))
	}

	byPath := make(map[string]recordedBatchTask, len(state.tasks))
	for _, task := range state.tasks {
		byPath[task.FilePath] = task
	}
	for _, skippedPath := range []string{"/Docs/Meeting.boxnote", "/Docs/Board.boxcanvas", "/Docs/Portal"} {
		task, ok := byPath[skippedPath]
		if !ok {
			t.Fatalf("missing task for %s", skippedPath)
		}
		if task.Status != "SKIPPED" {
			t.Fatalf("%s status = %q, want SKIPPED", skippedPath, task.Status)
		}
		if strings.TrimSpace(task.ErrorMessage) == "" {
			t.Fatalf("%s error_message is empty, want visible skip reason", skippedPath)
		}
	}
	normal, ok := byPath["/Docs/normal.txt"]
	if !ok || normal.Status != "PENDING" || normal.FileSize != int64(len("hello box")) {
		t.Fatalf("normal.txt task = %+v, want PENDING with size %d", normal, len("hello box"))
	}
}

type explicitSkipDBState struct {
	mu             sync.Mutex
	encryptedPass  string
	selectedPaths  []string
	indexingErrors []string
	migrationState string
	migrationErr   string
}

var (
	explicitSkipDriverOnce sync.Once
	explicitSkipStatesMu   sync.Mutex
	explicitSkipStates     = make(map[string]*explicitSkipDBState)
)

func newExplicitSkipTestDB(t *testing.T, state *explicitSkipDBState) *sql.DB {
	t.Helper()
	explicitSkipDriverOnce.Do(func() { sql.Register("indexer-explicit-skip-test", explicitSkipDriver{}) })
	dsn := t.Name()
	explicitSkipStatesMu.Lock()
	explicitSkipStates[dsn] = state
	explicitSkipStatesMu.Unlock()
	database, err := sql.Open("indexer-explicit-skip-test", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.Close()
		explicitSkipStatesMu.Lock()
		delete(explicitSkipStates, dsn)
		explicitSkipStatesMu.Unlock()
	})
	return database
}

type explicitSkipDriver struct{}

func (explicitSkipDriver) Open(dsn string) (driver.Conn, error) {
	explicitSkipStatesMu.Lock()
	state := explicitSkipStates[dsn]
	explicitSkipStatesMu.Unlock()
	if state == nil {
		return nil, errors.New("missing explicit skip test state")
	}
	return explicitSkipConn{state: state}, nil
}

type explicitSkipConn struct{ state *explicitSkipDBState }

func (explicitSkipConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not implemented") }
func (explicitSkipConn) Close() error                        { return nil }
func (explicitSkipConn) Begin() (driver.Tx, error)           { return explicitSkipTx{}, nil }
func (explicitSkipConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return explicitSkipTx{}, nil
}

func (c explicitSkipConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	switch {
	case strings.Contains(query, "INSERT INTO indexing_errors"):
		for i := 2; i < len(args); i += 4 {
			p, _ := args[i].Value.(string)
			msg, _ := args[i+1].Value.(string)
			c.state.indexingErrors = append(c.state.indexingErrors, p+":"+msg)
		}
		return driver.RowsAffected(int64(len(args) / 4)), nil
	case strings.Contains(query, "SET status = 'FAILED'"):
		c.state.migrationState = "FAILED"
		if len(args) > 0 {
			if msg, ok := args[0].Value.(string); ok {
				c.state.migrationErr = msg
			}
		}
		return driver.RowsAffected(1), nil
	default:
		return driver.RowsAffected(1), nil
	}
}

func (c explicitSkipConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	switch {
	case strings.Contains(query, "SELECT id, user_id"):
		now := time.Now()
		pathsJSON, _ := json.Marshal(c.state.selectedPaths)
		return &explicitSkipRows{
			cols: []string{
				"id", "user_id", "source_profile_id", "target_profile_id", "source_url", "source_username", "source_password_encrypted", "source_provider",
				"source_refresh_token_encrypted", "source_token_expires_at", "source_mega_session_id_encrypted", "source_mega_master_key_encrypted",
				"target_url", "target_username", "target_password_encrypted", "target_provider",
				"target_refresh_token_encrypted", "target_token_expires_at", "target_mega_session_id_encrypted", "target_mega_master_key_encrypted",
				"status", "conflict_strategy", "total_files", "total_bytes", "processed_files",
				"processed_bytes", "live_bytes", "verified_files", "skipped_files", "failed_files", "error_message",
				"discovery_complete", "discovery_generation", "indexing_lease_until",
				"created_at", "updated_at", "target_dir", "threads", "bandwidth_limit_mbps",
				"picker_session_id", "selected_paths", "selected_calendars", "selected_contacts",
			},
			rows: [][]driver.Value{{
				"mig-box-explicit", "user-1", nil, nil, "https://api.box.com", "box@example.test", c.state.encryptedPass, "box",
				nil, nil, "", "",
				"https://nc.example.test", "u", c.state.encryptedPass, "nextcloud",
				nil, nil, "", "",
				"INDEXING", "SKIP", int64(0), int64(0), int64(0),
				int64(0), int64(0), int64(0), int64(0), int64(0), nil,
				false, int64(1), nil,
				now, now, "/dest", int64(4), int64(0),
				"", pathsJSON, []byte("[]"), []byte("[]"),
			}},
		}, nil
	case strings.Contains(query, "UPDATE migrations SET indexing_lease_until"):
		return &explicitSkipRows{cols: []string{"ok"}, rows: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(query, "SELECT COUNT(*), COALESCE(SUM(file_size), 0) FROM tasks"):
		return &explicitSkipRows{cols: []string{"count", "sum"}, rows: [][]driver.Value{{int64(0), int64(0)}}}, nil
	case strings.Contains(query, "SELECT user_id FROM migrations"):
		return &explicitSkipRows{cols: []string{"user_id"}, rows: [][]driver.Value{{"user-1"}}}, nil
	case strings.Contains(query, "INSERT INTO tasks"):
		now := time.Now()
		return &explicitSkipRows{cols: []string{"id", "created_at", "updated_at"}, rows: [][]driver.Value{{"task-1", now, now}}}, nil
	default:
		return &explicitSkipRows{cols: []string{"c1", "c2", "c3"}}, nil
	}
}

type explicitSkipTx struct{}

func (explicitSkipTx) Commit() error   { return nil }
func (explicitSkipTx) Rollback() error { return nil }

type explicitSkipRows struct {
	cols []string
	rows [][]driver.Value
	idx  int
}

func (r *explicitSkipRows) Columns() []string { return r.cols }
func (r *explicitSkipRows) Close() error      { return nil }
func (r *explicitSkipRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func TestIndexerStartExplicitBoxSkippedItemRecordsIndexingError(t *testing.T) {
	srv := newFakeBoxIndexerServer(t, "box-token")
	const secretKey = "01234567890123456789012345678901"
	encryptedPass, err := crypto.EncryptWithDomain("box-token", secretKey, crypto.ConnectionCredentialDomain(true))
	if err != nil {
		t.Fatalf("EncryptWithDomain: %v", err)
	}

	for _, selectedPath := range []string{"/Docs/Meeting.boxnote", "/Docs/Board.boxcanvas", "/Docs/Portal"} {
		t.Run(selectedPath, func(t *testing.T) {
			state := &explicitSkipDBState{
				encryptedPass: encryptedPass,
				selectedPaths: []string{selectedPath},
			}
			database := newExplicitSkipTestDB(t, state)
			idx, err := NewIndexer(database, secretKey, nil, time.Minute)
			if err != nil {
				t.Fatalf("NewIndexer: %v", err)
			}
			idx.SetSourceFactory(func(_ context.Context, _, _, _, password string, _ ...storage.ProviderOption) (IndexSource, error) {
				return storage.NewBoxTestProvider(password, srv.URL+"/2.0", srv.URL+"/api/2.0", srv.Client())
			})

			idx.Start(context.Background(), "mig-box-explicit")

			state.mu.Lock()
			defer state.mu.Unlock()
			if len(state.indexingErrors) != 1 || !strings.HasPrefix(state.indexingErrors[0], selectedPath+":") {
				t.Fatalf("indexingErrors = %v, want clear indexing error for %s", state.indexingErrors, selectedPath)
			}
			if state.migrationState != "FAILED" {
				t.Fatalf("migrationState = %q, want FAILED when only a skipped item was selected", state.migrationState)
			}
		})
	}
}
