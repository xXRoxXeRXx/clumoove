package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

const profileRateLimit = 60

type profileCreds struct {
	Provider     string
	URL          string
	Username     string
	Password     string
	RefreshToken string
}

func (s *APIServer) loadProfile(r *http.Request, profileID string, base profileCreds) (profileCreds, error) {
	if profileID == "" {
		return base, nil
	}

	userID := auth.GetUserIDFromContext(r.Context())
	owned, err := db.VerifyProfileOwnership(s.db, profileID, userID)
	if err != nil {
		return base, err
	}
	if !owned {
		return base, errors.New("profile not owned")
	}
	p, err := db.GetConnectionProfile(s.db, profileID)
	if err != nil {
		return base, errors.New("profile not found")
	}

	provider := base.Provider
	if provider == "" {
		provider = p.Provider
	}
	urlStr := base.URL
	if urlStr == "" {
		urlStr = p.URL
	}
	username := base.Username
	if username == "" {
		username = p.Username
	}
	password := base.Password
	if password == "" && p.PasswordEncrypted != "" {
		if dec, derr := crypto.Decrypt(p.PasswordEncrypted, s.encryptionKey); derr == nil {
			password = dec
		}
	}
	refreshToken := base.RefreshToken
	if refreshToken == "" && p.RefreshTokenEncrypted != "" {
		if dec, derr := crypto.Decrypt(p.RefreshTokenEncrypted, s.encryptionKey); derr == nil {
			refreshToken = dec
		}
	}

	isOAuth := p.Provider == "dropbox" || p.Provider == "google"
	if isOAuth && refreshToken != "" {
		if tok, terr := oauth.RefreshToken(r.Context(), p.Provider, refreshToken); terr == nil && tok.AccessToken != "" {
			password = tok.AccessToken
		}
	}

	return profileCreds{
		Provider:     provider,
		URL:          urlStr,
		Username:     username,
		Password:     password,
		RefreshToken: refreshToken,
	}, nil
}

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

func (s *APIServer) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	profiles, err := db.GetConnectionProfiles(s.db, userID, "")
	if err != nil {
		log.Printf("handleListProfiles: query failed for user %s: %v", userID, err)
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

func (s *APIServer) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req ConnectionProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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

	var passEnc string
	if req.Password != "" {
		enc, err := crypto.Encrypt(req.Password, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		passEnc = enc
	}

	var refreshEnc sql.NullString
	var tokenExpiresAt sql.NullTime
	if req.RefreshToken != "" {
		enc, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		refreshEnc = sql.NullString{String: enc, Valid: true}
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
		PasswordEncrypted:    passEnc,
		RefreshTokenEncrypted: refreshEnc.String,
		TokenExpiresAt:       tokenExpiresAt,
		OAuthUser:            req.OAuthUser,
	}
	if !refreshEnc.Valid {
		p.RefreshTokenEncrypted = ""
	}

	id, err := db.CreateConnectionProfile(s.db, p)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		log.Printf("handleCreateProfile: insert failed for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditMigrationCreated, id, userID, map[string]interface{}{
		"action": "PROFILE_CREATED",
	})
	writeJSON(w, http.StatusCreated, map[string]interface{}{"success": true, "id": id})
}

func (s *APIServer) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleGetProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	p, err := db.GetConnectionProfile(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "profile": p.ToPublic()})
}

func (s *APIServer) handleUpdateConnectionProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleUpdateProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}

	var req ConnectionProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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
	if r.URL.Query().Get("url") == "1" || req.URL != "" {
		u := normalizeProviderURL(req.Provider, req.URL)
		in.URL = &u
	}
	if r.URL.Query().Get("username") == "1" || req.Username != "" {
		in.Username = &req.Username
	}
	if req.Password != "" {
		enc, err := crypto.Encrypt(req.Password, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		in.PasswordEncrypted = &enc
	}
	if req.RefreshToken != "" {
		enc, err := crypto.Encrypt(req.RefreshToken, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
			return
		}
		in.RefreshTokenEncrypted = &enc
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

	if existing, gerr := db.GetConnectionProfile(s.db, id); gerr == nil {
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
	}

	if err := db.UpdateConnectionProfile(s.db, id, in); err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrProfileNameExists)
			return
		}
		log.Printf("handleUpdateProfile: update failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleDeleteProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	if err := db.DeleteConnectionProfile(s.db, id); err != nil {
		log.Printf("handleDeleteProfile: delete failed for profile %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditMigrationDeleted, id, userID, map[string]interface{}{
		"action": "PROFILE_DELETED",
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleTestProfile(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), profileRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrProfileNotFound)
		return
	}
	owned, err := db.VerifyProfileOwnership(s.db, id, userID)
	if err != nil {
		log.Printf("handleTestProfile: ownership check failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	p, err := db.GetConnectionProfile(s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}

	var password, refreshToken string
	if p.PasswordEncrypted != "" {
		dec, derr := crypto.Decrypt(p.PasswordEncrypted, s.encryptionKey)
		if derr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrEncryptionFailed})
			return
		}
		password = dec
	}
	if p.RefreshTokenEncrypted != "" {
		dec, derr := crypto.Decrypt(p.RefreshTokenEncrypted, s.encryptionKey)
		if derr != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrEncryptionFailed})
			return
		}
		refreshToken = dec
	}
	isOAuth := p.Provider == "dropbox" || p.Provider == "google"
	if isOAuth && refreshToken != "" {
		password = refreshToken
		if tok, terr := oauth.RefreshToken(r.Context(), p.Provider, refreshToken); terr == nil && tok.AccessToken != "" {
			password = tok.AccessToken
		}
	}

	client, err := storage.NewProvider(r.Context(), p.Provider, p.URL, p.Username, password)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceUrlInvalid})
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ok, cerr := client.Connect(ctx)
	if !ok {
		log.Printf("handleTestProfile: connection failed for profile %s (provider %s): %v", id, p.Provider, cerr)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSourceConnectionFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
