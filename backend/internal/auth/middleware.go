package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"backend/internal/db"
	"backend/internal/httpresp"
	"backend/internal/storage"
)

type ContextKey string

const ClaimsKey ContextKey = "claims"

// writeUnauthorized emits a 401 response carrying only the machine-readable
// error_code (errors.UNAUTHORIZED), matching the rest of the API's error
// convention. Returning a structured code (rather than English text) lets the
// frontend localize via translateApiError and avoids leaking request details.
func writeUnauthorized(w http.ResponseWriter) {
	httpresp.WriteError(w, http.StatusUnauthorized, httpresp.ErrUnauthorized)
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
// Database state may further restrict authorization, but must never promote
// a temporary token into an access token. The nil-state check is defense-in-depth:
// db.GetUserAuthState reports a missing user as sql.ErrNoRows, while tests
// and alternative lookups may return nil.
func RefreshClaimsFromAuthState(claims *Claims, state *db.UserAuthState) error {
	if claims == nil || state == nil || !state.Active {
		return errors.New("inactive or missing user")
	}
	claims.Role = state.Role
	claims.MustChangePassword = claims.MustChangePassword || state.MustChangePassword
	return nil
}

func refreshClaims(claims *Claims, lookup AuthStateLookup) (*db.UserAuthState, error) {
	if lookup == nil {
		return nil, errors.New("missing auth state lookup")
	}
	state, err := lookup(claims.UserID)
	if err != nil {
		return nil, err
	}
	if err := RefreshClaimsFromAuthState(claims, state); err != nil {
		return nil, err
	}
	return state, nil
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

			tokenStr, ok := bearerToken(authHeader)
			if !ok {
				writeUnauthorized(w)
				return
			}

			claims, err := ValidateToken(tokenStr, secretKey)
			if err != nil {
				writeUnauthorized(w)
				return
			}

			// Reject temporary tokens (incomplete authentication) before refreshing
			// mutable account state. Intermediate tokens (2FA pending or must-change
			// password tokens) authenticate an intermediate step only and must never
			// grant access to protected routes, even if account state has since been updated.
			if err := RequireAuthenticated(claims); err != nil {
				writeUnauthorized(w)
				return
			}
			if _, err := refreshClaims(claims, lookup); err != nil {
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

			tokenStr, ok := bearerToken(authHeader)
			if !ok {
				writeUnauthorized(w)
				return
			}

			claims, err := ValidateToken(tokenStr, secretKey)
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
			state, err := refreshClaims(claims, lookup)
			if err != nil {
				writeUnauthorized(w)
				return
			}
			// The token's must-change marker must match the current DB state.
			// A mismatch means the password has already been rotated and this
			// token is stale; reject it so it cannot be replayed.
			if tokenMustChange != state.MustChangePassword {
				writeUnauthorized(w)
				return
			}

			ctx := storage.WithLocalUserScope(context.WithValue(r.Context(), ClaimsKey, claims), claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerToken extracts a non-empty Bearer token while allowing surrounding
// whitespace between the auth scheme and credentials.
func bearerToken(authHeader string) (string, bool) {
	authHeader = strings.TrimSpace(authHeader)
	const bearerScheme = "Bearer"
	if len(authHeader) <= len(bearerScheme) || !strings.EqualFold(authHeader[:len(bearerScheme)], bearerScheme) {
		return "", false
	}
	remainder := authHeader[len(bearerScheme):]
	if remainder[0] != ' ' && remainder[0] != '\t' {
		return "", false
	}
	token := strings.TrimSpace(remainder)
	return token, token != ""
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
