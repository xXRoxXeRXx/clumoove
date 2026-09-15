package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"

	"backend/internal/storage"
)

var (
	errProviderPoolClosed      = errors.New("provider pool is closed")
	errProviderPoolInvalidated = errors.New("provider pool entry invalidated during connect")
)

type providerPoolKey struct {
	entityType string
	entityID   string
	generation int
}

func migrationProviderPoolKey(migrationID string) providerPoolKey {
	return providerPoolKey{entityType: "migration", entityID: migrationID}
}

func syncProviderPoolKey(syncJobID string, generation int) providerPoolKey {
	return providerPoolKey{entityType: "sync", entityID: syncJobID, generation: generation}
}

type providerPoolSpec struct {
	providerType           string
	url                    string
	username               string
	password               string // Plaintext for construction only; never fingerprinted or stored by the pool.
	passwordEncrypted      string
	megaSessionIDEncrypted string
	megaMasterKeyEncrypted string
	threads                int
}

func (s providerPoolSpec) fingerprint() string {
	hash := sha256.New()
	for _, value := range []string{
		s.providerType, s.url, s.username, s.passwordEncrypted,
		s.megaSessionIDEncrypted, s.megaMasterKeyEncrypted,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type providerPoolEntry struct {
	client      storage.StorageProvider
	fingerprint string
}

// providerPool belongs to exactly one worker goroutine. Its mutex only makes
// Close and invalidation safe if future lifecycle hooks call them externally.
type providerPool struct {
	mu            sync.Mutex
	entries       map[providerPoolKey]map[string]providerPoolEntry
	entityEpochs  map[providerPoolKey]uint64
	syncJobEpochs map[string]uint64
	closed        bool
	factory       func(context.Context, string, string, string, string) (storage.StorageProvider, error)
}

func newProviderPool() *providerPool {
	return &providerPool{
		entries:       make(map[providerPoolKey]map[string]providerPoolEntry),
		entityEpochs:  make(map[providerPoolKey]uint64),
		syncJobEpochs: make(map[string]uint64),
		factory:       newProvider,
	}
}

func (p *providerPool) get(ctx context.Context, key providerPoolKey, role string, spec providerPoolSpec) (storage.StorageProvider, error) {
	fingerprint := spec.fingerprint()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errProviderPoolClosed
	}
	if roles := p.entries[key]; roles != nil {
		if entry, ok := roles[role]; ok && entry.fingerprint == fingerprint {
			configurePooledProvider(entry.client, spec)
			p.mu.Unlock()
			return entry.client, nil
		}
	}
	entityEpoch := p.entityEpochs[key]
	syncJobEpoch := uint64(0)
	if key.entityType == "sync" {
		syncJobEpoch = p.syncJobEpochs[key.entityID]
	}
	p.mu.Unlock()

	client, err := p.factory(ctx, spec.providerType, spec.url, spec.username, spec.password)
	if err != nil {
		return nil, err
	}
	configurePooledProvider(client, spec)
	connected, err := client.Connect(ctx)
	if err != nil || !connected {
		_ = client.Close()
		if err == nil {
			err = errors.New("provider rejected connection")
		}
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = client.Close()
		return nil, errProviderPoolClosed
	}
	if p.entityEpochs[key] != entityEpoch || (key.entityType == "sync" && p.syncJobEpochs[key.entityID] != syncJobEpoch) {
		_ = client.Close()
		return nil, errProviderPoolInvalidated
	}
	roles := p.entries[key]
	if roles == nil {
		roles = make(map[string]providerPoolEntry)
		p.entries[key] = roles
	}
	if previous, ok := roles[role]; ok {
		_ = previous.client.Close()
	}
	roles[role] = providerPoolEntry{client: client, fingerprint: fingerprint}
	return client, nil
}

func configurePooledProvider(client storage.StorageProvider, spec providerPoolSpec) {
	if nextcloud, ok := client.(*storage.NextcloudProvider); ok {
		nextcloud.Threads = spec.threads
	}
}

func (p *providerPool) invalidate(key providerPoolKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entityEpochs[key]++
	p.closeEntityLocked(key)
}

func (p *providerPool) invalidateSyncJob(syncJobID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncJobEpochs[syncJobID]++
	for key := range p.entries {
		if key.entityType == "sync" && key.entityID == syncJobID {
			p.entityEpochs[key]++
			p.closeEntityLocked(key)
		}
	}
}

func (p *providerPool) closeEntity(key providerPoolKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entityEpochs[key]++
	p.closeEntityLocked(key)
}

func (p *providerPool) closeEntityLocked(key providerPoolKey) {
	roles := p.entries[key]
	for _, entry := range roles {
		_ = entry.client.Close()
	}
	delete(p.entries, key)
}

func (p *providerPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for key := range p.entries {
		p.closeEntityLocked(key)
	}
}

func (p *providerPool) prune(terminal func(providerPoolKey) bool) {
	p.mu.Lock()
	keys := make([]providerPoolKey, 0, len(p.entries))
	for key := range p.entries {
		keys = append(keys, key)
	}
	p.mu.Unlock()

	for _, key := range keys {
		if terminal(key) {
			p.closeEntity(key)
		}
	}
}
