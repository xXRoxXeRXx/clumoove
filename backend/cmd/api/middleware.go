package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/observability"
	"github.com/redis/go-redis/v9"
)

// adminMiddleware authenticates the request before requiring the current user
// to hold the ADMIN role. Keeping both checks in one route middleware prevents
// new administrative handlers from accidentally being registered as JWT-only.
// Handlers may retain an equivalent check as defense in depth.
func adminMiddleware(authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
			if !ok || claims == nil || claims.Role != "ADMIN" {
				writeError(w, http.StatusForbidden, ErrAdminOnly)
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

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
		slog.ErrorContext(ctx, "rate_limiter_unavailable", slog.String("component", "rate_limiter"), slog.String("operation", scope), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
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
				// CORS controls response visibility, not request execution. Cookie-
				// authenticated endpoints must enforce their own CSRF/origin policy.
				next.ServeHTTP(w, r)
				return
			}
			// Credentialed requests are only allowed from the whitelisted origins.
			// Expose-Header is set only for trusted origins so rejected origins
			// receive no CORS metadata.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Cookie")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requireTrustedCookieOrigin protects endpoints authenticated by the refresh
// cookie. CORS does not prevent a browser from sending a cross-site request;
// reject an untrusted browser Origin before it can rotate or revoke a session.
func requireTrustedCookieOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && !allowedOrigins[origin] {
		writeError(w, http.StatusForbidden, ErrCorsOriginUntrusted)
		return false
	}
	return true
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	count, err := w.ResponseWriter.Write(data)
	w.bytes += int64(count)
	return count, err
}

func (w *loggingResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestLogMiddleware is outermost so CORS rejections and preflights are
// correlated too. Client supplied request IDs are intentionally ignored.
func (s *APIServer) requestLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := observability.NewRequestID()
		logger := slog.Default().With(slog.String("component", "http"), slog.String("request_id", requestID))
		r = r.WithContext(observability.WithLogger(r.Context(), logger))
		w.Header().Set("X-Request-ID", requestID)
		response := &loggingResponseWriter{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []slog.Attr{
			slog.String("operation", "http_request"),
			slog.String("method", r.Method),
			slog.String("route", routePattern(r)),
			slog.Int("http_status", status),
			slog.Int64("response_bytes", response.bytes),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			slog.String("client_ip_category", s.clientIPCategory(r)),
		}
		arguments := make([]any, len(attrs))
		for i, attr := range attrs {
			arguments[i] = attr
		}
		if status >= http.StatusInternalServerError {
			logger.ErrorContext(r.Context(), "http_request_completed", arguments...)
		} else {
			logger.InfoContext(r.Context(), "http_request_completed", arguments...)
		}
	})
}

func routePattern(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

func (s *APIServer) clientIPCategory(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// Reuse the canonical leftmost-IP extraction so proxy chains
	// ("client, proxy1, proxy2") are classified correctly rather than
	// collapsing to "unknown".
	if s.trustedProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			candidate := strings.TrimSpace(forwarded)
			if idx := strings.IndexByte(candidate, ','); idx >= 0 {
				candidate = strings.TrimSpace(candidate[:idx])
			}
			if candidate != "" {
				host = candidate
			}
		}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	if ip.IsLoopback() {
		return "loopback"
	}
	if ip.IsPrivate() {
		return "private"
	}
	return "public"
}

// securityHeadersMiddleware attaches defensive HTTP response headers to every
// response. The OAuth callback route serves HTML, so it sets its own CSP with a
// nonce via renderOAuthResultHTML; all other (JSON) responses get a strict
// default-src 'none' policy which is safe for non-document bodies.
func (s *APIServer) securityHeadersMiddleware(next http.Handler) http.Handler {
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
		// Trust X-Forwarded-Proto only when the server is explicitly configured
		// behind a proxy that strips client-supplied forwarding headers.
		if s.isSecure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}
