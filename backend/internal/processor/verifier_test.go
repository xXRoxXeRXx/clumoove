package processor

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"backend/internal/db"
	"backend/internal/storage"
)

func TestBestSourceHash(t *testing.T) {
	cases := []struct {
		name       string
		workerHash sql.NullString
		sourceHash sql.NullString
		want       string
	}{
		{
			name:       "prefer cryptographic worker hash over source hash",
			workerHash: sql.NullString{String: "SHA1:abc123456789", Valid: true},
			sourceHash: sql.NullString{String: "SHA1:def987654321", Valid: true},
			want:       "SHA1:abc123456789",
		},
		{
			name:       "fallback to cryptographic source hash if worker hash is etag",
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
			name:       "fallback to worker etag if both are etags",
			workerHash: sql.NullString{String: "ETAG:\"worker-etag\"", Valid: true},
			sourceHash: sql.NullString{String: "ETAG:\"source-etag\"", Valid: true},
			want:       "ETAG:\"worker-etag\"",
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
