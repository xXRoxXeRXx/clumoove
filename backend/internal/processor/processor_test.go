package processor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

type sizeRetryProvider struct{ fakeProvider }

func (*sizeRetryProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	return false, 0, errors.New("transient target error")
}

func TestExpectedSizeReader(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected int64
		wantErr  bool
	}{
		{name: "exact size", input: "data", expected: 4},
		{name: "clean early eof", input: "dat", expected: 4, wantErr: true},
		{name: "source grew", input: "data!", expected: 4, wantErr: true},
		{name: "empty exact size", input: "", expected: 0},
		{name: "non-empty source indexed as zero", input: "data", expected: 0, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := newExpectedSizeReader(bytes.NewBufferString(tc.input), tc.expected)
			_, readErr := io.ReadAll(reader)
			verifyErr := reader.VerifyComplete()
			if (readErr != nil || verifyErr != nil) != tc.wantErr {
				t.Fatalf("ReadAll error = %v, VerifyComplete error = %v, want error=%v", readErr, verifyErr, tc.wantErr)
			}
		})
	}
}

func TestOAuthAuthFailureRole(t *testing.T) {
	tests := []struct {
		name string
		mig  db.Migration
		err  string
		want string
	}{
		{name: "onedrive source download", mig: db.Migration{SourceProvider: "onedrive", TargetProvider: "nextcloud"}, err: "failed to download from source: onedrive download: authentication failed", want: "source"},
		{name: "onedrive target upload", mig: db.Migration{SourceProvider: "nextcloud", TargetProvider: "onedrive"}, err: "upload to target failed: onedrive upload: authentication failed", want: "target"},
		{name: "only oauth side without direction", mig: db.Migration{SourceProvider: "onedrive", TargetProvider: "nextcloud"}, err: "authentication failed", want: "source"},
		{name: "ambiguous two oauth sides", mig: db.Migration{SourceProvider: "onedrive", TargetProvider: "dropbox"}, err: "authentication failed", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oauthAuthFailureRole(&tt.mig, tt.err); got != tt.want {
				t.Fatalf("oauthAuthFailureRole() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOAuthSyncAuthFailureRole(t *testing.T) {
	job := &db.SyncJob{SourceProvider: "nextcloud", TargetProvider: "onedrive"}
	if got := oauthSyncAuthFailureRole(job, "failed to upload to target: authentication failed"); got != "target" {
		t.Fatalf("oauthSyncAuthFailureRole() = %q, want target", got)
	}
}

func TestExpectedSizeReaderVerifyCompleteWithoutRead(t *testing.T) {
	nonEmpty := newExpectedSizeReader(bytes.NewBufferString("data"), 0)
	if err := nonEmpty.VerifyComplete(); err == nil {
		t.Fatal("VerifyComplete() without Read accepted a non-empty source indexed as zero")
	}

	empty := newExpectedSizeReader(bytes.NewBuffer(nil), 0)
	if err := empty.VerifyComplete(); err != nil {
		t.Fatalf("VerifyComplete() without Read for an empty source: %v", err)
	}
	if err := empty.VerifyComplete(); err != nil {
		t.Fatalf("successful VerifyComplete() is not idempotent: %v", err)
	}
}

func TestVerifyTargetSizeStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, _, err := verifyTargetSize(ctx, &sizeRetryProvider{}, "files", "/file")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyTargetSize() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("verifyTargetSize() waited %v after cancellation", elapsed)
	}
}

// fakeProvider is a minimal StorageProvider used to exercise transfer-decision
// helpers without any network. Only SupportsAtomicRename is meaningful here;
// the other methods panic if accidentally called by the tested code.
type fakeProvider struct {
	atomicRename bool
}

func (f *fakeProvider) Close() error { return nil }
func (f *fakeProvider) Connect(ctx context.Context) (bool, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	panic("not implemented in test")
}
func (f *fakeProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	panic("not implemented in test")
}
func (f *fakeProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return nil
}
func (f *fakeProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	panic("not implemented in test")
}
func (f *fakeProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	panic("not implemented in test")
}
func (f *fakeProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	panic("not implemented in test")
}
func (f *fakeProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	panic("not implemented in test")
}
func (f *fakeProvider) SupportsAtomicRename() bool { return f.atomicRename }
func (f *fakeProvider) VerificationMode() storage.VerificationMode {
	return storage.VerificationCryptographicHash
}

// TestUseTempThenRename verifies the overwrite/retry decision: the temp-file +
// rename pattern must only be used when BOTH an overwrite is requested AND the
// target provider supports renaming. This is what keeps providers without an
// atomic rename (e.g. S3, Immich) from leaking .tmp objects when the conflict
// strategy is OVERWRITE.
func TestUseTempThenRename(t *testing.T) {
	renameable := &fakeProvider{atomicRename: true}
	noRename := &fakeProvider{atomicRename: false}

	cases := []struct {
		name              string
		target            storage.StorageProvider
		deleteAfterUpload bool
		want              bool
	}{
		{"renameable + overwrite", renameable, true, true},
		{"renameable + no overwrite", renameable, false, false},
		{"no-rename + overwrite", noRename, true, false},
		{"no-rename + no overwrite", noRename, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := useTempThenRename(c.target, c.deleteAfterUpload); got != c.want {
				t.Fatalf("useTempThenRename(...)=%v, want %v", got, c.want)
			}
		})
	}
}

type promotionProvider struct {
	fakeProvider
	files          map[string]string
	failRenameFrom string
	deleteFailures int
}

func (p *promotionProvider) FileExists(_ context.Context, _, filePath string) (bool, int64, error) {
	_, ok := p.files[filePath]
	return ok, 0, nil
}

func (p *promotionProvider) RenameFile(_ context.Context, _, oldPath, newPath string) error {
	if oldPath == p.failRenameFrom {
		return errors.New("rename failed")
	}
	contents, ok := p.files[oldPath]
	if !ok {
		return errors.New("source missing")
	}
	delete(p.files, oldPath)
	p.files[newPath] = contents
	return nil
}

func (p *promotionProvider) DeleteFile(_ context.Context, _, filePath string) error {
	if p.deleteFailures > 0 {
		p.deleteFailures--
		return errors.New("delete failed")
	}
	delete(p.files, filePath)
	return nil
}

func TestPromoteOverwriteRestoresOriginalWhenPromotionFails(t *testing.T) {
	const (
		target = "/target.txt"
		temp   = "/target.txt.tmp"
		backup = "/target.txt.clumoove-backup-task"
	)
	p := &promotionProvider{
		files:          map[string]string{target: "original", temp: "replacement"},
		failRenameFrom: temp,
	}

	err := promoteOverwrite(context.Background(), p, "files", target, temp, backup)
	if err == nil {
		t.Fatal("promoteOverwrite() succeeded despite a failed promotion")
	}
	if got := p.files[target]; got != "original" {
		t.Fatalf("target after rollback = %q, want original", got)
	}
	if _, ok := p.files[temp]; ok {
		t.Fatalf("temporary upload %q remained after failed promotion", temp)
	}
	if _, ok := p.files[backup]; ok {
		t.Fatalf("backup %q remained after successful rollback", backup)
	}
}

func TestPromoteOverwriteRetainsBackupWhenCleanupFails(t *testing.T) {
	const (
		target = "/target.txt"
		temp   = "/target.txt.tmp"
		backup = "/target.txt.clumoove-backup-task"
	)
	p := &promotionProvider{
		files:          map[string]string{target: "original", temp: "replacement"},
		deleteFailures: 1,
	}

	if err := promoteOverwrite(context.Background(), p, "files", target, temp, backup); err == nil {
		t.Fatal("promoteOverwrite() succeeded despite a failed backup cleanup")
	}
	if got := p.files[target]; got != "replacement" {
		t.Fatalf("target = %q, want replacement", got)
	}
	if got := p.files[backup]; got != "original" {
		t.Fatalf("backup = %q, want original", got)
	}
}

func TestPromoteOverwriteHappyPath(t *testing.T) {
	const (
		target = "/target.txt"
		temp   = "/target.txt.tmp"
		backup = "/target.txt.bak-task"
	)
	p := &promotionProvider{files: map[string]string{target: "original", temp: "replacement"}}

	if err := promoteOverwrite(context.Background(), p, "files", target, temp, backup); err != nil {
		t.Fatalf("promoteOverwrite() error = %v", err)
	}
	if got := p.files[target]; got != "replacement" {
		t.Fatalf("target = %q, want replacement", got)
	}
	if _, ok := p.files[backup]; ok {
		t.Fatalf("backup %q remained after successful promotion", backup)
	}
}

func TestPromoteOverwriteWithoutExistingTarget(t *testing.T) {
	const (
		target = "/target.txt"
		temp   = "/target.txt.tmp"
		backup = "/target.txt.bak-task"
	)
	p := &promotionProvider{files: map[string]string{temp: "replacement"}}

	if err := promoteOverwrite(context.Background(), p, "files", target, temp, backup); err != nil {
		t.Fatalf("promoteOverwrite() error = %v", err)
	}
	if got := p.files[target]; got != "replacement" {
		t.Fatalf("target = %q, want replacement", got)
	}
	if _, ok := p.files[backup]; ok {
		t.Fatalf("unexpected backup %q", backup)
	}
}

func TestPromoteOverwriteCleansStaleBackupFromPreviousAttempt(t *testing.T) {
	const (
		target = "/target.txt"
		temp   = "/target.txt.tmp"
		backup = "/target.txt.bak-task"
	)
	p := &promotionProvider{files: map[string]string{
		target: "previous replacement",
		temp:   "current replacement",
		backup: "original before previous attempt",
	}}

	if err := promoteOverwrite(context.Background(), p, "files", target, temp, backup); err != nil {
		t.Fatalf("promoteOverwrite() error = %v", err)
	}
	if got := p.files[target]; got != "current replacement" {
		t.Fatalf("target = %q, want current replacement", got)
	}
	if _, ok := p.files[backup]; ok {
		t.Fatalf("stale backup %q remained after retry", backup)
	}
}

func TestOverwriteBackupPathUsesShortStableSuffix(t *testing.T) {
	if got, want := overwriteBackupPath("/target.txt", "12345678-1234-1234-1234-123456789012"), "/target.txt.bak-12345678"; got != want {
		t.Fatalf("overwriteBackupPath() = %q, want %q", got, want)
	}
}

func TestTransferTimeout(t *testing.T) {
	const mb = int64(1024 * 1024)
	cases := []struct {
		name     string
		fileSize int64
		want     time.Duration
	}{
		{"zero", 0, transferTimeoutBase},
		{"negative", -1, transferTimeoutBase},
		{"tiny", 1024, transferTimeoutBase},
		{"just below 50MiB", 50*mb - 1, transferTimeoutBase},
		{"exactly 50MiB", 50 * mb, transferTimeoutBase + 1*time.Minute},
		{"150MiB", 150 * mb, transferTimeoutBase + 3*time.Minute},
		{"huge uncapped", 11 * 1024 * mb, transferTimeoutBase + 1024*time.Minute},
		{"capped at max", int64(1) << 62, transferTimeoutMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := transferTimeout(c.fileSize)
			if c.fileSize > 0 && c.fileSize < (1<<62) {
				// For in-range sizes verify the computed value directly.
				want := transferTimeoutBase + time.Duration(c.fileSize/chunkedUploadThreshold)*transferTimeoutPerChunk
				if want > transferTimeoutMax {
					want = transferTimeoutMax
				}
				if got != want {
					t.Fatalf("transferTimeout(%d) = %v, want %v", c.fileSize, got, want)
				}
				return
			}
			if got != c.want {
				t.Fatalf("transferTimeout(%d) = %v, want %v", c.fileSize, got, c.want)
			}
		})
	}
}

func TestTransferTimeoutDeterministic(t *testing.T) {
	// Download and upload phases must share one deadline for a given size.
	for _, sz := range []int64{0, 50 * 1024 * 1024, 500 * 1024 * 1024, (int64(1) << 40)} {
		if a, b := transferTimeout(sz), transferTimeout(sz); a != b {
			t.Fatalf("transferTimeout not deterministic for size %d: %v != %v", sz, a, b)
		}
	}
}

func TestConnLossCounts(t *testing.T) {
	p := &Processor{
		connLossCounts:       sync.Map{},
		connLossTaskAttempts: sync.Map{},
	}

	// Per-task counter only counts connection-loss failures for that task.
	if got := p.recordConnLossTask("task-a"); got != 1 {
		t.Fatalf("task-a conn-loss attempt = %d, want 1", got)
	}
	if got := p.recordConnLossTask("task-a"); got != 2 {
		t.Fatalf("task-a conn-loss attempt = %d, want 2", got)
	}
	if got := p.recordConnLossTask("task-b"); got != 1 {
		t.Fatalf("task-b conn-loss attempt = %d, want 1", got)
	}

	// Migration-wide counter is independent of per-task counter.
	if got := p.recordConnLoss("mig-1"); got != 1 {
		t.Fatalf("mig-1 conn-loss = %d, want 1", got)
	}

	// Clearing per-task drops only that task's entry.
	p.clearConnLossTask("task-a")
	if got := p.recordConnLossTask("task-a"); got != 1 {
		t.Fatalf("after clear, task-a conn-loss attempt = %d, want 1", got)
	}
	// Other task untouched.
	if got := p.recordConnLossTask("task-b"); got != 2 {
		t.Fatalf("task-b should be untouched = %d, want 2", got)
	}

	// Clearing migration-wide does not touch per-task entries.
	p.clearConnLoss("mig-1")
	if got := p.recordConnLoss("mig-1"); got != 1 {
		t.Fatalf("after clear, mig-1 conn-loss = %d, want 1", got)
	}
}

func TestResolveTargetPath(t *testing.T) {
	t.Run("unconditional target join when source path matches targetDir prefix", func(t *testing.T) {
		task := &db.Task{ResourceType: "files", FilePath: "/docs/file.txt"}
		got := ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, "/docs", "nextcloud", "nextcloud")
		want := "/docs/docs/file.txt"
		if got != want {
			t.Fatalf("ResolveTargetPath() = %q, want %q", got, want)
		}
	})

	t.Run("component by component path sanitization", func(t *testing.T) {
		task := &db.Task{ResourceType: "files", FilePath: "/invalid?dir/bad:name.txt"}
		got := ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, "/targetDir", "nextcloud", "smb")
		want := "/targetDir/invalid_dir/bad_name.txt"
		if got != want {
			t.Fatalf("ResolveTargetPath() = %q, want %q", got, want)
		}
	})

	t.Run("immich source resolves uuid to human-readable target name", func(t *testing.T) {
		task := &db.Task{
			ResourceType: "files",
			FilePath:     "/asset-uuid-2",
			Metadata:     []byte(`{"immich_filename":"bad:photo?.jpg"}`),
		}
		got := ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, "/Immich Alben", "immich", "smb")
		want := "/Immich Alben/bad_photo_.jpg"
		if got != want {
			t.Fatalf("ResolveTargetPath() = %q, want %q", got, want)
		}
	})

	t.Run("virtual target provider skips sanitization", func(t *testing.T) {
		task := &db.Task{
			ResourceType: "files",
			FilePath:     "/asset-uuid-2",
			Metadata:     []byte(`{"immich_filename":"photo:1.jpg"}`),
		}
		got := ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, "/Immich Target", "immich", "immich")
		want := "/Immich Target/photo:1.jpg"
		if got != want {
			t.Fatalf("ResolveTargetPath() = %q, want %q", got, want)
		}
	})
}

func TestIsWebDAVSystemConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "propfind 400 bad request", err: errors.New("failed to check if target file exists: PROPFIND check failed with status: 400"), want: true},
		{name: "sabredav bad request", err: errors.New("Sabre\\DAV\\Exception\\BadRequest: File name is blacklisted"), want: true},
		{name: "propfind 403 forbidden", err: errors.New("PROPFIND check failed with status: 403"), want: true},
		{name: "sabredav forbidden", err: errors.New("Sabre\\DAV\\Exception\\Forbidden: .htaccess forbidden"), want: true},
		{name: "sabredav conflict", err: errors.New("Sabre\\DAV\\Exception\\Conflict"), want: true},
		{name: "http 409 conflict", err: errors.New("target file error: status: 409"), want: true},
		{name: "http 404 not found", err: errors.New("resource status 404"), want: true},
		{name: "http status 400", err: errors.New("webdav request failed with status: 400"), want: true},

		// Negative test cases (C1, C2, S4)
		{name: "numeric port in URL", err: errors.New("dial tcp 127.0.0.1:4003: connect: connection refused"), want: false},
		{name: "written byte count", err: errors.New("wrote 400 bytes to stream"), want: false},
		{name: "SMB sharing conflict", err: errors.New("smb: sharing conflict on file"), want: false},
		{name: "S3 invalid request", err: errors.New("S3 error InvalidRequest: bad request payload"), want: false},
		{name: "unrelated error", err: errors.New("connection timeout"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWebDAVSystemConflict(tt.err); got != tt.want {
				t.Fatalf("isWebDAVSystemConflict(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// entryCount is a test-only helper that reports how many keyed mutex entries are
// currently live. It is not production API.
func (k *keyedMutexes) entryCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}

func TestKeyedMutexesReleaseRemovesEntry(t *testing.T) {
	var k keyedMutexes
	for i := 0; i < 5; i++ {
		release := k.lock("same-key")
		if k.entryCount() != 1 {
			t.Fatalf("while held: entryCount = %d, want 1", k.entryCount())
		}
		release()
		if k.entryCount() != 0 {
			t.Fatalf("after release %d: entryCount = %d, want 0", i, k.entryCount())
		}
	}
}

func TestKeyedMutexesDistinctKeys(t *testing.T) {
	var k keyedMutexes
	r1 := k.lock("k1")
	if k.entryCount() != 1 {
		t.Fatalf("after k1: entryCount = %d, want 1", k.entryCount())
	}
	r2 := k.lock("k2")
	if k.entryCount() != 2 {
		t.Fatalf("after k2: entryCount = %d, want 2", k.entryCount())
	}
	r1()
	r2()
	if k.entryCount() != 0 {
		t.Fatalf("after both released: entryCount = %d, want 0", k.entryCount())
	}
}

func TestKeyedMutexesMutualExclusion(t *testing.T) {
	var k keyedMutexes
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	var inside int32
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			release := k.lock("contended")
			cur := atomic.AddInt32(&inside, 1)
			if cur != 1 {
				t.Errorf("critical section entered %d times concurrently", cur)
			}
			atomic.AddInt32(&inside, -1)
			release()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&inside); got != 0 {
		t.Fatalf("inside = %d after all goroutines, want 0", got)
	}
	if k.entryCount() != 0 {
		t.Fatalf("after all released: entryCount = %d, want 0", k.entryCount())
	}
}

func TestLockTargetFileNoOpPaths(t *testing.T) {
	p := &Processor{}
	r1 := p.lockTargetFile("")
	r1()
	r2 := p.lockTargetFile("/")
	r2()
	if got := p.targetFileLocks.entryCount(); got != 0 {
		t.Fatalf("entryCount = %d, want 0", got)
	}
}

func TestLockTargetFileSerializes(t *testing.T) {
	p := &Processor{}
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	var inside int32
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			release := p.lockTargetFile("/shared/target.txt")
			cur := atomic.AddInt32(&inside, 1)
			if cur != 1 {
				t.Errorf("target file entered %d times concurrently", cur)
			}
			atomic.AddInt32(&inside, -1)
			release()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&inside); got != 0 {
		t.Fatalf("inside = %d after all goroutines, want 0", got)
	}
	if got := p.targetFileLocks.entryCount(); got != 0 {
		t.Fatalf("entryCount = %d, want 0", got)
	}
}

func TestRefreshOAuthTokenIfNeededNoTokenTakesNoLock(t *testing.T) {
	p := &Processor{}
	mig := &db.Migration{ID: "m1", SourceRefreshTokenEncrypted: sql.NullString{}}
	got, err := p.refreshOAuthTokenIfNeeded(context.Background(), mig, "source", "tok", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tok" {
		t.Fatalf("got = %q, want %q", got, "tok")
	}
	if got := p.refreshLocks.entryCount(); got != 0 {
		t.Fatalf("refreshLocks.entryCount = %d, want 0", got)
	}
}

func TestShouldEvictThrottler(t *testing.T) {
	notFound := func() (string, error) { return "", sql.ErrNoRows }
	tests := []struct {
		name   string
		status string
		err    error
		lookup func() (string, error)
		want   bool
	}{
		{
			name: "terminal migration evicts",
			status: "COMPLETED",
			err:    nil,
			lookup: func() (string, error) { t.Fatal("lookup should not be called"); return "", nil },
			want:   true,
		},
		{
			name: "active migration keeps",
			status: "RUNNING",
			err:    nil,
			lookup: func() (string, error) { t.Fatal("lookup should not be called"); return "", nil },
			want:   false,
		},
		{
			name: "transient migration error keeps and skips lookup",
			err:  errors.New("connection refused"),
			lookup: func() (string, error) {
				t.Fatal("lookup must not be called on transient error")
				return "", nil
			},
			want: false,
		},
		{
			name:   "migration not found + running sync job keeps",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "RUNNING", nil },
			want:   false,
		},
		{
			name:   "migration not found + indexing sync job keeps",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "INDEXING", nil },
			want:   false,
		},
		{
			name:   "migration not found + verifying sync job keeps",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "VERIFYING", nil },
			want:   false,
		},
		{
			name:   "migration not found + idle sync job keeps",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "IDLE", nil },
			want:   false,
		},
		{
			name:   "migration not found + completed sync job evicts",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "COMPLETED", nil },
			want:   true,
		},
		{
			name:   "migration not found + failed sync job evicts",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "FAILED", nil },
			want:   true,
		},
		{
			name:   "migration not found + sync job not found evicts",
			err:    sql.ErrNoRows,
			lookup: notFound,
			want:   true,
		},
		{
			name:   "migration not found + transient sync error keeps",
			err:    sql.ErrNoRows,
			lookup: func() (string, error) { return "", errors.New("timeout") },
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldEvictThrottler(tt.status, tt.err, tt.lookup); got != tt.want {
				t.Fatalf("shouldEvictThrottler() = %v, want %v", got, tt.want)
			}
		})
	}
}

