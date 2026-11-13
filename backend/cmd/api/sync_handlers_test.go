package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

func TestSyncMkdirPath(t *testing.T) {
	tests := []struct {
		name       string
		parent     string
		folderName string
		want       string
		valid      bool
	}{
		{name: "root", parent: "/", folderName: "Documents", want: "/Documents", valid: true},
		{name: "nested parent", parent: "/Team/Projects", folderName: "2026", want: "/Team/Projects/2026", valid: true},
		{name: "empty parent is root", folderName: "Documents", want: "/Documents", valid: true},
		{name: "normalizes harmless dot segment", parent: "/Team/./Projects", folderName: "2026", want: "/Team/Projects/2026", valid: true},
		{name: "rejects relative parent", parent: "Team", folderName: "Documents"},
		{name: "rejects parent traversal", parent: "/Team/../Private", folderName: "Documents"},
		{name: "rejects separator in name", parent: "/", folderName: "Team/Private"},
		{name: "rejects traversal name", parent: "/", folderName: ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := syncMkdirPath(tt.parent, tt.folderName)
			if valid != tt.valid || got != tt.want {
				t.Fatalf("syncMkdirPath(%q, %q) = (%q, %v), want (%q, %v)", tt.parent, tt.folderName, got, valid, tt.want, tt.valid)
			}
		})
	}
}

type syncTestBrowserProvider struct {
	*legacyFileManagerTestProvider
	closed bool
}

func (p *syncTestBrowserProvider) Close() error {
	p.closed = true
	return nil
}

func TestOpenSyncJobEndpointProfileAndDirectPaths(t *testing.T) {
	const key = "01234567890123456789012345678901"
	profilePasswordEnc, _ := crypto.EncryptWithDomain("profile-secret", key, crypto.ConnectionCredentialDomain(false))
	directPasswordEnc, _ := crypto.EncryptWithDomain("direct-secret", key, crypto.ConnectionCredentialDomain(false))
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://profile.example.test", "profile-user", profilePasswordEnc),
	})

	t.Run("profile-bound endpoint opens owned profile and closes", func(t *testing.T) {
		stub := &syncTestBrowserProvider{legacyFileManagerTestProvider: &legacyFileManagerTestProvider{}}
		var gotProvider, gotURL, gotUser, gotPass string
		server := &APIServer{
			db:            database,
			encryptionKey: key,
			providerFactory: func(_ context.Context, provider, url, username, password string, _ ...storage.ProviderOption) (profileProvider, error) {
				gotProvider, gotURL, gotUser, gotPass = provider, url, username, password
				return stub, nil
			},
		}
		job := &db.SyncJob{
			ID:              "sync-1",
			UserID:          "user-1",
			SourceProfileID: sql.NullString{String: "profile-1", Valid: true},
			SourceProvider:  "webdav",
		}
		endpoint, err := openSyncJobEndpoint[migrationBrowser](server, context.Background(), "sync-1", "user-1", "source", job)
		if err != nil {
			t.Fatalf("openSyncJobEndpoint(profile) error = %v", err)
		}
		if endpoint.Client == nil || endpoint.Provider != "nextcloud" {
			t.Fatalf("client=%v provider=%q, want non-nil nextcloud", endpoint.Client, endpoint.Provider)
		}
		if gotProvider != "nextcloud" || gotURL != "https://profile.example.test" || gotUser != "profile-user" || gotPass != "profile-secret" {
			t.Fatalf("opened args = (%q, %q, %q, %q)", gotProvider, gotURL, gotUser, gotPass)
		}
		endpoint.Close()
		if !stub.closed {
			t.Fatal("cleanup did not close profile provider")
		}
	})

	t.Run("profile-bound endpoint rejects unowned profile", func(t *testing.T) {
		server := &APIServer{db: database, encryptionKey: key}
		job := &db.SyncJob{
			ID:              "sync-2",
			UserID:          "user-1",
			TargetProfileID: sql.NullString{String: "missing-profile", Valid: true},
		}
		_, err := openSyncJobEndpoint[migrationBrowser](server, context.Background(), "sync-2", "user-1", "target", job)
		if !errors.Is(err, errProfileNotOwned) {
			t.Fatalf("error = %v, want errProfileNotOwned", err)
		}
	})

	t.Run("legacy direct endpoint opens without profile", func(t *testing.T) {
		stub := &syncTestBrowserProvider{legacyFileManagerTestProvider: &legacyFileManagerTestProvider{}}
		var gotProvider, gotURL, gotUser, gotPass string
		server := &APIServer{
			db:            database,
			encryptionKey: key,
			providerFactory: func(_ context.Context, provider, url, username, password string, _ ...storage.ProviderOption) (profileProvider, error) {
				gotProvider, gotURL, gotUser, gotPass = provider, url, username, password
				return stub, nil
			},
		}
		job := &db.SyncJob{
			ID:                      "sync-3",
			UserID:                  "user-1",
			SourceProvider:          "webdav",
			SourceURL:               "https://direct.example.test",
			SourceUsername:          "direct-user",
			SourcePasswordEncrypted: directPasswordEnc,
		}
		endpoint, err := openSyncJobEndpoint[migrationBrowser](server, context.Background(), "sync-3", "user-1", "source", job)
		if err != nil {
			t.Fatalf("openSyncJobEndpoint(direct) error = %v", err)
		}
		if endpoint.Client == nil || endpoint.Provider != "webdav" {
			t.Fatalf("client=%v provider=%q, want non-nil webdav", endpoint.Client, endpoint.Provider)
		}
		if gotProvider != "webdav" || gotURL != "https://direct.example.test" || gotUser != "direct-user" || gotPass != "direct-secret" {
			t.Fatalf("opened args = (%q, %q, %q, %q)", gotProvider, gotURL, gotUser, gotPass)
		}
		endpoint.Close()
		if !stub.closed {
			t.Fatal("cleanup did not close direct provider")
		}
	})
}
