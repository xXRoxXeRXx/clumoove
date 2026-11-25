package oauth

import (
	"backend/internal/db"
	"database/sql"
)

// NewDBLoader returns a CredentialLoader backed by the instance_oauth_providers
// table. Importing backend/internal/db is cycle-free: that package only depends
// on internal/sanitize, never on oauth.
func NewDBLoader(database *sql.DB) CredentialLoader {
	return NewStoreLoader(db.NewOAuthProviderStore(database))
}

// NewStoreLoader returns a CredentialLoader that reads the administrator-managed
// client credentials through an OAuthProviderStore.
func NewStoreLoader(providers db.OAuthProviderStore) CredentialLoader {
	return func() (map[string]Credentials, error) {
		rows, err := providers.ListInstanceOAuthProviders()
		if err != nil {
			return nil, err
		}
		out := make(map[string]Credentials, len(rows))
		for _, r := range rows {
			out[r.Provider] = Credentials{ClientID: r.ClientID, ClientSecretEnc: r.ClientSecretEnc}
		}
		return out, nil
	}
}
