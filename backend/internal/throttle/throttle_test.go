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
	r := NewThrottledReader(bytes.NewReader(make([]byte, 1<<20)), mt, context.Background())
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
	r := NewThrottledReader(bytes.NewReader(payload), mt, context.Background())
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
	r := NewUploadThrottledReader(bytes.NewReader(make([]byte, 4096)), mt, context.Background())
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
	mt := NewMigrationThrottler(10)
	mt.SetLimit(0)
	r := NewThrottledReader(bytes.NewReader(make([]byte, 1<<20)), mt, context.Background())
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
	mt := NewMigrationThrottler(1) // 1 Mbps; a large read should be cancellable.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := NewThrottledReader(bytes.NewReader(make([]byte, 1<<20)), mt, ctx)
	buf := make([]byte, 1<<20)
	_, _ = r.Read(buf) // must not panic; may return or be cancelled.
}
