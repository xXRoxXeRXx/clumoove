package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
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

func (api *accountAPI) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "register", api.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	regEnabled, err := api.settings.GetSetting("registrations_enabled")
	if err != nil {
		api.logf(r, "Register error: failed to check registrations_enabled: %v\n", err)
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

	if !validatePasswordLength(w, req.Password) {
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

	if _, err := api.users.GetUserByEmail(r.Context(), req.Email); err == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	} else if err != sql.ErrNoRows {
		api.logf(r, "Error checking existing user for %s: %v\n", req.Email, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := api.users.CreateUser(req.Email, passHash, req.DisplayName, req.Language)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
			return
		}
		api.logf(r, "Register error: failed to create user: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditRegistration, req.Email, u.ID, map[string]interface{}{"email": req.Email})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (api *accountAPI) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "login", api.clientIP(r), loginRateLimit, loginRateWindow) {
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

	u, err := api.users.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			_ = auth.CheckPasswordHash(req.Password, api.dummyPasswordHash)
			api.writeAudit(r, db.AuditLoginFailed, req.Email, "", map[string]interface{}{"reason": "no_such_user"})
			writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}
	passwordValid := auth.CheckPasswordHash(req.Password, u.PasswordHash)

	if !u.Active {
		api.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "disabled"})
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	}

	if u.LoginLockedUntil.Valid && time.Now().Before(u.LoginLockedUntil.Time) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	if !passwordValid {
		api.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "bad_password"})
		locked, lerr := api.users.IncrementLoginFailed(u.ID, loginMaxAttempts, loginLockDuration)
		if lerr != nil {
			api.logf(r, "Login error: failed to record failed attempt for user %s: %v\n", u.ID, lerr)
		}
		if locked {
			api.warnf(r, "Security: account %s locked for %v after reaching %d failed login attempts (source IP %s)",
				u.ID, loginLockDuration, loginMaxAttempts, api.clientIP(r))
			writeError(w, http.StatusTooManyRequests, ErrRateLimited)
			return
		}
		writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		return
	}

	if err := api.users.ResetLoginFailed(u.ID); err != nil {
		api.logf(r, "Login error: failed to reset failed attempts for user %s: %v\n", u.ID, err)
	}

	api.writeAudit(r, db.AuditLoginSuccess, req.Email, u.ID, nil)

	if u.MustChangePassword {
		mustToken, err := auth.GenerateMustChangePasswordToken(u, api.jwtSecret)
		if err != nil {
			api.logf(r, "Login error: failed to generate must-change token for user %s: %v\n", u.ID, err)
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
		tempToken, err := auth.Generate2FATempToken(u, api.jwtSecret)
		if err != nil {
			api.logf(r, "Login error: failed to generate 2FA temp token for user %s: %v\n", u.ID, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"totp_required": true,
			"temp_session":  tempToken,
		})
		return
	}

	api.issueTokens(w, r, u)
}

func (api *accountAPI) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedCookieOrigin(w, r) {
		return
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenMissing)
		return
	}

	oldTokenHash := hashToken(cookie.Value)
	newRefreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	newExpiresAt := time.Now().Add(7 * 24 * time.Hour)

	// The store consumes the presented token and stores its replacement in one
	// transaction that re-checks the account state under a row lock, so
	// concurrent use of the same token or a racing suspension cannot both win.
	outcome, u, err := api.tokens.RotateRefreshToken(r.Context(), oldTokenHash, hashToken(newRefreshToken), newExpiresAt)
	switch {
	case outcome == db.RefreshUserDisabled:
		// The presented token is dead whether or not the cleanup succeeded.
		auth.ClearRefreshTokenCookie(w)
		if err != nil {
			api.logf(r, "Error cleaning up refresh tokens of a suspended user: %v\n", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	case err != nil:
		api.logf(r, "Error rotating refresh token: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	case outcome == db.RefreshTokenInvalid:
		auth.ClearRefreshTokenCookie(w)
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
		return
	}

	auth.SetRefreshTokenCookie(w, newRefreshToken, newExpiresAt)

	accessToken, err := auth.GenerateAccessToken(u, api.jwtSecret)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": accessToken,
	})
}

func (api *accountAPI) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireTrustedCookieOrigin(w, r) {
		return
	}
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		tokenHash := hashToken(cookie.Value)
		_ = api.tokens.DeleteRefreshToken(tokenHash)
	}

	auth.ClearRefreshTokenCookie(w)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *accountAPI) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "sessions", api.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	sessions, err := api.tokens.ListRefreshSessions(r.Context(), userID)
	if err != nil {
		api.logf(r, "handleListSessions: failed to list sessions for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"sessions": sessions})
}

func (api *accountAPI) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !api.rateLimiter.Allow(r.Context(), "sessions", api.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	sessionID := r.PathValue("id")
	if _, err := uuid.Parse(sessionID); err != nil {
		writeError(w, http.StatusNotFound, ErrSessionNotFound)
		return
	}
	deleted, err := api.tokens.DeleteRefreshSessionForUser(r.Context(), sessionID, userID)
	if err != nil {
		api.logf(r, "handleDeleteSession: failed to revoke session for user %s: %v", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, ErrSessionNotFound)
		return
	}
	api.writeAudit(r, db.AuditSessionRevoked, sessionID, userID, nil)
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

func (api *accountAPI) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}
	u, err := api.users.GetUserByIDContext(r.Context(), userID)
	if err != nil {
		api.logf(r, "handleMe: failed to load user %s: %v\n", userID, err)
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
