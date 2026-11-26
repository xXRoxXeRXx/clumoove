package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

// defaultAccessTokenLifetime applies when a provider omits expires_in.
const defaultAccessTokenLifetime = time.Hour

// Failure stages of persisting a rotated pair. Refresh wraps them so callers can
// tell an encryption failure from a storage failure without parsing messages.
var (
	ErrRotatedTokenEncrypt = errors.New("encrypt rotated OAuth tokens")
	ErrRotatedTokenPersist = errors.New("persist rotated OAuth tokens")
)

// RefresherConfig is the infrastructure every OAuth refresh shares. It is wired
// once per component, so call sites describe only what differs per owner.
type RefresherConfig struct {
	DB          *sql.DB
	Rotations   db.OAuthRotationStore
	BackupStore RotationBackupStore
	Cache       *RotationCache
	LocalLocks  *KeyLockSet
	Locker      TokenLocker
	// EncryptionKey is ENCRYPTION_SECRET_KEY. It encrypts rotated tokens before
	// they reach a sink and nothing else; token signing keys never come here.
	EncryptionKey string
	// Now is the clock for rotation history and token expiry; defaults to time.Now.
	Now func() time.Time
}

// Refresher runs token-locked, chain-aware OAuth refreshes against shared
// infrastructure. See RefreshWithRotationLock for the locking guarantees.
type Refresher struct {
	cfg RefresherConfig
}

// NewRefresher returns a Refresher bound to cfg. A Locker holding a nil pointer
// (for example an unconfigured *queue.Queue) is treated as no locker, so callers
// can pass an optional dependency straight through.
func NewRefresher(cfg RefresherConfig) *Refresher {
	if v := reflect.ValueOf(cfg.Locker); v.Kind() == reflect.Pointer && v.IsNil() {
		cfg.Locker = nil
	}
	if cfg.Rotations == nil && cfg.DB != nil {
		cfg.Rotations = db.NewOAuthRotationStore(cfg.DB)
	}
	return &Refresher{cfg: cfg}
}

// OwnerRefresh describes one refresh: whose token it is, how to reach the
// provider, and where the rotated pair goes.
type OwnerRefresh struct {
	UserID   string
	Provider string
	// RefreshToken is the owner's current plaintext refresh token.
	RefreshToken string
	// Label names the owner in persistence errors, for example "migration source".
	Label string

	Refresh      func(ctx context.Context, provider, refreshToken string) (*TokenResponse, error)
	ExtraAliases func() []string

	// OnWait and OnLocked have the semantics documented on RotationRefreshRequest.
	OnWait   func(ctx context.Context) (adoptedAccess string, adopted bool)
	OnLocked func(ctx context.Context, currentToken string) (adoptedAccess string, adopted bool, updatedToken string, err error)

	// Sink receives the rotated pair, encrypted, once the provider issued it.
	// A nil Sink only records rotation history.
	Sink db.OAuthTokenSink
	// ExpectedRefresh returns the encrypted refresh token the owner's row must
	// still hold. It is read at persist time because OnLocked may advance it.
	ExpectedRefresh func() string
	// SkipPersist, when it returns true, leaves the sink untouched. It is
	// consulted at persist time, after OnLocked ran.
	SkipPersist func() bool
	// OnPersisted observes the pair the sink stored, for refreshing in-memory copies.
	OnPersisted func(tokens db.EncryptedOAuthTokens)
	// AdoptSuperseded is called when the sink reports the row was rotated by
	// someone else. Returning ok adopts that winner's access token (encrypted or
	// plaintext, whichever the caller works in); otherwise the refresh fails with
	// db.ErrOAuthTokenConflict.
	AdoptSuperseded func(ctx context.Context) (adoptedAccess string, ok bool)
}

// Refresh refreshes the owner's token under the rotation lock and persists the
// result through its sink.
func (r *Refresher) Refresh(ctx context.Context, req OwnerRefresh) (RotationRefreshResult, error) {
	rotation := RotationRefreshRequest{
		DB:            r.cfg.DB,
		Rotations:     r.cfg.Rotations,
		BackupStore:   r.cfg.BackupStore,
		Cache:         r.cfg.Cache,
		LocalLocks:    r.cfg.LocalLocks,
		Locker:        r.cfg.Locker,
		EncryptionKey: r.cfg.EncryptionKey,
		UserID:        req.UserID,
		Provider:      req.Provider,
		RefreshToken:  req.RefreshToken,
		Now:           r.now,
		Refresh:       req.Refresh,
		ExtraAliases:  req.ExtraAliases,
		OnWait:        req.OnWait,
		OnLocked:      req.OnLocked,
	}
	if req.Sink != nil {
		rotation.Persist = func(persistCtx context.Context, _ string, resp *TokenResponse) (string, bool, error) {
			return r.persistRotation(persistCtx, req, resp)
		}
	}
	return RefreshWithRotationLock(ctx, rotation)
}

func (r *Refresher) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now()
	}
	return time.Now()
}

func (r *Refresher) persistRotation(ctx context.Context, req OwnerRefresh, resp *TokenResponse) (string, bool, error) {
	if req.SkipPersist != nil && req.SkipPersist() {
		return "", false, nil
	}
	access, err := crypto.EncryptWithDomain(resp.AccessToken, r.cfg.EncryptionKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", false, fmt.Errorf("%w: %s access token: %w", ErrRotatedTokenEncrypt, req.Label, err)
	}
	refresh, err := crypto.EncryptWithDomain(resp.RefreshToken, r.cfg.EncryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", false, fmt.Errorf("%w: %s refresh token: %w", ErrRotatedTokenEncrypt, req.Label, err)
	}
	lifetime := time.Duration(resp.ExpiresIn) * time.Second
	if resp.ExpiresIn <= 0 {
		lifetime = defaultAccessTokenLifetime
	}
	tokens := db.EncryptedOAuthTokens{Access: access, Refresh: refresh, ExpiresAt: r.now().Add(lifetime)}

	var expected string
	if req.ExpectedRefresh != nil {
		expected = req.ExpectedRefresh()
	}
	if err := req.Sink.Persist(ctx, tokens, expected); err != nil {
		if errors.Is(err, db.ErrOAuthTokenConflict) && req.AdoptSuperseded != nil {
			if winner, ok := req.AdoptSuperseded(ctx); ok {
				return winner, true, nil
			}
		}
		return "", false, fmt.Errorf("%w: %s: %w", ErrRotatedTokenPersist, req.Label, err)
	}
	if req.OnPersisted != nil {
		req.OnPersisted(tokens)
	}
	return "", false, nil
}

// AdoptDecryptedWinner builds an AdoptSuperseded hook for owners whose callers
// work with plaintext access tokens: it loads the encrypted access token the
// winning rotation stored and decrypts it. Any load or decrypt failure declines
// adoption, which surfaces the superseded error instead.
func (r *Refresher) AdoptDecryptedWinner(load func(ctx context.Context) (accessEncrypted string, err error)) func(ctx context.Context) (string, bool) {
	return func(ctx context.Context) (string, bool) {
		accessEncrypted, err := load(ctx)
		if err != nil {
			return "", false
		}
		access, err := crypto.DecryptWithDomain(accessEncrypted, r.cfg.EncryptionKey, crypto.DomainOAuthAccessToken)
		return access, err == nil
	}
}
