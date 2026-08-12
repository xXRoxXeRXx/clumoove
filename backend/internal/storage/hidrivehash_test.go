package storage

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"
)

func TestHiDriveHasherDocumentationVector(t *testing.T) {
	// HiDrive Synchronization v3.1, section 5: 64 repetitions form one L0 block.
	// https://developer.hidrive.com/doc/HiDrive_Sync.pdf
	data := []byte("#ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstuvwxyz\n")
	h := NewHiDriveHasher()
	for i := 0; i < 64; i++ {
		_, _ = h.Write(data)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != "09f077820a8a41f34a639f2172f1133b1eafe4e6" {
		t.Fatalf("chash = %s", got)
	}
}

func TestHiDriveHasherEmptyAndZeroBlock(t *testing.T) {
	for _, data := range [][]byte{nil, make([]byte, hiDriveHashBlockSize)} {
		h := NewHiDriveHasher()
		_, _ = h.Write(data)
		if got, want := hex.EncodeToString(h.Sum(nil)), strings.Repeat("00", sha1.Size); got != want {
			t.Fatalf("HiDrive hash = %q, want %q", got, want)
		}
	}
}

func TestHiDriveHasherFinalizesCarryLevelsAddedDuringSum(t *testing.T) {
	// 255 full L1 slots plus one incomplete L0 slot. Finalising that slot
	// fills L1 and creates L2, which Sum must also visit.
	h := &HiDriveHasher{}
	node := sha1.Sum([]byte("block"))
	for i := 0; i < 255*256+1; i++ {
		h.add(0, node)
	}
	if got := h.Sum(nil); string(got) == string(make([]byte, sha1.Size)) {
		t.Fatal("chash is zero after final carry propagation")
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
