package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type allowAllLimiter struct{}

func (allowAllLimiter) Allow(context.Context, string, string, int, time.Duration) bool { return true }

func TestUserStreamPoolCapsConcurrentStreamsPerUser(t *testing.T) {
	pool := newUserStreamPool(2)

	if !pool.Acquire("alice") || !pool.Acquire("alice") {
		t.Fatal("expected first two slots for alice to be granted")
	}
	if pool.Acquire("alice") {
		t.Fatal("expected third slot for alice to be refused")
	}
	if !pool.Acquire("bob") {
		t.Fatal("expected bob to be unaffected by alice's streams")
	}

	pool.Release("alice")
	if !pool.Acquire("alice") {
		t.Fatal("expected a released slot to be reusable")
	}
}

func TestUserStreamPoolReleaseWithoutAcquireDoesNotGrantExtraSlots(t *testing.T) {
	pool := newUserStreamPool(1)
	pool.Release("alice")
	pool.Release("alice")

	if !pool.Acquire("alice") {
		t.Fatal("expected first slot to be granted")
	}
	if pool.Acquire("alice") {
		t.Fatal("stray releases must not raise the limit")
	}
}

func TestAPIServerStreamPoolIsLazyAndShared(t *testing.T) {
	server := &APIServer{}
	first := server.streamPool()
	if first == nil || first != server.streamPool() {
		t.Fatal("expected one lazily created pool per server")
	}
	var nilServer *APIServer
	if nilServer.streamPool() != nil {
		t.Fatal("nil server must have no pool")
	}
}

func TestAcquireStreamLeaseRefusesWhenPoolIsFull(t *testing.T) {
	pool := newUserStreamPool(1)
	acquire := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/stream", nil)
		if acquireStreamLease(rec, req, allowAllLimiter{}, "1.2.3.4", pool, "alice", "test-stream") {
			return nil
		}
		return rec
	}

	if acquire() != nil {
		t.Fatal("expected first lease to be granted")
	}
	rec := acquire()
	if rec == nil {
		t.Fatal("expected second lease to be refused")
	}
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "15" {
		t.Fatalf("unexpected refusal: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
