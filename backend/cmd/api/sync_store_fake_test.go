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

// fakeSyncStore overrides only the db.SyncStore methods a test needs. The
// embedded nil interface makes any unexpected call panic loudly.
type fakeSyncStore struct {
	db.SyncStore

	jobs    map[string]db.SyncJob // keyed by id; UserID is the owner
	threads map[string]int
	listErr error
	updates []string
	scopeErr error
}

func newFakeSyncStore() *fakeSyncStore {
	return &fakeSyncStore{jobs: map[string]db.SyncJob{}, threads: map[string]int{}}
}

func (f *fakeSyncStore) add(id, owner string) {
	f.jobs[id] = db.SyncJob{ID: id, UserID: owner, Status: "IDLE"}
}

func (f *fakeSyncStore) VerifySyncJobOwnershipContext(_ context.Context, id, userID string) (bool, error) {
	job, ok := f.jobs[id]
	return ok && job.UserID == userID, nil
}

func (f *fakeSyncStore) GetOwnedSyncJobContext(_ context.Context, id, userID string) (*db.SyncJob, error) {
	job, ok := f.jobs[id]
	if !ok || job.UserID != userID {
		return nil, sql.ErrNoRows
	}
	return &job, nil
}

func (f *fakeSyncStore) GetSyncJobsForUserContext(_ context.Context, userID string) ([]db.SyncJob, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.SyncJob
	for _, job := range f.jobs {
		if job.UserID == userID {
			out = append(out, job)
		}
	}
	return out, nil
}

func (f *fakeSyncStore) UpdateSyncJobThreads(id string, threads int) error {
	f.updates = append(f.updates, "threads:"+id)
	f.threads[id] = threads
	return nil
}

// fakeErrorTasks serves the sync error list for the sync errors handler.
type fakeErrorTasks struct {
	db.TaskStore
	items []db.ErrorListItem
	total int
	asked string
}

func (f *fakeErrorTasks) GetSyncErrorsContext(_ context.Context, syncJobID string, _, _ int) ([]db.ErrorListItem, int, error) {
	f.asked = syncJobID
	return f.items, f.total, nil
}

func newFakeStoreSyncAPI(syncs db.SyncStore, tasks db.TaskStore) *syncAPI {
	return newSyncAPI(syncDependencies{
		syncs:       syncs,
		tasks:       tasks,
		rateLimiter: allowAllFileRateLimiter{},
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
		},
	})
}

func serveSyncStoreAPI(api *syncAPI, method, target, userID, body string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)
	return performMigrationModuleRequest(mux, method, target, userID, body)
}

func TestSyncHandlersUseTheSyncStore(t *testing.T) {
	t.Run("list returns only the callers sync jobs", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		store.add("theirs", "user-2")

		rec := serveSyncStoreAPI(newFakeStoreSyncAPI(store, nil), http.MethodGet, "/api/sync", "user-1", "")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"mine"`) || strings.Contains(rec.Body.String(), `"theirs"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("list failure is a typed 500", func(t *testing.T) {
		store := newFakeSyncStore()
		store.listErr = errors.New("boom")
		rec := serveSyncStoreAPI(newFakeStoreSyncAPI(store, nil), http.MethodGet, "/api/sync", "user-1", "")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("set threads on a foreign job is a 404 that writes nothing", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("theirs", "user-2")
		rec := serveSyncStoreAPI(newFakeStoreSyncAPI(store, nil), http.MethodPut, "/api/sync/theirs/threads", "user-1", `{"threads":4}`)
		assertAPIError(t, rec, http.StatusNotFound, ErrSyncNotFound)
		if len(store.updates) != 0 {
			t.Fatalf("store updates = %v, want none", store.updates)
		}
	})

	t.Run("set threads on an owned job writes through the store", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		rec := serveSyncStoreAPI(newFakeStoreSyncAPI(store, nil), http.MethodPut, "/api/sync/mine/threads", "user-1", `{"threads":8}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if store.threads["mine"] != 8 {
			t.Fatalf("threads = %v", store.threads)
		}
	})

	t.Run("out of range threads never reach the store", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		rec := serveSyncStoreAPI(newFakeStoreSyncAPI(store, nil), http.MethodPut, "/api/sync/mine/threads", "user-1", `{"threads":99}`)
		assertAPIError(t, rec, http.StatusBadRequest, ErrThreadsOutOfRange)
		if len(store.updates) != 0 {
			t.Fatalf("store updates = %v, want none", store.updates)
		}
	})

	t.Run("the error list is served by the task store after an ownership check", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		tasks := &fakeErrorTasks{items: []db.ErrorListItem{{Path: "/a.txt", Status: "FAILED"}}, total: 1}
		api := newFakeStoreSyncAPI(store, tasks)

		rec := serveSyncStoreAPI(api, http.MethodGet, "/api/sync/mine/errors", "user-1", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/a.txt") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if tasks.asked != "mine" {
			t.Errorf("task store asked for %q, want mine", tasks.asked)
		}

		tasks.asked = ""
		rec = serveSyncStoreAPI(api, http.MethodGet, "/api/sync/mine/errors", "user-2", "")
		assertAPIError(t, rec, http.StatusNotFound, ErrSyncNotFound)
		if tasks.asked != "" {
			t.Errorf("task store was queried for a foreign job: %q", tasks.asked)
		}
	})
}

func (f *fakeSyncStore) UpdateSyncJobScope(syncJobID string, _ []string, _ string, _ string, _ string, _ *bool) error {
	f.updates = append(f.updates, "scope:"+syncJobID)
	return f.scopeErr
}

func TestSyncUpdateScopeUsesSyncStore(t *testing.T) {
	t.Run("owner can update scope", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		api := newFakeStoreSyncAPI(store, nil)
		rec := serveSyncStoreAPI(api, http.MethodPut, "/api/sync/mine/scope", "user-1", `{"selected_paths":["/docs"]}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.updates) != 1 || store.updates[0] != "scope:mine" {
			t.Fatalf("updates = %v, want [scope:mine]", store.updates)
		}
	})

	t.Run("foreign job returns 404 and invalid state returns 409", func(t *testing.T) {
		store := newFakeSyncStore()
		store.add("mine", "user-1")
		api := newFakeStoreSyncAPI(store, nil)
		rec := serveSyncStoreAPI(api, http.MethodPut, "/api/sync/mine/scope", "user-2", `{"selected_paths":["/docs"]}`)
		assertAPIError(t, rec, http.StatusNotFound, ErrSyncNotFound)

		store.scopeErr = db.ErrSyncInvalidState
		rec = serveSyncStoreAPI(api, http.MethodPut, "/api/sync/mine/scope", "user-1", `{"selected_paths":["/docs"]}`)
		assertAPIError(t, rec, http.StatusConflict, ErrSyncInvalidState)
	})
}
