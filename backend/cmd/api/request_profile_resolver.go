package main

import (
	"errors"
	"net/http"

	"backend/internal/auth"
)

func openRequestProviderFromRequest[T any](s *APIServer, r *http.Request, input requestConnectionInput) (*openedRequestConnection[T], error) {
	return openRequestProviderFor[T](s.profiles(), r.Context(), auth.GetUserIDFromContext(r.Context()), input)
}

func (s *APIServer) snapshotRequestConnection(r *http.Request, input requestConnectionInput) (requestConnectionSnapshot, error) {
	return s.profiles().SnapshotRequest(r.Context(), auth.GetUserIDFromContext(r.Context()), input)
}

func (s *APIServer) writeRequestSnapshotError(w http.ResponseWriter, r *http.Request, contextLabel string, err error) {
	if errors.Is(err, errProfileSecret) {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	if contextLabel != "" {
		s.logf(r, "%s: %v", contextLabel, err)
	}
	writeError(w, http.StatusNotFound, ErrProfileNotFound)
}

func (s *APIServer) writeRequestProviderBrowseError(w http.ResponseWriter, r *http.Request, contextLabel string, urlErrorCode APIErrorCode, err error) {
	if errors.Is(err, errProfileInvalidProvider) || errors.Is(err, errProfileProvider) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": urlErrorCode})
		return
	}
	s.logf(r, "%s: %v", contextLabel, err)
	writeError(w, http.StatusNotFound, ErrProfileNotFound)
}
