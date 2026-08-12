package main

import (
	"database/sql"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
)

type instanceSMTPRequest struct {
	SMTPHost       string `json:"smtp_host"`
	SMTPPort       int    `json:"smtp_port"`
	SMTPUsername   string `json:"smtp_username"`
	SMTPPassword   string `json:"smtp_password"`
	SMTPFromEmail  string `json:"smtp_from_email"`
	SMTPFromName   string `json:"smtp_from_name"`
	SMTPEncryption string `json:"smtp_encryption"`
}

func (s *APIServer) handleAdminGetSMTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}
	cfg, err := db.GetInstanceSMTPSettings(s.db)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "smtp_password_set": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "smtp_host": cfg.SMTPHost, "smtp_port": cfg.SMTPPort, "smtp_username": cfg.SMTPUsername, "smtp_from_email": cfg.SMTPFromEmail, "smtp_from_name": cfg.SMTPFromName, "smtp_encryption": cfg.SMTPEncryption, "smtp_password_set": cfg.SMTPPasswordEnc != ""})
}

func (s *APIServer) handleAdminPutSMTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	var req instanceSMTPRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	req.SMTPHost, req.SMTPUsername, req.SMTPFromEmail = strings.TrimSpace(req.SMTPHost), strings.TrimSpace(req.SMTPUsername), strings.TrimSpace(req.SMTPFromEmail)
	if req.SMTPHost == "" || req.SMTPUsername == "" || req.SMTPFromEmail == "" {
		writeError(w, http.StatusBadRequest, ErrSmtpConfigIncomplete)
		return
	}
	if err := email.ValidateSMTPHost(req.SMTPHost); err != nil {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}
	if req.SMTPPort < 1 || req.SMTPPort > 65535 {
		writeError(w, http.StatusBadRequest, ErrSmtpPortInvalid)
		return
	}
	if req.SMTPEncryption != "tls" && req.SMTPEncryption != "starttls" {
		writeError(w, http.StatusBadRequest, ErrSmtpEncryptionInvalid)
		return
	}
	if _, err := mail.ParseAddress(req.SMTPFromEmail); err != nil {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}
	existing, err := db.GetInstanceSMTPSettings(s.db)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	encrypted := ""
	if req.SMTPPassword == "" {
		if existing == nil {
			writeError(w, http.StatusBadRequest, ErrSmtpPasswordRequired)
			return
		}
		encrypted = existing.SMTPPasswordEnc
	} else {
		var e error
		encrypted, e = crypto.Encrypt(req.SMTPPassword, s.encryptionKey)
		if e != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
	}
	fromName := strings.TrimSpace(req.SMTPFromName)
	if fromName == "" {
		fromName = "Clumoove"
	}
	if err := db.UpsertInstanceSMTPSettings(s.db, &db.InstanceSMTPSettings{SMTPHost: req.SMTPHost, SMTPPort: req.SMTPPort, SMTPUsername: req.SMTPUsername, SMTPPasswordEnc: encrypted, SMTPFromEmail: req.SMTPFromEmail, SMTPFromName: fromName, SMTPEncryption: req.SMTPEncryption}); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditSettingUpdated, "instance-smtp", actor, map[string]interface{}{})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *APIServer) handleAdminTestSMTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	cfg, err := db.GetInstanceSMTPSettings(s.db)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrSmtpNotConfigured})
		return
	}
	if err != nil {
		s.logf(r, "instance SMTP test configuration lookup failed")
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrInternalError})
		return
	}
	password, err := crypto.Decrypt(cfg.SMTPPasswordEnc, s.encryptionKey)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrSmtpDecryptFailed})
		return
	}
	defer crypto.ZeroString(&password)
	user, err := db.GetUserByID(s.db, actor)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrInternalError})
		return
	}
	if err := email.SendMail(email.SMTPConfig{Host: cfg.SMTPHost, Port: strconv.Itoa(cfg.SMTPPort), Username: cfg.SMTPUsername, Password: password, FromEmail: cfg.SMTPFromEmail, FromName: cfg.SMTPFromName, Encryption: cfg.SMTPEncryption}, user.Email, email.SMTPTestSubject(user.Language), email.BuildTestEmailLocalized(user.Language)); err != nil {
		s.logf(r, "instance SMTP test failed")
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "error_code": ErrSmtpTestFailed})
		return
	}
	s.writeAudit(r, db.AuditSettingUpdated, "instance-smtp-test", actor, map[string]interface{}{})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *APIServer) handleAdminDeleteSMTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}
	if err := db.DeleteInstanceSMTPSettings(s.db); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditSettingUpdated, "instance-smtp", actor, map[string]interface{}{"action": "delete"})
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
