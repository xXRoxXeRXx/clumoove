package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"backend/internal/storage"
)

type memoryTransferProvider struct {
	fakeProvider
	download []byte
	files    map[string][]byte
}

func (p *memoryTransferProvider) StreamDownload(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(p.download)), nil
}

func (p *memoryTransferProvider) StreamUpload(_ context.Context, _, filePath string, stream io.Reader, _ int64) error {
	data, err := io.ReadAll(stream)
	if err == nil {
		p.files[filePath] = data
	}
	return err
}

func (p *memoryTransferProvider) StreamUploadChunked(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64) error {
	return p.StreamUpload(ctx, kind, filePath, stream, size)
}

func (p *memoryTransferProvider) FileExists(_ context.Context, _, filePath string) (bool, int64, error) {
	data, ok := p.files[filePath]
	return ok, int64(len(data)), nil
}

func TestRunTransferCoreStreamsHashesAndVerifiesPromotedPath(t *testing.T) {
	data := []byte("transfer core mock vector")
	source := &memoryTransferProvider{download: data}
	target := &memoryTransferProvider{files: make(map[string][]byte)}
	finalized := false
	result, err := runTransferCore(transferRequest{
		Context:          context.Background(),
		Source:           source,
		Target:           target,
		SourceProvider:   "nextcloud",
		TargetProvider:   "google",
		ResourceType:     "files",
		SourcePath:       "/source",
		TargetPath:       "/target.tmp",
		VerificationPath: "/target",
		FileSize:         int64(len(data)),
		Finalize: func(context.Context) error {
			finalized = true
			target.files["/target"] = target.files["/target.tmp"]
			delete(target.files, "/target.tmp")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("runTransferCore() error = %v", err)
	}
	if !finalized || !bytes.Equal(target.files["/target"], data) {
		t.Fatal("transfer did not upload and finalize the expected bytes")
	}
	wantSource := sha1.Sum(data)
	if got := hex.EncodeToString(result.SourceHasher.Sum(nil)); got != hex.EncodeToString(wantSource[:]) {
		t.Fatalf("source hash = %s", got)
	}
	wantTarget := md5.Sum(data)
	if got := hex.EncodeToString(result.TargetHasher.Sum(nil)); got != hex.EncodeToString(wantTarget[:]) {
		t.Fatalf("target hash = %s", got)
	}
}

func TestRunTransferCoreRejectsTruncatedDownload(t *testing.T) {
	source := &memoryTransferProvider{download: []byte("short")}
	target := &memoryTransferProvider{files: make(map[string][]byte)}
	_, err := runTransferCore(transferRequest{
		Context:      context.Background(),
		Source:       source,
		Target:       target,
		ResourceType: "files",
		SourcePath:   "/source",
		TargetPath:   "/target",
		FileSize:     6,
	})
	if err == nil {
		t.Fatal("runTransferCore() accepted a truncated source stream")
	}
}

func TestTransferHashersUseMD5ForKoofr(t *testing.T) {
	algorithm, expected, sourceHasher := transferSourceHasher("koofr", "MD5:ABCDEF")
	if algorithm != "MD5" || expected != "abcdef" {
		t.Fatalf("transferSourceHasher(koofr) = (%q, %q), want MD5 and normalized hash", algorithm, expected)
	}
	if got := fmt.Sprintf("%T", sourceHasher); !strings.Contains(got, "md5") {
		t.Fatalf("source hasher type = %s, want MD5", got)
	}

	targetAlgorithm, targetHasher := transferTargetHasher("koofr")
	if targetAlgorithm != "MD5" || !strings.Contains(fmt.Sprintf("%T", targetHasher), "md5") {
		t.Fatalf("transferTargetHasher(koofr) = (%q, %T), want MD5", targetAlgorithm, targetHasher)
	}
}

func TestTransferHashersHandleKoofrMixedAlgorithms(t *testing.T) {
	sourceAlgorithm, _, sourceHasher := transferSourceHasher("koofr", "MD5:abcdef")
	targetAlgorithm, targetHasher := transferTargetHasher("nextcloud")
	if sourceAlgorithm != "MD5" || targetAlgorithm != "SHA1" {
		t.Fatalf("koofr -> nextcloud algorithms = %s -> %s", sourceAlgorithm, targetAlgorithm)
	}
	if !strings.Contains(fmt.Sprintf("%T", sourceHasher), "md5") || !strings.Contains(fmt.Sprintf("%T", targetHasher), "sha1") {
		t.Fatalf("koofr -> nextcloud hashers = %T -> %T", sourceHasher, targetHasher)
	}

	sourceAlgorithm, _, sourceHasher = transferSourceHasher("nextcloud", "")
	targetAlgorithm, targetHasher = transferTargetHasher("koofr")
	if sourceAlgorithm != "SHA1" || targetAlgorithm != "MD5" {
		t.Fatalf("nextcloud -> koofr algorithms = %s -> %s", sourceAlgorithm, targetAlgorithm)
	}
	if !strings.Contains(fmt.Sprintf("%T", sourceHasher), "sha1") || !strings.Contains(fmt.Sprintf("%T", targetHasher), "md5") {
		t.Fatalf("nextcloud -> koofr hashers = %T -> %T", sourceHasher, targetHasher)
	}
}

// TestTransferSourceHasherRules pins the source-side hash selection, including
// the WebDAV listing-hash exclusion and HiDrive's chash, which cannot be
// recomputed by streaming and therefore never becomes an expected hash.
func TestTransferSourceHasherRules(t *testing.T) {
	cases := []struct {
		name         string
		provider     string
		listedHash   string
		wantAlgo     string
		wantExpected string
		wantHasher   string
	}{
		{"no listed hash streams SHA1", "nextcloud", "", "SHA1", "", "sha1"},
		{"listed SHA256 is honoured", "nextcloud", "SHA256:AB", "SHA256", "ab", "sha256"},
		{"listed MD5 is honoured", "nextcloud", "MD5:AB", "MD5", "ab", "md5"},
		{"listed Dropbox hash is honoured", "nextcloud", "DROPBOX:ab", "DROPBOX", "ab", "DropboxHasher"},
		{"listed HiDrive chash is not recomputable", "nextcloud", "HIDRIVE:ab", "SHA1", "", "sha1"},
		{"hidrive source never streams chash", "hidrive", "HIDRIVE:ab", "SHA1", "", "sha1"},
		{"webdav listed hash is excluded", "webdav", "SHA1:AB", "SHA1", "", "sha1"},
		{"s3 source follows its listing", "s3", "SHA256:AB", "SHA256", "ab", "sha256"},
		{"s3 source without listing streams SHA1", "s3", "", "SHA1", "", "sha1"},
		{"dropbox is always Dropbox", "dropbox", "", "DROPBOX", "", "DropboxHasher"},
		{"dropbox keeps a listed value", "dropbox", "SHA1:ab", "DROPBOX", "ab", "DropboxHasher"},
		{"google is always MD5", "google", "", "MD5", "", "md5"},
		{"koofr is always MD5", "koofr", "MD5:ABCDEF", "MD5", "abcdef", "md5"},
		{"onedrive is always QuickXor", "onedrive", "QUICKXOR:Ab", "QUICKXOR", "", "QuickXorHasher"},
		{"unknown provider streams SHA1", "not-a-provider", "", "SHA1", "", "sha1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			algo, expected, hasher := transferSourceHasher(tc.provider, tc.listedHash)
			if algo != tc.wantAlgo || expected != tc.wantExpected {
				t.Fatalf("transferSourceHasher(%q, %q) = (%q, %q), want (%q, %q)", tc.provider, tc.listedHash, algo, expected, tc.wantAlgo, tc.wantExpected)
			}
			if got := fmt.Sprintf("%T", hasher); !strings.Contains(got, tc.wantHasher) {
				t.Fatalf("hasher type = %s, want %q", got, tc.wantHasher)
			}
		})
	}
}

// TestTransferTargetHasherRules pins the target-side algorithms recorded for
// existing transfers, independent of the catalogue declarations.
func TestTransferTargetHasherRules(t *testing.T) {
	cases := map[string]struct{ algo, hasher string }{
		"dropbox":   {"DROPBOX", "DropboxHasher"},
		"s3":        {"SHA256", "sha256"},
		"google":    {"MD5", "md5"},
		"koofr":     {"MD5", "md5"},
		"hidrive":   {"HIDRIVE", "HiDriveHasher"},
		"onedrive":  {"QUICKXOR", "QuickXorHasher"},
		"nextcloud": {"SHA1", "sha1"},
		"webdav":    {"SHA1", "sha1"},
		"box":       {"SHA1", "sha1"},
		"":          {"SHA1", "sha1"},
	}
	for provider, want := range cases {
		algo, hasher := transferTargetHasher(provider)
		if algo != want.algo || !strings.Contains(fmt.Sprintf("%T", hasher), want.hasher) {
			t.Errorf("transferTargetHasher(%q) = (%q, %T), want (%q, %s)", provider, algo, hasher, want.algo, want.hasher)
		}
	}
}

var _ transferSource = (*memoryTransferProvider)(nil)
var _ transferTarget = (*memoryTransferProvider)(nil)
var _ transferSource = (storage.StorageProvider)(nil)
var _ transferTarget = (storage.StorageProvider)(nil)

type streamOnlyTransferSource struct {
	data []byte
}

func (s *streamOnlyTransferSource) StreamDownload(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

type uploadOnlyTransferTarget struct {
	files map[string][]byte
}

func (t *uploadOnlyTransferTarget) StreamUpload(_ context.Context, _, filePath string, stream io.Reader, _ int64) error {
	data, err := io.ReadAll(stream)
	if err == nil {
		t.files[filePath] = data
	}
	return err
}

func (t *uploadOnlyTransferTarget) StreamUploadChunked(ctx context.Context, kind, filePath string, stream io.Reader, size int64, _ chan<- int64) error {
	return t.StreamUpload(ctx, kind, filePath, stream, size)
}

func (t *uploadOnlyTransferTarget) FileExists(_ context.Context, _, filePath string) (bool, int64, error) {
	data, ok := t.files[filePath]
	return ok, int64(len(data)), nil
}

func TestRunTransferCoreAcceptsMinimalSourceAndTarget(t *testing.T) {
	data := []byte("narrow transfer core contract")
	source := &streamOnlyTransferSource{data: data}
	target := &uploadOnlyTransferTarget{files: make(map[string][]byte)}

	_, err := runTransferCore(transferRequest{
		Context:      context.Background(),
		Source:       source,
		Target:       target,
		ResourceType: "files",
		SourcePath:   "/source",
		TargetPath:   "/target",
		FileSize:     int64(len(data)),
	})
	if err != nil {
		t.Fatalf("runTransferCore() error = %v", err)
	}
	if !bytes.Equal(target.files["/target"], data) {
		t.Fatalf("uploaded bytes = %q, want %q", target.files["/target"], data)
	}
}

type typedUploadTarget struct {
	gotOpts        storage.UploadOptions
	gotResourceID  string
	pathFileExists bool
	receiptID      string
	uploadedSize   int64
}

func (t *typedUploadTarget) StreamUpload(context.Context, string, string, io.Reader, int64) error {
	return fmt.Errorf("unexpected untyped StreamUpload call")
}

func (t *typedUploadTarget) StreamUploadChunked(context.Context, string, string, io.Reader, int64, chan<- int64) error {
	return fmt.Errorf("unexpected untyped StreamUploadChunked call")
}

func (t *typedUploadTarget) FileExists(context.Context, string, string) (bool, int64, error) {
	t.pathFileExists = true
	return false, 0, nil
}

func (t *typedUploadTarget) StreamUploadWithOptions(_ context.Context, _, _ string, stream io.Reader, _ int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	t.gotOpts = opts
	n, err := io.Copy(io.Discard, stream)
	t.uploadedSize = n
	return storage.UploadResult{Receipt: storage.UploadReceipt{TargetResourceID: t.receiptID}}, err
}

func (t *typedUploadTarget) StreamUploadChunkedWithOptions(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, _ chan<- int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	return t.StreamUploadWithOptions(ctx, resourceType, filePath, stream, size, opts)
}

func (t *typedUploadTarget) FileExistsByResourceID(_ context.Context, _, resourceID string) (bool, int64, error) {
	t.gotResourceID = resourceID
	return true, t.uploadedSize, nil
}

func (t *typedUploadTarget) GetFileHashByResourceID(context.Context, string, string) (string, error) {
	return "", storage.ErrChecksumNotAvailable
}

func TestRunTransferCoreForwardsTypedUploadOptionsAndVerifiesByReceiptID(t *testing.T) {
	data := []byte("typed transfer payload")
	modified := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	source := &streamOnlyTransferSource{data: data}
	target := &typedUploadTarget{receiptID: "immich-asset-42"}

	result, err := runTransferCore(transferRequest{
		Context:        context.Background(),
		Source:         source,
		Target:         target,
		SourceProvider: "nextcloud",
		TargetProvider: "immich",
		ResourceType:   "files",
		SourcePath:     "/photo.jpg",
		TargetPath:     "/photo.jpg",
		FileSize:       int64(len(data)),
		SourceHash:     "SHA1:deadbeef",
		UploadOptions: storage.UploadOptions{
			Metadata: storage.FileMetadata{
				ModifiedTime: modified,
				CustomProps:  map[string]string{"immich_filename": "photo.jpg"},
			},
		},
	})
	if err != nil {
		t.Fatalf("runTransferCore() error = %v", err)
	}
	if !target.gotOpts.Metadata.ModifiedTime.Equal(modified) || target.gotOpts.Metadata.CustomProps["immich_filename"] != "photo.jpg" {
		t.Fatalf("forwarded metadata = %#v", target.gotOpts.Metadata)
	}
	if target.gotOpts.Checksum != "SHA1:deadbeef" {
		t.Fatalf("forwarded checksum = %q, want SHA1:deadbeef", target.gotOpts.Checksum)
	}
	if target.pathFileExists {
		t.Fatal("expected verification by TargetResourceVerifier rather than path FileExists")
	}
	if target.gotResourceID != "immich-asset-42" {
		t.Fatalf("verified resource ID = %q, want immich-asset-42", target.gotResourceID)
	}
	if result.UploadReceipt.TargetResourceID != "immich-asset-42" {
		t.Fatalf("result.UploadReceipt.TargetResourceID = %q, want immich-asset-42", result.UploadReceipt.TargetResourceID)
	}
}

// metadataCall records one ApplyMetadata invocation.
type metadataCall struct {
	path         string
	meta         storage.FileMetadata
	finalPresent bool
	hasDeadline  bool
}

// metadataRecorder is the shared ApplyMetadata fake for migration and sync tests.
type metadataRecorder struct {
	err   error
	calls []metadataCall
}

func (r *metadataRecorder) record(ctx context.Context, files map[string][]byte, filePath string, meta storage.FileMetadata) error {
	_, present := files[filePath]
	_, deadline := ctx.Deadline()
	r.calls = append(r.calls, metadataCall{path: filePath, meta: meta, finalPresent: present, hasDeadline: deadline})
	return r.err
}

type metadataTransferTarget struct {
	memoryTransferProvider
	metadataRecorder
}

func (t *metadataTransferTarget) ApplyMetadata(ctx context.Context, _, filePath string, meta storage.FileMetadata) error {
	return t.record(ctx, t.files, filePath, meta)
}

// TestRunTransferCoreAppliesMetadataToFinalPathUnderPolicy runs the real
// per-caller policies, so one table covers the migration and sync paths.
func TestRunTransferCoreAppliesMetadataToFinalPathUnderPolicy(t *testing.T) {
	data := []byte("metadata policy")
	modified := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	withTime := storage.FileMetadata{ModifiedTime: modified}
	authErr := fmt.Errorf("denied: %w", storage.ErrAuth)
	otherErr := errors.New("props rejected")
	cases := []struct {
		name         string
		policy       *MetadataPolicy
		meta         storage.FileMetadata
		applyErr     error
		wantCalls    int
		wantFatalErr error // returned by runTransferCore
		wantMetaErr  error // reported in transferResult.MetadataErr
		wantDeadline bool
	}{
		{name: "nil policy never applies", meta: withTime},
		{name: "empty metadata is skipped", policy: &migrationMetadataPolicy},
		{name: "migration applies without a deadline", policy: &migrationMetadataPolicy, meta: withTime, wantCalls: 1},
		{name: "description alone is applied", policy: &migrationMetadataPolicy, meta: storage.FileMetadata{Description: "note"}, wantCalls: 1},
		{name: "migration never fails on auth", policy: &migrationMetadataPolicy, meta: withTime, applyErr: authErr, wantCalls: 1, wantMetaErr: authErr},
		{name: "migration reports other failures", policy: &migrationMetadataPolicy, meta: withTime, applyErr: otherErr, wantCalls: 1, wantMetaErr: otherErr},
		{name: "migration reports unsupported platform", policy: &migrationMetadataPolicy, meta: withTime, applyErr: storage.ErrUnsupportedOnPlatform, wantCalls: 1, wantMetaErr: storage.ErrUnsupportedOnPlatform},
		{name: "sync succeeds under a deadline", policy: &syncMetadataPolicy, meta: withTime, wantCalls: 1, wantDeadline: true},
		{name: "sync fails on auth", policy: &syncMetadataPolicy, meta: withTime, applyErr: authErr, wantCalls: 1, wantFatalErr: authErr, wantDeadline: true},
		{name: "sync ignores other failures", policy: &syncMetadataPolicy, meta: withTime, applyErr: otherErr, wantCalls: 1, wantMetaErr: otherErr, wantDeadline: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := &metadataTransferTarget{memoryTransferProvider: memoryTransferProvider{files: map[string][]byte{}}}
			target.err = c.applyErr
			req := transferRequest{
				Context:          context.Background(),
				Source:           &streamOnlyTransferSource{data: data},
				Target:           target,
				ResourceType:     "files",
				SourcePath:       "/source",
				TargetPath:       "/target.tmp",
				VerificationPath: "/target",
				FileSize:         int64(len(data)),
				UploadOptions:    storage.UploadOptions{Metadata: c.meta},
				Metadata:         c.policy,
				// Promote the staging object so the final path exists when metadata runs.
				Finalize: func(context.Context) error {
					target.files["/target"] = target.files["/target.tmp"]
					delete(target.files, "/target.tmp")
					return nil
				},
			}
			result, err := runTransferCore(req)
			if c.wantFatalErr != nil {
				if !errors.Is(err, c.wantFatalErr) {
					t.Fatalf("error = %v, want %v", err, c.wantFatalErr)
				}
			} else if err != nil {
				t.Fatalf("runTransferCore() error = %v", err)
			}
			if len(target.calls) != c.wantCalls {
				t.Fatalf("ApplyMetadata calls = %v, want %d", target.calls, c.wantCalls)
			}
			if c.wantCalls > 0 {
				call := target.calls[0]
				if call.path != "/target" || !call.finalPresent {
					t.Fatalf("metadata applied to %q (final present=%v), want the promoted /target", call.path, call.finalPresent)
				}
				if !call.meta.ModifiedTime.Equal(c.meta.ModifiedTime) || call.meta.Description != c.meta.Description {
					t.Fatalf("applied metadata = %+v, want %+v", call.meta, c.meta)
				}
				if call.hasDeadline != c.wantDeadline {
					t.Fatalf("metadata context has deadline = %v, want %v", call.hasDeadline, c.wantDeadline)
				}
			}
			if c.wantFatalErr == nil && !errors.Is(result.MetadataErr, c.wantMetaErr) {
				t.Fatalf("MetadataErr = %v, want %v", result.MetadataErr, c.wantMetaErr)
			}
		})
	}
}

func TestTransferResultWorkerHashPrefersTargetStream(t *testing.T) {
	data := []byte("worker hash")
	for _, c := range []struct {
		name, src, tgt string
		want           string
	}{
		{"same algorithm uses the source stream", "nextcloud", "nextcloud", fmt.Sprintf("SHA1:%x", sha1.Sum(data))},
		{"different algorithm uses the target stream", "nextcloud", "google", fmt.Sprintf("MD5:%x", md5.Sum(data))},
	} {
		t.Run(c.name, func(t *testing.T) {
			result, err := runTransferCore(transferRequest{
				Context:        context.Background(),
				Source:         &streamOnlyTransferSource{data: data},
				Target:         &uploadOnlyTransferTarget{files: map[string][]byte{}},
				SourceProvider: c.src,
				TargetProvider: c.tgt,
				ResourceType:   "files",
				SourcePath:     "/source",
				TargetPath:     "/target",
				FileSize:       int64(len(data)),
			})
			if err != nil {
				t.Fatalf("runTransferCore() error = %v", err)
			}
			if got := result.WorkerHash(); got != c.want {
				t.Fatalf("WorkerHash() = %q, want %q", got, c.want)
			}
		})
	}
}
func TestRunTransferCoreAbortsStagingOnlyBeforeFinalize(t *testing.T) {
	data := []byte("abort hook")
	cases := []struct {
		name      string
		size      int64
		finalize  func(context.Context) error
		wantAbort bool
	}{
		{"truncated source aborts", int64(len(data)) + 1, nil, true},
		{"finalize failure does not abort", int64(len(data)), func(context.Context) error { return errors.New("promote failed") }, false},
		{"success does not abort", int64(len(data)), func(context.Context) error { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := &memoryTransferProvider{files: map[string][]byte{}}
			aborted := false
			_, _ = runTransferCore(transferRequest{
				Context:      context.Background(),
				Source:       &streamOnlyTransferSource{data: data},
				Target:       target,
				ResourceType: "files",
				SourcePath:   "/source",
				TargetPath:   "/target",
				FileSize:     c.size,
				Finalize:     c.finalize,
				AbortUpload:  func(context.Context) { aborted = true },
			})
			if aborted != c.wantAbort {
				t.Fatalf("abort called = %v, want %v", aborted, c.wantAbort)
			}
		})
	}
}
