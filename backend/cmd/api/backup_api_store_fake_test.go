package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/db"
)

// fakeBackupAPIStore overrides only the db.BackupAPIStore methods the backup
// handlers use; any other call panics through the nil embedded interface.
type fakeBackupAPIStore struct {
	db.BackupAPIStore

	owned    bool
	ownedErr error

	jobs    []db.BackupJob
	listErr error
	job     *db.BackupJob
	jobErr  error

	claim    db.BackupPassClaim
	claimErr error

	deletionStarted bool
	deletionErr     error

	pauseOK        bool
	pauseErr       error
	resumeOK       bool
	resumeErr      error
	resumeNextRun  time.Time
	resumeNextErr  error
	resumeComputed bool

	verifyID     string
	verifyErr    error
	verify       *db.BackupMaintenance
	verifyGetErr error
	cancelled    bool
	cancelErr    error

	calls []string
}

func (f *fakeBackupAPIStore) VerifyBackupJobOwnershipContext(_ context.Context, jobID, userID string) (bool, error) {
	f.calls = append(f.calls, "owns:"+jobID+":"+userID)
	return f.owned, f.ownedErr
}

func (f *fakeBackupAPIStore) ListBackupJobsForOwnerContext(_ context.Context, userID string) ([]db.BackupJob, error) {
	f.calls = append(f.calls, "list:"+userID)
	return f.jobs, f.listErr
}

func (f *fakeBackupAPIStore) GetBackupJobForOwnerContext(_ context.Context, jobID, userID string) (*db.BackupJob, error) {
	f.calls = append(f.calls, "get:"+jobID+":"+userID)
	return f.job, f.jobErr
}

func (f *fakeBackupAPIStore) ClaimBackupJobPassContext(_ context.Context, jobID, trigger string, _ *string) (db.BackupPassClaim, error) {
	f.calls = append(f.calls, "claim:"+jobID+":"+trigger)
	return f.claim, f.claimErr
}

func (f *fakeBackupAPIStore) RequestBackupRepositoryDeletionContext(_ context.Context, jobID, userID string) (bool, error) {
	f.calls = append(f.calls, "delete:"+jobID+":"+userID)
	return f.deletionStarted, f.deletionErr
}

func (f *fakeBackupAPIStore) PauseBackupJobContext(_ context.Context, jobID, userID string) (bool, error) {
	f.calls = append(f.calls, "pause:"+jobID+":"+userID)
	return f.pauseOK, f.pauseErr
}

func (f *fakeBackupAPIStore) ResumeBackupJobContext(_ context.Context, jobID, userID string, nextRun func(cronExpression, timezone string) (time.Time, error)) (bool, error) {
	f.calls = append(f.calls, "resume:"+jobID+":"+userID)
	if f.resumeOK && f.resumeErr == nil {
		f.resumeNextRun, f.resumeNextErr = nextRun("0 * * * *", "UTC")
		f.resumeComputed = true
	}
	return f.resumeOK, f.resumeErr
}

func (f *fakeBackupAPIStore) CreateBackupVerifyContext(_ context.Context, jobID, userID, mode string, _ *int64) (string, error) {
	f.calls = append(f.calls, "verify:"+jobID+":"+userID+":"+mode)
	return f.verifyID, f.verifyErr
}

func (f *fakeBackupAPIStore) GetBackupVerifyForOwnerContext(_ context.Context, verifyID, userID string) (*db.BackupMaintenance, error) {
	f.calls = append(f.calls, "getverify:"+verifyID+":"+userID)
	return f.verify, f.verifyGetErr
}

func (f *fakeBackupAPIStore) CancelBackupVerifyForOwnerContext(_ context.Context, verifyID, userID string) (bool, error) {
	f.calls = append(f.calls, "cancelverify:"+verifyID+":"+userID)
	return f.cancelled, f.cancelErr
}

type capturedAudit struct {
	action db.AuditAction
	target string
	actor  string
}

func newFakeBackupAPI(store db.BackupAPIStore) (*backupAPI, *[]capturedAudit) {
	var audits []capturedAudit
	api := newBackupAPI(backupDependencies{
		backups:     store,
		rateLimiter: allowAllFileRateLimiter{},
		requestEnv: requestEnv{
			requireUserID: func(http.ResponseWriter, *http.Request) (string, bool) { return "user-1", true },
			logf:          func(*http.Request, string, ...any) {},
			writeAudit: func(_ *http.Request, action db.AuditAction, target, actor string, _ map[string]interface{}) {
				audits = append(audits, capturedAudit{action: action, target: target, actor: actor})
			},
		},
	})
	return api, &audits
}

func serveBackup(t *testing.T, api *backupAPI, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.registerRoutes(mux, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBackupReadHandlersUseTheBackupAPIStore(t *testing.T) {
	t.Run("an empty list is an empty JSON array", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{})
		rec := serveBackup(t, api, http.MethodGet, "/api/backup", "")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("status = %d body = %s, want 200 []", rec.Code, rec.Body.String())
		}
	})

	t.Run("a list failure is a typed 500", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{listErr: errors.New("boom")})
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("a job outside the caller's account is a 404", func(t *testing.T) {
		store := &fakeBackupAPIStore{jobErr: sql.ErrNoRows}
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1", ""), http.StatusNotFound, ErrBackupNotFound)
		if len(store.calls) != 1 || store.calls[0] != "get:b1:user-1" {
			t.Fatalf("store calls = %v, want one owner-scoped get", store.calls)
		}
	})

	t.Run("an unowned backup id is a 404 before any work", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: false}
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/verify", ""), http.StatusNotFound, ErrBackupNotFound)
		if len(store.calls) != 1 || store.calls[0] != "owns:b1:user-1" {
			t.Fatalf("store calls = %v, want only the ownership check", store.calls)
		}
	})

	t.Run("an ownership lookup failure is a typed 500", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{ownedErr: errors.New("boom")})
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/verify", ""), http.StatusInternalServerError, ErrInternalError)
	})
}

func TestBackupMutationHandlersUseTheBackupAPIStore(t *testing.T) {
	t.Run("run starts a manual pass and audits it", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, claim: db.BackupPassClaim{Outcome: db.BackupClaimed}}
		api, audits := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodPost, "/api/backup/b1/run", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0] != (capturedAudit{db.AuditBackupStarted, "b1", "user-1"}) {
			t.Fatalf("audits = %+v", *audits)
		}
	})

	t.Run("run that is not claimed is a 409 and is not audited", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, claim: db.BackupPassClaim{Outcome: db.BackupClaimOutcome("busy")}}
		api, audits := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/run", ""), http.StatusConflict, ErrBackupInvalidState)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("delete that cannot start is a 409", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, deletionStarted: false}
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodDelete, "/api/backup/b1", ""), http.StatusConflict, ErrBackupInvalidState)
	})

	t.Run("delete that starts is accepted and audited", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, deletionStarted: true}
		api, audits := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodDelete, "/api/backup/b1", "")
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0].action != db.AuditBackupDeleted {
			t.Fatalf("audits = %+v", *audits)
		}
	})
}

func TestBackupPauseAndResumeUseTheBackupAPIStore(t *testing.T) {
	t.Run("pause in a state that cannot pause is a 409", func(t *testing.T) {
		api, audits := newFakeBackupAPI(&fakeBackupAPIStore{owned: true, pauseOK: false})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/pause", ""), http.StatusConflict, ErrBackupInvalidState)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("pause succeeds and is audited", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, pauseOK: true}
		api, audits := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodPost, "/api/backup/b1/pause", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0].action != db.AuditBackupPaused {
			t.Fatalf("audits = %+v", *audits)
		}
	})

	t.Run("a pause storage failure is a typed 500", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{owned: true, pauseErr: errors.New("boom")})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/pause", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("resume computes the next run from the job's own schedule", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, resumeOK: true}
		api, audits := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodPost, "/api/backup/b1/resume", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if !store.resumeComputed || store.resumeNextErr != nil || !store.resumeNextRun.After(time.Now()) {
			t.Fatalf("next-run callback = (computed %v, %v, %v), want a future time", store.resumeComputed, store.resumeNextRun, store.resumeNextErr)
		}
		if len(*audits) != 1 || (*audits)[0].action != db.AuditBackupResumed {
			t.Fatalf("audits = %+v", *audits)
		}
	})

	t.Run("resume in a state that cannot resume is a 409", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{owned: true, resumeOK: false})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/resume", ""), http.StatusConflict, ErrBackupInvalidState)
	})
}

func TestBackupVerifyHandlersUseTheBackupAPIStore(t *testing.T) {
	t.Run("creating a check for a vanished backup is a 404", func(t *testing.T) {
		api, _ := newFakeBackupAPI(&fakeBackupAPIStore{owned: true, verifyErr: sql.ErrNoRows})
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/verify", `{"mode":"METADATA"}`), http.StatusNotFound, ErrBackupNotFound)
	})

	t.Run("a check belonging to another backup is a 404", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, verify: &db.BackupMaintenance{ID: "v1", BackupJobID: "other"}}
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/verify/v1", ""), http.StatusNotFound, ErrBackupNotFound)
	})

	t.Run("cancelling a check that is no longer cancellable is a 404", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, verify: &db.BackupMaintenance{ID: "v1", BackupJobID: "b1"}, cancelled: false}
		api, audits := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodPost, "/api/backup/b1/verify/v1/cancel", ""), http.StatusNotFound, ErrBackupNotFound)
		if len(*audits) != 0 {
			t.Fatalf("audits = %+v, want none", *audits)
		}
	})

	t.Run("cancelling a running check succeeds and is audited", func(t *testing.T) {
		store := &fakeBackupAPIStore{owned: true, verify: &db.BackupMaintenance{ID: "v1", BackupJobID: "b1"}, cancelled: true}
		api, audits := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodPost, "/api/backup/b1/verify/v1/cancel", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(*audits) != 1 || (*audits)[0].action != db.AuditRepositoryCheckCancelled {
			t.Fatalf("audits = %+v", *audits)
		}
	})
}

// fakeBackupCatalog extends the store fake with the run and snapshot reads.
type fakeBackupCatalog struct {
	*fakeBackupAPIStore
	runs         []db.BackupRun
	runsErr      error
	snapshots    []db.BackupSnapshot
	snapshotsErr error
	items        []db.BackupSnapshotItem
	itemsErr     error
	gotDirectory string
}

func (f *fakeBackupCatalog) ListBackupRunsForOwnerContext(_ context.Context, jobID, userID string) ([]db.BackupRun, error) {
	f.calls = append(f.calls, "runs:"+jobID+":"+userID)
	return f.runs, f.runsErr
}

func (f *fakeBackupCatalog) ListVisibleBackupSnapshotsContext(_ context.Context, jobID string) ([]db.BackupSnapshot, error) {
	f.calls = append(f.calls, "snapshots:"+jobID)
	return f.snapshots, f.snapshotsErr
}

func (f *fakeBackupCatalog) ListBackupSnapshotChildrenContext(_ context.Context, jobID, snapshotID, directory string) ([]db.BackupSnapshotItem, error) {
	f.calls = append(f.calls, "items:"+jobID+":"+snapshotID)
	f.gotDirectory = directory
	return f.items, f.itemsErr
}

func TestBackupRunAndSnapshotReadsUseTheBackupAPIStore(t *testing.T) {
	newCatalog := func() *fakeBackupCatalog {
		return &fakeBackupCatalog{fakeBackupAPIStore: &fakeBackupAPIStore{owned: true}}
	}

	t.Run("runs are listed for the owning user", func(t *testing.T) {
		store := newCatalog()
		store.runs = []db.BackupRun{{ID: "run-1"}}
		api, _ := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodGet, "/api/backup/b1/runs", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run-1") {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if got := store.calls[len(store.calls)-1]; got != "runs:b1:user-1" {
			t.Fatalf("last store call = %s, want an owner-scoped run listing", got)
		}
	})

	t.Run("a run listing failure is a typed 500", func(t *testing.T) {
		store := newCatalog()
		store.runsErr = errors.New("boom")
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/runs", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("no snapshots is an empty JSON array", func(t *testing.T) {
		api, _ := newFakeBackupAPI(newCatalog())
		rec := serveBackup(t, api, http.MethodGet, "/api/backup/b1/snapshots", "")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("status = %d body = %s, want 200 []", rec.Code, rec.Body.String())
		}
	})

	t.Run("snapshot items strip the leading slash from the requested directory", func(t *testing.T) {
		store := newCatalog()
		api, _ := newFakeBackupAPI(store)
		rec := serveBackup(t, api, http.MethodGet, "/api/backup/b1/snapshots/s1/items?path=/docs", "")
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("status = %d body = %s, want 200 []", rec.Code, rec.Body.String())
		}
		if store.gotDirectory != "docs" {
			t.Fatalf("directory = %q, want docs", store.gotDirectory)
		}
	})

	t.Run("snapshot items for an unowned backup never reach the catalog", func(t *testing.T) {
		store := newCatalog()
		store.owned = false
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/snapshots/s1/items", ""), http.StatusNotFound, ErrBackupNotFound)
		for _, call := range store.calls {
			if strings.HasPrefix(call, "items:") {
				t.Fatalf("catalog was read for an unowned backup: %v", store.calls)
			}
		}
	})

	t.Run("an invalid directory is a validation error and any other failure a 500", func(t *testing.T) {
		store := newCatalog()
		store.itemsErr = errors.New("boom")
		api, _ := newFakeBackupAPI(store)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/snapshots/s1/items?path=a/../../b", ""), http.StatusBadRequest, ErrFolderPathInvalid)
		assertAPIError(t, serveBackup(t, api, http.MethodGet, "/api/backup/b1/snapshots/s1/items?path=docs", ""), http.StatusInternalServerError, ErrInternalError)
	})
}
