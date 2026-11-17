package main

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func newSyncOwnershipLookupHarness(t *testing.T) (http.Handler, *syncModuleDBState) {
	t.Helper()
	database, state := newSyncModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&syncModuleRecord{
		id: "s-1", userID: "user-1", sourceProvider: "nextcloud", targetProvider: "nextcloud",
		status: "IDLE", createdAt: now, updatedAt: now,
	})
	api := newSyncAPI(syncDependencies{
		db:            database,
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   allowAllFileRateLimiter{},
	})
	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)
	return mux, state
}

// Sync handlers that need the job row perform one owner-scoped lookup rather
// than an ownership check followed by a second read of the same row.
func TestSyncHandlersLoadOwnedRowWithSingleLookup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"status", http.MethodGet, "/api/sync/s-1", ""},
		{"report", http.MethodGet, "/api/sync/s-1/report", ""},
		{"bandwidth", http.MethodPut, "/api/sync/s-1/bandwidth", `{"limit_mbps":5}`},
		{"reauth", http.MethodPost, "/api/sync/s-1/reauth", `{"role":"source","access_token":"a","refresh_token":"r"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, state := newSyncOwnershipLookupHarness(t)

			performSyncModuleRequest(mux, tc.method, tc.path, "user-1", tc.body)

			if got := state.lookupCount(); got != 1 {
				t.Fatalf("%s performed %d sync lookups, want 1", tc.name, got)
			}
		})
	}
}

// Foreign and missing jobs share one response, and a failing lookup stays a
// distinguishable 500, whichever handler loads the row.
func TestSyncOwnershipFailuresKeepErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		caller     string
		id         string
		lookupErr  error
		wantStatus int
		wantCode   APIErrorCode
	}{
		{"foreign", "user-2", "s-1", nil, http.StatusNotFound, ErrSyncNotFound},
		{"missing", "user-1", "s-gone", nil, http.StatusNotFound, ErrSyncNotFound},
		{"lookup failure", "user-1", "s-1", errors.New("db down"), http.StatusInternalServerError, ErrInternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, state := newSyncOwnershipLookupHarness(t)
			state.setOwnershipErr(tc.lookupErr)

			rec := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/"+tc.id, tc.caller, "")

			assertAPIResponseError(t, rec, tc.wantStatus, tc.wantCode, tc.name)
		})
	}
}
