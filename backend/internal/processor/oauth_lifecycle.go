package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
)

type oauthTokenSnapshot struct {
	accessEnc  string
	refreshEnc sql.NullString
	expiresAt  sql.NullTime
	provider   string
}

// oauthTokenState keeps work-kind-specific database persistence outside the
// refresh lifecycle. It deliberately exposes ciphertext only.
type oauthTokenState interface {
	OAuthWorkKind() string
	OAuthJobID() string
	OAuthUserID() string
	OAuthTokens(role string) oauthTokenSnapshot
	OAuthUpdatedAt() time.Time
	ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime)
	ReloadOAuthTokens(context.Context) error
	PersistOAuthTokens(ctx context.Context, role, access, refresh string, expires time.Time, expectedRefresh string) error
}

type oauthRefreshLocker = oauth.TokenLocker

func (p *Processor) refreshToken(ctx context.Context, provider, refresh string) (*oauth.TokenResponse, error) {
	if p.oauthRefresh != nil {
		return p.oauthRefresh(ctx, provider, refresh)
	}
	return oauth.RefreshToken(ctx, provider, refresh)
}

func (p *Processor) refreshLocker() oauth.TokenLocker {
	if p.oauthRefreshLock != nil {
		return p.oauthRefreshLock
	}
	if p.queue != nil {
		return p.queue
	}
	return nil
}

func (p *Processor) refreshOAuthTokenIfNeeded(ctx context.Context, migration *db.Migration, role, access string, force bool) (string, error) {
	return p.refreshOAuthLifecycle(ctx, &migrationOAuthTokenState{p: p, migration: migration}, role, access, force)
}

func (p *Processor) refreshSyncOAuthTokenIfNeeded(ctx context.Context, job *db.SyncJob, role, access string, force bool) (string, error) {
	return p.refreshOAuthLifecycle(ctx, &syncOAuthTokenState{p: p, job: job}, role, access, force)
}

func (p *Processor) ensureFreshSyncOAuthToken(ctx context.Context, job *db.SyncJob, role, access string) (string, error) {
	return p.refreshSyncOAuthTokenIfNeeded(ctx, job, role, access, false)
}

func (p *Processor) refreshSyncOAuthToken(ctx context.Context, job *db.SyncJob, role string) (string, error) {
	return p.refreshSyncOAuthTokenIfNeeded(ctx, job, role, "", true)
}

func (p *Processor) refreshOAuthLifecycle(ctx context.Context, state oauthTokenState, role, access string, force bool) (string, error) {
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
	refresh, err := crypto.DecryptWithDomain(expectedRefreshEnc, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt %s refresh token: %w", role, err)
	}
	defer crypto.ZeroString(&refresh)

	// Hold the job-role lock alongside the token lock so concurrent force-refreshes
	// on the same job serialize cleanly even if the underlying token rotates.
	jobRelease := p.refreshLocks.lock(fmt.Sprintf("%s:%s:%s", state.OAuthWorkKind(), state.OAuthJobID(), role))
	defer jobRelease()

	res, err := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
		DB:            p.db,
		Locker:        p.refreshLocker(),
		EncryptionKey: p.secretKey,
		UserID:        state.OAuthUserID(),
		Provider:      initial.provider,
		RefreshToken:  refresh,
		Refresh: func(refreshCtx context.Context, provider, tok string) (*oauth.TokenResponse, error) {
			tCtx, cancel := context.WithTimeout(refreshCtx, 15*time.Second)
			defer cancel()
			resp, rerr := p.refreshToken(tCtx, provider, tok)
			if rerr != nil {
				return nil, fmt.Errorf("refresh OAuth token for %s: %w", role, rerr)
			}
			return resp, nil
		},
		OnWait: func(waitCtx context.Context) (string, bool) {
			if state.ReloadOAuthTokens(waitCtx) == nil {
				latest := state.OAuthTokens(role)
				if !force && latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
					if decrypted, decryptErr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); decryptErr == nil {
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
			decrypted, err := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken)
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
				updatedPlain, decErr := crypto.DecryptWithDomain(expectedRefreshEnc, p.secretKey, crypto.DomainOAuthRefreshToken)
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
		Persist: func(persistCtx context.Context, _ string, response *oauth.TokenResponse) (string, bool, error) {
			accessEnc, err := crypto.EncryptWithDomain(response.AccessToken, p.secretKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				return "", false, fmt.Errorf("encrypt OAuth access token: %w", err)
			}
			refreshEnc, err := crypto.EncryptWithDomain(response.RefreshToken, p.secretKey, crypto.DomainOAuthRefreshToken)
			if err != nil {
				return "", false, fmt.Errorf("encrypt OAuth refresh token: %w", err)
			}
			expiresIn := response.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			expires := time.Now().Add(time.Duration(expiresIn) * time.Second)
			if err := state.PersistOAuthTokens(persistCtx, role, accessEnc, refreshEnc, expires, expectedRefreshEnc); err != nil {
				if errors.Is(err, db.ErrOAuthTokenConflict) && state.ReloadOAuthTokens(persistCtx) == nil {
					winner := state.OAuthTokens(role)
					if decrypted, decryptErr := crypto.DecryptWithDomain(winner.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); decryptErr == nil {
						return decrypted, true, nil
					}
				}
				return "", false, fmt.Errorf("persist OAuth tokens: %w", err)
			}
			state.ApplyOAuthTokens(role, accessEnc, sql.NullString{String: refreshEnc, Valid: true}, sql.NullTime{Time: expires, Valid: true})
			return "", false, nil
		},
	})
	if err != nil {
		return "", err
	}
	if res.Adopted {
		return res.AdoptedAccess, nil
	}
	return res.Token.AccessToken, nil
}

type migrationOAuthTokenState struct {
	p         *Processor
	migration *db.Migration
}

func (s *migrationOAuthTokenState) OAuthWorkKind() string     { return "migration" }
func (s *migrationOAuthTokenState) OAuthJobID() string        { return s.migration.ID }
func (s *migrationOAuthTokenState) OAuthUserID() string       { return s.migration.UserID.String }
func (s *migrationOAuthTokenState) OAuthUpdatedAt() time.Time { return s.migration.UpdatedAt }
func (s *migrationOAuthTokenState) OAuthTokens(role string) oauthTokenSnapshot {
	if role == "source" {
		return oauthTokenSnapshot{s.migration.SourcePasswordEncrypted, s.migration.SourceRefreshTokenEncrypted, s.migration.SourceTokenExpiresAt, s.migration.SourceProvider}
	}
	return oauthTokenSnapshot{s.migration.TargetPasswordEncrypted, s.migration.TargetRefreshTokenEncrypted, s.migration.TargetTokenExpiresAt, s.migration.TargetProvider}
}
func (s *migrationOAuthTokenState) ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime) {
	applyOAuthTokensToMigration(s.migration, role, access, refresh, expires)
}
func (s *migrationOAuthTokenState) ReloadOAuthTokens(context.Context) error {
	latest, err := db.GetMigration(s.p.db, s.migration.ID)
	if err != nil {
		return err
	}
	s.ApplyOAuthTokens("source", latest.SourcePasswordEncrypted, latest.SourceRefreshTokenEncrypted, latest.SourceTokenExpiresAt)
	s.ApplyOAuthTokens("target", latest.TargetPasswordEncrypted, latest.TargetRefreshTokenEncrypted, latest.TargetTokenExpiresAt)
	s.migration.UpdatedAt = latest.UpdatedAt
	return nil
}
func (s *migrationOAuthTokenState) PersistOAuthTokens(_ context.Context, role, access, refresh string, expires time.Time, expected string) error {
	return db.UpdateMigrationOAuthTokens(s.p.db, db.OAuthTokenUpdate{MigrationID: s.migration.ID, Role: role, AccessTokenEncrypted: access, RefreshTokenEncrypted: refresh, ExpiresAt: expires}, expected)
}

type syncOAuthTokenState struct {
	p   *Processor
	job *db.SyncJob
}

func (s *syncOAuthTokenState) OAuthWorkKind() string     { return "sync" }
func (s *syncOAuthTokenState) OAuthJobID() string        { return s.job.ID }
func (s *syncOAuthTokenState) OAuthUserID() string       { return s.job.UserID }
func (s *syncOAuthTokenState) OAuthUpdatedAt() time.Time { return s.job.UpdatedAt }
func (s *syncOAuthTokenState) OAuthTokens(role string) oauthTokenSnapshot {
	if role == "source" {
		return oauthTokenSnapshot{s.job.SourcePasswordEncrypted, s.job.SourceRefreshTokenEncrypted, s.job.SourceTokenExpiresAt, s.job.SourceProvider}
	}
	return oauthTokenSnapshot{s.job.TargetPasswordEncrypted, s.job.TargetRefreshTokenEncrypted, s.job.TargetTokenExpiresAt, s.job.TargetProvider}
}
func (s *syncOAuthTokenState) ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime) {
	applyOAuthTokensToSyncJob(s.job, role, access, refresh, expires)
}
func (s *syncOAuthTokenState) ReloadOAuthTokens(context.Context) error {
	latest, err := db.GetSyncJob(s.p.db, s.job.ID)
	if err != nil {
		return err
	}
	s.ApplyOAuthTokens("source", latest.SourcePasswordEncrypted, latest.SourceRefreshTokenEncrypted, latest.SourceTokenExpiresAt)
	s.ApplyOAuthTokens("target", latest.TargetPasswordEncrypted, latest.TargetRefreshTokenEncrypted, latest.TargetTokenExpiresAt)
	s.job.UpdatedAt = latest.UpdatedAt
	return nil
}
func (s *syncOAuthTokenState) PersistOAuthTokens(_ context.Context, role, access, refresh string, expires time.Time, expected string) error {
	return db.UpdateSyncJobOAuthTokens(s.p.db, s.job.ID, role, access, refresh, expires, expected)
}
