package oauth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

type fakeMigrationStore struct {
	db.MigrationStore
	latest  *db.Migration
	loadErr error
	sinkFor [2]string
}

func (f *fakeMigrationStore) GetMigrationContext(context.Context, string) (*db.Migration, error) {
	return f.latest, f.loadErr
}

func (f *fakeMigrationStore) TokenSink(id, role string) db.OAuthReauthSink {
	f.sinkFor = [2]string{id, role}
	return nil
}

func TestMigrationJobTokenStateReloadsThroughTheMigrationStore(t *testing.T) {
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &fakeMigrationStore{latest: &db.Migration{
		ID:                          "mig-1",
		SourcePasswordEncrypted:     "new-src",
		SourceRefreshTokenEncrypted: sql.NullString{String: "new-src-refresh", Valid: true},
		TargetPasswordEncrypted:     "new-tgt",
		UpdatedAt:                   updated,
	}}
	mig := &db.Migration{ID: "mig-1", SourcePasswordEncrypted: "old"}
	state := &migrationJobTokenState{store: store, migration: mig}

	if err := state.ReloadOAuthTokens(context.Background()); err != nil {
		t.Fatalf("ReloadOAuthTokens() error: %v", err)
	}
	if mig.SourcePasswordEncrypted != "new-src" || mig.TargetPasswordEncrypted != "new-tgt" || !mig.UpdatedAt.Equal(updated) {
		t.Fatalf("migration not refreshed: %+v", mig)
	}
	if mig.SourceRefreshTokenEncrypted.String != "new-src-refresh" {
		t.Errorf("source refresh = %q", mig.SourceRefreshTokenEncrypted.String)
	}
}

func TestMigrationJobTokenStateReloadReportsLookupFailure(t *testing.T) {
	loadErr := errors.New("db down")
	state := &migrationJobTokenState{store: &fakeMigrationStore{loadErr: loadErr}, migration: &db.Migration{ID: "mig-1"}}

	if err := state.ReloadOAuthTokens(context.Background()); !errors.Is(err, loadErr) {
		t.Fatalf("ReloadOAuthTokens() error = %v, want %v", err, loadErr)
	}
}

func TestMigrationJobTokenStateSinkIsScopedToTheMigrationAndRole(t *testing.T) {
	store := &fakeMigrationStore{}
	state := &migrationJobTokenState{store: store, migration: &db.Migration{ID: "mig-1"}}

	state.OAuthTokenSink("target")

	if store.sinkFor != [2]string{"mig-1", "target"} {
		t.Fatalf("sink requested for %v, want [mig-1 target]", store.sinkFor)
	}
}
