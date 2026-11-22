package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/auth"
	"backend/internal/db"
)

// fakeProfileStore is an in-memory db.ProfileStore for handler tests. owners
// maps profile id to owning user id.
type fakeProfileStore struct {
	profiles  map[string]db.ConnectionProfile
	owners    map[string]string
	createErr error
	listErr   error
	deleted   []string
	created   []*db.ConnectionProfile
	updated   map[string]db.UpdateConnectionProfileInput
}

func newFakeProfileStore() *fakeProfileStore {
	return &fakeProfileStore{
		profiles: map[string]db.ConnectionProfile{},
		owners:   map[string]string{},
		updated:  map[string]db.UpdateConnectionProfileInput{},
	}
}

func (f *fakeProfileStore) add(p db.ConnectionProfile) {
	f.profiles[p.ID] = p
	f.owners[p.ID] = p.UserID
}

func (f *fakeProfileStore) CreateConnectionProfile(_ context.Context, p *db.ConnectionProfile) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	p.ID = "created-1"
	f.created = append(f.created, p)
	return p.ID, nil
}

func (f *fakeProfileStore) GetConnectionProfile(_ context.Context, id string) (*db.ConnectionProfile, error) {
	p, ok := f.profiles[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &p, nil
}

func (f *fakeProfileStore) ListConnectionProfiles(_ context.Context, userID string) ([]db.ConnectionProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.ConnectionProfile
	for _, p := range f.profiles {
		if p.UserID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProfileStore) UpdateConnectionProfile(_ context.Context, id string, in db.UpdateConnectionProfileInput) error {
	f.updated[id] = in
	return nil
}

func (f *fakeProfileStore) UpdateConnectionProfileMegaSession(context.Context, string, string, string, string) error {
	return nil
}

func (f *fakeProfileStore) DeleteConnectionProfile(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeProfileStore) VerifyProfileOwnership(_ context.Context, profileID, userID string) (bool, error) {
	return f.owners[profileID] == userID && userID != "", nil
}

func (f *fakeProfileStore) TokenSink(string) db.OAuthTokenSink { return nil }

func newFakeStoreProfileAPI(store db.ProfileStore) *profileAPI {
	return newProfileAPI(profileDependencies{
		store:         store,
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   allowAllFileRateLimiter{},
		requestEnv: requestEnv{
			requireUserID: requireAuthenticatedUserID,
			clientIP:      func(*http.Request) string { return "203.0.113.1" },
			logf:          func(*http.Request, string, ...any) {},
		},
	})
}

func TestProfileHandlersUseTheProfileStore(t *testing.T) {
	store := newFakeProfileStore()
	store.add(db.ConnectionProfile{ID: "mine", UserID: "user-1", Name: "Mine", Provider: "nextcloud", URL: "https://a.example.test"})
	store.add(db.ConnectionProfile{ID: "theirs", UserID: "user-2", Name: "Theirs", Provider: "nextcloud", URL: "https://b.example.test"})
	api := newFakeStoreProfileAPI(store)

	t.Run("list returns only the caller's profiles", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Mine") || strings.Contains(rec.Body.String(), "Theirs") {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("list failure is a typed 500", func(t *testing.T) {
		store.listErr = errors.New("boom")
		defer func() { store.listErr = nil }()
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("get of a foreign profile is a 404", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodGet, "/api/profiles/theirs", "user-1")
		assertAPIError(t, rec, http.StatusNotFound, ErrProfileNotFound)
	})

	t.Run("delete of a foreign profile never reaches the store", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodDelete, "/api/profiles/theirs", "user-1")
		assertAPIError(t, rec, http.StatusNotFound, ErrProfileNotFound)
		if len(store.deleted) != 0 {
			t.Fatalf("deleted = %v, want none", store.deleted)
		}
	})

	t.Run("delete of an owned profile", func(t *testing.T) {
		rec := serveProfile(t, api, http.MethodDelete, "/api/profiles/mine", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.deleted) != 1 || store.deleted[0] != "mine" {
			t.Fatalf("deleted = %v, want [mine]", store.deleted)
		}
	})
}

func TestCreateProfileMapsStoreErrors(t *testing.T) {
	store := newFakeProfileStore()
	api := newFakeStoreProfileAPI(store)
	body := `{"name":"n","provider":"nextcloud","url":"https://nc.example.test","username":"u","password":"p"}`

	post := func() *httptest.ResponseRecorder {
		return servePostAsUser(t, api.registerRoutesWithHandler, "/api/profiles", "user-1", body)
	}

	rec := post()
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.created) != 1 || store.created[0].UserID != "user-1" {
		t.Fatalf("created = %+v", store.created)
	}

	store.createErr = errors.New("insert exploded")
	rec = post()
	assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
}

func servePostAsUser(t *testing.T, register authenticatedRegistrar, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	register(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID})))
		})
	}, nil)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
