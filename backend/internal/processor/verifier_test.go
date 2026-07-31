package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"backend/internal/db"
	"backend/internal/storage"
)

type verifierProvider struct {
	fakeProvider
	hash    string
	hashErr error
	exists  bool
	size    int64
}

func (p *verifierProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	return p.exists, p.size, nil
}

func (p *verifierProvider) GetFileHash(context.Context, string, string) (string, error) {
	return p.hash, p.hashErr
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

func TestIsCryptographicHash(t *testing.T) {
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
		{"ETAG", false},
		{"ETAG_MATCH", false},
		{"UNKNOWN", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.algo, func(t *testing.T) {
			if got := isCryptographicHash(tc.algo); got != tc.want {
				t.Errorf("isCryptographicHash(%q) = %v, want %v", tc.algo, got, tc.want)
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
		FilePath:     "/Albums/album-uuid-123/asset-uuid-456",
		FileSize:     2000,
		Metadata:     []byte(`{"immich_filename":"sunset.jpg","immich_album_name":"Vacation"}`),
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

	wantPath := "/Immich Alben/Albums/Vacation/sunset.jpg"
	if requestedPath != wantPath {
		t.Fatalf("verifier requested path %q, want %q", requestedPath, wantPath)
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
