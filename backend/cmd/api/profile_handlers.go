package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

const profileRateLimit = 60

type ConnectionProfileRequest struct {
	Name                  string `json:"name"`
	Provider              string `json:"provider"`
	URL                   string `json:"url"`
	Username              string `json:"username"`
	Password              string `json:"password"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	OAuthUser             string `json:"oauth_user"`
}

func (api *profileAPI) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	profiles, err := db.GetConnectionProfiles(r.Context(), api.db, userID)
	if err != nil {
		api.logf(r, "handleListProfiles: query failed for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	public := make([]db.ConnectionProfilePublic, 0, len(profiles))
	for i := range profiles {
		public = append(public, profiles[i].ToPublic())
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"profiles": public,
	})
}

func (api *profileAPI) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "profile-create", api.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	var req ConnectionProfileRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.Name == "" || req.Provider == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}
	if !storage.IsValidProvider(req.Provider) {
		writeError(w, http.StatusBadRequest, ErrProfileInvalidProvider)
		return
	}

	urlStr := normalizeProviderURL(req.Provider, req.URL)

	if err := storage.ValidateProviderURL(req.Provider, urlStr); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}

	passEnc, refreshEncStr, err := api.profiles.EncryptProfileSecrets(req.Provider, req.Password, req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}

	var refreshEnc sql.NullString
	var tokenExpiresAt sql.NullTime
	if refreshEncStr != "" {
		refreshEnc = sql.NullString{String: refreshEncStr, Valid: true}
		expiresIn := req.RefreshTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		tokenExpiresAt = sql.NullTime{Time: time.Now().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}

	p := &db.ConnectionProfile{
		UserID:                userID,
		Name:                  req.Name,
		Provider:              req.Provider,
		URL:                   urlStr,
		Username:              req.Username,
		PasswordEncrypted:     passEnc,
		RefreshTokenEncrypted: refreshEnc.String,
		TokenExpiresAt:        tokenExpiresAt,
		OAuthUser:             req.OAuthUser,
	}
	if !refreshEnc.Valid {
		p.RefreshTokenEncrypted = ""
	}

	id, err := db.CreateConnectionProfile(api.db, p)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		api.logf(r, "handleCreateProfile: insert failed for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditProfileCreated, id, userID, nil)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

func (api *profileAPI) writeOwnedProfileAccessError(w http.ResponseWriter, r *http.Request, action string, err error) {
	if errors.Is(err, errProfileOwnership) {
		api.logf(r, "%s: ownership check failed: %v", action, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeError(w, http.StatusNotFound, ErrProfileNotFound)
}

func (api *profileAPI) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	p, err := api.profiles.LoadOwned(r.Context(), userID, id)
	if err != nil {
		api.writeOwnedProfileAccessError(w, r, "handleGetProfile", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "profile": p.ToPublic()})
}

func (api *profileAPI) handleUpdateConnectionProfile(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "profile-update", api.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	existing, err := api.profiles.LoadOwned(r.Context(), userID, id)
	if err != nil {
		api.writeOwnedProfileAccessError(w, r, "handleUpdateProfile", err)
		return
	}

	var req ConnectionProfileRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	in := db.UpdateConnectionProfileInput{}
	if req.Name != "" {
		in.Name = &req.Name
	}
	if req.Provider != "" {
		if !storage.IsValidProvider(req.Provider) {
			writeError(w, http.StatusBadRequest, ErrProfileInvalidProvider)
			return
		}
		in.Provider = &req.Provider
	}
	credentialProvider := existing.Provider
	if req.Provider != "" {
		credentialProvider = req.Provider
	}
	if r.URL.Query().Get("url") == "1" || req.URL != "" {
		u := normalizeProviderURL(req.Provider, req.URL)
		in.URL = &u
	}
	if r.URL.Query().Get("username") == "1" || req.Username != "" {
		in.Username = &req.Username
	}
	passEnc, refreshEnc, err := api.profiles.EncryptProfileSecrets(credentialProvider, req.Password, req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	if passEnc != "" {
		in.PasswordEncrypted = &passEnc
	}
	if existing.Provider == "mega" && (req.Password != "" || (in.Provider != nil && *in.Provider != "mega")) {
		empty := ""
		in.MegaSessionIDEncrypted = &empty
		in.MegaMasterKeyEncrypted = &empty
	}
	if refreshEnc != "" {
		in.RefreshTokenEncrypted = &refreshEnc
		expiresIn := req.RefreshTokenExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		exp := time.Now().Add(time.Duration(expiresIn) * time.Second)
		in.TokenExpiresAt = &exp
	}
	if req.OAuthUser != "" {
		in.OAuthUser = &req.OAuthUser
	}

	mergedProvider := existing.Provider
	if in.Provider != nil {
		mergedProvider = *in.Provider
	}
	mergedURL := existing.URL
	if in.URL != nil {
		mergedURL = *in.URL
	}
	if err := storage.ValidateProviderURL(mergedProvider, mergedURL); err != nil {
		writeError(w, http.StatusBadRequest, ErrProfileURLRequired)
		return
	}

	if err := db.UpdateConnectionProfile(api.db, id, in); err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		api.logf(r, "handleUpdateProfile: update failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditProfileUpdated, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *profileAPI) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	if err := api.profiles.VerifyOwned(r.Context(), userID, id); err != nil {
		api.writeOwnedProfileAccessError(w, r, "handleDeleteProfile", err)
		return
	}
	if err := db.DeleteConnectionProfile(api.db, id); err != nil {
		api.logf(r, "handleDeleteProfile: delete failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditProfileDeleted, id, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *profileAPI) handleTestProfile(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "profile-test", api.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	provider, err := api.profiles.TestOwned(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, errProfileNotFound) || errors.Is(err, errProfileNotOwned) {
			writeError(w, http.StatusNotFound, ErrProfileNotFound)
			return
		}
		if errors.Is(err, errProfileOwnership) {
			api.logf(r, "handleTestProfile: ownership check failed for profile %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if errors.Is(err, errProfileSecret) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrEncryptionFailed})
			return
		}
		if errors.Is(err, errProfileProvider) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
			return
		}
		if errors.Is(err, errProfileConnectFailed) {
			api.logf(r, "handleTestProfile: connection failed for profile %s (provider %s): %v", id, provider, err)
			if errors.Is(err, storage.ErrMegaMFARequired) {
				writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMegaMFAUnsupported})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
