package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/crypto"
	"backend/internal/db"
)

const notificationTestKey = "test-notification-encryption-key"

// fakeNotificationStore overrides only the db.NotificationStore methods the
// notification handlers use; any other call panics through the nil embedded
// interface.
type fakeNotificationStore struct {
	db.NotificationStore
	channels   map[string]db.NotificationChannel
	listErr    error
	getErr     error
	upsertErr  error
	upserts    []fakeChannelUpsert
	gotUserIDs []string
}

type fakeChannelUpsert struct {
	userID, typ string
	enabled     bool
	encrypted   string
}

func (f *fakeNotificationStore) GetNotificationChannels(userID string) ([]db.NotificationChannel, error) {
	f.gotUserIDs = append(f.gotUserIDs, userID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []db.NotificationChannel
	for _, c := range f.channels {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeNotificationStore) GetNotificationChannel(userID, typ string) (*db.NotificationChannel, error) {
	f.gotUserIDs = append(f.gotUserIDs, userID)
	if f.getErr != nil {
		return nil, f.getErr
	}
	c, ok := f.channels[typ]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &c, nil
}

func (f *fakeNotificationStore) UpsertNotificationChannel(userID, typ string, enabled bool, encrypted string) error {
	f.upserts = append(f.upserts, fakeChannelUpsert{userID: userID, typ: typ, enabled: enabled, encrypted: encrypted})
	return f.upsertErr
}

// fakeSMTPAvailability answers only the instance-mailer check the handlers make.
type fakeSMTPAvailability struct {
	db.SettingsStore
	configured bool
}

func (f fakeSMTPAvailability) IsInstanceSMTPConfigured() (bool, error) { return f.configured, nil }

func newFakeNotificationAPI(store db.NotificationStore, smtpConfigured bool) *notificationAPI {
	return newNotificationAPI(notificationDependencies{
		notifications: store,
		settings:      fakeSMTPAvailability{configured: smtpConfigured},
		encryptionKey: notificationTestKey,
		requestEnv: requestEnv{
			logf: func(*http.Request, string, ...any) {},
			requireUserID: func(w http.ResponseWriter, r *http.Request) (string, bool) {
				return "user-1", true
			},
		},
	})
}

func serveNotifications(t *testing.T, api *notificationAPI, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	passthrough := func(next http.Handler) http.Handler { return next }
	api.registerRoutes(mux, passthrough)
	req := httptest.NewRequest(method, "/api/settings/notifications", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func encryptedNotificationConfig(t *testing.T, plain string) string {
	t.Helper()
	enc, err := crypto.EncryptWithDomain(plain, notificationTestKey, crypto.DomainNotificationConfig)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	return enc
}

func TestNotificationSettingsHandlersUseTheNotificationStore(t *testing.T) {
	t.Run("GET lists the caller's channels with secrets masked", func(t *testing.T) {
		store := &fakeNotificationStore{channels: map[string]db.NotificationChannel{
			"ntfy": {Type: "ntfy", Enabled: true, ConfigEncrypted: encryptedNotificationConfig(t, `{"url":"https://ntfy.example","topic":"t","token":"s3cret"}`)},
		}}
		rec := serveNotifications(t, newFakeNotificationAPI(store, false), http.MethodGet, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "s3cret") {
			t.Fatalf("response leaked a channel secret: %s", rec.Body.String())
		}
		var body struct {
			Channels []struct {
				Type   string         `json:"type"`
				Config map[string]any `json:"config"`
			} `json:"channels"`
			EmailAvailable bool `json:"email_available"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(body.Channels) != 1 || body.Channels[0].Type != "ntfy" || body.Channels[0].Config["token_set"] != true || body.EmailAvailable {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
		if len(store.gotUserIDs) != 1 || store.gotUserIDs[0] != "user-1" {
			t.Fatalf("store was queried for %v, want only user-1", store.gotUserIDs)
		}
	})

	t.Run("GET store failure is a typed 500", func(t *testing.T) {
		api := newFakeNotificationAPI(&fakeNotificationStore{listErr: errors.New("boom")}, false)
		assertAPIError(t, serveNotifications(t, api, http.MethodGet, ""), http.StatusInternalServerError, ErrInternalError)
	})

	t.Run("PUT persists an encrypted config for the caller", func(t *testing.T) {
		store := &fakeNotificationStore{channels: map[string]db.NotificationChannel{}}
		rec := serveNotifications(t, newFakeNotificationAPI(store, false), http.MethodPut,
			`{"type":"discord","enabled":true,"config":{"webhook_url":"https://discord.com/api/webhooks/1/abc"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.upserts) != 1 {
			t.Fatalf("upserts = %+v, want exactly one", store.upserts)
		}
		got := store.upserts[0]
		if got.userID != "user-1" || got.typ != "discord" || !got.enabled {
			t.Fatalf("upsert = %+v", got)
		}
		plain, err := crypto.DecryptWithDomain(got.encrypted, notificationTestKey, crypto.DomainNotificationConfig)
		if err != nil || !strings.Contains(plain, "discord.com/api/webhooks/1/abc") {
			t.Fatalf("stored config does not decrypt to the submitted one: %q, %v", plain, err)
		}
	})

	t.Run("PUT with a blank secret keeps the stored secret", func(t *testing.T) {
		store := &fakeNotificationStore{channels: map[string]db.NotificationChannel{
			"discord": {Type: "discord", Enabled: true, ConfigEncrypted: encryptedNotificationConfig(t, `{"webhook_url":"https://discord.com/api/webhooks/1/old"}`)},
		}}
		rec := serveNotifications(t, newFakeNotificationAPI(store, false), http.MethodPut, `{"type":"discord","enabled":false,"config":{}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.upserts) != 1 || store.upserts[0].enabled {
			t.Fatalf("upserts = %+v, want one disabled upsert", store.upserts)
		}
		plain, _ := crypto.DecryptWithDomain(store.upserts[0].encrypted, notificationTestKey, crypto.DomainNotificationConfig)
		if !strings.Contains(plain, "webhooks/1/old") {
			t.Fatalf("blank secret did not keep the prior value: %q", plain)
		}
	})

	t.Run("PUT lookup failure other than no-rows is a typed 500 and writes nothing", func(t *testing.T) {
		store := &fakeNotificationStore{getErr: errors.New("boom")}
		rec := serveNotifications(t, newFakeNotificationAPI(store, false), http.MethodPut, `{"type":"discord","enabled":true,"config":{"webhook_url":"https://discord.com/api/webhooks/1/abc"}}`)
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
		if len(store.upserts) != 0 {
			t.Fatalf("upserts = %+v, want none", store.upserts)
		}
	})

	t.Run("PUT email stores an empty config when the instance mailer exists", func(t *testing.T) {
		store := &fakeNotificationStore{}
		rec := serveNotifications(t, newFakeNotificationAPI(store, true), http.MethodPut, `{"type":"email","enabled":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if len(store.upserts) != 1 || store.upserts[0].typ != "email" {
			t.Fatalf("upserts = %+v, want one email upsert", store.upserts)
		}
	})

	t.Run("PUT upsert failure is a typed 500", func(t *testing.T) {
		store := &fakeNotificationStore{upsertErr: errors.New("boom")}
		rec := serveNotifications(t, newFakeNotificationAPI(store, true), http.MethodPut, `{"type":"email","enabled":true}`)
		assertAPIError(t, rec, http.StatusInternalServerError, ErrInternalError)
	})
}
