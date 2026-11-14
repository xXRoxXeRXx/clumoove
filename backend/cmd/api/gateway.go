package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"sync"

	"backend/internal/auth"
	"backend/internal/indexer"
	"backend/internal/queue"
	appSync "backend/internal/sync"
)

type routeAccess int

const (
	routePublic routeAccess = iota
	routeAuthenticated
	routeAllowMustChange
	routeAdmin
)

type routeSpec struct {
	pattern string
	access  routeAccess
	handler http.HandlerFunc
}

type gatewayConfig struct {
	DB                *sql.DB
	Queue             *queue.Queue
	Indexer           *indexer.Indexer
	SyncPass          appSync.Pass
	EncryptionKey     string
	JWTSecret         string
	BackgroundCtx     context.Context
	RateLimiter       rateLimiter
	DummyPasswordHash string
	TrustedProxy      bool
	AuthStateLookup   auth.AuthStateLookup
}

func validateGatewayConfig(cfg gatewayConfig) error {
	if cfg.EncryptionKey == "" {
		return errors.New("encryption_key_missing")
	}
	if cfg.JWTSecret == "" {
		return errors.New("jwt_key_missing")
	}
	if subtle.ConstantTimeCompare([]byte(cfg.EncryptionKey), []byte(cfg.JWTSecret)) == 1 {
		return errors.New("keys_equal")
	}
	if len(cfg.JWTSecret) < 32 {
		return errors.New("jwt_key_too_short")
	}
	return nil
}

// newAPIGateway validates gateway configuration and composes the HTTP server
// without starting background daemons or network listeners.
func newAPIGateway(cfg gatewayConfig) (*APIServer, error) {
	if err := validateGatewayConfig(cfg); err != nil {
		return nil, err
	}

	bgCtx := cfg.BackgroundCtx
	if bgCtx == nil {
		bgCtx = context.Background()
	}

	rl := cfg.RateLimiter
	if rl == nil && cfg.Queue != nil {
		rl = &distributedRateLimiter{client: cfg.Queue.RedisClient()}
	}

	idx := cfg.Indexer
	if idx == nil && cfg.DB != nil {
		idx = indexer.NewIndexer(cfg.DB, cfg.EncryptionKey, cfg.Queue)
	}

	return &APIServer{
		db:                cfg.DB,
		queue:             cfg.Queue,
		indexer:           idx,
		syncPass:          cfg.SyncPass,
		encryptionKey:     cfg.EncryptionKey,
		jwtSecret:         cfg.JWTSecret,
		backgroundCtx:     bgCtx,
		rateLimiter:       rl,
		dummyPasswordHash: cfg.DummyPasswordHash,
		streams:           newUserStreamPool(maxStreamsPerUser),
		trustedProxy:      cfg.TrustedProxy,
		authStateLookup:   cfg.AuthStateLookup,
	}, nil
}

type userStreamPool struct {
	mu     sync.Mutex
	active map[string]int
	limit  int
}

func newUserStreamPool(limit int) *userStreamPool {
	return &userStreamPool{
		active: make(map[string]int),
		limit:  limit,
	}
}

func (p *userStreamPool) Acquire(userID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = make(map[string]int)
	}
	if p.active[userID] >= p.limit {
		return false
	}
	p.active[userID]++
	return true
}

func (p *userStreamPool) Release(userID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active[userID]--
	if p.active[userID] <= 0 {
		delete(p.active, userID)
	}
}

func (s *APIServer) streamPool() *userStreamPool {
	if s == nil {
		return nil
	}
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streams == nil {
		s.streams = newUserStreamPool(maxStreamsPerUser)
	}
	return s.streams
}

// Handler composes the HTTP routing mux and outer gateway middleware chain.
func (server *APIServer) Handler() http.Handler {
	return server.wrapMiddleware(server.setupRoutes())
}

func (server *APIServer) contractTestHandler(terminal http.HandlerFunc) http.Handler {
	return server.wrapMiddleware(server.buildMux(terminal))
}

func (server *APIServer) wrapMiddleware(mux *http.ServeMux) http.Handler {
	return server.requestLogMiddleware(server.securityHeadersMiddleware(corsMiddleware(mux)))
}

func (server *APIServer) setupRoutes() *http.ServeMux {
	return server.buildMux(nil)
}

func (server *APIServer) buildMux(overrideHandler http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()

	jwtMiddleware := auth.AuthMiddleware(server.db, server.jwtSecret)
	allowMustChangeMiddleware := auth.AuthMiddlewareAllowMustChange(server.db, server.jwtSecret)
	if server.authStateLookup != nil {
		jwtMiddleware = auth.AuthMiddlewareWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
		allowMustChangeMiddleware = auth.AuthMiddlewareAllowMustChangeWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
	}
	adminRouteMiddleware := adminMiddleware(jwtMiddleware)

	registerRouteSpecs(mux, server.gatewayRoutes(), jwtMiddleware, allowMustChangeMiddleware, adminRouteMiddleware, overrideHandler)
	files := server.files()
	migrations := server.migrations()
	syncs := server.syncs()
	backups := server.backups()
	if overrideHandler == nil {
		files.registerRoutes(mux, jwtMiddleware)
		migrations.registerRoutes(mux, jwtMiddleware)
		syncs.registerRoutes(mux, jwtMiddleware)
		backups.registerRoutes(mux, jwtMiddleware)
	} else {
		files.registerRoutesWithHandler(mux, jwtMiddleware, overrideHandler)
		migrations.registerRoutesWithHandler(mux, jwtMiddleware, overrideHandler)
		syncs.registerRoutesWithHandler(mux, jwtMiddleware, overrideHandler)
		backups.registerRoutesWithHandler(mux, jwtMiddleware, overrideHandler)
	}

	return mux
}

func registerRouteSpecs(
	mux *http.ServeMux,
	routes []routeSpec,
	authenticated func(http.Handler) http.Handler,
	allowMustChange func(http.Handler) http.Handler,
	adminOnly func(http.Handler) http.Handler,
	overrideHandler http.HandlerFunc,
) {
	for _, r := range routes {
		target := r.handler
		if overrideHandler != nil {
			target = overrideHandler
		}
		mux.Handle(r.pattern, wrapRouteAccess(r.access, authenticated, allowMustChange, adminOnly, target))
	}
}

func wrapRouteAccess(
	access routeAccess,
	authenticated func(http.Handler) http.Handler,
	allowMustChange func(http.Handler) http.Handler,
	adminOnly func(http.Handler) http.Handler,
	handler http.HandlerFunc,
) http.Handler {
	base := http.Handler(handler)
	switch access {
	case routePublic:
		return base
	case routeAuthenticated:
		if authenticated == nil {
			panic("authenticated middleware required for protected routes")
		}
		return authenticated(base)
	case routeAllowMustChange:
		if allowMustChange == nil {
			panic("allow-must-change middleware required for password rotation routes")
		}
		return allowMustChange(base)
	case routeAdmin:
		if adminOnly == nil {
			panic("admin middleware required for admin routes")
		}
		return adminOnly(base)
	default:
		panic("unsupported route access category")
	}
}

func (server *APIServer) gatewayRoutes() []routeSpec {
	notifications := newNotificationAPI(notificationDependencies{
		db:            server.db,
		encryptionKey: server.encryptionKey,
		rateLimiter:   server.rateLimiter,
		requireUserID: server.requireUserID,
		clientIP:      server.clientIP,
		writeAudit:    server.writeAudit,
		logf:          server.logf,
	})

	routes := []routeSpec{
		// Auth Routes (Public)
		{pattern: "GET /api/auth/setup-status", access: routePublic, handler: server.handleGetSetupStatus},
		{pattern: "POST /api/auth/setup-admin", access: routePublic, handler: server.handleSetupAdmin},
		{pattern: "POST /api/auth/register", access: routePublic, handler: server.handleRegister},
		{pattern: "POST /api/auth/login", access: routePublic, handler: server.handleLogin},
		{pattern: "POST /api/auth/totp", access: routePublic, handler: server.handleTOTP},
		{pattern: "POST /api/auth/refresh", access: routePublic, handler: server.handleRefresh},
		{pattern: "POST /api/auth/logout", access: routePublic, handler: server.handleLogout},
		{pattern: "GET /api/settings", access: routePublic, handler: server.handleGetSettings},

		// Protected Auth Routes
		{pattern: "GET /api/auth/me", access: routeAuthenticated, handler: server.handleMe},
		{pattern: "GET /api/auth/sessions", access: routeAuthenticated, handler: server.handleListSessions},
		{pattern: "DELETE /api/auth/sessions/{id}", access: routeAuthenticated, handler: server.handleDeleteSession},
		{pattern: "PUT /api/auth/me", access: routeAuthenticated, handler: server.handleUpdateProfile},
		{pattern: "PUT /api/auth/me/language", access: routeAuthenticated, handler: server.handleUpdateLanguage},
		{pattern: "POST /api/auth/change-password", access: routeAllowMustChange, handler: server.handleChangePassword},
		{pattern: "GET /api/auth/2fa/setup", access: routeAuthenticated, handler: server.handle2FASetup},
		{pattern: "POST /api/auth/2fa/enable", access: routeAuthenticated, handler: server.handle2FAEnable},
		{pattern: "POST /api/auth/2fa/disable", access: routeAuthenticated, handler: server.handle2FADisable},
		{pattern: "GET /api/auth/2fa/status", access: routeAuthenticated, handler: server.handle2FAStatus},
		{pattern: "POST /api/user/avatar", access: routeAuthenticated, handler: server.handleSetAvatar},
		{pattern: "DELETE /api/user/avatar", access: routeAuthenticated, handler: server.handleDeleteAvatar},
		{pattern: "PUT /api/settings", access: routeAdmin, handler: server.handleUpdateSetting},

		{pattern: "GET /api/auth/password-reset-available", access: routePublic, handler: server.handlePasswordResetAvailable},
		{pattern: "POST /api/auth/forgot-password", access: routePublic, handler: server.handleForgotPassword},
		{pattern: "POST /api/auth/reset-password", access: routePublic, handler: server.handleResetPassword},

		{pattern: "GET /api/auth/email-change-available", access: routePublic, handler: server.handleEmailChangeAvailable},
		{pattern: "POST /api/auth/change-email", access: routeAuthenticated, handler: server.handleChangeEmail},
		{pattern: "POST /api/auth/confirm-email-change", access: routePublic, handler: server.handleConfirmEmailChange},

		// Schedule Management Routes (Protected)
		{pattern: "GET /api/schedule", access: routeAuthenticated, handler: server.handleListSchedules},
		{pattern: "GET /api/schedule/{id}", access: routeAuthenticated, handler: server.handleGetSchedule},
		{pattern: "DELETE /api/schedule/{id}", access: routeAuthenticated, handler: server.handleDeleteSchedule},

		// Connection Profiles (Protected)
		{pattern: "GET /api/profiles", access: routeAuthenticated, handler: server.handleListProfiles},
		{pattern: "POST /api/profiles", access: routeAuthenticated, handler: server.handleCreateProfile},
		{pattern: "GET /api/profiles/{id}", access: routeAuthenticated, handler: server.handleGetProfile},
		{pattern: "PUT /api/profiles/{id}", access: routeAuthenticated, handler: server.handleUpdateConnectionProfile},
		{pattern: "DELETE /api/profiles/{id}", access: routeAuthenticated, handler: server.handleDeleteProfile},
		{pattern: "POST /api/profiles/{id}/test", access: routeAuthenticated, handler: server.handleTestProfile},

		// Administrative routes require both a current JWT and the ADMIN role.
		{pattern: "POST /api/admin/users", access: routeAdmin, handler: server.handleAdminCreateUser},
		{pattern: "POST /api/admin/users/{id}/suspend", access: routeAdmin, handler: server.handleAdminSuspendUser},
		{pattern: "POST /api/admin/users/{id}/reactivate", access: routeAdmin, handler: server.handleAdminReactivateUser},
		{pattern: "DELETE /api/admin/users/{id}", access: routeAdmin, handler: server.handleAdminDeleteUser},
		{pattern: "PUT /api/admin/users/{id}/role", access: routeAdmin, handler: server.handleAdminUpdateRole},
		{pattern: "GET /api/admin/users", access: routeAdmin, handler: server.handleAdminListUsers},
		{pattern: "GET /api/admin/stats", access: routeAdmin, handler: server.handleAdminStats},
		{pattern: "GET /api/admin/migrations", access: routeAdmin, handler: server.handleAdminListMigrations},
		{pattern: "GET /api/admin/syncs", access: routeAdmin, handler: server.handleAdminListSyncs},
		{pattern: "GET /api/admin/settings/smtp", access: routeAdmin, handler: server.handleAdminGetSMTP},
		{pattern: "PUT /api/admin/settings/smtp", access: routeAdmin, handler: server.handleAdminPutSMTP},
		{pattern: "POST /api/admin/settings/smtp/test", access: routeAdmin, handler: server.handleAdminTestSMTP},
		{pattern: "DELETE /api/admin/settings/smtp", access: routeAdmin, handler: server.handleAdminDeleteSMTP},
		{pattern: "GET /api/admin/settings/oauth", access: routeAdmin, handler: server.handleAdminGetOAuth},
		{pattern: "PUT /api/admin/settings/oauth/{provider}", access: routeAdmin, handler: server.handleAdminPutOAuth},
		{pattern: "DELETE /api/admin/settings/oauth/{provider}", access: routeAdmin, handler: server.handleAdminDeleteOAuth},
		{pattern: "GET /api/audit/log", access: routeAdmin, handler: server.handleAdminAuditLog},

		// OAuth callbacks use their own state validation.
		{pattern: "GET /api/oauth/auth", access: routePublic, handler: server.handleOAuthAuth},
		{pattern: "GET /api/oauth/callback", access: routePublic, handler: server.handleOAuthCallback},
	}

	routes = append(routes, notifications.routes()...)
	return routes
}
