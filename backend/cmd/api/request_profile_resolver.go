package main

import (
	"errors"
	"net/http"

	"backend/internal/auth"
)

func openRequestProviderFromRequest[T any](s *APIServer, r *http.Request, input requestConnectionInput) (*openedRequestConnection[T], error) {
	return openRequestProviderWith[T](s.profiles(), r, input)
}

func openRequestProviderWith[T any](profiles *ownedProfileAccess, r *http.Request, input requestConnectionInput) (*openedRequestConnection[T], error) {
	return openRequestProviderFor[T](profiles, r.Context(), auth.GetUserIDFromContext(r.Context()), input)
}

func (s *APIServer) snapshotRequestConnection(r *http.Request, input requestConnectionInput) (requestConnectionSnapshot, error) {
	return s.profiles().SnapshotRequest(r.Context(), auth.GetUserIDFromContext(r.Context()), input)
}

func (s *APIServer) writeRequestSnapshotError(w http.ResponseWriter, r *http.Request, contextLabel string, err error) {
	writeRequestSnapshotErrorWith(w, r, s.logf, contextLabel, err)
}

func writeRequestSnapshotErrorWith(w http.ResponseWriter, r *http.Request, logf func(*http.Request, string, ...any), contextLabel string, err error) {
	if errors.Is(err, errProfileSecret) {
		writeError(w, http.StatusInternalServerError, ErrEncryptionFailed)
		return
	}
	if contextLabel != "" && logf != nil {
		logf(r, "%s: %v", contextLabel, err)
	}
	writeError(w, http.StatusNotFound, ErrProfileNotFound)
}

func (s *APIServer) writeRequestProviderBrowseError(w http.ResponseWriter, r *http.Request, contextLabel string, urlErrorCode APIErrorCode, err error) {
	writeRequestProviderBrowseErrorWith(w, r, s.logf, contextLabel, urlErrorCode, err)
}

func writeRequestProviderBrowseErrorWith(w http.ResponseWriter, r *http.Request, logf func(*http.Request, string, ...any), contextLabel string, urlErrorCode APIErrorCode, err error) {
	if errors.Is(err, errProfileInvalidProvider) || errors.Is(err, errProfileProvider) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": urlErrorCode})
		return
	}
	if logf != nil {
		logf(r, "%s: %v", contextLabel, err)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrProfileNotFound})
}
