package main

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"backend/internal/auth"
	"backend/internal/db"
)

type SetupAdminRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Language    string `json:"language"`
}

func (s *APIServer) handleGetSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "setup-status", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	needsSetup, err := db.IsSetupRequired(s.db)
	if err != nil {
		s.logf(r, "handleGetSetupStatus: failed to check setup status: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"needs_setup": needsSetup})
}

func (s *APIServer) handleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "setup-admin", s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	// Avoid expensive password hashing after bootstrap is permanently closed.
	// CreateInitialAdmin repeats this check while holding its transaction lock.
	needsSetup, err := db.IsSetupRequired(s.db)
	if err != nil {
		s.logf(r, "handleSetupAdmin: failed to check setup status: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !needsSetup {
		writeError(w, http.StatusForbidden, ErrSetupAlreadyCompleted)
		return
	}

	var req SetupAdminRequest
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)

	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}

	if !validatePasswordLength(w, req.Password) {
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.logf(r, "handleSetupAdmin: password hashing error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateInitialAdmin(r.Context(), s.db, req.Email, passHash, req.DisplayName, req.Language)
	if err != nil {
		s.logf(r, "handleSetupAdmin: failed to create admin user: %v\n", err)
		if errors.Is(err, db.ErrSetupAlreadyCompleted) {
			writeError(w, http.StatusForbidden, ErrSetupAlreadyCompleted)
			return
		}
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditRegistration, req.Email, u.ID, map[string]interface{}{"role": "ADMIN", "setup": true})
	s.issueTokens(w, r, u)
}
