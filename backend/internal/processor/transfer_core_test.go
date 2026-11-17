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

type metadataTransferTarget struct {
	memoryTransferProvider
	err       error
	calls     []string
	finalSeen bool
	meta      storage.FileMetadata
	deadline  bool
}

func (t *metadataTransferTarget) ApplyMetadata(ctx context.Context, _, filePath string, meta storage.FileMetadata) error {
	_, t.finalSeen = t.files[filePath]
	_, t.deadline = ctx.Deadline()
	t.calls = append(t.calls, filePath)
	t.meta = meta
	return t.err
}

func metadataTransferRequest(source transferSource, target transferTarget, data []byte, meta storage.FileMetadata, policy *MetadataPolicy) transferRequest {
	return transferRequest{
		Context:          context.Background(),
		Source:           source,
		Target:           target,
		ResourceType:     "files",
		SourcePath:       "/source",
		TargetPath:       "/target.tmp",
		VerificationPath: "/target",
		FileSize:         int64(len(data)),
		UploadOptions:    storage.UploadOptions{Metadata: meta},
		Metadata:         policy,
		Finalize: func(context.Context) error {
			return nil
		},
	}
}

func TestRunTransferCoreAppliesMetadataToFinalPathUnderPolicy(t *testing.T) {
	data := []byte("metadata policy")
	modified := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	authErr := fmt.Errorf("denied: %w", storage.ErrAuth)
	otherErr := fmt.Errorf("props rejected")
	cases := []struct {
		name         string
		policy       *MetadataPolicy
		meta         storage.FileMetadata
		applyErr     error
		wantCalls    int
		wantErr      error
		wantNonFatal error
		wantDeadline bool
	}{
		{"nil policy never applies", nil, storage.FileMetadata{ModifiedTime: modified}, nil, 0, nil, nil, false},
		{"empty metadata is skipped", &MetadataPolicy{}, storage.FileMetadata{}, nil, 0, nil, nil, false},
		{"success", &MetadataPolicy{}, storage.FileMetadata{ModifiedTime: modified}, nil, 1, nil, nil, false},
		{"description alone is applied", &MetadataPolicy{}, storage.FileMetadata{Description: "note"}, nil, 1, nil, nil, false},
		{"default policy never fails", &MetadataPolicy{}, storage.FileMetadata{ModifiedTime: modified}, authErr, 1, nil, authErr, false},
		{"migration-style policy reports non-fatal error", &MetadataPolicy{}, storage.FileMetadata{ModifiedTime: modified}, otherErr, 1, nil, otherErr, false},
		{"auth policy fails on auth", &MetadataPolicy{Timeout: time.Minute, FailOn: failMetadataOnAuth}, storage.FileMetadata{ModifiedTime: modified}, authErr, 1, authErr, nil, true},
		{"auth policy ignores other errors", &MetadataPolicy{Timeout: time.Minute, FailOn: failMetadataOnAuth}, storage.FileMetadata{ModifiedTime: modified}, otherErr, 1, nil, otherErr, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := &metadataTransferTarget{memoryTransferProvider: memoryTransferProvider{files: map[string][]byte{}}, err: c.applyErr}
			source := &streamOnlyTransferSource{data: data}
			// Promote the staging object so the final path exists when metadata runs.
			req := metadataTransferRequest(source, target, data, c.meta, c.policy)
			req.Finalize = func(context.Context) error {
				target.files["/target"] = target.files["/target.tmp"]
				delete(target.files, "/target.tmp")
				return nil
			}
			result, err := runTransferCore(req)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("error = %v, want %v", err, c.wantErr)
				}
			} else if err != nil {
				t.Fatalf("runTransferCore() error = %v", err)
			}
			if len(target.calls) != c.wantCalls {
				t.Fatalf("ApplyMetadata calls = %v, want %d", target.calls, c.wantCalls)
			}
			if c.wantCalls > 0 {
				if target.calls[0] != "/target" || !target.finalSeen {
					t.Fatalf("metadata applied to %q (final present=%v), want the promoted /target", target.calls[0], target.finalSeen)
				}
				if !target.meta.ModifiedTime.Equal(c.meta.ModifiedTime) || target.meta.Description != c.meta.Description {
					t.Fatalf("applied metadata = %+v, want %+v", target.meta, c.meta)
				}
				if target.deadline != c.wantDeadline {
					t.Fatalf("metadata context has deadline = %v, want %v", target.deadline, c.wantDeadline)
				}
			}
			if c.wantErr == nil && !errors.Is(result.MetadataErr, c.wantNonFatal) {
				t.Fatalf("MetadataErr = %v, want %v", result.MetadataErr, c.wantNonFatal)
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
