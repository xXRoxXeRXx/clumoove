package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/db"
)

// fakeMigrationStore overrides only the db.MigrationStore methods a test needs.
// The embedded nil interface makes any unexpected call panic loudly instead of
// silently succeeding.
type fakeMigrationStore struct {
	db.MigrationStore

	migrations map[string]db.Migration // keyed by id; the owner is UserID
	calls      []string

	transitionApplied bool
	transitionErr     error
	cancelPendingErr  error
	cascadeErr        error
	listErr           error
	threads           map[string]int
}

func newFakeMigrationStore() *fakeMigrationStore {
	return &fakeMigrationStore{
		migrations:        map[string]db.Migration{},
		transitionApplied: true,
		threads:           map[string]int{},
	}
}

func (f *fakeMigrationStore) add(id, owner string) {
	f.migrations[id] = db.Migration{ID: id, UserID: sql.NullString{String: owner, Valid: true}, Status: "RUNNING"}
}

func (f *fakeMigrationStore) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeMigrationStore) called(call string) bool {
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeMigrationStore) GetOwnedMigrationContext(_ context.Context, id, userID string) (*db.Migration, error) {
	m, ok := f.migrations[id]
	if !ok || m.UserID.String != userID {
		return nil, sql.ErrNoRows
	}
	return &m, nil
}

func (f *fakeMigrationStore) MigrationExistsContext(_ context.Context, id string) (bool, error) {
	_, ok := f.migrations[id]
	return ok, nil
}

func (f *fakeMigrationStore) GetMigrationsForUserContext(_ context.Context, userID string) ([]db.Migration, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := []db.Migration{}
	for _, m := range f.migrations {
		if m.UserID.String == userID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeMigrationStore) TransitionMigration(_ context.Context, id string, event db.MigrationEvent, _ *string) (bool, error) {
	f.record("transition:" + string(event) + ":" + id)
	return f.transitionApplied, f.transitionErr
}

func (f *fakeMigrationStore) CancelPendingTasks(id string) error {
	f.record("cancel-pending:" + id)
	return f.cancelPendingErr
}

func (f *fakeMigrationStore) DeleteMigrationCascade(id string) error {
	f.record("cascade:" + id)
	return f.cascadeErr
}

func (f *fakeMigrationStore) UpdateMigrationThreads(id string, threads int) error {
	f.threads[id] = threads
	return nil
}

func newFakeStoreMigrationAPI(store db.MigrationStore, cancelled *[]string) *migrationAPI {
	return newMigrationAPI(migrationDependencies{
		store:       store,
		rateLimiter: allowAllFileRateLimiter{},
		publishCancelEvent: func(_ context.Context, id string) error {
			if cancelled != nil {
				*cancelled = append(*cancelled, id)
			}
			return nil
		},
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
		},
	})
}

func serveMigrationStoreAPI(api *migrationAPI, method, target, userID, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)
	return performMigrationModuleRequest(mux, method, target, userID, body)
}

func TestMigrationHandlersUseTheMigrationStore(t *testing.T) {
	t.Run("list returns only the callers migrations", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		store.add("theirs", "user-2")
		api := newFakeStoreMigrationAPI(store, nil)

		rec := serveMigrationStoreAPI(api, http.MethodGet, "/api/migration", "user-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"mine"`) || strings.Contains(rec.Body.String(), `"theirs"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("list failure is a typed 500", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.listErr = errors.New("boom")
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodGet, "/api/migration", "user-1", "")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("pause of a foreign migration never transitions it", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("theirs", "user-2")
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodPost, "/api/migration/theirs/pause", "user-1", "")
		assertAPIError(t, rec, http.StatusForbidden, ErrMigrationNotOwned)
		if len(store.calls) != 0 {
			t.Fatalf("store calls = %v, want none", store.calls)
		}
	})

	t.Run("pause that does not apply is a 409", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		store.transitionApplied = false
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodPost, "/api/migration/mine/pause", "user-1", "")
		assertAPIError(t, rec, http.StatusConflict, ErrMigrationInvalidState)
	})

	t.Run("cancel transitions, cancels pending tasks and publishes", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		var published []string
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, &published), http.MethodPost, "/api/migration/mine/cancel", "user-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !store.called("transition:CANCEL:mine") || !store.called("cancel-pending:mine") {
			t.Fatalf("store calls = %v", store.calls)
		}
		if len(published) != 1 || published[0] != "mine" {
			t.Fatalf("published = %v", published)
		}
	})

	t.Run("cancel that does not apply leaves tasks alone", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		store.transitionApplied = false
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodPost, "/api/migration/mine/cancel", "user-1", "")
		assertAPIError(t, rec, http.StatusConflict, ErrMigrationInvalidState)
		if store.called("cancel-pending:mine") {
			t.Fatalf("pending tasks were cancelled for a transition that did not apply")
		}
	})

	t.Run("delete refuses to cascade when pending tasks could not be cancelled", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		store.cancelPendingErr = errors.New("boom")
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodDelete, "/api/migration/mine", "user-1", "")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if store.called("cascade:mine") {
			t.Fatalf("migration was deleted although its pending tasks were not cancelled")
		}
	})

	t.Run("set threads writes through the store", func(t *testing.T) {
		store := newFakeMigrationStore()
		store.add("mine", "user-1")
		rec := serveMigrationStoreAPI(newFakeStoreMigrationAPI(store, nil), http.MethodPut, "/api/migration/mine/threads", "user-1", `{"threads":8}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if store.threads["mine"] != 8 {
			t.Fatalf("threads = %v", store.threads)
		}
	})
}
