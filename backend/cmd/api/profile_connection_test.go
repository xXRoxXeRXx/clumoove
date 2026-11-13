package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

func TestLoadOwnedProfileRequiresProfileAndOwner(t *testing.T) {
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
			_, err := server.profiles().LoadOwned(context.Background(), tc.userID, tc.profileID)
			if !errors.Is(err, errProfileNotFound) {
				t.Fatalf("LoadOwned() error = %v, want errProfileNotFound", err)
			}
		})
	}
}

func TestOwnedProfileAccessPropagatesOwnershipAndRejectsInvalidProvider(t *testing.T) {
	const key = "01234567890123456789012345678901"
	badProviderRow := profileTestRow("bad-prov", "user-1", "not-a-provider", "", "alice", "")
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"bad-prov": badProviderRow,
	})
	factoryCalled := false
	access := &ownedProfileAccess{
		db:            database,
		encryptionKey: key,
		providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
			factoryCalled = true
			return nil, nil
		},
	}

	if _, err := access.OpenOwned(context.Background(), "user-1", "other-users-profile"); !errors.Is(err, errProfileNotOwned) {
		t.Fatalf("OpenOwned(unowned) error = %v, want errProfileNotOwned", err)
	}
	if _, err := access.OpenOwned(context.Background(), "user-1", "bad-prov"); !errors.Is(err, errProfileInvalidProvider) {
		t.Fatalf("OpenOwned(invalid provider) error = %v, want errProfileInvalidProvider", err)
	}
	if factoryCalled {
		t.Fatal("providerFactory must not be called for invalid provider")
	}
}

func TestOwnedProfileConnectionCloseClearsSecrets(t *testing.T) {
	masterKey := []byte{1, 2, 3, 4}
	connection := &ownedProfileConnection{creds: profileCreds{
		Password:     "password",
		RefreshToken: "refresh-token",
		MegaSession:  storage.MegaSession{ID: "session", MasterKey: masterKey},
	}}

	connection.Close()

	if connection.creds.Password != "" || connection.creds.RefreshToken != "" {
		t.Fatal("Close must clear plaintext OAuth and password credentials")
	}
	for i, value := range masterKey {
		if value != 0 {
			t.Fatalf("MEGA master key byte %d = %d, want 0", i, value)
		}
	}
}

func TestOwnedProfileAccessRefreshAndPersistFailures(t *testing.T) {
	const key = "01234567890123456789012345678901"
	refreshEnc, _ := crypto.EncryptWithDomain("refresh-secret", key, crypto.DomainOAuthRefreshToken)

	withRefreshRow := profileTestRow("with-refresh", "user-1", "google", "", "user@example.test", "")
	withRefreshRow[7] = refreshEnc

	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"with-refresh": withRefreshRow,
	})

	t.Run("refresh failure aborts TestOwned", func(t *testing.T) {
		access := &ownedProfileAccess{
			db:            database,
			encryptionKey: key,
			refreshOAuth: func(context.Context, string, string) (*oauth.TokenResponse, error) {
				return nil, errors.New("refresh failed")
			},
		}
		if _, err := access.TestOwned(context.Background(), "user-1", "with-refresh"); err == nil {
			t.Fatal("TestOwned() error = nil, want refresh failure")
		}
	})

	t.Run("persist failure aborts profile refresh", func(t *testing.T) {
		spyDB, spyState := newProfileSpyDB(t, map[string][]driver.Value{
			"with-refresh": withRefreshRow,
		})
		spyState.execErr = errors.New("persist failed")
		access := &ownedProfileAccess{
			db:            spyDB,
			encryptionKey: key,
			refreshOAuth: func(context.Context, string, string) (*oauth.TokenResponse, error) {
				return &oauth.TokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
			},
		}
		if _, err := access.TestOwned(context.Background(), "user-1", "with-refresh"); err == nil {
			t.Fatal("TestOwned() error = nil, want persist failure")
		}
	})
}

func TestOwnedProfileAccessSuppliedOAuthDoesNotMutateProfileOrUseStaleExpiry(t *testing.T) {
	const key = "01234567890123456789012345678901"
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	savedAccessEnc, _ := crypto.EncryptWithDomain("saved-access", key, crypto.DomainOAuthAccessToken)
	savedRefreshEnc, _ := crypto.EncryptWithDomain("saved-refresh", key, crypto.DomainOAuthRefreshToken)
	row := profileTestRow("profile-oauth", "user-1", "google", "", "saved@example.test", savedAccessEnc)
	row[7] = savedRefreshEnc
	row[8] = now.Add(10 * time.Hour)

	t.Run("supplied refresh token refreshes ephemerally without mutating profile and preserves rotated token and expiry", func(t *testing.T) {
		spyDB, spyState := newProfileSpyDB(t, map[string][]driver.Value{"profile-oauth": row})
		var refreshedWith string
		access := &ownedProfileAccess{
			db:            spyDB,
			encryptionKey: key,
			now:           func() time.Time { return now },
			refreshOAuth: func(_ context.Context, _, refreshToken string) (*oauth.TokenResponse, error) {
				refreshedWith = refreshToken
				return &oauth.TokenResponse{AccessToken: "rotated-access", RefreshToken: "rotated-refresh", ExpiresIn: 900}, nil
			},
		}

		snap, err := access.SnapshotRequest(context.Background(), "user-1", requestConnectionInput{
			ProfileID:    "profile-oauth",
			RefreshToken: "supplied-refresh",
		})
		if err != nil {
			t.Fatalf("SnapshotRequest() error = %v", err)
		}
		if refreshedWith != "supplied-refresh" {
			t.Fatalf("refreshOAuth called with %q, want supplied-refresh", refreshedWith)
		}
		if spyState.execCalls != 0 {
			t.Fatalf("DB Exec calls = %d, want 0 (supplied refresh token must not overwrite saved profile)", spyState.execCalls)
		}
		if dec, _ := crypto.DecryptWithDomain(snap.Creds.PasswordEncrypted, key, crypto.DomainOAuthAccessToken); dec != "rotated-access" {
			t.Fatalf("snapshot access = %q, want rotated-access", dec)
		}
		if dec, _ := crypto.DecryptWithDomain(snap.Creds.RefreshTokenEncrypted.String, key, crypto.DomainOAuthRefreshToken); dec != "rotated-refresh" {
			t.Fatalf("snapshot refresh = %q, want rotated-refresh", dec)
		}
		wantExpiry := now.Add(900 * time.Second)
		if !snap.TokenExpiresAt.Valid || !snap.TokenExpiresAt.Time.Equal(wantExpiry) {
			t.Fatalf("snapshot TokenExpiresAt = %+v, want %v (must not use saved profile expiry)", snap.TokenExpiresAt, wantExpiry)
		}
	})

	t.Run("supplied access and refresh tokens ignore saved profile expiry", func(t *testing.T) {
		spyDB, spyState := newProfileSpyDB(t, map[string][]driver.Value{"profile-oauth": row})
		refreshCalls := 0
		access := &ownedProfileAccess{
			db:            spyDB,
			encryptionKey: key,
			now:           func() time.Time { return now },
			refreshOAuth: func(context.Context, string, string) (*oauth.TokenResponse, error) {
				refreshCalls++
				return nil, nil
			},
		}

		snap, err := access.SnapshotRequest(context.Background(), "user-1", requestConnectionInput{
			ProfileID:      "profile-oauth",
			Password:       "supplied-access",
			RefreshToken:   "supplied-refresh",
			TokenExpiresIn: 1800,
		})
		if err != nil {
			t.Fatalf("SnapshotRequest() error = %v", err)
		}
		if refreshCalls != 0 || spyState.execCalls != 0 {
			t.Fatalf("refreshCalls=%d execCalls=%d, want both 0", refreshCalls, spyState.execCalls)
		}
		wantExpiry := now.Add(1800 * time.Second)
		if !snap.TokenExpiresAt.Valid || !snap.TokenExpiresAt.Time.Equal(wantExpiry) {
			t.Fatalf("snapshot TokenExpiresAt = %+v, want %v (must not use saved profile expiry)", snap.TokenExpiresAt, wantExpiry)
		}
	})
}

type profileSpyState struct {
	profiles  map[string][]driver.Value
	execCalls int
	execErr   error
}

type profileSpyConnector struct{ state *profileSpyState }

func (c profileSpyConnector) Connect(context.Context) (driver.Conn, error) {
	return profileSpyConn{state: c.state}, nil
}
func (c profileSpyConnector) Driver() driver.Driver { return nil }

type profileSpyConn struct{ state *profileSpyState }

func (profileSpyConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (profileSpyConn) Close() error                        { return nil }
func (profileSpyConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (c profileSpyConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.state.execCalls++
	if c.state.execErr != nil {
		return nil, c.state.execErr
	}
	return driver.RowsAffected(1), nil
}
func (c profileSpyConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "connection_profiles") {
		_, ok := c.state.profiles[args[0].Value.(string)]
		return &profileSpyRows{rows: [][]driver.Value{{ok}}, columns: 1}, nil
	}
	if strings.Contains(query, "FROM connection_profiles WHERE id") {
		return &profileSpyRows{rows: [][]driver.Value{c.state.profiles[args[0].Value.(string)]}, columns: 14}, nil
	}
	return nil, driver.ErrSkip
}

type profileSpyRows struct {
	rows    [][]driver.Value
	pos     int
	columns int
}

func (r *profileSpyRows) Columns() []string { return make([]string, r.columns) }
func (r *profileSpyRows) Close() error      { return nil }
func (r *profileSpyRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

func newProfileSpyDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *profileSpyState) {
	t.Helper()
	state := &profileSpyState{profiles: profiles}
	database := sql.OpenDB(profileSpyConnector{state: state})
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

func TestOwnedProfileAccessSecretFailurePolicies(t *testing.T) {
	const key = "01234567890123456789012345678901"

	megaBadRow := profileTestRow("mega-bad", "user-1", "mega", "", "user@example.test", "bad-cipher")
	megaBadRow[9] = "bad-session"
	megaBadRow[10] = "bad-key"

	nextcloudBadRow := profileTestRow("nc-bad", "user-1", "nextcloud", "https://cloud.example.test", "alice", "bad-cipher")

	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"mega-bad": megaBadRow,
		"nc-bad":   nextcloudBadRow,
	})

	var gotPassword string
	access := &ownedProfileAccess{
		db:            database,
		encryptionKey: key,
		providerFactory: func(_ context.Context, _, _, _, password string, _ ...storage.ProviderOption) (profileProvider, error) {
			gotPassword = password
			return &connectionOnlyProvider{}, nil
		},
	}

	if _, err := access.OpenOwned(context.Background(), "user-1", "nc-bad"); !errors.Is(err, errProfileSecret) {
		t.Fatalf("OpenOwned(corrupt password) error = %v, want errProfileSecret", err)
	}
	if _, err := access.SnapshotOwned(context.Background(), "user-1", "nc-bad"); !errors.Is(err, errProfileSecret) {
		t.Fatalf("SnapshotOwned(corrupt password) error = %v, want errProfileSecret", err)
	}
	if _, err := openRequestProviderFor[profileProvider](access, context.Background(), "user-1", requestConnectionInput{
		ProfileID: "mega-bad",
		Password:  "fallback-password",
	}); !errors.Is(err, errProfileSecret) {
		t.Fatalf("openRequestProviderFor(corrupt mega) error = %v, want errProfileSecret", err)
	}

	conn, err := openRequestProviderFor[profileProvider](access, context.Background(), "user-1", requestConnectionInput{
		ProfileID: "nc-bad",
		Password:  "fallback",
	})
	if err != nil {
		t.Fatalf("openRequestProviderFor(fallback password) error = %v", err)
	}
	conn.Close()
	if gotPassword != "fallback" {
		t.Fatalf("fallback password = %q, want fallback", gotPassword)
	}
}

func TestOwnedProfileAccessCreatesProviderFromCredentials(t *testing.T) {
	var gotProvider, gotURL, gotUsername, gotPassword string
	access := &ownedProfileAccess{
		providerFactory: func(_ context.Context, provider, url, username, password string, _ ...storage.ProviderOption) (profileProvider, error) {
			gotProvider, gotURL, gotUsername, gotPassword = provider, url, username, password
			return nil, nil
		},
	}
	provider, err := access.openDirectProvider(context.Background(), "user-1", "nextcloud", "https://cloud.example.test", "alice", "secret", storage.MegaSession{})
	if err != nil {
		t.Fatalf("openDirectProvider() error = %v", err)
	}
	if provider != nil {
		_ = provider.Close()
	}
	if gotProvider != "nextcloud" || gotURL != "https://cloud.example.test" || gotUsername != "alice" || gotPassword != "secret" {
		t.Fatalf("provider creation args = (%q, %q, %q, %q)", gotProvider, gotURL, gotUsername, gotPassword)
	}
}

func TestOwnedProfileAccessClearsCopiedSecretsWhenFactoryFails(t *testing.T) {
	masterKey := []byte{1, 2, 3}
	access := &ownedProfileAccess{
		providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
			return nil, errors.New("factory failed")
		},
	}
	_, err := access.openDirectProvider(context.Background(), "user-1", "mega", "", "user@example.test", "secret", storage.MegaSession{MasterKey: masterKey})
	if err == nil {
		t.Fatal("openDirectProvider() error = nil, want factory failure")
	}
	for i, value := range masterKey {
		if value == 0 {
			t.Fatalf("caller MEGA master key byte %d was cleared through an alias", i)
		}
	}
}

func TestOwnedProfileAccessForwardsMegaSessionOption(t *testing.T) {
	masterKey := []byte{1, 2, 3}
	access := &ownedProfileAccess{
		providerFactory: func(ctx context.Context, provider, endpoint, username, password string, opts ...storage.ProviderOption) (profileProvider, error) {
			return storage.NewProvider(ctx, provider, endpoint, username, password, opts...)
		},
	}

	provider, err := access.openDirectProvider(context.Background(), "user-1", "mega", "", "user@example.test", "secret", storage.MegaSession{ID: "session", MasterKey: masterKey})
	if err != nil {
		t.Fatalf("openDirectProvider() error = %v", err)
	}
	defer func() { _ = provider.Close() }()
	megaProvider, ok := provider.(*storage.MegaProvider)
	if !ok {
		t.Fatalf("provider = %T, want *storage.MegaProvider", provider)
	}
	session := megaProvider.Session()
	if session.ID != "session" {
		t.Fatalf("MEGA session ID = %q, want session", session.ID)
	}
	if string(session.MasterKey) != string(masterKey) {
		t.Fatal("provider must retain MEGA master-key material passed via ProviderOption")
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

func TestOwnedProfileAccessLoadAndVerifyOwned(t *testing.T) {
	database, state := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://cloud.example.test", "alice", "enc"),
	})
	access := &ownedProfileAccess{db: database}

	if _, err := access.LoadOwned(context.Background(), "", "profile-1"); !errors.Is(err, errProfileNotFound) {
		t.Fatalf("LoadOwned(missing user) error = %v, want errProfileNotFound", err)
	}
	if _, err := access.LoadOwned(context.Background(), "user-1", ""); !errors.Is(err, errProfileNotFound) {
		t.Fatalf("LoadOwned(missing profile) error = %v, want errProfileNotFound", err)
	}
	if _, err := access.LoadOwned(context.Background(), "user-1", "missing"); !errors.Is(err, errProfileNotOwned) {
		t.Fatalf("LoadOwned(unowned) error = %v, want errProfileNotOwned", err)
	}
	profile, err := access.LoadOwned(context.Background(), "user-1", "profile-1")
	if err != nil || profile.ID != "profile-1" {
		t.Fatalf("LoadOwned(owned) = (%v, %v), want profile-1", profile, err)
	}
	if err := access.VerifyOwned(context.Background(), "user-1", "profile-1"); err != nil {
		t.Fatalf("VerifyOwned(owned) error = %v", err)
	}

	state.ownershipErr = errors.New("db down")
	if err := access.VerifyOwned(context.Background(), "user-1", "profile-1"); !errors.Is(err, errProfileOwnership) {
		t.Fatalf("VerifyOwned(db error) error = %v, want errProfileOwnership", err)
	}
}

func TestOwnedProfileAccessEncryptProfileSecrets(t *testing.T) {
	const key = "01234567890123456789012345678901"
	access := &ownedProfileAccess{encryptionKey: key}

	passEnc, refreshEnc, err := access.EncryptProfileSecrets("google", "access-secret", "refresh-secret")
	if err != nil {
		t.Fatalf("EncryptProfileSecrets() error = %v", err)
	}
	gotAccess, err := crypto.DecryptWithDomain(passEnc, key, crypto.DomainOAuthAccessToken)
	if err != nil || gotAccess != "access-secret" {
		t.Fatalf("decrypted access = (%q, %v), want access-secret", gotAccess, err)
	}
	gotRefresh, err := crypto.DecryptWithDomain(refreshEnc, key, crypto.DomainOAuthRefreshToken)
	if err != nil || gotRefresh != "refresh-secret" {
		t.Fatalf("decrypted refresh = (%q, %v), want refresh-secret", gotRefresh, err)
	}

	basicPassEnc, emptyRefreshEnc, err := access.EncryptProfileSecrets("nextcloud", "basic-secret", "")
	if err != nil {
		t.Fatalf("EncryptProfileSecrets(nextcloud) error = %v", err)
	}
	gotBasic, err := crypto.DecryptWithDomain(basicPassEnc, key, crypto.ConnectionCredentialDomain(false))
	if err != nil || gotBasic != "basic-secret" || emptyRefreshEnc != "" {
		t.Fatalf("decrypted basic = (%q, %v, %q), want basic-secret and empty refresh", gotBasic, err, emptyRefreshEnc)
	}
}

func TestOwnedProfileAccessTestOwnedAlwaysRefreshesOAuthAndCloses(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	const key = "01234567890123456789012345678901"
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	accessEnc, _ := crypto.EncryptWithDomain("old-access", key, crypto.DomainOAuthAccessToken)
	refreshEnc, _ := crypto.EncryptWithDomain("old-refresh", key, crypto.DomainOAuthRefreshToken)
	row := profileTestRow("profile-oauth", "user-1", "google", "", "user@example.test", accessEnc)
	row[7] = refreshEnc
	row[8] = now.Add(time.Hour)
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{"profile-oauth": row})

	stub := &connectionOnlyProvider{}
	var gotPassword string
	var gotScoped bool
	refreshCalls := 0
	access := &ownedProfileAccess{
		db:            database,
		encryptionKey: key,
		now:           func() time.Time { return now },
		refreshOAuth: func(context.Context, string, string) (*oauth.TokenResponse, error) {
			refreshCalls++
			return &oauth.TokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
		},
		providerFactory: func(ctx context.Context, _, _, _, password string, _ ...storage.ProviderOption) (profileProvider, error) {
			gotPassword = password
			_, localErr := storage.NewProvider(ctx, "local", "", "", "")
			gotScoped = localErr == nil || !strings.Contains(localErr.Error(), "authenticated user scope")
			return stub, nil
		},
	}

	provider, err := access.TestOwned(context.Background(), "user-1", "profile-oauth")
	if err != nil {
		t.Fatalf("TestOwned() error = %v", err)
	}
	if provider != "google" || refreshCalls != 1 || gotPassword != "fresh-access" || !gotScoped || !stub.closed {
		t.Fatalf("TestOwned() = (provider=%q, refresh=%d, pass=%q, scoped=%t, closed=%t)", provider, refreshCalls, gotPassword, gotScoped, stub.closed)
	}
}

func TestOwnedProfileAccessOpenOwnedRefreshesOnlyWhenExpiringAndSnapshotOwned(t *testing.T) {
	const key = "01234567890123456789012345678901"
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	accessEnc, _ := crypto.EncryptWithDomain("valid-access", key, crypto.DomainOAuthAccessToken)
	refreshEnc, _ := crypto.EncryptWithDomain("valid-refresh", key, crypto.DomainOAuthRefreshToken)

	freshRow := profileTestRow("fresh", "user-1", "google", "", "user@example.test", accessEnc)
	freshRow[7] = refreshEnc
	freshRow[8] = now.Add(time.Hour)

	expiringRow := profileTestRow("expiring", "user-1", "google", "", "user@example.test", accessEnc)
	expiringRow[7] = refreshEnc
	expiringRow[8] = now.Add(30 * time.Second)

	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"fresh":    freshRow,
		"expiring": expiringRow,
	})

	refreshCalls := 0
	access := &ownedProfileAccess{
		db:            database,
		encryptionKey: key,
		now:           func() time.Time { return now },
		refreshOAuth: func(context.Context, string, string) (*oauth.TokenResponse, error) {
			refreshCalls++
			return &oauth.TokenResponse{AccessToken: "renewed-access", RefreshToken: "renewed-refresh", ExpiresIn: 3600}, nil
		},
		providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
			return &connectionOnlyProvider{}, nil
		},
	}

	conn, err := access.OpenOwned(context.Background(), "user-1", "fresh")
	if err != nil {
		t.Fatalf("OpenOwned(fresh) error = %v", err)
	}
	if refreshCalls != 0 {
		t.Fatalf("OpenOwned(fresh) refreshCalls = %d, want 0", refreshCalls)
	}
	err = conn.SyncJobOAuthTokens("sync-1", "source", refreshEnc)
	conn.Close()
	if err != nil {
		t.Fatalf("SyncJobOAuthTokens() error = %v", err)
	}

	snap, err := access.SnapshotOwned(context.Background(), "user-1", "expiring")
	if err != nil {
		t.Fatalf("SnapshotOwned(expiring) error = %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("SnapshotOwned(expiring) refreshCalls = %d, want 1", refreshCalls)
	}
	if snap.Profile.ID != "expiring" {
		t.Fatalf("snapshot profile ID = %q, want expiring", snap.Profile.ID)
	}
	if dec, _ := crypto.DecryptWithDomain(snap.Creds.PasswordEncrypted, key, crypto.DomainOAuthAccessToken); dec != "renewed-access" {
		t.Fatalf("snapshot access = %q, want renewed-access", dec)
	}
	if dec, _ := crypto.DecryptWithDomain(snap.Creds.RefreshTokenEncrypted.String, key, crypto.DomainOAuthRefreshToken); dec != "renewed-refresh" {
		t.Fatalf("snapshot refresh = %q, want renewed-refresh", dec)
	}
}

func TestOwnedProfileAccessRequestProviderAndSnapshot(t *testing.T) {
	const key = "01234567890123456789012345678901"
	now := time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)
	passwordEnc, _ := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	database, _ := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "magentacloud", "https://ignored.example.test", "saved-user", passwordEnc),
	})

	stub := &legacyFileManagerTestProvider{}
	var gotProvider, gotURL, gotUsername, gotPassword string
	access := &ownedProfileAccess{
		db:            database,
		encryptionKey: key,
		now:           func() time.Time { return now },
		providerFactory: func(_ context.Context, provider, url, username, password string, _ ...storage.ProviderOption) (profileProvider, error) {
			gotProvider, gotURL, gotUsername, gotPassword = provider, url, username, password
			return stub, nil
		},
	}

	// 1. Selected profile overrides inline endpoint metadata, normalizes URL, and falls back to saved secret when request password is empty.
	conn, err := openRequestProviderFor[migrationBrowser](access, context.Background(), "user-1", requestConnectionInput{
		ProfileID:    "profile-1",
		NormalizeURL: true,
		Provider:     "nextcloud",
		URL:          "https://request.example.test",
		Username:     "request-user",
	})
	if err != nil {
		t.Fatalf("openRequestProviderFor(profile) error = %v", err)
	}
	conn.Close()
	if gotProvider != "magentacloud" || gotURL != "https://magentacloud.de/remote.php/webdav" || gotUsername != "saved-user" || gotPassword != "saved-secret" {
		t.Fatalf("opened args = (%q, %q, %q, %q)", gotProvider, gotURL, gotUsername, gotPassword)
	}

	// 1b. Non-empty request secrets take precedence over saved profile secrets for both opening and snapshotting.
	connOverride, err := openRequestProviderFor[migrationBrowser](access, context.Background(), "user-1", requestConnectionInput{
		ProfileID:    "profile-1",
		NormalizeURL: true,
		Password:     "request-override",
	})
	if err != nil {
		t.Fatalf("openRequestProviderFor(profile+override) error = %v", err)
	}
	connOverride.Close()
	if gotPassword != "request-override" {
		t.Fatalf("override password = %q, want request-override", gotPassword)
	}
	overrideSnap, err := access.SnapshotRequest(context.Background(), "user-1", requestConnectionInput{
		ProfileID:    "profile-1",
		Password:     "request-override",
		RefreshToken: "refresh-override",
	})
	if err != nil {
		t.Fatalf("SnapshotRequest(profile+override) error = %v", err)
	}
	if dec, _ := crypto.DecryptWithDomain(overrideSnap.Creds.PasswordEncrypted, key, crypto.ConnectionCredentialDomain(false)); dec != "request-override" {
		t.Fatalf("override snapshot password = %q, want request-override", dec)
	}
	if dec, _ := crypto.DecryptWithDomain(overrideSnap.Creds.RefreshTokenEncrypted.String, key, crypto.DomainOAuthRefreshToken); dec != "refresh-override" {
		t.Fatalf("override snapshot refresh = %q, want refresh-override", dec)
	}

	// 2. Unconditionally rejects invalid provider without calling factory.
	_, err = openRequestProviderFor[profileProvider](access, context.Background(), "user-1", requestConnectionInput{
		DefaultProvider: "nextcloud",
		Provider:        "not-a-provider",
	})
	if !errors.Is(err, errProfileInvalidProvider) {
		t.Fatalf("openRequestProviderFor(invalid provider) error = %v, want errProfileInvalidProvider", err)
	}

	// 3. Rejects missing user when ProfileID is set.
	_, err = access.SnapshotRequest(context.Background(), "", requestConnectionInput{ProfileID: "profile-1"})
	if !errors.Is(err, errProfileNotFound) {
		t.Fatalf("SnapshotRequest(missing user) error = %v, want errProfileNotFound", err)
	}

	// 4. SnapshotRequest encrypts direct credentials when no profile is selected.
	snap, err := access.SnapshotRequest(context.Background(), "user-1", requestConnectionInput{
		DefaultProvider: "nextcloud",
		NormalizeURL:    true,
		URL:             "https://direct.example.test",
		Username:        "alice",
		Password:        "direct-secret",
		RefreshToken:    "direct-refresh",
	})
	if err != nil {
		t.Fatalf("SnapshotRequest(direct) error = %v", err)
	}
	if snap.ProfileID.Valid || snap.Provider != "nextcloud" || !snap.HasPassword || !snap.HasRefreshToken || !snap.TokenExpiresAt.Valid {
		t.Fatalf("unexpected direct snapshot metadata: %+v", snap)
	}
	if dec, _ := crypto.DecryptWithDomain(snap.Creds.PasswordEncrypted, key, crypto.ConnectionCredentialDomain(false)); dec != "direct-secret" {
		t.Fatalf("snapshot password = %q, want direct-secret", dec)
	}
	if dec, _ := crypto.DecryptWithDomain(snap.Creds.RefreshTokenEncrypted.String, key, crypto.DomainOAuthRefreshToken); dec != "direct-refresh" {
		t.Fatalf("snapshot refresh = %q, want direct-refresh", dec)
	}
}
