package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDropboxHasherEmpty(t *testing.T) {
	hasher := NewDropboxHasher()
	// Test empty file
	hasher.Write([]byte{})
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Dropbox content_hash of empty file is:
	// sha256(empty_bytes)
	expectedHashBytes := sha256.Sum256([]byte{})
	expectedHash := hex.EncodeToString(expectedHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected empty file hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherLessThan4MB(t *testing.T) {
	hasher := NewDropboxHasher()
	data := []byte("Hello, this is a test string to verify the Dropbox hasher implementation.")
	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Since size is less than 4MB, it's 1 block
	blockHash := sha256.Sum256(data)
	finalHashBytes := sha256.Sum256(blockHash[:])
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherExact4MB(t *testing.T) {
	hasher := NewDropboxHasher()
	blockSize := 4 * 1024 * 1024
	data := make([]byte, blockSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Exactly 1 block
	blockHash := sha256.Sum256(data)
	finalHashBytes := sha256.Sum256(blockHash[:])
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxHasherMultiBlock(t *testing.T) {
	hasher := NewDropboxHasher()
	blockSize := 4 * 1024 * 1024
	// 10MB data (2 full 4MB blocks, 1 partial 2MB block)
	data := make([]byte, 10*1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	hasher.Write(data)
	sum := hasher.Sum(nil)
	hashStr := hex.EncodeToString(sum)

	// Manual block hashing
	block1 := data[:blockSize]
	block2 := data[blockSize : 2*blockSize]
	block3 := data[2*blockSize:]

	bh1 := sha256.Sum256(block1)
	bh2 := sha256.Sum256(block2)
	bh3 := sha256.Sum256(block3)

	var concat []byte
	concat = append(concat, bh1[:]...)
	concat = append(concat, bh2[:]...)
	concat = append(concat, bh3[:]...)

	finalHashBytes := sha256.Sum256(concat)
	expectedHash := hex.EncodeToString(finalHashBytes[:])

	if hashStr != expectedHash {
		t.Errorf("Expected hash to be %s, got %s", expectedHash, hashStr)
	}
}

func TestDropboxContentHashIsTagged(t *testing.T) {
	if got, want := dropboxContentHash("abcdef"), "DROPBOX:abcdef"; got != want {
		t.Fatalf("dropboxContentHash() = %q, want %q", got, want)
	}
	if got := dropboxContentHash(""); got != "" {
		t.Fatalf("dropboxContentHash(empty) = %q, want empty", got)
	}
}

type mockRoundTripper struct {
	fn func(req *http.Request) (*http.Response, error)
}

func (m mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.fn(req)
}

func TestDropboxInspectResourceNotFound(t *testing.T) {
	client := &http.Client{
		Transport: mockRoundTripper{
			fn: func(req *http.Request) (*http.Response, error) {
				body := `{"error_summary": "path/not_found/...", "error": {".tag": "path", "path": {".tag": "not_found"}}}`
				return &http.Response{
					StatusCode: http.StatusConflict,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	p := &DropboxProvider{HTTPClient: client}
	_, err := p.InspectResource(context.Background(), "files", "/missing.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectResource missing error = %v, want ErrNotFound", err)
	}
}

func TestDropboxStreamUploadIncludesClientModified(t *testing.T) {
	expectedTime := time.Date(2023, 5, 10, 14, 20, 0, 0, time.UTC)
	var gotAPIArg string

	client := &http.Client{
		Transport: mockRoundTripper{
			fn: func(req *http.Request) (*http.Response, error) {
				gotAPIArg = req.Header.Get("Dropbox-API-Arg")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	p := &DropboxProvider{HTTPClient: client}
	meta := FileMetadata{ModifiedTime: expectedTime}
	ctx := WithTransferMetadata(context.Background(), meta)

	err := p.StreamUpload(ctx, "files", "/test.txt", strings.NewReader("content"), 7)
	if err != nil {
		t.Fatalf("StreamUpload() error = %v", err)
	}

	if !strings.Contains(gotAPIArg, `"client_modified":"2023-05-10T14:20:00Z"`) {
		t.Errorf("Dropbox-API-Arg = %q, want client_modified to contain 2023-05-10T14:20:00Z", gotAPIArg)
	}
}

func TestDropboxCreateParentDirectoriesCreatesEachPathComponent(t *testing.T) {
	var createdPaths []string
	client := &http.Client{
		Transport: mockRoundTripper{
			fn: func(req *http.Request) (*http.Response, error) {
				var arg struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(req.Body).Decode(&arg); err != nil {
					t.Fatalf("decode create_folder_v2 request: %v", err)
				}
				createdPaths = append(createdPaths, arg.Path)
				if arg.Path == "/first/second" {
					return &http.Response{
						StatusCode: http.StatusConflict,
						Body:       io.NopCloser(strings.NewReader(`{"error_summary":"path/conflict/folder/..."}`)),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{}`)),
					Header:     make(http.Header),
				}, nil
			},
		},
	}
	p := &DropboxProvider{AccessToken: t.Name(), HTTPClient: client}

	if err := p.CreateParentDirectories(context.Background(), "files", "/first/second/third/file.txt"); err != nil {
		t.Fatalf("CreateParentDirectories() error = %v", err)
	}

	want := []string{"/first", "/first/second", "/first/second/third"}
	if len(createdPaths) != len(want) {
		t.Fatalf("create_folder_v2 calls = %v, want %v", createdPaths, want)
	}
	for i := range want {
		if createdPaths[i] != want[i] {
			t.Errorf("create_folder_v2 call %d path = %q, want %q", i, createdPaths[i], want[i])
		}
	}

	if err := p.CreateParentDirectories(context.Background(), "files", "/first/second/third/another.txt"); err != nil {
		t.Fatalf("CreateParentDirectories() cached call error = %v", err)
	}
	if len(createdPaths) != len(want) {
		t.Errorf("cached parent directories made additional requests: %v", createdPaths)
	}
}

func TestDropboxCreateParentDirectoriesErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantAuth   bool
		wantError  string
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantAuth: true},
		{name: "server error", statusCode: http.StatusInternalServerError, wantError: `dropbox mkdir "/first": status 500`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{
				Transport: mockRoundTripper{
					fn: func(req *http.Request) (*http.Response, error) {
						calls++
						return &http.Response{
							StatusCode: tt.statusCode,
							Body:       io.NopCloser(strings.NewReader(`{}`)),
							Header:     make(http.Header),
						}, nil
					},
				},
			}
			p := &DropboxProvider{AccessToken: t.Name(), HTTPClient: client}

			err := p.CreateParentDirectories(context.Background(), "files", "/first/second/file.txt")
			if err == nil {
				t.Fatal("CreateParentDirectories() error = nil")
			}
			if got := errors.Is(err, ErrAuth); got != tt.wantAuth {
				t.Errorf("errors.Is(err, ErrAuth) = %t, want %t (err = %v)", got, tt.wantAuth, err)
			}
			if tt.wantError != "" && err.Error() != tt.wantError {
				t.Errorf("CreateParentDirectories() error = %q, want %q", err, tt.wantError)
			}
			if calls != 1 {
				t.Errorf("create_folder_v2 calls = %d, want 1", calls)
			}
		})
	}
}
