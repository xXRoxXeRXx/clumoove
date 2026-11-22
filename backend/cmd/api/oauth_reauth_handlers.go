package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

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

// persistReauthTokens stores tokens the user just obtained by re-authorizing. When
// the owner already holds a refresh token the swap is anchored to it, so a
// concurrent rotation is reported as db.ErrOAuthTokenConflict instead of being
// overwritten; an owner without one is simply given the new pair.
func persistReauthTokens(ctx context.Context, sink db.OAuthReauthSink, tokens db.EncryptedOAuthTokens, expected sql.NullString) error {
	if expected.Valid && expected.String != "" {
		return sink.Persist(ctx, tokens, expected.String)
	}
	return sink.Reauthorize(ctx, tokens)
}

func (api *migrationAPI) handleMigrationReauth(w http.ResponseWriter, r *http.Request) {
	mig, _, ok := api.requireOwnedMigrationStrict(w, r)
	if !ok {
		return
	}
	id := mig.ID
	var req oauthReauthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	if (req.Role != "source" && req.Role != "target") || req.AccessToken == "" || req.RefreshToken == "" {
		writeValidationError(w, ErrRefreshTokenMissing)
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
	access, err := crypto.EncryptWithDomain(req.AccessToken, api.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	refresh, err := crypto.EncryptWithDomain(req.RefreshToken, api.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	err = persistReauthTokens(r.Context(), db.NewMigrationTokenSink(api.db, id, req.Role), db.EncryptedOAuthTokens{Access: access, Refresh: refresh, ExpiresAt: oauthExpiry(req.ExpiresIn)}, expected)
	if err != nil {
		if errors.Is(err, db.ErrOAuthTokenConflict) {
			writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}
	count, err := db.ResetFailedTasksForRetry(api.db, r.Context(), id)
	if errors.Is(err, db.ErrMigrationNotRetryable) {
		// Tokens were refreshed; a migration that is not finished simply has
		// nothing to requeue.
		count, err = 0, nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "retried": count})
}

func (api *syncAPI) handleSyncReauth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	job, ok := api.requireOwnedSync(w, r, id, userID)
	if !ok {
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
	access, err := crypto.EncryptWithDomain(req.AccessToken, api.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	refresh, err := crypto.EncryptWithDomain(req.RefreshToken, api.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	err = persistReauthTokens(r.Context(), db.NewSyncJobTokenSink(api.db, id, req.Role), db.EncryptedOAuthTokens{Access: access, Refresh: refresh, ExpiresAt: oauthExpiry(req.ExpiresIn)}, expected)
	if err != nil {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}
	if err = db.UpdateSyncJobStatus(api.db, id, "IDLE", nil); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
