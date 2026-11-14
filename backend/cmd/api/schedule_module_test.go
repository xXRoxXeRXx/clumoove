package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
)

var scheduleModuleDriverID atomic.Uint64

type scheduleModuleState struct {
	schedules    map[string][]driver.Value // id -> row values
	ownershipErr error
	lookupErr    error
	listErr      error
	deleteErr    error
	deleted      []string
}

var scheduleColumns = []string{
	"id", "user_id", "task_type", "task_id", "cron_expression", "run_at",
	"next_run_at", "is_active", "created_at", "updated_at",
}

func scheduleRow(id, userID, taskType string) []driver.Value {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []driver.Value{id, userID, taskType, "task-" + id, "0 * * * *", nil, nil, true, now, now}
}

func newScheduleModuleTestDB(t *testing.T, state *scheduleModuleState) *sql.DB {
	t.Helper()
	name := "schedule-module-test-" + strconv.FormatUint(scheduleModuleDriverID.Add(1), 10)
	sql.Register(name, scheduleModuleDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

type scheduleModuleDriver struct{ state *scheduleModuleState }

func (d scheduleModuleDriver) Open(string) (driver.Conn, error) {
	return scheduleModuleConn{state: d.state}, nil
}

type scheduleModuleConn struct{ state *scheduleModuleState }

func (c scheduleModuleConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c scheduleModuleConn) Close() error                        { return nil }
func (c scheduleModuleConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c scheduleModuleConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "DELETE FROM schedules") {
		if c.state.deleteErr != nil {
			return nil, c.state.deleteErr
		}
		c.state.deleted = append(c.state.deleted, args[0].Value.(string))
		return driver.RowsAffected(1), nil
	}
	return nil, errors.New("unexpected exec: " + query)
}

func (c scheduleModuleConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "FROM schedules"):
		if c.state.ownershipErr != nil {
			return nil, c.state.ownershipErr
		}
		row, ok := c.state.schedules[args[0].Value.(string)]
		owned := ok && row[1] == args[1].Value.(string)
		return &scheduleModuleRows{columns: []string{"exists"}, rows: [][]driver.Value{{owned}}}, nil
	case strings.Contains(query, "FROM schedules WHERE id = $1"):
		if c.state.lookupErr != nil {
			return nil, c.state.lookupErr
		}
		var rows [][]driver.Value
		if row, ok := c.state.schedules[args[0].Value.(string)]; ok {
			rows = append(rows, row)
		}
		return &scheduleModuleRows{columns: scheduleColumns, rows: rows}, nil
	case strings.Contains(query, "FROM schedules") && strings.Contains(query, "WHERE user_id = $1"):
		if c.state.listErr != nil {
			return nil, c.state.listErr
		}
		var rows [][]driver.Value
		for _, row := range c.state.schedules {
			if row[1] == args[0].Value.(string) {
				rows = append(rows, row)
			}
		}
		return &scheduleModuleRows{columns: scheduleColumns, rows: rows}, nil
	}
	return nil, errors.New("unexpected query: " + query)
}

type scheduleModuleRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *scheduleModuleRows) Columns() []string { return r.columns }
func (r *scheduleModuleRows) Close() error      { return nil }
func (r *scheduleModuleRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

func newScheduleModuleTestAPI(t *testing.T, state *scheduleModuleState) *scheduleAPI {
	t.Helper()
	return newScheduleAPI(scheduleDependencies{
		db:            newScheduleModuleTestDB(t, state),
		requireUserID: requireAuthenticatedUserID,
		logf:          func(*http.Request, string, ...any) {},
	})
}

// serveSchedule exercises the module through its registered HTTP routes with a
// pass-through authentication middleware that injects the given user.
func serveSchedule(t *testing.T, api *scheduleAPI, method, path, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.registerRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if userID != "" {
				r = r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID}))
			}
			next.ServeHTTP(w, r)
		})
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestScheduleModuleOwnsItsRoutes(t *testing.T) {
	api := newScheduleAPI(scheduleDependencies{})
	want := map[string]bool{
		"GET /api/schedule":         true,
		"GET /api/schedule/{id}":    true,
		"DELETE /api/schedule/{id}": true,
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

func TestScheduleModuleRegistersRoutesWithOverrideHandler(t *testing.T) {
	api := newScheduleAPI(scheduleDependencies{})
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

func TestScheduleModuleListRequiresUserAndReturnsOnlyOwnedSchedules(t *testing.T) {
	state := &scheduleModuleState{schedules: map[string][]driver.Value{
		"s1": scheduleRow("s1", "user-1", "sync"),
		"s2": scheduleRow("s2", "user-2", "sync"),
	}}
	api := newScheduleModuleTestAPI(t, state)

	t.Run("unauthenticated", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule", "")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrUnauthorized)
	})

	t.Run("owned only", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var got []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "s1" {
			t.Fatalf("schedules = %+v, want only s1", got)
		}
	})

	t.Run("empty list is a JSON array", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule", "user-3")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("response = (%d, %q), want (200, [])", rec.Code, rec.Body.String())
		}
	})

	t.Run("database failure is typed internal error", func(t *testing.T) {
		failing := newScheduleModuleTestAPI(t, &scheduleModuleState{listErr: errors.New("db down")})
		rec := serveSchedule(t, failing, http.MethodGet, "/api/schedule", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if strings.Contains(rec.Body.String(), "db down") {
			t.Fatalf("raw error leaked to client: %s", rec.Body.String())
		}
	})
}

func TestScheduleModuleGetEnforcesOwnership(t *testing.T) {
	state := &scheduleModuleState{schedules: map[string][]driver.Value{
		"s1": scheduleRow("s1", "user-1", "sync"),
	}}
	api := newScheduleModuleTestAPI(t, state)

	t.Run("owner", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule/s1", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("other user gets anti-enumeration 404", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule/s1", "user-2")
		assertAPIError(t, rec, http.StatusNotFound, ErrScheduleNotFound)
	})
	t.Run("missing schedule gets the same 404", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule/nope", "user-1")
		assertAPIError(t, rec, http.StatusNotFound, ErrScheduleNotFound)
	})
	t.Run("unauthenticated", func(t *testing.T) {
		rec := serveSchedule(t, api, http.MethodGet, "/api/schedule/s1", "")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrUnauthorized)
	})
	t.Run("ownership lookup failure", func(t *testing.T) {
		failing := newScheduleModuleTestAPI(t, &scheduleModuleState{ownershipErr: errors.New("db down")})
		rec := serveSchedule(t, failing, http.MethodGet, "/api/schedule/s1", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})
	t.Run("schedule load failure", func(t *testing.T) {
		failing := newScheduleModuleTestAPI(t, &scheduleModuleState{
			schedules: map[string][]driver.Value{"s1": scheduleRow("s1", "user-1", "sync")},
			lookupErr: errors.New("db down"),
		})
		rec := serveSchedule(t, failing, http.MethodGet, "/api/schedule/s1", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})
}

func TestScheduleModuleDeleteEnforcesOwnershipAndBackupCoupling(t *testing.T) {
	newState := func() *scheduleModuleState {
		return &scheduleModuleState{schedules: map[string][]driver.Value{
			"sync-sched":   scheduleRow("sync-sched", "user-1", "sync"),
			"backup-sched": scheduleRow("backup-sched", "user-1", "backup"),
		}}
	}

	t.Run("owner deletes sync schedule", func(t *testing.T) {
		state := newState()
		rec := serveSchedule(t, newScheduleModuleTestAPI(t, state), http.MethodDelete, "/api/schedule/sync-sched", "user-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(state.deleted) != 1 || state.deleted[0] != "sync-sched" {
			t.Fatalf("deleted = %v, want [sync-sched]", state.deleted)
		}
	})
	t.Run("other user cannot delete", func(t *testing.T) {
		state := newState()
		rec := serveSchedule(t, newScheduleModuleTestAPI(t, state), http.MethodDelete, "/api/schedule/sync-sched", "user-2")
		assertAPIError(t, rec, http.StatusNotFound, ErrScheduleNotFound)
		if len(state.deleted) != 0 {
			t.Fatalf("deleted = %v, want none", state.deleted)
		}
	})
	t.Run("backup schedule is protected with a conflict", func(t *testing.T) {
		state := newState()
		rec := serveSchedule(t, newScheduleModuleTestAPI(t, state), http.MethodDelete, "/api/schedule/backup-sched", "user-1")
		assertAPIError(t, rec, http.StatusConflict, ErrBackupInvalidState)
		if len(state.deleted) != 0 {
			t.Fatalf("deleted = %v, want none", state.deleted)
		}
	})
	t.Run("delete failure is typed internal error", func(t *testing.T) {
		state := newState()
		state.deleteErr = errors.New("db down")
		rec := serveSchedule(t, newScheduleModuleTestAPI(t, state), http.MethodDelete, "/api/schedule/sync-sched", "user-1")
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})
	t.Run("unauthenticated", func(t *testing.T) {
		rec := serveSchedule(t, newScheduleModuleTestAPI(t, newState()), http.MethodDelete, "/api/schedule/sync-sched", "")
		assertAPIError(t, rec, http.StatusUnauthorized, ErrUnauthorized)
	})
}
