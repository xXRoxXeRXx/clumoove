package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/processor"
	"backend/internal/queue"
	"backend/internal/sanitize"
	"backend/internal/storage"
)

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
	if !s.rateLimiter.Allow(r.Context(), "browse", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req BrowseRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	sourceCreds := profileCreds{Provider: req.SourceProvider, URL: req.SourceURL, Username: req.SourceUsername, Password: req.SourcePassword}
	if req.SourceProfileID != "" {
		src, err := s.loadProfile(r, req.SourceProfileID, sourceCreds)
		if err == nil {
			req.SourceProvider = src.Provider
			req.SourceURL = src.URL
			req.SourceUsername = src.Username
			req.SourcePassword = src.Password
			sourceCreds = src
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

	sourceClient, err := storage.NewProvider(withMegaProfileSession(r.Context(), sourceCreds), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	ok, err := sourceClient.Connect(ctx)
	if !ok {
		s.logf(r, "handleBrowse: source connection failed for provider %s: %v", req.SourceProvider, err)
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	items, err := sourceClient.GetDirectoryListing(ctx, req.ResourceType, reqPath)
	if err != nil {
		s.logf(r, "handleBrowse: failed to list %s for path %s (provider %s): %v", req.ResourceType, reqPath, req.SourceProvider, err)
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
	if !s.rateLimiter.Allow(r.Context(), "target-browse", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetBrowseRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}

	targetCreds := profileCreds{Provider: req.TargetProvider, URL: req.TargetURL, Username: req.TargetUsername, Password: req.TargetPassword}
	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, targetCreds)
		if err != nil {
			s.logf(r, "handleTargetBrowse: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
		targetCreds = tgt
	}

	targetClient, err := storage.NewProvider(withMegaProfileSession(r.Context(), targetCreds), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		s.logf(r, "handleTargetBrowse: connection failed for provider %s: %v", req.TargetProvider, err)
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	reqPath := req.Path
	if reqPath == "" {
		reqPath = "/"
	}

	files, err := targetClient.GetDirectoryListing(ctx, "files", reqPath)
	if err != nil {
		s.logf(r, "handleTargetBrowse: failed to list target files for path %s (provider %s): %v", reqPath, req.TargetProvider, err)
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
	if !s.rateLimiter.Allow(r.Context(), "target-mkdir", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TargetMkdirRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.TargetProvider == "" {
		req.TargetProvider = "nextcloud"
	}
	if req.Path == "" || req.Path == "/" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrFolderPathInvalid})
		return
	}

	targetCreds := profileCreds{Provider: req.TargetProvider, URL: req.TargetURL, Username: req.TargetUsername, Password: req.TargetPassword}
	if req.TargetProfileID != "" {
		tgt, err := s.loadProfile(r, req.TargetProfileID, targetCreds)
		if err != nil {
			s.logf(r, "handleTargetMkdir: failed to load target profile: %v", err)
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		req.TargetProvider = tgt.Provider
		req.TargetURL = tgt.URL
		req.TargetUsername = tgt.Username
		if req.TargetPassword == "" {
			req.TargetPassword = tgt.Password
		}
		targetCreds = tgt
	}

	targetClient, err := storage.NewProvider(withMegaProfileSession(r.Context(), targetCreds), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	ok, err := targetClient.Connect(ctx)
	if !ok {
		s.logf(r, "handleTargetMkdir: connection failed for provider %s: %v", req.TargetProvider, err)
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetConnectionFailed})
		return
	}

	err = targetClient.CreateDirectory(ctx, "files", req.Path)
	if err != nil {
		s.logf(r, "handleTargetMkdir: CreateDirectory(%s) failed: %v", req.Path, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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
		s.logf(r, "Error resetting failed tasks for retry: %v", err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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
		s.logf(r, "Reindex error for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	go s.indexer.Start(s.backgroundCtx, id)

	s.infof(r, "Migration %s re-index triggered", id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true, "migration_id": id})
}

func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
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
		s.logf(r, "Warning: failed to cancel pending tasks for migration %s: %v", id, err)
	}

	if err := s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		s.logf(r, "Warning: failed to publish cancel event for migration %s: %v — in-flight tasks will be aborted via DB status check", id, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req ThreadsRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	threads := req.Threads
	if threads < 1 || threads > 16 {
		writeError(w, http.StatusBadRequest, ErrThreadsOutOfRange)
		return
	}

	if err := db.UpdateMigrationThreads(s.db, id, threads); err != nil {
		s.logf(r, "Error updating threads for migration %s: %v", id, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil || !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	var req BandwidthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.LimitMbps < 0 || req.LimitMbps > 1000 {
		writeError(w, http.StatusBadRequest, ErrBandwidthOutOfRange)
		return
	}

	if err := db.UpdateMigrationBandwidthLimit(s.db, id, req.LimitMbps); err != nil {
		s.logf(r, "Error updating bandwidth limit for migration %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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
		s.logf(r, "Warning: failed to publish bandwidth change for migration %s: %v", id, err)
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
	Role                  string `json:"role"`
}

// handleConnectTest verifies a single-side connection (source or target).
// It reuses ConnectRequest but ignores Path, ResourceType,
// TargetProfileID, TargetProvider, and target credential fields
// when role="source" (and vice-versa for role="target").
func (s *APIServer) handleConnectTest(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "connect-test", s.clientIP(r), connectTestRateLimit, connectTestRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req ConnectRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	role := req.Role
	if role != "source" && role != "target" {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	var provider, url, username, password, refreshToken, profileID string
	if role == "source" {
		provider = req.SourceProvider
		url = req.SourceURL
		username = req.SourceUsername
		password = req.SourcePassword
		refreshToken = req.SourceRefreshToken
		profileID = req.SourceProfileID
	} else {
		provider = req.TargetProvider
		url = req.TargetURL
		username = req.TargetUsername
		password = req.TargetPassword
		refreshToken = req.TargetRefreshToken
		profileID = req.TargetProfileID
	}

	creds, err := s.loadProfile(r, profileID, profileCreds{
		Provider:     provider,
		URL:          url,
		Username:     username,
		Password:     password,
		RefreshToken: refreshToken,
	})
	if err != nil {
		s.logf(r, "handleConnectTest: failed to load %s profile: %v", role, err)
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	provider = creds.Provider
	url = creds.URL
	username = creds.Username
	if password == "" {
		password = creds.Password
	}

	if provider == "" {
		provider = "nextcloud"
	}
	url = normalizeProviderURL(provider, url)

	if !storage.IsValidProvider(provider) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error_code": ErrProviderUnsupported})
		return
	}

	providerCtx := withMegaProfileSession(r.Context(), creds)
	client, err := storage.NewProvider(providerCtx, provider, url, username, password)
	if err != nil {
		code := ErrSourceUrlInvalid
		if role == "target" {
			code = ErrTargetUrlInvalid
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": string(code)})
		return
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ok, err := client.Connect(ctx)
	if !ok {
		s.logf(r, "handleConnectTest: %s connection failed for provider %s: %v", role, provider, err)
		code := ErrSourceConnectionFailed
		if role == "target" {
			code = ErrTargetConnectionFailed
		}
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": code})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "connect", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req ConnectRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
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
		s.logf(r, "handleConnect: failed to load source profile: %v", err)
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
		s.logf(r, "handleConnect: failed to load target profile: %v", err)
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
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrProviderUnsupported})
		return
	}

	sourceClient, err := storage.NewProvider(withMegaProfileSession(r.Context(), src), req.SourceProvider, req.SourceURL, req.SourceUsername, req.SourcePassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer sourceClient.Close()
	srcCtx, srcCancel := context.WithTimeout(r.Context(), 15*time.Second)
	sourceOK, err := sourceClient.Connect(srcCtx)
	srcCancel()
	if !sourceOK {
		s.logf(r, "handleConnect: source connection failed for provider %s: %v", req.SourceProvider, err)
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}

	targetClient, err := storage.NewProvider(withMegaProfileSession(r.Context(), tgt), req.TargetProvider, req.TargetURL, req.TargetUsername, req.TargetPassword)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrTargetUrlInvalid})
		return
	}
	defer targetClient.Close()
	tgtCtx, tgtCancel := context.WithTimeout(r.Context(), 15*time.Second)
	targetOK, err := targetClient.Connect(tgtCtx)
	tgtCancel()
	if !targetOK {
		s.logf(r, "handleConnect: target connection failed for provider %s: %v", req.TargetProvider, err)
		if errors.Is(err, storage.ErrMegaMFARequired) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
			return
		}
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
		s.logf(r, "handleConnect: failed to list source files for path %s (provider %s): %v", reqPath, req.SourceProvider, err)
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
	if !s.rateLimiter.Allow(r.Context(), "migration-sync-mutation", s.clientIP(r), jobMutationRateLimit, jobMutationRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req StartRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
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
		s.logf(r, "handleStart: failed to load source profile: %v", err)
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
		s.logf(r, "handleStart: failed to load target profile: %v", err)
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
	// Long-running migrations must be able to recover from short-lived OAuth
	// access tokens. Reject incomplete OAuth credentials up front rather than
	// allowing a migration that is guaranteed to fail roughly an hour later.
	if oauth.IsProvider(req.SourceProvider) && req.SourceRefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	if oauth.IsProvider(req.TargetProvider) && req.TargetRefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	req.SourceURL = normalizeProviderURL(req.SourceProvider, req.SourceURL)
	req.TargetURL = normalizeProviderURL(req.TargetProvider, req.TargetURL)

	if len(req.Calendars) > 0 {
		if !storage.ProviderSupportsResourceType(req.SourceProvider, "calendars") || !storage.ProviderSupportsResourceType(req.TargetProvider, "calendars") {
			writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
			return
		}
	}
	if len(req.Contacts) > 0 {
		if !storage.ProviderSupportsResourceType(req.SourceProvider, "contacts") || !storage.ProviderSupportsResourceType(req.TargetProvider, "contacts") {
			writeError(w, http.StatusBadRequest, ErrInvalidResourceType)
			return
		}
	}
	if req.ConflictStrategy == "" {
		req.ConflictStrategy = "SKIP"
	}
	if !db.ValidConflictStrategy(req.ConflictStrategy) {
		writeError(w, http.StatusBadRequest, ErrConflictStrategyInvalid)
		return
	}
	// Conflict strategy controls writes at the target; Immich remains a valid
	// source with any strategy supported by the non-Immich target.
	if req.TargetProvider == "immich" && req.ConflictStrategy != "SKIP" {
		writeError(w, http.StatusBadRequest, ErrImmichConflictStrategy)
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

	sourcePassEnc, err := crypto.EncryptWithDomain(req.SourcePassword, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(req.SourceProvider)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	targetPassEnc, err := crypto.EncryptWithDomain(req.TargetPassword, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(req.TargetProvider)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	sourceMegaSessionIDEncrypted, sourceMegaMasterKeyEncrypted, err := s.encryptMegaSession(src.MegaSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	targetMegaSessionIDEncrypted, targetMegaMasterKeyEncrypted, err := s.encryptMegaSession(tgt.MegaSession)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	var sourceRefreshEnc sql.NullString
	var sourceTokenExpiresAt sql.NullTime
	if req.SourceRefreshToken != "" {
		enc, err := crypto.EncryptWithDomain(req.SourceRefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
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
		enc, err := crypto.EncryptWithDomain(req.TargetRefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	active, err := db.CountActiveMigrationsForUser(s.db, userID)
	if err != nil {
		s.logf(r, "handleStart: failed to count active migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if active >= maxActiveMigrations {
		writeError(w, http.StatusConflict, ErrTooManyActiveMigrations)
		return
	}

	threads := req.Threads
	if threads < 1 {
		threads = 8
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
		UserID:                       sql.NullString{String: userID, Valid: userID != ""},
		SourceURL:                    req.SourceURL,
		SourceUsername:               req.SourceUsername,
		SourcePasswordEncrypted:      sourcePassEnc,
		SourceRefreshTokenEncrypted:  sourceRefreshEnc,
		SourceTokenExpiresAt:         sourceTokenExpiresAt,
		SourceMegaSessionIDEncrypted: sourceMegaSessionIDEncrypted,
		SourceMegaMasterKeyEncrypted: sourceMegaMasterKeyEncrypted,
		TargetURL:                    req.TargetURL,
		TargetUsername:               req.TargetUsername,
		TargetPasswordEncrypted:      targetPassEnc,
		TargetRefreshTokenEncrypted:  targetRefreshEnc,
		TargetTokenExpiresAt:         targetTokenExpiresAt,
		TargetMegaSessionIDEncrypted: targetMegaSessionIDEncrypted,
		TargetMegaMasterKeyEncrypted: targetMegaMasterKeyEncrypted,
		SourceProvider:               req.SourceProvider,
		TargetProvider:               req.TargetProvider,
		Status:                       initialStatus,
		ConflictStrategy:             req.ConflictStrategy,
		TargetDir:                    targetDir,
		SelectedPaths:                db.StringArray(req.Paths),
		SelectedCalendars:            db.StringArray(req.Calendars),
		SelectedContacts:             db.StringArray(req.Contacts),
		Threads:                      threads,
		BandwidthLimitMbps:           bandwidthLimit,
		PickerSessionID:              req.SourcePickerSessionID,
	}

	var migrationID string
	if req.ScheduledTime != "" {
		schedule := &db.Schedule{
			UserID:    userID,
			TaskType:  "migration",
			RunAt:     sql.NullTime{Time: scheduledAt, Valid: true},
			NextRunAt: sql.NullTime{Time: scheduledAt, Valid: true},
			IsActive:  true,
		}
		migrationID, err = db.CreateMigrationAndSchedule(s.db, m, schedule)
	} else {
		migrationID, err = db.CreateMigration(s.db, m)
	}
	if err != nil {
		s.logf(r, "Start migration error: failed to create migration or schedule: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationCreated, migrationID, userID, map[string]interface{}{
		"source_provider": m.SourceProvider,
		"target_provider": m.TargetProvider,
		"scheduled":       req.ScheduledTime != "",
	})

	if req.ScheduledTime != "" {
		s.infof(r, "Migration %s scheduled for %s", migrationID, scheduledAt.Format(time.RFC3339))

		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"success":        true,
			"migration_id":   migrationID,
			"scheduled":      true,
			"scheduled_time": scheduledAt.Format(time.RFC3339),
		})
		return
	}

	go s.indexer.Start(s.backgroundCtx, migrationID)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"success":      true,
		"migration_id": migrationID,
	})
}

func (s *APIServer) handleListMigrations(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	list, err := db.GetMigrationsForUserContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "Error listing migrations for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *APIServer) handleMigrationStream(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !s.acquireMigrationStream(w, r, userID) {
		return
	}
	defer s.releaseMigrationStream(userID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

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

	initial, err := db.GetMigrationsForUserContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "Migration stream initial load error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	prev, err := json.Marshal(initial)
	if err != nil {
		s.logf(r, "Migration stream initial marshal error for user %s: %v\n", userID, err)
		writeErrorEvent(ErrInternalError)
		return
	}
	if err := writeEvent(prev); err != nil {
		s.logf(r, "Migration stream initial write error for user %s: %v\n", userID, err)
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
			list, err := db.GetMigrationsForUserContext(r.Context(), s.db, userID)
			if err != nil {
				s.logf(r, "Migration stream reload error for user %s: %v\n", userID, err)
				return
			}
			cur, err := json.Marshal(list)
			if err != nil {
				s.logf(r, "Migration stream marshal error for user %s: %v\n", userID, err)
				return
			}
			if !bytes.Equal(cur, prev) {
				if err := writeEvent(cur); err != nil {
					s.logf(r, "Migration stream write error for user %s: %v\n", userID, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	owned, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !owned {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	// A worker can already have loaded this migration and be streaming a file.
	// Mark it cancelled and broadcast before deleting its rows so active workers
	// cancel their request contexts instead of continuing against deleted tasks.
	if err = db.UpdateMigrationStatus(s.db, id, "CANCELLED", nil); err != nil {
		s.logf(r, "Error cancelling migration %s before deletion: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err = db.CancelPendingTasks(s.db, id); err != nil {
		// Once the migration row is removed, workers cannot observe its CANCELLED
		// status. Do not delete until all not-yet-running tasks are cancelled.
		s.logf(r, "Failed to cancel pending tasks for migration %s before deletion: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditMigrationCancelled, id, userID, map[string]interface{}{"reason": "deletion"})
	if err = s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		// Do not remove the cancellation state while a worker may still be
		// streaming. Retrying the delete will publish a fresh cancellation event.
		s.logf(r, "Failed to publish cancel event for migration %s before deletion: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	err = db.DeleteMigrationCascade(s.db, id)
	if err != nil {
		s.logf(r, "Error deleting migration %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	err = db.DeleteSchedulesForTask(s.db, "migration", id)
	if err != nil {
		s.logf(r, "Warning: failed to delete schedules for migration %s: %v\n", id, err)
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

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			s.logf(r, "Error fetching migration %s: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !mig.UserID.Valid || mig.UserID.String != userID {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}

	payload := s.migrationDetailPayload(r.Context(), mig)
	writeJSON(w, http.StatusOK, payload)
}

func (s *APIServer) handleDownloadReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}

	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		} else {
			s.logf(r, "Error fetching migration %s for report: %v\n", id, err)
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
		s.logf(r, "Download report error: failed to get report: %v\n", err)
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
		displayPath := processor.ResolveTargetPath(task.ResourceType, task.FilePath, task.Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
		_ = writer.Write([]string{
			csvCell(displayPath),
			fmt.Sprintf("%d", task.FileSize),
			fmt.Sprintf("%d", task.Attempts),
			csvCell(errMsg),
		})
	}

	indexErrs, err := db.GetIndexingErrorsForReport(s.db, id)
	if err != nil {
		s.logf(r, "Download report error: failed to get indexing errors: %v\n", err)
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

func parseErrorListPagination(r *http.Request) (int, int) {
	limit, offset := 20, 0
	if value, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && value > 0 {
		limit = min(value, 100)
	}
	// Offset zero is valid; negative offsets keep the safe default of zero.
	if value, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && value >= 0 {
		offset = value
	}
	return limit, offset
}

func sanitizeErrorListItems(items []db.ErrorListItem) {
	for i := range items {
		items[i].ErrorMessage = sanitize.SanitizeError(items[i].ErrorMessage)
	}
}

func (s *APIServer) handleMigrationErrors(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	owned, err := db.VerifyMigrationOwnershipContext(r.Context(), s.db, id, userID)
	if err != nil {
		s.logf(r, "Error checking migration %s error-list ownership: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}
	limit, offset := parseErrorListPagination(r)
	items, total, err := db.GetMigrationErrorsContext(r.Context(), s.db, id, limit, offset)
	if err != nil {
		s.logf(r, "Error fetching migration %s errors: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if mig, mErr := db.GetMigrationContext(r.Context(), s.db, id); mErr == nil {
		for i := range items {
			if items[i].Kind == "transfer" && items[i].ResourceType == "files" {
				items[i].Path = processor.ResolveTargetPath(items[i].ResourceType, items[i].Path, items[i].Metadata, mig.TargetDir, mig.SourceProvider, mig.TargetProvider)
			}
		}
	}
	sanitizeErrorListItems(items)
	writeJSON(w, http.StatusOK, map[string]interface{}{"errors": items, "total": total, "limit": limit, "offset": offset})
}

// migrationDetailPayload is the canonical, sanitized live migration payload
// shared by the detail API and its SSE stream.
func (s *APIServer) migrationDetailPayload(ctx context.Context, mig *db.Migration) map[string]interface{} {
	activeFiles, err := db.GetActiveTaskPaths(s.db, ctx, mig.ID)
	if err != nil {
		s.logfContext(ctx, "Migration detail active paths error for %s: %v", mig.ID, err)
		activeFiles = nil
	}
	activeFile := ""
	if len(activeFiles) > 0 {
		activeFile = activeFiles[0]
	}

	payload := map[string]interface{}{
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
		payload["error_message"] = sanitize.SanitizeError(mig.ErrorMessage.String)
	}

	stats, err := db.GetMigrationResourceStats(s.db, mig.ID)
	if err != nil {
		s.logfContext(ctx, "Migration detail resource stats error for %s: %v", mig.ID, err)
		return payload
	}
	payload["resource_stats"] = stats
	return payload
}

// acquireStream applies the shared SSE request and concurrent-stream limits.
// All stream types intentionally consume the same per-user pool.
func (s *APIServer) acquireStream(w http.ResponseWriter, r *http.Request, userID, rateLimitScope string) bool {
	if !s.rateLimiter.Allow(r.Context(), rateLimitScope, s.clientIP(r), streamRateLimit, streamRateWindow) {
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return false
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.activeStreams[userID] >= maxStreamsPerUser {
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return false
	}
	s.activeStreams[userID]++
	return true
}

func (s *APIServer) acquireMigrationStream(w http.ResponseWriter, r *http.Request, userID string) bool {
	return s.acquireStream(w, r, userID, "migration-stream")
}

func (s *APIServer) acquireSyncStream(w http.ResponseWriter, r *http.Request, userID string) bool {
	return s.acquireStream(w, r, userID, "sync-stream")
}

func (s *APIServer) releaseMigrationStream(userID string) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	s.activeStreams[userID]--
	if s.activeStreams[userID] <= 0 {
		delete(s.activeStreams, userID)
	}
}

// releaseSyncStream mirrors acquireSyncStream. Both stream types use the same
// per-user pool, which is released by releaseMigrationStream.
func (s *APIServer) releaseSyncStream(userID string) {
	s.releaseMigrationStream(userID)
}

func isTerminalMigrationStatus(status string) bool {
	return status == "COMPLETED" || status == "COMPLETED_WITH_ERRORS" || status == "FAILED"
}

func (s *APIServer) handleMigrationDetailStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return
	}
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	if !s.acquireMigrationStream(w, r, userID) {
		return
	}
	defer s.releaseMigrationStream(userID)

	mig, err := db.GetMigrationContext(r.Context(), s.db, id)
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeEvent := func(payload []byte) error {
		if _, err := fmt.Fprintf(w, "event: migration\ndata: %s\n\n", payload); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	writePayload := func(migration *db.Migration) ([]byte, error) {
		return json.Marshal(s.migrationDetailPayload(r.Context(), migration))
	}

	previous, err := writePayload(mig)
	if err != nil {
		s.logf(r, "Migration detail stream initial payload error for %s: %v", id, err)
		return
	}
	if err := writeEvent(previous); err != nil {
		return
	}
	if isTerminalMigrationStatus(mig.Status) {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			mig, err := db.GetMigrationContext(r.Context(), s.db, id)
			if err != nil {
				return
			}
			current, err := writePayload(mig)
			if err != nil {
				s.logf(r, "Migration detail stream payload error for %s: %v", id, err)
				return
			}
			if !bytes.Equal(current, previous) {
				if err := writeEvent(current); err != nil {
					return
				}
				previous = current
			}
			if isTerminalMigrationStatus(mig.Status) {
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
			s.loglnContext(ctx, "Running Garbage Collector for old migrations...")
			count, err := db.DeleteOldMigrations(s.db)
			if err != nil {
				s.logfContext(ctx, "Garbage Collector error: %v\n", err)
			} else if count > 0 {
				s.logfContext(ctx, "Garbage Collector cleaned up %d old migrations & task histories.\n", count)
			}
		}
	}
}
