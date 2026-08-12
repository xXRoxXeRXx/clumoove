package main

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
)

// The fixed provider order returned by GET /api/admin/settings/oauth.
var oauthProviderOrder = []string{"google", "onedrive", "dropbox", "hidrive"}

type instanceOAuthRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type instanceOAuthProviderView struct {
	Provider        string     `json:"provider"`
	Configured      bool       `json:"configured"`
	ClientID        string     `json:"client_id"`
	ClientSecretSet bool       `json:"client_secret_set"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

func (s *APIServer) handleAdminGetOAuth(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}
	rows, err := db.ListInstanceOAuthProviders(s.db)
	if err != nil {
		s.logf(r, "instance OAuth settings lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	byProvider := make(map[string]db.InstanceOAuthProvider, len(rows))
	for _, row := range rows {
		byProvider[row.Provider] = row
	}

	providers := make([]instanceOAuthProviderView, 0, len(oauthProviderOrder))
	for _, name := range oauthProviderOrder {
		view := instanceOAuthProviderView{Provider: name}
		if row, ok := byProvider[name]; ok {
			view.Configured = row.ClientID != "" && row.ClientSecretEnc != ""
			view.ClientID = row.ClientID
			view.ClientSecretSet = row.ClientSecretEnc != ""
			updatedAt := row.UpdatedAt
			view.UpdatedAt = &updatedAt
		}
		providers = append(providers, view)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"redirect_uri": s.getRedirectURI(r),
		"providers":    providers,
	})
}

func (s *APIServer) handleAdminPutOAuth(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	provider := strings.TrimSpace(r.PathValue("provider"))
	if !oauth.IsProvider(provider) {
		writeError(w, http.StatusBadRequest, ErrOauthProviderUnknown)
		return
	}

	var req instanceOAuthRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.ClientID == "" || len(req.ClientID) > 512 {
		writeError(w, http.StatusBadRequest, ErrOauthConfigIncomplete)
		return
	}
	if len(req.ClientSecret) > 1024 {
		writeError(w, http.StatusBadRequest, ErrOauthConfigIncomplete)
		return
	}

	existing, err := db.GetInstanceOAuthProvider(s.db, provider)
	if err != nil && err != sql.ErrNoRows {
		s.logf(r, "instance OAuth lookup failed for %s: %v", provider, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	var encrypted string
	if req.ClientSecret == "" {
		if existing == nil {
			writeError(w, http.StatusBadRequest, ErrOauthSecretRequired)
			return
		}
		// Keep the existing ciphertext; the secret is unchanged.
		encrypted = existing.ClientSecretEnc
	} else {
		encrypted, err = crypto.Encrypt(req.ClientSecret, s.encryptionKey)
		if err != nil {
			s.logf(r, "instance OAuth secret encryption failed for %s: %v", provider, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
	}

	if err := db.UpsertInstanceOAuthProvider(s.db, &db.InstanceOAuthProvider{
		Provider:        provider,
		ClientID:        req.ClientID,
		ClientSecretEnc: encrypted,
	}); err != nil {
		s.logf(r, "instance OAuth upsert failed for %s: %v", provider, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	oauth.Invalidate()
	s.writeAudit(r, db.AuditSettingUpdated, "instance-oauth-"+provider, actor, map[string]interface{}{})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *APIServer) handleAdminDeleteOAuth(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	provider := strings.TrimSpace(r.PathValue("provider"))
	if !oauth.IsProvider(provider) {
		writeError(w, http.StatusBadRequest, ErrOauthProviderUnknown)
		return
	}

	if err := db.DeleteInstanceOAuthProvider(s.db, provider); err != nil {
		s.logf(r, "instance OAuth delete failed for %s: %v", provider, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	oauth.Invalidate()
	s.writeAudit(r, db.AuditSettingUpdated, "instance-oauth-"+provider, actor, map[string]interface{}{"action": "delete"})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
