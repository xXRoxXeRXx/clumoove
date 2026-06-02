package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/webdav"

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
		encryptionKey = "default-secret-key-32-chars-long!!"
		log.Println("WARNING: ENCRYPTION_SECRET_KEY not set. Using default insecure key.")
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

	server := &APIServer{
		db:        database,
		queue:     q,
		secretKey: encryptionKey,
	}

	// Context for background processes
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Garbage Collector (GC)
	go server.runGarbageCollector(ctx)

	// Go 1.22 Router
	mux := http.NewServeMux()

	// Routes
	mux.HandleFunc("POST /api/migration/connect", server.handleConnect)
	mux.HandleFunc("POST /api/migration/start", server.handleStart)
	mux.HandleFunc("GET /api/migration/{id}", server.handleGetStatus)
	mux.HandleFunc("GET /api/migration/{id}/report", server.handleDownloadReport)
	mux.HandleFunc("GET /api/migration/{id}/ws", server.handleWebSocket)

	// Middleware (CORS)
	handler := corsMiddleware(mux)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
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

// REST Route Handlers
type ConnectRequest struct {
	SourceURL      string `json:"source_url"`
	SourceUsername string `json:"source_username"`
	SourcePassword string `json:"source_password"`
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	Path           string `json:"path"`
}

func (s *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// Test Source Connection
	sourceClient, err := webdav.NewClient(req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid source URL format"})
		return
	}
	sourceOK, err := sourceClient.Connect(ctx)
	if !sourceOK {
		errMsg := "Source connection failed"
		if err != nil {
			errMsg = fmt.Sprintf("Source connection failed: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": errMsg})
		return
	}

	// Test Target Connection
	targetClient, err := webdav.NewClient(req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid target URL format"})
		return
	}
	targetOK, err := targetClient.Connect(ctx)
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
	files, err := sourceClient.GetDirectoryListing(ctx, reqPath)
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
}

func (s *APIServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if len(req.Paths) == 0 {
		http.Error(w, "No source paths selected", http.StatusBadRequest)
		return
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
		Status:                  "INDEXING",
		ConflictStrategy:        req.ConflictStrategy,
	}

	migrationID, err := db.CreateMigration(s.db, m)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to save migration: %v", err), http.StatusInternalServerError)
		return
	}

	// Spawn Background Indexer
	go s.startIndexing(migrationID, req.SourceURL, req.SourceUsername, req.SourcePassword, req.Paths)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
}

func (s *APIServer) startIndexing(migID, sURL, sUser, sPass string, paths []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	sourceClient, err := webdav.NewClient(sURL, sUser, sPass)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to create WebDAV client: %v", err))
		return
	}

	var totalFiles int
	var totalBytes int64
	var taskIDs []string

	for _, p := range paths {
		// Index files recursively
		// First verify if the path is a directory or file using PROPFIND Depth: 0
		u := sourceClient.BaseURL + "/files/" + sourceClient.Username + "/" + strings.TrimPrefix(p, "/")
		
		body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
			<d:propfind xmlns:d="DAV:">
				<d:prop>
					<d:resourcetype/>
					<d:getcontentlength/>
				</d:prop>
			</d:propfind>`)
		
		req, err := http.NewRequest("PROPFIND", u, bytes.NewReader(body))
		if err != nil {
			s.failMigration(migID, err.Error())
			return
		}
		req.SetBasicAuth(sUser, sPass)
		req.Header.Set("Depth", "0")
		req.Header.Set("Content-Type", "application/xml")

		resp, err := sourceClient.HTTPClient.Do(req)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Failed to inspect path %s: %v", p, err))
			return
		}

		var multistatus webdav.XMLMultistatus
		err = xml.NewDecoder(resp.Body).Decode(&multistatus)
		resp.Body.Close()

		if err != nil || len(multistatus.Responses) == 0 {
			s.failMigration(migID, fmt.Sprintf("Failed to parse metadata for path %s", p))
			return
		}

		isDir := false
		var fileSize int64
		response := multistatus.Responses[0]
		for _, pstat := range response.Propstat {
			if strings.Contains(pstat.Status, "200 OK") {
				isDir = pstat.Prop.ResourceType.Collection != nil
				fileSize, _ = strconv.ParseInt(pstat.Prop.GetContentLength, 10, 64)
			}
		}

		if isDir {
			err = s.indexFolder(ctx, sourceClient, p, migID, &totalFiles, &totalBytes, &taskIDs)
			if err != nil {
				s.failMigration(migID, fmt.Sprintf("Indexing folder %s failed: %v", p, err))
				return
			}
		} else {
			// Single file
			// Get hash
			hashVal, _ := sourceClient.GetFileHash(ctx, p)
			task := &db.Task{
				MigrationID: migID,
				FilePath:    p,
				FileSize:    fileSize,
				SourceHash:  sql.NullString{String: hashVal, Valid: hashVal != ""},
				Status:      "PENDING",
			}
			taskID, err := db.CreateTask(s.db, task)
			if err != nil {
				s.failMigration(migID, fmt.Sprintf("Failed to create task in DB: %v", err))
				return
			}
			taskIDs = append(taskIDs, taskID)
			totalFiles++
			totalBytes += fileSize
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

func (s *APIServer) indexFolder(ctx context.Context, client *webdav.Client, currentPath string, migID string, totalFiles *int, totalBytes *int64, taskIDs *[]string) error {
	files, err := client.GetDirectoryListing(ctx, currentPath)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir {
			err = s.indexFolder(ctx, client, file.Path, migID, totalFiles, totalBytes, taskIDs)
			if err != nil {
				return err
			}
		} else {
			task := &db.Task{
				MigrationID: migID,
				FilePath:    file.Path,
				FileSize:    file.Size,
				SourceHash:  sql.NullString{String: file.Hash, Valid: file.Hash != ""},
				Status:      "PENDING",
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
		http.Error(w, "Migration not found", http.StatusNotFound)
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
		var activeFile string
		activeQuery := `SELECT file_path FROM tasks WHERE migration_id = $1 AND status = 'RUNNING' LIMIT 1`
		err = s.db.QueryRow(activeQuery, id).Scan(&activeFile)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("Error fetching active task path: %v\n", err)
		}

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
