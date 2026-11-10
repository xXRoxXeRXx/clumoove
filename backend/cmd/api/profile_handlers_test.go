package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/storage"
)

func TestTestProfileMapsProviderSetupFailureToSourceURLInvalid(t *testing.T) {
	const key = "01234567890123456789012345678901"
	password, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt saved password: %v", err)
	}
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://saved.example.test", "saved-user", password),
	})
	server := &APIServer{
		db:            database,
		encryptionKey: key,
		rateLimiter:   allowAllFileRateLimiter{},
		providerFactory: func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
			return nil, errors.New("provider setup failed")
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/profiles/profile-1/test", nil)
	request.SetPathValue("id", "profile-1")
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleTestProfile(recorder, request)

	var response struct {
		Success   bool         `json:"success"`
		ErrorCode APIErrorCode `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recorder.Code != http.StatusOK || response.Success || response.ErrorCode != ErrSourceUrlInvalid {
		t.Fatalf("response = (%d, %t, %q), want (200, false, %q)", recorder.Code, response.Success, response.ErrorCode, ErrSourceUrlInvalid)
	}
}

func TestTestProfileMapsOwnershipCheckFailureToInternalError(t *testing.T) {
	database, state := newBackupProfileConnectionTestDB(t, nil)
	state.ownershipErr = errors.New("database unavailable")
	server := &APIServer{db: database, rateLimiter: allowAllFileRateLimiter{}}
	request := httptest.NewRequest(http.MethodPost, "/api/profiles/profile-1/test", nil)
	request.SetPathValue("id", "profile-1")
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleTestProfile(recorder, request)

	assertAPIError(t, recorder, http.StatusInternalServerError, ErrInternalError)
}
