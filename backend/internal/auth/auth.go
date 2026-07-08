package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"backend/internal/db"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	UserID      string `json:"sub"`
	Email       string `json:"email"`
	DisplayName string `json:"name"`
	Role        string `json:"role"`
	jwt.RegisteredClaims
}

// HashPassword hashes a raw password using bcrypt
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

// CheckPasswordHash checks if a raw password matches a bcrypt hash
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// GenerateAccessToken generates a short-lived (15 minutes) JWT token
func GenerateAccessToken(user *db.User, secretKey string) (string, error) {
	if secretKey == "" {
		return "", errors.New("empty JWT secret key")
	}

	expirationTime := time.Now().Add(15 * time.Minute)
	claims := &Claims{
		UserID:      user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "cloudmove-api",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

// ValidateToken parses and validates a JWT access token
func ValidateToken(tokenStr, secretKey string) (*Claims, error) {
	if secretKey == "" {
		return nil, errors.New("empty JWT secret key")
	}

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

// GenerateRefreshToken generates a secure, random refresh token
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SetRefreshTokenCookie sets the refresh token in an HTTP-only, secure, SameSite cookie
func SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	// Determine if connection is HTTPS (either TLS is active, or we are behind a proxy with X-Forwarded-Proto)
	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"

	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/api/auth", // Restricted to auth endpoints for security
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode, // Lax is suitable for cross-origin credentials exchange (e.g. Vite to Go API)
	})
}

// ClearRefreshTokenCookie clears the refresh token cookie
func ClearRefreshTokenCookie(w http.ResponseWriter, r *http.Request) {
	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"

	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/auth",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
