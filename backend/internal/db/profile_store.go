package db

import (
	"context"
	"database/sql"
)

// ProfileStore is the persistence seam for saved connection profiles. It is the
// only exported way to read or write the connection_profiles table through the
// db package; its queries stay unexported. (The migration list query in cmd/api
// still joins the table directly until the migrations slice of #83 lands.)
//
// Ownership is not enforced by the individual calls: callers verify it with
// VerifyProfileOwnership before reading or mutating a profile by id.
type ProfileStore interface {
	CreateConnectionProfile(ctx context.Context, p *ConnectionProfile) (string, error)
	GetConnectionProfile(ctx context.Context, id string) (*ConnectionProfile, error)
	ListConnectionProfiles(ctx context.Context, userID string) ([]ConnectionProfile, error)
	UpdateConnectionProfile(ctx context.Context, id string, in UpdateConnectionProfileInput) error
	// UpdateConnectionProfileMegaSession swaps the cached MEGA session only
	// while the stored session still equals expectedSessionIDEncrypted, and
	// returns ErrOAuthTokenConflict otherwise.
	UpdateConnectionProfileMegaSession(ctx context.Context, id, sessionIDEncrypted, masterKeyEncrypted, expectedSessionIDEncrypted string) error
	DeleteConnectionProfile(ctx context.Context, id string) error
	VerifyProfileOwnership(ctx context.Context, profileID, userID string) (bool, error)
	// TokenSink persists rotated OAuth tokens of one profile, guarded by the
	// refresh token the caller last read.
	TokenSink(profileID string) OAuthTokenSink
}

type postgresProfileStore struct{ db *sql.DB }

// NewProfileStore returns the PostgreSQL-backed ProfileStore. It panics on a
// nil handle so a wiring mistake fails at startup instead of on the first query.
func NewProfileStore(database *sql.DB) ProfileStore {
	if database == nil {
		panic("db: NewProfileStore requires a database handle")
	}
	return postgresProfileStore{db: database}
}

func (s postgresProfileStore) CreateConnectionProfile(ctx context.Context, p *ConnectionProfile) (string, error) {
	return createConnectionProfile(ctx, s.db, p)
}

func (s postgresProfileStore) GetConnectionProfile(ctx context.Context, id string) (*ConnectionProfile, error) {
	return getConnectionProfile(ctx, s.db, id)
}

func (s postgresProfileStore) ListConnectionProfiles(ctx context.Context, userID string) ([]ConnectionProfile, error) {
	return listConnectionProfiles(ctx, s.db, userID)
}

func (s postgresProfileStore) UpdateConnectionProfile(ctx context.Context, id string, in UpdateConnectionProfileInput) error {
	return updateConnectionProfile(ctx, s.db, id, in)
}

func (s postgresProfileStore) UpdateConnectionProfileMegaSession(ctx context.Context, id, sessionIDEncrypted, masterKeyEncrypted, expectedSessionIDEncrypted string) error {
	return updateConnectionProfileMegaSession(ctx, s.db, id, sessionIDEncrypted, masterKeyEncrypted, expectedSessionIDEncrypted)
}

func (s postgresProfileStore) DeleteConnectionProfile(ctx context.Context, id string) error {
	return deleteConnectionProfile(ctx, s.db, id)
}

func (s postgresProfileStore) VerifyProfileOwnership(ctx context.Context, profileID, userID string) (bool, error) {
	return verifyProfileOwnership(ctx, s.db, profileID, userID)
}

func (s postgresProfileStore) TokenSink(profileID string) OAuthTokenSink {
	return newConnectionProfileTokenSink(s.db, profileID)
}
