package main

import (
	"database/sql"
	"net/http"

	"backend/internal/db"
)

// profileDependencies contains only the infrastructure required by the
// connection-profile feature module. Cross-cutting request concerns stay owned
// by the root server and are injected as narrow functions.
type profileDependencies struct {
	db            *sql.DB
	profiles      *ownedProfileAccess
	rateLimiter   rateLimiter
	requireUserID func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP      func(*http.Request) string
	writeAudit    func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	logf          func(*http.Request, string, ...any)
}

type profileAPI struct {
	profileDependencies
}

func newProfileAPI(deps profileDependencies) *profileAPI {
	if deps.profiles == nil {
		deps.profiles = &ownedProfileAccess{db: deps.db}
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
	return &profileAPI{profileDependencies: deps}
}

func (s *APIServer) connectionProfiles() *profileAPI {
	return newProfileAPI(profileDependencies{
		db:            s.db,
		profiles:      s.profiles(),
		rateLimiter:   s.rateLimiter,
		requireUserID: s.requireUserID,
		clientIP:      s.clientIP,
		writeAudit:    s.writeAudit,
		logf:          s.logf,
	})
}

// routes returns the connection-profile feature's route definitions for gateway wiring.
func (api *profileAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/profiles", access: routeAuthenticated, handler: api.handleListProfiles},
		{pattern: "POST /api/profiles", access: routeAuthenticated, handler: api.handleCreateProfile},
		{pattern: "GET /api/profiles/{id}", access: routeAuthenticated, handler: api.handleGetProfile},
		{pattern: "PUT /api/profiles/{id}", access: routeAuthenticated, handler: api.handleUpdateConnectionProfile},
		{pattern: "DELETE /api/profiles/{id}", access: routeAuthenticated, handler: api.handleDeleteProfile},
		{pattern: "POST /api/profiles/{id}/test", access: routeAuthenticated, handler: api.handleTestProfile},
	}
}

// registerRoutes registers the profile module's routes on mux with the
// supplied authentication middleware wrapping every endpoint.
func (api *profileAPI) registerRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, nil)
}

func (api *profileAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerAuthenticatedRouteSpecs("profile", mux, api.routes(), authenticated, overrideHandler)
}
