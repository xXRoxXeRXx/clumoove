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

// fakeScheduleStore overrides only the db.ScheduleStore methods a test needs;
// any other call panics through the nil embedded interface.
type fakeScheduleStore struct {
	db.ScheduleStore
	schedules map[string]db.Schedule // keyed by id; UserID is the owner
	deleted   []string
	listErr   error
	reads     []string
}

func newFakeScheduleStore() *fakeScheduleStore {
	return &fakeScheduleStore{schedules: map[string]db.Schedule{}}
}

func (f *fakeScheduleStore) add(id, owner, taskType string) {
	f.schedules[id] = db.Schedule{ID: id, UserID: owner, TaskType: taskType}
}

func (f *fakeScheduleStore) VerifyScheduleOwnershipContext(_ context.Context, id, userID string) (bool, error) {
	s, ok := f.schedules[id]
	return ok && s.UserID == userID, nil
}

func (f *fakeScheduleStore) GetScheduleContext(_ context.Context, id string) (*db.Schedule, error) {
	f.reads = append(f.reads, id)
	s, ok := f.schedules[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &s, nil
}

func (f *fakeScheduleStore) GetSchedulesForUserContext(_ context.Context, userID string) ([]db.Schedule, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.Schedule
	for _, s := range f.schedules {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeScheduleStore) DeleteSchedule(id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func serveScheduleStoreAPI(store db.ScheduleStore, method, target, userID string) *httptest.ResponseRecorder {
	api := newScheduleAPI(scheduleDependencies{
		store: store,
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
		},
	})
	mux := http.NewServeMux()
	api.registerRoutesWithHandler(mux, headerAuthMiddleware, nil)
	return performMigrationModuleRequest(mux, method, target, userID, "")
}

func TestScheduleHandlersUseTheScheduleStore(t *testing.T) {
	t.Run("list returns only the callers schedules", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.add("mine", "user-1", "sync")
		store.add("theirs", "user-2", "sync")

		rec := serveScheduleStoreAPI(store, http.MethodGet, "/api/schedule", "user-1")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"mine"`) || strings.Contains(rec.Body.String(), `"theirs"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("list failure is a typed 500", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.listErr = errors.New("boom")
		assertAPIError(t, serveScheduleStoreAPI(store, http.MethodGet, "/api/schedule", "user-1"), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("a foreign schedule is a 404 that is never loaded", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.add("theirs", "user-2", "sync")

		assertAPIError(t, serveScheduleStoreAPI(store, http.MethodGet, "/api/schedule/theirs", "user-1"), http.StatusNotFound, ErrScheduleNotFound)
		if len(store.reads) != 0 {
			t.Fatalf("schedule rows read = %v, want none for a foreign schedule", store.reads)
		}
	})

	t.Run("deleting a foreign schedule writes nothing", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.add("theirs", "user-2", "sync")

		assertAPIError(t, serveScheduleStoreAPI(store, http.MethodDelete, "/api/schedule/theirs", "user-1"), http.StatusNotFound, ErrScheduleNotFound)
		if len(store.deleted) != 0 {
			t.Fatalf("deleted = %v, want none", store.deleted)
		}
	})

	t.Run("a backup schedule is refused with a conflict", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.add("backup-sched", "user-1", "backup")

		assertAPIError(t, serveScheduleStoreAPI(store, http.MethodDelete, "/api/schedule/backup-sched", "user-1"), http.StatusConflict, ErrBackupInvalidState)
		if len(store.deleted) != 0 {
			t.Fatalf("deleted = %v, want none", store.deleted)
		}
	})

	t.Run("an owned schedule is deleted through the store", func(t *testing.T) {
		store := newFakeScheduleStore()
		store.add("mine", "user-1", "sync")

		rec := serveScheduleStoreAPI(store, http.MethodDelete, "/api/schedule/mine", "user-1")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.deleted) != 1 || store.deleted[0] != "mine" {
			t.Fatalf("deleted = %v, want [mine]", store.deleted)
		}
	})
}
