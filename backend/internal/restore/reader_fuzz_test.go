package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

func FuzzReconstructFileWithRanges(f *testing.F) {
	// Seed corpus with simple data
	f.Add([]byte("sample data for fuzz testing"), 8)
	f.Add([]byte(""), 4)
	f.Add([]byte("large block with arbitrary characters \x00\xff\xfe\x01\x02"), 16)
	f.Add([]byte("multi block boundary test payload that spans multiple blocks properly"), 10)

	f.Fuzz(func(t *testing.T, payload []byte, chunkSize int) {
		if len(payload) > 1<<20 { // Cap at 1MB
			return
		}
		if chunkSize <= 0 || chunkSize > 64<<10 {
			chunkSize = 4096
		}

		fullHash := sha256.Sum256(payload)
		var recipes []BlockRecipe
		for offset := 0; offset < len(payload); offset += chunkSize {
			end := offset + chunkSize
			if end > len(payload) {
				end = len(payload)
			}
			blockData := payload[offset:end]
			blockHash := sha256.Sum256(blockData)
			recipes = append(recipes, BlockRecipe{
				PackPath:      "/fuzz-pack",
				PackSHA256:    fullHash,
				PayloadOffset: int64(offset),
				PayloadLength: len(blockData),
				PlaintextSize: len(blockData),
				BlockSHA256:   blockHash,
			})
		}

		var output bytes.Buffer
		err := ReconstructFileWithRanges(
			context.Background(),
			&output,
			recipes,
			int64(len(payload)),
			fullHash,
			func(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error) {
				if offset < 0 || length <= 0 || int(offset+length) > len(payload) {
					return nil, errors.New("invalid range slice in mock")
				}
				slice := payload[offset : offset+length]
				return memoryReadCloser{bytes.NewReader(slice)}, nil
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
