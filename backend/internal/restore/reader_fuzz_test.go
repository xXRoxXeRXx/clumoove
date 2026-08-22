package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"
)

func FuzzReconstructFileWithRanges(f *testing.F) {
	// Seed corpus with simple data
	f.Add([]byte("sample data for fuzz testing"))
	f.Add([]byte(""))
	f.Add([]byte("large block with arbitrary characters \x00\xff\xfe\x01\x02"))

	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 1<<20 { // Cap at 1MB
			return
		}
		hash := sha256.Sum256(payload)
		recipes := []BlockRecipe{
			{
				PackPath:      "/fuzz-pack",
				PackSHA256:    hash,
				PayloadOffset: 0,
				PayloadLength: len(payload),
				PlaintextSize: len(payload),
				BlockSHA256:   hash,
			},
		}

		if len(payload) == 0 {
			recipes = nil
		}

		var output bytes.Buffer
		err := ReconstructFileWithRanges(
			context.Background(),
			&output,
			recipes,
			int64(len(payload)),
			hash,
			func(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
				return memoryReadCloser{bytes.NewReader(payload)}, nil
			},
			nil,
		)

		if err != nil {
			t.Fatalf("unexpected error during fuzz reconstruction: %v", err)
		}

		if !bytes.Equal(output.Bytes(), payload) {
			t.Fatalf("output mismatch: got %d bytes, want %d bytes", output.Len(), len(payload))
		}
	})
}
