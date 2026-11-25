package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"

	"backend/internal/db"
)

// fakeRestoreAPIStore overrides only the db.RestoreAPIStore methods the restore
// handlers use; any other call panics through the nil embedded interface.
type fakeRestoreAPIStore struct {
	db.RestoreAPIStore

	preview    *db.RestorePreview
	previewErr error

	cancelPreviewOK  bool
	cancelPreviewErr error

	consumeJob *db.RestoreJob
	consumeRun *db.RestoreRun
	consumeErr error

	runs    []db.RestoreRun
	runsErr error
	run     *db.RestoreRun
	runErr  error

	items    []db.RestoreItem
	itemsErr error

	cancelRunOK  bool
	cancelRunErr error

	deleteJobOK  bool
	deleteJobErr error

	calls []string
}

func (f *fakeRestoreAPIStore) GetRestorePreviewForOwnerContext(_ context.Context, previewID, userID string) (*db.RestorePreview, error) {
	f.calls = append(f.calls, "preview:"+previewID+":"+userID)
	return f.preview, f.previewErr
}

func (f *fakeRestoreAPIStore) CancelRestorePreviewForOwnerContext(_ context.Context, previewID, userID string) (bool, error) {
	f.calls = append(f.calls, "cancelpreview:"+previewID+":"+userID)
	return f.cancelPreviewOK, f.cancelPreviewErr
}

func (f *fakeRestoreAPIStore) ConsumeRestorePreviewContext(_ context.Context, previewID, userID string) (*db.RestoreJob, *db.RestoreRun, error) {
	f.calls = append(f.calls, "consume:"+previewID+":"+userID)
	return f.consumeJob, f.consumeRun, f.consumeErr
}

func (f *fakeRestoreAPIStore) ListRestoreRunsForOwnerContext(_ context.Context, userID string) ([]db.RestoreRun, error) {
	f.calls = append(f.calls, "runs:"+userID)
	return f.runs, f.runsErr
}

func (f *fakeRestoreAPIStore) GetRestoreRunForOwnerContext(_ context.Context, runID, userID string) (*db.RestoreRun, error) {
	f.calls = append(f.calls, "run:"+runID+":"+userID)
	return f.run, f.runErr
}

func (f *fakeRestoreAPIStore) ListRestoreItemsForOwnerContext(_ context.Context, runID, userID string) ([]db.RestoreItem, error) {
	f.calls = append(f.calls, "items:"+runID+":"+userID)
	return f.items, f.itemsErr
}

func (f *fakeRestoreAPIStore) ListRestoreItemsForRunOwnerContext(_ context.Context, runID, userID string) ([]db.RestoreItem, error) {
	f.calls = append(f.calls, "reportitems:"+runID+":"+userID)
	return f.items, f.itemsErr
}

func (f *fakeRestoreAPIStore) CancelRestoreRunForOwnerContext(_ context.Context, runID, userID string) (bool, error) {
	f.calls = append(f.calls, "cancelrun:"+runID+":"+userID)
	return f.cancelRunOK, f.cancelRunErr
}

func (f *fakeRestoreAPIStore) DeleteRestoreJobForOwnerContext(_ context.Context, jobID, userID string) (bool, error) {
	f.calls = append(f.calls, "deletejob:"+jobID+":"+userID)
	return f.deleteJobOK, f.deleteJobErr
}

func newFakeRestoreAPI(store db.RestoreAPIStore) (*backupAPI, *[]capturedAudit) {
	api, audits := newFakeBackupAPI(&fakeBackupAPIStore{})
	api.restores = store
	return api, audits
}

func TestRestorePreviewHandlersUseTheRestoreAPIStore(t *testing.T) {
	t.Run("a preview outside the caller's account is a 404", func(t *testing.T) {
		store := &fakeRestoreAPIStore{previewErr: sql.ErrNoRows}
		api, _ := newFakeRestoreAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/previews/p1", ""), http.StatusNotFound, ErrRestoreNotFound)
		if len(store.calls) != 1 || store.calls[0] != "preview:p1:user-1" {
			t.Fatalf("store calls = %v, want one owner-scoped preview read", store.calls)
		}
	})

	t.Run("a preview read failure is a typed 500", func(t *testing.T) {
		api, _ := newFakeRestoreAPI(&fakeRestoreAPIStore{previewErr: errors.New("boom")})
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/previews/p1", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("cancelling a preview that cannot be cancelled is a 404 and is not audited", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{cancelPreviewOK: false})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/restore/previews/p1/cancel", ""), http.StatusNotFound, ErrRestoreNotFound)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("cancelling a preview succeeds and is audited", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{cancelPreviewOK: true})
		rec := serveBackup(t, api, http.MethodPost, "/api/restore/previews/p1/cancel", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0] != (capturedAudit{db.AuditRestorePreviewCancelled, "p1", "user-1"}) {
			t.Fatalf("audits = %+v", *audits)
		}
	})
}

func TestRestoreConsumeMapsStoreOutcomesToTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   APIErrorCode
	}{
		{"missing preview", db.ErrRestorePreviewNotFound, http.StatusNotFound, ErrRestoreNotFound},
		{"no rows", sql.ErrNoRows, http.StatusNotFound, ErrRestoreNotFound},
		{"expired preview", db.ErrRestorePreviewExpired, http.StatusConflict, ErrRestorePreviewExpired},
		{"unavailable snapshot", db.ErrRestoreSnapshotUnavailable, http.StatusConflict, ErrRestorePreviewStale},
		{"stale preview", db.ErrRestorePreviewStale, http.StatusConflict, ErrRestorePreviewStale},
		{"retry mismatch", db.ErrRestoreRetryMismatch, http.StatusConflict, ErrRestorePreviewStale},
		{"anything else", errors.New("boom"), http.StatusConflict, ErrRestorePreviewInvalidState},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{consumeErr: tc.err})
			assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/restore/previews/p1/consume", ""), tc.status, tc.code)
			if len(*audits) != 0 {
				t.Fatalf("audits = %+v, want none for a failed consume", *audits)
			}
		})
	}

	t.Run("success starts the run and audits the job and the run", func(t *testing.T) {
		store := &fakeRestoreAPIStore{consumeJob: &db.RestoreJob{ID: "job-1"}, consumeRun: &db.RestoreRun{ID: "run-1"}}
		api, audits := newFakeRestoreAPI(store)
		rec := serveBackup(t, api, http.MethodPost, "/api/restore/previews/p1/consume", "")
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "run-1") || !strings.Contains(rec.Body.String(), "job-1") {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		want := []capturedAudit{{db.AuditRestoreCreated, "job-1", "user-1"}, {db.AuditRestoreStarted, "run-1", "user-1"}}
		if len(*audits) != 2 || (*audits)[0] != want[0] || (*audits)[1] != want[1] {
			t.Fatalf("audits = %+v, want %+v", *audits, want)
		}
	})
}

func TestRestoreRunHandlersUseTheRestoreAPIStore(t *testing.T) {
	t.Run("runs are listed for the owning user", func(t *testing.T) {
		store := &fakeRestoreAPIStore{runs: []db.RestoreRun{{ID: "run-1"}}}
		api, _ := newFakeRestoreAPI(store)
		rec := serveBackup(t, api, http.MethodGet, "/api/restore/runs", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-1") {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if len(store.calls) != 1 || store.calls[0] != "runs:user-1" {
			t.Fatalf("store calls = %v", store.calls)
		}
	})

	t.Run("a run list failure is a typed 500", func(t *testing.T) {
		api, _ := newFakeRestoreAPI(&fakeRestoreAPIStore{runsErr: errors.New("boom")})
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/runs", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("a run outside the caller's account is a 404", func(t *testing.T) {
		api, _ := newFakeRestoreAPI(&fakeRestoreAPIStore{runErr: sql.ErrNoRows})
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/runs/r1", ""), http.StatusNotFound, ErrRestoreNotFound)
	})

	t.Run("items are only listed after the run's ownership is confirmed", func(t *testing.T) {
		store := &fakeRestoreAPIStore{runErr: sql.ErrNoRows}
		api, _ := newFakeRestoreAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/runs/r1/items", ""), http.StatusNotFound, ErrRestoreNotFound)
		for _, call := range store.calls {
			if strings.HasPrefix(call, "items:") {
				t.Fatalf("items were read for an unowned run: %v", store.calls)
			}
		}
	})

	t.Run("items are listed for an owned run", func(t *testing.T) {
		store := &fakeRestoreAPIStore{run: &db.RestoreRun{ID: "r1"}, items: []db.RestoreItem{{ID: "i1", SnapshotRelativePath: "a.txt"}}}
		api, _ := newFakeRestoreAPI(store)
		rec := serveBackup(t, api, http.MethodGet, "/api/restore/runs/r1/items", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "a.txt") {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("cancelling a run that cannot be cancelled is a 409 and is not audited", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{cancelRunOK: false})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/restore/runs/r1/cancel", ""), http.StatusConflict, ErrRestorePreviewInvalidState)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("cancelling a run is accepted and audited", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{cancelRunOK: true})
		rec := serveBackup(t, api, http.MethodPost, "/api/restore/runs/r1/cancel", "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0] != (capturedAudit{db.AuditRestoreCancelled, "r1", "user-1"}) {
			t.Fatalf("audits = %+v", *audits)
		}
	})
}

func TestRestoreJobDeleteAndReportUseTheRestoreAPIStore(t *testing.T) {
	t.Run("deleting an unknown job is a 404", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{deleteJobOK: false})
		assertAPIError(t, serveBackup(t, api, http.MethodDelete, "/api/restore/jobs/j1", ""), http.StatusNotFound, ErrRestoreNotFound)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("deleting a job is 204 and audited", func(t *testing.T) {
		api, audits := newFakeRestoreAPI(&fakeRestoreAPIStore{deleteJobOK: true})
		rec := serveBackup(t, api, http.MethodDelete, "/api/restore/jobs/j1", "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0] != (capturedAudit{db.AuditRestoreDeleted, "j1", "user-1"}) {
			t.Fatalf("audits = %+v", *audits)
		}
	})

	t.Run("a report for a run that is still active is a 409", func(t *testing.T) {
		store := &fakeRestoreAPIStore{run: &db.RestoreRun{ID: "r1", Status: "RUNNING"}}
		api, _ := newFakeRestoreAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/restore/runs/r1/report", ""), http.StatusConflict, ErrRestorePreviewInvalidState)
		for _, call := range store.calls {
			if strings.HasPrefix(call, "reportitems:") {
				t.Fatalf("report items were read for an active run: %v", store.calls)
			}
		}
	})

	t.Run("a report for a finished run is a CSV with one row per item", func(t *testing.T) {
		store := &fakeRestoreAPIStore{
			run:   &db.RestoreRun{ID: "r1", Status: "COMPLETED"},
			items: []db.RestoreItem{{ID: "i1", SnapshotRelativePath: "docs/a.txt", Status: "COMPLETED", SizeBytes: 12}},
		}
		api, _ := newFakeRestoreAPI(store)
		rec := serveBackup(t, api, http.MethodGet, "/api/restore/runs/r1/report", "")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
			t.Fatalf("status = %d type = %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "source_path,") || !strings.Contains(lines[1], "docs/a.txt") {
			t.Fatalf("report = %q", rec.Body.String())
		}
	})
}
