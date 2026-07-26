package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/db"

	_ "github.com/lib/pq"
)

// setupChangePassword2FATestDB connects to a real PostgreSQL (via DATABASE_URL)
// and creates the minimal schema needed to exercise the forced-password-change
// / 2FA-enforcement interaction. The test is skipped when no DATABASE_URL is
// configured, so it does not fail in environments without a database.
func setupChangePassword2FATestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping change-password 2FA DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	schema := []string{
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			email VARCHAR(255) UNIQUE NOT NULL,
			password_hash VARCHAR(255) NOT NULL,
			display_name VARCHAR(255) NOT NULL DEFAULT '',
			role VARCHAR(32) NOT NULL DEFAULT 'USER',
			active BOOLEAN NOT NULL DEFAULT TRUE,
			must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
			avatar BYTEA,
			avatar_mime VARCHAR(64),
			totp_enabled BOOLEAN NOT NULL DEFAULT FALSE,
			totp_secret_enc TEXT,
			totp_backup_codes JSONB,
			totp_failed_attempts INT NOT NULL DEFAULT 0,
			totp_locked_until TIMESTAMP WITH TIME ZONE,
			login_failed_attempts INT NOT NULL DEFAULT 0,
			login_locked_until TIMESTAMP WITH TIME ZONE,
			created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS refresh_tokens (
			token_hash TEXT PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id BIGSERIAL PRIMARY KEY,
			user_id UUID,
			action TEXT NOT NULL,
			target TEXT,
			ip TEXT,
			details JSONB,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
	}
	for _, s := range schema {
		if _, err := database.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	return database
}

func createChangePasswordTestUser(t *testing.T, database *sql.DB, totpEnabled bool) *db.User {
	t.Helper()
	hash, err := auth.HashPassword("initial-password-123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	var u db.User
	email := "chpw-2fa-" + generateRandomString(12) + "@example.test"
	err = database.QueryRow(
		`INSERT INTO users (email, password_hash, display_name, role, active, must_change_password, totp_enabled)
		 VALUES ($1, $2, $3, 'USER', TRUE, TRUE, $4)
		 RETURNING id, email`,
		email, hash, "Test User", totpEnabled,
	).Scan(&u.ID, &u.Email)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = database.Exec(`DELETE FROM users WHERE id = $1`, u.ID)
	})
	return &u
}

func changePasswordRequest(t *testing.T, s *APIServer, userID string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"new_password":"BrandNewPassword-456","confirm_password":"BrandNewPassword-456"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(body))
	claims := &auth.Claims{UserID: userID, MustChangePassword: true}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	s.handleChangePassword(rec, req)
	return rec
}

func TestHandleChangePassword_MustChange_With2FA_RequiresTOTP(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	s := &APIServer{db: database, jwtSecret: "test-jwt-secret-at-least-32-bytes-long!!"}
	user := createChangePasswordTestUser(t, database, true)

	rec := changePasswordRequest(t, s, user.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["totp_required"] != true {
		t.Errorf("expected totp_required=true, got %v", resp["totp_required"])
	}
	temp, _ := resp["temp_session"].(string)
	if temp == "" {
		t.Errorf("expected non-empty temp_session")
	}
	if _, ok := resp["access_token"]; ok {
		t.Errorf("expected NO access_token in 2FA response, got %v", resp["access_token"])
	}

	claims, err := auth.Validate2FATempToken(temp, s.jwtSecret)
	if err != nil {
		t.Fatalf("temp_session is not a valid 2FA temp token: %v", err)
	}
	if claims.MustChangePassword {
		t.Errorf("2FA temp token must not carry a stale must_change_password flag")
	}
}

func TestHandleChangePassword_MustChange_No2FA_ReturnsAccessToken(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	s := &APIServer{db: database, jwtSecret: "test-jwt-secret-at-least-32-bytes-long!!"}
	user := createChangePasswordTestUser(t, database, false)

	rec := changePasswordRequest(t, s, user.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if _, ok := resp["success"]; ok {
		t.Errorf("expected no success field, got %v", resp["success"])
	}
	token, _ := resp["access_token"].(string)
	if token == "" {
		t.Errorf("expected non-empty access_token")
	}
	if _, ok := resp["user"]; !ok {
		t.Errorf("expected user in response")
	}
	if _, ok := resp["totp_required"]; ok {
		t.Errorf("did not expect totp_required for a non-2FA account")
	}
}
