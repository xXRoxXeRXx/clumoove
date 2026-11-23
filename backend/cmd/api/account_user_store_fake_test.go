package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/db"
)

// fakeUserStore overrides only the db.UserStore methods a test needs; any other
// call panics through the nil embedded interface.
type fakeUserStore struct {
	db.UserStore

	users        map[string]*db.User // keyed by email
	lookupErr    error
	incrementHit bool // IncrementLoginFailed reports an account lock
	incremented  []string
	reset        []string
	languages    map[string]string
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[string]*db.User{}, languages: map[string]string{}}
}

func (f *fakeUserStore) add(u *db.User) { f.users[u.Email] = u }

func (f *fakeUserStore) GetUserByEmail(_ context.Context, email string) (*db.User, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	u, ok := f.users[email]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

func (f *fakeUserStore) IncrementLoginFailed(userID string, _ int, _ time.Duration) (bool, error) {
	f.incremented = append(f.incremented, userID)
	return f.incrementHit, nil
}

func (f *fakeUserStore) ResetLoginFailed(userID string) error {
	f.reset = append(f.reset, userID)
	return nil
}

func (f *fakeUserStore) UpdateUserLanguage(id, language string) error {
	f.languages[id] = language
	return nil
}

func loginTestUser(t *testing.T, mutate func(*db.User)) *db.User {
	t.Helper()
	hash, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &db.User{ID: "user-1", Email: "a@example.test", PasswordHash: hash, Active: true, Role: "USER"}
	if mutate != nil {
		mutate(u)
	}
	return u
}

func newFakeUserStoreAccountAPI(users db.UserStore) *accountAPI {
	dummy, _ := auth.HashPassword("dummy")
	return newAccountAPI(accountDependencies{
		users:             users,
		jwtSecret:         "test-secret-test-secret-test-secret",
		rateLimiter:       allowAllFileRateLimiter{},
		dummyPasswordHash: dummy,
		requestEnv: requestEnv{
			requireUserID: requireAuthenticatedUserID,
			logf:          func(*http.Request, string, ...any) {},
			warnf:         func(*http.Request, string, ...any) {},
		},
	})
}

func serveLogin(api *accountAPI, email, password string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	api.registerRoutesWithHandler(mux, func(next http.Handler) http.Handler { return next }, func(next http.Handler) http.Handler { return next }, nil)
	body := `{"email":"` + email + `","password":"` + password + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLoginUsesTheUserStore(t *testing.T) {
	t.Run("an unknown email is a generic 401 and touches no counters", func(t *testing.T) {
		store := newFakeUserStore()
		rec := serveLogin(newFakeUserStoreAccountAPI(store), "nobody@example.test", "whatever")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrCredentialsInvalid)
		if len(store.incremented) != 0 || len(store.reset) != 0 {
			t.Fatalf("counters touched: incremented %v, reset %v", store.incremented, store.reset)
		}
	})

	t.Run("a lookup failure is a typed 500", func(t *testing.T) {
		store := newFakeUserStore()
		store.lookupErr = errors.New("boom")
		assertAPIError(t, serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "x"), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("a wrong password records a failed attempt", func(t *testing.T) {
		store := newFakeUserStore()
		store.add(loginTestUser(t, nil))
		rec := serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "wrong")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrCredentialsInvalid)
		if len(store.incremented) != 1 || store.incremented[0] != "user-1" {
			t.Fatalf("incremented = %v, want [user-1]", store.incremented)
		}
	})

	t.Run("the attempt that locks the account is a 429, not a 401", func(t *testing.T) {
		store := newFakeUserStore()
		store.incrementHit = true
		store.add(loginTestUser(t, nil))
		assertAPIError(t, serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "wrong"), http.StatusTooManyRequests, ErrRateLimited)
	})

	t.Run("a locked account is refused without recording another attempt", func(t *testing.T) {
		store := newFakeUserStore()
		store.add(loginTestUser(t, func(u *db.User) {
			u.LoginLockedUntil = sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true}
		}))
		assertAPIError(t, serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "correct-horse"), http.StatusTooManyRequests, ErrRateLimited)
		if len(store.incremented) != 0 || len(store.reset) != 0 {
			t.Fatalf("counters touched on a locked account: incremented %v, reset %v", store.incremented, store.reset)
		}
	})

	t.Run("a disabled account is refused with 403", func(t *testing.T) {
		store := newFakeUserStore()
		store.add(loginTestUser(t, func(u *db.User) { u.Active = false }))
		assertAPIError(t, serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "correct-horse"), http.StatusForbidden, ErrUserDisabled)
		if len(store.incremented) != 0 {
			t.Fatalf("a disabled login recorded an attempt: %v", store.incremented)
		}
	})

	t.Run("a correct password resets the counter and asks for a password change", func(t *testing.T) {
		store := newFakeUserStore()
		store.add(loginTestUser(t, func(u *db.User) { u.MustChangePassword = true }))
		rec := serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "correct-horse")
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "must_change_password") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.reset) != 1 || store.reset[0] != "user-1" {
			t.Fatalf("reset = %v, want [user-1]", store.reset)
		}
	})

	t.Run("a correct password with 2FA enabled returns a temporary session", func(t *testing.T) {
		store := newFakeUserStore()
		store.add(loginTestUser(t, func(u *db.User) { u.TotpEnabled = true }))
		rec := serveLogin(newFakeUserStoreAccountAPI(store), "a@example.test", "correct-horse")
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "totp_required") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestUpdateLanguageWritesThroughTheUserStore(t *testing.T) {
	store := newFakeUserStore()
	api := newFakeUserStoreAccountAPI(store)
	mux := http.NewServeMux()
	api.registerRoutesWithHandler(mux, headerAuthMiddleware, headerAuthMiddleware, nil)

	req := func(body string) *httptest.ResponseRecorder {
		return performMigrationModuleRequest(mux, http.MethodPut, "/api/auth/me/language", "user-1", body)
	}

	if rec := req(`{"language":"de"}`); rec.Code != http.StatusOK || store.languages["user-1"] != "de" {
		t.Fatalf("status = %d, stored = %v: %s", rec.Code, store.languages, rec.Body.String())
	}
	if rec := req(`{"language":"fr"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported language status = %d, want 400", rec.Code)
	}
	if store.languages["user-1"] != "de" {
		t.Fatalf("an unsupported language reached the store: %v", store.languages)
	}
}
