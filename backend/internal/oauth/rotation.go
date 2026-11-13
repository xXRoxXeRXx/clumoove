package oauth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const (
	rotationCacheTTL     = 24 * time.Hour
	rotationCacheMaxSize = 1024
	maxRotationHops      = 32
	tokenLockTTL         = 30 * time.Second
	tokenLockMaxAttempts = 50
	tokenLockPollDelay   = 100 * time.Millisecond
)

// TokenLocker serializes distributed OAuth token refreshes across API and worker
// processes. *queue.Queue implements this interface.
type TokenLocker interface {
	TryClaimOAuthLock(ctx context.Context, jobType, jobID, role string, ttl time.Duration) (string, bool, error)
	ReleaseOAuthLock(ctx context.Context, jobType, jobID, role, token string) error
}

type keyedLockEntry struct {
	mu   sync.Mutex
	refs int
}

// KeyLockSet provides reference-counted per-key mutual exclusion without
// unbounded map growth across distinct refresh tokens.
type KeyLockSet struct {
	mu    sync.Mutex
	locks map[string]*keyedLockEntry
}

var defaultRotationLocks = &KeyLockSet{}

// DefaultRotationLocks returns the process-wide lock set shared across API handlers
// and embedded workers so concurrent in-process refreshes serialize even when no
// distributed TokenLocker is configured.
func DefaultRotationLocks() *KeyLockSet {
	return defaultRotationLocks
}

// Lock acquires an exclusive mutex for key and returns an idempotent release function.
func (l *KeyLockSet) Lock(key string) func() {
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

type rotationEntry struct {
	rotatedHash           string
	refreshTokenEncrypted string
	expiresAt             time.Time
}

// RotationCache acts as a bounded encrypted in-memory fallback when no durable
// database rotation store is attached (or a stub driver returns driver.ErrSkip).
type RotationCache struct {
	mu      sync.Mutex
	entries map[string]rotationEntry
}

// NewRotationCache allocates an encrypted fallback OAuth rotation cache.
func NewRotationCache() *RotationCache {
	return &RotationCache{entries: make(map[string]rotationEntry)}
}

// Lookup resolves a potentially pre-rotation refresh token transitively through
// any cached chain (A -> B -> C) with cycle protection.
func (c *RotationCache) Lookup(userID, provider, refreshToken, encryptionKey string, now time.Time) (string, bool) {
	if c == nil || refreshToken == "" || encryptionKey == "" {
		return "", false
	}
	key := db.OAuthTokenRotationHash(userID, provider, refreshToken)
	visited := map[string]struct{}{key: {}}
	var latestEncrypted string
	found := false

	c.mu.Lock()
	for hop := 0; hop < maxRotationHops; hop++ {
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

// RecordEncrypted stores an already-encrypted rotation edge in the fallback cache.
func (c *RotationCache) RecordEncrypted(key, rotatedHash, encrypted string, now time.Time) {
	if c == nil || key == "" || encrypted == "" {
		return
	}
	entry := rotationEntry{
		rotatedHash:           rotatedHash,
		refreshTokenEncrypted: encrypted,
		expiresAt:             now.Add(rotationCacheTTL),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]rotationEntry)
	}
	for k, existing := range c.entries {
		if !now.Before(existing.expiresAt) {
			delete(c.entries, k)
		}
	}
	for len(c.entries) >= rotationCacheMaxSize {
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

// LookupRotatedRefreshToken resolves a refresh token along any recorded rotation
// chain in PostgreSQL first, falling back to cache when DB is absent or skipped.
func LookupRotatedRefreshToken(ctx context.Context, database *sql.DB, cache *RotationCache, encryptionKey, userID, provider, refreshToken string, now time.Time) (string, bool) {
	if refreshToken == "" || encryptionKey == "" {
		return "", false
	}
	if database != nil && provider != "" {
		tokenHash := db.OAuthTokenRotationHash(userID, provider, refreshToken)
		_, encrypted, found, err := db.LookupOAuthTokenRotation(ctx, database, userID, provider, tokenHash)
		if err == nil && found {
			plain, decErr := crypto.DecryptWithDomain(encrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
			if decErr == nil && plain != "" {
				return plain, true
			}
		}
	}
	return cache.Lookup(userID, provider, refreshToken, encryptionKey, now)
}

// RecordRotatedRefreshToken persists previous -> rotatedToken aliases encrypted at
// rest in PostgreSQL (returning real DB errors rather than silently ignoring them).
func RecordRotatedRefreshToken(ctx context.Context, database *sql.DB, cache *RotationCache, encryptionKey, userID, provider, rotatedToken string, now time.Time, previousTokens ...string) error {
	if rotatedToken == "" {
		return nil
	}
	filtered := make([]string, 0, len(previousTokens))
	seen := make(map[string]struct{}, len(previousTokens))
	for _, prev := range previousTokens {
		if prev == "" || prev == rotatedToken {
			continue
		}
		if _, dup := seen[prev]; dup {
			continue
		}
		seen[prev] = struct{}{}
		filtered = append(filtered, prev)
	}
	if len(filtered) == 0 {
		return nil
	}
	if encryptionKey == "" {
		return errors.New("encryption key unavailable for oauth rotation persistence")
	}
	encrypted, err := crypto.EncryptWithDomain(rotatedToken, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt rotated oauth refresh token: %w", err)
	}
	rotatedHash := db.OAuthTokenRotationHash(userID, provider, rotatedToken)
	for _, prev := range filtered {
		prevHash := db.OAuthTokenRotationHash(userID, provider, prev)
		if database != nil && userID != "" && provider != "" {
			if err := db.RecordOAuthTokenRotation(ctx, database, userID, provider, prevHash, rotatedHash, encrypted); err != nil && !errors.Is(err, driver.ErrSkip) {
				return fmt.Errorf("persist oauth token rotation: %w", err)
			}
		}
		cache.RecordEncrypted(prevHash, rotatedHash, encrypted, now)
	}
	return nil
}

// RotationRefreshRequest configures a token-locked, chain-aware OAuth refresh.
type RotationRefreshRequest struct {
	DB            *sql.DB
	Cache         *RotationCache
	LocalLocks    *KeyLockSet
	Locker        TokenLocker
	EncryptionKey string
	UserID        string
	Provider      string
	RefreshToken  string
	Now           func() time.Time
	Refresh       func(ctx context.Context, provider, refreshToken string) (*TokenResponse, error)
	ExtraAliases  func() []string

	// OnWait is invoked when distributed lock acquisition contends. Returning
	// adopted=true lets the caller adopt another worker's already-committed token.
	OnWait func(ctx context.Context) (adoptedAccess string, adopted bool)

	// OnLocked is invoked inside the token lock after re-checking rotation history.
	// Returning adopted=true exits without refreshing; returning a non-empty
	// updatedToken != currentToken releases the current lock and re-acquires the
	// lock on updatedToken before refreshing.
	OnLocked func(ctx context.Context, currentToken string) (adoptedAccess string, adopted bool, updatedToken string, err error)

	// Persist is invoked inside the lock after durable rotation recording.
	Persist func(ctx context.Context, targetToken string, resp *TokenResponse) (adoptedAccess string, adopted bool, err error)
}

// RotationRefreshResult holds the outcome of RefreshWithRotationLock.
type RotationRefreshResult struct {
	Token         *TokenResponse
	UsedRefresh   string
	AdoptedAccess string
	Adopted       bool
}

// RefreshWithRotationLock serializes OAuth refresh across API requests and background
// workers on the active token's hash, resolves transitive rotation chains, refuses to
// refresh a token whose lock is not held, and fails loudly if durable recording fails.
func RefreshWithRotationLock(ctx context.Context, req RotationRefreshRequest) (RotationRefreshResult, error) {
	if req.RefreshToken == "" {
		return RotationRefreshResult{}, errors.New("oauth refresh token unavailable")
	}
	nowFn := req.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	locks := req.LocalLocks
	if locks == nil {
		locks = defaultRotationLocks
	}
	refreshFn := req.Refresh
	if refreshFn == nil {
		refreshFn = RefreshToken
	}

	currentToken := req.RefreshToken
	for hop := 0; hop < maxRotationHops; hop++ {
		if rotated, ok := LookupRotatedRefreshToken(ctx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, currentToken, nowFn()); ok && rotated != "" {
			currentToken = rotated
		}

		target := currentToken
		targetKey := db.OAuthTokenRotationHash(req.UserID, req.Provider, target)
		releaseLocal := locks.Lock(targetKey)
		releaseDist, waitAccess, waitAdopted, lockErr := acquireDistributedTokenLock(ctx, req.Locker, targetKey, req.OnWait)
		if lockErr != nil {
			releaseLocal()
			return RotationRefreshResult{}, lockErr
		}
		releaseLocks := func() {
			releaseDist()
			releaseLocal()
		}
		if waitAdopted {
			releaseLocks()
			return RotationRefreshResult{AdoptedAccess: waitAccess, Adopted: true}, nil
		}

		if rotated, ok := LookupRotatedRefreshToken(ctx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, target, nowFn()); ok && rotated != "" && rotated != target {
			currentToken = rotated
			releaseLocks()
			continue
		}

		if req.OnLocked != nil {
			adoptedAccess, adopted, updatedToken, err := req.OnLocked(ctx, target)
			if err != nil {
				releaseLocks()
				return RotationRefreshResult{}, err
			}
			if adopted {
				releaseLocks()
				return RotationRefreshResult{AdoptedAccess: adoptedAccess, Adopted: true}, nil
			}
			if updatedToken != "" && updatedToken != target {
				currentToken = updatedToken
				releaseLocks()
				continue
			}
		}

		tokenResp, err := refreshFn(ctx, req.Provider, target)
		if err != nil || tokenResp == nil || tokenResp.AccessToken == "" {
			releaseLocks()
			if err != nil {
				return RotationRefreshResult{}, err
			}
			return RotationRefreshResult{}, errors.New("oauth refresh returned empty access token")
		}

		now := nowFn()
		if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != target {
			aliases := []string{target, req.RefreshToken}
			if req.ExtraAliases != nil {
				aliases = append(aliases, req.ExtraAliases()...)
			}
			if recErr := RecordRotatedRefreshToken(ctx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, tokenResp.RefreshToken, now, aliases...); recErr != nil {
				releaseLocks()
				return RotationRefreshResult{}, recErr
			}
		}

		if req.Persist != nil {
			adoptedAccess, adopted, err := req.Persist(ctx, target, tokenResp)
			if err != nil {
				releaseLocks()
				return RotationRefreshResult{}, err
			}
			if adopted {
				releaseLocks()
				return RotationRefreshResult{AdoptedAccess: adoptedAccess, Adopted: true}, nil
			}
		}

		releaseLocks()
		return RotationRefreshResult{Token: tokenResp, UsedRefresh: target}, nil
	}

	return RotationRefreshResult{}, errors.New("oauth token rotation limit exceeded")
}

func acquireDistributedTokenLock(ctx context.Context, locker TokenLocker, lockKey string, onWait func(context.Context) (string, bool)) (func(), string, bool, error) {
	if locker == nil {
		return func() {}, "", false, nil
	}
	for attempt := 0; attempt < tokenLockMaxAttempts; attempt++ {
		lockToken, claimed, err := locker.TryClaimOAuthLock(ctx, "oauth-token", lockKey, "refresh", tokenLockTTL)
		if err == nil && claimed {
			return func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = locker.ReleaseOAuthLock(releaseCtx, "oauth-token", lockKey, "refresh", lockToken)
			}, "", false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		timer := time.NewTimer(tokenLockPollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, "", false, ctx.Err()
		case <-timer.C:
		}
		if onWait != nil {
			if adoptedAccess, adopted := onWait(ctx); adopted {
				return func() {}, adoptedAccess, true, nil
			}
		}
	}
	return nil, "", false, errors.New("lock contention: unable to claim OAuth token lock")
}
