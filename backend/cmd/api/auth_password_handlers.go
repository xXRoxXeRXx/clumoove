package main

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
)

func (s *APIServer) handlePasswordResetAvailable(w http.ResponseWriter, r *http.Request) {
	available, err := db.IsInstanceSMTPConfigured(s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) instanceSMTPConfig() (email.SMTPConfig, error) {
	settings, err := db.GetInstanceSMTPSettings(s.db)
	if err != nil {
		return email.SMTPConfig{}, err
	}
	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, s.encryptionKey)
	if err != nil {
		return email.SMTPConfig{}, err
	}
	return email.SMTPConfig{Host: settings.SMTPHost, Port: strconv.Itoa(settings.SMTPPort), Username: settings.SMTPUsername, Password: password, FromEmail: settings.SMTPFromEmail, FromName: settings.SMTPFromName, Encryption: settings.SMTPEncryption}, nil
}

func (s *APIServer) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(r.Context(), "forgot-password", ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSONBodySilent(r, &req, authJSONBodyLimit) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	smtpCfg, smtpErr := s.instanceSMTPConfig()
	if smtpErr != nil {
		log.Printf("handleForgotPassword: SMTP not configured, skipping\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	u, err := db.GetUserByEmail(r.Context(), s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("handleForgotPassword: error fetching user: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	rawToken := generateRandomString(32)
	if rawToken == "" {
		log.Printf("handleForgotPassword: failed to generate token\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}
	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().Add(4 * time.Hour)

	if err := db.CreatePasswordResetToken(s.db, tokenHash, u.ID, expiresAt); err != nil {
		log.Printf("handleForgotPassword: error storing token: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:5173"
	}
	resetURL := fmt.Sprintf("%s/?reset-token=%s", strings.TrimRight(frontendURL, "/"), rawToken)

	htmlBody := email.BuildPasswordResetEmailLocalized(resetURL, u.Language)
	if err := email.SendMail(smtpCfg, u.Email, email.PasswordResetSubject(u.Language), htmlBody); err != nil {
		log.Printf("handleForgotPassword: error sending email: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	emailHash := sha256.Sum256([]byte(req.Email))
	log.Printf("handleForgotPassword: reset email sent (hash: %x)\n", emailHash[:8])
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(r.Context(), "reset-password", ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	if req.Token == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, ErrResetFieldsRequired)
		return
	}

	if !validatePasswordLength(w, req.NewPassword) {
		return
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		log.Printf("handleResetPassword: error hashing password: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	tokenHash := hashToken(req.Token)
	userID, err := db.ClaimPasswordResetToken(s.db, r.Context(), tokenHash, newHash)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusBadRequest, ErrResetTokenInvalid)
			return
		}
		log.Printf("handleResetPassword: error claiming token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.ResetLoginFailed(s.db, userID); err != nil {
		log.Printf("handleResetPassword: failed to clear login lockout for user %s: %v\n", userID, err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleEmailChangeAvailable(w http.ResponseWriter, r *http.Request) {
	available, err := db.IsInstanceSMTPConfigured(s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())

	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(r.Context(), "change-email", ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		NewEmail string `json:"new_email"`
	}
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	req.NewEmail = strings.TrimSpace(strings.ToLower(req.NewEmail))
	if req.NewEmail == "" || !strings.Contains(req.NewEmail, "@") || !strings.Contains(req.NewEmail, ".") {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleChangeEmail: error fetching user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if req.NewEmail == strings.ToLower(u.Email) {
		writeError(w, http.StatusBadRequest, ErrEmailUnchanged)
		return
	}

	existing, err := db.GetUserByEmail(r.Context(), s.db, req.NewEmail)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("handleChangeEmail: error checking email: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err == nil && existing.ID != userID {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	smtpCfg, smtpErr := s.instanceSMTPConfig()
	if smtpErr != nil {
		writeError(w, http.StatusBadRequest, ErrMailNotConfigured)
		return
	}

	rawToken := generateRandomString(32)
	if rawToken == "" {
		log.Printf("handleChangeEmail: failed to generate token\n")
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	tokenHash := hashToken(rawToken)
	expiresAt := time.Now().Add(4 * time.Hour)

	if err := db.CreateEmailChangeToken(s.db, tokenHash, userID, req.NewEmail, expiresAt); err != nil {
		log.Printf("handleChangeEmail: error storing token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:5173"
	}
	confirmURL := fmt.Sprintf("%s/?email-change-token=%s", strings.TrimRight(frontendURL, "/"), rawToken)

	htmlBody := email.BuildEmailChangeEmailLocalized(confirmURL, req.NewEmail, u.Language)
	if err := email.SendMail(smtpCfg, u.Email, email.EmailChangeSubject(u.Language), htmlBody); err != nil {
		log.Printf("handleChangeEmail: error sending email: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	emailHash := sha256.Sum256([]byte(u.Email))
	log.Printf("handleChangeEmail: confirmation email sent to %x\n", emailHash[:8])
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleConfirmEmailChange(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(r.Context(), "confirm-email-change", ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, ErrEmailChangeTokenInvalid)
		return
	}

	tokenHash := hashToken(req.Token)
	userID, newEmail, err := db.ClaimEmailChangeToken(s.db, r.Context(), tokenHash)
	if err != nil {
		if errorsIsEmailTaken(err) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		if err == sql.ErrNoRows {
			writeError(w, http.StatusBadRequest, ErrEmailChangeTokenInvalid)
			return
		}
		log.Printf("handleConfirmEmailChange: error claiming token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.DeleteAllRefreshTokensForUser(s.db, userID); err != nil {
		log.Printf("handleConfirmEmailChange: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	if smtpCfg, smtpErr := s.instanceSMTPConfig(); smtpErr == nil {
		u, lookupErr := db.GetUserByID(s.db, userID)
		language := "en"
		if lookupErr == nil {
			language = u.Language
		}
		htmlBody := email.BuildEmailChangedNotificationEmailLocalized(newEmail, language)
		if err := email.SendMail(smtpCfg, newEmail, email.EmailChangedSubject(language), htmlBody); err != nil {
			log.Printf("handleConfirmEmailChange: error sending notification to new email (user %s): %v\n", userID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func errorsIsEmailTaken(err error) bool {
	return errors.Is(err, db.ErrEmailTaken)
}
