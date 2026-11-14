package oauth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const (
	rotationCacheTTL       = 24 * time.Hour
	// Fallback entries stay in Redis without TTL expiry until PostgreSQL repair
	// explicitly removes them, preventing dormant tokens from losing history.
	rotationBackupTTL      = 0
	rotationCacheMaxSize   = 1024
	maxRotationHops        = 32
	tokenPreRefreshTimeout = 5 * time.Second
	tokenRefreshTimeout    = 15 * time.Second
	tokenPersistTimeout    = 10 * time.Second
	tokenHistoryDBTimeout  = 4 * time.Second
	tokenBackupTimeout     = 2 * time.Second
	tokenRepairTimeout      = 2 * time.Second
	defaultRepairBatchLimit = 50
	// Base backoff exceeds the 5-minute daemon sweep interval; repeated failures
	// back off exponentially up to 24h while untouched entries stay prioritized.
	repairFailureCooldown    = 15 * time.Minute
	repairFailureMaxCooldown = 24 * time.Hour
	repairCooldownMaxSize    = 4096
	tokenLockSafetyMargin   = 3 * time.Second
	tokenLockTTL           = 40 * time.Second
	tokenLockWaitTimeout   = 35 * time.Second
	tokenLockPollDelay     = 100 * time.Millisecond
)

// ErrRotationPersistence indicates a durable OAuth rotation-history operation failed.
var ErrRotationPersistence = errors.New("oauth rotation persistence failed")

// TokenLocker serializes distributed OAuth token refreshes across API and worker
// processes. *queue.Queue implements this interface.
type TokenLocker interface {
	TryClaimOAuthLock(ctx context.Context, jobType, jobID, role string, ttl time.Duration) (string, bool, error)
	ReleaseOAuthLock(ctx context.Context, jobType, jobID, role, token string) error
}

// RotationBackupStore optionally persists AES-256-GCM encrypted rotation edges
// in a shared store (such as Redis via *queue.Queue) so a failed PostgreSQL
// write does not lose replacement refresh tokens across instances or restarts.
type RotationBackupStore interface {
	RecordOAuthRotationBackup(ctx context.Context, userID, provider, prevHash, rotatedHash, encryptedToken string, ttl time.Duration) error
	LookupOAuthRotationBackup(ctx context.Context, userID, provider, tokenHash string) (rotatedHash, encryptedToken string, found bool, err error)
}

// RotationBackupRepairStore extends RotationBackupStore with enumeration and
// conditional deletion so dormant backup-only mappings can be proactively repaired
// into PostgreSQL without removing newer concurrent rotations.
type RotationBackupRepairStore interface {
	RotationBackupStore
	VisitOAuthRotationBackups(ctx context.Context, visit func(userID, provider, prevHash, rotatedHash, encryptedToken string) error) error
	DeleteOAuthRotationBackup(ctx context.Context, userID, provider, prevHash, expectedRotatedHash, expectedEncrypted string) (bool, error)
}

// OAuthRotationScanResumer may be implemented by RotationBackupRepairStore
// stores (such as Queue) that persist scan progress internally across
// VisitOAuthRotationBackups calls.
type OAuthRotationScanResumer interface {
	ResumesOAuthRotationScan() bool
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

func (c *RotationCache) lookupOne(userID, provider, refreshToken, encryptionKey string, now time.Time) (string, bool, error) {
	if c == nil || refreshToken == "" || encryptionKey == "" {
		return "", false, nil
	}
	key := db.OAuthTokenRotationHash(userID, provider, refreshToken)
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return "", false, nil
	}
	plain, err := crypto.DecryptWithDomain(entry.refreshTokenEncrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", false, fmt.Errorf("%w: decrypt cached oauth rotation: %w", ErrRotationPersistence, err)
	}
	if plain == "" {
		return "", false, fmt.Errorf("%w: cached oauth rotation is empty", ErrRotationPersistence)
	}
	return plain, true, nil
}

// LookupRotatedRefreshToken resolves a refresh token along any recorded rotation
// chain in PostgreSQL and the process cache without ignoring database errors.
func LookupRotatedRefreshToken(ctx context.Context, database *sql.DB, cache *RotationCache, encryptionKey, userID, provider, refreshToken string, now time.Time) (string, bool, error) {
	return LookupRotatedRefreshTokenWithStore(ctx, database, nil, cache, encryptionKey, userID, provider, refreshToken, now)
}

// LookupRotatedRefreshTokenWithStore resolves a refresh token transitively across
// PostgreSQL, an optional shared RotationBackupStore (e.g. Redis), and the
// process cache so partial chains in one store continue seamlessly through newer
// hops recorded in another.
func LookupRotatedRefreshTokenWithStore(ctx context.Context, database *sql.DB, backup RotationBackupStore, cache *RotationCache, encryptionKey, userID, provider, refreshToken string, now time.Time) (string, bool, error) {
	if refreshToken == "" || encryptionKey == "" {
		return "", false, nil
	}
	current := refreshToken
	resolved := false
	seenHashes := make(map[string]struct{}, 4)

	for hop := 0; hop < maxRotationHops; hop++ {
		curHash := db.OAuthTokenRotationHash(userID, provider, current)
		if _, seen := seenHashes[curHash]; seen {
			break
		}
		seenHashes[curHash] = struct{}{}

		nextToken, found, err := lookupOneRotationHop(ctx, database, backup, cache, encryptionKey, userID, provider, current, curHash, now)
		if err != nil {
			return "", false, err
		}
		if !found || nextToken == "" || nextToken == current {
			break
		}
		current = nextToken
		resolved = true
	}
	if !resolved {
		return "", false, nil
	}
	return current, true, nil
}

func lookupOneDatabaseRotationHop(ctx context.Context, database *sql.DB, encryptionKey, userID, provider, tokenHash string) (string, string, bool, error) {
	rotatedHash, encrypted, found, err := db.LookupOAuthTokenRotation(ctx, database, userID, provider, tokenHash)
	if err != nil {
		if errors.Is(err, driver.ErrSkip) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("%w: lookup oauth token rotation: %w", ErrRotationPersistence, err)
	}
	if !found {
		return "", "", false, nil
	}
	plain, decErr := crypto.DecryptWithDomain(encrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
	if decErr != nil {
		return "", "", false, fmt.Errorf("%w: decrypt oauth token rotation: %w", ErrRotationPersistence, decErr)
	}
	if plain == "" {
		return "", "", false, fmt.Errorf("%w: decrypted oauth token rotation is empty", ErrRotationPersistence)
	}
	return rotatedHash, plain, true, nil
}

func deleteBackupIfRepairStore(ctx context.Context, backup RotationBackupStore, userID, provider, tokenHash, rotatedHash, encrypted string) {
	rs, ok := backup.(RotationBackupRepairStore)
	if !ok {
		return
	}
	delCtx, cancelDel := context.WithTimeout(ctx, tokenRepairTimeout)
	_, _ = rs.DeleteOAuthRotationBackup(delCtx, userID, provider, tokenHash, rotatedHash, encrypted)
	cancelDel()
}

// isBackupAccountedInDatabase reports whether PostgreSQL's chain starting at
// prevHash already reaches or converges with backupRotatedHash. An unrelated
// successor elsewhere in PostgreSQL (e.g. A -> B and C -> D when B -> C was
// never persisted) returns false so backup A -> C is neither discarded nor bypassed.
func isBackupAccountedInDatabase(ctx context.Context, database *sql.DB, userID, provider, prevHash, backupRotatedHash string) (bool, error) {
	dbLatestHash, _, prevVisited, dbFound, dbErr := db.LookupOAuthTokenRotationChain(ctx, database, userID, provider, prevHash)
	if dbErr != nil {
		if errors.Is(dbErr, driver.ErrSkip) {
			return false, nil
		}
		return false, fmt.Errorf("%w: lookup oauth token rotation: %w", ErrRotationPersistence, dbErr)
	}
	if !dbFound {
		return false, nil
	}
	if _, seen := prevVisited[backupRotatedHash]; seen || dbLatestHash == backupRotatedHash {
		return true, nil
	}
	targetLatestHash, _, targetVisited, targetFound, sErr := db.LookupOAuthTokenRotationChain(ctx, database, userID, provider, backupRotatedHash)
	if sErr != nil {
		if errors.Is(sErr, driver.ErrSkip) {
			return false, nil
		}
		return false, fmt.Errorf("%w: lookup oauth token rotation successor: %w", ErrRotationPersistence, sErr)
	}
	if !targetFound {
		return false, nil
	}
	if targetLatestHash == dbLatestHash {
		return true, nil
	}
	for h := range targetVisited {
		if _, intersects := prevVisited[h]; intersects && h != prevHash {
			return true, nil
		}
	}
	return false, nil
}

func lookupOneRotationHop(ctx context.Context, database *sql.DB, backup RotationBackupStore, cache *RotationCache, encryptionKey, userID, provider, refreshToken, tokenHash string, now time.Time) (string, bool, error) {
	if database != nil && provider != "" && userID != "" {
		dbHash, dbPlain, found, err := lookupOneDatabaseRotationHop(ctx, database, encryptionKey, userID, provider, tokenHash)
		if err != nil {
			return "", false, err
		}
		if found {
			// If backup holds a target not yet accounted for by PostgreSQL's chain from
			// tokenHash (e.g. DB holds A -> B while backup holds A -> C), prefer C.
			if backup != nil {
				bHash, bEnc, bFound, bErr := backup.LookupOAuthRotationBackup(ctx, userID, provider, tokenHash)
				if bErr != nil {
					return "", false, fmt.Errorf("%w: lookup oauth rotation backup: %w", ErrRotationPersistence, bErr)
				}
				if bFound {
					bPlain, decErr := crypto.DecryptWithDomain(bEnc, encryptionKey, crypto.DomainOAuthRefreshToken)
					if decErr != nil {
						return "", false, fmt.Errorf("%w: decrypt oauth rotation backup: %w", ErrRotationPersistence, decErr)
					}
					if bPlain == "" {
						return "", false, fmt.Errorf("%w: decrypted oauth rotation backup is empty", ErrRotationPersistence)
					}
					if bHash == "" {
						bHash = db.OAuthTokenRotationHash(userID, provider, bPlain)
					}
					accounted := bHash == dbHash
					if !accounted {
						var aErr error
						accounted, aErr = isBackupAccountedInDatabase(ctx, database, userID, provider, tokenHash, bHash)
						if aErr != nil {
							return "", false, aErr
						}
					}
					if !accounted {
						return bPlain, true, nil
					}
					deleteBackupIfRepairStore(ctx, backup, userID, provider, tokenHash, bHash, bEnc)
				}
			}
			return dbPlain, true, nil
		}
	}
	if backup != nil && provider != "" && userID != "" {
		rotatedHash, encrypted, found, err := backup.LookupOAuthRotationBackup(ctx, userID, provider, tokenHash)
		if err != nil {
			return "", false, fmt.Errorf("%w: lookup oauth rotation backup: %w", ErrRotationPersistence, err)
		}
		if found {
			plain, decErr := crypto.DecryptWithDomain(encrypted, encryptionKey, crypto.DomainOAuthRefreshToken)
			if decErr != nil {
				return "", false, fmt.Errorf("%w: decrypt oauth rotation backup: %w", ErrRotationPersistence, decErr)
			}
			if plain == "" {
				return "", false, fmt.Errorf("%w: decrypted oauth rotation backup is empty", ErrRotationPersistence)
			}
			if database != nil {
				if rotatedHash == "" {
					rotatedHash = db.OAuthTokenRotationHash(userID, provider, plain)
				}
				repairCtx, cancelRepair := context.WithTimeout(ctx, tokenRepairTimeout)
				inserted, repairErr := db.RepairOAuthTokenRotation(repairCtx, database, userID, provider, tokenHash, rotatedHash, encrypted)
				cancelRepair()
				if repairErr != nil && !errors.Is(repairErr, driver.ErrSkip) {
					return "", false, fmt.Errorf("%w: repair oauth token rotation: %w", ErrRotationPersistence, repairErr)
				}
				if repairErr == nil {
					safeToDelete := inserted
					if !inserted {
						safeToDelete, err = isBackupAccountedInDatabase(ctx, database, userID, provider, tokenHash, rotatedHash)
						if err != nil {
							return "", false, err
						}
					}
					if safeToDelete {
						deleteBackupIfRepairStore(ctx, backup, userID, provider, tokenHash, rotatedHash, encrypted)
					}
					if !inserted && safeToDelete {
						if _, dbPlain, dbFound, dbErr := lookupOneDatabaseRotationHop(ctx, database, encryptionKey, userID, provider, tokenHash); dbErr != nil {
							return "", false, dbErr
						} else if dbFound {
							return dbPlain, true, nil
						}
					}
				}
			}
			return plain, true, nil
		}
		// Re-check PostgreSQL after a backup miss so a concurrent repair committing
		// to PostgreSQL and deleting the Redis entry between reads is never missed.
		if database != nil {
			_, plain, found, err := lookupOneDatabaseRotationHop(ctx, database, encryptionKey, userID, provider, tokenHash)
			if err != nil {
				return "", false, err
			}
			if found {
				return plain, true, nil
			}
		}
	}
	return cache.lookupOne(userID, provider, refreshToken, encryptionKey, now)
}

// RecordRotatedRefreshToken persists previous -> rotatedToken aliases encrypted at
// rest in PostgreSQL (returning real DB errors rather than silently ignoring them).
// The fallback in-memory cache is updated best-effort even when the DB write fails so
// in-process callers do not lose a newly rotated token while the caller still receives
// the persistence error.
func RecordRotatedRefreshToken(ctx context.Context, database *sql.DB, cache *RotationCache, encryptionKey, userID, provider, rotatedToken string, now time.Time, previousTokens ...string) error {
	return RecordRotatedRefreshTokenWithStore(ctx, database, nil, cache, encryptionKey, userID, provider, rotatedToken, now, previousTokens...)
}

// RecordRotatedRefreshTokenWithStore persists previous -> rotatedToken aliases
// encrypted at rest in PostgreSQL and, when PostgreSQL recording fails or is
// unavailable, mirrors them into the shared RotationBackupStore (e.g. Redis) and
// local cache so other instances or restarts can still resolve the rotated token.
func RecordRotatedRefreshTokenWithStore(ctx context.Context, database *sql.DB, backup RotationBackupStore, cache *RotationCache, encryptionKey, userID, provider, rotatedToken string, now time.Time, previousTokens ...string) error {
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
	dbSkippedOrMissing := database == nil || userID == "" || provider == ""
	if !dbSkippedOrMissing {
		dbCtx, cancelDB := context.WithTimeout(ctx, tokenHistoryDBTimeout)
		for _, prev := range filtered {
			prevHash := db.OAuthTokenRotationHash(userID, provider, prev)
			if persistErr == nil {
				if err := db.RecordOAuthTokenRotation(dbCtx, database, userID, provider, prevHash, rotatedHash, encrypted); err != nil {
					if errors.Is(err, driver.ErrSkip) {
						dbSkippedOrMissing = true
					} else {
						persistErr = fmt.Errorf("%w: persist oauth token rotation: %w", ErrRotationPersistence, err)
					}
				}
			}
			cache.RecordEncrypted(prevHash, rotatedHash, encrypted, now)
		}
		cancelDB()
	} else {
		for _, prev := range filtered {
			prevHash := db.OAuthTokenRotationHash(userID, provider, prev)
			cache.RecordEncrypted(prevHash, rotatedHash, encrypted, now)
		}
	}
	if (persistErr != nil || dbSkippedOrMissing) && backup != nil && userID != "" && provider != "" {
		// Use an independent short deadline so a timed-out PostgreSQL write cannot
		// leave the fallback store with an already-expired context.
		backupCtx, cancelBackup := context.WithTimeout(context.WithoutCancel(ctx), tokenBackupTimeout)
		var backupErr error
		for _, prev := range filtered {
			prevHash := db.OAuthTokenRotationHash(userID, provider, prev)
			if berr := backup.RecordOAuthRotationBackup(backupCtx, userID, provider, prevHash, rotatedHash, encrypted, rotationBackupTTL); berr != nil && backupErr == nil {
				backupErr = fmt.Errorf("%w: backup oauth rotation: %w", ErrRotationPersistence, berr)
			}
		}
		cancelBackup()
		if backupErr != nil {
			persistErr = errors.Join(persistErr, backupErr)
		}
	}
	return persistErr
}

var errRepairBatchLimitReached = errors.New("repair batch limit reached")

type repairFailureEntry struct {
	failures    int
	lastAttempt time.Time
	nextRetry   time.Time
}

// RotationRepairState tracks per-entry failure backoff, attempt counts, and
// a round-robin resume anchor across repair sweeps so persistently failing keys
// cannot starve untouched or less-failing entries even when backlogs exceed
// repairCooldownMaxSize.
type RotationRepairState struct {
	mu           sync.Mutex
	failures     map[string]repairFailureEntry
	resumeAnchor string
}

// NewRotationRepairState allocates a bounded fair-scheduling tracker for
// rotation backup sweeps.
func NewRotationRepairState() *RotationRepairState {
	return &RotationRepairState{failures: make(map[string]repairFailureEntry)}
}

var defaultRepairState = NewRotationRepairState()

func (s *RotationRepairState) getResumeAnchor() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumeAnchor
}

func (s *RotationRepairState) setResumeAnchor(anchor string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.resumeAnchor = anchor
	s.mu.Unlock()
}

func (s *RotationRepairState) key(userID, provider, prevHash string) string {
	return userID + "\x00" + provider + "\x00" + prevHash
}

func (s *RotationRepairState) failureState(key string) (repairFailureEntry, bool) {
	if s == nil {
		return repairFailureEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.failures[key]
	return entry, ok
}

func (s *RotationRepairState) markFailed(key string, now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failures == nil {
		s.failures = make(map[string]repairFailureEntry)
	}
	if _, exists := s.failures[key]; !exists && len(s.failures) >= repairCooldownMaxSize {
		var evictKey string
		var evictEntry repairFailureEntry
		for k, entry := range s.failures {
			if evictKey == "" || entry.lastAttempt.Before(evictEntry.lastAttempt) {
				evictKey = k
				evictEntry = entry
			}
		}
		if evictKey != "" {
			delete(s.failures, evictKey)
		}
	}
	prev := s.failures[key]
	failures := prev.failures + 1
	backoff := repairFailureCooldown
	for i := 1; i < failures && backoff < repairFailureMaxCooldown; i++ {
		backoff *= 2
		if backoff > repairFailureMaxCooldown {
			backoff = repairFailureMaxCooldown
		}
	}
	s.failures[key] = repairFailureEntry{
		failures:    failures,
		lastAttempt: now,
		nextRetry:   now.Add(backoff),
	}
}

func (s *RotationRepairState) clear(key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()
}

// RepairRotationBackups reconciles up to defaultRepairBatchLimit pending rotation
// mappings from the shared backup store back into PostgreSQL and conditionally
// removes each backup entry once durable without overwriting newer rotations.
func RepairRotationBackups(ctx context.Context, database *sql.DB, store RotationBackupRepairStore) (int, error) {
	return defaultRepairState.RepairWithLimit(ctx, database, store, defaultRepairBatchLimit, time.Now())
}

// RepairRotationBackupsWithLimit reconciles at most limit pending rotation
// mappings (unlimited when limit <= 0) from store into PostgreSQL.
func RepairRotationBackupsWithLimit(ctx context.Context, database *sql.DB, store RotationBackupRepairStore, limit int) (int, error) {
	return defaultRepairState.RepairWithLimit(ctx, database, store, limit, time.Now())
}

type pendingBackupRecord struct {
	key, userID, provider, prevHash, rotatedHash, encrypted string
	failures                                                int
	lastAttempt                                             time.Time
}

// RepairWithLimit reconciles at most limit pending mappings per pass, giving
// strict priority to untouched entries ahead of past failures and ordering
// retries fairly by fewest failures and oldest attempt so arbitrarily large
// failing backlogs cannot starve later valid entries.
func (s *RotationRepairState) RepairWithLimit(ctx context.Context, database *sql.DB, store RotationBackupRepairStore, limit int, now time.Time) (int, error) {
	if database == nil || store == nil {
		return 0, nil
	}
	resumer, hasResumer := store.(OAuthRotationScanResumer)
	resumesScan := hasResumer && resumer.ResumesOAuthRotationScan()
	var anchor string
	if !resumesScan {
		anchor = s.getResumeAnchor()
	}
	passedAnchor := anchor == ""
	sawAnchor := anchor == ""
	scanCtx := ctx
	var cancelScan context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		// Reserve half of the caller's deadline for repairing collected entries so
		// scanning a large failed-retry backlog cannot exhaust the budget before repairs run.
		if remaining := time.Until(deadline); remaining > 0 {
			scanCtx, cancelScan = context.WithTimeout(ctx, remaining/2)
		}
	}
	if cancelScan != nil {
		defer cancelScan()
	}
	var fresh, wrappedFresh, dueRetries, cooled []pendingBackupRecord
	visitErr := store.VisitOAuthRotationBackups(scanCtx, func(userID, provider, prevHash, rotatedHash, encryptedToken string) error {
		k := s.key(userID, provider, prevHash)
		st, hasFailed := s.failureState(k)
		rec := pendingBackupRecord{
			key:         k,
			userID:      userID,
			provider:    provider,
			prevHash:    prevHash,
			rotatedHash: rotatedHash,
			encrypted:   encryptedToken,
			failures:    st.failures,
			lastAttempt: st.lastAttempt,
		}
		if !passedAnchor {
			if k == anchor {
				passedAnchor = true
				sawAnchor = true
			}
			if !hasFailed || st.failures == 0 {
				if limit <= 0 || len(wrappedFresh) < limit {
					wrappedFresh = append(wrappedFresh, rec)
				}
			} else if !now.Before(st.nextRetry) {
				dueRetries = append(dueRetries, rec)
			} else {
				cooled = append(cooled, rec)
			}
			return nil
		}
		if !hasFailed || st.failures == 0 {
			fresh = append(fresh, rec)
			if limit > 0 && len(fresh) >= limit {
				return errRepairBatchLimitReached
			}
			return nil
		}
		if !now.Before(st.nextRetry) {
			dueRetries = append(dueRetries, rec)
			return nil
		}
		cooled = append(cooled, rec)
		return nil
	})
	hasCandidates := len(fresh) > 0 || len(wrappedFresh) > 0 || len(dueRetries) > 0 || len(cooled) > 0
	var deferredScanErr error
	if visitErr != nil && !errors.Is(visitErr, errRepairBatchLimitReached) {
		scanBudgetElapsed := cancelScan != nil && errors.Is(scanCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		if !resumesScan && !sawAnchor {
			// Clear an unreachable or deleted anchor so the next sweep does not
			// repeat a full-keyspace traversal and hit the same scan error/timeout.
			s.setResumeAnchor("")
		}
		if ctx.Err() != nil || !hasCandidates {
			return 0, fmt.Errorf("%w: scan oauth rotation backups: %w", ErrRotationPersistence, visitErr)
		}
		if !scanBudgetElapsed {
			deferredScanErr = fmt.Errorf("%w: scan oauth rotation backups: %w", ErrRotationPersistence, visitErr)
		}
	}
	sortFair := func(list []pendingBackupRecord) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].failures != list[j].failures {
				return list[i].failures < list[j].failures
			}
			return list[i].lastAttempt.Before(list[j].lastAttempt)
		})
	}
	pending := fresh
	if limit <= 0 || len(pending) < limit {
		for _, rec := range wrappedFresh {
			pending = append(pending, rec)
			if limit > 0 && len(pending) >= limit {
				break
			}
		}
	}
	if limit <= 0 || len(pending) < limit {
		sortFair(dueRetries)
		for _, rec := range dueRetries {
			pending = append(pending, rec)
			if limit > 0 && len(pending) >= limit {
				break
			}
		}
	}
	if len(pending) == 0 && len(cooled) > 0 {
		sortFair(cooled)
		for _, rec := range cooled {
			pending = append(pending, rec)
			if limit > 0 && len(pending) >= limit {
				break
			}
		}
	}
	repaired := 0
	var repairErrs []error
	var lastSurvivingAnchor string
	anchorDeleted := false
	for _, rec := range pending {
		if ctx.Err() != nil {
			repairErrs = append(repairErrs, fmt.Errorf("%w: repair oauth token rotation: %w", ErrRotationPersistence, ctx.Err()))
			break
		}
		cooldownKey := s.key(rec.userID, rec.provider, rec.prevHash)
		wasRepaired, survived, rerr := s.repairOnePendingBackup(ctx, database, store, rec, cooldownKey, now)
		if wasRepaired {
			repaired++
		}
		if rerr != nil {
			repairErrs = append(repairErrs, rerr)
		}
		if survived {
			lastSurvivingAnchor = cooldownKey
		} else if !resumesScan && cooldownKey == anchor {
			anchorDeleted = true
		}
	}
	if !resumesScan {
		switch {
		case lastSurvivingAnchor != "":
			s.setResumeAnchor(lastSurvivingAnchor)
		case sawAnchor && !anchorDeleted:
			s.setResumeAnchor(anchor)
		default:
			s.setResumeAnchor("")
		}
	}
	if deferredScanErr != nil {
		repairErrs = append(repairErrs, deferredScanErr)
	}
	return repaired, errors.Join(repairErrs...)
}

func (s *RotationRepairState) repairOnePendingBackup(ctx context.Context, database *sql.DB, store RotationBackupRepairStore, rec pendingBackupRecord, cooldownKey string, now time.Time) (repaired bool, survived bool, err error) {
	// Refresh the backup entry immediately before writing to avoid repairing
	// an older snapshot after a concurrent rotation replaced the backup key.
	curRotated, curEnc, stillPresent, lerr := store.LookupOAuthRotationBackup(ctx, rec.userID, rec.provider, rec.prevHash)
	if lerr != nil {
		s.markFailed(cooldownKey, now)
		return false, true, fmt.Errorf("%w: recheck oauth rotation backup: %w", ErrRotationPersistence, lerr)
	}
	if !stillPresent {
		s.clear(cooldownKey)
		return false, false, nil
	}
	rec.rotatedHash = curRotated
	rec.encrypted = curEnc

	inserted, rerr := db.RepairOAuthTokenRotation(ctx, database, rec.userID, rec.provider, rec.prevHash, rec.rotatedHash, rec.encrypted)
	if rerr != nil {
		if errors.Is(rerr, driver.ErrSkip) {
			return false, true, nil
		}
		s.markFailed(cooldownKey, now)
		return false, true, fmt.Errorf("%w: repair oauth token rotation: %w", ErrRotationPersistence, rerr)
	}
	safeToDelete := inserted
	if !inserted {
		var cerr error
		safeToDelete, cerr = isBackupAccountedInDatabase(ctx, database, rec.userID, rec.provider, rec.prevHash, rec.rotatedHash)
		if cerr != nil {
			s.markFailed(cooldownKey, now)
			return false, true, cerr
		}
	}
	if !safeToDelete {
		// PostgreSQL holds a diverging mapping while this backup target is not yet
		// superseded there; keep the backup intact and defer it so later entries advance.
		s.markFailed(cooldownKey, now)
		return false, true, nil
	}
	deleted, derr := store.DeleteOAuthRotationBackup(ctx, rec.userID, rec.provider, rec.prevHash, rec.rotatedHash, rec.encrypted)
	if derr != nil {
		s.markFailed(cooldownKey, now)
		return false, true, fmt.Errorf("%w: cleanup oauth rotation backup: %w", ErrRotationPersistence, derr)
	}
	s.clear(cooldownKey)
	return deleted, false, nil
}

// RotationRefreshRequest configures a token-locked, chain-aware OAuth refresh.
type RotationRefreshRequest struct {
	DB            *sql.DB
	BackupStore   RotationBackupStore
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

	backupStore := req.BackupStore
	if backupStore == nil {
		if bs, ok := req.Locker.(RotationBackupStore); ok {
			backupStore = bs
		}
	}

	currentToken := req.RefreshToken
	for hop := 0; hop < maxRotationHops; hop++ {
		rotated, ok, lookupErr := LookupRotatedRefreshTokenWithStore(ctx, req.DB, backupStore, req.Cache, req.EncryptionKey, req.UserID, req.Provider, currentToken, nowFn())
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
		lockExpiry := time.Now().Add(tokenLockTTL)
		releaseLocks := func() {
			releaseDist()
			releaseLocal()
		}
		if waitAdopted {
			releaseLocks()
			return RotationRefreshResult{AdoptedAccess: waitAccess, Adopted: true}, nil
		}

		preRefreshBudget := tokenPreRefreshTimeout
		if req.Locker != nil {
			preRefreshReserve := tokenRefreshTimeout + tokenPersistTimeout + tokenLockSafetyMargin
			remainingForPreRefresh := time.Until(lockExpiry) - preRefreshReserve
			if remainingForPreRefresh <= 0 {
				releaseLocks()
				return RotationRefreshResult{}, fmt.Errorf("%w: distributed oauth lock lease insufficient for pre-refresh check", ErrRotationPersistence)
			}
			if remainingForPreRefresh < preRefreshBudget {
				preRefreshBudget = remainingForPreRefresh
			}
		}
		preRefreshCtx, cancelPreRefresh := context.WithTimeout(ctx, preRefreshBudget)

		rotated, ok, lookupErr = LookupRotatedRefreshTokenWithStore(preRefreshCtx, req.DB, backupStore, req.Cache, req.EncryptionKey, req.UserID, req.Provider, target, nowFn())
		if lookupErr != nil {
			cancelPreRefresh()
			releaseLocks()
			return RotationRefreshResult{}, lookupErr
		}
		if ok && rotated != "" && rotated != target {
			cancelPreRefresh()
			currentToken = rotated
			releaseLocks()
			continue
		}

		if req.OnLocked != nil {
			adoptedAccess, adopted, updatedToken, err := req.OnLocked(preRefreshCtx, target)
			if err != nil {
				cancelPreRefresh()
				releaseLocks()
				return RotationRefreshResult{}, err
			}
			if adopted {
				cancelPreRefresh()
				releaseLocks()
				return RotationRefreshResult{AdoptedAccess: adoptedAccess, Adopted: true}, nil
			}
			if updatedToken != "" && updatedToken != target {
				cancelPreRefresh()
				currentToken = updatedToken
				releaseLocks()
				continue
			}
		}
		cancelPreRefresh()

		refreshBudget := tokenRefreshTimeout
		if req.Locker != nil {
			refreshReserve := tokenPersistTimeout + tokenLockSafetyMargin
			remainingForRefresh := time.Until(lockExpiry) - refreshReserve
			if remainingForRefresh <= 0 {
				releaseLocks()
				return RotationRefreshResult{}, fmt.Errorf("%w: distributed oauth lock lease exhausted before provider refresh", ErrRotationPersistence)
			}
			if remainingForRefresh < refreshBudget {
				refreshBudget = remainingForRefresh
			}
		}
		refreshCtx, cancelRefresh := context.WithTimeout(ctx, refreshBudget)
		tokenResp, err := refreshFn(refreshCtx, req.Provider, target)
		cancelRefresh()
		if err != nil || tokenResp == nil || tokenResp.AccessToken == "" {
			releaseLocks()
			if err != nil {
				return RotationRefreshResult{}, err
			}
			return RotationRefreshResult{}, errors.New("oauth refresh returned empty access token")
		}

		// Once the provider has consumed target and issued a replacement token,
		// persist both history and caller-owned rows using a detached deadline
		// strictly bounded by the remaining distributed lock lease so blocked DB
		// calls cancel before another instance can acquire the expired lock.
		persistTimeout := tokenPersistTimeout
		if req.Locker != nil {
			remainingLease := time.Until(lockExpiry) - tokenLockSafetyMargin
			if remainingLease <= 0 {
				if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != target {
					aliases := []string{target, req.RefreshToken}
					if req.ExtraAliases != nil {
						aliases = append(aliases, req.ExtraAliases()...)
					}
					shortCtx, cancelShort := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
					_ = RecordRotatedRefreshTokenWithStore(shortCtx, nil, backupStore, req.Cache, req.EncryptionKey, req.UserID, req.Provider, tokenResp.RefreshToken, nowFn(), aliases...)
					cancelShort()
				}
				releaseLocks()
				return RotationRefreshResult{}, fmt.Errorf("%w: distributed oauth lock lease exhausted before persistence", ErrRotationPersistence)
			}
			if remainingLease < persistTimeout {
				persistTimeout = remainingLease
			}
		}
		persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
		now := nowFn()
		var recErr error
		if tokenResp.RefreshToken != "" && tokenResp.RefreshToken != target {
			aliases := []string{target, req.RefreshToken}
			if req.ExtraAliases != nil {
				aliases = append(aliases, req.ExtraAliases()...)
			}
			recErr = RecordRotatedRefreshTokenWithStore(persistCtx, req.DB, backupStore, req.Cache, req.EncryptionKey, req.UserID, req.Provider, tokenResp.RefreshToken, now, aliases...)
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
