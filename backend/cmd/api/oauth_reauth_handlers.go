package main

import (
	"errors"
	"net/http"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
)

type oauthReauthRequest struct {
	Role         string `json:"role"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func oauthExpiry(seconds int) time.Time {
	if seconds <= 0 {
		seconds = 3600
	}
	return time.Now().Add(time.Duration(seconds) * time.Second)
}

func (s *APIServer) handleMigrationReauth(w http.ResponseWriter, r *http.Request) {
	id, userID := r.PathValue("id"), auth.GetUserIDFromContext(r.Context())
	if ok, _ := db.VerifyMigrationOwnership(s.db, id, userID); !ok {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return
	}
	var req oauthReauthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if (req.Role != "source" && req.Role != "target") || req.AccessToken == "" || req.RefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	mig, err := db.GetMigration(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrMigrationNotFound)
		return
	}
	provider, expected := mig.SourceProvider, mig.SourceRefreshTokenEncrypted
	if req.Role == "target" {
		provider, expected = mig.TargetProvider, mig.TargetRefreshTokenEncrypted
	}
	if !oauth.IsProvider(provider) {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	access, err := crypto.Encrypt(req.AccessToken, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	refresh, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	update := db.OAuthTokenUpdate{MigrationID: id, Role: req.Role, AccessTokenEncrypted: access, RefreshTokenEncrypted: refresh, ExpiresAt: oauthExpiry(req.ExpiresIn)}
	if expected.Valid && expected.String != "" {
		err = db.UpdateMigrationOAuthTokens(s.db, update, expected.String)
	} else {
		err = db.UpdateMigrationOAuthTokensForReauth(s.db, update)
	}
	if err != nil {
		if errors.Is(err, db.ErrOAuthTokenConflict) {
			writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}
	count, err := db.ResetFailedTasksForRetry(s.db, r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "retried": count})
}

func (s *APIServer) handleSyncReauth(w http.ResponseWriter, r *http.Request) {
	id, userID := r.PathValue("id"), auth.GetUserIDFromContext(r.Context())
	job, err := db.GetSyncJob(s.db, id)
	if err != nil || job.UserID != userID {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return
	}
	var req oauthReauthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if (req.Role != "source" && req.Role != "target") || req.AccessToken == "" || req.RefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
		return
	}
	provider, expected := job.SourceProvider, job.SourceRefreshTokenEncrypted
	if req.Role == "target" {
		provider, expected = job.TargetProvider, job.TargetRefreshTokenEncrypted
	}
	if !oauth.IsProvider(provider) {
		writeValidationError(w, ErrProviderUnsupported)
		return
	}
	access, err := crypto.Encrypt(req.AccessToken, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	refresh, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	if expected.Valid && expected.String != "" {
		err = db.UpdateSyncJobOAuthTokens(s.db, id, req.Role, access, refresh, oauthExpiry(req.ExpiresIn), expected.String)
	} else {
		err = db.UpdateSyncJobOAuthTokensForReauth(s.db, id, req.Role, access, refresh, oauthExpiry(req.ExpiresIn))
	}
	if err != nil {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}
	if err = db.UpdateSyncJobStatus(s.db, id, "IDLE", nil); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
