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
	return ErrNotificationConfigIncomplete
}
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *APIServer) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	channels, err := db.GetNotificationChannels(s.db, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	emailAvailable, err := db.IsInstanceSMTPConfigured(s.db)
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
		plain, err := crypto.DecryptWithDomain(c.ConfigEncrypted, s.encryptionKey, crypto.DomainNotificationConfig)
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

func (s *APIServer) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
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
		available, err := db.IsInstanceSMTPConfigured(s.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if !available {
			writeError(w, http.StatusBadRequest, ErrSmtpNotConfigured)
			return
		}
		enc, err := crypto.EncryptWithDomain("{}", s.encryptionKey, crypto.DomainNotificationConfig)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if err := db.UpsertNotificationChannel(s.db, userID, "email", req.Enabled, enc); err != nil {
			s.logf(r, "handleUpdateNotificationSettings: upsert email channel failed: %v", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		s.writeAudit(r, db.AuditSettingUpdated, "notification:email", userID, map[string]interface{}{"enabled": req.Enabled})
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	req.Config = allowedNotificationConfig(req.Type, req.Config)
	old, err := db.GetNotificationChannel(s.db, userID, req.Type)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if old != nil {
		plain, err := crypto.DecryptWithDomain(old.ConfigEncrypted, s.encryptionKey, crypto.DomainNotificationConfig)
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
	enc, err := crypto.EncryptWithDomain(string(raw), s.encryptionKey, crypto.DomainNotificationConfig)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err = db.UpsertNotificationChannel(s.db, userID, req.Type, req.Enabled, enc); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditSettingUpdated, "notification:"+req.Type, userID, map[string]interface{}{"enabled": req.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *APIServer) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	var req notificationRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if !s.rateLimiter.Allow(r.Context(), "notification-test", s.clientIP(r), connectTestRateLimit, connectTestRateWindow) {
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
	if existing, err := db.GetNotificationChannel(s.db, userID, req.Type); err == nil {
		if plain, derr := crypto.DecryptWithDomain(existing.ConfigEncrypted, s.encryptionKey, crypto.DomainNotificationConfig); derr == nil {
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
	u, err := db.GetUserByID(s.db, userID)
	if err != nil || notify.Send(r.Context(), req.Type, notify.Config(req.Config), json.RawMessage(`{"kind":"test","name":"Clumoove","status":"TEST","total":0,"processed":0,"failed":0,"skipped":0}`), u.Email, u.Language) != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrNotificationTestFailed})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
