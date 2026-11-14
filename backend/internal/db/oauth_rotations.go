package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

const maxOAuthRotationHops = 32

// OAuthTokenRotationHash computes the deterministic domain-separated lookup key
// for a refresh token without storing plaintext token material in indexes.
func OAuthTokenRotationHash(userID, provider, refreshToken string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + provider + "\x00" + refreshToken))
	return hex.EncodeToString(sum[:])
}

// RecordOAuthTokenRotation upserts an encrypted OAuth refresh token rotation mapping.
func RecordOAuthTokenRotation(ctx context.Context, database *sql.DB, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) error {
	if database == nil || userID == "" || provider == "" || previousTokenHash == "" || rotatedTokenHash == "" || previousTokenHash == rotatedTokenHash || rotatedRefreshTokenEncrypted == "" {
		return nil
	}
	_, err := database.ExecContext(ctx, `
		INSERT INTO oauth_token_rotations (
			user_id, provider, token_hash, rotated_token_hash, rotated_refresh_token_encrypted
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, provider, token_hash) DO UPDATE
		SET rotated_token_hash = EXCLUDED.rotated_token_hash,
		    rotated_refresh_token_encrypted = EXCLUDED.rotated_refresh_token_encrypted,
		    updated_at = CURRENT_TIMESTAMP
	`, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted)
	return err
}

// RepairOAuthTokenRotation inserts a backup rotation mapping only if PostgreSQL
// does not already have a mapping for previousTokenHash, preventing a delayed
// repair from overwriting a newer rotation. Returns true when inserted.
func RepairOAuthTokenRotation(ctx context.Context, database *sql.DB, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted string) (bool, error) {
	if database == nil || userID == "" || provider == "" || previousTokenHash == "" || rotatedTokenHash == "" || previousTokenHash == rotatedTokenHash || rotatedRefreshTokenEncrypted == "" {
		return false, nil
	}
	res, err := database.ExecContext(ctx, `
		INSERT INTO oauth_token_rotations (
			user_id, provider, token_hash, rotated_token_hash, rotated_refresh_token_encrypted
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, provider, token_hash) DO NOTHING
	`, userID, provider, previousTokenHash, rotatedTokenHash, rotatedRefreshTokenEncrypted)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// LookupOAuthTokenRotation transitively resolves a possibly pre-rotation
// token hash along any recorded rotation chain (A -> B -> C) with cycle protection.
func LookupOAuthTokenRotation(ctx context.Context, database *sql.DB, userID, provider, tokenHash string) (string, string, bool, error) {
	latestHash, latestEncrypted, _, found, err := LookupOAuthTokenRotationChain(ctx, database, userID, provider, tokenHash)
	return latestHash, latestEncrypted, found, err
}

// LookupOAuthTokenRotationChain resolves a rotation chain like
// LookupOAuthTokenRotation and also returns every token hash visited along it.
func LookupOAuthTokenRotationChain(ctx context.Context, database *sql.DB, userID, provider, tokenHash string) (string, string, map[string]struct{}, bool, error) {
	if database == nil || userID == "" || provider == "" || tokenHash == "" {
		return "", "", nil, false, nil
	}
	visited := map[string]struct{}{tokenHash: {}}
	currentHash := tokenHash
	var latestHash, latestEncrypted string
	found := false

	for hop := 0; hop < maxOAuthRotationHops; hop++ {
		var nextHash, nextEncrypted string
		err := database.QueryRowContext(ctx, `
			SELECT rotated_token_hash, rotated_refresh_token_encrypted
			FROM oauth_token_rotations
			WHERE user_id = $1 AND provider = $2 AND token_hash = $3
		`, userID, provider, currentHash).Scan(&nextHash, &nextEncrypted)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			return "", "", nil, false, err
		}
		if nextHash == "" || nextEncrypted == "" {
			break
		}
		latestHash = nextHash
		latestEncrypted = nextEncrypted
		found = true
		if _, seen := visited[nextHash]; seen {
			break
		}
		visited[nextHash] = struct{}{}
		currentHash = nextHash
	}
	return latestHash, latestEncrypted, visited, found, nil
}
