package storage

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// bareCapabilityTarget implements none of the optional capabilities.
type bareCapabilityTarget struct{}

type metadataCapableTarget struct {
	bareCapabilityTarget
	resourceType, path string
	meta               FileMetadata
	err                error
}

func (m *metadataCapableTarget) ApplyMetadata(_ context.Context, resourceType, filePath string, meta FileMetadata) error {
	m.resourceType, m.path, m.meta = resourceType, filePath, meta
	return m.err
}

type typedUploadTarget struct {
	bareCapabilityTarget
	calls   []string
	opts    UploadOptions
	size    int64
	body    string
	err     error
	receipt string
}

func (u *typedUploadTarget) StreamUploadWithOptions(_ context.Context, _, _ string, stream io.Reader, size int64, opts UploadOptions) (UploadResult, error) {
	u.calls = append(u.calls, "single")
	data, _ := io.ReadAll(stream)
	u.body, u.size, u.opts = string(data), size, opts
	return UploadResult{Receipt: UploadReceipt{TargetResourceID: u.receipt}}, u.err
}

func (u *typedUploadTarget) StreamUploadChunkedWithOptions(_ context.Context, _, _ string, stream io.Reader, size int64, progress chan<- int64, opts UploadOptions) (UploadResult, error) {
	u.calls = append(u.calls, "chunked")
	data, _ := io.ReadAll(stream)
	u.body, u.size, u.opts = string(data), size, opts
	return UploadResult{Receipt: UploadReceipt{TargetResourceID: u.receipt}}, u.err
}

func TestApplyMetadataDelegatesToCapableTarget(t *testing.T) {
	target := &metadataCapableTarget{}
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	err := ApplyMetadata(context.Background(), target, "files", "/a.txt", FileMetadata{ModifiedTime: mtime})
	if err != nil {
		t.Fatalf("ApplyMetadata: %v", err)
	}
	if target.resourceType != "files" || target.path != "/a.txt" || !target.meta.ModifiedTime.Equal(mtime) {
		t.Fatalf("target got %q %q %+v", target.resourceType, target.path, target.meta)
	}
}

func TestApplyMetadataReturnsProviderError(t *testing.T) {
	boom := errors.New("boom")
	err := ApplyMetadata(context.Background(), &metadataCapableTarget{err: boom}, "files", "/a.txt", FileMetadata{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want provider error", err)
	}
	if errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("provider failure must not read as unsupported: %v", err)
	}
}

func TestApplyMetadataReportsUnsupported(t *testing.T) {
	err := ApplyMetadata(context.Background(), bareCapabilityTarget{}, "files", "/a.txt", FileMetadata{})
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want ErrCapabilityUnsupported", err)
	}
}

func TestSupportsMetadata(t *testing.T) {
	if !SupportsMetadata(&metadataCapableTarget{}) {
		t.Fatal("capable target should support metadata")
	}
	if SupportsMetadata(bareCapabilityTarget{}) {
		t.Fatal("bare target should not support metadata")
	}
}

func TestStreamUploadWithOptionsUsesSingleUploadAtThreshold(t *testing.T) {
	target := &typedUploadTarget{receipt: "id-1"}
	opts := UploadOptions{Overwrite: true, Checksum: "abc"}

	res, err := StreamUploadWithOptions(context.Background(), target, "files", "/a.bin", strings.NewReader("hello"), DefaultChunkedUploadThreshold, nil, opts)
	if err != nil {
		t.Fatalf("StreamUploadWithOptions: %v", err)
	}
	if len(target.calls) != 1 || target.calls[0] != "single" {
		t.Fatalf("calls = %v, want one single upload at the threshold", target.calls)
	}
	if target.opts.Overwrite != opts.Overwrite || target.opts.Checksum != opts.Checksum || target.body != "hello" {
		t.Fatalf("target got opts %+v body %q", target.opts, target.body)
	}
	if res.Receipt.TargetResourceID != "id-1" {
		t.Fatalf("receipt = %q, want id-1", res.Receipt.TargetResourceID)
	}
}

func TestStreamUploadWithOptionsUsesChunkedUploadAboveThreshold(t *testing.T) {
	target := &typedUploadTarget{}

	_, err := StreamUploadWithOptions(context.Background(), target, "files", "/a.bin", strings.NewReader("hello"), DefaultChunkedUploadThreshold+1, nil, UploadOptions{})
	if err != nil {
		t.Fatalf("StreamUploadWithOptions: %v", err)
	}
	if len(target.calls) != 1 || target.calls[0] != "chunked" {
		t.Fatalf("calls = %v, want one chunked upload above the threshold", target.calls)
	}
}

func TestStreamUploadWithOptionsReturnsProviderError(t *testing.T) {
	boom := errors.New("boom")
	_, err := StreamUploadWithOptions(context.Background(), &typedUploadTarget{err: boom}, "files", "/a.bin", strings.NewReader("x"), 1, nil, UploadOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want provider error", err)
	}
	if errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("provider failure must not read as unsupported: %v", err)
	}
}

func TestStreamUploadWithOptionsReportsUnsupportedWithoutReadingStream(t *testing.T) {
	stream := strings.NewReader("untouched")
	_, err := StreamUploadWithOptions(context.Background(), bareCapabilityTarget{}, "files", "/a.bin", stream, 9, nil, UploadOptions{})
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want ErrCapabilityUnsupported", err)
	}
	if stream.Len() != 9 {
		t.Fatalf("stream was consumed: %d bytes left, want 9", stream.Len())
	}
}

type duplicateDetectingTarget struct {
	bareCapabilityTarget
	native bool
}

func (d duplicateDetectingTarget) UsesNativeDuplicateDetection() bool { return d.native }

func TestUsesNativeDuplicateDetection(t *testing.T) {
	if !UsesNativeDuplicateDetection(duplicateDetectingTarget{native: true}) {
		t.Fatal("provider declaring native detection should report true")
	}
	if UsesNativeDuplicateDetection(duplicateDetectingTarget{native: false}) {
		t.Fatal("provider declining native detection should report false")
	}
	if UsesNativeDuplicateDetection(bareCapabilityTarget{}) {
		t.Fatal("bare target should report false")
	}
}

type rangeCapableSource struct {
	bareCapabilityTarget
	resourceType, path string
	offset, length     int64
	err                error
}

func (r *rangeCapableSource) StreamDownloadRange(_ context.Context, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	r.resourceType, r.path, r.offset, r.length = resourceType, filePath, offset, length
	if r.err != nil {
		return nil, r.err
	}
	return io.NopCloser(strings.NewReader("range")), nil
}

func TestStreamDownloadRangeDelegatesToCapableSource(t *testing.T) {
	source := &rangeCapableSource{}
	if !SupportsRangeDownload(source) {
		t.Fatal("capable source should support range download")
	}
	body, err := StreamDownloadRange(context.Background(), source, "files", "/pack", 10, 5)
	if err != nil {
		t.Fatalf("StreamDownloadRange: %v", err)
	}
	defer body.Close()
	if data, _ := io.ReadAll(body); string(data) != "range" {
		t.Fatalf("body = %q, want range", data)
	}
	if source.resourceType != "files" || source.path != "/pack" || source.offset != 10 || source.length != 5 {
		t.Fatalf("source got %q %q %d %d", source.resourceType, source.path, source.offset, source.length)
	}
}

func TestStreamDownloadRangeReturnsProviderError(t *testing.T) {
	boom := errors.New("boom")
	_, err := StreamDownloadRange(context.Background(), &rangeCapableSource{err: boom}, "files", "/pack", 0, 1)
	if !errors.Is(err, boom) || errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want provider error that is not unsupported", err)
	}
}

func TestStreamDownloadRangeReportsUnsupported(t *testing.T) {
	if SupportsRangeDownload(bareCapabilityTarget{}) {
		t.Fatal("bare source should not support range download")
	}
	_, err := StreamDownloadRange(context.Background(), bareCapabilityTarget{}, "files", "/pack", 0, 1)
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want ErrCapabilityUnsupported", err)
	}
}

type sizeResolvingProvider struct {
	bareCapabilityTarget
	size int64
	err  error
}

func (s sizeResolvingProvider) ResolveResourceSize(context.Context, string, string) (int64, error) {
	return s.size, s.err
}

func TestResolveResourceSize(t *testing.T) {
	size, err := ResolveResourceSize(context.Background(), sizeResolvingProvider{size: 42}, "files", "/doc")
	if err != nil || size != 42 {
		t.Fatalf("ResolveResourceSize = %d, %v; want 42, nil", size, err)
	}

	boom := errors.New("boom")
	_, err = ResolveResourceSize(context.Background(), sizeResolvingProvider{err: boom}, "files", "/doc")
	if !errors.Is(err, boom) || errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want provider error that is not unsupported", err)
	}

	_, err = ResolveResourceSize(context.Background(), bareCapabilityTarget{}, "files", "/doc")
	if !errors.Is(err, ErrCapabilityUnsupported) {
		t.Fatalf("err = %v, want ErrCapabilityUnsupported", err)
	}
}
