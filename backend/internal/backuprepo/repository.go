// Package backuprepo implements the Clumoove backup repository v1 wire format.
package backuprepo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"strings"
)

const (
	FormatVersion  uint16 = 1
	BlockSize             = 4 * 1024 * 1024
	MaxPackSize           = 64 * 1024 * 1024
	packPrefixSize        = 16
	packFooterSize        = 20
)

var (
	packMagic   = [8]byte{'C', 'L', 'M', 'P', 'A', 'C', 'K', '1'}
	footerMagic = [8]byte{'C', 'L', 'M', 'E', 'N', 'D', '0', '1'}
)

// Entry is one plaintext block in a pack. Hash is the SHA-256 of Data.
type Entry struct {
	Hash [sha256.Size]byte
	Data []byte
}

// Pack contains the immutable pack identifier and parsed metadata.
type Pack struct {
	ID         [sha256.Size]byte
	Size       int64
	EntryCount uint32
}

// NormalizeRelativePath returns a portable, non-rooted snapshot path.
func NormalizeRelativePath(value string) (string, error) {
	if value == "" || strings.Contains(value, `\`) {
		return "", errors.New("backup path must be a non-empty slash-separated relative path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || strings.HasPrefix(cleaned, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("backup path escapes the snapshot root")
	}
	return cleaned, nil
}

// SplitBlocks reads a file in fixed-size blocks without retaining the whole
// file. callback takes ownership of each block and must not retain it after it
// returns. It returns the SHA-256 of the complete file stream.
func SplitBlocks(ctx context.Context, source io.Reader, callback func(Entry) error) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if source == nil || callback == nil {
		return zero, errors.New("backup source and callback are required")
	}

	fileHash := sha256.New()
	buffer := make([]byte, BlockSize)
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		n, err := io.ReadFull(source, buffer)
		if n > 0 {
			block := append([]byte(nil), buffer[:n]...)
			_, _ = fileHash.Write(block)
			blockHash := sha256.Sum256(block)
			if err := callback(Entry{Hash: blockHash, Data: block}); err != nil {
				return zero, err
			}
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return zero, fmt.Errorf("read backup source: %w", err)
		}
	}
	return sha256Sum(fileHash), nil
}

// EncodePack writes a complete v1 pack and returns its full-stream SHA-256.
func EncodePack(destination io.Writer, entries []Entry) (Pack, error) {
	if destination == nil {
		return Pack{}, errors.New("pack destination is required")
	}

	if len(entries) > int(^uint32(0)) {
		return Pack{}, errors.New("too many pack entries")
	}
	var payloadSize int64
	for _, entry := range entries {
		if len(entry.Data) == 0 || len(entry.Data) > BlockSize {
			return Pack{}, fmt.Errorf("invalid block length: %d", len(entry.Data))
		}
		if sha256.Sum256(entry.Data) != entry.Hash {
			return Pack{}, errors.New("block hash does not match payload")
		}
		payloadSize += int64(sha256.Size + 4 + len(entry.Data))
	}
	if int64(packPrefixSize+packFooterSize)+payloadSize > MaxPackSize {
		return Pack{}, errors.New("encoded pack exceeds maximum size")
	}

	hasher := sha256.New()
	writer := io.MultiWriter(destination, hasher)
	if _, err := writer.Write(packMagic[:]); err != nil {
		return Pack{}, fmt.Errorf("write pack magic: %w", err)
	}
	var prefix [8]byte
	binary.BigEndian.PutUint16(prefix[:2], FormatVersion)
	// The reserved bytes and v1 header length intentionally remain zero.
	if _, err := writer.Write(prefix[:]); err != nil {
		return Pack{}, fmt.Errorf("write pack prefix: %w", err)
	}

	dataEnd := int64(packPrefixSize)
	for _, entry := range entries {
		if _, err := writer.Write(entry.Hash[:]); err != nil {
			return Pack{}, fmt.Errorf("write block hash: %w", err)
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(entry.Data)))
		if _, err := writer.Write(length[:]); err != nil {
			return Pack{}, fmt.Errorf("write block length: %w", err)
		}
		if _, err := writer.Write(entry.Data); err != nil {
			return Pack{}, fmt.Errorf("write block payload: %w", err)
		}
		dataEnd += int64(sha256.Size + 4 + len(entry.Data))
	}
	if _, err := writer.Write(footerMagic[:]); err != nil {
		return Pack{}, fmt.Errorf("write pack footer: %w", err)
	}
	var footer [12]byte
	binary.BigEndian.PutUint32(footer[:4], uint32(len(entries)))
	binary.BigEndian.PutUint64(footer[4:], uint64(dataEnd))
	if _, err := writer.Write(footer[:]); err != nil {
		return Pack{}, fmt.Errorf("write pack footer fields: %w", err)
	}

	return Pack{ID: sha256Sum(hasher), Size: dataEnd + packFooterSize, EntryCount: uint32(len(entries))}, nil
}

// ValidatePack parses and validates a v1 pack. It verifies framing, all block
// hashes, footer metadata, the expected full-stream hash, and trailing data.
// callback receives the payload offset and block entry after validation.
func ValidatePack(source io.Reader, expectedID [sha256.Size]byte, callback func(offset int64, entry Entry) error) (Pack, error) {
	if source == nil {
		return Pack{}, errors.New("pack source is required")
	}
	reader := bufio.NewReader(source)
	hasher := sha256.New()
	var offset int64
	read := func(data []byte) error {
		if _, err := io.ReadFull(reader, data); err != nil {
			return err
		}
		_, _ = hasher.Write(data)
		offset += int64(len(data))
		return nil
	}

	var magic [8]byte
	if err := read(magic[:]); err != nil || magic != packMagic {
		return Pack{}, errors.New("invalid pack magic")
	}
	var prefix [8]byte
	if err := read(prefix[:]); err != nil {
		return Pack{}, fmt.Errorf("read pack prefix: %w", err)
	}
	if binary.BigEndian.Uint16(prefix[:2]) != FormatVersion || prefix[2] != 0 || prefix[3] != 0 || binary.BigEndian.Uint32(prefix[4:]) != 0 {
		return Pack{}, errors.New("unsupported pack version, flags, or header")
	}

	var count uint32
	for {
		leading, err := reader.Peek(8)
		if err != nil {
			return Pack{}, fmt.Errorf("read pack entry or footer: %w", err)
		}
		if bytes.Equal(leading, footerMagic[:]) {
			break
		}
		if offset+int64(sha256.Size+4)+packFooterSize > MaxPackSize {
			return Pack{}, errors.New("pack exceeds maximum size")
		}
		var blockHash [sha256.Size]byte
		if err := read(blockHash[:]); err != nil {
			return Pack{}, fmt.Errorf("read block hash: %w", err)
		}
		var encodedLength [4]byte
		if err := read(encodedLength[:]); err != nil {
			return Pack{}, fmt.Errorf("read block length: %w", err)
		}
		length := binary.BigEndian.Uint32(encodedLength[:])
		if length == 0 || length > BlockSize || offset+int64(length)+packFooterSize > MaxPackSize {
			return Pack{}, errors.New("invalid block length")
		}
		payload := make([]byte, length)
		payloadOffset := offset
		if err := read(payload); err != nil {
			return Pack{}, fmt.Errorf("read block payload: %w", err)
		}
		if sha256.Sum256(payload) != blockHash {
			return Pack{}, errors.New("block payload hash mismatch")
		}
		if count == ^uint32(0) {
			return Pack{}, errors.New("too many pack entries")
		}
		count++
		if callback != nil {
			if err := callback(payloadOffset, Entry{Hash: blockHash, Data: payload}); err != nil {
				return Pack{}, err
			}
		}
	}

	dataEnd := offset
	var footer [20]byte
	if err := read(footer[:]); err != nil {
		return Pack{}, fmt.Errorf("read pack footer: %w", err)
	}
	if !bytes.Equal(footer[:8], footerMagic[:]) || binary.BigEndian.Uint32(footer[8:12]) != count || binary.BigEndian.Uint64(footer[12:]) != uint64(dataEnd) {
		return Pack{}, errors.New("pack footer does not match entries")
	}
	if offset > MaxPackSize {
		return Pack{}, errors.New("pack exceeds maximum size")
	}
	if _, err := reader.Peek(1); !errors.Is(err, io.EOF) {
		if err == nil {
			return Pack{}, errors.New("pack has trailing data")
		}
		return Pack{}, fmt.Errorf("check pack trailing data: %w", err)
	}
	actualID := sha256Sum(hasher)
	if actualID != expectedID {
		return Pack{}, errors.New("pack hash does not match catalogued pack ID")
	}
	return Pack{ID: actualID, Size: offset, EntryCount: count}, nil
}

// sha256Sum avoids retaining a second pack-sized byte slice.
func sha256Sum(hash hash.Hash) [sha256.Size]byte {
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum
}
