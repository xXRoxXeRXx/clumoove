package main

import (
	"context"
	"database/sql"
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
	encryptionKey          string
	backgroundCtx          context.Context
	rateLimiter            rateLimiter
	profiles               *ownedProfileAccess
	streamPool             streamSlotPool
	syncPass               appSync.Pass
	publishBandwidthChange func(context.Context, queue.BandwidthEvent) error
	oauthLocker            oauth.TokenLocker
	oauthRotations         *oauth.RotationCache
	requireUserID          func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP               func(*http.Request) string
	writeAudit             func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf                   func(*http.Request, string, ...any)
	warnf                  func(*http.Request, string, ...any)
	streamPollInterval     time.Duration
}

type syncAPI struct {
	syncDependencies
}

func newSyncAPI(deps syncDependencies) *syncAPI {
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
	if deps.profiles == nil {
		deps.profiles = &ownedProfileAccess{
			db:             deps.db,
			encryptionKey:  deps.encryptionKey,
			oauthRotations: deps.oauthRotations,
			oauthLocker:    deps.oauthLocker,
		}
	}
	if deps.streamPool == nil {
		deps.streamPool = newUserStreamPool(maxStreamsPerUser)
	}
	if deps.requireUserID == nil {
		deps.requireUserID = requireAuthenticatedUserID
	}
	if deps.clientIP == nil {
		deps.clientIP = (&APIServer{}).clientIP
	}
	if deps.writeAudit == nil {
		deps.writeAudit = func(*http.Request, db.AuditAction, string, string, map[string]interface{}) {}
	}
	if deps.logf == nil {
		deps.logf = (&APIServer{}).logf
	}
	if deps.warnf == nil {
		deps.warnf = (&APIServer{}).warnf
	}
	if deps.streamPollInterval <= 0 {
		deps.streamPollInterval = 3 * time.Second
	}
	return &syncAPI{syncDependencies: deps}
}

func (s *APIServer) syncs() *syncAPI {
	var publishBandwidth func(context.Context, queue.BandwidthEvent) error
	var oauthLocker oauth.TokenLocker
	if s.queue != nil {
		publishBandwidth = s.queue.PublishBandwidthChange
		oauthLocker = s.queue
	}
	return newSyncAPI(syncDependencies{
		db:                     s.db,
		encryptionKey:          s.encryptionKey,
		backgroundCtx:          s.backgroundCtx,
		rateLimiter:            s.rateLimiter,
		profiles:               s.profiles(),
		streamPool:             s.streamPool(),
		syncPass:               s.syncPass,
		publishBandwidthChange: publishBandwidth,
		oauthLocker:            oauthLocker,
		oauthRotations:         s.rotationCache(),
		requireUserID:          s.requireUserID,
		clientIP:               s.clientIP,
		writeAudit:             s.writeAudit,
		logf:                   s.logf,
		warnf:                  s.warnf,
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

// requireSyncOwnership deliberately maps a missing job and a job belonging to
// another account to the same response. This prevents sync IDs being used for
// account enumeration while keeping database failures distinguishable.
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
