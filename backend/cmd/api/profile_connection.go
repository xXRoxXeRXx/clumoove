package main

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/storage"
)

var (
	errProfileNotFound = errors.New("profile not found")
	errProfileNotOwned = errors.New("profile not owned")
)

// profileCreds is task-scoped plaintext connection material. Callers that keep
// it past provider creation must call clear when the task finishes.
type profileCreds struct {
	Provider     string
	URL          string
	Username     string
	Password     string
	RefreshToken string
	MegaSession  storage.MegaSession
}

func (c *profileCreds) clear() {
	crypto.ZeroString(&c.Password)
	crypto.ZeroString(&c.RefreshToken)
	clear(c.MegaSession.MasterKey)
}

// profileConnection owns task-scoped provider credentials and the provider.
// It deliberately has no HTTP request or response knowledge.
type profileConnection struct {
	profile  *db.ConnectionProfile
	provider storage.StorageProvider
	ctx      context.Context
	creds    profileCreds
}

func (c *profileConnection) close() {
	if c.provider != nil {
		_ = c.provider.Close()
	}
	c.creds.clear()
}

func (s *APIServer) resolveOwnedProfile(ctx context.Context, userID, profileID string) (*db.ConnectionProfile, error) {
	if userID == "" || profileID == "" || s.db == nil {
		return nil, errProfileNotFound
	}
	owned, err := db.VerifyProfileOwnershipContext(ctx, s.db, profileID, userID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, errProfileNotOwned
	}
	profile, err := db.GetConnectionProfile(ctx, s.db, profileID)
	if err != nil {
		return nil, errProfileNotFound
	}
	return profile, nil
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

type profileCredentialResolver struct {
	loadOwned  func(context.Context, string, string) (*db.ConnectionProfile, error)
	decrypt    func(string, string) ([]byte, error)
	decodeMega func(string, string) (storage.MegaSession, error)
	refresh    func(context.Context, string, string) (*oauth.TokenResponse, error)
	persist    func(*db.ConnectionProfile, *oauth.TokenResponse) error
	now        func() time.Time
}

func (s *APIServer) profileCredentialResolver() profileCredentialResolver {
	return profileCredentialResolver{
		loadOwned: s.resolveOwnedProfile,
		decrypt: func(ciphertext, domain string) ([]byte, error) {
			return crypto.DecryptBytesWithDomain(ciphertext, s.encryptionKey, domain)
		},
		decodeMega: func(sessionID, masterKey string) (storage.MegaSession, error) {
			return megasecret.DecodeMegaSession(sessionID, masterKey, s.encryptionKey)
		},
		refresh: oauth.RefreshToken,
		persist: s.persistProfileOAuthTokens,
		now:     time.Now,
	}
}

// resolveProfileCredentials is deliberately compatible with legacy request
// callers: they may supply direct credentials as a fallback. File-manager
// callers pass an empty base and fail closed on stored-secret errors.
func (s *APIServer) resolveProfileCredentials(ctx context.Context, userID, profileID string, base profileCreds, refreshPolicy profileOAuthRefreshPolicy, secretFailurePolicy profileSecretFailurePolicy) (profileCreds, *db.ConnectionProfile, error) {
	return s.profileCredentialResolver().resolve(ctx, userID, profileID, base, refreshPolicy, secretFailurePolicy)
}

func (r profileCredentialResolver) resolve(ctx context.Context, userID, profileID string, base profileCreds, refreshPolicy profileOAuthRefreshPolicy, secretFailurePolicy profileSecretFailurePolicy) (profileCreds, *db.ConnectionProfile, error) {
	if profileID == "" {
		return base, nil, nil
	}
	profile, err := r.loadOwned(ctx, userID, profileID)
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
	if profile.PasswordEncrypted != "" {
		plain, decryptErr := r.decrypt(profile.PasswordEncrypted, crypto.ConnectionCredentialDomain(oauth.IsProvider(creds.Provider)))
		if decryptErr != nil {
			if secretFailurePolicy == failOnProfileSecretError {
				creds.clear()
				return base, nil, decryptErr
			}
		} else {
			creds.Password = string(plain)
			clear(plain)
		}
	}
	if profile.RefreshTokenEncrypted != "" {
		plain, decryptErr := r.decrypt(profile.RefreshTokenEncrypted, crypto.DomainOAuthRefreshToken)
		if decryptErr != nil {
			if secretFailurePolicy == failOnProfileSecretError {
				creds.clear()
				return base, nil, decryptErr
			}
		} else {
			creds.RefreshToken = string(plain)
			clear(plain)
		}
	}
	if creds.Provider == "mega" && (profile.MegaSessionIDEncrypted != "" || profile.MegaMasterKeyEncrypted != "") {
		session, decodeErr := r.decodeMega(profile.MegaSessionIDEncrypted, profile.MegaMasterKeyEncrypted)
		if decodeErr != nil {
			creds.clear()
			return base, nil, decodeErr
		}
		creds.MegaSession = session
	}
	if creds.RefreshToken != "" && oauth.IsProvider(profile.Provider) && (refreshPolicy == refreshOAuthAlways || profileOAuthAccessNeedsRefresh(profile, creds.Password, r.now())) {
		token, refreshErr := r.refresh(ctx, creds.Provider, creds.RefreshToken)
		if refreshErr != nil || token == nil || token.AccessToken == "" {
			creds.clear()
			return base, nil, errors.New("profile OAuth token refresh failed")
		}
		if err := r.persist(profile, token); err != nil {
			creds.clear()
			return base, nil, err
		}
		creds.Password = token.AccessToken
		creds.RefreshToken = token.RefreshToken
	}
	return creds, profile, nil
}

func (s *APIServer) persistProfileOAuthTokens(profile *db.ConnectionProfile, token *oauth.TokenResponse) error {
	accessEncrypted, err := crypto.EncryptWithDomain(token.AccessToken, s.encryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return err
	}
	refreshEncrypted, err := crypto.EncryptWithDomain(token.RefreshToken, s.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return err
	}
	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return db.UpdateConnectionProfileOAuthTokens(s.db, profile.ID, accessEncrypted, refreshEncrypted, time.Now().Add(time.Duration(expiresIn)*time.Second), profile.RefreshTokenEncrypted)
}

func (s *APIServer) openOwnedProfileConnection(ctx context.Context, userID, profileID string) (*profileConnection, error) {
	creds, profile, err := s.resolveProfileCredentials(ctx, userID, profileID, profileCreds{}, refreshOAuthWhenExpiring, failOnProfileSecretError)
	if err != nil {
		return nil, err
	}
	return newProfileConnection(ctx, userID, profile, creds, storage.NewProvider)
}

type profileProviderFactory func(context.Context, string, string, string, string) (storage.StorageProvider, error)

func (s *APIServer) newRequestProvider(ctx context.Context, provider, url, username, password string) (storage.StorageProvider, error) {
	factory := s.providerFactory
	if factory == nil {
		factory = storage.NewProvider
	}
	return factory(ctx, provider, url, username, password)
}

func newProfileConnection(ctx context.Context, userID string, profile *db.ConnectionProfile, creds profileCreds, factory profileProviderFactory) (*profileConnection, error) {
	creds.MegaSession.MasterKey = append([]byte(nil), creds.MegaSession.MasterKey...)
	providerCtx := storage.WithLocalUserScope(ctx, userID)
	providerCtx = withMegaProfileSession(providerCtx, creds)
	provider, err := factory(providerCtx, creds.Provider, creds.URL, creds.Username, creds.Password)
	if err != nil {
		creds.clear()
		return nil, err
	}
	return &profileConnection{profile: profile, provider: provider, ctx: providerCtx, creds: creds}, nil
}

func (s *APIServer) persistMegaProfileSession(ctx context.Context, connection *profileConnection) error {
	megaProvider, ok := connection.provider.(*storage.MegaProvider)
	if !ok {
		return nil
	}
	sessionIDEncrypted, masterKeyEncrypted, err := s.encryptMegaSession(megaProvider.Session())
	if err != nil {
		return err
	}
	return db.UpdateConnectionProfileMegaSession(ctx, s.db, connection.profile.ID, sessionIDEncrypted, masterKeyEncrypted, connection.profile.MegaSessionIDEncrypted)
}

func (s *APIServer) encryptMegaSession(session storage.MegaSession) (string, string, error) {
	if session.ID == "" || len(session.MasterKey) == 0 {
		return "", "", nil
	}
	defer clear(session.MasterKey)
	sessionID, err := crypto.EncryptWithDomain(session.ID, s.encryptionKey, crypto.DomainMegaSessionID)
	if err != nil {
		return "", "", err
	}
	masterKey, err := crypto.EncryptWithDomain(base64.StdEncoding.EncodeToString(session.MasterKey), s.encryptionKey, crypto.DomainMegaMasterKey)
	if err != nil {
		return "", "", err
	}
	return sessionID, masterKey, nil
}

func withMegaProfileSession(ctx context.Context, creds profileCreds) context.Context {
	if creds.Provider == "mega" && creds.MegaSession.ID != "" {
		return storage.WithMegaSession(ctx, creds.MegaSession)
	}
	return ctx
}
