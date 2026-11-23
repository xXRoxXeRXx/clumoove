package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/db"
)

// fakeSettingsStore overrides only the db.SettingsStore methods a test needs;
// any other call panics through the nil embedded interface.
type fakeSettingsStore struct {
	db.SettingsStore
	values map[string]string
	getErr error
	setErr error
	set    [][2]string
}

func (f *fakeSettingsStore) GetSetting(key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.values[key], nil
}

func (f *fakeSettingsStore) SetSetting(key, value string) error {
	f.set = append(f.set, [2]string{key, value})
	return f.setErr
}

// fakeSetupUsers answers the setup check shown next to the settings.
type fakeSetupUsers struct {
	db.UserStore
	needsSetup bool
	created    []string
}

func (f *fakeSetupUsers) IsSetupRequired() (bool, error) { return f.needsSetup, nil }

func (f *fakeSetupUsers) CreateUser(email, _, _, _ string) (*db.User, error) {
	f.created = append(f.created, email)
	return &db.User{ID: "new-user", Email: email}, nil
}

func newSettingsAdminAPI(settings db.SettingsStore, users db.UserStore) *adminAPI {
	return newAdminAPI(adminDependencies{
		settings: settings,
		users:    users,
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
		},
	})
}

func serveAdminSettings(api *adminAPI, method, target, userID, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	api.registerRoutesWithHandler(mux, adminHeaderAuthMiddleware, adminHeaderAuthMiddleware, nil)
	return performMigrationModuleRequest(mux, method, target, userID, body)
}

func TestAdminSettingsHandlersUseTheSettingsStore(t *testing.T) {
	t.Run("an unset registration flag reads as false and carries the setup state", func(t *testing.T) {
		api := newSettingsAdminAPI(&fakeSettingsStore{values: map[string]string{}}, &fakeSetupUsers{needsSetup: true})
		rec := serveAdminSettings(api, http.MethodGet, "/api/settings", "user-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"registrations_enabled":"false"`) || !strings.Contains(body, `"needs_setup":true`) {
			t.Fatalf("unexpected body: %s", body)
		}
	})

	t.Run("a settings read failure is a typed 500", func(t *testing.T) {
		api := newSettingsAdminAPI(&fakeSettingsStore{getErr: errors.New("boom")}, &fakeSetupUsers{})
		assertAPIError(t, serveAdminSettings(api, http.MethodGet, "/api/settings", "user-1", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("only the registration flag can be written, and only with a boolean", func(t *testing.T) {
		store := &fakeSettingsStore{values: map[string]string{}}
		api := newSettingsAdminAPI(store, &fakeSetupUsers{})

		assertAPIError(t, serveAdminSettings(api, http.MethodPut, "/api/settings", "admin-1", `{"key":"other","value":"true"}`), http.StatusForbidden, ErrSettingForbidden)
		assertAPIError(t, serveAdminSettings(api, http.MethodPut, "/api/settings", "admin-1", `{"key":"registrations_enabled","value":"maybe"}`), http.StatusBadRequest, ErrSettingInvalid)
		if len(store.set) != 0 {
			t.Fatalf("a rejected write reached the store: %v", store.set)
		}

		rec := serveAdminSettings(api, http.MethodPut, "/api/settings", "admin-1", `{"key":"registrations_enabled","value":"true"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.set) != 1 || store.set[0] != [2]string{"registrations_enabled", "true"} {
			t.Fatalf("store writes = %v, want the registration flag set to true", store.set)
		}
	})
}

func TestRegistrationIsGatedByTheSettingsStore(t *testing.T) {
	register := func(settings db.SettingsStore, users db.UserStore) *httptest.ResponseRecorder {
		api := newFakeUserStoreAccountAPI(users)
		api.settings = settings
		mux := http.NewServeMux()
		passthrough := func(next http.Handler) http.Handler { return next }
		api.registerRoutesWithHandler(mux, passthrough, passthrough, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(`{"email":"new@example.test","password":"a-long-enough-password","display_name":"New"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	t.Run("a disabled registration flag refuses before any account is created", func(t *testing.T) {
		users := &fakeSetupUsers{}
		rec := register(&fakeSettingsStore{values: map[string]string{"registrations_enabled": "false"}}, users)
		assertAPIError(t, rec, http.StatusForbidden, ErrRegistrationDisabled)
		if len(users.created) != 0 {
			t.Fatalf("created %v despite disabled registration", users.created)
		}
	})

	t.Run("an unset flag is treated as disabled", func(t *testing.T) {
		rec := register(&fakeSettingsStore{values: map[string]string{}}, &fakeSetupUsers{})
		assertAPIError(t, rec, http.StatusForbidden, ErrRegistrationDisabled)
	})

	t.Run("a settings failure is a typed 500", func(t *testing.T) {
		rec := register(&fakeSettingsStore{getErr: errors.New("boom")}, &fakeSetupUsers{})
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})
}

// adminHeaderAuthMiddleware authenticates the X-Test-User header as an ADMIN.
func adminHeaderAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-Test-User")
		if userID == "" {
			writeError(w, http.StatusUnauthorized, ErrUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID, Role: "ADMIN"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
