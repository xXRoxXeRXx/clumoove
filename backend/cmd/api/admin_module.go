package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// adminDependencies contains only the infrastructure required by the
// administration feature module: instance settings, administrative user
// management, instance SMTP and OAuth configuration, global statistics, and the
// audit log. Cross-cutting request helpers are injected at module construction.
type adminDependencies struct {
	db             *sql.DB
	encryptionKey  string
	cancelSyncJob  func(context.Context, string) error
	getRedirectURI func(*http.Request) string
	requestEnv
}

type adminAPI struct {
	adminDependencies
}

func newAdminAPI(deps adminDependencies) *adminAPI {
	deps.requestEnv = deps.requestEnv.withDefaults("admin")
	if deps.cancelSyncJob == nil {
		deps.cancelSyncJob = func(context.Context, string) error {
			return errors.New("sync cancellation unavailable")
		}
	}
	if deps.getRedirectURI == nil {
		deps.getRedirectURI = defaultRedirectURI
	}
	return &adminAPI{adminDependencies: deps}
}

func (s *APIServer) administration() *adminAPI {
	return newAdminAPI(adminDependencies{
		requestEnv:    s.requestEnv(),
		db:            s.db,
		encryptionKey: s.encryptionKey,
		cancelSyncJob: func(ctx context.Context, syncJobID string) error {
			return s.syncPass.Cancel(ctx, syncJobID)
		},
		getRedirectURI: s.getRedirectURI,
	})
}

// routes returns the administration feature's route definitions for gateway
// wiring. Instance settings are readable publicly (the login and setup screens
// need them); every mutation and every /api/admin or audit route requires both
// a current JWT and the ADMIN role.
func (api *adminAPI) routes() []routeSpec {
	return []routeSpec{
		// Instance settings.
		{pattern: "GET /api/settings", access: routePublic, handler: api.handleGetSettings},
		{pattern: "PUT /api/settings", access: routeAdmin, handler: api.handleUpdateSetting},

		// User management.
		{pattern: "POST /api/admin/users", access: routeAdmin, handler: api.handleAdminCreateUser},
		{pattern: "POST /api/admin/users/{id}/suspend", access: routeAdmin, handler: api.handleAdminSuspendUser},
		{pattern: "POST /api/admin/users/{id}/reactivate", access: routeAdmin, handler: api.handleAdminReactivateUser},
		{pattern: "DELETE /api/admin/users/{id}", access: routeAdmin, handler: api.handleAdminDeleteUser},
		{pattern: "PUT /api/admin/users/{id}/role", access: routeAdmin, handler: api.handleAdminUpdateRole},
		{pattern: "GET /api/admin/users", access: routeAdmin, handler: api.handleAdminListUsers},

		// Instance-wide visibility.
		{pattern: "GET /api/admin/stats", access: routeAdmin, handler: api.handleAdminStats},
		{pattern: "GET /api/admin/migrations", access: routeAdmin, handler: api.handleAdminListMigrations},
		{pattern: "GET /api/admin/syncs", access: routeAdmin, handler: api.handleAdminListSyncs},
		{pattern: "GET /api/audit/log", access: routeAdmin, handler: api.handleAdminAuditLog},

		// Instance SMTP and OAuth configuration.
		{pattern: "GET /api/admin/settings/smtp", access: routeAdmin, handler: api.handleAdminGetSMTP},
		{pattern: "PUT /api/admin/settings/smtp", access: routeAdmin, handler: api.handleAdminPutSMTP},
		{pattern: "POST /api/admin/settings/smtp/test", access: routeAdmin, handler: api.handleAdminTestSMTP},
		{pattern: "DELETE /api/admin/settings/smtp", access: routeAdmin, handler: api.handleAdminDeleteSMTP},
		{pattern: "GET /api/admin/settings/oauth", access: routeAdmin, handler: api.handleAdminGetOAuth},
		{pattern: "PUT /api/admin/settings/oauth/{provider}", access: routeAdmin, handler: api.handleAdminPutOAuth},
		{pattern: "DELETE /api/admin/settings/oauth/{provider}", access: routeAdmin, handler: api.handleAdminDeleteOAuth},
	}
}

// registerRoutes registers the administration module's routes on mux. adminOnly
// must authenticate the request and then require the ADMIN role.
func (api *adminAPI) registerRoutes(mux *http.ServeMux, authenticated, adminOnly func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, adminOnly, nil)
}

func (api *adminAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated, adminOnly func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerRouteSpecs(mux, api.routes(), authenticated, nil, adminOnly, overrideHandler)
}
