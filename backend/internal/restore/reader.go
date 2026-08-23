package restore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"backend/internal/backuprepo"
)

// ErrRepositoryCorrupt identifies immutable backup data that cannot become
// valid through retrying a target transfer.
var ErrRepositoryCorrupt = errors.New("restore repository data is corrupt")

// BlockRecipe is the immutable locator copied during restore planning. It has
// no live foreign key dependency, so compaction cannot redirect an active run.
type BlockRecipe struct {
	PackPath      string
	PackSHA256    [sha256.Size]byte
	PayloadOffset int64
	PayloadLength int
	BlockSHA256   [sha256.Size]byte
	PlaintextSize int
}

// PackOpener supplies a fresh full-pack stream. Callers enforce any shared
// process-wide reader limit around the provider operation.
type PackOpener func(ctx context.Context, path string) (io.ReadCloser, error)

// RangeOpener returns an exact byte range from an immutable pack. A nil opener
// selects the full-pack parser fallback.
type RangeOpener func(ctx context.Context, path string, offset, length int64) (io.ReadCloser, error)

// ReconstructFile streams block recipes in ordinal order into destination. A
// full-pack reread is intentional fallback behavior for providers without safe
// range downloads: the v1 parser validates framing, every entry, footer, and
// the catalogued pack SHA-256 before a selected payload is emitted.
func ReconstructFile(ctx context.Context, destination io.Writer, recipes []BlockRecipe, expectedSize int64, expectedSHA256 [sha256.Size]byte, openPack PackOpener) error {
	if destination == nil || openPack == nil || expectedSize < 0 {
		return errors.New("restore reconstruction parameters are invalid")
	}
	fileHash := sha256.New()
	var written int64
	for i := 0; i < len(recipes); {
		if err := ctx.Err(); err != nil {
			return err
		}
		packPath := recipes[i].PackPath
		packSHA256 := recipes[i].PackSHA256
		end := i + 1
		for end < len(recipes) && recipes[end].PackPath == packPath && recipes[end].PackSHA256 == packSHA256 {
			end++
		}
		group := recipes[i:end]

		for _, recipe := range group {
			if recipe.PackPath == "" || recipe.PayloadOffset < 0 || recipe.PayloadLength <= 0 || recipe.PayloadLength != recipe.PlaintextSize {
				return fmt.Errorf("%w: invalid restore block recipe", ErrRepositoryCorrupt)
			}
		}

		reader, err := openPack(ctx, packPath)
		if err != nil {
			return fmt.Errorf("open restore pack: %w", err)
		}

		extracted := make([][]byte, len(group))
		foundCount := 0

		_, validateErr := backuprepo.ValidatePack(reader, packSHA256, func(offset int64, entry backuprepo.Entry) error {
			for idx, recipe := range group {
				if recipe.PayloadOffset == offset {
					if entry.Hash != recipe.BlockSHA256 || len(entry.Data) != recipe.PayloadLength {
						return errors.New("restore block locator does not match pack entry")
					}
					if extracted[idx] != nil {
						return errors.New("restore block locator is ambiguous")
					}
					blockData := make([]byte, len(entry.Data))
					copy(blockData, entry.Data)
					extracted[idx] = blockData
					foundCount++
				}
			}
			return nil
		})
		closeErr := reader.Close()
		if validateErr != nil {
			return fmt.Errorf("%w: validate restore pack: %v", ErrRepositoryCorrupt, validateErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close restore pack: %w", closeErr)
		}
		if foundCount != len(group) {
			return fmt.Errorf("%w: restore block is absent from pack", ErrRepositoryCorrupt)
		}

		for _, data := range extracted {
			if _, err := destination.Write(data); err != nil {
				return fmt.Errorf("write reconstructed file: %w", err)
			}
			_, _ = fileHash.Write(data)
			written += int64(len(data))
		}

		i = end
	}
	if written != expectedSize {
		return fmt.Errorf("%w: reconstructed file size mismatch: got %d, want %d", ErrRepositoryCorrupt, written, expectedSize)
	}
	var actual [sha256.Size]byte
	copy(actual[:], fileHash.Sum(nil))
	if actual != expectedSHA256 {
		return fmt.Errorf("%w: reconstructed file hash mismatch", ErrRepositoryCorrupt)
	}
	return nil
}

// ReconstructFileWithRanges uses exact range downloads when the repository
// provider supports them. Each payload remains independently SHA-256 checked;
// the full-pack fallback is retained because only it can verify the complete
// pack framing and catalogued pack checksum in one read.
func ReconstructFileWithRanges(ctx context.Context, destination io.Writer, recipes []BlockRecipe, expectedSize int64, expectedSHA256 [sha256.Size]byte, openRange RangeOpener, openPack PackOpener) error {
	if openRange == nil {
		return ReconstructFile(ctx, destination, recipes, expectedSize, expectedSHA256, openPack)
	}
	if destination == nil || expectedSize < 0 {
		return errors.New("restore reconstruction parameters are invalid")
	}

	fileHash := sha256.New()
	var written int64
	for _, recipe := range recipes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if recipe.PackPath == "" || recipe.PayloadOffset < 0 || recipe.PayloadLength <= 0 || recipe.PayloadLength != recipe.PlaintextSize {
			return fmt.Errorf("%w: invalid restore block recipe", ErrRepositoryCorrupt)
		}
		reader, err := openRange(ctx, recipe.PackPath, recipe.PayloadOffset, int64(recipe.PayloadLength))
		if err != nil {
			return fmt.Errorf("open restore pack range: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(reader, int64(recipe.PayloadLength)+1))
		closeErr := reader.Close()
		if readErr != nil {
			return fmt.Errorf("read restore pack range: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close restore pack range: %w", closeErr)
		}
		if len(payload) != recipe.PayloadLength {
			return fmt.Errorf("%w: restore pack range length mismatch", ErrRepositoryCorrupt)
		}
		if sha256.Sum256(payload) != recipe.BlockSHA256 {
			return fmt.Errorf("%w: restore block hash mismatch", ErrRepositoryCorrupt)
		}
		if _, err := destination.Write(payload); err != nil {
			return fmt.Errorf("write reconstructed file: %w", err)
		}
		_, _ = fileHash.Write(payload)
		written += int64(len(payload))
	}
	if written != expectedSize {
		return fmt.Errorf("%w: reconstructed file size mismatch: got %d, want %d", ErrRepositoryCorrupt, written, expectedSize)
	}
	var actual [sha256.Size]byte
	copy(actual[:], fileHash.Sum(nil))
	if actual != expectedSHA256 {
		return fmt.Errorf("%w: reconstructed file hash mismatch", ErrRepositoryCorrupt)
	}
	return nil
}
