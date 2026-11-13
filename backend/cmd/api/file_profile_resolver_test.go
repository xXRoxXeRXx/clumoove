package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

func TestMapFileProfileError(t *testing.T) {
	if got := mapFileProfileError(errProfileNotOwned); !errors.Is(got, errFileProfileNotFound) {
		t.Fatalf("ownership error mapped to %v, want errFileProfileNotFound", got)
	}
	if got := mapFileProfileError(errors.New("database unavailable")); !errors.Is(got, errFileProviderUnavailable) {
		t.Fatalf("provider error mapped to %v, want errFileProviderUnavailable", got)
	}
}

func TestFileProfileOAuthAccessNeedsRefreshWhenAccessTokenIsMissing(t *testing.T) {
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	profile := &db.ConnectionProfile{
		Provider:       "google",
		TokenExpiresAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true},
	}
	if !profileOAuthAccessNeedsRefresh(profile, "", now) {
		t.Fatal("missing OAuth access token must be refreshed even before its stored expiry")
	}
	if profileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("present OAuth access token with a future expiry should not refresh")
	}
	profile.TokenExpiresAt = sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	if !profileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("near-expiry OAuth access token must refresh")
	}
}

func TestFileEntriesListUsesOnlyOwnedProfileCredentials(t *testing.T) {
	const key = "01234567890123456789012345678901"
	password, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt saved password: %v", err)
	}
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://saved.example.test", "saved-user", password),
	})
	var gotProvider, gotURL, gotUsername, gotPassword string
	server := &APIServer{
		db:            database,
		encryptionKey: key,
		rateLimiter:   allowAllFileRateLimiter{},
		providerFactory: func(_ context.Context, provider, url, username, password string, _ ...storage.ProviderOption) (profileProvider, error) {
			gotProvider, gotURL, gotUsername, gotPassword = provider, url, username, password
			return &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{"/": {}}}, nil
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files/profiles/profile-1/entries:list", strings.NewReader(`{"resource_type":"files","provider":"sftp","url":"https://request.example.test","username":"request-user","password":"request-secret","refresh_token":"request-refresh"}`))
	request.SetPathValue("profileID", "profile-1")
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleFileEntriesList(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if gotProvider != "nextcloud" || gotURL != "https://saved.example.test" || gotUsername != "saved-user" || gotPassword != "saved-secret" {
		t.Fatalf("provider setup = (%q, %q, %q, %q), want saved profile only", gotProvider, gotURL, gotUsername, gotPassword)
	}
}

func TestResolveFileProfileMapsProviderSetupFailure(t *testing.T) {
	const key = "01234567890123456789012345678901"
	password, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt saved password: %v", err)
	}
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://saved.example.test", "saved-user", password),
	})
	providerSetupCalled := false
	server := &APIServer{
		db:            database,
		encryptionKey: key,
		providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
			providerSetupCalled = true
			return nil, errors.New("provider setup failed")
		},
	}

	_, err = server.resolveFileProfile(context.Background(), "user-1", "profile-1")

	if !errors.Is(err, errFileProviderUnavailable) {
		t.Fatalf("resolveFileProfile() error = %v, want errFileProviderUnavailable", err)
	}
	if !providerSetupCalled {
		t.Fatal("file profile resolution did not use the configured provider setup")
	}
}

func TestFileEntriesListMapsProviderSetupFailure(t *testing.T) {
	const key = "01234567890123456789012345678901"
	password, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt saved password: %v", err)
	}
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://saved.example.test", "saved-user", password),
	})
	providerSetupCalled := false
	server := &APIServer{
		db:            database,
		encryptionKey: key,
		rateLimiter:   allowAllFileRateLimiter{},
		providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
			providerSetupCalled = true
			return nil, errors.New("provider setup failed")
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/files/profiles/profile-1/entries:list", strings.NewReader(`{"resource_type":"files"}`))
	request.SetPathValue("profileID", "profile-1")
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-1"}))
	recorder := httptest.NewRecorder()

	server.handleFileEntriesList(recorder, request)

	assertAPIError(t, recorder, http.StatusBadGateway, ErrFilesProviderUnavailable)
	if !providerSetupCalled {
		t.Fatal("file entries handler did not use the configured provider setup")
	}
}
