package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrOAuthTokenConflict is the typed "superseded" result of a token sink: the
// owner's stored refresh token no longer matches the one the rotation started
// from, so another worker already rotated (or the owner lost its OAuth credential).
// Callers adopt the stored winner instead of treating it as a failure.
var ErrOAuthTokenConflict = errors.New("oauth token update conflict: persisted token changed concurrently")

// EncryptedOAuthTokens is a rotated token pair as persisted. Both secrets are
// already encrypted with ENCRYPTION_SECRET_KEY; a sink never sees plaintext.
type EncryptedOAuthTokens struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
}

// OAuthTokenSink is where one owner's rotated OAuth tokens are persisted. There
// is one adapter per owner type (migration, sync job, backup job, backup
// repository, restore run, connection profile), all sharing this contract.
type OAuthTokenSink interface {
	// Persist stores rotated only if the owner's stored refresh token still equals
	// expectedRefresh (compare-and-swap). A lost race, an empty expectedRefresh or
	// an owner that no longer exists yields ErrOAuthTokenConflict and leaves the
	// stored tokens untouched.
	Persist(ctx context.Context, rotated EncryptedOAuthTokens, expectedRefresh string) error
}

// OAuthReauthSink is an OAuthTokenSink whose owner also accepts tokens a user
// just obtained by re-authorizing. That is not a rotation: nothing is anchored to
// a previous refresh token, so the stored pair is replaced unconditionally.
type OAuthReauthSink interface {
	OAuthTokenSink
	Reauthorize(ctx context.Context, tokens EncryptedOAuthTokens) error
}

// casTokenSink is the shared adapter. Every query binds the same positional
// parameters: $1 access, $2 refresh, $3 expiry, $4 owner id, $5 expected refresh.
// Table and column names come from the fixed constants below, never from input.
type casTokenSink struct {
	db *sql.DB
	id string
	// persistQuery is the CAS UPDATE; setupErr is returned instead when the sink
	// could not be built from its arguments (an invalid role).
	persistQuery string
	setupErr     error
}

func (s casTokenSink) Persist(ctx context.Context, rotated EncryptedOAuthTokens, expectedRefresh string) error {
	if s.setupErr != nil {
		return s.setupErr
	}
	// OAuth rotation must always be anchored to an existing encrypted refresh
	// token. An empty expected value would turn the SQL NULL/empty branch into an
	// unconditional credential upsert for owners without OAuth.
	if expectedRefresh == "" {
		return ErrOAuthTokenConflict
	}
	res, err := s.db.ExecContext(ctx, s.persistQuery, rotated.Access, rotated.Refresh, rotated.ExpiresAt, s.id, expectedRefresh)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrOAuthTokenConflict
	}
	return nil
}

// reauthTokenSink adds unconditional replacement for owners that support it.
type reauthTokenSink struct {
	casTokenSink
	reauthQuery string
}

func (s reauthTokenSink) Reauthorize(ctx context.Context, tokens EncryptedOAuthTokens) error {
	if s.setupErr != nil {
		return s.setupErr
	}
	_, err := s.db.ExecContext(ctx, s.reauthQuery, tokens.Access, tokens.Refresh, tokens.ExpiresAt, s.id)
	return err
}

// credentialColumns returns the allowlisted column prefix of a role, so the role
// string can never reach SQL.
func credentialColumns(role string) (string, bool) {
	switch role {
	case "source", "target":
		return role, true
	}
	return "", false
}

// roleCredentialQueries builds the CAS and re-authorization queries for tables
// that hold both a source and a target credential pair under an `id` key.
func roleCredentialQueries(table, role string) (persist, reauth string, ok bool) {
	prefix, ok := credentialColumns(role)
	if !ok {
		return "", "", false
	}
	set := prefix + "_password_encrypted = $1, " + prefix + "_refresh_token_encrypted = $2, " + prefix + "_token_expires_at = $3, updated_at = CURRENT_TIMESTAMP"
	persist = "UPDATE " + table + " SET " + set + " WHERE id = $4 AND " + prefix + "_refresh_token_encrypted = $5"
	reauth = "UPDATE " + table + " SET " + set + " WHERE id = $4"
	return persist, reauth, true
}

func newRoleReauthSink(database *sql.DB, table, id, role string) OAuthReauthSink {
	persist, reauth, ok := roleCredentialQueries(table, role)
	if !ok {
		return reauthTokenSink{casTokenSink: casTokenSink{db: database, id: id, setupErr: fmt.Errorf("invalid oauth token role %q", role)}}
	}
	return reauthTokenSink{casTokenSink: casTokenSink{db: database, id: id, persistQuery: persist}, reauthQuery: reauth}
}

// NewMigrationTokenSink persists the source or target OAuth tokens of a migration.
func NewMigrationTokenSink(database *sql.DB, migrationID, role string) OAuthReauthSink {
	return newRoleReauthSink(database, "migrations", migrationID, role)
}

// NewSyncJobTokenSink persists the source or target OAuth tokens of a sync job.
func NewSyncJobTokenSink(database *sql.DB, syncJobID, role string) OAuthReauthSink {
	return newRoleReauthSink(database, "sync_jobs", syncJobID, role)
}

// backupCredentialQuery is the CAS UPDATE for a backup job's credential snapshot.
// Rotation only touches live jobs, so a job being deleted keeps its tokens.
func backupCredentialQuery(prefix string) string {
	return "UPDATE backup_jobs SET " + prefix + "_password_encrypted = $1, " + prefix + "_refresh_token_encrypted = $2, " + prefix + "_token_expires_at = $3 WHERE id = $4 AND " + prefix + "_refresh_token_encrypted = $5 AND deletion_state = 'ACTIVE'"
}

// NewBackupJobTokenSink persists the source or target OAuth tokens of a backup
// job. The target role is the backup repository itself.
func NewBackupJobTokenSink(database *sql.DB, backupJobID, role string) OAuthTokenSink {
	prefix, ok := credentialColumns(role)
	if !ok {
		return casTokenSink{db: database, id: backupJobID, setupErr: ErrOAuthTokenConflict}
	}
	return casTokenSink{db: database, id: backupJobID, persistQuery: backupCredentialQuery(prefix)}
}

// NewBackupRepositoryTokenSink persists the OAuth tokens of the backup
// repository. Restore rows never copy these source secrets; concurrent workers
// adopt the CAS winner instead of persisting a stale token.
func NewBackupRepositoryTokenSink(database *sql.DB, backupJobID string) OAuthTokenSink {
	return casTokenSink{db: database, id: backupJobID, persistQuery: backupCredentialQuery("target")}
}

// NewRestoreRunTokenSink persists the target token snapshot owned by an active
// restore run. A finished run keeps whatever it last held.
func NewRestoreRunTokenSink(database *sql.DB, restoreRunID string) OAuthTokenSink {
	return casTokenSink{db: database, id: restoreRunID, persistQuery: `
		UPDATE restore_runs
		SET target_password_encrypted = $1, target_refresh_token_encrypted = $2, target_token_expires_at = $3
		WHERE id = $4 AND target_refresh_token_encrypted = $5
			AND status IN ('QUEUED','PLANNING','RUNNING','VERIFYING','CANCELLING')`}
}

// NewConnectionProfileTokenSink persists the OAuth tokens of a saved connection profile.
func NewConnectionProfileTokenSink(database *sql.DB, profileID string) OAuthTokenSink {
	return casTokenSink{db: database, id: profileID, persistQuery: `
		UPDATE connection_profiles
		SET password_encrypted = $1,
		    refresh_token_encrypted = $2,
		    token_expires_at = $3,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $4
		  AND refresh_token_encrypted = $5`}
}
