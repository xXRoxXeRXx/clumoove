package db

import (
	"database/sql"
	"time"
)

// InstanceOAuthProvider holds administrator-managed OAuth2 client credentials for
// one provider. ClientSecretEnc is always AES-GCM encrypted and must never be
// serialized to the client.
type InstanceOAuthProvider struct {
	Provider        string
	ClientID        string
	ClientSecretEnc string // never serialized
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func ListInstanceOAuthProviders(db *sql.DB) ([]InstanceOAuthProvider, error) {
	query := `
		SELECT provider, client_id, client_secret_encrypted, created_at, updated_at
		FROM instance_oauth_providers
		ORDER BY provider
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []InstanceOAuthProvider
	for rows.Next() {
		var p InstanceOAuthProvider
		if err := rows.Scan(&p.Provider, &p.ClientID, &p.ClientSecretEnc, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// GetInstanceOAuthProvider returns the configured credentials for a single
// provider. It returns sql.ErrNoRows when the provider is not configured.
func GetInstanceOAuthProvider(db *sql.DB, provider string) (*InstanceOAuthProvider, error) {
	query := `
		SELECT provider, client_id, client_secret_encrypted, created_at, updated_at
		FROM instance_oauth_providers WHERE provider = $1
	`
	var p InstanceOAuthProvider
	err := db.QueryRow(query, provider).Scan(&p.Provider, &p.ClientID, &p.ClientSecretEnc, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func UpsertInstanceOAuthProvider(db *sql.DB, p *InstanceOAuthProvider) error {
	query := `
		INSERT INTO instance_oauth_providers (provider, client_id, client_secret_encrypted)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider) DO UPDATE SET
			client_id = EXCLUDED.client_id,
			client_secret_encrypted = EXCLUDED.client_secret_encrypted,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(query, p.Provider, p.ClientID, p.ClientSecretEnc)
	return err
}

func DeleteInstanceOAuthProvider(db *sql.DB, provider string) error {
	_, err := db.Exec(`DELETE FROM instance_oauth_providers WHERE provider = $1`, provider)
	return err
}
