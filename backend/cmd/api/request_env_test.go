package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/db"
	"backend/internal/observability"
)

func TestRequestEnvWithDefaultsIsUsableWhenEmpty(t *testing.T) {
	env := requestEnv{}.withDefaults("test")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:4711"

	rec := httptest.NewRecorder()
	if _, ok := env.requireUserID(rec, req); ok {
		t.Fatal("requireUserID accepted a request without an authenticated user")
	}
	assertAPIError(t, rec, http.StatusUnauthorized, ErrUnauthorized)

	if got := env.clientIP(req); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want 203.0.113.9", got)
	}

	env.logf(req, "logf %s", "ok")
	env.infof(req, "infof %s", "ok")
	env.warnf(req, "warnf %s", "ok")
}

func TestRequestEnvWithDefaultsKeepsInjectedHelpers(t *testing.T) {
	var audited db.AuditAction
	env := requestEnv{
		clientIP:   func(*http.Request) string { return "198.51.100.1" },
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) { audited = action },
	}.withDefaults("test")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := env.clientIP(req); got != "198.51.100.1" {
		t.Fatalf("clientIP = %q, want the injected helper", got)
	}
	env.writeAudit(req, db.AuditAction("EXAMPLE"), "", "", nil)
	if audited != db.AuditAction("EXAMPLE") {
		t.Fatalf("injected audit writer not used, got %q", audited)
	}
}

func TestRequestEnvMissingAuditWriterIsLoggedNotSilentlyDropped(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(observability.WithLogger(req.Context(), logger))

	env := requestEnv{}.withDefaults("widgets")
	env.writeAudit(req, db.AuditAction("WIDGET_DELETED"), "target", "actor", nil)

	out := logs.String()
	if !strings.Contains(out, "WIDGET_DELETED") || !strings.Contains(out, "widgets") {
		t.Fatalf("dropped audit event was not reported, log output: %q", out)
	}
}

func TestComposeFeatureModuleBuildsRoutesOnce(t *testing.T) {
	builds := 0
	build := func() []routeSpec {
		builds++
		return []routeSpec{{
			pattern: "GET /api/example",
			access:  routePublic,
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
		}}
	}
	module := composeFeatureModule("example", build,
		func(mux *http.ServeMux, routes []routeSpec, override http.HandlerFunc) {
			registerRouteSpecs(mux, routes, nil, nil, nil, override)
		})

	module.register(http.NewServeMux(), nil)
	module.register(http.NewServeMux(), nil)

	if builds != 1 {
		t.Fatalf("routes built %d times, want exactly once per composition", builds)
	}
	if len(module.routes) != 1 || module.routes[0].pattern != "GET /api/example" {
		t.Fatalf("module routes = %+v", module.routes)
	}
}
