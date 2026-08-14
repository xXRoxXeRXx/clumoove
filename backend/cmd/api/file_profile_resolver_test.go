package main

import (
	"database/sql"
	"testing"
	"time"

	"backend/internal/db"
)

func TestFileProfileOAuthAccessNeedsRefreshWhenAccessTokenIsMissing(t *testing.T) {
	now := time.Date(2026, time.August, 14, 20, 0, 0, 0, time.UTC)
	profile := &db.ConnectionProfile{
		Provider:       "google",
		TokenExpiresAt: sql.NullTime{Time: now.Add(time.Hour), Valid: true},
	}
	if !fileProfileOAuthAccessNeedsRefresh(profile, "", now) {
		t.Fatal("missing OAuth access token must be refreshed even before its stored expiry")
	}
	if fileProfileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("present OAuth access token with a future expiry should not refresh")
	}
	profile.TokenExpiresAt = sql.NullTime{Time: now.Add(time.Minute), Valid: true}
	if !fileProfileOAuthAccessNeedsRefresh(profile, "access-token", now) {
		t.Fatal("near-expiry OAuth access token must refresh")
	}
}
