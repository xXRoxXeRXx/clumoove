package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log/slog"
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
	"backend/internal/observability"
	"backend/internal/queue"
	"backend/internal/scheduler"
	appSync "backend/internal/sync"
)

type rateLimiter interface {
	Allow(context.Context, string, string, int, time.Duration) bool
}

type APIServer struct {
	db            *sql.DB
	queue         *queue.Queue
	indexer       *indexer.Indexer
	syncEngine    *appSync.Engine
	encryptionKey string // AES key for credential encryption
	jwtSecret     string // HMAC key for JWT signing (separate from encryptionKey)
	// backgroundCtx owns long-running work started after a request returns.
	// Request handlers use r.Context() for request-scoped operations.
	backgroundCtx     context.Context
	rateLimiter       rateLimiter
	dummyPasswordHash string
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
	loginRateLimit        = 10
	loginRateWindow       = 1 * time.Minute
	registerRateLimit     = 5
	registerRateWindow    = 5 * time.Minute
	connectRateLimit      = 30
	connectRateWindow     = 1 * time.Minute
	connectTestRateLimit  = 30
	connectTestRateWindow = 1 * time.Minute
	jobMutationRateLimit  = 10
	jobMutationRateWindow = 1 * time.Minute
	totpRateLimit         = 10
	totpRateWindow        = 1 * time.Minute
	streamRateLimit       = 60
	streamRateWindow      = 1 * time.Minute
	maxStreamsPerUser     = 10

	loginMaxAttempts  = 5
	loginLockDuration = 15 * time.Minute

	maxActiveMigrations = 10
	minPasswordLength   = 12
)

func validatePasswordLength(w http.ResponseWriter, password string) bool {
	if len(password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return false
	}
	if len(password) > auth.MaxPasswordBytes {
		writeError(w, http.StatusBadRequest, ErrPasswordTooLong)
		return false
	}
	return true
}

func main() {
	if _, err := observability.Configure("api"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.Info("service_starting", slog.String("component", "api"))

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Warn("database_url_defaulted", slog.String("component", "api"))
		dbURL = "postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=require"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	encryptionKey := os.Getenv("ENCRYPTION_SECRET_KEY")
	if encryptionKey == "" {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", "encryption_key_missing"))
		os.Exit(1)
	}

	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", "jwt_key_missing"))
		os.Exit(1)
	}

	if subtle.ConstantTimeCompare([]byte(encryptionKey), []byte(jwtSecret)) == 1 {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", "keys_equal"))
		os.Exit(1)
	}

	if len(jwtSecret) < 32 {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", "jwt_key_too_short"))
		os.Exit(1)
	}

	// Unknown accounts are checked against this bcrypt hash so their login path
	// takes the same password-verification work as an existing account.
	dummyPasswordHash, err := auth.HashPassword("clumoove-login-timing-placeholder")
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("reason", "timing_protection_init_failed"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// 1. Initialize PostgreSQL
	database, err := db.InitDB(dbURL)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "database"), slog.String("reason", "init_failed"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		os.Exit(1)
	}
	defer database.Close()
	slog.Info("database_connected", slog.String("component", "database"))

	// 1b. Install the administrator-managed OAuth credential loader. Credentials
	// live in instance_oauth_providers and are decrypted only when a token is
	// requested; the cache holds ciphertext only.
	oauth.Configure(oauth.NewDBLoader(database), encryptionKey)

	// 2. Initialize Redis Queue
	q, err := queue.NewQueue(redisURL)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "queue"), slog.String("reason", "init_failed"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		os.Exit(1)
	}
	slog.Info("queue_connected", slog.String("component", "queue"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	trustedProxy := os.Getenv("TRUSTED_PROXY") == "1" ||
		strings.EqualFold(os.Getenv("TRUSTED_PROXY"), "true")

	if !trustedProxy {
		slog.Warn("trusted_proxy_not_configured", slog.String("component", "api"))
	}

	syncEng := appSync.NewEngine(database, q, encryptionKey)
	go syncEng.SubscribeToCancelEvents(ctx)
	server := &APIServer{
		db:                database,
		queue:             q,
		indexer:           indexer.NewIndexer(database, encryptionKey, q),
		syncEngine:        syncEng,
		encryptionKey:     encryptionKey,
		jwtSecret:         jwtSecret,
		backgroundCtx:     ctx,
		rateLimiter:       &distributedRateLimiter{client: q.RedisClient()},
		dummyPasswordHash: dummyPasswordHash,
		activeStreams:     make(map[string]int),
		trustedProxy:      trustedProxy,
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
	jwtMiddleware := auth.AuthMiddleware(server.db, server.jwtSecret)
	adminRouteMiddleware := adminMiddleware(jwtMiddleware)
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleMe)))
	mux.Handle("GET /api/auth/sessions", jwtMiddleware(http.HandlerFunc(server.handleListSessions)))
	mux.Handle("DELETE /api/auth/sessions/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteSession)))
	mux.Handle("PUT /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleUpdateProfile)))
	mux.Handle("PUT /api/auth/me/language", jwtMiddleware(http.HandlerFunc(server.handleUpdateLanguage)))
	mux.Handle("POST /api/auth/change-password", auth.AuthMiddlewareAllowMustChange(server.db, server.jwtSecret)(http.HandlerFunc(server.handleChangePassword)))
	mux.Handle("GET /api/auth/2fa/setup", jwtMiddleware(http.HandlerFunc(server.handle2FASetup)))
	mux.Handle("POST /api/auth/2fa/enable", jwtMiddleware(http.HandlerFunc(server.handle2FAEnable)))
	mux.Handle("POST /api/auth/2fa/disable", jwtMiddleware(http.HandlerFunc(server.handle2FADisable)))
	mux.Handle("GET /api/auth/2fa/status", jwtMiddleware(http.HandlerFunc(server.handle2FAStatus)))
	mux.Handle("POST /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleSetAvatar)))
	mux.Handle("DELETE /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleDeleteAvatar)))
	mux.Handle("PUT /api/settings", adminRouteMiddleware(http.HandlerFunc(server.handleUpdateSetting)))
	mux.Handle("GET /api/settings/notifications", jwtMiddleware(http.HandlerFunc(server.handleGetNotificationSettings)))
	mux.Handle("PUT /api/settings/notifications", jwtMiddleware(http.HandlerFunc(server.handleUpdateNotificationSettings)))
	mux.Handle("POST /api/settings/notifications/test", jwtMiddleware(http.HandlerFunc(server.handleTestNotification)))

	mux.HandleFunc("GET /api/auth/password-reset-available", server.handlePasswordResetAvailable)
	mux.HandleFunc("POST /api/auth/forgot-password", server.handleForgotPassword)
	mux.HandleFunc("POST /api/auth/reset-password", server.handleResetPassword)

	mux.HandleFunc("GET /api/auth/email-change-available", server.handleEmailChangeAvailable)
	mux.Handle("POST /api/auth/change-email", jwtMiddleware(http.HandlerFunc(server.handleChangeEmail)))
	mux.HandleFunc("POST /api/auth/confirm-email-change", server.handleConfirmEmailChange)

	mux.Handle("GET /api/migration", jwtMiddleware(http.HandlerFunc(server.handleListMigrations)))
	mux.Handle("GET /api/migration/stream", jwtMiddleware(http.HandlerFunc(server.handleMigrationStream)))
	mux.Handle("POST /api/migration/connect", jwtMiddleware(http.HandlerFunc(server.handleConnect)))
	mux.Handle("POST /api/migration/connect/test", jwtMiddleware(http.HandlerFunc(server.handleConnectTest)))
	mux.Handle("POST /api/migration/browse", jwtMiddleware(http.HandlerFunc(server.handleBrowse)))
	mux.Handle("POST /api/migration/target/browse", jwtMiddleware(http.HandlerFunc(server.handleTargetBrowse)))
	mux.Handle("POST /api/migration/target/mkdir", jwtMiddleware(http.HandlerFunc(server.handleTargetMkdir)))
	mux.Handle("POST /api/migration/start", jwtMiddleware(http.HandlerFunc(server.handleStart)))
	mux.Handle("GET /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetStatus)))
	mux.Handle("GET /api/migration/{id}/stream", jwtMiddleware(http.HandlerFunc(server.handleMigrationDetailStream)))
	mux.Handle("POST /api/migration/{id}/pause", jwtMiddleware(http.HandlerFunc(server.handlePause)))
	mux.Handle("POST /api/migration/{id}/resume", jwtMiddleware(http.HandlerFunc(server.handleResume)))
	mux.Handle("POST /api/migration/{id}/cancel", jwtMiddleware(http.HandlerFunc(server.handleCancel)))
	mux.Handle("DELETE /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteMigration)))
	mux.Handle("GET /api/migration/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadReport)))
	mux.Handle("GET /api/migration/{id}/errors", jwtMiddleware(http.HandlerFunc(server.handleMigrationErrors)))
	mux.Handle("POST /api/migration/{id}/retry-failed", jwtMiddleware(http.HandlerFunc(server.handleRetryFailed)))
	mux.Handle("POST /api/migration/{id}/reauth", jwtMiddleware(http.HandlerFunc(server.handleMigrationReauth)))
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
	mux.Handle("POST /api/sync/{id}/reauth", jwtMiddleware(http.HandlerFunc(server.handleSyncReauth)))
	mux.Handle("DELETE /api/sync/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteSync)))
	mux.Handle("GET /api/sync/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadSyncReport)))
	mux.Handle("GET /api/sync/{id}/errors", jwtMiddleware(http.HandlerFunc(server.handleSyncErrors)))
	mux.Handle("PUT /api/sync/{id}/threads", jwtMiddleware(http.HandlerFunc(server.handleSetSyncThreads)))
	mux.Handle("PUT /api/sync/{id}/bandwidth", jwtMiddleware(http.HandlerFunc(server.handleSetSyncBandwidth)))
	mux.Handle("GET /api/sync/{id}/browse", jwtMiddleware(http.HandlerFunc(server.handleBrowseSyncJob)))
	mux.Handle("PUT /api/sync/{id}/schedule", jwtMiddleware(http.HandlerFunc(server.handleUpdateSyncSchedule)))
	mux.Handle("PUT /api/sync/{id}/scope", jwtMiddleware(http.HandlerFunc(server.handleUpdateSyncScope)))

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

	// Administrative routes require both a current JWT and the ADMIN role.
	mux.Handle("POST /api/admin/users", adminRouteMiddleware(http.HandlerFunc(server.handleAdminCreateUser)))
	mux.Handle("POST /api/admin/users/{id}/suspend", adminRouteMiddleware(http.HandlerFunc(server.handleAdminSuspendUser)))
	mux.Handle("POST /api/admin/users/{id}/reactivate", adminRouteMiddleware(http.HandlerFunc(server.handleAdminReactivateUser)))
	mux.Handle("DELETE /api/admin/users/{id}", adminRouteMiddleware(http.HandlerFunc(server.handleAdminDeleteUser)))
	mux.Handle("PUT /api/admin/users/{id}/role", adminRouteMiddleware(http.HandlerFunc(server.handleAdminUpdateRole)))
	mux.Handle("GET /api/admin/users", adminRouteMiddleware(http.HandlerFunc(server.handleAdminListUsers)))
	mux.Handle("GET /api/admin/stats", adminRouteMiddleware(http.HandlerFunc(server.handleAdminStats)))
	mux.Handle("GET /api/admin/migrations", adminRouteMiddleware(http.HandlerFunc(server.handleAdminListMigrations)))
	mux.Handle("GET /api/admin/syncs", adminRouteMiddleware(http.HandlerFunc(server.handleAdminListSyncs)))
	mux.Handle("GET /api/admin/settings/smtp", adminRouteMiddleware(http.HandlerFunc(server.handleAdminGetSMTP)))
	mux.Handle("PUT /api/admin/settings/smtp", adminRouteMiddleware(http.HandlerFunc(server.handleAdminPutSMTP)))
	mux.Handle("POST /api/admin/settings/smtp/test", adminRouteMiddleware(http.HandlerFunc(server.handleAdminTestSMTP)))
	mux.Handle("DELETE /api/admin/settings/smtp", adminRouteMiddleware(http.HandlerFunc(server.handleAdminDeleteSMTP)))
	mux.Handle("GET /api/admin/settings/oauth", adminRouteMiddleware(http.HandlerFunc(server.handleAdminGetOAuth)))
	mux.Handle("PUT /api/admin/settings/oauth/{provider}", adminRouteMiddleware(http.HandlerFunc(server.handleAdminPutOAuth)))
	mux.Handle("DELETE /api/admin/settings/oauth/{provider}", adminRouteMiddleware(http.HandlerFunc(server.handleAdminDeleteOAuth)))
	mux.Handle("GET /api/audit/log", adminRouteMiddleware(http.HandlerFunc(server.handleAdminAuditLog)))

	// OAuth callbacks use their own state validation.
	mux.HandleFunc("GET /api/oauth/auth", server.handleOAuthAuth)
	mux.HandleFunc("GET /api/oauth/callback", server.handleOAuthCallback)

	handler := server.requestLogMiddleware(server.securityHeadersMiddleware(corsMiddleware(mux)))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go server.RunOAuthRotationDaemon(ctx)

	sched := scheduler.NewScheduler(database, q, server.indexer)
	sched.SetSyncEngine(syncEng)
	go sched.Run(ctx)
	go sched.RunOrphanedSyncJobRecovery(ctx)

	go func() {
		slog.Info("http_server_listening", slog.String("component", "api"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http_server_failed", slog.String("component", "api"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		}
	}()

	sig := <-sigChan
	slog.Info("shutdown_requested", slog.String("component", "api"), slog.String("signal", sig.String()))

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown_failed", slog.String("component", "api"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	slog.Info("service_stopped", slog.String("component", "api"))
}
