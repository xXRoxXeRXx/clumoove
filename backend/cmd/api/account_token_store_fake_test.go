package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/db"
)

const refreshTestOrigin = "http://localhost:5173"

// fakeTokenStore overrides only the db.AuthTokenStore methods a test needs; any
// other call panics through the nil embedded interface.
type fakeTokenStore struct {
	db.AuthTokenStore

	outcome    db.RefreshRotationOutcome
	rotateUser *db.User
	rotateErr  error
	rotatedOld []string
	rotatedNew []string

	sessions     []db.RefreshSession
	listedFor    []string
	deleteResult bool
	deleteErr    error
	revoked      [][2]string // session id, user id
	deleted      []string    // token hashes removed by logout
	deleteTokErr error
}

func (f *fakeTokenStore) RotateRefreshToken(_ context.Context, oldHash, newHash string, _ time.Time) (db.RefreshRotationOutcome, *db.User, error) {
	f.rotatedOld = append(f.rotatedOld, oldHash)
	f.rotatedNew = append(f.rotatedNew, newHash)
	return f.outcome, f.rotateUser, f.rotateErr
}

func (f *fakeTokenStore) ListRefreshSessions(_ context.Context, userID string) ([]db.RefreshSession, error) {
	f.listedFor = append(f.listedFor, userID)
	return f.sessions, nil
}

func (f *fakeTokenStore) DeleteRefreshSessionForUser(_ context.Context, sessionID, userID string) (bool, error) {
	f.revoked = append(f.revoked, [2]string{sessionID, userID})
	return f.deleteResult, f.deleteErr
}

func (f *fakeTokenStore) DeleteRefreshToken(tokenHash string) error {
	f.deleted = append(f.deleted, tokenHash)
	return f.deleteTokErr
}

func newFakeTokenStoreAccountAPI(tokens db.AuthTokenStore) *accountAPI {
	return newAccountAPI(accountDependencies{
		tokens:      tokens,
		jwtSecret:   "test-secret-test-secret-test-secret",
		rateLimiter: allowAllFileRateLimiter{},
		requestEnv: requestEnv{
			logf:       func(*http.Request, string, ...any) {},
			warnf:      func(*http.Request, string, ...any) {},
			clientIP:   func(*http.Request) string { return "203.0.113.1" },
			writeAudit: func(*http.Request, db.AuditAction, string, string, map[string]interface{}) {},
		},
	})
}

func serveCookieRoute(api *accountAPI, method, target, cookie string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	passthrough := func(next http.Handler) http.Handler { return next }
	api.registerRoutesWithHandler(mux, passthrough, passthrough, nil)
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("Origin", refreshTestOrigin)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: cookie})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func clearsRefreshCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "refresh_token" && (c.MaxAge < 0 || c.Value == "") {
			return true
		}
	}
	return false
}

func TestRefreshRotatesThroughTheTokenStore(t *testing.T) {
	t.Run("a missing cookie never reaches the store", func(t *testing.T) {
		store := &fakeTokenStore{}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrRefreshTokenMissing)
		if len(store.rotatedOld) != 0 {
			t.Fatalf("store rotated %v, want nothing", store.rotatedOld)
		}
	})

	t.Run("an invalid or expired token is a 401 and clears the cookie", func(t *testing.T) {
		store := &fakeTokenStore{outcome: db.RefreshTokenInvalid}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "stale")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrRefreshTokenInvalid)
		if !clearsRefreshCookie(rec) {
			t.Error("the stale refresh cookie was not cleared")
		}
		if len(store.rotatedOld) != 1 || store.rotatedOld[0] != hashToken("stale") {
			t.Fatalf("store was asked to rotate %v, want the hash of the presented token", store.rotatedOld)
		}
	})

	t.Run("a suspended account is a 403 and clears the cookie", func(t *testing.T) {
		store := &fakeTokenStore{outcome: db.RefreshUserDisabled}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "tok")
		assertAPIError(t, rec, http.StatusForbidden, ErrUserDisabled)
		if !clearsRefreshCookie(rec) {
			t.Error("the refresh cookie was not cleared for a suspended account")
		}
	})

	t.Run("a failed cleanup of a suspended account's tokens is a 500 that still clears the cookie", func(t *testing.T) {
		store := &fakeTokenStore{outcome: db.RefreshUserDisabled, rotateErr: errors.New("commit failed")}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "tok")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if !clearsRefreshCookie(rec) {
			t.Error("the refresh cookie was not cleared")
		}
	})

	t.Run("any other failure is a 500 that keeps the cookie for a retry", func(t *testing.T) {
		store := &fakeTokenStore{rotateErr: errors.New("database unavailable")}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "tok")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if clearsRefreshCookie(rec) {
			t.Error("the cookie was cleared on a transient failure")
		}
		if strings.Contains(rec.Body.String(), "database unavailable") {
			t.Fatalf("raw error leaked to the client: %s", rec.Body.String())
		}
	})

	t.Run("a rotation issues a new access token and a different refresh cookie", func(t *testing.T) {
		store := &fakeTokenStore{outcome: db.RefreshRotated, rotateUser: &db.User{ID: "user-1", Email: "a@example.test", Role: "USER", Active: true}}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/refresh", "old-token")

		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "access_token") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var fresh string
		for _, c := range rec.Result().Cookies() {
			if c.Name == "refresh_token" {
				fresh = c.Value
			}
		}
		if fresh == "" || fresh == "old-token" {
			t.Fatalf("refresh cookie = %q, want a newly generated token", fresh)
		}
		if len(store.rotatedNew) != 1 || store.rotatedNew[0] != hashToken(fresh) {
			t.Errorf("store persisted %v, want the hash of the cookie that was set", store.rotatedNew)
		}
	})
}

func TestSessionsAndLogoutUseTheTokenStore(t *testing.T) {
	serve := func(store *fakeTokenStore, method, target, userID string) *httptest.ResponseRecorder {
		api := newFakeTokenStoreAccountAPI(store)
		mux := http.NewServeMux()
		api.registerRoutesWithHandler(mux, headerAuthMiddleware, headerAuthMiddleware, nil)
		return performMigrationModuleRequest(mux, method, target, userID, "")
	}

	t.Run("sessions are listed for the authenticated user only", func(t *testing.T) {
		store := &fakeTokenStore{sessions: []db.RefreshSession{{ID: "s1", UserAgent: "Firefox"}}}
		rec := serve(store, http.MethodGet, "/api/auth/sessions", "user-1")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Firefox") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.listedFor) != 1 || store.listedFor[0] != "user-1" {
			t.Fatalf("listed for %v, want [user-1]", store.listedFor)
		}
	})

	t.Run("a malformed session id never reaches the store", func(t *testing.T) {
		store := &fakeTokenStore{}
		assertAPIError(t, serve(store, http.MethodDelete, "/api/auth/sessions/not-a-uuid", "user-1"), http.StatusNotFound, ErrSessionNotFound)
		if len(store.revoked) != 0 {
			t.Fatalf("revoked = %v, want none", store.revoked)
		}
	})

	t.Run("revoking binds the session to the caller", func(t *testing.T) {
		store := &fakeTokenStore{deleteResult: true}
		const id = "0f1e2d3c-4b5a-4968-8776-655443322110"
		rec := serve(store, http.MethodDelete, "/api/auth/sessions/"+id, "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.revoked) != 1 || store.revoked[0] != [2]string{id, "user-1"} {
			t.Fatalf("revoked = %v, want the session scoped to user-1", store.revoked)
		}
	})

	t.Run("a session of another account is a 404", func(t *testing.T) {
		store := &fakeTokenStore{deleteResult: false}
		rec := serve(store, http.MethodDelete, "/api/auth/sessions/0f1e2d3c-4b5a-4968-8776-655443322110", "user-2")
		assertAPIError(t, rec, http.StatusNotFound, ErrSessionNotFound)
	})

	t.Run("logout removes the presented token and always clears the cookie", func(t *testing.T) {
		store := &fakeTokenStore{deleteTokErr: errors.New("boom")}
		rec := serveCookieRoute(newFakeTokenStoreAccountAPI(store), http.MethodPost, "/api/auth/logout", "my-token")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.deleted) != 1 || store.deleted[0] != hashToken("my-token") {
			t.Fatalf("deleted = %v, want the hash of the presented token", store.deleted)
		}
		if !clearsRefreshCookie(rec) {
			t.Error("the refresh cookie was not cleared")
		}
	})
}
