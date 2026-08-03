package storage

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestHiDriveHasherDocumentationVector(t *testing.T) {
	// HiDrive Synchronization v3.1, section 5: 64 repetitions form one L0 block.
	data := []byte("#ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstuvwxyz\n")
	h := NewHiDriveHasher()
	for i := 0; i < 64; i++ {
		_, _ = h.Write(data)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != "09f077820a8a41f34a639f2172f1133b1eafe4e6" {
		t.Fatalf("chash = %s", got)
	}
}

func TestHiDriveHasherIsIndependentOfWriteBoundaries(t *testing.T) {
	data := []byte(strings.Repeat("hello rclone\n", 316))
	a, b := NewHiDriveHasher(), NewHiDriveHasher()
	_, _ = a.Write(data)
	for len(data) > 0 {
		n := 397
		if n > len(data) {
			n = len(data)
		}
		_, _ = b.Write(data[:n])
		data = data[n:]
	}
	if string(a.Sum(nil)) != string(b.Sum(nil)) {
		t.Fatal("chash depends on write boundaries")
	}
}
