package main

import (
	"context"
	"errors"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
	"backend/internal/storage"
)

var (
	errFileProfileNotFound     = errors.New("file profile not found")
	errFileProviderUnavailable = errors.New("file provider unavailable")
)

type resolvedFileProfile struct {
	profile  *db.ConnectionProfile
	provider storage.StorageProvider
	ctx      context.Context
	close    func()
}

func fileProfileOAuthAccessNeedsRefresh(profile *db.ConnectionProfile, accessToken string, now time.Time) bool {
	return oauth.IsProvider(profile.Provider) &&
		(accessToken == "" || !profile.TokenExpiresAt.Valid || profile.TokenExpiresAt.Time.Before(now.Add(2*time.Minute)))
}

func (s *APIServer) loadOwnedFileProfile(ctx context.Context, userID, profileID string) (*db.ConnectionProfile, error) {
	if userID == "" || profileID == "" {
		return nil, errFileProfileNotFound
	}
	owned, err := db.VerifyProfileOwnershipContext(ctx, s.db, profileID, userID)
	if err != nil {
		return nil, errFileProviderUnavailable
	}
	if !owned {
		return nil, errFileProfileNotFound
	}
	profile, err := db.GetConnectionProfile(ctx, s.db, profileID)
	if err != nil {
		return nil, errFileProfileNotFound
	}
	return profile, nil
}

// resolveFileProfile is intentionally profile-only: credentials from a request
// are never accepted by file-manager endpoints. It also preserves the factory's
// SSRF, host-key, local-tenant, and redirect protections.
func (s *APIServer) resolveFileProfile(ctx context.Context, userID, profileID string) (*resolvedFileProfile, error) {
	profile, err := s.loadOwnedFileProfile(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}

	password := ""
	if profile.PasswordEncrypted != "" {
		plain, decryptErr := crypto.DecryptBytesWithDomain(profile.PasswordEncrypted, s.encryptionKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(profile.Provider)))
		if decryptErr != nil {
			return nil, errFileProviderUnavailable
		}
		password = string(plain)
		clear(plain)
	}
	refreshToken := ""
	if profile.RefreshTokenEncrypted != "" {
		plain, decryptErr := crypto.DecryptBytesWithDomain(profile.RefreshTokenEncrypted, s.encryptionKey, crypto.DomainOAuthRefreshToken)
		if decryptErr != nil {
			crypto.ZeroString(&password)
			return nil, errFileProviderUnavailable
		}
		refreshToken = string(plain)
		clear(plain)
	}

	if refreshToken != "" && fileProfileOAuthAccessNeedsRefresh(profile, password, time.Now()) {
		token, refreshErr := oauth.RefreshToken(ctx, profile.Provider, refreshToken)
		if refreshErr != nil || token == nil || token.AccessToken == "" || s.persistProfileOAuthTokens(profile, token) != nil {
			crypto.ZeroString(&password)
			crypto.ZeroString(&refreshToken)
			return nil, errFileProviderUnavailable
		}
		password = token.AccessToken
		refreshToken = token.RefreshToken
	}
	crypto.ZeroString(&refreshToken)

	providerCtx := storage.WithLocalUserScope(ctx, userID)
	megaSession := storage.MegaSession{}
	if profile.Provider == "mega" && (profile.MegaSessionIDEncrypted != "" || profile.MegaMasterKeyEncrypted != "") {
		var megaErr error
		megaSession, megaErr = megasecret.DecodeMegaSession(profile.MegaSessionIDEncrypted, profile.MegaMasterKeyEncrypted, s.encryptionKey)
		if megaErr != nil {
			crypto.ZeroString(&password)
			return nil, errFileProviderUnavailable
		}
		providerCtx = storage.WithMegaSession(providerCtx, megaSession)
	}

	provider, err := storage.NewProvider(providerCtx, profile.Provider, profile.URL, profile.Username, password)
	if err != nil {
		crypto.ZeroString(&password)
		clear(megaSession.MasterKey)
		return nil, errFileProviderUnavailable
	}
	connectCtx, cancel := context.WithTimeout(providerCtx, 30*time.Second)
	connected := false
	var connectErr error
	if managerConnector, ok := provider.(storage.ManagerConnector); ok {
		connected, connectErr = managerConnector.ConnectManager(connectCtx)
	} else {
		connected, connectErr = provider.Connect(connectCtx)
	}
	cancel()
	if connectErr != nil || !connected {
		_ = provider.Close()
		crypto.ZeroString(&password)
		clear(megaSession.MasterKey)
		return nil, errFileProviderUnavailable
	}

	if megaProvider, ok := provider.(*storage.MegaProvider); ok {
		session := megaProvider.Session()
		sessionIDEncrypted, masterKeyEncrypted, saveErr := s.encryptMegaSession(session)
		if saveErr != nil || db.UpdateConnectionProfileMegaSession(ctx, s.db, profile.ID, sessionIDEncrypted, masterKeyEncrypted, profile.MegaSessionIDEncrypted) != nil {
			_ = provider.Close()
			crypto.ZeroString(&password)
			clear(megaSession.MasterKey)
			return nil, errFileProviderUnavailable
		}
	}

	return &resolvedFileProfile{
		profile:  profile,
		provider: provider,
		ctx:      providerCtx,
		close: func() {
			_ = provider.Close()
			crypto.ZeroString(&password)
			clear(megaSession.MasterKey)
		},
	}, nil
}
