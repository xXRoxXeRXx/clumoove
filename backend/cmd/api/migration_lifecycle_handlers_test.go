package main

import (
	"context"
	"net/http"
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
