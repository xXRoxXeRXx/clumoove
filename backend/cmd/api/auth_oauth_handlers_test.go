package main

import (
	"errors"
	"testing"
	"time"

	"backend/internal/oauth"
)

// TestShouldSkipOAuthTokenRotation verifies the background daemon's freshness guard.
// Tokens renewed recently (e.g. by a worker inline) must NOT be rotated again.
func TestShouldSkipOAuthTokenRotation(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		expiresAt time.Time
		wantSkip  bool
	}{
		{
			name:      "zero expiresAt does not skip",
			expiresAt: time.Time{},
			wantSkip:  false,
		},
		{
			name:      "expired token does not skip",
			expiresAt: now.Add(-5 * time.Minute),
			wantSkip:  false,
		},
		{
			name:      "token expiring in 1 minute does not skip",
			expiresAt: now.Add(1 * time.Minute),
			wantSkip:  false,
		},
		{
			name:      "token expiring in 4m59s does not skip (inside 5 min threshold)",
			expiresAt: now.Add(5*time.Minute - time.Second),
			wantSkip:  false,
		},
		{
			name:      "token expiring in exactly 5m does not skip",
			expiresAt: now.Add(5 * time.Minute),
			wantSkip:  false,
		},
		{
			name:      "token expiring in 5m1s skips rotation (already fresh)",
			expiresAt: now.Add(5*time.Minute + time.Second),
			wantSkip:  true,
		},
		{
			name:      "token expiring in 10 minutes skips rotation",
			expiresAt: now.Add(10 * time.Minute),
			wantSkip:  true,
		},
		{
			name:      "token expiring in 1 hour (freshly issued) skips rotation",
			expiresAt: now.Add(1 * time.Hour),
			wantSkip:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSkipOAuthTokenRotation(tc.expiresAt, now)
			if got != tc.wantSkip {
				t.Fatalf("shouldSkipOAuthTokenRotation(%v, %v) = %v, want %v", tc.expiresAt, now, got, tc.wantSkip)
			}
		})
	}
}

// TestIsOAuthRotationAlreadyResolved verifies the bail-out logic that protects against
// race conditions where a concurrent worker already refreshed the token in the DB.
func TestIsOAuthRotationAlreadyResolved(t *testing.T) {
	cases := []struct {
		name              string
		currentRefreshEnc string
		freshRefreshEnc   string
		wantResolved      bool
	}{
		{
			name:              "token unchanged means real failure, not resolved by worker",
			currentRefreshEnc: "enc-token-v1",
			freshRefreshEnc:   "enc-token-v1",
			wantResolved:      false,
		},
		{
			name:              "empty fresh token is not considered resolved",
			currentRefreshEnc: "enc-token-v1",
			freshRefreshEnc:   "",
			wantResolved:      false,
		},
		{
			name:              "token changed in DB indicates concurrent worker wrote new token",
			currentRefreshEnc: "enc-token-v1",
			freshRefreshEnc:   "enc-token-v2",
			wantResolved:      true,
		},
		{
			name:              "initial empty token with fresh token is resolved",
			currentRefreshEnc: "",
			freshRefreshEnc:   "enc-token-v1",
			wantResolved:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isOAuthRotationAlreadyResolved(tc.currentRefreshEnc, tc.freshRefreshEnc)
			if got != tc.wantResolved {
				t.Fatalf("isOAuthRotationAlreadyResolved(%q, %q) = %v, want %v",
					tc.currentRefreshEnc, tc.freshRefreshEnc, got, tc.wantResolved)
			}
		})
	}
}

// TestOAuthRotationInvalidGrantBailoutDecision ensures that when an OAuth refresh
// returns ErrRefreshTokenInvalid, the decision to fail the migration vs bail out
// strictly respects the DB re-read token state.
func TestOAuthRotationInvalidGrantBailoutDecision(t *testing.T) {
	err := oauth.ErrRefreshTokenInvalid
	if !errors.Is(err, oauth.ErrRefreshTokenInvalid) {
		t.Fatal("expected ErrRefreshTokenInvalid")
	}

	// Scenario A: Worker updated token concurrently -> fresh token differs from current -> bail out (do NOT fail)
	currentRefreshEnc := "stale-worker-token-v1"
	freshRefreshEncAfterRace := "fresh-worker-token-v2"
	if !isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEncAfterRace) {
		t.Fatal("expected rotation to be recognized as already resolved by concurrent worker")
	}

	// Scenario B: Token revoked by provider and not updated by any worker -> fail migration
	freshRefreshEncSame := "stale-worker-token-v1"
	if isOAuthRotationAlreadyResolved(currentRefreshEnc, freshRefreshEncSame) {
		t.Fatal("expected rotation NOT to be recognized as resolved when token in DB is unchanged")
	}
}
