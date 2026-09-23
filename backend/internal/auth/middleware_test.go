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
	cases := []string{"", "Token abc", "bearer", "Basic abcdef", "Bearer", "Bearer    "}
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

func TestAuthMiddlewareAcceptsWhitespaceAroundBearerToken(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"
	token, err := GenerateAccessToken(testUser(), secret)
	if err != nil {
		t.Fatalf("GenerateAccessToken failed: %v", err)
	}
	for _, header := range []string{"Bearer  " + token, "Bearer\t" + token, "  Bearer " + token + "  ", "bearer " + token} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", header)
		AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(testUser()))(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("header %q: expected 200, got %d", header, rec.Code)
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

func TestAuthMiddlewareRejectsMustChangeTokenBeforeAndAfterRotation(t *testing.T) {
	secret := "secret-key-32-bytes-long-abcdefghij!!"

	// Both standard users and admin users must not be able to bypass 2FA / auth
	// boundaries by reusing a temporary password-change token.
	roles := []string{"USER", "ADMIN"}
	for _, role := range roles {
		t.Run("role_"+role, func(t *testing.T) {
			user := testUser()
			user.Role = role
			user.MustChangePassword = true
			token, err := GenerateMustChangePasswordToken(user, secret)
			if err != nil {
				t.Fatalf("GenerateMustChangePasswordToken failed: %v", err)
			}

			// 1. Token presented before rotation (user.MustChangePassword is still true).
			recBefore := httptest.NewRecorder()
			reqBefore := httptest.NewRequest(http.MethodGet, "/protected", nil)
			reqBefore.Header.Set("Authorization", "Bearer "+token)
			AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(user))(okHandler()).ServeHTTP(recBefore, reqBefore)

			if recBefore.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 before rotation, got %d", recBefore.Code)
			}

			// 2. Token presented after password rotation:
			// In the database, must_change_password is now false.
			// Middleware must NOT promote the temporary token into an access token.
			rotatedUser := testUser()
			rotatedUser.Role = role
			rotatedUser.MustChangePassword = false

			recAfter := httptest.NewRecorder()
			reqAfter := httptest.NewRequest(http.MethodGet, "/protected", nil)
			reqAfter.Header.Set("Authorization", "Bearer "+token)
			AuthMiddlewareWithAuthStateLookup(secret, authStateLookup(rotatedUser))(okHandler()).ServeHTTP(recAfter, reqAfter)

			if recAfter.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 after rotation, got %d (body %q)", recAfter.Code, recAfter.Body.String())
			}
		})
	}
}

func TestRefreshClaimsFromAuthStateDoesNotPromoteTemporaryToken(t *testing.T) {
	// A temporary token signed with MustChangePassword=true must never have
	// that marker cleared to false by RefreshClaimsFromAuthState.
	claims := &Claims{
		UserID:             "user-1",
		Role:               "USER",
		MustChangePassword: true,
	}
	state := &db.UserAuthState{
		Role:               "ADMIN",
		Active:             true,
		MustChangePassword: false,
	}

	if err := RefreshClaimsFromAuthState(claims, state); err != nil {
		t.Fatalf("RefreshClaimsFromAuthState returned unexpected error: %v", err)
	}

	if !claims.MustChangePassword {
		t.Errorf("claims.MustChangePassword was cleared to false; temporary token was promoted to access token")
	}
	if claims.Role != "ADMIN" {
		t.Errorf("expected role to be refreshed to ADMIN, got %q", claims.Role)
	}

	// Normal access tokens (MustChangePassword=false) are restricted to true if DB requires rotation.
	normalClaims := &Claims{
		UserID:             "user-2",
		Role:               "USER",
		MustChangePassword: false,
	}
	forcedState := &db.UserAuthState{
		Role:               "USER",
		Active:             true,
		MustChangePassword: true,
	}
	if err := RefreshClaimsFromAuthState(normalClaims, forcedState); err != nil {
		t.Fatalf("RefreshClaimsFromAuthState returned unexpected error: %v", err)
	}
	if !normalClaims.MustChangePassword {
		t.Errorf("expected normal claims to be restricted when DB requires password change")
	}
}

