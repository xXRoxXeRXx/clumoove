package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

func TestResolveOwnedProfileRequiresProfileAndOwner(t *testing.T) {
	server := &APIServer{}
	for _, tc := range []struct {
		name      string
		userID    string
		profileID string
	}{
		{name: "missing user", profileID: "profile-1"},
		{name: "missing profile", userID: "user-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveOwnedProfile(context.Background(), tc.userID, tc.profileID)
			if !errors.Is(err, errProfileNotFound) {
				t.Fatalf("resolveOwnedProfile() error = %v, want errProfileNotFound", err)
			}
		})
	}
}

func TestProfileCredentialResolverPropagatesOwnershipFailure(t *testing.T) {
	resolver := testProfileCredentialResolver(nil)
	resolver.loadOwned = func(context.Context, string, string) (*db.ConnectionProfile, error) {
		return nil, errProfileNotOwned
	}
	_, _, err := resolver.resolve(context.Background(), "user-1", "profile-1", profileCreds{}, refreshOAuthAlways, failOnProfileSecretError)
	if !errors.Is(err, errProfileNotOwned) {
		t.Fatalf("resolve() error = %v, want errProfileNotOwned", err)
	}
}

func TestProfileCredentialResolverSelectedProfilePrecedence(t *testing.T) {
	profile := &db.ConnectionProfile{
		Provider:              "nextcloud",
		URL:                   "https://profile.example.test",
		Username:              "profile-user",
		PasswordEncrypted:     "profile-password",
		RefreshTokenEncrypted: "profile-refresh",
	}
	resolver := testProfileCredentialResolver(profile)
	base := profileCreds{
		Provider:     "webdav",
		URL:          "https://request.example.test",
		Username:     "request-user",
		Password:     "request-password",
		RefreshToken: "request-refresh",
	}

	creds, _, err := resolver.resolve(context.Background(), "user-1", "profile-1", base, refreshOAuthAlways, fallbackOnProfileSecretError)
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if creds.Provider != profile.Provider || creds.URL != profile.URL || creds.Username != profile.Username || creds.Password != "profile-password" || creds.RefreshToken != "profile-refresh" {
		t.Fatal("selected profile did not retain precedence over request credentials")
	}
}

func TestProfileConnectionCloseClearsSecrets(t *testing.T) {
	masterKey := []byte{1, 2, 3, 4}
	connection := &profileConnection{creds: profileCreds{
		Password:     "password",
		RefreshToken: "refresh-token",
		MegaSession:  storage.MegaSession{ID: "session", MasterKey: masterKey},
	}}

	connection.close()

	if connection.creds.Password != "" || connection.creds.RefreshToken != "" {
		t.Fatal("close must clear plaintext OAuth and password credentials")
	}
	for i, value := range masterKey {
		if value != 0 {
			t.Fatalf("MEGA master key byte %d = %d, want 0", i, value)
		}
	}
}

func TestProfileCredentialResolverRefreshPolicies(t *testing.T) {
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	profile := &db.ConnectionProfile{Provider: "google", PasswordEncrypted: "access", RefreshTokenEncrypted: "refresh", TokenExpiresAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true}}
	for _, tc := range []struct {
		name        string
		policy      profileOAuthRefreshPolicy
		wantRefresh bool
	}{
		{name: "always", policy: refreshOAuthAlways, wantRefresh: true},
		{name: "when expiring", policy: refreshOAuthWhenExpiring, wantRefresh: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refreshCalls := 0
			resolver := testProfileCredentialResolver(profile)
			resolver.now = func() time.Time { return now }
			resolver.refresh = func(context.Context, string, string) (*oauth.TokenResponse, error) {
				refreshCalls++
				return &oauth.TokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 60}, nil
			}
			creds, _, err := resolver.resolve(context.Background(), "user-1", "profile-1", profileCreds{}, tc.policy, failOnProfileSecretError)
			if err != nil {
				t.Fatalf("resolve() error = %v", err)
			}
			if (refreshCalls == 1) != tc.wantRefresh {
				t.Fatalf("refresh calls = %d, wantRefresh = %t", refreshCalls, tc.wantRefresh)
			}
			if tc.wantRefresh && (creds.Password != "fresh-access" || creds.RefreshToken != "fresh-refresh") {
				t.Fatalf("refreshed credentials = %#v", creds)
			}
		})
	}
}

func TestProfileCredentialResolverRefreshAndPersistFailures(t *testing.T) {
	profile := &db.ConnectionProfile{Provider: "google", RefreshTokenEncrypted: "refresh"}
	for _, tc := range []struct {
		name      string
		configure func(*profileCredentialResolver)
	}{
		{name: "refresh", configure: func(r *profileCredentialResolver) {
			r.refresh = func(context.Context, string, string) (*oauth.TokenResponse, error) {
				return nil, errors.New("refresh failed")
			}
		}},
		{name: "persist", configure: func(r *profileCredentialResolver) {
			r.persist = func(*db.ConnectionProfile, *oauth.TokenResponse) error { return errors.New("persist failed") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := testProfileCredentialResolver(profile)
			tc.configure(&resolver)
			_, _, err := resolver.resolve(context.Background(), "user-1", "profile-1", profileCreds{}, refreshOAuthAlways, failOnProfileSecretError)
			if err == nil {
				t.Fatal("resolve() error = nil, want failure")
			}
		})
	}
}

func TestProfileCredentialResolverSecretFailures(t *testing.T) {
	profile := &db.ConnectionProfile{Provider: "mega", PasswordEncrypted: "password", RefreshTokenEncrypted: "refresh", MegaSessionIDEncrypted: "session", MegaMasterKeyEncrypted: "key"}
	baseKey := []byte{1, 2, 3}
	base := profileCreds{Password: "fallback-password", RefreshToken: "fallback-refresh", MegaSession: storage.MegaSession{MasterKey: baseKey}}
	resolver := testProfileCredentialResolver(profile)
	resolver.decrypt = func(string, string) ([]byte, error) { return nil, errors.New("decrypt failed") }
	resolver.decodeMega = func(string, string) (storage.MegaSession, error) {
		return storage.MegaSession{}, errors.New("MEGA decode failed")
	}
	creds, _, err := resolver.resolve(context.Background(), "user-1", "profile-1", base, refreshOAuthAlways, fallbackOnProfileSecretError)
	if err == nil || creds.Password != "fallback-password" {
		t.Fatal("MEGA decode failure must stop credential resolution")
	}
	for i, value := range baseKey {
		if value == 0 {
			t.Fatalf("base MEGA master key byte %d was cleared through an alias", i)
		}
	}

	profile = &db.ConnectionProfile{Provider: "nextcloud", PasswordEncrypted: "password"}
	resolver = testProfileCredentialResolver(profile)
	resolver.decrypt = func(string, string) ([]byte, error) { return nil, errors.New("decrypt failed") }
	creds, _, err = resolver.resolve(context.Background(), "user-1", "profile-1", profileCreds{Password: "fallback"}, refreshOAuthAlways, fallbackOnProfileSecretError)
	if err != nil || creds.Password != "fallback" {
		t.Fatalf("fallback secret behavior = (%#v, %v)", creds, err)
	}
	_, _, err = resolver.resolve(context.Background(), "user-1", "profile-1", profileCreds{}, refreshOAuthAlways, failOnProfileSecretError)
	if err == nil {
		t.Fatal("strict secret behavior must return decrypt failure")
	}
}

func TestNewProfileConnectionCreatesProviderFromResolvedCredentials(t *testing.T) {
	creds := profileCreds{Provider: "nextcloud", URL: "https://cloud.example.test", Username: "alice", Password: "secret"}
	var gotProvider, gotURL, gotUsername, gotPassword string
	connection, err := newProfileConnection(context.Background(), "user-1", &db.ConnectionProfile{ID: "profile-1"}, creds,
		func(_ context.Context, provider, url, username, password string) (storage.StorageProvider, error) {
			gotProvider, gotURL, gotUsername, gotPassword = provider, url, username, password
			return nil, nil
		})
	if err != nil {
		t.Fatalf("newProfileConnection() error = %v", err)
	}
	if connection.profile.ID != "profile-1" {
		t.Fatalf("connection profile = %q, want profile-1", connection.profile.ID)
	}
	if gotProvider != "nextcloud" || gotURL != "https://cloud.example.test" || gotUsername != "alice" || gotPassword != "secret" {
		t.Fatalf("provider creation args = (%q, %q, %q, %q)", gotProvider, gotURL, gotUsername, gotPassword)
	}
}

func TestNewProfileConnectionClearsCopiedSecretsWhenFactoryFails(t *testing.T) {
	masterKey := []byte{1, 2, 3}
	creds := profileCreds{Provider: "mega", Password: "secret", RefreshToken: "refresh", MegaSession: storage.MegaSession{MasterKey: masterKey}}
	_, err := newProfileConnection(context.Background(), "user-1", &db.ConnectionProfile{}, creds, func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		return nil, errors.New("factory failed")
	})
	if err == nil {
		t.Fatal("newProfileConnection() error = nil, want factory failure")
	}
	for i, value := range masterKey {
		if value == 0 {
			t.Fatalf("caller MEGA master key byte %d was cleared through an alias", i)
		}
	}
}

func TestProfileConnectionScopesMegaSessionToProviderContext(t *testing.T) {
	masterKey := []byte{1, 2, 3}
	creds := profileCreds{Provider: "mega", MegaSession: storage.MegaSession{ID: "session", MasterKey: masterKey}}

	ctx := withMegaProfileSession(context.Background(), creds)
	session, ok := storage.MegaSessionFromContext(ctx)
	if !ok || session.ID != "session" {
		t.Fatalf("MEGA session = %#v, %t; want session in provider context", session, ok)
	}
	if string(session.MasterKey) != string(masterKey) {
		t.Fatal("provider context must retain MEGA master-key material")
	}
	clear(session.MasterKey)
	clear(masterKey)
}

func TestProfileOAuthAccessNeedsRefreshOnlyForOAuthProfiles(t *testing.T) {
	profile := &db.ConnectionProfile{Provider: "nextcloud"}
	if profileOAuthAccessNeedsRefresh(profile, "", time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)) {
		t.Fatal("non-OAuth profiles must not enter the OAuth credential lifecycle")
	}
}

func testProfileCredentialResolver(profile *db.ConnectionProfile) profileCredentialResolver {
	return profileCredentialResolver{
		loadOwned: func(context.Context, string, string) (*db.ConnectionProfile, error) { return profile, nil },
		decrypt:   func(ciphertext, _ string) ([]byte, error) { return []byte(ciphertext), nil },
		decodeMega: func(string, string) (storage.MegaSession, error) {
			return storage.MegaSession{ID: "session", MasterKey: []byte{9, 8, 7}}, nil
		},
		refresh: func(context.Context, string, string) (*oauth.TokenResponse, error) {
			return &oauth.TokenResponse{AccessToken: "access", RefreshToken: "refresh"}, nil
		},
		persist: func(*db.ConnectionProfile, *oauth.TokenResponse) error { return nil },
		now:     time.Now,
	}
}
