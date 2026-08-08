package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testTransport struct {
	targetURL *url.URL
	base      http.RoundTripper
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.targetURL.Scheme
	req.URL.Host = t.targetURL.Host
	return t.base.RoundTrip(req)
}

func TestNewSeafileProviderValid(t *testing.T) {
	p, err := NewSeafileProvider("http://1.1.1.1", "user@example.com", "pass123")
	if err != nil {
		t.Fatalf("unexpected error creating Seafile provider: %v", err)
	}
	defer p.Close()

	if p.BaseURL != "http://1.1.1.1" {
		t.Errorf("expected BaseURL http://1.1.1.1, got %s", p.BaseURL)
	}
}

func TestNewSeafileProviderInvalidURL(t *testing.T) {
	_, err := NewSeafileProvider("", "user", "pass")
	if err == nil {
		t.Error("expected error for empty URL, got nil")
	}
}

func TestSeafileProviderCapabilities(t *testing.T) {
	p := &SeafileProvider{}
	if p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename to return false")
	}
	if p.VerificationMode() != VerificationSizeOnly {
		t.Errorf("expected VerificationMode to be %s, got %s", VerificationSizeOnly, p.VerificationMode())
	}
}

func TestSeafileProviderNonFilesRejected(t *testing.T) {
	p := &SeafileProvider{}
	ctx := context.Background()

	if _, err := p.GetDirectoryListing(ctx, "calendars", "/"); err != ErrUnsupportedResourceType {
		t.Errorf("expected ErrUnsupportedResourceType for calendars listing, got %v", err)
	}
	if _, err := p.InspectResource(ctx, "contacts", "/"); err != ErrUnsupportedResourceType {
		t.Errorf("expected ErrUnsupportedResourceType for contacts inspect, got %v", err)
	}
	if _, err := p.StreamDownload(ctx, "calendars", "/file.ics"); err != ErrUnsupportedResourceType {
		t.Errorf("expected ErrUnsupportedResourceType for calendars download, got %v", err)
	}
	if err := p.StreamUpload(ctx, "contacts", "/card.vcf", strings.NewReader(""), 0); err != ErrUnsupportedResourceType {
		t.Errorf("expected ErrUnsupportedResourceType for contacts upload, got %v", err)
	}
}

func TestSeafileProviderSharesAccountTokenAcrossTaskScopedClients(t *testing.T) {
	var authRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/auth-token/" {
			http.NotFound(w, r)
			return
		}
		authRequests.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "shared-token"})
	}))
	defer server.Close()

	const clients = 20
	errs := make(chan error, clients)
	var wg sync.WaitGroup
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &SeafileProvider{
				BaseURL:    server.URL,
				Username:   "user@example.com",
				Password:   "pass123",
				HTTPClient: server.Client(),
				repoCache:  make(map[string]string),
			}
			_, err := p.getToken(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("getToken() error = %v", err)
		}
	}
	if got := authRequests.Load(); got != 1 {
		t.Fatalf("auth endpoint requests = %d, want 1", got)
	}
}

func TestSeafileProviderHonorsAuthRetryAfterAcrossTaskScopedClients(t *testing.T) {
	var authRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authRequests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	newProvider := func() *SeafileProvider {
		return &SeafileProvider{
			BaseURL:    server.URL,
			Username:   "user@example.com",
			Password:   "pass123",
			HTTPClient: server.Client(),
			repoCache:  make(map[string]string),
		}
	}

	_, err := newProvider().getToken(context.Background())
	var retryAfterErr *RetryAfterError
	if !errors.As(err, &retryAfterErr) {
		t.Fatalf("getToken() error = %v, want RetryAfterError", err)
	}
	if retryAfterErr.After != time.Minute {
		t.Fatalf("RetryAfterError.After = %s, want 1m", retryAfterErr.After)
	}

	_, err = newProvider().getToken(context.Background())
	if !errors.As(err, &retryAfterErr) {
		t.Fatalf("second getToken() error = %v, want RetryAfterError", err)
	}
	if got := authRequests.Load(); got != 1 {
		t.Fatalf("auth endpoint requests = %d, want 1", got)
	}
}

func TestSeafileProviderAuthAndOperations(t *testing.T) {
	mux := http.NewServeMux()

	// Mock /api2/auth-token/
	mux.HandleFunc("/api2/auth-token/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("username") == "user@example.com" && r.Form.Get("password") == "pass123" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "mock-seafile-token"})
			return
		}
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
	})

	// Mock /api2/repos/
	mux.HandleFunc("/api2/repos/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token mock-seafile-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == "GET" {
			repos := []seafileRepo{
				{ID: "repo-uuid-1", Name: "MyLibrary", Mtime: 1600000000},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(repos)
			return
		}
		if r.Method == "POST" {
			_ = r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"repo_id": "repo-uuid-2", "name": r.Form.Get("name")})
			return
		}
	})

	// Mock /api2/repos/repo-uuid-1/dir/
	mux.HandleFunc("/api2/repos/repo-uuid-1/dir/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token mock-seafile-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		items := []seafileDirItem{
			{Type: "file", ID: "sha1hash123", Name: "document.pdf", Size: 1024, Mtime: 1600000050},
			{Type: "dir", ID: "dirhash456", Name: "Subfolder", Size: 0, Mtime: 1600000060},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})

	// Mock /api2/repos/repo-uuid-1/file/
	mux.HandleFunc("/api2/repos/repo-uuid-1/file/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Token mock-seafile-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == "GET" {
			// Download link
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`"http://1.1.1.1/seafhttp/download/stream"`))
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == "POST" {
			w.WriteHeader(http.StatusOK)
			return
		}
	})

	// Mock /seafhttp/download/stream
	mux.HandleFunc("/seafhttp/download/stream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mock seafile file content"))
	})

	// Mock /api2/repos/repo-uuid-1/upload-link/
	mux.HandleFunc("/api2/repos/repo-uuid-1/upload-link/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"http://1.1.1.1/seafhttp/upload"`))
	})

	// Mock /seafhttp/upload
	mux.HandleFunc("/seafhttp/upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	p, err := NewSeafileProvider("http://1.1.1.1", "user@example.com", "pass123")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	defer p.Close()

	serverParsed, _ := url.Parse(server.URL)
	client := server.Client()
	client.Transport = &testTransport{
		targetURL: serverParsed,
		base:      client.Transport,
	}
	p.HTTPClient = client

	ctx := context.Background()

	// Test Connect
	ok, err := p.Connect(ctx)
	if !ok || err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Test GetDirectoryListing root
	repos, err := p.GetDirectoryListing(ctx, "files", "/")
	if err != nil {
		t.Fatalf("GetDirectoryListing root failed: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "MyLibrary" {
		t.Errorf("unexpected root listing: %+v", repos)
	}

	// Test GetDirectoryListing in library
	files, err := p.GetDirectoryListing(ctx, "files", "/MyLibrary")
	if err != nil {
		t.Fatalf("GetDirectoryListing in library failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 items in library, got %d", len(files))
	}

	// Test InspectResource
	res, err := p.InspectResource(ctx, "files", "/MyLibrary/document.pdf")
	if err != nil {
		t.Fatalf("InspectResource failed: %v", err)
	}
	if res.Size != 1024 {
		t.Errorf("unexpected inspected resource size: %d", res.Size)
	}

	// Test StreamDownload
	rc, err := p.StreamDownload(ctx, "files", "/MyLibrary/document.pdf")
	if err != nil {
		t.Fatalf("StreamDownload failed: %v", err)
	}
	downloaded, err := io.ReadAll(rc)
	_ = rc.Close()
	if string(downloaded) != "mock seafile file content" {
		t.Errorf("downloaded content mismatch: got %q", string(downloaded))
	}

	// Test StreamUpload with io.Pipe streaming
	uploadData := []byte("upload content")
	if err := p.StreamUpload(ctx, "files", "/MyLibrary/newdoc.txt", bytes.NewReader(uploadData), int64(len(uploadData))); err != nil {
		t.Fatalf("StreamUpload failed: %v", err)
	}

	// Test StreamUpload directly at root /rootfile.txt (fallbacks to default library MyLibrary)
	if err := p.StreamUpload(ctx, "files", "/rootfile.txt", bytes.NewReader(uploadData), int64(len(uploadData))); err != nil {
		t.Fatalf("StreamUpload at root failed: %v", err)
	}

	// Test GetFileHash returns ErrChecksumNotAvailable
	_, err = p.GetFileHash(ctx, "files", "/MyLibrary/document.pdf")
	if err != ErrChecksumNotAvailable {
		t.Errorf("expected ErrChecksumNotAvailable, got %v", err)
	}

	// Test DeleteFile
	if err := p.DeleteFile(ctx, "files", "/MyLibrary/document.pdf"); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	// Test RenameFile
	if err := p.RenameFile(ctx, "files", "/MyLibrary/old.pdf", "/MyLibrary/new.pdf"); err != nil {
		t.Fatalf("RenameFile failed: %v", err)
	}
}
