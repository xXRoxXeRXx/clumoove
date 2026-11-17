package main

import (
	"net/http"
	"sync"
)

// Per-user SSE stream slot policy.
//
// Home decision: the per-user stream cap is cross-cutting HTTP policy. The
// migration, sync and backup stream endpoints all enforce the same limit, so a
// slot count held by any one feature module would let a user multiply the cap
// by opening streams on several features. The gateway therefore owns a single
// pool instance and injects it into each feature module as a streamSlotPool;
// this file defines the policy, and gateway.go only wires it.

// streamSlotPool is the seam feature modules depend on to cap concurrent
// streams per user.
type streamSlotPool interface {
	Acquire(userID string) bool
	Release(userID string)
}

// userStreamPool is the in-process streamSlotPool: at most limit concurrent
// streams per user ID.
type userStreamPool struct {
	mu     sync.Mutex
	active map[string]int
	limit  int
}

func newUserStreamPool(limit int) *userStreamPool {
	return &userStreamPool{
		active: make(map[string]int),
		limit:  limit,
	}
}

func (p *userStreamPool) Acquire(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = make(map[string]int)
	}
	if p.active[userID] >= p.limit {
		return false
	}
	p.active[userID]++
	return true
}

func (p *userStreamPool) Release(userID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active[userID]--
	if p.active[userID] <= 0 {
		delete(p.active, userID)
	}
}

// streamPool returns the server's shared pool, creating it on first use.
func (s *APIServer) streamPool() *userStreamPool {
	if s == nil {
		return nil
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streams == nil {
		s.streams = newUserStreamPool(maxStreamsPerUser)
	}
	return s.streams
}

// acquireStreamLease applies the stream rate limit and per-user slot cap shared
// by every stream endpoint. On refusal it writes the 429 response and returns
// false; on success the caller must Release the slot when the stream ends.
func acquireStreamLease(
	w http.ResponseWriter,
	r *http.Request,
	limiter rateLimiter,
	clientIP string,
	pool streamSlotPool,
	userID, rateLimitScope string,
) bool {
	if limiter == nil || !limiter.Allow(r.Context(), rateLimitScope, clientIP, streamRateLimit, streamRateWindow) {
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return false
	}
	if pool == nil || !pool.Acquire(userID) {
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return false
	}
	return true
}
