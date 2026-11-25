package db

import "database/sql"

// OAuthProviderStore is the persistence seam for administrator-managed OAuth2
// client credentials (the instance_oauth_providers table). It delegates to the
// package-level functions without adding queries.
//
// The client secret is stored already encrypted by the caller; the store never
// sees the encryption key, and reads return the ciphertext as stored.
type OAuthProviderStore interface {
	ListInstanceOAuthProviders() ([]InstanceOAuthProvider, error)
	// GetInstanceOAuthProvider returns sql.ErrNoRows when the provider is not configured.
	GetInstanceOAuthProvider(provider string) (*InstanceOAuthProvider, error)
	UpsertInstanceOAuthProvider(p *InstanceOAuthProvider) error
	DeleteInstanceOAuthProvider(provider string) error
}

type postgresOAuthProviderStore struct{ db *sql.DB }

// NewOAuthProviderStore returns the PostgreSQL-backed OAuthProviderStore. It
// panics on a nil handle so a wiring mistake fails at startup instead of on the
// first query.
func NewOAuthProviderStore(database *sql.DB) OAuthProviderStore {
	if database == nil {
		panic("db: NewOAuthProviderStore requires a database handle")
	}
	return postgresOAuthProviderStore{db: database}
}

func (s postgresOAuthProviderStore) ListInstanceOAuthProviders() ([]InstanceOAuthProvider, error) {
	return listInstanceOAuthProviders(s.db)
}

func (s postgresOAuthProviderStore) GetInstanceOAuthProvider(provider string) (*InstanceOAuthProvider, error) {
	return getInstanceOAuthProvider(s.db, provider)
}

func (s postgresOAuthProviderStore) UpsertInstanceOAuthProvider(p *InstanceOAuthProvider) error {
	return upsertInstanceOAuthProvider(s.db, p)
}

func (s postgresOAuthProviderStore) DeleteInstanceOAuthProvider(provider string) error {
	return deleteInstanceOAuthProvider(s.db, provider)
}
