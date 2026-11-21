package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/auth"
	"backend/internal/oauth"
)

func TestAdminModuleRegistersRoutesWithExpectedAccessControls(t *testing.T) {
	api := newAdminAPI(adminDependencies{})
	mux := http.NewServeMux()
	authCalls, adminCalls := 0, 0
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	}
	adminOnly := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			adminCalls++
			writeError(w, http.StatusForbidden, ErrAdminOnly)
		})
	}
	api.registerRoutesWithHandler(mux, authenticated, adminOnly, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	for _, spec := range api.routes() {
		t.Run(spec.pattern, func(t *testing.T) {
			authCalls, adminCalls = 0, 0
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, requestForRoutePattern(spec.pattern))
			switch spec.access {
			case routePublic:
				if rec.Code != http.StatusNoContent || authCalls != 0 || adminCalls != 0 {
					t.Fatalf("public route: status=%d auth=%d admin=%d, want 204 with no middleware", rec.Code, authCalls, adminCalls)
				}
			case routeAdmin:
				assertAPIResponseError(t, rec, http.StatusForbidden, ErrAdminOnly, spec.pattern)
				if adminCalls != 1 || authCalls != 0 {
					t.Fatalf("admin calls = %d, auth calls = %d, want 1 and 0", adminCalls, authCalls)
				}
			default:
				t.Fatalf("admin module must only expose public or admin routes, got %v", spec.access)
			}
		})
	}
}

func TestAdminModuleOnlyPublishesInstanceSettingsReadAsPublic(t *testing.T) {
	for _, spec := range newAdminAPI(adminDependencies{}).routes() {
		if spec.access == routePublic && spec.pattern != "GET /api/settings" {
			t.Errorf("unexpected public admin-module route %s", spec.pattern)
		}
	}
}

func TestAdminModuleHandlersRejectNonAdminClaimsWithoutTouchingDependencies(t *testing.T) {
	api := newAdminAPI(adminDependencies{})
	for _, spec := range api.routes() {
		if spec.access != routeAdmin {
			continue
		}
		t.Run(spec.pattern, func(t *testing.T) {
			for name, claims := range map[string]*auth.Claims{"missing": nil, "user": {UserID: "u1", Role: "USER"}} {
				req := requestForRoutePattern(spec.pattern)
				if claims != nil {
					req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
				}
				rec := httptest.NewRecorder()
				spec.handler(rec, req)
				assertAPIResponseError(t, rec, http.StatusForbidden, ErrAdminOnly, name+" "+spec.pattern)
			}
		})
	}
}

func TestAdminOAuthProviderOrderIncludesBox(t *testing.T) {
	want := []string{"google", "onedrive", "dropbox", "hidrive", "box"}
	if len(oauthProviderOrder) != len(want) {
		t.Fatalf("oauthProviderOrder = %v, want %v", oauthProviderOrder, want)
	}
	for i, provider := range want {
		if oauthProviderOrder[i] != provider {
			t.Fatalf("oauthProviderOrder[%d] = %q, want %q", i, oauthProviderOrder[i], provider)
		}
		if !oauth.IsProvider(provider) {
			t.Fatalf("oauth.IsProvider(%q) = false, want true", provider)
		}
	}
}
