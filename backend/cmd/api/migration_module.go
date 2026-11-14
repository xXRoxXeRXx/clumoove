package main

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"backend/internal/db"
	"backend/internal/queue"
)

type streamSlotPool interface {
	Acquire(userID string) bool
	Release(userID string)
}

// migrationDependencies contains only the infrastructure required by the
// migration feature module. Cross-cutting request, profile-access, stream-pool,
// and background concerns are injected at module construction.
type migrationDependencies struct {
	db                     *sql.DB
	encryptionKey          string
	backgroundCtx          context.Context
	rateLimiter            rateLimiter
	profiles               *ownedProfileAccess
	streamPool             streamSlotPool
	publishCancelEvent     func(context.Context, string) error
	publishBandwidthChange func(context.Context, queue.BandwidthEvent) error
	startIndexer           func(context.Context, string)
	requireUserID          func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP               func(*http.Request) string
	writeAudit             func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf                   func(*http.Request, string, ...any)
	logfContext            func(context.Context, string, ...any)
	infof                  func(*http.Request, string, ...any)
}

type migrationAPI struct {
	migrationDependencies
}

func newMigrationAPI(deps migrationDependencies) *migrationAPI {
	if deps.backgroundCtx == nil {
		deps.backgroundCtx = context.Background()
	}
	if deps.profiles == nil {
		deps.profiles = &ownedProfileAccess{
			db:            deps.db,
			encryptionKey: deps.encryptionKey,
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
	if deps.logfContext == nil {
		deps.logfContext = (&APIServer{}).logfContext
	}
	if deps.infof == nil {
		deps.infof = (&APIServer{}).infof
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
		db:                     s.db,
		encryptionKey:          s.encryptionKey,
		backgroundCtx:          s.backgroundCtx,
		rateLimiter:            s.rateLimiter,
		profiles:               s.profiles(),
		streamPool:             s.streamPool(),
		publishCancelEvent:     publishCancel,
		publishBandwidthChange: publishBandwidth,
		startIndexer:           startIndexer,
		requireUserID:          s.requireUserID,
		clientIP:               s.clientIP,
		writeAudit:             s.writeAudit,
		logf:                   s.logf,
		logfContext:            s.logfContext,
		infof:                  s.infof,
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

func (api *migrationAPI) requireOwnedMigration(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrMigrationIdMissing)
		return "", "", false
	}

	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return "", "", false
	}

	owns, err := db.VerifyMigrationOwnershipContext(r.Context(), api.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return "", "", false
	}
	if !owns {
		writeError(w, http.StatusForbidden, ErrMigrationNotOwned)
		return "", "", false
	}

	return id, userID, true
}
