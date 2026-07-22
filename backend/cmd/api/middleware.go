package main

import (
	"context"
	"net/http"
	"os"
	"sync"
	"time"
)

type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*rateVisitor
}

type rateVisitor struct {
	count   int
	resetAt time.Time
}

// Allow reports whether a request from key is permitted given a max request
// count per window. Counters reset when the window expires. The limiter is a
// simple fixed-window limiter; callers should use a stable per-client key.
func (rl *ipRateLimiter) Allow(key string, maxRequests int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	v, ok := rl.visitors[key]
	if !ok || now.After(v.resetAt) {
		rl.visitors[key] = &rateVisitor{count: 1, resetAt: now.Add(window)}
		return true
	}
	if v.count >= maxRequests {
		return false
	}
	v.count++
	return true
}

// evictExpired periodically drops rate-limit visitors whose window has passed so
// the in-memory map cannot grow without bound.
func (rl *ipRateLimiter) evictExpired(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for k, v := range rl.visitors {
				if now.After(v.resetAt) {
					delete(rl.visitors, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}

// allowedOrigins defines the exact origins that may send credentialed cross-site requests.
// Credentials (cookies) are only reflected for these origins; all others receive no Allow-Credentials header.
var allowedOrigins = func() map[string]bool {
	allowed := map[string]bool{
		"http://localhost:5173": true, // Vite dev server
		"http://localhost:3000": true, // alternative dev port
		"http://localhost:3001": true, // docker compose port
	}
	// Allow the production domain if set via environment variable
	if prod := os.Getenv("CORS_ALLOWED_ORIGIN"); prod != "" {
		allowed[prod] = true
	}
	return allowed
}()

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !allowedOrigins[origin] {
				writeError(w, http.StatusForbidden, ErrCorsOriginUntrusted)
				return
			}
			// Credentialed requests are only allowed from the whitelisted origins
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		// Requests from unknown or empty origins receive no Allow-Origin header (blocked by browser if necessary)
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Cookie")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware attaches defensive HTTP response headers to every
// response. The OAuth callback route serves HTML, so it sets its own CSP with a
// nonce via renderOAuthResultHTML; all other (JSON) responses get a strict
// default-src 'none' policy which is safe for non-document bodies.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// Only the OAuth HTML callback needs script execution; everything else
		// is JSON and benefits from a locked-down policy.
		if r.URL.Path != "/api/oauth/callback" {
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		}
		// HSTS only makes sense over a real TLS connection.
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}
