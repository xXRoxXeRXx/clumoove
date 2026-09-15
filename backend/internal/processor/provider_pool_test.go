package processor

import (
	"context"
	"errors"
	"testing"

	"backend/internal/storage"
)

type pooledTestProvider struct {
	fakeProvider
	connects int
	closes   int
}

func (p *pooledTestProvider) Connect(context.Context) (bool, error) {
	p.connects++
	return true, nil
}

func (p *pooledTestProvider) Close() error {
	p.closes++
	return nil
}

func TestProviderPoolReusesMatchingEncryptedConfiguration(t *testing.T) {
	pool := newProviderPool()
	var created []*pooledTestProvider
	pool.factory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		provider := &pooledTestProvider{}
		created = append(created, provider)
		return provider, nil
	}

	key := migrationProviderPoolKey("migration-1")
	spec := providerPoolSpec{
		providerType:      "webdav",
		url:               "https://storage.example.test",
		username:          "user",
		password:          "plaintext-one",
		passwordEncrypted: "encrypted-one",
	}
	first, err := pool.get(context.Background(), key, "source", spec)
	if err != nil {
		t.Fatalf("first pool.get: %v", err)
	}

	// The plaintext is deliberately not part of the cache metadata. A stable
	// encrypted credential snapshot is what controls provider reuse.
	spec.password = "plaintext-two"
	second, err := pool.get(context.Background(), key, "source", spec)
	if err != nil {
		t.Fatalf("second pool.get: %v", err)
	}
	if first != second || len(created) != 1 || created[0].connects != 1 {
		t.Fatalf("matching provider reused incorrectly: clients=%d connects=%d", len(created), created[0].connects)
	}
}

func TestProviderPoolReplacesChangedConfigurationAndClosesEntity(t *testing.T) {
	pool := newProviderPool()
	var created []*pooledTestProvider
	pool.factory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		provider := &pooledTestProvider{}
		created = append(created, provider)
		return provider, nil
	}

	key := syncProviderPoolKey("sync-1", 4)
	spec := providerPoolSpec{providerType: "sftp", url: "sftp://storage.example.test", username: "user", password: "first", passwordEncrypted: "encrypted-first"}
	if _, err := pool.get(context.Background(), key, "target", spec); err != nil {
		t.Fatalf("first pool.get: %v", err)
	}
	spec.password = "second"
	spec.passwordEncrypted = "encrypted-second"
	if _, err := pool.get(context.Background(), key, "target", spec); err != nil {
		t.Fatalf("replacement pool.get: %v", err)
	}
	if len(created) != 2 || created[0].closes != 1 || created[1].connects != 1 {
		t.Fatalf("configuration change did not replace provider: created=%d first closes=%d second connects=%d", len(created), created[0].closes, created[1].connects)
	}

	pool.invalidate(key)
	pool.invalidate(key)
	if created[1].closes != 1 {
		t.Fatalf("entity invalidation closes=%d, want 1", created[1].closes)
	}
}

func TestProviderPoolClosesClientInvalidatedDuringConnect(t *testing.T) {
	pool := newProviderPool()
	provider := &pooledTestProvider{}
	started := make(chan struct{})
	release := make(chan struct{})
	pool.factory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		close(started)
		<-release
		return provider, nil
	}

	key := migrationProviderPoolKey("migration-connecting")
	done := make(chan error, 1)
	go func() {
		_, err := pool.get(context.Background(), key, "source", providerPoolSpec{providerType: "webdav", passwordEncrypted: "encrypted"})
		done <- err
	}()
	<-started
	pool.invalidate(key)
	close(release)

	if err := <-done; !errors.Is(err, errProviderPoolInvalidated) {
		t.Fatalf("pool.get error = %v, want invalidated-during-connect", err)
	}
	if provider.connects != 1 || provider.closes != 1 {
		t.Fatalf("connected provider was not released: connects=%d closes=%d", provider.connects, provider.closes)
	}
}

func TestProviderPoolClosesClientWhenShutdownDuringConnect(t *testing.T) {
	pool := newProviderPool()
	provider := &pooledTestProvider{}
	started := make(chan struct{})
	release := make(chan struct{})
	pool.factory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		close(started)
		<-release
		return provider, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := pool.get(context.Background(), migrationProviderPoolKey("migration-shutdown"), "source", providerPoolSpec{providerType: "webdav", passwordEncrypted: "encrypted"})
		done <- err
	}()
	<-started
	pool.Close()
	close(release)

	if err := <-done; !errors.Is(err, errProviderPoolClosed) {
		t.Fatalf("pool.get error = %v, want closed", err)
	}
	if provider.connects != 1 || provider.closes != 1 {
		t.Fatalf("connected provider was not released: connects=%d closes=%d", provider.connects, provider.closes)
	}
}

func TestProviderPoolPrunesTerminalEntriesAndClosesOnShutdown(t *testing.T) {
	pool := newProviderPool()
	var created []*pooledTestProvider
	pool.factory = func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
		provider := &pooledTestProvider{}
		created = append(created, provider)
		return provider, nil
	}

	migrationKey := migrationProviderPoolKey("migration-1")
	syncKey := syncProviderPoolKey("sync-1", 2)
	spec := providerPoolSpec{providerType: "local", passwordEncrypted: "local"}
	if _, err := pool.get(context.Background(), migrationKey, "source", spec); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.get(context.Background(), syncKey, "target", spec); err != nil {
		t.Fatal(err)
	}

	pool.prune(func(key providerPoolKey) bool { return key == migrationKey })
	if created[0].closes != 1 || created[1].closes != 0 {
		t.Fatalf("prune closed unexpected providers: migration=%d sync=%d", created[0].closes, created[1].closes)
	}
	pool.Close()
	pool.Close()
	if created[1].closes != 1 {
		t.Fatalf("shutdown closes=%d, want 1", created[1].closes)
	}
}
