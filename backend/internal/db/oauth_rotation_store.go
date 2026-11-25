package db

import (
	"context"
	"database/sql"
)

// OAuthRotationStore is the persistence seam for the durable OAuth refresh-token
// rotation chain (the oauth_token_rotations table). It delegates to the
// package-level functions without adding queries.
//
// Rows are keyed by hashes from OAuthTokenRotationHash and carry the rotated
// refresh token already encrypted by the caller; the store never sees plaintext
// tokens or the encryption key. Record overwrites an existing mapping, while
// Repair only inserts when none exists, so a delayed repair can never replace a
// newer rotation. Both are no-ops for incomplete arguments.
type OAuthRotationStore interface {
	// RecordOAuthTokenRotation upserts previous -> rotated.
	RecordOAuthTokenRotation(ctx context.Context, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) error
	// RepairOAuthTokenRotation inserts previous -> rotated only when previous has
	// no mapping yet and reports whether it inserted.
	RepairOAuthTokenRotation(ctx context.Context, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) (bool, error)
	// LookupOAuthTokenRotation resolves the newest hash and ciphertext reachable
	// from tokenHash along the recorded chain.
	LookupOAuthTokenRotation(ctx context.Context, userID, provider, tokenHash string) (latestHash, latestEncrypted string, found bool, err error)
	// LookupOAuthTokenRotationChain is LookupOAuthTokenRotation that also returns
	// every hash visited along the chain.
	LookupOAuthTokenRotationChain(ctx context.Context, userID, provider, tokenHash string) (latestHash, latestEncrypted string, visited map[string]struct{}, found bool, err error)
}

type postgresOAuthRotationStore struct{ db *sql.DB }

// NewOAuthRotationStore returns the PostgreSQL-backed OAuthRotationStore. It
// panics on a nil handle so a wiring mistake fails at startup instead of on the
// first query.
func NewOAuthRotationStore(database *sql.DB) OAuthRotationStore {
	if database == nil {
		panic("db: NewOAuthRotationStore requires a database handle")
	}
	return postgresOAuthRotationStore{db: database}
}

func (s postgresOAuthRotationStore) RecordOAuthTokenRotation(ctx context.Context, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) error {
	return recordOAuthTokenRotation(ctx, s.db, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted)
}

func (s postgresOAuthRotationStore) RepairOAuthTokenRotation(ctx context.Context, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) (bool, error) {
	return repairOAuthTokenRotation(ctx, s.db, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted)
}

func (s postgresOAuthRotationStore) LookupOAuthTokenRotation(ctx context.Context, userID, provider, tokenHash string) (string, string, bool, error) {
	return lookupOAuthTokenRotation(ctx, s.db, userID, provider, tokenHash)
}

func (s postgresOAuthRotationStore) LookupOAuthTokenRotationChain(ctx context.Context, userID, provider, tokenHash string) (string, string, map[string]struct{}, bool, error) {
	return lookupOAuthTokenRotationChain(ctx, s.db, userID, provider, tokenHash)
}
