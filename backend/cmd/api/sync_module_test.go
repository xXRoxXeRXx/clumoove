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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/queue"
	"backend/internal/storage"
	appSync "backend/internal/sync"
)

func TestSyncModuleRegistersAllRoutesWithExpectedAccessControls(t *testing.T) {
	api := newSyncAPI(syncDependencies{
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
		{http.MethodGet, "/api/sync"},
		{http.MethodGet, "/api/sync/stream"},
		{http.MethodPost, "/api/sync"},
		{http.MethodGet, "/api/sync/s1"},
		{http.MethodPost, "/api/sync/s1/start"},
		{http.MethodPost, "/api/sync/s1/pause"},
		{http.MethodPost, "/api/sync/s1/resume"},
		{http.MethodPost, "/api/sync/s1/reauth"},
		{http.MethodDelete, "/api/sync/s1"},
		{http.MethodGet, "/api/sync/s1/report"},
		{http.MethodGet, "/api/sync/s1/errors"},
		{http.MethodPut, "/api/sync/s1/threads"},
		{http.MethodPut, "/api/sync/s1/bandwidth"},
		{http.MethodGet, "/api/sync/s1/browse"},
		{http.MethodPost, "/api/sync/s1/mkdir"},
		{http.MethodPut, "/api/sync/s1/schedule"},
		{http.MethodPut, "/api/sync/s1/scope"},
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

func performSyncModuleRequest(mux http.Handler, method, target, userID, body string) *httptest.ResponseRecorder {
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

func TestSyncModuleSetupSchedulingScopeAndTuning(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	database, state := newSyncModuleTestDB(t, map[string][]driver.Value{
		"p-src":   profileTestRow("p-src", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
		"p-tgt":   profileTestRow("p-tgt", "user-1", "webdav", "https://tgt.example.test", "bob", encSecret),
		"p-other": profileTestRow("p-other", "user-2", "nextcloud", "https://other.example.test", "mallory", encSecret),
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	var bandwidthEvents []queue.BandwidthEvent
	var audits []db.AuditAction

	api := newSyncAPI(syncDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   limiter,
		publishBandwidthChange: func(_ context.Context, ev queue.BandwidthEvent) error {
			bandwidthEvents = append(bandwidthEvents, ev)
			return nil
		},
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. POST /api/sync validations, rate limit, profile snapshot, and creation.
	limiter.blocked["migration-sync-mutation"] = true
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{}`), http.StatusTooManyRequests, ErrRateLimited, "create rate limit")
	limiter.blocked["migration-sync-mutation"] = false

	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-other"}`), http.StatusNotFound, ErrProfileNotFound, "create cross-user target")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_provider":"google","source_url":"https://www.googleapis.com","target_profile_id":"p-tgt"}`), http.StatusBadRequest, ErrRefreshTokenMissing, "create oauth requires refresh token")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_profile_id":"p-src","target_provider":"immich","target_url":"https://immich.example.test","target_password":"k"}`), http.StatusBadRequest, ErrImmichSyncUnsupported, "create rejects immich")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","direction":"sideways"}`), http.StatusBadRequest, ErrSyncDirectionInvalid, "create invalid direction")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","conflict_strategy":"INVALID"}`), http.StatusBadRequest, ErrConflictStrategyInvalid, "create invalid conflict strategy")

	recCreate := performSyncModuleRequest(mux, http.MethodPost, "/api/sync", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","selected_paths":["/Docs"],"target_dir":"/Backup","interval_minutes":30}`)
	if recCreate.Code != http.StatusOK {
		t.Fatalf("create sync = %d %s", recCreate.Code, recCreate.Body.String())
	}
	var createResp map[string]string
	if err := json.Unmarshal(recCreate.Body.Bytes(), &createResp); err != nil || createResp["id"] == "" {
		t.Fatalf("create response = %s err=%v", recCreate.Body.String(), err)
	}
	jobID := createResp["id"]
	if state.scheduleCount() != 1 {
		t.Fatalf("scheduleCount = %d, want 1", state.scheduleCount())
	}

	// 2. PUT /api/sync/{id}/schedule
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/schedule", "user-2", `{"interval_minutes":15}`), http.StatusNotFound, ErrSyncNotFound, "schedule non-owner")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/schedule", "user-1", `{"interval_minutes":10}`), http.StatusBadRequest, ErrSyncIntervalInvalid, "schedule invalid interval")
	recSched := performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/schedule", "user-1", `{"interval_minutes":60}`)
	if recSched.Code != http.StatusOK || !strings.Contains(recSched.Body.String(), `"success":true`) {
		t.Fatalf("update schedule = %d %s", recSched.Code, recSched.Body.String())
	}
	if rec := state.getJob(jobID); rec == nil || rec.intervalMinutes != 60 {
		t.Fatalf("persisted interval = %+v, want 60", rec)
	}

	// 3. PUT /api/sync/{id}/scope
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/scope", "user-2", `{"selected_paths":["/A"]}`), http.StatusNotFound, ErrSyncNotFound, "scope non-owner")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/scope", "user-1", `{"selected_paths":[]}`), http.StatusBadRequest, ErrNoSourcePaths, "scope empty paths")

	recScope := performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/scope", "user-1", `{"selected_paths":["/Photos"],"target_dir":"/Archive","direction":"two_way","conflict_strategy":"RENAME"}`)
	if recScope.Code != http.StatusOK || !strings.Contains(recScope.Body.String(), `"success":true`) {
		t.Fatalf("update scope = %d %s", recScope.Code, recScope.Body.String())
	}

	// Active status prevents scope update.
	state.setJobStatus(jobID, "RUNNING")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/scope", "user-1", `{"selected_paths":["/Photos"]}`), http.StatusConflict, ErrSyncInvalidState, "scope while running")

	// 4. PUT /api/sync/{id}/threads
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/threads", "user-1", `{"threads":0}`), http.StatusBadRequest, ErrThreadsOutOfRange, "threads too low")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/threads", "user-1", `{"threads":17}`), http.StatusBadRequest, ErrThreadsOutOfRange, "threads too high")
	recThreads := performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/threads", "user-1", `{"threads":12}`)
	if recThreads.Code != http.StatusOK || state.getJob(jobID).threads != 12 {
		t.Fatalf("update threads = %d %s threads=%d", recThreads.Code, recThreads.Body.String(), state.getJob(jobID).threads)
	}

	// 5. PUT /api/sync/{id}/bandwidth (broadcast only when active)
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/bandwidth", "user-1", `{"limit_mbps":-1}`), http.StatusBadRequest, ErrBandwidthOutOfRange, "bandwidth negative")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/bandwidth", "user-1", `{"limit_mbps":1001}`), http.StatusBadRequest, ErrBandwidthOutOfRange, "bandwidth too high")

	state.setJobStatus(jobID, "IDLE")
	recBWIdle := performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/bandwidth", "user-1", `{"limit_mbps":50}`)
	if recBWIdle.Code != http.StatusOK || len(bandwidthEvents) != 0 {
		t.Fatalf("bandwidth idle = %d %s events=%v", recBWIdle.Code, recBWIdle.Body.String(), bandwidthEvents)
	}

	state.setJobStatus(jobID, "RUNNING")
	recBWRun := performSyncModuleRequest(mux, http.MethodPut, "/api/sync/"+jobID+"/bandwidth", "user-1", `{"limit_mbps":75}`)
	if recBWRun.Code != http.StatusOK || len(bandwidthEvents) != 1 || bandwidthEvents[0].SyncJobID != jobID || bandwidthEvents[0].BandwidthLimitMbps != 75 {
		t.Fatalf("bandwidth running = %d %s events=%+v", recBWRun.Code, recBWRun.Body.String(), bandwidthEvents)
	}

	if len(audits) < 3 {
		t.Fatalf("expected sync creation/schedule/scope audits, got %v", audits)
	}
}

func TestSyncModuleLifecycleOwnershipAndReauth(t *testing.T) {
	const key = "01234567890123456789012345678901"
	oldRefreshEnc, err := crypto.EncryptWithDomain("old-refresh", key, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatalf("encrypt refresh: %v", err)
	}

	database, state := newSyncModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&syncModuleRecord{
		id:                    "s-owned",
		userID:                "user-1",
		sourceProvider:        "google",
		targetProvider:        "nextcloud",
		sourceRefreshTokenEnc: sql.NullString{String: oldRefreshEnc, Valid: true},
		status:                "PAUSED_CONNECTION_LOSS",
		createdAt:             now,
		updatedAt:             now,
	})
	state.setJob(&syncModuleRecord{
		id:             "s-other",
		userID:         "user-2",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "IDLE",
		createdAt:      now,
		updatedAt:      now,
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	pass := &fakeSyncPass{}
	var audits []db.AuditAction
	var warnings []string

	api := newSyncAPI(syncDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   limiter,
		syncPass:      pass,
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
		warnf: func(_ *http.Request, format string, _ ...any) {
			warnings = append(warnings, format)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. Anti-enumeration: missing and other-user jobs both return 404 SYNC_NOT_FOUND.
	for _, targetID := range []string{"s-missing", "s-other"} {
		for _, tc := range []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPost, "/api/sync/" + targetID + "/start", ""},
			{http.MethodPost, "/api/sync/" + targetID + "/pause", ""},
			{http.MethodPost, "/api/sync/" + targetID + "/resume", ""},
			{http.MethodDelete, "/api/sync/" + targetID, ""},
			{http.MethodPost, "/api/sync/" + targetID + "/reauth", `{"role":"source","access_token":"a","refresh_token":"r"}`},
		} {
			rec := performSyncModuleRequest(mux, tc.method, tc.path, "user-1", tc.body)
			assertAPIResponseError(t, rec, http.StatusNotFound, ErrSyncNotFound, "anti-enum "+tc.method+" "+tc.path)
		}
	}

	// Ownership DB failure returns 500 INTERNAL_ERROR rather than masking as 404.
	state.setOwnershipErr(errors.New("db unavailable"))
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/start", "user-1", ""), http.StatusInternalServerError, ErrInternalError, "ownership db err")
	state.setOwnershipErr(nil)

	// 2. Start lifecycle outcomes & rate limit.
	limiter.blocked["migration-sync-mutation"] = true
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/start", "user-1", ""), http.StatusTooManyRequests, ErrRateLimited, "start rate limit")
	limiter.blocked["migration-sync-mutation"] = false

	pass.startRes = appSync.StartResult{Started: false, Status: "RUNNING"}
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/start", "user-1", ""), http.StatusConflict, ErrSyncAlreadyRunning, "start already running")

	pass.startRes = appSync.StartResult{Started: false, Status: "PAUSED"}
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/start", "user-1", ""), http.StatusConflict, ErrSyncInvalidState, "start paused state")

	pass.startRes = appSync.StartResult{Started: true}
	recStart := performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/start", "user-1", "")
	if recStart.Code != http.StatusOK || !strings.Contains(recStart.Body.String(), `"success":true`) {
		t.Fatalf("start ok = %d %s", recStart.Code, recStart.Body.String())
	}

	// 3. Pause lifecycle.
	pass.pauseRes = false
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/pause", "user-1", ""), http.StatusConflict, ErrSyncInvalidState, "pause invalid state")
	pass.pauseRes = true
	recPause := performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/pause", "user-1", "")
	if recPause.Code != http.StatusOK || !strings.Contains(recPause.Body.String(), `"success":true`) {
		t.Fatalf("pause ok = %d %s", recPause.Code, recPause.Body.String())
	}

	// 4. Resume lifecycle (deferred + immediate).
	pass.resumeRes = appSync.ResumeResult{Resumed: false}
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/resume", "user-1", ""), http.StatusConflict, ErrSyncInvalidState, "resume invalid state")

	pass.resumeRes = appSync.ResumeResult{Resumed: true, Deferred: true, StartError: errors.New("busy")}
	recResumeDef := performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/resume", "user-1", "")
	if recResumeDef.Code != http.StatusOK || !strings.Contains(recResumeDef.Body.String(), `"deferred":true`) || len(warnings) != 1 {
		t.Fatalf("resume deferred = %d %s warnings=%v", recResumeDef.Code, recResumeDef.Body.String(), warnings)
	}

	// 5. OAuth reauth validation, success (updates tokens and resets status to IDLE), and conflict.
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/reauth", "user-1", `{"role":"target","access_token":"a","refresh_token":"r"}`), http.StatusBadRequest, ErrProviderUnsupported, "reauth non-oauth target")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/reauth", "user-1", `{"role":"source","access_token":"","refresh_token":"r"}`), http.StatusBadRequest, ErrRefreshTokenMissing, "reauth missing access token")

	recReauth := performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/reauth", "user-1", `{"role":"source","access_token":"new-acc","refresh_token":"new-ref","expires_in":1800}`)
	if recReauth.Code != http.StatusOK || !strings.Contains(recReauth.Body.String(), `"success":true`) {
		t.Fatalf("reauth ok = %d %s", recReauth.Code, recReauth.Body.String())
	}
	if updated := state.getJob("s-owned"); updated.status != "IDLE" || updated.sourceRefreshTokenEnc.String == oldRefreshEnc {
		t.Fatalf("reauth did not update status/tokens: status=%s", updated.status)
	}

	// Concurrent expected-token mismatch produces 409 conflict.
	state.mu.Lock()
	state.jobs["s-owned"].sourceRefreshTokenEnc = sql.NullString{String: "changed-concurrently", Valid: true}
	state.mu.Unlock()
	// Wait: GetSyncJobContext reads the current expected token from DB, so simulate conflict via oauthErr or 0 rows:
	// Set oauthErr to db.ErrOAuthTokenConflict to simulate concurrent CAS failure between read and update.
	state.mu.Lock()
	state.oauthErr = db.ErrOAuthTokenConflict
	state.mu.Unlock()
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-owned/reauth", "user-1", `{"role":"source","access_token":"a2","refresh_token":"r2"}`), http.StatusConflict, ErrMigrationInvalidState, "reauth conflict")
	state.mu.Lock()
	state.oauthErr = nil
	state.mu.Unlock()

	// 6. Delete lifecycle.
	pass.deleteErr = errors.New("delete failed")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodDelete, "/api/sync/s-owned", "user-1", ""), http.StatusInternalServerError, ErrInternalError, "delete failure")
	pass.deleteErr = nil
	recDel := performSyncModuleRequest(mux, http.MethodDelete, "/api/sync/s-owned", "user-1", "")
	if recDel.Code != http.StatusOK || !strings.Contains(recDel.Body.String(), `"success":true`) {
		t.Fatalf("delete ok = %d %s", recDel.Code, recDel.Body.String())
	}

	if len(audits) != 4 {
		t.Fatalf("expected 4 lifecycle audits (start/pause/resume/delete), got %v", audits)
	}
}

func TestSyncModuleBrowsingReportsErrorsAndStreams(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	database, state := newSyncModuleTestDB(t, map[string][]driver.Value{
		"p-src": profileTestRow("p-src", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
	})

	now := time.Now().UTC()
	state.setJob(&syncModuleRecord{
		id:                "s-running",
		userID:            "user-1",
		sourceProfileID:   sql.NullString{String: "p-src", Valid: true},
		sourceProvider:    "nextcloud",
		targetProvider:    "webdav",
		targetURL:         "https://tgt.example.test",
		targetUsername:    "bob",
		targetPasswordEnc: encSecret,
		direction:         "one_way",
		conflictStrategy:  "OVERWRITE",
		intervalMinutes:   15,
		threads:           4,
		status:            "RUNNING",
		runGeneration:     2,
		targetDir:         "/Dest",
		createdAt:         now,
		updatedAt:         now,
	})
	state.setJob(&syncModuleRecord{
		id:             "s-verifying",
		userID:         "user-1",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "VERIFYING",
		createdAt:      now,
		updatedAt:      now,
	})
	state.setJob(&syncModuleRecord{
		id:             "s-other",
		userID:         "user-2",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "IDLE",
		createdAt:      now,
		updatedAt:      now,
	})
	state.activeFiles["s-running"] = []string{"/Docs/active.txt"}
	state.verifyFiles["s-verifying"] = []string{"/Docs/verify.txt"}
	state.failedTasks["s-running"] = []syncModuleFailedTask{
		{
			id:         "t-1",
			syncJobID:  "s-running",
			generation: 2,
			filePath:   "=cmd('/Docs/a.txt')",
			fileSize:   42,
			status:     "FAILED",
			errMsg:     sql.NullString{String: "+https://alice:super-secret-token@src.example.test?access_token=super-secret-token", Valid: true},
			attempts:   3,
			createdAt:  now,
		},
	}

	srcProvider := &migrationTestProvider{
		legacyFileManagerTestProvider: &legacyFileManagerTestProvider{
			listings: map[string][]storage.CloudResource{
				"/": {{Name: "Docs", Path: "/Docs", IsDir: true}},
			},
		},
	}
	tgtProvider := &migrationTestProvider{
		legacyFileManagerTestProvider: &legacyFileManagerTestProvider{
			listings: map[string][]storage.CloudResource{
				"/": {{Name: "Dest", Path: "/Dest", IsDir: true}},
			},
		},
	}

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	sharedPool := newUserStreamPool(maxStreamsPerUser)

	api := newSyncAPI(syncDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   limiter,
		streamPool:    sharedPool,
		profiles: &ownedProfileAccess{
			db:            database,
			encryptionKey: key,
			providerFactory: func(_ context.Context, _ string, baseURL, _, _ string, _ ...storage.ProviderOption) (profileProvider, error) {
				if strings.Contains(baseURL, "tgt.example.test") {
					return tgtProvider, nil
				}
				return srcProvider, nil
			},
		},
		streamPollInterval: 10 * time.Millisecond,
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	// 1. List and detail status (including active_files for RUNNING & VERIFYING).
	recList := performSyncModuleRequest(mux, http.MethodGet, "/api/sync", "user-1", "")
	if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), `"s-running"`) || strings.Contains(recList.Body.String(), `"s-other"`) {
		t.Fatalf("list syncs = %d %s", recList.Code, recList.Body.String())
	}

	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-other", "user-1", ""), http.StatusNotFound, ErrSyncNotFound, "non-owner status")
	recStatusRun := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running", "user-1", "")
	if recStatusRun.Code != http.StatusOK || !strings.Contains(recStatusRun.Body.String(), `"/Docs/active.txt"`) {
		t.Fatalf("status running = %d %s", recStatusRun.Code, recStatusRun.Body.String())
	}
	recStatusVer := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-verifying", "user-1", "")
	if recStatusVer.Code != http.StatusOK || !strings.Contains(recStatusVer.Body.String(), `"/Docs/verify.txt"`) {
		t.Fatalf("status verifying = %d %s", recStatusVer.Code, recStatusVer.Body.String())
	}

	// 2. Browse source (profile-bound) and target (legacy direct), plus failures.
	limiter.blocked["browse"] = true
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/browse", "user-1", ""), http.StatusTooManyRequests, ErrRateLimited, "browse rate limit")
	limiter.blocked["browse"] = false

	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/browse?resource_type=notes", "user-1", ""), http.StatusBadRequest, ErrInvalidResourceType, "browse invalid resource type")

	recBrowseSrc := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/browse?role=source&path=/", "user-1", "")
	if recBrowseSrc.Code != http.StatusOK || !strings.Contains(recBrowseSrc.Body.String(), `"/Docs"`) {
		t.Fatalf("browse source = %d %s", recBrowseSrc.Code, recBrowseSrc.Body.String())
	}
	recBrowseTgt := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/browse?role=target&path=/", "user-1", "")
	if recBrowseTgt.Code != http.StatusOK || !strings.Contains(recBrowseTgt.Body.String(), `"/Dest"`) {
		t.Fatalf("browse target = %d %s", recBrowseTgt.Code, recBrowseTgt.Body.String())
	}

	srcProvider.connectErr = errors.New("dial timeout")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/browse?role=source", "user-1", ""), http.StatusOK, ErrSourceConnectionFailed, "browse connect fail")
	srcProvider.connectErr = nil

	// 3. Mkdir on target.
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-running/mkdir", "user-1", `{"role":"source","path":"/","name":"X"}`), http.StatusBadRequest, ErrInvalidBody, "mkdir rejects non-target role")
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-running/mkdir", "user-1", `{"role":"target","path":"/Dest","name":".."}`), http.StatusOK, ErrFolderPathInvalid, "mkdir rejects traversal")
	recMkdir := performSyncModuleRequest(mux, http.MethodPost, "/api/sync/s-running/mkdir", "user-1", `{"role":"target","path":"/Dest","name":"NewFolder"}`)
	if recMkdir.Code != http.StatusOK || !strings.Contains(recMkdir.Body.String(), `"success":true`) || len(tgtProvider.createdDirs) != 1 || tgtProvider.createdDirs[0] != "/Dest/NewFolder" {
		t.Fatalf("mkdir = %d %s dirs=%v", recMkdir.Code, recMkdir.Body.String(), tgtProvider.createdDirs)
	}

	// 4. CSV report (formula sanitization) and paginated errors (secret sanitization).
	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-other/report", "user-1", ""), http.StatusNotFound, ErrSyncNotFound, "non-owner report")
	recReport := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/report", "user-1", "")
	if recReport.Code != http.StatusOK || recReport.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("report = %d ct=%s", recReport.Code, recReport.Header().Get("Content-Type"))
	}
	if !strings.Contains(recReport.Body.String(), "'=cmd('/Docs/a.txt')") || !strings.Contains(recReport.Body.String(), "'+https://") {
		t.Fatalf("report did not sanitize CSV formulas:\n%s", recReport.Body.String())
	}

	assertAPIResponseError(t, performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-other/errors", "user-1", ""), http.StatusNotFound, ErrSyncNotFound, "non-owner errors")
	recErrors := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/s-running/errors?limit=10&offset=0", "user-1", "")
	if recErrors.Code != http.StatusOK || !strings.Contains(recErrors.Body.String(), `"total":1`) || strings.Contains(recErrors.Body.String(), "super-secret-token") {
		t.Fatalf("errors = %d %s", recErrors.Code, recErrors.Body.String())
	}

	// 5. SSE stream emits sync_jobs event, exits on context cancel, and releases shared pool slot.
	ctx, cancel := context.WithCancel(context.Background())
	reqStream := httptest.NewRequest(http.MethodGet, "/api/sync/stream", nil).WithContext(ctx)
	reqStream.Header.Set("X-Test-User", "user-1")
	recStream := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(recStream, reqStream)
		close(done)
	}()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(recStream.Body.String(), "event: sync_jobs\ndata: ") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("sync stream did not exit on context cancel")
	}
	if recStream.Code != http.StatusOK || !strings.Contains(recStream.Body.String(), "event: sync_jobs\ndata: ") {
		t.Fatalf("sync stream = %d %s", recStream.Code, recStream.Body.String())
	}

	// 6. SSE rate limit and concurrent stream pool cap.
	limiter.blocked["sync-stream"] = true
	recRL := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/stream", "user-1", "")
	assertAPIResponseError(t, recRL, http.StatusTooManyRequests, ErrRateLimited, "sync stream rate limit")
	if recRL.Header().Get("Retry-After") != "15" {
		t.Fatalf("Retry-After = %q, want 15", recRL.Header().Get("Retry-After"))
	}
	limiter.blocked["sync-stream"] = false

	for i := 0; i < maxStreamsPerUser; i++ {
		if !sharedPool.Acquire("user-1") {
			t.Fatalf("sharedPool.Acquire failed at %d", i)
		}
	}
	recCap := performSyncModuleRequest(mux, http.MethodGet, "/api/sync/stream", "user-1", "")
	assertAPIResponseError(t, recCap, http.StatusTooManyRequests, ErrRateLimited, "sync stream concurrent cap")
	if recCap.Header().Get("Retry-After") != "15" {
		t.Fatalf("Retry-After on stream cap = %q, want 15", recCap.Header().Get("Retry-After"))
	}
	for i := 0; i < maxStreamsPerUser; i++ {
		sharedPool.Release("user-1")
	}
}

func TestSyncModuleThroughGatewayHandler(t *testing.T) {
	server := newTestAPIGateway(t, map[string]*db.UserAuthState{
		"user-1": {Active: true, Role: "USER"},
	})
	server.rateLimiter = allowAllFileRateLimiter{}
	database, state := newSyncModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&syncModuleRecord{
		id:        "s-gw",
		userID:    "user-1",
		status:    "IDLE",
		createdAt: now,
		updatedAt: now,
	})
	server.db = database

	handler := server.Handler()
	token, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/api/sync", nil))
	assertAPIResponseError(t, recUnauth, http.StatusUnauthorized, ErrUnauthorized, "gateway unauthenticated sync route")

	reqAuth := httptest.NewRequest(http.MethodGet, "/api/sync", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+token)
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK || !strings.Contains(recAuth.Body.String(), `"s-gw"`) {
		t.Fatalf("gateway authenticated sync = %d %s", recAuth.Code, recAuth.Body.String())
	}
}

type syncModuleRecord struct {
	id                    string
	userID                string
	sourceProfileID       sql.NullString
	targetProfileID       sql.NullString
	sourceURL             string
	sourceUsername        string
	sourcePasswordEnc     string
	sourceRefreshTokenEnc sql.NullString
	sourceTokenExpiresAt  sql.NullTime
	targetURL             string
	targetUsername        string
	targetPasswordEnc     string
	targetRefreshTokenEnc sql.NullString
	targetTokenExpiresAt  sql.NullTime
	sourceProvider        string
	targetProvider        string
	direction             string
	conflictStrategy      string
	deletePropagation     bool
	intervalMinutes       int
	threads               int
	bandwidthLimitMbps    int
	status                string
	runGeneration         int
	targetDir             string
	selectedPaths         db.StringArray
	errorMessage          sql.NullString
	createdAt             time.Time
	updatedAt             time.Time
}

type syncModuleFailedTask struct {
	id         string
	syncJobID  string
	generation int
	filePath   string
	fileSize   int64
	status     string
	errMsg     sql.NullString
	attempts   int
	createdAt  time.Time
}

type syncModuleDBState struct {
	mu           sync.Mutex
	profiles     map[string][]driver.Value
	jobs         map[string]*syncModuleRecord
	failedTasks  map[string][]syncModuleFailedTask
	activeFiles  map[string][]string
	verifyFiles  map[string][]string
	schedules    int
	ownershipErr error
	oauthErr     error
	nextID       int
}

func (s *syncModuleDBState) setJob(rec *syncModuleRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = make(map[string]*syncModuleRecord)
	}
	s.jobs[rec.id] = rec
}

func (s *syncModuleDBState) getJob(id string) *syncModuleRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.jobs[id]
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

func (s *syncModuleDBState) setJobStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.jobs[id]; rec != nil {
		rec.status = status
	}
}

func (s *syncModuleDBState) setOwnershipErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ownershipErr = err
}

func (s *syncModuleDBState) scheduleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules
}

var syncModuleDriverID atomic.Uint64

func newSyncModuleTestDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *syncModuleDBState) {
	t.Helper()
	state := &syncModuleDBState{
		profiles:    profiles,
		jobs:        make(map[string]*syncModuleRecord),
		failedTasks: make(map[string][]syncModuleFailedTask),
		activeFiles: make(map[string][]string),
		verifyFiles: make(map[string][]string),
	}
	name := "sync-module-test-" + strconv.FormatUint(syncModuleDriverID.Add(1), 10)
	sql.Register(name, syncModuleTestDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

type syncModuleTestDriver struct{ state *syncModuleDBState }

func (d syncModuleTestDriver) Open(string) (driver.Conn, error) {
	return syncModuleTestConn{state: d.state}, nil
}

type syncModuleTestConn struct{ state *syncModuleDBState }

func (c syncModuleTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c syncModuleTestConn) Close() error                       { return nil }
func (c syncModuleTestConn) Begin() (driver.Tx, error)          { return syncModuleTestTx{}, nil }
func (c syncModuleTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return syncModuleTestTx{}, nil
}

type syncModuleTestTx struct{}

func (syncModuleTestTx) Commit() error   { return nil }
func (syncModuleTestTx) Rollback() error { return nil }

func (c syncModuleTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	case strings.Contains(query, "UPDATE sync_jobs SET threads"):
		threads := int(args[0].Value.(int64))
		id := args[1].Value.(string)
		if rec := c.state.jobs[id]; rec != nil {
			rec.threads = threads
		}
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "SET bandwidth_limit_mbps"):
		limit := int(args[0].Value.(int64))
		id := args[1].Value.(string)
		if rec := c.state.jobs[id]; rec == nil {
			return driver.RowsAffected(0), nil
		} else {
			rec.bandwidthLimitMbps = limit
		}
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "SET interval_minutes"):
		interval := int(args[0].Value.(int64))
		id := args[1].Value.(string)
		if rec := c.state.jobs[id]; rec == nil {
			return driver.RowsAffected(0), nil
		} else {
			rec.intervalMinutes = interval
		}
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE schedules SET next_run_at"):
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "UPDATE sync_jobs SET selected_paths"):
		id := args[len(args)-1].Value.(string)
		rec := c.state.jobs[id]
		if rec == nil {
			return driver.RowsAffected(0), nil
		}
		if raw, ok := args[0].Value.([]byte); ok {
			var paths []string
			if err := json.Unmarshal(raw, &paths); err == nil {
				rec.selectedPaths = db.StringArray(paths)
			}
		}
		rec.targetDir = args[1].Value.(string)
		return driver.RowsAffected(1), nil

	case strings.Contains(query, "DELETE FROM sync_state"):
		return driver.RowsAffected(1), nil

	case (strings.Contains(query, "source_refresh_token_encrypted") || strings.Contains(query, "target_refresh_token_encrypted")) && strings.Contains(query, "UPDATE sync_jobs"):
		return c.updateSyncJobOAuthTestRecord(strings.Contains(query, "target_refresh_token_encrypted"), args)

	case strings.Contains(query, "SET status = $1") && strings.Contains(query, "FROM") == false && strings.Contains(query, "UPDATE sync_jobs"):
		status := args[0].Value.(string)
		id := args[2].Value.(string)
		if rec := c.state.jobs[id]; rec != nil {
			rec.status = status
		}
		return driver.RowsAffected(1), nil
	}

	return driver.RowsAffected(1), nil
}

func (c syncModuleTestConn) updateSyncJobOAuthTestRecord(isTarget bool, args []driver.NamedValue) (driver.Result, error) {
	if c.state.oauthErr != nil {
		return nil, c.state.oauthErr
	}
	id := args[3].Value.(string)
	rec := c.state.jobs[id]
	if rec == nil {
		return driver.RowsAffected(0), nil
	}
	passEnc := &rec.sourcePasswordEnc
	refreshEnc := &rec.sourceRefreshTokenEnc
	expiresAt := &rec.sourceTokenExpiresAt
	if isTarget {
		passEnc = &rec.targetPasswordEnc
		refreshEnc = &rec.targetRefreshTokenEnc
		expiresAt = &rec.targetTokenExpiresAt
	}
	if len(args) == 5 && refreshEnc.String != args[4].Value.(string) {
		return driver.RowsAffected(0), nil
	}
	*passEnc = args[0].Value.(string)
	*refreshEnc = sql.NullString{String: args[1].Value.(string), Valid: true}
	if exp, ok := args[2].Value.(time.Time); ok {
		*expiresAt = sql.NullTime{Time: exp, Valid: true}
	}
	return driver.RowsAffected(1), nil
}

func (c syncModuleTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	case strings.Contains(query, "SELECT EXISTS(SELECT 1 FROM sync_jobs"):
		if c.state.ownershipErr != nil {
			return nil, c.state.ownershipErr
		}
		id := args[0].Value.(string)
		userID := args[1].Value.(string)
		rec := c.state.jobs[id]
		return &backupProfileConnectionRows{rows: [][]driver.Value{{rec != nil && rec.userID == userID}}, columns: 1}, nil

	case strings.Contains(query, "SELECT EXISTS(SELECT 1 FROM connection_profiles"):
		id := args[0].Value.(string)
		userID := args[1].Value.(string)
		row, ok := c.state.profiles[id]
		return &backupProfileConnectionRows{rows: [][]driver.Value{{ok && row[1] == userID}}, columns: 1}, nil

	case strings.Contains(query, "FROM connection_profiles WHERE id"):
		id := args[0].Value.(string)
		row, ok := c.state.profiles[id]
		if !ok {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 14}, nil

	case strings.Contains(query, "INSERT INTO sync_jobs"):
		c.state.nextID++
		id := "sync-" + strconv.Itoa(c.state.nextID)
		now := time.Now().UTC()
		var selectedPaths db.StringArray
		if raw, ok := args[27].Value.(string); ok {
			_ = selectedPaths.Scan(raw)
		}
		rec := &syncModuleRecord{
			id:                 id,
			userID:             args[0].Value.(string),
			sourceURL:          args[3].Value.(string),
			sourceUsername:     args[4].Value.(string),
			sourcePasswordEnc:  args[5].Value.(string),
			targetURL:          args[10].Value.(string),
			targetUsername:     args[11].Value.(string),
			targetPasswordEnc:  args[12].Value.(string),
			sourceProvider:     args[17].Value.(string),
			targetProvider:     args[18].Value.(string),
			direction:          args[19].Value.(string),
			conflictStrategy:   args[20].Value.(string),
			deletePropagation:  args[21].Value.(bool),
			intervalMinutes:    int(args[22].Value.(int64)),
			threads:            int(args[23].Value.(int64)),
			bandwidthLimitMbps: int(args[24].Value.(int64)),
			status:             args[25].Value.(string),
			targetDir:          args[26].Value.(string),
			selectedPaths:      selectedPaths,
			createdAt:          now,
			updatedAt:          now,
		}
		if v, ok := args[1].Value.(string); ok && v != "" {
			rec.sourceProfileID = sql.NullString{String: v, Valid: true}
		}
		if v, ok := args[2].Value.(string); ok && v != "" {
			rec.targetProfileID = sql.NullString{String: v, Valid: true}
		}
		c.state.jobs[id] = rec
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id, now, now}}, columns: 3}, nil

	case strings.Contains(query, "INSERT INTO schedules"):
		c.state.schedules++
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"sched-" + strconv.Itoa(c.state.schedules), now, now}}, columns: 3}, nil

	case strings.Contains(query, "SELECT status FROM sync_jobs WHERE id = $1 FOR UPDATE"):
		id := args[0].Value.(string)
		rec := c.state.jobs[id]
		if rec == nil {
			return &backupProfileConnectionRows{columns: 1}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{{rec.status}}, columns: 1}, nil

	case strings.Contains(query, "FROM sync_jobs WHERE id = $1"):
		id := args[0].Value.(string)
		rec := c.state.jobs[id]
		if rec == nil {
			return &backupProfileConnectionRows{columns: 47}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{syncModuleJobRow(rec)}, columns: 47}, nil

	case strings.Contains(query, "FROM sync_jobs") && strings.Contains(query, "WHERE user_id = $1"):
		userID := args[0].Value.(string)
		var rows [][]driver.Value
		for _, rec := range c.state.jobs {
			if rec.userID == userID {
				rows = append(rows, syncModuleListRow(rec))
			}
		}
		return &backupProfileConnectionRows{rows: rows, columns: 34}, nil

	case strings.Contains(query, "SELECT file_path, metadata FROM tasks WHERE sync_job_id = $1 AND status = 'RUNNING'"):
		id := args[0].Value.(string)
		var rows [][]driver.Value
		for _, p := range c.state.activeFiles[id] {
			rows = append(rows, []driver.Value{p, []byte("{}")})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 2}, nil

	case strings.Contains(query, "SELECT file_path, metadata FROM tasks WHERE sync_job_id = $1 AND status = 'COMPLETED'"):
		id := args[0].Value.(string)
		var rows [][]driver.Value
		for _, p := range c.state.verifyFiles[id] {
			rows = append(rows, []driver.Value{p, []byte("{}")})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 2}, nil

	case strings.Contains(query, "FROM tasks") && strings.Contains(query, "pass_generation = $2 AND status = 'FAILED'"):
		id := args[0].Value.(string)
		gen := int(args[1].Value.(int64))
		var rows [][]driver.Value
		for _, ft := range c.state.failedTasks[id] {
			if ft.generation != gen {
				continue
			}
			var errVal any
			if ft.errMsg.Valid {
				errVal = ft.errMsg.String
			}
			rows = append(rows, []driver.Value{
				ft.id, id, ft.filePath, ft.fileSize, "", "", "",
				ft.status, errVal, int64(ft.attempts), nil, ft.createdAt, ft.createdAt, "files", []byte("{}"),
			})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 15}, nil

	case strings.Contains(query, "WITH errors AS MATERIALIZED"):
		id := args[0].Value.(string)
		list := c.state.failedTasks[id]
		if len(list) == 0 {
			return &backupProfileConnectionRows{rows: [][]driver.Value{{nil, nil, nil, nil, nil, nil, nil, nil, int64(0)}}, columns: 9}, nil
		}
		var rows [][]driver.Value
		for _, ft := range list {
			rows = append(rows, []driver.Value{
				ft.id, "transfer", "files", ft.filePath, ft.status, int64(ft.attempts), ft.errMsg.String, ft.createdAt, int64(len(list)),
			})
		}
		return &backupProfileConnectionRows{rows: rows, columns: 9}, nil
	}

	return &backupProfileConnectionRows{columns: 1}, nil
}

func syncModuleJobRow(rec *syncModuleRecord) []driver.Value {
	var srcProf, tgtProf, srcRef, tgtRef, errMsg any
	var srcExp, tgtExp any
	if rec.sourceProfileID.Valid {
		srcProf = rec.sourceProfileID.String
	}
	if rec.targetProfileID.Valid {
		tgtProf = rec.targetProfileID.String
	}
	if rec.sourceRefreshTokenEnc.Valid {
		srcRef = rec.sourceRefreshTokenEnc.String
	}
	if rec.targetRefreshTokenEnc.Valid {
		tgtRef = rec.targetRefreshTokenEnc.String
	}
	if rec.sourceTokenExpiresAt.Valid {
		srcExp = rec.sourceTokenExpiresAt.Time
	}
	if rec.targetTokenExpiresAt.Valid {
		tgtExp = rec.targetTokenExpiresAt.Time
	}
	if rec.errorMessage.Valid {
		errMsg = rec.errorMessage.String
	}
	pathsVal, _ := rec.selectedPaths.Value()
	return []driver.Value{
		rec.id, rec.userID, srcProf, tgtProf, rec.sourceURL, rec.sourceUsername, rec.sourcePasswordEnc,
		srcRef, srcExp, "", "",
		rec.targetURL, rec.targetUsername, rec.targetPasswordEnc,
		tgtRef, tgtExp, "", "",
		rec.sourceProvider, rec.targetProvider, rec.direction, rec.conflictStrategy,
		rec.deletePropagation, int64(rec.intervalMinutes), int64(rec.threads), int64(rec.bandwidthLimitMbps), rec.status, int64(rec.runGeneration), int64(0), nil, rec.targetDir,
		pathsVal, nil, nil, errMsg, nil,
		int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0),
		rec.createdAt, rec.updatedAt,
	}
}

func syncModuleListRow(rec *syncModuleRecord) []driver.Value {
	var srcProf, tgtProf, errMsg any
	if rec.sourceProfileID.Valid {
		srcProf = rec.sourceProfileID.String
	}
	if rec.targetProfileID.Valid {
		tgtProf = rec.targetProfileID.String
	}
	if rec.errorMessage.Valid {
		errMsg = rec.errorMessage.String
	}
	pathsVal, _ := rec.selectedPaths.Value()
	return []driver.Value{
		rec.id, rec.userID, srcProf, tgtProf, rec.sourceURL, rec.sourceUsername, rec.sourceProvider,
		rec.targetURL, rec.targetUsername, rec.targetProvider, rec.direction, rec.conflictStrategy,
		rec.deletePropagation, int64(rec.intervalMinutes), int64(rec.threads), int64(rec.bandwidthLimitMbps), rec.status, rec.targetDir,
		pathsVal, nil, nil, errMsg, nil,
		int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0), int64(0),
		rec.createdAt, rec.updatedAt,
	}
}

type fakeSyncPass struct {
	startRes  appSync.StartResult
	startErr  error
	pauseRes  bool
	pauseErr  error
	resumeRes appSync.ResumeResult
	resumeErr error
	deleteErr error

	startedIDs []string
	pausedIDs  []string
	resumedIDs []string
	deletedIDs []string
}

func (f *fakeSyncPass) Start(_ context.Context, id string) (appSync.StartResult, error) {
	f.startedIDs = append(f.startedIDs, id)
	return f.startRes, f.startErr
}

func (f *fakeSyncPass) Pause(_ context.Context, id string) (bool, error) {
	f.pausedIDs = append(f.pausedIDs, id)
	return f.pauseRes, f.pauseErr
}

func (f *fakeSyncPass) Resume(_, _ context.Context, id string) (appSync.ResumeResult, error) {
	f.resumedIDs = append(f.resumedIDs, id)
	return f.resumeRes, f.resumeErr
}

func (f *fakeSyncPass) Delete(_ context.Context, id string) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return f.deleteErr
}

func (f *fakeSyncPass) Cancel(context.Context, string) error              { return nil }
func (f *fakeSyncPass) Trigger(context.Context, string) (appSync.TriggerResult, error) {
	return appSync.TriggerResult{}, nil
}
func (f *fakeSyncPass) Recover(context.Context) ([]appSync.RecoveredPass, error) { return nil, nil }
func (f *fakeSyncPass) Finalize(appSync.Finalization) (bool, error)              { return false, nil }

var _ = auth.ClaimsKey
var _ = storage.ManagerLocator{}
