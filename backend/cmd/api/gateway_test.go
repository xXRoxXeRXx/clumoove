package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/db"
)

const (
	testGatewayEncryptionKey = "enc-secret-key-at-least-32-bytes-long"
	testGatewayJWTSecret     = "jwt-secret-key-at-least-32-bytes-long"
)

func newTestAPIGateway(t *testing.T, users map[string]*db.UserAuthState) *APIServer {
	t.Helper()
	server, err := newAPIGateway(gatewayConfig{
		EncryptionKey: testGatewayEncryptionKey,
		JWTSecret:     testGatewayJWTSecret,
		AuthStateLookup: func(id string) (*db.UserAuthState, error) {
			return users[id], nil
		},
	})
	if err != nil {
		t.Fatalf("newAPIGateway() unexpected error: %v", err)
	}
	return server
}

func assertAPIResponseError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode APIErrorCode, context string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("%s: status = %d, want %d (body=%s)", context, rec.Code, wantStatus, rec.Body.String())
	}
	if wantCode == "" {
		return
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decode error body: %v", context, err)
	}
	if body["error_code"] != string(wantCode) {
		t.Fatalf("%s: error_code = %v, want %s", context, body["error_code"], wantCode)
	}
}

func TestNewAPIGatewayValidatesKeyConfiguration(t *testing.T) {
	tests := []struct {
		name string
		cfg  gatewayConfig
	}{
		{
			name: "missing encryption key",
			cfg: gatewayConfig{
				JWTSecret: testGatewayJWTSecret,
			},
		},
		{
			name: "missing JWT secret",
			cfg: gatewayConfig{
				EncryptionKey: testGatewayEncryptionKey,
			},
		},
		{
			name: "equal encryption and JWT keys",
			cfg: gatewayConfig{
				EncryptionKey: testGatewayJWTSecret,
				JWTSecret:     testGatewayJWTSecret,
			},
		},
		{
			name: "JWT secret shorter than 32 bytes",
			cfg: gatewayConfig{
				EncryptionKey: testGatewayEncryptionKey,
				JWTSecret:     "too-short-jwt-secret",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, err := newAPIGateway(tt.cfg)
			if err == nil {
				t.Fatalf("newAPIGateway() error = nil, want configuration error")
			}
			if server != nil {
				t.Fatalf("newAPIGateway() returned non-nil server on error")
			}
		})
	}

	t.Run("accepts valid segregated keys without starting daemons", func(t *testing.T) {
		server, err := newAPIGateway(gatewayConfig{
			EncryptionKey: testGatewayEncryptionKey,
			JWTSecret:     testGatewayJWTSecret,
		})
		if err != nil {
			t.Fatalf("newAPIGateway() unexpected error: %v", err)
		}
		if server == nil {
			t.Fatal("newAPIGateway() returned nil server")
		}
	})
}

func TestAPIGatewayHandlerComposesMiddlewareAndAccessControl(t *testing.T) {
	server := newTestAPIGateway(t, map[string]*db.UserAuthState{
		"user-1": {Active: true, Role: "USER"},
	})

	handler := server.Handler()
	if handler == nil {
		t.Fatal("server.Handler() returned nil")
	}

	t.Run("applies gateway security, request ID, and CORS headers on authenticated rejection", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/migration", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/migration")
		if rec.Header().Get("X-Request-ID") == "" {
			t.Fatal("expected server-generated X-Request-ID header")
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want http://localhost:5173", got)
		}
	})

	t.Run("enforces admin role on admin-only routes using injected auth state lookup", func(t *testing.T) {
		token, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
		if err != nil {
			t.Fatalf("GenerateAccessToken: %v", err)
		}

		req := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assertAPIResponseError(t, rec, http.StatusForbidden, ErrAdminOnly, "GET /api/admin/users")
	})
}

// receiverTypeName returns the base type name of a method receiver, looking
// through pointers, parentheses and generic instantiations so value receivers
// are not missed.
func receiverTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// apiServerMethods returns every method declared on APIServer (pointer or
// value receiver) in the given files, keyed by method name with the declaring
// file as value.
func apiServerMethods(files map[string]*ast.File) map[string]string {
	methods := map[string]string{}
	for fileName, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			if receiverTypeName(fn.Recv.List[0].Type) == "APIServer" {
				methods[fn.Name.Name] = fileName
			}
		}
	}
	return methods
}

func parseNonTestPackageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			files[name] = file
		}
	}
	return files
}

func TestAPIServerMethodsDetectsPointerAndValueReceivers(t *testing.T) {
	src := `package main
type APIServer struct{}
func (s *APIServer) pointerMethod() {}
func (s APIServer) valueMethod() {}
func (APIServer) anonymousValueMethod() {}
func (s *(APIServer)) parenMethod() {}
func (s *Other) unrelated() {}
func free() {}
`
	file, err := parser.ParseFile(token.NewFileSet(), "src.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := apiServerMethods(map[string]*ast.File{"src.go": file})
	for _, want := range []string{"pointerMethod", "valueMethod", "anonymousValueMethod", "parenMethod"} {
		if _, ok := got[want]; !ok {
			t.Errorf("apiServerMethods missed %s (got %v)", want, got)
		}
	}
	for _, unwanted := range []string{"unrelated", "free"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("apiServerMethods wrongly reported %s", unwanted)
		}
	}
}

// The root gateway composes feature modules and cross-cutting HTTP policy only.
// Feature endpoint handlers must live on their feature module, not on APIServer.
func TestGatewayOwnsNoFeatureHandlers(t *testing.T) {
	for name, fileName := range apiServerMethods(parseNonTestPackageFiles(t)) {
		if strings.HasPrefix(name, "handle") {
			t.Errorf("%s: APIServer.%s is a feature handler; move it to its feature module", fileName, name)
		}
	}
}

// gatewayMethodAllowlist is the explicit set of methods APIServer may declare,
// each with the reason it belongs on the gateway. Feature-owned behaviour such
// as daemons, profile access wiring, and request connection helpers belongs on
// its feature module or shared component.
var gatewayMethodAllowlist = map[string]string{
	// composition
	"Handler":             "public entry point returning the composed http.Handler",
	"contractTestHandler": "composes the full mux for route-contract tests",
	"wrapMiddleware":      "applies the gateway-wide middleware chain",
	"setupRoutes":         "registers every feature module's routes on the mux",
	"featureModules":      "lists the composed feature modules",
	"buildMux":            "builds the route mux from the feature modules",
	"routeMiddleware":     "wraps a route with the middleware its access category requires",
	"backgroundDaemons":   "composes feature-owned daemons for main.go",
	"profiles":            "composes the shared profile access component",
	"streamPool":          "owns the process-wide shared stream pool",
	"requestEnv":          "hands cross-cutting request helpers to modules",
	"accounts":            "feature module accessor",
	"administration":      "feature module accessor",
	"backups":             "feature module accessor",
	"files":               "feature module accessor",
	"migrations":          "feature module accessor",
	"notifications":       "feature module accessor",
	"connectionProfiles":  "feature module accessor",
	"schedules":           "feature module accessor",
	"syncs":               "feature module accessor",
	// cross-cutting HTTP policy
	"logf":                      "request-scoped failure logging policy",
	"logfContext":               "context-scoped failure logging policy",
	"infof":                     "request-scoped lifecycle logging policy",
	"warnf":                     "request-scoped warning logging policy",
	"logln":                     "request-scoped plain logging policy",
	"loglnContext":              "context-scoped plain logging policy",
	"requestLogMiddleware":      "gateway-wide request logging middleware",
	"clientIPCategory":          "classifies client address for request logs",
	"securityHeadersMiddleware": "gateway-wide security headers middleware",
	"requireUserID":             "request identity lookup shared by modules",
	"clientIP":                  "trusted-proxy-aware client address resolution",
	"isSecure":                  "TLS/scheme detection for headers and cookies",
	"writeAudit":                "audit logging shared by modules",
	"getRedirectURI":            "OAuth callback URL resolution from public host",
	"requestScheme":             "externally visible scheme resolution",
	"publicHost":                "externally visible host resolution",
}

// APIServer carries composition and cross-cutting HTTP policy only; any other
// method is feature-owned behaviour that must move to its feature module.
func TestGatewayDeclaresOnlyCompositionMethods(t *testing.T) {
	methods := apiServerMethods(parseNonTestPackageFiles(t))
	for name, fileName := range methods {
		if _, ok := gatewayMethodAllowlist[name]; !ok {
			t.Errorf("%s: APIServer.%s is not in the composition allowlist; move it to its feature module", fileName, name)
		}
	}
	for name, reason := range gatewayMethodAllowlist {
		if reason == "" {
			t.Errorf("allowlist entry %q has no reason", name)
		}
		if _, ok := methods[name]; !ok {
			t.Errorf("allowlist entry %q is not an APIServer method; remove it", name)
		}
	}
}

// Feature modules own their background daemons; the gateway only composes them
// so main.go can start them without knowing what they do.
func TestGatewayComposesFeatureOwnedDaemons(t *testing.T) {
	server := newTestAPIGateway(t, nil)
	var names []string
	for _, d := range server.backgroundDaemons() {
		names = append(names, d.Name())
	}
	want := []string{"oauth_rotation", "migration_gc"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("backgroundDaemons() = %v, want %v", names, want)
	}
}

// The gateway file composes modules; it must never declare route specs itself.
func TestGatewayFileDeclaresNoRouteSpecs(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "gateway.go", nil, 0)
	if err != nil {
		t.Fatalf("parse gateway.go: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "pattern" {
			t.Errorf("gateway.go:%d declares a route pattern; routes belong to feature modules", fset.Position(kv.Pos()).Line)
		}
		return true
	})
}

// Every contract route must be served by the real composed mux (not merely
// listed by a module) and must match the declared HTTP method and path.
func TestGatewayServesEveryContractRoute(t *testing.T) {
	server := newTestAPIGateway(t, nil)
	handler := server.contractTestHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	for _, c := range expectedRouteContracts {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestForRoutePattern(c.pattern))
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s is not served by the composed gateway (status %d)", c.pattern, rec.Code)
		}
	}

	// Each contract path must reject a method no module registers for it, so a
	// route cannot silently widen to accept extra verbs.
	for _, c := range expectedRouteContracts {
		_, path, _ := strings.Cut(c.pattern, " ")
		req := requestForRoutePattern("PATCH " + path)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("PATCH %s status = %d, want 405", path, rec.Code)
		}
	}

	// A path that no module owns must stay unrouted.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/not-a-route", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unowned path status = %d, want 404", rec.Code)
	}
}
