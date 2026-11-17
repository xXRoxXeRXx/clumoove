package main

import (
	"database/sql"
	"net/http"
)

// scheduleDependencies contains only the infrastructure required by the
// schedule feature module. Cross-cutting request concerns stay owned by the
// root server and are injected as narrow functions.
type scheduleDependencies struct {
	db *sql.DB
	requestEnv
}

type scheduleAPI struct {
	scheduleDependencies
}

func newScheduleAPI(deps scheduleDependencies) *scheduleAPI {
	deps.requestEnv = deps.requestEnv.withDefaults("schedule")
	return &scheduleAPI{scheduleDependencies: deps}
}

func (s *APIServer) schedules() *scheduleAPI {
	return newScheduleAPI(scheduleDependencies{
		requestEnv: s.requestEnv(),
		db:         s.db,
	})
}

// routes returns the schedule feature's route definitions for gateway wiring.
func (api *scheduleAPI) routes() []routeSpec {
	return []routeSpec{
		{pattern: "GET /api/schedule", access: routeAuthenticated, handler: api.handleListSchedules},
		{pattern: "GET /api/schedule/{id}", access: routeAuthenticated, handler: api.handleGetSchedule},
		{pattern: "DELETE /api/schedule/{id}", access: routeAuthenticated, handler: api.handleDeleteSchedule},
	}
}

func (api *scheduleAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerAuthenticatedRouteSpecs("schedule", mux, api.routes(), authenticated, overrideHandler)
}
