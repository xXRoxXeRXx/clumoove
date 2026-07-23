package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
	"backend/internal/oauth"
)

// handleOAuthAuth handles the OAuth authorization redirect.
func (s *APIServer) handleOAuthAuth(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	log.Printf("handleOAuthAuth: Hit with provider=%q", provider)

	if provider == "" {
		writeError(w, http.StatusBadRequest, ErrOauthProviderMissing)
		return
	}

	origin := r.URL.Query().Get("origin")
	log.Printf("handleOAuthAuth: origin query param=%q", origin)
	if origin == "" {
		if referer := r.Header.Get("Referer"); referer != "" {
			if parsed, err := url.Parse(referer); err == nil {
				origin = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
			}
		}
	}
	if origin == "" {
		log.Printf("handleOAuthAuth: rejected request with no determinable origin")
		writeError(w, http.StatusBadRequest, ErrOauthOriginMissing)
		return
	}
	if parsedOrigin, err := url.Parse(origin); err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") {
		log.Printf("handleOAuthAuth: rejected invalid origin %q", origin)
		writeError(w, http.StatusBadRequest, ErrOauthOriginInvalid)
		return
	}
	if !allowedOrigins[origin] {
		log.Printf("handleOAuthAuth: rejected untrusted origin %q", origin)
		writeError(w, http.StatusBadRequest, ErrOauthOriginUntrusted)
		return
	}
	log.Printf("handleOAuthAuth: final origin set to %q", origin)

	purpose := r.URL.Query().Get("purpose")
	if purpose == "" {
		purpose = "login"
	}

	stateToken := generateRandomString(16)
	if stateToken == "" {
		log.Printf("handleOAuthAuth: Failed to generate state token")
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	isSecure := s.isSecure(r)
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	cookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    stateToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
		MaxAge:   300,
	}
	http.SetCookie(w, cookie)

	stateParam := fmt.Sprintf("%s:%s:%s:%s", stateToken, provider, purpose, origin)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthAuth: constructing authURL with redirectURI=%s", redirectURI)
	authURL, err := oauth.GetAuthURL(provider, redirectURI, stateParam)
	if err != nil {
		log.Printf("handleOAuthAuth: GetAuthURL failed: %v", err)
		writeError(w, http.StatusInternalServerError, ErrOauthGenerationFailed)
		return
	}

	log.Printf("handleOAuthAuth: Redirecting user to %s", authURL)
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func (s *APIServer) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	log.Printf("handleOAuthCallback: Received request with code length %d, state: %q", len(code), state)

	if code == "" || state == "" {
		log.Printf("handleOAuthCallback: Missing code or state")
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", "Authorization code or state missing")
		return
	}

	parts := strings.SplitN(state, ":", 4)
	if len(parts) < 3 {
		log.Printf("handleOAuthCallback: Invalid state format (length %d)", len(parts))
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", "http://localhost:5173", "Invalid state parameter format")
		return
	}
	stateToken := parts[0]
	provider := parts[1]
	origin := parts[len(parts)-1]
	purpose := "login"
	if len(parts) >= 4 {
		purpose = parts[2]
	}

	log.Printf("handleOAuthCallback: parsed provider=%s, origin=%s, purpose=%s", provider, origin, purpose)

	if !allowedOrigins[origin] {
		log.Printf("handleOAuthCallback: rejected untrusted origin %q in state", origin)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, "CSRF verification failed: untrusted origin")
		return
	}

	cookie, err := r.Cookie("oauth_state")
	if err != nil || cookie.Value == "" || cookie.Value != stateToken {
		log.Printf("handleOAuthCallback: CSRF check failed. Cookie err: %v, stateToken: %q", err, stateToken)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, "CSRF verification failed: state mismatch")
		return
	}

	isSecure := s.isSecure(r)
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	clearCookie := &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
		MaxAge:   -1,
	}
	http.SetCookie(w, clearCookie)

	redirectURI := s.getRedirectURI(r)
	log.Printf("handleOAuthCallback: using redirectURI=%s", redirectURI)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	log.Printf("handleOAuthCallback: exchanging code for provider %s...", provider)
	tokenResp, err := oauth.ExchangeCode(ctx, provider, code, redirectURI)
	if err != nil {
		log.Printf("handleOAuthCallback: ExchangeCode failed: %v", err)
		s.renderOAuthResultHTML(w, "", "", "", 0, "", "", origin, fmt.Sprintf("Failed to exchange code: %v", err))
		return
	}

	log.Printf("handleOAuthCallback: token exchange successful. Fetching user info...")
	username, err := oauth.GetUserInfo(ctx, provider, tokenResp.AccessToken)
	if err != nil {
		log.Printf("handleOAuthCallback: GetUserInfo failed (defaulting to OAuth User): %v", err)
		username = "OAuth User"
	}

	log.Printf("handleOAuthCallback: rendering successful login for user %q", username)
	s.renderOAuthResultHTML(w, provider, tokenResp.AccessToken, tokenResp.RefreshToken, tokenResp.ExpiresIn, username, purpose, origin)
}

func (s *APIServer) renderOAuthResultHTML(w http.ResponseWriter, provider, token, refreshToken string, expiresIn int, username, purpose, targetOrigin string, errorMsg ...string) {
	provider = stripScriptTerminator(provider)
	token = stripScriptTerminator(token)
	refreshToken = stripScriptTerminator(refreshToken)
	username = stripScriptTerminator(username)

	var errStr string
	if len(errorMsg) > 0 {
		errStr = stripScriptTerminator(errorMsg[0])
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	w.Header().Set("Content-Security-Policy", "script-src 'nonce-"+nonce+"'; frame-ancestors 'none'; object-src 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var script string
	if errStr != "" {
		script = fmt.Sprintf(`
			console.log("OAuth error occurred:", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null on error page!");
				} else {
					window.opener.postMessage({
						type: "oauth-error",
						error: %q
					}, %q);
				}
			} catch (e) {
				console.error("Failed to post oauth-error:", e);
			}
			setTimeout(() => { window.close(); }, 1000);
		`, errStr, errStr, targetOrigin)
	} else {
		script = fmt.Sprintf(`
			console.log("OAuth successful. Sending credentials to opener at", %q);
			try {
				if (!window.opener) {
					console.error("window.opener is null!");
					var errMsg = document.createElement("p");
					errMsg.style.color = "red";
					errMsg.style.fontWeight = "bold";
					errMsg.style.marginTop = "15px";
					errMsg.innerText = "Fehler: window.opener ist null. Bitte überprüfe deine Browser-Sicherheitseinstellungen (z.B. Pop-up-Blocker oder Brave Shields).";
					document.querySelector(".card").appendChild(errMsg);
				} else {
					window.opener.postMessage({
						type: "oauth-success",
						provider: %q,
						purpose: %q,
						token: %q,
						refreshToken: %q,
						expiresIn: %d,
						username: %q
					}, %q);
					console.log("postMessage sent successfully.");
					window.close();
				}
			} catch (e) {
				console.error("Failed to post oauth-success:", e);
				var errMsg = document.createElement("p");
				errMsg.style.color = "red";
				errMsg.innerText = "Fehler beim Senden der Anmeldedaten: " + e.message;
				document.querySelector(".card").appendChild(errMsg);
			}
		`, targetOrigin, provider, purpose, token, refreshToken, expiresIn, username, targetOrigin)
	}

	fmt.Fprintf(w, `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Authorization Status</title>
			<style>
				body {
					font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
					display: flex;
					align-items: center;
					justify-content: center;
					height: 100vh;
					margin: 0;
					background-color: #f8fafc;
					color: #334155;
				}
				.card {
					background: white;
					padding: 2rem;
					border-radius: 8px;
					box-shadow: 0 4px 6px -1px rgb(0 0 0 / 0.1);
					text-align: center;
				}
			</style>
		</head>
		<body>
			<div class="card">
				%s
			</div>
			<script nonce="%s">%s</script>
		</body>
		</html>
	`, func() string {
		if errStr != "" {
			return fmt.Sprintf("<h3 style='color: #ef4444;'>Authorization Failed</h3><p>%s</p>", html.EscapeString(errStr))
		}
		return "<h3>Authorization Successful</h3><p>You can close this window now.</p>"
	}(), nonce, script)
}

type RegisterRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	regEnabled, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("Register error: failed to check registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if regEnabled == "false" {
		writeError(w, http.StatusForbidden, ErrRegistrationDisabled)
		return
	}

	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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

	if _, err := db.GetUserByEmail(s.db, req.Email); err == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	} else if err != sql.ErrNoRows {
		log.Printf("Error checking existing user for %s: %v\n", req.Email, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUser(s.db, req.Email, passHash, req.DisplayName)
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
	if !s.rateLimiter.Allow(s.clientIP(r), loginRateLimit, loginRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			s.writeAudit(r, db.AuditLoginFailed, req.Email, "", map[string]interface{}{"reason": "no_such_user"})
			writeError(w, http.StatusUnauthorized, ErrCredentialsInvalid)
		} else {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
		}
		return
	}

	if !u.Active {
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "disabled"})
		writeError(w, http.StatusForbidden, ErrUserDisabled)
		return
	}

	if u.LoginLockedUntil.Valid && time.Now().Before(u.LoginLockedUntil.Time) {
		retryAfter := int(time.Until(u.LoginLockedUntil.Time).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	if !auth.CheckPasswordHash(req.Password, u.PasswordHash) {
		s.writeAudit(r, db.AuditLoginFailed, req.Email, u.ID, map[string]interface{}{"reason": "bad_password"})
		locked, lerr := db.IncrementLoginFailed(s.db, u.ID, loginMaxAttempts, loginLockDuration)
		if lerr != nil {
			log.Printf("Login error: failed to record failed attempt for user %s: %v\n", u.ID, lerr)
		}
		if locked {
			log.Printf("Security: account %s locked for %v after reaching %d failed login attempts (source IP %s)",
				u.ID, loginLockDuration, loginMaxAttempts, s.clientIP(r))
			w.Header().Set("Retry-After", strconv.Itoa(int(loginLockDuration.Seconds())))
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
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenMissing)
		return
	}

	oldTokenHash := hashToken(cookie.Value)
	userID, err := db.GetUserIDByRefreshToken(s.db, oldTokenHash)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrRefreshTokenInvalid)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	defer tx.Rollback()

	deleteQuery := `DELETE FROM refresh_tokens WHERE token_hash = $1`
	if _, err := tx.ExecContext(r.Context(), deleteQuery, oldTokenHash); err != nil {
		log.Printf("Error deleting old refresh token in tx: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
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
		INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`
	if _, err := tx.ExecContext(r.Context(), insertQuery, newHashedToken, u.ID, newExpiresAt); err != nil {
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
	cookie, err := r.Cookie("refresh_token")
	if err == nil {
		tokenHash := hashToken(cookie.Value)
		_ = db.DeleteRefreshToken(s.db, tokenHash)
	}

	auth.ClearRefreshTokenCookie(w, r, s.isSecure(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
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

func userResponse(u *db.User) map[string]interface{} {
	resp := map[string]interface{}{
		"id":           u.ID,
		"email":        u.Email,
		"display_name": u.DisplayName,
		"role":         u.Role,
		"totp_enabled": u.TotpEnabled,
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

func (s *APIServer) RunOAuthRotationDaemon(ctx context.Context) {
	log.Println("[OAuthDaemon] Started. Scanning every 5 minutes for expiring tokens...")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[OAuthDaemon] Shutting down.")
			return
		case <-ticker.C:
			s.rotateExpiringOAuthTokens(ctx)
		}
	}
}

func (s *APIServer) rotateExpiringOAuthTokens(ctx context.Context) {
	expiring, err := db.GetExpiringOAuthMigrations(s.db)
	if err != nil {
		log.Printf("[OAuthDaemon] Error querying expiring tokens: %v\n", err)
		return
	}

	for _, entry := range expiring {
		refreshToken, err := crypto.Decrypt(entry.RefreshTokenEncrypted, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to decrypt refresh token for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		tokenResp, err := oauth.RefreshToken(refreshCtx, entry.Provider, refreshToken)
		cancel()

		if err != nil {
			log.Printf("[OAuthDaemon] Refresh failed for migration %s (%s provider=%s): %v — marking INVALID\n",
				entry.MigrationID, entry.Role, entry.Provider, err)
			errMsg := fmt.Sprintf("OAuth token refresh failed (%s): %v", entry.Provider, err)
			_ = db.UpdateMigrationStatus(s.db, entry.MigrationID, "FAILED", &errMsg)
			continue
		}

		newAccessEnc, err := crypto.Encrypt(tokenResp.AccessToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new access token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}
		newRefreshEnc, err := crypto.Encrypt(tokenResp.RefreshToken, s.encryptionKey)
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to encrypt new refresh token for migration %s: %v\n", entry.MigrationID, err)
			continue
		}

		expiresIn := tokenResp.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		newExpiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

		err = db.UpdateMigrationOAuthTokens(s.db, db.OAuthTokenUpdate{
			MigrationID:           entry.MigrationID,
			Role:                  entry.Role,
			AccessTokenEncrypted:  newAccessEnc,
			RefreshTokenEncrypted: newRefreshEnc,
			ExpiresAt:             newExpiresAt,
		})
		if err != nil {
			log.Printf("[OAuthDaemon] Failed to persist new tokens for migration %s (%s): %v\n",
				entry.MigrationID, entry.Role, err)
			continue
		}

		log.Printf("[OAuthDaemon] Successfully rotated %s OAuth token for migration %s (provider=%s, new_expires_at=%s)\n",
			entry.Role, entry.MigrationID, entry.Provider, newExpiresAt.Format(time.RFC3339))
	}
}

func (s *APIServer) handlePasswordResetAvailable(w http.ResponseWriter, r *http.Request) {
	available := os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM_EMAIL") != ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost == "" || smtpFromEmail == "" {
		log.Printf("handleForgotPassword: SMTP not configured, skipping\n")
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	u, err := db.GetUserByEmail(s.db, req.Email)
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

	smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
	if smtpEncryption == "" {
		smtpEncryption = "starttls"
	}

	smtpCfg := email.SMTPConfig{
		Host:       smtpHost,
		Port:       os.Getenv("SMTP_PORT"),
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		FromEmail:  smtpFromEmail,
		FromName:   os.Getenv("SMTP_FROM_NAME"),
		Encryption: smtpEncryption,
	}
	if smtpCfg.Port == "" {
		smtpCfg.Port = "587"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "Clumoove"
	}

	htmlBody := email.BuildPasswordResetEmail(resetURL)
	if err := email.SendMail(smtpCfg, u.Email, "Clumoove — Passwort zurücksetzen", htmlBody); err != nil {
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
	if !s.rateLimiter.Allow(ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token       string `json:"token"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Token == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, ErrResetFieldsRequired)
		return
	}

	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
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
	available := os.Getenv("SMTP_HOST") != "" && os.Getenv("SMTP_FROM_EMAIL") != ""
	writeJSON(w, http.StatusOK, map[string]interface{}{"available": available})
}

func (s *APIServer) handleChangeEmail(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())

	ip := s.clientIP(r)
	if !s.rateLimiter.Allow(ip, 3, 1*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		NewEmail string `json:"new_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
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

	existing, err := db.GetUserByEmail(s.db, req.NewEmail)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("handleChangeEmail: error checking email: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err == nil && existing.ID != userID {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost == "" || smtpFromEmail == "" {
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

	smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
	if smtpEncryption == "" {
		smtpEncryption = "starttls"
	}

	smtpCfg := email.SMTPConfig{
		Host:       smtpHost,
		Port:       os.Getenv("SMTP_PORT"),
		Username:   os.Getenv("SMTP_USERNAME"),
		Password:   os.Getenv("SMTP_PASSWORD"),
		FromEmail:  smtpFromEmail,
		FromName:   os.Getenv("SMTP_FROM_NAME"),
		Encryption: smtpEncryption,
	}
	if smtpCfg.Port == "" {
		smtpCfg.Port = "587"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "Clumoove"
	}

	htmlBody := email.BuildEmailChangeEmail(confirmURL, req.NewEmail)
	if err := email.SendMail(smtpCfg, u.Email, "Clumoove — E-Mail-Adresse ändern", htmlBody); err != nil {
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
	if !s.rateLimiter.Allow(ip, 10, 5*time.Minute) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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

	smtpHost := os.Getenv("SMTP_HOST")
	smtpFromEmail := os.Getenv("SMTP_FROM_EMAIL")
	if smtpHost != "" && smtpFromEmail != "" {
		smtpEncryption := os.Getenv("SMTP_ENCRYPTION")
		if smtpEncryption == "" {
			smtpEncryption = "starttls"
		}
		smtpCfg := email.SMTPConfig{
			Host:       smtpHost,
			Port:       os.Getenv("SMTP_PORT"),
			Username:   os.Getenv("SMTP_USERNAME"),
			Password:   os.Getenv("SMTP_PASSWORD"),
			FromEmail:  smtpFromEmail,
			FromName:   os.Getenv("SMTP_FROM_NAME"),
			Encryption: smtpEncryption,
		}
		if smtpCfg.Port == "" {
			smtpCfg.Port = "587"
		}
		if smtpCfg.FromName == "" {
			smtpCfg.FromName = "Clumoove"
		}

		htmlBody := email.BuildEmailChangedNotificationEmail(newEmail)
		if err := email.SendMail(smtpCfg, newEmail, "Clumoove — E-Mail-Adresse geändert", htmlBody); err != nil {
			log.Printf("handleConfirmEmailChange: error sending notification to new email (user %s): %v\n", userID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func errorsIsEmailTaken(err error) bool {
	return err == db.ErrEmailTaken
}

type SetupAdminRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleGetSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), connectRateLimit, connectRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	needsSetup, err := db.IsSetupRequired(s.db)
	if err != nil {
		log.Printf("handleGetSetupStatus: failed to check setup status: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"needs_setup": needsSetup})
}

func (s *APIServer) handleSetupAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), registerRateLimit, registerRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	needsSetup, err := db.IsSetupRequired(s.db)
	if err != nil {
		log.Printf("handleSetupAdmin: failed to check setup status: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !needsSetup {
		writeError(w, http.StatusForbidden, ErrSetupAlreadyCompleted)
		return
	}

	var req SetupAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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

	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		log.Printf("handleSetupAdmin: password hashing error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUserWithRole(s.db, req.Email, passHash, req.DisplayName, "ADMIN", false)
	if err != nil {
		log.Printf("handleSetupAdmin: failed to create admin user: %v\n", err)
		if db.IsUniqueViolation(err) {
			stillNeedsSetup, checkErr := db.IsSetupRequired(s.db)
			if checkErr == nil && !stillNeedsSetup {
				writeError(w, http.StatusForbidden, ErrSetupAlreadyCompleted)
				return
			}
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditRegistration, req.Email, u.ID, map[string]interface{}{"role": "ADMIN", "setup": true})
	s.issueTokens(w, r, u)
}
