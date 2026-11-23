package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RefreshRotationOutcome reports how RotateRefreshToken ended when it did not
// fail with an error.
type RefreshRotationOutcome int

const (
	// RefreshRotated: the presented token was consumed and the replacement stored.
	RefreshRotated RefreshRotationOutcome = iota
	// RefreshTokenInvalid: the token is unknown, expired, already used, or its
	// user no longer exists. Nothing was changed.
	RefreshTokenInvalid
	// RefreshRotationFailed: the rotation did not complete and the call returns an
	// error. Callers must check the error first; this value only keeps the zero
	// value (RefreshRotated) from being mistaken for success on a failure path.
	RefreshRotationFailed
	// RefreshUserDisabled: the account is suspended. The presented token is
	// consumed and the user's remaining tokens were removed. If that cleanup
	// failed the call also returns the error; the token must be treated as dead.
	RefreshUserDisabled
)

// AuthTokenStore is the persistence seam for the credentials that outlive a
// request: refresh tokens (sessions), password-reset tokens and email-change
// tokens. It delegates to package-level functions without adding queries.
//
// Only token hashes are stored and passed; callers hash the raw token before
// any call. Consuming calls are single-use: the claim and its side effects
// run in one transaction, so concurrent use of the same token lets exactly one
// request continue.
type AuthTokenStore interface {
	// Sessions (refresh tokens).
	StoreRefreshToken(ctx context.Context, tokenHash, userID string, expiresAt time.Time, userAgent string) error
	ListRefreshSessions(ctx context.Context, userID string) ([]RefreshSession, error)
	// DeleteRefreshSessionForUser revokes one session only if it belongs to
	// userID and reports whether a session was removed.
	DeleteRefreshSessionForUser(ctx context.Context, sessionID, userID string) (bool, error)
	DeleteRefreshToken(tokenHash string) error
	DeleteAllRefreshTokensForUser(userID string) error
	// RotateRefreshToken consumes oldTokenHash and stores newTokenHash for the
	// same user and user agent in one transaction. The account row is locked and
	// its active flag re-checked before the replacement is written, so a
	// concurrent suspension either removes the new token or wins first. Check the
	// error before the outcome: only RefreshUserDisabled carries an error that
	// still has a defined outcome.
	RotateRefreshToken(ctx context.Context, oldTokenHash, newTokenHash string, newExpiresAt time.Time) (RefreshRotationOutcome, *User, error)

	// Password reset and email change. Claim returns sql.ErrNoRows for an
	// unknown, used or expired token.
	CreatePasswordResetToken(tokenHash, userID string, expiresAt time.Time) error
	ClaimPasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) (string, error)
	CreateEmailChangeToken(tokenHash, userID, newEmail string, expiresAt time.Time) error
	ClaimEmailChangeToken(ctx context.Context, tokenHash string) (userID, newEmail string, err error)
	DeleteExpiredPasswordResetTokens() error
	DeleteExpiredEmailChangeTokens() error
}

type postgresAuthTokenStore struct{ db *sql.DB }

// NewAuthTokenStore returns the PostgreSQL-backed AuthTokenStore. It panics on
// a nil handle so a wiring mistake fails at startup instead of on the first
// query.
func NewAuthTokenStore(database *sql.DB) AuthTokenStore {
	if database == nil {
		panic("db: NewAuthTokenStore requires a database handle")
	}
	return postgresAuthTokenStore{db: database}
}

func (s postgresAuthTokenStore) StoreRefreshToken(ctx context.Context, tokenHash, userID string, expiresAt time.Time, userAgent string) error {
	return storeRefreshToken(ctx, s.db, tokenHash, userID, expiresAt, userAgent)
}

func (s postgresAuthTokenStore) ListRefreshSessions(ctx context.Context, userID string) ([]RefreshSession, error) {
	return listRefreshSessions(ctx, s.db, userID)
}

func (s postgresAuthTokenStore) DeleteRefreshSessionForUser(ctx context.Context, sessionID, userID string) (bool, error) {
	return deleteRefreshSessionForUser(ctx, s.db, sessionID, userID)
}

func (s postgresAuthTokenStore) DeleteRefreshToken(tokenHash string) error {
	return deleteRefreshToken(s.db, tokenHash)
}

func (s postgresAuthTokenStore) DeleteAllRefreshTokensForUser(userID string) error {
	return deleteAllRefreshTokensForUser(s.db, userID)
}

func (s postgresAuthTokenStore) RotateRefreshToken(ctx context.Context, oldTokenHash, newTokenHash string, newExpiresAt time.Time) (RefreshRotationOutcome, *User, error) {
	return rotateRefreshToken(ctx, s.db, oldTokenHash, newTokenHash, newExpiresAt)
}

func (s postgresAuthTokenStore) CreatePasswordResetToken(tokenHash, userID string, expiresAt time.Time) error {
	return createPasswordResetToken(s.db, tokenHash, userID, expiresAt)
}

func (s postgresAuthTokenStore) ClaimPasswordResetToken(ctx context.Context, tokenHash, newPasswordHash string) (string, error) {
	return claimPasswordResetToken(s.db, ctx, tokenHash, newPasswordHash)
}

func (s postgresAuthTokenStore) CreateEmailChangeToken(tokenHash, userID, newEmail string, expiresAt time.Time) error {
	return createEmailChangeToken(s.db, tokenHash, userID, newEmail, expiresAt)
}

func (s postgresAuthTokenStore) ClaimEmailChangeToken(ctx context.Context, tokenHash string) (string, string, error) {
	return claimEmailChangeToken(s.db, ctx, tokenHash)
}

func (s postgresAuthTokenStore) DeleteExpiredPasswordResetTokens() error {
	return deleteExpiredPasswordResetTokens(s.db)
}

func (s postgresAuthTokenStore) DeleteExpiredEmailChangeTokens() error {
	return deleteExpiredEmailChangeTokens(s.db)
}

// rotateRefreshToken is the refresh-token rotation that handleRefresh used to
// run inline. The statements and their order are unchanged: DELETE ... RETURNING
// consumes the presented token (locking its row), the user is loaded in the same
// transaction, the account row is locked with FOR UPDATE and its active flag
// re-checked, and only then is the replacement inserted.
func rotateRefreshToken(ctx context.Context, database *sql.DB, oldTokenHash, newTokenHash string, newExpiresAt time.Time) (RefreshRotationOutcome, *User, error) {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return RefreshRotationFailed, nil, fmt.Errorf("begin refresh rotation: %w", err)
	}
	defer tx.Rollback()

	var userID, userAgent string
	if err := tx.QueryRowContext(ctx, `
		DELETE FROM refresh_tokens
		WHERE token_hash = $1 AND expires_at > NOW()
		RETURNING user_id, user_agent
	`, oldTokenHash).Scan(&userID, &userAgent); err != nil {
		if err == sql.ErrNoRows {
			return RefreshTokenInvalid, nil, nil
		}
		return RefreshRotationFailed, nil, fmt.Errorf("consume refresh token: %w", err)
	}

	u, err := getUserByIDTx(tx, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return RefreshTokenInvalid, nil, nil
		}
		return RefreshRotationFailed, nil, fmt.Errorf("load refresh token user: %w", err)
	}

	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT active FROM users WHERE id = $1 FOR UPDATE`, u.ID).Scan(&active); err != nil {
		if err == sql.ErrNoRows {
			return RefreshTokenInvalid, nil, nil
		}
		return RefreshRotationFailed, nil, fmt.Errorf("lock refresh token user: %w", err)
	}
	if !active {
		// Normally suspension has already deleted these rows. Removing them here
		// also cleans up a residual token if an earlier operation was interrupted.
		if _, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, u.ID); err != nil {
			return RefreshUserDisabled, nil, fmt.Errorf("purge tokens of suspended user: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return RefreshUserDisabled, nil, fmt.Errorf("commit purge of suspended user: %w", err)
		}
		return RefreshUserDisabled, nil, nil
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO refresh_tokens (token_hash, user_id, user_agent, expires_at)
		VALUES ($1, $2, $3, $4)
	`, newTokenHash, u.ID, userAgent, newExpiresAt); err != nil {
		return RefreshRotationFailed, nil, fmt.Errorf("store replacement refresh token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return RefreshRotationFailed, nil, fmt.Errorf("commit refresh rotation: %w", err)
	}
	return RefreshRotated, u, nil
}
