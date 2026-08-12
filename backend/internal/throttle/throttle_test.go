package throttle

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestSetLimitUnlimited(t *testing.T) {
	mt := NewMigrationThrottler(0)
	r := NewThrottledReader(context.Background(), bytes.NewReader(make([]byte, 1<<20)), mt)
	buf := make([]byte, 1<<20)
	start := time.Now()
	n, err := io.ReadFull(r, buf)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if n != 1<<20 {
		t.Errorf("expected to read %d bytes, got %d", 1<<20, n)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("unlimited read took too long: %v", elapsed)
	}
}

func TestThrottledReaderPassesDataThrough(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 1024) // 8 KB
	mt := NewMigrationThrottler(0)
	r := NewThrottledReader(context.Background(), bytes.NewReader(payload), mt)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("data not passed through intact (got %d bytes, want %d)", len(got), len(payload))
	}
}

func TestUploadThrottledReaderUsesUploadLimiter(t *testing.T) {
	mt := NewMigrationThrottler(0)
	r := NewUploadThrottledReader(context.Background(), bytes.NewReader(make([]byte, 4096)), mt)
	buf := make([]byte, 4096)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("upload read failed: %v", err)
	}
	if n != 4096 {
		t.Errorf("expected 4096 bytes, got %d", n)
	}
}

func TestSetLimitChangesLimits(t *testing.T) {
	mt := NewMigrationThrottler(1)
	burst := 128 * 1024
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewThrottledReader(ctx, bytes.NewReader(make([]byte, burst+64*1024)), mt)
	if n, err := r.Read(make([]byte, burst)); err != nil || n != burst {
		t.Fatalf("initial limited read = (%d, %v), want (%d, nil)", n, err, burst)
	}
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	if n, err := r.Read(make([]byte, 64*1024)); n != 0 || err != context.Canceled {
		t.Fatalf("limited read = (%d, %v), want (0, %v)", n, err, context.Canceled)
	}

	mt.SetLimit(0)
	r = NewThrottledReader(context.Background(), bytes.NewReader(make([]byte, 1<<20)), mt)
	buf := make([]byte, 1<<20)
	start := time.Now()
	if _, err := io.ReadFull(r, buf); err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("read failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("expected fast read after disabling limit, took %v", elapsed)
	}
}

func TestThrottledReaderContextCancel(t *testing.T) {
	mt := NewMigrationThrottler(1)
	burst := 128 * 1024
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := bytes.NewReader(make([]byte, burst+64*1024))
	r := NewThrottledReader(ctx, source, mt)
	if n, err := r.Read(make([]byte, burst)); err != nil || n != burst {
		t.Fatalf("initial read = (%d, %v), want (%d, nil)", n, err, burst)
	}
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	n, err := r.Read(make([]byte, 64*1024))
	if err != context.Canceled {
		t.Fatalf("Read error = %v, want %v", err, context.Canceled)
	}
	if n != 0 {
		t.Fatalf("Read returned %d bytes after context cancellation, want 0", n)
	}
	if remaining := source.Len(); remaining != 64*1024 {
		t.Fatalf("cancelled read consumed source bytes: %d remain, want %d", remaining, 64*1024)
	}
}

func TestThrottledReaderBoundsLargeReadsToBurst(t *testing.T) {
	mt := NewMigrationThrottler(1)
	burst := 128 * 1024
	r := NewThrottledReader(context.Background(), bytes.NewReader(make([]byte, burst*2)), mt)
	n, err := r.Read(make([]byte, burst*2))
	if err != nil {
		t.Fatalf("large read failed: %v", err)
	}
	if n != burst {
		t.Fatalf("large read returned %d bytes, want burst size %d", n, burst)
	}
}

func TestDownloadAndUploadUseIndependentLimiters(t *testing.T) {
	mt := NewMigrationThrottler(1)
	burst := 128 * 1024
	download := NewThrottledReader(context.Background(), bytes.NewReader(make([]byte, burst)), mt)
	if n, err := download.Read(make([]byte, burst)); err != nil || n != burst {
		t.Fatalf("download read = (%d, %v), want (%d, nil)", n, err, burst)
	}

	upload := NewUploadThrottledReader(context.Background(), bytes.NewReader(make([]byte, 64*1024)), mt)
	n, err := upload.Read(make([]byte, 64*1024))
	if err != nil {
		t.Fatalf("upload read failed after download exhausted its bucket: %v", err)
	}
	if n != 64*1024 {
		t.Fatalf("upload read returned %d bytes, want %d", n, 64*1024)
	}
}
