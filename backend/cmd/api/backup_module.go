package main

import (
	"database/sql"
	"net/http"
	"time"

	"backend/internal/db"
)

// backupDependencies contains only the infrastructure required by the
// backup, verification, and restore feature module. Cross-cutting request,
// profile-access, and stream-pool concerns are injected at module construction.
type backupDependencies struct {
	db                 *sql.DB
	encryptionKey      string
	rateLimiter        rateLimiter
	profiles           *ownedProfileAccess
	streamPool         streamSlotPool
	requireUserID      func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP           func(*http.Request) string
	writeAudit         func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf               func(*http.Request, string, ...any)
	streamPollInterval time.Duration
}

type backupAPI struct {
	backupDependencies
}

func newBackupAPI(deps backupDependencies) *backupAPI {
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
	return &backupAPI{backupDependencies: deps}
}

func (s *APIServer) backups() *backupAPI {
	return newBackupAPI(backupDependencies{
		db:            s.db,
		encryptionKey: s.encryptionKey,
		rateLimiter:   s.rateLimiter,
		profiles:      s.profiles(),
		streamPool:    s.streamPool(),
		requireUserID: s.requireUserID,
		clientIP:      s.clientIP,
		writeAudit:    s.writeAudit,
		logf:          s.logf,
	})
}

// routes returns the backup, verification, and restore feature's route
// definitions for gateway wiring.
func (api *backupAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/backup", access: routeAuthenticated, handler: api.handleListBackups},
		{pattern: "GET /api/backup/stream", access: routeAuthenticated, handler: api.handleBackupStream},
		{pattern: "POST /api/backup", access: routeAuthenticated, handler: api.handleCreateBackup},
		{pattern: "GET /api/backup/{id}", access: routeAuthenticated, handler: api.handleGetBackup},
		{pattern: "PUT /api/backup/{id}", access: routeAuthenticated, handler: api.handleUpdateBackup},
		{pattern: "GET /api/backup/{id}/runs", access: routeAuthenticated, handler: api.handleListBackupRuns},
		{pattern: "POST /api/backup/{id}/run", access: routeAuthenticated, handler: api.handleRunBackup},
		{pattern: "POST /api/backup/{id}/verify", access: routeAuthenticated, handler: api.handleCreateBackupVerify},
		{pattern: "GET /api/backup/{id}/verify", access: routeAuthenticated, handler: api.handleListBackupVerifies},
		{pattern: "GET /api/backup/{id}/verify/{verifyID}/stream", access: routeAuthenticated, handler: api.handleBackupVerifyStream},
		{pattern: "GET /api/backup/{id}/verify/{verifyID}", access: routeAuthenticated, handler: api.handleGetBackupVerify},
		{pattern: "POST /api/backup/{id}/verify/{verifyID}/cancel", access: routeAuthenticated, handler: api.handleCancelBackupVerify},
		{pattern: "POST /api/backup/{id}/pause", access: routeAuthenticated, handler: api.handlePauseBackup},
		{pattern: "POST /api/backup/{id}/resume", access: routeAuthenticated, handler: api.handleResumeBackup},
		{pattern: "DELETE /api/backup/{id}", access: routeAuthenticated, handler: api.handleDeleteBackup},
		{pattern: "GET /api/backup/{id}/snapshots", access: routeAuthenticated, handler: api.handleListBackupSnapshots},
		{pattern: "GET /api/backup/{id}/snapshots/{snapshotID}/items", access: routeAuthenticated, handler: api.handleListBackupSnapshotItems},
		{pattern: "POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews", access: routeAuthenticated, handler: api.handleCreateRestorePreview},
		{pattern: "GET /api/restore/previews/{previewID}", access: routeAuthenticated, handler: api.handleGetRestorePreview},
		{pattern: "POST /api/restore/previews/{previewID}/cancel", access: routeAuthenticated, handler: api.handleCancelRestorePreview},
		{pattern: "POST /api/restore/previews/{previewID}/consume", access: routeAuthenticated, handler: api.handleConsumeRestorePreview},
		{pattern: "GET /api/restore", access: routeAuthenticated, handler: api.handleListRestoreRuns},
		{pattern: "GET /api/restore/runs", access: routeAuthenticated, handler: api.handleListRestoreRuns},
		{pattern: "GET /api/restore/runs/{runID}/stream", access: routeAuthenticated, handler: api.handleRestoreRunStream},
		{pattern: "GET /api/restore/runs/{runID}", access: routeAuthenticated, handler: api.handleGetRestoreRun},
		{pattern: "GET /api/restore/runs/{runID}/items", access: routeAuthenticated, handler: api.handleListRestoreRunItems},
		{pattern: "POST /api/restore/runs/{runID}/cancel", access: routeAuthenticated, handler: api.handleCancelRestoreRun},
		{pattern: "GET /api/restore/runs/{runID}/report", access: routeAuthenticated, handler: api.handleDownloadRestoreReport},
		{pattern: "DELETE /api/restore/jobs/{jobID}", access: routeAuthenticated, handler: api.handleDeleteRestoreJob},
	}
}

// registerRoutes registers the backup, verification, and restore module's
// routes on mux with the supplied authentication middleware wrapping protected endpoints.
func (api *backupAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, nil)
}

func (api *backupAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerRouteSpecs(mux, api.routes(), authenticated, nil, nil, overrideHandler)
}

func (api *backupAPI) allowRateLimit(r *http.Request, scope string, limit int, window time.Duration) bool {
	if api.rateLimiter == nil {
		return false
	}
	return api.rateLimiter.Allow(r.Context(), scope, api.clientIP(r), limit, window)
}

func (api *backupAPI) acquireBackupStream(w http.ResponseWriter, r *http.Request, userID, rateLimitScope string) bool {
	return acquireStreamLease(w, r, api.rateLimiter, api.clientIP(r), api.streamPool, userID, rateLimitScope)
}

func (api *backupAPI) releaseBackupStream(userID string) {
	if api.streamPool != nil {
		api.streamPool.Release(userID)
	}
}

func (api *backupAPI) listStreamPollInterval() time.Duration {
	if api.streamPollInterval > 0 {
		return api.streamPollInterval
	}
	return 3 * time.Second
}

func (api *backupAPI) detailStreamPollInterval() time.Duration {
	if api.streamPollInterval > 0 {
		return api.streamPollInterval
	}
	return 2 * time.Second
}

// requireBackupOwnership deliberately maps a missing job and a job belonging to
// another account to the same response. This prevents backup IDs being used for
// account enumeration while keeping database failures distinguishable.
func (api *backupAPI) requireBackupOwnership(w http.ResponseWriter, r *http.Request, id, userID string) bool {
	owned, err := db.VerifyBackupJobOwnershipContext(r.Context(), api.db, id, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return false
	}
	if !owned {
		writeError(w, http.StatusNotFound, ErrBackupNotFound)
		return false
	}
	return true
}
