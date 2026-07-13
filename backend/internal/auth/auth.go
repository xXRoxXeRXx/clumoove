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
	TwoFAPending bool  `json:"2fa_pending"`
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
			Issuer:    "clumove-api",
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

// Generate2FATempToken issues a short-lived (5 minutes) JWT carrying the
// TwoFAPending marker. It is returned by the login endpoint when 2FA is enabled
// and must be presented to /api/auth/totp to complete authentication.
func Generate2FATempToken(user *db.User, secretKey string) (string, error) {
	if secretKey == "" {
		return "", errors.New("empty JWT secret key")
	}

	expirationTime := time.Now().Add(5 * time.Minute)
	claims := &Claims{
		UserID:       user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		Role:         user.Role,
		TwoFAPending: true,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expirationTime),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
			Issuer:    "clumove-api",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secretKey))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

// Validate2FATempToken parses and validates a 2FA temp token, ensuring it
// actually carries the TwoFAPending marker.
func Validate2FATempToken(tokenStr, secretKey string) (*Claims, error) {
	claims, err := ValidateToken(tokenStr, secretKey)
	if err != nil {
		return nil, err
	}
	if !claims.TwoFAPending {
		return nil, errors.New("not a 2FA pending token")
	}
	return claims, nil
}

// RequireAuthenticated returns an error if the claims represent a token that is
// not fully authenticated (e.g. a 2FA temp token still awaiting the second
// factor). It must be called at every full-auth boundary after ValidateToken.
func RequireAuthenticated(claims *Claims) error {
	if claims == nil {
		return errors.New("missing claims")
	}
	if claims.TwoFAPending {
		return errors.New("second factor required")
	}
	return nil
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

// SetRefreshTokenCookie sets the refresh token in an HTTP-only, secure, SameSite cookie.
// When the connection is HTTPS (production), SameSite=None is used so the cookie is sent
// on cross-site credentialed requests (e.g. app.example.com → api.example.com).
// SameSite=None requires Secure=true, which is already true on HTTPS.
// On plain HTTP (local dev), SameSite=Lax is used; same-site localhost still works
// because browsers ignore port differences for the SameSite check.
func SetRefreshTokenCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	// Determine if connection is HTTPS (either TLS is active, or we are behind a proxy with X-Forwarded-Proto)
	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"

	sameSite := http.SameSiteLaxMode
	if isSecure {
		// SameSite=None is required for cross-site cookie delivery; only valid over HTTPS.
		sameSite = http.SameSiteNoneMode
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/api/auth", // Restricted to auth endpoints for security
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
	})
}

// ClearRefreshTokenCookie clears the refresh token cookie.
// Must mirror the same Secure/SameSite attributes used when setting it, otherwise browsers
// treat it as a different cookie and the clear has no effect.
func ClearRefreshTokenCookie(w http.ResponseWriter, r *http.Request) {
	isSecure := r.TLS != nil || strings.ToLower(r.Header.Get("X-Forwarded-Proto")) == "https"

	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/auth",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: sameSite,
	})
}
