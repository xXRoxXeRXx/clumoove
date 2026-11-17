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
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
)

// These tests drive requests through server.Handler() with the real module
// handlers behind it. They complement TestGatewayRouteContracts, which probes
// the composed mux with a stand-in terminal handler: here the assertion is on
// the status code and the typed error_code or JSON shape the client receives,
// for one representative route per endpoint family, covering both success and
// an access-control rejection.

const (
	e2eOwner    = "user-1"
	e2eOther    = "user-2"
	e2eAdmin    = "admin-1"
	e2eNoAuth   = ""
	e2eNoBody   = ""
	e2eSecretPW = "super-secret-password"
)

type e2eGateway struct {
	t       *testing.T
	handler http.Handler
	tokens  map[string]string
}

// newE2EGateway composes a gateway whose auth-state lookup knows an owner, a
// second regular user and an administrator, and mints a valid access token for
// each. The database is whatever fake the family under test needs.
func newE2EGateway(t *testing.T, database *sql.DB) *e2eGateway {
	t.Helper()
	users := map[string]*db.UserAuthState{
		e2eOwner: {Active: true, Role: "USER"},
		e2eOther: {Active: true, Role: "USER"},
		e2eAdmin: {Active: true, Role: "ADMIN"},
	}
	server := newTestAPIGateway(t, users)
	server.rateLimiter = allowAllFileRateLimiter{}
	server.db = database

	tokens := map[string]string{}
	for id, state := range users {
		token, err := auth.GenerateAccessToken(&db.User{ID: id, Email: id + "@example.test", Role: state.Role, Active: true}, testGatewayJWTSecret)
		if err != nil {
			t.Fatalf("GenerateAccessToken(%s): %v", id, err)
		}
		tokens[id] = token
	}
	return &e2eGateway{t: t, handler: server.Handler(), tokens: tokens}
}

// do sends one request through the full gateway as the given user ("" sends it
// unauthenticated).
func (g *e2eGateway) do(method, target, user, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+g.tokens[user])
	}
	rec := httptest.NewRecorder()
	g.handler.ServeHTTP(rec, req)
	return rec
}

// expectJSON asserts a 200 response and returns its decoded JSON object.
func (g *e2eGateway) expectJSON(rec *httptest.ResponseRecorder, context string) map[string]any {
	g.t.Helper()
	if rec.Code != http.StatusOK {
		g.t.Fatalf("%s: status = %d, want 200 (body=%s)", context, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		g.t.Fatalf("%s: Content-Type = %q, want application/json", context, ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		g.t.Fatalf("%s: response is not a JSON object: %v (body=%s)", context, err, rec.Body.String())
	}
	return body
}

// expectJSONList asserts a 200 response and returns its decoded JSON array.
func (g *e2eGateway) expectJSONList(rec *httptest.ResponseRecorder, context string) []any {
	g.t.Helper()
	if rec.Code != http.StatusOK {
		g.t.Fatalf("%s: status = %d, want 200 (body=%s)", context, rec.Code, rec.Body.String())
	}
	var body []any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		g.t.Fatalf("%s: response is not a JSON array: %v (body=%s)", context, err, rec.Body.String())
	}
	return body
}

func requireE2EField(t *testing.T, body map[string]any, key string, want any, context string) {
	t.Helper()
	if got := body[key]; got != want {
		t.Fatalf("%s: %q = %v, want %v (body=%v)", context, key, got, want, body)
	}
}

// --- accounts ---

func TestGatewayEndToEndAccounts(t *testing.T) {
	gw := newE2EGateway(t, newE2EScriptedDB(t, e2eScript{
		{match: "FROM users WHERE id = $1", columns: e2eUserColumns, rows: [][]driver.Value{e2eUserRow(e2eOwner, "USER")}},
	}))

	t.Run("owner reads own account", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/auth/me", e2eOwner, e2eNoBody), "GET /api/auth/me")
		requireE2EField(t, body, "id", e2eOwner, "GET /api/auth/me")
		requireE2EField(t, body, "role", "USER", "GET /api/auth/me")
		requireE2EField(t, body, "language", "en", "GET /api/auth/me")
		for _, secret := range []string{"password_hash", "totp_secret", e2eSecretPW} {
			if strings.Contains(strings.ToLower(gwBody(body)), secret) {
				t.Fatalf("GET /api/auth/me leaks %q: %v", secret, body)
			}
		}
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/auth/me", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/auth/me anonymous")
	})
}

func gwBody(body map[string]any) string {
	raw, _ := json.Marshal(body)
	return string(raw)
}

// --- notifications ---

func TestGatewayEndToEndNotifications(t *testing.T) {
	const key = testGatewayEncryptionKey
	ntfyConfig, err := crypto.EncryptWithDomain(`{"url":"https://ntfy.example.test/topic"}`, key, crypto.DomainNotificationConfig)
	if err != nil {
		t.Fatalf("encrypt notification config: %v", err)
	}
	gw := newE2EGateway(t, newE2EScriptedDB(t, e2eScript{
		{match: "FROM notification_channels WHERE user_id = $1", columns: []string{"type", "enabled", "config_encrypted"},
			rows: [][]driver.Value{{"ntfy", true, ntfyConfig}}},
		{match: "FROM instance_smtp_settings", columns: []string{"exists"}, rows: [][]driver.Value{{false}}},
	}))

	t.Run("owner lists configured channels", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/settings/notifications", e2eOwner, e2eNoBody), "GET /api/settings/notifications")
		channels, ok := body["channels"].([]any)
		if !ok || len(channels) != 1 {
			t.Fatalf("channels = %v, want exactly one ntfy channel (body=%v)", body["channels"], body)
		}
		channel := channels[0].(map[string]any)
		requireE2EField(t, channel, "type", "ntfy", "notification channel")
		requireE2EField(t, channel, "enabled", true, "notification channel")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/settings/notifications", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/settings/notifications anonymous")
	})
}

// --- administration ---

func TestGatewayEndToEndAdministration(t *testing.T) {
	gw := newE2EGateway(t, newE2EScriptedDB(t, e2eScript{
		{match: "FROM users", columns: []string{"category", "status", "count"}, rows: [][]driver.Value{
			{"users", "total", int64(3)},
			{"users", "active", int64(2)},
			{"migrations", "COMPLETED", int64(5)},
		}},
	}))

	t.Run("administrator reads global statistics", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/admin/stats", e2eAdmin, e2eNoBody), "GET /api/admin/stats")
		if body["total_users"] != float64(3) || body["active_users"] != float64(2) {
			t.Fatalf("stats users = %v/%v, want 3/2 (body=%v)", body["total_users"], body["active_users"], body)
		}
	})

	t.Run("regular user is rejected as admin only", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/admin/stats", e2eOwner, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusForbidden, ErrAdminOnly, "GET /api/admin/stats as USER")
	})

	t.Run("anonymous request is rejected as unauthorized", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/admin/stats", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/admin/stats anonymous")
	})
}

// --- schedules ---

func TestGatewayEndToEndSchedules(t *testing.T) {
	state := &scheduleModuleState{schedules: map[string][]driver.Value{
		"sch-1": scheduleRow("sch-1", e2eOwner, "SYNC"),
	}}
	gw := newE2EGateway(t, newScheduleModuleTestDB(t, state))

	t.Run("owner reads own schedule", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/schedule/sch-1", e2eOwner, e2eNoBody), "GET /api/schedule/sch-1")
		requireE2EField(t, body, "id", "sch-1", "GET /api/schedule/sch-1")
		requireE2EField(t, body, "task_type", "SYNC", "GET /api/schedule/sch-1")
	})

	t.Run("owner lists schedules", func(t *testing.T) {
		list := gw.expectJSONList(gw.do(http.MethodGet, "/api/schedule", e2eOwner, e2eNoBody), "GET /api/schedule")
		if len(list) != 1 {
			t.Fatalf("schedules = %d, want 1 (%v)", len(list), list)
		}
	})

	t.Run("another user's schedule is indistinguishable from a missing one", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/schedule/sch-1", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusNotFound, ErrScheduleNotFound, "GET /api/schedule/sch-1 as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/schedule/sch-1", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/schedule/sch-1 anonymous")
	})
}

// --- profiles ---

func TestGatewayEndToEndProfiles(t *testing.T) {
	password, err := crypto.EncryptWithDomain(e2eSecretPW, testGatewayEncryptionKey, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt profile password: %v", err)
	}
	profile := profileTestRow("p-1", e2eOwner, "nextcloud", "https://nc.example.test", "alice", password)
	gw := newE2EGateway(t, newE2EScriptedDB(t, e2eScript{
		// Ownership lookup: args are (profile id, user id).
		{match: "SELECT EXISTS", columns: []string{"exists"}, respond: func(args []driver.NamedValue) [][]driver.Value {
			return [][]driver.Value{{args[0].Value == "p-1" && args[1].Value == e2eOwner}}
		}},
		{match: "FROM connection_profiles WHERE id", columns: e2eProfileColumns, rows: [][]driver.Value{profile}},
	}))

	t.Run("owner reads own profile without credential material", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/profiles/p-1", e2eOwner, e2eNoBody)
		body := gw.expectJSON(rec, "GET /api/profiles/p-1")
		if strings.Contains(rec.Body.String(), e2eSecretPW) || strings.Contains(rec.Body.String(), password) {
			t.Fatalf("profile response leaks credentials: %s", rec.Body.String())
		}
		if body["success"] != true {
			t.Fatalf("success = %v, want true (body=%v)", body["success"], body)
		}
	})

	t.Run("another user's profile is indistinguishable from a missing one", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/profiles/p-1", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusNotFound, ErrProfileNotFound, "GET /api/profiles/p-1 as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/profiles/p-1", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/profiles/p-1 anonymous")
	})
}

// --- files ---

func TestGatewayEndToEndFiles(t *testing.T) {
	database, _ := newFileModuleTestDB(t, map[string][]driver.Value{
		"p-1": profileTestRow("p-1", e2eOwner, "nextcloud", "https://nc.example.test", "alice", ""),
	})
	gw := newE2EGateway(t, database)

	t.Run("owner reads profile capabilities", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/files/profiles/p-1/capabilities", e2eOwner, e2eNoBody), "GET capabilities")
		requireE2EField(t, body, "success", true, "GET capabilities")
		if _, ok := body["capabilities"].(map[string]any); !ok {
			t.Fatalf("capabilities = %v, want an object (body=%v)", body["capabilities"], body)
		}
	})

	t.Run("another user's profile is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/files/profiles/p-1/capabilities", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusNotFound, ErrProfileNotFound, "GET capabilities as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/files/profiles/p-1/capabilities", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET capabilities anonymous")
	})
}

// --- migrations ---

func TestGatewayEndToEndMigrations(t *testing.T) {
	database, state := newMigrationModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setMigration(&migrationModuleRecord{id: "m-1", userID: e2eOwner, status: "COMPLETED", createdAt: now, updatedAt: now})
	gw := newE2EGateway(t, database)

	t.Run("owner reads own migration", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/migration/m-1", e2eOwner, e2eNoBody), "GET /api/migration/m-1")
		requireE2EField(t, body, "id", "m-1", "GET /api/migration/m-1")
		requireE2EField(t, body, "status", "COMPLETED", "GET /api/migration/m-1")
	})

	t.Run("owner lists migrations", func(t *testing.T) {
		list := gw.expectJSONList(gw.do(http.MethodGet, "/api/migration", e2eOwner, e2eNoBody), "GET /api/migration")
		if len(list) != 1 {
			t.Fatalf("migrations = %d, want 1 (%v)", len(list), list)
		}
	})

	t.Run("another user's migration is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/migration/m-1", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusForbidden, ErrMigrationNotOwned, "GET /api/migration/m-1 as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/migration/m-1", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/migration/m-1 anonymous")
	})
}

// --- syncs ---

func TestGatewayEndToEndSyncs(t *testing.T) {
	database, state := newSyncModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&syncModuleRecord{id: "s-1", userID: e2eOwner, status: "IDLE", createdAt: now, updatedAt: now})
	gw := newE2EGateway(t, database)

	t.Run("owner reads own sync", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/sync/s-1", e2eOwner, e2eNoBody), "GET /api/sync/s-1")
		requireE2EField(t, body, "id", "s-1", "GET /api/sync/s-1")
		requireE2EField(t, body, "status", "IDLE", "GET /api/sync/s-1")
	})

	t.Run("owner lists syncs", func(t *testing.T) {
		list := gw.expectJSONList(gw.do(http.MethodGet, "/api/sync", e2eOwner, e2eNoBody), "GET /api/sync")
		if len(list) != 1 {
			t.Fatalf("syncs = %d, want 1 (%v)", len(list), list)
		}
	})

	t.Run("another user's sync is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/sync/s-1", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusNotFound, ErrSyncNotFound, "GET /api/sync/s-1 as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/sync/s-1", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/sync/s-1 anonymous")
	})
}

// --- backups ---

func TestGatewayEndToEndBackups(t *testing.T) {
	database, state := newBackupModuleTestDB(t, nil)
	now := time.Now().UTC()
	state.setJob(&backupModuleJobRecord{id: "b-1", userID: e2eOwner, status: "IDLE", deletionState: "ACTIVE", createdAt: now, updatedAt: now})
	gw := newE2EGateway(t, database)

	t.Run("owner reads own backup", func(t *testing.T) {
		body := gw.expectJSON(gw.do(http.MethodGet, "/api/backup/b-1", e2eOwner, e2eNoBody), "GET /api/backup/b-1")
		requireE2EField(t, body, "id", "b-1", "GET /api/backup/b-1")
		requireE2EField(t, body, "status", "IDLE", "GET /api/backup/b-1")
	})

	t.Run("owner lists backups", func(t *testing.T) {
		list := gw.expectJSONList(gw.do(http.MethodGet, "/api/backup", e2eOwner, e2eNoBody), "GET /api/backup")
		if len(list) != 1 {
			t.Fatalf("backups = %d, want 1 (%v)", len(list), list)
		}
	})

	t.Run("another user's backup is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/backup/b-1", e2eOther, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusNotFound, ErrBackupNotFound, "GET /api/backup/b-1 as other user")
	})

	t.Run("anonymous request is rejected with a typed code", func(t *testing.T) {
		rec := gw.do(http.MethodGet, "/api/backup/b-1", e2eNoAuth, e2eNoBody)
		assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, "GET /api/backup/b-1 anonymous")
	})
}

// --- scripted fake SQL driver ---

// e2eScript answers queries by substring. The first rule whose match is
// contained in the query text wins; an unmatched query is an error so a
// handler reaching for data the test did not script fails loudly.
type e2eScript []e2eRule

type e2eRule struct {
	match   string
	columns []string
	rows    [][]driver.Value
	// respond, when set, computes the rows from the query arguments instead.
	respond func(args []driver.NamedValue) [][]driver.Value
}

var e2eDriverID atomic.Uint64

func newE2EScriptedDB(t *testing.T, script e2eScript) *sql.DB {
	t.Helper()
	name := "gateway-e2e-test-" + strconv.FormatUint(e2eDriverID.Add(1), 10)
	sql.Register(name, e2eDriver{script: script})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

type e2eDriver struct{ script e2eScript }

func (d e2eDriver) Open(string) (driver.Conn, error) { return e2eConn{script: d.script}, nil }

type e2eConn struct{ script e2eScript }

func (e2eConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (e2eConn) Close() error                        { return nil }
func (e2eConn) Begin() (driver.Tx, error)           { return nil, errors.New("unsupported") }

func (c e2eConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	for _, rule := range c.script {
		if strings.Contains(query, rule.match) {
			rows := rule.rows
			if rule.respond != nil {
				rows = rule.respond(args)
			}
			return &e2eRows{columns: rule.columns, rows: rows}, nil
		}
	}
	return nil, errors.New("unscripted query: " + query)
}

func (e2eConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("unscripted exec: " + query)
}

type e2eRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *e2eRows) Columns() []string { return r.columns }
func (r *e2eRows) Close() error      { return nil }
func (r *e2eRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

var e2eProfileColumns = []string{
	"id", "user_id", "name", "provider", "url", "username", "password_encrypted", "refresh_token_encrypted",
	"token_expires_at", "oauth_user", "mega_session_id_encrypted", "mega_master_key_encrypted", "created_at", "updated_at",
}

var e2eUserColumns = []string{
	"id", "email", "password_hash", "display_name", "language", "role", "active", "must_change_password",
	"avatar", "avatar_mime", "created_at", "updated_at", "totp_enabled", "totp_secret_enc", "totp_backup_codes",
	"totp_failed_attempts", "totp_locked_until", "login_failed_attempts", "login_locked_until", "last_login_at",
}

func e2eUserRow(id, role string) []driver.Value {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []driver.Value{
		id, id + "@example.test", "hash-" + e2eSecretPW, "Test User", "en", role, true, false,
		nil, nil, now, now, false, nil, nil,
		int64(0), nil, int64(0), nil, nil,
	}
}
