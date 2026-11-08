package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/notify"
)

type notificationRequest struct {
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// notificationDependencies contains only the infrastructure required by the
// notification settings feature. Cross-cutting request concerns stay owned by
// the root server and are injected as narrow functions.
type notificationDependencies struct {
	db            *sql.DB
	encryptionKey string
	rateLimiter   rateLimiter
	requireUserID func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP      func(*http.Request) string
	writeAudit    func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf          func(*http.Request, string, ...any)
}

type notificationAPI struct {
	notificationDependencies
}

func newNotificationAPI(deps notificationDependencies) *notificationAPI {
	return &notificationAPI{notificationDependencies: deps}
}

// registerRoutes keeps notification route wiring with its feature handlers.
// The root server supplies the already-composed authentication middleware.
func (api *notificationAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	mux.Handle("GET /api/settings/notifications", authenticated(http.HandlerFunc(api.handleGetSettings)))
	mux.Handle("PUT /api/settings/notifications", authenticated(http.HandlerFunc(api.handleUpdateSettings)))
	mux.Handle("POST /api/settings/notifications/test", authenticated(http.HandlerFunc(api.handleTest)))
}

func secretKeys(typ string) map[string]bool {
	switch typ {
	case "gotify":
		return map[string]bool{"token": true}
	case "ntfy":
		return map[string]bool{"token": true}
	case "telegram":
		return map[string]bool{"bot_token": true}
	case "discord":
		return map[string]bool{"webhook_url": true}
	}
	return nil
}

func publicConfig(typ string, cfg map[string]any) map[string]any {
	out := map[string]any{}
	secrets := secretKeys(typ)
	for k, v := range cfg {
		if secrets[k] {
			out[k+"_set"] = strings.TrimSpace(toString(v)) != ""
		} else {
			out[k] = v
		}
	}
	return out
}

func allowedNotificationConfig(typ string, cfg map[string]any) map[string]any {
	allowed := map[string][]string{"gotify": {"url", "token"}, "ntfy": {"url", "topic", "token", "priority"}, "telegram": {"bot_token", "chat_id"}, "discord": {"webhook_url"}}[typ]
	out := map[string]any{}
	for _, key := range allowed {
		if value, ok := cfg[key]; ok {
			out[key] = value
		}
	}
	return out
}

func notificationErrorCode(err error) APIErrorCode {
	if errors.Is(err, notify.ErrURLBlocked) {
		return ErrNotificationURLBlocked
	}
	if errors.Is(err, notify.ErrInvalidChannel) {
		return ErrNotificationChannelInvalid
	}
	if errors.Is(err, notify.ErrInvalidURL) {
		return ErrNotificationURLInvalid
	}
	if errors.Is(err, notify.ErrInvalidPriority) {
		return ErrNotificationPriorityInvalid
	}
	return ErrNotificationConfigIncomplete
}
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (api *notificationAPI) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	channels, err := db.GetNotificationChannels(api.db, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	emailAvailable, err := db.IsInstanceSMTPConfigured(api.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	out := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		if c.Type == "email" {
			if emailAvailable {
				out = append(out, map[string]any{"type": c.Type, "enabled": c.Enabled, "config": map[string]any{}})
			}
			continue
		}
		plain, err := crypto.DecryptWithDomain(c.ConfigEncrypted, api.encryptionKey, crypto.DomainNotificationConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrNotificationDecryptFailed)
			return
		}
		cfg := map[string]any{}
		if json.Unmarshal([]byte(plain), &cfg) != nil {
			writeError(w, http.StatusInternalServerError, ErrNotificationDecryptFailed)
			return
		}
		out = append(out, map[string]any{"type": c.Type, "enabled": c.Enabled, "config": publicConfig(c.Type, cfg)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out, "email_available": emailAvailable})
}

func (api *notificationAPI) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	var req notificationRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if !db.NotificationTypes[req.Type] {
		writeError(w, http.StatusBadRequest, ErrNotificationChannelInvalid)
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if req.Type == "email" {
		available, err := db.IsInstanceSMTPConfigured(api.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if !available {
			writeError(w, http.StatusBadRequest, ErrSmtpNotConfigured)
			return
		}
		enc, err := crypto.EncryptWithDomain("{}", api.encryptionKey, crypto.DomainNotificationConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if err := db.UpsertNotificationChannel(api.db, userID, "email", req.Enabled, enc); err != nil {
			api.logf(r, "handleUpdateSettings: upsert email channel failed: %v", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		api.writeAudit(r, db.AuditSettingUpdated, "notification:email", userID, map[string]interface{}{"enabled": req.Enabled})
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	req.Config = allowedNotificationConfig(req.Type, req.Config)
	old, err := db.GetNotificationChannel(api.db, userID, req.Type)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if old != nil {
		plain, err := crypto.DecryptWithDomain(old.ConfigEncrypted, api.encryptionKey, crypto.DomainNotificationConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrNotificationDecryptFailed)
			return
		}
		var prior map[string]any
		if json.Unmarshal([]byte(plain), &prior) != nil {
			writeError(w, http.StatusInternalServerError, ErrNotificationDecryptFailed)
			return
		}
		for key := range secretKeys(req.Type) {
			if strings.TrimSpace(toString(req.Config[key])) == "" {
				req.Config[key] = prior[key]
			}
		}
	}
	if err := notify.Validate(req.Type, notify.Config(req.Config)); err != nil {
		writeError(w, http.StatusBadRequest, notificationErrorCode(err))
		return
	}
	raw, _ := json.Marshal(req.Config)
	enc, err := crypto.EncryptWithDomain(string(raw), api.encryptionKey, crypto.DomainNotificationConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err = db.UpsertNotificationChannel(api.db, userID, req.Type, req.Enabled, enc); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditSettingUpdated, "notification:"+req.Type, userID, map[string]interface{}{"enabled": req.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (api *notificationAPI) handleTest(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	var req notificationRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if !api.rateLimiter.Allow(r.Context(), "notification-test", api.clientIP(r), connectTestRateLimit, connectTestRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	if !db.NotificationTypes[req.Type] {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrNotificationChannelInvalid})
		return
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	req.Config = allowedNotificationConfig(req.Type, req.Config)
	// A blank secret means “keep the saved secret”, matching PUT semantics;
	// plaintext is still supplied only to the sender and never returned.
	if existing, err := db.GetNotificationChannel(api.db, userID, req.Type); err == nil {
		if plain, derr := crypto.DecryptWithDomain(existing.ConfigEncrypted, api.encryptionKey, crypto.DomainNotificationConfig); derr == nil {
			var prior map[string]any
			if json.Unmarshal([]byte(plain), &prior) == nil {
				for key := range secretKeys(req.Type) {
					if strings.TrimSpace(toString(req.Config[key])) == "" {
						req.Config[key] = prior[key]
					}
				}
			}
		}
	}
	if err := notify.Validate(req.Type, notify.Config(req.Config)); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": notificationErrorCode(err)})
		return
	}
	u, err := db.GetUserByIDContext(r.Context(), api.db, userID)
	if err != nil || notify.Send(r.Context(), req.Type, notify.Config(req.Config), json.RawMessage(`{"kind":"test","name":"Clumoove","status":"TEST","total":0,"processed":0,"failed":0,"skipped":0}`), u.Email, u.Language) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrNotificationTestFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
