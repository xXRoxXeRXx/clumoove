package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"database/sql/driver"
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

type oauthRefreshLocker interface {
	TryClaimOAuthLock(ctx context.Context, jobType, jobID, role string, ttl time.Duration) (string, bool, error)
	ReleaseOAuthLock(ctx context.Context, jobType, jobID, role, token string) error
}

// ownedProfileAccess centralizes ownership checks, secret decryption/encryption,
// OAuth token refresh, MEGA session persistence, and provider construction so
// feature handlers interact with owned profiles only at intent level.
type ownedProfileAccess struct {
	db              *sql.DB
	encryptionKey   string
	providerFactory profileProviderFactory
	refreshOAuth    func(context.Context, string, string) (*oauth.TokenResponse, error)
	now             func() time.Time
	oauthRotations  *oauthRotationCache
	oauthLocker     oauthRefreshLocker
}

func (s *APIServer) rotationCache() *oauthRotationCache {
	if s == nil {
		return nil
	}
	s.oauthRotationMu.Lock()
	defer s.oauthRotationMu.Unlock()
	if s.oauthRotations == nil {
		s.oauthRotations = newOAuthRotationCache()
	}
	return s.oauthRotations
}

func (s *APIServer) profiles() *ownedProfileAccess {
	var locker oauthRefreshLocker
	if s != nil && s.queue != nil {
		locker = s.queue
	}
	return &ownedProfileAccess{
		db:              s.db,
		encryptionKey:   s.encryptionKey,
		providerFactory: s.providerFactory,
		oauthRotations:  s.rotationCache(),
		oauthLocker:     locker,
	}
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
	if userID == "" || profileID == "" || a == nil || a.db == nil {
		return errProfileNotFound
	}
	owned, err := db.VerifyProfileOwnershipContext(ctx, a.db, profileID, userID)
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
	profile, err := db.GetConnectionProfile(ctx, a.db, profileID)
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
	if err := db.UpdateSyncJobOAuthTokens(c.access.db, syncJobID, role, accessEncrypted, refreshEncrypted, expiresAt, expectedRefreshEncrypted); err != nil && !errors.Is(err, db.ErrOAuthTokenConflict) {
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

func (a *ownedProfileAccess) persistOAuthTokens(profile *db.ConnectionProfile, token *oauth.TokenResponse) error {
	accessEncrypted, err := crypto.EncryptWithDomain(token.AccessToken, a.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return err
	}
	refreshEncrypted, err := crypto.EncryptWithDomain(token.RefreshToken, a.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return err
	}
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := a.currentTime().Add(time.Duration(expiresIn) * time.Second)
	if err := db.UpdateConnectionProfileOAuthTokens(a.db, profile.ID, accessEncrypted, refreshEncrypted, expiresAt, profile.RefreshTokenEncrypted); err != nil {
		return err
	}
	profile.PasswordEncrypted = accessEncrypted
	profile.RefreshTokenEncrypted = refreshEncrypted
	profile.TokenExpiresAt = sql.NullTime{Time: expiresAt, Valid: true}
	return nil
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
	return db.UpdateConnectionProfileMegaSession(ctx, a.db, profile.ID, sessionIDEncrypted, masterKeyEncrypted, profile.MegaSessionIDEncrypted)
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

const (
	oauthRotationCacheTTL     = 24 * time.Hour
	oauthRotationCacheMaxSize = 1024
	oauthMaxResolveHops       = 32
	oauthLockTTL              = 30 * time.Second
	oauthLockMaxAttempts      = 50
	oauthLockPollInterval     = 100 * time.Millisecond
)

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
	var resolvedFromRotation string
	defer crypto.ZeroString(&resolvedFromRotation)
	defer crypto.ZeroString(&storedRefresh)

	if !allowRefresh {
		if rotated, ok := a.lookupOAuthRotation(ctx, userID, creds.Provider, creds.RefreshToken, a.currentTime()); ok {
			resolvedFromRotation = rotated
			creds.RefreshToken = rotated
		}
		return creds, nil
	}

	for hop := 0; hop < oauthMaxResolveHops; hop++ {
		if rotated, ok := a.lookupOAuthRotation(ctx, userID, creds.Provider, creds.RefreshToken, a.currentTime()); ok && rotated != creds.RefreshToken {
			resolvedFromRotation = rotated
			creds.RefreshToken = rotated
		}

		target := creds.RefreshToken
		targetKey := db.OAuthTokenRotationHash(userID, creds.Provider, target)
		releaseLocal := a.oauthRotations.lockKey(targetKey)
		releaseDist, lockErr := a.acquireDistributedOAuthLock(ctx, targetKey)
		if lockErr != nil {
			releaseLocal()
			return creds, lockErr
		}

		releaseLocks := func() {
			releaseDist()
			releaseLocal()
		}

		// Re-check inside the critical section: another worker or instance holding
		// target's lock may have just rotated it while we waited.
		if rotated, ok := a.lookupOAuthRotation(ctx, userID, creds.Provider, target, a.currentTime()); ok && rotated != target {
			resolvedFromRotation = rotated
			creds.RefreshToken = rotated
			releaseLocks()
			continue
		}

		if err := a.reloadProfileOAuthState(ctx, profile, suppliedRefresh, &creds, &storedRefresh, secretFailurePolicy); err != nil {
			releaseLocks()
			return creds, err
		}

		now := a.currentTime()
		if refreshPolicy != refreshOAuthAlways && !profileOAuthAccessNeedsRefresh(profile, creds.Password, now) {
			releaseLocks()
			return creds, nil
		}

		isProfileToken := suppliedRefresh == "" || (storedRefresh != "" && (subtle.ConstantTimeCompare([]byte(suppliedRefresh), []byte(storedRefresh)) == 1 || subtle.ConstantTimeCompare([]byte(creds.RefreshToken), []byte(storedRefresh)) == 1))
		previousRefresh := creds.RefreshToken
		token, refreshErr := a.refreshToken(ctx, creds.Provider, creds.RefreshToken)
		if refreshErr != nil || token == nil || token.AccessToken == "" {
			releaseLocks()
			return creds, errors.New("profile OAuth token refresh failed")
		}

		if token.RefreshToken != "" {
			a.recordOAuthRotationAliases(ctx, userID, creds.Provider, initialRefresh, suppliedRefresh, previousRefresh, storedRefresh, isProfileToken, token.RefreshToken, now)
		}
		if isProfileToken {
			if err := a.persistOAuthTokens(profile, token); err != nil {
				releaseLocks()
				return creds, err
			}
			creds.TokenExpiresAt = profile.TokenExpiresAt
		} else {
			expiresIn := token.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			creds.TokenExpiresAt = sql.NullTime{Time: now.Add(time.Duration(expiresIn) * time.Second), Valid: true}
		}
		creds.Password = token.AccessToken
		if token.RefreshToken != "" {
			creds.RefreshToken = token.RefreshToken
		}
		releaseLocks()
		return creds, nil
	}

	return creds, errors.New("profile OAuth token rotation limit exceeded")
}

func (a *ownedProfileAccess) reloadProfileOAuthState(
	ctx context.Context,
	profile *db.ConnectionProfile,
	suppliedRefresh string,
	creds *profileCreds,
	storedRefresh *string,
	secretFailurePolicy profileSecretFailurePolicy,
) error {
	if a.db == nil || profile == nil || profile.ID == "" {
		return nil
	}
	latest, err := db.GetConnectionProfile(ctx, a.db, profile.ID)
	if err != nil || latest == nil || latest.UserID != profile.UserID {
		return nil
	}
	*profile = *latest
	if latest.RefreshTokenEncrypted != "" {
		plain, decErr := crypto.DecryptBytesWithDomain(latest.RefreshTokenEncrypted, a.encryptionKey, crypto.DomainOAuthRefreshToken)
		if decErr != nil {
			if suppliedRefresh == "" && secretFailurePolicy == failOnProfileSecretError {
				return fmt.Errorf("%w: %w", errProfileSecret, decErr)
			}
		} else {
			*storedRefresh = string(plain)
			clear(plain)
			if suppliedRefresh == "" && *storedRefresh != "" {
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
					return fmt.Errorf("%w: %w", errProfileSecret, decErr)
				}
			} else {
				creds.Password = string(plain)
				clear(plain)
			}
		}
	}
	return nil
}

func (a *ownedProfileAccess) acquireDistributedOAuthLock(ctx context.Context, lockKey string) (func(), error) {
	if a.oauthLocker == nil {
		return func() {}, nil
	}
	for attempt := 0; attempt < oauthLockMaxAttempts; attempt++ {
		lockToken, claimed, err := a.oauthLocker.TryClaimOAuthLock(ctx, "profile", lockKey, "refresh", oauthLockTTL)
		if err != nil {
			return nil, err
		}
		if claimed {
			return func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = a.oauthLocker.ReleaseOAuthLock(releaseCtx, "profile", lockKey, "refresh", lockToken)
			}, nil
		}
		timer := time.NewTimer(oauthLockPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, errors.New("profile OAuth token refresh lock timeout")
}

func (a *ownedProfileAccess) lookupOAuthRotation(ctx context.Context, userID, provider, refreshToken string, now time.Time) (string, bool) {
	if refreshToken == "" || a.encryptionKey == "" {
		return "", false
	}
	if a.db != nil && userID != "" && provider != "" {
		tokenHash := db.OAuthTokenRotationHash(userID, provider, refreshToken)
		_, encrypted, found, err := db.LookupOAuthTokenRotation(ctx, a.db, userID, provider, tokenHash)
		if err == nil && found {
			plain, decErr := crypto.DecryptWithDomain(encrypted, a.encryptionKey, crypto.DomainOAuthRefreshToken)
			if decErr == nil && plain != "" {
				return plain, true
			}
		}
		// Fall through to in-memory cache when DB has no entry or minimal test drivers return driver.ErrSkip.
		_ = errors.Is(err, driver.ErrSkip)
	}
	return a.oauthRotations.lookup(userID, provider, refreshToken, a.encryptionKey, now)
}

func (a *ownedProfileAccess) recordOAuthRotationAliases(
	ctx context.Context,
	userID, provider string,
	initialRefresh, suppliedRefresh, previousRefresh, storedRefresh string,
	isProfileToken bool,
	rotatedRefresh string,
	now time.Time,
) {
	seen := make(map[string]struct{}, 4)
	recordAlias := func(alias string) {
		if alias == "" || alias == rotatedRefresh {
			return
		}
		if _, dup := seen[alias]; dup {
			return
		}
		seen[alias] = struct{}{}
		a.recordOAuthRotation(ctx, userID, provider, alias, rotatedRefresh, now)
	}
	recordAlias(previousRefresh)
	recordAlias(initialRefresh)
	recordAlias(suppliedRefresh)
	if isProfileToken {
		recordAlias(storedRefresh)
	}
}

func (a *ownedProfileAccess) recordOAuthRotation(ctx context.Context, userID, provider, previousToken, rotatedToken string, now time.Time) {
	if previousToken == "" || rotatedToken == "" || previousToken == rotatedToken || a.encryptionKey == "" {
		return
	}
	encrypted, err := crypto.EncryptWithDomain(rotatedToken, a.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return
	}
	prevHash := db.OAuthTokenRotationHash(userID, provider, previousToken)
	rotatedHash := db.OAuthTokenRotationHash(userID, provider, rotatedToken)
	a.oauthRotations.recordEncrypted(prevHash, rotatedHash, encrypted, now)
	if a.db != nil && userID != "" && provider != "" {
		_ = db.RecordOAuthTokenRotation(ctx, a.db, userID, provider, prevHash, rotatedHash, encrypted)
	}
}

type keyedLockEntry struct {
	mu   sync.Mutex
	refs int
}

type oauthKeyLockSet struct {
	mu    sync.Mutex
	locks map[string]*keyedLockEntry
}

func (l *oauthKeyLockSet) lock(key string) func() {
	if l == nil || key == "" {
		return func() {}
	}
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*keyedLockEntry)
	}
	entry, ok := l.locks[key]
	if !ok {
		entry = &keyedLockEntry{}
		l.locks[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			l.mu.Lock()
			entry.refs--
			if entry.refs == 0 && l.locks[key] == entry {
				delete(l.locks, key)
			}
			l.mu.Unlock()
		})
	}
}

type oauthRotationEntry struct {
	rotatedHash           string
	refreshTokenEncrypted string
	expiresAt             time.Time
}

// oauthRotationCache serializes in-process OAuth refresh attempts and acts as
// an encrypted cache fallback when no durable database rotation store is attached.
type oauthRotationCache struct {
	mu       sync.Mutex
	entries  map[string]oauthRotationEntry
	keyLocks oauthKeyLockSet
}

func newOAuthRotationCache() *oauthRotationCache {
	return &oauthRotationCache{entries: make(map[string]oauthRotationEntry)}
}

func (c *oauthRotationCache) lockKey(key string) func() {
	if c == nil {
		return func() {}
	}
	return c.keyLocks.lock(key)
}

func oauthRotationKey(userID, provider, refreshToken string) string {
	return db.OAuthTokenRotationHash(userID, provider, refreshToken)
}

func (c *oauthRotationCache) lookup(userID, provider, refreshToken, encryptionKey string, now time.Time) (string, bool) {
	if c == nil || refreshToken == "" || encryptionKey == "" {
		return "", false
	}
	key := oauthRotationKey(userID, provider, refreshToken)
	visited := map[string]struct{}{key: {}}
	var latestEncrypted string
	found := false

	c.mu.Lock()
	for hop := 0; hop < oauthMaxResolveHops; hop++ {
		entry, ok := c.entries[key]
		if !ok {
			break
		}
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			break
		}
		latestEncrypted = entry.refreshTokenEncrypted
		found = true
		if entry.rotatedHash == "" {
			break
		}
		if _, seen := visited[entry.rotatedHash]; seen {
			break
		}
		visited[entry.rotatedHash] = struct{}{}
		key = entry.rotatedHash
	}
	c.mu.Unlock()
	if !found {
		return "", false
	}
	plain, err := crypto.DecryptWithDomain(latestEncrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil || plain == "" {
		return "", false
	}
	return plain, true
}

func (c *oauthRotationCache) record(userID, provider, previousToken, rotatedToken, encryptionKey string, now time.Time) {
	if c == nil || previousToken == "" || rotatedToken == "" || previousToken == rotatedToken || encryptionKey == "" {
		return
	}
	encrypted, err := crypto.EncryptWithDomain(rotatedToken, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return
	}
	c.recordEncrypted(oauthRotationKey(userID, provider, previousToken), oauthRotationKey(userID, provider, rotatedToken), encrypted, now)
}

func (c *oauthRotationCache) recordEncrypted(key, rotatedHash, encrypted string, now time.Time) {
	if c == nil || key == "" || encrypted == "" {
		return
	}
	entry := oauthRotationEntry{
		rotatedHash:           rotatedHash,
		refreshTokenEncrypted: encrypted,
		expiresAt:             now.Add(oauthRotationCacheTTL),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]oauthRotationEntry)
	}
	for k, existing := range c.entries {
		if !now.Before(existing.expiresAt) {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= oauthRotationCacheMaxSize {
		var oldestKey string
		var oldestExpiry time.Time
		for k, existing := range c.entries {
			if oldestKey == "" || existing.expiresAt.Before(oldestExpiry) {
				oldestKey = k
				oldestExpiry = existing.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = entry
}

type profileProviderFactory func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error)

func megaProfileProviderOptions(creds profileCreds) []storage.ProviderOption {
	if creds.Provider == "mega" {
		return []storage.ProviderOption{storage.WithProviderMegaSession(creds.MegaSession)}
	}
	return nil
}
