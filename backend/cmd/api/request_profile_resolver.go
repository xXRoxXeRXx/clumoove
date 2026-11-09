package main

import (
	"errors"
	"net/http"

	"backend/internal/auth"
)

// resolveRequestProfileCredentials is the HTTP adapter for the
// shared profile connection module. It deliberately leaves request-supplied
// credentials intact when no profile is selected, and delegates all selected
// profile ownership, decryption, OAuth refresh, and fallback behavior to the
// shared resolver.
func (s *APIServer) resolveRequestProfileCredentials(r *http.Request, profileID string, base profileCreds) (profileCreds, error) {
	if profileID == "" {
		return base, nil
	}

	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		return base, errors.New("missing authenticated user")
	}
	creds, _, err := s.resolveProfileCredentials(r.Context(), userID, profileID, base, refreshOAuthAlways, fallbackOnProfileSecretError)
	return creds, err
}
