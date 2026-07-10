package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
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
	db            *sql.DB
	queue         *queue.Queue
	encryptionKey string // AES key for credential encryption
	jwtSecret     string // HMAC key for JWT signing (separate from encryptionKey)
	ctx           context.Context
}

func main() {
	log.Println("Starting Migration API Gateway...")
	oauth.InitConfigs()

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

	// Separate secret for JWT signing — must not share AES encryption key
	jwtSecret := os.Getenv("JWT_SECRET_KEY")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET_KEY is required but not set. Refusing to start with an insecure key.")
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
		db:            database,
		queue:         q,
		encryptionKey: encryptionKey,
		jwtSecret:     jwtSecret,
		ctx:           ctx,
	}

	// Start Garbage Collector (GC) is removed as per requirements (permanent history until manual deletion)
	// go server.runGarbageCollector(ctx)

	// Go 1.22 Router
	mux := http.NewServeMux()

	// Auth Routes (Public)
	mux.HandleFunc("POST /api/auth/register", server.handleRegister)
	mux.HandleFunc("POST /api/auth/login", server.handleLogin)
	mux.HandleFunc("POST /api/auth/refresh", server.handleRefresh)
	mux.HandleFunc("POST /api/auth/logout", server.handleLogout)

	// Protected Auth Routes
	jwtMiddleware := auth.AuthMiddleware(server.jwtSecret)
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleMe)))

	mux.Handle("GET /api/migration", jwtMiddleware(http.HandlerFunc(server.handleListMigrations)))
	mux.Handle("POST /api/migration/connect", jwtMiddleware(http.HandlerFunc(server.handleConnect)))
	mux.Handle("POST /api/migration/browse", jwtMiddleware(http.HandlerFunc(server.handleBrowse)))
	mux.Handle("POST /api/migration/target/browse", jwtMiddleware(http.HandlerFunc(server.handleTargetBrowse)))
	mux.Handle("POST /api/migration/target/mkdir", jwtMiddleware(http.HandlerFunc(server.handleTargetMkdir)))
	mux.Handle("POST /api/migration/start", jwtMiddleware(http.HandlerFunc(server.handleStart)))
	mux.Handle("GET /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetStatus)))
	mux.Handle("DELETE /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteMigration)))
	mux.Handle("GET /api/migration/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadReport)))

	// WebSockets & OAuth Callbacks (Require custom/token-based verification inside handler)
	mux.HandleFunc("GET /api/migration/{id}/ws", server.handleWebSocket)
	mux.HandleFunc("GET /api/oauth/auth", server.handleOAuthAuth)
	mux.HandleFunc("GET /api/oauth/callback", server.handleOAuthCallback)

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

	// Start the OAuth Token Rotation Daemon (PRD-12)
	go server.RunOAuthRotationDaemon(ctx)

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

// allowedOrigins defines the exact origins that may send credentialed cross-site requests.
// Credentials (cookies) are only reflected for these origins; all others receive no Allow-Credentials header.
var allowedOrigins = func() map[string]bool {
	allowed := map[string]bool{
		"http://localhost:5173": true, // Vite dev server
		"http://localhost:3000": true, // alternative dev port
		"http://localhost:3001": true, // docker compose port
	}
	// Allow the production domain if set via environment variable
	if prod := os.Getenv("CORS_ALLOWED_ORIGIN"); prod != "" {
		allowed[prod] = true
	}
	return allowed
}()

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigins[origin] {
			// Credentialed requests are only allowed from the whitelisted origins
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		// Requests from unknown or empty origins receive no Allow-Origin header (blocked by browser if necessary)
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Cookie")

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

	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
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

type TargetBrowseRequest struct {
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	TargetProvider string `json:"target_provider"`
	Path           string `json:"path"`
}

func (s *APIServer) handleTargetBrowse(w http.ResponseWriter, r *http.Request) {
	var req TargetBrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid target URL format"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		// Do NOT forward err.Error() verbatim — the HTTP client may embed the URL
		// including credentials in the error string.
		log.Printf("handleTargetBrowse: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Target connection failed. Check URL and credentials."})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	files, err := targetClient.GetDirectoryListing(ctx, "files", reqPath)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": fmt.Sprintf("Failed to list target files for path %s: %v", reqPath, err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type TargetMkdirRequest struct {
	TargetURL      string `json:"target_url"`
	TargetUsername string `json:"target_username"`
	TargetPassword string `json:"target_password"`
	TargetProvider string `json:"target_provider"`
	Path           string `json:"path"`
}

func (s *APIServer) handleTargetMkdir(w http.ResponseWriter, r *http.Request) {
	var req TargetMkdirRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if req.Path == "" || req.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid folder path"})
		return
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid target URL format"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		// Do NOT forward err.Error() verbatim — the HTTP client may embed the URL
		// including credentials in the error string.
		log.Printf("handleTargetMkdir: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Target connection failed. Check URL and credentials."})
		return
	}

	err = targetClient.CreateDirectory(ctx, "files", req.Path)
	if err != nil {
		log.Printf("handleTargetMkdir: CreateDirectory(%s) failed: %v", req.Path, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": fmt.Sprintf("Failed to create folder: %s", req.Path)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

type ConnectRequest struct {
	SourceURL            string `json:"source_url"`
	SourceUsername       string `json:"source_username"`
	SourcePassword       string `json:"source_password"`
	SourceRefreshToken   string `json:"source_refresh_token"`
	SourceTokenExpiresIn int    `json:"source_token_expires_in"`
	TargetURL            string `json:"target_url"`
	TargetUsername       string `json:"target_username"`
	TargetPassword       string `json:"target_password"`
	TargetRefreshToken   string `json:"target_refresh_token"`
	TargetTokenExpiresIn int    `json:"target_token_expires_in"`
	SourceProvider       string `json:"source_provider"`
	TargetProvider       string `json:"target_provider"`
	Path                 string `json:"path"`
	ResourceType         string `json:"resource_type"`
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

	// Whitelist provider values to fail fast with a clear error
	validProviders := map[string]bool{"nextcloud": true, "webdav": true, "dropbox": true, "google": true}
	if !validProviders[req.SourceProvider] {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": fmt.Sprintf("unsupported source provider: %s", req.SourceProvider)})
		return
	}
	if !validProviders[req.TargetProvider] {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": fmt.Sprintf("unsupported target provider: %s", req.TargetProvider)})
		return
	}

	// Test Source Connection
	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
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
	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
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
	TargetDir        string   `json:"target_dir"`        // Target directory to copy files to
	Threads          int      `json:"threads"`
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

	targetDir := req.TargetDir
	if targetDir == "" {
		targetDir = "/"
	}

	// Encrypt credentials
	sourcePassEnc, err := crypto.Encrypt(req.SourcePassword, s.encryptionKey)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	targetPassEnc, err := crypto.Encrypt(req.TargetPassword, s.encryptionKey)
	if err != nil {
		http.Error(w, "Encryption failed", http.StatusInternalServerError)
		return
	}

	// Encrypt OAuth refresh tokens (if provided)
	var sourceRefreshEnc sql.NullString
	var sourceTokenExpiresAt sql.NullTime
	if req.SourceRefreshToken != "" {
		enc, err := crypto.Encrypt(req.SourceRefreshToken, s.encryptionKey)
		if err != nil {
			http.Error(w, "Encryption failed", http.StatusInternalServerError)
			return
		}
		sourceRefreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.SourceTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		sourceTokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	var targetRefreshEnc sql.NullString
	var targetTokenExpiresAt sql.NullTime
	if req.TargetRefreshToken != "" {
		enc, err := crypto.Encrypt(req.TargetRefreshToken, s.encryptionKey)
		if err != nil {
			http.Error(w, "Encryption failed", http.StatusInternalServerError)
			return
		}
		targetRefreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.TargetTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		targetTokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	// Get userID from context
	userID := auth.GetUserIDFromContext(r.Context())

	// Validate threads
	threads := req.Threads
	if threads < 1 {
		threads = 4
	} else if threads > 16 {
		threads = 16
	}

	// Create Migration Record
	m := &db.Migration{
		UserID:                      sql.NullString{String: userID, Valid: userID != ""},
		SourceURL:                   req.SourceURL,
		SourceUsername:              req.SourceUsername,
		SourcePasswordEncrypted:     sourcePassEnc,
		SourceRefreshTokenEncrypted: sourceRefreshEnc,
		SourceTokenExpiresAt:        sourceTokenExpiresAt,
		TargetURL:                   req.TargetURL,
		TargetUsername:              req.TargetUsername,
		TargetPasswordEncrypted:     targetPassEnc,
		TargetRefreshTokenEncrypted: targetRefreshEnc,
		TargetTokenExpiresAt:        targetTokenExpiresAt,
		SourceProvider:              req.SourceProvider,
		TargetProvider:              req.TargetProvider,
		Status:                      "INDEXING",
		ConflictStrategy:            req.ConflictStrategy,
		TargetDir:                   targetDir,
		Threads:                     threads,
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
	sourcePass, err := crypto.Decrypt(mig.SourcePasswordEncrypted, s.encryptionKey)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to decrypt source password: %v", err))
		return
	}

	sourceClient, err := storage.NewProvider(ctx, mig.SourceProvider, mig.SourceURL, mig.SourceUsername, sourcePass)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to create storage provider: %v", err))
		return
	}

	var totalFiles int
	var totalBytes int64
	var taskIDs []string
	indexedPaths := make(map[string]bool)

	// 1. Index files
	for _, p := range paths {
		res, err := sourceClient.InspectResource(ctx, "files", p)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Failed to inspect path %s: %v", p, err))
			return
		}

		if res.IsDir {
			err = s.indexFolder(ctx, sourceClient, "files", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths)
			if err != nil {
				s.failMigration(migID, fmt.Sprintf("Indexing folder %s failed: %v", p, err))
				return
			}
		} else {
			// Single file
			key := fmt.Sprintf("files:%s", p)
			if indexedPaths[key] {
				continue
			}
			indexedPaths[key] = true
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
		err = s.indexFolder(ctx, sourceClient, "calendars", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Indexing calendar %s failed: %v", p, err))
			return
		}
	}

	// 3. Index contacts
	for _, p := range contacts {
		err = s.indexFolder(ctx, sourceClient, "contacts", p, migID, &totalFiles, &totalBytes, &taskIDs, indexedPaths)
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

	// Re-evaluate completion: tasks may have all finished before totals were written
	// (race condition with fast/small migrations). A zero-delta increment re-checks
	// processed >= total inside the same transaction logic.
	if err := db.IncrementMigrationProgress(s.db, migID, 0, 0, 0, 0); err != nil {
		log.Printf("Warning: zero-delta progress check after indexing failed for %s: %v\n", migID, err)
	}

	if totalFiles == 0 {
		err = db.UpdateMigrationStatus(s.db, migID, "COMPLETED", nil)
		if err != nil {
			s.failMigration(migID, fmt.Sprintf("Failed to set migration completed: %v", err))
			return
		}
		log.Printf("Finished indexing migration %s. 0 files to migrate. Marked COMPLETED.\n", migID)
		return
	}

	err = db.UpdateMigrationStatus(s.db, migID, "RUNNING", nil)
	if err != nil {
		s.failMigration(migID, fmt.Sprintf("Failed to set migration running: %v", err))
		return
	}

	log.Printf("Finished indexing migration %s. Total files: %d, Total size: %d bytes.\n", migID, totalFiles, totalBytes)
}

func (s *APIServer) indexFolder(ctx context.Context, client storage.StorageProvider, resourceType string, startPath string, migID string, totalFiles *int, totalBytes *int64, taskIDs *[]string, indexedPaths map[string]bool) error {
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
				key := fmt.Sprintf("%s:%s", resourceType, file.Path)
				if indexedPaths[key] {
					continue
				}
				indexedPaths[key] = true
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

	userID := auth.GetUserIDFromContext(r.Context())

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

	if !mig.UserID.Valid || mig.UserID.String != userID {
		http.Error(w, "Forbidden: You do not own this migration", http.StatusForbidden)
		return
	}

	stats, err := db.GetMigrationResourceStats(s.db, id)
	if err != nil {
		log.Printf("Error fetching resource stats for migration %s: %v\n", id, err)
	} else {
		mig.ResourceStats = stats
	}

	writeJSON(w, http.StatusOK, mig)
}

func (s *APIServer) handleDownloadReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Migration not found", http.StatusNotFound)
		} else {
			log.Printf("Error fetching migration %s for report: %v\n", id, err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		http.Error(w, "Forbidden: You do not own this migration", http.StatusForbidden)
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

	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		http.Error(w, "Unauthorized: token query parameter missing", http.StatusUnauthorized)
		return
	}

	claims, err := auth.ValidateToken(tokenStr, s.jwtSecret)
	if err != nil {
		http.Error(w, "Unauthorized: invalid or expired token", http.StatusUnauthorized)
		return
	}
	userID := claims.UserID

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Migration not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		http.Error(w, "Forbidden: You do not own this migration", http.StatusForbidden)
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
		mig, err = db.GetMigration(s.db, id)
		if err != nil {
			break
		}

		// Query active file paths
		activeFiles, _ := db.GetActiveTaskPaths(s.db, r.Context(), id)
		var activeFile string
		if len(activeFiles) > 0 {
			activeFile = activeFiles[0]
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
			"active_files":    activeFiles,
			"threads":         mig.Threads,
		}

		if mig.ErrorMessage.Valid {
			responsePayload["error_message"] = mig.ErrorMessage.String
		}

		stats, err := db.GetMigrationResourceStats(s.db, id)
		if err == nil {
			responsePayload["resource_stats"] = stats
		} else {
			log.Printf("WebSocket error fetching resource stats: %v\n", err)
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

func generateRandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *APIServer) getRedirectURI(r *http.Request) string {
	envRedirect := os.Getenv("OAUTH_REDIRECT_URI")
	if envRedirect != "" {
		return envRedirect
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, r.Host)
}

func (s *APIServer) handleOAuthAuth(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	log.Printf("handleOAuthAuth: Hit with provider=%q", provider)

	if provider == "" {
		http.Error(w, "Missing provider parameter", http.StatusBadRequest)
		return
	}

	origin := r.URL.Query().Get("origin")
	log.Printf("handleOAuthAuth: origin query param=%q", origin)
	if origin == "" {
		if referer := r.Header.Get("Referer"); referer != "" {
			if parsed, err := url.Parse(referer); err == nil {
				origin = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
			}
		}
	}
	if origin == "" {
		log.Printf("handleOAuthAuth: rejected request with no determinable origin")
		http.Error(w, "Missing origin: supply ?origin=https://your-app.example.com", http.StatusBadRequest)
		return
	}
	// Validate origin is an absolute URL with a recognised scheme (no wildcard)
	if parsedOrigin, err := url.Parse(origin); err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") {
		log.Printf("handleOAuthAuth: rejected invalid origin %q", origin)
		http.Error(w, "Invalid origin: must be an absolute http(s) URL", http.StatusBadRequest)
		return
	}
	log.Printf("handleOAuthAuth: final origin set to %q", origin)

	stateToken := generateRandomString(16)
	if stateToken == "" {
		log.Printf("handleOAuthAuth: Failed to generate state token")
		http.Error(w, "Failed to generate state token", http.StatusInternalServerError)
		return
	}

	cookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    stateToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	}
	http.SetCookie(w, cookie)

	stateParam := fmt.Sprintf("%s:%s:%s", stateToken, provider, origin)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthAuth: constructing authURL with redirectURI=%s", redirectURI)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		log.Printf("handleOAuthAuth: GetAuthURL failed: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("handleOAuthAuth: Redirecting user to %s", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (s *APIServer) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	log.Printf("handleOAuthCallback: Received request with code length %d, state: %q", len(code), state)

	if code == "" || state == "" {
		log.Printf("handleOAuthCallback: Missing code or state")
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "*", "Authorization code or state missing")
		return
	}

	parts := strings.SplitN(state, ":", 3)
	if len(parts) < 3 {
		log.Printf("handleOAuthCallback: Invalid state format (length %d)", len(parts))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "*", "Invalid state parameter format")
		return
	}
	stateToken := parts[0]
	provider := parts[1]
	origin := parts[2]

	log.Printf("handleOAuthCallback: parsed provider=%s, origin=%s", provider, origin)

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		log.Printf("handleOAuthCallback: CSRF check failed. Cookie err: %v, stateToken: %q", err, stateToken)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "*", "CSRF verification failed: state mismatch")
		return
	}

	clearCookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, clearCookie)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthCallback: using redirectURI=%s", redirectURI)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	log.Printf("handleOAuthCallback: exchanging code for provider %s...", provider)
	tokenResp, err := oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		log.Printf("handleOAuthCallback: ExchangeCode failed: %v", err)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", origin, fmt.Sprintf("Failed to exchange code: %v", err))
		return
	}

	log.Printf("handleOAuthCallback: token exchange successful. Fetching user info...")
	username, err := oauth.GetUserInfo(ctx, provider, tokenResp.AccessToken)
	if err != nil {
		log.Printf("handleOAuthCallback: GetUserInfo failed (defaulting to OAuth User): %v", err)
		username = "OAuth User"
	}

	log.Printf("handleOAuthCallback: rendering successful login for user %q", username)
	s.renderOAuthResultHTML(w, provider, tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, username, origin)
}

func (s *APIServer) renderOAuthResultHTML(w http.ResponseWriter, provider, token, refreshToken string, expiresIn int, username, targetOrigin string, errorMsg ...string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var errStr string
	if len(errorMsg) > 0 {
		errStr = errorMsg[0]
	}

	var script string
	if errStr != "" {
		script = fmt.Sprintf(`
			console.log("OAuth error occurred:", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null on error page!");
				} else {
					window.opener.postMessage({
						type: "oauth-error",
						error: %q
					}, %q);
				}
			} catch (e) {
				console.error("Failed to post oauth-error:", e);
			}
			// Don't close immediately so the user can read the error if it fails to post
			setTimeout(() => { window.close(); }, 1000);
		`, errStr, errStr, targetOrigin)
	} else {
		script = fmt.Sprintf(`
			console.log("OAuth successful. Sending credentials to opener at", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null!");
					var errMsg = document.createElement("p");
					errMsg.style.color = "red";
					errMsg.style.fontWeight = "bold";
					errMsg.style.marginTop = "15px";
					errMsg.innerText = "Fehler: window.opener ist null. Bitte überprüfe deine Browser-Sicherheitseinstellungen (z.B. Pop-up-Blocker oder Brave Shields).";
					document.querySelector(".card").appendChild(errMsg);
				} else {
					window.opener.postMessage({
						type: "oauth-success",
						provider: %q,
						token: %q,
						refreshToken: %q,
						expiresIn: %d,
						username: %q
					}, %q);
					console.log("postMessage sent successfully.");
					window.close();
				}
			} catch (e) {
				console.error("Failed to post oauth-success:", e);
				var errMsg = document.createElement("p");
				errMsg.style.color = "red";
				errMsg.innerText = "Fehler beim Senden der Anmeldedaten: " + e.message;
				document.querySelector(".card").appendChild(errMsg);
			}
		`, targetOrigin, provider, token, refreshToken, expiresIn, username, targetOrigin)
	}

	fmt.Fprintf(w, `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Authorization Status</title>
			<style>
				body {
					font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
					display: flex;
					align-items: center;
					justify-content: center;
					height: 100vh;
					margin: 0;
					background-color: #f8fafc;
					color: #334155;
				}
				.card {
					background: white;
					padding: 2rem;
					border-radius: 8px;
					box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1);
					text-align: center;
				}
			</style>
		</head>
		<body>
			<div class="card">
				%s
			</div>
			<script>%s</script>
		</body>
		</html>
	`, func() string {
		if errStr != "" {
			return fmt.Sprintf("<h3 style='color: #ef4444;'>Authorization Failed</h3><p>%s</p>", html.EscapeString(errStr))
		}
		return "<h3>Authorization Successful</h3><p>You can close this window now.</p>"
	}(), script)
}

func hashToken(token string) string {
	h := sha256.New()
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		http.Error(w, "Email, password, and display name are required", http.StatusBadRequest)
		return
	}

	// Verify if user already exists
	_, err := db.GetUserByEmail(s.db, req.Email)
	if err == nil {
		// User found — reject duplicate
		http.Error(w, "User with this email already exists", http.StatusConflict)
		return
	}
	if err != sql.ErrNoRows {
		// Unexpected DB error — do not proceed with registration
		log.Printf("Error checking existing user for %s: %v\n", req.Email, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Hash password
	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "Internal server error during password hashing", http.StatusInternalServerError)
		return
	}

	// Create user
	u, err := db.CreateUser(s.db, req.Email, passHash, req.DisplayName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create user: %v", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, u)
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *APIServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		http.Error(w, "Email and password are required", http.StatusBadRequest)
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	if !auth.CheckPasswordHash(req.Password, u.PasswordHash) {
		http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	// Access Token (15 mins)
	accessToken, err := auth.GenerateAccessToken(u, s.jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate access token", http.StatusInternalServerError)
		return
	}

	// Refresh Token (7 days)
	refreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		http.Error(w, "Failed to generate refresh token", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	tokenHash := hashToken(refreshToken)

	err = db.StoreRefreshToken(s.db, tokenHash, u.ID, expiresAt)
	if err != nil {
		http.Error(w, "Failed to store refresh token", http.StatusInternalServerError)
		return
	}

	auth.SetRefreshTokenCookie(w, r, refreshToken, expiresAt)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":         u,
		"access_token": accessToken,
	})
}

func (s *APIServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		http.Error(w, "Unauthorized: Refresh token missing", http.StatusUnauthorized)
		return
	}

	oldTokenHash := hashToken(cookie.Value)
	userID, err := db.GetUserIDByRefreshToken(s.db, oldTokenHash)
	if err != nil {
		http.Error(w, "Unauthorized: Invalid or expired refresh token", http.StatusUnauthorized)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Rotate refresh token: invalidate old token before issuing new one
	if err := db.DeleteRefreshToken(s.db, oldTokenHash); err != nil {
		log.Printf("Error: failed to delete old refresh token during rotation: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	newRefreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		http.Error(w, "Failed to generate refresh token", http.StatusInternalServerError)
		return
	}
	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)
	if err := db.StoreRefreshToken(s.db, hashToken(newRefreshToken), u.ID, newExpiresAt); err != nil {
		http.Error(w, "Failed to store refresh token", http.StatusInternalServerError)
		return
	}
	auth.SetRefreshTokenCookie(w, r, newRefreshToken, newExpiresAt)

	// New Access Token
	accessToken, err := auth.GenerateAccessToken(u, s.jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate access token", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": accessToken,
	})
}

func (s *APIServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		tokenHash := hashToken(cookie.Value)
		_ = db.DeleteRefreshToken(s.db, tokenHash)
	}

	auth.ClearRefreshTokenCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// RunOAuthRotationDaemon is the PRD-12 background goroutine.
// It scans every 5 minutes for active migrations whose OAuth access token expires
// within the next 15 minutes and proactively refreshes them using the stored
// refresh token, ensuring long-running jobs never hit a 401.
func (s *APIServer) RunOAuthRotationDaemon(ctx context.Context) {
	log.Println("[OAuthDaemon] Started. Scanning every 5 minutes for expiring tokens...")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[OAuthDaemon] Shutting down.")
			return
		case <-ticker.C:
			s.rotateExpiringOAuthTokens(ctx)
		}
	}
}

func (s *APIServer) rotateExpiringOAuthTokens(ctx context.Context) {
	expiring, err := db.GetExpiringOAuthMigrations(s.db)
	if err != nil {
		log.Printf("[OAuthDaemon] Error querying expiring tokens: %v\n", err)
		return
	}

	for _, entry := range expiring {
		// Decrypt refresh token — happens immediately before the HTTP call (Zero Plaintext rule)
		refreshToken, err := crypto.Decrypt(entry.RefreshTokenEncrypted, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to decrypt refresh token for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		tokenResp, err := oauth.RefreshToken(refreshCtx, entry.Provider, refreshToken)
		cancel()

		if err != nil {
			log.Printf("[OAuthDaemon] Refresh failed for migration %s (%s provider=%s): %v — marking INVALID\n",
				entry.MigrationID, entry.Role, entry.Provider, err)
			// F-05: mark connection as invalid so workers stop retrying
			errMsg := fmt.Sprintf("OAuth token refresh failed (%s): %v", entry.Provider, err)
			_ = db.UpdateMigrationStatus(s.db, entry.MigrationID, "FAILED", &errMsg)
			continue
		}

		// Encrypt new tokens immediately after receipt (Zero Plaintext rule)
		newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new access token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}
		newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new refresh token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}

		expiresIn := tokenResp.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

		// Atomically overwrite old tokens (Token Rotation Constraint F-03)
		err = db.UpdateMigrationOAuthTokens(s.db, db.OAuthTokenUpdate{
			MigrationID:           entry.MigrationID,
			Role:                  entry.Role,
			AccessTokenEncrypted:  newAccessEnc,
			RefreshTokenEncrypted: newRefreshEnc,
			ExpiresAt:             newExpiresAt,
		})
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to persist new tokens for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		log.Printf("[OAuthDaemon] Successfully rotated %s OAuth token for migration %s (provider=%s, new_expires_at=%s)\n",
			entry.Role, entry.MigrationID, entry.Provider, newExpiresAt.Format(time.RFC3339))
	}
}
func (s *APIServer) handleMe(w http.ResponseWriter, r *http.Request) {
	// All user data is already embedded in the validated JWT claims;
	// no extra DB round-trip needed for a simple /me endpoint.
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":           claims.UserID,
		"email":        claims.Email,
		"display_name": claims.DisplayName,
		"role":         claims.Role,
	})
}

func (s *APIServer) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	list, err := db.GetMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("Error listing migrations for user %s: %v\n", userID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *APIServer) handleDeleteMigration(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	// Verify ownership
	owned, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !owned {
		http.Error(w, "Forbidden: You do not own this migration", http.StatusForbidden)
		return
	}

	// Cascade delete
	err = db.DeleteMigrationCascade(s.db, id)
	if err != nil {
		log.Printf("Error deleting migration %s: %v\n", id, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
