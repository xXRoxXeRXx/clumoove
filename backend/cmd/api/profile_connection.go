package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/storage"
)

var (
	errProfileNotFound        = errors.New("profile not found")
	errProfileNotOwned        = errors.New("profile not owned")
	errProfileOwnership       = errors.New("profile ownership unavailable")
	errProfileSecret          = errors.New("profile secret unavailable")
	errProfileProvider        = errors.New("profile provider unavailable")
	errProfileConnectFailed   = errors.New("profile connection failed")
	errProfileInvalidProvider = errors.New("profile provider invalid")
)

type oauthRotationCache = oauth.RotationCache

func newOAuthRotationCache() *oauthRotationCache {
	return oauth.NewRotationCache()
}

// ownedProfileAccess centralizes ownership checks, secret decryption/encryption,
// OAuth token refresh, MEGA session persistence, and provider construction so
// feature handlers interact with owned profiles only at intent level.
type ownedProfileAccess struct {
	db              *sql.DB
	store           db.ProfileStore
	syncs           db.SyncStore
	encryptionKey   string
	providerFactory profileProviderFactory
	refreshOAuth    func(context.Context, string, string) (*oauth.TokenResponse, error)
	now             func() time.Time
	oauthRotations  *oauth.RotationCache
	oauthLocker     oauth.TokenLocker
}

// sharedRotationCache lazily creates the single OAuth rotation cache that
// profile access, sync, and the rotation daemon must share.
type sharedRotationCache struct {
	mu    sync.Mutex
	cache *oauth.RotationCache
}

func (c *sharedRotationCache) get() *oauth.RotationCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = oauth.NewRotationCache()
	}
	return c.cache
}

// newOwnedProfileAccess assembles profile access from explicit dependencies so
// it never needs the HTTP gateway.
func newOwnedProfileAccess(database *sql.DB, encryptionKey string, factory profileProviderFactory, locker oauth.TokenLocker, rotations *oauth.RotationCache) *ownedProfileAccess {
	return &ownedProfileAccess{
		db:              database,
		store:           profileStoreFor(database),
		syncs:           syncStoreFor(database),
		encryptionKey:   encryptionKey,
		providerFactory: factory,
		oauthRotations:  rotations,
		oauthLocker:     locker,
	}
}

// profileStoreFor returns the PostgreSQL profile store, or nil when the module is
// built without a database handle (route-only tests).
func profileStoreFor(database *sql.DB) db.ProfileStore {
	if database == nil {
		return nil
	}
	return db.NewProfileStore(database)
}

// syncStore returns the injected sync store, falling back to one built on the
// database handle for callers that assemble the struct by hand.
func (a *ownedProfileAccess) syncStore() db.SyncStore {
	if a == nil {
		return nil
	}
	if a.syncs != nil {
		return a.syncs
	}
	return syncStoreFor(a.db)
}

// profileStore returns the injected store, falling back to one built on the
// database handle for callers that assemble the struct by hand.
func (a *ownedProfileAccess) profileStore() db.ProfileStore {
	if a == nil {
		return nil
	}
	if a.store != nil {
		return a.store
	}
	return profileStoreFor(a.db)
}

// profiles composes the shared profile access component from gateway state.
func (s *APIServer) profiles() *ownedProfileAccess {
	var locker oauth.TokenLocker
	if s.queue != nil {
		locker = s.queue
	}
	return newOwnedProfileAccess(s.db, s.encryptionKey, s.providerFactory, locker, s.oauthRotations.get())
}

func (a *ownedProfileAccess) currentTime() time.Time {
	if a != nil && a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *ownedProfileAccess) refreshToken(ctx context.Context, provider, refreshToken string) (*oauth.TokenResponse, error) {
	if a != nil && a.refreshOAuth != nil {
		return a.refreshOAuth(ctx, provider, refreshToken)
	}
	return oauth.RefreshToken(ctx, provider, refreshToken)
}

func (a *ownedProfileAccess) VerifyOwned(ctx context.Context, userID, profileID string) error {
	if userID == "" || profileID == "" {
		return errProfileNotFound
	}
	store := a.profileStore()
	if store == nil {
		return errProfileNotFound
	}
	owned, err := store.VerifyProfileOwnership(ctx, profileID, userID)
	if err != nil {
		return fmt.Errorf("%w: %w", errProfileOwnership, err)
	}
	if !owned {
		return errProfileNotOwned
	}
	return nil
}

func (a *ownedProfileAccess) LoadOwned(ctx context.Context, userID, profileID string) (*db.ConnectionProfile, error) {
	if err := a.VerifyOwned(ctx, userID, profileID); err != nil {
		return nil, err
	}
	profile, err := a.profileStore().GetConnectionProfile(ctx, profileID)
	if err != nil {
		return nil, errProfileNotFound
	}
	return profile, nil
}

func (a *ownedProfileAccess) EncryptProfileSecrets(provider, password, refreshToken string) (string, string, error) {
	var passwordEnc string
	if password != "" {
		enc, err := crypto.EncryptWithDomain(password, a.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(provider)))
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", errProfileSecret, err)
		}
		passwordEnc = enc
	}
	var refreshEnc string
	if refreshToken != "" {
		enc, err := crypto.EncryptWithDomain(refreshToken, a.encryptionKey, crypto.DomainOAuthRefreshToken)
		if err != nil {
			return "", "", fmt.Errorf("%w: %w", errProfileSecret, err)
		}
		refreshEnc = enc
	}
	return passwordEnc, refreshEnc, nil
}

type ownedProfileSnapshot struct {
	Profile *db.ConnectionProfile
	Creds   persistedProfileCreds
}

func (a *ownedProfileAccess) SnapshotOwned(ctx context.Context, userID, profileID string) (ownedProfileSnapshot, error) {
	creds, profile, err := a.resolveCredentials(ctx, userID, profileID, profileCreds{}, refreshOAuthWhenExpiring, failOnProfileSecretError)
	if err != nil {
		return ownedProfileSnapshot{}, err
	}
	defer creds.clear()
	persisted, err := a.encryptCreds(creds)
	if err != nil {
		return ownedProfileSnapshot{}, fmt.Errorf("%w: %w", errProfileSecret, err)
	}
	return ownedProfileSnapshot{Profile: profile, Creds: persisted}, nil
}

type ownedProfileConnection struct {
	access   *ownedProfileAccess
	profile  *db.ConnectionProfile
	provider profileProvider
	ctx      context.Context
	creds    profileCreds
}

func (c *ownedProfileConnection) Profile() *db.ConnectionProfile { return c.profile }
func (c *ownedProfileConnection) Provider() profileProvider    { return c.provider }
func (c *ownedProfileConnection) Context() context.Context     { return c.ctx }

func (c *ownedProfileConnection) PersistSession(ctx context.Context) error {
	return c.access.persistMegaSession(ctx, c.profile, c.provider)
}

func (c *ownedProfileConnection) SyncJobOAuthTokens(syncJobID, role, expectedRefreshEncrypted string) error {
	if !oauth.IsProvider(c.creds.Provider) || c.creds.RefreshToken == "" {
		return nil
	}
	accessEncrypted, err := crypto.EncryptWithDomain(c.creds.Password, c.access.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return fmt.Errorf("%w: %w", errProfileSecret, err)
	}
	refreshEncrypted, err := crypto.EncryptWithDomain(c.creds.RefreshToken, c.access.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return fmt.Errorf("%w: %w", errProfileSecret, err)
	}
	expiresAt := c.profile.TokenExpiresAt.Time
	if !c.profile.TokenExpiresAt.Valid {
		expiresAt = c.access.currentTime().Add(time.Hour)
	}
	rotated := db.EncryptedOAuthTokens{Access: accessEncrypted, Refresh: refreshEncrypted, ExpiresAt: expiresAt}
	if err := c.access.syncStore().SyncJobTokenSink(syncJobID, role).Persist(context.Background(), rotated, expectedRefreshEncrypted); err != nil && !errors.Is(err, db.ErrOAuthTokenConflict) {
		return err
	}
	return nil
}

func (c *ownedProfileConnection) Close() {
	if c.provider != nil {
		_ = c.provider.Close()
	}
	c.creds.clear()
}

func (a *ownedProfileAccess) openOwnedWithPolicy(ctx context.Context, userID, profileID string, refreshPolicy profileOAuthRefreshPolicy) (*ownedProfileConnection, error) {
	creds, profile, err := a.resolveCredentials(ctx, userID, profileID, profileCreds{}, refreshPolicy, failOnProfileSecretError)
	if err != nil {
		return nil, err
	}
	creds.MegaSession.MasterKey = append([]byte(nil), creds.MegaSession.MasterKey...)
	providerCtx := storage.WithLocalUserScope(ctx, userID)
	provider, err := a.newProvider(providerCtx, creds.Provider, creds.URL, creds.Username, creds.Password, megaProfileProviderOptions(creds)...)
	if err != nil {
		creds.clear()
		return nil, wrapProfileProviderError(err)
	}
	return &ownedProfileConnection{access: a, profile: profile, provider: provider, ctx: providerCtx, creds: creds}, nil
}

func (a *ownedProfileAccess) OpenOwned(ctx context.Context, userID, profileID string) (*ownedProfileConnection, error) {
	return a.openOwnedWithPolicy(ctx, userID, profileID, refreshOAuthWhenExpiring)
}

func (a *ownedProfileAccess) TestOwned(ctx context.Context, userID, profileID string) (string, error) {
	connection, err := a.openOwnedWithPolicy(ctx, userID, profileID, refreshOAuthAlways)
	if err != nil {
		return "", err
	}
	defer connection.Close()

	connectCtx, cancel := context.WithTimeout(connection.ctx, 15*time.Second)
	defer cancel()
	ok, cerr := connection.provider.Connect(connectCtx)
	if !ok {
		if cerr != nil {
			return connection.profile.Provider, fmt.Errorf("%w: %w", errProfileConnectFailed, cerr)
		}
		return connection.profile.Provider, errProfileConnectFailed
	}
	if err := connection.PersistSession(ctx); err != nil {
		return connection.profile.Provider, fmt.Errorf("%w: %w", errProfileConnectFailed, err)
	}
	return connection.profile.Provider, nil
}

type requestConnectionInput struct {
	ProfileID       string
	DefaultProvider string
	NormalizeURL    bool
	Provider        string
	URL             string
	Username        string
	Password        string
	RefreshToken    string
	TokenExpiresAt  sql.NullTime
	TokenExpiresIn  int
}

type openedRequestConnection[T any] struct {
	Client   T
	Provider string
	URL      string
	Username string
	closeFn  func()
}

func (c *openedRequestConnection[T]) Close() {
	if c != nil && c.closeFn != nil {
		c.closeFn()
	}
}

type requestConnectionSnapshot struct {
	ProfileID       sql.NullString
	Provider        string
	URL             string
	Username        string
	HasPassword     bool
	HasRefreshToken bool
	TokenExpiresAt  sql.NullTime
	Creds           persistedProfileCreds
}

func (a *ownedProfileAccess) resolveRequestConnection(ctx context.Context, userID string, input requestConnectionInput) (profileCreds, error) {
	base := profileCreds{
		Provider:     input.Provider,
		URL:          input.URL,
		Username:     input.Username,
		Password:     input.Password,
		RefreshToken: input.RefreshToken,
	}
	var (
		creds profileCreds
		err   error
	)
	if input.ProfileID != "" {
		if userID == "" {
			return profileCreds{}, errProfileNotFound
		}
		creds, _, err = a.resolveCredentials(ctx, userID, input.ProfileID, base, refreshOAuthAlways, fallbackOnProfileSecretError)
		if err != nil {
			return profileCreds{}, err
		}
	} else {
		creds = base
	}
	if creds.Provider == "" && input.DefaultProvider != "" {
		creds.Provider = input.DefaultProvider
	}
	if input.NormalizeURL {
		creds.URL = normalizeProviderURL(creds.Provider, creds.URL)
	}
	return creds, nil
}

func wrapProfileProviderError(err error) error {
	if errors.Is(err, errProfileInvalidProvider) {
		return err
	}
	return fmt.Errorf("%w: %w", errProfileProvider, err)
}

func narrowProvider[T any](client profileProvider) (T, error) {
	typed, ok := any(client).(T)
	if !ok {
		if client != nil {
			_ = client.Close()
		}
		var zero T
		return zero, errProfileProvider
	}
	return typed, nil
}

func openRequestProviderFor[T any](access *ownedProfileAccess, ctx context.Context, userID string, input requestConnectionInput) (*openedRequestConnection[T], error) {
	creds, err := access.resolveRequestConnection(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	providerCtx := ctx
	if userID != "" {
		providerCtx = storage.WithLocalUserScope(ctx, userID)
	}
	client, err := access.newProvider(providerCtx, creds.Provider, creds.URL, creds.Username, creds.Password, megaProfileProviderOptions(creds)...)
	if err != nil {
		creds.clear()
		return nil, wrapProfileProviderError(err)
	}
	typed, err := narrowProvider[T](client)
	if err != nil {
		creds.clear()
		return nil, err
	}
	return &openedRequestConnection[T]{
		Client:   typed,
		Provider: creds.Provider,
		URL:      creds.URL,
		Username: creds.Username,
		closeFn: func() {
			if client != nil {
				_ = client.Close()
			}
			creds.clear()
		},
	}, nil
}

func (a *ownedProfileAccess) SnapshotRequest(ctx context.Context, userID string, input requestConnectionInput) (requestConnectionSnapshot, error) {
	creds, err := a.resolveRequestConnection(ctx, userID, input)
	if err != nil {
		return requestConnectionSnapshot{}, err
	}
	defer creds.clear()

	tokenExpiresAt := creds.TokenExpiresAt
	if !tokenExpiresAt.Valid && creds.RefreshToken != "" {
		if input.TokenExpiresAt.Valid {
			tokenExpiresAt = input.TokenExpiresAt
		} else if input.TokenExpiresIn > 0 {
			tokenExpiresAt = sql.NullTime{Time: a.currentTime().Add(time.Duration(input.TokenExpiresIn) * time.Second), Valid: true}
		} else {
			tokenExpiresAt = sql.NullTime{Time: a.currentTime().Add(time.Hour), Valid: true}
		}
	}

	persisted, err := a.encryptCreds(creds)
	if err != nil {
		return requestConnectionSnapshot{}, fmt.Errorf("%w: %w", errProfileSecret, err)
	}
	return requestConnectionSnapshot{
		ProfileID:       sql.NullString{String: input.ProfileID, Valid: input.ProfileID != ""},
		Provider:        creds.Provider,
		URL:             creds.URL,
		Username:        creds.Username,
		HasPassword:     creds.Password != "",
		HasRefreshToken: creds.RefreshToken != "",
		TokenExpiresAt:  tokenExpiresAt,
		Creds:           persisted,
	}, nil
}

func (a *ownedProfileAccess) openDirectProvider(ctx context.Context, userID, provider, url, username, password string, megaSession storage.MegaSession) (profileProvider, error) {
	providerCtx := ctx
	if userID != "" {
		providerCtx = storage.WithLocalUserScope(ctx, userID)
	}
	var opts []storage.ProviderOption
	if provider == "mega" {
		opts = []storage.ProviderOption{storage.WithProviderMegaSession(megaSession)}
	}
	client, err := a.newProvider(providerCtx, provider, url, username, password, opts...)
	if err != nil {
		return nil, wrapProfileProviderError(err)
	}
	return client, nil
}

// profileCreds is task-scoped plaintext connection material. Callers that keep
// it past provider creation must call clear when the task finishes.
type profileCreds struct {
	Provider       string
	URL            string
	Username       string
	Password       string
	RefreshToken   string
	TokenExpiresAt sql.NullTime
	MegaSession    storage.MegaSession
}

func (c *profileCreds) clear() {
	crypto.ZeroString(&c.Password)
	crypto.ZeroString(&c.RefreshToken)
	clear(c.MegaSession.MasterKey)
}

// profileProvider is the minimal provider lifecycle contract owned by
// connection-profile access.
type profileProvider interface {
	storage.ProviderLifecycle
	Connect(context.Context) (bool, error)
}

// persistedProfileCreds is the encrypted task snapshot of a resolved profile.
// It contains no plaintext and is safe to persist with a long-running job.
type persistedProfileCreds struct {
	PasswordEncrypted      string
	RefreshTokenEncrypted  sql.NullString
	MegaSessionIDEncrypted string
	MegaMasterKeyEncrypted string
}

func (a *ownedProfileAccess) encryptCreds(creds profileCreds) (persistedProfileCreds, error) {
	password, err := crypto.EncryptWithDomain(creds.Password, a.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.Provider)))
	if err != nil {
		return persistedProfileCreds{}, err
	}
	persisted := persistedProfileCreds{PasswordEncrypted: password}
	if creds.RefreshToken != "" {
		refresh, err := crypto.EncryptWithDomain(creds.RefreshToken, a.encryptionKey, crypto.DomainOAuthRefreshToken)
		if err != nil {
			return persistedProfileCreds{}, err
		}
		persisted.RefreshTokenEncrypted = sql.NullString{String: refresh, Valid: true}
	}
	persisted.MegaSessionIDEncrypted, persisted.MegaMasterKeyEncrypted, err = a.encryptMegaSession(creds.MegaSession)
	if err != nil {
		return persistedProfileCreds{}, err
	}
	return persisted, nil
}

// refresher binds the profile access's shared rotation infrastructure.
func (a *ownedProfileAccess) refresher() *oauth.Refresher {
	return oauth.NewRefresher(oauth.RefresherConfig{DB: a.db, Cache: a.oauthRotations, Locker: a.oauthLocker, EncryptionKey: a.encryptionKey, Now: a.currentTime})
}

func (a *ownedProfileAccess) newProvider(ctx context.Context, provider, url, username, password string, opts ...storage.ProviderOption) (profileProvider, error) {
	if !storage.IsValidProvider(provider) {
		return nil, errProfileInvalidProvider
	}
	if a != nil && a.providerFactory != nil {
		return a.providerFactory(ctx, provider, url, username, password, opts...)
	}
	return storage.NewProvider(ctx, provider, url, username, password, opts...)
}

func (a *ownedProfileAccess) persistMegaSession(ctx context.Context, profile *db.ConnectionProfile, provider profileProvider) error {
	megaProvider, ok := provider.(*storage.MegaProvider)
	if !ok {
		return nil
	}
	sessionIDEncrypted, masterKeyEncrypted, err := a.encryptMegaSession(megaProvider.Session())
	if err != nil {
		return err
	}
	return a.profileStore().UpdateConnectionProfileMegaSession(ctx, profile.ID, sessionIDEncrypted, masterKeyEncrypted, profile.MegaSessionIDEncrypted)
}

func (a *ownedProfileAccess) encryptMegaSession(session storage.MegaSession) (string, string, error) {
	if session.ID == "" || len(session.MasterKey) == 0 {
		return "", "", nil
	}
	defer clear(session.MasterKey)
	sessionID, err := crypto.EncryptWithDomain(session.ID, a.encryptionKey, crypto.DomainMegaSessionID)
	if err != nil {
		return "", "", err
	}
	masterKey, err := crypto.EncryptWithDomain(base64.StdEncoding.EncodeToString(session.MasterKey), a.encryptionKey, crypto.DomainMegaMasterKey)
	if err != nil {
		return "", "", err
	}
	return sessionID, masterKey, nil
}

func profileOAuthAccessNeedsRefresh(profile *db.ConnectionProfile, accessToken string, now time.Time) bool {
	return oauth.IsProvider(profile.Provider) &&
		(accessToken == "" || !profile.TokenExpiresAt.Valid || profile.TokenExpiresAt.Time.Before(now.Add(2*time.Minute)))
}

// profileOAuthRefreshPolicy chooses whether a saved OAuth access token is
// refreshed eagerly or only when it is missing or close to expiry.
type profileOAuthRefreshPolicy uint8

const (
	refreshOAuthAlways profileOAuthRefreshPolicy = iota
	refreshOAuthWhenExpiring
)

type profileSecretFailurePolicy uint8

const (
	failOnProfileSecretError profileSecretFailurePolicy = iota
	fallbackOnProfileSecretError
)

func (a *ownedProfileAccess) resolveCredentials(ctx context.Context, userID, profileID string, base profileCreds, refreshPolicy profileOAuthRefreshPolicy, secretFailurePolicy profileSecretFailurePolicy) (profileCreds, *db.ConnectionProfile, error) {
	if profileID == "" {
		return base, nil, nil
	}
	profile, err := a.LoadOwned(ctx, userID, profileID)
	if err != nil {
		return base, nil, err
	}
	creds := base
	creds.MegaSession.MasterKey = append([]byte(nil), base.MegaSession.MasterKey...)
	if profile.Provider != "" {
		creds.Provider = profile.Provider
	}
	if profile.URL != "" {
		creds.URL = profile.URL
	}
	if profile.Username != "" {
		creds.Username = profile.Username
	}
	if profile.PasswordEncrypted != "" && base.Password == "" {
		plain, decryptErr := crypto.DecryptBytesWithDomain(profile.PasswordEncrypted, a.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.Provider)))
		if decryptErr != nil {
			if secretFailurePolicy == failOnProfileSecretError {
				creds.clear()
				return base, nil, fmt.Errorf("%w: %w", errProfileSecret, decryptErr)
			}
		} else {
			creds.Password = string(plain)
			clear(plain)
		}
	}
	var storedRefresh string
	defer crypto.ZeroString(&storedRefresh)
	if profile.RefreshTokenEncrypted != "" {
		plain, decryptErr := crypto.DecryptBytesWithDomain(profile.RefreshTokenEncrypted, a.encryptionKey, crypto.DomainOAuthRefreshToken)
		if decryptErr != nil {
			if base.RefreshToken == "" && secretFailurePolicy == failOnProfileSecretError {
				creds.clear()
				return base, nil, fmt.Errorf("%w: %w", errProfileSecret, decryptErr)
			}
		} else {
			storedRefresh = string(plain)
			clear(plain)
			if base.RefreshToken == "" {
				creds.RefreshToken = storedRefresh
			}
		}
	}
	if creds.Provider == "mega" && (profile.MegaSessionIDEncrypted != "" || profile.MegaMasterKeyEncrypted != "") {
		session, decodeErr := megasecret.DecodeMegaSession(profile.MegaSessionIDEncrypted, profile.MegaMasterKeyEncrypted, a.encryptionKey)
		if decodeErr != nil {
			creds.clear()
			return base, nil, fmt.Errorf("%w: %w", errProfileSecret, decodeErr)
		}
		creds.MegaSession = session
	}
	if base.Password == "" && base.RefreshToken == "" {
		creds.TokenExpiresAt = profile.TokenExpiresAt
	}
	if creds.RefreshToken != "" && oauth.IsProvider(profile.Provider) {
		var err error
		creds, err = a.resolveAndRefreshOAuth(ctx, userID, base.Password == "", base.RefreshToken, profile, creds, storedRefresh, refreshPolicy, secretFailurePolicy)
		if err != nil {
			creds.clear()
			return base, nil, err
		}
	}
	return creds, profile, nil
}

func (a *ownedProfileAccess) resolveAndRefreshOAuth(
	ctx context.Context,
	userID string,
	allowRefresh bool,
	suppliedRefresh string,
	profile *db.ConnectionProfile,
	creds profileCreds,
	storedRefresh string,
	refreshPolicy profileOAuthRefreshPolicy,
	secretFailurePolicy profileSecretFailurePolicy,
) (profileCreds, error) {
	initialRefresh := creds.RefreshToken
	rowRefreshEnc := profile.RefreshTokenEncrypted
	var resolvedFromRotation string
	defer crypto.ZeroString(&resolvedFromRotation)
	defer crypto.ZeroString(&storedRefresh)

	if !allowRefresh {
		var backupStore oauth.RotationBackupStore
		if bs, ok := a.oauthLocker.(oauth.RotationBackupStore); ok {
			backupStore = bs
		}
		rotated, ok, err := oauth.LookupRotatedRefreshTokenWithStore(ctx, a.db, backupStore, a.oauthRotations, a.encryptionKey, userID, creds.Provider, creds.RefreshToken, a.currentTime())
		if err != nil {
			return creds, err
		}
		if ok {
			resolvedFromRotation = rotated
			creds.RefreshToken = rotated
		}
		return creds, nil
	}

	var isProfileToken bool
	result, err := a.refresher().Refresh(ctx, oauth.OwnerRefresh{
		UserID:       userID,
		Provider:     creds.Provider,
		RefreshToken: creds.RefreshToken,
		Label:        "connection profile " + profile.ID,
		Refresh:      a.refreshToken,
		ExtraAliases: func() []string {
			aliases := []string{initialRefresh, suppliedRefresh}
			if isProfileToken {
				aliases = append(aliases, storedRefresh)
			}
			return aliases
		},
		OnLocked: func(ctx context.Context, target string) (string, bool, string, error) {
			creds.RefreshToken = target
			rowChanged, reloadErr := a.reloadProfileOAuthState(ctx, profile, suppliedRefresh, rowRefreshEnc, &creds, &storedRefresh, secretFailurePolicy)
			if reloadErr != nil {
				return "", false, "", reloadErr
			}
			if rowChanged {
				rowRefreshEnc = profile.RefreshTokenEncrypted
				if suppliedRefresh == "" && storedRefresh != "" && storedRefresh != target {
					// Another writer updated the profile's stored refresh token while we waited.
					// Release target's lock and re-acquire on the newer token before inspecting/refreshing.
					return "", false, storedRefresh, nil
				}
			}
			now := a.currentTime()
			if refreshPolicy != refreshOAuthAlways && !profileOAuthAccessNeedsRefresh(profile, creds.Password, now) {
				return "", true, "", nil
			}
			isProfileToken = suppliedRefresh == "" || (storedRefresh != "" && (subtle.ConstantTimeCompare([]byte(suppliedRefresh), []byte(storedRefresh)) == 1 || subtle.ConstantTimeCompare([]byte(target), []byte(storedRefresh)) == 1))
			return "", false, "", nil
		},
		Sink:            a.profileStore().TokenSink(profile.ID),
		ExpectedRefresh: func() string { return profile.RefreshTokenEncrypted },
		SkipPersist:     func() bool { return !isProfileToken },
		OnPersisted: func(tokens db.EncryptedOAuthTokens) {
			profile.PasswordEncrypted = tokens.Access
			profile.RefreshTokenEncrypted = tokens.Refresh
			profile.TokenExpiresAt = tokens.NullExpiresAt()
		},
	})
	if err != nil {
		return creds, err
	}
	if result.Adopted {
		return creds, nil
	}

	token := result.Token
	if isProfileToken {
		creds.TokenExpiresAt = profile.TokenExpiresAt
	} else {
		expiresIn := token.ExpiresIn
		if expiresIn <= 0 {
			expiresIn = 3600
		}
		creds.TokenExpiresAt = sql.NullTime{Time: a.currentTime().Add(time.Duration(expiresIn) * time.Second), Valid: true}
	}
	creds.Password = token.AccessToken
	if token.RefreshToken != "" {
		resolvedFromRotation = token.RefreshToken
		creds.RefreshToken = token.RefreshToken
	}
	return creds, nil
}

func (a *ownedProfileAccess) reloadProfileOAuthState(
	ctx context.Context,
	profile *db.ConnectionProfile,
	suppliedRefresh string,
	expectedRowRefreshEnc string,
	creds *profileCreds,
	storedRefresh *string,
	secretFailurePolicy profileSecretFailurePolicy,
) (bool, error) {
	store := a.profileStore()
	if store == nil || profile == nil || profile.ID == "" {
		return false, nil
	}
	latest, err := store.GetConnectionProfile(ctx, profile.ID)
	if err != nil || latest == nil || latest.UserID != profile.UserID {
		return false, nil
	}
	rowChanged := latest.RefreshTokenEncrypted != expectedRowRefreshEnc
	*profile = *latest
	if latest.RefreshTokenEncrypted != "" {
		plain, decErr := crypto.DecryptBytesWithDomain(latest.RefreshTokenEncrypted, a.encryptionKey, crypto.DomainOAuthRefreshToken)
		if decErr != nil {
			if suppliedRefresh == "" && secretFailurePolicy == failOnProfileSecretError {
				return rowChanged, fmt.Errorf("%w: %w", errProfileSecret, decErr)
			}
		} else {
			*storedRefresh = string(plain)
			clear(plain)
			if suppliedRefresh == "" && rowChanged && *storedRefresh != "" {
				creds.RefreshToken = *storedRefresh
			}
		}
	}
	if suppliedRefresh == "" {
		creds.TokenExpiresAt = latest.TokenExpiresAt
		if latest.PasswordEncrypted != "" {
			plain, decErr := crypto.DecryptBytesWithDomain(latest.PasswordEncrypted, a.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.Provider)))
			if decErr != nil {
				if secretFailurePolicy == failOnProfileSecretError {
					return rowChanged, fmt.Errorf("%w: %w", errProfileSecret, decErr)
				}
			} else {
				creds.Password = string(plain)
				clear(plain)
			}
		}
	}
	return rowChanged, nil
}

type profileProviderFactory func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error)

func megaProfileProviderOptions(creds profileCreds) []storage.ProviderOption {
	if creds.Provider == "mega" {
		return []storage.ProviderOption{storage.WithProviderMegaSession(creds.MegaSession)}
	}
	return nil
}
