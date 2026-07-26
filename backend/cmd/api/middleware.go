package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type distributedRateLimiter struct {
	client *redis.Client
}

var rateLimitScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return count <= tonumber(ARGV[1])
`)

// Allow uses Redis so every API instance applies the same fixed-window limit.
// Scope is part of the key, keeping independent endpoint groups from consuming
// one another's request budget.
func (rl *distributedRateLimiter) Allow(ctx context.Context, scope, clientKey string, maxRequests int, window time.Duration) bool {
	result, err := rateLimitScript.Run(ctx, rl.client, []string{"rate-limit:" + scope + ":" + clientKey}, maxRequests, window.Milliseconds()).Bool()
	if err != nil {
		// Redis is required at startup; fail closed if it becomes unavailable so a
		// transient outage cannot silently disable abuse protection.
		log.Printf("rate limiter unavailable for scope %q: %v", scope, err)
		return false
	}
	return result
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
