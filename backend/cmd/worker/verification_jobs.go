package main

import (
	"context"
	"sync/atomic"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/megasecret"
	"backend/internal/oauth"
)

func (v *checksumVerifier) verifyMigrationChecksums(ctx context.Context, migrationID string, guardAlreadyHeld bool) {
	generation, claimed, err := v.migrations.ClaimMigrationVerification(ctx, migrationID)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Cannot claim verification for migration %s: %v\n", migrationID, err)
		return
	}
	if !claimed {
		return
	}
	mig, err := v.migrations.GetMigration(migrationID)
	if err != nil {
		_ = v.migrations.ReleaseMigrationVerificationLease(context.Background(), migrationID, generation)
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if mig.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(mig.TargetPasswordEncrypted, v.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(mig.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = v.tokenRefresher().EnsureFreshMigrationToken(ctx, mig, "target", targetPass)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to refresh target OAuth token for migration %s: %v\n", migrationID, err)
		_ = v.migrations.ReleaseMigrationVerificationLease(context.Background(), migrationID, generation)
		return
	}
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(mig.TargetProvider, mig.TargetMegaSessionIDEncrypted, mig.TargetMegaMasterKeyEncrypted, v.secretKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to decode target MEGA session for migration %s: %v\n", migrationID, err)
		_ = v.migrations.ReleaseMigrationVerificationLease(context.Background(), migrationID, generation)
		return
	}
	defer clear(targetMegaSession.MasterKey)

	cfg := verificationPassConfig{
		GuardAlreadyHeld: guardAlreadyHeld,
		EntityType:       "Migration",
		EntityID:         migrationID,
		UserID:           mig.UserID.String,
		SourceProvider:   mig.SourceProvider,
		TargetProvider:   mig.TargetProvider,
		TargetURL:        mig.TargetURL,
		TargetUsername:   mig.TargetUsername,
		TargetPassword:   targetPass,
		MegaSession:      targetMegaSession,
		TargetDir:        mig.TargetDir,
		Threads:          mig.Threads,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedTasks(v.db, ctx, migrationID)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return v.migrations.MarkMigrationTaskChecksumVerifiedWhileVerifying(ctx, task.ID, targetHash, generation)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return v.migrations.MarkMigrationTaskChecksumMismatchWhileVerifying(ctx, task, generation)
		},
		ReconcileProgress: func() error {
			_, err := v.migrations.ReconcileMigrationProgressWhileVerifying(migrationID, generation)
			return err
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := v.migrations.RenewMigrationVerificationLease(ctx, migrationID, generation)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := v.migrations.ReleaseMigrationVerificationLease(ctx, migrationID, generation); err != nil {
				workerLogf("verifier", "[VERIFIER] Cannot release verification lease for migration %s: %v\n", migrationID, err)
			}
		},
	}

	v.runPass(ctx, cfg)
}

func (v *checksumVerifier) verifySyncJobChecksums(ctx context.Context, syncJobID string, guardAlreadyHeld bool) {
	verificationGeneration, claimed, err := db.ClaimSyncJobVerification(v.db, ctx, syncJobID)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Cannot claim verification for sync job %s: %v", syncJobID, err)
		return
	}
	if !claimed {
		return
	}
	job, err := db.GetSyncJob(v.db, syncJobID)
	if err != nil || job.Status != "VERIFYING" {
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	var leaseOwned atomic.Bool
	leaseOwned.Store(true)

	targetPass := ""
	if job.TargetPasswordEncrypted != "" {
		dec, err := crypto.DecryptWithDomain(job.TargetPasswordEncrypted, v.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(job.TargetProvider)))
		if err == nil {
			targetPass = dec
		}
	}
	targetPass, err = v.tokenRefresher().EnsureFreshSyncToken(ctx, job, "target", targetPass)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to refresh target OAuth token for sync job %s: %v\n", syncJobID, err)
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	targetMegaSession, err := megasecret.DecodeProviderMegaSession(job.TargetProvider, job.TargetMegaSessionIDEncrypted, job.TargetMegaMasterKeyEncrypted, v.secretKey)
	if err != nil {
		workerLogf("verifier", "[VERIFIER] Failed to decode target MEGA session for sync job %s: %v\n", syncJobID, err)
		if releaseErr := db.ReleaseSyncJobVerificationLease(v.db, context.Background(), syncJobID, verificationGeneration); releaseErr != nil {
			workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, releaseErr)
		}
		return
	}
	defer clear(targetMegaSession.MasterKey)

	cfg := verificationPassConfig{
		GuardAlreadyHeld: guardAlreadyHeld,
		EntityType:       "Sync job",
		EntityID:         syncJobID,
		UserID:           job.UserID,
		SourceProvider:   job.SourceProvider,
		TargetProvider:   job.TargetProvider,
		TargetURL:        job.TargetURL,
		TargetUsername:   job.TargetUsername,
		TargetPassword:   targetPass,
		MegaSession:      targetMegaSession,
		TargetDir:        job.TargetDir,
		GetTasks: func(ctx context.Context) ([]*db.Task, error) {
			return db.GetUnverifiedCompletedSyncTasks(v.db, ctx, syncJobID, job.RunGeneration)
		},
		MarkVerified: func(ctx context.Context, task *db.Task, targetHash string) (bool, error) {
			return db.MarkSyncTaskChecksumVerifiedWhileVerifying(v.db, ctx, task.ID, targetHash, job.RunGeneration, verificationGeneration)
		},
		MarkMismatch: func(ctx context.Context, task *db.Task) (bool, error) {
			return db.MarkSyncTaskChecksumMismatchWhileVerifying(v.db, ctx, task, job.RunGeneration, verificationGeneration)
		},
		ReconcileProgress: func() error {
			return nil
		},
		RenewLease: func(ctx context.Context) (bool, error) {
			owned, err := db.RenewSyncJobVerificationLease(v.db, ctx, syncJobID, job.RunGeneration, verificationGeneration)
			leaseOwned.Store(owned && err == nil)
			return owned, err
		},
		CanWrite: leaseOwned.Load,
		Release: func(ctx context.Context) {
			if err := db.ReleaseSyncJobVerificationLease(v.db, ctx, syncJobID, verificationGeneration); err != nil {
				workerLogf("verifier", "[VERIFIER] Cannot release verification lease for sync job %s: %v", syncJobID, err)
			}
		},
	}

	v.runPass(ctx, cfg)
}
