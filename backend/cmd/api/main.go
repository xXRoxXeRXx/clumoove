package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"backend/internal/auth"
	"backend/internal/db"
	"backend/internal/indexer"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/scheduler"
	appSync "backend/internal/sync"
)

type APIServer struct {
	db            *sql.DB
	queue         *queue.Queue
	indexer       *indexer.Indexer
	syncEngine    *appSync.Engine
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
	maxStreamsPerUser  = 5

	loginMaxAttempts  = 5
	loginLockDuration = 15 * time.Minute

	maxActiveMigrations = 10
	minPasswordLength   = 12
)

func main() {
	log.Println("Starting Migration API Gateway...")
	oauth.InitConfigs()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
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

	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
	}

	if subtle.ConstantTimeCompare([]byte(encryptionKey), []byte(jwtSecret)) == 1 {
		log.Fatal("ENCRYPTION_SECRET_KEY and JWT_SECRET_KEY must be different to maintain cryptographic key segregation.")
	}

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

	// 2. Initialize Redis Queue
	q, err := queue.NewQueue(redisURL)
	if err != nil {
		log.Fatalf("Failed to initialize Redis queue: %v", err)
	}
	log.Println("Connected to Redis.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trustedProxy := os.Getenv("TRUSTED_PROXY") == "1" ||
		strings.EqualFold(os.Getenv("TRUSTED_PROXY"), "true")

	if !trustedProxy {
		log.Println("WARNING: TRUSTED_PROXY is not set. If the API runs behind a reverse proxy, per-IP rate limiting and lockout accounting will be ineffective (all clients share the proxy's address). Set TRUSTED_PROXY=1 if a trusted proxy sits in front of the API.")
	}

	syncEng := appSync.NewEngine(database, q, encryptionKey)
	server := &APIServer{
		db:            database,
		queue:         q,
		indexer:       indexer.NewIndexer(database, encryptionKey, q),
		syncEngine:    syncEng,
		encryptionKey: encryptionKey,
		jwtSecret:     jwtSecret,
		ctx:           ctx,
		rateLimiter:   ipRateLimiter{visitors: make(map[string]*rateVisitor)},
		activeStreams: make(map[string]int),
		trustedProxy:  trustedProxy,
	}

	mux := http.NewServeMux()

	// Auth Routes (Public)
	mux.HandleFunc("GET /api/auth/setup-status", server.handleGetSetupStatus)
	mux.HandleFunc("POST /api/auth/setup-admin", server.handleSetupAdmin)
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

	// Sync Engine Routes
	mux.Handle("GET /api/sync", jwtMiddleware(http.HandlerFunc(server.handleListSyncs)))
	mux.Handle("GET /api/sync/stream", jwtMiddleware(http.HandlerFunc(server.handleSyncStream)))
	mux.Handle("POST /api/sync", jwtMiddleware(http.HandlerFunc(server.handleCreateSync)))
	mux.Handle("GET /api/sync/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetSyncStatus)))
	mux.Handle("POST /api/sync/{id}/start", jwtMiddleware(http.HandlerFunc(server.handleStartSync)))
	mux.Handle("POST /api/sync/{id}/pause", jwtMiddleware(http.HandlerFunc(server.handlePauseSync)))
	mux.Handle("POST /api/sync/{id}/resume", jwtMiddleware(http.HandlerFunc(server.handleResumeSync)))
	mux.Handle("DELETE /api/sync/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteSync)))
	mux.Handle("GET /api/sync/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadSyncReport)))
	mux.Handle("PUT /api/sync/{id}/threads", jwtMiddleware(http.HandlerFunc(server.handleSetSyncThreads)))

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
	mux.Handle("GET /api/admin/syncs", jwtMiddleware(http.HandlerFunc(server.handleAdminListSyncs)))
	mux.Handle("GET /api/audit/log", jwtMiddleware(http.HandlerFunc(server.handleAdminAuditLog)))

	// WebSockets & OAuth Callbacks (Require custom/token-based verification inside handler)
	mux.HandleFunc("GET /api/migration/{id}/ws", server.handleWebSocket)
	mux.HandleFunc("GET /api/oauth/auth", server.handleOAuthAuth)
	mux.HandleFunc("GET /api/oauth/callback", server.handleOAuthCallback)

	handler := securityHeadersMiddleware(corsMiddleware(mux))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go server.rateLimiter.evictExpired(ctx, 1*time.Minute)
	go server.RunOAuthRotationDaemon(ctx)

	sched := scheduler.NewScheduler(database, q, server.indexer)
	sched.SetSyncEngine(syncEng)
	go sched.Run(ctx)

	go func() {
		log.Printf("API Server listening on port %s\n", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	sig := <-sigChan
	log.Printf("Received signal %v. Shutting down API server...\n", sig)

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("API Server Shutdown Failed:%+v", err)
	}
	log.Println("API Server exited gracefully.")
}
