package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"backend/internal/backup"
	"backend/internal/config"
	"backend/internal/coordcfg"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/observability"
	"backend/internal/processor"
	"backend/internal/queue"
	"backend/internal/restore"
)

type workerConfig struct {
	databaseURL           string
	databaseURLDefaulted  bool
	redisURL              string
	redisPassword         string
	encryptionKey         string
	maxThreads            int
	indexingTimeout       time.Duration
	dbMaxConns            int
	maxBackupPackWriters  int
	maxRestorePackReaders int
}

// maxThreadsCeiling bounds MAX_THREADS so a typo cannot size huge channels.
const maxThreadsCeiling = 256

// defaultMaxThreads matches the maximum selectable threads per migration in
// the UI slider; per-migration concurrency is still limited by its own setting.
const defaultMaxThreads = 16

func loadWorkerConfig(getenv func(string) string) (workerConfig, error) {
	databaseURL, databaseURLDefaulted := config.DatabaseURL(getenv("DATABASE_URL"))
	redisURL, _ := config.RedisURL(getenv("REDIS_URL"))
	workerCfg := workerConfig{
		databaseURL:           databaseURL,
		databaseURLDefaulted:  databaseURLDefaulted,
		redisURL:              redisURL,
		redisPassword:         getenv("REDIS_PASSWORD"),
		encryptionKey:         getenv("ENCRYPTION_SECRET_KEY"),
		maxThreads:            defaultMaxThreads,
		maxBackupPackWriters:  1,
		maxRestorePackReaders: 1,
	}
	if workerCfg.encryptionKey == "" {
		return workerConfig{}, errors.New("ENCRYPTION_SECRET_KEY is required")
	}
	var err error
	if workerCfg.maxThreads, err = intSetting(getenv, "MAX_THREADS", defaultMaxThreads, 1, maxThreadsCeiling); err != nil {
		return workerConfig{}, err
	}
	if workerCfg.indexingTimeout, err = config.IndexingTimeout(getenv("INDEXING_TIMEOUT_MINUTES")); err != nil {
		return workerConfig{}, err
	}
	workerCfg.dbMaxConns = db.MaxConnsForThreads(workerCfg.maxThreads)
	if workerCfg.maxBackupPackWriters, err = intSetting(getenv, "MAX_BACKUP_PACK_WRITERS", 1, 1, 4); err != nil {
		return workerConfig{}, err
	}
	if workerCfg.maxRestorePackReaders, err = intSetting(getenv, "MAX_RESTORE_PACK_READERS", 1, 1, 4); err != nil {
		return workerConfig{}, err
	}
	return workerCfg, nil
}

// intSetting reads an optional integer setting, returning def when unset and
// an error naming the setting when it is not an integer within [min, max].
func intSetting(getenv func(string) string, name string, def, min, max int) (int, error) {
	value := getenv(name)
	if value == "" {
		return def, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, min, max)
	}
	return parsed, nil
}

// processorConfig is the explicit execution configuration handed to the
// processor; the database DSN doubles as the pg_notify LISTEN connection.
func (c workerConfig) processorConfig(workerID string) processor.Config {
	return processor.Config{
		WorkerID:        workerID,
		SecretKey:       c.encryptionKey,
		MaxThreads:      c.maxThreads,
		NotifyDSN:       c.databaseURL,
		IndexingTimeout: c.indexingTimeout,
	}
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

	// Registered first so it runs after every other deferred cleanup (database
	// close, signal teardown) when the worker stops because of a failure.
	failed := false
	defer func() {
		if failed {
			os.Exit(1)
		}
	}()

	// Load and validate all worker settings once, before any component starts.
	config, err := loadWorkerConfig(os.Getenv)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "worker"), slog.String("reason", "invalid_configuration"), slog.String("error_kind", "configuration"), observability.Error(err))
		os.Exit(1)
	}
	if config.databaseURLDefaulted {
		// No explicit DATABASE_URL: default to TLS-required rather than
		// silently falling back to an unencrypted connection.
		slog.Warn("database_url_defaulted", slog.String("component", "worker"))
	}

	// 1. Initialize PostgreSQL
	database, err := db.InitDB(config.databaseURL, config.dbMaxConns)
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
	q, err := queue.NewQueue(config.redisURL, config.redisPassword)
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
	proc, err := processor.NewProcessor(database, q, config.processorConfig(workerID))
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "processor"), observability.Error(err), slog.String("error_kind", "configuration"))
		os.Exit(1)
	}
	packReaders, err := restore.NewPackReaderLimiter(config.maxRestorePackReaders)
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "restore"), observability.Error(err), slog.String("error_kind", "configuration"))
		os.Exit(1)
	}
	backupCoordinator, err := backup.NewCoordinator(database, backup.Config{
		Common:         coordcfg.Common{EncryptionKey: config.encryptionKey, WorkerID: workerID, OAuthLocker: q},
		MaxPackWriters: config.maxBackupPackWriters,
		PackReaders:    packReaders,
	})
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "backup"), slog.String("reason", "invalid_pack_writer_limit"), observability.Error(err), slog.String("error_kind", "configuration"))
		os.Exit(1)
	}
	restoreCoordinator, err := restore.NewCoordinator(database, restore.Config{
		Common:      coordcfg.Common{EncryptionKey: config.encryptionKey, WorkerID: workerID, OAuthLocker: q},
		PackReaders: packReaders,
	})
	if err != nil {
		slog.Error("startup_failed", slog.String("component", "restore"), observability.Error(err), slog.String("error_kind", "configuration"))
		os.Exit(1)
	}

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Wait for termination signals
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// A second signal lets an operator abort a potentially long task drain.
	go watchShutdownSignals(sigChan, cancel, os.Exit)
	// A coordinator loop that stops before shutdown is a terminal failure: the
	// supervisor cancels the worker context so the drain below begins and the
	// liveness heartbeat stops, then main exits non-zero for a platform restart.
	supervisor := newCoordinatorSupervisor(cancel)
	supervisor.Go(ctx, "backup", func(ctx context.Context) error { return backupCoordinator.Run(ctx, time.Second) })
	supervisor.Go(ctx, "restore", func(ctx context.Context) error { return restoreCoordinator.Run(ctx, time.Second) })

	// Block until context is cancelled AND all in-flight tasks have finished.
	proc.Start(ctx)
	// Coordinators stop on the cancelled context; wait so none is still using
	// the database when it closes, and so a failure is never missed.
	supervisor.Wait()
	failed = supervisor.Err() != nil
	slog.Info("service_stopped", slog.String("component", "worker"), slog.Bool("failed", failed))
}
