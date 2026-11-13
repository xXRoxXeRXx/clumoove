package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/oauth"
)

const oauthLifecycleTestKey = "test-encryption-secret-key-32-bytes-long!"

func TestOAuthRefreshLifecycleMatrix(t *testing.T) {
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
				state := newTestOAuthTokenState(t, kind, tc.fresh)
				if tc.lock {
					state.onReload = func(s *testOAuthTokenState) {
						s.snapshot = encryptedSnapshot(t, "winner-access", "winner-refresh", true)
					}
				}
				if tc.conflict {
					state.conflict = true
					state.winner = encryptedSnapshot(t, "winner-access", "winner-refresh", true)
				}
				p := &Processor{secretKey: oauthLifecycleTestKey, oauthRefresh: func(context.Context, string, string) (*oauth.TokenResponse, error) {
					return &oauth.TokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 60}, nil
				}}
				if tc.lock {
					p.oauthRefreshLock = &testOAuthLocker{claims: []bool{false}}
				}
				got, err := p.refreshOAuthLifecycle(context.Background(), state, "source", "caller-access", tc.force)
				if err != nil {
					t.Fatalf("refreshOAuthLifecycle: %v", err)
				}
				if got != tc.want {
					t.Fatalf("token = %q, want %q", got, tc.want)
				}
			})
		}
	}
}

type testOAuthTokenState struct {
	kind, id string
	snapshot oauthTokenSnapshot
	winner   oauthTokenSnapshot
	conflict bool
	onReload func(*testOAuthTokenState)
}

func (s *testOAuthTokenState) OAuthWorkKind() string                 { return s.kind }
func (s *testOAuthTokenState) OAuthJobID() string                    { return s.id }
func (s *testOAuthTokenState) OAuthUserID() string                   { return "user-1" }
func (s *testOAuthTokenState) OAuthTokens(string) oauthTokenSnapshot { return s.snapshot }
func (s *testOAuthTokenState) OAuthUpdatedAt() time.Time             { return time.Time{} }
func (s *testOAuthTokenState) ApplyOAuthTokens(_ string, access string, refresh sql.NullString, expires sql.NullTime) {
	s.snapshot.accessEnc, s.snapshot.refreshEnc, s.snapshot.expiresAt = access, refresh, expires
}
func (s *testOAuthTokenState) ReloadOAuthTokens(context.Context) error {
	if s.onReload != nil {
		s.onReload(s)
	}
	return nil
}
func (s *testOAuthTokenState) PersistOAuthTokens(_ context.Context, _ string, access, refresh string, expires time.Time, _ string) error {
	if s.conflict {
		s.snapshot = s.winner
		s.conflict = false
		return db.ErrOAuthTokenConflict
	}
	s.ApplyOAuthTokens("source", access, sql.NullString{String: refresh, Valid: true}, sql.NullTime{Time: expires, Valid: true})
	return nil
}

func newTestOAuthTokenState(t *testing.T, kind string, fresh bool) *testOAuthTokenState {
	return &testOAuthTokenState{kind: kind, id: "job-1", snapshot: encryptedSnapshot(t, "stored-access", "stored-refresh", fresh)}
}
func encryptedSnapshot(t *testing.T, access, refresh string, fresh bool) oauthTokenSnapshot {
	t.Helper()
	accessEnc, err := crypto.EncryptWithDomain(access, oauthLifecycleTestKey, crypto.DomainOAuthAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	refreshEnc, err := crypto.EncryptWithDomain(refresh, oauthLifecycleTestKey, crypto.DomainOAuthRefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(-time.Minute)
	if fresh {
		expiry = time.Now().Add(time.Hour)
	}
	return oauthTokenSnapshot{accessEnc: accessEnc, refreshEnc: sql.NullString{String: refreshEnc, Valid: true}, expiresAt: sql.NullTime{Time: expiry, Valid: true}, provider: "google"}
}

type testOAuthLocker struct{ claims []bool }

func (l *testOAuthLocker) TryClaimOAuthLock(context.Context, string, string, string, time.Duration) (string, bool, error) {
	if len(l.claims) == 0 {
		return "lock", true, nil
	}
	claimed := l.claims[0]
	l.claims = l.claims[1:]
	return "", claimed, nil
}
func (l *testOAuthLocker) ReleaseOAuthLock(context.Context, string, string, string, string) error {
	return errors.New("unexpected release")
}
