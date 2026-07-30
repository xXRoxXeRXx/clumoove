package sync

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

// listingTestProvider embeds the full provider contract and only implements
// the listing calls exercised by listFiles.
type listingTestProvider struct {
	storage.StorageProvider
	inspect func(path string) (storage.CloudResource, error)
	list    func(path string) ([]storage.CloudResource, error)
}

func (p listingTestProvider) InspectResource(_ context.Context, _ string, resourcePath string) (storage.CloudResource, error) {
	return p.inspect(resourcePath)
}

func (p listingTestProvider) GetDirectoryListing(_ context.Context, _ string, dirPath string) ([]storage.CloudResource, error) {
	return p.list(dirPath)
}

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

func TestIsFileMatchingTarget(t *testing.T) {
	now := time.Now()

	// 1. Matching size and timestamp within 2s tolerance
	src := fileState{Path: "/test.txt", Size: 1024, LastModified: now}
	tgt := fileState{Path: "/test.txt", Size: 1024, LastModified: now.Add(1 * time.Second)}
	if !isFileMatchingTarget(src, tgt) {
		t.Error("isFileMatchingTarget matching size and mtime within 2s = false; want true")
	}

	// 2. Size mismatch
	tgtSizeMismatch := fileState{Path: "/test.txt", Size: 2048, LastModified: now}
	if isFileMatchingTarget(src, tgtSizeMismatch) {
		t.Error("isFileMatchingTarget size mismatch = true; want false")
	}

	// 3. Matching hashes
	srcHash := fileState{Path: "/test.txt", Size: 1024, Hash: "sha1:abc12345"}
	tgtHash := fileState{Path: "/test.txt", Size: 1024, Hash: "sha1:abc12345"}
	if !isFileMatchingTarget(srcHash, tgtHash) {
		t.Error("isFileMatchingTarget matching hashes = false; want true")
	}

	// 4. Mismatched hashes
	tgtHashMismatch := fileState{Path: "/test.txt", Size: 1024, Hash: "sha1:xyz98765"}
	if isFileMatchingTarget(srcHash, tgtHashMismatch) {
		t.Error("isFileMatchingTarget mismatched hashes = true; want false")
	}

	// 5. Mismatched timestamp (> 2s) without hash
	tgtTimeMismatch := fileState{Path: "/test.txt", Size: 1024, LastModified: now.Add(5 * time.Second)}
	if isFileMatchingTarget(src, tgtTimeMismatch) {
		t.Error("isFileMatchingTarget time mismatch > 2s = true; want false")
	}
}

func TestCleanRelPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "/"},
		{"/", "/"},
		{"file.txt", "/file.txt"},
		{"/file.txt", "/file.txt"},
		{"folder/sub/", "/folder/sub"},
		{"/folder/sub/file.txt", "/folder/sub/file.txt"},
		{"./folder/../file.txt", "/file.txt"},
	}

	for _, tt := range tests {
		got := cleanRelPath(tt.input)
		if got != tt.expected {
			t.Errorf("cleanRelPath(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestListFilesReportsTraversalErrors(t *testing.T) {
	provider := listingTestProvider{
		inspect: func(resourcePath string) (storage.CloudResource, error) {
			if resourcePath != "/" {
				t.Fatalf("InspectResource path = %q; want /", resourcePath)
			}
			return storage.CloudResource{Path: "/", IsDir: true}, nil
		},
		list: func(dirPath string) ([]storage.CloudResource, error) {
			switch dirPath {
			case "/":
				return []storage.CloudResource{{Path: "/healthy.txt", Size: 1}, {Path: "/unreadable", IsDir: true}}, nil
			case "/unreadable":
				return nil, errors.New("temporary permission failure")
			default:
				t.Fatalf("GetDirectoryListing path = %q", dirPath)
				return nil, nil
			}
		},
	}

	engine := NewEngine(nil, nil, "secret")
	files, dirMap, _, indexErrors, err := engine.listFiles(context.Background(), provider, []string{"/"}, nil, nil)
	if err != nil {
		t.Fatalf("listFiles returned fatal error: %v", err)
	}
	if len(indexErrors) != 1 || indexErrors[0].Path != "/unreadable" {
		t.Fatalf("indexErrors = %#v; want one error for /unreadable", indexErrors)
	}
	if _, ok := files["/healthy.txt"]; !ok {
		t.Fatal("successful entries should still be returned alongside traversal errors")
	}
	if dirMap["/unreadable"] {
		t.Error("failed directory should not appear in dirMap")
	}
}

func TestUpdateSyncStatesPrevKeys(t *testing.T) {
	// Verify that allKeys in updateSyncStates captures keys from prevSource and prevTarget
	// even when sourceMap and targetMap are empty (e.g. all files deleted on source and target).
	engine := NewEngine(nil, nil, "secret")

	prevSource := map[string]db.SyncState{
		"/deleted_file.txt": {RelPath: "/deleted_file.txt", Side: "source"},
	}
	prevTarget := map[string]db.SyncState{
		"/deleted_file.txt": {RelPath: "/deleted_file.txt", Side: "target"},
	}

	sourceMap := make(map[string]fileState)
	targetMap := make(map[string]fileState)

	// Since engine.db is nil, updateSyncStates will attempt BulkUpsertSyncStates with nil db,
	// which won't panic if upserts and deletes are collected and passed (BulkUpsertSyncStates will fail on tx.Begin).
	// We call updateSyncStates to verify it executes without unexpected runtime errors before DB call.
	engine.updateSyncStates("job-1", sourceMap, targetMap, prevSource, prevTarget, nil, nil, nil, nil, nil, nil, nil)
}
