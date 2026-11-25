package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/db"
)

type recordedAudit struct {
	action  db.AuditAction
	target  string
	actor   string
	details map[string]interface{}
}

type adminTestHarness struct {
	t        *testing.T
	database *sql.DB
	api      *adminAPI
	audits   []recordedAudit
	cancels  []string
	// cancelErr maps a sync job ID to the error its cancellation returns.
	cancelErr map[string]error
}

func newAdminTestHarness(t *testing.T) *adminTestHarness {
	t.Helper()
	database := setupChangePassword2FATestDB(t)
	t.Cleanup(func() { database.Close() })
	h := &adminTestHarness{t: t, database: database, cancelErr: map[string]error{}}
	h.api = newAdminAPI(adminDependencies{
		db:            database,
		encryptionKey: testGatewayEncryptionKey,
		cancelSyncJob: func(_ context.Context, id string) error {
			h.cancels = append(h.cancels, id)
			return h.cancelErr[id]
		},
		requestEnv: requestEnv{
			writeAudit: func(_ *http.Request, action db.AuditAction, target, actor string, details map[string]interface{}) {
				h.audits = append(h.audits, recordedAudit{action, target, actor, details})
			},
		},
	})
	return h
}

func (h *adminTestHarness) insertUser(role string, active bool) string {
	h.t.Helper()
	var id string
	err := h.database.QueryRow(
		`INSERT INTO users (email, password_hash, display_name, role, active)
		 VALUES ($1, 'unused-hash', 'Test', $2, $3) RETURNING id`,
		"admin-mod-"+generateRandomString(8)+"@example.test", role, active,
	).Scan(&id)
	if err != nil {
		h.t.Fatalf("insert %s user: %v", role, err)
	}
	return id
}

func (h *adminTestHarness) insertRunningSyncJob(userID string) string {
	h.t.Helper()
	var id string
	err := h.database.QueryRow(
		`INSERT INTO sync_jobs (user_id, source_url, source_username, source_password_encrypted,
		                        target_url, target_username, target_password_encrypted, status)
		 VALUES ($1, 'https://src.example.test', 'u', 'x', 'https://dst.example.test', 'u', 'x', 'RUNNING')
		 RETURNING id`, userID,
	).Scan(&id)
	if err != nil {
		h.t.Fatalf("insert sync job: %v", err)
	}
	return id
}

func (h *adminTestHarness) userActive(id string) bool {
	h.t.Helper()
	var active bool
	if err := h.database.QueryRow(`SELECT active FROM users WHERE id = $1`, id).Scan(&active); err != nil {
		h.t.Fatalf("select user active: %v", err)
	}
	return active
}

// do invokes handler as the given actor (role ADMIN unless noted) and returns
// the recorded response.
func (h *adminTestHarness) do(handler http.HandlerFunc, method, path, body, actor, role string, pathValues map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{UserID: actor, Role: role}))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func (h *adminTestHarness) decode(rec *httptest.ResponseRecorder) map[string]any {
	h.t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		h.t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func (h *adminTestHarness) requireSingleAudit(want recordedAudit) {
	h.t.Helper()
	if len(h.audits) != 1 {
		h.t.Fatalf("audit entries = %+v, want exactly one", h.audits)
	}
	if !reflect.DeepEqual(h.audits[0], want) {
		h.t.Fatalf("audit entry = %+v, want %+v", h.audits[0], want)
	}
}

func TestAdminSuspendUserCancelsSyncJobsAndAuditsSuspension(t *testing.T) {
	h := newAdminTestHarness(t)
	actor := h.insertUser("ADMIN", true)
	target := h.insertUser("USER", true)
	jobID := h.insertRunningSyncJob(target)

	rec := h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/x/suspend", "", actor, "ADMIN", map[string]string{"id": target})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := h.decode(rec)
	if body["success"] != true || body["active"] != false {
		t.Fatalf("body = %v, want success=true active=false", body)
	}
	if _, partial := body["partial"]; partial {
		t.Fatalf("body = %v, must not be partial when every cancellation succeeds", body)
	}
	if !reflect.DeepEqual(h.cancels, []string{jobID}) {
		t.Fatalf("cancelled sync jobs = %v, want [%s]", h.cancels, jobID)
	}
	if h.userActive(target) {
		t.Fatal("suspended user is still active")
	}
	h.requireSingleAudit(recordedAudit{action: db.AuditUserSuspended, target: target, actor: actor})
}

func TestAdminSuspendUserReportsPartialWhenSyncCancelEventFails(t *testing.T) {
	h := newAdminTestHarness(t)
	actor := h.insertUser("ADMIN", true)
	target := h.insertUser("USER", true)
	failing := h.insertRunningSyncJob(target)
	succeeding := h.insertRunningSyncJob(target)
	h.cancelErr[failing] = errors.New("redis unavailable")

	rec := h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/x/suspend", "", actor, "ADMIN", map[string]string{"id": target})

	// The suspension is committed even though a cancel event could not be published.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := h.decode(rec)
	if body["success"] != true || body["active"] != false || body["partial"] != true {
		t.Fatalf("body = %v, want success=true active=false partial=true", body)
	}
	if len(h.cancels) != 2 {
		t.Fatalf("cancel attempts = %v, want both sync jobs attempted (%s, %s)", h.cancels, failing, succeeding)
	}
	if h.userActive(target) {
		t.Fatal("suspended user is still active")
	}
	h.requireSingleAudit(recordedAudit{
		action:  db.AuditUserSuspended,
		target:  target,
		actor:   actor,
		details: map[string]interface{}{"sync_cancel_event_failures": []string{failing}},
	})
}

func TestAdminSuspendUserRejectionsWriteNoAuditAndCancelNothing(t *testing.T) {
	h := newAdminTestHarness(t)
	admin := h.insertUser("ADMIN", true)
	// A second active admin exists, so only the self-modification guard applies to admin.
	h.insertUser("ADMIN", true)
	lastAdminTarget := h.insertUser("USER", true)
	_ = lastAdminTarget

	rec := h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/x/suspend", "", admin, "ADMIN", map[string]string{"id": admin})
	assertAPIResponseError(t, rec, http.StatusBadRequest, ErrCannotModifySelf, "suspend self")

	rec = h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/suspend", "", admin, "ADMIN", nil)
	assertAPIResponseError(t, rec, http.StatusBadRequest, ErrUserNotFound, "suspend without id")

	rec = h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/x/suspend", "", admin, "USER", map[string]string{"id": lastAdminTarget})
	assertAPIResponseError(t, rec, http.StatusForbidden, ErrAdminOnly, "suspend as non-admin")

	if len(h.audits) != 0 || len(h.cancels) != 0 {
		t.Fatalf("rejected requests produced audits=%+v cancels=%v, want none", h.audits, h.cancels)
	}
}

func TestAdminSuspendLastActiveAdminIsRejectedWithoutAudit(t *testing.T) {
	h := newAdminTestHarness(t)
	// The actor is a different (already suspended) admin identity, so the only
	// active admin is the target and the last-admin guard must refuse.
	actor := h.insertUser("ADMIN", false)
	onlyActiveAdmin := h.insertUser("ADMIN", true)

	rec := h.do(h.api.handleAdminSuspendUser, http.MethodPost, "/api/admin/users/x/suspend", "", actor, "ADMIN", map[string]string{"id": onlyActiveAdmin})

	assertAPIResponseError(t, rec, http.StatusConflict, ErrLastAdmin, "suspend last active admin")
	if len(h.audits) != 0 || len(h.cancels) != 0 {
		t.Fatalf("rejected request produced audits=%+v cancels=%v, want none", h.audits, h.cancels)
	}
	if !h.userActive(onlyActiveAdmin) {
		t.Fatal("last active admin was suspended")
	}
}

func TestAdminMutationsWriteExpectedAuditEntries(t *testing.T) {
	h := newAdminTestHarness(t)
	actor := h.insertUser("ADMIN", true)

	t.Run("create user", func(t *testing.T) {
		h.audits = nil
		email := "created-" + generateRandomString(6) + "@example.test"
		rec := h.do(h.api.handleAdminCreateUser, http.MethodPost, "/api/admin/users",
			`{"email":"`+strings.ToUpper(email)+`","password":"a-long-enough-password","display_name":"Created","role":"ADMIN"}`,
			actor, "ADMIN", nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		id, _ := h.decode(rec)["id"].(string)
		if id == "" {
			t.Fatalf("created user response has no id: %s", rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditUserCreated,
			target:  id,
			actor:   actor,
			details: map[string]interface{}{"email": email, "role": "ADMIN"},
		})
	})

	t.Run("update role", func(t *testing.T) {
		h.audits = nil
		target := h.insertUser("USER", true)
		rec := h.do(h.api.handleAdminUpdateRole, http.MethodPut, "/api/admin/users/x/role", `{"role":"ADMIN"}`, actor, "ADMIN", map[string]string{"id": target})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditUserRoleChanged,
			target:  target,
			actor:   actor,
			details: map[string]interface{}{"from": "USER", "to": "ADMIN"},
		})
	})

	t.Run("update role no-op writes no audit", func(t *testing.T) {
		h.audits = nil
		target := h.insertUser("USER", true)
		rec := h.do(h.api.handleAdminUpdateRole, http.MethodPut, "/api/admin/users/x/role", `{"role":"USER"}`, actor, "ADMIN", map[string]string{"id": target})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if len(h.audits) != 0 {
			t.Fatalf("audits = %+v, want none for an unchanged role", h.audits)
		}
	})

	t.Run("reactivate user", func(t *testing.T) {
		h.audits = nil
		target := h.insertUser("USER", false)
		rec := h.do(h.api.handleAdminReactivateUser, http.MethodPost, "/api/admin/users/x/reactivate", "", actor, "ADMIN", map[string]string{"id": target})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if !h.userActive(target) {
			t.Fatal("reactivated user is not active")
		}
		h.requireSingleAudit(recordedAudit{action: db.AuditUserReactivated, target: target, actor: actor})
	})

	t.Run("delete user", func(t *testing.T) {
		h.audits = nil
		target := h.insertUser("USER", true)
		rec := h.do(h.api.handleAdminDeleteUser, http.MethodDelete, "/api/admin/users/x", "", actor, "ADMIN", map[string]string{"id": target})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{action: db.AuditUserDeleted, target: target, actor: actor})
	})

	t.Run("update instance setting", func(t *testing.T) {
		h.audits = nil
		rec := h.do(h.api.handleUpdateSetting, http.MethodPut, "/api/settings", `{"key":"registrations_enabled","value":"true"}`, actor, "ADMIN", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditSettingUpdated,
			target:  "registrations_enabled",
			actor:   actor,
			details: map[string]interface{}{"value": "true"},
		})
	})

	t.Run("rejected setting writes no audit", func(t *testing.T) {
		h.audits = nil
		rec := h.do(h.api.handleUpdateSetting, http.MethodPut, "/api/settings", `{"key":"other","value":"true"}`, actor, "ADMIN", nil)
		assertAPIResponseError(t, rec, http.StatusForbidden, ErrSettingForbidden, "unknown key")
		rec = h.do(h.api.handleUpdateSetting, http.MethodPut, "/api/settings", `{"key":"registrations_enabled","value":"maybe"}`, actor, "ADMIN", nil)
		assertAPIResponseError(t, rec, http.StatusBadRequest, ErrSettingInvalid, "invalid value")
		if len(h.audits) != 0 {
			t.Fatalf("audits = %+v, want none for rejected updates", h.audits)
		}
	})

	t.Run("instance SMTP delete", func(t *testing.T) {
		h.audits = nil
		rec := h.do(h.api.handleAdminDeleteSMTP, http.MethodDelete, "/api/admin/settings/smtp", "", actor, "ADMIN", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditSettingUpdated,
			target:  "instance-smtp",
			actor:   actor,
			details: map[string]interface{}{"action": "delete"},
		})
	})

	t.Run("instance OAuth put and delete", func(t *testing.T) {
		h.audits = nil
		rec := h.do(h.api.handleAdminPutOAuth, http.MethodPut, "/api/admin/settings/oauth/dropbox", `{"client_id":"id","client_secret":"secret"}`, actor, "ADMIN", map[string]string{"provider": "dropbox"})
		if rec.Code != http.StatusOK {
			t.Fatalf("put status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditSettingUpdated,
			target:  "instance-oauth-dropbox",
			actor:   actor,
			details: map[string]interface{}{},
		})

		h.audits = nil
		rec = h.do(h.api.handleAdminDeleteOAuth, http.MethodDelete, "/api/admin/settings/oauth/dropbox", "", actor, "ADMIN", map[string]string{"provider": "dropbox"})
		if rec.Code != http.StatusOK {
			t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
		}
		h.requireSingleAudit(recordedAudit{
			action:  db.AuditSettingUpdated,
			target:  "instance-oauth-dropbox",
			actor:   actor,
			details: map[string]interface{}{"action": "delete"},
		})
	})

	t.Run("instance OAuth put get and delete for box", func(t *testing.T) {
		h.audits = nil
		rec := h.do(h.api.handleAdminPutOAuth, http.MethodPut, "/api/admin/settings/oauth/box", `{"client_id":"box-id","client_secret":"box-secret"}`, actor, "ADMIN", map[string]string{"provider": "box"})
		if rec.Code != http.StatusOK {
			t.Fatalf("put status = %d, body=%s", rec.Code, rec.Body.String())
		}

		stored, err := db.NewOAuthProviderStore(h.database).GetInstanceOAuthProvider("box")
		if err != nil {
			t.Fatalf("GetInstanceOAuthProvider(box): %v", err)
		}
		if stored.ClientSecretEnc == "" || stored.ClientSecretEnc == "box-secret" {
			t.Fatalf("Box client secret was not encrypted in DB: %q", stored.ClientSecretEnc)
		}

		rec = h.do(h.api.handleAdminGetOAuth, http.MethodGet, "/api/admin/settings/oauth", "", actor, "ADMIN", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Providers []instanceOAuthProviderView `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var foundBox bool
		for _, p := range body.Providers {
			if p.Provider == "box" {
				foundBox = true
				if !p.Configured || p.ClientID != "box-id" || !p.ClientSecretSet {
					t.Fatalf("unexpected box OAuth view: %+v", p)
				}
			}
		}
		if !foundBox {
			t.Fatalf("box missing from GET /api/admin/settings/oauth providers: %+v", body.Providers)
		}

		rec = h.do(h.api.handleAdminDeleteOAuth, http.MethodDelete, "/api/admin/settings/oauth/box", "", actor, "ADMIN", map[string]string{"provider": "box"})
		if rec.Code != http.StatusOK {
			t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
}

// TestGatewayAdministrationPersistsAuditEntries verifies the production wiring:
// the module built from APIServer writes audit rows through the real audit log,
// attributed to the acting administrator.
func TestGatewayAdministrationPersistsAuditEntries(t *testing.T) {
	database := setupChangePassword2FATestDB(t)
	defer database.Close()
	server := &APIServer{db: database, encryptionKey: testGatewayEncryptionKey, jwtSecret: testGatewayJWTSecret}
	h := &adminTestHarness{t: t, database: database}

	actor := h.insertUser("ADMIN", true)
	rec := h.do(server.administration().handleUpdateSetting, http.MethodPut, "/api/settings", `{"key":"registrations_enabled","value":"false"}`, actor, "ADMIN", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// WriteAuditLog persists asynchronously, so wait for the row to appear.
	var entries []db.AuditLogRow
	auditStore := db.NewAuditStore(database)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		entries, _, err = auditStore.ListAuditLog(db.AuditLogParams{Page: 1, Limit: 10, UserID: actor})
		if err != nil {
			t.Fatalf("ListAuditLog: %v", err)
		}
		if len(entries) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(entries) != 1 {
		t.Fatalf("audit log entries = %d, want 1", len(entries))
	}
	if entries[0].Action != db.AuditSettingUpdated || entries[0].Target != "registrations_enabled" {
		t.Fatalf("audit entry = %+v, want %s on registrations_enabled", entries[0], db.AuditSettingUpdated)
	}
}

func TestNewAdminAPIDefaultCancelSyncJobFailsInsteadOfSilentlySucceeding(t *testing.T) {
	api := newAdminAPI(adminDependencies{})
	if err := api.cancelSyncJob(context.Background(), "job"); err == nil {
		t.Fatal("default cancelSyncJob must report failure so suspension surfaces a partial result")
	}
}
