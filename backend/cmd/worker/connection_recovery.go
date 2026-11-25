package main

import (
	"context"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/storage"
)

const (
	maxMigrationRecoveryProbesPerTick = 10
	maxSyncRecoveryProbesPerTick      = 10
	recoveryScanWindow                = 30
)

type recoveryState struct {
	lastAttempt     time.Time
	attempts        int
	pausedUpdatedAt time.Time
}

// recoveryCandidateLister pages paused entities for the connection probe; the
// store methods ListPausedConnectionLossMigrations and
// ListPausedConnectionLossSyncJobs fit it.
type recoveryCandidateLister func(ctx context.Context, cursor string, limit int) ([]db.ConnectionRecoveryCandidate, error)

func (m *workerMaintenance) awaitRecoveredIndexers(run func(ctx context.Context)) func(ctx context.Context) {
	return func(ctx context.Context) {
		run(ctx)
		m.recoveredIndexers.Wait()
	}
}

func (m *workerMaintenance) runConnectionRecoveryScheduler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.recoverPausedMigrations(ctx)
			m.recoverPausedSyncJobs(ctx)
		}
	}
}

func (m *workerMaintenance) recoveryCursor(syncJob bool) string {
	m.recoveryCursorMu.Lock()
	defer m.recoveryCursorMu.Unlock()
	if syncJob {
		return m.syncRecoveryCursor
	}
	return m.migrationRecoveryCursor
}

func (m *workerMaintenance) setRecoveryCursor(syncJob bool, id string) {
	m.recoveryCursorMu.Lock()
	defer m.recoveryCursorMu.Unlock()
	if syncJob {
		m.syncRecoveryCursor = id
		return
	}
	m.migrationRecoveryCursor = id
}

func recoveryAttemptKey(syncJob bool, id string) string {
	if syncJob {
		return "sync:" + id
	}
	return "migration:" + id
}

func (m *workerMaintenance) effectiveRecoveryState(key string, updatedAt time.Time) recoveryState {
	if v, ok := m.recoveryAttempts.Load(key); ok {
		state := v.(recoveryState)
		if state.pausedUpdatedAt.Equal(updatedAt) {
			return state
		}
	}
	return recoveryState{pausedUpdatedAt: updatedAt}
}

func (m *workerMaintenance) recordRecoveryFailure(syncJob bool, id string, attempts int, pausedUpdatedAt time.Time) {
	m.recoveryAttempts.Store(recoveryAttemptKey(syncJob, id), recoveryState{
		lastAttempt:     time.Now(),
		attempts:        attempts + 1,
		pausedUpdatedAt: pausedUpdatedAt,
	})
}

func recoveryBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 0:
		return 0
	case attempts == 1:
		return 60 * time.Second
	default:
		return 5 * time.Minute
	}
}

func shouldProbeRecovery(state recoveryState, now time.Time) bool {
	backoff := recoveryBackoff(state.attempts)
	return backoff == 0 || now.Sub(state.lastAttempt) >= backoff
}

func examineRecoveryCandidate(lastExamined *string, probes, budget int, id string) bool {
	if probes >= budget {
		return false
	}
	*lastExamined = id
	return true
}

func (m *workerMaintenance) probeCandidateConnections(ctx context.Context, c db.ConnectionRecoveryCandidate) bool {
	sPass, err := crypto.DecryptWithDomain(c.SourcePassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.SourceProvider)))
	if err != nil {
		return false
	}
	tPass, err := crypto.DecryptWithDomain(c.TargetPassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.TargetProvider)))
	if err != nil {
		return false
	}

	userCtx := storage.WithLocalUserScope(ctx, c.UserID)
	sClient, err := storage.NewProvider(userCtx, c.SourceProvider, c.SourceURL, c.SourceUsername, sPass)
	if err != nil {
		return false
	}
	tClient, err := storage.NewProvider(userCtx, c.TargetProvider, c.TargetURL, c.TargetUsername, tPass)
	if err != nil {
		sClient.Close()
		return false
	}

	connCtx, connCancel := context.WithTimeout(ctx, 15*time.Second)
	sOK, _ := sClient.Connect(connCtx)
	tOK, _ := tClient.Connect(connCtx)
	connCancel()
	sClient.Close()
	tClient.Close()
	return sOK && tOK
}

func (m *workerMaintenance) recoverPausedMigrations(ctx context.Context) {
	m.recoverPausedEntities(ctx, false, m.migrations.ListPausedConnectionLossMigrations, maxMigrationRecoveryProbesPerTick, func(candidate db.ConnectionRecoveryCandidate) bool {
		workerLogf("maintenance", "[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", candidate.ID)
		recovered, needsDiscovery, recoverErr := m.migrations.RecoverConnectionLostMigration(candidate.ID)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error resuming migration %s: %v\n", candidate.ID, recoverErr)
			return false
		}
		if !recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Did not resume migration %s because its status changed", candidate.ID)
			return false
		}
		if needsDiscovery && m.indexer != nil {
			m.recoveredIndexers.Add(1)
			id := candidate.ID
			go func() {
				defer m.recoveredIndexers.Done()
				m.indexer.Start(ctx, id)
			}()
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedSyncJobs(ctx context.Context) {
	m.recoverPausedEntities(ctx, true, m.syncs.ListPausedConnectionLossSyncJobs, maxSyncRecoveryProbesPerTick, func(candidate db.ConnectionRecoveryCandidate) bool {
		recovered, recoverErr := m.syncs.RecoverConnectionLostSyncJob(ctx, candidate.ID)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error recovering sync job %s: %v\n", candidate.ID, recoverErr)
			return false
		}
		if recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Connection restored for sync job %s; scheduled API retry\n", candidate.ID)
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedEntities(
	ctx context.Context,
	syncJob bool,
	list recoveryCandidateLister,
	maxProbes int,
	recoverFn func(candidate db.ConnectionRecoveryCandidate) bool,
) {
	candidates, err := list(ctx, m.recoveryCursor(syncJob), recoveryScanWindow)
	if err != nil {
		// The store returns the candidates it could read, so one unreadable row
		// no longer hides the rest. A prefix of the ordered page is a valid page
		// to examine, and examining past the bad row moves the cursor beyond it.
		workerLogf("maintenance", "[RecoveryScheduler] listing paused entities: %v\n", err)
	}

	probes := 0
	lastExamined := ""
	for _, candidate := range candidates {
		if !examineRecoveryCandidate(&lastExamined, probes, maxProbes, candidate.ID) {
			break
		}
		key := recoveryAttemptKey(syncJob, candidate.ID)
		ra := m.effectiveRecoveryState(key, candidate.UpdatedAt)
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++

		if !m.probeCandidateConnections(ctx, candidate) {
			m.recordRecoveryFailure(syncJob, candidate.ID, ra.attempts, candidate.UpdatedAt)
			continue
		}

		if recoverFn(candidate) {
			m.recoveryAttempts.Delete(key)
		}
	}
	if lastExamined != "" {
		m.setRecoveryCursor(syncJob, lastExamined)
	}
}
