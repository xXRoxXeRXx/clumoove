package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/auth"
	"backend/internal/db"

	"github.com/google/uuid"
)

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Language    string `json:"language"`
}

func (s *APIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "register", s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	regEnabled, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("Register error: failed to check registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if regEnabled != "true" {
		writeError(w, http.StatusForbidden, ErrRegistrationDisabled)
		return
	}

	var req RegisterRequest
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != strings.TrimSpace(req.Email) {
		writeError(w, http.StatusBadRequest, ErrEmailInvalid)
		return
	}
	req.Email = addr.Address

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if _, err := db.GetUserByEmail(r.Context(), s.db, req.Email); err == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	} else if err != sql.ErrNoRows {
		log.Printf("Error checking existing user for %s: %v\n", req.Email, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUser(s.db, req.Email, passHash, req.DisplayName, req.Language)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		log.Printf("Register error: failed to create user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditRegistration, req.Email, u.ID, map[string]interface{}{"email": req.Email})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *APIServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "login", s.clientIP(r), loginRateLimit, loginRateWindow) {
		// Retry-After describes the IP-wide limiter only. Do not emit it for an
		// account lockout, since that would reveal account state to unauthenticated
		// callers.
		w.Header().Set("Retry-After", strconv.Itoa(int(loginRateWindow.Seconds())))
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req LoginRequest
	if !decodeJSONBody(w, r, &req, authJSONBodyLimit) {
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	u, err := db.GetUserByEmail(r.Context(), s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			_ = auth.CheckPasswordHash(req.Password, s.dummyPasswordHash)
			s.writeAudit(r, db.AuditLoginFailed, req.Email, "", map[string]interface{}{"reason": "no_such_user"})
			writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}
	passwordValid := auth.CheckPasswordHash(req.Password, u.PasswordHash)

	if !u.Active {
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "disabled"})
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	}

	if u.LoginLockedUntil.Valid && time.Now().Before(u.LoginLockedUntil.Time) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	if !passwordValid {
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "bad_password"})
		locked, lerr := db.IncrementLoginFailed(s.db, u.ID, loginMaxAttempts, loginLockDuration)
		if lerr != nil {
			log.Printf("Login error: failed to record failed attempt for user %s: %v\n", u.ID, lerr)
		}
		if locked {
			log.Printf("Security: account %s locked for %v after reaching %d failed login attempts (source IP %s)",
				u.ID, loginLockDuration, loginMaxAttempts, s.clientIP(r))
			writeError(w, http.StatusTooManyRequests, ErrRateLimited)
			return
		}
		writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		return
	}

	if err := db.ResetLoginFailed(s.db, u.ID); err != nil {
		log.Printf("Login error: failed to reset failed attempts for user %s: %v\n", u.ID, err)
	}

	s.writeAudit(r, db.AuditLoginSuccess, req.Email, u.ID, nil)

	if u.MustChangePassword {
		mustToken, err := auth.GenerateMustChangePasswordToken(u, s.jwtSecret)
		if err != nil {
			log.Printf("Login error: failed to generate must-change token for user %s: %v\n", u.ID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"must_change_password": true,
			"temp_session":         mustToken,
		})
		return
	}

	if u.TotpEnabled {
		tempToken, err := auth.Generate2FATempToken(u, s.jwtSecret)
		if err != nil {
			log.Printf("Login error: failed to generate 2FA temp token for user %s: %v\n", u.ID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"totp_required": true,
			"temp_session":  tempToken,
		})
		return
	}

	s.issueTokens(w, r, u)
}

func (s *APIServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedCookieOrigin(w, r) {
		return
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenMissing)
		return
	}

	oldTokenHash := hashToken(cookie.Value)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	defer tx.Rollback()

	// Consume the presented token before issuing a replacement. DELETE ...
	// RETURNING locks the token row, so concurrent uses of the same token leave
	// exactly one request able to continue.
	var userID, userAgent string
	if err := tx.QueryRowContext(r.Context(), `
		DELETE FROM refresh_tokens
		WHERE token_hash = $1 AND expires_at > NOW()
		RETURNING user_id, user_agent
	`, oldTokenHash).Scan(&userID, &userAgent); err != nil {
		if err == sql.ErrNoRows {
			auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
			writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
			return
		}
		log.Printf("Error consuming refresh token in tx: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.GetUserByIDTx(tx, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
			writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
			return
		}
		log.Printf("Error loading refresh-token user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Re-check the account state while holding a row lock. If suspension wins
	// the race, its transaction removes all refresh tokens before this request
	// can rotate one; if this request wins, suspension removes the new token.
	var active bool
	if err := tx.QueryRowContext(r.Context(), `SELECT active FROM users WHERE id = $1 FOR UPDATE`, u.ID).Scan(&active); err != nil {
		if err == sql.ErrNoRows {
			auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
			writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
			return
		}
		log.Printf("Error locking refresh-token user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !active {
		// Normally suspension has already deleted these rows. Removing them here
		// also cleans up a residual token if an earlier operation was interrupted.
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM refresh_tokens WHERE user_id = $1`, u.ID); err != nil {
			log.Printf("Error deleting suspended user's refresh tokens: %v\n", err)
			auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("Error committing suspended-user refresh-token cleanup: %v\n", err)
			auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	}

	newRefreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)
	newHashedToken := hashToken(newRefreshToken)

	insertQuery := `
		INSERT INTO refresh_tokens (token_hash, user_id, user_agent, expires_at)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := tx.ExecContext(r.Context(), insertQuery, newHashedToken, u.ID, userAgent, newExpiresAt); err != nil {
		log.Printf("Error storing new refresh token in tx: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Error committing token rotation transaction: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	auth.SetRefreshTokenCookie(w, r, newRefreshToken, newExpiresAt, s.isSecure(r))

	accessToken, err := auth.GenerateAccessToken(u, s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": accessToken,
	})
}

func (s *APIServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedCookieOrigin(w, r) {
		return
	}
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		tokenHash := hashToken(cookie.Value)
		_ = db.DeleteRefreshToken(s.db, tokenHash)
	}

	auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "sessions", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	sessions, err := db.ListRefreshSessions(r.Context(), s.db, userID)
	if err != nil {
		log.Printf("handleListSessions: failed to list sessions for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

func (s *APIServer) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "sessions", s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	sessionID := r.PathValue("id")
	if _, err := uuid.Parse(sessionID); err != nil {
		writeError(w, http.StatusNotFound, ErrSessionNotFound)
		return
	}
	deleted, err := db.DeleteRefreshSessionForUser(r.Context(), s.db, sessionID, userID)
	if err != nil {
		log.Printf("handleDeleteSession: failed to revoke session for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, ErrSessionNotFound)
		return
	}
	s.writeAudit(r, db.AuditSessionRevoked, sessionID, userID, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func sessionUserAgent(r *http.Request) string {
	const maxUserAgentLength = 512
	userAgent := strings.TrimSpace(r.UserAgent())
	if len(userAgent) > maxUserAgentLength {
		// Keep the storage cap in bytes without leaving an incomplete UTF-8 rune.
		end := maxUserAgentLength
		for end > 0 && !utf8.RuneStart(userAgent[end]) {
			end--
		}
		return userAgent[:end]
	}
	return userAgent
}

func (s *APIServer) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleMe: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	resp := userResponse(u)
	writeJSON(w, http.StatusOK, resp)
}

// userResponse formats the public user payload. last_login_at reflects the
// timestamp of the user's latest established login session (updated during token issuance).
func userResponse(u *db.User) map[string]interface{} {
	language := u.Language
	if language != "de" && language != "en" {
		language = "en"
	}
	resp := map[string]interface{}{
		"id":            u.ID,
		"email":         u.Email,
		"display_name":  u.DisplayName,
		"role":          u.Role,
		"totp_enabled":  u.TotpEnabled,
		"language":      language,
		"last_login_at": u.LastLoginAt,
	}
	if len(u.Avatar) > 0 {
		resp["avatar"] = avatarDataURL(u)
	}
	return resp
}

func avatarDataURL(u *db.User) string {
	if len(u.Avatar) == 0 {
		return ""
	}
	mime := u.AvatarMime
	if mime == "" {
		mime = "image/png"
	}
	encoded := base64.StdEncoding.EncodeToString(u.Avatar)
	return fmt.Sprintf("data:%s;base64,%s", mime, encoded)
}
