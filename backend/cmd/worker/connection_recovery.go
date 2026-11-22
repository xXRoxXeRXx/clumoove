package main

import (
	"context"
	"time"

	"backend/internal/crypto"
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

type recoveryCandidate struct {
	id, userID, sourceURL, sourceUsername, sourcePassword, targetURL, targetUsername, targetPassword, sourceProvider, targetProvider string
	updatedAt                                                                                                                        time.Time
}

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

func (m *workerMaintenance) probeCandidateConnections(ctx context.Context, c recoveryCandidate) bool {
	sPass, err := crypto.DecryptWithDomain(c.sourcePassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.sourceProvider)))
	if err != nil {
		return false
	}
	tPass, err := crypto.DecryptWithDomain(c.targetPassword, m.secretKey, crypto.ConnectionCredentialDomain(oauth.IsProvider(c.targetProvider)))
	if err != nil {
		return false
	}

	userCtx := storage.WithLocalUserScope(ctx, c.userID)
	sClient, err := storage.NewProvider(userCtx, c.sourceProvider, c.sourceURL, c.sourceUsername, sPass)
	if err != nil {
		return false
	}
	tClient, err := storage.NewProvider(userCtx, c.targetProvider, c.targetURL, c.targetUsername, tPass)
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
	query := `
		SELECT id, COALESCE(user_id, ''), source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM migrations
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`
	m.recoverPausedEntities(ctx, false, query, maxMigrationRecoveryProbesPerTick, func(candidate recoveryCandidate) bool {
		workerLogf("maintenance", "[RecoveryScheduler] Connection restored for migration %s! Resuming...\n", candidate.id)
		recovered, needsDiscovery, recoverErr := m.migrations.RecoverConnectionLostMigration(candidate.id)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error resuming migration %s: %v\n", candidate.id, recoverErr)
			return false
		}
		if !recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Did not resume migration %s because its status changed", candidate.id)
			return false
		}
		if needsDiscovery && m.indexer != nil {
			m.recoveredIndexers.Add(1)
			id := candidate.id
			go func() {
				defer m.recoveredIndexers.Done()
				m.indexer.Start(ctx, id)
			}()
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedSyncJobs(ctx context.Context) {
	query := `
		SELECT id, user_id, source_url, source_username, source_password_encrypted,
		       target_url, target_username, target_password_encrypted,
		       source_provider, target_provider, updated_at
		FROM sync_jobs
		WHERE status = 'PAUSED_CONNECTION_LOSS'
		ORDER BY (id::text <= $1), id
		LIMIT $2
	`
	m.recoverPausedEntities(ctx, true, query, maxSyncRecoveryProbesPerTick, func(candidate recoveryCandidate) bool {
		recovered, recoverErr := m.syncs.RecoverConnectionLostSyncJob(ctx, candidate.id)
		if recoverErr != nil {
			workerLogf("maintenance", "[RecoveryScheduler] Error recovering sync job %s: %v\n", candidate.id, recoverErr)
			return false
		}
		if recovered {
			workerLogf("maintenance", "[RecoveryScheduler] Connection restored for sync job %s; scheduled API retry\n", candidate.id)
		}
		return true
	})
}

func (m *workerMaintenance) recoverPausedEntities(
	ctx context.Context,
	syncJob bool,
	query string,
	maxProbes int,
	recoverFn func(candidate recoveryCandidate) bool,
) {
	candidates, err := m.queryRecoveryCandidates(ctx, query, m.recoveryCursor(syncJob))
	if err != nil {
		return
	}

	probes := 0
	lastExamined := ""
	for _, candidate := range candidates {
		if !examineRecoveryCandidate(&lastExamined, probes, maxProbes, candidate.id) {
			break
		}
		key := recoveryAttemptKey(syncJob, candidate.id)
		ra := m.effectiveRecoveryState(key, candidate.updatedAt)
		if !shouldProbeRecovery(ra, time.Now()) {
			continue
		}
		probes++

		if !m.probeCandidateConnections(ctx, candidate) {
			m.recordRecoveryFailure(syncJob, candidate.id, ra.attempts, candidate.updatedAt)
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

func (m *workerMaintenance) queryRecoveryCandidates(ctx context.Context, query, cursor string) ([]recoveryCandidate, error) {
	rows, err := m.db.QueryContext(ctx, query, cursor, recoveryScanWindow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []recoveryCandidate
	for rows.Next() {
		var candidate recoveryCandidate
		if err := rows.Scan(
			&candidate.id, &candidate.userID,
			&candidate.sourceURL, &candidate.sourceUsername, &candidate.sourcePassword,
			&candidate.targetURL, &candidate.targetUsername, &candidate.targetPassword,
			&candidate.sourceProvider, &candidate.targetProvider, &candidate.updatedAt,
		); err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		workerLogf("maintenance", "[RecoveryScheduler] rows error: %v\n", err)
		return nil, err
	}
	return candidates, nil
}
