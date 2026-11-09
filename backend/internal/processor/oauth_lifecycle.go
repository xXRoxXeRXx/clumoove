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
	OAuthTokens(role string) oauthTokenSnapshot
	OAuthUpdatedAt() time.Time
	ApplyOAuthTokens(role, access string, refresh sql.NullString, expires sql.NullTime)
	ReloadOAuthTokens(context.Context) error
	PersistOAuthTokens(ctx context.Context, role, access, refresh string, expires time.Time, expectedRefresh string) error
}

type oauthRefreshLocker interface {
	TryClaimOAuthLock(context.Context, string, string, string, time.Duration) (string, bool, error)
	ReleaseOAuthLock(context.Context, string, string, string, string) error
}

func (p *Processor) refreshToken(ctx context.Context, provider, refresh string) (*oauth.TokenResponse, error) {
	if p.oauthRefresh != nil {
		return p.oauthRefresh(ctx, provider, refresh)
	}
	return oauth.RefreshToken(ctx, provider, refresh)
}

func (p *Processor) refreshLocker() oauthRefreshLocker {
	if p.oauthRefreshLock != nil {
		return p.oauthRefreshLock
	}
	return p.queue
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
	release := p.refreshLocks.lock(fmt.Sprintf("%s:%s:%s", state.OAuthWorkKind(), state.OAuthJobID(), role))
	defer release()
	var lockToken string
	if locker := p.refreshLocker(); locker != nil {
		var claimed bool
		var err error
		for attempt := 0; attempt < 15; attempt++ {
			lockToken, claimed, err = locker.TryClaimOAuthLock(ctx, state.OAuthWorkKind(), state.OAuthJobID(), role, 30*time.Second)
			if err == nil && claimed {
				break
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if state.ReloadOAuthTokens(ctx) == nil {
				latest := state.OAuthTokens(role)
				if latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
					if decrypted, decryptErr := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); decryptErr == nil {
						return decrypted, nil
					}
				}
			}
		}
		if lockToken == "" || !claimed {
			return "", fmt.Errorf("lock contention: unable to claim OAuth refresh lock for %s %s (%s)", state.OAuthWorkKind(), state.OAuthJobID(), role)
		}
		defer locker.ReleaseOAuthLock(ctx, state.OAuthWorkKind(), state.OAuthJobID(), role, lockToken)
	}
	if err := state.ReloadOAuthTokens(ctx); err != nil {
		return "", fmt.Errorf("reload OAuth token state: %w", err)
	}
	latest := state.OAuthTokens(role)
	decrypted, err := crypto.DecryptWithDomain(latest.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("decrypt %s access token: %w", role, err)
	}
	access = decrypted
	if force && state.OAuthUpdatedAt().After(originalUpdatedAt) {
		return access, nil
	}
	if !force && latest.expiresAt.Valid && time.Now().Before(latest.expiresAt.Time.Add(-2*time.Minute)) {
		return access, nil
	}

	refresh, err := crypto.DecryptWithDomain(latest.refreshEnc.String, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("decrypt %s refresh token: %w", role, err)
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	response, err := p.refreshToken(refreshCtx, latest.provider, refresh)
	cancel()
	if err != nil {
		return "", fmt.Errorf("refresh OAuth token for %s: %w", role, err)
	}
	accessEnc, err := crypto.EncryptWithDomain(response.AccessToken, p.secretKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		return "", fmt.Errorf("encrypt OAuth access token: %w", err)
	}
	refreshEnc, err := crypto.EncryptWithDomain(response.RefreshToken, p.secretKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		return "", fmt.Errorf("encrypt OAuth refresh token: %w", err)
	}
	expiresIn := response.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expires := time.Now().Add(time.Duration(expiresIn) * time.Second)
	if err := state.PersistOAuthTokens(ctx, role, accessEnc, refreshEnc, expires, latest.refreshEnc.String); err != nil {
		if errors.Is(err, db.ErrOAuthTokenConflict) && state.ReloadOAuthTokens(ctx) == nil {
			winner := state.OAuthTokens(role)
			if decrypted, decryptErr := crypto.DecryptWithDomain(winner.accessEnc, p.secretKey, crypto.DomainOAuthAccessToken); decryptErr == nil {
				return decrypted, nil
			}
		}
		return "", fmt.Errorf("persist OAuth tokens: %w", err)
	}
	state.ApplyOAuthTokens(role, accessEnc, sql.NullString{String: refreshEnc, Valid: true}, sql.NullTime{Time: expires, Valid: true})
	return response.AccessToken, nil
}

type migrationOAuthTokenState struct {
	p         *Processor
	migration *db.Migration
}

func (s *migrationOAuthTokenState) OAuthWorkKind() string     { return "migration" }
func (s *migrationOAuthTokenState) OAuthJobID() string        { return s.migration.ID }
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
