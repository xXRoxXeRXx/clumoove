package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/db"
)

func TestSetupRoutesDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("setupRoutes panicked: %v", r)
		}
	}()

	server := &APIServer{
		db:            nil,
		jwtSecret:     "01234567890123456789012345678901",
		encryptionKey: "01234567890123456789012345678901",
		backgroundCtx: context.Background(),
	}

	mux := server.setupRoutes()
	if mux == nil {
		t.Fatal("expected non-nil http.ServeMux")
	}

	// Verify that the handler wrapper chain builds correctly as well
	handler := server.requestLogMiddleware(server.securityHeadersMiddleware(corsMiddleware(mux)))
	if handler == nil {
		t.Fatal("expected non-nil wrapped http.Handler")
	}
}

func TestSetupRoutesRegistersSyncMkdir(t *testing.T) {
	server := &APIServer{
		db:            nil,
		jwtSecret:     "01234567890123456789012345678901",
		encryptionKey: "01234567890123456789012345678901",
		backgroundCtx: context.Background(),
	}

	recorder := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/sync/sync-id/mkdir", nil))
	if recorder.Code == http.StatusNotFound {
		t.Fatal("POST /api/sync/{id}/mkdir is not registered")
	}
}

type routeContract struct {
	pattern string
	access  routeAccess
}

var expectedRouteContracts = []routeContract{
	// Public routes
	{pattern: "GET /api/auth/setup-status", access: routePublic},
	{pattern: "POST /api/auth/setup-admin", access: routePublic},
	{pattern: "POST /api/auth/register", access: routePublic},
	{pattern: "POST /api/auth/login", access: routePublic},
	{pattern: "POST /api/auth/totp", access: routePublic},
	{pattern: "POST /api/auth/refresh", access: routePublic},
	{pattern: "POST /api/auth/logout", access: routePublic},
	{pattern: "GET /api/settings", access: routePublic},
	{pattern: "GET /api/auth/password-reset-available", access: routePublic},
	{pattern: "POST /api/auth/forgot-password", access: routePublic},
	{pattern: "POST /api/auth/reset-password", access: routePublic},
	{pattern: "GET /api/auth/email-change-available", access: routePublic},
	{pattern: "POST /api/auth/confirm-email-change", access: routePublic},
	{pattern: "GET /api/files/download/{ticket}", access: routePublic},
	{pattern: "GET /api/files/archive/{ticket}", access: routePublic},
	{pattern: "GET /api/oauth/auth", access: routePublic},
	{pattern: "GET /api/oauth/callback", access: routePublic},

	// Must-change-password authenticated route
	{pattern: "POST /api/auth/change-password", access: routeAllowMustChange},

	// Authenticated routes
	{pattern: "GET /api/auth/me", access: routeAuthenticated},
	{pattern: "GET /api/auth/sessions", access: routeAuthenticated},
	{pattern: "DELETE /api/auth/sessions/{id}", access: routeAuthenticated},
	{pattern: "PUT /api/auth/me", access: routeAuthenticated},
	{pattern: "PUT /api/auth/me/language", access: routeAuthenticated},
	{pattern: "GET /api/auth/2fa/setup", access: routeAuthenticated},
	{pattern: "POST /api/auth/2fa/enable", access: routeAuthenticated},
	{pattern: "POST /api/auth/2fa/disable", access: routeAuthenticated},
	{pattern: "GET /api/auth/2fa/status", access: routeAuthenticated},
	{pattern: "POST /api/user/avatar", access: routeAuthenticated},
	{pattern: "DELETE /api/user/avatar", access: routeAuthenticated},
	{pattern: "GET /api/settings/notifications", access: routeAuthenticated},
	{pattern: "PUT /api/settings/notifications", access: routeAuthenticated},
	{pattern: "POST /api/settings/notifications/test", access: routeAuthenticated},
	{pattern: "POST /api/auth/change-email", access: routeAuthenticated},

	// Migration routes
	{pattern: "GET /api/migration", access: routeAuthenticated},
	{pattern: "GET /api/migration/stream", access: routeAuthenticated},
	{pattern: "POST /api/migration/connect", access: routeAuthenticated},
	{pattern: "POST /api/migration/connect/test", access: routeAuthenticated},
	{pattern: "POST /api/migration/browse", access: routeAuthenticated},
	{pattern: "POST /api/migration/target/browse", access: routeAuthenticated},
	{pattern: "POST /api/migration/target/mkdir", access: routeAuthenticated},
	{pattern: "POST /api/migration/start", access: routeAuthenticated},
	{pattern: "GET /api/migration/{id}", access: routeAuthenticated},
	{pattern: "GET /api/migration/{id}/stream", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/pause", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/resume", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/cancel", access: routeAuthenticated},
	{pattern: "DELETE /api/migration/{id}", access: routeAuthenticated},
	{pattern: "GET /api/migration/{id}/report", access: routeAuthenticated},
	{pattern: "GET /api/migration/{id}/errors", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/retry-failed", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/reauth", access: routeAuthenticated},
	{pattern: "POST /api/migration/{id}/reindex", access: routeAuthenticated},
	{pattern: "PUT /api/migration/{id}/bandwidth", access: routeAuthenticated},
	{pattern: "PUT /api/migration/{id}/threads", access: routeAuthenticated},

	// Sync routes
	{pattern: "GET /api/sync", access: routeAuthenticated},
	{pattern: "GET /api/sync/stream", access: routeAuthenticated},
	{pattern: "POST /api/sync", access: routeAuthenticated},
	{pattern: "GET /api/sync/{id}", access: routeAuthenticated},
	{pattern: "POST /api/sync/{id}/start", access: routeAuthenticated},
	{pattern: "POST /api/sync/{id}/pause", access: routeAuthenticated},
	{pattern: "POST /api/sync/{id}/resume", access: routeAuthenticated},
	{pattern: "POST /api/sync/{id}/reauth", access: routeAuthenticated},
	{pattern: "DELETE /api/sync/{id}", access: routeAuthenticated},
	{pattern: "GET /api/sync/{id}/report", access: routeAuthenticated},
	{pattern: "GET /api/sync/{id}/errors", access: routeAuthenticated},
	{pattern: "PUT /api/sync/{id}/threads", access: routeAuthenticated},
	{pattern: "PUT /api/sync/{id}/bandwidth", access: routeAuthenticated},
	{pattern: "GET /api/sync/{id}/browse", access: routeAuthenticated},
	{pattern: "POST /api/sync/{id}/mkdir", access: routeAuthenticated},
	{pattern: "PUT /api/sync/{id}/schedule", access: routeAuthenticated},
	{pattern: "PUT /api/sync/{id}/scope", access: routeAuthenticated},

	// Backup & restore routes
	{pattern: "GET /api/backup", access: routeAuthenticated},
	{pattern: "GET /api/backup/stream", access: routeAuthenticated},
	{pattern: "POST /api/backup", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}", access: routeAuthenticated},
	{pattern: "PUT /api/backup/{id}", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/runs", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/run", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/verify", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/verify", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/verify/{verifyID}/stream", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/verify/{verifyID}", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/verify/{verifyID}/cancel", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/pause", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/resume", access: routeAuthenticated},
	{pattern: "DELETE /api/backup/{id}", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/snapshots", access: routeAuthenticated},
	{pattern: "GET /api/backup/{id}/snapshots/{snapshotID}/items", access: routeAuthenticated},
	{pattern: "POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews", access: routeAuthenticated},
	{pattern: "GET /api/restore/previews/{previewID}", access: routeAuthenticated},
	{pattern: "POST /api/restore/previews/{previewID}/cancel", access: routeAuthenticated},
	{pattern: "POST /api/restore/previews/{previewID}/consume", access: routeAuthenticated},
	{pattern: "GET /api/restore", access: routeAuthenticated},
	{pattern: "GET /api/restore/runs", access: routeAuthenticated},
	{pattern: "GET /api/restore/runs/{runID}/stream", access: routeAuthenticated},
	{pattern: "GET /api/restore/runs/{runID}", access: routeAuthenticated},
	{pattern: "GET /api/restore/runs/{runID}/items", access: routeAuthenticated},
	{pattern: "POST /api/restore/runs/{runID}/cancel", access: routeAuthenticated},
	{pattern: "GET /api/restore/runs/{runID}/report", access: routeAuthenticated},
	{pattern: "DELETE /api/restore/jobs/{jobID}", access: routeAuthenticated},

	// Schedule routes
	{pattern: "GET /api/schedule", access: routeAuthenticated},
	{pattern: "GET /api/schedule/{id}", access: routeAuthenticated},
	{pattern: "DELETE /api/schedule/{id}", access: routeAuthenticated},

	// Connection profiles routes
	{pattern: "GET /api/profiles", access: routeAuthenticated},
	{pattern: "POST /api/profiles", access: routeAuthenticated},
	{pattern: "GET /api/profiles/{id}", access: routeAuthenticated},
	{pattern: "PUT /api/profiles/{id}", access: routeAuthenticated},
	{pattern: "DELETE /api/profiles/{id}", access: routeAuthenticated},
	{pattern: "POST /api/profiles/{id}/test", access: routeAuthenticated},

	// File manager routes
	{pattern: "GET /api/files/profiles/{profileID}/capabilities", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:list", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:resolve", access: routeAuthenticated},
	{pattern: "DELETE /api/files/profiles/{profileID}/entries", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:rename", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:copy", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:move", access: routeAuthenticated},
	{pattern: "PUT /api/files/profiles/{profileID}/content", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/directories", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/download-tickets", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/archive-tickets", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:batch-delete", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:batch-copy", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/entries:batch-move", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/transfers", access: routeAuthenticated},
	{pattern: "GET /api/files/profiles/{profileID}/transfers", access: routeAuthenticated},
	{pattern: "POST /api/files/transfers/{id}/cancel", access: routeAuthenticated},
	{pattern: "POST /api/files/profiles/{profileID}/thumbnail", access: routeAuthenticated},

	// Administrator-only routes
	{pattern: "PUT /api/settings", access: routeAdmin},
	{pattern: "POST /api/admin/users", access: routeAdmin},
	{pattern: "POST /api/admin/users/{id}/suspend", access: routeAdmin},
	{pattern: "POST /api/admin/users/{id}/reactivate", access: routeAdmin},
	{pattern: "DELETE /api/admin/users/{id}", access: routeAdmin},
	{pattern: "PUT /api/admin/users/{id}/role", access: routeAdmin},
	{pattern: "GET /api/admin/users", access: routeAdmin},
	{pattern: "GET /api/admin/stats", access: routeAdmin},
	{pattern: "GET /api/admin/migrations", access: routeAdmin},
	{pattern: "GET /api/admin/syncs", access: routeAdmin},
	{pattern: "GET /api/admin/settings/smtp", access: routeAdmin},
	{pattern: "PUT /api/admin/settings/smtp", access: routeAdmin},
	{pattern: "POST /api/admin/settings/smtp/test", access: routeAdmin},
	{pattern: "DELETE /api/admin/settings/smtp", access: routeAdmin},
	{pattern: "GET /api/admin/settings/oauth", access: routeAdmin},
	{pattern: "PUT /api/admin/settings/oauth/{provider}", access: routeAdmin},
	{pattern: "DELETE /api/admin/settings/oauth/{provider}", access: routeAdmin},
	{pattern: "GET /api/audit/log", access: routeAdmin},
}

var routeParamPattern = regexp.MustCompile(`\{[^}]+\}`)

func requestForRoutePattern(pattern string) *http.Request {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		panic("invalid route pattern: " + pattern)
	}
	concretePath := routeParamPattern.ReplaceAllString(path, "test-id")
	return httptest.NewRequest(method, concretePath, nil)
}

func TestGatewayRouteContracts(t *testing.T) {
	users := map[string]*db.UserAuthState{
		"user-1":        {Role: "USER", Active: true},
		"admin-1":       {Role: "ADMIN", Active: true},
		"must-change-1": {Role: "USER", Active: true, MustChangePassword: true},
	}

	server := newTestAPIGateway(t, users)

	userToken, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken(user): %v", err)
	}
	adminToken, err := auth.GenerateAccessToken(&db.User{ID: "admin-1", Email: "admin@example.com", Role: "ADMIN", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken(admin): %v", err)
	}
	twoFAToken, err := auth.Generate2FATempToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("Generate2FATempToken: %v", err)
	}
	mustChangeToken, err := auth.GenerateMustChangePasswordToken(&db.User{ID: "must-change-1", Email: "rotate@example.com", Role: "USER", Active: true, MustChangePassword: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateMustChangePasswordToken: %v", err)
	}

	// Verify the composed gateway and feature-module route tables match the independent contract table 1:1.
	actualRoutes := append(server.gatewayRoutes(), server.files().routes()...)
	actualRoutes = append(actualRoutes, server.migrations().routes()...)
	actualByPattern := make(map[string]routeSpec, len(actualRoutes))
	for _, r := range actualRoutes {
		if r.handler == nil {
			t.Errorf("route %s has nil handler", r.pattern)
		}
		if _, dup := actualByPattern[r.pattern]; dup {
			t.Fatalf("duplicate route pattern in gateway: %s", r.pattern)
		}
		actualByPattern[r.pattern] = r
	}
	expectedByPattern := make(map[string]routeContract, len(expectedRouteContracts))
	for _, c := range expectedRouteContracts {
		expectedByPattern[c.pattern] = c
		actual, ok := actualByPattern[c.pattern]
		if !ok {
			t.Errorf("missing expected route %s", c.pattern)
			continue
		}
		if actual.access != c.access {
			t.Errorf("route %s access = %v, want %v", c.pattern, actual.access, c.access)
		}
	}
	for pattern := range actualByPattern {
		if _, ok := expectedByPattern[pattern]; !ok {
			t.Errorf("unexpected route registered without contract: %s", pattern)
		}
	}

	// Exercise the composed router and access-control middleware with sentinel handlers
	// so every route's method, path, and access category (allow & deny) is verified.
	contractHandler := server.contractTestHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	assertStatus := func(t *testing.T, pattern, token string, wantStatus int, wantCode APIErrorCode) {
		t.Helper()
		req := requestForRoutePattern(pattern)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		contractHandler.ServeHTTP(rec, req)
		assertAPIResponseError(t, rec, wantStatus, wantCode, pattern)
	}

	for _, c := range expectedRouteContracts {
		t.Run(c.pattern, func(t *testing.T) {
			switch c.access {
			case routePublic:
				assertStatus(t, c.pattern, "", http.StatusOK, "")
			case routeAuthenticated:
				assertStatus(t, c.pattern, "", http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, twoFAToken, http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, mustChangeToken, http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, userToken, http.StatusOK, "")
				assertStatus(t, c.pattern, adminToken, http.StatusOK, "")
			case routeAllowMustChange:
				assertStatus(t, c.pattern, "", http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, twoFAToken, http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, mustChangeToken, http.StatusOK, "")
				assertStatus(t, c.pattern, userToken, http.StatusOK, "")
			case routeAdmin:
				assertStatus(t, c.pattern, "", http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, twoFAToken, http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, mustChangeToken, http.StatusUnauthorized, ErrUnauthorized)
				assertStatus(t, c.pattern, userToken, http.StatusForbidden, ErrAdminOnly)
				assertStatus(t, c.pattern, adminToken, http.StatusOK, "")
			default:
				t.Fatalf("unknown access category %v", c.access)
			}
		})
	}
}
