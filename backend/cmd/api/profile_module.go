package main

import (
	"database/sql"
	"net/http"
)

// profileDependencies contains only the infrastructure required by the
// connection-profile feature module. Cross-cutting request concerns stay owned
// by the root server and are injected as narrow functions.
type profileDependencies struct {
	db            *sql.DB
	encryptionKey string
	profiles      *ownedProfileAccess
	rateLimiter   rateLimiter
	requestEnv
}

type profileAPI struct {
	profileDependencies
}

func newProfileAPI(deps profileDependencies) *profileAPI {
	deps.requestEnv = deps.requestEnv.withDefaults("profile")
	if deps.profiles == nil {
		deps.profiles = &ownedProfileAccess{db: deps.db, encryptionKey: deps.encryptionKey}
	}
	return &profileAPI{profileDependencies: deps}
}

func (s *APIServer) connectionProfiles() *profileAPI {
	return newProfileAPI(profileDependencies{
		requestEnv:    s.requestEnv(),
		db:            s.db,
		encryptionKey: s.encryptionKey,
		profiles:      s.profiles(),
		rateLimiter:   s.rateLimiter,
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

func (api *profileAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerAuthenticatedRouteSpecs("profile", mux, api.routes(), authenticated, overrideHandler)
}
