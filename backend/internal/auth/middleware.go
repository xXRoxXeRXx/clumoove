package auth

import (
	"context"
	"net/http"
	"strings"
)

type ContextKey string

const ClaimsKey ContextKey = "claims"

// AuthMiddleware intercepts requests to validate the JWT bearer token and inject userID into context
func AuthMiddleware(secretKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Unauthorized: Authorization header missing", http.StatusUnauthorized)
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				http.Error(w, "Unauthorized: Invalid Authorization format", http.StatusUnauthorized)
				return
			}

			tokenStr := parts[1]
			claims, err := ValidateToken(tokenStr, secretKey)
			if err != nil {
				http.Error(w, "Unauthorized: Invalid or expired token", http.StatusUnauthorized)
				return
			}

			// Reject 2FA temp tokens: they authenticate the password step only and
			// must never grant access to protected routes before the second factor.
			if err := RequireAuthenticated(claims); err != nil {
				http.Error(w, "Unauthorized: second factor required", http.StatusUnauthorized)
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
