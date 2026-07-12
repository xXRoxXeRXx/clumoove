package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
	"backend/internal/indexer"
	"backend/internal/oauth"
	"backend/internal/queue"
	"backend/internal/scheduler"
	"backend/internal/storage"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		return allowedOrigins[origin]
	},
}

type APIServer struct {
	db            *sql.DB
	queue         *queue.Queue
	indexer       *indexer.Indexer
	encryptionKey string // AES key for credential encryption
	jwtSecret     string // HMAC key for JWT signing (separate from encryptionKey)
	ctx           context.Context
	rateLimiter   ipRateLimiter
}


type ipRateLimiter struct {
	mu      sync.Mutex
	visitors map[string]*rateVisitor
}

type rateVisitor struct {
	count    int
	resetAt  time.Time
}

func (rl *ipRateLimiter) Allow(ip string, maxRequests int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	v, ok := rl.visitors[ip]
	if !ok || now.After(v.resetAt) {
		rl.visitors[ip] = &rateVisitor{count: 1, resetAt: now.Add(window)}
		return true
	}
	if v.count >= maxRequests {
		return false
	}
	v.count++
	return true
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

	if subtle.ConstantTimeCompare([]byte(encryptionKey), []byte(jwtSecret)) == 1 {
		log.Fatal("ENCRYPTION_SECRET_KEY and JWT_SECRET_KEY must be different to maintain cryptographic key segregation.")
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
		indexer:       indexer.NewIndexer(database, encryptionKey),
		encryptionKey: encryptionKey,
		jwtSecret:     jwtSecret,
		ctx:           ctx,
		rateLimiter:   ipRateLimiter{visitors: make(map[string]*rateVisitor)},
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
	mux.HandleFunc("GET /api/settings", server.handleGetSettings)

	// Protected Auth Routes
	jwtMiddleware := auth.AuthMiddleware(server.jwtSecret)
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleMe)))
	mux.Handle("PUT /api/auth/me", jwtMiddleware(http.HandlerFunc(server.handleUpdateProfile)))
	mux.Handle("POST /api/auth/change-password", jwtMiddleware(http.HandlerFunc(server.handleChangePassword)))
	mux.Handle("POST /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleSetAvatar)))
	mux.Handle("DELETE /api/user/avatar", jwtMiddleware(http.HandlerFunc(server.handleDeleteAvatar)))
	mux.Handle("PUT /api/settings", jwtMiddleware(http.HandlerFunc(server.handleUpdateSetting)))
	mux.Handle("GET /api/settings/smtp", jwtMiddleware(http.HandlerFunc(server.handleGetSMTPSettings)))
	mux.Handle("PUT /api/settings/smtp", jwtMiddleware(http.HandlerFunc(server.handleUpdateSMTPSettings)))
	mux.Handle("POST /api/settings/smtp/test", jwtMiddleware(http.HandlerFunc(server.handleTestSMTP)))

	mux.HandleFunc("GET /api/auth/password-reset-available", server.handlePasswordResetAvailable)
	mux.HandleFunc("POST /api/auth/forgot-password", server.handleForgotPassword)
	mux.HandleFunc("POST /api/auth/reset-password", server.handleResetPassword)

	mux.Handle("GET /api/migration", jwtMiddleware(http.HandlerFunc(server.handleListMigrations)))
	mux.Handle("POST /api/migration/connect", jwtMiddleware(http.HandlerFunc(server.handleConnect)))
	mux.Handle("POST /api/migration/browse", jwtMiddleware(http.HandlerFunc(server.handleBrowse)))
	mux.Handle("POST /api/migration/target/browse", jwtMiddleware(http.HandlerFunc(server.handleTargetBrowse)))
	mux.Handle("POST /api/migration/target/mkdir", jwtMiddleware(http.HandlerFunc(server.handleTargetMkdir)))
	mux.Handle("POST /api/migration/start", jwtMiddleware(http.HandlerFunc(server.handleStart)))
	mux.Handle("GET /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetStatus)))
	mux.Handle("POST /api/migration/{id}/pause", jwtMiddleware(http.HandlerFunc(server.handlePause)))
	mux.Handle("POST /api/migration/{id}/resume", jwtMiddleware(http.HandlerFunc(server.handleResume)))
	mux.Handle("POST /api/migration/{id}/cancel", jwtMiddleware(http.HandlerFunc(server.handleCancel)))
	mux.Handle("DELETE /api/migration/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteMigration)))
	mux.Handle("GET /api/migration/{id}/report", jwtMiddleware(http.HandlerFunc(server.handleDownloadReport)))
	mux.Handle("POST /api/migration/{id}/retry-failed", jwtMiddleware(http.HandlerFunc(server.handleRetryFailed)))
	mux.Handle("PUT /api/migration/{id}/bandwidth", jwtMiddleware(http.HandlerFunc(server.handleSetBandwidth)))

	// Schedule Management Routes (Protected)
	mux.Handle("GET /api/schedule", jwtMiddleware(http.HandlerFunc(server.handleListSchedules)))
	mux.Handle("GET /api/schedule/{id}", jwtMiddleware(http.HandlerFunc(server.handleGetSchedule)))
	mux.Handle("DELETE /api/schedule/{id}", jwtMiddleware(http.HandlerFunc(server.handleDeleteSchedule)))

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

	// Start the Core Scheduler Engine Daemon
	sched := scheduler.NewScheduler(database, q, server.indexer)
	go sched.Run(ctx)

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
		if origin != "" {
			if !allowedOrigins[origin] {
				http.Error(w, "Forbidden: untrusted origin", http.StatusForbidden)
				return
			}
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
	ResourceType   string `json:"resource_type"` // "calendars", "contacts", or "files"
	Path           string `json:"path"`
}

// handleBrowse lists the top-level calendar collections or addressbooks, or files/directories on the source server.
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
	if req.ResourceType != "calendars" && req.ResourceType != "contacts" && req.ResourceType != "files" {
		http.Error(w, "resource_type must be 'calendars', 'contacts', or 'files'", http.StatusBadRequest)
		return
	}

	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid source URL format"})
		return
	}
	defer sourceClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ok, err := sourceClient.Connect(ctx)
	if !ok {
		log.Printf("handleBrowse: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Source connection failed. Check URL and credentials."})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	// List the requested path for files, or root "/" for calendars/contacts
	items, err := sourceClient.GetDirectoryListing(ctx, req.ResourceType, reqPath)
	if err != nil {
		log.Printf("handleBrowse: failed to list %s for path %s (provider %s): %v", req.ResourceType, reqPath, req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to list %s. Check URL and credentials.", req.ResourceType),
		})
		return
	}

	// For files, return all resources. For calendars/contacts, only return directories (collections).
	var collections []storage.CloudResource
	for _, item := range items {
		if req.ResourceType == "files" || item.IsDir {
			collections = append(collections, item)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"items":   collections,
		"files":   collections, // back-compat for frontend using data.files
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
	defer targetClient.Close()

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
		log.Printf("handleTargetBrowse: failed to list target files for path %s (provider %s): %v", reqPath, req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Failed to list target files. Check URL and credentials."})
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
	defer targetClient.Close()

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

func (s *APIServer) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		http.Error(w, "Failed to fetch migration", http.StatusInternalServerError)
		return
	}
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		http.Error(w, "Migration cannot be paused in its current state", http.StatusConflict)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "PAUSED", nil)
	if err != nil {
		http.Error(w, "Failed to pause migration", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		http.Error(w, "Failed to fetch migration", http.StatusInternalServerError)
		return
	}
	if mig.Status != "PAUSED" && mig.Status != "PAUSED_CONNECTION_LOSS" {
		http.Error(w, "Migration cannot be resumed in its current state", http.StatusConflict)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "RUNNING", nil)
	if err != nil {
		http.Error(w, "Failed to resume migration", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		http.Error(w, "Failed to fetch migration", http.StatusInternalServerError)
		return
	}
	if mig.Status != "COMPLETED" && mig.Status != "FAILED" {
		http.Error(w, "Migration cannot be retried in its current state", http.StatusConflict)
		return
	}

	count, err := db.ResetFailedTasksForRetry(s.db, id)
	if err != nil {
		log.Printf("Error resetting failed tasks for retry: %v", err)
		http.Error(w, "Failed to retry failed tasks", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "retried": count})
}

func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// 1. Update Migration Status
	err = db.UpdateMigrationStatus(s.db, id, "CANCELLED", nil)
	if err != nil {
		http.Error(w, "Failed to cancel migration", http.StatusInternalServerError)
		return
	}

	// 2. Mark pending tasks as CANCELLED
	err = db.CancelPendingTasks(s.db, id)
	if err != nil {
		log.Printf("Warning: failed to cancel pending tasks for migration %s: %v", id, err)
	}

	// 3. Publish Redis PubSub event to cancel running contexts on workers
	if err := s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		log.Printf("Warning: failed to publish cancel event for migration %s: %v — in-flight tasks will be aborted via DB status check", id, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type BandwidthRequest struct {
	LimitMbps int `json:"limit_mbps"`
}

func (s *APIServer) handleSetBandwidth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing migration ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req BandwidthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.LimitMbps < 0 {
		http.Error(w, "limit_mbps must be >= 0", http.StatusBadRequest)
		return
	}
	if req.LimitMbps > 1000 {
		http.Error(w, "limit_mbps must be <= 1000", http.StatusBadRequest)
		return
	}

	if err := db.UpdateMigrationBandwidthLimit(s.db, id, req.LimitMbps); err != nil {
		log.Printf("Error updating bandwidth limit for migration %s: %v", id, err)
		http.Error(w, "Failed to update bandwidth limit", http.StatusInternalServerError)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err == nil {
		switch mig.Status {
		case "COMPLETED", "FAILED", "CANCELLED":
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
	}

	if err := s.queue.PublishBandwidthChange(r.Context(), queue.BandwidthEvent{
		MigrationID:        id,
		BandwidthLimitMbps: req.LimitMbps,
	}); err != nil {
		log.Printf("Warning: failed to publish bandwidth change for migration %s: %v", id, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
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
	validProviders := map[string]bool{"nextcloud": true, "webdav": true, "dropbox": true, "google": true, "smb": true, "s3": true, "sftp": true}
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
	defer sourceClient.Close()
	srcCtx, srcCancel := context.WithTimeout(r.Context(), 15*time.Second)
	sourceOK, err := sourceClient.Connect(srcCtx)
	srcCancel()
	if !sourceOK {
		log.Printf("handleConnect: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Source connection failed. Check URL and credentials."})
		return
	}

	// Test Target Connection
	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Invalid target URL format"})
		return
	}
	defer targetClient.Close()
	tgtCtx, tgtCancel := context.WithTimeout(r.Context(), 15*time.Second)
	targetOK, err := targetClient.Connect(tgtCtx)
	tgtCancel()
	if !targetOK {
		log.Printf("handleConnect: target connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Target connection failed. Check URL and credentials."})
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
		log.Printf("handleConnect: failed to list source files for path %s (provider %s): %v", reqPath, req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Failed to list source files. Check URL and credentials."})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type StartRequest struct {
	ConnectRequest
	ConflictStrategy   string   `json:"conflict_strategy"`
	Paths              []string `json:"paths"`
	Calendars          []string `json:"calendars"`
	Contacts           []string `json:"contacts"`
	TargetDir          string   `json:"target_dir"`
	Threads            int      `json:"threads"`
	ScheduledTime      string   `json:"scheduled_time"`
	BandwidthLimitMbps int      `json:"bandwidth_limit_mbps"`
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

	// Validate bandwidth limit
	bandwidthLimit := req.BandwidthLimitMbps
	if bandwidthLimit < 0 {
		bandwidthLimit = 0
	} else if bandwidthLimit > 1000 {
		bandwidthLimit = 1000
	}

	// Determine initial status based on whether scheduling is requested
	initialStatus := "INDEXING"
	var scheduledAt time.Time
	if req.ScheduledTime != "" {
		var err error
		scheduledAt, err = time.Parse(time.RFC3339, req.ScheduledTime)
		if err != nil {
			http.Error(w, "Invalid scheduled_time format. Use ISO 8601 (e.g., 2026-07-15T02:00:00Z)", http.StatusBadRequest)
			return
		}
		if scheduledAt.Before(time.Now()) {
			http.Error(w, "scheduled_time must be in the future", http.StatusBadRequest)
			return
		}
		initialStatus = "SCHEDULED"
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
		Status:                      initialStatus,
		ConflictStrategy:            req.ConflictStrategy,
		TargetDir:                   targetDir,
		SelectedPaths:               db.StringArray(req.Paths),
		SelectedCalendars:           db.StringArray(req.Calendars),
		SelectedContacts:            db.StringArray(req.Contacts),
		Threads:                     threads,
		BandwidthLimitMbps:          bandwidthLimit,
	}

	migrationID, err := db.CreateMigration(s.db, m)
	if err != nil {
		log.Printf("Start migration error: failed to create migration: %v\n", err)
		http.Error(w, "Failed to start migration", http.StatusInternalServerError)
		return
	}

	// If scheduled, create a schedule entry instead of starting immediately
	if req.ScheduledTime != "" {
		schedule := &db.Schedule{
			UserID:    userID,
			TaskType:  "migration",
			TaskID:    migrationID,
			RunAt:     sql.NullTime{Time: scheduledAt, Valid: true},
			NextRunAt: sql.NullTime{Time: scheduledAt, Valid: true},
			IsActive:  true,
		}

		_, err = db.CreateSchedule(s.db, schedule)
		if err != nil {
			log.Printf("Failed to create schedule for migration %s: %v\n", migrationID, err)
			http.Error(w, "Failed to create schedule", http.StatusInternalServerError)
			return
		}

		log.Printf("Migration %s scheduled for %s\n", migrationID, scheduledAt.Format(time.RFC3339))

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"success":        true,
			"migration_id":   migrationID,
			"scheduled":      true,
			"scheduled_time": scheduledAt.Format(time.RFC3339),
		})
		return
	}

	// Spawn Background Indexer (immediate start) using shared indexer package
	go s.indexer.Start(s.ctx, migrationID)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
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
		log.Printf("Download report error: failed to get report: %v\n", err)
		http.Error(w, "Failed to generate report", http.StatusInternalServerError)
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

	tokenStr := ""
	isProtocolToken := false

	if protocol := r.Header.Get("Sec-WebSocket-Protocol"); protocol != "" {
		parts := strings.Split(protocol, ",")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				tokenStr = trimmed
				isProtocolToken = true
				break
			}
		}
	}

	if tokenStr == "" {
		// Fallback to query param, but reject on secure HTTPS connections
		queryToken := r.URL.Query().Get("token")
		if queryToken != "" {
			isHTTPS := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			if isHTTPS {
				http.Error(w, "Unauthorized: token in query string is disallowed on secure connections. Use Sec-WebSocket-Protocol subprotocol header instead.", http.StatusUnauthorized)
				return
			}
			tokenStr = queryToken
		}
	}

	if tokenStr == "" {
		http.Error(w, "Unauthorized: token missing", http.StatusUnauthorized)
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

	var responseHeader http.Header
	if isProtocolToken {
		// Echo the extracted token back in the Sec-WebSocket-Protocol header.
		// Browser WebSocket clients require the upgraded response to contain the selected
		// subprotocol from the handshake request, otherwise the browser will refuse the connection.
		responseHeader = make(http.Header)
		responseHeader.Set("Sec-WebSocket-Protocol", tokenStr)
	}

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket: %v\n", err)
		return
	}
	defer ws.Close()

	log.Printf("WebSocket client connected for migration: %s\n", id)

	// Set read limits and deadlines (Finding 11)
	ws.SetReadLimit(512) // small control frames only
	ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		return nil
	})

	// Start discard loop to process control frames (ping/pong/close)
	go func() {
		for {
			if _, _, err := ws.NextReader(); err != nil {
				break
			}
		}
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-pingTicker.C:
			if err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				log.Printf("WebSocket write ping error: %v\n", err)
				return
			}
		case <-ticker.C:
			// Fetch migration state
			mig, err = db.GetMigration(s.db, id)
			if err != nil {
				return
			}

			// Query active file paths
			activeFiles, _ := db.GetActiveTaskPaths(s.db, r.Context(), id)
			var activeFile string
			if len(activeFiles) > 0 {
				activeFile = activeFiles[0]
			}

			responsePayload := map[string]interface{}{
				"id":                   mig.ID,
				"status":               mig.Status,
				"total_files":          mig.TotalFiles,
				"total_bytes":          mig.TotalBytes,
				"processed_files":      mig.ProcessedFiles,
				"processed_bytes":      mig.ProcessedBytes,
				"skipped_files":        mig.SkippedFiles,
				"failed_files":         mig.FailedFiles,
				"error_message":        "",
				"active_file":          activeFile,
				"active_files":         activeFiles,
				"threads":              mig.Threads,
				"bandwidth_limit_mbps": mig.BandwidthLimitMbps,
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
				return
			}

			ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err = ws.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				return // Client disconnected
			}

			// If migration is in a terminal state (COMPLETED or FAILED) and all tasks finished, close socket after final state
			if (mig.Status == "COMPLETED" || mig.Status == "FAILED") && mig.ProcessedFiles >= mig.TotalFiles {
				// Pause a bit to let client read the final completed status
				time.Sleep(1 * time.Second)
				return
			}
		}
	}
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
	// Check against allowedOrigins whitelist (C1 security fix)
	if !allowedOrigins[origin] {
		log.Printf("handleOAuthAuth: rejected untrusted origin %q", origin)
		http.Error(w, "Untrusted origin: must be whitelisted", http.StatusBadRequest)
		return
	}
	log.Printf("handleOAuthAuth: final origin set to %q", origin)

	stateToken := generateRandomString(16)
	if stateToken == "" {
		log.Printf("handleOAuthAuth: Failed to generate state token")
		http.Error(w, "Failed to generate state token", http.StatusInternalServerError)
		return
	}

	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	cookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    stateToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
		MaxAge:   300,
	}
	http.SetCookie(w, cookie)

	stateParam := fmt.Sprintf("%s:%s:%s", stateToken, provider, origin)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthAuth: constructing authURL with redirectURI=%s", redirectURI)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		log.Printf("handleOAuthAuth: GetAuthURL failed: %v", err)
		http.Error(w, "Failed to generate OAuth URL", http.StatusInternalServerError)
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

	if !allowedOrigins[origin] {
		log.Printf("handleOAuthCallback: rejected untrusted origin %q in state", origin)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "http://localhost:5173", "CSRF verification failed: untrusted origin")
		return
	}

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		log.Printf("handleOAuthCallback: CSRF check failed. Cookie err: %v, stateToken: %q", err, stateToken)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "*", "CSRF verification failed: state mismatch")
		return
	}

	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	clearCookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
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
	regEnabled, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("Register error: failed to check registrations_enabled: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if regEnabled == "false" {
		http.Error(w, "Registrations are disabled", http.StatusForbidden)
		return
	}

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
	_, err = db.GetUserByEmail(s.db, req.Email)
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
		log.Printf("Register error: failed to create user: %v\n", err)
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
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

	userResp := map[string]interface{}{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"role":         u.Role,
	}
	if len(u.Avatar) > 0 {
		userResp["avatar"] = avatarDataURL(u)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":         userResp,
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

	// Rotate refresh token atomically using a database transaction
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	deleteQuery := `DELETE FROM refresh_tokens WHERE token_hash = $1`
	if _, err := tx.ExecContext(r.Context(), deleteQuery, oldTokenHash); err != nil {
		log.Printf("Error deleting old refresh token in tx: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	newRefreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		http.Error(w, "Failed to generate refresh token", http.StatusInternalServerError)
		return
	}
	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)
	newHashedToken := hashToken(newRefreshToken)

	insertQuery := `
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	if _, err := tx.ExecContext(r.Context(), insertQuery, newHashedToken, u.ID, newExpiresAt); err != nil {
		log.Printf("Error storing new refresh token in tx: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing token rotation transaction: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleMe: failed to load user %s: %v\n", userID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"role":         u.Role,
	}
	if len(u.Avatar) > 0 {
		resp["avatar"] = avatarDataURL(u)
	}
	writeJSON(w, http.StatusOK, resp)
}

func avatarDataURL(u *db.User) string {
	if len(u.Avatar) == 0 {
		return ""
	}
	mime := u.AvatarMime
	if mime == "" {
		mime = "image/png"
	}
	encoded := base64.StdEncoding.EncodeToString(u.Avatar)
	return fmt.Sprintf("data:%s;base64,%s", mime, encoded)
}

type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		http.Error(w, "Display name is required", http.StatusBadRequest)
		return
	}

	if err := db.UpdateUserDisplayName(s.db, userID, req.DisplayName); err != nil {
		log.Printf("handleUpdateProfile: failed to update display name: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "display_name": req.DisplayName})
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *APIServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.NewPassword != req.ConfirmPassword {
		http.Error(w, "New passwords do not match", http.StatusBadRequest)
		return
	}

	if len(req.NewPassword) < 8 {
		http.Error(w, "New password must be at least 8 characters long", http.StatusBadRequest)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleChangePassword: user not found: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !auth.CheckPasswordHash(req.CurrentPassword, u.PasswordHash) {
		http.Error(w, "Invalid current password", http.StatusUnauthorized)
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("handleChangePassword: hash error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := db.UpdateUserPassword(s.db, userID, newHash); err != nil {
		log.Printf("handleChangePassword: update error: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type SetAvatarRequest struct {
	Avatar string `json:"avatar"`
}

func (s *APIServer) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req SetAvatarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.Avatar, "data:") {
		http.Error(w, "Invalid avatar format: missing data prefix", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(req.Avatar, ",", 2)
	if len(parts) != 2 {
		http.Error(w, "Invalid avatar format: missing comma separator", http.StatusBadRequest)
		return
	}

	header := parts[0]
	payload := parts[1]

	if !strings.HasSuffix(header, ";base64") {
		http.Error(w, "Invalid avatar format: only base64 encoding supported", http.StatusBadRequest)
		return
	}

	mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	validMimes := map[string]bool{
		"image/png":  true,
		"image/jpeg": true,
		"image/webp": true,
		"image/gif":  true,
	}
	if !validMimes[mime] {
		http.Error(w, "Unsupported image type. Use PNG, JPEG, WebP, or GIF.", http.StatusBadRequest)
		return
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		http.Error(w, "Invalid base64 payload", http.StatusBadRequest)
		return
	}

	if len(data) > 2*1024*1024 {
		http.Error(w, "Avatar exceeds 2 MB size limit", http.StatusBadRequest)
		return
	}

	if err := db.UpdateUserAvatar(s.db, userID, data, mime); err != nil {
		log.Printf("handleSetAvatar: failed to update avatar: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"avatar":  req.Avatar,
	})
}

func (s *APIServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if err := db.DeleteUserAvatar(s.db, userID); err != nil {
		log.Printf("handleDeleteAvatar: failed to delete avatar: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	val, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("handleGetSettings: failed to fetch registrations_enabled: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if val == "" {
		val = "true"
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"registrations_enabled": val,
	})
}

type UpdateSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *APIServer) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if claims.Role != "ADMIN" {
		http.Error(w, "Forbidden: administrators only", http.StatusForbidden)
		return
	}

	var req UpdateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.Key != "registrations_enabled" {
		http.Error(w, "Forbidden setting key", http.StatusForbidden)
		return
	}

	if req.Value != "true" && req.Value != "false" {
		http.Error(w, "Invalid setting value", http.StatusBadRequest)
		return
	}

	if err := db.SetSetting(s.db, req.Key, req.Value); err != nil {
		log.Printf("handleUpdateSetting: failed to set setting: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
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

	// Cascade delete migration and associated schedules
	err = db.DeleteMigrationCascade(s.db, id)
	if err != nil {
		log.Printf("Error deleting migration %s: %v\n", id, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Delete any schedules associated with this migration
	err = db.DeleteSchedulesForTask(s.db, "migration", id)
	if err != nil {
		log.Printf("Warning: failed to delete schedules for migration %s: %v\n", id, err)
		// Non-fatal: schedules will become orphaned but won't cause issues
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// Schedule Management Handlers
// ============================================================================

// handleListSchedules returns all schedules for the authenticated user
func (s *APIServer) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	schedules, err := db.GetSchedulesForUser(s.db, userID)
	if err != nil {
		log.Printf("handleListSchedules: failed to get schedules for user %s: %v\n", userID, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Return empty array instead of null if no schedules
	if schedules == nil {
		schedules = []db.Schedule{}
	}

	writeJSON(w, http.StatusOK, schedules)
}

// handleGetSchedule returns a specific schedule if owned by the user
func (s *APIServer) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing schedule ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Verify ownership. VerifyScheduleOwnership uses EXISTS, so it never returns
	// sql.ErrNoRows. A non-owning result means the schedule either does not exist
	// or belongs to another user — return 404 in both cases to avoid leaking
	// existence/ownership information.
	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleGetSchedule: error verifying ownership: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "Schedule not found", http.StatusNotFound)
		return
	}

	schedule, err := db.GetSchedule(s.db, id)
	if err != nil {
		log.Printf("handleGetSchedule: failed to get schedule %s: %v\n", id, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, schedule)
}

// handleDeleteSchedule deletes a schedule if owned by the user
func (s *APIServer) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Missing schedule ID", http.StatusBadRequest)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Verify ownership. VerifyScheduleOwnership uses EXISTS, so it never returns
	// sql.ErrNoRows. A non-owning result means the schedule either does not exist
	// or belongs to another user — return 404 in both cases to avoid leaking
	// existence/ownership information.
	owns, err := db.VerifyScheduleOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleDeleteSchedule: error verifying ownership: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !owns {
		http.Error(w, "Schedule not found", http.StatusNotFound)
		return
	}

	err = db.DeleteSchedule(s.db, id)
	if err != nil {
		log.Printf("handleDeleteSchedule: failed to delete schedule %s: %v\n", id, err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// SMTP Settings Handlers
// ============================================================================

func (s *APIServer) handleGetSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		log.Printf("handleGetSMTPSettings: error fetching settings: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"smtp_host":            settings.SMTPHost,
		"smtp_port":            settings.SMTPPort,
		"smtp_username":        settings.SMTPUsername,
		"smtp_password_set":    true,
		"smtp_from_email":      settings.SMTPFromEmail,
		"smtp_from_name":       settings.SMTPFromName,
		"smtp_encryption":      settings.SMTPEncryption,
		"notify_on_completion": settings.NotifyOnCompletion,
	})
}

func (s *APIServer) handleUpdateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		SMTPHost           string `json:"smtp_host"`
		SMTPPort           int    `json:"smtp_port"`
		SMTPUsername       string `json:"smtp_username"`
		SMTPPassword       string `json:"smtp_password"`
		PasswordChanged    bool   `json:"password_changed"`
		SMTPFromEmail      string `json:"smtp_from_email"`
		SMTPFromName       string `json:"smtp_from_name"`
		SMTPEncryption     string `json:"smtp_encryption"`
		NotifyOnCompletion *bool  `json:"notify_on_completion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.SMTPHost == "" || req.SMTPUsername == "" || req.SMTPFromEmail == "" {
		http.Error(w, "SMTP host, username, and from email are required", http.StatusBadRequest)
		return
	}

	if err := email.ValidateSMTPHost(req.SMTPHost); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.SMTPPort < 1 || req.SMTPPort > 65535 {
		http.Error(w, "SMTP port must be between 1 and 65535", http.StatusBadRequest)
		return
	}

	switch req.SMTPEncryption {
	case "tls", "starttls", "none":
	default:
		http.Error(w, "smtp_encryption must be 'tls', 'starttls', or 'none'", http.StatusBadRequest)
		return
	}

	notify := true
	if req.NotifyOnCompletion != nil {
		notify = *req.NotifyOnCompletion
	}

	var encryptedPassword string
	if !req.PasswordChanged {
		existing, err := db.GetUserSMTPSettings(s.db, userID)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("handleUpdateSMTPSettings: error fetching existing settings: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if existing != nil {
			encryptedPassword = existing.SMTPPasswordEnc
		} else {
			http.Error(w, "SMTP password is required for initial configuration", http.StatusBadRequest)
			return
		}
	} else {
		enc, err := crypto.Encrypt(req.SMTPPassword, s.encryptionKey)
		if err != nil {
			log.Printf("handleUpdateSMTPSettings: error encrypting password: %v\n", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		encryptedPassword = enc
	}

	settings := &db.UserSMTPSettings{
		UserID:             userID,
		SMTPHost:           req.SMTPHost,
		SMTPPort:           req.SMTPPort,
		SMTPUsername:       req.SMTPUsername,
		SMTPPasswordEnc:    encryptedPassword,
		SMTPFromEmail:      req.SMTPFromEmail,
		SMTPFromName:       req.SMTPFromName,
		SMTPEncryption:     req.SMTPEncryption,
		NotifyOnCompletion: notify,
	}

	if err := db.UpsertUserSMTPSettings(s.db, settings); err != nil {
		log.Printf("handleUpdateSMTPSettings: error upserting settings: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "No SMTP settings configured"})
			return
		}
		log.Printf("handleTestSMTP: error fetching settings: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Internal server error"})
		return
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, s.encryptionKey)
	if err != nil {
		log.Printf("handleTestSMTP: error decrypting password: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Failed to decrypt SMTP password"})
		return
	}

	user, err := db.GetUserByID(s.db, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Failed to fetch user email"})
		return
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	if err := email.SendMail(smtpCfg, user.Email, "Clumove — SMTP-Test erfolgreich", email.BuildTestEmail()); err != nil {
		log.Printf("handleTestSMTP: send failed: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "SMTP test failed: check your settings"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================================================
// Password Reset Handlers
// ============================================================================

func (s *APIServer) handlePasswordResetAvailable(w http.ResponseWriter, r *http.Request) {
	available := os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM_EMAIL") != ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if !s.rateLimiter.Allow(ip, 3, 1*time.Minute) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost == "" || smtpFromEmail == "" {
		log.Printf("handleForgotPassword: SMTP not configured, skipping\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("handleForgotPassword: error fetching user: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	rawToken := generateRandomString(32)
	if rawToken == "" {
		log.Printf("handleForgotPassword: failed to generate token\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}
	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().Add(4 * time.Hour)

	if err := db.CreatePasswordResetToken(s.db, tokenHash, u.ID, expiresAt); err != nil {
		log.Printf("handleForgotPassword: error storing token: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:5173"
	}
	resetURL := fmt.Sprintf("%s/?reset-token=%s", strings.TrimRight(frontendURL, "/"), rawToken)

	smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
	if smtpEncryption == "" {
		smtpEncryption = "starttls"
	}

	smtpCfg := email.SMTPConfig{
		Host:       smtpHost,
		Port:       os.Getenv("SMTP_PORT"),
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		FromEmail:  smtpFromEmail,
		FromName:   os.Getenv("SMTP_FROM_NAME"),
		Encryption: smtpEncryption,
	}
	if smtpCfg.Port == "" {
		smtpCfg.Port = "587"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "Clumove"
	}

	htmlBody := email.BuildPasswordResetEmail(resetURL)
	if err := email.SendMail(smtpCfg, u.Email, "Clumove — Passwort zurücksetzen", htmlBody); err != nil {
		log.Printf("handleForgotPassword: error sending email: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	emailHash := sha256.Sum256([]byte(req.Email))
	log.Printf("handleForgotPassword: reset email sent (hash: %x)\n", emailHash[:8])
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr
	if !s.rateLimiter.Allow(ip, 10, 5*time.Minute) {
		http.Error(w, "Too many requests", http.StatusTooManyRequests)
		return
	}

	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	if req.Token == "" || req.NewPassword == "" {
		http.Error(w, "Token and new password are required", http.StatusBadRequest)
		return
	}

	if len(req.NewPassword) < 8 {
		http.Error(w, "Password must be at least 8 characters long", http.StatusBadRequest)
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("handleResetPassword: error hashing password: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	tokenHash := hashToken(req.Token)
	_, err = db.ClaimPasswordResetToken(s.db, tokenHash, newHash)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Invalid or expired reset token", http.StatusBadRequest)
			return
		}
		log.Printf("handleResetPassword: error claiming token: %v\n", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
