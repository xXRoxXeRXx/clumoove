package auth

import (
	"errors"
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
		Active:      true,
	}
}

func authStateLookup(user *db.User) AuthStateLookup {
	return func(id string) (*db.UserAuthState, error) {
		if id != user.ID {
			return nil, nil
		}
		return &db.UserAuthState{
			Role:               user.Role,
			Active:             user.Active,
			MustChangePassword: user.MustChangePassword,
		}, nil
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
	AuthMiddlewareWithAuthStateLookup("secret-key-32-bytes-long-abcdefghij!!", authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)

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
		AuthMiddlewareWithAuthStateLookup("secret-key-32-bytes-long-abcdefghij!!", authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)
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
	AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)

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
	AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 2FA temp token to be rejected with 401, got %d", rec.Code)
	}
}

func TestAuthMiddlewareRejectsSuspendedUserWithValidToken(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := GenerateAccessToken(testUser(), secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	suspended := testUser()
	suspended.Active = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(suspended))(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected suspended user to be rejected with 401, got %d", rec.Code)
	}
}

func TestAuthMiddlewareUsesCurrentRoleInsteadOfTokenRole(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	user := testUser()
	user.Role = "ADMIN"
	token, err := GenerateAccessToken(user, secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	currentUser := testUser()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := r.Context().Value(ClaimsKey).(*Claims)
		_, _ = w.Write([]byte(claims.Role))
	})

	AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(currentUser))(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "USER" {
		t.Errorf("expected current USER role, got status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddlewareRejectsAccessTokenWhenPasswordChangeIsNowRequired(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := GenerateAccessToken(testUser(), secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	currentUser := testUser()
	currentUser.MustChangePassword = true
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(currentUser))(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected access token blocked after forced password change, got %d", rec.Code)
	}
}

func TestAuthMiddlewareFailsClosedForMissingUserAndLookupError(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := GenerateAccessToken(testUser(), secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	cases := []struct {
		name   string
		lookup AuthStateLookup
	}{
		{"missing user", func(string) (*db.UserAuthState, error) { return nil, nil }},
		{"lookup error", func(string) (*db.UserAuthState, error) { return nil, errors.New("database unavailable") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			AuthMiddlewareWithAuthStateLookup(secret, tc.lookup)(okHandler()).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rec.Code)
			}
		})
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
	AuthMiddlewareAllowMustChangeWithAuthStateLookup(secret, authStateLookup(user))(okHandler()).ServeHTTP(rec, req)

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
	AuthMiddlewareAllowMustChangeWithAuthStateLookup(secret, authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 2FA temp token rejected by AllowMustChange, got %d", rec.Code)
	}
}

func TestAuthMiddlewareAllowMustChangeRejectsSuspendedAndStaleMustChangeTokens(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	user := testUser()
	user.MustChangePassword = true
	token, err := GenerateMustChangePasswordToken(user, secret)
	if err != nil {
		t.Fatalf("GenerateMustChangePasswordToken failed: %v", err)
	}
	cases := []struct {
		name    string
		current *db.User
	}{
		{"suspended", func() *db.User { u := testUser(); u.MustChangePassword = true; u.Active = false; return u }()},
		{"password already changed", testUser()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/change", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			AuthMiddlewareAllowMustChangeWithAuthStateLookup(secret, authStateLookup(tc.current))(okHandler()).ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rec.Code)
			}
		})
	}
}
