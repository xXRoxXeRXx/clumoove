package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for dev
	},
}

type APIServer struct {
	db        *sql.DB
	queue     *queue.Queue
	secretKey string
	ctx       context.Context
}

func main() {
	log.Println("Starting Migration API Gateway...")

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
		log.Fatal("ENCRYPTION_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
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

	// Context for background processes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := &APIServer{
		db:        database,
		queue:     q,
		secretKey: encryptionKey,
		ctx:       ctx,
	}

	// Start Garbage Collector (GC)
	go server.runGarbageCollector(ctx)

	// Go 1.22 Router
	mux := http.NewServeMux()

	// Routes
	mux.HandleFunc("POST /api/migration/connect", server.handleConnect)
	mux.HandleFunc("POST /api/migration/browse", server.handleBrowse)
	mux.HandleFunc("POST /api/migration/start", server.handleStart)
	mux.HandleFunc("GET /api/migration/{id}", server.handleGetStatus)
	mux.HandleFunc("GET /api/migration/{id}/report", server.handleDownloadReport)
	mux.HandleFunc("GET /api/migration/{id}/ws", server.handleWebSocket)

	// Middleware (CORS)
	handler := corsMiddleware(mux)

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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
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
	ResourceType   string `json:"resource_type"` // "calendars" or "contacts"
}

// handleBrowse lists the top-level calendar collections or addressbooks on the source server.
// It contacts only the source, avoiding the two extra round-trips that reusing handleConnect would cause.
func (s *APIServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	var req BrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.ResourceType != "calendars" && req.ResourceType != "contacts" {
		http.Error(w, "resource_type must be 'calendars' or 'contacts'", http.StatusBadRequest)
		return
	}

	sourceClient, err := storage.NewProvider(req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid source URL format"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ok, err := sourceClient.Connect(ctx)
	if !ok {
		errMsg := "Source connection failed"
		if err != nil {
			errMsg = fmt.Sprintf("Source connection failed: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": errMsg})
		return
	}

	// List the root of the resource type — each top-level collection is one calendar / addressbook
	items, err := sourceClient.GetDirectoryListing(ctx, req.ResourceType, "/")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to list %s: %v", req.ResourceType, err),
		})
		return
	}

	// Only return top-level collections (IsDir == true); individual resource files are not selectable here
	var collections []storage.CloudResource
	for _, item := range items {
		if item.IsDir {
			collections = append(collections, item)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"items":   collections,
	})
}

type ConnectRequest struct {
	SourceURL      string `json:"source_url"`
	SourceUsername string `json:"source_username"`
	SourcePassword string `json:"source_password"`
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	SourceProvider string `json:"source_provider"`
	TargetProvider string `json:"target_provider"`
	Path           string `json:"path"`
	ResourceType   string `json:"resource_type"`
}

func (s *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if req.ResourceType == "" {
		req.ResourceType = "files"
	}

	// Test Source Connection
	sourceClient, err := storage.NewProvider(req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid source URL format"})
		return
	}
	srcCtx, srcCancel := context.WithTimeout(r.Context(), 15*time.Second)
	sourceOK, err := sourceClient.Connect(srcCtx)
	srcCancel()
	if !sourceOK {
		errMsg := "Source connection failed"
		if err != nil {
			errMsg = fmt.Sprintf("Source connection failed: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": errMsg})
		return
	}

	// Test Target Connection
	targetClient, err := storage.NewProvider(req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid target URL format"})
		return
	}
	tgtCtx, tgtCancel := context.WithTimeout(r.Context(), 15*time.Second)
	targetOK, err := targetClient.Connect(tgtCtx)
	tgtCancel()
	if !targetOK {
		errMsg := "Target connection failed"
		if err != nil {
			errMsg = fmt.Sprintf("Target connection failed: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": errMsg})
		return
	}

	// Also render the source folder structure (defaults to root /)
	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}
	listCtx, listCancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer listCancel()
	files, err := sourceClient.GetDirectoryListing(listCtx, req.ResourceType, reqPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": fmt.Sprintf("Failed to list source files for path %s: %v", reqPath, err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type StartRequest struct {
	ConnectRequest
	ConflictStrategy string   `json:"conflict_strategy"` // SKIP, OVERWRITE, RENAME
	Paths            []string `json:"paths"`             // List of selected paths (files or directories)
	Calendars        []string `json:"calendars"`         // List of selected calendars
	Contacts         []string `json:"contacts"`          // List of selected contacts
}

func (s *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if len(req.Paths) == 0 && len(req.Calendars) == 0 && len(req.Contacts) == 0 {
		http.Error(w, "No source paths selected", http.StatusBadRequest)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}

	// Encrypt credentials
	sourcePassEnc, err := crypto.Encrypt(req.SourcePassword, s.secretKey)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	targetPassEnc, err := crypto.Encrypt(req.TargetPassword, s.secretKey)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	// Create Migration Record
	m := &db.Migration{
		SourceURL:               req.SourceURL,
		SourceUsername:          req.SourceUsername,
		SourcePasswordEncrypted: sourcePassEnc,
		TargetURL:               req.TargetURL,
		TargetUsername:          req.TargetUsername,
		TargetPasswordEncrypted: targetPassEnc,
		SourceProvider:          req.SourceProvider,
		TargetProvider:          req.TargetProvider,
		Status:                  "INDEXING",
		ConflictStrategy:        req.ConflictStrategy,
	}

	migrationID, err := db.CreateMigration(s.db, m)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save migration: %v", err), http.StatusInternalServerError)
		return
	}

	// Spawn Background Indexer
	go s.startIndexing(s.ctx, migrationID, req.Paths, req.Calendars, req.Contacts)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
}

func (s *APIServer) startIndexing(serverCtx context.Context, migID string, paths []string, calendars []string, contacts []string) {
	ctx, cancel := context.WithTimeout(serverCtx, 20*time.Minute)
	defer cancel()

	// Load migration from DB
	mig, err := db.GetMigration(s.db, migID)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to fetch migration: %v", err))
		return
	}

	// Decrypt source credentials
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, s.secretKey)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}

	sourceClient, err := storage.NewProvider(mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to create storage provider: %v", err))
		return
	}

	var totalFiles int
	var totalBytes int64
	var taskIDs []string

	// 1. Index files
	for _, p := range paths {
		res, err := sourceClient.InspectResource(ctx, "files", p)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Failed to inspect path %s: %v", p, err))
			return
		}

		if res.IsDir {
			err = s.indexFolder(ctx, sourceClient, "files", p, migID, &totalFiles, &totalBytes, &taskIDs)
			if err != nil {
				s.failMigration(migID, fmt.Sprintf("Indexing folder %s failed: %v", p, err))
				return
			}
		} else {
			// Single file
			hashVal := res.Hash
			task := &db.Task{
				MigrationID:  migID,
				ResourceType: "files",
				FilePath:     p,
				FileSize:     res.Size,
				SourceHash:   sql.NullString{String: hashVal, Valid: hashVal != ""},
				Status:       "PENDING",
			}
			taskID, err := db.CreateTask(s.db, task)
			if err != nil {
				s.failMigration(migID, fmt.Sprintf("Failed to create task in DB: %v", err))
				return
			}
			taskIDs = append(taskIDs, taskID)
			totalFiles++
			totalBytes += res.Size
		}
	}

	// 2. Index calendars
	for _, p := range calendars {
		err = s.indexFolder(ctx, sourceClient, "calendars", p, migID, &totalFiles, &totalBytes, &taskIDs)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Indexing calendar %s failed: %v", p, err))
			return
		}
	}

	// 3. Index contacts
	for _, p := range contacts {
		err = s.indexFolder(ctx, sourceClient, "contacts", p, migID, &totalFiles, &totalBytes, &taskIDs)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Indexing contacts %s failed: %v", p, err))
			return
		}
	}

	// Update Totals and status to RUNNING in PostgreSQL
	err = db.UpdateMigrationTotals(s.db, migID, totalFiles, totalBytes)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to update migration totals: %v", err))
		return
	}

	err = db.UpdateMigrationStatus(s.db, migID, "RUNNING", nil)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to set migration running: %v", err))
		return
	}

	// Push task IDs to Redis queue
	for _, tID := range taskIDs {
		err = s.queue.Enqueue(ctx, migID, tID)
		if err != nil {
			log.Printf("Failed to enqueue task %s in Redis: %v\n", tID, err)
		}
	}
	log.Printf("Finished indexing migration %s. Total files: %d, Total size: %d bytes. Enqueued tasks.\n", migID, totalFiles, totalBytes)
}

func (s *APIServer) indexFolder(ctx context.Context, client storage.StorageProvider, resourceType string, startPath string, migID string, totalFiles *int, totalBytes *int64, taskIDs *[]string) error {
	queue := []string{startPath}
	visited := make(map[string]bool)
	visited[startPath] = true

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		currentPath := queue[0]
		queue = queue[1:]

		files, err := client.GetDirectoryListing(ctx, resourceType, currentPath)
		if err != nil {
			return err
		}

		for _, file := range files {
			if file.IsDir {
				if !visited[file.Path] {
					visited[file.Path] = true
					queue = append(queue, file.Path)
				}
			} else {
				task := &db.Task{
					MigrationID:  migID,
					ResourceType: resourceType,
					FilePath:     file.Path,
					FileSize:     file.Size,
					SourceHash:   sql.NullString{String: file.Hash, Valid: file.Hash != ""},
					Status:       "PENDING",
				}
				taskID, err := db.CreateTask(s.db, task)
				if err != nil {
					return err
				}
				*taskIDs = append(*taskIDs, taskID)
				*totalFiles++
				*totalBytes += file.Size
			}
		}
	}
	return nil
}

func (s *APIServer) failMigration(migID string, errMsg string) {
	log.Printf("Migration %s failed during indexing: %s\n", migID, errMsg)
	_ = db.UpdateMigrationStatus(s.db, migID, "FAILED", &errMsg)
}

func (s *APIServer) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Migration not found", http.StatusNotFound)
		} else {
			log.Printf("Error fetching migration %s: %v\n", id, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, http.StatusOK, mig)
}

func (s *APIServer) handleDownloadReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	tasks, err := db.GetFailedTasksForReport(s.db, id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get report: %v", err), http.StatusInternalServerError)
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
			task.FilePath,
			fmt.Sprintf("%d", task.FileSize),
			fmt.Sprintf("%d", task.Attempts),
			errMsg,
		})
	}
}

// handleWebSocket handles the progress update stream
func (s *APIServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket: %v\n", err)
		return
	}
	defer ws.Close()

	log.Printf("WebSocket client connected for migration: %s\n", id)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Fetch migration state
		mig, err := db.GetMigration(s.db, id)
		if err != nil {
			break
		}

		// Query active file path
		activeFile, _ := db.GetActiveTaskPath(s.db, r.Context(), id)

		responsePayload := map[string]interface{}{
			"id":              mig.ID,
			"status":          mig.Status,
			"total_files":     mig.TotalFiles,
			"total_bytes":     mig.TotalBytes,
			"processed_files": mig.ProcessedFiles,
			"processed_bytes": mig.ProcessedBytes,
			"skipped_files":   mig.SkippedFiles,
			"failed_files":    mig.FailedFiles,
			"error_message":   "",
			"active_file":     activeFile,
		}

		if mig.ErrorMessage.Valid {
			responsePayload["error_message"] = mig.ErrorMessage.String
		}

		// Write to WS
		data, err := json.Marshal(responsePayload)
		if err != nil {
			break
		}

		err = ws.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			break // Client disconnected
		}

		// If migration is in a terminal state (COMPLETED or FAILED) and all tasks finished, close socket after final state
		if (mig.Status == "COMPLETED" || mig.Status == "FAILED") && mig.ProcessedFiles >= mig.TotalFiles {
			// Pause a bit to let client read the final completed status
			time.Sleep(1 * time.Second)
			break
		}
	}
	log.Printf("WebSocket client disconnected for migration: %s\n", id)
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
