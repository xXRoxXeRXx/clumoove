package storage

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxRetries          = 3
	defaultRetryBaseBackoff    = 500 * time.Millisecond
	defaultRetryMaxBackoff     = 30 * time.Second
	defaultMaxInlineRetryAfter = 30 * time.Second
)

type retryOptions struct {
	maxRetries          int
	baseBackoff         time.Duration
	maxBackoff          time.Duration
	maxInlineRetryAfter time.Duration
	now                 func() time.Time
	jitter              func(time.Duration) time.Duration
	sleep               func(context.Context, time.Duration) error
}

type retryTransport struct {
	base http.RoundTripper
	opts retryOptions
}

func newRetryTransport(base http.RoundTripper, opts retryOptions) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if opts.maxRetries <= 0 {
		opts.maxRetries = defaultMaxRetries
	}
	if opts.baseBackoff <= 0 {
		opts.baseBackoff = defaultRetryBaseBackoff
	}
	if opts.maxBackoff <= 0 {
		opts.maxBackoff = defaultRetryMaxBackoff
	}
	if opts.maxInlineRetryAfter <= 0 {
		opts.maxInlineRetryAfter = defaultMaxInlineRetryAfter
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.jitter == nil {
		opts.jitter = defaultRetryJitter
	}
	if opts.sleep == nil {
		opts.sleep = sleepWithContext
	}
	return &retryTransport{
		base: base,
		opts: opts,
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultRetryJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	quarter := d / 4
	if quarter <= 0 {
		return 0
	}
	return rand.N(quarter)
}

func (t *retryTransport) backoffDelay(attempt int) time.Duration {
	delay := t.opts.baseBackoff
	for i := 0; i < attempt; i++ {
		if delay >= t.opts.maxBackoff/2 {
			delay = t.opts.maxBackoff
			break
		}
		delay *= 2
	}
	if delay > t.opts.maxBackoff {
		delay = t.opts.maxBackoff
	}
	delay += t.opts.jitter(delay)
	if delay > t.opts.maxBackoff {
		delay = t.opts.maxBackoff
	}
	return delay
}

func parseRetryAfterHeader(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		d := retryAt.Sub(now)
		if d < 0 {
			return 0, true
		}
		return d, true
	}
	return 0, false
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	replayable := req.Body == nil || req.GetBody != nil
	for attempt := 0; ; attempt++ {
		attemptReq := req
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = body
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}

		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) || !replayable || attempt >= t.opts.maxRetries {
			return resp, nil
		}

		delay, explicit := parseRetryAfterHeader(resp.Header.Get("Retry-After"), t.opts.now())
		if explicit && delay > t.opts.maxInlineRetryAfter {
			return resp, nil
		}
		if !explicit {
			delay = t.backoffDelay(attempt)
		}

		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if err := t.opts.sleep(req.Context(), delay); err != nil {
			return nil, err
		}
	}
}

var _ http.RoundTripper = (*retryTransport)(nil)
