package main

import (
	"context"
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
	"backend/internal/config"
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
	syncPass      appSync.Pass
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
	// providerFactory is nil in production, where storage.NewProvider is used.
	// It lets handler tests exercise provider behavior without external egress.
	providerFactory profileProviderFactory
	// authStateLookup is nil in production, where AuthMiddleware queries PostgreSQL.
	// Tests may inject an in-memory lookup to exercise route access contracts.
	authStateLookup auth.AuthStateLookup
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

	dbURL, databaseURLDefaulted := config.DatabaseURL(os.Getenv("DATABASE_URL"))
	if databaseURLDefaulted {
		slog.Warn("database_url_defaulted", slog.String("component", "api"))
	}

	redisURL, _ := config.RedisURL(os.Getenv("REDIS_URL"))

	encryptionKey := os.Getenv("ENCRYPTION_SECRET_KEY")
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if err := validateGatewayConfig(gatewayConfig{
		EncryptionKey: encryptionKey,
		JWTSecret:     jwtSecret,
	}); err != nil {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", err.Error()))
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
	server, err := newAPIGateway(gatewayConfig{
		DB:                database,
		Queue:             q,
		Indexer:           indexer.NewIndexer(database, encryptionKey, q),
		SyncPass:          syncEng.Pass(),
		EncryptionKey:     encryptionKey,
		JWTSecret:         jwtSecret,
		BackgroundCtx:     ctx,
		RateLimiter:       &distributedRateLimiter{client: q.RedisClient()},
		DummyPasswordHash: dummyPasswordHash,
		TrustedProxy:      trustedProxy,
	})
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "api"), slog.String("error_kind", "configuration"), slog.String("reason", err.Error()))
		os.Exit(1)
	}
	handler := server.Handler()

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
	go server.runGarbageCollector(ctx)

	sched := scheduler.NewScheduler(database, q, server.indexer)
	sched.SetSyncEngine(syncEng)
	go sched.Run(ctx)
	go sched.RunOrphanedSyncJobRecovery(ctx)
	go sched.RunOrphanedMigrationIndexingRecovery(ctx)

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
