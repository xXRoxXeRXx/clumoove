package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
	"backend/internal/throttle"
)

type syncFakeSource struct {
	data     []byte
	modified time.Time
}

func (s *syncFakeSource) StreamDownload(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *syncFakeSource) InspectResource(context.Context, string, string) (storage.CloudResource, error) {
	return storage.CloudResource{LastModified: s.modified}, nil
}

// syncFakeTarget is an in-memory provider that records the order of uploads,
// promotions and metadata calls so the sync transfer sequence can be asserted.
type syncFakeTarget struct {
	atomicRename bool
	verifyMode   storage.VerificationMode
	files        map[string][]byte
	uploads      []string
	renames      []string
	storeShort   bool
	uploadOpts   []storage.UploadOptions
	metadata     metadataRecorder
}

func newSyncFakeTarget(atomic bool) *syncFakeTarget {
	return &syncFakeTarget{
		atomicRename: atomic,
		verifyMode:   storage.VerificationSizeOnly,
		files:        make(map[string][]byte),
	}
}

func (t *syncFakeTarget) CreateParentDirectories(context.Context, string, string) error { return nil }
func (t *syncFakeTarget) SupportsAtomicRename() bool                                    { return t.atomicRename }
func (t *syncFakeTarget) VerificationMode() storage.VerificationMode                    { return t.verifyMode }
func (t *syncFakeTarget) GetFileHash(context.Context, string, string) (string, error) {
	return "", storage.ErrChecksumNotAvailable
}

func (t *syncFakeTarget) StreamUpload(_ context.Context, _, filePath string, stream io.Reader, _ int64) error {
	data, err := io.ReadAll(stream)
	if err != nil {
		return err
	}
	if t.storeShort && len(data) > 0 {
		data = data[:len(data)-1]
	}
	t.uploads = append(t.uploads, filePath)
	t.files[filePath] = data
	return nil
}

func (t *syncFakeTarget) StreamUploadChunked(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64) error {
	return t.StreamUpload(ctx, kind, filePath, stream, size)
}

// StreamUploadWithOptions makes the fake a storage.TransferUploader, which is
// how real providers receive the source checksum and modification time.
func (t *syncFakeTarget) StreamUploadWithOptions(ctx context.Context, kind, filePath string, stream io.Reader, size int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	t.uploadOpts = append(t.uploadOpts, opts)
	return storage.UploadResult{}, t.StreamUpload(ctx, kind, filePath, stream, size)
}

func (t *syncFakeTarget) StreamUploadChunkedWithOptions(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	return t.StreamUploadWithOptions(ctx, kind, filePath, stream, size, opts)
}

func (t *syncFakeTarget) FileExists(_ context.Context, _, filePath string) (bool, int64, error) {
	data, ok := t.files[filePath]
	return ok, int64(len(data)), nil
}

func (t *syncFakeTarget) DeleteFile(_ context.Context, _, filePath string) error {
	delete(t.files, filePath)
	return nil
}

func (t *syncFakeTarget) RenameFile(_ context.Context, _, oldPath, newPath string) error {
	data, ok := t.files[oldPath]
	if !ok {
		return fmt.Errorf("rename source %q missing", oldPath)
	}
	t.renames = append(t.renames, oldPath+"->"+newPath)
	delete(t.files, oldPath)
	t.files[newPath] = data
	return nil
}

// syncMetaTarget adds the optional MetadataApplier capability.
type syncMetaTarget struct{ *syncFakeTarget }

func (t syncMetaTarget) ApplyMetadata(ctx context.Context, _, filePath string, meta storage.FileMetadata) error {
	return t.metadata.record(ctx, t.files, filePath, meta)
}

func newSyncTestTask(size int) *db.Task {
	return &db.Task{ID: "task-1234567890", ResourceType: "files", FilePath: "docs/a.txt", FileSize: int64(size)}
}

func runSyncTestTransfer(t *testing.T, task *db.Task, source *syncFakeSource, target syncTransferTarget, srcProvider, tgtProvider string) error {
	t.Helper()
	return executeSyncTransfer(context.Background(), syncTransfer{
		Task:           task,
		Source:         source,
		Target:         target,
		SourceProvider: srcProvider,
		TargetProvider: tgtProvider,
		SourcePath:     "docs/a.txt",
		TargetPath:     "/dst/docs/a.txt",
		Throttler:      throttle.NewMigrationThrottler(0),
		Progress:       make(chan int64, 64),
	})
}

func TestExecuteSyncTransferRecordsTargetStreamHash(t *testing.T) {
	data := []byte("sync characterization payload")
	cases := []struct {
		name, src, tgt string
		want           string
	}{
		{"same algorithm keeps the source hash", "nextcloud", "nextcloud", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"s3 target records SHA256", "nextcloud", "s3", fmt.Sprintf("SHA256:%x", sha256.Sum256(data))},
		{"google target records MD5", "nextcloud", "google", fmt.Sprintf("MD5:%x", md5.Sum(data))},
		{"google to google keeps MD5", "google", "google", fmt.Sprintf("MD5:%x", md5.Sum(data))},
		{"onedrive target records QUICKXOR", "nextcloud", "onedrive", "QUICKXOR:" + quickXor(data)},
		{"dropbox target records DROPBOX", "nextcloud", "dropbox", "DROPBOX:" + dropboxHash(data)},
		{"hidrive target records HIDRIVE", "nextcloud", "hidrive", "HIDRIVE:" + hidriveHash(data)},
		{"dropbox source keeps its own algorithm", "dropbox", "dropbox", "DROPBOX:" + dropboxHash(data)},
		{"box target records SHA1", "nextcloud", "box", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"dropbox to box records SHA1", "dropbox", "box", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"google to box records SHA1", "google", "box", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"onedrive to box records SHA1", "onedrive", "box", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"box source to nextcloud records SHA1", "box", "nextcloud", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := newSyncTestTask(len(data))
			target := newSyncFakeTarget(true)
			if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, c.src, c.tgt); err != nil {
				t.Fatalf("executeSyncTransfer() error = %v", err)
			}
			if task.WorkerHash.String != c.want || task.TargetHash.String != c.want {
				t.Fatalf("worker/target hash = %q/%q, want %q", task.WorkerHash.String, task.TargetHash.String, c.want)
			}
			if !task.ChecksumVerified {
				t.Fatal("size-only target must be marked checksum verified")
			}
			if !bytes.Equal(target.files["/dst/docs/a.txt"], data) {
				t.Fatal("target file does not hold the source bytes")
			}
		})
	}
}

func quickXor(data []byte) string {
	h := storage.NewQuickXorHasher()
	h.Write(data)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func dropboxHash(data []byte) string {
	h := storage.NewDropboxHasher()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func hidriveHash(data []byte) string {
	h := storage.NewHiDriveHasher()
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestExecuteSyncTransferForwardsSourceChecksum(t *testing.T) {
	data := []byte("checksum forwarding")
	cases := []struct {
		name         string
		srcProvider  string
		sourceHash   string
		wantChecksum string
	}{
		{"parsed source hash is forwarded", "nextcloud", "SHA1:ABCDEF", "SHA1:abcdef"},
		{"webdav source hash is ignored", "webdav", "SHA1:abcdef", ""},
		{"hidrive native hash is ignored", "hidrive", "HIDRIVE:abcdef", ""},
		{"no source hash forwards nothing", "nextcloud", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := newSyncTestTask(len(data))
			task.SourceHash = sql.NullString{String: c.sourceHash, Valid: c.sourceHash != ""}
			target := newSyncFakeTarget(false)
			if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, c.srcProvider, "nextcloud"); err != nil {
				t.Fatalf("executeSyncTransfer() error = %v", err)
			}
			if len(target.uploadOpts) != 1 || target.uploadOpts[0].Checksum != c.wantChecksum {
				t.Fatalf("upload options = %+v, want checksum %q", target.uploadOpts, c.wantChecksum)
			}
		})
	}
}

func TestExecuteSyncTransferForwardsSourceModifiedTimeToUpload(t *testing.T) {
	data := []byte("upload mtime")
	modified := time.Date(2026, 9, 8, 7, 6, 5, 0, time.UTC)
	task := newSyncTestTask(len(data))
	target := newSyncFakeTarget(false)
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data, modified: modified}, target, "nextcloud", "nextcloud"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if len(target.uploadOpts) != 1 || !target.uploadOpts[0].Metadata.ModifiedTime.Equal(modified) {
		t.Fatalf("upload options = %+v, want the source modification time", target.uploadOpts)
	}
	if !target.uploadOpts[0].Overwrite {
		t.Fatalf("sync upload options Overwrite = false, want true")
	}
}

func TestExecuteSyncTransferHashVerificationDeferredForCryptographicTargets(t *testing.T) {
	data := []byte("hash mode")
	task := newSyncTestTask(len(data))
	target := newSyncFakeTarget(true)
	target.verifyMode = storage.VerificationCryptographicHash // GetFileHash reports unavailable
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, "nextcloud", "nextcloud"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if task.ChecksumVerified {
		t.Fatal("ChecksumVerified must stay false when the inline hash lookup is unavailable")
	}
	if !task.WorkerHash.Valid || !task.TargetHash.Valid {
		t.Fatal("stream hash must still be recorded for the verifier daemon")
	}
}

func TestExecuteSyncTransferStagesAtomicRenameTargets(t *testing.T) {
	data := []byte("new content")
	task := newSyncTestTask(len(data))
	target := newSyncFakeTarget(true)
	target.files["/dst/docs/a.txt"] = []byte("old content")
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, "nextcloud", "nextcloud"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if len(target.uploads) != 1 || target.uploads[0] != "/dst/docs/a.txt.tmp" {
		t.Fatalf("uploads = %v, want a single upload to the .tmp staging path", target.uploads)
	}
	if got := string(target.files["/dst/docs/a.txt"]); got != "new content" {
		t.Fatalf("final target = %q, want replaced content", got)
	}
	for name := range target.files {
		if name != "/dst/docs/a.txt" {
			t.Fatalf("leftover object %q after promotion", name)
		}
	}
	wantBackup := overwriteBackupPath("/dst/docs/a.txt", task.ID)
	if len(target.renames) < 2 || !strings.Contains(strings.Join(target.renames, ","), "/dst/docs/a.txt->"+wantBackup) {
		t.Fatalf("renames = %v, want the existing target preserved at %s", target.renames, wantBackup)
	}
}

func TestExecuteSyncTransferUploadsDirectlyWithoutAtomicRename(t *testing.T) {
	data := []byte("direct")
	task := newSyncTestTask(len(data))
	target := newSyncFakeTarget(false)
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, "nextcloud", "s3"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if len(target.uploads) != 1 || target.uploads[0] != "/dst/docs/a.txt" {
		t.Fatalf("uploads = %v, want a single direct upload", target.uploads)
	}
	if len(target.renames) != 0 {
		t.Fatalf("renames = %v, want none", target.renames)
	}
}

func TestExecuteSyncTransferRejectsTargetSizeMismatch(t *testing.T) {
	data := []byte("size check")
	task := newSyncTestTask(len(data))
	target := newSyncFakeTarget(false)
	target.storeShort = true
	err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, target, "nextcloud", "nextcloud")
	if err == nil || !strings.Contains(err.Error(), "target size mismatch") {
		t.Fatalf("error = %v, want target size mismatch", err)
	}
	if task.WorkerHash.Valid || task.ChecksumVerified {
		t.Fatal("hash and verification state must not be recorded for a failed transfer")
	}
}

func TestExecuteSyncTransferCleansStagingAfterTruncatedSource(t *testing.T) {
	task := newSyncTestTask(10) // source stream yields 5 bytes only
	target := newSyncFakeTarget(true)
	err := runSyncTestTransfer(t, task, &syncFakeSource{data: []byte("short")}, target, "nextcloud", "nextcloud")
	if err == nil {
		t.Fatal("executeSyncTransfer() accepted a truncated source stream")
	}
	if len(target.files) != 0 {
		t.Fatalf("staging objects left behind: %v", target.files)
	}
}

func TestExecuteSyncTransferAppliesSourceModifiedTimeToFinalPath(t *testing.T) {
	data := []byte("mtime")
	modified := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	task := newSyncTestTask(len(data))
	base := newSyncFakeTarget(true)
	target := syncMetaTarget{base}
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data, modified: modified}, target, "nextcloud", "nextcloud"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if len(base.metadata.calls) != 1 {
		t.Fatalf("ApplyMetadata calls = %d, want 1", len(base.metadata.calls))
	}
	call := base.metadata.calls[0]
	if call.path != "/dst/docs/a.txt" || !call.finalPresent || !call.meta.ModifiedTime.Equal(modified) {
		t.Fatalf("ApplyMetadata call = %+v, want the final promoted path with the source mtime", call)
	}
}

func TestExecuteSyncTransferSkipsMetadataWithoutSourceModifiedTime(t *testing.T) {
	data := []byte("no mtime")
	task := newSyncTestTask(len(data))
	base := newSyncFakeTarget(true)
	if err := runSyncTestTransfer(t, task, &syncFakeSource{data: data}, syncMetaTarget{base}, "nextcloud", "nextcloud"); err != nil {
		t.Fatalf("executeSyncTransfer() error = %v", err)
	}
	if len(base.metadata.calls) != 0 {
		t.Fatalf("ApplyMetadata calls = %d, want 0", len(base.metadata.calls))
	}
}

func TestExecuteSyncTransferMetadataFailurePolicy(t *testing.T) {
	data := []byte("policy")
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := []struct {
		name    string
		metaErr error
		wantErr bool
	}{
		{"authentication failure fails the task", fmt.Errorf("apply: %w", storage.ErrAuth), true},
		{"other metadata failures are ignored", errors.New("boom"), false},
		{"unsupported platform is ignored", storage.ErrUnsupportedOnPlatform, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := newSyncTestTask(len(data))
			base := newSyncFakeTarget(true)
			base.metadata.err = c.metaErr
			err := runSyncTestTransfer(t, task, &syncFakeSource{data: data, modified: modified}, syncMetaTarget{base}, "nextcloud", "nextcloud")
			if c.wantErr {
				if !errors.Is(err, storage.ErrAuth) {
					t.Fatalf("error = %v, want ErrAuth", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("executeSyncTransfer() error = %v", err)
			}
			if !task.WorkerHash.Valid {
				t.Fatal("task must still record its hash after a non-fatal metadata failure")
			}
		})
	}
}
