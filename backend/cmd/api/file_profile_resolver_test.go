package main

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

func TestMapFileProfileError(t *testing.T) {
	if got := mapFileProfileError(errProfileNotOwned); !errors.Is(got, errFileProfileNotFound) {
		t.Fatalf("ownership error mapped to %v, want errFileProfileNotFound", got)
	}
	if got := mapFileProfileError(errors.New("database unavailable")); !errors.Is(got, errFileProviderUnavailable) {
		t.Fatalf("provider error mapped to %v, want errFileProviderUnavailable", got)
	}
}

func TestFileProfileOAuthAccessNeedsRefreshWhenAccessTokenIsMissing(t *testing.T) {
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	profile := &db.ConnectionProfile{
		Provider:       "google",
		TokenExpiresAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true},
	}
	if !profileOAuthAccessNeedsRefresh(profile, "", now) {
		t.Fatal("missing OAuth access token must be refreshed even before its stored expiry")
	}
	if profileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("present OAuth access token with a future expiry should not refresh")
	}
	profile.TokenExpiresAt = sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	if !profileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("near-expiry OAuth access token must refresh")
	}
}
