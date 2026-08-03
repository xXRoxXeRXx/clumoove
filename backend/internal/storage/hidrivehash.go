package storage

import (
	"crypto/sha1"
	"hash"
)

// HiDriveHasher implements HiDrive's hierarchical chash algorithm. It keeps
// only the current 4 KiB block and one accumulator per hierarchy level.
const hiDriveHashBlockSize = 4096

type hiDriveHashLevel struct {
	sum   [sha1.Size]byte
	count int
}

type HiDriveHasher struct {
	block  []byte
	levels []hiDriveHashLevel
	last   [sha1.Size]byte
}

func NewHiDriveHasher() hash.Hash { return &HiDriveHasher{} }

func (h *HiDriveHasher) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		need := hiDriveHashBlockSize - len(h.block)
		if need > len(p) {
			need = len(p)
		}
		h.block = append(h.block, p[:need]...)
		p = p[need:]
		if len(h.block) == hiDriveHashBlockSize {
			h.commitBlock()
		}
	}
	return n, nil
}

func (h *HiDriveHasher) commitBlock() {
	var node [sha1.Size]byte
	nonZero := false
	for _, b := range h.block {
		if b != 0 {
			nonZero = true
			break
		}
	}
	if nonZero {
		node = sha1.Sum(h.block)
	}
	h.block = h.block[:0]
	h.add(0, node)
}

func addHiDriveModulo(dst *[sha1.Size]byte, src [sha1.Size]byte) {
	carry := 0
	for i := sha1.Size - 1; i >= 0; i-- {
		v := int(dst[i]) + int(src[i]) + carry
		dst[i] = byte(v)
		carry = v >> 8
	}
}

func (h *HiDriveHasher) add(level int, node [sha1.Size]byte) {
	for {
		if level == len(h.levels) {
			h.levels = append(h.levels, hiDriveHashLevel{})
		}
		l := &h.levels[level]
		if node != ([sha1.Size]byte{}) {
			input := make([]byte, sha1.Size+1)
			copy(input, node[:])
			input[sha1.Size] = byte(l.count)
			addHiDriveModulo(&l.sum, sha1.Sum(input))
		}
		l.count++
		h.last = node
		if l.count < 256 {
			return
		}
		node = l.sum
		*l = hiDriveHashLevel{}
		level++
	}
}

func (h *HiDriveHasher) Sum(b []byte) []byte {
	clone := &HiDriveHasher{block: append([]byte(nil), h.block...), levels: append([]hiDriveHashLevel(nil), h.levels...), last: h.last}
	if len(clone.block) > 0 {
		clone.block = append(clone.block, make([]byte, hiDriveHashBlockSize-len(clone.block))...)
		clone.commitBlock()
	}
	checksum := [sha1.Size]byte{}
	for i := range clone.levels {
		l := &clone.levels[i]
		if i < len(clone.levels)-1 {
			if l.count > 0 {
				node := l.sum
				clone.add(i+1, node)
				*l = hiDriveHashLevel{}
			}
		} else if l.count > 1 {
			checksum = l.sum
		} else {
			checksum = clone.last
		}
	}
	return append(b, checksum[:]...)
}
func (h *HiDriveHasher) Reset()       { h.block = h.block[:0]; h.levels = nil; h.last = [sha1.Size]byte{} }
func (*HiDriveHasher) Size() int      { return sha1.Size }
func (*HiDriveHasher) BlockSize() int { return hiDriveHashBlockSize }
