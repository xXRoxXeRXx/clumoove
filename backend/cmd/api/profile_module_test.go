package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/crypto"
)

func TestProfileModuleOwnsItsRoutes(t *testing.T) {
	api := newProfileAPI(profileDependencies{})
	want := map[string]bool{
		"GET /api/profiles":            true,
		"POST /api/profiles":           true,
		"GET /api/profiles/{id}":       true,
		"PUT /api/profiles/{id}":       true,
		"DELETE /api/profiles/{id}":    true,
		"POST /api/profiles/{id}/test": true,
	}
	routes := api.routes()
	if len(routes) != len(want) {
		t.Fatalf("routes = %d, want %d", len(routes), len(want))
	}
	for _, r := range routes {
		if !want[r.pattern] {
			t.Errorf("unexpected route %s", r.pattern)
		}
		if r.access != routeAuthenticated {
			t.Errorf("route %s access = %v, want authenticated", r.pattern, r.access)
		}
		if r.handler == nil {
			t.Errorf("route %s has nil handler", r.pattern)
		}
	}
}

func TestProfileModuleRegistersRoutesWithOverrideHandler(t *testing.T) {
	api := newProfileAPI(profileDependencies{})
	mux := http.NewServeMux()
	authCalls := 0
	api.registerRoutesWithHandler(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, spec := range api.routes() {
		authCalls = 0
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, requestForRoutePattern(spec.pattern))
		if rec.Code != http.StatusNoContent || authCalls != 1 {
			t.Errorf("%s: status=%d authCalls=%d, want 204 and 1", spec.pattern, rec.Code, authCalls)
		}
	}
}

func serveProfile(t *testing.T, api *profileAPI, method, path, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.registerRoutesWithHandler(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if userID != "" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID}))
			}
			next.ServeHTTP(w, r)
		})
	}, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestProfileModuleRateLimitsMutatingAndTestRoutes(t *testing.T) {
	api := newProfileAPI(profileDependencies{
		rateLimiter:   blockAllFileRateLimiter{},
		requireUserID: requireAuthenticatedUserID,
		clientIP:      func(*http.Request) string { return "203.0.113.1" },
	})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/profiles"},
		{http.MethodPut, "/api/profiles/profile-1"},
		{http.MethodPost, "/api/profiles/profile-1/test"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := serveProfile(t, api, route.method, route.path, "user-1")
			assertAPIError(t, rec, http.StatusTooManyRequests, ErrRateLimited)
		})
	}
}

func TestProfileModuleRequiresAuthenticatedUser(t *testing.T) {
	api := newProfileAPI(profileDependencies{
		rateLimiter:   allowAllFileRateLimiter{},
		requireUserID: requireAuthenticatedUserID,
		clientIP:      func(*http.Request) string { return "203.0.113.1" },
	})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/profiles"},
		{http.MethodPost, "/api/profiles"},
		{http.MethodGet, "/api/profiles/profile-1"},
		{http.MethodPut, "/api/profiles/profile-1"},
		{http.MethodDelete, "/api/profiles/profile-1"},
		{http.MethodPost, "/api/profiles/profile-1/test"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rec := serveProfile(t, api, route.method, route.path, "")
			assertAPIError(t, rec, http.StatusUnauthorized, ErrUnauthorized)
		})
	}
}

func TestProfileModuleGetMapsOwnershipOutcomes(t *testing.T) {
	const key = "01234567890123456789012345678901"
	password, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatal(err)
	}
	database, state := newBackupProfileConnectionTestDB(t, map[string][]driver.Value{
		"profile-1": profileTestRow("profile-1", "user-1", "nextcloud", "https://saved.example.test", "saved-user", password),
	})
	api := newProfileAPI(profileDependencies{
		profiles:      &ownedProfileAccess{db: database, encryptionKey: key},
		rateLimiter:   allowAllFileRateLimiter{},
		requireUserID: requireAuthenticatedUserID,
		logf:          func(*http.Request, string, ...any) {},
	})

	t.Run("owner", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles/profile-1", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "saved-secret") {
			t.Fatalf("response leaks credential material: %s", rec.Body.String())
		}
	})
	t.Run("missing profile is a typed 404", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles/nope", "user-1")
		assertAPIError(t, rec, http.StatusNotFound, ErrProfileNotFound)
	})
	t.Run("ownership lookup failure is a typed 500 without raw error", func(t *testing.T) {
		state.ownershipErr = errors.New("database unavailable")
		defer func() { state.ownershipErr = nil }()
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles/profile-1", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if strings.Contains(rec.Body.String(), "database unavailable") {
			t.Fatalf("raw error leaked to client: %s", rec.Body.String())
		}
	})
}
