package storage

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRetryTransportRetries429WithSecondsRetryAfter(t *testing.T) {
	var attempts int
	var slept []time.Duration

	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			h := make(http.Header)
			h.Set("Retry-After", "2")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     h,
				Body:       io.NopCloser(strings.NewReader("rate limited")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}), retryOptions{
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.com/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(slept) != 1 || slept[0] != 2*time.Second {
		t.Fatalf("slept = %v, want [2s]", slept)
	}
}

func TestRetryTransport503BoundedExponentialBackoffWithJitterAndMaxAttempts(t *testing.T) {
	var attempts int
	var slept []time.Duration

	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("unavailable")),
		}, nil
	}), retryOptions{
		maxRetries:  3,
		baseBackoff: 100 * time.Millisecond,
		maxBackoff:  250 * time.Millisecond,
		jitter: func(d time.Duration) time.Duration {
			// Deterministic +10% jitter for testing
			return d / 10
		},
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})

	req, err := http.NewRequest(http.MethodGet, "https://example.com/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4 (1 initial + 3 retries)", attempts)
	}
	// Attempt 0 -> 100ms + 10ms = 110ms
	// Attempt 1 -> 200ms + 20ms = 220ms
	// Attempt 2 -> capped at 250ms + 25ms = 275ms (or capped at maxBackoff 250ms; let's cap base before jitter or total at maxBackoff: 250ms)
	want := []time.Duration{110 * time.Millisecond, 220 * time.Millisecond, 250 * time.Millisecond}
	if len(slept) != len(want) {
		t.Fatalf("slept = %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("slept[%d] = %v, want %v (all=%v)", i, slept[i], want[i], slept)
		}
	}
}

func TestRetryTransportRetries429WithHTTPDateRetryAfter(t *testing.T) {
	fixedNow := time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	var attempts int
	var slept []time.Duration

	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			h := make(http.Header)
			h.Set("Retry-After", fixedNow.Add(5*time.Second).Format(http.TimeFormat))
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     h,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}), retryOptions{
		now: func() time.Time { return fixedNow },
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	})

	req, _ := http.NewRequest(http.MethodGet, "https://example.com/date", nil)
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	resp.Body.Close()

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(slept) != 1 || slept[0] != 5*time.Second {
		t.Fatalf("slept = %v, want [5s]", slept)
	}
}

func TestRetryTransportCancelsDuringBackoffWait(t *testing.T) {
	var attempts int
	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		h := make(http.Header)
		h.Set("Retry-After", "5")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}), retryOptions{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/cancel", nil)
	_, err := (&http.Client{Transport: rt}).Do(req)
	if err == nil {
		t.Fatal("expected context deadline exceeded error, got nil")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (must not retry after context cancellation)", attempts)
	}
}

func TestRetryTransportDoesNotRetryNonReplayableBody(t *testing.T) {
	var attempts int
	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		_, _ = io.ReadAll(req.Body)
		h := make(http.Header)
		h.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	}), retryOptions{
		sleep: func(_ context.Context, _ time.Duration) error {
			t.Fatal("sleep must not be called for non-replayable body")
			return nil
		},
	})

	stream := io.NopCloser(strings.NewReader("streaming-upload-payload"))
	req, _ := http.NewRequest(http.MethodPut, "https://example.com/upload", stream)
	req.GetBody = nil

	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestRetryTransportReplaysReplayableBody(t *testing.T) {
	var bodies []string
	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("busy")),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
		}, nil
	}), retryOptions{
		sleep: func(_ context.Context, _ time.Duration) error { return nil },
	})

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/rpc", strings.NewReader(`{"hello":"world"}`))
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(bodies) != 2 || bodies[0] != `{"hello":"world"}` || bodies[1] != `{"hello":"world"}` {
		t.Fatalf("replayed bodies = %v, want two identical payloads", bodies)
	}
}

func TestRetryTransportReturnsLongRetryAfterImmediately(t *testing.T) {
	var attempts int
	rt := newRetryTransport(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		h := make(http.Header)
		h.Set("Retry-After", "60")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	}), retryOptions{
		maxInlineRetryAfter: 30 * time.Second,
		sleep: func(_ context.Context, d time.Duration) error {
			t.Fatalf("sleep(%v) must not be called when Retry-After exceeds maxInlineRetryAfter", d)
			return nil
		},
	})

	req, _ := http.NewRequest(http.MethodPost, "https://example.com/auth", strings.NewReader("a=b"))
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
