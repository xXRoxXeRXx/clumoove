package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"backend/internal/db"
	"backend/internal/queue"
)

// migrationDependencies contains only the infrastructure required by the
// migration feature module. Cross-cutting request, profile-access, stream-pool,
// and background concerns are injected at module construction.
type migrationDependencies struct {
	db                     *sql.DB
	store                  db.MigrationStore
	encryptionKey          string
	backgroundCtx          context.Context
	rateLimiter            rateLimiter
	profiles               *ownedProfileAccess
	streamPool             streamSlotPool
	publishCancelEvent     func(context.Context, string) error
	publishBandwidthChange func(context.Context, queue.BandwidthEvent) error
	startIndexer           func(context.Context, string)
	logfContext            func(context.Context, string, ...any)
	requestEnv
}

// migrationStoreFor returns the PostgreSQL migration store, or nil when a module
// is built without a database handle (route-only tests).
func migrationStoreFor(database *sql.DB) db.MigrationStore {
	if database == nil {
		return nil
	}
	return db.NewMigrationStore(database)
}

type migrationAPI struct {
	migrationDependencies
}

func newMigrationAPI(deps migrationDependencies) *migrationAPI {
	deps.requestEnv = deps.requestEnv.withDefaults("migration")
	if deps.backgroundCtx == nil {
		deps.backgroundCtx = context.Background()
	}
	if deps.store == nil {
		deps.store = migrationStoreFor(deps.db)
	}
	if deps.profiles == nil {
		deps.profiles = newOwnedProfileAccess(deps.db, deps.encryptionKey, nil, nil, nil)
	}
	if deps.streamPool == nil {
		deps.streamPool = newDefaultUserStreamPool()
	}
	if deps.logfContext == nil {
		deps.logfContext = handlerLogfContext
	}
	return &migrationAPI{migrationDependencies: deps}
}

func (s *APIServer) migrations() *migrationAPI {
	var publishCancel func(context.Context, string) error
	var publishBandwidth func(context.Context, queue.BandwidthEvent) error
	if s.queue != nil {
		publishCancel = s.queue.PublishCancelEvent
		publishBandwidth = s.queue.PublishBandwidthChange
	}
	var startIndexer func(context.Context, string)
	if s.indexer != nil {
		startIndexer = s.indexer.Start
	}
	return newMigrationAPI(migrationDependencies{
		requestEnv:             s.requestEnv(),
		db:                     s.db,
		store:                  migrationStoreFor(s.db),
		encryptionKey:          s.encryptionKey,
		backgroundCtx:          s.backgroundCtx,
		rateLimiter:            s.rateLimiter,
		profiles:               s.profiles(),
		streamPool:             s.streamPool(),
		publishCancelEvent:     publishCancel,
		publishBandwidthChange: publishBandwidth,
		startIndexer:           startIndexer,
		logfContext:            s.logfContext,
	})
}

// routes returns the migration feature's route definitions for gateway wiring.
func (api *migrationAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/migration", access: routeAuthenticated, handler: api.handleListMigrations},
		{pattern: "GET /api/migration/stream", access: routeAuthenticated, handler: api.handleMigrationStream},
		{pattern: "POST /api/migration/connect", access: routeAuthenticated, handler: api.handleConnect},
		{pattern: "POST /api/migration/connect/test", access: routeAuthenticated, handler: api.handleConnectTest},
		{pattern: "POST /api/migration/browse", access: routeAuthenticated, handler: api.handleBrowse},
		{pattern: "POST /api/migration/target/browse", access: routeAuthenticated, handler: api.handleTargetBrowse},
		{pattern: "POST /api/migration/target/mkdir", access: routeAuthenticated, handler: api.handleTargetMkdir},
		{pattern: "POST /api/migration/start", access: routeAuthenticated, handler: api.handleStart},
		{pattern: "GET /api/migration/{id}", access: routeAuthenticated, handler: api.handleGetStatus},
		{pattern: "GET /api/migration/{id}/stream", access: routeAuthenticated, handler: api.handleMigrationDetailStream},
		{pattern: "POST /api/migration/{id}/pause", access: routeAuthenticated, handler: api.handlePause},
		{pattern: "POST /api/migration/{id}/resume", access: routeAuthenticated, handler: api.handleResume},
		{pattern: "POST /api/migration/{id}/cancel", access: routeAuthenticated, handler: api.handleCancel},
		{pattern: "DELETE /api/migration/{id}", access: routeAuthenticated, handler: api.handleDeleteMigration},
		{pattern: "GET /api/migration/{id}/report", access: routeAuthenticated, handler: api.handleDownloadReport},
		{pattern: "GET /api/migration/{id}/errors", access: routeAuthenticated, handler: api.handleMigrationErrors},
		{pattern: "POST /api/migration/{id}/retry-failed", access: routeAuthenticated, handler: api.handleRetryFailed},
		{pattern: "POST /api/migration/{id}/reauth", access: routeAuthenticated, handler: api.handleMigrationReauth},
		{pattern: "POST /api/migration/{id}/reindex", access: routeAuthenticated, handler: api.handleReindex},
		{pattern: "PUT /api/migration/{id}/bandwidth", access: routeAuthenticated, handler: api.handleSetBandwidth},
		{pattern: "PUT /api/migration/{id}/threads", access: routeAuthenticated, handler: api.handleSetThreads},
	}
}

// registerRoutes registers the migration module's routes on mux with the
// supplied authentication middleware wrapping protected endpoints.
func (api *migrationAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, nil)
}

func (api *migrationAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerRouteSpecs(mux, api.routes(), authenticated, nil, nil, overrideHandler)
}

func (api *migrationAPI) allowRateLimit(r *http.Request, scope string, limit int, window time.Duration) bool {
	if api.rateLimiter == nil {
		return false
	}
	return api.rateLimiter.Allow(r.Context(), scope, api.clientIP(r), limit, window)
}

// requireOwnedMigration loads the migration with one owner-scoped query and
// writes the error response itself when it cannot return it. A missing and a
// foreign migration are indistinguishable (403 MIGRATION_NOT_OWNED), and a
// lookup failure is reported the same way.
//
// The returned row is a snapshot: it must not be used to guard a state
// transition. Status guards belong in the UPDATE statement itself.
func (api *migrationAPI) requireOwnedMigration(w http.ResponseWriter, r *http.Request) (*db.Migration, string, bool) {
	return api.loadOwnedMigrationRow(w, r, false)
}

// requireOwnedMigrationStrict behaves like requireOwnedMigration but reports a
// lookup failure as 500 INTERNAL_ERROR instead of masking it as not-owned.
func (api *migrationAPI) requireOwnedMigrationStrict(w http.ResponseWriter, r *http.Request) (*db.Migration, string, bool) {
	return api.loadOwnedMigrationRow(w, r, true)
}

func (api *migrationAPI) loadOwnedMigrationRow(w http.ResponseWriter, r *http.Request, internalErrorOnLookupFailure bool) (*db.Migration, string, bool) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return nil, "", false
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return nil, "", false
	}

	mig, err := api.store.GetOwnedMigrationContext(r.Context(), id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
			return nil, "", false
		}
		if internalErrorOnLookupFailure {
			api.logf(r, "Error checking migration %s ownership: %v", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return nil, "", false
		}
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return nil, "", false
	}

	return mig, userID, true
}

// lookupOwnedMigrationDetail is the loader behind the read endpoints (detail,
// report, detail stream), whose contract differs from requireOwnedMigration: a
// missing migration is 404 MIGRATION_NOT_FOUND, a foreign one 403
// MIGRATION_NOT_OWNED, and a lookup failure 500 INTERNAL_ERROR.
//
// The row is fetched with the owner-scoped query, so another account's data is
// never loaded. Only when that finds nothing does a row-less existence check
// pick between 404 and 403.
func (api *migrationAPI) lookupOwnedMigrationDetail(w http.ResponseWriter, r *http.Request, id, userID, errorLabel string) (*db.Migration, bool) {
	mig, err := api.store.GetOwnedMigrationContext(r.Context(), id, userID)
	if err == nil {
		return mig, true
	}
	if errors.Is(err, sql.ErrNoRows) {
		exists, existsErr := api.store.MigrationExistsContext(r.Context(), id)
		if existsErr == nil {
			if exists {
				writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
			} else {
				writeError(w, http.StatusNotFound, ErrMigrationNotFound)
			}
			return nil, false
		}
		err = existsErr
	}
	if errorLabel != "" {
		api.logf(r, "%s %s: %v\n", errorLabel, id, err)
	}
	writeError(w, http.StatusInternalServerError, ErrInternalError)
	return nil, false
}

// migrationGarbageCollector owns the hourly deletion of old terminal migrations
// and their task histories.
type migrationGarbageCollector struct {
	store        db.MigrationStore
	loglnContext func(context.Context, ...any)
	logfContext  func(context.Context, string, ...any)
}

func (gc *migrationGarbageCollector) Name() string { return "migration_gc" }

func (gc *migrationGarbageCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			gc.loglnContext(ctx, "Running Garbage Collector for old migrations...")
			count, err := gc.store.DeleteOldMigrations()
			if err != nil {
				gc.logfContext(ctx, "Garbage Collector error: %v\n", err)
			} else if count > 0 {
				gc.logfContext(ctx, "Garbage Collector cleaned up %d old migrations & task histories.\n", count)
			}
		}
	}
}
