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
