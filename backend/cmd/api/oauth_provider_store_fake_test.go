package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const oauthProviderTestKey = "test-oauth-provider-encryption-key"

// fakeOAuthProviderStore overrides only the db.OAuthProviderStore methods the
// admin handlers use; any other call panics through the nil embedded interface.
type fakeOAuthProviderStore struct {
	db.OAuthProviderStore
	rows      map[string]db.InstanceOAuthProvider
	listErr   error
	getErr    error
	upsertErr error
	deleteErr error
	upserts   []db.InstanceOAuthProvider
	deleted   []string
}

func (f *fakeOAuthProviderStore) ListInstanceOAuthProviders() ([]db.InstanceOAuthProvider, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.InstanceOAuthProvider
	for _, row := range f.rows {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeOAuthProviderStore) GetInstanceOAuthProvider(provider string) (*db.InstanceOAuthProvider, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	row, ok := f.rows[provider]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &row, nil
}

func (f *fakeOAuthProviderStore) UpsertInstanceOAuthProvider(p *db.InstanceOAuthProvider) error {
	f.upserts = append(f.upserts, *p)
	return f.upsertErr
}

func (f *fakeOAuthProviderStore) DeleteInstanceOAuthProvider(provider string) error {
	f.deleted = append(f.deleted, provider)
	return f.deleteErr
}

func newFakeOAuthAdminAPI(store db.OAuthProviderStore) *adminAPI {
	return newAdminAPI(adminDependencies{
		oauthProviders: store,
		encryptionKey:  oauthProviderTestKey,
		requestEnv: requestEnv{
			logf:       func(*http.Request, string, ...any) {},
			writeAudit: func(*http.Request, db.AuditAction, string, string, map[string]interface{}) {},
		},
	})
}

func TestAdminOAuthHandlersUseTheOAuthProviderStore(t *testing.T) {
	t.Run("GET reports configured providers in the fixed order without secrets", func(t *testing.T) {
		updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		store := &fakeOAuthProviderStore{rows: map[string]db.InstanceOAuthProvider{
			"box": {Provider: "box", ClientID: "box-id", ClientSecretEnc: "ciphertext", UpdatedAt: updated},
		}}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodGet, "/api/admin/settings/oauth", "admin-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Providers []instanceOAuthProviderView `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Providers) != len(oauthProviderOrder) {
			t.Fatalf("providers = %d, want %d", len(body.Providers), len(oauthProviderOrder))
		}
		for i, p := range body.Providers {
			if p.Provider != oauthProviderOrder[i] {
				t.Fatalf("provider[%d] = %s, want %s", i, p.Provider, oauthProviderOrder[i])
			}
			if p.Provider == "box" && (!p.Configured || p.ClientID != "box-id" || !p.ClientSecretSet) {
				t.Fatalf("box view = %+v", p)
			}
			if p.Provider != "box" && p.Configured {
				t.Fatalf("%s reported configured", p.Provider)
			}
		}
		if strings.Contains(rec.Body.String(), "ciphertext") {
			t.Fatalf("response leaked the stored secret: %s", rec.Body.String())
		}
	})

	t.Run("GET store failure is a typed 500", func(t *testing.T) {
		api := newFakeOAuthAdminAPI(&fakeOAuthProviderStore{listErr: errors.New("boom")})
		assertAPIError(t, serveAdminSettings(api, http.MethodGet, "/api/admin/settings/oauth", "admin-1", ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("PUT encrypts a new secret before it reaches the store", func(t *testing.T) {
		store := &fakeOAuthProviderStore{rows: map[string]db.InstanceOAuthProvider{}}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodPut, "/api/admin/settings/oauth/box", "admin-1", `{"client_id":"box-id","client_secret":"box-secret"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.upserts) != 1 || store.upserts[0].Provider != "box" || store.upserts[0].ClientID != "box-id" {
			t.Fatalf("upserts = %+v", store.upserts)
		}
		enc := store.upserts[0].ClientSecretEnc
		if enc == "" || enc == "box-secret" {
			t.Fatalf("secret was not encrypted: %q", enc)
		}
		if plain, err := crypto.DecryptWithDomain(enc, oauthProviderTestKey, crypto.DomainOAuthClientSecret); err != nil || plain != "box-secret" {
			t.Fatalf("stored secret does not decrypt to the submitted one: %q, %v", plain, err)
		}
	})

	t.Run("PUT with a blank secret keeps the stored ciphertext", func(t *testing.T) {
		store := &fakeOAuthProviderStore{rows: map[string]db.InstanceOAuthProvider{
			"box": {Provider: "box", ClientID: "old-id", ClientSecretEnc: "existing-ciphertext"},
		}}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodPut, "/api/admin/settings/oauth/box", "admin-1", `{"client_id":"new-id"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.upserts) != 1 || store.upserts[0].ClientID != "new-id" || store.upserts[0].ClientSecretEnc != "existing-ciphertext" {
			t.Fatalf("upserts = %+v, want the new id with the old ciphertext", store.upserts)
		}
	})

	t.Run("PUT with a blank secret and nothing stored is rejected without a write", func(t *testing.T) {
		store := &fakeOAuthProviderStore{rows: map[string]db.InstanceOAuthProvider{}}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodPut, "/api/admin/settings/oauth/box", "admin-1", `{"client_id":"box-id"}`)
		assertAPIError(t, rec, http.StatusBadRequest, ErrOauthSecretRequired)
		if len(store.upserts) != 0 {
			t.Fatalf("upserts = %+v, want none", store.upserts)
		}
	})

	t.Run("PUT lookup failure other than no-rows is a typed 500 and writes nothing", func(t *testing.T) {
		store := &fakeOAuthProviderStore{getErr: errors.New("boom")}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodPut, "/api/admin/settings/oauth/box", "admin-1", `{"client_id":"box-id","client_secret":"s"}`)
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if len(store.upserts) != 0 {
			t.Fatalf("upserts = %+v, want none", store.upserts)
		}
	})

	t.Run("PUT for an unknown provider never touches the store", func(t *testing.T) {
		store := &fakeOAuthProviderStore{}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodPut, "/api/admin/settings/oauth/nope", "admin-1", `{"client_id":"x","client_secret":"y"}`)
		assertAPIError(t, rec, http.StatusBadRequest, ErrOauthProviderUnknown)
	})

	t.Run("DELETE removes the provider through the store", func(t *testing.T) {
		store := &fakeOAuthProviderStore{}
		rec := serveAdminSettings(newFakeOAuthAdminAPI(store), http.MethodDelete, "/api/admin/settings/oauth/dropbox", "admin-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.deleted) != 1 || store.deleted[0] != "dropbox" {
			t.Fatalf("deleted = %v", store.deleted)
		}
	})

	t.Run("DELETE store failure is a typed 500", func(t *testing.T) {
		api := newFakeOAuthAdminAPI(&fakeOAuthProviderStore{deleteErr: errors.New("boom")})
		assertAPIError(t, serveAdminSettings(api, http.MethodDelete, "/api/admin/settings/oauth/dropbox", "admin-1", ""), http.StatusInternalServerError, ErrInternalError)
	})
}
