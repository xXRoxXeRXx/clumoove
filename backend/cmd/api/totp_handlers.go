package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/totp2fa"
)

const (
	totpMaxAttempts  = 5
	totpLockDuration = 15 * time.Minute
)

// TOTPVerifyRequest is the body for POST /api/auth/totp (login second factor).
type TOTPVerifyRequest struct {
	TempSession string `json:"temp_session"`
	Code        string `json:"code"`
}

// handleTOTP verifies the second factor during login and, on success, issues
// the access + refresh tokens. Accepts either a TOTP code or a single-use
// backup code. Enforces the failed-attempt lockout.
func (s *APIServer) handleTOTP(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(s.clientIP(r), totpRateLimit, totpRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var req TOTPVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Code = sanitizeCode(req.Code)
	if req.TempSession == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, ErrTotpCodeRequired)
		return
	}

	claims, err := auth.Validate2FATempToken(req.TempSession, s.jwtSecret)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrTotpSessionInvalid)
		return
	}

	u, err := db.GetUserByID(s.db, claims.UserID)
	if err != nil {
		log.Printf("handleTOTP: failed to load user %s: %v\n", claims.UserID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !u.TotpEnabled {
		writeError(w, http.StatusUnauthorized, ErrTotpNotEnabled)
		return
	}

	// Lockout must be checked BEFORE validating the code to avoid leaking
	// attempt state via timing/responses. The lockout timestamp is already
	// loaded by GetUserByID above, so no extra query is needed.
	lockedUntil := u.TotpLockedUntil
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		retryAfter := int(time.Until(lockedUntil.Time).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	secret, err := crypto.Decrypt(u.TotpSecretEnc, s.encryptionKey)
	if err != nil {
		log.Printf("handleTOTP: failed to decrypt secret for user %s: %v\n", u.ID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	valid := totp2fa.Validate(secret, req.Code)

	if !valid && len(u.TotpBackupCodes) > 0 {
		// Note: backup-code consumption is read-then-write and not fully atomic.
		// Two concurrent requests presenting the same code could both succeed
		// before ReplaceUsedBackupCode commits. This is acceptable here: both
		// requests belong to the same authenticating user, and the outcome is
		// only that one backup code is spent instead of one — no privilege gain.
		idx := totp2fa.VerifyBackupCode([]string(u.TotpBackupCodes), req.Code)
		if idx >= 0 {
			valid = true
			remaining := make(db.StringArray, 0, len(u.TotpBackupCodes)-1)
			remaining = append(remaining, u.TotpBackupCodes[:idx]...)
			remaining = append(remaining, u.TotpBackupCodes[idx+1:]...)
			if err := db.ReplaceUsedBackupCode(s.db, u.ID, remaining); err != nil {
				log.Printf("handleTOTP: failed to consume backup code for user %s: %v\n", u.ID, err)
				writeError(w, http.StatusInternalServerError, ErrInternalError)
				return
			}
		}
	}

	if !valid {
		locked, lerr := db.IncrementTOTPFailed(s.db, u.ID, totpMaxAttempts, totpLockDuration)
		if lerr != nil {
			log.Printf("handleTOTP: failed to increment attempts for user %s: %v\n", u.ID, lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if locked {
			w.Header().Set("Retry-After", strconv.Itoa(int(totpLockDuration.Seconds())))
			writeError(w, http.StatusTooManyRequests, ErrRateLimited)
			return
		}
		writeError(w, http.StatusUnauthorized, ErrTotpInvalidCode)
		return
	}

	// Success: clear failed attempts/lockout and issue normal tokens.
	if err := db.ResetTOTPFailed(s.db, u.ID); err != nil {
		log.Printf("handleTOTP: failed to reset attempts for user %s: %v\n", u.ID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.issueTokens(w, r, u)
}

// TOTPSetupResponse is returned by GET /api/auth/2fa/setup.
type TOTPSetupResponse struct {
	OtpauthURI string `json:"otpauth_uri"`
	QRPNG      string `json:"qr_png"`
	Secret     string `json:"secret"`
}

// handle2FASetup generates a new TOTP secret + QR, stores the encrypted secret
// (still disabled — acceptance criterion 1), and returns the provisioning data.
func (s *APIServer) handle2FASetup(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	// Load the user to obtain the email for the otpauth account name.
	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handle2FASetup: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	// Do not allow re-provisioning while 2FA is already active: that would reset
	// the secret and clear backup codes without a password check, defeating the
	// password gate on /api/auth/2fa/disable (session-theft protection).
	if u.TotpEnabled {
		writeError(w, http.StatusConflict, ErrTotpAlreadyEnabled)
		return
	}

	secret, otpauthURI, qrPNG, err := totp2fa.GenerateProvisioning(u.Email)
	if err != nil {
		log.Printf("handle2FASetup: failed to generate provisioning for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	encrypted, err := crypto.Encrypt(secret, s.encryptionKey)
	if err != nil {
		log.Printf("handle2FASetup: failed to encrypt secret for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.SetUserTOTPSecret(s.db, userID, encrypted); err != nil {
		log.Printf("handle2FASetup: failed to store secret for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, TOTPSetupResponse{
		OtpauthURI: otpauthURI,
		QRPNG:      qrPNG,
		Secret:     secret,
	})
}

// TOTPEnableRequest is the body for POST /api/auth/2fa/enable.
type TOTPEnableRequest struct {
	Code string `json:"code"`
}

// handle2FAEnable verifies the first code, enables 2FA, generates backup codes,
// and returns the plaintext codes exactly once.
func (s *APIServer) handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req TOTPEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	req.Code = sanitizeCode(req.Code)
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, ErrTotpCodeRequired)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handle2FAEnable: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if u.TotpEnabled {
		writeError(w, http.StatusBadRequest, ErrTotpAlreadyEnabled)
		return
	}
	if u.TotpSecretEnc == "" {
		writeError(w, http.StatusBadRequest, ErrTotpNoPendingSetup)
		return
	}

	secret, err := crypto.Decrypt(u.TotpSecretEnc, s.encryptionKey)
	if err != nil {
		log.Printf("handle2FAEnable: failed to decrypt secret for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !totp2fa.Validate(secret, req.Code) {
		writeError(w, http.StatusBadRequest, ErrTotpInvalidCode)
		return
	}

	plainCodes, hashes, err := totp2fa.GenerateBackupCodes()
	if err != nil {
		log.Printf("handle2FAEnable: failed to generate backup codes for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.EnableUserTOTP(s.db, userID, db.StringArray(hashes)); err != nil {
		log.Printf("handle2FAEnable: failed to enable 2FA for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"backup_codes": plainCodes,
	})
}

// TOTPDisableRequest is the body for POST /api/auth/2fa/disable.
type TOTPDisableRequest struct {
	Password string `json:"password"`
}

// handle2FADisable disables 2FA, requiring the current password to defend
// against session theft.
func (s *APIServer) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req TOTPDisableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	req.Password = strings.TrimSpace(req.Password)
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, ErrPasswordRequired)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handle2FADisable: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !auth.CheckPasswordHash(req.Password, u.PasswordHash) {
		writeError(w, http.StatusUnauthorized, ErrPasswordInvalid)
		return
	}

	if err := db.DisableUserTOTP(s.db, userID); err != nil {
		log.Printf("handle2FADisable: failed to disable 2FA for user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// handle2FAStatus reports whether 2FA is currently enabled.
func (s *APIServer) handle2FAStatus(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handle2FAStatus: failed to load user %s: %v\n", userID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"totp_enabled": u.TotpEnabled})
}

// issueTokens mints a fresh access + refresh token pair for the given user,
// mirroring the token issuance in handleLogin.
func (s *APIServer) issueTokens(w http.ResponseWriter, r *http.Request, u *db.User) {
	accessToken, err := auth.GenerateAccessToken(u, s.jwtSecret)
	if err != nil {
		log.Printf("issueTokens: failed to generate access token for user %s: %v\n", u.ID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	refreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		log.Printf("issueTokens: failed to generate refresh token for user %s: %v\n", u.ID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	expiresAt := time.Now().Add(7 * 24 * time.Hour)
	tokenHash := hashToken(refreshToken)

	if err := db.StoreRefreshToken(s.db, tokenHash, u.ID, expiresAt); err != nil {
		log.Printf("issueTokens: failed to store refresh token for user %s: %v\n", u.ID, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	auth.SetRefreshTokenCookie(w, r, refreshToken, expiresAt, s.isSecure(r))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user":         userResponse(u),
		"access_token": accessToken,
	})
}

// sanitizeCode strips spaces/dashes users often include when typing OTP/backup codes.
func sanitizeCode(code string) string {
	out := make([]rune, 0, len(code))
	for _, c := range code {
		if c == ' ' || c == '-' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
