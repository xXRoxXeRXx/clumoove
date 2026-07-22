package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return false
		}
		return allowedOrigins[origin]
	},
}

type BrowseRequest struct {
	SourceURL       string `json:"source_url"`
	SourceUsername  string `json:"source_username"`
	SourcePassword  string `json:"source_password"`
	SourceProvider  string `json:"source_provider"`
	SourceProfileID string `json:"source_profile_id"`
	ResourceType    string `json:"resource_type"`
	Path            string `json:"path"`
}

func normalizeProviderURL(provider, urlStr string) string {
	if provider == "magentacloud" {
		return "https://magentacloud.de/remote.php/webdav"
	}
	return urlStr
}

func (s *APIServer) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req BrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.SourceProfileID != "" {
		src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
			Provider: req.SourceProvider,
			URL:      req.SourceURL,
			Username: req.SourceUsername,
			Password: req.SourcePassword,
		})
		if err == nil {
			req.SourceProvider = src.Provider
			req.SourceURL = src.URL
			req.SourceUsername = src.Username
			req.SourcePassword = src.Password
		}
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	if req.ResourceType != "calendars" && req.ResourceType != "contacts" && req.ResourceType != "files" {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ok, err := sourceClient.Connect(ctx)
	if !ok {
		log.Printf("handleBrowse: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	items, err := sourceClient.GetDirectoryListing(ctx, req.ResourceType, reqPath)
	if err != nil {
		log.Printf("handleBrowse: failed to list %s for path %s (provider %s): %v", req.ResourceType, reqPath, req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":    false,
			"error_code": ErrListFailed,
		})
		return
	}

	var collections []storage.CloudResource
	for _, item := range items {
		if req.ResourceType == "files" || req.ResourceType == "calendars" || req.ResourceType == "contacts" || item.IsDir {
			collections = append(collections, item)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"items":   collections,
		"files":   collections,
	})
}

type TargetBrowseRequest struct {
	TargetURL       string `json:"target_url"`
	TargetUsername  string `json:"target_username"`
	TargetPassword  string `json:"target_password"`
	TargetProvider  string `json:"target_provider"`
	TargetProfileID string `json:"target_profile_id"`
	Path            string `json:"path"`
}

func (s *APIServer) handleTargetBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetBrowseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}

	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
			Provider: req.TargetProvider,
			URL:      req.TargetURL,
			Username: req.TargetUsername,
			Password: req.TargetPassword,
		})
		if err != nil {
			log.Printf("handleTargetBrowse: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		log.Printf("handleTargetBrowse: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	files, err := targetClient.GetDirectoryListing(ctx, "files", reqPath)
	if err != nil {
		log.Printf("handleTargetBrowse: failed to list target files for path %s (provider %s): %v", reqPath, req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrListFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"files":   files,
	})
}

type TargetMkdirRequest struct {
	TargetURL       string `json:"target_url"`
	TargetUsername  string `json:"target_username"`
	TargetPassword  string `json:"target_password"`
	TargetProvider  string `json:"target_provider"`
	TargetProfileID string `json:"target_profile_id"`
	Path            string `json:"path"`
}

func (s *APIServer) handleTargetMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetMkdirRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if req.Path == "" || req.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrFolderPathInvalid})
		return
	}

	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
			Provider: req.TargetProvider,
			URL:      req.TargetURL,
			Username: req.TargetUsername,
			Password: req.TargetPassword,
		})
		if err != nil {
			log.Printf("handleTargetMkdir: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		log.Printf("handleTargetMkdir: connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	err = targetClient.CreateDirectory(ctx, "files", req.Path)
	if err != nil {
		log.Printf("handleTargetMkdir: CreateDirectory(%s) failed: %v", req.Path, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrFolderCreateFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
	})
}

func (s *APIServer) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "RUNNING" && mig.Status != "INDEXING" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "PAUSED", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationPaused, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "PAUSED" && mig.Status != "PAUSED_CONNECTION_LOSS" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "RUNNING", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationResumed, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleRetryFailed(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "COMPLETED" && mig.Status != "COMPLETED_WITH_ERRORS" && mig.Status != "FAILED" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	count, err := db.ResetFailedTasksForRetry(s.db, r.Context(), id)
	if err != nil {
		log.Printf("Error resetting failed tasks for retry: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "retried": count})
}

func (s *APIServer) handleReindex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig.Status != "FAILED" && mig.Status != "COMPLETED_WITH_ERRORS" {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}

	if err := db.ResetMigrationForReindex(s.db, r.Context(), id); err != nil {
		if errors.Is(err, db.ErrMigrationNotFailed) {
			writeError(w, http.StatusConflict, ErrMigrationReindexConflict)
			return
		}
		log.Printf("Reindex error for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	go s.indexer.Start(s.ctx, id)

	log.Printf("Migration %s re-index triggered.\n", id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true, "migration_id": id})
}

func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	err = db.UpdateMigrationStatus(s.db, id, "CANCELLED", nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	err = db.CancelPendingTasks(s.db, id)
	if err != nil {
		log.Printf("Warning: failed to cancel pending tasks for migration %s: %v", id, err)
	}

	if err := s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		log.Printf("Warning: failed to publish cancel event for migration %s: %v — in-flight tasks will be aborted via DB status check", id, err)
	}

	s.writeAudit(r, db.AuditMigrationCancelled, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type BandwidthRequest struct {
	LimitMbps int `json:"limit_mbps"`
}

type ThreadsRequest struct {
	Threads int `json:"threads"`
}

func (s *APIServer) handleSetThreads(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req ThreadsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateMigrationThreads(s.db, id, threads); err != nil {
		log.Printf("Error updating threads for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleSetBandwidth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owns, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req BandwidthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.LimitMbps < 0 || req.LimitMbps > 1000 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}

	if err := db.UpdateMigrationBandwidthLimit(s.db, id, req.LimitMbps); err != nil {
		log.Printf("Error updating bandwidth limit for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	mig, err := db.GetMigration(s.db, id)
	if err == nil {
		switch mig.Status {
		case "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED":
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
	SourceURL             string `json:"source_url"`
	SourceUsername        string `json:"source_username"`
	SourcePassword        string `json:"source_password"`
	SourceRefreshToken    string `json:"source_refresh_token"`
	SourceTokenExpiresIn  int    `json:"source_token_expires_in"`
	TargetURL             string `json:"target_url"`
	TargetUsername        string `json:"target_username"`
	TargetPassword        string `json:"target_password"`
	TargetRefreshToken    string `json:"target_refresh_token"`
	TargetTokenExpiresIn  int    `json:"target_token_expires_in"`
	SourceProvider        string `json:"source_provider"`
	TargetProvider        string `json:"target_provider"`
	SourcePickerSessionID string `json:"source_picker_session_id"`
	Path                  string `json:"path"`
	ResourceType          string `json:"resource_type"`
	SourceProfileID       string `json:"source_profile_id"`
	TargetProfileID       string `json:"target_profile_id"`
}

func (s *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
		Provider:     req.SourceProvider,
		URL:          req.SourceURL,
		Username:     req.SourceUsername,
		Password:     req.SourcePassword,
		RefreshToken: req.SourceRefreshToken,
	})
	if err != nil {
		log.Printf("handleConnect: failed to load source profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.SourceProvider = src.Provider
	req.SourceURL = src.URL
	req.SourceUsername = src.Username
	if req.SourcePassword == "" {
		req.SourcePassword = src.Password
	}
	if req.SourceRefreshToken == "" {
		req.SourceRefreshToken = src.RefreshToken
	}

	tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider:     req.TargetProvider,
		URL:          req.TargetURL,
		Username:     req.TargetUsername,
		Password:     req.TargetPassword,
		RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		log.Printf("handleConnect: failed to load target profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.TargetProvider = tgt.Provider
	req.TargetURL = tgt.URL
	req.TargetUsername = tgt.Username
	if req.TargetPassword == "" {
		req.TargetPassword = tgt.Password
	}
	if req.TargetRefreshToken == "" {
		req.TargetRefreshToken = tgt.RefreshToken
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)
	if req.ResourceType == "" {
		req.ResourceType = "files"
	}

	if !storage.IsValidProvider(req.SourceProvider) || !storage.IsValidProvider(req.TargetProvider) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error_code": ErrProviderUnsupported})
		return
	}

	sourceClient, err := storage.NewProvider(r.Context(), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()
	srcCtx, srcCancel := context.WithTimeout(r.Context(), 15*time.Second)
	sourceOK, err := sourceClient.Connect(srcCtx)
	srcCancel()
	if !sourceOK {
		log.Printf("handleConnect: source connection failed for provider %s: %v", req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	targetClient, err := storage.NewProvider(r.Context(), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()
	tgtCtx, tgtCancel := context.WithTimeout(r.Context(), 15*time.Second)
	targetOK, err := targetClient.Connect(tgtCtx)
	tgtCancel()
	if !targetOK {
		log.Printf("handleConnect: target connection failed for provider %s: %v", req.TargetProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}
	listCtx, listCancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer listCancel()
	files, err := sourceClient.GetDirectoryListing(listCtx, req.ResourceType, reqPath)
	if err != nil {
		log.Printf("handleConnect: failed to list source files for path %s (provider %s): %v", reqPath, req.SourceProvider, err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrListFailed})
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
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	src, err := s.loadProfile(r, req.SourceProfileID, profileCreds{
		Provider:     req.SourceProvider,
		URL:          req.SourceURL,
		Username:     req.SourceUsername,
		Password:     req.SourcePassword,
		RefreshToken: req.SourceRefreshToken,
	})
	if err != nil {
		log.Printf("handleStart: failed to load source profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.SourceProvider = src.Provider
	req.SourceURL = src.URL
	req.SourceUsername = src.Username
	if req.SourcePassword == "" {
		req.SourcePassword = src.Password
	}
	if req.SourceRefreshToken == "" {
		req.SourceRefreshToken = src.RefreshToken
	}

	tgt, err := s.loadProfile(r, req.TargetProfileID, profileCreds{
		Provider:     req.TargetProvider,
		URL:          req.TargetURL,
		Username:     req.TargetUsername,
		Password:     req.TargetPassword,
		RefreshToken: req.TargetRefreshToken,
	})
	if err != nil {
		log.Printf("handleStart: failed to load target profile: %v", err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	req.TargetProvider = tgt.Provider
	req.TargetURL = tgt.URL
	req.TargetUsername = tgt.Username
	if req.TargetPassword == "" {
		req.TargetPassword = tgt.Password
	}
	if req.TargetRefreshToken == "" {
		req.TargetRefreshToken = tgt.RefreshToken
	}

	if len(req.Paths) == 0 && len(req.Calendars) == 0 && len(req.Contacts) == 0 {
		writeError(w, http.StatusBadRequest, ErrNoSourcePaths)
		return
	}

	if req.SourceProvider == "" {
		req.SourceProvider = "nextcloud"
	}
	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)

	if req.SourceProvider == "magentacloud" && (len(req.Calendars) > 0 || len(req.Contacts) > 0) {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}
	if req.TargetProvider == "magentacloud" && (len(req.Calendars) > 0 || len(req.Contacts) > 0) {
		writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
		return
	}

	if err := storage.ValidateProviderURL(req.SourceProvider, req.SourceURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrSourceUrlInvalid)
		return
	}
	if err := storage.ValidateProviderURL(req.TargetProvider, req.TargetURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrTargetUrlInvalid)
		return
	}

	targetDir := req.TargetDir
	if targetDir == "" {
		targetDir = "/"
	}

	sourcePassEnc, err := crypto.Encrypt(req.SourcePassword, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	targetPassEnc, err := crypto.Encrypt(req.TargetPassword, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	var sourceRefreshEnc sql.NullString
	var sourceTokenExpiresAt sql.NullTime
	if req.SourceRefreshToken != "" {
		enc, err := crypto.Encrypt(req.SourceRefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
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
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		targetRefreshEnc = sql.NullString{String: enc, Valid: true}
		expiresIn := req.TargetTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		targetTokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	userID := auth.GetUserIDFromContext(r.Context())

	active, err := db.CountActiveMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("handleStart: failed to count active migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if active >= maxActiveMigrations {
		writeError(w, http.StatusConflict, ErrTooManyActiveMigrations)
		return
	}

	threads := req.Threads
	if threads < 1 {
		threads = 4
	} else if threads > 16 {
		threads = 16
	}

	bandwidthLimit := req.BandwidthLimitMbps
	if bandwidthLimit < 0 {
		bandwidthLimit = 0
	} else if bandwidthLimit > 1000 {
		bandwidthLimit = 1000
	}

	initialStatus := "INDEXING"
	var scheduledAt time.Time
	if req.ScheduledTime != "" {
		var err error
		scheduledAt, err = time.Parse(time.RFC3339, req.ScheduledTime)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrInvalidScheduledTime)
			return
		}
		if scheduledAt.Before(time.Now()) {
			writeError(w, http.StatusBadRequest, ErrScheduledTimePast)
			return
		}
		initialStatus = "SCHEDULED"
	}

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
		PickerSessionID:             req.SourcePickerSessionID,
	}

	migrationID, err := db.CreateMigration(s.db, m)
	if err != nil {
		log.Printf("Start migration error: failed to create migration: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationCreated, migrationID, userID, map[string]interface{}{
		"source_provider": m.SourceProvider,
		"target_provider": m.TargetProvider,
		"scheduled":       req.ScheduledTime != "",
	})

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
			writeError(w, http.StatusInternalServerError, ErrInternalError)
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

	go s.indexer.Start(s.ctx, migrationID)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
}

func (s *APIServer) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	list, err := db.GetMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("Error listing migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *APIServer) handleMigrationStream(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())

	if !s.rateLimiter.Allow(s.clientIP(r), streamRateLimit, streamRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	s.streamMu.Lock()
	if s.activeStreams[userID] >= maxStreamsPerUser {
		s.streamMu.Unlock()
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	s.activeStreams[userID]++
	s.streamMu.Unlock()
	defer func() {
		s.streamMu.Lock()
		s.activeStreams[userID]--
		if s.activeStreams[userID] <= 0 {
			delete(s.activeStreams, userID)
		}
		s.streamMu.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(payload []byte) error {
		if _, err := fmt.Fprintf(w, "event: migrations\ndata: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	writeErrorEvent := func(code APIErrorCode) error {
		if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", code); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	initial, err := db.GetMigrationsForUser(s.db, userID)
	if err != nil {
		log.Printf("Migration stream initial load error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	prev, err := json.Marshal(initial)
	if err != nil {
		log.Printf("Migration stream initial marshal error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	if err := writeEvent(prev); err != nil {
		log.Printf("Migration stream initial write error for user %s: %v\n", userID, err)
		return
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	keepaliveTicker := time.NewTicker(20 * time.Second)
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepaliveTicker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			list, err := db.GetMigrationsForUser(s.db, userID)
			if err != nil {
				log.Printf("Migration stream reload error for user %s: %v\n", userID, err)
				return
			}
			cur, err := json.Marshal(list)
			if err != nil {
				log.Printf("Migration stream marshal error for user %s: %v\n", userID, err)
				return
			}
			if !bytes.Equal(cur, prev) {
				if err := writeEvent(cur); err != nil {
					log.Printf("Migration stream write error for user %s: %v\n", userID, err)
					return
				}
				prev = cur
			}
		}
	}
}

func (s *APIServer) handleDeleteMigration(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	owned, err := db.VerifyMigrationOwnership(s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !owned {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	err = db.DeleteMigrationCascade(s.db, id)
	if err != nil {
		log.Printf("Error deleting migration %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	err = db.DeleteSchedulesForTask(s.db, "migration", id)
	if err != nil {
		log.Printf("Warning: failed to delete schedules for migration %s: %v\n", id, err)
	}

	s.writeAudit(r, db.AuditMigrationDeleted, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			log.Printf("Error fetching migration %s: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
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
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID := auth.GetUserIDFromContext(r.Context())

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			log.Printf("Error fetching migration %s for report: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	tasks, err := db.GetFailedTasksForReport(s.db, id)
	if err != nil {
		log.Printf("Download report error: failed to get report: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=migration_report_%s.csv", id))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	_ = writer.Write([]string{"File Path", "Size (Bytes)", "Retries", "WebDAV Error Message"})

	for _, task := range tasks {
		errMsg := ""
		if task.ErrorMessage.Valid {
			errMsg = task.ErrorMessage.String
		}
		_ = writer.Write([]string{
			csvCell(task.FilePath),
			fmt.Sprintf("%d", task.FileSize),
			fmt.Sprintf("%d", task.Attempts),
			csvCell(errMsg),
		})
	}

	indexErrs, err := db.GetIndexingErrorsForReport(s.db, id)
	if err != nil {
		log.Printf("Download report error: failed to get indexing errors: %v\n", err)
	} else {
		for _, ie := range indexErrs {
			_ = writer.Write([]string{
				csvCell(ie.Path),
				"0",
				"",
				csvCell(fmt.Sprintf("[indexing/%s] %s", ie.ResourceType, ie.ErrorMessage)),
			})
		}
	}
}

func (s *APIServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
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
		queryToken := r.URL.Query().Get("token")
		if queryToken != "" {
			isHTTPS := s.isSecure(r)
			if isHTTPS {
				writeError(w, http.StatusUnauthorized, ErrWsTokenInsecure)
				return
			}
			tokenStr = queryToken
		}
	}

	if tokenStr == "" {
		writeError(w, http.StatusUnauthorized, ErrWsTokenMissing)
		return
	}

	claims, err := auth.ValidateToken(tokenStr, s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrWsTokenInvalid)
		return
	}
	if err := auth.RequireAuthenticated(claims); err != nil {
		writeError(w, http.StatusUnauthorized, ErrTotpRequired)
		return
	}
	userID := claims.UserID

	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var responseHeader http.Header
	if isProtocolToken {
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

	ws.SetReadLimit(512)
	ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		return nil
	})

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
			mig, err = db.GetMigration(s.db, id)
			if err != nil {
				return
			}

			activeFiles, _ := db.GetActiveTaskPaths(s.db, r.Context(), id)
			var activeFile string
			if len(activeFiles) > 0 {
				activeFile = activeFiles[0]
			}

			responsePayload := map[string]interface{}{
				"id":                   mig.ID,
				"status":               mig.Status,
				"source_provider":      mig.SourceProvider,
				"source_url":           mig.SourceURL,
				"target_provider":      mig.TargetProvider,
				"target_url":           mig.TargetURL,
				"target_dir":           mig.TargetDir,
				"selected_paths":       mig.SelectedPaths,
				"selected_calendars":   mig.SelectedCalendars,
				"selected_contacts":    mig.SelectedContacts,
				"created_at":           mig.CreatedAt,
				"total_files":          mig.TotalFiles,
				"total_bytes":          mig.TotalBytes,
				"processed_files":      mig.ProcessedFiles,
				"processed_bytes":      mig.ProcessedBytes,
				"live_bytes":           mig.LiveBytes,
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

			data, err := json.Marshal(responsePayload)
			if err != nil {
				return
			}

			ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			err = ws.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				return
			}

			if (mig.Status == "COMPLETED" || mig.Status == "COMPLETED_WITH_ERRORS" || mig.Status == "FAILED") && mig.ProcessedFiles >= mig.TotalFiles {
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
