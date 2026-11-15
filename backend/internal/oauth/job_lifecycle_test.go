package oauth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const jobLifecycleTestKey = "test-encryption-secret-key-32-bytes-long!"

func TestJobTokenRefresherLifecycleMatrix(t *testing.T) {
	for _, kind := range []string{"migration", "sync"} {
		for _, tc := range []struct {
			name, want string
			force      bool
			fresh      bool
			lock       bool
			conflict   bool
		}{
			{name: "normal refresh", want: "fresh-access"},
			{name: "forced refresh", force: true, fresh: true, want: "fresh-access"},
			{name: "lock contention adopts refreshed credential", lock: true, want: "winner-access"},
			{name: "compare-and-swap conflict adopts winner", conflict: true, want: "winner-access"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				state := newTestJobTokenState(t, kind, tc.fresh)
				if tc.lock {
					state.onReload = func(s *testJobTokenState) {
						s.snapshot = encryptedJobSnapshot(t, "winner-access", "winner-refresh", true)
					}
				}
				if tc.conflict {
					state.conflict = true
					state.winner = encryptedJobSnapshot(t, "winner-access", "winner-refresh", true)
				}
				refresher := NewJobTokenRefresher(nil, jobLifecycleTestKey, nil)
				refresher.refresh = func(context.Context, string, string) (*TokenResponse, error) {
					return &TokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 60}, nil
				}
				if tc.lock {
					refresher.locker = &testJobOAuthLocker{claims: []bool{false}}
				}
				got, err := refresher.refreshLifecycle(context.Background(), state, "source", "caller-access", tc.force)
				if err != nil {
					t.Fatalf("refreshLifecycle: %v", err)
				}
				if got != tc.want {
					t.Fatalf("token = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

func TestJobTokenRefresherNoRefreshTokenTakesNoLock(t *testing.T) {
	refresher := NewJobTokenRefresher(nil, jobLifecycleTestKey, nil)
	mig := &db.Migration{ID: "m1", SourceRefreshTokenEncrypted: sql.NullString{}}
	got, err := refresher.EnsureFreshMigrationToken(context.Background(), mig, "source", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tok" {
		t.Fatalf("got = %q, want %q", got, "tok")
	}
	if len(refresher.locks.locks) != 0 {
		t.Fatalf("locks count = %d, want 0", len(refresher.locks.locks))
	}
}

func TestJobTokenStateRoleIsolation(t *testing.T) {
	srcExpires := sql.NullTime{Time: time.Unix(1700000000, 0).UTC(), Valid: true}
	tgtExpires := sql.NullTime{Time: time.Unix(1700003600, 0).UTC(), Valid: true}

	mig := &db.Migration{
		ID:                          "mig-1",
		SourceProvider:              "gdrive",
		TargetProvider:              "onedrive",
		SourcePasswordEncrypted:     "mig-src-access-old",
		SourceRefreshTokenEncrypted: sql.NullString{String: "mig-src-refresh-old", Valid: true},
		TargetPasswordEncrypted:     "mig-tgt-access-old",
		TargetRefreshTokenEncrypted: sql.NullString{String: "mig-tgt-refresh-old", Valid: true},
	}
	syncJob := &db.SyncJob{
		ID:                          "sync-1",
		SourceProvider:              "dropbox",
		TargetProvider:              "box",
		SourcePasswordEncrypted:     "sync-src-access-old",
		SourceRefreshTokenEncrypted: sql.NullString{String: "sync-src-refresh-old", Valid: true},
		TargetPasswordEncrypted:     "sync-tgt-access-old",
		TargetRefreshTokenEncrypted: sql.NullString{String: "sync-tgt-refresh-old", Valid: true},
	}

	states := []struct {
		name     string
		state    jobOAuthTokenState
		wantSrcP string
		wantTgtP string
		origTgtA string
		origSrcA string
	}{
		{
			name:     "migration",
			state:    &migrationJobTokenState{migration: mig},
			wantSrcP: "gdrive",
			wantTgtP: "onedrive",
			origSrcA: "mig-src-access-old",
			origTgtA: "mig-tgt-access-old",
		},
		{
			name:     "sync",
			state:    &syncJobTokenState{job: syncJob},
			wantSrcP: "dropbox",
			wantTgtP: "box",
			origSrcA: "sync-src-access-old",
			origTgtA: "sync-tgt-access-old",
		},
	}

	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			tc.state.ApplyOAuthTokens("source", "src-access-new", sql.NullString{String: "src-refresh-new", Valid: true}, srcExpires)
			src := tc.state.OAuthTokens("source")
			tgt := tc.state.OAuthTokens("target")
			if src.provider != tc.wantSrcP || src.accessEnc != "src-access-new" || src.refreshEnc.String != "src-refresh-new" || !src.expiresAt.Valid || !src.expiresAt.Time.Equal(srcExpires.Time) {
				t.Fatalf("source snapshot after source update = %+v", src)
			}
			if tgt.provider != tc.wantTgtP || tgt.accessEnc != tc.origTgtA {
				t.Fatalf("target snapshot mutated after source update: %+v", tgt)
			}

			tc.state.ApplyOAuthTokens("target", "tgt-access-new", sql.NullString{String: "tgt-refresh-new", Valid: true}, tgtExpires)
			srcAfter := tc.state.OAuthTokens("source")
			tgtAfter := tc.state.OAuthTokens("target")
			if srcAfter.accessEnc != "src-access-new" {
				t.Fatalf("source snapshot mutated after target update: %+v", srcAfter)
			}
			if tgtAfter.provider != tc.wantTgtP || tgtAfter.accessEnc != "tgt-access-new" || tgtAfter.refreshEnc.String != "tgt-refresh-new" || !tgtAfter.expiresAt.Valid || !tgtAfter.expiresAt.Time.Equal(tgtExpires.Time) {
				t.Fatalf("target snapshot after target update = %+v", tgtAfter)
			}
		})
	}

	if mig.SourcePasswordEncrypted != "src-access-new" || mig.SourceRefreshTokenEncrypted.String != "src-refresh-new" || !mig.SourceTokenExpiresAt.Time.Equal(srcExpires.Time) ||
		mig.TargetPasswordEncrypted != "tgt-access-new" || mig.TargetRefreshTokenEncrypted.String != "tgt-refresh-new" || !mig.TargetTokenExpiresAt.Time.Equal(tgtExpires.Time) {
		t.Fatalf("underlying migration fields mismatch: %+v", mig)
	}
	if syncJob.SourcePasswordEncrypted != "src-access-new" || syncJob.SourceRefreshTokenEncrypted.String != "src-refresh-new" || !syncJob.SourceTokenExpiresAt.Time.Equal(srcExpires.Time) ||
		syncJob.TargetPasswordEncrypted != "tgt-access-new" || syncJob.TargetRefreshTokenEncrypted.String != "tgt-refresh-new" || !syncJob.TargetTokenExpiresAt.Time.Equal(tgtExpires.Time) {
		t.Fatalf("underlying syncJob fields mismatch: %+v", syncJob)
	}
}

type testJobTokenState struct {
	kind, id string
	snapshot jobOAuthTokenSnapshot
	winner   jobOAuthTokenSnapshot
	conflict bool
	onReload func(*testJobTokenState)
}

func (s *testJobTokenState) OAuthWorkKind() string                    { return s.kind }
func (s *testJobTokenState) OAuthJobID() string                       { return s.id }
func (s *testJobTokenState) OAuthUserID() string                      { return "user-1" }
func (s *testJobTokenState) OAuthTokens(string) jobOAuthTokenSnapshot { return s.snapshot }
func (s *testJobTokenState) OAuthUpdatedAt() time.Time                { return time.Time{} }
func (s *testJobTokenState) ApplyOAuthTokens(_ string, access string, refresh sql.NullString, expires sql.NullTime) {
	s.snapshot.accessEnc, s.snapshot.refreshEnc, s.snapshot.expiresAt = access, refresh, expires
}
func (s *testJobTokenState) ReloadOAuthTokens(context.Context) error {
	if s.onReload != nil {
		s.onReload(s)
	}
	return nil
}
func (s *testJobTokenState) PersistOAuthTokens(_ context.Context, _ string, access, refresh string, expires time.Time, _ string) error {
	if s.conflict {
		s.snapshot = s.winner
		s.conflict = false
		return db.ErrOAuthTokenConflict
	}
	s.ApplyOAuthTokens("source", access, sql.NullString{String: refresh, Valid: true}, sql.NullTime{Time: expires, Valid: true})
	return nil
}

func newTestJobTokenState(t *testing.T, kind string, fresh bool) *testJobTokenState {
	return &testJobTokenState{kind: kind, id: "job-1", snapshot: encryptedJobSnapshot(t, "stored-access", "stored-refresh", fresh)}
}

func encryptedJobSnapshot(t *testing.T, access, refresh string, fresh bool) jobOAuthTokenSnapshot {
	t.Helper()
	accessEnc, err := crypto.EncryptWithDomain(access, jobLifecycleTestKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshEnc, err := crypto.EncryptWithDomain(refresh, jobLifecycleTestKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(-time.Minute)
	if fresh {
		expiry = time.Now().Add(time.Hour)
	}
	return jobOAuthTokenSnapshot{
		accessEnc:  accessEnc,
		refreshEnc: sql.NullString{String: refreshEnc, Valid: true},
		expiresAt:  sql.NullTime{Time: expiry, Valid: true},
		provider:   "google",
	}
}

type testJobOAuthLocker struct{ claims []bool }

func (l *testJobOAuthLocker) TryClaimOAuthLock(context.Context, string, string, string, time.Duration) (string, bool, error) {
	if len(l.claims) == 0 {
		return "lock", true, nil
	}
	claimed := l.claims[0]
	l.claims = l.claims[1:]
	return "", claimed, nil
}

func (l *testJobOAuthLocker) ReleaseOAuthLock(context.Context, string, string, string, string) error {
	return errors.New("unexpected release")
}
