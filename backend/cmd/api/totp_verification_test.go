package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"

	"github.com/pquerna/otp/totp"
)

const (
	totpVerificationTestSecret        = "JBSWY3DPEHPK3PXP"
	totpVerificationTestEncryptionKey = "test-encryption-secret"
	totpVerificationTestJWTSecret     = "test-jwt-secret-at-least-32-bytes-long!!"
	totpVerificationWrongCode         = "000000"
)

// totpEntryPoint describes one HTTP operation that requires a second factor,
// along with how it reports an invalid code and a success.
type totpEntryPoint struct {
	name          string
	invalidStatus int
	successStatus int
	call          func(t *testing.T, api *accountAPI, user *db.User, code string) *httptest.ResponseRecorder
}

var totpEntryPoints = []totpEntryPoint{
	{
		name:          "login",
		invalidStatus: http.StatusUnauthorized,
		// createChangePasswordTestUser requires a password change, so a
		// successful login second factor returns a must-change session.
		successStatus: http.StatusAccepted,
		call: func(t *testing.T, api *accountAPI, user *db.User, code string) *httptest.ResponseRecorder {
			t.Helper()
			tempSession, err := auth.Generate2FATempToken(user, api.jwtSecret)
			if err != nil {
				t.Fatalf("generate TOTP session: %v", err)
			}
			body, _ := json.Marshal(TOTPVerifyRequest{TempSession: tempSession, Code: code})
			req := httptest.NewRequest(http.MethodPost, "/api/auth/totp", strings.NewReader(string(body)))
			rec := httptest.NewRecorder()
			api.handleTOTP(rec, req)
			return rec
		},
	},
	{
		name:          "disable",
		invalidStatus: http.StatusBadRequest,
		successStatus: http.StatusOK,
		call: func(t *testing.T, api *accountAPI, user *db.User, code string) *httptest.ResponseRecorder {
			t.Helper()
			body, _ := json.Marshal(TOTPDisableRequest{Code: code})
			req := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/disable", strings.NewReader(string(body)))
			req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
			rec := httptest.NewRecorder()
			api.handle2FADisable(rec, req)
			return rec
		},
	},
}

func setupTOTPVerificationUser(t *testing.T, database *sql.DB, failedAttempts int, lockedUntil *time.Time) *db.User {
	t.Helper()
	user := createChangePasswordTestUser(t, database, true)
	secret, err := crypto.EncryptWithDomain(totpVerificationTestSecret, totpVerificationTestEncryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE users
		SET totp_secret_enc = $1, totp_failed_attempts = $2, totp_locked_until = $3
		WHERE id = $4
	`, secret, failedAttempts, lockedUntil, user.ID); err != nil {
		t.Fatalf("configure TOTP user: %v", err)
	}
	return user
}

func newTOTPVerificationAPI(database *sql.DB) *accountAPI {
	return newAccountAPI(accountDependencies{
		db:            database,
		encryptionKey: totpVerificationTestEncryptionKey,
		jwtSecret:     totpVerificationTestJWTSecret,
		rateLimiter:   allowRateLimiter{},
	})
}

func totpAttemptState(t *testing.T, database *sql.DB, userID string) (int, sql.NullTime) {
	t.Helper()
	var attempts int
	var lockedUntil sql.NullTime
	if err := database.QueryRow(`SELECT totp_failed_attempts, totp_locked_until FROM users WHERE id = $1`, userID).Scan(&attempts, &lockedUntil); err != nil {
		t.Fatalf("load TOTP attempt state: %v", err)
	}
	return attempts, lockedUntil
}

func TestTOTPEntryPointsCountInvalidCodes(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()
	api := newTOTPVerificationAPI(database)

	for _, entry := range totpEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			user := setupTOTPVerificationUser(t, database, 1, nil)

			rec := entry.call(t, api, user, totpVerificationWrongCode)

			assertAPIResponseError(t, rec, entry.invalidStatus, ErrTotpInvalidCode, entry.name)
			if attempts, _ := totpAttemptState(t, database, user.ID); attempts != 2 {
				t.Fatalf("failed attempts = %d, want 2", attempts)
			}
		})
	}
}

func TestTOTPEntryPointsLockAfterMaxInvalidCodes(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()
	api := newTOTPVerificationAPI(database)

	for _, entry := range totpEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			user := setupTOTPVerificationUser(t, database, totpMaxAttempts-1, nil)

			rec := entry.call(t, api, user, totpVerificationWrongCode)

			assertAPIResponseError(t, rec, http.StatusTooManyRequests, ErrRateLimited, entry.name)
			if got, want := rec.Header().Get("Retry-After"), strconv.Itoa(int(totpLockDuration.Seconds())); got != want {
				t.Fatalf("Retry-After = %q, want %q", got, want)
			}
			attempts, lockedUntil := totpAttemptState(t, database, user.ID)
			if attempts != 0 || !lockedUntil.Valid || !lockedUntil.Time.After(time.Now()) {
				t.Fatalf("attempt state = (%d, %v), want reset counter and future lockout", attempts, lockedUntil)
			}
		})
	}
}

func TestTOTPEntryPointsRejectValidCodeWhileLocked(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()
	api := newTOTPVerificationAPI(database)

	for _, entry := range totpEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			lockedUntil := time.Now().Add(10 * time.Minute)
			user := setupTOTPVerificationUser(t, database, 0, &lockedUntil)
			code, err := totp.GenerateCode(totpVerificationTestSecret, time.Now())
			if err != nil {
				t.Fatalf("generate TOTP code: %v", err)
			}

			rec := entry.call(t, api, user, code)

			assertAPIResponseError(t, rec, http.StatusTooManyRequests, ErrRateLimited, entry.name)
			retryAfter, err := strconv.Atoi(rec.Header().Get("Retry-After"))
			if err != nil || retryAfter < 1 || retryAfter > 600 {
				t.Fatalf("Retry-After = %q, want remaining lockout seconds", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestTOTPEntryPointsAcceptValidCodeAndResetAttempts(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()
	api := newTOTPVerificationAPI(database)

	for _, entry := range totpEntryPoints {
		t.Run(entry.name, func(t *testing.T) {
			user := setupTOTPVerificationUser(t, database, 3, nil)
			code, err := totp.GenerateCode(totpVerificationTestSecret, time.Now())
			if err != nil {
				t.Fatalf("generate TOTP code: %v", err)
			}

			rec := entry.call(t, api, user, code)

			if rec.Code != entry.successStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, entry.successStatus, rec.Body.String())
			}
			if attempts, _ := totpAttemptState(t, database, user.ID); attempts != 0 {
				t.Fatalf("failed attempts = %d, want 0", attempts)
			}
		})
	}
}
