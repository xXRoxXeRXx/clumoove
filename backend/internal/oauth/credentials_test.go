package oauth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCacheSingleLoadWithinTTL(t *testing.T) {
	var calls int64
	Configure(func() (map[string]Credentials, error) {
		atomic.AddInt64(&calls, 1)
		return map[string]Credentials{
			"google": {ClientID: "id", ClientSecretEnc: "enc"},
		}, nil
	}, testEncryptionKey)

	if _, err := clientID("google"); err != nil {
		t.Fatalf("clientID: %v", err)
	}
	// A second read within the TTL must not reload.
	if _, err := clientID("google"); err != nil {
		t.Fatalf("clientID: %v", err)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("loader called %d times, want 1", atomic.LoadInt64(&calls))
	}
}

func TestInvalidateForcesReload(t *testing.T) {
	var calls int64
	Configure(func() (map[string]Credentials, error) {
		atomic.AddInt64(&calls, 1)
		return map[string]Credentials{
			"google": {ClientID: "id", ClientSecretEnc: "enc"},
		}, nil
	}, testEncryptionKey)

	if _, err := clientID("google"); err != nil {
		t.Fatalf("clientID: %v", err)
	}
	Invalidate()
	if _, err := clientID("google"); err != nil {
		t.Fatalf("clientID after invalidate: %v", err)
	}
	if atomic.LoadInt64(&calls) != 2 {
		t.Fatalf("loader called %d times after invalidate, want 2", atomic.LoadInt64(&calls))
	}
}

func TestLoaderErrorReportsUnconfigured(t *testing.T) {
	Configure(func() (map[string]Credentials, error) {
		return nil, errors.New("boom")
	}, testEncryptionKey)
	defer Invalidate()

	cp := ConfiguredProviders()
	for name := range providerNames {
		if cp[name] {
			t.Errorf("ConfiguredProviders()[%q] = true on loader error, want false", name)
		}
	}
	// Access functions must surface the not-configured state, never panic.
	if id, err := clientID("google"); err == nil {
		t.Fatalf("expected error for missing provider, got id %q", id)
	}
}

func TestMissingLoaderReportsUnconfigured(t *testing.T) {
	Configure(nil, testEncryptionKey)
	defer Invalidate()

	if id, err := clientID("google"); err == nil {
		t.Fatalf("expected error with nil loader, got id %q", id)
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	Configure(func() (map[string]Credentials, error) {
		return map[string]Credentials{
			"google": {ClientID: "id", ClientSecretEnc: "enc"},
		}, nil
	}, testEncryptionKey)
	defer Invalidate()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ConfiguredProviders()
			_, _ = clientID("google")
		}()
	}
	wg.Wait()
}
