package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"backend/internal/db"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type Claims struct {
	UserID             string `json:"sub"`
	Email              string `json:"email"`
	DisplayName        string `json:"name"`
	Role               string `json:"role"`
	TwoFAPending       bool   `json:"2fa_pending"`
	MustChangePassword bool   `json:"must_change_password"`
	jwt.RegisteredClaims
}

const (
	tokenIssuer       = "clumoove-api"
	accessTokenTTL    = 15 * time.Minute
	temporaryTokenTTL = 5 * time.Minute

	// MaxPasswordBytes is bcrypt's maximum accepted password size. Password
	// validation rejects larger values before they reach bcrypt, so distinct
	// passphrases cannot be treated as equal after a 72-byte truncation.
	MaxPasswordBytes = 72
)

// HashPassword hashes a raw password using bcrypt. API password validation
// enforces MaxPasswordBytes before calling this; this check protects any future
// non-HTTP callers too.
func HashPassword(password string) (string, error) {
	if len(password) > MaxPasswordBytes {
		return "", fmt.Errorf("password exceeds bcrypt's %d-byte limit", MaxPasswordBytes)
	}
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

// CheckPasswordHash checks if a raw password matches a bcrypt hash
func CheckPasswordHash(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// generateToken issues an HS256 JWT for a specific authentication state.
// Access tokens reflect the account's must-change state; temporary tokens use
// only their explicit marker so a 2FA session cannot double as a password
// rotation session.
func generateToken(user *db.User, secretKey string, ttl time.Duration, twoFA, mustChange bool) (string, error) {
	if secretKey == "" {
		return "", errors.New("empty JWT secret key")
	}

	now := time.Now()
	claims := &Claims{
		UserID:             user.ID,
		Email:              user.Email,
		DisplayName:        user.DisplayName,
		Role:               user.Role,
		TwoFAPending:       twoFA,
		MustChangePassword: mustChange,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    tokenIssuer,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

// GenerateAccessToken generates a short-lived (15 minutes) JWT token.
func GenerateAccessToken(user *db.User, secretKey string) (string, error) {
	return generateToken(user, secretKey, accessTokenTTL, false, user.MustChangePassword)
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
	}, jwt.WithIssuer(tokenIssuer), jwt.WithLeeway(30*time.Second))

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
	return generateToken(user, secretKey, temporaryTokenTTL, true, false)
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

// GenerateMustChangePasswordToken issues a short-lived (5 minutes) JWT carrying
// the MustChangePassword marker. It is returned by the login endpoint when the
// account must rotate its password before any other access is granted. Crucially
// TwoFAPending is FALSE, so it is not mistaken for an incomplete 2FA auth state;
// a dedicated middleware allowing must-change tokens is used for the password
// rotation route.
func GenerateMustChangePasswordToken(user *db.User, secretKey string) (string, error) {
	return generateToken(user, secretKey, temporaryTokenTTL, false, true)
}

// RequireAuthenticated returns an error if the claims represent a token that is
// not fully authenticated. This rejects both 2FA temp tokens (awaiting the
// second factor) and must-change-password tokens (awaiting password rotation).
// Every full-auth boundary must call this after ValidateToken.
func RequireAuthenticated(claims *Claims) error {
	if claims == nil {
		return errors.New("missing claims")
	}
	if claims.TwoFAPending {
		return errors.New("second factor required")
	}
	if claims.MustChangePassword {
		return errors.New("password change required")
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

// SetRefreshTokenCookie sets the refresh token in an HTTP-only Secure, SameSite=None
// cookie. Secure is intentionally unconditional: refresh tokens must never be sent
// over HTTP, including when TLS is terminated upstream.
func SetRefreshTokenCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    token,
		Path:     "/api/auth", // Restricted to auth endpoints for security
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})
}

// ClearRefreshTokenCookie clears the refresh-token cookie, using the same Secure and
// SameSite attributes as SetRefreshTokenCookie.
func ClearRefreshTokenCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    "",
		Path:     "/api/auth",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
	})
}
