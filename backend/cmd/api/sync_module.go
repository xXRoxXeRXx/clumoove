package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"backend/internal/db"
	"backend/internal/oauth"
	"backend/internal/queue"
	appSync "backend/internal/sync"
)

// syncDependencies contains only the infrastructure required by the
// synchronization feature module. Cross-cutting request, profile-access,
// stream-pool, and background concerns are injected at module construction.
type syncDependencies struct {
	db                     *sql.DB
	tasks                  db.TaskStore
	encryptionKey          string
	backgroundCtx          context.Context
	rateLimiter            rateLimiter
	profiles               *ownedProfileAccess
	streamPool             streamSlotPool
	syncPass               appSync.Pass
	publishBandwidthChange func(context.Context, queue.BandwidthEvent) error
	oauthLocker            oauth.TokenLocker
	oauthRotations         *oauth.RotationCache
	streamPollInterval     time.Duration
	requestEnv
}

type syncAPI struct {
	syncDependencies
}

func newSyncAPI(deps syncDependencies) *syncAPI {
	deps.requestEnv = deps.requestEnv.withDefaults("sync")
	if deps.backgroundCtx == nil {
		deps.backgroundCtx = context.Background()
	}
	if deps.oauthRotations == nil {
		if deps.profiles != nil && deps.profiles.oauthRotations != nil {
			deps.oauthRotations = deps.profiles.oauthRotations
		} else {
			deps.oauthRotations = oauth.NewRotationCache()
		}
	}
	if deps.oauthLocker == nil && deps.profiles != nil {
		deps.oauthLocker = deps.profiles.oauthLocker
	}
	if deps.tasks == nil {
		deps.tasks = taskStoreFor(deps.db)
	}
	if deps.profiles == nil {
		deps.profiles = newOwnedProfileAccess(deps.db, deps.encryptionKey, nil, deps.oauthLocker, deps.oauthRotations)
	}
	if deps.streamPool == nil {
		deps.streamPool = newDefaultUserStreamPool()
	}
	if deps.streamPollInterval <= 0 {
		deps.streamPollInterval = 3 * time.Second
	}
	return &syncAPI{syncDependencies: deps}
}

// taskStoreFor returns the PostgreSQL task store, or nil when a module is built
// without a database handle (route-only tests).
func taskStoreFor(database *sql.DB) db.TaskStore {
	if database == nil {
		return nil
	}
	return db.NewTaskStore(database)
}

func (s *APIServer) syncs() *syncAPI {
	var publishBandwidth func(context.Context, queue.BandwidthEvent) error
	var oauthLocker oauth.TokenLocker
	if s.queue != nil {
		publishBandwidth = s.queue.PublishBandwidthChange
		oauthLocker = s.queue
	}
	return newSyncAPI(syncDependencies{
		requestEnv:             s.requestEnv(),
		db:                     s.db,
		tasks:                  taskStoreFor(s.db),
		encryptionKey:          s.encryptionKey,
		backgroundCtx:          s.backgroundCtx,
		rateLimiter:            s.rateLimiter,
		profiles:               s.profiles(),
		streamPool:             s.streamPool(),
		syncPass:               s.syncPass,
		publishBandwidthChange: publishBandwidth,
		oauthLocker:            oauthLocker,
		oauthRotations:         s.oauthRotations.get(),
	})
}

// routes returns the synchronization feature's route definitions for gateway wiring.
func (api *syncAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/sync", access: routeAuthenticated, handler: api.handleListSyncs},
		{pattern: "GET /api/sync/stream", access: routeAuthenticated, handler: api.handleSyncStream},
		{pattern: "POST /api/sync", access: routeAuthenticated, handler: api.handleCreateSync},
		{pattern: "GET /api/sync/{id}", access: routeAuthenticated, handler: api.handleGetSyncStatus},
		{pattern: "POST /api/sync/{id}/start", access: routeAuthenticated, handler: api.handleStartSync},
		{pattern: "POST /api/sync/{id}/pause", access: routeAuthenticated, handler: api.handlePauseSync},
		{pattern: "POST /api/sync/{id}/resume", access: routeAuthenticated, handler: api.handleResumeSync},
		{pattern: "POST /api/sync/{id}/reauth", access: routeAuthenticated, handler: api.handleSyncReauth},
		{pattern: "DELETE /api/sync/{id}", access: routeAuthenticated, handler: api.handleDeleteSync},
		{pattern: "GET /api/sync/{id}/report", access: routeAuthenticated, handler: api.handleDownloadSyncReport},
		{pattern: "GET /api/sync/{id}/errors", access: routeAuthenticated, handler: api.handleSyncErrors},
		{pattern: "PUT /api/sync/{id}/threads", access: routeAuthenticated, handler: api.handleSetSyncThreads},
		{pattern: "PUT /api/sync/{id}/bandwidth", access: routeAuthenticated, handler: api.handleSetSyncBandwidth},
		{pattern: "GET /api/sync/{id}/browse", access: routeAuthenticated, handler: api.handleBrowseSyncJob},
		{pattern: "POST /api/sync/{id}/mkdir", access: routeAuthenticated, handler: api.handleSyncMkdir},
		{pattern: "PUT /api/sync/{id}/schedule", access: routeAuthenticated, handler: api.handleUpdateSyncSchedule},
		{pattern: "PUT /api/sync/{id}/scope", access: routeAuthenticated, handler: api.handleUpdateSyncScope},
	}
}

// registerRoutes registers the synchronization module's routes on mux with the
// supplied authentication middleware wrapping protected endpoints.
func (api *syncAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, nil)
}

func (api *syncAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerRouteSpecs(mux, api.routes(), authenticated, nil, nil, overrideHandler)
}

func (api *syncAPI) allowRateLimit(r *http.Request, scope string, limit int, window time.Duration) bool {
	if api.rateLimiter == nil {
		return false
	}
	return api.rateLimiter.Allow(r.Context(), scope, api.clientIP(r), limit, window)
}

func (api *syncAPI) acquireSyncStream(w http.ResponseWriter, r *http.Request, userID string) bool {
	return acquireStreamLease(w, r, api.rateLimiter, api.clientIP(r), api.streamPool, userID, "sync-stream")
}

func (api *syncAPI) releaseSyncStream(userID string) {
	if api.streamPool != nil {
		api.streamPool.Release(userID)
	}
}

// requireOwnedSync loads the sync job with one owner-scoped query. A missing job
// and a job belonging to another account get the same response, which prevents
// sync IDs being used for account enumeration while keeping database failures
// distinguishable.
//
// The returned row is a snapshot: it must not be used to guard a state
// transition. Status guards belong in the UPDATE statement itself.
func (api *syncAPI) requireOwnedSync(w http.ResponseWriter, r *http.Request, id, userID string) (*db.SyncJob, bool) {
	job, err := db.GetOwnedSyncJobContext(r.Context(), api.db, id, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, ErrSyncNotFound)
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return nil, false
	}
	return job, true
}

// requireSyncOwnership is the existence-only variant of requireOwnedSync for
// handlers that never read the row. It shares the same error contract.
func (api *syncAPI) requireSyncOwnership(w http.ResponseWriter, r *http.Request, id, userID string) bool {
	owned, err := db.VerifySyncJobOwnershipContext(r.Context(), api.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return false
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrSyncNotFound)
		return false
	}
	return true
}
