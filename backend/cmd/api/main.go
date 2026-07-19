package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
	"backend/internal/indexer"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/scheduler"
	"backend/internal/storage"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header (non-browser clients). The WS is authenticated via
			// the token in Sec-WebSocket-Protocol / ?token, but we still require a
			// browser-style origin from the whitelist to avoid open relay usage.
			return false
		}
		return allowedOrigins[origin]
	},
}

type APIServer struct {
	db            *sql.DB
	queue         *queue.Queue
	indexer       *indexer.Indexer
	encryptionKey string // AES key for credential encryption
	jwtSecret     string // HMAC key for JWT signing (separate from encryptionKey)
	ctx           context.Context
	rateLimiter   ipRateLimiter
	// activeStreams tracks the number of open SSE migration-stream connections
	// per user so we can cap concurrent streams (each polls the DB on an
	// interval) and prevent resource exhaustion via connection flooding.
	streamMu      sync.Mutex
	activeStreams map[string]int
	// trustedProxy, when true, lets the server derive the real client IP and
	// HTTPS state from X-Forwarded-For / X-Forwarded-Proto. Only enable this
	// when a trusted reverse proxy (that strips client-supplied copies of these
	// headers) sits in front of the API — otherwise clients can spoof them.
	trustedProxy bool
}

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

// Rate-limit and quota configuration for the public / sensitive endpoints.
const (
	loginRateLimit     = 10
	loginRateWindow    = 1 * time.Minute
	registerRateLimit  = 5
	registerRateWindow = 5 * time.Minute
	connectRateLimit   = 30
	connectRateWindow  = 1 * time.Minute
	totpRateLimit      = 10
	totpRateWindow     = 1 * time.Minute
	streamRateLimit    = 10
	streamRateWindow   = 1 * time.Minute
	// maxStreamsPerUser caps concurrent SSE migration-stream connections per
	// user. Each stream polls the DB on an interval, so this bounds the
	// per-user goroutine / query footprint against connection flooding.
	maxStreamsPerUser = 5

	// Account lockout after repeated failed logins (mirrors the TOTP lockout).
	loginMaxAttempts  = 5
	loginLockDuration = 15 * time.Minute

	// Per-user cap on simultaneously active (non-terminal) migrations.
	maxActiveMigrations = 10

	// minPasswordLength enforces ASVS V2.1.1 (≥ 12 characters). Every password
	// entry point (register, admin create, change, reset) must use this so the
	// policy stays consistent in one place.
	minPasswordLength = 12
)

// clientIP returns a stable per-client key for rate limiting. When a trusted
// reverse proxy is configured, the leftmost X-Forwarded-For address is used;
// otherwise the connection's remote address (port stripped) is used. Trusting
// X-Forwarded-For from an untrusted client would let the client spoof their key
// and bypass the limiter, so it is only honoured behind a trusted proxy.
//
// The returned value is also written verbatim into audit_log.ip, so it is
// stripped of CR/LF and other control characters to prevent log injection
// (CWE-117 / ASVS A09). The leftmost XFF hop is attacker-influenced even behind
// a trusted proxy, so the sanitizer additionally bounds the length and rejects
// anything that cannot be represented as a sane token.
func (s *APIServer) clientIP(r *http.Request) string {
	var raw string
	if s.trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.IndexByte(xff, ','); idx >= 0 {
				raw = strings.TrimSpace(xff[:idx])
			} else {
				raw = strings.TrimSpace(xff)
			}
		}
	}
	if raw == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			raw = r.RemoteAddr
		} else {
			raw = host
		}
	}
	return sanitizeAuditToken(raw)
}

// sanitizeAuditToken removes CR/LF and all control characters (C0 + DEL) from a
// value that will be persisted into structured/audit logs or used as a rate
// limiting key. It bounds the length to avoid oversized keys and defends
// against log forging (newline injection) from attacker-controlled headers such
// as X-Forwarded-For. Printable text and real IPs pass through unchanged.
func sanitizeAuditToken(s string) string {
	const maxTokenLen = 254
	if len(s) > maxTokenLen {
		s = s[:maxTokenLen]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Allow everything except control characters (U+0000-U+001F, U+007F).
		if r <= 0x1f || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isSecure reports whether the request arrived over HTTPS. Behind a trusted
// proxy this honours X-Forwarded-Proto; otherwise only a real TLS connection
// counts. Client-supplied X-Forwarded-Proto is ignored unless the proxy is
// trusted, preventing attackers from spoofing Secure-cookie / token behaviour.
func (s *APIServer) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.trustedProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

func main() {
	log.Println("Starting Migration API Gateway...")
	oauth.InitConfigs()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// No explicit DATABASE_URL: refuse to silently fall back to an
		// unencrypted connection. Default to TLS-required; operators must set
		// DATABASE_URL explicitly (e.g. with sslmode=disable) for trusted
		// local/dev setups that lack a TLS-capable Postgres.
		log.Println("WARNING: DATABASE_URL not set — defaulting to sslmode=require. Set DATABASE_URL explicitly to override (e.g. for a local dev database).")
		dbURL = "postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=require"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	encryptionKey := os.Getenv("ENCRYPTION_SECRET_KEY")
	if encryptionKey == "" {
		log.Fatal("ENCRYPTION_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
	}

	// Separate secret for JWT signing — must not share AES encryption key
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
	}

	if subtle.ConstantTimeCompare([]byte(encryptionKey), []byte(jwtSecret)) == 1 {
		log.Fatal("ENCRYPTION_SECRET_KEY and JWT_SECRET_KEY must be different to maintain cryptographic key segregation.")
	}

	// Enforce a minimum entropy/length for the HMAC key. A short shared
	// secret weakens HS256 and is brute-forceable offline from a leaked token.
	if len(jwtSecret) < 32 {
		log.Fatalf("JWT_SECRET_KEY must be at least 32 bytes long (got %d). Refusing to start with an insecure signing key.", len(jwtSecret))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// 1. Initialize PostgreSQL
	database, err := db.InitDB(dbURL)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()
	log.Println("Connected to PostgreSQL database.")

	// Bootstrap the initial admin account from environment identity. A random,
	// strong password is generated and printed exactly once to stdout; the admin
	// must rotate it on first login (must_change_password = TRUE). If ADMIN_EMAIL
	// is unset, no bootstrap account is created. Idempotent across restarts and
	// instances for an account that is already an ADMIN (an existing non-admin
	// account is never auto-promoted).
	if adminEmail := os.Getenv("ADMIN_EMAIL"); adminEmail != "" {
		adminName := os.Getenv("ADMIN_DISPLAY_NAME")
		created, pw, err := db.EnsureAdminUser(database, adminEmail, adminName)
		if err != nil {
			log.Printf("WARNING: failed to bootstrap admin user %q: %v", adminEmail, err)
		} else if created {
			log.Printf("BOOTSTRAP ADMIN created — email=%s password=%s (rotate on first login)", adminEmail, pw)
		}
	}

	// 2. Initialize Redis Queue
	q, err := queue.NewQueue(redisURL)
	if err != nil {
		log.Fatalf("Failed to initialize Redis queue: %v", err)
	}
	log.Println("Connected to Redis.")

	// Context for background processes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The JWT access token is signed with jwtSecret; the AES key is used only
	// for credential encryption. They must differ (enforced above).
	trustedProxy := os.Getenv("TRUSTED_PROXY") == "1" ||
		strings.EqualFold(os.Getenv("TRUSTED_PROXY"), "true")

	if !trustedProxy {
		// When the API is fronted by a reverse proxy (the documented
		// deployment: app → api), every request's RemoteAddr is the proxy,
		// so per-IP rate limiting collapses onto a single shared bucket and
		// is effectively disabled. Operators MUST set TRUSTED_PROXY=1 (and
		// ensure the proxy strips client-supplied X-Forwarded-For) so the
		// real client IP is used. Fail loudly here rather than silently.
		log.Println("WARNING: TRUSTED_PROXY is not set. If the API runs behind a reverse proxy, per-IP rate limiting and lockout accounting will be ineffective (all clients share the proxy's address). Set TRUSTED_PROXY=1 if a trusted proxy sits in front of the API.")
	}

	server := &APIServer{
		db:            database,
		queue:         q,
		indexer:       indexer.NewIndexer(database, encryptionKey),
		encryptionKey: encryptionKey,
		jwtSecret:     jwtSecret,
		ctx:           ctx,
		rateLimiter:   ipRateLimiter{visitors: make(map[string]*rateVisitor)},
		activeStreams: make(map[string]int),
		trustedProxy:  trustedProxy,
	}
	// Start Garbage Collector (GC) is removed as per requirements (permanent history until manual deletion)
	// go server.runGarbageCollector(ctx)

	// Go 1.22 Router
	mux := http.NewServeMux()

	// Auth Routes (Public)
	mux.HandleFunc("POST /api/auth/register", server.handleRegister)
	mux.HandleFunc("POST /api/auth/login", server.handleLogin)
	mux.HandleFunc("POST /api/auth/totp", server.handleTOTP)
	mux.HandleFunc("POST /api/auth/refresh", server.handleRefresh)
	mux.HandleFunc("POST /api/auth/logout", server.handleLogout)
	mux.HandleFunc("GET /api/settings", server.handleGetSettings)

	// Protected Auth Routes
	jwtMiddleware := auth.AuthMiddleware(server.jwtSecret)
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleMe)))
	mux.Handle("PUT /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleUpdateProfile)))
	mux.Handle("POST /api/auth/change-password", auth.AuthMiddlewareAllowMustChange(server.jwtSecret)(http.HandlerFunc(server.handleChangePassword)))
	mux.Handle("GET /api/auth/2fa/setup", jwtMiddleware(http.HandlerFunc(server.handle2FASetup)))
	mux.Handle("POST /api/auth/2fa/enable", jwtMiddleware(http.HandlerFunc(server.handle2FAEnable)))
	mux.Handle("POST /api/auth/2fa/disable", jwtMiddleware(http.HandlerFunc(server.handle2FADisable)))
	mux.Handle("GET /api/auth/2fa/status", jwtMiddleware(http.HandlerFunc(server.handle2FAStatus)))
	mux.Handle("POST /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleSetAvatar)))
	mux.Handle("DELETE /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleDeleteAvatar)))
	mux.Handle("PUT /api/settings", jwtMiddleware(http.HandlerFunc(server.handleUpdateSetting)))
	mux.Handle("GET /api/settings/smtp", jwtMiddleware(http.HandlerFunc(server.handleGetSMTPSettings)))
	mux.Handle("PUT /api/settings/smtp", jwtMiddleware(http.HandlerFunc(server.handleUpdateSMTPSettings)))
	mux.Handle("POST /api/settings/smtp/test", jwtMiddleware(http.HandlerFunc(server.handleTestSMTP)))

	mux.HandleFunc("GET /api/auth/password-reset-available", server.handlePasswordResetAvailable)
	mux.HandleFunc("POST /api/auth/forgot-password", server.handleForgotPassword)
	mux.HandleFunc("POST /api/auth/reset-password", server.handleResetPassword)

	mux.HandleFunc("GET /api/auth/email-change-available", server.handleEmailChangeAvailable)
	mux.Handle("POST /api/auth/change-email", jwtMiddleware(http.HandlerFunc(server.handleChangeEmail)))
	mux.HandleFunc("POST /api/auth/confirm-email-change", server.handleConfirmEmailChange)

	mux.Handle("GET /api/migration", jwtMiddleware(http.HandlerFunc(server.handleListMigrations)))
	mux.Handle("GET /api/migration/stream", jwtMiddleware(http.HandlerFunc(server.handleMigrationStream)))
	mux.Handle("POST /api/migration/connect", jwtMiddleware(http.HandlerFunc(server.handleConnect)))
	mux.Handle("POST /api/migration/browse", jwtMiddleware(http.HandlerFunc(server.handleBrowse)))
	mux.Handle("POST /api/migration/target/browse", jwtMiddleware(http.HandlerFunc(server.handleTargetBrowse)))
	mux.Handle("POST /api/migration/target/mkdir", jwtMiddleware(http.HandlerFunc(server.handleTargetMkdir)))
	mux.Handle("POST /api/migration/start", jwtMiddleware(http.HandlerFunc(server.handleStart)))
	mux.Handle("POST /api/googlephotos/picker/session", jwtMiddleware(http.HandlerFunc(server.handleGooglePhotosPickerSession)))
	mux.Handle("POST /api/googlephotos/picker/poll", jwtMiddleware(http.HandlerFunc(server.handleGooglePhotosPickerPoll)))
	mux.Handle("POST /api/googlephotos/picker/media", jwtMiddleware(http.HandlerFunc(server.handleGooglePhotosPickerMedia)))
	mux.Handle("GET /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetStatus)))
	mux.Handle("POST /api/migration/{id}/pause", jwtMiddleware(http.HandlerFunc(server.handlePause)))
	mux.Handle("POST /api/migration/{id}/resume", jwtMiddleware(http.HandlerFunc(server.handleResume)))
	mux.Handle("POST /api/migration/{id}/cancel", jwtMiddleware(http.HandlerFunc(server.handleCancel)))
	mux.Handle("DELETE /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteMigration)))
	mux.Handle("GET /api/migration/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadReport)))
	mux.Handle("POST /api/migration/{id}/retry-failed", jwtMiddleware(http.HandlerFunc(server.handleRetryFailed)))
	mux.Handle("POST /api/migration/{id}/reindex", jwtMiddleware(http.HandlerFunc(server.handleReindex)))
	mux.Handle("PUT /api/migration/{id}/bandwidth", jwtMiddleware(http.HandlerFunc(server.handleSetBandwidth)))
	mux.Handle("PUT /api/migration/{id}/threads", jwtMiddleware(http.HandlerFunc(server.handleSetThreads)))

	// Schedule Management Routes (Protected)
	mux.Handle("GET /api/schedule", jwtMiddleware(http.HandlerFunc(server.handleListSchedules)))
	mux.Handle("GET /api/schedule/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetSchedule)))
	mux.Handle("DELETE /api/schedule/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteSchedule)))

	// Connection Profiles (Protected)
	mux.Handle("GET /api/profiles", jwtMiddleware(http.HandlerFunc(server.handleListProfiles)))
	mux.Handle("POST /api/profiles", jwtMiddleware(http.HandlerFunc(server.handleCreateProfile)))
	mux.Handle("GET /api/profiles/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetProfile)))
	mux.Handle("PUT /api/profiles/{id}", jwtMiddleware(http.HandlerFunc(server.handleUpdateConnectionProfile)))
	mux.Handle("DELETE /api/profiles/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteProfile)))
	mux.Handle("POST /api/profiles/{id}/test", jwtMiddleware(http.HandlerFunc(server.handleTestProfile)))

	// Admin User-Management & Oversight (ADMIN-only, gated inside each handler)
	mux.Handle("POST /api/admin/users", jwtMiddleware(http.HandlerFunc(server.handleAdminCreateUser)))
	mux.Handle("POST /api/admin/users/{id}/suspend", jwtMiddleware(http.HandlerFunc(server.handleAdminSuspendUser)))
	mux.Handle("POST /api/admin/users/{id}/reactivate", jwtMiddleware(http.HandlerFunc(server.handleAdminReactivateUser)))
	mux.Handle("DELETE /api/admin/users/{id}", jwtMiddleware(http.HandlerFunc(server.handleAdminDeleteUser)))
	mux.Handle("PUT /api/admin/users/{id}/role", jwtMiddleware(http.HandlerFunc(server.handleAdminUpdateRole)))
	mux.Handle("GET /api/admin/users", jwtMiddleware(http.HandlerFunc(server.handleAdminListUsers)))
	mux.Handle("GET /api/admin/stats", jwtMiddleware(http.HandlerFunc(server.handleAdminStats)))
	mux.Handle("GET /api/admin/migrations", jwtMiddleware(http.HandlerFunc(server.handleAdminListMigrations)))
	mux.Handle("GET /api/audit/log", jwtMiddleware(http.HandlerFunc(server.handleAdminAuditLog)))

	// WebSockets & OAuth Callbacks (Require custom/token-based verification inside handler)
	mux.HandleFunc("GET /api/migration/{id}/ws", server.handleWebSocket)
	mux.HandleFunc("GET /api/oauth/auth", server.handleOAuthAuth)
	mux.HandleFunc("GET /api/oauth/callback", server.handleOAuthCallback)

	// Middleware (CORS + Security Headers)
	handler := securityHeadersMiddleware(corsMiddleware(mux))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // must cover the longest legitimate request (30s listing)
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the garbage collector style rate-limiter eviction loop
	go server.rateLimiter.evictExpired(ctx, 1*time.Minute)

	// Start the OAuth Token Rotation Daemon (PRD-12)
	go server.RunOAuthRotationDaemon(ctx)

	// Start the Core Scheduler Engine Daemon
	sched := scheduler.NewScheduler(database, q, server.indexer)
	go sched.Run(ctx)

	go func() {
		log.Printf("API Server listening on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	sig := <-sigChan
	log.Printf("Received signal %v. Shutting down API server...\n", sig)

	// Cancel context to stop GC
	cancel()

	// Shut down server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("API Server Shutdown Failed:%+v", err)
	}
	log.Println("API Server exited gracefully.")
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

// BrowseRequest is used by the dedicated resource-discovery endpoint.
// Unlike ConnectRequest it only requires source credentials and a resource_type;
// the target is not contacted (we are only browsing what to migrate from).
type BrowseRequest struct {
	SourceURL      string `json:"source_url"`
	SourceUsername string `json:"source_username"`
	SourcePassword string `json:"source_password"`
	SourceProvider string `json:"source_provider"`
	ResourceType   string `json:"resource_type"` // "calendars", "contacts", or "files"
	Path           string `json:"path"`
}

// normalizeProviderURL returns the canonical URL for providers that have a
// fixed, hardcoded endpoint (currently MagentaCLOUD). The frontend sends an
// empty URL for these; the factory ignores the URL anyway, but persisting the
// constant keeps migration records consistent and avoids leaking internal state.
func normalizeProviderURL(provider, urlStr string) string {
	if provider == "magentacloud" {
		return "https://magentacloud.de/remote.php/webdav"
	}
	return urlStr
}

// profileCreds holds the credentials resolved from a stored connection profile.
type profileCreds struct {
	Provider     string
	URL          string
	Username     string
	Password     string
	RefreshToken string
}

// loadProfile resolves a stored connection profile (referenced by ID) into its
// credentials. Profiles are role-agnostic and may be used as source or target.
// Explicit request fields (passed into the creds struct) take precedence over
// profile values. Returns the merged creds and the decrypted refresh token so
// callers can re-store it on a migration.
func (s *APIServer) loadProfile(r *http.Request, profileID string, base profileCreds) (profileCreds, error) {
	if profileID == "" {
		return base, nil
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owned, err := db.VerifyProfileOwnership(s.db, profileID, userID)
	if err != nil {
		return base, err
	}
	if !owned {
		return base, errors.New("profile not owned")
	}
	p, err := db.GetConnectionProfile(s.db, profileID)
	if err != nil {
		return base, errors.New("profile not found")
	}

	provider := base.Provider
	if provider == "" {
		provider = p.Provider
	}
	urlStr := base.URL
	if urlStr == "" {
		urlStr = p.URL
	}
	username := base.Username
	if username == "" {
		username = p.Username
	}
	password := base.Password
	if password == "" && p.PasswordEncrypted != "" {
		if dec, derr := crypto.Decrypt(p.PasswordEncrypted, s.encryptionKey); derr == nil {
			password = dec
		}
	}
	refreshToken := base.RefreshToken
	if refreshToken == "" && p.RefreshTokenEncrypted != "" {
		if dec, derr := crypto.Decrypt(p.RefreshTokenEncrypted, s.encryptionKey); derr == nil {
			refreshToken = dec
		}
	}

	// OAuth providers need an *access* token in the password field (the storage
	// layer treats it as a Bearer token). A stored profile only keeps the refresh
	// token, so exchange it for a fresh access token before connecting/starting.
	isOAuth := p.Provider == "dropbox" || p.Provider == "google" || p.Provider == "googlephotos"
	if isOAuth && refreshToken != "" {
		if tok, terr := oauth.RefreshToken(r.Context(), p.Provider, refreshToken); terr == nil && tok.AccessToken != "" {
			password = tok.AccessToken
		}
	}

	return profileCreds{
		Provider:     provider,
		URL:          urlStr,
		Username:     username,
		Password:     password,
		RefreshToken: refreshToken,
	}, nil
}

// handleBrowse lists the top-level calendar collections or addressbooks, or files/directories on the source server.
// It contacts only the source, avoiding the two extra round-trips that reusing handleConnect would cause.
func (s *APIServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req BrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	if req.ResourceType != "calendars" && req.ResourceType != "contacts" && req.ResourceType != "files" {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ok, err := sourceClient.Connect(ctx)
	if !ok {
		log.Printf("handleBrowse: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	// A Google Photos SOURCE is selected via the Picker UI, not a browsable
	// folder tree (the read scope no longer grants library listing). Browsing
	// would 403 and surface a confusing generic error, so short-circuit with a
	// clear machine-readable code and an empty list.
	if req.SourceProvider == "googlephotos" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":    false,
			"error_code": ErrGooglePhotosPickerBrowseUnsupported,
		})
		return
	}

	// List the requested path for files, or root "/" for calendars/contacts
	items, err := sourceClient.GetDirectoryListing(ctx, req.ResourceType, reqPath)
	if err != nil {
		log.Printf("handleBrowse: failed to list %s for path %s (provider %s): %v", req.ResourceType, reqPath, req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":    false,
			"error_code": ErrListFailed,
		})
		return
	}

	// For files, return all resources. For calendars/contacts, only return directories (collections).
	var collections []storage.CloudResource
	for _, item := range items {
		if req.ResourceType == "files" || item.IsDir {
			collections = append(collections, item)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"items":   collections,
		"files":   collections, // back-compat for frontend using data.files
	})
}

type TargetBrowseRequest struct {
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	TargetProvider string `json:"target_provider"`
	TargetProfileID string `json:"target_profile_id"`
	Path           string `json:"path"`
}

func (s *APIServer) handleTargetBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetBrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}

	// Resolve a stored connection profile (by ID) into its credentials, so a
	// profile-selected target works even when the inline URL is blank (the same
	// server-side resolution used by handleConnect/handleStart).
	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
			Provider: req.TargetProvider,
			URL:      req.TargetURL,
			Username: req.TargetUsername,
			Password: req.TargetPassword,
		})
		if err != nil {
			log.Printf("handleTargetBrowse: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		// Do NOT forward err.Error() verbatim — the HTTP client may embed the URL
		// including credentials in the error string.
		log.Printf("handleTargetBrowse: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	files, err := targetClient.GetDirectoryListing(ctx, "files", reqPath)
	if err != nil {
		log.Printf("handleTargetBrowse: failed to list target files for path %s (provider %s): %v", reqPath, req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrListFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type TargetMkdirRequest struct {
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	TargetProvider string `json:"target_provider"`
	TargetProfileID string `json:"target_profile_id"`
	Path           string `json:"path"`
}

func (s *APIServer) handleTargetMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetMkdirRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if req.Path == "" || req.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrFolderPathInvalid})
		return
	}

	// Resolve a stored connection profile (by ID) into its credentials, so a
	// profile-selected target works even when the inline URL is blank.
	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
			Provider: req.TargetProvider,
			URL:      req.TargetURL,
			Username: req.TargetUsername,
			Password: req.TargetPassword,
		})
		if err != nil {
			log.Printf("handleTargetMkdir: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		// Do NOT forward err.Error() verbatim — the HTTP client may embed the URL
		// including credentials in the error string.
		log.Printf("handleTargetMkdir: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	err = targetClient.CreateDirectory(ctx, "files", req.Path)
	if err != nil {
		log.Printf("handleTargetMkdir: CreateDirectory(%s) failed: %v", req.Path, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrFolderCreateFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

func (s *APIServer) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "PAUSED", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationPaused, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "PAUSED" && mig.Status != "PAUSED_CONNECTION_LOSS" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "RUNNING", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationResumed, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "COMPLETED" && mig.Status != "COMPLETED_WITH_ERRORS" && mig.Status != "FAILED" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	count, err := db.ResetFailedTasksForRetry(s.db, r.Context(), id)
	if err != nil {
		log.Printf("Error resetting failed tasks for retry: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "retried": count})
}

// handleReindex re-runs the indexing phase for an existing FAILED migration. This
// is the recovery path for migrations that failed during indexing (e.g. a WebDAV
// PROPFIND timeout) where there are no FAILED tasks for handleRetryFailed to reset.
// It clears prior tasks/indexing errors, resets counters, and spawns the shared
// indexer again on the same migration.
func (s *APIServer) handleReindex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "FAILED" && mig.Status != "COMPLETED_WITH_ERRORS" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	if err := db.ResetMigrationForReindex(s.db, r.Context(), id); err != nil {
		if errors.Is(err, db.ErrMigrationNotFailed) {
			// Already re-indexing or no longer FAILED (e.g. a concurrent re-trigger).
			// Treat as a benign conflict rather than a server error.
			writeError(w, http.StatusConflict, ErrMigrationReindexConflict)
			return
		}
		log.Printf("Reindex error for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Spawn the shared indexer (it will set status to INDEXING then RUNNING).
	go s.indexer.Start(s.ctx, id)

	log.Printf("Migration %s re-index triggered.\n", id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true, "migration_id": id})
}

func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	// 1. Update Migration Status
	err = db.UpdateMigrationStatus(s.db, id, "CANCELLED", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// 2. Mark pending tasks as CANCELLED
	err = db.CancelPendingTasks(s.db, id)
	if err != nil {
		log.Printf("Warning: failed to cancel pending tasks for migration %s: %v", id, err)
	}

	// 3. Publish Redis PubSub event to cancel running contexts on workers
	if err := s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		log.Printf("Warning: failed to publish cancel event for migration %s: %v — in-flight tasks will be aborted via DB status check", id, err)
	}

	s.writeAudit(r, db.AuditMigrationCancelled, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type BandwidthRequest struct {
	LimitMbps int `json:"limit_mbps"`
}

type ThreadsRequest struct {
	Threads int `json:"threads"`
}

func (s *APIServer) handleSetThreads(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req ThreadsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateMigrationThreads(s.db, id, threads); err != nil {
		log.Printf("Error updating threads for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleSetBandwidth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req BandwidthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.LimitMbps < 0 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}
	if req.LimitMbps > 1000 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}

	if err := db.UpdateMigrationBandwidthLimit(s.db, id, req.LimitMbps); err != nil {
		log.Printf("Error updating bandwidth limit for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err == nil {
		switch mig.Status {
		case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
	}

	if err := s.queue.PublishBandwidthChange(r.Context(), queue.BandwidthEvent{
		MigrationID:        id,
		BandwidthLimitMbps: req.LimitMbps,
	}); err != nil {
		log.Printf("Warning: failed to publish bandwidth change for migration %s: %v", id, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type ConnectRequest struct {
	SourceURL            string `json:"source_url"`
	SourceUsername       string `json:"source_username"`
	SourcePassword       string `json:"source_password"`
	SourceRefreshToken   string `json:"source_refresh_token"`
	SourceTokenExpiresIn int    `json:"source_token_expires_in"`
	TargetURL            string `json:"target_url"`
	TargetUsername       string `json:"target_username"`
	TargetPassword       string `json:"target_password"`
	TargetRefreshToken   string `json:"target_refresh_token"`
	TargetTokenExpiresIn int    `json:"target_token_expires_in"`
	SourceProvider       string `json:"source_provider"`
	TargetProvider       string `json:"target_provider"`
	SourcePickerSessionID string `json:"source_picker_session_id"`
	Path                 string `json:"path"`
	ResourceType         string `json:"resource_type"`
	// Optional reusable profile references (explicit request fields override).
	SourceProfileID string `json:"source_profile_id"`
	TargetProfileID string `json:"target_profile_id"`
}

func (s *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	// Merge any referenced reusable connection profiles into the request.
	// Explicit request fields win; profile values fill the blanks.
	src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
		Provider:     req.SourceProvider,
		URL:          req.SourceURL,
		Username:     req.SourceUsername,
		Password:     req.SourcePassword,
		RefreshToken: req.SourceRefreshToken,
	})
	if err != nil {
		log.Printf("handleConnect: failed to load source profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.SourceProvider = src.Provider
	req.SourceURL = src.URL
	req.SourceUsername = src.Username
	if req.SourcePassword == "" {
		req.SourcePassword = src.Password
	}
	if req.SourceRefreshToken == "" {
		req.SourceRefreshToken = src.RefreshToken
	}

	tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider:     req.TargetProvider,
		URL:          req.TargetURL,
		Username:     req.TargetUsername,
		Password:     req.TargetPassword,
		RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		log.Printf("handleConnect: failed to load target profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.TargetProvider = tgt.Provider
	req.TargetURL = tgt.URL
	req.TargetUsername = tgt.Username
	if req.TargetPassword == "" {
		req.TargetPassword = tgt.Password
	}
	if req.TargetRefreshToken == "" {
		req.TargetRefreshToken = tgt.RefreshToken
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)
	if req.ResourceType == "" {
		req.ResourceType = "files"
	}

	// Whitelist provider values to fail fast with a clear error (single source of
	// truth: storage.IsValidProvider / storage.ValidProviders).
	if !storage.IsValidProvider(req.SourceProvider) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error_code": ErrProviderUnsupported})
		return
	}
	if !storage.IsValidProvider(req.TargetProvider) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error_code": ErrProviderUnsupported})
		return
	}

	// Test Source Connection
	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()
	srcCtx, srcCancel := context.WithTimeout(r.Context(), 15*time.Second)
	sourceOK, err := sourceClient.Connect(srcCtx)
	srcCancel()
	if !sourceOK {
		log.Printf("handleConnect: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	// Test Target Connection
	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()
	tgtCtx, tgtCancel := context.WithTimeout(r.Context(), 15*time.Second)
	targetOK, err := targetClient.Connect(tgtCtx)
	tgtCancel()
	if !targetOK {
		log.Printf("handleConnect: target connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	// Also render the source folder structure (defaults to root /).
	// For a Google Photos Picker source the user selected individual media in
	// the Picker UI; enumerate exactly those items here so the file-selection
	// screen can show (and pre-select) them before the migration starts.
	var files []storage.CloudResource
	if req.SourceProvider == "googlephotos" {
		if req.SourcePickerSessionID != "" {
			if gp, ok := sourceClient.(*storage.GooglePhotosProvider); ok {
				gp.SetPickerSession(req.SourcePickerSessionID)
				items, lerr := gp.GetPickerMediaItems(r.Context(), req.SourcePickerSessionID)
				if lerr != nil {
					// Try a single token refresh on expiry, mirroring the picker
					// media/session/poll handlers, so a stale access token picked
					// up on a lingering connect screen does not abort the connect.
					if (errors.Is(lerr, storage.ErrAuth) || errors.Is(lerr, storage.ErrPickerSessionExpired)) && req.SourceRefreshToken != "" {
						refreshed, rerr := refreshGooglePhotosClient(r.Context(), req.SourceRefreshToken)
						if rerr == nil {
							gp = refreshed
							defer gp.Close()
							gp.SetPickerSession(req.SourcePickerSessionID)
							items, lerr = gp.GetPickerMediaItems(r.Context(), req.SourcePickerSessionID)
						}
					}
					if lerr != nil {
						log.Printf("handleConnect: failed to list google photos picker items (session %s): %v", req.SourcePickerSessionID, lerr)
						writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrListFailed})
						return
					}
				}
				for _, item := range items {
					files = append(files, storage.CloudResource{
						Path:   storage.PickerPath(item),
						Name:   item.Name,
						Size:   item.Size,
						IsDir:  false,
						Hash:   "",
						LastModified: time.Time{},
					})
				}
			}
		}
		// Without a Picker session there is no folder tree to render; return an
		// empty list (media is selected later on the file-selection screen).
	} else {
		reqPath := req.Path
		if reqPath == "" {
			reqPath = "/"
		}
		listCtx, listCancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer listCancel()
		files, err = sourceClient.GetDirectoryListing(listCtx, req.ResourceType, reqPath)
		if err != nil {
			log.Printf("handleConnect: failed to list source files for path %s (provider %s): %v", reqPath, req.SourceProvider, err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrListFailed})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

// GooglePhotosPickerSessionRequest is the body of POST /api/googlephotos/picker/session.
type GooglePhotosPickerSessionRequest struct {
	Provider      string `json:"provider"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
}

// refreshGooglePhotosClient attempts a single OAuth token refresh for the
// googlephotos provider and, on success, builds a fresh *storage.GooglePhotosProvider
// from the refreshed access token. The caller is responsible for closing the
// returned client. This centralises the refresh-plus-rebuild logic shared by the
// Picker session and poll handlers so the two cannot drift apart.
func refreshGooglePhotosClient(ctx context.Context, refreshToken string) (*storage.GooglePhotosProvider, error) {
	if refreshToken == "" {
		return nil, errors.New("empty refresh token")
	}
	refreshed, rerr := oauth.RefreshToken(ctx, "googlephotos", refreshToken)
	if rerr != nil || refreshed.AccessToken == "" {
		if rerr != nil {
			return nil, rerr
		}
		return nil, errors.New("refresh returned empty access token")
	}
	return storage.NewGooglePhotosProvider(context.Background(), refreshed.AccessToken)
}

// handleGooglePhotosPickerSession creates a Google Photos Picker session for an
// already-authenticated googlephotos source. The returned session_id is used by
// the frontend to open the Photos Picker in a new tab; the actual media items
// are enumerated later by the indexer (so the session is reused end-to-end).
// Never log the picker session's derived URLs/baseUrls — they are credentialed.
func (s *APIServer) handleGooglePhotosPickerSession(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req GooglePhotosPickerSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	if req.Provider != "googlephotos" {
		writeError(w, http.StatusBadRequest, ErrProviderUnsupported)
		return
	}
	if req.AccessToken == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	// Build a provider with the caller-supplied access token and create a Picker
	// session. We do not decrypt any DB-stored token here — the frontend passes
	// the freshly returned OAuth access token from the popup. Use a detached
	// context for the provider's HTTP client: the request body is already fully
	// decoded above, and a client built from r.Context() would have its transport
	// torn down if the client disconnects mid-call, failing the session creation.
	accessToken := req.AccessToken
	client, err := storage.NewGooglePhotosProvider(context.Background(), accessToken)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
		return
	}
	defer client.Close()

	sessionCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	session, err := client.CreatePickerSessionFull(sessionCtx)
	if err != nil {
		// A 403 means the Picker API service is not enabled in the Cloud
		// project — tell the user specifically instead of the generic failure.
		if errors.Is(err, storage.ErrPickerAPIForbidden) {
			log.Printf("handleGooglePhotosPickerSession: picker API forbidden (user=%s): %v", userID, err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerAPIDisabled})
			return
		}
		// If the access token expired (401), try a single refresh before giving
		// up. The frontend passes a freshly minted access token, but it may still
		// be stale if the user lingered on the connect screen.
		if errors.Is(err, storage.ErrAuth) && req.RefreshToken != "" {
			var refreshed *storage.GooglePhotosProvider
			refreshed, rerr := refreshGooglePhotosClient(sessionCtx, req.RefreshToken)
			if rerr == nil {
				client = refreshed
				defer client.Close()
				session, err = client.CreatePickerSessionFull(sessionCtx)
			}
		}
		if err != nil {
			log.Printf("handleGooglePhotosPickerSession: failed to create session (user=%s): %v", userID, err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
			return
		}
	}

	// Log only the session id length — never the id itself or the pickerUri.
	log.Printf("handleGooglePhotosPickerSession: created picker session for user %s (idLen=%d)", userID, len(session.SessionID))

	resp := map[string]interface{}{
		"success":    true,
		"session_id": session.SessionID,
		"picker_uri": session.PickerURI,
	}
	if session.PollingConfig != nil {
		resp["poll_interval"] = session.PollingConfig.PollInterval
		resp["timeout_in"] = session.PollingConfig.TimeoutIn
	}
	writeJSON(w, http.StatusOK, resp)
}

// GooglePhotosPickerPollRequest is the body of POST /api/googlephotos/picker/poll.
// It carries the session id to check and the OAuth token needed to authenticate
// the sessions.get call. The frontend polls this after the user opens the
// pickerUri, until media_items_set becomes true.
type GooglePhotosPickerPollRequest struct {
	Provider     string `json:"provider"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
}

// handleGooglePhotosPickerPoll checks whether the user has finished selecting
// media in a Google Photos Picker session. The Photos Picker API has no
// embeddable widget: the user opens the pickerUri in a new tab, and the app
// polls sessions.get until mediaItemsSet is true.
func (s *APIServer) handleGooglePhotosPickerPoll(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req GooglePhotosPickerPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	if req.Provider != "googlephotos" || req.AccessToken == "" || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	client, err := storage.NewGooglePhotosProvider(context.Background(), req.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
		return
	}
	defer client.Close()

	pollCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	session, err := client.GetPickerSession(pollCtx, req.SessionID)
	if err != nil {
		// Try one token refresh on expiry, mirroring session creation.
		if errors.Is(err, storage.ErrAuth) && req.RefreshToken != "" {
			refreshed, rerr := refreshGooglePhotosClient(pollCtx, req.RefreshToken)
			if rerr == nil {
				client = refreshed
				defer client.Close()
				session, err = client.GetPickerSession(pollCtx, req.SessionID)
			}
		}
		if err != nil {
			if errors.Is(err, storage.ErrPickerSessionExpired) {
				writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
				return
			}
			log.Printf("handleGooglePhotosPickerPoll: failed to poll session (user=%s): %v", userID, err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
			return
		}
	}

	resp := map[string]interface{}{
		"success":         true,
		"media_items_set": session.MediaItemsSet,
	}
	if session.PollingConfig != nil {
		resp["poll_interval"] = session.PollingConfig.PollInterval
		resp["timeout_in"] = session.PollingConfig.TimeoutIn
	}
	writeJSON(w, http.StatusOK, resp)
}

// GooglePhotosPickerMediaRequest is the body of POST /api/googlephotos/picker/media.
// It carries the confirmed Picker session id and the OAuth tokens. The frontend
// calls this once the user has finished selecting media in the Picker UI (after
// the poll reports media_items_set), to fetch the concrete list of chosen items
// for display in the file-selection screen.
type GooglePhotosPickerMediaRequest struct {
	Provider     string `json:"provider"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
}

// handleGooglePhotosPickerMedia enumerates the media items the user selected in a
// Google Photos Picker session and returns them as CloudResources (path =
// PickerPath transport handle, name, size, is_dir=false). These are shown in the
// file-selection screen so the user can review and (de)select before starting.
func (s *APIServer) handleGooglePhotosPickerMedia(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req GooglePhotosPickerMediaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	if req.Provider != "googlephotos" || req.AccessToken == "" || req.SessionID == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	client, err := storage.NewGooglePhotosProvider(context.Background(), req.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
		return
	}
	defer client.Close()

	mediaCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	client.SetPickerSession(req.SessionID)
	items, err := client.GetPickerMediaItems(mediaCtx, req.SessionID)
	if err != nil {
		// Try one token refresh on expiry, mirroring session creation/poll.
		if (errors.Is(err, storage.ErrAuth) || errors.Is(err, storage.ErrPickerSessionExpired)) && req.RefreshToken != "" {
			refreshed, rerr := refreshGooglePhotosClient(mediaCtx, req.RefreshToken)
			if rerr == nil {
				client = refreshed
				defer client.Close()
				client.SetPickerSession(req.SessionID)
				items, err = client.GetPickerMediaItems(mediaCtx, req.SessionID)
			}
		}
		if err != nil {
			if errors.Is(err, storage.ErrPickerSessionExpired) {
				writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
				return
			}
			log.Printf("handleGooglePhotosPickerMedia: failed to list items (user=%s, session=%s): %v", userID, req.SessionID, err)
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrGooglePhotosPickerSessionFailed})
			return
		}
	}

	files := make([]storage.CloudResource, 0, len(items))
	for _, item := range items {
		files = append(files, storage.CloudResource{
			Path:         storage.PickerPath(item),
			Name:         item.Name,
			Size:         item.Size,
			IsDir:        false,
			Hash:         "",
			LastModified: time.Time{},
		})
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type StartRequest struct {
	ConnectRequest
	ConflictStrategy   string   `json:"conflict_strategy"`
	Paths              []string `json:"paths"`
	Calendars          []string `json:"calendars"`
	Contacts           []string `json:"contacts"`
	TargetDir          string   `json:"target_dir"`
	Threads            int      `json:"threads"`
	ScheduledTime      string   `json:"scheduled_time"`
	BandwidthLimitMbps int      `json:"bandwidth_limit_mbps"`
}

func (s *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	// Merge any referenced reusable connection profiles into the request.
	// Explicit request fields win; profile values fill the blanks.
	src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
		Provider:     req.SourceProvider,
		URL:          req.SourceURL,
		Username:     req.SourceUsername,
		Password:     req.SourcePassword,
		RefreshToken: req.SourceRefreshToken,
	})
	if err != nil {
		log.Printf("handleStart: failed to load source profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.SourceProvider = src.Provider
	req.SourceURL = src.URL
	req.SourceUsername = src.Username
	if req.SourcePassword == "" {
		req.SourcePassword = src.Password
	}
	if req.SourceRefreshToken == "" {
		req.SourceRefreshToken = src.RefreshToken
	}

	tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider:     req.TargetProvider,
		URL:          req.TargetURL,
		Username:     req.TargetUsername,
		Password:     req.TargetPassword,
		RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		log.Printf("handleStart: failed to load target profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.TargetProvider = tgt.Provider
	req.TargetURL = tgt.URL
	req.TargetUsername = tgt.Username
	if req.TargetPassword == "" {
		req.TargetPassword = tgt.Password
	}
	if req.TargetRefreshToken == "" {
		req.TargetRefreshToken = tgt.RefreshToken
	}

	if len(req.Paths) == 0 && len(req.Calendars) == 0 && len(req.Contacts) == 0 {
		writeError(w, http.StatusBadRequest, ErrNoSourcePaths)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)

	// MagentaCLOUD is files-only (CalDAV/CardDAV are on a separate service), so
	// it cannot migrate calendars or contacts.
	if req.SourceProvider == "magentacloud" && (len(req.Calendars) > 0 || len(req.Contacts) > 0) {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}
	if req.TargetProvider == "magentacloud" && (len(req.Calendars) > 0 || len(req.Contacts) > 0) {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	// Reject host-based providers submitted with a blank URL up front, instead of
	// letting them fail cryptically during indexing with a raw error string.
	if err := storage.ValidateProviderURL(req.SourceProvider, req.SourceURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrSourceUrlInvalid)
		return
	}
	if err := storage.ValidateProviderURL(req.TargetProvider, req.TargetURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrTargetUrlInvalid)
		return
	}

	targetDir := req.TargetDir
	if targetDir == "" {
		targetDir = "/"
	}

	// Encrypt credentials
	sourcePassEnc, err := crypto.Encrypt(req.SourcePassword, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	targetPassEnc, err := crypto.Encrypt(req.TargetPassword, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	// Encrypt OAuth refresh tokens (if provided)
	var sourceRefreshEnc sql.NullString
	var sourceTokenExpiresAt sql.NullTime
	if req.SourceRefreshToken != "" {
		enc, err := crypto.Encrypt(req.SourceRefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		sourceRefreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.SourceTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		sourceTokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	var targetRefreshEnc sql.NullString
	var targetTokenExpiresAt sql.NullTime
	if req.TargetRefreshToken != "" {
		enc, err := crypto.Encrypt(req.TargetRefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		targetRefreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.TargetTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		targetTokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	// Get userID from context
	userID := auth.GetUserIDFromContext(r.Context())

	// Enforce a per-user cap on simultaneously active migrations to prevent
	// resource exhaustion / runaway scheduling from a single account.
	active, err := db.CountActiveMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("handleStart: failed to count active migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if active >= maxActiveMigrations {
		writeError(w, http.StatusConflict, ErrTooManyActiveMigrations)
		return
	}

	// Validate threads
	threads := req.Threads
	if threads < 1 {
		threads = 4
	} else if threads > 16 {
		threads = 16
	}

	// Google Photos is rate-limited and single-threaded (parallel uploads
	// exhaust the quota with HTTP 429 almost immediately). Force threads to 1
	// when either side is googlephotos, regardless of what the client sent.
	if req.SourceProvider == "googlephotos" || req.TargetProvider == "googlephotos" {
		threads = 1
	}

	// Validate bandwidth limit
	bandwidthLimit := req.BandwidthLimitMbps
	if bandwidthLimit < 0 {
		bandwidthLimit = 0
	} else if bandwidthLimit > 1000 {
		bandwidthLimit = 1000
	}

	// Determine initial status based on whether scheduling is requested
	initialStatus := "INDEXING"
	var scheduledAt time.Time
	if req.ScheduledTime != "" {
		var err error
		scheduledAt, err = time.Parse(time.RFC3339, req.ScheduledTime)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrInvalidScheduledTime)
			return
		}
		if scheduledAt.Before(time.Now()) {
			writeError(w, http.StatusBadRequest, ErrScheduledTimePast)
			return
		}
		initialStatus = "SCHEDULED"
	}

	// Create Migration Record
	m := &db.Migration{
		UserID:                      sql.NullString{String: userID, Valid: userID != ""},
		SourceURL:                   req.SourceURL,
		SourceUsername:              req.SourceUsername,
		SourcePasswordEncrypted:     sourcePassEnc,
		SourceRefreshTokenEncrypted: sourceRefreshEnc,
		SourceTokenExpiresAt:        sourceTokenExpiresAt,
		TargetURL:                   req.TargetURL,
		TargetUsername:              req.TargetUsername,
		TargetPasswordEncrypted:     targetPassEnc,
		TargetRefreshTokenEncrypted: targetRefreshEnc,
		TargetTokenExpiresAt:        targetTokenExpiresAt,
		SourceProvider:              req.SourceProvider,
		TargetProvider:              req.TargetProvider,
		Status:                      initialStatus,
		ConflictStrategy:            req.ConflictStrategy,
		TargetDir:                   targetDir,
		SelectedPaths:               db.StringArray(req.Paths),
		SelectedCalendars:           db.StringArray(req.Calendars),
		SelectedContacts:            db.StringArray(req.Contacts),
		Threads:                     threads,
		BandwidthLimitMbps:          bandwidthLimit,
		PickerSessionID:            req.SourcePickerSessionID,
	}

	migrationID, err := db.CreateMigration(s.db, m)
	if err != nil {
		log.Printf("Start migration error: failed to create migration: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationCreated, migrationID, userID, map[string]interface{}{
		"source_provider": m.SourceProvider,
		"target_provider": m.TargetProvider,
		"scheduled":       req.ScheduledTime != "",
	})

	// If scheduled, create a schedule entry instead of starting immediately
	if req.ScheduledTime != "" {
		schedule := &db.Schedule{
			UserID:    userID,
			TaskType:  "migration",
			TaskID:    migrationID,
			RunAt:     sql.NullTime{Time: scheduledAt, Valid: true},
			NextRunAt: sql.NullTime{Time: scheduledAt, Valid: true},
			IsActive:  true,
		}

		_, err = db.CreateSchedule(s.db, schedule)
		if err != nil {
			log.Printf("Failed to create schedule for migration %s: %v\n", migrationID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}

		log.Printf("Migration %s scheduled for %s\n", migrationID, scheduledAt.Format(time.RFC3339))

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"success":        true,
			"migration_id":   migrationID,
			"scheduled":      true,
			"scheduled_time": scheduledAt.Format(time.RFC3339),
		})
		return
	}

	// Spawn Background Indexer (immediate start) using shared indexer package
	go s.indexer.Start(s.ctx, migrationID)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
}

// --------------------------------------------------------------------------
// Connection Profiles (reusable source/target credentials)
// --------------------------------------------------------------------------

const profileRateLimit = 60

// ConnectionProfileRequest is the body for POST/PUT /api/profiles.
// Credentials are optional on PUT (omitted fields are left unchanged).
type ConnectionProfileRequest struct {
	Name                  string  `json:"name"`
	Provider              string  `json:"provider"`
	URL                   string  `json:"url"`
	Username              string  `json:"username"`
	Password              string  `json:"password"`
	RefreshToken          string  `json:"refresh_token"`
	RefreshTokenExpiresIn int     `json:"refresh_token_expires_in"`
	OAuthUser             string  `json:"oauth_user"`
}

// handleListProfiles returns the user's profiles.
func (s *APIServer) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	profiles, err := db.GetConnectionProfiles(s.db, userID, "")
	if err != nil {
		log.Printf("handleListProfiles: query failed for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	public := make([]db.ConnectionProfilePublic, 0, len(profiles))
	for i := range profiles {
		public = append(public, profiles[i].ToPublic())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"profiles": public,
	})
}

// handleCreateProfile inserts a new profile, validating the provider whitelist
// and encrypting any supplied credentials.
func (s *APIServer) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req ConnectionProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Name == "" || req.Provider == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}
	if !storage.IsValidProvider(req.Provider) {
		writeError(w, http.StatusBadRequest, ErrProfileInvalidProvider)
		return
	}

	urlStr := normalizeProviderURL(req.Provider, req.URL)

	// Reject host-based providers submitted with a blank URL up front, instead
	// of letting them fail cryptically during indexing with a raw error string.
	if err := storage.ValidateProviderURL(req.Provider, urlStr); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}

	var passEnc string
	if req.Password != "" {
		enc, err := crypto.Encrypt(req.Password, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		passEnc = enc
	}

	var refreshEnc sql.NullString
	var tokenExpiresAt sql.NullTime
	if req.RefreshToken != "" {
		enc, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		refreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.RefreshTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		tokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	p := &db.ConnectionProfile{
		UserID:                 userID,
		Name:                   req.Name,
		Provider:               req.Provider,
		URL:                    urlStr,
		Username:               req.Username,
		PasswordEncrypted:     passEnc,
		RefreshTokenEncrypted: refreshEnc.String,
		TokenExpiresAt:        tokenExpiresAt,
		OAuthUser:             req.OAuthUser,
	}
	if !refreshEnc.Valid {
		p.RefreshTokenEncrypted = ""
	}

	id, err := db.CreateConnectionProfile(s.db, p)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		log.Printf("handleCreateProfile: insert failed for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationCreated, id, userID, map[string]interface{}{
		"action": "PROFILE_CREATED",
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

// handleGetProfile returns a single profile (404 on non-owner / missing).
func (s *APIServer) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleGetProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	p, err := db.GetConnectionProfile(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "profile": p.ToPublic()})
}

// handleUpdateConnectionProfile applies a partial update to a profile.
func (s *APIServer) handleUpdateConnectionProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleUpdateProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}

	var req ConnectionProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	in := db.UpdateConnectionProfileInput{}
	if req.Name != "" {
		in.Name = &req.Name
	}
	if req.Provider != "" {
		if !storage.IsValidProvider(req.Provider) {
			writeError(w, http.StatusBadRequest, ErrProfileInvalidProvider)
			return
		}
		in.Provider = &req.Provider
	}
	if r.URL.Query().Get("url") == "1" || req.URL != "" {
		u := normalizeProviderURL(req.Provider, req.URL)
		in.URL = &u
	}
	if r.URL.Query().Get("username") == "1" || req.Username != "" {
		in.Username = &req.Username
	}
	if req.Password != "" {
		enc, err := crypto.Encrypt(req.Password, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		in.PasswordEncrypted = &enc
	}
	if req.RefreshToken != "" {
		enc, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		in.RefreshTokenEncrypted = &enc
		expiresIn := req.RefreshTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		exp := time.Now().Add(time.Duration(expiresIn) * time.Second)
		in.TokenExpiresAt = &exp
	}
	if req.OAuthUser != "" {
		in.OAuthUser = &req.OAuthUser
	}

	// Validate the resulting provider/URL combination (merging with the existing
	// profile, since updates are partial). Rejects host-based providers left with
	// a blank URL so they cannot fail cryptically during indexing later.
	if existing, gerr := db.GetConnectionProfile(s.db, id); gerr == nil {
		mergedProvider := existing.Provider
		if in.Provider != nil {
			mergedProvider = *in.Provider
		}
		mergedURL := existing.URL
		if in.URL != nil {
			mergedURL = *in.URL
		}
		if err := storage.ValidateProviderURL(mergedProvider, mergedURL); err != nil {
			writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
			return
		}
	}

	if err := db.UpdateConnectionProfile(s.db, id, in); err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		log.Printf("handleUpdateProfile: update failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleDeleteProfile removes a profile (404 on non-owner / missing).
func (s *APIServer) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleDeleteProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	if err := db.DeleteConnectionProfile(s.db, id); err != nil {
		log.Printf("handleDeleteProfile: delete failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditMigrationDeleted, id, userID, map[string]interface{}{
		"action": "PROFILE_DELETED",
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handleTestProfile verifies the stored credentials of a profile by performing
// a real connection attempt. Returns success:false with a machine-readable
// error_code (never a 4xx) so the frontend can localize the result.
func (s *APIServer) handleTestProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleTestProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	p, err := db.GetConnectionProfile(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}

	// Decrypt the stored credentials for the test only (never returned to client).
	// For OAuth providers the refresh token is supplied as the "password" field
	// (the storage layer exchanges it for an access token on Connect).
	var password, refreshToken string
	if p.PasswordEncrypted != "" {
		dec, derr := crypto.Decrypt(p.PasswordEncrypted, s.encryptionKey)
		if derr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrEncryptionFailed})
			return
		}
		password = dec
	}
	if p.RefreshTokenEncrypted != "" {
		dec, derr := crypto.Decrypt(p.RefreshTokenEncrypted, s.encryptionKey)
		if derr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrEncryptionFailed})
			return
		}
		refreshToken = dec
	}
	// A stored profile keeps only the OAuth refresh token; exchange it for a
	// fresh access token (used as the Bearer token by the storage layer) before
	// testing the connection.
	isOAuth := p.Provider == "dropbox" || p.Provider == "google" || p.Provider == "googlephotos"
	if isOAuth && refreshToken != "" {
		password = refreshToken
		if tok, terr := oauth.RefreshToken(r.Context(), p.Provider, refreshToken); terr == nil && tok.AccessToken != "" {
			password = tok.AccessToken
		}
	}

	client, err := storage.NewProvider(r.Context(), p.Provider, p.URL, p.Username, password)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ok, cerr := client.Connect(ctx)
	if !ok {
		log.Printf("handleTestProfile: connection failed for profile %s (provider %s): %v", id, p.Provider, cerr)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}


func (s *APIServer) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			log.Printf("Error fetching migration %s: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	stats, err := db.GetMigrationResourceStats(s.db, id)
	if err != nil {
		log.Printf("Error fetching resource stats for migration %s: %v\n", id, err)
	} else {
		mig.ResourceStats = stats
	}

	writeJSON(w, http.StatusOK, mig)
}

// csvCell neutralises CSV formula injection: a cell that begins with one of the
// spreadsheet formula trigger characters (= + - @ or a tab/CR) is prefixed with a
// single quote so clients render it as literal text instead of executing it.
// File paths and error messages in the report originate from the source server
// and are therefore attacker-influenced.
func csvCell(s string) string {
	switch {
	case s == "",
		strings.HasPrefix(s, "="), strings.HasPrefix(s, "+"),
		strings.HasPrefix(s, "-"), strings.HasPrefix(s, "@"),
		strings.HasPrefix(s, "\t"), strings.HasPrefix(s, "\r"), strings.HasPrefix(s, "\n"):
		return "'" + s
	}
	return s
}

func (s *APIServer) handleDownloadReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			log.Printf("Error fetching migration %s for report: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	tasks, err := db.GetFailedTasksForReport(s.db, id)
	if err != nil {
		log.Printf("Download report error: failed to get report: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Set headers for download
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=migration_report_%s.csv", id))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write CSV Header
	_ = writer.Write([]string{"File Path", "Size (Bytes)", "Retries", "WebDAV Error Message"})

	for _, task := range tasks {
		errMsg := ""
		if task.ErrorMessage.Valid {
			errMsg = task.ErrorMessage.String
		}
		_ = writer.Write([]string{
			csvCell(task.FilePath),
			fmt.Sprintf("%d", task.FileSize),
			fmt.Sprintf("%d", task.Attempts),
			csvCell(errMsg),
		})
	}

	// Append per-folder indexing errors (skipped folders) so they appear in the
	// same report list rather than being silently dropped.
	indexErrs, err := db.GetIndexingErrorsForReport(s.db, id)
	if err != nil {
		log.Printf("Download report error: failed to get indexing errors: %v\n", err)
	} else {
		for _, ie := range indexErrs {
			// Size/Retries columns are not meaningful for skipped folders; leave
			// Retries blank so the report does not imply a retry was attempted.
			_ = writer.Write([]string{
				csvCell(ie.Path),
				"0",
				"",
				csvCell(fmt.Sprintf("[indexing/%s] %s", ie.ResourceType, ie.ErrorMessage)),
			})
		}
	}
}

// handleWebSocket handles the progress update stream
func (s *APIServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	tokenStr := ""
	isProtocolToken := false

	if protocol := r.Header.Get("Sec-WebSocket-Protocol"); protocol != "" {
		parts := strings.Split(protocol, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				tokenStr = trimmed
				isProtocolToken = true
				break
			}
		}
	}

	if tokenStr == "" {
		// Fallback to query param, but reject on secure HTTPS connections
		queryToken := r.URL.Query().Get("token")
		if queryToken != "" {
			isHTTPS := s.isSecure(r)
			if isHTTPS {
				writeError(w, http.StatusUnauthorized, ErrWsTokenInsecure)
				return
			}
			tokenStr = queryToken
		}
	}

	if tokenStr == "" {
		writeError(w, http.StatusUnauthorized, ErrWsTokenMissing)
		return
	}

	claims, err := auth.ValidateToken(tokenStr, s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrWsTokenInvalid)
		return
	}
	// Reject 2FA temp tokens: password-only tokens must not reach the migration WS.
	if err := auth.RequireAuthenticated(claims); err != nil {
		writeError(w, http.StatusUnauthorized, ErrTotpRequired)
		return
	}
	userID := claims.UserID

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var responseHeader http.Header
	if isProtocolToken {
		// Echo the extracted token back in the Sec-WebSocket-Protocol header.
		// Browser WebSocket clients require the upgraded response to contain the selected
		// subprotocol from the handshake request, otherwise the browser will refuse the connection.
		responseHeader = make(http.Header)
		responseHeader.Set("Sec-WebSocket-Protocol", tokenStr)
	}

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket: %v\n", err)
		return
	}
	defer ws.Close()

	log.Printf("WebSocket client connected for migration: %s\n", id)

	// Set read limits and deadlines (Finding 11)
	ws.SetReadLimit(512) // small control frames only
	ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		return nil
	})

	// Start discard loop to process control frames (ping/pong/close)
	go func() {
		for {
			if _, _, err := ws.NextReader(); err != nil {
				break
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-pingTicker.C:
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				log.Printf("WebSocket write ping error: %v\n", err)
				return
			}
		case <-ticker.C:
			// Fetch migration state
			mig, err = db.GetMigration(s.db, id)
			if err != nil {
				return
			}

			// Query active file paths
			activeFiles, _ := db.GetActiveTaskPaths(s.db, r.Context(), id)
			var activeFile string
			if len(activeFiles) > 0 {
				activeFile = activeFiles[0]
			}

			responsePayload := map[string]interface{}{
				"id":                   mig.ID,
				"status":               mig.Status,
				"total_files":          mig.TotalFiles,
				"total_bytes":          mig.TotalBytes,
				"processed_files":      mig.ProcessedFiles,
				"processed_bytes":      mig.ProcessedBytes,
				// live_bytes feeds the transfer-speed / ETA display and may
				// transiently exceed total_bytes on retried uploads. The
				// authoritative "transferred X / Y" byte display uses
				// processed_bytes, which can never exceed total_bytes.
				"live_bytes":           mig.LiveBytes,
				"skipped_files":        mig.SkippedFiles,
				"failed_files":         mig.FailedFiles,
				"error_message":        "",
				"active_file":          activeFile,
				"active_files":         activeFiles,
				"threads":              mig.Threads,
				"bandwidth_limit_mbps": mig.BandwidthLimitMbps,
			}

			if mig.ErrorMessage.Valid {
				responsePayload["error_message"] = mig.ErrorMessage.String
			}

			stats, err := db.GetMigrationResourceStats(s.db, id)
			if err == nil {
				responsePayload["resource_stats"] = stats
			} else {
				log.Printf("WebSocket error fetching resource stats: %v\n", err)
			}

			// Write to WS
			data, err := json.Marshal(responsePayload)
			if err != nil {
				return
			}

			ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err = ws.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				return // Client disconnected
			}

			// If migration is in a terminal state (COMPLETED, COMPLETED_WITH_ERRORS or FAILED) and all tasks finished, close socket after final state
			if (mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED") && mig.ProcessedFiles >= mig.TotalFiles {
				// Pause a bit to let client read the final completed status
				time.Sleep(1 * time.Second)
				return
			}
		}
	}
}

func (s *APIServer) runGarbageCollector(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Println("Running Garbage Collector for old migrations...")
			count, err := db.DeleteOldMigrations(s.db)
			if err != nil {
				log.Printf("Garbage Collector error: %v\n", err)
			} else if count > 0 {
				log.Printf("Garbage Collector cleaned up %d old migrations & task histories.\n", count)
			}
		}
	}
}

// Helpers
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// APIErrorCode is a machine-readable error identifier sent to the client.
// The frontend localizes it via its own translation tables; the backend
// never sends localized text.
type APIErrorCode string

const (
	ErrInvalidBody              APIErrorCode = "INVALID_BODY"
	ErrUnauthorized             APIErrorCode = "UNAUTHORIZED"
	ErrForbidden                APIErrorCode = "FORBIDDEN"
	ErrCredentialsInvalid       APIErrorCode = "CREDENTIALS_INVALID"
	ErrRefreshTokenMissing      APIErrorCode = "REFRESH_TOKEN_MISSING"
	ErrRefreshTokenInvalid      APIErrorCode = "REFRESH_TOKEN_INVALID"
	ErrRegistrationDisabled     APIErrorCode = "REGISTRATION_DISABLED"
	ErrMissingRequiredFields    APIErrorCode = "MISSING_REQUIRED_FIELDS"
	ErrEmailAlreadyExists       APIErrorCode = "EMAIL_ALREADY_EXISTS"
	ErrRateLimited              APIErrorCode = "RATE_LIMITED"
	ErrTotpRequired             APIErrorCode = "TOTP_REQUIRED"
	ErrTotpCodeRequired         APIErrorCode = "TOTP_CODE_REQUIRED"
	ErrTotpSessionInvalid       APIErrorCode = "TOTP_SESSION_INVALID"
	ErrTotpNotEnabled           APIErrorCode = "TOTP_NOT_ENABLED"
	ErrTotpInvalidCode          APIErrorCode = "TOTP_INVALID_CODE"
	ErrTotpAlreadyEnabled       APIErrorCode = "TOTP_ALREADY_ENABLED"
	ErrTotpNoPendingSetup       APIErrorCode = "TOTP_NO_PENDING_SETUP"
	ErrPasswordRequired         APIErrorCode = "PASSWORD_REQUIRED"
	ErrPasswordInvalid          APIErrorCode = "PASSWORD_INVALID"
	ErrMigrationIdMissing       APIErrorCode = "MIGRATION_ID_MISSING"
	ErrMigrationNotOwned        APIErrorCode = "MIGRATION_NOT_OWNED"
	ErrMigrationInvalidState    APIErrorCode = "MIGRATION_INVALID_STATE"
	ErrMigrationReindexConflict APIErrorCode = "MIGRATION_REINDEX_CONFLICT"
	ErrTooManyActiveMigrations  APIErrorCode = "TOO_MANY_ACTIVE_MIGRATIONS"
	ErrMigrationNotFound        APIErrorCode = "MIGRATION_NOT_FOUND"
	ErrThreadsOutOfRange        APIErrorCode = "THREADS_OUT_OF_RANGE"
	ErrBandwidthOutOfRange      APIErrorCode = "BANDWIDTH_OUT_OF_RANGE"
	ErrNoSourcePaths            APIErrorCode = "NO_SOURCE_PATHS"
	ErrEncryptionFailed         APIErrorCode = "ENCRYPTION_FAILED"
	ErrInvalidScheduledTime     APIErrorCode = "INVALID_SCHEDULED_TIME"
	ErrScheduledTimePast        APIErrorCode = "SCHEDULED_TIME_PAST"
	ErrSourceUrlInvalid         APIErrorCode = "SOURCE_URL_INVALID"
	ErrTargetUrlInvalid         APIErrorCode = "TARGET_URL_INVALID"
	ErrSourceConnectionFailed   APIErrorCode = "SOURCE_CONNECTION_FAILED"
	ErrTargetConnectionFailed   APIErrorCode = "TARGET_CONNECTION_FAILED"
	ErrListFailed               APIErrorCode = "LIST_FAILED"
	ErrProviderUnsupported      APIErrorCode = "PROVIDER_UNSUPPORTED"
	ErrFolderPathInvalid        APIErrorCode = "FOLDER_PATH_INVALID"
	ErrFolderCreateFailed       APIErrorCode = "FOLDER_CREATE_FAILED"
	ErrInvalidResourceType      APIErrorCode = "INVALID_RESOURCE_TYPE"
	ErrOauthProviderMissing     APIErrorCode = "OAUTH_PROVIDER_MISSING"
	ErrOauthOriginMissing       APIErrorCode = "OAUTH_ORIGIN_MISSING"
	ErrOauthOriginInvalid       APIErrorCode = "OAUTH_ORIGIN_INVALID"
	ErrOauthOriginUntrusted     APIErrorCode = "OAUTH_ORIGIN_UNTRUSTED"
	ErrOauthGenerationFailed    APIErrorCode = "OAUTH_GENERATION_FAILED"
	ErrDisplayNameRequired      APIErrorCode = "DISPLAY_NAME_REQUIRED"
	ErrPasswordMismatch         APIErrorCode = "PASSWORD_MISMATCH"
	ErrPasswordTooShort         APIErrorCode = "PASSWORD_TOO_SHORT"
	ErrAvatarInvalid            APIErrorCode = "AVATAR_INVALID"
	ErrAvatarTypeUnsupported    APIErrorCode = "AVATAR_TYPE_UNSUPPORTED"
	ErrAvatarTooLarge           APIErrorCode = "AVATAR_TOO_LARGE"
	ErrAdminOnly                APIErrorCode = "ADMIN_ONLY"
	ErrSettingForbidden         APIErrorCode = "SETTING_FORBIDDEN"
	ErrSettingInvalid           APIErrorCode = "SETTING_INVALID"
	ErrScheduleIdMissing        APIErrorCode = "SCHEDULE_ID_MISSING"
	ErrScheduleNotFound         APIErrorCode = "SCHEDULE_NOT_FOUND"
	ErrSmtpConfigIncomplete     APIErrorCode = "SMTP_CONFIG_INCOMPLETE"
	ErrSmtpPortInvalid          APIErrorCode = "SMTP_PORT_INVALID"
	ErrSmtpEncryptionInvalid    APIErrorCode = "SMTP_ENCRYPTION_INVALID"
	ErrSmtpPasswordRequired     APIErrorCode = "SMTP_PASSWORD_REQUIRED"
	ErrMailNotConfigured        APIErrorCode = "MAIL_NOT_CONFIGURED"
	ErrSmtpNotConfigured        APIErrorCode = "SMTP_NOT_CONFIGURED"
	ErrSmtpDecryptFailed        APIErrorCode = "SMTP_DECRYPT_FAILED"
	ErrSmtpTestFailed           APIErrorCode = "SMTP_TEST_FAILED"
	ErrResetFieldsRequired      APIErrorCode = "RESET_FIELDS_REQUIRED"
	ErrResetTokenInvalid        APIErrorCode = "RESET_TOKEN_INVALID"
	ErrEmailInvalid             APIErrorCode = "EMAIL_INVALID"
	ErrEmailUnchanged           APIErrorCode = "EMAIL_UNCHANGED"
	ErrEmailChangeTokenInvalid  APIErrorCode = "EMAIL_CHANGE_TOKEN_INVALID"
	ErrCorsOriginUntrusted      APIErrorCode = "CORS_ORIGIN_UNTRUSTED"
	ErrWsTokenInsecure          APIErrorCode = "WS_TOKEN_INSECURE"
	ErrWsTokenMissing           APIErrorCode = "WS_TOKEN_MISSING"
	ErrWsTokenInvalid           APIErrorCode = "WS_TOKEN_INVALID"
	ErrInternalError            APIErrorCode = "INTERNAL_ERROR"

	// Google Photos Picker
	ErrGooglePhotosPickerSessionFailed APIErrorCode = "GOOGLE_PHOTOS_PICKER_SESSION_FAILED"
	ErrGooglePhotosPickerBrowseUnsupported APIErrorCode = "GOOGLE_PHOTOS_PICKER_BROWSE_UNSUPPORTED"
	// Returned when Google rejects the Picker session creation with HTTP 403,
	// which almost always means the "Google Photos Picker API" service is not
	// enabled for the OAuth client's Cloud project (the scopes alone are not
	// enough — the API must be activated in the Cloud Console).
	ErrGooglePhotosPickerAPIDisabled APIErrorCode = "GOOGLE_PHOTOS_PICKER_API_DISABLED"
	ErrUserDisabled           APIErrorCode = "USER_DISABLED"
	ErrUserNotFound           APIErrorCode = "USER_NOT_FOUND"
	ErrCannotModifySelf       APIErrorCode = "CANNOT_MODIFY_SELF"
	ErrLastAdmin              APIErrorCode = "LAST_ADMIN"
	ErrInvalidRole            APIErrorCode = "INVALID_ROLE"
	ErrPasswordChangeRequired APIErrorCode = "PASSWORD_CHANGE_REQUIRED"

	// Connection profiles
	ErrProfileNotFound        APIErrorCode = "PROFILE_NOT_FOUND"
	ErrProfileNameExists      APIErrorCode = "PROFILE_NAME_EXISTS"
	ErrProfileInvalidProvider APIErrorCode = "PROFILE_INVALID_PROVIDER"
	ErrProfileURLRequired     APIErrorCode = "PROFILE_URL_REQUIRED"
)

// writeError emits a structured error response carrying only a machine-readable
// code. It deliberately omits any localized message (the frontend translates).
func writeError(w http.ResponseWriter, status int, code APIErrorCode) {
	writeJSON(w, status, map[string]any{"success": false, "error_code": string(code)})
}

func writeValidationError(w http.ResponseWriter, code APIErrorCode) {
	writeError(w, http.StatusBadRequest, code)
}

func writeConflictError(w http.ResponseWriter, code APIErrorCode) {
	writeError(w, http.StatusConflict, code)
}

// writeAudit appends an audit-log entry for the current request. actor is the
// acting user id (may be empty for anonymous/failed events). details is an
// optional structured payload; pass nil for none. Audit failures are non-fatal.
func (s *APIServer) writeAudit(r *http.Request, action db.AuditAction, target string, actor string, details map[string]interface{}) {
	var uid sql.NullString
	if actor != "" {
		uid = sql.NullString{String: actor, Valid: true}
	}
	var d json.RawMessage
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			d = b
		}
	}
	db.WriteAuditLog(s.db, db.AuditEntry{
		UserID:  uid,
		Action:  action,
		Target:  target,
		IP:      s.clientIP(r),
		Details: d,
	})
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *APIServer) getRedirectURI(r *http.Request) string {
	// 1. Full override (highest precedence).
	if envRedirect := os.Getenv("OAUTH_REDIRECT_URI"); envRedirect != "" {
		return envRedirect
	}

	// 2. Base URL configured by the operator. Deriving the callback from a
	// trusted, operator-controlled value (rather than the client-supplied
	// Host header) prevents an attacker from steering the OAuth provider's
	// redirect toward a host they control.
	if envBase := os.Getenv("OAUTH_PUBLIC_BASE_URL"); envBase != "" {
		// Honour an explicit scheme in the configured base URL; default to https.
		scheme := "https"
		if strings.HasPrefix(envBase, "http://") {
			scheme = "http"
		}
		envBase = strings.TrimPrefix(strings.TrimPrefix(envBase, "https://"), "http://")
		envBase = strings.TrimRight(envBase, "/")
		return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, envBase)
	}

	// 3. Fallback to the request Host (acceptable only when the API is not
	// fronted by a reverse proxy; the Host header is client-controlled).
	scheme := "http"
	if s.isSecure(r) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, r.Host)
}

// handleOAuthAuth handles the OAuth authorization redirect.
func (s *APIServer) handleOAuthAuth(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	log.Printf("handleOAuthAuth: Hit with provider=%q", provider)

	if provider == "" {
		writeError(w, http.StatusBadRequest, ErrOauthProviderMissing)
		return
	}

	origin := r.URL.Query().Get("origin")
	log.Printf("handleOAuthAuth: origin query param=%q", origin)
	if origin == "" {
		if referer := r.Header.Get("Referer"); referer != "" {
			if parsed, err := url.Parse(referer); err == nil {
				origin = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
			}
		}
	}
	if origin == "" {
		log.Printf("handleOAuthAuth: rejected request with no determinable origin")
		writeError(w, http.StatusBadRequest, ErrOauthOriginMissing)
		return
	}
	// Validate origin is an absolute URL with a recognised scheme (no wildcard)
	if parsedOrigin, err := url.Parse(origin); err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") {
		log.Printf("handleOAuthAuth: rejected invalid origin %q", origin)
		writeError(w, http.StatusBadRequest, ErrOauthOriginInvalid)
		return
	}
	// Check against allowedOrigins whitelist (C1 security fix)
	if !allowedOrigins[origin] {
		log.Printf("handleOAuthAuth: rejected untrusted origin %q", origin)
		writeError(w, http.StatusBadRequest, ErrOauthOriginUntrusted)
		return
	}
	log.Printf("handleOAuthAuth: final origin set to %q", origin)

	purpose := r.URL.Query().Get("purpose")
	if purpose == "" {
		purpose = "login"
	}

	stateToken := generateRandomString(16)
	if stateToken == "" {
		log.Printf("handleOAuthAuth: Failed to generate state token")
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	isSecure := s.isSecure(r)
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	cookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    stateToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
		MaxAge:   300,
	}
	http.SetCookie(w, cookie)

	stateParam := fmt.Sprintf("%s:%s:%s:%s", stateToken, provider, purpose, origin)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthAuth: constructing authURL with redirectURI=%s", redirectURI)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		log.Printf("handleOAuthAuth: GetAuthURL failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	log.Printf("handleOAuthAuth: Redirecting user to %s", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (s *APIServer) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	log.Printf("handleOAuthCallback: Received request with code length %d, state: %q", len(code), state)

	if code == "" || state == "" {
		log.Printf("handleOAuthCallback: Missing code or state")
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", "Authorization code or state missing")
		return
	}

	parts := strings.SplitN(state, ":", 4)
	if len(parts) < 3 {
		log.Printf("handleOAuthCallback: Invalid state format (length %d)", len(parts))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", "Invalid state parameter format")
		return
	}
	stateToken := parts[0]
	provider := parts[1]
	origin := parts[len(parts)-1]
	purpose := "login"
	if len(parts) >= 4 {
		purpose = parts[2]
	}

	log.Printf("handleOAuthCallback: parsed provider=%s, origin=%s, purpose=%s", provider, origin, purpose)

	if !allowedOrigins[origin] {
		log.Printf("handleOAuthCallback: rejected untrusted origin %q in state", origin)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, "CSRF verification failed: untrusted origin")
		return
	}

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		log.Printf("handleOAuthCallback: CSRF check failed. Cookie err: %v, stateToken: %q", err, stateToken)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, "CSRF verification failed: state mismatch")
		return
	}

	isSecure := s.isSecure(r)
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	clearCookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
		MaxAge:   -1,
	}
	http.SetCookie(w, clearCookie)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthCallback: using redirectURI=%s", redirectURI)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	log.Printf("handleOAuthCallback: exchanging code for provider %s...", provider)
	tokenResp, err := oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		log.Printf("handleOAuthCallback: ExchangeCode failed: %v", err)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, fmt.Sprintf("Failed to exchange code: %v", err))
		return
	}

	log.Printf("handleOAuthCallback: token exchange successful. Fetching user info...")
	username, err := oauth.GetUserInfo(ctx, provider, tokenResp.AccessToken)
	if err != nil {
		log.Printf("handleOAuthCallback: GetUserInfo failed (defaulting to OAuth User): %v", err)
		username = "OAuth User"
	}

	log.Printf("handleOAuthCallback: rendering successful login for user %q", username)
	s.renderOAuthResultHTML(w, provider, tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, username, purpose, origin)
}

// stripScriptTerminator removes sequences that could prematurely close or open
// a <script> element when a value is reflected into an inline script via %q
// (which does not escape '<' or '>'). Case-insensitive to defeat casing tricks.
func stripScriptTerminator(s string) string {
	replacer := strings.NewReplacer(
		"<script", "&#60;script",
		"</script", "&#60;/script",
		"<\\/script", "&#60;\\/script",
	)
	return replacer.Replace(s)
}

func (s *APIServer) renderOAuthResultHTML(w http.ResponseWriter, provider, token, refreshToken string, expiresIn int, username, purpose, targetOrigin string, errorMsg ...string) {
	// Sanitize any value that is reflected into the inline <script> below.
	// Go's %q escapes quotes and backslashes but NOT '<' or '>', so a
	// malicious/compromised OAuth provider returning "</script>" in an error
	// or username string could otherwise terminate the script element (L-2).
	provider = stripScriptTerminator(provider)
	token = stripScriptTerminator(token)
	refreshToken = stripScriptTerminator(refreshToken)
	username = stripScriptTerminator(username)

	var errStr string
	if len(errorMsg) > 0 {
		errStr = stripScriptTerminator(errorMsg[0])
	}

	// Generate a CSP nonce and lock script execution to it. The inline script
	// below is the only script on this page, so this prevents injection of
	// arbitrary scripts even if a value were to be reflected unsafely.
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var script string
	if errStr != "" {
		script = fmt.Sprintf(`
			console.log("OAuth error occurred:", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null on error page!");
				} else {
					window.opener.postMessage({
						type: "oauth-error",
						error: %q
					}, %q);
				}
			} catch (e) {
				console.error("Failed to post oauth-error:", e);
			}
			// Don't close immediately so the user can read the error if it fails to post
			setTimeout(() => { window.close(); }, 1000);
		`, errStr, errStr, targetOrigin)
	} else {
		script = fmt.Sprintf(`
			console.log("OAuth successful. Sending credentials to opener at", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null!");
					var errMsg = document.createElement("p");
					errMsg.style.color = "red";
					errMsg.style.fontWeight = "bold";
					errMsg.style.marginTop = "15px";
					errMsg.innerText = "Fehler: window.opener ist null. Bitte überprüfe deine Browser-Sicherheitseinstellungen (z.B. Pop-up-Blocker oder Brave Shields).";
					document.querySelector(".card").appendChild(errMsg);
				} else {
					window.opener.postMessage({
						type: "oauth-success",
						provider: %q,
						purpose: %q,
						token: %q,
						refreshToken: %q,
						expiresIn: %d,
						username: %q
					}, %q);
					console.log("postMessage sent successfully.");
					window.close();
				}
			} catch (e) {
				console.error("Failed to post oauth-success:", e);
				var errMsg = document.createElement("p");
				errMsg.style.color = "red";
				errMsg.innerText = "Fehler beim Senden der Anmeldedaten: " + e.message;
				document.querySelector(".card").appendChild(errMsg);
			}
		`, targetOrigin, provider, purpose, token, refreshToken, expiresIn, username, targetOrigin)
	}

	fmt.Fprintf(w, `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Authorization Status</title>
			<style>
				body {
					font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
					display: flex;
					align-items: center;
					justify-content: center;
					height: 100vh;
					margin: 0;
					background-color: #f8fafc;
					color: #334155;
				}
				.card {
					background: white;
					padding: 2rem;
					border-radius: 8px;
					box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1);
					text-align: center;
				}
			</style>
		</head>
		<body>
			<div class="card">
				%s
			</div>
			<script nonce="%s">%s</script>
		</body>
		</html>
	`, func() string {
		if errStr != "" {
			return fmt.Sprintf("<h3 style='color: #ef4444;'>Authorization Failed</h3><p>%s</p>", html.EscapeString(errStr))
		}
		return "<h3>Authorization Successful</h3><p>You can close this window now.</p>"
	}(), nonce, script)
}

func hashToken(token string) string {
	h := sha256.New()
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	regEnabled, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("Register error: failed to check registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if regEnabled == "false" {
		writeError(w, http.StatusForbidden, ErrRegistrationDisabled)
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	// Enforce the same password policy as change/reset (M-1): signup must not
	// be the weakest entry point. The actual strength is bounded by bcrypt; we
	// reject trivially short passwords here.
	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	// Validate email shape so we do not persist malformed addresses that would
	// later break notification/reset flows. Reject display-name forms
	// (e.g. `"Foo" <a@b.com>`) and normalise to the bare addr-spec, so the
	// stored value is always a plain address usable by SMTP/reset flows.
	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != strings.TrimSpace(req.Email) {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}
	req.Email = addr.Address

	// Hash the password up front so both the "already registered" and the
	// "freshly created" paths perform the (dominant) bcrypt work. This removes
	// the timing side-channel that would otherwise distinguish the two cases.
	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Anti-enumeration: we must not reveal whether an email is already
	// registered. Both "already exists" and "freshly created" return the same
	// generic 200 response, so the endpoint cannot be used to enumerate
	// accounts. (A 409-vs-201 distinction, or any body difference, would be an
	// oracle.) The frontend simply switches to the login view on success.
	if _, err := db.GetUserByEmail(s.db, req.Email); err == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	} else if err != sql.ErrNoRows {
		log.Printf("Error checking existing user for %s: %v\n", req.Email, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Create user. A unique-violation here means a concurrent registration won
	// the race; treat it identically to "already exists" (generic success) to
	// stay non-enumerable.
	u, err := db.CreateUser(s.db, req.Email, passHash, req.DisplayName)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("Register error: failed to create user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditRegistration, req.Email, u.ID, map[string]interface{}{"email": req.Email})

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *APIServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), loginRateLimit, loginRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			s.writeAudit(r, db.AuditLoginFailed, req.Email, "", map[string]interface{}{"reason": "no_such_user"})
			writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	// Reject logins for soft-deactivated (suspended) accounts. This is an
	// intentional account-enumeration oracle (RISK accepted in plan) so the
	// admin UX can clearly distinguish "disabled" from "bad password".
	if !u.Active {
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "disabled"})
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	}

	// Reject outright if the account is temporarily locked from failed logins.
	if u.LoginLockedUntil.Valid && time.Now().Before(u.LoginLockedUntil.Time) {
		retryAfter := int(time.Until(u.LoginLockedUntil.Time).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	if !auth.CheckPasswordHash(req.Password, u.PasswordHash) {
		// Lockout accounting is keyed by the victim's email, not the
		// attacker's IP, so a bad actor who knows a user's address can
		// trigger a denial-of-service lockout. The per-IP login rate limit
		// (loginRateLimit, applied at the top of this handler) bounds how
		// fast any single source can attempt this; operators should also
		// monitor the warning below for signs of a distributed lockout
		// attack and consider a CAPTCHA / proof-of-work step.
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "bad_password"})
		locked, lerr := db.IncrementLoginFailed(s.db, u.ID, loginMaxAttempts, loginLockDuration)
		if lerr != nil {
			log.Printf("Login error: failed to record failed attempt for user %s: %v\n", u.ID, lerr)
		}
		if locked {
			log.Printf("Security: account %s locked for %v after reaching %d failed login attempts (source IP %s)",
				u.ID, loginLockDuration, loginMaxAttempts, s.clientIP(r))
			w.Header().Set("Retry-After", strconv.Itoa(int(loginLockDuration.Seconds())))
			writeError(w, http.StatusTooManyRequests, ErrRateLimited)
			return
		}
		writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		return
	}

	// Successful credential check: clear any failed-login lockout state.
	if err := db.ResetLoginFailed(s.db, u.ID); err != nil {
		log.Printf("Login error: failed to reset failed attempts for user %s: %v\n", u.ID, err)
	}

	s.writeAudit(r, db.AuditLoginSuccess, req.Email, u.ID, nil)

	// Forced password rotation: a freshly provisioned/bootstrap account must
	// change its password before any other access. Issue a short-lived
	// must-change token and let the client show the rotation form. The token
	// is rejected by every protected route (RequireAuthenticated) until rotated.
	if u.MustChangePassword {
		mustToken, err := auth.GenerateMustChangePasswordToken(u, s.jwtSecret)
		if err != nil {
			log.Printf("Login error: failed to generate must-change token for user %s: %v\n", u.ID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"must_change_password": true,
			"temp_session":         mustToken,
		})
		return
	}

	// If 2FA is enabled, issue only a short-lived temp token and require a
	// second factor at /api/auth/totp. No access/refresh tokens are issued yet.
	if u.TotpEnabled {
		tempToken, err := auth.Generate2FATempToken(u, s.jwtSecret)
		if err != nil {
			log.Printf("Login error: failed to generate 2FA temp token for user %s: %v\n", u.ID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"totp_required": true,
			"temp_session":  tempToken,
		})
		return
	}

	// No 2FA: issue access + refresh tokens directly.
	s.issueTokens(w, r, u)
}

func (s *APIServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenMissing)
		return
	}

	oldTokenHash := hashToken(cookie.Value)
	userID, err := db.GetUserIDByRefreshToken(s.db, oldTokenHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Rotate refresh token atomically using a database transaction
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	defer tx.Rollback()

	deleteQuery := `DELETE FROM refresh_tokens WHERE token_hash = $1`
	if _, err := tx.ExecContext(r.Context(), deleteQuery, oldTokenHash); err != nil {
		log.Printf("Error deleting old refresh token in tx: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	newRefreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)
	newHashedToken := hashToken(newRefreshToken)

	insertQuery := `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	if _, err := tx.ExecContext(r.Context(), insertQuery, newHashedToken, u.ID, newExpiresAt); err != nil {
		log.Printf("Error storing new refresh token in tx: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing token rotation transaction: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	auth.SetRefreshTokenCookie(w, r, newRefreshToken, newExpiresAt, s.isSecure(r))

	// New Access Token
	accessToken, err := auth.GenerateAccessToken(u, s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": accessToken,
	})
}

func (s *APIServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		tokenHash := hashToken(cookie.Value)
		_ = db.DeleteRefreshToken(s.db, tokenHash)
	}

	auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// RunOAuthRotationDaemon is the PRD-12 background goroutine.
// It scans every 5 minutes for active migrations whose OAuth access token expires
// within the next 15 minutes and proactively refreshes them using the stored
// refresh token, ensuring long-running jobs never hit a 401.
func (s *APIServer) RunOAuthRotationDaemon(ctx context.Context) {
	log.Println("[OAuthDaemon] Started. Scanning every 5 minutes for expiring tokens...")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[OAuthDaemon] Shutting down.")
			return
		case <-ticker.C:
			s.rotateExpiringOAuthTokens(ctx)
		}
	}
}

func (s *APIServer) rotateExpiringOAuthTokens(ctx context.Context) {
	expiring, err := db.GetExpiringOAuthMigrations(s.db)
	if err != nil {
		log.Printf("[OAuthDaemon] Error querying expiring tokens: %v\n", err)
		return
	}

	for _, entry := range expiring {
		// Decrypt refresh token — happens immediately before the HTTP call (Zero Plaintext rule)
		refreshToken, err := crypto.Decrypt(entry.RefreshTokenEncrypted, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to decrypt refresh token for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		tokenResp, err := oauth.RefreshToken(refreshCtx, entry.Provider, refreshToken)
		cancel()

		if err != nil {
			log.Printf("[OAuthDaemon] Refresh failed for migration %s (%s provider=%s): %v — marking INVALID\n",
				entry.MigrationID, entry.Role, entry.Provider, err)
			// F-05: mark connection as invalid so workers stop retrying
			errMsg := fmt.Sprintf("OAuth token refresh failed (%s): %v", entry.Provider, err)
			_ = db.UpdateMigrationStatus(s.db, entry.MigrationID, "FAILED", &errMsg)
			continue
		}

		// Encrypt new tokens immediately after receipt (Zero Plaintext rule)
		newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new access token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}
		newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new refresh token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}

		expiresIn := tokenResp.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

		// Atomically overwrite old tokens (Token Rotation Constraint F-03)
		err = db.UpdateMigrationOAuthTokens(s.db, db.OAuthTokenUpdate{
			MigrationID:           entry.MigrationID,
			Role:                  entry.Role,
			AccessTokenEncrypted:  newAccessEnc,
			RefreshTokenEncrypted: newRefreshEnc,
			ExpiresAt:             newExpiresAt,
		})
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to persist new tokens for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		log.Printf("[OAuthDaemon] Successfully rotated %s OAuth token for migration %s (provider=%s, new_expires_at=%s)\n",
			entry.Role, entry.MigrationID, entry.Provider, newExpiresAt.Format(time.RFC3339))
	}
}
func (s *APIServer) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleMe: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	resp := userResponse(u)
	writeJSON(w, http.StatusOK, resp)
}

// userResponse builds the client-facing user object shared by all auth endpoints
// (login, 2FA verification, and /api/auth/me) so the shape cannot drift.
func userResponse(u *db.User) map[string]interface{} {
	resp := map[string]interface{}{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"totp_enabled": u.TotpEnabled,
	}
	if len(u.Avatar) > 0 {
		resp["avatar"] = avatarDataURL(u)
	}
	return resp
}

func avatarDataURL(u *db.User) string {
	if len(u.Avatar) == 0 {
		return ""
	}
	mime := u.AvatarMime
	if mime == "" {
		mime = "image/png"
	}
	encoded := base64.StdEncoding.EncodeToString(u.Avatar)
	return fmt.Sprintf("data:%s;base64,%s", mime, encoded)
}

type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrDisplayNameRequired)
		return
	}

	if err := db.UpdateUserDisplayName(s.db, userID, req.DisplayName); err != nil {
		log.Printf("handleUpdateProfile: failed to update display name: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "display_name": req.DisplayName})
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *APIServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	userID := claims.UserID
	mustChange := claims.MustChangePassword

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.NewPassword != req.ConfirmPassword {
		writeError(w, http.StatusBadRequest, ErrPasswordMismatch)
		return
	}

	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleChangePassword: user not found: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// For a forced rotation (must-change token) the current password is not
	// known/required; for a normal change the current password must verify.
	if !mustChange {
		if !auth.CheckPasswordHash(req.CurrentPassword, u.PasswordHash) {
			writeError(w, http.StatusUnauthorized, ErrPasswordInvalid)
			return
		}
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("handleChangePassword: hash error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Clear the forced-rotation flag together with the password.
	if _, err := s.db.Exec(`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, newHash, userID); err != nil {
		log.Printf("handleChangePassword: update error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Revoke all existing sessions so a previously compromised token can no
	// longer mint new access tokens after the password changed.
	if err := db.DeleteAllRefreshTokensForUser(s.db, userID); err != nil {
		log.Printf("handleChangePassword: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	s.writeAudit(r, db.AuditSettingUpdated, "password", userID, map[string]interface{}{"type": "password_change", "forced": mustChange})

	// If this completed a forced rotation, issue a fresh full-auth JWT so the
	// client can proceed without re-logging in.
	if mustChange {
		rotated, lerr := db.GetUserByID(s.db, userID)
		if lerr != nil {
			log.Printf("handleChangePassword: failed to load user for token rotation: %v\n", lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		rotated.MustChangePassword = false
		accessToken, terr := auth.GenerateAccessToken(rotated, s.jwtSecret)
		if terr != nil {
			log.Printf("handleChangePassword: failed to issue rotated token: %v\n", terr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":      true,
			"access_token": accessToken,
			"user":         userResponse(rotated),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type SetAvatarRequest struct {
	Avatar string `json:"avatar"`
}

func (s *APIServer) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req SetAvatarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if !strings.HasPrefix(req.Avatar, "data:") {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	parts := strings.SplitN(req.Avatar, ",", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	header := parts[0]
	payload := parts[1]

	if !strings.HasSuffix(header, ";base64") {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	validMimes := map[string]bool{
		"image/png":  true,
		"image/jpeg": true,
		"image/webp": true,
		"image/gif":  true,
	}
	if !validMimes[mime] {
		writeError(w, http.StatusBadRequest, ErrAvatarTypeUnsupported)
		return
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	if len(data) > 2*1024*1024 {
		writeError(w, http.StatusBadRequest, ErrAvatarTooLarge)
		return
	}

	if err := db.UpdateUserAvatar(s.db, userID, data, mime); err != nil {
		log.Printf("handleSetAvatar: failed to update avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"avatar":  req.Avatar,
	})
}

func (s *APIServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	if err := db.DeleteUserAvatar(s.db, userID); err != nil {
		log.Printf("handleDeleteAvatar: failed to delete avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	val, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("handleGetSettings: failed to fetch registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if val == "" {
		val = "true"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"registrations_enabled": val,
		"local_storage_enabled": os.Getenv("LOCAL_STORAGE_ROOT") != "",
		"oauth_providers":       oauth.ConfiguredProviders(),
	})
}

type UpdateSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *APIServer) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	if claims.Role != "ADMIN" {
		writeError(w, http.StatusForbidden, ErrAdminOnly)
		return
	}

	var req UpdateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Key != "registrations_enabled" {
		writeError(w, http.StatusForbidden, ErrSettingForbidden)
		return
	}

	if req.Value != "true" && req.Value != "false" {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}

	if err := db.SetSetting(s.db, req.Key, req.Value); err != nil {
		log.Printf("handleUpdateSetting: failed to set setting: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSettingUpdated, req.Key, claims.UserID, map[string]interface{}{"value": req.Value})

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	list, err := db.GetMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("Error listing migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleMigrationStream pushes the user's migration list over Server-Sent Events.
// It sends an initial snapshot once, then re-polls every 3 seconds and only emits
// a new event when the marshaled payload actually changes (diff-on-change), keeping
// idle traffic to a keepalive comment every 20 seconds.
func (s *APIServer) handleMigrationStream(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())

	// Rate-limit connection attempts to prevent stream flooding / abuse.
	if !s.rateLimiter.Allow(s.clientIP(r), streamRateLimit, streamRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	// Cap concurrent streams per user so a single client cannot open unlimited
	// long-lived DB-polling goroutines.
	s.streamMu.Lock()
	if s.activeStreams[userID] >= maxStreamsPerUser {
		s.streamMu.Unlock()
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	s.activeStreams[userID]++
	s.streamMu.Unlock()
	defer func() {
		s.streamMu.Lock()
		s.activeStreams[userID]--
		if s.activeStreams[userID] <= 0 {
			delete(s.activeStreams, userID)
		}
		s.streamMu.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// The server enforces a global WriteTimeout that would otherwise kill this
	// long-lived response after 60s. Disable the write deadline for this
	// connection (ReadTimeout still protects the request read).
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(payload []byte) error {
		if _, err := fmt.Fprintf(w, "event: migrations\ndata: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	writeErrorEvent := func(code APIErrorCode) error {
		if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", code); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	// Initial snapshot
	initial, err := db.GetMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("Migration stream initial load error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	prev, err := json.Marshal(initial)
	if err != nil {
		log.Printf("Migration stream initial marshal error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	if err := writeEvent(prev); err != nil {
		log.Printf("Migration stream initial write error for user %s: %v\n", userID, err)
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	keepaliveTicker := time.NewTicker(20 * time.Second)
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepaliveTicker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			list, err := db.GetMigrationsForUser(s.db, userID)
			if err != nil {
				log.Printf("Migration stream reload error for user %s: %v\n", userID, err)
				return
			}
			cur, err := json.Marshal(list)
			if err != nil {
				log.Printf("Migration stream marshal error for user %s: %v\n", userID, err)
				return
			}
			if !bytes.Equal(cur, prev) {
				if err := writeEvent(cur); err != nil {
					log.Printf("Migration stream write error for user %s: %v\n", userID, err)
					return
				}
				prev = cur
			}
		}
	}
}

func (s *APIServer) handleDeleteMigration(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	// Verify ownership
	owned, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !owned {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	// Cascade delete migration and associated schedules
	err = db.DeleteMigrationCascade(s.db, id)
	if err != nil {
		log.Printf("Error deleting migration %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Delete any schedules associated with this migration
	err = db.DeleteSchedulesForTask(s.db, "migration", id)
	if err != nil {
		log.Printf("Warning: failed to delete schedules for migration %s: %v\n", id, err)
		// Non-fatal: schedules will become orphaned but won't cause issues
	}

	s.writeAudit(r, db.AuditMigrationDeleted, id, userID, nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// Admin User-Management & Oversight Handlers (all ADMIN-only)
// ============================================================================

// adminActorID returns the caller's user id and rejects non-ADMIN callers.
func (s *APIServer) adminActorID(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil || claims.Role != "ADMIN" {
		writeError(w, http.StatusForbidden, ErrAdminOnly)
		return "", false
	}
	return claims.UserID, true
}

// lastActiveAdminCheck reports whether removing/downgrading the given user would
// leave zero active ADMIN accounts (which would lock the instance out of admin).
func (s *APIServer) wouldRemoveLastActiveAdmin(targetID string) (bool, error) {
	u, err := db.GetUserByID(s.db, targetID)
	if err != nil {
		return false, err
	}
	if u.Role != "ADMIN" || !u.Active {
		return false, nil
	}
	count, err := db.CountActiveAdmins(s.db)
	if err != nil {
		return false, err
	}
	return count <= 1, nil
}

type AdminCreateUserRequest struct {
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	Password           string `json:"password"`
	Role               string `json:"role"`
	MustChangePassword *bool  `json:"must_change_password"`
}

func (s *APIServer) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	if !s.rateLimiter.Allow(s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req AdminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}
	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}
	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != strings.TrimSpace(req.Email) {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}
	req.Email = addr.Address

	role := req.Role
	if role == "" {
		role = "USER"
	}
	if !db.ValidRoles[role] {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	mustChange := true
	if req.MustChangePassword != nil {
		mustChange = *req.MustChangePassword
	}

	// Anti-enumeration: a duplicate email returns a generic success so the
	// endpoint cannot be used to confirm which addresses are registered.
	if _, err := db.GetUserByEmail(s.db, req.Email); err == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	} else if err != sql.ErrNoRows {
		log.Printf("Admin create user: failed to check existing user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUserWithRole(s.db, req.Email, passHash, req.DisplayName, role, mustChange)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("Admin create user: failed to create user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserCreated, u.ID, actor, map[string]interface{}{
		"email":                req.Email,
		"role":                 role,
		"must_change_password": mustChange,
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":              true,
		"id":                   u.ID,
		"email":                req.Email,
		"role":                 role,
		"active":               true,
		"must_change_password": mustChange,
	})
}

func (s *APIServer) handleAdminSuspendUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}
	if id == actor {
		writeConflictError(w, ErrCannotModifySelf)
		return
	}

	if _, err := db.GetUserByID(s.db, id); err != nil {
		writeError(w, http.StatusNotFound, ErrUserNotFound)
		return
	}

	if err := db.UpdateUserActive(s.db, id, false); err != nil {
		log.Printf("Admin suspend user %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserSuspended, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleAdminReactivateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}
	if id == actor {
		writeConflictError(w, ErrCannotModifySelf)
		return
	}

	if _, err := db.GetUserByID(s.db, id); err != nil {
		writeError(w, http.StatusNotFound, ErrUserNotFound)
		return
	}

	if err := db.UpdateUserActive(s.db, id, true); err != nil {
		log.Printf("Admin reactivate user %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserReactivated, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}
	if id == actor {
		writeConflictError(w, ErrCannotModifySelf)
		return
	}

	target, err := db.GetUserByID(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrUserNotFound)
		return
	}

	last, err := s.wouldRemoveLastActiveAdmin(id)
	if err != nil {
		log.Printf("Admin delete user %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if last {
		writeConflictError(w, ErrLastAdmin)
		return
	}

	// Capture identifying info for the audit before the cascade wipes it.
	targetEmail := target.Email
	if err := db.DeleteUser(s.db, id); err != nil {
		log.Printf("Admin delete user %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserDeleted, id, actor, map[string]interface{}{"email": targetEmail})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type AdminUpdateRoleRequest struct {
	Role string `json:"role"`
}

func (s *APIServer) handleAdminUpdateRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}
	if id == actor {
		writeConflictError(w, ErrCannotModifySelf)
		return
	}

	var req AdminUpdateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	if !db.ValidRoles[req.Role] {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	target, err := db.GetUserByID(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrUserNotFound)
		return
	}

	// Downgrading the last active admin would lock everyone out of admin.
	if target.Role == "ADMIN" && req.Role != "ADMIN" {
		last, lerr := s.wouldRemoveLastActiveAdmin(id)
		if lerr != nil {
			log.Printf("Admin role change %s: %v\n", id, lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if last {
			writeConflictError(w, ErrLastAdmin)
			return
		}
	}

	if err := db.UpdateUserRole(s.db, id, req.Role); err != nil {
		log.Printf("Admin role change %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserRoleChanged, id, actor, map[string]interface{}{
		"from": target.Role,
		"to":   req.Role,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "role": req.Role})
}

func (s *APIServer) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	role := q.Get("role")
	var active *bool
	if v := q.Get("active"); v != "" {
		b := v == "true" || v == "1"
		active = &b
	}
	search := strings.TrimSpace(q.Get("q"))

	users, total, err := db.ListUsers(s.db, db.UserListParams{
		Page:   page,
		Limit:  limit,
		Role:   role,
		Active: active,
		Query:  search,
	})
	if err != nil {
		log.Printf("Admin list users: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users": users,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func (s *APIServer) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	stats, err := db.GetGlobalStats(s.db)
	if err != nil {
		log.Printf("Admin stats: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *APIServer) handleAdminListMigrations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	migrations, total, err := db.ListAllMigrations(s.db, db.MigrationListParams{Page: page, Limit: limit})
	if err != nil {
		log.Printf("Admin list migrations: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"migrations": migrations,
		"total":      total,
		"page":       page,
		"limit":      limit,
	})
}

func (s *APIServer) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	entries, total, err := db.ListAuditLog(s.db, db.AuditLogParams{
		Page:   page,
		Limit:  limit,
		Action: q.Get("action"),
		UserID: q.Get("user_id"),
		Target: q.Get("target"),
		From:   q.Get("from"),
		To:     q.Get("to"),
	})
	if err != nil {
		log.Printf("Admin audit log: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   total,
		"page":    page,
		"limit":   limit,
	})
}

// ============================================================================
// Schedule Management Handlers
// ============================================================================

// handleListSchedules returns all schedules for the authenticated user
func (s *APIServer) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	schedules, err := db.GetSchedulesForUser(s.db, userID)
	if err != nil {
		log.Printf("handleListSchedules: failed to get schedules for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Return empty array instead of null if no schedules
	if schedules == nil {
		schedules = []db.Schedule{}
	}

	writeJSON(w, http.StatusOK, schedules)
}

// handleGetSchedule returns a specific schedule if owned by the user
func (s *APIServer) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrScheduleIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	// Verify ownership. VerifyScheduleOwnership uses EXISTS, so it never returns
	// sql.ErrNoRows. A non-owning result means the schedule either does not exist
	// or belongs to another user — return 404 in both cases to avoid leaking
	// existence/ownership information.
	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleGetSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}

	schedule, err := db.GetSchedule(s.db, id)
	if err != nil {
		log.Printf("handleGetSchedule: failed to get schedule %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, schedule)
}

// handleDeleteSchedule deletes a schedule if owned by the user
func (s *APIServer) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrScheduleIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	// Verify ownership. VerifyScheduleOwnership uses EXISTS, so it never returns
	// sql.ErrNoRows. A non-owning result means the schedule either does not exist
	// or belongs to another user — return 404 in both cases to avoid leaking
	// existence/ownership information.
	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleDeleteSchedule: error verifying ownership: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owns {
		writeError(w, http.StatusNotFound, ErrScheduleNotFound)
		return
	}

	err = db.DeleteSchedule(s.db, id)
	if err != nil {
		log.Printf("handleDeleteSchedule: failed to delete schedule %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// SMTP Settings Handlers
// ============================================================================

func (s *APIServer) handleGetSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		log.Printf("handleGetSMTPSettings: error fetching settings: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"smtp_host":            settings.SMTPHost,
		"smtp_port":            settings.SMTPPort,
		"smtp_username":        settings.SMTPUsername,
		"smtp_password_set":    true,
		"smtp_from_email":      settings.SMTPFromEmail,
		"smtp_from_name":       settings.SMTPFromName,
		"smtp_encryption":      settings.SMTPEncryption,
		"notify_on_completion": settings.NotifyOnCompletion,
	})
}

func (s *APIServer) handleUpdateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req struct {
		SMTPHost           string `json:"smtp_host"`
		SMTPPort           int    `json:"smtp_port"`
		SMTPUsername       string `json:"smtp_username"`
		SMTPPassword       string `json:"smtp_password"`
		PasswordChanged    bool   `json:"password_changed"`
		SMTPFromEmail      string `json:"smtp_from_email"`
		SMTPFromName       string `json:"smtp_from_name"`
		SMTPEncryption     string `json:"smtp_encryption"`
		NotifyOnCompletion *bool  `json:"notify_on_completion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.SMTPHost == "" || req.SMTPUsername == "" || req.SMTPFromEmail == "" {
		writeError(w, http.StatusBadRequest, ErrSmtpConfigIncomplete)
		return
	}

	if err := email.ValidateSMTPHost(req.SMTPHost); err != nil {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}

	if req.SMTPPort < 1 || req.SMTPPort > 65535 {
		writeError(w, http.StatusBadRequest, ErrSmtpPortInvalid)
		return
	}

	switch req.SMTPEncryption {
	case "tls", "starttls", "none":
	default:
		writeError(w, http.StatusBadRequest, ErrSmtpEncryptionInvalid)
		return
	}

	notify := true
	if req.NotifyOnCompletion != nil {
		notify = *req.NotifyOnCompletion
	}

	var encryptedPassword string
	// A password is considered "changed" when the client explicitly flags it
	// or when a non-empty password is supplied in the request body. This handles
	// the common case where the frontend only sends smtp_password on first setup
	// or when the user types a new password, without sending password_changed.
	passwordProvided := req.PasswordChanged || req.SMTPPassword != ""
	if !passwordProvided {
		existing, err := db.GetUserSMTPSettings(s.db, userID)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("handleUpdateSMTPSettings: error fetching existing settings: %v\n", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if existing != nil {
			encryptedPassword = existing.SMTPPasswordEnc
		} else {
			writeError(w, http.StatusBadRequest, ErrSmtpPasswordRequired)
			return
		}
	} else {
		if req.SMTPPassword == "" {
			writeError(w, http.StatusBadRequest, ErrSmtpPasswordRequired)
			return
		}
		enc, err := crypto.Encrypt(req.SMTPPassword, s.encryptionKey)
		if err != nil {
			log.Printf("handleUpdateSMTPSettings: error encrypting password: %v\n", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		encryptedPassword = enc
	}

	settings := &db.UserSMTPSettings{
		UserID:             userID,
		SMTPHost:           req.SMTPHost,
		SMTPPort:           req.SMTPPort,
		SMTPUsername:       req.SMTPUsername,
		SMTPPasswordEnc:    encryptedPassword,
		SMTPFromEmail:      req.SMTPFromEmail,
		SMTPFromName:       req.SMTPFromName,
		SMTPEncryption:     req.SMTPEncryption,
		NotifyOnCompletion: notify,
	}

	if err := db.UpsertUserSMTPSettings(s.db, settings); err != nil {
		log.Printf("handleUpdateSMTPSettings: error upserting settings: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpNotConfigured})
			return
		}
		log.Printf("handleTestSMTP: error fetching settings: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrInternalError})
		return
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, s.encryptionKey)
	if err != nil {
		log.Printf("handleTestSMTP: error decrypting password: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpDecryptFailed})
		return
	}

	user, err := db.GetUserByID(s.db, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMailNotConfigured})
		return
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	if err := email.SendMail(smtpCfg, user.Email, "Clumoove — SMTP-Test erfolgreich", email.BuildTestEmail()); err != nil {
		log.Printf("handleTestSMTP: send failed: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpTestFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// Password Reset Handlers
// ============================================================================

func (s *APIServer) handlePasswordResetAvailable(w http.ResponseWriter, r *http.Request) {
	available := os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM_EMAIL") != ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost == "" || smtpFromEmail == "" {
		log.Printf("handleForgotPassword: SMTP not configured, skipping\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("handleForgotPassword: error fetching user: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	rawToken := generateRandomString(32)
	if rawToken == "" {
		log.Printf("handleForgotPassword: failed to generate token\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}
	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().Add(4 * time.Hour)

	if err := db.CreatePasswordResetToken(s.db, tokenHash, u.ID, expiresAt); err != nil {
		log.Printf("handleForgotPassword: error storing token: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:5173"
	}
	resetURL := fmt.Sprintf("%s/?reset-token=%s", strings.TrimRight(frontendURL, "/"), rawToken)

	smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
	if smtpEncryption == "" {
		smtpEncryption = "starttls"
	}

	smtpCfg := email.SMTPConfig{
		Host:       smtpHost,
		Port:       os.Getenv("SMTP_PORT"),
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		FromEmail:  smtpFromEmail,
		FromName:   os.Getenv("SMTP_FROM_NAME"),
		Encryption: smtpEncryption,
	}
	if smtpCfg.Port == "" {
		smtpCfg.Port = "587"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "Clumoove"
	}

	htmlBody := email.BuildPasswordResetEmail(resetURL)
	if err := email.SendMail(smtpCfg, u.Email, "Clumoove — Passwort zurücksetzen", htmlBody); err != nil {
		log.Printf("handleForgotPassword: error sending email: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	emailHash := sha256.Sum256([]byte(req.Email))
	log.Printf("handleForgotPassword: reset email sent (hash: %x)\n", emailHash[:8])
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Token == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, ErrResetFieldsRequired)
		return
	}

	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("handleResetPassword: error hashing password: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	tokenHash := hashToken(req.Token)
	userID, err := db.ClaimPasswordResetToken(s.db, r.Context(), tokenHash, newHash)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusBadRequest, ErrResetTokenInvalid)
			return
		}
		log.Printf("handleResetPassword: error claiming token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Clear any failed-login lockout so a victim of a lockout-DoS attack can
	// self-service unlock by resetting their password (the attacker cannot
	// satisfy the reset email, so only the real owner reaches this point).
	if err := db.ResetLoginFailed(s.db, userID); err != nil {
		log.Printf("handleResetPassword: failed to clear login lockout for user %s: %v\n", userID, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// Email Change Handlers
// ============================================================================

func (s *APIServer) handleEmailChangeAvailable(w http.ResponseWriter, r *http.Request) {
	available := os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM_EMAIL") != ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())

	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		NewEmail string `json:"new_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	req.NewEmail = strings.TrimSpace(strings.ToLower(req.NewEmail))
	if req.NewEmail == "" || !strings.Contains(req.NewEmail, "@") || !strings.Contains(req.NewEmail, ".") {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleChangeEmail: error fetching user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if req.NewEmail == strings.ToLower(u.Email) {
		writeError(w, http.StatusBadRequest, ErrEmailUnchanged)
		return
	}

	// Ensure the new email is not already taken by another user
	existing, err := db.GetUserByEmail(s.db, req.NewEmail)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("handleChangeEmail: error checking email: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err == nil && existing.ID != userID {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost == "" || smtpFromEmail == "" {
		writeError(w, http.StatusBadRequest, ErrMailNotConfigured)
		return
	}

	rawToken := generateRandomString(32)
	if rawToken == "" {
		log.Printf("handleChangeEmail: failed to generate token\n")
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().Add(4 * time.Hour)

	if err := db.CreateEmailChangeToken(s.db, tokenHash, userID, req.NewEmail, expiresAt); err != nil {
		log.Printf("handleChangeEmail: error storing token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:5173"
	}
	confirmURL := fmt.Sprintf("%s/?email-change-token=%s", strings.TrimRight(frontendURL, "/"), rawToken)

	smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
	if smtpEncryption == "" {
		smtpEncryption = "starttls"
	}

	smtpCfg := email.SMTPConfig{
		Host:       smtpHost,
		Port:       os.Getenv("SMTP_PORT"),
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		FromEmail:  smtpFromEmail,
		FromName:   os.Getenv("SMTP_FROM_NAME"),
		Encryption: smtpEncryption,
	}
	if smtpCfg.Port == "" {
		smtpCfg.Port = "587"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "Clumoove"
	}

	htmlBody := email.BuildEmailChangeEmail(confirmURL, req.NewEmail)
	if err := email.SendMail(smtpCfg, u.Email, "Clumoove — E-Mail-Adresse ändern", htmlBody); err != nil {
		log.Printf("handleChangeEmail: error sending email: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	emailHash := sha256.Sum256([]byte(u.Email))
	log.Printf("handleChangeEmail: confirmation email sent to %x\n", emailHash[:8])
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleConfirmEmailChange(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, ErrEmailChangeTokenInvalid)
		return
	}

	tokenHash := hashToken(req.Token)
	userID, newEmail, err := db.ClaimEmailChangeToken(s.db, r.Context(), tokenHash)
	if err != nil {
		if errors.Is(err, db.ErrEmailTaken) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		if err == sql.ErrNoRows {
			writeError(w, http.StatusBadRequest, ErrEmailChangeTokenInvalid)
			return
		}
		log.Printf("handleConfirmEmailChange: error claiming token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Email changed: revoke all existing sessions so a previously compromised
	// token can no longer mint new access tokens.
	if err := db.DeleteAllRefreshTokensForUser(s.db, userID); err != nil {
		log.Printf("handleConfirmEmailChange: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost != "" && smtpFromEmail != "" {
		smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
		if smtpEncryption == "" {
			smtpEncryption = "starttls"
		}
		smtpCfg := email.SMTPConfig{
			Host:       smtpHost,
			Port:       os.Getenv("SMTP_PORT"),
			Username:   os.Getenv("SMTP_USERNAME"),
			Password:   os.Getenv("SMTP_PASSWORD"),
			FromEmail:  smtpFromEmail,
			FromName:   os.Getenv("SMTP_FROM_NAME"),
			Encryption: smtpEncryption,
		}
		if smtpCfg.Port == "" {
			smtpCfg.Port = "587"
		}
		if smtpCfg.FromName == "" {
			smtpCfg.FromName = "Clumoove"
		}

		htmlBody := email.BuildEmailChangedNotificationEmail(newEmail)
		if err := email.SendMail(smtpCfg, newEmail, "Clumoove — E-Mail-Adresse geändert", htmlBody); err != nil {
			log.Printf("handleConfirmEmailChange: error sending notification to new email (user %s): %v\n", userID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
