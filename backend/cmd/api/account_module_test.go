package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountModuleSMTPAvailabilityRoutesReportInternalErrorWhenDatabaseFails(t *testing.T) {
	database, err := sql.Open("postgres", "postgres://unused@127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	database.Close()

	api := newAccountAPI(accountDependencies{db: database})
	mux := http.NewServeMux()
	noAuth := func(next http.Handler) http.Handler { return next }
	api.registerRoutes(mux, noAuth, noAuth)

	for _, path := range []string{"/api/auth/password-reset-available", "/api/auth/email-change-available"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assertAPIResponseError(t, rec, http.StatusInternalServerError, ErrInternalError, path)
		})
	}
}

func TestAccountModuleRegistersAllRoutesWithExpectedAccessControls(t *testing.T) {
	api := newAccountAPI(accountDependencies{
		encryptionKey: "01234567890123456789012345678901",
		jwtSecret:     "abcdefghijklmnopqrstuvwxyz012345",
		rateLimiter:   allowAllFileRateLimiter{},
	})

	mux := http.NewServeMux()
	authCalls, allowMustChangeCalls := 0, 0
	denyAll := func(counter *int) func(http.Handler) http.Handler {
		return func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				*counter++
				writeError(w, http.StatusUnauthorized, ErrUnauthorized)
			})
		}
	}
	api.registerRoutes(mux, denyAll(&authCalls), denyAll(&allowMustChangeCalls))

	authenticatedRoutes := []struct{ method, path string }{
		{http.MethodGet, "/api/auth/me"},
		{http.MethodGet, "/api/auth/sessions"},
		{http.MethodDelete, "/api/auth/sessions/s1"},
		{http.MethodPut, "/api/auth/me"},
		{http.MethodPut, "/api/auth/me/language"},
		{http.MethodGet, "/api/auth/2fa/setup"},
		{http.MethodPost, "/api/auth/2fa/enable"},
		{http.MethodPost, "/api/auth/2fa/disable"},
		{http.MethodGet, "/api/auth/2fa/status"},
		{http.MethodPost, "/api/user/avatar"},
		{http.MethodDelete, "/api/user/avatar"},
		{http.MethodPost, "/api/auth/change-email"},
	}
	for _, route := range authenticatedRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			authCalls, allowMustChangeCalls = 0, 0
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
			assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, route.method+" "+route.path)
			if authCalls != 1 || allowMustChangeCalls != 0 {
				t.Fatalf("auth calls = %d, allow-must-change calls = %d, want 1 and 0", authCalls, allowMustChangeCalls)
			}
		})
	}

	t.Run("POST /api/auth/change-password", func(t *testing.T) {
		authCalls, allowMustChangeCalls = 0, 0
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/change-password", nil))
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "change-password")
		if authCalls != 0 || allowMustChangeCalls != 1 {
			t.Fatalf("auth calls = %d, allow-must-change calls = %d, want 0 and 1", authCalls, allowMustChangeCalls)
		}
	})

	publicRoutes := []struct{ method, path string }{
		{http.MethodGet, "/api/auth/setup-status"},
		{http.MethodPost, "/api/auth/setup-admin"},
		{http.MethodPost, "/api/auth/register"},
		{http.MethodPost, "/api/auth/login"},
		{http.MethodPost, "/api/auth/totp"},
		{http.MethodPost, "/api/auth/refresh"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/auth/password-reset-available"},
		{http.MethodPost, "/api/auth/forgot-password"},
		{http.MethodPost, "/api/auth/reset-password"},
		{http.MethodGet, "/api/auth/email-change-available"},
		{http.MethodPost, "/api/auth/confirm-email-change"},
		{http.MethodGet, "/api/oauth/auth"},
		{http.MethodGet, "/api/oauth/callback"},
	}
	// Public routes must never pass through either authentication middleware.
	// Use the override handler so no database is needed to observe this.
	publicMux := http.NewServeMux()
	api.registerRoutesWithHandler(publicMux, denyAll(&authCalls), denyAll(&allowMustChangeCalls), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, route := range publicRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			authCalls, allowMustChangeCalls = 0, 0
			rec := httptest.NewRecorder()
			publicMux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if authCalls != 0 || allowMustChangeCalls != 0 {
				t.Fatalf("public route invoked auth middleware (auth=%d, allowMustChange=%d)", authCalls, allowMustChangeCalls)
			}
		})
	}

	if got, want := len(api.routes()), len(authenticatedRoutes)+1+len(publicRoutes); got != want {
		t.Fatalf("account module route count = %d, want %d", got, want)
	}
}
