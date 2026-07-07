package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDropboxHasherEmpty(t *testing.T) {
	hasher := NewDropboxHasher()
	// Test empty file
	hasher.Write([]byte{})
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Dropbox content_hash of empty file is:
	// sha256(empty_bytes)
	expectedHashBytes := sha256.Sum256([]byte{})
	expectedHash := hex.EncodeToString(expectedHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected empty file hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherLessThan4MB(t *testing.T) {
	hasher := NewDropboxHasher()
	data := []byte("Hello, this is a test string to verify the Dropbox hasher implementation.")
	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Since size is less than 4MB, it's 1 block
	blockHash := sha256.Sum256(data)
	finalHashBytes := sha256.Sum256(blockHash[:])
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherExact4MB(t *testing.T) {
	hasher := NewDropboxHasher()
	blockSize := 4 * 1024 * 1024
	data := make([]byte, blockSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Exactly 1 block
	blockHash := sha256.Sum256(data)
	finalHashBytes := sha256.Sum256(blockHash[:])
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherMultiBlock(t *testing.T) {
	hasher := NewDropboxHasher()
	blockSize := 4 * 1024 * 1024
	// 10MB data (2 full 4MB blocks, 1 partial 2MB block)
	data := make([]byte, 10*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Manual block hashing
	block1 := data[:blockSize]
	block2 := data[blockSize : 2*blockSize]
	block3 := data[2*blockSize:]

	bh1 := sha256.Sum256(block1)
	bh2 := sha256.Sum256(block2)
	bh3 := sha256.Sum256(block3)

	var concat []byte
	concat = append(concat, bh1[:]...)
	concat = append(concat, bh2[:]...)
	concat = append(concat, bh3[:]...)

	finalHashBytes := sha256.Sum256(concat)
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}
