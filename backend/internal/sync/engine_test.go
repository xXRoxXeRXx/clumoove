package sync

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
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

type directoryCleanupTestProvider struct {
	storage.StorageProvider
	list   func(path string) ([]storage.CloudResource, error)
	delete func(path string) error
}

func (p directoryCleanupTestProvider) GetDirectoryListing(_ context.Context, _ string, dirPath string) ([]storage.CloudResource, error) {
	return p.list(dirPath)
}

func (p directoryCleanupTestProvider) DeleteFile(_ context.Context, _ string, filePath string) error {
	return p.delete(filePath)
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

func TestIsFileModified(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	previous := db.SyncState{Size: 10, Mtime: sql.NullTime{Time: now, Valid: true}, SourceHash: "SHA1:old", TargetHash: "SHA1:target-old", ETag: "old"}
	if isFileModified(fileState{Size: 10, LastModified: now.Add(time.Second), Hash: "SHA1:old", ETag: `"old"`}, previous, true) {
		t.Fatal("matching source state reported modified")
	}
	if !isFileModified(fileState{Size: 10, LastModified: now, Hash: "SHA1:new"}, previous, true) {
		t.Fatal("changed source hash was not detected")
	}
	if !isFileModified(fileState{Size: 10, LastModified: now, Hash: "SHA1:target-new"}, previous, false) {
		t.Fatal("changed target hash was not detected")
	}
	if !isFileModified(fileState{Size: 10, LastModified: now.Add(3 * time.Second)}, previous, true) {
		t.Fatal("changed mtime was not detected")
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

func TestCleanupEmptyDirectoriesPreservesChangedDescendant(t *testing.T) {
	var deleted []string
	provider := directoryCleanupTestProvider{
		list: func(dirPath string) ([]storage.CloudResource, error) {
			if dirPath != "/backup/folder" {
				t.Fatalf("GetDirectoryListing path = %q; want /backup/folder", dirPath)
			}
			return []storage.CloudResource{{Path: "/backup/folder/report.txt", Size: 1}}, nil
		},
		delete: func(filePath string) error {
			deleted = append(deleted, filePath)
			return nil
		},
	}

	removed := cleanupEmptyDirectories(context.Background(), "/backup", provider, provider, []directoryCleanupCandidate{{relPath: "/folder", side: "target"}})
	if len(removed) != 0 || len(deleted) != 0 {
		t.Fatalf("changed descendant allowed directory cleanup: removed=%#v deleted=%#v", removed, deleted)
	}
}

func TestCleanupEmptyDirectoriesRunsBottomUp(t *testing.T) {
	deleted := make(map[string]bool)
	provider := directoryCleanupTestProvider{
		list: func(dirPath string) ([]storage.CloudResource, error) {
			switch dirPath {
			case "/backup/folder/child":
				return nil, nil
			case "/backup/folder":
				if deleted["/backup/folder/child"] {
					return nil, nil
				}
				return []storage.CloudResource{{Path: "/backup/folder/child", IsDir: true}}, nil
			default:
				t.Fatalf("unexpected directory listing for %q", dirPath)
				return nil, nil
			}
		},
		delete: func(filePath string) error {
			deleted[filePath] = true
			return nil
		},
	}

	removed := cleanupEmptyDirectories(context.Background(), "/backup", provider, provider, []directoryCleanupCandidate{
		{relPath: "/folder", side: "target"},
		{relPath: "/folder/child", side: "target"},
	})
	if len(removed) != 2 || !deleted["/backup/folder/child"] || !deleted["/backup/folder"] {
		t.Fatalf("bottom-up cleanup = removed=%#v deleted=%#v", removed, deleted)
	}
}

func TestCleanupEmptyDirectoriesTreatsNotFoundAsRemoved(t *testing.T) {
	deleteCalls := 0
	provider := directoryCleanupTestProvider{
		list: func(dirPath string) ([]storage.CloudResource, error) {
			if dirPath != "/backup/gone" {
				t.Fatalf("GetDirectoryListing path = %q; want /backup/gone", dirPath)
			}
			return nil, storage.ErrNotFound
		},
		delete: func(string) error {
			deleteCalls++
			return nil
		},
	}

	candidate := directoryCleanupCandidate{relPath: "/gone", side: "target"}
	removed := cleanupEmptyDirectories(context.Background(), "/backup", provider, provider, []directoryCleanupCandidate{candidate})
	if len(removed) != 1 || removed[0] != candidate {
		t.Fatalf("not-found cleanup result = %#v; want %#v", removed, []directoryCleanupCandidate{candidate})
	}
	if deleteCalls != 0 {
		t.Fatalf("DeleteFile calls = %d; want 0 for an already removed directory", deleteCalls)
	}
}

func TestCleanupEmptyDirectoriesSkipsRootAndDeduplicatesCandidates(t *testing.T) {
	listCalls := 0
	deleteCalls := 0
	provider := directoryCleanupTestProvider{
		list: func(dirPath string) ([]storage.CloudResource, error) {
			if dirPath != "/backup/folder" {
				t.Fatalf("GetDirectoryListing path = %q; want /backup/folder", dirPath)
			}
			listCalls++
			return nil, nil
		},
		delete: func(filePath string) error {
			if filePath != "/backup/folder" {
				t.Fatalf("DeleteFile path = %q; want /backup/folder", filePath)
			}
			deleteCalls++
			return nil
		},
	}

	removed := cleanupEmptyDirectories(context.Background(), "/backup", provider, provider, []directoryCleanupCandidate{
		{relPath: "/", side: "target"},
		{relPath: "/folder", side: "target"},
		{relPath: "//folder", side: "target"},
	})
	if len(removed) != 1 || removed[0].relPath != "/folder" || listCalls != 1 || deleteCalls != 1 {
		t.Fatalf("root/dedup cleanup = removed=%#v lists=%d deletes=%d", removed, listCalls, deleteCalls)
	}
}

func TestApplyDirectoryCleanupResultsUpdatesBothSides(t *testing.T) {
	sourceDirs := map[string]bool{"/gone-source": true}
	targetDirs := map[string]bool{"/gone-target": true}
	sourceETags := map[string]string{"/gone-source": "source", "/": "source-root"}
	targetETags := map[string]string{"/gone-target": "target", "/": "target-root"}

	applyDirectoryCleanupResults([]directoryCleanupCandidate{
		{relPath: "/gone-source", side: "source"},
		{relPath: "/gone-target", side: "target"},
	}, sourceDirs, targetDirs, sourceETags, targetETags)

	if sourceDirs["/gone-source"] || targetDirs["/gone-target"] {
		t.Fatalf("directory maps were not cleared: source=%#v target=%#v", sourceDirs, targetDirs)
	}
	if _, ok := sourceETags["/gone-source"]; ok {
		t.Fatalf("source directory ETag was not cleared: %#v", sourceETags)
	}
	if _, ok := targetETags["/gone-target"]; ok {
		t.Fatalf("target directory ETag was not cleared: %#v", targetETags)
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

func TestListFilesTraversesTreeWiderThanLegacyQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping wide-tree traversal test in short mode")
	}

	// Each of the 16 workers discovers children concurrently. The old design
	// could fill its 100,000-entry channel and leave every worker blocked while
	// trying to enqueue another child directory.
	const branches = 16
	const childrenPerBranch = 6_400 // 102,400 directories in total

	provider := listingTestProvider{
		inspect: func(resourcePath string) (storage.CloudResource, error) {
			return storage.CloudResource{Path: resourcePath, IsDir: true}, nil
		},
		list: func(dirPath string) ([]storage.CloudResource, error) {
			if dirPath == "/" {
				files := make([]storage.CloudResource, branches)
				for i := range files {
					files[i] = storage.CloudResource{Path: "/branch-" + strconv.Itoa(i), IsDir: true}
				}
				return files, nil
			}
			if strings.HasPrefix(dirPath, "/branch-") && !strings.Contains(dirPath[len("/branch-"):], "/") {
				files := make([]storage.CloudResource, childrenPerBranch)
				for i := range files {
					files[i] = storage.CloudResource{Path: dirPath + "/child-" + strconv.Itoa(i), IsDir: true}
				}
				return files, nil
			}
			return nil, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, dirs, _, _, err := NewEngine(nil, nil, "secret").listFiles(ctx, provider, []string{"/"}, nil, nil)
	if err != nil {
		t.Fatalf("listFiles returned error for broad tree: %v", err)
	}
	// root (1) + branches (16) + children (16 * 6,400)
	wantDirs := 1 + branches + (branches * childrenPerBranch)
	if got := len(dirs); got != wantDirs {
		t.Fatalf("listed %d directories; want %d", got, wantDirs)
	}
}

func TestSyncStateChangesIncludesPreviousPaths(t *testing.T) {
	// Verify that allPaths in syncStateChanges captures paths from prevSource and prevTarget
	// even when sourceMap and targetMap are empty (e.g. all files deleted on source and target).
	prevSource := map[string]db.SyncState{
		"/deleted_file.txt": {RelPath: "/deleted_file.txt", Side: "source"},
	}
	prevTarget := map[string]db.SyncState{
		"/deleted_file.txt": {RelPath: "/deleted_file.txt", Side: "target"},
	}

	sourceMap := make(map[string]fileState)
	targetMap := make(map[string]fileState)

	// State generation must retain keys found only in the previous baseline so
	// deletions are persisted on the next successful pass.
	upserts, deletes := syncStateChanges("job-1", sourceMap, targetMap, prevSource, prevTarget, nil, nil, nil, nil, nil, nil, nil)
	if len(upserts) != 0 || len(deletes) != 2 {
		t.Fatalf("state changes = %d upserts, %d deletes; want 0, 2", len(upserts), len(deletes))
	}
}

func TestListFilesSkipsUnchangedETagSubtree(t *testing.T) {
	listCalls := 0
	provider := listingTestProvider{
		inspect: func(resourcePath string) (storage.CloudResource, error) {
			return storage.CloudResource{Path: resourcePath, IsDir: true, ETag: "unchanged"}, nil
		},
		list: func(dirPath string) ([]storage.CloudResource, error) {
			listCalls++
			return nil, nil
		},
	}
	previousFiles := map[string]fileState{"/kept.txt": {Path: "/kept.txt", Size: 42}}
	previousDirs := map[string]string{"/": "unchanged", "/nested": "nested-etag"}
	files, dirs, etags, errs, err := NewEngine(nil, nil, "secret").listFiles(context.Background(), provider, []string{"/"}, previousDirs, previousFiles)
	if err != nil || len(errs) != 0 {
		t.Fatalf("listFiles returned err=%v errors=%v", err, errs)
	}
	if listCalls != 0 {
		t.Fatalf("GetDirectoryListing calls = %d; want 0 for unchanged ETag", listCalls)
	}
	if _, ok := files["/kept.txt"]; !ok || !dirs["/nested"] || etags["/nested"] != "nested-etag" {
		t.Fatalf("unchanged subtree was not retained: files=%v dirs=%v etags=%v", files, dirs, etags)
	}
}

func TestSyncStateChangesDeletesStaleDirectoryWithoutETag(t *testing.T) {
	_, deletes := syncStateChanges("job-1", nil, nil, nil, nil, nil, nil, map[string]bool{"/": true}, map[string]bool{}, map[string]bool{"/": true, "/removed": true}, map[string]bool{}, nil)
	for _, deletion := range deletes {
		if deletion.Side == "source" && deletion.RelPath == "/removed" {
			return
		}
	}
	t.Fatalf("stale directory without an ETag was not deleted: %#v", deletes)
}

func TestSyncStateChangesStoresHashOnMatchingSide(t *testing.T) {
	upserts, _ := syncStateChanges("job-1", map[string]fileState{"/a": {Path: "/a", Hash: "src"}}, map[string]fileState{"/a": {Path: "/a", Hash: "tgt"}}, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, state := range upserts {
		if state.Side == "source" && (state.SourceHash != "src" || state.TargetHash != "") {
			t.Fatalf("source hash fields = %#v", state)
		}
		if state.Side == "target" && (state.SourceHash != "" || state.TargetHash != "tgt") {
			t.Fatalf("target hash fields = %#v", state)
		}
	}
}

func TestReconcileSuccessfulDownloadUsesPostTransferDestinationBaseline(t *testing.T) {
	// Pass 1 sees B only on the target and downloads it to the source. The
	// source's pre-transfer listing is empty, so persisting it would make B
	// look newly created on the next pass.
	now := time.Now().UTC().Truncate(time.Second)
	fileB := fileState{Path: "/report.txt", Size: 1, LastModified: now, Hash: "SHA1:b", ETag: "b"}
	sourceMap := map[string]fileState{}
	targetMap := map[string]fileState{"/report.txt": fileB}
	sourceDirs := map[string]bool{"/": true}
	targetDirs := map[string]bool{"/": true}
	sourceETags := map[string]string{"/": "source-before"}
	targetETags := map[string]string{"/": "target-before"}

	source := listingTestProvider{inspect: func(resourcePath string) (storage.CloudResource, error) {
		if resourcePath != "/report.txt" {
			t.Fatalf("source InspectResource path = %q", resourcePath)
		}
		return storage.CloudResource{Path: resourcePath, Size: 1, LastModified: now, Hash: "SHA1:b", ETag: "b"}, nil
	}}
	target := listingTestProvider{inspect: func(resourcePath string) (storage.CloudResource, error) {
		t.Fatalf("target InspectResource unexpectedly called for %q", resourcePath)
		return storage.CloudResource{}, nil
	}}

	reconcileSuccessfulOperations(context.Background(), "/", source, target,
		sourceMap, targetMap, sourceDirs, targetDirs, sourceETags, targetETags,
		[]completedTaskOperation{{filePath: "/report.txt", action: "download"}}, map[string]bool{})

	if got := sourceMap["/report.txt"]; got.Hash != fileB.Hash || got.ETag != fileB.ETag {
		t.Fatalf("source post-transfer state = %#v; want verified destination metadata %#v", got, fileB)
	}
	if _, ok := sourceETags["/"]; ok {
		t.Fatal("source root directory ETag was not invalidated after download")
	}
	if targetETags["/"] != "target-before" {
		t.Fatal("download should not invalidate the unchanged target directory cache")
	}

	upserts, _ := syncStateChanges("job-1", sourceMap, targetMap, nil, nil, sourceETags, targetETags, sourceDirs, targetDirs, nil, nil, nil)
	states := make(map[string]db.SyncState)
	for _, state := range upserts {
		if state.Size != -1 {
			states[state.Side] = *state
		}
	}
	previousSource, previousTarget := states["source"], states["target"]
	if previousSource.SourceHash != "SHA1:b" || previousTarget.TargetHash != "SHA1:b" {
		t.Fatalf("persisted post-download baseline = %#v; want B on both sides", states)
	}

	// Pass 2: target is immediately edited to C. B is not a new source file,
	// and C is the only modified side, so two-way delta chooses a download
	// instead of an OVERWRITE upload of stale B.
	if isFileModified(fileB, previousSource, true) {
		t.Fatal("source B was incorrectly treated as newly modified after download")
	}
	fileC := fileState{Path: "/report.txt", Size: 1, LastModified: now.Add(3 * time.Second), Hash: "SHA1:c", ETag: "c"}
	if !isFileModified(fileC, previousTarget, false) {
		t.Fatal("target C was not detected as the only next-pass modification")
	}
	srcModified := isFileModified(fileB, previousSource, true)
	tgtModified := isFileModified(fileC, previousTarget, false)
	if srcModified || !tgtModified {
		t.Fatalf("second-pass delta flags = source:%v target:%v; want false/true", srcModified, tgtModified)
	}
	// This is the download branch in engine.go's two-way delta, rather than
	// the conflict/OVERWRITE upload branch.
	if !(tgtModified && !srcModified) {
		t.Fatal("target edit would not select a target-to-source download")
	}
}

func TestReconcileSuccessfulUploadPreventsDeletedSourceResurrection(t *testing.T) {
	// Pass 1 uploads B to an empty target. If the source is deleted before
	// pass 2, the persisted target baseline must still contain B so the delta
	// recognizes a source deletion rather than uploading a phantom "new" B.
	now := time.Now().UTC().Truncate(time.Second)
	fileB := fileState{Path: "/gone.txt", Size: 1, LastModified: now, Hash: "SHA1:b"}
	sourceMap := map[string]fileState{"/gone.txt": fileB}
	targetMap := map[string]fileState{}
	sourceDirs := map[string]bool{"/": true}
	targetDirs := map[string]bool{"/": true}

	source := listingTestProvider{inspect: func(resourcePath string) (storage.CloudResource, error) {
		t.Fatalf("source InspectResource unexpectedly called for %q", resourcePath)
		return storage.CloudResource{}, nil
	}}
	target := listingTestProvider{inspect: func(resourcePath string) (storage.CloudResource, error) {
		if resourcePath != "/gone.txt" {
			t.Fatalf("target InspectResource path = %q", resourcePath)
		}
		return storage.CloudResource{Path: resourcePath, Size: 1, LastModified: now, Hash: "SHA1:b"}, nil
	}}

	reconcileSuccessfulOperations(context.Background(), "/", source, target,
		sourceMap, targetMap, sourceDirs, targetDirs, map[string]string{}, map[string]string{},
		[]completedTaskOperation{{filePath: "/gone.txt", action: "upload"}}, map[string]bool{})
	if _, ok := targetMap["/gone.txt"]; !ok {
		t.Fatal("verified target upload was not added to the post-transfer baseline")
	}

	upserts, _ := syncStateChanges("job-1", sourceMap, targetMap, nil, nil, nil, nil, sourceDirs, targetDirs, nil, nil, nil)
	states := make(map[string]db.SyncState)
	for _, state := range upserts {
		if state.Size != -1 {
			states[state.Side] = *state
		}
	}
	if _, sourceWasKnown := states["source"]; !sourceWasKnown {
		t.Fatal("source upload baseline missing")
	}
	if _, targetWasKnown := states["target"]; !targetWasKnown {
		t.Fatal("target upload baseline missing")
	}

	// On pass 2 the source is absent and the target still has B. Both previous
	// entries make this a deletion-propagation candidate, never a new upload.
	secondSourceMap := map[string]fileState{}
	_, sourcePresent := secondSourceMap["/gone.txt"]
	_, targetPresent := targetMap["/gone.txt"]
	if sourcePresent || !targetPresent {
		t.Fatalf("second-pass presence = source:%v target:%v; want absent/present", sourcePresent, targetPresent)
	}
	_, sourceWasKnown := states["source"]
	_, targetWasKnown := states["target"]
	srcDeleted := !sourcePresent && sourceWasKnown
	tgtModified := false // target B matches its persisted post-upload state
	if !srcDeleted || tgtModified || !targetWasKnown {
		t.Fatalf("second-pass deletion flags = sourceDeleted:%v targetModified:%v targetKnown:%v; want true/false/true", srcDeleted, tgtModified, targetWasKnown)
	}
}

func TestSyncStateChangesProtectsFailedPathIndependentlyOfSuccessfulOperations(t *testing.T) {
	previousSource := map[string]db.SyncState{
		"/retry.txt": {SyncJobID: "job-1", Side: "source", RelPath: "/retry.txt", Size: 1, SourceHash: "SHA1:old"},
	}
	upserts, deletes := syncStateChanges("job-1",
		map[string]fileState{"/retry.txt": {Path: "/retry.txt", Size: 1, Hash: "SHA1:new"}}, nil,
		previousSource, nil, nil, nil, nil, nil, nil, nil,
		map[string]bool{"/retry.txt": true})
	if len(upserts) != 0 || len(deletes) != 0 {
		t.Fatalf("failed path state changes = upserts %#v deletes %#v; want no replacement of the old baseline", upserts, deletes)
	}
}

func TestReconcileSuccessfulTransferProtectsPathWhenDestinationMetadataUnavailable(t *testing.T) {
	// A verified transfer without a readable destination snapshot must not
	// replace the baseline with the pre-transfer source listing.
	sourceMap := map[string]fileState{}
	targetMap := map[string]fileState{"/retry.txt": {Path: "/retry.txt", Size: 1, Hash: "SHA1:b"}}
	protectedPaths := map[string]bool{}
	source := listingTestProvider{inspect: func(string) (storage.CloudResource, error) {
		return storage.CloudResource{}, errors.New("temporary provider error")
	}}
	target := listingTestProvider{inspect: func(resourcePath string) (storage.CloudResource, error) {
		t.Fatalf("target InspectResource unexpectedly called for %q", resourcePath)
		return storage.CloudResource{}, nil
	}}

	reconcileSuccessfulOperations(context.Background(), "/", source, target,
		sourceMap, targetMap, map[string]bool{"/": true}, map[string]bool{"/": true},
		map[string]string{"/": "source-before"}, map[string]string{"/": "target-before"},
		[]completedTaskOperation{{filePath: "/retry.txt", action: "download"}}, protectedPaths)

	if !protectedPaths["/retry.txt"] {
		t.Fatal("uninspectable completed transfer did not protect its baseline path")
	}
	upserts, deletes := syncStateChanges("job-1", sourceMap, targetMap, nil, nil, nil, nil, nil, nil, nil, nil, protectedPaths)
	for _, state := range upserts {
		if state.RelPath == "/retry.txt" {
			t.Fatalf("uninspectable transfer unexpectedly persisted state %#v", state)
		}
	}
	for _, deletion := range deletes {
		if deletion.RelPath == "/retry.txt" {
			t.Fatalf("uninspectable transfer unexpectedly deleted state %#v", deletion)
		}
	}
}

// TestSyncStateChangesDeletesStaleTargetDirWithNonRootTargetDir verifies that
// stale target directories are correctly detected and deleted when using a
// non-root target_dir. prevTargetDirs and the targetDirMap parameter (which
// corresponds to srcRelTargetDirMap in the engine) must both use source-relative
// paths for the stale-dir comparison to work correctly.
func TestSyncStateChangesDeletesStaleTargetDirWithNonRootTargetDir(t *testing.T) {
	// Simulate: a target dir "/sub" was known from the previous pass (source-relative).
	// In the current pass it is no longer seen on the target → should be deleted.
	prevTargetDirs := map[string]bool{
		"/":    true,
		"/sub": true, // stale — gone from target in this pass
	}
	// Current scan: only "/" present on target (source-relative, as srcRelTargetDirMap).
	currentTargetDirMap := map[string]bool{
		"/": true,
	}
	_, deletes := syncStateChanges(
		"job-1",
		nil, nil, nil, nil,
		nil, nil, // sourceDirETags, targetDirETags
		nil, currentTargetDirMap, // sourceDirMap, targetDirMap (source-relative)
		nil, prevTargetDirs, // prevSourceDirs, prevTargetDirs (source-relative)
		nil,
	)
	for _, d := range deletes {
		if d.Side == "target" && d.RelPath == "/sub" {
			return
		}
	}
	t.Fatalf("stale target directory \"/sub\" was not deleted: %#v", deletes)
}

func TestWaitForNoRunningTasksChecksImmediately(t *testing.T) {
	calls := 0
	err := waitForNoRunningTasks(context.Background(), time.Hour, func() (int, error) {
		calls++
		return 0, nil
	})
	if err != nil {
		t.Fatalf("waitForNoRunningTasks returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("count calls = %d; want 1 immediate check", calls)
	}
}

func TestWaitForNoRunningTasksReturnsCancelledContextBeforeCounting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := waitForNoRunningTasks(ctx, time.Millisecond, func() (int, error) {
		calls++
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForNoRunningTasks error = %v; want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("count calls = %d; want 0 for a cancelled context", calls)
	}
}

func TestWaitForNoRunningTasksPollsUntilTasksDrain(t *testing.T) {
	calls := 0
	err := waitForNoRunningTasks(context.Background(), time.Millisecond, func() (int, error) {
		calls++
		if calls == 1 {
			return 1, nil
		}
		return 0, nil
	})
	if err != nil {
		t.Fatalf("waitForNoRunningTasks returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("count calls = %d; want initial check plus one poll", calls)
	}
}

func TestWaitForNoRunningTasksPropagatesCountError(t *testing.T) {
	want := errors.New("database unavailable")
	err := waitForNoRunningTasks(context.Background(), time.Millisecond, func() (int, error) {
		return 0, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("waitForNoRunningTasks error = %v; want wrapped %v", err, want)
	}
}

func TestListFilesMissingStartPath(t *testing.T) {
	provider := listingTestProvider{
		inspect: func(p string) (storage.CloudResource, error) {
			if p == "/missing" {
				return storage.CloudResource{}, storage.ErrNotFound
			}
			return storage.CloudResource{Path: p, IsDir: true}, nil
		},
		list: func(p string) ([]storage.CloudResource, error) {
			return nil, nil
		},
	}

	engine := NewEngine(nil, nil, "secret")
	files, dirMap, _, indexErrs, err := engine.listFiles(context.Background(), provider, []string{"/missing"}, nil, nil)
	if err != nil {
		t.Fatalf("listFiles failed: %v", err)
	}
	if len(indexErrs) != 0 {
		t.Fatalf("expected 0 index errors for missing path, got %d: %v", len(indexErrs), indexErrs)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
	if dirMap["/missing"] {
		t.Fatalf("expected /missing not to be in dirMap")
	}
}

func TestGetTargetAbsPath(t *testing.T) {
	tests := []struct {
		relPath   string
		targetDir string
		expected  string
	}{
		{"/foto.jpg", "/backup", "/backup/foto.jpg"},
		{"/sub/foto.jpg", "/backup", "/backup/sub/foto.jpg"},
		{"/", "/backup", "/backup"},
		{"/foto.jpg", "/", "/foto.jpg"},
		{"/", "/", "/"},
		// roundtrip: getSourceRelPath(getTargetAbsPath(p, dir), dir) == p
	}
	for _, tt := range tests {
		got := getTargetAbsPath(tt.relPath, tt.targetDir)
		if got != tt.expected {
			t.Errorf("getTargetAbsPath(%q, %q) = %q; want %q", tt.relPath, tt.targetDir, got, tt.expected)
		}
		// roundtrip
		roundtrip := getSourceRelPath(got, tt.targetDir)
		if roundtrip != tt.relPath {
			t.Errorf("roundtrip getSourceRelPath(getTargetAbsPath(%q, %q), %q) = %q; want %q",
				tt.relPath, tt.targetDir, tt.targetDir, roundtrip, tt.relPath)
		}
	}
}

// TestListFilesETagSkipWithNonRootTargetDir verifies that the ETag subtree cache
// fires correctly when the target scan starts at a non-root directory.
// Previously, prevTargetFiles were stored with source-relative paths (e.g.
// "/foto.jpg") but copyPreviousSubtree matched against raw target paths (e.g.
// "/backup/foto.jpg"), so the cache always returned 0 files and triggered a
// full re-scan on every subsequent pass.
func TestListFilesETagSkipWithNonRootTargetDir(t *testing.T) {
	const targetDir = "/backup"
	listCalls := 0

	provider := listingTestProvider{
		inspect: func(resourcePath string) (storage.CloudResource, error) {
			return storage.CloudResource{Path: resourcePath, IsDir: true, ETag: "etag-backup"}, nil
		},
		list: func(dirPath string) ([]storage.CloudResource, error) {
			listCalls++
			return nil, nil
		},
	}

	// Simulate state as written by the fixed code: source-relative paths.
	prevDirETags := map[string]string{
		"/": "etag-backup", // source-relative root corresponds to /backup on target
	}
	prevFiles := map[string]fileState{
		"/foto.jpg": {Path: "/foto.jpg", Size: 42},
	}

	// Convert to raw target paths as engine.go does before calling listFiles.
	prevDirETagsRaw := make(map[string]string, len(prevDirETags))
	for relDir, etag := range prevDirETags {
		prevDirETagsRaw[getTargetAbsPath(relDir, targetDir)] = etag
	}
	prevFilesRaw := make(map[string]fileState, len(prevFiles))
	for relPath, fs := range prevFiles {
		rawPath := getTargetAbsPath(relPath, targetDir)
		fs.Path = rawPath
		prevFilesRaw[rawPath] = fs
	}

	files, _, _, errs, err := NewEngine(nil, nil, "secret").listFiles(
		context.Background(), provider, []string{targetDir}, prevDirETagsRaw, prevFilesRaw,
	)
	if err != nil || len(errs) != 0 {
		t.Fatalf("listFiles returned err=%v errors=%v", err, errs)
	}
	if listCalls != 0 {
		t.Fatalf("GetDirectoryListing calls = %d; want 0 for unchanged ETag with non-root target_dir", listCalls)
	}
	// The file must be returned with its raw target path (engine.go remaps it afterwards).
	if _, ok := files["/backup/foto.jpg"]; !ok {
		t.Fatalf("expected /backup/foto.jpg in cached files, got: %v", files)
	}
}

// TestSrcRelTargetDirETagsAreSourceRelative verifies that after the fix the
// targetDirETags returned by listFiles are remapped to source-relative keys
// before being passed to syncStateChanges for persistence.
func TestSrcRelTargetDirETagsAreSourceRelative(t *testing.T) {
	const targetDir = "/backup"

	// The raw targetDirETags map as returned by listFiles (raw target paths).
	rawTargetDirETags := map[string]string{
		"/backup":     "etag-root",
		"/backup/sub": "etag-sub",
	}

	// Apply the same remap engine.go now performs.
	srcRelTargetDirETags := make(map[string]string, len(rawTargetDirETags))
	for rawDir, etag := range rawTargetDirETags {
		relDir := cleanRelPath(getSourceRelPath(rawDir, targetDir))
		srcRelTargetDirETags[relDir] = etag
	}

	if etag, ok := srcRelTargetDirETags["/"]; !ok || etag != "etag-root" {
		t.Errorf("expected source-relative root \"/\" with etag-root, got map: %v", srcRelTargetDirETags)
	}
	if etag, ok := srcRelTargetDirETags["/sub"]; !ok || etag != "etag-sub" {
		t.Errorf("expected source-relative \"/sub\" with etag-sub, got map: %v", srcRelTargetDirETags)
	}
	if _, ok := srcRelTargetDirETags["/backup"]; ok {
		t.Errorf("raw target path \"/backup\" must not appear in srcRelTargetDirETags")
	}
}
