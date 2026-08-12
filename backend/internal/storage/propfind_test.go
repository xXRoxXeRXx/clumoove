package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDoPropfindRetriesWithFreshRequestBody(t *testing.T) {
	originalWait := propfindRetryWait
	propfindRetryWait = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { propfindRetryWait = originalWait })

	const body = "<propfind/>"
	attempts := 0
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		got, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if string(got) != body {
			t.Fatalf("attempt %d body = %q, want %q", attempts, got, body)
		}
		if attempts == 1 {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: http.StatusMultiStatus, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
	})}
	req, err := http.NewRequest(http.MethodGet, "https://dav.example.test/", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := doPropfind(context.Background(), client, req)
	if err != nil {
		t.Fatalf("doPropfind() error = %v", err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
