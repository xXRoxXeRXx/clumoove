package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

// AuthMiddleware intercepts requests to validate the JWT bearer token and inject userID into context
func AuthMiddleware(secretKey string) func(http.Handler) http.Handler {
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
			if err := RequireAuthenticated(claims); err != nil {
				writeUnauthorized(w)
				return
			}

			// Inject full Claims into request context
			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
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
