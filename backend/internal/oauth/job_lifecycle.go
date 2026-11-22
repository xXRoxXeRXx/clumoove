package oauth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

type jobOAuthTokenSnapshot struct {
	accessEnc  string
	refreshEnc sql.NullString
	expiresAt  sql.NullTime
	provider   string
}

type jobOAuthTokenState interface {
	OAuthWorkKind() string
	OAuthJobID() string
	OAuthUserID() string
	OAuthTokens(role string) jobOAuthTokenSnapshot
	OAuthUpdatedAt() time.Time
	ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime)
	ReloadOAuthTokens(context.Context) error
	OAuthTokenSink(role string) db.OAuthTokenSink
}

// JobTokenRefresher coordinates inline and forced OAuth access-token refreshes
// for migration and sync jobs with per-job locking and rotation-lock adoption.
type JobTokenRefresher struct {
	db         *sql.DB
	migrations db.MigrationStore
	secretKey  string
	locker     TokenLocker
	refresh    func(context.Context, string, string) (*TokenResponse, error)
	locks      KeyLockSet
}

// NewJobTokenRefresher builds a JobTokenRefresher using the supplied database,
// credential encryption key, and optional distributed TokenLocker.
func NewJobTokenRefresher(database *sql.DB, secretKey string, locker TokenLocker) *JobTokenRefresher {
	r := &JobTokenRefresher{
		db:        database,
		secretKey: secretKey,
		locker:    locker,
	}
	if database != nil {
		r.migrations = db.NewMigrationStore(database)
	}
	return r
}

func (r *JobTokenRefresher) refresher() *Refresher {
	return NewRefresher(RefresherConfig{DB: r.db, Locker: r.locker, EncryptionKey: r.secretKey})
}

func (r *JobTokenRefresher) refreshToken(ctx context.Context, provider, refresh string) (*TokenResponse, error) {
	if r.refresh != nil {
		return r.refresh(ctx, provider, refresh)
	}
	return RefreshToken(ctx, provider, refresh)
}

// EnsureFreshMigrationToken refreshes a migration's OAuth access token when
// expired or within 2 minutes of expiry, returning access unchanged otherwise.
func (r *JobTokenRefresher) EnsureFreshMigrationToken(ctx context.Context, mig *db.Migration, role, access string) (string, error) {
	return r.refreshLifecycle(ctx, &migrationJobTokenState{store: r.migrations, migration: mig}, role, access, false)
}

// ForceRefreshMigrationToken forces a refresh after an OAuth provider rejects
// the current migration access token.
func (r *JobTokenRefresher) ForceRefreshMigrationToken(ctx context.Context, mig *db.Migration, role string) (string, error) {
	return r.refreshLifecycle(ctx, &migrationJobTokenState{store: r.migrations, migration: mig}, role, "", true)
}

// EnsureFreshSyncToken refreshes a sync job's OAuth access token when expired
// or within 2 minutes of expiry, returning access unchanged otherwise.
func (r *JobTokenRefresher) EnsureFreshSyncToken(ctx context.Context, job *db.SyncJob, role, access string) (string, error) {
	return r.refreshLifecycle(ctx, &syncJobTokenState{db: r.db, job: job}, role, access, false)
}

// ForceRefreshSyncToken forces a refresh after an OAuth provider rejects the
// current sync-job access token.
func (r *JobTokenRefresher) ForceRefreshSyncToken(ctx context.Context, job *db.SyncJob, role string) (string, error) {
	return r.refreshLifecycle(ctx, &syncJobTokenState{db: r.db, job: job}, role, "", true)
}

func (r *JobTokenRefresher) refreshLifecycle(ctx context.Context, state jobOAuthTokenState, role, access string, force bool) (string, error) {
	initial := state.OAuthTokens(role)
	if !initial.refreshEnc.Valid || initial.refreshEnc.String == "" {
		if force {
			return "", fmt.Errorf("no OAuth refresh token is stored for %s", role)
		}
		return access, nil
	}
	if !force && initial.expiresAt.Valid && time.Now().Before(initial.expiresAt.Time.Add(-2*time.Minute)) {
		return access, nil
	}

	originalUpdatedAt := state.OAuthUpdatedAt()
	expectedRefreshEnc := initial.refreshEnc.String
	refresh, err := crypto.DecryptWithDomain(expectedRefreshEnc, r.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt %s refresh token: %w", role, err)
	}
	defer crypto.ZeroString(&refresh)

	// Hold the job-role lock alongside the token lock so concurrent force-refreshes
	// on the same job serialize cleanly even if the underlying token rotates.
	jobRelease := r.locks.Lock(fmt.Sprintf("%s:%s:%s", state.OAuthWorkKind(), state.OAuthJobID(), role))
	defer jobRelease()

	refresher := r.refresher()
	res, err := refresher.Refresh(ctx, OwnerRefresh{
		UserID:       state.OAuthUserID(),
		Provider:     initial.provider,
		RefreshToken: refresh,
		Label:        fmt.Sprintf("%s %s %s", state.OAuthWorkKind(), state.OAuthJobID(), role),
		Refresh: func(refreshCtx context.Context, provider, tok string) (*TokenResponse, error) {
			resp, rerr := r.refreshToken(refreshCtx, provider, tok)
			if rerr != nil {
				return nil, fmt.Errorf("refresh OAuth token for %s: %w", role, rerr)
			}
			return resp, nil
		},
		OnWait: func(waitCtx context.Context) (string, bool) {
			if state.ReloadOAuthTokens(waitCtx) == nil {
				latest := state.OAuthTokens(role)
				if !force && latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
					if decrypted, decryptErr := crypto.DecryptWithDomain(latest.accessEnc, r.secretKey, crypto.DomainOAuthAccessToken); decryptErr == nil {
						return decrypted, true
					}
				}
			}
			return "", false
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			if err := state.ReloadOAuthTokens(lockCtx); err != nil {
				return "", false, "", fmt.Errorf("reload OAuth token state: %w", err)
			}
			latest := state.OAuthTokens(role)
			decrypted, err := crypto.DecryptWithDomain(latest.accessEnc, r.secretKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				return "", false, "", fmt.Errorf("decrypt %s access token: %w", role, err)
			}
			if force && state.OAuthUpdatedAt().After(originalUpdatedAt) {
				return decrypted, true, "", nil
			}
			if !force && latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
				return decrypted, true, "", nil
			}
			if latest.refreshEnc.Valid && latest.refreshEnc.String != "" && latest.refreshEnc.String != expectedRefreshEnc {
				expectedRefreshEnc = latest.refreshEnc.String
				updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, r.secretKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", fmt.Errorf("decrypt %s refresh token: %w", role, decErr)
				}
				if updatedPlain != "" && updatedPlain != target {
					refresh = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Sink:            state.OAuthTokenSink(role),
		ExpectedRefresh: func() string { return expectedRefreshEnc },
		OnPersisted: func(tokens db.EncryptedOAuthTokens) {
			state.ApplyOAuthTokens(role, tokens.Access, tokens.NullRefresh(), tokens.NullExpiresAt())
		},
		AdoptSuperseded: refresher.AdoptDecryptedWinner(func(persistCtx context.Context) (string, error) {
			if err := state.ReloadOAuthTokens(persistCtx); err != nil {
				return "", err
			}
			return state.OAuthTokens(role).accessEnc, nil
		}),
	})
	if err != nil {
		return "", err
	}
	if res.Adopted {
		return res.AdoptedAccess, nil
	}
	return res.Token.AccessToken, nil
}

type migrationJobTokenState struct {
	store     db.MigrationStore
	migration *db.Migration
}

func (s *migrationJobTokenState) OAuthWorkKind() string     { return "migration" }
func (s *migrationJobTokenState) OAuthJobID() string        { return s.migration.ID }
func (s *migrationJobTokenState) OAuthUserID() string       { return s.migration.UserID.String }
func (s *migrationJobTokenState) OAuthUpdatedAt() time.Time { return s.migration.UpdatedAt }
func (s *migrationJobTokenState) OAuthTokens(role string) jobOAuthTokenSnapshot {
	if role == "source" {
		return jobOAuthTokenSnapshot{s.migration.SourcePasswordEncrypted, s.migration.SourceRefreshTokenEncrypted, s.migration.SourceTokenExpiresAt, s.migration.SourceProvider}
	}
	return jobOAuthTokenSnapshot{s.migration.TargetPasswordEncrypted, s.migration.TargetRefreshTokenEncrypted, s.migration.TargetTokenExpiresAt, s.migration.TargetProvider}
}
func (s *migrationJobTokenState) ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime) {
	if role == "source" {
		s.migration.SourcePasswordEncrypted = access
		s.migration.SourceRefreshTokenEncrypted = refresh
		s.migration.SourceTokenExpiresAt = expires
	} else {
		s.migration.TargetPasswordEncrypted = access
		s.migration.TargetRefreshTokenEncrypted = refresh
		s.migration.TargetTokenExpiresAt = expires
	}
}
func (s *migrationJobTokenState) ReloadOAuthTokens(ctx context.Context) error {
	latest, err := s.store.GetMigrationContext(ctx, s.migration.ID)
	if err != nil {
		return err
	}
	s.ApplyOAuthTokens("source", latest.SourcePasswordEncrypted, latest.SourceRefreshTokenEncrypted, latest.SourceTokenExpiresAt)
	s.ApplyOAuthTokens("target", latest.TargetPasswordEncrypted, latest.TargetRefreshTokenEncrypted, latest.TargetTokenExpiresAt)
	s.migration.UpdatedAt = latest.UpdatedAt
	return nil
}
func (s *migrationJobTokenState) OAuthTokenSink(role string) db.OAuthTokenSink {
	return s.store.TokenSink(s.migration.ID, role)
}

type syncJobTokenState struct {
	db  *sql.DB
	job *db.SyncJob
}

func (s *syncJobTokenState) OAuthWorkKind() string     { return "sync" }
func (s *syncJobTokenState) OAuthJobID() string        { return s.job.ID }
func (s *syncJobTokenState) OAuthUserID() string       { return s.job.UserID }
func (s *syncJobTokenState) OAuthUpdatedAt() time.Time { return s.job.UpdatedAt }
func (s *syncJobTokenState) OAuthTokens(role string) jobOAuthTokenSnapshot {
	if role == "source" {
		return jobOAuthTokenSnapshot{s.job.SourcePasswordEncrypted, s.job.SourceRefreshTokenEncrypted, s.job.SourceTokenExpiresAt, s.job.SourceProvider}
	}
	return jobOAuthTokenSnapshot{s.job.TargetPasswordEncrypted, s.job.TargetRefreshTokenEncrypted, s.job.TargetTokenExpiresAt, s.job.TargetProvider}
}
func (s *syncJobTokenState) ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime) {
	if role == "source" {
		s.job.SourcePasswordEncrypted = access
		s.job.SourceRefreshTokenEncrypted = refresh
		s.job.SourceTokenExpiresAt = expires
	} else {
		s.job.TargetPasswordEncrypted = access
		s.job.TargetRefreshTokenEncrypted = refresh
		s.job.TargetTokenExpiresAt = expires
	}
}
func (s *syncJobTokenState) ReloadOAuthTokens(ctx context.Context) error {
	latest, err := db.GetSyncJobContext(ctx, s.db, s.job.ID)
	if err != nil {
		return err
	}
	s.ApplyOAuthTokens("source", latest.SourcePasswordEncrypted, latest.SourceRefreshTokenEncrypted, latest.SourceTokenExpiresAt)
	s.ApplyOAuthTokens("target", latest.TargetPasswordEncrypted, latest.TargetRefreshTokenEncrypted, latest.TargetTokenExpiresAt)
	s.job.UpdatedAt = latest.UpdatedAt
	return nil
}
func (s *syncJobTokenState) OAuthTokenSink(role string) db.OAuthTokenSink {
	return db.NewSyncJobTokenSink(s.db, s.job.ID, role)
}
