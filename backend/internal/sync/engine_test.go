package sync

import (
	"database/sql"
	"testing"
	"time"
)

func TestGetSourceRelPath(t *testing.T) {
	tests := []struct {
		targetPath string
		targetDir  string
		expected   string
	}{
		{
			targetPath: "/backup/folder/file.txt",
			targetDir:  "/backup",
			expected:   "/folder/file.txt",
		},
		{
			targetPath: "/file.txt",
			targetDir:  "/",
			expected:   "/file.txt",
		},
		{
			targetPath: "/backup",
			targetDir:  "/backup",
			expected:   "/",
		},
		{
			targetPath: "/other/file.txt",
			targetDir:  "/backup",
			expected:   "/other/file.txt",
		},
		{
			// Nested target dir: prefix must be the full directory, not a partial match.
			targetPath: "/backuped/file.txt",
			targetDir:  "/backup",
			expected:   "/backuped/file.txt",
		},
	}

	for _, tt := range tests {
		result := getSourceRelPath(tt.targetPath, tt.targetDir)
		if result != tt.expected {
			t.Errorf("getSourceRelPath(%q, %q) = %q; expected %q", tt.targetPath, tt.targetDir, result, tt.expected)
		}
	}
}

func TestConflictNeedsRename(t *testing.T) {
	cases := map[string]bool{
		"OVERWRITE": false,
		"SKIP":      false,
		"RENAME":    true,
		"":          false,
	}
	for strategy, want := range cases {
		if got := conflictNeedsRename(strategy); got != want {
			t.Errorf("conflictNeedsRename(%q) = %v; want %v", strategy, got, want)
		}
	}
}

func TestShouldRefreshToken(t *testing.T) {
	now := time.Now()

	// No expiry known → do not refresh (preserves pre-existing behaviour;
	// callers now always populate an expiry on creation).
	if shouldRefreshToken(sql.NullTime{Valid: false}) {
		t.Error("shouldRefreshToken(invalid) = true; want false")
	}

	// Expires in 10 minutes → still valid, no refresh.
	if shouldRefreshToken(sql.NullTime{Time: now.Add(10 * time.Minute), Valid: true}) {
		t.Error("shouldRefreshToken(10m) = true; want false")
	}

	// Expires in 1 minute (< 2-min threshold) → refresh.
	if !shouldRefreshToken(sql.NullTime{Time: now.Add(1 * time.Minute), Valid: true}) {
		t.Error("shouldRefreshToken(1m) = false; want true")
	}

	// Already expired → refresh.
	if !shouldRefreshToken(sql.NullTime{Time: now.Add(-1 * time.Hour), Valid: true}) {
		t.Error("shouldRefreshToken(expired) = false; want true")
	}
}
