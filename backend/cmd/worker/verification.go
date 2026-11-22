package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"backend/internal/db"
	"backend/internal/oauth"
)

type verificationWork struct {
	key string
	run func(context.Context)
}

// checksumVerifier schedules and executes post-transfer checksum/size
// verification passes for migrations and sync jobs in VERIFYING state.
type checksumVerifier struct {
	db                  *sql.DB
	migrations          db.MigrationStore
	tasks               db.TaskStore
	syncs               db.SyncStore
	secretKey           string
	oauthRefresher      *oauth.JobTokenRefresher
	providerSlots       chan struct{}
	verificationWorkers int
	migrationQueue      chan verificationWork
	syncQueue           chan verificationWork
	verifyingEntities   sync.Map
	verificationWG      sync.WaitGroup
}

func verificationWorkerCount(maxThreads int) int {
	if maxThreads < 1 {
		maxThreads = 1
	}
	return min(4, maxThreads)
}

func newChecksumVerifier(database *sql.DB, secretKey string, maxThreads int, providerSlots chan struct{}, refresher *oauth.JobTokenRefresher) *checksumVerifier {
	if maxThreads < 1 {
		maxThreads = 1
	}
	if refresher == nil && database != nil {
		refresher = oauth.NewJobTokenRefresher(database, secretKey, nil)
	}
	var migrations db.MigrationStore
	var tasks db.TaskStore
	var syncs db.SyncStore
	if database != nil {
		migrations = db.NewMigrationStore(database)
		tasks = db.NewTaskStore(database)
		syncs = db.NewSyncStore(database)
	}
	return &checksumVerifier{
		db:                  database,
		migrations:          migrations,
		tasks:               tasks,
		syncs:               syncs,
		secretKey:           secretKey,
		oauthRefresher:      refresher,
		providerSlots:       providerSlots,
		verificationWorkers: verificationWorkerCount(maxThreads),
		migrationQueue:      make(chan verificationWork, maxThreads*2),
		syncQueue:           make(chan verificationWork, maxThreads*2),
	}
}

func (v *checksumVerifier) tokenRefresher() *oauth.JobTokenRefresher {
	if v.oauthRefresher != nil {
		return v.oauthRefresher
	}
	return oauth.NewJobTokenRefresher(v.db, v.secretKey, nil)
}

func (v *checksumVerifier) acquireProviderSlot(ctx context.Context) bool {
	if v.providerSlots == nil {
		return true
	}
	select {
	case v.providerSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (v *checksumVerifier) releaseProviderSlot() {
	if v.providerSlots == nil {
		return
	}
	<-v.providerSlots
}

// Run starts the bounded verification dispatcher, polls for VERIFYING
// migrations and sync jobs until ctx is cancelled, and waits for all in-flight
// verification passes to finish before returning.
func (v *checksumVerifier) Run(ctx context.Context) error {
	v.startDispatcher(ctx)

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			v.verificationWG.Wait()
			return nil
		case <-ticker.C:
			if v.db != nil {
				v.processVerifyingMigrations(ctx)
				v.processVerifyingSyncJobs(ctx)
			}
		}
	}
}

// dequeue alternates preference when both entity queues have work so a long
// migration verification backlog cannot starve waiting sync passes.
func (v *checksumVerifier) dequeue(ctx context.Context, preferSync bool) (verificationWork, bool) {
	preferred, alternate := v.migrationQueue, v.syncQueue
	if preferSync {
		preferred, alternate = alternate, preferred
	}
	select {
	case work := <-preferred:
		return work, true
	default:
	}
	select {
	case work := <-alternate:
		return work, true
	default:
	}
	select {
	case <-ctx.Done():
		return verificationWork{}, false
	case work := <-preferred:
		return work, true
	case work := <-alternate:
		return work, true
	}
}

func (v *checksumVerifier) startDispatcher(ctx context.Context) {
	for i := 0; i < v.verificationWorkers; i++ {
		v.verificationWG.Add(1)
		go func() {
			defer v.verificationWG.Done()
			preferSync := false
			for {
				work, ok := v.dequeue(ctx, preferSync)
				if !ok {
					return
				}
				preferSync = !preferSync
				if !v.acquireProviderSlot(ctx) {
					v.verifyingEntities.Delete(work.key)
					return
				}
				work.run(ctx)
				v.releaseProviderSlot()
				v.verifyingEntities.Delete(work.key)
			}
		}()
	}
}

func verificationGuardKey(entityType, entityID string) string {
	normalized := strings.ToLower(strings.TrimSpace(entityType))
	if normalized == "sync job" {
		normalized = "sync"
	}
	return fmt.Sprintf("%s:%s", normalized, entityID)
}

func (v *checksumVerifier) schedule(ctx context.Context, entityType, entityID string, run func(context.Context)) {
	key := verificationGuardKey(entityType, entityID)
	if _, loaded := v.verifyingEntities.LoadOrStore(key, true); loaded {
		return
	}
	work := verificationWork{key: key, run: run}
	queue := v.migrationQueue
	if entityType == "sync" {
		queue = v.syncQueue
	}
	select {
	case <-ctx.Done():
		v.verifyingEntities.Delete(key)
	case queue <- work:
	default:
		// Keep discovery non-blocking; the next tick retries when capacity frees.
		v.verifyingEntities.Delete(key)
	}
}

func (v *checksumVerifier) processVerifyingMigrations(ctx context.Context) {
	query := `SELECT id FROM migrations WHERE status = 'VERIFYING'`
	rows, err := v.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	var migIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			migIDs = append(migIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		workerLogf("verifier", "[VERIFIER] Listing verifying migrations: %v", err)
		return
	}

	for _, migID := range migIDs {
		v.schedule(ctx, "migration", migID, func(passCtx context.Context) {
			v.verifyMigrationChecksums(passCtx, migID, true)
		})
	}
}

func (v *checksumVerifier) processVerifyingSyncJobs(ctx context.Context) {
	query := `SELECT id FROM sync_jobs WHERE status = 'VERIFYING'`
	rows, err := v.db.QueryContext(ctx, query)
	if err != nil {
		return
	}
	defer rows.Close()

	var syncIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			syncIDs = append(syncIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		workerLogf("verifier", "[VERIFIER] Listing verifying sync jobs: %v", err)
		return
	}

	for _, syncID := range syncIDs {
		v.schedule(ctx, "sync", syncID, func(passCtx context.Context) {
			v.verifySyncJobChecksums(passCtx, syncID, true)
		})
	}
}
