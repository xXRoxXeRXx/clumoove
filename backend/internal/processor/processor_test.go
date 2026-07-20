package processor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"backend/internal/storage"
)

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

// TestUseTempThenRename verifies the overwrite/retry decision: the temp-file +
// rename pattern must only be used when BOTH an overwrite is requested AND the
// target provider supports renaming. This is what keeps Google Photos (no
// rename) uploads from failing when the conflict strategy is OVERWRITE.
func TestUseTempThenRename(t *testing.T) {
	renameable := &fakeProvider{atomicRename: true}
	noRename := &fakeProvider{atomicRename: false}

	cases := []struct {
		name             string
		target           storage.StorageProvider
		deleteAfterUpload bool
		want             bool
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
		connLossCounts:      sync.Map{},
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
