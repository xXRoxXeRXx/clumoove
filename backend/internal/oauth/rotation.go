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
	tokenLockWaitTimeout = 20 * time.Second
	tokenLockPollDelay   = 100 * time.Millisecond
)

// ErrRotationPersistence indicates a durable OAuth rotation-history operation failed.
var ErrRotationPersistence = errors.New("oauth rotation persistence failed")

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
func (c *RotationCache) Lookup(userID, provider, refreshToken, encryptionKey string, now time.Time) (string, bool, error) {
	if c == nil || refreshToken == "" || encryptionKey == "" {
		return "", false, nil
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
		return "", false, nil
	}
	plain, err := crypto.DecryptWithDomain(latestEncrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", false, fmt.Errorf("%w: decrypt cached oauth rotation: %w", ErrRotationPersistence, err)
	}
	if plain == "" {
		return "", false, fmt.Errorf("%w: cached oauth rotation is empty", ErrRotationPersistence)
	}
	return plain, true, nil
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
// chain in PostgreSQL first, falling back to cache only when no database is
// configured or a stub driver returns driver.ErrSkip.
func LookupRotatedRefreshToken(ctx context.Context, database *sql.DB, cache *RotationCache, encryptionKey, userID, provider, refreshToken string, now time.Time) (string, bool, error) {
	if refreshToken == "" || encryptionKey == "" {
		return "", false, nil
	}
	if database != nil && provider != "" && userID != "" {
		tokenHash := db.OAuthTokenRotationHash(userID, provider, refreshToken)
		_, encrypted, found, err := db.LookupOAuthTokenRotation(ctx, database, userID, provider, tokenHash)
		if err != nil {
			if !errors.Is(err, driver.ErrSkip) {
				return "", false, fmt.Errorf("%w: lookup oauth token rotation: %w", ErrRotationPersistence, err)
			}
		} else if found {
			plain, decErr := crypto.DecryptWithDomain(encrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
			if decErr != nil {
				return "", false, fmt.Errorf("%w: decrypt oauth token rotation: %w", ErrRotationPersistence, decErr)
			}
			if plain == "" {
				return "", false, fmt.Errorf("%w: decrypted oauth token rotation is empty", ErrRotationPersistence)
			}
			return plain, true, nil
		}
	}
	return cache.Lookup(userID, provider, refreshToken, encryptionKey, now)
}

// RecordRotatedRefreshToken persists previous -> rotatedToken aliases encrypted at
// rest in PostgreSQL (returning real DB errors rather than silently ignoring them).
// The fallback in-memory cache is updated best-effort even when the DB write fails so
// in-process callers do not lose a newly rotated token while the caller still receives
// the persistence error.
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
		return fmt.Errorf("%w: encryption key unavailable", ErrRotationPersistence)
	}
	encrypted, err := crypto.EncryptWithDomain(rotatedToken, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return fmt.Errorf("%w: encrypt rotated oauth refresh token: %w", ErrRotationPersistence, err)
	}
	rotatedHash := db.OAuthTokenRotationHash(userID, provider, rotatedToken)
	var persistErr error
	for _, prev := range filtered {
		prevHash := db.OAuthTokenRotationHash(userID, provider, prev)
		if persistErr == nil && database != nil && userID != "" && provider != "" {
			if err := db.RecordOAuthTokenRotation(ctx, database, userID, provider, prevHash, rotatedHash, encrypted); err != nil && !errors.Is(err, driver.ErrSkip) {
				persistErr = fmt.Errorf("%w: persist oauth token rotation: %w", ErrRotationPersistence, err)
			}
		}
		cache.RecordEncrypted(prevHash, rotatedHash, encrypted, now)
	}
	return persistErr
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
		rotated, ok, lookupErr := LookupRotatedRefreshToken(ctx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, currentToken, nowFn())
		if lookupErr != nil {
			return RotationRefreshResult{}, lookupErr
		}
		if ok && rotated != "" {
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

		rotated, ok, lookupErr = LookupRotatedRefreshToken(ctx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, target, nowFn())
		if lookupErr != nil {
			releaseLocks()
			return RotationRefreshResult{}, lookupErr
		}
		if ok && rotated != "" && rotated != target {
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

		// Once the provider has consumed target and issued a replacement token,
		// persist both history and caller-owned rows using a detached deadline so
		// caller cancellation mid-exchange cannot drop the new credential.
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		now := nowFn()
		var recErr error
		if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != target {
			aliases := []string{target, req.RefreshToken}
			if req.ExtraAliases != nil {
				aliases = append(aliases, req.ExtraAliases()...)
			}
			recErr = RecordRotatedRefreshToken(persistCtx, req.DB, req.Cache, req.EncryptionKey, req.UserID, req.Provider, tokenResp.RefreshToken, now, aliases...)
		}

		if req.Persist != nil {
			adoptedAccess, adopted, persistErr := req.Persist(persistCtx, target, tokenResp)
			cancelPersist()
			if persistErr != nil {
				releaseLocks()
				if recErr != nil {
					return RotationRefreshResult{}, errors.Join(recErr, persistErr)
				}
				return RotationRefreshResult{}, persistErr
			}
			if recErr != nil {
				releaseLocks()
				return RotationRefreshResult{}, recErr
			}
			if adopted {
				releaseLocks()
				return RotationRefreshResult{AdoptedAccess: adoptedAccess, Adopted: true}, nil
			}
		} else {
			cancelPersist()
			if recErr != nil {
				releaseLocks()
				return RotationRefreshResult{}, recErr
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
	waitDeadline := time.Now().Add(tokenLockWaitTimeout)
	for {
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
		remaining := time.Until(waitDeadline)
		if remaining <= 0 {
			return nil, "", false, errors.New("lock contention: unable to claim OAuth token lock")
		}
		sleep := tokenLockPollDelay
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
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
}
