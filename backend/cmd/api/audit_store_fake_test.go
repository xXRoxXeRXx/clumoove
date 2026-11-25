package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/db"
)

// fakeAuditStore overrides only the db.AuditStore methods a test needs; any
// other call panics through the nil embedded interface.
type fakeAuditStore struct {
	db.AuditStore
	rows      []db.AuditLogRow
	total     int
	listErr   error
	gotParams []db.AuditLogParams
	written   []db.AuditEntry
}

func (f *fakeAuditStore) ListAuditLog(p db.AuditLogParams) ([]db.AuditLogRow, int, error) {
	f.gotParams = append(f.gotParams, p)
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.rows, f.total, nil
}

func (f *fakeAuditStore) WriteAuditLog(e db.AuditEntry) { f.written = append(f.written, e) }

func (f *fakeAuditStore) WriteAuditLogContext(_ context.Context, e db.AuditEntry) {
	f.written = append(f.written, e)
}

func newAuditAdminAPI(audit db.AuditStore) *adminAPI {
	return newAdminAPI(adminDependencies{
		audit: audit,
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
		},
	})
}

func TestAdminAuditLogHandlerUsesTheAuditStore(t *testing.T) {
	t.Run("filters and paging reach the store and the rows are returned", func(t *testing.T) {
		store := &fakeAuditStore{
			rows:  []db.AuditLogRow{{ID: 7, UserID: "u-1", Action: db.AuditSettingUpdated, Target: "registrations_enabled"}},
			total: 41,
		}
		api := newAuditAdminAPI(store)
		rec := serveAdminSettings(api, http.MethodGet, "/api/audit/log?page=2&limit=10&action=SETTING_UPDATED&user_id=u-1&target=t&from=2026-01-01&to=2026-02-01", "admin-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		want := db.AuditLogParams{Page: 2, Limit: 10, Action: "SETTING_UPDATED", UserID: "u-1", Target: "t", From: "2026-01-01", To: "2026-02-01"}
		if len(store.gotParams) != 1 || store.gotParams[0] != want {
			t.Fatalf("store params = %+v, want [%+v]", store.gotParams, want)
		}
		var body struct {
			Entries []db.AuditLogRow `json:"entries"`
			Total   int              `json:"total"`
			Page    int              `json:"page"`
			Limit   int              `json:"limit"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Entries) != 1 || body.Entries[0].ID != 7 || body.Total != 41 || body.Page != 2 || body.Limit != 10 {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
	})

	t.Run("an out-of-range limit falls back to the default page size", func(t *testing.T) {
		store := &fakeAuditStore{}
		api := newAuditAdminAPI(store)
		serveAdminSettings(api, http.MethodGet, "/api/audit/log?limit=1000", "admin-1", "")
		if len(store.gotParams) != 1 || store.gotParams[0].Limit != 20 || store.gotParams[0].Page != 1 {
			t.Fatalf("store params = %+v, want page 1 limit 20", store.gotParams)
		}
	})

	t.Run("a store failure is a typed 500", func(t *testing.T) {
		api := newAuditAdminAPI(&fakeAuditStore{listErr: errors.New("boom")})
		assertAPIError(t, serveAdminSettings(api, http.MethodGet, "/api/audit/log", "admin-1", ""), http.StatusInternalServerError, ErrInternalError)
	})
}

func TestWriteAuditRoutesThroughTheAuditStore(t *testing.T) {
	store := &fakeAuditStore{}
	server := &APIServer{audit: store}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", nil)
	req.RemoteAddr = "203.0.113.9:1234"

	server.writeAudit(req, db.AuditSettingUpdated, "registrations_enabled", "admin-1", map[string]interface{}{"value": "false"})

	if len(store.written) != 1 {
		t.Fatalf("written = %d entries, want 1", len(store.written))
	}
	got := store.written[0]
	if got.Action != db.AuditSettingUpdated || got.Target != "registrations_enabled" || got.UserID != (sql.NullString{String: "admin-1", Valid: true}) {
		t.Fatalf("entry = %+v", got)
	}
	if string(got.Details) != `{"value":"false"}` {
		t.Fatalf("details = %s", got.Details)
	}
}
