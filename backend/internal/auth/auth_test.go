package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/db"

	"github.com/golang-jwt/jwt/v5"
)

func TestHashPassword(t *testing.T) {
	password := "supersecure123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	if hash == password {
		t.Errorf("expected hash to be different from password, but they are identical")
	}

	if !CheckPasswordHash(password, hash) {
		t.Errorf("expected CheckPasswordHash to return true for correct password")
	}

	if CheckPasswordHash("wrongpassword", hash) {
		t.Errorf("expected CheckPasswordHash to return false for incorrect password")
	}
}

func TestHashPasswordRejectsPasswordsOverBcryptLimit(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1))
	if err == nil {
		t.Fatalf("expected password over bcrypt limit to be rejected")
	}
}

func TestAccessToken(t *testing.T) {
	secretKey := "test-secret-key-12345-67890-abcdef"
	user := &db.User{
		ID:          "user-uuid-1",
		Email:       "test@example.com",
		DisplayName: "Test User",
		Role:        "USER",
	}

	token, err := GenerateAccessToken(user, secretKey)
	if err != nil {
		t.Fatalf("failed to generate access token: %v", err)
	}

	claims, err := ValidateToken(token, secretKey)
	if err != nil {
		t.Fatalf("failed to validate token: %v", err)
	}

	if claims.UserID != user.ID {
		t.Errorf("expected claims.UserID to be %q, got %q", user.ID, claims.UserID)
	}

	if claims.Email != user.Email {
		t.Errorf("expected claims.Email to be %q, got %q", user.Email, claims.Email)
	}

	if claims.DisplayName != user.DisplayName {
		t.Errorf("expected claims.DisplayName to be %q, got %q", user.DisplayName, claims.DisplayName)
	}

	if claims.Role != user.Role {
		t.Errorf("expected claims.Role to be %q, got %q", user.Role, claims.Role)
	}

	// Test invalid signature
	_, err = ValidateToken(token, "wrong-secret-key")
	if err == nil {
		t.Errorf("expected validation to fail for incorrect secret key, but it succeeded")
	}
}

func TestGeneratedTokenStates(t *testing.T) {
	secret := "test-secret-key-12345-67890-abcdef"
	user := &db.User{ID: "user-uuid-1", MustChangePassword: true}

	tests := []struct {
		name               string
		generate           func(*db.User, string) (string, error)
		wantTwoFAPending   bool
		wantMustChangePass bool
	}{
		{"access", GenerateAccessToken, false, true},
		{"two-factor temporary", Generate2FATempToken, true, false},
		{"must-change temporary", GenerateMustChangePasswordToken, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := tt.generate(user, secret)
			if err != nil {
				t.Fatalf("generate token: %v", err)
			}
			claims, err := ValidateToken(token, secret)
			if err != nil {
				t.Fatalf("validate token: %v", err)
			}
			if claims.TwoFAPending != tt.wantTwoFAPending || claims.MustChangePassword != tt.wantMustChangePass {
				t.Fatalf("unexpected token state: twoFA=%v mustChange=%v", claims.TwoFAPending, claims.MustChangePassword)
			}
		})
	}
}

func TestValidate2FATempTokenRejectsAccessToken(t *testing.T) {
	secret := "test-secret-key-12345-67890-abcdef"
	token, err := GenerateAccessToken(&db.User{ID: "user-uuid-1"}, secret)
	if err != nil {
		t.Fatalf("generate access token: %v", err)
	}
	if _, err := Validate2FATempToken(token, secret); err == nil {
		t.Fatal("expected access token to be rejected as a 2FA temp token")
	}
}

func TestRequireAuthenticated(t *testing.T) {
	tests := []struct {
		name    string
		claims  *Claims
		wantErr bool
	}{
		{"nil claims", nil, true},
		{"two-factor pending", &Claims{TwoFAPending: true}, true},
		{"must change password", &Claims{MustChangePassword: true}, true},
		{"fully authenticated", &Claims{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequireAuthenticated(tt.claims)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RequireAuthenticated() error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTokenRejectsUnexpectedIssuer(t *testing.T) {
	secret := "test-secret-key-12345-67890-abcdef"
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "another-service",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	})
	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	if _, err := ValidateToken(tokenString, secret); err == nil {
		t.Fatal("expected token with unexpected issuer to be rejected")
	}
}

func TestRefreshToken(t *testing.T) {
	token, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("failed to generate refresh token: %v", err)
	}

	if len(token) != 64 { // hex of 32 bytes is 64 characters
		t.Errorf("expected hex-encoded refresh token of length 64, got length %d", len(token))
	}
}

func TestRefreshTokenCookieAttributes(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	rec := httptest.NewRecorder()
	SetRefreshTokenCookie(rec, "refresh-value", expiresAt)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "refresh_token" || cookie.Value != "refresh-value" || cookie.Path != "/api/auth" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteNoneMode || !cookie.Expires.Equal(expiresAt) {
		t.Fatalf("unexpected refresh cookie: %+v", cookie)
	}

	rec = httptest.NewRecorder()
	ClearRefreshTokenCookie(rec)
	cookies = rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected one cleared cookie, got %d", len(cookies))
	}
	cookie = cookies[0]
	if cookie.Name != "refresh_token" || cookie.Path != "/api/auth" || cookie.MaxAge >= 0 || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteNoneMode {
		t.Fatalf("unexpected cleared refresh cookie: %+v", cookie)
	}
}
