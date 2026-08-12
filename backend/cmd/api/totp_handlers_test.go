package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/totp2fa"
)

type allowRateLimiter struct{}

func (allowRateLimiter) Allow(context.Context, string, string, int, time.Duration) bool {
	return true
}

func TestHandleTOTP_ConcurrentBackupCodeIsAcceptedOnce(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	const encryptionKey = "test-encryption-secret"
	user := createChangePasswordTestUser(t, database, true)
	code := "ABCDEFGH23"
	hash, err := totp2fa.HashBackupCode(code)
	if err != nil {
		t.Fatalf("hash backup code: %v", err)
	}
	secret, err := crypto.EncryptWithDomain("JBSWY3DPEHPK3PXP", encryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	if _, err := database.Exec(`
		UPDATE users
		SET totp_secret_enc = $1,
		    totp_backup_codes = $2,
		    totp_failed_attempts = 2
		WHERE id = $3
	`, secret, db.StringArray{hash}, user.ID); err != nil {
		t.Fatalf("configure TOTP user: %v", err)
	}

	s := &APIServer{
		db:            database,
		encryptionKey: encryptionKey,
		jwtSecret:     "test-jwt-secret-at-least-32-bytes-long!!",
		rateLimiter:   allowRateLimiter{},
	}
	tempSession, err := auth.Generate2FATempToken(user, s.jwtSecret)
	if err != nil {
		t.Fatalf("generate TOTP session: %v", err)
	}
	body, err := json.Marshal(TOTPVerifyRequest{TempSession: tempSession, Code: code})
	if err != nil {
		t.Fatalf("marshal TOTP request: %v", err)
	}

	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/auth/totp", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			s.handleTOTP(rec, req)
			responses <- rec
		}()
	}
	close(start)
	wg.Wait()
	close(responses)

	var success, rejected int
	var mustChangeResponse *httptest.ResponseRecorder
	for response := range responses {
		switch response.Code {
		case http.StatusAccepted:
			success++
			mustChangeResponse = response
		case http.StatusUnauthorized:
			rejected++
		default:
			t.Fatalf("unexpected TOTP response %d: %s", response.Code, response.Body.String())
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("responses = %d success, %d rejected; want 1 each", success, rejected)
	}
	var responseBody struct {
		TempSession        string `json:"temp_session"`
		MustChangePassword bool   `json:"must_change_password"`
	}
	if err := json.Unmarshal(mustChangeResponse.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode must-change response: %v", err)
	}
	if !responseBody.MustChangePassword {
		t.Fatalf("expected must_change_password response")
	}
	claims, err := auth.ValidateToken(responseBody.TempSession, s.jwtSecret)
	if err != nil {
		t.Fatalf("validate must-change token: %v", err)
	}
	if !claims.MustChangePassword || claims.TwoFAPending {
		t.Fatalf("unexpected returned token state: twoFA=%v mustChange=%v", claims.TwoFAPending, claims.MustChangePassword)
	}

	var refreshTokenCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`, user.ID).Scan(&refreshTokenCount); err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if refreshTokenCount != 0 {
		t.Fatalf("refresh tokens = %d, want 0", refreshTokenCount)
	}
}
