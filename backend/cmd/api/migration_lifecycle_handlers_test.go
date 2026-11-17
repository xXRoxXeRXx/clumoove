package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/db"
)

type lifecycleHandlerHarness struct {
	mux       http.Handler
	state     *migrationModuleDBState
	audits    *[]db.AuditAction
	cancelled *[]string
}

func newLifecycleHandlerHarness(t *testing.T, statuses map[string]string) lifecycleHandlerHarness {
	t.Helper()
	database, state := newMigrationModuleTestDB(t, nil)
	now := time.Now().UTC()
	for id, status := range statuses {
		state.setMigration(&migrationModuleRecord{
			id: id, userID: "user-1", sourceProvider: "nextcloud", targetProvider: "nextcloud",
			status: status, createdAt: now, updatedAt: now,
		})
	}
	audits := []db.AuditAction{}
	cancelled := []string{}
	api := newMigrationAPI(migrationDependencies{
		db:            database,
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   allowAllFileRateLimiter{},
		publishCancelEvent: func(_ context.Context, id string) error {
			cancelled = append(cancelled, id)
			return nil
		},
		requestEnv: requestEnv{
			writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
				audits = append(audits, action)
			},
		},
	})
	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)
	return lifecycleHandlerHarness{mux: mux, state: state, audits: &audits, cancelled: &cancelled}
}

// The handler's ownership/status read can be stale: a worker may complete the
// migration before the pause is written. The pause must not overwrite it.
func TestMigrationPauseDoesNotOverwriteCompletionThatRacesTheRead(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-race": "RUNNING"})
	h.state.afterMigrationRead = func(id string) { h.state.setStatus(id, "COMPLETED") }

	rec := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-race/pause", "user-1", "")

	assertAPIResponseError(t, rec, http.StatusConflict, ErrMigrationInvalidState, "pause racing completion")
	if got := h.state.getStatus("m-race"); got != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", got)
	}
	if len(*h.audits) != 0 {
		t.Fatalf("audits = %v, want none for a transition that did not apply", *h.audits)
	}
}

// statusInPQArray reports whether status is an element of a lib/pq text[]
// parameter such as "{RUNNING,INDEXING}".
func statusInPQArray(status string, param any) bool {
	text, _ := param.(string)
	text = strings.Trim(text, "{}")
	for _, candidate := range strings.Split(text, ",") {
		if strings.Trim(candidate, `"`) == status {
			return true
		}
	}
	return false
}

func TestMigrationCancelLeavesFinishedMigrationsUnchanged(t *testing.T) {
	for _, status := range []string{"COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED"} {
		t.Run(status, func(t *testing.T) {
			h := newLifecycleHandlerHarness(t, map[string]string{"m-done": status})

			rec := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-done/cancel", "user-1", "")

			assertAPIResponseError(t, rec, http.StatusConflict, ErrMigrationInvalidState, "cancel "+status)
			if got := h.state.getStatus("m-done"); got != status {
				t.Fatalf("status = %q, want unchanged %q", got, status)
			}
			if len(*h.audits) != 0 || len(*h.cancelled) != 0 {
				t.Fatalf("audits = %v, cancel events = %v, want none", *h.audits, *h.cancelled)
			}
		})
	}
}

func TestMigrationCancelStopsActiveMigration(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-run": "RUNNING"})

	rec := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-run/cancel", "user-1", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d %s", rec.Code, rec.Body.String())
	}
	if got := h.state.getStatus("m-run"); got != "CANCELLED" {
		t.Fatalf("status = %q, want CANCELLED", got)
	}
	if len(*h.audits) != 1 || (*h.audits)[0] != db.AuditMigrationCancelled || len(*h.cancelled) != 1 {
		t.Fatalf("audits = %v, cancel events = %v", *h.audits, *h.cancelled)
	}
}

// Deleting a finished migration has nothing to cancel, but must still delete it
// without recording a cancellation.
func TestMigrationDeleteOfFinishedMigrationDoesNotCancelIt(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-done": "COMPLETED"})

	rec := performMigrationModuleRequest(h.mux, http.MethodDelete, "/api/migration/m-done", "user-1", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if h.state.hasMigration("m-done") {
		t.Fatal("migration still exists after delete")
	}
	for _, action := range *h.audits {
		if action == db.AuditMigrationCancelled {
			t.Fatalf("audits = %v, want no cancellation for an already finished migration", *h.audits)
		}
	}
}

// A user may resume a connection-loss pause early (unlike a sync job, whose
// connection-loss pause belongs to the recovery worker). The SQL guard and the
// advertised allowed_actions must agree on it.
func TestMigrationResumeFromConnectionLossMatchesAllowedActions(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-loss": "PAUSED_CONNECTION_LOSS", "m-run": "RUNNING"})

	if got := decodeAllowedActions(t, performMigrationModuleRequest(h.mux, http.MethodGet, "/api/migration/m-loss", "user-1", "")); !slicesEqual(got, []string{"resume", "cancel"}) {
		t.Fatalf("detail allowed_actions = %v, want [resume cancel]", got)
	}
	assertAPIResponseError(t, performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-run/resume", "user-1", ""), http.StatusConflict, ErrMigrationInvalidState, "resume RUNNING")

	rec := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-loss/resume", "user-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume = %d %s", rec.Code, rec.Body.String())
	}
	if got := h.state.getStatus("m-loss"); got != "INDEXING" {
		t.Fatalf("status = %q, want INDEXING (discovery is incomplete in the fixture)", got)
	}
}

func TestMigrationListAndDetailExposeAllowedActions(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-run": "RUNNING", "m-done": "COMPLETED"})

	rec := performMigrationModuleRequest(h.mux, http.MethodGet, "/api/migration", "user-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var list []struct {
		ID             string   `json:"id"`
		AllowedActions []string `json:"allowed_actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"m-run": {"pause", "cancel"}, "m-done": {}}
	if len(list) != len(want) {
		t.Fatalf("list = %+v", list)
	}
	for _, item := range list {
		if item.AllowedActions == nil || !slicesEqual(item.AllowedActions, want[item.ID]) {
			t.Errorf("list %s allowed_actions = %v, want %v", item.ID, item.AllowedActions, want[item.ID])
		}
	}
	if got := decodeAllowedActions(t, performMigrationModuleRequest(h.mux, http.MethodGet, "/api/migration/m-run", "user-1", "")); !slicesEqual(got, []string{"pause", "cancel"}) {
		t.Fatalf("detail allowed_actions = %v", got)
	}
}

func decodeAllowedActions(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status request = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		AllowedActions []string `json:"allowed_actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AllowedActions == nil {
		t.Fatalf("allowed_actions missing from %s", rec.Body.String())
	}
	return body.AllowedActions
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Retry-failed and reindex are guarded by the UPDATE itself: a worker may move
// the migration out of the retryable states after the handler loaded the row.
func TestMigrationRetryAndReindexRejectStatusThatChangesAfterTheRead(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"retry-failed", "/api/migration/m-race/retry-failed"},
		{"reindex", "/api/migration/m-race/reindex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newLifecycleHandlerHarness(t, map[string]string{"m-race": "FAILED"})
			h.state.setFailedTasks("m-race", 2, 10)
			h.state.afterMigrationRead = func(id string) { h.state.setStatus(id, "RUNNING") }

			rec := performMigrationModuleRequest(h.mux, http.MethodPost, tc.path, "user-1", "")

			assertAPIResponseError(t, rec, http.StatusConflict, ErrMigrationInvalidState, tc.name+" racing a status change")
			if got := h.state.getStatus("m-race"); got != "RUNNING" {
				t.Fatalf("status = %q, want RUNNING left untouched", got)
			}
		})
	}
}

// Retry-failed is only offered for finished migrations; reindex only for the
// failed ones. Anything else is a conflict, decided in SQL.
func TestMigrationRetryAndReindexStatusGuards(t *testing.T) {
	for _, tc := range []struct {
		path, status string
		wantOK       bool
	}{
		{"retry-failed", "COMPLETED", true},
		{"retry-failed", "COMPLETED_WITH_ERRORS", true},
		{"retry-failed", "FAILED", true},
		{"retry-failed", "RUNNING", false},
		{"retry-failed", "PAUSED", false},
		{"reindex", "FAILED", true},
		{"reindex", "COMPLETED_WITH_ERRORS", true},
		{"reindex", "COMPLETED", false},
		{"reindex", "RUNNING", false},
	} {
		t.Run(tc.path+"/"+tc.status, func(t *testing.T) {
			h := newLifecycleHandlerHarness(t, map[string]string{"m-1": tc.status})
			h.state.setFailedTasks("m-1", 1, 1)

			rec := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-1/"+tc.path, "user-1", "")

			if tc.wantOK {
				if rec.Code >= 400 {
					t.Fatalf("%s from %s = %d %s", tc.path, tc.status, rec.Code, rec.Body.String())
				}
				return
			}
			assertAPIResponseError(t, rec, http.StatusConflict, ErrMigrationInvalidState, tc.path+" from "+tc.status)
			if got := h.state.getStatus("m-1"); got != tc.status {
				t.Fatalf("status = %q, want unchanged %q", got, tc.status)
			}
		})
	}
}

// Reauth refreshes the tokens even when the migration is not finished; it then
// simply has no tasks to requeue.
func TestMigrationReauthOfUnfinishedMigrationRequeuesNothing(t *testing.T) {
	h := newLifecycleHandlerHarness(t, map[string]string{"m-run": "RUNNING"})
	h.state.setFailedTasks("m-run", 2, 10)
	rec := h.state.migrations["m-run"]
	rec.sourceProvider = "google"

	out := performMigrationModuleRequest(h.mux, http.MethodPost, "/api/migration/m-run/reauth", "user-1",
		`{"role":"source","access_token":"a","refresh_token":"r"}`)

	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"retried":0`) {
		t.Fatalf("reauth = %d %s", out.Code, out.Body.String())
	}
	if got := h.state.getStatus("m-run"); got != "RUNNING" {
		t.Fatalf("status = %q, want RUNNING", got)
	}
}
