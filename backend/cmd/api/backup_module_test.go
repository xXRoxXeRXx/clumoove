package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
)

func TestBackupModuleRegistersAllRoutesWithExpectedAccessControls(t *testing.T) {
	api := newBackupAPI(backupDependencies{
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   allowAllFileRateLimiter{},
	})

	mux := http.NewServeMux()
	authCalls := 0
	api.registerRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			if r.Header.Get("X-Test-User") == "" {
				writeError(w, http.StatusUnauthorized, ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	protectedRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/backup"},
		{http.MethodGet, "/api/backup/stream"},
		{http.MethodPost, "/api/backup"},
		{http.MethodGet, "/api/backup/b1"},
		{http.MethodPut, "/api/backup/b1"},
		{http.MethodGet, "/api/backup/b1/runs"},
		{http.MethodPost, "/api/backup/b1/run"},
		{http.MethodPost, "/api/backup/b1/verify"},
		{http.MethodGet, "/api/backup/b1/verify"},
		{http.MethodGet, "/api/backup/b1/verify/v1/stream"},
		{http.MethodGet, "/api/backup/b1/verify/v1"},
		{http.MethodPost, "/api/backup/b1/verify/v1/cancel"},
		{http.MethodPost, "/api/backup/b1/pause"},
		{http.MethodPost, "/api/backup/b1/resume"},
		{http.MethodDelete, "/api/backup/b1"},
		{http.MethodGet, "/api/backup/b1/snapshots"},
		{http.MethodGet, "/api/backup/b1/snapshots/s1/items"},
		{http.MethodPost, "/api/backup/b1/snapshots/s1/restore/previews"},
		{http.MethodGet, "/api/restore/previews/p1"},
		{http.MethodPost, "/api/restore/previews/p1/cancel"},
		{http.MethodPost, "/api/restore/previews/p1/consume"},
		{http.MethodGet, "/api/restore"},
		{http.MethodGet, "/api/restore/runs"},
		{http.MethodGet, "/api/restore/runs/r1/stream"},
		{http.MethodGet, "/api/restore/runs/r1"},
		{http.MethodGet, "/api/restore/runs/r1/items"},
		{http.MethodPost, "/api/restore/runs/r1/cancel"},
		{http.MethodGet, "/api/restore/runs/r1/report"},
		{http.MethodDelete, "/api/restore/jobs/j1"},
	}

	for _, route := range protectedRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			authCalls = 0
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))

			assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, route.method+" "+route.path)
			if authCalls != 1 {
				t.Fatalf("auth middleware calls = %d, want 1", authCalls)
			}
		})
	}
}

func performBackupModuleRequest(mux http.Handler, method, target, userID, body string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	if userID != "" {
		req.Header.Set("X-Test-User", userID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

type syncStreamRecorder struct {
	mu      sync.Mutex
	headers http.Header
	code    int
	body    bytes.Buffer
}

func newSyncStreamRecorder() *syncStreamRecorder {
	return &syncStreamRecorder{headers: make(http.Header)}
}

func (r *syncStreamRecorder) Header() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers
}

func (r *syncStreamRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(p)
}

func (r *syncStreamRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.code == 0 {
		r.code = statusCode
	}
}

func (r *syncStreamRecorder) Flush() {}

func (r *syncStreamRecorder) snapshot() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code, r.body.String()
}

func runBackupSSETest(t *testing.T, mux http.Handler, path, userID, initialSubstr string, triggerChange func(), updatedSubstr string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.Header.Set("X-Test-User", userID)
	rec := newSyncStreamRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(rec, req)
		close(done)
	}()

	waitForSubstr := func(substr string) {
		deadline := time.Now().Add(1 * time.Second)
		for time.Now().Before(deadline) {
			_, body := rec.snapshot()
			if strings.Contains(body, substr) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, body := rec.snapshot()
		t.Fatalf("SSE %s timed out waiting for %q in %q", path, substr, body)
	}

	waitForSubstr(initialSubstr)
	if triggerChange != nil {
		triggerChange()
	}
	if updatedSubstr != "" {
		waitForSubstr(updatedSubstr)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("SSE %s did not exit on cancel", path)
	}
	code, body := rec.snapshot()
	if code != http.StatusOK {
		t.Fatalf("SSE %s status = %d, want 200: %s", path, code, body)
	}
	return body
}

func assertStreamPoolSlotsAvailable(t *testing.T, pool streamSlotPool, userID string) {
	t.Helper()
	for i := 0; i < maxStreamsPerUser; i++ {
		if !pool.Acquire(userID) {
			t.Fatalf("expected full stream pool capacity for %s after stream exit (failed at slot %d)", userID, i)
		}
	}
	for i := 0; i < maxStreamsPerUser; i++ {
		pool.Release(userID)
	}
}

func TestBackupModuleNilRateLimiterFailsClosed(t *testing.T) {
	module := newBackupAPI(backupDependencies{
		requireUserID: func(_ http.ResponseWriter, r *http.Request) (string, bool) {
			return r.Header.Get("X-Test-User"), true
		},
	})
	mux := http.NewServeMux()
	module.registerRoutes(mux, func(next http.Handler) http.Handler { return next })
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", "{}"), http.StatusTooManyRequests, ErrRateLimited, "nil rateLimiter create backup")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/backup/stream", "user-1", ""), http.StatusTooManyRequests, ErrRateLimited, "nil rateLimiter backup stream")
}

func TestBackupModuleLifecycleOwnershipAndStateTransitions(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}

	database, state := newBackupModuleTestDB(t, map[string][]driver.Value{
		"p-src":      profileTestRow("p-src", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
		"p-src-same": profileTestRow("p-src-same", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
		"p-tgt":      profileTestRow("p-tgt", "user-1", "webdav", "https://tgt.example.test", "bob", encSecret),
		"p-other":    profileTestRow("p-other", "user-2", "nextcloud", "https://other.example.test", "mallory", encSecret),
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	var audits []db.AuditAction

	api := newBackupAPI(backupDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   limiter,
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. POST /api/backup validations, rate limit, overlap check, and creation.
	limiter.blocked["migration-sync-mutation"] = true
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{}`), http.StatusTooManyRequests, ErrRateLimited, "create backup rate limit")
	limiter.blocked["migration-sync-mutation"] = false

	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src"}`), http.StatusBadRequest, ErrMissingRequiredFields, "missing target profile")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-other"}`), http.StatusNotFound, ErrProfileNotFound, "cross-user target profile")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-src-same","selected_paths":["/Docs"],"target_dir":"/Docs/Backup","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":7,"threads":2}`), http.StatusBadRequest, ErrBackupSourceTargetOverlap, "same-account overlap")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backups","cron_expression":"invalid","timezone":"UTC","retention_count":7,"threads":2}`), http.StatusBadRequest, ErrBackupCronInvalid, "invalid cron")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"Invalid/Zone","retention_count":7,"threads":2}`), http.StatusBadRequest, ErrBackupTimezoneInvalid, "invalid timezone")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":0,"threads":2}`), http.StatusBadRequest, ErrBackupRetentionInvalid, "invalid retention")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":7,"threads":17}`), http.StatusBadRequest, ErrThreadsOutOfRange, "invalid threads")

	recCreate := performBackupModuleRequest(mux, http.MethodPost, "/api/backup", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backups","cron_expression":"0 0 * * *","timezone":"UTC","retention_count":7,"threads":4}`)
	if recCreate.Code != http.StatusOK {
		t.Fatalf("create backup = %d %s", recCreate.Code, recCreate.Body.String())
	}
	var createResp map[string]string
	if err := json.Unmarshal(recCreate.Body.Bytes(), &createResp); err != nil || createResp["id"] == "" {
		t.Fatalf("create response = %s err=%v", recCreate.Body.String(), err)
	}
	backupID := createResp["id"]

	// Seed a second job owned by user-2 to verify anti-enumeration (404 BACKUP_NOT_FOUND).
	now := time.Now().UTC()
	state.setJob(&backupModuleJobRecord{
		id:             "backup-other",
		userID:         "user-2",
		sourceProvider: "nextcloud",
		targetProvider: "webdav",
		selectedPaths:  db.StringArray{"/Other"},
		targetDir:      "/Backups",
		repositoryID:   "repo-other",
		repositoryRoot: "/Backups/.clumoove-backup/repo-other",
		cronExpression: "0 0 * * *",
		timezone:       "UTC",
		retentionCount: 5,
		threads:        2,
		status:         "IDLE",
		deletionState:  "ACTIVE",
		createdAt:      now,
		updatedAt:      now,
	})

	// 2. List, get, and anti-enumeration checks.
	recList := performBackupModuleRequest(mux, http.MethodGet, "/api/backup", "user-1", "")
	if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), backupID) || strings.Contains(recList.Body.String(), "backup-other") {
		t.Fatalf("list backups = %d %s", recList.Code, recList.Body.String())
	}
	for _, targetID := range []string{"backup-missing", "backup-other"} {
		for _, tc := range []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodGet, "/api/backup/" + targetID, ""},
			{http.MethodPut, "/api/backup/" + targetID, `{"cron_expression":"0 1 * * *","timezone":"UTC","retention_count":10,"threads":4}`},
			{http.MethodGet, "/api/backup/" + targetID + "/runs", ""},
			{http.MethodPost, "/api/backup/" + targetID + "/run", ""},
			{http.MethodPost, "/api/backup/" + targetID + "/pause", ""},
			{http.MethodPost, "/api/backup/" + targetID + "/resume", ""},
			{http.MethodDelete, "/api/backup/" + targetID, ""},
			{http.MethodGet, "/api/backup/" + targetID + "/snapshots", ""},
			{http.MethodGet, "/api/backup/" + targetID + "/snapshots/snap-1/items", ""},
		} {
			assertAPIResponseError(t, performBackupModuleRequest(mux, tc.method, tc.path, "user-1", tc.body), http.StatusNotFound, ErrBackupNotFound, tc.method+" "+tc.path)
		}
	}

	recGet := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID, "user-1", "")
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), backupID) {
		t.Fatalf("get backup = %d %s", recGet.Code, recGet.Body.String())
	}

	// 3. Update backup schedule/retention/threads (+ validation errors).
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPut, "/api/backup/"+backupID, "user-1", `{"cron_expression":"bad","timezone":"UTC","retention_count":7,"threads":4}`), http.StatusBadRequest, ErrBackupCronInvalid, "update invalid cron")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPut, "/api/backup/"+backupID, "user-1", `{"cron_expression":"0 2 * * *","timezone":"UTC","retention_count":0,"threads":4}`), http.StatusBadRequest, ErrBackupRetentionInvalid, "update invalid retention")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPut, "/api/backup/"+backupID, "user-1", `{"cron_expression":"0 2 * * *","timezone":"UTC","retention_count":7,"threads":99}`), http.StatusBadRequest, ErrThreadsOutOfRange, "update invalid threads")

	recUpdate := performBackupModuleRequest(mux, http.MethodPut, "/api/backup/"+backupID, "user-1", `{"cron_expression":"0 2 * * *","timezone":"Europe/Berlin","retention_count":14,"threads":6}`)
	if recUpdate.Code != http.StatusOK {
		t.Fatalf("update backup = %d %s", recUpdate.Code, recUpdate.Body.String())
	}
	job := state.getJob(backupID)
	if job.cronExpression != "0 2 * * *" || job.timezone != "Europe/Berlin" || job.retentionCount != 14 || job.threads != 6 {
		t.Fatalf("updated job = %+v", job)
	}

	// 4. Run claim and overlap protection, plus runs listing.
	recRun := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/run", "user-1", "")
	if recRun.Code != http.StatusOK || !strings.Contains(recRun.Body.String(), `"success":true`) {
		t.Fatalf("run backup = %d %s", recRun.Code, recRun.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/run", "user-1", ""), http.StatusConflict, ErrBackupInvalidState, "overlapping manual run")

	recRuns := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID+"/runs", "user-1", "")
	if recRuns.Code != http.StatusOK || !strings.Contains(recRuns.Body.String(), `"trigger":"manual"`) {
		t.Fatalf("list backup runs = %d %s", recRuns.Code, recRuns.Body.String())
	}

	// 5. Pause and resume state transitions.
	recPause := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/pause", "user-1", "")
	if recPause.Code != http.StatusOK || state.getJob(backupID).status != "PAUSED" {
		t.Fatalf("pause backup = %d %s status=%s", recPause.Code, recPause.Body.String(), state.getJob(backupID).status)
	}
	if state.scheduleActive(backupID) {
		t.Fatal("expected schedule deactivated on pause")
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/pause", "user-1", ""), http.StatusConflict, ErrBackupInvalidState, "pause already paused")

	recResume := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/resume", "user-1", "")
	if recResume.Code != http.StatusOK || state.getJob(backupID).status != "IDLE" {
		t.Fatalf("resume backup = %d %s status=%s", recResume.Code, recResume.Body.String(), state.getJob(backupID).status)
	}
	if !state.scheduleActive(backupID) {
		t.Fatal("expected schedule reactivated on resume")
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/"+backupID+"/resume", "user-1", ""), http.StatusConflict, ErrBackupInvalidState, "resume already idle")

	// 6. Snapshots and snapshot items browsing + cross-backup snapshot isolation.
	state.setSnapshot(&backupModuleSnapshotRecord{
		id:             "snap-1",
		backupJobID:    backupID,
		state:          "READY",
		integrityState: "OK",
		totalFiles:     2,
		totalDirs:      1,
		totalBytes:     128,
		createdAt:      now,
	})
	state.setSnapshotItem(&backupModuleSnapshotItemRecord{
		snapshotID:   "snap-1",
		relativePath: "Docs",
		isDir:        true,
		state:        "AVAILABLE",
	})
	state.setSnapshotItem(&backupModuleSnapshotItemRecord{
		snapshotID:   "snap-1",
		relativePath: "Docs/readme.txt",
		isDir:        false,
		sizeBytes:    128,
		state:        "AVAILABLE",
	})
	state.setSnapshot(&backupModuleSnapshotRecord{
		id:             "snap-other",
		backupJobID:    "backup-other",
		state:          "READY",
		integrityState: "OK",
		createdAt:      now,
	})
	state.setSnapshotItem(&backupModuleSnapshotItemRecord{
		snapshotID:   "snap-other",
		relativePath: "Secret/keys.txt",
		isDir:        false,
		sizeBytes:    64,
		state:        "AVAILABLE",
	})

	recSnaps := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID+"/snapshots", "user-1", "")
	if recSnaps.Code != http.StatusOK || !strings.Contains(recSnaps.Body.String(), "snap-1") || strings.Contains(recSnaps.Body.String(), "snap-other") {
		t.Fatalf("list snapshots = %d %s", recSnaps.Code, recSnaps.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID+"/snapshots/snap-1/items?path=../secret", "user-1", ""), http.StatusBadRequest, ErrFolderPathInvalid, "invalid snapshot path")

	recItems := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID+"/snapshots/snap-1/items?path=/Docs", "user-1", "")
	if recItems.Code != http.StatusOK || !strings.Contains(recItems.Body.String(), "Docs/readme.txt") {
		t.Fatalf("list snapshot items = %d %s", recItems.Code, recItems.Body.String())
	}
	recCrossSnap := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/"+backupID+"/snapshots/snap-other/items", "user-1", "")
	if recCrossSnap.Code != http.StatusOK || strings.TrimSpace(recCrossSnap.Body.String()) != "[]" {
		t.Fatalf("cross-backup snapshot items = %d %s, want []", recCrossSnap.Code, recCrossSnap.Body.String())
	}

	// 7. Delete backup marks DELETING and rejects duplicate deletion.
	recDelete := performBackupModuleRequest(mux, http.MethodDelete, "/api/backup/"+backupID, "user-1", "")
	if recDelete.Code != http.StatusAccepted || state.getJob(backupID).deletionState != "DELETING" {
		t.Fatalf("delete backup = %d %s", recDelete.Code, recDelete.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodDelete, "/api/backup/"+backupID, "user-1", ""), http.StatusConflict, ErrBackupInvalidState, "delete already deleting")

	if len(audits) < 5 {
		t.Fatalf("expected backup lifecycle audits, got %v", audits)
	}
}

func TestBackupModuleVerificationLifecycleAndStream(t *testing.T) {
	database, state := newBackupModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&backupModuleJobRecord{
		id:            "b-1",
		userID:        "user-1",
		status:        "IDLE",
		deletionState: "ACTIVE",
		createdAt:     now,
		updatedAt:     now,
	})
	state.setJob(&backupModuleJobRecord{
		id:            "b-2",
		userID:        "user-1",
		status:        "IDLE",
		deletionState: "ACTIVE",
		createdAt:     now,
		updatedAt:     now,
	})
	state.setJob(&backupModuleJobRecord{
		id:            "b-other",
		userID:        "user-2",
		status:        "IDLE",
		deletionState: "ACTIVE",
		createdAt:     now,
		updatedAt:     now,
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	sharedPool := newUserStreamPool(maxStreamsPerUser)
	var audits []db.AuditAction

	api := newBackupAPI(backupDependencies{
		db:                 database,
		encryptionKey:      "01234567890123456789012345678901",
		rateLimiter:        limiter,
		streamPool:         sharedPool,
		streamPollInterval: 10 * time.Millisecond,
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. Verify request validation & rate limiting.
	limiter.blocked["backup-verify"] = true
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"METADATA"}`), http.StatusTooManyRequests, ErrRateLimited, "verify rate limit")
	limiter.blocked["backup-verify"] = false

	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-other/verify", "user-1", `{"mode":"METADATA"}`), http.StatusNotFound, ErrBackupNotFound, "verify cross-user")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"INVALID"}`), http.StatusBadRequest, ErrBackupVerifyInvalid, "verify invalid mode")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"BUDGETED","byte_budget":1024}`), http.StatusBadRequest, ErrBackupVerifyInvalid, "verify budget too small")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"METADATA","byte_budget":67108864}`), http.StatusBadRequest, ErrBackupVerifyInvalid, "verify metadata with budget")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"FULL","confirm_full":false}`), http.StatusBadRequest, ErrBackupVerifyInvalid, "verify full without confirm")

	recCreate := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"BUDGETED","byte_budget":67108864}`)
	if recCreate.Code != http.StatusAccepted {
		t.Fatalf("create verify = %d %s", recCreate.Code, recCreate.Body.String())
	}
	var createResp map[string]string
	if err := json.Unmarshal(recCreate.Body.Bytes(), &createResp); err != nil || createResp["id"] == "" {
		t.Fatalf("create verify body = %s err=%v", recCreate.Body.String(), err)
	}
	verifyID := createResp["id"]

	// Creating another verify while one is active returns 400 BACKUP_VERIFY_INVALID.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify", "user-1", `{"mode":"METADATA"}`), http.StatusBadRequest, ErrBackupVerifyInvalid, "duplicate active verify")

	// Empty backup ID on list verifies returns 400 BACKUP_ID_MISSING.
	recMissingID := httptest.NewRecorder()
	reqMissingID := httptest.NewRequest(http.MethodGet, "/api/backup//verify", nil)
	reqMissingID.Header.Set("X-Test-User", "user-1")
	headerAuthMiddleware(http.HandlerFunc(api.handleListBackupVerifies)).ServeHTTP(recMissingID, reqMissingID)
	assertAPIResponseError(t, recMissingID, http.StatusBadRequest, ErrBackupIDMissing, "list verifies missing backup id")

	// 2. List, get, and cross-backup isolation.
	recList := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/b-1/verify", "user-1", "")
	if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), verifyID) {
		t.Fatalf("list verifies = %d %s", recList.Code, recList.Body.String())
	}
	recGet := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/b-1/verify/"+verifyID, "user-1", "")
	if recGet.Code != http.StatusOK || !strings.Contains(recGet.Body.String(), verifyID) {
		t.Fatalf("get verify = %d %s", recGet.Code, recGet.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/backup/b-2/verify/"+verifyID, "user-1", ""), http.StatusNotFound, ErrBackupNotFound, "cross-backup verify get")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-2/verify/"+verifyID+"/cancel", "user-1", ""), http.StatusNotFound, ErrBackupNotFound, "cross-backup verify cancel")

	// 3. Verify stream emits initial & updated repository-check events and releases pool slot.
	runBackupSSETest(t, mux, "/api/backup/b-1/verify/"+verifyID+"/stream", "user-1", "event: repository-check\ndata: ", func() {
		v := state.getVerify(verifyID)
		v.state = "RUNNING"
		v.checkedPacks = 3
		state.setVerify(v)
	}, `"checked_packs":3`)
	assertStreamPoolSlotsAvailable(t, sharedPool, "user-1")

	// Stream rate limit and concurrent pool cap.
	limiter.blocked["backup-verify-stream"] = true
	recRL := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/b-1/verify/"+verifyID+"/stream", "user-1", "")
	assertAPIResponseError(t, recRL, http.StatusTooManyRequests, ErrRateLimited, "verify stream rate limit")
	limiter.blocked["backup-verify-stream"] = false

	for i := 0; i < maxStreamsPerUser; i++ {
		if !sharedPool.Acquire("user-1") {
			t.Fatalf("sharedPool.Acquire failed at %d", i)
		}
	}
	recCap := performBackupModuleRequest(mux, http.MethodGet, "/api/backup/b-1/verify/"+verifyID+"/stream", "user-1", "")
	assertAPIResponseError(t, recCap, http.StatusTooManyRequests, ErrRateLimited, "verify stream cap")
	if recCap.Header().Get("Retry-After") != "15" {
		t.Fatalf("Retry-After = %q, want 15", recCap.Header().Get("Retry-After"))
	}
	for i := 0; i < maxStreamsPerUser; i++ {
		sharedPool.Release("user-1")
	}

	// 4. Cancel verification & reject duplicate cancel.
	recCancel := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify/"+verifyID+"/cancel", "user-1", "")
	if recCancel.Code != http.StatusOK || !strings.Contains(recCancel.Body.String(), `"success":true`) {
		t.Fatalf("cancel verify = %d %s", recCancel.Code, recCancel.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/verify/"+verifyID+"/cancel", "user-1", ""), http.StatusNotFound, ErrBackupNotFound, "cancel already cancelled verify")

	if len(audits) < 2 {
		t.Fatalf("expected verify create & cancel audits, got %v", audits)
	}
}

func TestBackupModuleRestorePreviewsRunsReportsAndStreams(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}

	database, state := newBackupModuleTestDB(t, map[string][]driver.Value{
		"p-repo-tgt":    profileTestRow("p-repo-tgt", "user-1", "webdav", "https://repo.example.test", "repo-user", encSecret),
		"p-restore-tgt": profileTestRow("p-restore-tgt", "user-1", "nextcloud", "https://restore.example.test", "alice", encSecret),
	})

	now := time.Now().UTC()
	state.setJob(&backupModuleJobRecord{
		id:              "b-1",
		userID:          "user-1",
		targetProfileID: sql.NullString{String: "p-repo-tgt", Valid: true},
		targetProvider:  "webdav",
		targetURL:       "https://repo.example.test",
		targetUsername:  "repo-user",
		repositoryID:    "repo-1",
		repositoryRoot:  "/Backups/.clumoove-backup/repo-1",
		status:          "IDLE",
		deletionState:   "ACTIVE",
		createdAt:       now,
		updatedAt:       now,
	})
	state.setJob(&backupModuleJobRecord{
		id:             "b-other",
		userID:         "user-2",
		targetProvider: "webdav",
		repositoryRoot: "/Backups/.clumoove-backup/repo-2",
		status:         "IDLE",
		deletionState:  "ACTIVE",
		createdAt:      now,
		updatedAt:      now,
	})
	state.setSnapshot(&backupModuleSnapshotRecord{
		id:             "snap-ok",
		backupJobID:    "b-1",
		state:          "READY",
		integrityState: "OK",
		createdAt:      now,
	})
	state.setSnapshot(&backupModuleSnapshotRecord{
		id:             "snap-damaged",
		backupJobID:    "b-1",
		state:          "DAMAGED",
		integrityState: "DAMAGED",
		createdAt:      now,
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	sharedPool := newUserStreamPool(maxStreamsPerUser)
	var audits []db.AuditAction

	api := newBackupAPI(backupDependencies{
		db:                 database,
		encryptionKey:      key,
		rateLimiter:        limiter,
		streamPool:         sharedPool,
		streamPollInterval: 10 * time.Millisecond,
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. Restore preview validations & protections.
	limiter.blocked["restore-preview"] = true
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{}`), http.StatusTooManyRequests, ErrRateLimited, "restore preview rate limit")
	limiter.blocked["restore-preview"] = false

	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-other/snapshots/snap-ok/restore/previews", "user-1", `{}`), http.StatusNotFound, ErrBackupNotFound, "restore preview cross-user")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_provider":"immich","target_url":"https://immich.example.test","target_password":"k","selected_paths":["/Docs"],"target_root":"/Restored"}`), http.StatusBadRequest, ErrImmichBackupUnsupported, "restore preview immich")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_provider":"google","target_url":"https://www.googleapis.com","selected_paths":["/Docs"],"target_root":"/Restored"}`), http.StatusBadRequest, ErrRefreshTokenMissing, "restore preview oauth missing token")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_provider":"webdav","target_url":"https://tgt.example.test","target_username":"u","selected_paths":["/Docs"],"target_root":"/Restored"}`), http.StatusBadRequest, ErrMissingRequiredFields, "restore preview missing password")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Safe/.clumoove-backup/sub"}`), http.StatusBadRequest, ErrRestoreRepositoryOverlap, "restore preview .clumoove-backup overlap")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_profile_id":"p-repo-tgt","selected_paths":["/Docs"],"target_root":"/Backups"}`), http.StatusBadRequest, ErrRestoreRepositoryOverlap, "restore preview repo overlap")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-damaged/restore/previews", "user-1", `{"target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Restored"}`), http.StatusNotFound, ErrRestoreSnapshotUnavailable, "restore preview damaged snapshot")

	recPreview := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Restored","conflict_strategy":"OVERWRITE","threads":4,"bandwidth_mbps":50}`)
	if recPreview.Code != http.StatusAccepted {
		t.Fatalf("create restore preview = %d %s", recPreview.Code, recPreview.Body.String())
	}
	var previewResp map[string]string
	if err := json.Unmarshal(recPreview.Body.Bytes(), &previewResp); err != nil || previewResp["id"] == "" {
		t.Fatalf("preview response = %s err=%v", recPreview.Body.String(), err)
	}
	previewID := previewResp["id"]

	// 2. Get, cancel, and consume restore preview state transitions.
	recGetPreview := performBackupModuleRequest(mux, http.MethodGet, "/api/restore/previews/"+previewID, "user-1", "")
	if recGetPreview.Code != http.StatusOK || !strings.Contains(recGetPreview.Body.String(), previewID) {
		t.Fatalf("get restore preview = %d %s", recGetPreview.Code, recGetPreview.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/restore/previews/"+previewID, "user-2", ""), http.StatusNotFound, ErrRestoreNotFound, "get restore preview non-owner")

	// Queued preview cannot be consumed yet.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/"+previewID+"/consume", "user-1", ""), http.StatusConflict, ErrRestorePreviewInvalidState, "consume queued preview")

	// Cancel preview works on QUEUED/READY, fails 404 for non-owner or already cancelled.
	recCancelPreview := performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/"+previewID+"/cancel", "user-1", "")
	if recCancelPreview.Code != http.StatusOK {
		t.Fatalf("cancel restore preview = %d %s", recCancelPreview.Code, recCancelPreview.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/"+previewID+"/cancel", "user-1", ""), http.StatusNotFound, ErrRestoreNotFound, "cancel already cancelled preview")

	// Expired and stale preview consume mappings.
	state.setRestorePreview(&backupModuleRestorePreviewRecord{
		id:               "preview-expired",
		userID:           "user-1",
		backupJobID:      "b-1",
		backupSnapshotID: "snap-ok",
		targetProvider:   "nextcloud",
		targetRoot:       "/Restored",
		conflictStrategy: "RENAME",
		status:           "READY",
		expiresAt:        sql.NullTime{Time: now.Add(-time.Minute), Valid: true},
		createdAt:        now,
		updatedAt:        now,
	})
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/preview-expired/consume", "user-1", ""), http.StatusConflict, ErrRestorePreviewExpired, "consume expired preview")

	state.setRestorePreview(&backupModuleRestorePreviewRecord{
		id:               "preview-stale",
		userID:           "user-1",
		backupJobID:      "b-1",
		backupSnapshotID: "snap-damaged",
		targetProvider:   "nextcloud",
		targetRoot:       "/Restored",
		conflictStrategy: "RENAME",
		status:           "READY",
		expiresAt:        sql.NullTime{Time: now.Add(time.Hour), Valid: true},
		createdAt:        now,
		updatedAt:        now,
	})
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/preview-stale/consume", "user-1", ""), http.StatusConflict, ErrRestorePreviewStale, "consume stale snapshot preview")

	// Create and mark a READY preview, then consume it into a restore job & run.
	recPreview2 := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Restored","conflict_strategy":"OVERWRITE","threads":4}`)
	if recPreview2.Code != http.StatusAccepted {
		t.Fatalf("create preview 2 = %d %s", recPreview2.Code, recPreview2.Body.String())
	}
	var preview2Resp map[string]string
	_ = json.Unmarshal(recPreview2.Body.Bytes(), &preview2Resp)
	readyID := preview2Resp["id"]
	state.mu.Lock()
	state.restorePreviews[readyID].status = "READY"
	state.restorePreviews[readyID].expiresAt = sql.NullTime{Time: now.Add(time.Hour), Valid: true}
	state.mu.Unlock()

	recConsume := performBackupModuleRequest(mux, http.MethodPost, "/api/restore/previews/"+readyID+"/consume", "user-1", "")
	if recConsume.Code != http.StatusAccepted {
		t.Fatalf("consume preview = %d %s", recConsume.Code, recConsume.Body.String())
	}
	var consumeResp map[string]string
	if err := json.Unmarshal(recConsume.Body.Bytes(), &consumeResp); err != nil || consumeResp["restore_job_id"] == "" || consumeResp["restore_run_id"] == "" {
		t.Fatalf("consume response = %s err=%v", recConsume.Body.String(), err)
	}
	restoreJobID := consumeResp["restore_job_id"]
	restoreRunID := consumeResp["restore_run_id"]

	// Retry preview rejects mismatched configuration and active run.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"retry_restore_job_id":"`+restoreJobID+`","target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Restored","conflict_strategy":"OVERWRITE"}`), http.StatusConflict, ErrRestorePreviewInvalidState, "retry while run active")

	// 3. List, get, items, report (non-terminal conflict), stream, cancel, report (CSV neutralization), delete.
	for _, listPath := range []string{"/api/restore", "/api/restore/runs"} {
		recList := performBackupModuleRequest(mux, http.MethodGet, listPath, "user-1", "")
		if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), restoreRunID) {
			t.Fatalf("GET %s = %d %s", listPath, recList.Code, recList.Body.String())
		}
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID, "user-2", ""), http.StatusNotFound, ErrRestoreNotFound, "get restore run non-owner")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID+"/items", "user-2", ""), http.StatusNotFound, ErrRestoreNotFound, "list restore items non-owner")
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID+"/report", "user-1", ""), http.StatusConflict, ErrRestorePreviewInvalidState, "report while queued")

	// Active run blocks restore job deletion.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodDelete, "/api/restore/jobs/"+restoreJobID, "user-1", ""), http.StatusNotFound, ErrRestoreNotFound, "delete restore job while active")

	// Restore run SSE stream rejects non-owner, emits initial + updated restore event, and releases pool slot.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID+"/stream", "user-2", ""), http.StatusNotFound, ErrRestoreNotFound, "restore run stream non-owner")
	assertStreamPoolSlotsAvailable(t, sharedPool, "user-2")

	runBackupSSETest(t, mux, "/api/restore/runs/"+restoreRunID+"/stream", "user-1", "event: restore\ndata: ", func() {
		rr := state.getRestoreRun(restoreRunID)
		rr.status = "RUNNING"
		rr.processedFiles = 2
		state.setRestoreRun(rr)
	}, `"processed_files":2`)
	assertStreamPoolSlotsAvailable(t, sharedPool, "user-1")

	// Backup jobs SSE stream emits backup_jobs event and releases pool slot.
	runBackupSSETest(t, mux, "/api/backup/stream", "user-1", "event: backup_jobs\ndata: ", nil, "")
	assertStreamPoolSlotsAvailable(t, sharedPool, "user-1")

	// Cancel active restore run -> 202, repeating cancel -> 409.
	recCancelRun := performBackupModuleRequest(mux, http.MethodPost, "/api/restore/runs/"+restoreRunID+"/cancel", "user-1", "")
	if recCancelRun.Code != http.StatusAccepted {
		t.Fatalf("cancel restore run = %d %s", recCancelRun.Code, recCancelRun.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/restore/runs/"+restoreRunID+"/cancel", "user-1", ""), http.StatusConflict, ErrRestorePreviewInvalidState, "cancel already cancelled restore run")

	// Items & CSV report with formula neutralization on terminal run.
	state.addRestoreItem(restoreRunID, &backupModuleRestoreItemRecord{
		id:                   "ritem-1",
		restoreRunID:         restoreRunID,
		snapshotRelativePath: "=cmd|'/C calc'!A0",
		targetPath:           "+Restored/doc.txt",
		status:               "FAILED",
		verificationKind:     sql.NullString{String: "@sha256", Valid: true},
		sizeBytes:            42,
		errorCode:            sql.NullString{String: "-ERR", Valid: true},
	})
	recItems := performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID+"/items", "user-1", "")
	if recItems.Code != http.StatusOK || !strings.Contains(recItems.Body.String(), "ritem-1") {
		t.Fatalf("restore items = %d %s", recItems.Code, recItems.Body.String())
	}
	recReport := performBackupModuleRequest(mux, http.MethodGet, "/api/restore/runs/"+restoreRunID+"/report", "user-1", "")
	if recReport.Code != http.StatusOK {
		t.Fatalf("restore report = %d %s", recReport.Code, recReport.Body.String())
	}
	reportCSV := recReport.Body.String()
	for _, want := range []string{"'=cmd", "'+Restored/doc.txt", "'@sha256", "'-ERR"} {
		if !strings.Contains(reportCSV, want) {
			t.Fatalf("restore CSV missing neutralized %q: %s", want, reportCSV)
		}
	}

	// Retry preview succeeds once run is terminal with matching fingerprint, rejects mismatched fingerprint.
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"retry_restore_job_id":"`+restoreJobID+`","target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Different","conflict_strategy":"OVERWRITE"}`), http.StatusConflict, ErrRestorePreviewInvalidState, "retry mismatched config")
	recRetry := performBackupModuleRequest(mux, http.MethodPost, "/api/backup/b-1/snapshots/snap-ok/restore/previews", "user-1", `{"retry_restore_job_id":"`+restoreJobID+`","target_profile_id":"p-restore-tgt","selected_paths":["/Docs"],"target_root":"/Restored","conflict_strategy":"OVERWRITE"}`)
	if recRetry.Code != http.StatusAccepted {
		t.Fatalf("retry restore preview = %d %s", recRetry.Code, recRetry.Body.String())
	}

	// Delete restore job now succeeds with 204.
	recDel := performBackupModuleRequest(mux, http.MethodDelete, "/api/restore/jobs/"+restoreJobID, "user-1", "")
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("delete restore job = %d %s", recDel.Code, recDel.Body.String())
	}
	assertAPIResponseError(t, performBackupModuleRequest(mux, http.MethodDelete, "/api/restore/jobs/"+restoreJobID, "user-2", ""), http.StatusNotFound, ErrRestoreNotFound, "delete missing/non-owner restore job")

	if len(audits) < 5 {
		t.Fatalf("expected restore lifecycle audits, got %v", audits)
	}
}

func TestBackupModuleThroughGatewayHandler(t *testing.T) {
	server := newTestAPIGateway(t, map[string]*db.UserAuthState{
		"user-1": {Active: true, Role: "USER"},
	})
	server.rateLimiter = allowAllFileRateLimiter{}
	database, state := newBackupModuleTestDB(t, nil)
	server.db = database

	now := time.Now().UTC()
	state.setJob(&backupModuleJobRecord{
		id:            "b-gw",
		userID:        "user-1",
		status:        "IDLE",
		deletionState: "ACTIVE",
		createdAt:     now,
		updatedAt:     now,
	})
	state.setRestoreJob(&backupModuleRestoreJobRecord{
		id:               "rjob-gw",
		userID:           "user-1",
		backupJobID:      "b-gw",
		backupSnapshotID: "snap-gw",
		targetProvider:   "nextcloud",
		targetRoot:       "/Restored",
		conflictStrategy: "RENAME",
		createdAt:        now,
		updatedAt:        now,
	})
	state.setRestoreRun(&backupModuleRestoreRunRecord{
		id:           "rrun-gw",
		restoreJobID: "rjob-gw",
		generation:   1,
		status:       "COMPLETED",
		createdAt:    now,
		updatedAt:    now,
	})

	token, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	handler := server.Handler()

	doGatewayReq := func(method, target, body string) *httptest.ResponseRecorder {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, r)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := doGatewayReq(http.MethodGet, "/api/backup", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "b-gw") {
		t.Fatalf("gateway GET /api/backup = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doGatewayReq(http.MethodPost, "/api/backup/b-gw/verify", `{"mode":"METADATA"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("gateway POST /api/backup/b-gw/verify = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doGatewayReq(http.MethodGet, "/api/restore/runs/rrun-gw", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "rrun-gw") {
		t.Fatalf("gateway GET /api/restore/runs/rrun-gw = %d %s", rec.Code, rec.Body.String())
	}

	// Shared stream pool on APIServer caps backup streams when slots are exhausted.
	for i := 0; i < maxStreamsPerUser; i++ {
		if !server.streamPool().Acquire("user-1") {
			t.Fatalf("server.streamPool().Acquire failed at %d", i)
		}
	}
	recStream := doGatewayReq(http.MethodGet, "/api/backup/stream", "")
	assertAPIResponseError(t, recStream, http.StatusTooManyRequests, ErrRateLimited, "gateway backup stream cap")
	for i := 0; i < maxStreamsPerUser; i++ {
		server.streamPool().Release("user-1")
	}
}

var backupModuleDriverID atomic.Uint64

type backupModuleJobRecord struct {
	id                    string
	userID                string
	lockID                sql.NullInt64
	sourceProfileID       sql.NullString
	targetProfileID       sql.NullString
	sourceURL             string
	sourceUsername        string
	sourcePasswordEnc     string
	sourceRefreshTokenEnc sql.NullString
	sourceTokenExpiresAt  sql.NullTime
	sourceMegaSessionEnc  sql.NullString
	sourceMegaMasterEnc   sql.NullString
	targetURL             string
	targetUsername        string
	targetPasswordEnc     string
	targetRefreshTokenEnc sql.NullString
	targetTokenExpiresAt  sql.NullTime
	targetMegaSessionEnc  sql.NullString
	targetMegaMasterEnc   sql.NullString
	sourceProvider        string
	targetProvider        string
	selectedPaths         db.StringArray
	targetDir             string
	repositoryID          string
	repositoryRoot        string
	cronExpression        string
	timezone              string
	retentionCount        int
	threads               int
	status                string
	runGeneration         int
	lastRunAt             sql.NullTime
	lastRunStatus         sql.NullString
	totalFiles            int
	totalBytes            int64
	processedFiles        int
	processedBytes        int64
	deduplicatedBytes     int64
	failedFiles           int
	errorCode             sql.NullString
	deletionState         string
	createdAt             time.Time
	updatedAt             time.Time
}

type backupModuleRunRecord struct {
	id                string
	backupJobID       string
	generation        int
	trigger           string
	scheduledLocalKey sql.NullString
	state             string
	totalFiles        int
	totalBytes        int64
	processedFiles    int
	processedBytes    int64
	deduplicatedBytes int64
	failedFiles       int
	errorCode         sql.NullString
	startedAt         sql.NullTime
	finishedAt        sql.NullTime
	createdAt         time.Time
	updatedAt         time.Time
}

type backupModuleSnapshotRecord struct {
	id                   string
	backupJobID          string
	state                string
	createdAt            time.Time
	totalFiles           int
	totalDirs            int
	totalBytes           int64
	omittedUnstableCount int
	omittedErrorCount    int
	integrityState       string
}

type backupModuleSnapshotItemRecord struct {
	snapshotID   string
	relativePath string
	isDir        bool
	sizeBytes    int64
	mtime        sql.NullTime
	state        string
	errorCode    sql.NullString
}

type backupModuleVerifyRecord struct {
	id             string
	backupJobID    string
	kind           string
	state          string
	byteBudget     sql.NullInt64
	verifyMode     sql.NullString
	processedBytes int64
	totalPacks     int
	checkedPacks   int
	missingPacks   int
	damagedPacks   int
	errorCode      sql.NullString
	startedAt      sql.NullTime
	finishedAt     sql.NullTime
	createdAt      time.Time
}

type backupModuleRestorePreviewRecord struct {
	id                       string
	userID                   string
	backupJobID              string
	backupSnapshotID         string
	retryRestoreJobID        sql.NullString
	targetProfileID          sql.NullString
	selectedPaths            db.StringArray
	targetProvider           string
	targetURL                string
	targetUsername           string
	targetPasswordEnc        sql.NullString
	targetRefreshTokenEnc    sql.NullString
	targetTokenExpiresAt     sql.NullTime
	targetMegaSessionEnc     sql.NullString
	targetMegaMasterEnc      sql.NullString
	targetConnectionIdentity string
	targetRoot               string
	conflictStrategy         string
	threads                  int
	bandwidthMbps            int
	configFingerprint        []byte
	status                   string
	totalFiles               int
	totalDirectories         int
	totalBytes               int64
	existingFileConflicts    int
	mergeableDirectories     int
	typeConflicts            int
	unavailableItems         int
	expectedSkips            int
	expectedRenames          int
	metadataWarnings         int
	conflictExamples         json.RawMessage
	errorCode                sql.NullString
	coordinatorGeneration    int
	coordinatorLeaseUntil    sql.NullTime
	workerHash               sql.NullString
	readyAt                  sql.NullTime
	expiresAt                sql.NullTime
	createdAt                time.Time
	updatedAt                time.Time
}

type backupModuleRestoreJobRecord struct {
	id                       string
	userID                   string
	backupJobID              string
	backupSnapshotID         string
	sourceBackupRef          string
	sourceSnapshotRef        string
	targetProfileID          sql.NullString
	selectedPaths            db.StringArray
	targetProvider           string
	targetURL                string
	targetUsername           string
	targetConnectionIdentity string
	targetRoot               string
	conflictStrategy         string
	threads                  int
	bandwidthMbps            int
	configFingerprint        []byte
	createdAt                time.Time
	updatedAt                time.Time
}

type backupModuleRestoreRunRecord struct {
	id             string
	restoreJobID   string
	generation     int
	status         string
	totalFiles     int
	totalBytes     int64
	processedFiles int
	processedBytes int64
	failedFiles    int
	errorCode      sql.NullString
	startedAt      sql.NullTime
	finishedAt     sql.NullTime
	createdAt      time.Time
	updatedAt      time.Time
}

type backupModuleRestoreItemRecord struct {
	id                   string
	restoreRunID         string
	parentItemID         sql.NullString
	snapshotRelativePath string
	isDir                bool
	sizeBytes            int64
	fileSHA256           []byte
	sourceMTime          sql.NullTime
	sourceMetadata       json.RawMessage
	targetPath           string
	resolvedTargetPath   sql.NullString
	status               string
	verificationKind     sql.NullString
	outcomeCode          sql.NullString
	attempts             int
	nextRetryAt          sql.NullTime
	errorCode            sql.NullString
}

type backupModuleTestState struct {
	mu              sync.Mutex
	seq             int
	profiles        map[string][]driver.Value
	jobs            map[string]*backupModuleJobRecord
	runs            map[string]*backupModuleRunRecord
	schedules       map[string]bool
	snapshots       map[string]*backupModuleSnapshotRecord
	snapshotItems   []*backupModuleSnapshotItemRecord
	verifies        map[string]*backupModuleVerifyRecord
	restorePreviews map[string]*backupModuleRestorePreviewRecord
	restoreJobs     map[string]*backupModuleRestoreJobRecord
	restoreRuns     map[string]*backupModuleRestoreRunRecord
	restoreItems    map[string][]*backupModuleRestoreItemRecord
}

func (s *backupModuleTestState) nextID(prefix string) string {
	s.seq++
	return prefix + "-" + strconv.Itoa(s.seq)
}

func (s *backupModuleTestState) setJob(j *backupModuleJobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *j
	s.jobs[j.id] = &cp
}

func (s *backupModuleTestState) getJob(id string) *backupModuleJobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil
	}
	cp := *j
	return &cp
}

func (s *backupModuleTestState) scheduleActive(backupJobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules[backupJobID]
}

func (s *backupModuleTestState) setSnapshot(sn *backupModuleSnapshotRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sn
	s.snapshots[sn.id] = &cp
}

func (s *backupModuleTestState) setSnapshotItem(item *backupModuleSnapshotItemRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *item
	s.snapshotItems = append(s.snapshotItems, &cp)
}

func (s *backupModuleTestState) setVerify(v *backupModuleVerifyRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *v
	s.verifies[v.id] = &cp
}

func (s *backupModuleTestState) getVerify(id string) *backupModuleVerifyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.verifies[id]
	if !ok {
		return nil
	}
	cp := *v
	return &cp
}

func (s *backupModuleTestState) setRestorePreview(p *backupModuleRestorePreviewRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.restorePreviews[p.id] = &cp
}

func (s *backupModuleTestState) setRestoreJob(j *backupModuleRestoreJobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *j
	s.restoreJobs[j.id] = &cp
}

func (s *backupModuleTestState) setRestoreRun(r *backupModuleRestoreRunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.restoreRuns[r.id] = &cp
}

func (s *backupModuleTestState) getRestoreRun(id string) *backupModuleRestoreRunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.restoreRuns[id]
	if !ok {
		return nil
	}
	cp := *r
	return &cp
}

func (s *backupModuleTestState) addRestoreItem(runID string, item *backupModuleRestoreItemRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *item
	s.restoreItems[runID] = append(s.restoreItems[runID], &cp)
}

func newBackupModuleTestDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *backupModuleTestState) {
	t.Helper()
	if profiles == nil {
		profiles = map[string][]driver.Value{}
	}
	state := &backupModuleTestState{
		profiles:        profiles,
		jobs:            map[string]*backupModuleJobRecord{},
		runs:            map[string]*backupModuleRunRecord{},
		schedules:       map[string]bool{},
		snapshots:       map[string]*backupModuleSnapshotRecord{},
		verifies:        map[string]*backupModuleVerifyRecord{},
		restorePreviews: map[string]*backupModuleRestorePreviewRecord{},
		restoreJobs:     map[string]*backupModuleRestoreJobRecord{},
		restoreRuns:     map[string]*backupModuleRestoreRunRecord{},
		restoreItems:    map[string][]*backupModuleRestoreItemRecord{},
	}
	name := "backup-module-test-" + strconv.FormatUint(backupModuleDriverID.Add(1), 10)
	sql.Register(name, backupModuleDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

type backupModuleDriver struct{ state *backupModuleTestState }

func (d backupModuleDriver) Open(string) (driver.Conn, error) {
	return backupModuleConn{state: d.state}, nil
}

type backupModuleConn struct{ state *backupModuleTestState }

func (c backupModuleConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c backupModuleConn) Close() error                        { return nil }
func (c backupModuleConn) Begin() (driver.Tx, error) {
	return backupModuleTx{state: c.state}, nil
}
func (c backupModuleConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return backupModuleTx{state: c.state}, nil
}
func (c backupModuleConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.state.execQuery(query, args)
}
func (c backupModuleConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.state.execUpdate(query, args)
}

type backupModuleTx struct{ state *backupModuleTestState }

func (tx backupModuleTx) Commit() error   { return nil }
func (tx backupModuleTx) Rollback() error { return nil }
func (tx backupModuleTx) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return tx.state.execQuery(query, args)
}
func (tx backupModuleTx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return tx.state.execUpdate(query, args)
}

func asNullString(v any) sql.NullString {
	switch val := v.(type) {
	case nil:
		return sql.NullString{}
	case string:
		return sql.NullString{String: val, Valid: val != ""}
	case sql.NullString:
		return val
	default:
		return sql.NullString{}
	}
}

func asNullTime(v any) sql.NullTime {
	switch val := v.(type) {
	case time.Time:
		return sql.NullTime{Time: val, Valid: true}
	case sql.NullTime:
		return val
	default:
		return sql.NullTime{}
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asStringArray(v any) db.StringArray {
	switch val := v.(type) {
	case db.StringArray:
		return append(db.StringArray(nil), val...)
	case []string:
		return append(db.StringArray(nil), val...)
	case string:
		var out db.StringArray
		_ = out.Scan(val)
		return out
	default:
		return nil
	}
}

func asInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	default:
		return 0
	}
}

func jobRowValues(j *backupModuleJobRecord) []driver.Value {
	lockID := j.lockID.Int64
	var srcProfile, tgtProfile driver.Value
	if j.sourceProfileID.Valid {
		srcProfile = j.sourceProfileID.String
	}
	if j.targetProfileID.Valid {
		tgtProfile = j.targetProfileID.String
	}
	var srcRefresh, tgtRefresh, srcMegaSess, srcMegaKey, tgtMegaSess, tgtMegaKey driver.Value
	if j.sourceRefreshTokenEnc.Valid {
		srcRefresh = j.sourceRefreshTokenEnc.String
	}
	if j.targetRefreshTokenEnc.Valid {
		tgtRefresh = j.targetRefreshTokenEnc.String
	}
	if j.sourceMegaSessionEnc.Valid {
		srcMegaSess = j.sourceMegaSessionEnc.String
	}
	if j.sourceMegaMasterEnc.Valid {
		srcMegaKey = j.sourceMegaMasterEnc.String
	}
	if j.targetMegaSessionEnc.Valid {
		tgtMegaSess = j.targetMegaSessionEnc.String
	}
	if j.targetMegaMasterEnc.Valid {
		tgtMegaKey = j.targetMegaMasterEnc.String
	}
	var srcExp, tgtExp, lastRun driver.Value
	if j.sourceTokenExpiresAt.Valid {
		srcExp = j.sourceTokenExpiresAt.Time
	}
	if j.targetTokenExpiresAt.Valid {
		tgtExp = j.targetTokenExpiresAt.Time
	}
	if j.lastRunAt.Valid {
		lastRun = j.lastRunAt.Time
	}
	var lastRunStatus, errCode driver.Value
	if j.lastRunStatus.Valid {
		lastRunStatus = j.lastRunStatus.String
	}
	if j.errorCode.Valid {
		errCode = j.errorCode.String
	}
	pathsRaw, _ := j.selectedPaths.Value()
	return []driver.Value{
		j.id, j.userID, lockID, srcProfile, tgtProfile, j.sourceURL, j.sourceUsername, j.sourcePasswordEnc,
		srcRefresh, srcExp, srcMegaSess, srcMegaKey,
		j.targetURL, j.targetUsername, j.targetPasswordEnc, tgtRefresh, tgtExp,
		tgtMegaSess, tgtMegaKey, j.sourceProvider, j.targetProvider, pathsRaw,
		j.targetDir, j.repositoryID, j.repositoryRoot, j.cronExpression, j.timezone, int64(j.retentionCount), int64(j.threads), j.status, int64(j.runGeneration),
		lastRun, lastRunStatus, int64(j.totalFiles), j.totalBytes, int64(j.processedFiles), j.processedBytes, j.deduplicatedBytes, int64(j.failedFiles),
		errCode, j.deletionState, j.createdAt, j.updatedAt,
	}
}

func verifyRowValues(v *backupModuleVerifyRecord) []driver.Value {
	var budget, mode, errCode, startedAt, finishedAt driver.Value
	if v.byteBudget.Valid {
		budget = v.byteBudget.Int64
	}
	if v.verifyMode.Valid {
		mode = v.verifyMode.String
	}
	if v.errorCode.Valid {
		errCode = v.errorCode.String
	}
	if v.startedAt.Valid {
		startedAt = v.startedAt.Time
	}
	if v.finishedAt.Valid {
		finishedAt = v.finishedAt.Time
	}
	return []driver.Value{
		v.id, v.backupJobID, v.kind, v.state, budget, mode, v.processedBytes,
		int64(v.totalPacks), int64(v.checkedPacks), int64(v.missingPacks), int64(v.damagedPacks),
		errCode, startedAt, finishedAt, v.createdAt,
	}
}

func restoreRunRowValues(r *backupModuleRestoreRunRecord) []driver.Value {
	var errCode, started, finished driver.Value
	if r.errorCode.Valid {
		errCode = r.errorCode.String
	}
	if r.startedAt.Valid {
		started = r.startedAt.Time
	}
	if r.finishedAt.Valid {
		finished = r.finishedAt.Time
	}
	return []driver.Value{
		r.id, r.restoreJobID, int64(r.generation), r.status, int64(r.totalFiles), r.totalBytes,
		int64(r.processedFiles), r.processedBytes, int64(r.failedFiles), errCode,
		started, finished, r.createdAt, r.updatedAt,
	}
}

func (s *backupModuleTestState) execQuery(query string, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "connection_profiles"):
		row, ok := s.profiles[asString(args[0].Value)]
		owned := ok && len(row) > 1 && asString(row[1]) == asString(args[1].Value)
		return &backupProfileConnectionRows{rows: [][]driver.Value{{owned}}, columns: 1}, nil

	case strings.Contains(query, "FROM connection_profiles WHERE id"):
		row, ok := s.profiles[asString(args[0].Value)]
		if !ok {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 14}, nil

	case strings.Contains(query, "INSERT INTO backup_jobs"):
		now := time.Now().UTC()
		id := s.nextID("backup")
		repoID := s.nextID("repo")
		targetDir := asString(args[20].Value)
		job := &backupModuleJobRecord{
			id:                    id,
			userID:                asString(args[0].Value),
			sourceProfileID:       asNullString(args[1].Value),
			targetProfileID:       asNullString(args[2].Value),
			sourceURL:             asString(args[3].Value),
			sourceUsername:        asString(args[4].Value),
			sourcePasswordEnc:     asString(args[5].Value),
			sourceRefreshTokenEnc: asNullString(args[6].Value),
			sourceTokenExpiresAt:  asNullTime(args[7].Value),
			sourceMegaSessionEnc:  asNullString(args[8].Value),
			sourceMegaMasterEnc:   asNullString(args[9].Value),
			targetURL:             asString(args[10].Value),
			targetUsername:        asString(args[11].Value),
			targetPasswordEnc:     asString(args[12].Value),
			targetRefreshTokenEnc: asNullString(args[13].Value),
			targetTokenExpiresAt:  asNullTime(args[14].Value),
			targetMegaSessionEnc:  asNullString(args[15].Value),
			targetMegaMasterEnc:   asNullString(args[16].Value),
			sourceProvider:        asString(args[17].Value),
			targetProvider:        asString(args[18].Value),
			selectedPaths:         asStringArray(args[19].Value),
			targetDir:             targetDir,
			repositoryID:          repoID,
			repositoryRoot:        targetDir + "/.clumoove-backup/" + repoID,
			cronExpression:        asString(args[21].Value),
			timezone:              asString(args[22].Value),
			retentionCount:        asInt(args[23].Value),
			threads:               asInt(args[24].Value),
			status:                "IDLE",
			deletionState:         "ACTIVE",
			createdAt:             now,
			updatedAt:             now,
		}
		s.jobs[id] = job
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id}}, columns: 1}, nil

	case strings.Contains(query, "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE id = $1 AND user_id = $2)"):
		j, ok := s.jobs[asString(args[0].Value)]
		owned := ok && j.userID == asString(args[1].Value)
		return &backupProfileConnectionRows{rows: [][]driver.Value{{owned}}, columns: 1}, nil

	case strings.Contains(query, "FROM backup_jobs WHERE user_id = $1 ORDER BY created_at DESC"):
		userID := asString(args[0].Value)
		var rows [][]driver.Value
		for _, j := range s.jobs {
			if j.userID == userID {
				rows = append(rows, jobRowValues(j))
			}
		}
		return &backupProfileConnectionRows{rows: rows, columns: 43}, nil

	case strings.Contains(query, "SELECT cron_expression, timezone FROM backup_jobs WHERE id = $1 AND user_id = $2 AND status = 'PAUSED' FOR UPDATE"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) || j.status != "PAUSED" {
			return &backupProfileConnectionRows{columns: 2}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{j.cronExpression, j.timezone}}, columns: 2}, nil

	case strings.Contains(query, "FROM backup_jobs WHERE id = $1 AND user_id = $2"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) {
			return &backupProfileConnectionRows{columns: 43}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{jobRowValues(j)}, columns: 43}, nil

	case strings.Contains(query, "SELECT status, run_generation FROM backup_jobs WHERE id = $1 FOR UPDATE"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok {
			return &backupProfileConnectionRows{columns: 2}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{j.status, int64(j.runGeneration)}}, columns: 2}, nil

	case strings.Contains(query, "INSERT INTO backup_runs"):
		now := time.Now().UTC()
		runID := s.nextID("run")
		run := &backupModuleRunRecord{
			id:                runID,
			backupJobID:       asString(args[0].Value),
			generation:        asInt(args[1].Value),
			trigger:           asString(args[2].Value),
			scheduledLocalKey: asNullString(args[3].Value),
			state:             "QUEUED",
			createdAt:         now,
			updatedAt:         now,
		}
		s.runs[runID] = run
		var schedKey driver.Value
		if run.scheduledLocalKey.Valid {
			schedKey = run.scheduledLocalKey.String
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{run.id, run.backupJobID, int64(run.generation), run.trigger, schedKey, run.state, run.createdAt, run.updatedAt}}, columns: 8}, nil

	case strings.Contains(query, "FROM backup_runs r"):
		jobID, userID := asString(args[0].Value), asString(args[1].Value)
		j, ok := s.jobs[jobID]
		if !ok || j.userID != userID {
			return &backupProfileConnectionRows{columns: 17}, nil
		}
		var rows [][]driver.Value
		for _, r := range s.runs {
			if r.backupJobID != jobID {
				continue
			}
			var schedKey, errCode, started, finished driver.Value
			if r.scheduledLocalKey.Valid {
				schedKey = r.scheduledLocalKey.String
			}
			if r.errorCode.Valid {
				errCode = r.errorCode.String
			}
			if r.startedAt.Valid {
				started = r.startedAt.Time
			}
			if r.finishedAt.Valid {
				finished = r.finishedAt.Time
			}
			rows = append(rows, []driver.Value{
				r.id, r.backupJobID, int64(r.generation), r.trigger, schedKey, r.state,
				int64(r.totalFiles), r.totalBytes, int64(r.processedFiles), r.processedBytes, r.deduplicatedBytes, int64(r.failedFiles),
				errCode, started, finished, r.createdAt, r.updatedAt,
			})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 17}, nil

	case strings.Contains(query, "FROM backup_snapshots") && strings.Contains(query, "ORDER BY created_at DESC"):
		jobID := asString(args[0].Value)
		var rows [][]driver.Value
		for _, sn := range s.snapshots {
			if sn.backupJobID == jobID && (sn.state == "READY" || sn.state == "PARTIAL" || sn.state == "DAMAGED") {
				rows = append(rows, []driver.Value{
					sn.id, sn.state, sn.createdAt, int64(sn.totalFiles), int64(sn.totalDirs), sn.totalBytes,
					int64(sn.omittedUnstableCount), int64(sn.omittedErrorCount), sn.integrityState,
				})
			}
		}
		return &backupProfileConnectionRows{rows: rows, columns: 9}, nil

	case strings.Contains(query, "FROM backup_snapshot_items i"):
		snapID, jobID, likePattern := asString(args[0].Value), asString(args[1].Value), asString(args[2].Value)
		prefix := strings.TrimSuffix(likePattern, "%")
		prefix = strings.NewReplacer(`\\`, `\`, `\%`, `%`, `\_`, `_`).Replace(prefix)
		sn, ok := s.snapshots[snapID]
		if !ok || sn.backupJobID != jobID || (sn.state != "READY" && sn.state != "PARTIAL" && sn.state != "DAMAGED") {
			return &backupProfileConnectionRows{columns: 6}, nil
		}
		var rows [][]driver.Value
		for _, item := range s.snapshotItems {
			if item.snapshotID != snapID || !strings.HasPrefix(item.relativePath, prefix) {
				continue
			}
			var mtime, errCode driver.Value
			if item.mtime.Valid {
				mtime = item.mtime.Time
			}
			if item.errorCode.Valid {
				errCode = item.errorCode.String
			}
			rows = append(rows, []driver.Value{item.relativePath, item.isDir, item.sizeBytes, mtime, item.state, errCode})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 6}, nil

	case strings.Contains(query, "INSERT INTO backup_maintenance"):
		jobID, userID, mode := asString(args[0].Value), asString(args[1].Value), asString(args[2].Value)
		j, ok := s.jobs[jobID]
		if !ok || j.userID != userID || j.deletionState != "ACTIVE" {
			return &backupProfileConnectionRows{columns: 1}, nil
		}
		for _, v := range s.verifies {
			if v.backupJobID == jobID && v.kind == "VERIFY" && (v.state == "PENDING" || v.state == "RUNNING" || v.state == "RETRY_WAIT" || v.state == "CANCELLING") {
				return nil, errors.New("idx_backup_maintenance_active_verify")
			}
		}
		id := s.nextID("verify")
		var budget sql.NullInt64
		if args[3].Value != nil {
			if b, ok := args[3].Value.(int64); ok {
				budget = sql.NullInt64{Int64: b, Valid: true}
			}
		}
		s.verifies[id] = &backupModuleVerifyRecord{
			id:          id,
			backupJobID: jobID,
			kind:        "VERIFY",
			state:       "PENDING",
			byteBudget:  budget,
			verifyMode:  sql.NullString{String: mode, Valid: true},
			createdAt:   time.Now().UTC(),
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id}}, columns: 1}, nil

	case strings.Contains(query, "FROM backup_maintenance m JOIN backup_jobs j") && strings.Contains(query, "WHERE m.backup_job_id = $1"):
		jobID, userID := asString(args[0].Value), asString(args[1].Value)
		var list []*backupModuleVerifyRecord
		for _, v := range s.verifies {
			j := s.jobs[v.backupJobID]
			if v.backupJobID == jobID && j != nil && j.userID == userID && v.kind == "VERIFY" {
				list = append(list, v)
			}
		}
		sort.Slice(list, func(i, k int) bool { return list[i].createdAt.After(list[k].createdAt) })
		var rows [][]driver.Value
		for _, v := range list {
			rows = append(rows, verifyRowValues(v))
		}
		return &backupProfileConnectionRows{rows: rows, columns: 15}, nil

	case strings.Contains(query, "FROM backup_maintenance m JOIN backup_jobs j") && strings.Contains(query, "WHERE m.id = $1"):
		verifyID, userID := asString(args[0].Value), asString(args[1].Value)
		v, ok := s.verifies[verifyID]
		if !ok || v.kind != "VERIFY" {
			return &backupProfileConnectionRows{columns: 15}, nil
		}
		j := s.jobs[v.backupJobID]
		if j == nil || j.userID != userID {
			return &backupProfileConnectionRows{columns: 15}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{verifyRowValues(v)}, columns: 15}, nil

	case strings.Contains(query, "SELECT EXISTS (SELECT 1 FROM backup_snapshots WHERE id = $1 AND backup_job_id = $2"):
		sn, ok := s.snapshots[asString(args[0].Value)]
		restorable := ok && sn.backupJobID == asString(args[1].Value) && (sn.state == "READY" || sn.state == "PARTIAL") && sn.integrityState != "DAMAGED"
		return &backupProfileConnectionRows{rows: [][]driver.Value{{restorable}}, columns: 1}, nil

	case strings.Contains(query, "SELECT EXISTS (") && strings.Contains(query, "FROM backup_snapshots s JOIN backup_jobs j"):
		snapID, jobID, userID := asString(args[0].Value), asString(args[1].Value), asString(args[2].Value)
		sn, ok := s.snapshots[snapID]
		j := s.jobs[jobID]
		restorable := ok && j != nil && j.userID == userID && j.deletionState == "ACTIVE" && sn.backupJobID == jobID && (sn.state == "READY" || sn.state == "PARTIAL") && sn.integrityState != "DAMAGED"
		return &backupProfileConnectionRows{rows: [][]driver.Value{{restorable}}, columns: 1}, nil

	case strings.Contains(query, "SELECT j.config_fingerprint, j.source_backup_ref, j.source_snapshot_ref"):
		rj, ok := s.restoreJobs[asString(args[0].Value)]
		if !ok || rj.userID != asString(args[1].Value) {
			return &backupProfileConnectionRows{columns: 4}, nil
		}
		active := false
		for _, rr := range s.restoreRuns {
			if rr.restoreJobID == rj.id && (rr.status == "QUEUED" || rr.status == "PLANNING" || rr.status == "RUNNING" || rr.status == "VERIFYING" || rr.status == "CANCELLING") {
				active = true
				break
			}
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{rj.configFingerprint, rj.sourceBackupRef, rj.sourceSnapshotRef, active}}, columns: 4}, nil

	case strings.Contains(query, "INSERT INTO restore_previews"):
		now := time.Now().UTC()
		id := s.nextID("preview")
		fp, _ := args[19].Value.([]byte)
		s.restorePreviews[id] = &backupModuleRestorePreviewRecord{
			id:                       id,
			userID:                   asString(args[0].Value),
			backupJobID:              asString(args[1].Value),
			backupSnapshotID:         asString(args[2].Value),
			retryRestoreJobID:        asNullString(args[3].Value),
			targetProfileID:          asNullString(args[4].Value),
			selectedPaths:            asStringArray(args[5].Value),
			targetProvider:           asString(args[6].Value),
			targetURL:                asString(args[7].Value),
			targetUsername:           asString(args[8].Value),
			targetPasswordEnc:        asNullString(args[9].Value),
			targetRefreshTokenEnc:    asNullString(args[10].Value),
			targetTokenExpiresAt:     asNullTime(args[11].Value),
			targetMegaSessionEnc:     asNullString(args[12].Value),
			targetMegaMasterEnc:      asNullString(args[13].Value),
			targetConnectionIdentity: asString(args[14].Value),
			targetRoot:               asString(args[15].Value),
			conflictStrategy:         asString(args[16].Value),
			threads:                  asInt(args[17].Value),
			bandwidthMbps:            asInt(args[18].Value),
			configFingerprint:        append([]byte(nil), fp...),
			status:                   "QUEUED",
			createdAt:                now,
			updatedAt:                now,
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id}}, columns: 1}, nil

	case strings.Contains(query, "FROM restore_previews WHERE id = $1 AND user_id = $2 FOR UPDATE"):
		p, ok := s.restorePreviews[asString(args[0].Value)]
		if !ok || p.userID != asString(args[1].Value) {
			return &backupProfileConnectionRows{columns: 41}, nil
		}
		pathsRaw, _ := p.selectedPaths.Value()
		var conflictsRaw driver.Value = []byte("[]")
		if len(p.conflictExamples) > 0 {
			conflictsRaw = []byte(p.conflictExamples)
		}
		var retryID, tgtProfile, pwd, ref, megaSess, megaKey, errCode, workerHash driver.Value
		var tokExp, lease, readyAt, expiresAt driver.Value
		if p.retryRestoreJobID.Valid {
			retryID = p.retryRestoreJobID.String
		}
		if p.targetProfileID.Valid {
			tgtProfile = p.targetProfileID.String
		}
		if p.targetPasswordEnc.Valid {
			pwd = p.targetPasswordEnc.String
		}
		if p.targetRefreshTokenEnc.Valid {
			ref = p.targetRefreshTokenEnc.String
		}
		if p.targetTokenExpiresAt.Valid {
			tokExp = p.targetTokenExpiresAt.Time
		}
		if p.targetMegaSessionEnc.Valid {
			megaSess = p.targetMegaSessionEnc.String
		}
		if p.targetMegaMasterEnc.Valid {
			megaKey = p.targetMegaMasterEnc.String
		}
		if p.errorCode.Valid {
			errCode = p.errorCode.String
		}
		if p.coordinatorLeaseUntil.Valid {
			lease = p.coordinatorLeaseUntil.Time
		}
		if p.workerHash.Valid {
			workerHash = p.workerHash.String
		}
		if p.readyAt.Valid {
			readyAt = p.readyAt.Time
		}
		if p.expiresAt.Valid {
			expiresAt = p.expiresAt.Time
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{
			p.id, p.userID, p.backupJobID, p.backupSnapshotID, retryID, tgtProfile, pathsRaw, p.targetProvider, p.targetURL, p.targetUsername,
			pwd, ref, tokExp, megaSess, megaKey, p.targetConnectionIdentity, p.targetRoot, p.conflictStrategy, int64(p.threads), int64(p.bandwidthMbps),
			p.configFingerprint, p.status, int64(p.totalFiles), int64(p.totalDirectories), p.totalBytes, int64(p.existingFileConflicts), int64(p.mergeableDirectories),
			int64(p.typeConflicts), int64(p.unavailableItems), int64(p.expectedSkips), int64(p.expectedRenames), int64(p.metadataWarnings), conflictsRaw,
			errCode, int64(p.coordinatorGeneration), lease, workerHash, readyAt, expiresAt, p.createdAt, p.updatedAt,
		}}, columns: 41}, nil

	case strings.Contains(query, "FROM restore_previews WHERE id = $1 AND user_id = $2"):
		p, ok := s.restorePreviews[asString(args[0].Value)]
		if !ok || p.userID != asString(args[1].Value) {
			return &backupProfileConnectionRows{columns: 30}, nil
		}
		pathsRaw, _ := p.selectedPaths.Value()
		var conflictsRaw driver.Value = []byte("[]")
		if len(p.conflictExamples) > 0 {
			conflictsRaw = []byte(p.conflictExamples)
		}
		var retryID, tgtProfile, errCode, readyAt, expiresAt driver.Value
		if p.retryRestoreJobID.Valid {
			retryID = p.retryRestoreJobID.String
		}
		if p.targetProfileID.Valid {
			tgtProfile = p.targetProfileID.String
		}
		if p.errorCode.Valid {
			errCode = p.errorCode.String
		}
		if p.readyAt.Valid {
			readyAt = p.readyAt.Time
		}
		if p.expiresAt.Valid {
			expiresAt = p.expiresAt.Time
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{
			p.id, p.userID, p.backupJobID, p.backupSnapshotID, retryID, tgtProfile, pathsRaw, p.targetProvider, p.targetRoot, p.conflictStrategy,
			int64(p.threads), int64(p.bandwidthMbps), p.configFingerprint, p.status, int64(p.totalFiles), int64(p.totalDirectories), p.totalBytes,
			int64(p.existingFileConflicts), int64(p.mergeableDirectories), int64(p.typeConflicts), int64(p.unavailableItems), int64(p.expectedSkips),
			int64(p.expectedRenames), int64(p.metadataWarnings), conflictsRaw, errCode, readyAt, expiresAt, p.createdAt, p.updatedAt,
		}}, columns: 30}, nil

	case strings.Contains(query, "FROM restore_jobs WHERE id = $1 AND user_id = $2 FOR UPDATE"):
		rj, ok := s.restoreJobs[asString(args[0].Value)]
		if !ok || rj.userID != asString(args[1].Value) {
			return &backupProfileConnectionRows{columns: 17}, nil
		}
		pathsRaw, _ := rj.selectedPaths.Value()
		var tgtProfile driver.Value
		if rj.targetProfileID.Valid {
			tgtProfile = rj.targetProfileID.String
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{
			rj.id, rj.userID, rj.backupJobID, rj.backupSnapshotID, tgtProfile, pathsRaw, rj.targetProvider, rj.targetURL, rj.targetUsername,
			rj.targetConnectionIdentity, rj.targetRoot, rj.conflictStrategy, int64(rj.threads), int64(rj.bandwidthMbps), rj.configFingerprint, rj.createdAt, rj.updatedAt,
		}}, columns: 17}, nil

	case strings.Contains(query, "SELECT NOT EXISTS (SELECT 1 FROM restore_runs WHERE restore_job_id = $1"):
		jobID := asString(args[0].Value)
		terminal := true
		for _, rr := range s.restoreRuns {
			if rr.restoreJobID == jobID && (rr.status == "QUEUED" || rr.status == "PLANNING" || rr.status == "RUNNING" || rr.status == "VERIFYING" || rr.status == "CANCELLING") {
				terminal = false
				break
			}
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{terminal}}, columns: 1}, nil

	case strings.Contains(query, "INSERT INTO restore_jobs"):
		now := time.Now().UTC()
		id := s.nextID("rjob")
		fp, _ := args[13].Value.([]byte)
		rj := &backupModuleRestoreJobRecord{
			id:                       id,
			userID:                   asString(args[0].Value),
			backupJobID:              asString(args[1].Value),
			backupSnapshotID:         asString(args[2].Value),
			sourceBackupRef:          asString(args[1].Value),
			sourceSnapshotRef:        asString(args[2].Value),
			targetProfileID:          asNullString(args[3].Value),
			selectedPaths:            asStringArray(args[4].Value),
			targetProvider:           asString(args[5].Value),
			targetURL:                asString(args[6].Value),
			targetUsername:           asString(args[7].Value),
			targetConnectionIdentity: asString(args[8].Value),
			targetRoot:               asString(args[9].Value),
			conflictStrategy:         asString(args[10].Value),
			threads:                  asInt(args[11].Value),
			bandwidthMbps:            asInt(args[12].Value),
			configFingerprint:        append([]byte(nil), fp...),
			createdAt:                now,
			updatedAt:                now,
		}
		s.restoreJobs[id] = rj
		pathsRaw, _ := rj.selectedPaths.Value()
		var tgtProfile driver.Value
		if rj.targetProfileID.Valid {
			tgtProfile = rj.targetProfileID.String
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{
			rj.id, rj.userID, rj.backupJobID, rj.backupSnapshotID, tgtProfile, pathsRaw, rj.targetProvider, rj.targetURL, rj.targetUsername,
			rj.targetConnectionIdentity, rj.targetRoot, rj.conflictStrategy, int64(rj.threads), int64(rj.bandwidthMbps), rj.createdAt, rj.updatedAt,
		}}, columns: 16}, nil

	case strings.Contains(query, "INSERT INTO restore_runs"):
		now := time.Now().UTC()
		id := s.nextID("rrun")
		jobID := asString(args[0].Value)
		maxGen := 0
		for _, rr := range s.restoreRuns {
			if rr.restoreJobID == jobID && rr.generation > maxGen {
				maxGen = rr.generation
			}
		}
		rr := &backupModuleRestoreRunRecord{
			id:           id,
			restoreJobID: jobID,
			generation:   maxGen + 1,
			status:       "QUEUED",
			createdAt:    now,
			updatedAt:    now,
		}
		s.restoreRuns[id] = rr
		return &backupProfileConnectionRows{rows: [][]driver.Value{{
			rr.id, rr.restoreJobID, int64(rr.generation), rr.status, int64(asInt(args[1].Value)), int64(asInt(args[2].Value)),
			args[3].Value, args[4].Value, args[5].Value, args[6].Value, args[7].Value,
			int64(0), nil, nil, int64(0), int64(0), int64(0), int64(0), int64(0), nil, nil, nil, rr.createdAt, rr.updatedAt,
		}}, columns: 24}, nil

	case strings.Contains(query, "FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE j.user_id = $1"):
		userID := asString(args[0].Value)
		var rows [][]driver.Value
		for _, rr := range s.restoreRuns {
			rj := s.restoreJobs[rr.restoreJobID]
			if rj != nil && rj.userID == userID {
				rows = append(rows, restoreRunRowValues(rr))
			}
		}
		return &backupProfileConnectionRows{rows: rows, columns: 14}, nil

	case strings.Contains(query, "FROM restore_runs r JOIN restore_jobs j ON j.id = r.restore_job_id WHERE r.id = $1 AND j.user_id = $2"):
		runID, userID := asString(args[0].Value), asString(args[1].Value)
		rr, ok := s.restoreRuns[runID]
		if !ok {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		rj := s.restoreJobs[rr.restoreJobID]
		if rj == nil || rj.userID != userID {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{restoreRunRowValues(rr)}, columns: 14}, nil

	case strings.Contains(query, "FROM restore_items i JOIN restore_runs r"):
		runID, userID := asString(args[0].Value), asString(args[1].Value)
		rr, ok := s.restoreRuns[runID]
		if !ok {
			return &backupProfileConnectionRows{columns: 17}, nil
		}
		rj := s.restoreJobs[rr.restoreJobID]
		if rj == nil || rj.userID != userID {
			return &backupProfileConnectionRows{columns: 17}, nil
		}
		var rows [][]driver.Value
		for _, item := range s.restoreItems[runID] {
			var parentID, sourceMTime, resolvedPath, verKind, outcomeCode, nextRetry, errCode driver.Value
			if item.parentItemID.Valid {
				parentID = item.parentItemID.String
			}
			if item.sourceMTime.Valid {
				sourceMTime = item.sourceMTime.Time
			}
			if item.resolvedTargetPath.Valid {
				resolvedPath = item.resolvedTargetPath.String
			}
			if item.verificationKind.Valid {
				verKind = item.verificationKind.String
			}
			if item.outcomeCode.Valid {
				outcomeCode = item.outcomeCode.String
			}
			if item.nextRetryAt.Valid {
				nextRetry = item.nextRetryAt.Time
			}
			if item.errorCode.Valid {
				errCode = item.errorCode.String
			}
			rows = append(rows, []driver.Value{
				item.id, item.restoreRunID, parentID, item.snapshotRelativePath, item.isDir, item.sizeBytes,
				item.fileSHA256, sourceMTime, []byte(item.sourceMetadata), item.targetPath, resolvedPath,
				item.status, verKind, outcomeCode, int64(item.attempts), nextRetry, errCode,
			})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 17}, nil
	}

	return nil, driver.ErrSkip
}

func (s *backupModuleTestState) execUpdate(query string, args []driver.NamedValue) (driver.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.Contains(query, "INSERT INTO schedules"):
		s.schedules[asString(args[1].Value)] = true
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_jobs") && strings.Contains(query, "SET cron_expression = $3"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) || j.deletionState != "ACTIVE" {
			return driver.RowsAffected(0), nil
		}
		j.cronExpression = asString(args[2].Value)
		j.timezone = asString(args[3].Value)
		j.retentionCount = asInt(args[4].Value)
		j.threads = asInt(args[5].Value)
		j.updatedAt = time.Now().UTC()
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE schedules") && strings.Contains(query, "SET cron_expression = $3"):
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_jobs SET status = 'QUEUED'"):
		j, ok := s.jobs[asString(args[1].Value)]
		if !ok || j.runGeneration != asInt(args[2].Value) || (j.status != "IDLE" && j.status != "FAILED") {
			return driver.RowsAffected(0), nil
		}
		j.runGeneration = asInt(args[0].Value)
		j.status = "QUEUED"
		j.errorCode = sql.NullString{}
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_jobs SET status = 'PAUSED'"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) || (j.status != "IDLE" && j.status != "FAILED" && j.status != "QUEUED" && j.status != "SCANNING" && j.status != "RUNNING" && j.status != "VERIFYING") {
			return driver.RowsAffected(0), nil
		}
		j.status = "PAUSED"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_runs SET state = 'CANCELLED'"):
		jobID := asString(args[0].Value)
		var n int64
		for _, r := range s.runs {
			if r.backupJobID == jobID && (r.state == "QUEUED" || r.state == "SCANNING" || r.state == "RUNNING" || r.state == "VERIFYING") {
				r.state = "CANCELLED"
				n++
			}
		}
		return driver.RowsAffected(n), nil

	case strings.Contains(query, "UPDATE schedules SET is_active = FALSE"):
		s.schedules[asString(args[0].Value)] = false
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_jobs SET status = 'IDLE'"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) || j.status != "PAUSED" {
			return driver.RowsAffected(0), nil
		}
		j.status = "IDLE"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE schedules SET is_active = TRUE"):
		s.schedules[asString(args[1].Value)] = true
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_jobs SET status = 'DELETING', deletion_state = 'DELETING'"):
		j, ok := s.jobs[asString(args[0].Value)]
		if !ok || j.userID != asString(args[1].Value) || j.deletionState != "ACTIVE" {
			return driver.RowsAffected(0), nil
		}
		j.status = "DELETING"
		j.deletionState = "DELETING"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_snapshots SET state = 'EXPIRED'"):
		return driver.RowsAffected(0), nil

	case strings.Contains(query, "INSERT INTO backup_maintenance (backup_job_id, kind, byte_budget)"):
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE backup_maintenance m"):
		verifyID, userID := asString(args[0].Value), asString(args[1].Value)
		v, ok := s.verifies[verifyID]
		if !ok || v.kind != "VERIFY" {
			return driver.RowsAffected(0), nil
		}
		j := s.jobs[v.backupJobID]
		if j == nil || j.userID != userID {
			return driver.RowsAffected(0), nil
		}
		switch v.state {
		case "PENDING", "RETRY_WAIT":
			v.state = "CANCELLED"
			v.errorCode = sql.NullString{String: "BACKUP_VERIFY_CANCELLED", Valid: true}
			return driver.RowsAffected(1), nil
		case "RUNNING":
			v.state = "CANCELLING"
			v.errorCode = sql.NullString{String: "BACKUP_VERIFY_CANCELLED", Valid: true}
			return driver.RowsAffected(1), nil
		default:
			return driver.RowsAffected(0), nil
		}

	case strings.Contains(query, "UPDATE restore_previews SET status = 'CANCELLED'"):
		p, ok := s.restorePreviews[asString(args[0].Value)]
		if !ok || p.userID != asString(args[1].Value) || (p.status != "QUEUED" && p.status != "RUNNING" && p.status != "READY") {
			return driver.RowsAffected(0), nil
		}
		p.status = "CANCELLED"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE restore_previews SET status = 'CONSUMED'"):
		p, ok := s.restorePreviews[asString(args[0].Value)]
		if !ok || p.status != "READY" {
			return driver.RowsAffected(0), nil
		}
		p.status = "CONSUMED"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE restore_runs r SET status = 'CANCELLING'"):
		rr, ok := s.restoreRuns[asString(args[0].Value)]
		if !ok {
			return driver.RowsAffected(0), nil
		}
		rj := s.restoreJobs[rr.restoreJobID]
		if rj == nil || rj.userID != asString(args[1].Value) || (rr.status != "QUEUED" && rr.status != "PLANNING" && rr.status != "RUNNING") {
			return driver.RowsAffected(0), nil
		}
		rr.status = "CANCELLING"
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE restore_items SET status = 'CANCELLED'"):
		return driver.RowsAffected(0), nil

	case strings.Contains(query, "UPDATE restore_runs SET status = 'CANCELLED'"):
		rr, ok := s.restoreRuns[asString(args[0].Value)]
		if ok && rr.status == "CANCELLING" {
			rr.status = "CANCELLED"
			return driver.RowsAffected(1), nil
		}
		return driver.RowsAffected(0), nil

	case strings.Contains(query, "DELETE FROM restore_pack_pins"):
		return driver.RowsAffected(0), nil

	case strings.Contains(query, "DELETE FROM restore_jobs j"):
		jobID, userID := asString(args[0].Value), asString(args[1].Value)
		rj, ok := s.restoreJobs[jobID]
		if !ok || rj.userID != userID {
			return driver.RowsAffected(0), nil
		}
		for _, rr := range s.restoreRuns {
			if rr.restoreJobID == jobID && (rr.status == "QUEUED" || rr.status == "PLANNING" || rr.status == "RUNNING" || rr.status == "VERIFYING" || rr.status == "CANCELLING") {
				return driver.RowsAffected(0), nil
			}
		}
		delete(s.restoreJobs, jobID)
		return driver.RowsAffected(1), nil
	}

	return driver.RowsAffected(0), driver.ErrSkip
}
