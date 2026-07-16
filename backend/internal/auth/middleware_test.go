package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/db"
)

func testUser() *db.User {
	return &db.User{
		ID:          "user-uuid-1",
		Email:       "test@example.com",
		DisplayName: "Test User",
		Role:        "USER",
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := GetUserIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(uid))
	})
}

func TestAuthMiddlewareNoHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	AuthMiddleware("secret-key-32-bytes-long-abcdefghij!!")(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "UNAUTHORIZED") {
		t.Errorf("expected UNAUTHORIZED error_code, got body %q", rec.Body.String())
	}
}

func TestAuthMiddlewareMalformedHeader(t *testing.T) {
	cases := []string{"", "Token abc", "bearer", "Basic abcdef"}
	for _, h := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		AuthMiddleware("secret-key-32-bytes-long-abcdefghij!!")(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", h, rec.Code)
		}
	}
}

func TestAuthMiddlewareValidToken(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := GenerateAccessToken(testUser(), secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	AuthMiddleware(secret)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "user-uuid-1" {
		t.Errorf("expected user id in body, got %q", rec.Body.String())
	}
}

func TestAuthMiddlewareRejects2FATempToken(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := Generate2FATempToken(testUser(), secret)
	if err != nil {
		t.Fatalf("Generate2FATempToken failed: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	AuthMiddleware(secret)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 2FA temp token to be rejected with 401, got %d", rec.Code)
	}
}

func TestAuthMiddlewareAllowMustChangePermitsMustChange(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	user := testUser()
	user.MustChangePassword = true
	token, err := GenerateMustChangePasswordToken(user, secret)
	if err != nil {
		t.Fatalf("GenerateMustChangePasswordToken failed: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/change", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	AuthMiddlewareAllowMustChange(secret)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected must-change token allowed, got %d (body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "user-uuid-1" {
		t.Errorf("expected user id in body, got %q", rec.Body.String())
	}
}

func TestAuthMiddlewareAllowMustChangeRejects2FA(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := Generate2FATempToken(testUser(), secret)
	if err != nil {
		t.Fatalf("Generate2FATempToken failed: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/change", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	AuthMiddlewareAllowMustChange(secret)(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 2FA temp token rejected by AllowMustChange, got %d", rec.Code)
	}
}
