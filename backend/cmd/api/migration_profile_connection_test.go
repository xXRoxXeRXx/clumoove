package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/auth"
	"backend/internal/storage"
)

func TestResolveRequestProfileCredentialsKeepsRequestCredentialsWithoutProfile(t *testing.T) {
	server := &APIServer{}
	request := httptest.NewRequest(http.MethodPost, "/api/migration/connect", nil)
	request = request.WithContext(storage.WithLocalUserScope(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}), "user-1"))
	base := profileCreds{Provider: "local", URL: "", Username: "", Password: "request-secret", RefreshToken: "request-refresh"}

	got, err := server.resolveRequestProfileCredentials(request, "", base)
	if err != nil {
		t.Fatalf("resolveRequestProfileCredentials() error = %v", err)
	}
	if got.Provider != base.Provider || got.Password != base.Password || got.RefreshToken != base.RefreshToken {
		t.Fatal("request-supplied credentials were not preserved")
	}
}

func TestHandleConnectTestConnectionResults(t *testing.T) {
	server := &APIServer{
		rateLimiter: allowAllFileRateLimiter{},
		providerFactory: func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
			return &legacyFileManagerTestProvider{}, nil
		},
	}

	t.Run("successful connection", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/migration/connect/test", bytes.NewBufferString(`{"role":"source","source_provider":"nextcloud","source_url":"https://cloud.example.test"}`))
		request = request.WithContext(storage.WithLocalUserScope(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}), "user-1"))
		recorder := httptest.NewRecorder()

		server.handleConnectTest(recorder, request)

		if recorder.Code != http.StatusOK || recorder.Body.String() != "{\"success\":true}\n" {
			t.Fatalf("response = %d %s, want successful connection", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("unsupported provider stays machine readable", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/migration/connect/test", bytes.NewBufferString(`{"role":"target","target_provider":"not-a-provider"}`))
		request = request.WithContext(storage.WithLocalUserScope(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}), "user-1"))
		recorder := httptest.NewRecorder()

		server.handleConnectTest(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
		}
		var response struct {
			Success   bool         `json:"success"`
			ErrorCode APIErrorCode `json:"error_code"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.Success || response.ErrorCode != ErrProviderUnsupported {
			t.Fatalf("response = %#v, want machine-readable unsupported provider", response)
		}
	})

	t.Run("provider creation failure stays machine readable", func(t *testing.T) {
		server.providerFactory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
			return nil, errors.New("provider unavailable")
		}
		defer func() {
			server.providerFactory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
				return &legacyFileManagerTestProvider{}, nil
			}
		}()
		request := httptest.NewRequest(http.MethodPost, "/api/migration/connect/test", bytes.NewBufferString(`{"role":"source","source_provider":"nextcloud","source_url":"https://cloud.example.test"}`))
		request = request.WithContext(storage.WithLocalUserScope(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}), "user-1"))
		recorder := httptest.NewRecorder()

		server.handleConnectTest(recorder, request)

		assertAPIError(t, recorder, http.StatusOK, ErrSourceUrlInvalid)
	})

	t.Run("selected profile is not found", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/migration/connect/test", bytes.NewBufferString(`{"role":"source","source_profile_id":"missing-profile"}`))
		request = request.WithContext(storage.WithLocalUserScope(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}), "user-1"))
		recorder := httptest.NewRecorder()

		server.handleConnectTest(recorder, request)

		assertAPIError(t, recorder, http.StatusNotFound, ErrProfileNotFound)
	})
}

func TestMigrationBrowseAndStartProfileFailures(t *testing.T) {
	server := &APIServer{
		rateLimiter: allowAllFileRateLimiter{},
		providerFactory: func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
			return &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{"/": {{Name: "document", Path: "document"}}}}, nil
		},
	}

	t.Run("browse succeeds through provider factory", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/migration/browse", bytes.NewBufferString(`{"source_provider":"nextcloud","source_url":"https://cloud.example.test","resource_type":"files"}`))
		recorder := httptest.NewRecorder()

		server.handleBrowse(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
		}
		var response struct {
			Success bool                    `json:"success"`
			Items   []storage.CloudResource `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !response.Success || len(response.Items) != 1 || response.Items[0].Path != "document" {
			t.Fatalf("browse response did not preserve provider result")
		}
	})

	t.Run("start rejects an unavailable source profile", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/api/migration/start", bytes.NewBufferString(`{"source_profile_id":"missing-profile","paths":["/"]}`))
		request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
		recorder := httptest.NewRecorder()

		server.handleStart(recorder, request)

		assertAPIError(t, recorder, http.StatusNotFound, ErrProfileNotFound)
	})
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode APIErrorCode) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var response struct {
		ErrorCode APIErrorCode `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ErrorCode != wantCode {
		t.Fatalf("error code = %q, want %q", response.ErrorCode, wantCode)
	}
}
