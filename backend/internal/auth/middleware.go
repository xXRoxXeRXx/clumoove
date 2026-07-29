package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"backend/internal/db"
	"backend/internal/storage"
)

type ContextKey string

const ClaimsKey ContextKey = "claims"

// writeUnauthorized emits a 401 response carrying only the machine-readable
// error_code (errors.UNAUTHORIZED), matching the rest of the API's error
// convention. Returning a structured code (rather than English text) lets the
// frontend localize via translateApiError and avoids leaking request details.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":    false,
		"error_code": "UNAUTHORIZED",
	})
}

// AuthStateLookup retrieves the authoritative, current account state for an
// authenticated user. It keeps middleware testable while production middleware
// always queries the database on every protected request.
type AuthStateLookup func(id string) (*db.UserAuthState, error)

func databaseAuthStateLookup(database *sql.DB) AuthStateLookup {
	return func(id string) (*db.UserAuthState, error) {
		return db.GetUserAuthState(database, id)
	}
}

// RefreshClaimsFromAuthState fails closed when an account is missing or
// suspended, and copies mutable authorization claims from the database.
func RefreshClaimsFromAuthState(claims *Claims, state *db.UserAuthState) error {
	if claims == nil || state == nil || !state.Active {
		return errors.New("inactive or missing user")
	}
	claims.Role = state.Role
	claims.MustChangePassword = state.MustChangePassword
	return nil
}

func refreshClaims(claims *Claims, lookup AuthStateLookup) error {
	if lookup == nil {
		return errors.New("missing auth state lookup")
	}
	state, err := lookup(claims.UserID)
	if err != nil {
		return err
	}
	return RefreshClaimsFromAuthState(claims, state)
}

// AuthMiddleware intercepts requests to validate the JWT bearer token and
// checks the user's active status and role against the database.
func AuthMiddleware(database *sql.DB, secretKey string) func(http.Handler) http.Handler {
	return AuthMiddlewareWithAuthStateLookup(secretKey, databaseAuthStateLookup(database))
}

// AuthMiddlewareWithAuthStateLookup is the testable form of AuthMiddleware.
func AuthMiddlewareWithAuthStateLookup(secretKey string, lookup AuthStateLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeUnauthorized(w)
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				writeUnauthorized(w)
				return
			}

			tokenStr := parts[1]
			claims, err := ValidateToken(tokenStr, secretKey)
			if err != nil {
				writeUnauthorized(w)
				return
			}

			// Reject 2FA temp tokens: they authenticate the password step only and
			// must never grant access to protected routes before the second factor.
			if claims.TwoFAPending || refreshClaims(claims, lookup) != nil {
				writeUnauthorized(w)
				return
			}
			if err := RequireAuthenticated(claims); err != nil {
				writeUnauthorized(w)
				return
			}

			// Inject full Claims into request context
			ctx := storage.WithLocalUserScope(context.WithValue(r.Context(), ClaimsKey, claims), claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AuthMiddlewareAllowMustChange validates the JWT and allows must-change-password
// temp tokens (used by the forced password-rotation flow) through to the
// change-password route. It still rejects 2FA temp tokens, which are a distinct,
// incomplete auth state that must never reach a protected route.
func AuthMiddlewareAllowMustChange(database *sql.DB, secretKey string) func(http.Handler) http.Handler {
	return AuthMiddlewareAllowMustChangeWithAuthStateLookup(secretKey, databaseAuthStateLookup(database))
}

// AuthMiddlewareAllowMustChangeWithAuthStateLookup is the testable form of
// AuthMiddlewareAllowMustChange.
func AuthMiddlewareAllowMustChangeWithAuthStateLookup(secretKey string, lookup AuthStateLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				writeUnauthorized(w)
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				writeUnauthorized(w)
				return
			}

			claims, err := ValidateToken(parts[1], secretKey)
			if err != nil {
				writeUnauthorized(w)
				return
			}

			// 2FA temp tokens are still rejected; only MustChangePassword is permitted.
			if claims.TwoFAPending {
				writeUnauthorized(w)
				return
			}
			tokenMustChange := claims.MustChangePassword
			if refreshClaims(claims, lookup) != nil || tokenMustChange != claims.MustChangePassword {
				writeUnauthorized(w)
				return
			}

			ctx := storage.WithLocalUserScope(context.WithValue(r.Context(), ClaimsKey, claims), claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserIDFromContext retrieves the authenticated user's ID from the context
func GetUserIDFromContext(ctx context.Context) string {
	if val := ctx.Value(ClaimsKey); val != nil {
		if claims, ok := val.(*Claims); ok {
			return claims.UserID
		}
	}
	return ""
}
