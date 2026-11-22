package processor

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"backend/internal/storage"
)

// legacyUploadTarget implements only the untyped upload methods, so it is
// neither a storage.TransferUploader nor a storage.MetadataApplier.
type legacyUploadTarget struct {
	calls []string
}

func (t *legacyUploadTarget) StreamUpload(context.Context, string, string, io.Reader, int64) error {
	t.calls = append(t.calls, "plain")
	return nil
}

func (t *legacyUploadTarget) StreamUploadChunked(context.Context, string, string, io.Reader, int64, chan<- int64) error {
	t.calls = append(t.calls, "chunked")
	return nil
}

// typedChunkRecorder is a storage.TransferUploader that records which typed
// variant ran.
type typedChunkRecorder struct {
	legacyUploadTarget
	opts storage.UploadOptions
}

func (t *typedChunkRecorder) StreamUploadWithOptions(_ context.Context, _, _ string, _ io.Reader, _ int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	t.calls = append(t.calls, "typed-plain")
	t.opts = opts
	return storage.UploadResult{Receipt: storage.UploadReceipt{TargetResourceID: "id"}}, nil
}

func (t *typedChunkRecorder) StreamUploadChunkedWithOptions(_ context.Context, _, _ string, _ io.Reader, _ int64, _ chan<- int64, opts storage.UploadOptions) (storage.UploadResult, error) {
	t.calls = append(t.calls, "typed-chunked")
	t.opts = opts
	return storage.UploadResult{Receipt: storage.UploadReceipt{TargetResourceID: "id"}}, nil
}

func TestStreamUploadWithOptionsFallsBackToUntypedUploadsBySize(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want string
	}{
		{"at the threshold uses the plain upload", chunkedUploadThreshold, "plain"},
		{"above the threshold uses the chunked upload", chunkedUploadThreshold + 1, "chunked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := &legacyUploadTarget{}
			result, err := streamUploadWithOptions(context.Background(), target, "files", "/a.bin", strings.NewReader(""), c.size, nil, storage.UploadOptions{Overwrite: true})
			if err != nil {
				t.Fatalf("streamUploadWithOptions() error = %v", err)
			}
			if len(target.calls) != 1 || target.calls[0] != c.want {
				t.Fatalf("calls = %v, want [%s]", target.calls, c.want)
			}
			if result.Receipt.TargetResourceID != "" {
				t.Fatalf("untyped upload returned receipt %q, want none", result.Receipt.TargetResourceID)
			}
		})
	}
}

func TestStreamUploadWithOptionsPrefersTypedUploadsBySize(t *testing.T) {
	cases := []struct {
		name string
		size int64
		want string
	}{
		{"at the threshold", chunkedUploadThreshold, "typed-plain"},
		{"above the threshold", chunkedUploadThreshold + 1, "typed-chunked"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := &typedChunkRecorder{}
			result, err := streamUploadWithOptions(context.Background(), target, "files", "/a.bin", strings.NewReader(""), c.size, nil, storage.UploadOptions{Overwrite: true})
			if err != nil {
				t.Fatalf("streamUploadWithOptions() error = %v", err)
			}
			if len(target.calls) != 1 || target.calls[0] != c.want {
				t.Fatalf("calls = %v, want [%s] and no untyped upload", target.calls, c.want)
			}
			if !target.opts.Overwrite || result.Receipt.TargetResourceID != "id" {
				t.Fatalf("typed upload got opts %+v, receipt %q", target.opts, result.Receipt.TargetResourceID)
			}
		})
	}
}

// TestRunTransferCoreSkipsMetadataForTargetsWithoutMetadataSupport runs both
// real per-caller policies against a target that cannot apply metadata.
func TestRunTransferCoreSkipsMetadataForTargetsWithoutMetadataSupport(t *testing.T) {
	data := []byte("no metadata support")
	meta := storage.FileMetadata{ModifiedTime: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), Description: "note"}
	for name, policy := range map[string]*MetadataPolicy{"migration": &migrationMetadataPolicy, "sync": &syncMetadataPolicy} {
		t.Run(name, func(t *testing.T) {
			target := &memoryTransferProvider{files: map[string][]byte{}}
			if storage.SupportsMetadata(target) {
				t.Fatal("test target must not support metadata")
			}
			result, err := runTransferCore(transferRequest{
				Context:       context.Background(),
				Source:        &streamOnlyTransferSource{data: data},
				Target:        target,
				ResourceType:  "files",
				SourcePath:    "/source",
				TargetPath:    "/target",
				FileSize:      int64(len(data)),
				UploadOptions: storage.UploadOptions{Metadata: meta},
				Metadata:      policy,
			})
			if err != nil {
				t.Fatalf("runTransferCore() error = %v, want the transfer to succeed", err)
			}
			if result.MetadataErr != nil {
				t.Fatalf("MetadataErr = %v, want nil for an unsupported target", result.MetadataErr)
			}
			if string(target.files["/target"]) != string(data) {
				t.Fatalf("uploaded %q, want %q", target.files["/target"], data)
			}
		})
	}
}
