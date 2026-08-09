package processor

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"io"
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
		UploadContext:    context.Background(),
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
		Context:       context.Background(),
		UploadContext: context.Background(),
		Source:        source,
		Target:        target,
		ResourceType:  "files",
		SourcePath:    "/source",
		TargetPath:    "/target",
		FileSize:      6,
	})
	if err == nil {
		t.Fatal("runTransferCore() accepted a truncated source stream")
	}
}

func TestRecoveryBackoffAndProbeEligibility(t *testing.T) {
	now := time.Now()
	cases := []struct {
		attempts int
		age      time.Duration
		want     bool
	}{
		{0, 0, true}, {1, 59 * time.Second, false}, {1, time.Minute, true},
		{2, 4*time.Minute + 59*time.Second, false}, {2, 5 * time.Minute, true},
	}
	for _, tc := range cases {
		if got := shouldProbeRecovery(recoveryState{attempts: tc.attempts, lastAttempt: now.Add(-tc.age)}, now); got != tc.want {
			t.Errorf("shouldProbeRecovery(attempts=%d, age=%s) = %v, want %v", tc.attempts, tc.age, got, tc.want)
		}
	}
}

func TestRecoveryCursorKeepsMigrationAndSyncQueuesIndependent(t *testing.T) {
	p := &Processor{}
	p.setRecoveryCursor(false, "migration-1")
	p.setRecoveryCursor(true, "sync-1")
	if got := p.recoveryCursor(false); got != "migration-1" {
		t.Fatalf("migration recovery cursor = %q", got)
	}
	if got := p.recoveryCursor(true); got != "sync-1" {
		t.Fatalf("sync recovery cursor = %q", got)
	}
}

var _ storage.StorageProvider = (*memoryTransferProvider)(nil)
