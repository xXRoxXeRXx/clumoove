package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"

	"backend/internal/auth"
	"backend/internal/config"
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
		var err error
		if idx, err = indexer.NewIndexer(cfg.DB, cfg.EncryptionKey, cfg.Queue, config.DefaultIndexingTimeout); err != nil {
			return nil, err
		}
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
		streams:           newDefaultUserStreamPool(),
		trustedProxy:      cfg.TrustedProxy,
		authStateLookup:   cfg.AuthStateLookup,
	}, nil
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

// authenticatedRegistrar is the registration shape shared by every module whose
// routes only need the authenticated middleware.
type authenticatedRegistrar func(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, overrideHandler http.HandlerFunc)

// composeFeatureModule builds a module's route list exactly once and registers
// that same list, so the contract view and the mux can never diverge.
func composeFeatureModule(
	name string,
	buildRoutes func() []routeSpec,
	register func(mux *http.ServeMux, routes []routeSpec, overrideHandler http.HandlerFunc),
) featureModule {
	routes := buildRoutes()
	return featureModule{
		name:   name,
		routes: routes,
		register: func(mux *http.ServeMux, overrideHandler http.HandlerFunc) {
			register(mux, routes, overrideHandler)
		},
	}
}

// featureModules lists every feature module composed into the gateway. Each
// module owns its route table and feature dependencies; the gateway decides
// only which access middleware guards them.
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

	authenticated := func(mux *http.ServeMux, routes []routeSpec, o http.HandlerFunc) {
		registerRouteSpecs(mux, routes, mw.authenticated, nil, nil, o)
	}
	strictAuthenticated := func(module string) func(*http.ServeMux, []routeSpec, http.HandlerFunc) {
		return func(mux *http.ServeMux, routes []routeSpec, o http.HandlerFunc) {
			registerAuthenticatedRouteSpecs(module, mux, routes, mw.authenticated, o)
		}
	}
	return []featureModule{
		composeFeatureModule("accounts", accounts.routes, func(mux *http.ServeMux, routes []routeSpec, o http.HandlerFunc) {
			registerRouteSpecs(mux, routes, mw.authenticated, mw.allowMustChange, nil, o)
		}),
		composeFeatureModule("files", files.routes, authenticated),
		composeFeatureModule("migrations", migrations.routes, authenticated),
		composeFeatureModule("syncs", syncs.routes, authenticated),
		composeFeatureModule("backups", backups.routes, authenticated),
		composeFeatureModule("schedules", schedules.routes, strictAuthenticated("schedule")),
		composeFeatureModule("profiles", profiles.routes, strictAuthenticated("profile")),
		composeFeatureModule("administration", admin.routes, func(mux *http.ServeMux, routes []routeSpec, o http.HandlerFunc) {
			registerRouteSpecs(mux, routes, mw.authenticated, nil, mw.adminOnly, o)
		}),
		composeFeatureModule("notifications", notifications.routes, strictAuthenticated("notification")),
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

// backgroundDaemons composes the long-running loops owned by feature modules.
// main.go starts each one under the process context.
func (server *APIServer) backgroundDaemons() []backgroundDaemon {
	return []backgroundDaemon{
		&oauthRotationDaemon{
			db:            server.db,
			queue:         server.queue,
			encryptionKey: server.encryptionKey,
			cache:         server.oauthRotations.get(),
		},
		&migrationGarbageCollector{
			db:           server.db,
			loglnContext: server.loglnContext,
			logfContext:  server.logfContext,
		},
	}
}
