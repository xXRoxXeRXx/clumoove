package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/db"
	"backend/internal/processor"
	"backend/internal/queue"
)

func main() {
	log.Println("Starting Migration Worker...")

	// Read environment variables
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=disable"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "localhost:6379"
	}

	encryptionKey := os.Getenv("ENCRYPTION_SECRET_KEY")
	if encryptionKey == "" {
		encryptionKey = "default-secret-key-32-chars-long!!"
		log.Println("WARNING: ENCRYPTION_SECRET_KEY not set. Using default insecure key.")
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

	// Create processor
	proc := processor.NewProcessor(database, q, workerID, encryptionKey)

	// Context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Wait for termination signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start processor loop in background
	go proc.Start(ctx)

	// Block until signal received
	sig := <-sigChan
	log.Printf("Received signal %v. Initiating graceful shutdown...\n", sig)

	// Cancel context to stop processor loops
	cancel()

	// Allow some time for active tasks to finalize
	time.Sleep(3 * time.Second)
	log.Println("Worker shut down successfully.")
}
