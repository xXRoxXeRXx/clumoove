package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
	"time"

	"backend/internal/backuprepo"
)

type memoryReadCloser struct{ *bytes.Reader }

func (memoryReadCloser) Close() error { return nil }

func TestReconstructFile(t *testing.T) {
	first := []byte("first block")
	second := []byte("second block")
	firstHash, secondHash := sha256.Sum256(first), sha256.Sum256(second)
	var encoded bytes.Buffer
	pack, err := backuprepo.EncodePack(&encoded, []backuprepo.Entry{{Hash: firstHash, Data: first}, {Hash: secondHash, Data: second}})
	if err != nil {
		t.Fatal(err)
	}
	var offsets []int64
	if _, err := backuprepo.ValidatePack(bytes.NewReader(encoded.Bytes()), pack.ID, func(offset int64, _ backuprepo.Entry) error {
		offsets = append(offsets, offset)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte(nil), first...), second...)
	wantHash := sha256.Sum256(want)
	var got bytes.Buffer
	err = ReconstructFile(context.Background(), &got, []BlockRecipe{
		{PackPath: "/pack", PackSHA256: pack.ID, PayloadOffset: offsets[0], PayloadLength: len(first), PlaintextSize: len(first), BlockSHA256: firstHash},
		{PackPath: "/pack", PackSHA256: pack.ID, PayloadOffset: offsets[1], PayloadLength: len(second), PlaintextSize: len(second), BlockSHA256: secondHash},
	}, int64(len(want)), wantHash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(encoded.Bytes())}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("reconstructed %q, want %q", got.Bytes(), want)
	}
}

func TestReconstructFileZeroBytes(t *testing.T) {
	var got bytes.Buffer
	emptyHash := sha256.Sum256([]byte{})
	err := ReconstructFile(context.Background(), &got, nil, 0, emptyHash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return nil, errors.New("open pack should not be called for 0 byte file")
	})
	if err != nil {
		t.Fatalf("unexpected error for 0-byte file: %v", err)
	}
	if got.Len() != 0 {
		t.Fatalf("expected 0 bytes written, got %d", got.Len())
	}
}

func TestReconstructFileCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	data := []byte("block")
	hash := sha256.Sum256(data)
	err := ReconstructFile(ctx, io.Discard, []BlockRecipe{
		{PackPath: "/pack", PackSHA256: hash, PayloadOffset: 0, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash},
	}, int64(len(data)), hash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(data)}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestReconstructFileRejectsWrongLocator(t *testing.T) {
	data := []byte("block")
	hash := sha256.Sum256(data)
	var encoded bytes.Buffer
	pack, err := backuprepo.EncodePack(&encoded, []backuprepo.Entry{{Hash: hash, Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = ReconstructFile(context.Background(), &output, []BlockRecipe{{PackPath: "/pack", PackSHA256: pack.ID, PayloadOffset: 0, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash}}, int64(len(data)), hash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(encoded.Bytes())}, nil
	})
	if err == nil {
		t.Fatal("expected invalid payload offset to fail")
	}
	if !errors.Is(err, ErrRepositoryCorrupt) {
		t.Fatalf("error = %v, want ErrRepositoryCorrupt", err)
	}
}

func TestReconstructFileHashMismatch(t *testing.T) {
	data := []byte("block")
	hash := sha256.Sum256(data)
	wrongHash := sha256.Sum256([]byte("different"))
	var encoded bytes.Buffer
	pack, err := backuprepo.EncodePack(&encoded, []backuprepo.Entry{{Hash: hash, Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	var offsets []int64
	_, _ = backuprepo.ValidatePack(bytes.NewReader(encoded.Bytes()), pack.ID, func(offset int64, _ backuprepo.Entry) error {
		offsets = append(offsets, offset)
		return nil
	})

	var output bytes.Buffer
	err = ReconstructFile(context.Background(), &output, []BlockRecipe{{PackPath: "/pack", PackSHA256: pack.ID, PayloadOffset: offsets[0], PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash}}, int64(len(data)), wrongHash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(encoded.Bytes())}, nil
	})
	if !errors.Is(err, ErrRepositoryCorrupt) {
		t.Fatalf("expected ErrRepositoryCorrupt for whole file hash mismatch, got %v", err)
	}
}

func TestReconstructFileWithRanges(t *testing.T) {
	data := []byte("range payload")
	hash := sha256.Sum256(data)
	var output bytes.Buffer
	err := ReconstructFileWithRanges(context.Background(), &output, []BlockRecipe{{PackPath: "/pack", PackSHA256: hash, PayloadOffset: 42, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash}}, int64(len(data)), hash, func(_ context.Context, gotPath string, offset, length int64) (io.ReadCloser, error) {
		if gotPath != "/pack" || offset != 42 || length != int64(len(data)) {
			t.Fatalf("range = (%q, %d, %d)", gotPath, offset, length)
		}
		return memoryReadCloser{bytes.NewReader(data)}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("reconstructed %q, want %q", output.Bytes(), data)
	}
}

func TestReconstructFileWithRangesBlockHashMismatch(t *testing.T) {
	data := []byte("block content")
	hash := sha256.Sum256(data)
	wrongBlockHash := sha256.Sum256([]byte("wrong"))
	var output bytes.Buffer

	err := ReconstructFileWithRanges(context.Background(), &output, []BlockRecipe{
		{PackPath: "/pack", PackSHA256: hash, PayloadOffset: 0, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: wrongBlockHash},
	}, int64(len(data)), hash, func(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(data)}, nil
	}, nil)

	if !errors.Is(err, ErrRepositoryCorrupt) {
		t.Fatalf("expected ErrRepositoryCorrupt for block hash mismatch, got: %v", err)
	}
}

func TestReconstructFileWithRangesRejectsShortData(t *testing.T) {
	data := []byte("block")
	hash := sha256.Sum256(data)
	err := ReconstructFileWithRanges(context.Background(), io.Discard, []BlockRecipe{{PackPath: "/pack", PackSHA256: hash, PayloadOffset: 0, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash}}, int64(len(data)), hash, func(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(data[:2])}, nil
	}, nil)
	if !errors.Is(err, ErrRepositoryCorrupt) {
		t.Fatalf("expected ErrRepositoryCorrupt for short range data, got %v", err)
	}
}

func TestReconstructFileWithRangesRejectsTrailingBytes(t *testing.T) {
	data := []byte("block")
	hash := sha256.Sum256(data)
	err := ReconstructFileWithRanges(context.Background(), io.Discard, []BlockRecipe{{PackPath: "/pack", PackSHA256: hash, PayloadOffset: 0, PayloadLength: len(data), PlaintextSize: len(data), BlockSHA256: hash}}, int64(len(data)), hash, func(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
		return memoryReadCloser{bytes.NewReader(append(data, '!'))}, nil
	}, nil)
	if err == nil {
		t.Fatal("expected trailing range bytes to fail")
	}
}

func TestNewPackReaderLimiter(t *testing.T) {
	for _, limit := range []int{0, 5} {
		if _, err := NewPackReaderLimiter(limit); err == nil {
			t.Errorf("NewPackReaderLimiter(%d) succeeded", limit)
		}
	}
	limiter, err := NewPackReaderLimiter(2)
	if err != nil {
		t.Fatal(err)
	}
	if limiter.Capacity() != 2 {
		t.Fatalf("capacity = %d, want 2", limiter.Capacity())
	}

	ctx := context.Background()
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatal(err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := limiter.Acquire(timeoutCtx); err == nil {
		t.Fatal("expected 3rd acquire to block and timeout")
	}

	limiter.Release()
	if err := limiter.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	limiter.Release()
	limiter.Release()
}
