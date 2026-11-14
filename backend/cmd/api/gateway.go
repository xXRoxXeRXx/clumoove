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

// routeMiddleware holds the access-control middleware the gateway composes once
// and hands to every feature module for its own route registration.
type routeMiddleware struct {
	authenticated   func(http.Handler) http.Handler
	allowMustChange func(http.Handler) http.Handler
	adminOnly       func(http.Handler) http.Handler
}

// featureModule describes one feature module as seen by the gateway: the routes
// it owns and how it registers them. The gateway never declares routes itself.
type featureModule struct {
	name     string
	routes   []routeSpec
	register func(mux *http.ServeMux, overrideHandler http.HandlerFunc)
}

// featureModules lists every feature module composed into the gateway. Each
// module owns its endpoint registration and feature dependencies.
func (server *APIServer) featureModules(mw routeMiddleware) []featureModule {
	accounts := server.accounts()
	files := server.files()
	migrations := server.migrations()
	syncs := server.syncs()
	backups := server.backups()
	schedules := server.schedules()
	profiles := server.connectionProfiles()
	admin := server.administration()
	notifications := server.notifications()
	return []featureModule{
		{"accounts", accounts.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			accounts.registerRoutesWithHandler(mux, mw.authenticated, mw.allowMustChange, o)
		}},
		{"files", files.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			files.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"migrations", migrations.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			migrations.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"syncs", syncs.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			syncs.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"backups", backups.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			backups.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"schedules", schedules.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			schedules.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"profiles", profiles.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			profiles.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
		{"administration", admin.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			admin.registerRoutesWithHandler(mux, mw.authenticated, mw.adminOnly, o)
		}},
		{"notifications", notifications.routes(), func(mux *http.ServeMux, o http.HandlerFunc) {
			notifications.registerRoutesWithHandler(mux, mw.authenticated, o)
		}},
	}
}

func (server *APIServer) routeMiddleware() routeMiddleware {
	jwtMiddleware := auth.AuthMiddleware(server.db, server.jwtSecret)
	allowMustChangeMiddleware := auth.AuthMiddlewareAllowMustChange(server.db, server.jwtSecret)
	if server.authStateLookup != nil {
		jwtMiddleware = auth.AuthMiddlewareWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
		allowMustChangeMiddleware = auth.AuthMiddlewareAllowMustChangeWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
	}
	return routeMiddleware{
		authenticated:   jwtMiddleware,
		allowMustChange: allowMustChangeMiddleware,
		adminOnly:       adminMiddleware(jwtMiddleware),
	}
}

// buildMux composes feature-module routes only. When overrideHandler is non-nil
// every route still passes through its own access middleware but terminates in
// the override, which route-contract tests use to probe access control.
func (server *APIServer) buildMux(overrideHandler http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	for _, module := range server.featureModules(server.routeMiddleware()) {
		module.register(mux, overrideHandler)
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

// registerAuthenticatedRouteSpecs registers a module whose routes must all
// require authentication, refusing any route that declares another access level.
func registerAuthenticatedRouteSpecs(
	module string,
	mux *http.ServeMux,
	routes []routeSpec,
	authenticated func(http.Handler) http.Handler,
	overrideHandler http.HandlerFunc,
) {
	for _, route := range routes {
		if route.access != routeAuthenticated {
			panic(module + " routes must require authentication")
		}
	}
	registerRouteSpecs(mux, routes, authenticated, nil, nil, overrideHandler)
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
