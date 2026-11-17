package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/observability"
	"backend/internal/queue"
)

// backgroundDaemon is a long-running loop owned by a feature module. The
// gateway composes daemons and main.go starts them under the process context.
type backgroundDaemon interface {
	name() string
	Run(ctx context.Context)
}

// oauthRotationDaemon owns proactive OAuth refresh-token rotation for active
// migrations, sync jobs, and restore runs.
type oauthRotationDaemon struct {
	db            *sql.DB
	queue         *queue.Queue
	encryptionKey string
	cache         *oauth.RotationCache
}

func (d *oauthRotationDaemon) name() string { return "oauth_rotation" }

func (d *oauthRotationDaemon) Run(ctx context.Context) {
	logger := oauthRotationLogger(ctx)
	logger.InfoContext(ctx, "oauth_rotation_daemon_started")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "oauth_rotation_daemon_stopped")
			return
		case <-ticker.C:
			d.rotateExpiringOAuthTokens(ctx)
		}
	}
}

func oauthRotationLogger(ctx context.Context) *slog.Logger {
	return observability.Logger(ctx).With(slog.String("component", "oauth_rotation"))
}

// shouldSkipOAuthTokenRotation checks whether a token's expiry still lies sufficiently
// far in the future (at least 5 minutes) that rotation by the background daemon should
// be skipped, for example because a worker recently performed an inline refresh.
func shouldSkipOAuthTokenRotation(expiresAt time.Time, now time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return now.Before(expiresAt.Add(-5 * time.Minute))
}

// isOAuthRotationAlreadyResolved detects whether a concurrent worker has already
// persisted a new refresh token in the database, meaning that an invalid_grant /
// rejected refresh token was caused by rotation race rather than real revocation.
func isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEnc string) bool {
	return freshRefreshEnc != "" && freshRefreshEnc != currentRefreshEnc
}

func (d *oauthRotationDaemon) rotateExpiringOAuthTokens(ctx context.Context) {
	logger := oauthRotationLogger(ctx)
	expiringMig, err := db.GetExpiringOAuthMigrations(d.db)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "migration"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
	}

	expiringSync, errSync := db.GetExpiringOAuthSyncJobs(d.db)
	if errSync != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "sync"), observability.Error(errSync), slog.String("error_kind", observability.ErrorKind(errSync)))
	}

	expiringRestore, errRestore := db.GetExpiringOAuthRestoreRuns(d.db)
	if errRestore != nil {
		logger.ErrorContext(ctx, "oauth_rotation_scan_failed", slog.String("job_type", "restore"), observability.Error(errRestore), slog.String("error_kind", observability.ErrorKind(errRestore)))
	}

	if err == nil {
		for _, entry := range expiringMig {
			entry := entry
			d.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "migration",
				jobID:    entry.MigrationID,
				role:     entry.Role,
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					mig, loadErr := db.GetMigrationContext(loadCtx, d.db, entry.MigrationID)
					if loadErr != nil {
						return "", "", time.Time{}, false, "", loadErr
					}
					active := mig.Status == "RUNNING" || mig.Status == "INDEXING"
					var refreshEnc string
					var expiresAt time.Time
					if entry.Role == "source" {
						refreshEnc = mig.SourceRefreshTokenEncrypted.String
						if mig.SourceTokenExpiresAt.Valid {
							expiresAt = mig.SourceTokenExpiresAt.Time
						}
					} else {
						refreshEnc = mig.TargetRefreshTokenEncrypted.String
						if mig.TargetTokenExpiresAt.Valid {
							expiresAt = mig.TargetTokenExpiresAt.Time
						}
					}
					return mig.UserID.String, refreshEnc, expiresAt, active, mig.Status, nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateMigrationOAuthTokensContext(persistCtx, d.db, db.OAuthTokenUpdate{
						MigrationID:           entry.MigrationID,
						Role:                  entry.Role,
						AccessTokenEncrypted:  accessEnc,
						RefreshTokenEncrypted: refreshEnc,
						ExpiresAt:             expiresAt,
					}, expectedRefreshEnc)
				},
				markFailed: func() {
					errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", entry.Provider)
					_ = db.UpdateMigrationStatus(d.db, entry.MigrationID, "FAILED", &errMsg)
				},
			})
		}
	}

	if errSync == nil {
		for _, entry := range expiringSync {
			entry := entry
			d.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "sync",
				jobID:    entry.SyncJobID,
				role:     entry.Role,
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					job, loadErr := db.GetSyncJobContext(loadCtx, d.db, entry.SyncJobID)
					if loadErr != nil {
						return "", "", time.Time{}, false, "", loadErr
					}
					active := job.Status == "RUNNING" || job.Status == "ACTIVE"
					var refreshEnc string
					var expiresAt time.Time
					if entry.Role == "source" {
						refreshEnc = job.SourceRefreshTokenEncrypted.String
						if job.SourceTokenExpiresAt.Valid {
							expiresAt = job.SourceTokenExpiresAt.Time
						}
					} else {
						refreshEnc = job.TargetRefreshTokenEncrypted.String
						if job.TargetTokenExpiresAt.Valid {
							expiresAt = job.TargetTokenExpiresAt.Time
						}
					}
					return job.UserID, refreshEnc, expiresAt, active, job.Status, nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateSyncJobOAuthTokensContext(persistCtx, d.db, entry.SyncJobID, entry.Role, accessEnc, refreshEnc, expiresAt, expectedRefreshEnc)
				},
				markFailed: func() {
					errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", entry.Provider)
					_ = db.UpdateSyncJobStatus(d.db, entry.SyncJobID, "FAILED", &errMsg)
				},
			})
		}
	}

	if errRestore == nil {
		for _, entry := range expiringRestore {
			entry := entry
			d.rotateExpiringOAuthJob(ctx, logger, expiringOAuthJobSpec{
				jobType:  "restore",
				jobID:    entry.RestoreRunID,
				role:     "target",
				provider: entry.Provider,
				loadSnapshot: func(loadCtx context.Context) (string, string, time.Time, bool, string, error) {
					_, refreshEnc, expiresAt, loadErr := db.GetRestoreRunCredentialSnapshotContext(loadCtx, d.db, entry.RestoreRunID)
					if loadErr != nil {
						return entry.UserID, "", time.Time{}, false, "", loadErr
					}
					var exp time.Time
					if expiresAt.Valid {
						exp = expiresAt.Time
					}
					return entry.UserID, refreshEnc.String, exp, true, "", nil
				},
				persist: func(persistCtx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error {
					return db.UpdateRestoreRunOAuthTokens(persistCtx, d.db, entry.RestoreRunID, accessEnc, refreshEnc, expiresAt, expectedRefreshEnc)
				},
			})
		}
	}

	// Reconcile dormant Redis rotation backups after active token refreshes so
	// large backlogs or slow DB writes never delay active OAuth jobs.
	if d.queue != nil {
		repairCtx, cancelRepair := context.WithTimeout(ctx, 5*time.Second)
		repaired, repairErr := oauth.RepairRotationBackups(repairCtx, d.db, d.queue)
		cancelRepair()
		if repairErr != nil {
			logger.ErrorContext(ctx, "oauth_rotation_backup_repair_failed", observability.Error(repairErr), slog.String("error_kind", observability.ErrorKind(repairErr)))
		} else if repaired > 0 {
			logger.InfoContext(ctx, "oauth_rotation_backups_repaired", slog.Int("repaired_count", repaired))
		}
	}
}

type expiringOAuthJobSpec struct {
	jobType      string
	jobID        string
	role         string
	provider     string
	loadSnapshot func(ctx context.Context) (userID, refreshEnc string, expiresAt time.Time, active bool, status string, err error)
	persist      func(ctx context.Context, accessEnc, refreshEnc string, expiresAt time.Time, expectedRefreshEnc string) error
	markFailed   func()
}

func (d *oauthRotationDaemon) rotateExpiringOAuthJob(ctx context.Context, logger *slog.Logger, spec expiringOAuthJobSpec) {
	userID, currentRefreshEnc, currentExpiresAt, active, status, err := spec.loadSnapshot(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_fetch_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	if !active {
		logger.DebugContext(ctx, fmt.Sprintf("oauth_rotation_%s_not_active", spec.jobType), slog.String("job_id", spec.jobID), slog.String("status", status))
		return
	}
	if currentRefreshEnc == "" {
		return
	}
	if shouldSkipOAuthTokenRotation(currentExpiresAt, time.Now()) {
		logger.DebugContext(ctx, "oauth_rotation_already_fresh", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
		return
	}

	refreshToken, err := crypto.DecryptWithDomain(currentRefreshEnc, d.encryptionKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_decrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		return
	}
	defer crypto.ZeroString(&refreshToken)

	var locker oauth.TokenLocker
	if d.queue != nil {
		locker = d.queue
	}

	var rotatedExpiresAt time.Time
	res, err := oauth.RefreshWithRotationLock(ctx, oauth.RotationRefreshRequest{
		DB:            d.db,
		Cache:         d.cache,
		Locker:        locker,
		EncryptionKey: d.encryptionKey,
		UserID:        userID,
		Provider:      spec.provider,
		RefreshToken:  refreshToken,
		Refresh: func(refreshCtx context.Context, prov, tok string) (*oauth.TokenResponse, error) {
			tCtx, cancel := context.WithTimeout(refreshCtx, 15*time.Second)
			defer cancel()
			return oauth.RefreshToken(tCtx, prov, tok)
		},
		OnLocked: func(lockCtx context.Context, target string) (string, bool, string, error) {
			_, latestRefreshEnc, latestExpiresAt, latestActive, _, lerr := spec.loadSnapshot(lockCtx)
			if lerr != nil {
				return "", false, "", nil
			}
			if !latestActive || latestRefreshEnc == "" || shouldSkipOAuthTokenRotation(latestExpiresAt, time.Now()) {
				return "", true, "", nil
			}
			if latestRefreshEnc != currentRefreshEnc {
				currentRefreshEnc = latestRefreshEnc
				updatedPlain, decErr := crypto.DecryptWithDomain(currentRefreshEnc, d.encryptionKey, crypto.DomainOAuthRefreshToken)
				if decErr != nil {
					return "", false, "", decErr
				}
				if updatedPlain != "" && updatedPlain != target {
					refreshToken = updatedPlain
					return "", false, updatedPlain, nil
				}
			}
			return "", false, "", nil
		},
		Persist: func(persistCtx context.Context, _ string, tokenResp *oauth.TokenResponse) (string, bool, error) {
			newAccessEnc, err := crypto.EncryptWithDomain(tokenResp.AccessToken, d.encryptionKey, crypto.DomainOAuthAccessToken)
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_encrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("token_type", "access"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			newRefreshEnc, err := crypto.EncryptWithDomain(tokenResp.RefreshToken, d.encryptionKey, crypto.DomainOAuthRefreshToken)
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_encrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("token_type", "refresh"), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			expiresIn := tokenResp.ExpiresIn
			if expiresIn <= 0 {
				expiresIn = 3600
			}
			rotatedExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
			err = spec.persist(persistCtx, newAccessEnc, newRefreshEnc, rotatedExpiresAt, currentRefreshEnc)
			if errors.Is(err, db.ErrOAuthTokenConflict) {
				logger.InfoContext(persistCtx, "oauth_rotation_update_conflict", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
				return "", true, nil
			}
			if err != nil {
				logger.ErrorContext(persistCtx, "oauth_rotation_persist_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
				return "", false, err
			}
			return "", false, nil
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "oauth_rotation_refresh_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), slog.String("provider", spec.provider), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		if errors.Is(err, oauth.ErrRefreshTokenInvalid) && spec.markFailed != nil {
			if _, freshRefreshEnc, _, _, _, ferr := spec.loadSnapshot(ctx); ferr == nil && isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEnc) {
				logger.InfoContext(ctx, "oauth_rotation_already_resolved", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
				return
			}
			spec.markFailed()
		}
		return
	}
	if res.Adopted {
		return
	}
	logger.InfoContext(ctx, "oauth_rotation_completed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), slog.String("provider", spec.provider), slog.Time("expires_at", rotatedExpiresAt))
}
