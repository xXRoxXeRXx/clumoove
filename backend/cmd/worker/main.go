package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/processor"
	"backend/internal/queue"
	appSync "backend/internal/sync"
)

func main() {
	log.Println("Starting Migration Worker...")

	// Initialize OAuth provider configs up front so any inline token refresh
	// (Finding 9) has a populated configs map instead of failing silently.
	oauth.InitConfigs()

	// Read environment variables
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		// No explicit DATABASE_URL: default to TLS-required rather than
		// silently falling back to an unencrypted connection.
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

	// Generate worker ID
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}
	workerID := fmt.Sprintf("worker-%s-%d", hostname, os.Getpid())

	// Create processor and sync engine, then wire them together so the
	// connection-recovery scheduler can trigger a new sync pass.
	proc := processor.NewProcessor(database, q, workerID, encryptionKey)
	proc.SetDBConnStr(dbURL) // Enable pg_notify-based wake-up for idle worker threads
	syncEng := appSync.NewEngine(database, q, encryptionKey)
	proc.SetSyncEngine(syncEng)

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
