package restore

import (
	"context"
	"errors"
)

// PackReaderLimiter is shared by restore and repository verification within a
// worker process. It bounds concurrent full-pack reads, each of which can use
// up to the v1 64 MiB pack ceiling.
type PackReaderLimiter struct {
	slots chan struct{}
}

func NewPackReaderLimiter(limit int) (*PackReaderLimiter, error) {
	if limit < 1 || limit > 4 {
		return nil, errors.New("MAX_RESTORE_PACK_READERS must be an integer between 1 and 4")
	}
	return &PackReaderLimiter{slots: make(chan struct{}, limit)}, nil
}

func (l *PackReaderLimiter) Acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *PackReaderLimiter) Release() {
	if l != nil {
		<-l.slots
	}
}

func (l *PackReaderLimiter) Capacity() int {
	if l == nil {
		return 0
	}
	return cap(l.slots)
}
