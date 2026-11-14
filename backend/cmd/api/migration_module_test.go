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
)

func TestMigrationModuleRegistersAllRoutesWithExpectedAccessControls(t *testing.T) {
	api := newMigrationAPI(migrationDependencies{
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
		{http.MethodGet, "/api/migration"},
		{http.MethodGet, "/api/migration/stream"},
		{http.MethodPost, "/api/migration/connect"},
		{http.MethodPost, "/api/migration/connect/test"},
		{http.MethodPost, "/api/migration/browse"},
		{http.MethodPost, "/api/migration/target/browse"},
		{http.MethodPost, "/api/migration/target/mkdir"},
		{http.MethodPost, "/api/migration/start"},
		{http.MethodGet, "/api/migration/m1"},
		{http.MethodGet, "/api/migration/m1/stream"},
		{http.MethodPost, "/api/migration/m1/pause"},
		{http.MethodPost, "/api/migration/m1/resume"},
		{http.MethodPost, "/api/migration/m1/cancel"},
		{http.MethodDelete, "/api/migration/m1"},
		{http.MethodGet, "/api/migration/m1/report"},
		{http.MethodGet, "/api/migration/m1/errors"},
		{http.MethodPost, "/api/migration/m1/retry-failed"},
		{http.MethodPost, "/api/migration/m1/reauth"},
		{http.MethodPost, "/api/migration/m1/reindex"},
		{http.MethodPut, "/api/migration/m1/bandwidth"},
		{http.MethodPut, "/api/migration/m1/threads"},
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

type migrationTestProvider struct {
	*legacyFileManagerTestProvider
	connectErr  error
	createdDirs []string
	mkdirErr    error
}

func (p *migrationTestProvider) Connect(context.Context) (bool, error) {
	if p.connectErr != nil {
		return false, p.connectErr
	}
	return true, nil
}

func (p *migrationTestProvider) CreateDirectory(_ context.Context, _ string, dirPath string) error {
	if p.mkdirErr != nil {
		return p.mkdirErr
	}
	p.createdDirs = append(p.createdDirs, dirPath)
	return nil
}

func performMigrationModuleRequest(mux http.Handler, method, target, userID, body string) *httptest.ResponseRecorder {
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

func TestMigrationModuleConnectBrowseAndStartWorkflows(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	database, state := newMigrationModuleTestDB(t, map[string][]driver.Value{
		"p-src":   profileTestRow("p-src", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
		"p-tgt":   profileTestRow("p-tgt", "user-1", "nextcloud", "https://tgt.example.test", "alice", encSecret),
		"p-other": profileTestRow("p-other", "user-2", "nextcloud", "https://other.example.test", "bob", encSecret),
	})

	srcProvider := &migrationTestProvider{
		legacyFileManagerTestProvider: &legacyFileManagerTestProvider{
			listings: map[string][]storage.CloudResource{
				"/": {{Name: "docs", Path: "/docs", IsDir: true}},
			},
		},
	}
	tgtProvider := &migrationTestProvider{
		legacyFileManagerTestProvider: &legacyFileManagerTestProvider{
			listings: map[string][]storage.CloudResource{
				"/": {{Name: "dest", Path: "/dest", IsDir: true}},
			},
		},
	}

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	var indexed []string
	var indexedMu sync.Mutex
	var audits []db.AuditAction

	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	api := newMigrationAPI(migrationDependencies{
		db:            database,
		encryptionKey: key,
		backgroundCtx: bgCtx,
		rateLimiter:   limiter,
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
		startIndexer: func(_ context.Context, id string) {
			indexedMu.Lock()
			indexed = append(indexed, id)
			indexedMu.Unlock()
		},
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	doReq := func(method, target, userID, body string) *httptest.ResponseRecorder {
		return performMigrationModuleRequest(mux, method, target, userID, body)
	}

	// 1. connect/test: rate limit, validation, ownership, and connection error mapping.
	limiter.blocked["connect-test"] = true
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/connect/test", "user-1", `{"role":"source"}`), http.StatusTooManyRequests, ErrRateLimited, "connect/test rate limit")
	limiter.blocked["connect-test"] = false

	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/connect/test", "user-1", `{"role":"invalid"}`), http.StatusBadRequest, ErrInvalidBody, "connect/test invalid role")
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/connect/test", "user-1", `{"role":"source","source_profile_id":"p-other"}`), http.StatusNotFound, ErrProfileNotFound, "connect/test cross-user profile")

	recTestOK := doReq(http.MethodPost, "/api/migration/connect/test", "user-1", `{"role":"source","source_profile_id":"p-src"}`)
	if recTestOK.Code != http.StatusOK || !strings.Contains(recTestOK.Body.String(), `"success":true`) {
		t.Fatalf("connect/test ok = %d %s", recTestOK.Code, recTestOK.Body.String())
	}

	srcProvider.connectErr = errors.New("dial timeout")
	recTestFail := doReq(http.MethodPost, "/api/migration/connect/test", "user-1", `{"role":"source","source_profile_id":"p-src"}`)
	assertAPIResponseError(t, recTestFail, http.StatusOK, ErrSourceConnectionFailed, "connect/test connection failure")
	srcProvider.connectErr = nil

	// 2. connect: connects both sides and lists source files; rejects cross-user target profile.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/connect", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-other"}`), http.StatusNotFound, ErrProfileNotFound, "connect cross-user target")
	recConnect := doReq(http.MethodPost, "/api/migration/connect", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt"}`)
	if recConnect.Code != http.StatusOK || !strings.Contains(recConnect.Body.String(), `"/docs"`) {
		t.Fatalf("connect = %d %s", recConnect.Code, recConnect.Body.String())
	}

	// 3. browse, target/browse, target/mkdir.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/browse", "user-1", `{"source_profile_id":"p-src","resource_type":"notes"}`), http.StatusBadRequest, ErrInvalidResourceType, "browse invalid resource type")
	recBrowse := doReq(http.MethodPost, "/api/migration/browse", "user-1", `{"source_profile_id":"p-src","resource_type":"files","path":"/"}`)
	if recBrowse.Code != http.StatusOK || !strings.Contains(recBrowse.Body.String(), `"/docs"`) {
		t.Fatalf("browse = %d %s", recBrowse.Code, recBrowse.Body.String())
	}
	recTgtBrowse := doReq(http.MethodPost, "/api/migration/target/browse", "user-1", `{"target_profile_id":"p-tgt","path":"/"}`)
	if recTgtBrowse.Code != http.StatusOK || !strings.Contains(recTgtBrowse.Body.String(), `"/dest"`) {
		t.Fatalf("target/browse = %d %s", recTgtBrowse.Code, recTgtBrowse.Body.String())
	}
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/target/mkdir", "user-1", `{"target_profile_id":"p-tgt","path":"/"}`), http.StatusOK, ErrFolderPathInvalid, "target/mkdir root rejected")
	recMkdir := doReq(http.MethodPost, "/api/migration/target/mkdir", "user-1", `{"target_profile_id":"p-tgt","path":"/new-dir"}`)
	if recMkdir.Code != http.StatusOK || !strings.Contains(recMkdir.Body.String(), `"success":true`) || len(tgtProvider.createdDirs) != 1 || tgtProvider.createdDirs[0] != "/new-dir" {
		t.Fatalf("target/mkdir = %d %s dirs=%v", recMkdir.Code, recMkdir.Body.String(), tgtProvider.createdDirs)
	}

	// 4. start: validations, OAuth refresh-token check, active cap, scheduled vs immediate start.
	limiter.blocked["migration-sync-mutation"] = true
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/start", "user-1", `{}`), http.StatusTooManyRequests, ErrRateLimited, "start rate limit")
	limiter.blocked["migration-sync-mutation"] = false

	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_provider":"google","source_url":"https://www.googleapis.com","target_profile_id":"p-tgt","paths":["/"]}`), http.StatusBadRequest, ErrRefreshTokenMissing, "start requires oauth refresh token")
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_profile_id":"p-src","target_provider":"immich","target_url":"https://immich.example.test","target_password":"k","conflict_strategy":"OVERWRITE","paths":["/"]}`), http.StatusBadRequest, ErrImmichConflictStrategy, "start rejects non-SKIP on immich target")

	pastTime := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","paths":["/"],"scheduled_time":"`+pastTime+`"}`), http.StatusBadRequest, ErrScheduledTimePast, "start rejects past scheduled time")

	futureTime := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	recSched := doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","paths":["/docs"],"scheduled_time":"`+futureTime+`"}`)
	if recSched.Code != http.StatusAccepted {
		t.Fatalf("scheduled start = %d %s", recSched.Code, recSched.Body.String())
	}
	var schedResp struct {
		Success     bool   `json:"success"`
		MigrationID string `json:"migration_id"`
		Scheduled   bool   `json:"scheduled"`
	}
	if err := json.Unmarshal(recSched.Body.Bytes(), &schedResp); err != nil || !schedResp.Success || !schedResp.Scheduled || schedResp.MigrationID == "" {
		t.Fatalf("scheduled start body = %s err=%v", recSched.Body.String(), err)
	}
	if state.scheduleCount() != 1 {
		t.Fatalf("scheduleCount = %d, want 1", state.scheduleCount())
	}

	recStart := doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","paths":["/docs"]}`)
	if recStart.Code != http.StatusAccepted {
		t.Fatalf("immediate start = %d %s", recStart.Code, recStart.Body.String())
	}
	var startResp struct {
		Success     bool   `json:"success"`
		MigrationID string `json:"migration_id"`
	}
	if err := json.Unmarshal(recStart.Body.Bytes(), &startResp); err != nil || !startResp.Success || startResp.MigrationID == "" {
		t.Fatalf("immediate start body = %s err=%v", recStart.Body.String(), err)
	}

	// Wait briefly for the async startIndexer goroutine.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		indexedMu.Lock()
		done := len(indexed) == 1 && indexed[0] == startResp.MigrationID
		indexedMu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	indexedMu.Lock()
	if len(indexed) != 1 || indexed[0] != startResp.MigrationID {
		t.Fatalf("startIndexer calls = %v, want [%s]", indexed, startResp.MigrationID)
	}
	indexedMu.Unlock()
	if len(audits) != 2 || audits[0] != db.AuditMigrationCreated || audits[1] != db.AuditMigrationCreated {
		t.Fatalf("audits = %v, want two AuditMigrationCreated", audits)
	}

	// Active migrations cap enforced (409).
	state.setActiveCount("user-1", maxActiveMigrations)
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/start", "user-1", `{"source_profile_id":"p-src","target_profile_id":"p-tgt","paths":["/docs"]}`), http.StatusConflict, ErrTooManyActiveMigrations, "start enforces active migration cap")
}

func TestMigrationModuleLifecycleActionsAndOwnership(t *testing.T) {
	const key = "01234567890123456789012345678901"
	database, state := newMigrationModuleTestDB(t, nil)
	now := time.Now().UTC()

	oldRefreshEnc, err := crypto.EncryptWithDomain("old-refresh", key, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatalf("encrypt refresh token: %v", err)
	}

	state.setMigration(&migrationModuleRecord{
		id:                    "m-run",
		userID:                "user-1",
		sourceProvider:        "google",
		targetProvider:        "nextcloud",
		sourceURL:             "https://www.googleapis.com",
		targetURL:             "https://tgt.example.test",
		targetDir:             "/",
		status:                "RUNNING",
		threads:               8,
		sourceRefreshTokenEnc: oldRefreshEnc,
		createdAt:             now,
		updatedAt:             now,
	})
	state.setMigration(&migrationModuleRecord{
		id:                "m-paused-nodisc",
		userID:            "user-1",
		sourceProvider:    "nextcloud",
		targetProvider:    "nextcloud",
		status:            "PAUSED",
		discoveryComplete: false,
		createdAt:         now,
		updatedAt:         now,
	})
	state.setMigration(&migrationModuleRecord{
		id:                "m-paused-disc",
		userID:            "user-1",
		sourceProvider:    "nextcloud",
		targetProvider:    "nextcloud",
		status:            "PAUSED",
		discoveryComplete: true,
		createdAt:         now,
		updatedAt:         now,
	})
	state.setMigration(&migrationModuleRecord{
		id:             "m-failed",
		userID:         "user-1",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "FAILED",
		createdAt:      now,
		updatedAt:      now,
	})
	state.setFailedTasks("m-failed", 3, 1024)
	state.setFailedTasks("m-run", 2, 512)

	var cancelErr error
	var cancelledIDs []string
	var bwEvents []queue.BandwidthEvent
	var indexed []string
	var indexedMu sync.Mutex
	var audits []db.AuditAction

	api := newMigrationAPI(migrationDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   allowAllFileRateLimiter{},
		publishCancelEvent: func(_ context.Context, id string) error {
			if cancelErr != nil {
				return cancelErr
			}
			cancelledIDs = append(cancelledIDs, id)
			return nil
		},
		publishBandwidthChange: func(_ context.Context, evt queue.BandwidthEvent) error {
			bwEvents = append(bwEvents, evt)
			return nil
		},
		startIndexer: func(_ context.Context, id string) {
			indexedMu.Lock()
			indexed = append(indexed, id)
			indexedMu.Unlock()
		},
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	doReq := func(method, target, userID, body string) *httptest.ResponseRecorder {
		return performMigrationModuleRequest(mux, method, target, userID, body)
	}

	// 1. Cross-user ownership rejected (403) across all lifecycle mutations.
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/migration/m-run/pause", ""},
		{http.MethodPost, "/api/migration/m-paused-disc/resume", ""},
		{http.MethodPost, "/api/migration/m-run/cancel", ""},
		{http.MethodDelete, "/api/migration/m-run", ""},
		{http.MethodPost, "/api/migration/m-failed/retry-failed", ""},
		{http.MethodPost, "/api/migration/m-failed/reindex", ""},
		{http.MethodPost, "/api/migration/m-run/reauth", `{"role":"source","access_token":"a","refresh_token":"r"}`},
		{http.MethodPut, "/api/migration/m-run/bandwidth", `{"limit_mbps":50}`},
		{http.MethodPut, "/api/migration/m-run/threads", `{"threads":4}`},
	} {
		assertAPIResponseError(t, doReq(tc.method, tc.path, "user-2", tc.body), http.StatusForbidden, ErrMigrationNotOwned, "non-owner "+tc.method+" "+tc.path)
	}

	// 2. Pause & invalid pause state.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-failed/pause", "user-1", ""), http.StatusConflict, ErrMigrationInvalidState, "pause failed migration")
	recPause := doReq(http.MethodPost, "/api/migration/m-run/pause", "user-1", "")
	if recPause.Code != http.StatusOK || state.getStatus("m-run") != "PAUSED" {
		t.Fatalf("pause = %d %s status=%s", recPause.Code, recPause.Body.String(), state.getStatus("m-run"))
	}

	// 3. Resume: invalid state, discovery-incomplete -> INDEXING + indexer, discovery-complete -> RUNNING without indexer.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-failed/resume", "user-1", ""), http.StatusConflict, ErrMigrationInvalidState, "resume failed migration")
	recResumeNoDisc := doReq(http.MethodPost, "/api/migration/m-paused-nodisc/resume", "user-1", "")
	if recResumeNoDisc.Code != http.StatusOK || state.getStatus("m-paused-nodisc") != "INDEXING" {
		t.Fatalf("resume nodisc = %d %s status=%s", recResumeNoDisc.Code, recResumeNoDisc.Body.String(), state.getStatus("m-paused-nodisc"))
	}
	recResumeDisc := doReq(http.MethodPost, "/api/migration/m-paused-disc/resume", "user-1", "")
	if recResumeDisc.Code != http.StatusOK || state.getStatus("m-paused-disc") != "RUNNING" {
		t.Fatalf("resume disc = %d %s status=%s", recResumeDisc.Code, recResumeDisc.Body.String(), state.getStatus("m-paused-disc"))
	}

	// 4. Threads & bandwidth range validation + bandwidth event publish only on active jobs.
	assertAPIResponseError(t, doReq(http.MethodPut, "/api/migration/m-run/threads", "user-1", `{"threads":0}`), http.StatusBadRequest, ErrThreadsOutOfRange, "threads 0")
	assertAPIResponseError(t, doReq(http.MethodPut, "/api/migration/m-run/threads", "user-1", `{"threads":17}`), http.StatusBadRequest, ErrThreadsOutOfRange, "threads 17")
	if rec := doReq(http.MethodPut, "/api/migration/m-run/threads", "user-1", `{"threads":12}`); rec.Code != http.StatusOK {
		t.Fatalf("threads 12 = %d %s", rec.Code, rec.Body.String())
	}

	assertAPIResponseError(t, doReq(http.MethodPut, "/api/migration/m-run/bandwidth", "user-1", `{"limit_mbps":-1}`), http.StatusBadRequest, ErrBandwidthOutOfRange, "bandwidth -1")
	assertAPIResponseError(t, doReq(http.MethodPut, "/api/migration/m-run/bandwidth", "user-1", `{"limit_mbps":1001}`), http.StatusBadRequest, ErrBandwidthOutOfRange, "bandwidth 1001")
	if rec := doReq(http.MethodPut, "/api/migration/m-paused-disc/bandwidth", "user-1", `{"limit_mbps":25}`); rec.Code != http.StatusOK {
		t.Fatalf("bandwidth 25 = %d %s", rec.Code, rec.Body.String())
	}
	if len(bwEvents) != 1 || bwEvents[0].MigrationID != "m-paused-disc" || bwEvents[0].BandwidthLimitMbps != 25 {
		t.Fatalf("bwEvents = %+v", bwEvents)
	}

	// 5. Retry-failed & Reindex state checks and resets.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-paused-disc/retry-failed", "user-1", ""), http.StatusConflict, ErrMigrationInvalidState, "retry-failed on running")
	recRetry := doReq(http.MethodPost, "/api/migration/m-failed/retry-failed", "user-1", "")
	if recRetry.Code != http.StatusOK || !strings.Contains(recRetry.Body.String(), `"retried":3`) || state.getStatus("m-failed") != "RUNNING" {
		t.Fatalf("retry-failed = %d %s status=%s", recRetry.Code, recRetry.Body.String(), state.getStatus("m-failed"))
	}

	state.setStatus("m-failed", "FAILED")
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-paused-disc/reindex", "user-1", ""), http.StatusConflict, ErrMigrationInvalidState, "reindex on running")
	recReindex := doReq(http.MethodPost, "/api/migration/m-failed/reindex", "user-1", "")
	if recReindex.Code != http.StatusAccepted || state.getStatus("m-failed") != "INDEXING" {
		t.Fatalf("reindex = %d %s status=%s", recReindex.Code, recReindex.Body.String(), state.getStatus("m-failed"))
	}

	// Wait for resume(nodisc) and reindex to invoke startIndexer.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		indexedMu.Lock()
		n := len(indexed)
		indexedMu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	indexedMu.Lock()
	gotSet := map[string]bool{}
	for _, id := range indexed {
		gotSet[id] = true
	}
	if len(indexed) != 2 || !gotSet["m-paused-nodisc"] || !gotSet["m-failed"] {
		t.Fatalf("indexed = %v, want {m-paused-nodisc, m-failed}", indexed)
	}
	indexedMu.Unlock()

	// 6. Reauth: rejects non-OAuth provider, missing token, token conflict; succeeds on valid OAuth token.
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-run/reauth", "user-1", `{"role":"target","access_token":"a","refresh_token":"r"}`), http.StatusBadRequest, ErrProviderUnsupported, "reauth non-oauth target")
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-run/reauth", "user-1", `{"role":"source","access_token":"","refresh_token":"r"}`), http.StatusBadRequest, ErrRefreshTokenMissing, "reauth missing access token")

	state.setOAuthConflict(true)
	assertAPIResponseError(t, doReq(http.MethodPost, "/api/migration/m-run/reauth", "user-1", `{"role":"source","access_token":"new-a","refresh_token":"new-r"}`), http.StatusConflict, ErrMigrationInvalidState, "reauth conflict")
	state.setOAuthConflict(false)

	recReauth := doReq(http.MethodPost, "/api/migration/m-run/reauth", "user-1", `{"role":"source","access_token":"new-a","refresh_token":"new-r","expires_in":1800}`)
	if recReauth.Code != http.StatusOK || !strings.Contains(recReauth.Body.String(), `"retried":2`) {
		t.Fatalf("reauth = %d %s", recReauth.Code, recReauth.Body.String())
	}

	// 7. Cancel & Delete (delete aborts before row deletion if publishCancelEvent fails).
	recCancel := doReq(http.MethodPost, "/api/migration/m-run/cancel", "user-1", "")
	if recCancel.Code != http.StatusOK || state.getStatus("m-run") != "CANCELLED" || len(cancelledIDs) != 1 || cancelledIDs[0] != "m-run" {
		t.Fatalf("cancel = %d %s status=%s cancelled=%v", recCancel.Code, recCancel.Body.String(), state.getStatus("m-run"), cancelledIDs)
	}

	cancelErr = errors.New("redis unavailable")
	assertAPIResponseError(t, doReq(http.MethodDelete, "/api/migration/m-run", "user-1", ""), http.StatusInternalServerError, ErrInternalError, "delete aborts when cancel publish fails")
	if !state.hasMigration("m-run") {
		t.Fatalf("expected migration m-run to remain when cancel publish fails")
	}
	cancelErr = nil

	recDelete := doReq(http.MethodDelete, "/api/migration/m-run", "user-1", "")
	if recDelete.Code != http.StatusOK || state.hasMigration("m-run") {
		t.Fatalf("delete = %d %s stillExists=%v", recDelete.Code, recDelete.Body.String(), state.hasMigration("m-run"))
	}

	if len(audits) < 4 {
		t.Fatalf("expected lifecycle audit events, got %v", audits)
	}
}

func TestMigrationModuleQueriesReportsAndStreamLimits(t *testing.T) {
	database, state := newMigrationModuleTestDB(t, nil)
	now := time.Now().UTC()

	state.setMigration(&migrationModuleRecord{
		id:             "m-owned",
		userID:         "user-1",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		sourceURL:      "https://src.example.test",
		targetURL:      "https://tgt.example.test",
		targetDir:      "/dest",
		status:         "COMPLETED",
		threads:        4,
		errorMessage:   sql.NullString{String: "failed https://alice:super-secret-token@src.example.test?access_token=super-secret-token", Valid: true},
		createdAt:      now,
		updatedAt:      now,
	})
	state.setMigration(&migrationModuleRecord{
		id:             "m-other",
		userID:         "user-2",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "RUNNING",
		createdAt:      now,
		updatedAt:      now,
	})
	state.setMigration(&migrationModuleRecord{
		id:             "m-live",
		userID:         "user-1",
		sourceProvider: "nextcloud",
		targetProvider: "nextcloud",
		status:         "RUNNING",
		createdAt:      now,
		updatedAt:      now,
	})

	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	sharedPool := newUserStreamPool(maxStreamsPerUser)

	api := newMigrationAPI(migrationDependencies{
		db:            database,
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   limiter,
		streamPool:    sharedPool,
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	doReq := func(method, target, userID string) *httptest.ResponseRecorder {
		return performMigrationModuleRequest(mux, method, target, userID, "")
	}

	// 1. List migrations returns only user-1's migrations.
	recList := doReq(http.MethodGet, "/api/migration", "user-1")
	if recList.Code != http.StatusOK || !strings.Contains(recList.Body.String(), `"m-owned"`) || strings.Contains(recList.Body.String(), `"m-other"`) {
		t.Fatalf("list migrations = %d %s", recList.Code, recList.Body.String())
	}

	// 2. Detail status: 404 missing, 403 cross-user, 200 sanitized payload for owner.
	assertAPIResponseError(t, doReq(http.MethodGet, "/api/migration/missing", "user-1"), http.StatusNotFound, ErrMigrationNotFound, "missing migration status")
	assertAPIResponseError(t, doReq(http.MethodGet, "/api/migration/m-other", "user-1"), http.StatusForbidden, ErrMigrationNotOwned, "non-owner migration status")

	recDetail := doReq(http.MethodGet, "/api/migration/m-owned", "user-1")
	if recDetail.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", recDetail.Code, recDetail.Body.String())
	}
	if strings.Contains(recDetail.Body.String(), "super-secret-token") {
		t.Fatalf("detail response leaked unredacted token: %s", recDetail.Body.String())
	}
	if !strings.Contains(recDetail.Body.String(), `"active_file":"/docs/file.txt"`) {
		t.Fatalf("detail missing active_file: %s", recDetail.Body.String())
	}

	// 3. CSV report & paginated errors: ownership check + sanitized outputs.
	assertAPIResponseError(t, doReq(http.MethodGet, "/api/migration/m-other/report", "user-1"), http.StatusForbidden, ErrMigrationNotOwned, "non-owner report")
	recReport := doReq(http.MethodGet, "/api/migration/m-owned/report", "user-1")
	if recReport.Code != http.StatusOK || recReport.Header().Get("Content-Type") != "text/csv" {
		t.Fatalf("report = %d ct=%s body=%s", recReport.Code, recReport.Header().Get("Content-Type"), recReport.Body.String())
	}
	if !strings.Contains(recReport.Body.String(), "File Path,Size (Bytes),Retries,WebDAV Error Message") ||
		!strings.Contains(recReport.Body.String(), "/dest/fail.txt") ||
		!strings.Contains(recReport.Body.String(), "[indexing/files] permission denied") {
		t.Fatalf("unexpected report CSV:\n%s", recReport.Body.String())
	}

	assertAPIResponseError(t, doReq(http.MethodGet, "/api/migration/m-other/errors", "user-1"), http.StatusForbidden, ErrMigrationNotOwned, "non-owner errors")
	recErrors := doReq(http.MethodGet, "/api/migration/m-owned/errors?limit=10&offset=0", "user-1")
	if recErrors.Code != http.StatusOK || !strings.Contains(recErrors.Body.String(), `"total":1`) || strings.Contains(recErrors.Body.String(), "super-secret-token") {
		t.Fatalf("errors = %d %s", recErrors.Code, recErrors.Body.String())
	}

	// 4. Detail SSE stream: terminal migration emits initial event, exits immediately, and releases slot.
	assertAPIResponseError(t, doReq(http.MethodGet, "/api/migration/m-other/stream", "user-1"), http.StatusForbidden, ErrMigrationNotOwned, "non-owner detail stream")

	recTermStream := doReq(http.MethodGet, "/api/migration/m-owned/stream", "user-1")
	if recTermStream.Code != http.StatusOK ||
		!strings.HasPrefix(recTermStream.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(recTermStream.Body.String(), "event: migration\ndata: ") {
		t.Fatalf("terminal detail stream = %d ct=%s body=%s", recTermStream.Code, recTermStream.Header().Get("Content-Type"), recTermStream.Body.String())
	}

	// 5. List SSE stream & non-terminal detail SSE stream exit on context cancel and release stream slots.
	for _, streamPath := range []string{"/api/migration/stream", "/api/migration/m-live/stream"} {
		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, streamPath, nil).WithContext(ctx)
		req.Header.Set("X-Test-User", "user-1")
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			mux.ServeHTTP(rec, req)
			close(done)
		}()
		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatalf("%s did not exit on context cancel", streamPath)
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data: ") {
			t.Fatalf("%s = %d %s", streamPath, rec.Code, rec.Body.String())
		}
	}

	// 6. Stream rate limit and shared concurrent stream pool cap (maxStreamsPerUser = 10).
	limiter.blocked["migration-stream"] = true
	recRL := doReq(http.MethodGet, "/api/migration/stream", "user-1")
	assertAPIResponseError(t, recRL, http.StatusTooManyRequests, ErrRateLimited, "migration stream rate limit")
	if recRL.Header().Get("Retry-After") != "15" {
		t.Fatalf("Retry-After = %q, want 15", recRL.Header().Get("Retry-After"))
	}
	limiter.blocked["migration-stream"] = false

	for i := 0; i < maxStreamsPerUser; i++ {
		if !sharedPool.Acquire("user-1") {
			t.Fatalf("sharedPool.Acquire failed at %d", i)
		}
	}
	recCap := doReq(http.MethodGet, "/api/migration/stream", "user-1")
	assertAPIResponseError(t, recCap, http.StatusTooManyRequests, ErrRateLimited, "migration stream concurrent cap")
	if recCap.Header().Get("Retry-After") != "15" {
		t.Fatalf("Retry-After on stream cap = %q, want 15", recCap.Header().Get("Retry-After"))
	}
	recDetailCap := doReq(http.MethodGet, "/api/migration/m-owned/stream", "user-1")
	assertAPIResponseError(t, recDetailCap, http.StatusTooManyRequests, ErrRateLimited, "migration detail stream concurrent cap")

	for i := 0; i < maxStreamsPerUser; i++ {
		sharedPool.Release("user-1")
	}
	// After releasing all shared slots, terminal stream succeeds again.
	if recAfter := doReq(http.MethodGet, "/api/migration/m-owned/stream", "user-1"); recAfter.Code != http.StatusOK {
		t.Fatalf("stream after release = %d %s", recAfter.Code, recAfter.Body.String())
	}
}

func TestMigrationModuleThroughGatewayHandler(t *testing.T) {
	server := newTestAPIGateway(t, map[string]*db.UserAuthState{
		"user-1": {Active: true, Role: "USER"},
	})
	server.rateLimiter = allowAllFileRateLimiter{}
	database, state := newMigrationModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setMigration(&migrationModuleRecord{
		id:        "m-gw",
		userID:    "user-1",
		status:    "COMPLETED",
		createdAt: now,
		updatedAt: now,
	})
	server.db = database

	handler := server.Handler()
	token, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	// Unauthenticated request rejected by gateway JWT middleware.
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/api/migration", nil))
	assertAPIResponseError(t, recUnauth, http.StatusUnauthorized, ErrUnauthorized, "gateway unauthenticated migration route")

	// Authenticated request passes through gateway into the migration module.
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/migration", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+token)
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK || !strings.Contains(recAuth.Body.String(), `"m-gw"`) {
		t.Fatalf("gateway authenticated list = %d %s", recAuth.Code, recAuth.Body.String())
	}

	// Shared stream pool on APIServer caps migration streams when other streams consume slots.
	for i := 0; i < maxStreamsPerUser; i++ {
		if !server.streamPool().Acquire("user-1") {
			t.Fatalf("server.streamPool().Acquire failed at %d", i)
		}
	}
	reqStream := httptest.NewRequest(http.MethodGet, "/api/migration/m-gw/stream", nil)
	reqStream.Header.Set("Authorization", "Bearer "+token)
	recStream := httptest.NewRecorder()
	handler.ServeHTTP(recStream, reqStream)
	assertAPIResponseError(t, recStream, http.StatusTooManyRequests, ErrRateLimited, "gateway shared stream cap")
}

// --- Test SQL driver for migration module feature tests ---

var migrationModuleDriverID atomic.Uint64

type migrationModuleRecord struct {
	id                    string
	userID                string
	sourceProfileID       string
	targetProfileID       string
	sourceProvider        string
	targetProvider        string
	sourceURL             string
	targetURL             string
	targetDir             string
	status                string
	threads               int64
	bandwidthLimitMbps    int64
	sourceRefreshTokenEnc string
	targetRefreshTokenEnc string
	discoveryComplete     bool
	errorMessage          sql.NullString
	createdAt             time.Time
	updatedAt             time.Time
}

type migrationModuleDBState struct {
	mu                sync.Mutex
	profiles          map[string][]driver.Value
	migrations        map[string]*migrationModuleRecord
	failedTaskCounts  map[string][2]int64
	schedules         int
	activeCountByUser map[string]int
	oauthConflict     bool
	nextID            int
}

func (s *migrationModuleDBState) setMigration(rec *migrationModuleRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.migrations[rec.id] = rec
}

func (s *migrationModuleDBState) hasMigration(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.migrations[id]
	return ok
}

func (s *migrationModuleDBState) getStatus(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.migrations[id]; rec != nil {
		return rec.status
	}
	return ""
}

func (s *migrationModuleDBState) setStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.migrations[id]; rec != nil {
		rec.status = status
	}
}

func (s *migrationModuleDBState) setFailedTasks(migrationID string, count int64, bytesSum int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failedTaskCounts[migrationID] = [2]int64{count, bytesSum}
}

func (s *migrationModuleDBState) setOAuthConflict(conflict bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthConflict = conflict
}

func (s *migrationModuleDBState) setActiveCount(userID string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCountByUser[userID] = count
}

func (s *migrationModuleDBState) scheduleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.schedules
}

func newMigrationModuleTestDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *migrationModuleDBState) {
	t.Helper()
	state := &migrationModuleDBState{
		profiles:          profiles,
		migrations:        make(map[string]*migrationModuleRecord),
		failedTaskCounts:  make(map[string][2]int64),
		activeCountByUser: make(map[string]int),
	}
	name := "migration-module-test-" + strconv.FormatUint(migrationModuleDriverID.Add(1), 10)
	sql.Register(name, migrationModuleTestDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

type migrationModuleTestDriver struct{ state *migrationModuleDBState }

func (d migrationModuleTestDriver) Open(string) (driver.Conn, error) {
	return migrationModuleTestConn{state: d.state}, nil
}

type migrationModuleTestConn struct{ state *migrationModuleDBState }

func (c migrationModuleTestConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c migrationModuleTestConn) Close() error                        { return nil }
func (c migrationModuleTestConn) Begin() (driver.Tx, error) {
	return migrationModuleTestTx{state: c.state}, nil
}
func (c migrationModuleTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return migrationModuleTestTx{state: c.state}, nil
}

func (c migrationModuleTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()

	switch {
	case strings.Contains(query, "SET notification_generation") && strings.Contains(query, "UPDATE migrations"):
		if len(args) >= 3 {
			if rec := c.state.migrations[args[2].Value.(string)]; rec != nil {
				rec.status, _ = args[0].Value.(string)
				rec.updatedAt = time.Now().UTC()
			}
		}
	case strings.Contains(query, "SET threads = $1") && strings.Contains(query, "UPDATE migrations"):
		if rec := c.state.migrations[args[1].Value.(string)]; rec != nil {
			rec.threads, _ = args[0].Value.(int64)
		}
	case strings.Contains(query, "SET bandwidth_limit_mbps = $1") && strings.Contains(query, "UPDATE migrations"):
		if rec := c.state.migrations[args[1].Value.(string)]; rec != nil {
			rec.bandwidthLimitMbps, _ = args[0].Value.(int64)
		}
	case strings.Contains(query, "source_refresh_token_encrypted = $5"):
		if c.state.oauthConflict {
			return driver.RowsAffected(0), nil
		}
		if rec := c.state.migrations[args[3].Value.(string)]; rec != nil {
			rec.sourceRefreshTokenEnc, _ = args[1].Value.(string)
		}
	case strings.Contains(query, "SET failed_retry_done = FALSE") || strings.Contains(query, "failed_retry_done = FALSE"):
		if len(args) >= 3 {
			if rec := c.state.migrations[args[2].Value.(string)]; rec != nil {
				rec.status = "RUNNING"
			}
		}
	case strings.Contains(query, "SET total_files = 0") && strings.Contains(query, "UPDATE migrations"):
		if rec := c.state.migrations[args[0].Value.(string)]; rec != nil {
			rec.status = "INDEXING"
			rec.discoveryComplete = false
		}
	case strings.Contains(query, "DELETE FROM migrations WHERE id = $1"):
		delete(c.state.migrations, args[0].Value.(string))
	}
	return driver.RowsAffected(1), nil
}

func (c migrationModuleTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "connection_profiles") {
		id := args[0].Value.(string)
		userID := args[1].Value.(string)
		row, ok := c.state.profiles[id]
		owned := ok && len(row) > 1 && row[1] == userID
		return &backupProfileConnectionRows{rows: [][]driver.Value{{owned}}, columns: 1}, nil
	}
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "migrations") {
		id := args[0].Value.(string)
		userID := args[1].Value.(string)
		c.state.mu.Lock()
		rec := c.state.migrations[id]
		owned := rec != nil && rec.userID == userID
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{owned}}, columns: 1}, nil
	}
	if strings.Contains(query, "FROM connection_profiles WHERE id") {
		row, ok := c.state.profiles[args[0].Value.(string)]
		if !ok {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 14}, nil
	}
	if strings.Contains(query, "SELECT COUNT(*) FROM migrations") {
		userID := args[0].Value.(string)
		c.state.mu.Lock()
		count := c.state.activeCountByUser[userID]
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{int64(count)}}, columns: 1}, nil
	}
	if strings.Contains(query, "SELECT COUNT(*), COALESCE(SUM(file_size), 0)") && strings.Contains(query, "FROM tasks") {
		id := args[0].Value.(string)
		c.state.mu.Lock()
		pair := c.state.failedTaskCounts[id]
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{pair[0], pair[1]}}, columns: 2}, nil
	}
	if strings.Contains(query, "RETURNING NOT discovery_complete") && strings.Contains(query, "UPDATE migrations") {
		id := args[0].Value.(string)
		c.state.mu.Lock()
		rec := c.state.migrations[id]
		if rec == nil || (rec.status != "PAUSED" && rec.status != "PAUSED_CONNECTION_LOSS") {
			c.state.mu.Unlock()
			return &backupProfileConnectionRows{columns: 1}, nil
		}
		needsDiscovery := !rec.discoveryComplete
		if rec.discoveryComplete {
			rec.status = "RUNNING"
		} else {
			rec.status = "INDEXING"
		}
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{needsDiscovery}}, columns: 1}, nil
	}
	if strings.Contains(query, "FROM migrations") && strings.Contains(query, "WHERE user_id = $1") && strings.Contains(query, "ORDER BY created_at DESC") {
		userID := args[0].Value.(string)
		c.state.mu.Lock()
		var rows [][]driver.Value
		for _, m := range c.state.migrations {
			if m.userID != userID {
				continue
			}
			var errMsg interface{}
			if m.errorMessage.Valid {
				errMsg = m.errorMessage.String
			}
			rows = append(rows, []driver.Value{
				m.id, m.userID, m.sourceProfileID, m.targetProfileID, m.sourceURL, "", m.sourceProvider,
				m.targetURL, "", m.targetProvider, m.status,
				"SKIP", int64(0), int64(0), int64(0),
				int64(0), int64(0), int64(0), int64(0), int64(0), errMsg,
				m.createdAt, m.updatedAt, m.targetDir, m.threads,
			})
		}
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: rows, columns: 25}, nil
	}
	if strings.Contains(query, "FROM tasks") && strings.Contains(query, "status = 'RUNNING'") {
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"/docs/file.txt", []byte(`{}`)}}, columns: 2}, nil
	}
	if strings.Contains(query, "FROM tasks") && strings.Contains(query, "GROUP BY resource_type, status") {
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"files", "COMPLETED", int64(2)}}, columns: 3}, nil
	}
	if strings.Contains(query, "FROM tasks") && strings.Contains(query, "status = 'FAILED'") && strings.Contains(query, "resolved_target_path") {
		now := time.Now().UTC()
		return &backupProfileConnectionRows{
			rows: [][]driver.Value{{
				"t-1", args[0].Value.(string), "files", "/fail.txt", "/dest/fail.txt", int64(42), "FAILED", int64(2), "forbidden", []byte(`{}`), now, now,
			}},
			columns: 12,
		}, nil
	}
	if strings.Contains(query, "FROM indexing_errors") && strings.Contains(query, "ORDER BY path ASC") {
		now := time.Now().UTC()
		return &backupProfileConnectionRows{
			rows: [][]driver.Value{{
				int64(1), args[0].Value.(string), "files", "/bad-dir", "permission denied", now,
			}},
			columns: 6,
		}, nil
	}
	if strings.Contains(query, "WITH errors AS MATERIALIZED") {
		now := time.Now().UTC()
		return &backupProfileConnectionRows{
			rows: [][]driver.Value{{
				"t-1", "transfer", "files", "/fail.txt", "FAILED", int64(2), "failed https://alice:super-secret-token@src.example.test?access_token=super-secret-token", []byte(`{}`), now, int64(1),
			}},
			columns: 10,
		}, nil
	}
	if strings.Contains(query, "FROM migrations WHERE id = $1") {
		id := args[0].Value.(string)
		c.state.mu.Lock()
		m := c.state.migrations[id]
		c.state.mu.Unlock()
		if m == nil {
			return &backupProfileConnectionRows{columns: 43}, nil
		}
		row := make([]driver.Value, 43)
		for _, idx := range []int{4, 5, 6, 7, 10, 11, 12, 13, 14, 15, 18, 19, 36, 39} {
			row[idx] = ""
		}
		row[0] = m.id
		row[1] = m.userID
		row[2] = m.sourceProfileID
		row[3] = m.targetProfileID
		row[4] = m.sourceURL
		row[6] = ""
		row[7] = m.sourceProvider
		row[8] = m.sourceRefreshTokenEnc
		row[12] = m.targetURL
		row[15] = m.targetProvider
		row[16] = m.targetRefreshTokenEnc
		row[20] = m.status
		row[21] = "SKIP"
		for _, idx := range []int{22, 23, 24, 25, 26, 27, 28, 29, 32, 37, 38} {
			row[idx] = int64(0)
		}
		row[31] = m.discoveryComplete
		row[34] = m.createdAt
		row[35] = m.updatedAt
		row[36] = m.targetDir
		row[37] = m.threads
		row[38] = m.bandwidthLimitMbps
		if m.errorMessage.Valid {
			row[30] = m.errorMessage.String
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 43}, nil
	}
	if strings.Contains(query, "INSERT INTO migrations") {
		c.state.mu.Lock()
		c.state.nextID++
		id := "mig-" + strconv.Itoa(c.state.nextID)
		now := time.Now().UTC()
		userID, _ := args[0].Value.(string)
		srcID, _ := args[1].Value.(string)
		tgtID, _ := args[2].Value.(string)
		srcURL, _ := args[3].Value.(string)
		srcProv, _ := args[6].Value.(string)
		tgtURL, _ := args[11].Value.(string)
		tgtProv, _ := args[14].Value.(string)
		status, _ := args[19].Value.(string)
		tgtDir, _ := args[21].Value.(string)
		threads, _ := args[22].Value.(int64)
		bw, _ := args[23].Value.(int64)
		c.state.migrations[id] = &migrationModuleRecord{
			id:                 id,
			userID:             userID,
			sourceProfileID:    srcID,
			targetProfileID:    tgtID,
			sourceURL:          srcURL,
			sourceProvider:     srcProv,
			targetURL:          tgtURL,
			targetProvider:     tgtProv,
			status:             status,
			targetDir:          tgtDir,
			threads:            threads,
			bandwidthLimitMbps: bw,
			createdAt:          now,
			updatedAt:          now,
		}
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id, now, now}}, columns: 3}, nil
	}
	if strings.Contains(query, "INSERT INTO schedules") {
		c.state.mu.Lock()
		c.state.schedules++
		c.state.mu.Unlock()
		now := time.Now().UTC()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{"sched-1", now, now}}, columns: 3}, nil
	}
	return nil, driver.ErrSkip
}

type migrationModuleTestTx struct{ state *migrationModuleDBState }

func (tx migrationModuleTestTx) Commit() error   { return nil }
func (tx migrationModuleTestTx) Rollback() error { return nil }
func (tx migrationModuleTestTx) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return migrationModuleTestConn{state: tx.state}.ExecContext(ctx, query, args)
}
func (tx migrationModuleTestTx) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return migrationModuleTestConn{state: tx.state}.QueryContext(ctx, query, args)
}
