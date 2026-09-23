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

	"github.com/pquerna/otp/totp"
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

func TestHandle2FASetup_RejectsWhenAlreadyEnabled(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	const encryptionKey = "test-encryption-secret"
	user := createChangePasswordTestUser(t, database, true)

	s := &APIServer{
		db:            database,
		encryptionKey: encryptionKey,
		jwtSecret:     "test-jwt-secret-at-least-32-bytes-long!!",
		rateLimiter:   allowRateLimiter{},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/2fa/setup", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
	rec := httptest.NewRecorder()
	s.handle2FASetup(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	var errResp struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errResp.ErrorCode != string(ErrTotpAlreadyEnabled) {
		t.Fatalf("expected %s, got %s", ErrTotpAlreadyEnabled, errResp.ErrorCode)
	}
}

func TestHandle2FAEnable_RejectsWhenSecretSuperseded(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	const encryptionKey = "test-encryption-secret"
	user := createChangePasswordTestUser(t, database, false)

	plainSecretA := "JBSWY3DPEHPK3PXP"
	secretA, err := crypto.EncryptWithDomain(plainSecretA, encryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt secret A: %v", err)
	}
	if ok, err := db.SetUserTOTPSecret(database, user.ID, secretA); err != nil || !ok {
		t.Fatalf("store secret A: ok=%v, err=%v", ok, err)
	}

	codeA, err := totp.GenerateCode(plainSecretA, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code A: %v", err)
	}

	// Another setup request replaces secret A with secret B before enable finishes
	plainSecretB := "KRSXG5CTMVRXEZLU"
	secretB, err := crypto.EncryptWithDomain(plainSecretB, encryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt secret B: %v", err)
	}
	if ok, err := db.SetUserTOTPSecret(database, user.ID, secretB); err != nil || !ok {
		t.Fatalf("store secret B: ok=%v, err=%v", ok, err)
	}

	s := &APIServer{
		db:            database,
		encryptionKey: encryptionKey,
		jwtSecret:     "test-jwt-secret-at-least-32-bytes-long!!",
		rateLimiter:   allowRateLimiter{},
	}

	body, _ := json.Marshal(TOTPEnableRequest{Code: codeA})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/enable", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
	rec := httptest.NewRecorder()
	s.handle2FAEnable(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected enable to be rejected when secret was superseded")
	}

	updated, err := db.GetUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if updated.TotpEnabled {
		t.Fatal("expected TotpEnabled to remain false")
	}
}

// Note: There is no Go-level shared mutable state across goroutines; the safety
// guarantee comes entirely from the atomic database predicates.
func TestHandle2FA_ConcurrentSetupAndEnable(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	const encryptionKey = "test-encryption-secret"
	user := createChangePasswordTestUser(t, database, false)

	plainSecretA := "JBSWY3DPEHPK3PXP"
	secretA, err := crypto.EncryptWithDomain(plainSecretA, encryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt secret A: %v", err)
	}
	if ok, err := db.SetUserTOTPSecret(database, user.ID, secretA); err != nil || !ok {
		t.Fatalf("store secret A: ok=%v, err=%v", ok, err)
	}

	codeA, err := totp.GenerateCode(plainSecretA, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code A: %v", err)
	}

	s := &APIServer{
		db:            database,
		encryptionKey: encryptionKey,
		jwtSecret:     "test-jwt-secret-at-least-32-bytes-long!!",
		rateLimiter:   allowRateLimiter{},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var recEnable, recSetup *httptest.ResponseRecorder

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		body, _ := json.Marshal(TOTPEnableRequest{Code: codeA})
		req := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/enable", bytes.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
		recEnable = httptest.NewRecorder()
		s.handle2FAEnable(recEnable, req)
	}()

	go func() {
		defer wg.Done()
		<-start
		req := httptest.NewRequest(http.MethodGet, "/api/auth/2fa/setup", nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
		recSetup = httptest.NewRecorder()
		s.handle2FASetup(recSetup, req)
	}()

	close(start)
	wg.Wait()

	updated, err := db.GetUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}

	// Critical invariant:
	// If TOTP was enabled, the stored secret MUST be secret A (the verified secret).
	// Secret B must NEVER be enabled without verification.
	if updated.TotpEnabled {
		if updated.TotpSecretEnc != secretA {
			t.Fatalf("TOTP enabled with unverified secret! got %q, want %q", updated.TotpSecretEnc, secretA)
		}
		if recEnable.Code != http.StatusOK {
			t.Fatalf("expected enable to succeed if TotpEnabled is true, got %d", recEnable.Code)
		}
		if recSetup.Code != http.StatusOK && recSetup.Code != http.StatusConflict {
			t.Fatalf("unexpected setup code: %d", recSetup.Code)
		}
	} else {
		if recEnable.Code == http.StatusOK {
			t.Fatalf("expected enable to fail if TotpEnabled is false")
		}
	}
}

