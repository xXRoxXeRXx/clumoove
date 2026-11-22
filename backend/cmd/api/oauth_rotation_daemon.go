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
	Name() string
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

func (d *oauthRotationDaemon) Name() string { return "oauth_rotation" }

// failMigrationAfterRefreshFailure marks a migration FAILED only if it is still
// in flight; a migration the user cancelled or that already finished keeps its
// status. Failing to record the outcome is logged, not retried, because the next
// rotation tick re-evaluates the job.
func (d *oauthRotationDaemon) failMigrationAfterRefreshFailure(ctx context.Context, migrationID, provider string) {
	errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", provider)
	if _, err := db.TransitionMigration(ctx, d.db, migrationID, db.MigrationEventFail, &errMsg); err != nil {
		oauthRotationLogger(ctx).ErrorContext(ctx, "oauth_rotation_mark_failed_error", slog.String("job_type", "migration"), slog.String("job_id", migrationID), observability.Error(err))
	}
}

// failSyncJobAfterRefreshFailure is the sync-job counterpart of
// failMigrationAfterRefreshFailure.
func (d *oauthRotationDaemon) failSyncJobAfterRefreshFailure(ctx context.Context, syncJobID, provider string) {
	errMsg := fmt.Sprintf("OAuth token refresh failed (%s)", provider)
	if _, err := db.FailSyncJobIfActive(ctx, d.db, syncJobID, errMsg); err != nil {
		oauthRotationLogger(ctx).ErrorContext(ctx, "oauth_rotation_mark_failed_error", slog.String("job_type", "sync"), slog.String("job_id", syncJobID), observability.Error(err))
	}
}

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
				sink: db.NewMigrationTokenSink(d.db, entry.MigrationID, entry.Role),
				markFailed: func() {
					d.failMigrationAfterRefreshFailure(ctx, entry.MigrationID, entry.Provider)
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
				sink: db.NewSyncJobTokenSink(d.db, entry.SyncJobID, entry.Role),
				markFailed: func() {
					d.failSyncJobAfterRefreshFailure(ctx, entry.SyncJobID, entry.Provider)
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
				sink: db.NewRestoreRunTokenSink(d.db, entry.RestoreRunID),
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
	sink         db.OAuthTokenSink
	markFailed   func()
}

// refresher binds the daemon's shared rotation infrastructure. The queue is the
// distributed token locker when present.
func (d *oauthRotationDaemon) refresher() *oauth.Refresher {
	return oauth.NewRefresher(oauth.RefresherConfig{DB: d.db, Cache: d.cache, Locker: d.queue, EncryptionKey: d.encryptionKey})
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

	var rotatedExpiresAt time.Time
	res, err := d.refresher().Refresh(ctx, oauth.OwnerRefresh{
		UserID:       userID,
		Provider:     spec.provider,
		RefreshToken: refreshToken,
		Label:        spec.jobType + " " + spec.role,
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
		Sink:            spec.sink,
		ExpectedRefresh: func() string { return currentRefreshEnc },
		OnPersisted:     func(tokens db.EncryptedOAuthTokens) { rotatedExpiresAt = tokens.ExpiresAt },
		AdoptSuperseded: func(persistCtx context.Context) (string, bool) {
			logger.InfoContext(persistCtx, "oauth_rotation_update_conflict", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role))
			return "", true
		},
	})
	if err != nil {
		switch {
		case errors.Is(err, oauth.ErrRotatedTokenEncrypt):
			logger.ErrorContext(ctx, "oauth_rotation_encrypt_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		case errors.Is(err, oauth.ErrRotatedTokenPersist):
			logger.ErrorContext(ctx, "oauth_rotation_persist_failed", slog.String("job_type", spec.jobType), slog.String("job_id", spec.jobID), slog.String("role", spec.role), observability.Error(err), slog.String("error_kind", observability.ErrorKind(err)))
		}
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
