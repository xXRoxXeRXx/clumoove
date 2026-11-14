package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/totp2fa"

	_ "github.com/lib/pq"
)

// setupChangePassword2FATestDB connects to a real PostgreSQL (via DATABASE_URL)
// and bootstraps the production schema (db/schema.sql plus db.InitDB
// migrations) inside an isolated, per-test PostgreSQL schema, so fixtures
// never drift from the real tables.
// The test is skipped when no DATABASE_URL is configured.
func setupChangePassword2FATestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping change-password 2FA DB test")
	}

	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := adminDB.Ping(); err != nil {
		adminDB.Close()
		t.Fatalf("ping db: %v", err)
	}

	schemaName := fmt.Sprintf("api_account_test_%d", time.Now().UnixNano())
	if _, err := adminDB.Exec("CREATE SCHEMA " + schemaName); err != nil {
		adminDB.Close()
		t.Fatalf("create isolated schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec("DROP SCHEMA " + schemaName + " CASCADE")
		adminDB.Close()
	})

	// Mirror production bootstrap: Docker loads db/schema.sql first, then the
	// API runs InitDB migrations on top. Extensions live in public, so it stays
	// on the search path behind the isolated schema.
	if _, err := adminDB.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`); err != nil {
		t.Fatalf("create uuid-ossp extension: %v", err)
	}
	isolatedDSN := testDSNWithSearchPath(t, dsn, schemaName+",public")
	schemaSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "schema.sql"))
	if err != nil {
		t.Fatalf("read db/schema.sql: %v", err)
	}
	bootstrapDB, err := sql.Open("postgres", isolatedDSN)
	if err != nil {
		t.Fatalf("open isolated schema: %v", err)
	}
	if _, err := bootstrapDB.Exec(string(schemaSQL)); err != nil {
		bootstrapDB.Close()
		t.Fatalf("load db/schema.sql: %v", err)
	}
	bootstrapDB.Close()

	database, err := db.InitDB(isolatedDSN)
	if err != nil {
		t.Fatalf("InitDB() on isolated schema: %v", err)
	}
	return database
}

func testDSNWithSearchPath(t *testing.T, dsn, schemaName string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse DATABASE_URL: %v", err)
		}
		query := parsed.Query()
		query.Set("search_path", schemaName)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schemaName
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
	s.accounts().handleChangePassword(rec, req)
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

func TestHandleChangePassword_MustChange_ReusingOriginalTokenAfterRotationIsRejected(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	s := &APIServer{db: database, jwtSecret: "test-jwt-secret-at-least-32-bytes-long!!"}
	user := createChangePasswordTestUser(t, database, true)

	// Issue the original must-change temporary token as login would do.
	mustChangeToken, err := auth.GenerateMustChangePasswordToken(user, s.jwtSecret)
	if err != nil {
		t.Fatalf("generate must-change token: %v", err)
	}

	// 1. Password change succeeds using the temporary token.
	changeReq := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", strings.NewReader(`{"new_password":"BrandNewPassword-456","confirm_password":"BrandNewPassword-456"}`))
	changeReq.Header.Set("Authorization", "Bearer "+mustChangeToken)
	changeRec := httptest.NewRecorder()
	auth.AuthMiddlewareAllowMustChange(s.db, s.jwtSecret)(http.HandlerFunc(s.accounts().handleChangePassword)).ServeHTTP(changeRec, changeReq)
	if changeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from change-password, got %d: %s", changeRec.Code, changeRec.Body.String())
	}

	// 2. Caller attempts to present the original must-change token to a protected route (e.g. handleMe).
	// Because 2FA has not been completed, this token must NOT grant access.
	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+mustChangeToken)
	meRec := httptest.NewRecorder()
	auth.AuthMiddleware(s.db, s.jwtSecret)(http.HandlerFunc(s.accounts().handleMe)).ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when reusing original must-change token on protected route, got %d: %s", meRec.Code, meRec.Body.String())
	}
}

func TestHandle2FADisable_WithBackupCode(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	const encryptionKey = "test-encryption-key"
	s := &APIServer{db: database, encryptionKey: encryptionKey}
	user := createChangePasswordTestUser(t, database, false)
	secret, err := crypto.EncryptWithDomain("JBSWY3DPEHPK3PXP", encryptionKey, crypto.DomainTOTPSecret)
	if err != nil {
		t.Fatalf("encrypt TOTP secret: %v", err)
	}
	backupCodes, hashes, err := totp2fa.GenerateBackupCodes()
	if err != nil {
		t.Fatalf("generate backup codes: %v", err)
	}
	if ok, err := db.SetUserTOTPSecret(database, user.ID, secret); err != nil || !ok {
		t.Fatalf("store TOTP secret: ok=%v err=%v", ok, err)
	}
	if ok, err := db.EnableUserTOTP(database, user.ID, secret, db.StringArray(hashes)); err != nil || !ok {
		t.Fatalf("enable TOTP: ok=%v err=%v", ok, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/disable", strings.NewReader(`{"code":"`+backupCodes[0]+`"}`))
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: user.ID}))
	rec := httptest.NewRecorder()
	s.accounts().handle2FADisable(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, err := db.GetUserByID(database, user.ID)
	if err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if updated.TotpEnabled || len(updated.TotpBackupCodes) != 0 {
		t.Fatalf("expected 2FA and all backup-code hashes to be cleared, got enabled=%v codes=%d", updated.TotpEnabled, len(updated.TotpBackupCodes))
	}
}

func TestSuspensionRevokesRefreshTokensAndBlocksRefresh(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()

	s := &APIServer{db: database, jwtSecret: "test-jwt-secret-at-least-32-bytes-long!!"}
	user := createChangePasswordTestUser(t, database, false)
	refreshToken := "refresh-token-that-must-be-revoked"
	if err := db.StoreRefreshToken(context.Background(), database, hashToken(refreshToken), user.ID, time.Now().Add(time.Hour), "test-agent"); err != nil {
		t.Fatalf("store refresh token: %v", err)
	}

	if _, err := db.SuspendUser(database, user.ID); err != nil {
		t.Fatalf("suspend user: %v", err)
	}

	var tokens int
	if err := database.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`, user.ID).Scan(&tokens); err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("suspension left %d refresh tokens", tokens)
	}

	// Simulate a stale token that was read before the suspension transaction
	// committed. Refresh must still refuse it after checking users.active.
	if err := db.StoreRefreshToken(context.Background(), database, hashToken(refreshToken), user.ID, time.Now().Add(time.Hour), "test-agent"); err != nil {
		t.Fatalf("store stale refresh token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshToken})
	rec := httptest.NewRecorder()
	s.accounts().handleRefresh(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected suspended user refresh to return 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.ErrorCode != string(ErrUserDisabled) {
		t.Fatalf("error_code = %q, want %q", body.ErrorCode, ErrUserDisabled)
	}

	var clearedCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "refresh_token" {
			clearedCookie = cookie
			break
		}
	}
	if clearedCookie == nil || clearedCookie.Value != "" || clearedCookie.MaxAge >= 0 {
		t.Fatalf("refresh cookie was not cleared: %#v", clearedCookie)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM refresh_tokens WHERE user_id = $1`, user.ID).Scan(&tokens); err != nil {
		t.Fatalf("count residual refresh tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("inactive refresh cleanup left %d refresh tokens", tokens)
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
