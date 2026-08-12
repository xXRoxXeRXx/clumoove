package throttle

import (
	"context"
	"io"
	"sync/atomic"

	"golang.org/x/time/rate"
)

// MigrationThrottler applies the configured bandwidth limit independently to
// download and upload streams for one migration or sync job.
type MigrationThrottler struct {
	downloadLimiter atomic.Pointer[rate.Limiter]
	uploadLimiter   atomic.Pointer[rate.Limiter]
}

// NewMigrationThrottler creates a throttler with the supplied per-direction
// limit in Mbps. A zero or negative limit permits unlimited transfer.
func NewMigrationThrottler(limitMbps int) *MigrationThrottler {
	mt := &MigrationThrottler{}
	mt.SetLimit(limitMbps)
	return mt
}

// SetLimit updates the shared configured limit for both directions. Each
// direction receives its own token bucket, so a limit applies in full to both
// download and upload traffic rather than to their combined throughput.
func (mt *MigrationThrottler) SetLimit(limitMbps int) {
	newLimiter := func() *rate.Limiter {
		if limitMbps <= 0 {
			return rate.NewLimiter(rate.Inf, 0)
		}

		bytesPerSec := int64(limitMbps) * 1024 * 1024 / 8
		return rate.NewLimiter(rate.Limit(bytesPerSec), int(bytesPerSec))
	}
	mt.downloadLimiter.Store(newLimiter())
	mt.uploadLimiter.Store(newLimiter())
}

// ThrottledReader limits reads from an underlying stream according to a
// MigrationThrottler. It waits for capacity before reading so cancellation
// never consumes source bytes that cannot be returned to the caller.
type ThrottledReader struct {
	r      io.Reader
	mt     *MigrationThrottler
	ctx    context.Context
	upload bool
}

// NewThrottledReader wraps r with the download limiter from mt.
func NewThrottledReader(ctx context.Context, r io.Reader, mt *MigrationThrottler) *ThrottledReader {
	return &ThrottledReader{r: r, mt: mt, ctx: ctx, upload: false}
}

// NewUploadThrottledReader wraps r with the upload limiter from mt.
func NewUploadThrottledReader(ctx context.Context, r io.Reader, mt *MigrationThrottler) *ThrottledReader {
	return &ThrottledReader{r: r, mt: mt, ctx: ctx, upload: true}
}

// Read waits for enough capacity to read a bounded portion of p, then reads
// that portion. Bounded reads keep every WaitN request at or below the
// limiter's burst size and preserve the io.Reader contract on cancellation.
func (tr *ThrottledReader) Read(p []byte) (int, error) {
	if err := tr.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return tr.r.Read(p)
	}

	limiter := tr.mt.downloadLimiter.Load()
	if tr.upload {
		limiter = tr.mt.uploadLimiter.Load()
	}
	if limiter == nil {
		// A zero-value throttler is intentionally safe to use without limiting.
		return tr.r.Read(p)
	}

	readSize := len(p)
	if burst := limiter.Burst(); burst > 0 && readSize > burst {
		readSize = burst
	}
	if err := limiter.WaitN(tr.ctx, readSize); err != nil {
		return 0, err
	}
	return tr.r.Read(p[:readSize])
}
