package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"backend/internal/config"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/observability"
	"backend/internal/processor"
	"backend/internal/queue"
)

type workerConfig struct {
	databaseURL          string
	databaseURLDefaulted bool
	redisURL             string
	encryptionKey        string
}

func loadWorkerConfig(getenv func(string) string) (workerConfig, error) {
	databaseURL, databaseURLDefaulted := config.DatabaseURL(getenv("DATABASE_URL"))
	redisURL, _ := config.RedisURL(getenv("REDIS_URL"))
	workerCfg := workerConfig{
		databaseURL:          databaseURL,
		databaseURLDefaulted: databaseURLDefaulted,
		redisURL:             redisURL,
		encryptionKey:        getenv("ENCRYPTION_SECRET_KEY"),
	}
	if workerCfg.encryptionKey == "" {
		return workerConfig{}, errors.New("ENCRYPTION_SECRET_KEY is required")
	}
	return workerCfg, nil
}

func watchShutdownSignals(sigChan <-chan os.Signal, cancel context.CancelFunc, exit func(int)) {
	sig := <-sigChan
	slog.Info("shutdown_requested", slog.String("component", "worker"), slog.String("signal", sig.String()))
	cancel()

	sig = <-sigChan
	slog.Warn("shutdown_forced", slog.String("component", "worker"), slog.String("signal", sig.String()))
	exit(1)
}

func main() {
	if _, err := observability.Configure("worker"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	slog.Info("service_starting", slog.String("component", "worker"))

	// Read environment variables.
	config, err := loadWorkerConfig(os.Getenv)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "worker"), slog.String("reason", "encryption_key_missing"), slog.String("error_kind", "configuration"), observability.Error(err))
		os.Exit(1)
	}
	if config.databaseURLDefaulted {
		// No explicit DATABASE_URL: default to TLS-required rather than
		// silently falling back to an unencrypted connection.
		slog.Warn("database_url_defaulted", slog.String("component", "worker"))
	}

	// 1. Initialize PostgreSQL
	database, err := db.InitDB(config.databaseURL)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "database"), slog.String("reason", "init_failed"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		os.Exit(1)
	}
	defer database.Close()
	slog.Info("database_connected", slog.String("component", "database"))

	// 1b. Install the administrator-managed OAuth credential loader so any inline
	// token refresh (Finding 9) has a populated credential cache instead of
	// failing silently. The cache holds ciphertext only and is decrypted at the
	// moment a token request is made.
	oauth.Configure(oauth.NewDBLoader(database), config.encryptionKey)

	// 2. Initialize Redis Queue
	q, err := queue.NewQueue(config.redisURL)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "queue"), slog.String("reason", "init_failed"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		os.Exit(1)
	}
	slog.Info("queue_connected", slog.String("component", "queue"))

	// Generate worker ID
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}
	workerID := fmt.Sprintf("worker-%s-%d", hostname, os.Getpid())

	// The worker only processes queued tasks. Sync-pass coordinators are owned
	// by API instances and started exclusively by the API scheduler.
	proc := processor.NewProcessor(database, q, workerID, config.encryptionKey)
	proc.SetDBConnStr(config.databaseURL) // Enable pg_notify-based wake-up for idle worker threads

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Wait for termination signals
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// A second signal lets an operator abort a potentially long task drain.
	go watchShutdownSignals(sigChan, cancel, os.Exit)

	// Block until context is cancelled AND all in-flight tasks have finished.
	proc.Start(ctx)
	slog.Info("service_stopped", slog.String("component", "worker"))
}
