package oauth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"backend/internal/crypto"
)

// credentialCacheTTL bounds how long the process-local credential cache (which
// only ever holds ciphertext) is considered fresh before it is lazily reloaded
// from the loader. It deliberately trades up to 30s of staleness for a very low
// database load. The API invalidates its own cache immediately on write.
const credentialCacheTTL = 30 * time.Second

// Credentials holds the administrator-managed client identity for one provider.
// ClientSecretEnc stays AES-GCM encrypted until the moment it is used.
type Credentials struct {
	ClientID        string
	ClientSecretEnc string
}

// CredentialLoader returns every configured provider in a single call. It is the
// sole source of truth for client credentials and must never return plaintext
// secrets — only ciphertext.
type CredentialLoader func() (map[string]Credentials, error)

type oauthConfig struct {
	loader CredentialLoader
	key    string
}

// cacheEntry is an immutable snapshot of the credential state. The cache holds a
// pointer to it so readers can load it with a single atomic operation and never
// observe a partially written map.
type cacheEntry struct {
	data     map[string]Credentials
	loadedAt time.Time
	err      error
}

var (
	// cfg holds the loader + decryption key. Stored as a pointer so Configure
	// can swap it atomically without locking every read.
	cfg atomic.Pointer[oauthConfig]
	// credCache holds the current snapshot.
	credCache atomic.Pointer[cacheEntry]
	// reloadMu serializes the rare reload path (stampede protection).
	reloadMu sync.Mutex
)

// Configure installs the credential loader and the AES key used to decrypt
// client secrets. It replaces any previous configuration and invalidates the
// cache so the next read is fresh.
func Configure(loader CredentialLoader, encryptionKey string) {
	cfg.Store(&oauthConfig{loader: loader, key: encryptionKey})
	Invalidate()
}

// Invalidate marks the cache as stale so the next read reloads from the loader.
// It stores a fresh empty snapshot rather than mutating the current one, which
// avoids a lost-update window where a concurrent reload could be clobbered by a
// stale copy. It is safe to call from any goroutine.
func Invalidate() {
	credCache.Store(&cacheEntry{})
}

// get returns the current cache snapshot, reloading it from the loader when the
// cached value is missing or older than credentialCacheTTL. It is safe for
// concurrent use by many goroutines without an RWMutex: readers only ever
// atomically load the pointer; the single reloadMu-guarded writer uses
// double-checked locking so it never clobbers a fresh snapshot.
func get() cacheEntry {
	if p := credCache.Load(); p != nil {
		entry := *p
		if !entry.loadedAt.IsZero() && time.Since(entry.loadedAt) < credentialCacheTTL {
			return entry
		}
	}
	return reload()
}

func reload() cacheEntry {
	reloadMu.Lock()
	defer reloadMu.Unlock()

	// Double-checked: another goroutine may have refreshed the cache between
	// our unlocked peek and acquiring the lock.
	if p := credCache.Load(); p != nil {
		entry := *p
		if !entry.loadedAt.IsZero() && time.Since(entry.loadedAt) < credentialCacheTTL {
			return entry
		}
	}

	c := cfg.Load()
	if c == nil || c.loader == nil {
		entry := cacheEntry{loadedAt: time.Now()}
		credCache.Store(&entry)
		return entry
	}

	data, err := c.loader()
	entry := cacheEntry{data: data, err: err, loadedAt: time.Now()}
	credCache.Store(&entry)
	return entry
}

// clientID returns the configured client ID, or an error if the provider is not
// configured or the loader failed.
func clientID(provider string) (string, error) {
	entry := get()
	if entry.err != nil {
		return "", fmt.Errorf("oauth credentials are not configured")
	}
	c, ok := entry.data[provider]
	if !ok {
		return "", fmt.Errorf("oauth provider %s is not configured", provider)
	}
	return c.ClientID, nil
}

// clientSecret decrypts the configured client secret at the moment of use. The
// plaintext is never retained beyond the caller's scope.
func clientSecret(provider string) (string, error) {
	entry := get()
	if entry.err != nil {
		return "", fmt.Errorf("oauth credentials are not configured")
	}
	c, ok := entry.data[provider]
	if !ok || c.ClientSecretEnc == "" {
		return "", fmt.Errorf("oauth provider %s is not configured", provider)
	}
	cfgRef := cfg.Load()
	if cfgRef == nil {
		return "", fmt.Errorf("oauth credentials are not configured")
	}
	return crypto.DecryptWithDomain(c.ClientSecretEnc, cfgRef.key, crypto.DomainOAuthClientSecret)
}

// ConfiguredProviders returns the set of OAuth provider keys that have both a
// client ID and secret configured. Every known provider is represented (false
// when not configured), so the result is stable regardless of loader errors.
func ConfiguredProviders() map[string]bool {
	entry := get()
	result := make(map[string]bool, len(providerNames))
	for name := range providerNames {
		// A nil snapshot map is safe to read and yields zero-value Credentials.
		c := entry.data[name]
		result[name] = c.ClientID != "" && c.ClientSecretEnc != ""
	}
	return result
}
