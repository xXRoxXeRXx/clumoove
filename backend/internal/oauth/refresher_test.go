package oauth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
)

const refresherTestKey = "01234567890123456789012345678901"

type recordingSink struct {
	calls    int
	rotated  db.EncryptedOAuthTokens
	expected string
	err      error
}

func (s *recordingSink) Persist(_ context.Context, rotated db.EncryptedOAuthTokens, expected string) error {
	s.calls++
	s.rotated = rotated
	s.expected = expected
	return s.err
}

func newTestRefresher(t *testing.T, locker TokenLocker, now time.Time) *Refresher {
	t.Helper()
	sqlDB := sql.OpenDB(&stubRotationDB{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewRefresher(RefresherConfig{
		DB:            sqlDB,
		Cache:         NewRotationCache(),
		Locker:        locker,
		EncryptionKey: refresherTestKey,
		Now:           func() time.Time { return now },
	})
}

func providerReturning(access, refresh string, expiresIn int) func(context.Context, string, string) (*TokenResponse, error) {
	return func(context.Context, string, string) (*TokenResponse, error) {
		return &TokenResponse{AccessToken: access, RefreshToken: refresh, ExpiresIn: expiresIn}, nil
	}
}

func TestRefresherSharesInfrastructureAcrossRefreshes(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	locker := &trackingTokenLocker{}
	refresher := newTestRefresher(t, locker, now)
	hash := db.OAuthTokenRotationHash("u1", "google", "T1")

	var heldDuringRefresh bool
	res, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID:       "u1",
		Provider:     "google",
		RefreshToken: "T1",
		Refresh: func(context.Context, string, string) (*TokenResponse, error) {
			heldDuringRefresh = locker.isHeld("oauth-token", hash, "refresh")
			return &TokenResponse{AccessToken: "at-2", RefreshToken: "T2", ExpiresIn: 3600}, nil
		},
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !heldDuringRefresh {
		t.Fatal("Refresh() must run under the distributed lock of the configured locker")
	}
	if res.Token == nil || res.Token.AccessToken != "at-2" {
		t.Fatalf("Refresh() token = %+v, want access at-2", res.Token)
	}
}

func TestRefresherPersistsEncryptedRotationThroughSink(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	refresher := newTestRefresher(t, nil, now)
	sink := &recordingSink{}
	expected := "enc-old-refresh"
	var persisted db.EncryptedOAuthTokens

	res, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID:          "u1",
		Provider:        "google",
		RefreshToken:    "T1",
		Refresh:         providerReturning("plain-access", "plain-refresh", 1800),
		Sink:            sink,
		ExpectedRefresh: func() string { return expected },
		OnPersisted:     func(tokens db.EncryptedOAuthTokens) { persisted = tokens },
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if res.Adopted {
		t.Fatal("a successful rotation must not report adoption")
	}
	if sink.calls != 1 || sink.expected != "enc-old-refresh" {
		t.Fatalf("sink calls = %d expected = %q, want one call anchored to enc-old-refresh", sink.calls, sink.expected)
	}
	if wantExpiry := now.Add(1800 * time.Second); !sink.rotated.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("sink expiry = %v, want %v", sink.rotated.ExpiresAt, wantExpiry)
	}
	if persisted != sink.rotated {
		t.Fatalf("OnPersisted() got %+v, want what the sink stored %+v", persisted, sink.rotated)
	}

	// The sink only ever sees ciphertext, encrypted under the credential key with
	// the per-token domain, never the provider's plaintext response.
	for _, secret := range []string{"plain-access", "plain-refresh"} {
		if strings.Contains(sink.rotated.Access, secret) || strings.Contains(sink.rotated.Refresh, secret) {
			t.Fatalf("sink received plaintext %q", secret)
		}
	}
	access, err := crypto.DecryptWithDomain(sink.rotated.Access, refresherTestKey, crypto.DomainOAuthAccessToken)
	if err != nil || access != "plain-access" {
		t.Fatalf("stored access decrypts to %q, %v; want plain-access", access, err)
	}
	refresh, err := crypto.DecryptWithDomain(sink.rotated.Refresh, refresherTestKey, crypto.DomainOAuthRefreshToken)
	if err != nil || refresh != "plain-refresh" {
		t.Fatalf("stored refresh decrypts to %q, %v; want plain-refresh", refresh, err)
	}
}

func TestRefresherDefaultsMissingExpiryToOneHour(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	refresher := newTestRefresher(t, nil, now)
	sink := &recordingSink{}

	_, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID: "u1", Provider: "google", RefreshToken: "T1",
		Refresh:         providerReturning("a", "r", 0),
		Sink:            sink,
		ExpectedRefresh: func() string { return "enc" },
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if want := now.Add(time.Hour); !sink.rotated.ExpiresAt.Equal(want) {
		t.Fatalf("expiry = %v, want %v", sink.rotated.ExpiresAt, want)
	}
}

func TestRefresherAdoptsTheWinnerWhenSuperseded(t *testing.T) {
	refresher := newTestRefresher(t, nil, time.Now())
	sink := &recordingSink{err: db.ErrOAuthTokenConflict}

	res, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID: "u1", Provider: "google", RefreshToken: "T1",
		Refresh:         providerReturning("a", "r", 3600),
		Sink:            sink,
		ExpectedRefresh: func() string { return "enc" },
		AdoptSuperseded: func(context.Context) (string, bool) { return "winner-access", true },
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !res.Adopted || res.AdoptedAccess != "winner-access" {
		t.Fatalf("Refresh() = %+v, want the winner adopted", res)
	}
}

func TestRefresherReportsSupersededWhenNothingToAdopt(t *testing.T) {
	refresher := newTestRefresher(t, nil, time.Now())
	for name, adopt := range map[string]func(context.Context) (string, bool){
		"no adopter":       nil,
		"adopter declines": func(context.Context) (string, bool) { return "", false },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := refresher.Refresh(context.Background(), OwnerRefresh{
				UserID: "u1", Provider: "google", RefreshToken: "T1",
				Refresh:         providerReturning("a", "r", 3600),
				Sink:            &recordingSink{err: db.ErrOAuthTokenConflict},
				ExpectedRefresh: func() string { return "enc" },
				AdoptSuperseded: adopt,
			})
			if !errors.Is(err, db.ErrOAuthTokenConflict) {
				t.Fatalf("Refresh() error = %v, want a wrapped ErrOAuthTokenConflict", err)
			}
		})
	}
}

func TestRefresherPropagatesSinkFailures(t *testing.T) {
	refresher := newTestRefresher(t, nil, time.Now())
	boom := errors.New("connection reset")
	adopted := false
	_, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID: "u1", Provider: "google", RefreshToken: "T1",
		Refresh:         providerReturning("a", "r", 3600),
		Sink:            &recordingSink{err: boom},
		ExpectedRefresh: func() string { return "enc" },
		AdoptSuperseded: func(context.Context) (string, bool) { adopted = true; return "x", true },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Refresh() error = %v, want %v", err, boom)
	}
	if adopted {
		t.Fatal("only a superseded rotation may be adopted, not an arbitrary sink failure")
	}
}

func TestRefresherSkipPersistLeavesTheSinkUntouched(t *testing.T) {
	refresher := newTestRefresher(t, nil, time.Now())
	sink := &recordingSink{}
	persisted := false

	res, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID: "u1", Provider: "google", RefreshToken: "T1",
		Refresh:         providerReturning("a", "r", 3600),
		Sink:            sink,
		ExpectedRefresh: func() string { return "enc" },
		SkipPersist:     func() bool { return true },
		OnPersisted:     func(db.EncryptedOAuthTokens) { persisted = true },
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if sink.calls != 0 || persisted {
		t.Fatalf("sink calls = %d, OnPersisted = %v; want neither when persistence is skipped", sink.calls, persisted)
	}
	if res.Token == nil || res.Token.AccessToken != "a" {
		t.Fatalf("Refresh() token = %+v, want the refreshed access token", res.Token)
	}
}

func TestRefresherWithoutSinkOnlyRecordsRotationHistory(t *testing.T) {
	refresher := newTestRefresher(t, nil, time.Now())
	res, err := refresher.Refresh(context.Background(), OwnerRefresh{
		UserID: "u1", Provider: "google", RefreshToken: "T1",
		Refresh: providerReturning("a", "r", 3600),
	})
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if res.Token == nil || res.Token.RefreshToken != "r" {
		t.Fatalf("Refresh() token = %+v, want the refreshed token", res.Token)
	}
}
