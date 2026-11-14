package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"

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
		activeStreams:     make(map[string]int),
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

func (server *APIServer) buildMux(overrideHandler http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()

	jwtMiddleware := auth.AuthMiddleware(server.db, server.jwtSecret)
	allowMustChangeMiddleware := auth.AuthMiddlewareAllowMustChange(server.db, server.jwtSecret)
	if server.authStateLookup != nil {
		jwtMiddleware = auth.AuthMiddlewareWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
		allowMustChangeMiddleware = auth.AuthMiddlewareAllowMustChangeWithAuthStateLookup(server.jwtSecret, server.authStateLookup)
	}
	adminRouteMiddleware := adminMiddleware(jwtMiddleware)

	for _, r := range server.gatewayRoutes() {
		target := r.handler
		if overrideHandler != nil {
			target = overrideHandler
		}
		mux.Handle(r.pattern, wrapRouteAccess(r.access, jwtMiddleware, allowMustChangeMiddleware, adminRouteMiddleware, target))
	}

	return mux
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
		return authenticated(base)
	case routeAllowMustChange:
		return allowMustChange(base)
	case routeAdmin:
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
	files := server.files()

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

		// Migration Routes
		{pattern: "GET /api/migration", access: routeAuthenticated, handler: server.handleListMigrations},
		{pattern: "GET /api/migration/stream", access: routeAuthenticated, handler: server.handleMigrationStream},
		{pattern: "POST /api/migration/connect", access: routeAuthenticated, handler: server.handleConnect},
		{pattern: "POST /api/migration/connect/test", access: routeAuthenticated, handler: server.handleConnectTest},
		{pattern: "POST /api/migration/browse", access: routeAuthenticated, handler: server.handleBrowse},
		{pattern: "POST /api/migration/target/browse", access: routeAuthenticated, handler: server.handleTargetBrowse},
		{pattern: "POST /api/migration/target/mkdir", access: routeAuthenticated, handler: server.handleTargetMkdir},
		{pattern: "POST /api/migration/start", access: routeAuthenticated, handler: server.handleStart},
		{pattern: "GET /api/migration/{id}", access: routeAuthenticated, handler: server.handleGetStatus},
		{pattern: "GET /api/migration/{id}/stream", access: routeAuthenticated, handler: server.handleMigrationDetailStream},
		{pattern: "POST /api/migration/{id}/pause", access: routeAuthenticated, handler: server.handlePause},
		{pattern: "POST /api/migration/{id}/resume", access: routeAuthenticated, handler: server.handleResume},
		{pattern: "POST /api/migration/{id}/cancel", access: routeAuthenticated, handler: server.handleCancel},
		{pattern: "DELETE /api/migration/{id}", access: routeAuthenticated, handler: server.handleDeleteMigration},
		{pattern: "GET /api/migration/{id}/report", access: routeAuthenticated, handler: server.handleDownloadReport},
		{pattern: "GET /api/migration/{id}/errors", access: routeAuthenticated, handler: server.handleMigrationErrors},
		{pattern: "POST /api/migration/{id}/retry-failed", access: routeAuthenticated, handler: server.handleRetryFailed},
		{pattern: "POST /api/migration/{id}/reauth", access: routeAuthenticated, handler: server.handleMigrationReauth},
		{pattern: "POST /api/migration/{id}/reindex", access: routeAuthenticated, handler: server.handleReindex},
		{pattern: "PUT /api/migration/{id}/bandwidth", access: routeAuthenticated, handler: server.handleSetBandwidth},
		{pattern: "PUT /api/migration/{id}/threads", access: routeAuthenticated, handler: server.handleSetThreads},

		// Sync Engine Routes
		{pattern: "GET /api/sync", access: routeAuthenticated, handler: server.handleListSyncs},
		{pattern: "GET /api/sync/stream", access: routeAuthenticated, handler: server.handleSyncStream},
		{pattern: "POST /api/sync", access: routeAuthenticated, handler: server.handleCreateSync},
		{pattern: "GET /api/sync/{id}", access: routeAuthenticated, handler: server.handleGetSyncStatus},
		{pattern: "POST /api/sync/{id}/start", access: routeAuthenticated, handler: server.handleStartSync},
		{pattern: "POST /api/sync/{id}/pause", access: routeAuthenticated, handler: server.handlePauseSync},
		{pattern: "POST /api/sync/{id}/resume", access: routeAuthenticated, handler: server.handleResumeSync},
		{pattern: "POST /api/sync/{id}/reauth", access: routeAuthenticated, handler: server.handleSyncReauth},
		{pattern: "DELETE /api/sync/{id}", access: routeAuthenticated, handler: server.handleDeleteSync},
		{pattern: "GET /api/sync/{id}/report", access: routeAuthenticated, handler: server.handleDownloadSyncReport},
		{pattern: "GET /api/sync/{id}/errors", access: routeAuthenticated, handler: server.handleSyncErrors},
		{pattern: "PUT /api/sync/{id}/threads", access: routeAuthenticated, handler: server.handleSetSyncThreads},
		{pattern: "PUT /api/sync/{id}/bandwidth", access: routeAuthenticated, handler: server.handleSetSyncBandwidth},
		{pattern: "GET /api/sync/{id}/browse", access: routeAuthenticated, handler: server.handleBrowseSyncJob},
		{pattern: "POST /api/sync/{id}/mkdir", access: routeAuthenticated, handler: server.handleSyncMkdir},
		{pattern: "PUT /api/sync/{id}/schedule", access: routeAuthenticated, handler: server.handleUpdateSyncSchedule},
		{pattern: "PUT /api/sync/{id}/scope", access: routeAuthenticated, handler: server.handleUpdateSyncScope},

		// Backup Routes
		{pattern: "GET /api/backup", access: routeAuthenticated, handler: server.handleListBackups},
		{pattern: "GET /api/backup/stream", access: routeAuthenticated, handler: server.handleBackupStream},
		{pattern: "POST /api/backup", access: routeAuthenticated, handler: server.handleCreateBackup},
		{pattern: "GET /api/backup/{id}", access: routeAuthenticated, handler: server.handleGetBackup},
		{pattern: "PUT /api/backup/{id}", access: routeAuthenticated, handler: server.handleUpdateBackup},
		{pattern: "GET /api/backup/{id}/runs", access: routeAuthenticated, handler: server.handleListBackupRuns},
		{pattern: "POST /api/backup/{id}/run", access: routeAuthenticated, handler: server.handleRunBackup},
		{pattern: "POST /api/backup/{id}/verify", access: routeAuthenticated, handler: server.handleCreateBackupVerify},
		{pattern: "GET /api/backup/{id}/verify", access: routeAuthenticated, handler: server.handleListBackupVerifies},
		{pattern: "GET /api/backup/{id}/verify/{verifyID}/stream", access: routeAuthenticated, handler: server.handleBackupVerifyStream},
		{pattern: "GET /api/backup/{id}/verify/{verifyID}", access: routeAuthenticated, handler: server.handleGetBackupVerify},
		{pattern: "POST /api/backup/{id}/verify/{verifyID}/cancel", access: routeAuthenticated, handler: server.handleCancelBackupVerify},
		{pattern: "POST /api/backup/{id}/pause", access: routeAuthenticated, handler: server.handlePauseBackup},
		{pattern: "POST /api/backup/{id}/resume", access: routeAuthenticated, handler: server.handleResumeBackup},
		{pattern: "DELETE /api/backup/{id}", access: routeAuthenticated, handler: server.handleDeleteBackup},
		{pattern: "GET /api/backup/{id}/snapshots", access: routeAuthenticated, handler: server.handleListBackupSnapshots},
		{pattern: "GET /api/backup/{id}/snapshots/{snapshotID}/items", access: routeAuthenticated, handler: server.handleListBackupSnapshotItems},
		{pattern: "POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews", access: routeAuthenticated, handler: server.handleCreateRestorePreview},
		{pattern: "GET /api/restore/previews/{previewID}", access: routeAuthenticated, handler: server.handleGetRestorePreview},
		{pattern: "POST /api/restore/previews/{previewID}/cancel", access: routeAuthenticated, handler: server.handleCancelRestorePreview},
		{pattern: "POST /api/restore/previews/{previewID}/consume", access: routeAuthenticated, handler: server.handleConsumeRestorePreview},
		{pattern: "GET /api/restore", access: routeAuthenticated, handler: server.handleListRestoreRuns},
		{pattern: "GET /api/restore/runs", access: routeAuthenticated, handler: server.handleListRestoreRuns},
		{pattern: "GET /api/restore/runs/{runID}/stream", access: routeAuthenticated, handler: server.handleRestoreRunStream},
		{pattern: "GET /api/restore/runs/{runID}", access: routeAuthenticated, handler: server.handleGetRestoreRun},
		{pattern: "GET /api/restore/runs/{runID}/items", access: routeAuthenticated, handler: server.handleListRestoreRunItems},
		{pattern: "POST /api/restore/runs/{runID}/cancel", access: routeAuthenticated, handler: server.handleCancelRestoreRun},
		{pattern: "GET /api/restore/runs/{runID}/report", access: routeAuthenticated, handler: server.handleDownloadRestoreReport},
		{pattern: "DELETE /api/restore/jobs/{jobID}", access: routeAuthenticated, handler: server.handleDeleteRestoreJob},

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
	routes = append(routes, files.routes()...)
	return routes
}
