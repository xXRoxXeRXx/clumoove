package processor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

func TestFormatWorkerHashValueUsesQuickXorBase64(t *testing.T) {
	h := storage.NewQuickXorHasher()
	_, _ = h.Write([]byte("quickxor"))
	if got, want := formatWorkerHashValue("QUICKXOR", h), base64.StdEncoding.EncodeToString(h.Sum(nil)); got != want {
		t.Fatalf("formatWorkerHashValue() = %q, want %q", got, want)
	}
}

type verifierProvider struct {
	fakeProvider
	hash             string
	hashErr          error
	exists           bool
	size             int64
	fileExistsErr    error
	fileExistsCalls  int
	hashCalls        int
	verificationMode storage.VerificationMode
}

type connectableVerifierProvider struct {
	verifierProvider
	connected  bool
	connectErr error
	connects   int
}

func (p *connectableVerifierProvider) Connect(context.Context) (bool, error) {
	p.connects++
	return p.connected, p.connectErr
}

func (p *verifierProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	p.fileExistsCalls++
	return p.exists, p.size, p.fileExistsErr
}

func (p *verifierProvider) GetFileHash(context.Context, string, string) (string, error) {
	p.hashCalls++
	return p.hash, p.hashErr
}

func (p *verifierProvider) VerificationMode() storage.VerificationMode {
	if p.verificationMode == "" {
		return storage.VerificationCryptographicHash
	}
	return p.verificationMode
}

func TestVerificationPassConnectsConstructedTargetProvider(t *testing.T) {
	provider := &connectableVerifierProvider{verifierProvider: verifierProvider{
		exists:           true,
		size:             3,
		verificationMode: storage.VerificationSizeOnly,
	}, connected: true}
	verified := false
	(&Processor{}).runVerificationPass(context.Background(), verificationPassConfig{
		EntityType: "Migration",
		EntityID:   "connect-target",
		NewTargetProvider: func(context.Context, string, string, string, string) (storage.StorageProvider, error) {
			return provider, nil
		},
		GetTasks: func(context.Context) ([]*db.Task, error) {
			return []*db.Task{{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 3}}, nil
		},
		ReconcileProgress: func() error { return nil },
		MarkVerified: func(context.Context, *db.Task, string) (bool, error) {
			verified = true
			return true, nil
		},
	})
	if provider.connects != 1 {
		t.Fatalf("target Connect calls = %d, want 1", provider.connects)
	}
	if !verified {
		t.Fatal("connected target was not verified")
	}
}

func TestBestSourceHash(t *testing.T) {
	cases := []struct {
		name       string
		workerHash sql.NullString
		sourceHash sql.NullString
		want       string
	}{
		{
			name:       "prefer cryptographic source hash over worker hash",
			workerHash: sql.NullString{String: "SHA1:abc123456789", Valid: true},
			sourceHash: sql.NullString{String: "SHA1:def987654321", Valid: true},
			want:       "SHA1:def987654321",
		},
		{
			name:       "prefer cryptographic source hash if worker hash is etag",
			workerHash: sql.NullString{String: "ETAG:\"etag123\"", Valid: true},
			sourceHash: sql.NullString{String: "SHA256:fedcba9876543210", Valid: true},
			want:       "SHA256:fedcba9876543210",
		},
		{
			name:       "fallback to cryptographic source hash if worker hash is empty",
			workerHash: sql.NullString{String: "", Valid: false},
			sourceHash: sql.NullString{String: "MD5:0123456789abcdef", Valid: true},
			want:       "MD5:0123456789abcdef",
		},
		{
			name:       "prefer source etag if both are etags",
			workerHash: sql.NullString{String: "ETAG:\"worker-etag\"", Valid: true},
			sourceHash: sql.NullString{String: "ETAG:\"source-etag\"", Valid: true},
			want:       "ETAG:\"source-etag\"",
		},
		{
			name:       "fallback to source etag if worker hash is invalid",
			workerHash: sql.NullString{String: "", Valid: false},
			sourceHash: sql.NullString{String: "ETAG:\"source-etag\"", Valid: true},
			want:       "ETAG:\"source-etag\"",
		},
		{
			name:       "both empty",
			workerHash: sql.NullString{String: "", Valid: false},
			sourceHash: sql.NullString{String: "", Valid: false},
			want:       "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task := &db.Task{
				WorkerHash: c.workerHash,
				SourceHash: c.sourceHash,
			}
			got := bestSourceHash(task)
			if got != c.want {
				t.Errorf("bestSourceHash() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsComparableHash(t *testing.T) {
	tests := []struct {
		algo string
		want bool
	}{
		{"SHA1", true},
		{"sha1", true},
		{"SHA256", true},
		{"MD5", true},
		{"SHA512", true},
		{"DROPBOX", true},
		{"QUICKXOR", true},
		{"ETAG", false},
		{"ETAG_MATCH", false},
		{"UNKNOWN", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.algo, func(t *testing.T) {
			if got := isComparableHash(tc.algo); got != tc.want {
				t.Errorf("isComparableHash(%q) = %v, want %v", tc.algo, got, tc.want)
			}
		})
	}
}

func TestIsNonRetryableHashError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, true},
		{"sentinel ErrChecksumNotAvailable", storage.ErrChecksumNotAvailable, true},
		{"sentinel ErrHashNotSupported", storage.ErrHashNotSupported, true},
		{"ErrNotFound remains retryable", storage.ErrNotFound, false},
		{"wrapped ErrChecksumNotAvailable", fmt.Errorf("provider error: %w", storage.ErrChecksumNotAvailable), true},
		{"substring checksum not available", errors.New("webdav: checksum not available"), true},
		{"substring hash not supported", errors.New("sftp: hash not supported for resource"), true},
		{"substring is a directory", errors.New("read bbb: is a directory"), true},
		{"transient 404 error (should retry)", errors.New("nextcloud 404 file not found"), false},
		{"transient network timeout (should retry)", errors.New("dial tcp 1.2.3.4:443: i/o timeout"), false},
		{"transient 502 Bad Gateway (should retry)", errors.New("502 bad gateway"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNonRetryableHashError(tc.err); got != tc.want {
				t.Errorf("isNonRetryableHashError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWorkerCapacity(t *testing.T) {
	for _, tc := range []struct {
		maxThreads, transfers, verifiers int
	}{
		{1, 1, 1},
		{2, 2, 2},
		{4, 4, 4},
		{16, 16, 4},
	} {
		transfers, verifiers := workerCapacity(tc.maxThreads)
		if transfers != tc.transfers || verifiers != tc.verifiers {
			t.Errorf("workerCapacity(%d) = (%d, %d), want (%d, %d)", tc.maxThreads, transfers, verifiers, tc.transfers, tc.verifiers)
		}
		if transfers != tc.maxThreads {
			t.Errorf("workerCapacity(%d) must retain all transfer workers", tc.maxThreads)
		}
	}
}

func TestVerificationDispatcherDeduplicatesQueuedEntity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Processor{verificationWorkers: 1, migrationVerificationQueue: make(chan verificationWork, 2), syncVerificationQueue: make(chan verificationWork, 2), providerSlots: make(chan struct{}, 1)}
	p.startVerificationDispatcher(ctx)
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int32
	run := func(context.Context) {
		runs.Add(1)
		close(started)
		<-release
	}
	p.scheduleVerification(ctx, "migration", "same", run)
	p.scheduleVerification(ctx, "migration", "same", run)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("verification work did not start")
	}
	close(release)
	cancel()
	p.verificationWG.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("verification runs = %d, want 1", got)
	}
}

func TestVerificationDispatcherServesSyncWhileMigrationsAreQueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Processor{verificationWorkers: 1, migrationVerificationQueue: make(chan verificationWork, 2), syncVerificationQueue: make(chan verificationWork, 2), providerSlots: make(chan struct{}, 1)}
	p.startVerificationDispatcher(ctx)

	started := make(chan string, 3)
	releaseFirstMigration := make(chan struct{})
	p.scheduleVerification(ctx, "migration", "first", func(context.Context) {
		started <- "migration:first"
		<-releaseFirstMigration
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first migration verification did not start")
	}
	p.scheduleVerification(ctx, "migration", "second", func(context.Context) { started <- "migration:second" })
	p.scheduleVerification(ctx, "sync", "waiting", func(context.Context) { started <- "sync:waiting" })
	close(releaseFirstMigration)

	select {
	case got := <-started:
		if got != "sync:waiting" {
			t.Fatalf("next verification = %q, want sync work to avoid starvation", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sync verification was starved by queued migrations")
	}
	cancel()
	p.verificationWG.Wait()
}

func TestVerificationPassMarksDirectoryAsVerified(t *testing.T) {
	provider := &verifierProvider{
		hashErr: errors.New("read bbb: is a directory"),
		exists:  true,
		size:    4096,
	}
	task := &db.Task{ID: "dir-task", ResourceType: "files", FilePath: "/bbb", FileSize: 0}
	verified := false
	mismatched := false
	p := &Processor{}
	p.runVerificationPass(context.Background(), verificationPassConfig{
		EntityType:        "Migration",
		EntityID:          "dir-test",
		Threads:           1,
		TargetClient:      provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified: func(context.Context, *db.Task, string) (bool, error) {
			verified = true
			return true, nil
		},
		MarkMismatch: func(_ context.Context, got *db.Task) (bool, error) {
			mismatched = true
			return true, nil
		},
	})

	if !verified {
		t.Fatal("expected directory task returning 'is a directory' error to be marked verified")
	}
	if mismatched {
		t.Fatal("directory task should not have been marked as mismatched")
	}
}

func TestVerificationFallbackRejectsMissingOrTruncatedTarget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hash       string
		hashErr    error
		sourceHash sql.NullString
		exists     bool
		size       int64
	}{
		{name: "hash unavailable, missing target", hashErr: storage.ErrHashNotSupported, exists: false, size: 0},
		{name: "hash unavailable, truncated target", hashErr: storage.ErrHashNotSupported, exists: true, size: 7},
		{name: "different algorithms, truncated target", hash: "MD5:target-md5", sourceHash: sql.NullString{String: "SHA1:source-sha1", Valid: true}, exists: true, size: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &verifierProvider{
				hash:    tc.hash,
				hashErr: tc.hashErr,
				exists:  tc.exists,
				size:    tc.size,
			}
			task := &db.Task{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 10, SourceHash: tc.sourceHash}
			verified := false
			mismatched := false
			p := &Processor{}
			p.runVerificationPass(context.Background(), verificationPassConfig{
				EntityType:        "Migration",
				EntityID:          tc.name,
				Threads:           1,
				TargetClient:      provider,
				GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
				ReconcileProgress: func() error { return nil },
				MarkVerified: func(context.Context, *db.Task, string) (bool, error) {
					verified = true
					return true, nil
				},
				MarkMismatch: func(_ context.Context, got *db.Task) (bool, error) {
					mismatched = got.Status == "FAILED"
					return true, nil
				},
			})

			if verified {
				t.Fatal("fallback verified a missing or truncated target")
			}
			if !mismatched {
				t.Fatal("fallback did not mark the invalid target as mismatched")
			}
		})
	}
}

func TestVerificationSizeOnlySkipsHashQueryAndRejectsSizeMismatch(t *testing.T) {
	provider := &verifierProvider{
		verificationMode: storage.VerificationSizeOnly,
		exists:           true,
		size:             7,
	}
	task := &db.Task{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 10}
	verified, mismatched := false, false
	p := &Processor{}
	p.runVerificationPass(context.Background(), verificationPassConfig{
		EntityType: "Migration", EntityID: "size-only", Threads: 1, TargetClient: provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified:      func(context.Context, *db.Task, string) (bool, error) { verified = true; return true, nil },
		MarkMismatch:      func(context.Context, *db.Task) (bool, error) { mismatched = true; return true, nil },
	})
	if provider.hashCalls != 0 {
		t.Fatalf("GetFileHash calls = %d, want 0 for size_only target", provider.hashCalls)
	}
	if provider.fileExistsCalls != 1 {
		t.Fatalf("FileExists calls = %d, want 1", provider.fileExistsCalls)
	}
	if verified || !mismatched {
		t.Fatal("size_only target with wrong size must be marked mismatched")
	}
}

func TestVerificationNoneSkipsAllChecksAndLeavesTaskUnverified(t *testing.T) {
	provider := &verifierProvider{verificationMode: storage.VerificationNone}
	task := &db.Task{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 5}
	verified, mismatched := false, false
	(&Processor{}).runVerificationPass(context.Background(), verificationPassConfig{
		EntityType: "Migration", EntityID: "none", Threads: 1, TargetClient: provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified:      func(context.Context, *db.Task, string) (bool, error) { verified = true; return true, nil },
		MarkMismatch:      func(context.Context, *db.Task) (bool, error) { mismatched = true; return true, nil },
	})
	if provider.hashCalls != 0 || provider.fileExistsCalls != 0 {
		t.Fatal("VerificationNone must not make provider calls")
	}
	if verified || mismatched {
		t.Fatal("VerificationNone must not mutate task state")
	}
}

func TestVerificationDifferentAlgorithmsRequiresSizeAndPersistsTargetHash(t *testing.T) {
	provider := &verifierProvider{hash: "MD5:target-md5", exists: true, size: 10}
	task := &db.Task{
		ID:           "task",
		ResourceType: "files",
		FilePath:     "/file",
		FileSize:     10,
		SourceHash:   sql.NullString{String: "SHA1:source-sha1", Valid: true},
	}
	var persistedTargetHash string
	p := &Processor{}
	p.runVerificationPass(context.Background(), verificationPassConfig{
		EntityType:        "Migration",
		EntityID:          "different-algorithms",
		Threads:           1,
		TargetClient:      provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified: func(_ context.Context, got *db.Task, targetHash string) (bool, error) {
			if got.SourceHash.String != "SHA1:source-sha1" {
				t.Fatalf("source hash changed before persistence: %q", got.SourceHash.String)
			}
			persistedTargetHash = targetHash
			return true, nil
		},
	})

	if persistedTargetHash != "MD5:target-md5" {
		t.Fatalf("persisted target hash = %q, want provider hash", persistedTargetHash)
	}
	if provider.fileExistsCalls == 0 {
		t.Fatal("different-algorithm fallback marked verified without a target size query")
	}
}

func TestVerificationFallbackLeavesTaskUnverifiedWhenSizeQueryFails(t *testing.T) {
	provider := &verifierProvider{
		hashErr:       storage.ErrHashNotSupported,
		fileExistsErr: errors.New("target unavailable"),
	}
	task := &db.Task{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 10}
	verified := false
	mismatched := false
	p := &Processor{}
	p.runVerificationPass(context.Background(), verificationPassConfig{
		EntityType:        "Migration",
		EntityID:          "size-query-failure",
		Threads:           1,
		TargetProvider:    "webdav",
		TargetClient:      provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified: func(context.Context, *db.Task, string) (bool, error) {
			verified = true
			return true, nil
		},
		MarkMismatch: func(context.Context, *db.Task) (bool, error) {
			mismatched = true
			return true, nil
		},
	})

	if provider.fileExistsCalls != 3 {
		t.Fatalf("FileExists calls = %d, want 3 retries", provider.fileExistsCalls)
	}
	if verified || mismatched {
		t.Fatal("failed fallback size query must leave the task unverified")
	}
}

func TestVerificationRetriesNonImmichNotFoundHash(t *testing.T) {
	provider := &verifierProvider{
		hashErr: storage.ErrNotFound,
		exists:  true,
		size:    10,
	}
	task := &db.Task{ID: "task", ResourceType: "files", FilePath: "/file", FileSize: 10}
	verified := false
	(&Processor{}).runVerificationPass(context.Background(), verificationPassConfig{
		EntityType: "Migration", EntityID: "retry-not-found", TargetProvider: "nextcloud", Threads: 1, TargetClient: provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified:      func(context.Context, *db.Task, string) (bool, error) { verified = true; return true, nil },
	})
	if provider.hashCalls != 3 {
		t.Fatalf("GetFileHash calls = %d, want 3", provider.hashCalls)
	}
	if !verified {
		t.Fatal("non-Immich not-found hash fallback should verify the target size")
	}
}

func TestVerificationPassImmichTargetPathResolution(t *testing.T) {
	var requestedPath string
	provider := &verifierProvider{
		hash:   "SHA1:abcd1234efgh5678",
		exists: true,
		size:   2000,
	}
	// Custom provider mock to track the exact path checked during verification
	trackProvider := &pathTrackingVerifierProvider{
		verifierProvider: provider,
		onHash: func(p string) {
			requestedPath = p
		},
	}

	task := &db.Task{
		ID:           "immich-task-1",
		ResourceType: "files",
		FilePath:     "/asset-uuid-456",
		FileSize:     2000,
		Metadata:     []byte(`{"immich_filename":"sunset.jpg"}`),
		SourceHash:   sql.NullString{String: "SHA1:abcd1234efgh5678", Valid: true},
	}

	verified := false
	p := &Processor{}
	p.runVerificationPass(context.Background(), verificationPassConfig{
		EntityType:        "Migration",
		EntityID:          "immich-verification-test",
		Threads:           1,
		SourceProvider:    "immich",
		TargetProvider:    "nextcloud",
		TargetDir:         "/Immich Alben",
		TargetClient:      trackProvider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified: func(_ context.Context, _ *db.Task, _ string) (bool, error) {
			verified = true
			return true, nil
		},
	})

	if !verified {
		t.Fatal("expected Immich task to be marked verified")
	}

	wantPath := "/Immich Alben/sunset.jpg"
	if requestedPath != wantPath {
		t.Fatalf("verifier requested path %q, want %q", requestedPath, wantPath)
	}
}

func TestVerificationPassImmichUsesPersistedTargetAssetID(t *testing.T) {
	checksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xab}, 20))
	canonicalHash := "SHA1:" + "abababababababababababababababababababab"
	for _, tc := range []struct {
		name       string
		status     int
		checksum   string
		sourceHash sql.NullString
		workerHash sql.NullString
		wantVerify bool
		wantFail   bool
		wantCalls  int
	}{
		{
			name:       "sha1 match",
			status:     http.StatusOK,
			checksum:   checksum,
			sourceHash: sql.NullString{String: canonicalHash, Valid: true},
			wantVerify: true,
			wantCalls:  1,
		},
		{
			name:       "sha1 mismatch",
			status:     http.StatusOK,
			checksum:   checksum,
			sourceHash: sql.NullString{String: "SHA1:0000000000000000000000000000000000000000", Valid: true},
			wantFail:   true,
			wantCalls:  1,
		},
		{
			name:       "checksum unavailable uses size",
			status:     http.StatusOK,
			sourceHash: sql.NullString{String: canonicalHash, Valid: true},
			wantVerify: true,
			wantCalls:  2,
		},
		{
			name:      "missing target asset mismatches",
			status:    http.StatusNotFound,
			wantFail:  true,
			wantCalls: 2,
		},
		{
			name:       "worker sha1 preferred over source md5",
			status:     http.StatusOK,
			checksum:   checksum,
			sourceHash: sql.NullString{String: "MD5:source-md5", Valid: true},
			workerHash: sql.NullString{String: canonicalHash, Valid: true},
			wantVerify: true,
			wantCalls:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/assets/target-asset" {
					t.Errorf("request path = %q", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`{"id":"target-asset","checksum":"` + tc.checksum + `","exifInfo":{"fileSizeInByte":10}}`))
				}
			}))
			defer server.Close()

			provider := &storage.ImmichProvider{BaseURL: server.URL, HTTPClient: server.Client()}
			task := &db.Task{
				ID:           "immich-task",
				ResourceType: "files",
				FilePath:     "/photo.jpg",
				FileSize:     10,
				Metadata:     []byte(`{"custom_props":{"immich_target_asset_id":"target-asset"}}`),
				SourceHash:   tc.sourceHash,
				WorkerHash:   tc.workerHash,
			}
			verified, failed := false, false
			(&Processor{}).runVerificationPass(context.Background(), verificationPassConfig{
				EntityType: "Migration", EntityID: tc.name, TargetProvider: "immich", Threads: 1, TargetClient: provider,
				GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
				ReconcileProgress: func() error { return nil },
				MarkVerified:      func(context.Context, *db.Task, string) (bool, error) { verified = true; return true, nil },
				MarkMismatch:      func(context.Context, *db.Task) (bool, error) { failed = true; return true, nil },
			})
			if verified != tc.wantVerify || failed != tc.wantFail {
				t.Fatalf("verified = %v, failed = %v; want %v, %v", verified, failed, tc.wantVerify, tc.wantFail)
			}
			if calls != tc.wantCalls {
				t.Fatalf("Immich asset requests = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestVerificationPassImmichWithoutTargetAssetIDLeavesTaskUnverified(t *testing.T) {
	provider := &verifierProvider{hash: "SHA1:target", exists: true, size: 10}
	task := &db.Task{ID: "immich-task", ResourceType: "files", FilePath: "/photo.jpg", FileSize: 10, Metadata: []byte(`{"custom_props":{}}`)}
	verified, failed := false, false
	(&Processor{}).runVerificationPass(context.Background(), verificationPassConfig{
		EntityType: "Migration", EntityID: "immich-missing-id", TargetProvider: "immich", Threads: 1, TargetClient: provider,
		GetTasks:          func(context.Context) ([]*db.Task, error) { return []*db.Task{task}, nil },
		ReconcileProgress: func() error { return nil },
		MarkVerified:      func(context.Context, *db.Task, string) (bool, error) { verified = true; return true, nil },
		MarkMismatch:      func(context.Context, *db.Task) (bool, error) { failed = true; return true, nil },
	})
	if verified || failed || provider.hashCalls != 0 || provider.fileExistsCalls != 0 {
		t.Fatal("Immich task without a target asset ID must remain unverified without provider lookups")
	}
}

type pathTrackingVerifierProvider struct {
	*verifierProvider
	onHash func(path string)
}

func (p *pathTrackingVerifierProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if p.onHash != nil {
		p.onHash(filePath)
	}
	return p.verifierProvider.GetFileHash(ctx, resourceType, filePath)
}
