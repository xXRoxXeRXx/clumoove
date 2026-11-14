package main

import (
	"database/sql"
	"net/http"

	"backend/internal/db"
)

// accountDependencies contains only the infrastructure required by the
// account feature module: instance setup, registration, sessions, password and
// email changes, profile settings, OAuth login, and two-factor authentication.
// Cross-cutting request helpers are injected at module construction.
type accountDependencies struct {
	db                *sql.DB
	encryptionKey     string
	jwtSecret         string
	rateLimiter       rateLimiter
	dummyPasswordHash string
	requireUserID     func(http.ResponseWriter, *http.Request) (string, bool)
	clientIP          func(*http.Request) string
	writeAudit        func(*http.Request, db.AuditAction, string, string, map[string]interface{})
	getRedirectURI    func(*http.Request) string
	logf              func(*http.Request, string, ...any)
	infof             func(*http.Request, string, ...any)
	warnf             func(*http.Request, string, ...any)
}

type accountAPI struct {
	accountDependencies
}

func newAccountAPI(deps accountDependencies) *accountAPI {
	defaults := &APIServer{db: deps.db}
	if deps.requireUserID == nil {
		deps.requireUserID = requireAuthenticatedUserID
	}
	if deps.clientIP == nil {
		deps.clientIP = defaults.clientIP
	}
	if deps.writeAudit == nil {
		deps.writeAudit = func(*http.Request, db.AuditAction, string, string, map[string]interface{}) {}
	}
	if deps.getRedirectURI == nil {
		deps.getRedirectURI = defaults.getRedirectURI
	}
	if deps.logf == nil {
		deps.logf = defaults.logf
	}
	if deps.infof == nil {
		deps.infof = defaults.infof
	}
	if deps.warnf == nil {
		deps.warnf = defaults.warnf
	}
	return &accountAPI{accountDependencies: deps}
}

func (s *APIServer) accounts() *accountAPI {
	return newAccountAPI(accountDependencies{
		db:                s.db,
		encryptionKey:     s.encryptionKey,
		jwtSecret:         s.jwtSecret,
		rateLimiter:       s.rateLimiter,
		dummyPasswordHash: s.dummyPasswordHash,
		requireUserID:     s.requireUserID,
		clientIP:          s.clientIP,
		writeAudit:        s.writeAudit,
		getRedirectURI:    s.getRedirectURI,
		logf:              s.logf,
		infof:             s.infof,
		warnf:             s.warnf,
	})
}

// routes returns the account feature's route definitions for gateway wiring.
// Public routes enforce their own protections (rate limits, trusted cookie
// origin, OAuth state, temporary 2FA tokens) inside the handlers.
func (api *accountAPI) routes() []routeSpec {
	return []routeSpec{
		// Instance setup, registration, and sessions.
		{pattern: "GET /api/auth/setup-status", access: routePublic, handler: api.handleGetSetupStatus},
		{pattern: "POST /api/auth/setup-admin", access: routePublic, handler: api.handleSetupAdmin},
		{pattern: "POST /api/auth/register", access: routePublic, handler: api.handleRegister},
		{pattern: "POST /api/auth/login", access: routePublic, handler: api.handleLogin},
		{pattern: "POST /api/auth/totp", access: routePublic, handler: api.handleTOTP},
		{pattern: "POST /api/auth/refresh", access: routePublic, handler: api.handleRefresh},
		{pattern: "POST /api/auth/logout", access: routePublic, handler: api.handleLogout},
		{pattern: "GET /api/auth/me", access: routeAuthenticated, handler: api.handleMe},
		{pattern: "GET /api/auth/sessions", access: routeAuthenticated, handler: api.handleListSessions},
		{pattern: "DELETE /api/auth/sessions/{id}", access: routeAuthenticated, handler: api.handleDeleteSession},

		// Profile settings.
		{pattern: "PUT /api/auth/me", access: routeAuthenticated, handler: api.handleUpdateProfile},
		{pattern: "PUT /api/auth/me/language", access: routeAuthenticated, handler: api.handleUpdateLanguage},
		{pattern: "POST /api/user/avatar", access: routeAuthenticated, handler: api.handleSetAvatar},
		{pattern: "DELETE /api/user/avatar", access: routeAuthenticated, handler: api.handleDeleteAvatar},

		// Password rotation must remain reachable with a must-change token.
		{pattern: "POST /api/auth/change-password", access: routeAllowMustChange, handler: api.handleChangePassword},

		// Two-factor authentication.
		{pattern: "GET /api/auth/2fa/setup", access: routeAuthenticated, handler: api.handle2FASetup},
		{pattern: "POST /api/auth/2fa/enable", access: routeAuthenticated, handler: api.handle2FAEnable},
		{pattern: "POST /api/auth/2fa/disable", access: routeAuthenticated, handler: api.handle2FADisable},
		{pattern: "GET /api/auth/2fa/status", access: routeAuthenticated, handler: api.handle2FAStatus},

		// Password reset and email change.
		{pattern: "GET /api/auth/password-reset-available", access: routePublic, handler: api.handleSMTPFeatureAvailable},
		{pattern: "POST /api/auth/forgot-password", access: routePublic, handler: api.handleForgotPassword},
		{pattern: "POST /api/auth/reset-password", access: routePublic, handler: api.handleResetPassword},
		{pattern: "GET /api/auth/email-change-available", access: routePublic, handler: api.handleSMTPFeatureAvailable},
		{pattern: "POST /api/auth/change-email", access: routeAuthenticated, handler: api.handleChangeEmail},
		{pattern: "POST /api/auth/confirm-email-change", access: routePublic, handler: api.handleConfirmEmailChange},

		// OAuth callbacks use their own state validation.
		{pattern: "GET /api/oauth/auth", access: routePublic, handler: api.handleOAuthAuth},
		{pattern: "GET /api/oauth/callback", access: routePublic, handler: api.handleOAuthCallback},
	}
}

// registerRoutes registers the account module's routes on mux. authenticated
// guards ordinary protected routes; allowMustChange guards password rotation,
// which must remain reachable while a forced password change is pending.
func (api *accountAPI) registerRoutes(mux *http.ServeMux, authenticated, allowMustChange func(http.Handler) http.Handler) {
	api.registerRoutesWithHandler(mux, authenticated, allowMustChange, nil)
}

func (api *accountAPI) registerRoutesWithHandler(mux *http.ServeMux, authenticated, allowMustChange func(http.Handler) http.Handler, overrideHandler http.HandlerFunc) {
	registerRouteSpecs(mux, api.routes(), authenticated, allowMustChange, nil, overrideHandler)
}
