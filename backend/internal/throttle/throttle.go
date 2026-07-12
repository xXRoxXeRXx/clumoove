package throttle

import (
	"context"
	"io"
	"sync/atomic"

	"golang.org/x/time/rate"
)

type MigrationThrottler struct {
	downloadLimiter atomic.Pointer[rate.Limiter]
	uploadLimiter   atomic.Pointer[rate.Limiter]
}

func NewMigrationThrottler(limitMbps int) *MigrationThrottler {
	mt := &MigrationThrottler{}
	mt.SetLimit(limitMbps)
	return mt
}

func (mt *MigrationThrottler) SetLimit(limitMbps int) {
	var limiter *rate.Limiter
	if limitMbps <= 0 {
		limiter = rate.NewLimiter(rate.Inf, 0)
	} else {
		bytesPerSec := int64(limitMbps) * 1024 * 1024 / 8
		r := rate.Limit(bytesPerSec)
		burst := int(bytesPerSec)
		limiter = rate.NewLimiter(r, burst)
	}
	mt.downloadLimiter.Store(limiter)
	mt.uploadLimiter.Store(limiter)
}

type ThrottledReader struct {
	r      io.Reader
	mt     *MigrationThrottler
	ctx    context.Context
	upload bool
}

func NewThrottledReader(r io.Reader, mt *MigrationThrottler, ctx context.Context) *ThrottledReader {
	return &ThrottledReader{r: r, mt: mt, ctx: ctx, upload: false}
}

func NewUploadThrottledReader(r io.Reader, mt *MigrationThrottler, ctx context.Context) *ThrottledReader {
	return &ThrottledReader{r: r, mt: mt, ctx: ctx, upload: true}
}

func (tr *ThrottledReader) Read(p []byte) (int, error) {
	n, err := tr.r.Read(p)
	if n > 0 {
		if tr.upload {
			if limiter := tr.mt.uploadLimiter.Load(); limiter != nil {
				limiter.WaitN(tr.ctx, n)
			}
		} else {
			if limiter := tr.mt.downloadLimiter.Load(); limiter != nil {
				limiter.WaitN(tr.ctx, n)
			}
		}
	}
	return n, err
}
