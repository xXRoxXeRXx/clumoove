package storage

import "hash"

// QuickXorHasher implements Microsoft's 160-bit QuickXorHash used by OneDrive.
// It is not cryptographic, but lets us compare the hash Graph exposes with the
// same value calculated while a file is streamed.
type QuickXorHasher struct {
	data   [20]byte
	length int64
	shift  int
}

var _ hash.Hash = (*QuickXorHasher)(nil)

func NewQuickXorHasher() *QuickXorHasher { return &QuickXorHasher{} }

func (h *QuickXorHasher) Write(p []byte) (int, error) {
	for _, b := range p {
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) == 0 {
				continue
			}
			pos := (h.shift + bit) % 160
			h.data[pos/8] ^= 1 << (pos % 8)
		}
		h.shift = (h.shift + 11) % 160
	}
	h.length += int64(len(p))
	return len(p), nil
}

func (h *QuickXorHasher) Sum(b []byte) []byte {
	out := h.data
	for i := 0; i < 8; i++ {
		out[12+i] ^= byte(uint64(h.length) >> (8 * i))
	}
	return append(b, out[:]...)
}

func (h *QuickXorHasher) Reset()         { *h = QuickXorHasher{} }
func (h *QuickXorHasher) Size() int      { return 20 }
func (h *QuickXorHasher) BlockSize() int { return 1 }
