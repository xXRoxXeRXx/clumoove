package auth

import (
	"testing"

	"backend/internal/db"
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

func TestRefreshToken(t *testing.T) {
	token, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("failed to generate refresh token: %v", err)
	}

	if len(token) != 64 { // hex of 32 bytes is 64 characters
		t.Errorf("expected hex-encoded refresh token of length 64, got length %d", len(token))
	}
}
