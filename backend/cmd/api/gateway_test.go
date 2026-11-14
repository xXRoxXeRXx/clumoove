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

// The root gateway composes feature modules and cross-cutting HTTP policy only.
// Feature endpoint handlers must live on their feature module, not on APIServer.
func TestGatewayOwnsNoFeatureHandlers(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	for _, pkg := range pkgs {
		for fileName, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "APIServer" && strings.HasPrefix(fn.Name.Name, "handle") {
					t.Errorf("%s: APIServer.%s is a feature handler; move it to its feature module", fileName, fn.Name.Name)
				}
			}
		}
	}
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

	// A path that no module owns must stay unrouted.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/not-a-route", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unowned path status = %d, want 404", rec.Code)
	}
}
