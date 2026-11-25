package oauth

import (
	"errors"
	"testing"

	"backend/internal/db"
)

type fakeProviderLister struct {
	db.OAuthProviderStore
	rows []db.InstanceOAuthProvider
	err  error
}

func (f fakeProviderLister) ListInstanceOAuthProviders() ([]db.InstanceOAuthProvider, error) {
	return f.rows, f.err
}

func TestStoreLoaderMapsStoredProvidersToCredentials(t *testing.T) {
	load := NewStoreLoader(fakeProviderLister{rows: []db.InstanceOAuthProvider{
		{Provider: "google", ClientID: "g-id", ClientSecretEnc: "g-enc"},
		{Provider: "box", ClientID: "b-id", ClientSecretEnc: "b-enc"},
	}})

	got, err := load()
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if len(got) != 2 || got["google"] != (Credentials{ClientID: "g-id", ClientSecretEnc: "g-enc"}) || got["box"] != (Credentials{ClientID: "b-id", ClientSecretEnc: "b-enc"}) {
		t.Fatalf("credentials = %+v", got)
	}
}

func TestStoreLoaderPropagatesStoreFailure(t *testing.T) {
	boom := errors.New("boom")
	if _, err := NewStoreLoader(fakeProviderLister{err: boom})(); !errors.Is(err, boom) {
		t.Fatalf("load() error = %v, want %v", err, boom)
	}
}
