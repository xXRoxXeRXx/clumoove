package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
