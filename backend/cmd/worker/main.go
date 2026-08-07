package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/processor"
	"backend/internal/queue"
)

const defaultDatabaseURL = "postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=require"

type workerConfig struct {
	databaseURL   string
	redisURL      string
	encryptionKey string
}

func loadWorkerConfig(getenv func(string) string) (workerConfig, error) {
	config := workerConfig{
		databaseURL:   getenv("DATABASE_URL"),
		redisURL:      getenv("REDIS_URL"),
		encryptionKey: getenv("ENCRYPTION_SECRET_KEY"),
	}
	if config.databaseURL == "" {
		config.databaseURL = defaultDatabaseURL
	}
	if config.redisURL == "" {
		config.redisURL = "localhost:6379"
	}
	if config.encryptionKey == "" {
		return workerConfig{}, errors.New("ENCRYPTION_SECRET_KEY is required")
	}
	return config, nil
}

func main() {
	log.Println("Starting Migration Worker...")

	// Read environment variables
	if os.Getenv("DATABASE_URL") == "" {
		// No explicit DATABASE_URL: default to TLS-required rather than
		// silently falling back to an unencrypted connection.
		log.Println("WARNING: DATABASE_URL not set — defaulting to sslmode=require. Set DATABASE_URL explicitly to override (e.g. for a local dev database).")
	}
	config, err := loadWorkerConfig(os.Getenv)
	if err != nil {
		log.Fatal("ENCRYPTION_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
	}

	// 1. Initialize PostgreSQL
	database, err := db.InitDB(config.databaseURL)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()
	log.Println("Connected to PostgreSQL database.")

	// 1b. Install the administrator-managed OAuth credential loader so any inline
	// token refresh (Finding 9) has a populated credential cache instead of
	// failing silently. The cache holds ciphertext only and is decrypted at the
	// moment a token request is made.
	oauth.Configure(oauth.NewDBLoader(database), config.encryptionKey)

	// 2. Initialize Redis Queue
	q, err := queue.NewQueue(config.redisURL)
	if err != nil {
		log.Fatalf("Failed to initialize Redis queue: %v", err)
	}
	log.Println("Connected to Redis.")

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
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Cancel context on signal, in a separate goroutine so Start() blocks main.
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v. Initiating graceful shutdown...\n", sig)
		cancel()
	}()

	// Block until context is cancelled AND all in-flight tasks have finished.
	proc.Start(ctx)
	log.Println("Worker shut down successfully.")
}
