package main

import (
	"strings"
	"errors"
	"net/http"
	"testing"
	"time"
)

func newOwnershipLookupHarness(t *testing.T, status string) (http.Handler, *migrationModuleDBState) {
	t.Helper()
	h := newLifecycleHandlerHarness(t, map[string]string{"m-1": status})
	h.state.setMigration(&migrationModuleRecord{
		id: "m-1", userID: "user-1", sourceProvider: "nextcloud", targetProvider: "nextcloud",
		status: status, createdAt: time.Now().UTC(), updatedAt: time.Now().UTC(),
	})
	return h.mux, h.state
}

// Handlers that need the migration row perform one owner-scoped lookup rather
// than an ownership check followed by a second read of the same row.
func TestMigrationHandlersLoadOwnedRowWithSingleLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		status string
	}{
		{"resume", http.MethodPost, "/api/migration/m-1/resume", "", "PAUSED"},
		{"retry-failed", http.MethodPost, "/api/migration/m-1/retry-failed", "", "FAILED"},
		{"reindex", http.MethodPost, "/api/migration/m-1/reindex", "", "FAILED"},
		{"bandwidth", http.MethodPut, "/api/migration/m-1/bandwidth", `{"limit_mbps":5}`, "RUNNING"},
		{"errors", http.MethodGet, "/api/migration/m-1/errors", "", "RUNNING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, state := newOwnershipLookupHarness(t, tc.status)

			rec := performMigrationModuleRequest(mux, tc.method, tc.path, "user-1", tc.body)

			if rec.Code >= 400 {
				t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body.String())
			}
			if got := state.lookupCount(); got != 1 {
				t.Fatalf("%s performed %d migration lookups, want 1", tc.name, got)
			}
		})
	}
}

// Foreign and missing migrations keep the anti-enumeration contract: the same
// status and code, whichever of the two the caller probes.
func TestMigrationOwnershipFailuresKeepErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		path       string
		wantStatus int
		wantCode   APIErrorCode
		lookupErr  error
	}{
		{"foreign non-strict", "/api/migration/m-1/resume", http.StatusForbidden, ErrMigrationNotOwned, nil},
		{"missing non-strict", "/api/migration/m-gone/resume", http.StatusForbidden, ErrMigrationNotOwned, nil},
		{"foreign strict", "/api/migration/m-1/errors", http.StatusForbidden, ErrMigrationNotOwned, nil},
		{"missing strict", "/api/migration/m-gone/errors", http.StatusForbidden, ErrMigrationNotOwned, nil},
		{"lookup failure non-strict", "/api/migration/m-1/resume", http.StatusForbidden, ErrMigrationNotOwned, errors.New("db down")},
		{"lookup failure strict", "/api/migration/m-1/errors", http.StatusInternalServerError, ErrInternalError, errors.New("db down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, state := newOwnershipLookupHarness(t, "PAUSED")
			caller := "user-2"
			if tc.lookupErr != nil {
				caller = "user-1"
				state.setOwnershipErr(tc.lookupErr)
			}

			rec := performMigrationModuleRequest(mux, http.MethodGet, tc.path, caller, "")
			if tc.path[len(tc.path)-6:] == "resume" {
				rec = performMigrationModuleRequest(mux, http.MethodPost, tc.path, caller, "")
			}

			assertAPIResponseError(t, rec, tc.wantStatus, tc.wantCode, tc.name)
		})
	}
}

// Detail, report and the detail stream are owner-scoped in SQL: another
// account's row is never loaded, yet a missing id is still 404 and a foreign
// one still 403.
func TestMigrationDetailEndpointsNeverReadForeignRows(t *testing.T) {
	for _, path := range []string{"/api/migration/m-1", "/api/migration/m-1/report", "/api/migration/m-1/stream"} {
		t.Run(path, func(t *testing.T) {
			mux, state := newOwnershipLookupHarness(t, "COMPLETED")

			rec := performMigrationModuleRequest(mux, http.MethodGet, path, "user-2", "")
			assertAPIResponseError(t, rec, http.StatusForbidden, ErrMigrationNotOwned, "foreign "+path)

			gone := strings.Replace(path, "m-1", "m-gone", 1)
			rec = performMigrationModuleRequest(mux, http.MethodGet, gone, "user-1", "")
			assertAPIResponseError(t, rec, http.StatusNotFound, ErrMigrationNotFound, "missing "+gone)

			if got := state.unscopedReads(); got != 0 {
				t.Fatalf("%d unscoped migration row reads, want 0", got)
			}
		})
	}
}

func TestMigrationDetailLookupFailureIsInternalError(t *testing.T) {
	mux, state := newOwnershipLookupHarness(t, "COMPLETED")
	state.setOwnershipErr(errors.New("db down"))

	rec := performMigrationModuleRequest(mux, http.MethodGet, "/api/migration/m-1", "user-1", "")

	assertAPIResponseError(t, rec, http.StatusInternalServerError, ErrInternalError, "detail lookup failure")
}
