package storage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOpenCloudProviderValid(t *testing.T) {
	p, err := NewOpenCloudProvider("https://opencloud.example.com", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil OpenCloudProvider")
	}
	expectedBase := "https://opencloud.example.com/dav/spaces"
	if p.BaseURL != expectedBase {
		t.Errorf("expected BaseURL %s, got %s", expectedBase, p.BaseURL)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if p.VerificationMode() != VerificationSizeOnly {
		t.Errorf("expected VerificationMode = %s, got %s", VerificationSizeOnly, p.VerificationMode())
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestNewOpenCloudProviderExplicitPath(t *testing.T) {
	p, err := NewOpenCloudProvider("https://opencloud.example.com/custom/path", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	expectedBase := "https://opencloud.example.com/custom/path"
	if p.BaseURL != expectedBase {
		t.Errorf("expected BaseURL %s, got %s", expectedBase, p.BaseURL)
	}
}

func TestNewOpenCloudProviderInvalidURL(t *testing.T) {
	invalidURLs := []string{
		"not-a-url",
		"://missing-scheme",
		"http://",
	}

	for _, u := range invalidURLs {
		if _, err := NewOpenCloudProvider(u, "user", "pass"); err == nil {
			t.Errorf("expected error for invalid URL %q, got nil", u)
		}
	}
}

func TestOpenCloudPaths(t *testing.T) {
	op := openCloudPaths{}
	urlStr := op.resourceURL("https://opencloud.example.com/dav/spaces", "user", "files", "/my folder/test.txt")
	expected := "https://opencloud.example.com/dav/spaces/my%20folder/test.txt"
	if urlStr != expected {
		t.Errorf("resourceURL = %s, want %s", urlStr, expected)
	}
}

func TestOpenCloudAuthHeaders(t *testing.T) {
	// 1. Basic Auth test
	pBasic, err := NewOpenCloudProvider("https://opencloud.example.com", "myuser", "mypass")
	if err != nil {
		t.Fatalf("failed to create basic auth provider: %v", err)
	}
	reqBasic, err := pBasic.newRequest("GET", "https://opencloud.example.com/dav/spaces/file.txt", nil)
	if err != nil {
		t.Fatalf("newRequest failed: %v", err)
	}
	username, password, ok := reqBasic.BasicAuth()
	if !ok || username != "myuser" || password != "mypass" {
		t.Errorf("expected basic auth myuser:mypass, got ok=%v user=%s pass=%s", ok, username, password)
	}

	// 2. Bearer Token test (empty username, token in password)
	pBearer, err := NewOpenCloudProvider("https://opencloud.example.com", "", "secret-bearer-token")
	if err != nil {
		t.Fatalf("failed to create bearer auth provider: %v", err)
	}
	reqBearer, err := pBearer.newRequest("GET", "https://opencloud.example.com/dav/spaces/file.txt", nil)
	if err != nil {
		t.Fatalf("newRequest failed: %v", err)
	}
	authHeader := reqBearer.Header.Get("Authorization")
	if authHeader != "Bearer secret-bearer-token" {
		t.Errorf("expected Authorization header 'Bearer secret-bearer-token', got %q", authHeader)
	}
}

func TestOpenCloudConnectUsesBearerAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-bearer-token" {
			t.Errorf("Authorization = %q, want Bearer token", got)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p, err := NewOpenCloudProvider("https://opencloud.example.test/dav/spaces", "", "secret-bearer-token")
	if err != nil {
		t.Fatalf("NewOpenCloudProvider: %v", err)
	}
	p.BaseURL = server.URL + "/dav/spaces"
	p.HTTPClient = server.Client()
	connected, err := p.Connect(context.Background())
	if err != nil || !connected {
		t.Fatalf("Connect() = (%v, %v), want (true, nil)", connected, err)
	}
}

func TestOpenCloudConnectFallback(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if strings.Contains(r.URL.Path, "/dav/spaces") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/remote.php/webdav") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	p, err := NewOpenCloudProvider("https://opencloud.example.test/dav/spaces", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	p.BaseURL = server.URL + "/dav/spaces"
	p.HTTPClient = server.Client()

	ctx := context.Background()
	connected, err := p.Connect(ctx)
	if err != nil || !connected {
		t.Fatalf("Connect failed: connected=%v, err=%v", connected, err)
	}
	if !strings.Contains(p.BaseURL, "/remote.php/webdav") {
		t.Errorf("expected BaseURL to fallback to /remote.php/webdav, got %s", p.BaseURL)
	}
}

func TestOpenCloudTUSChunkedUpload(t *testing.T) {
	var postReceived, patchReceived bool
	var postMetadata, patchOffset string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case "POST":
			postReceived = true
			postMetadata = r.Header.Get("Upload-Metadata")
			w.Header().Set("Location", r.URL.String()+"/upload-session-123")
			w.WriteHeader(http.StatusCreated)
		case "PATCH":
			patchReceived = true
			patchOffset = r.Header.Get("Upload-Offset")
			w.Header().Set("Upload-Offset", "13")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	p, err := NewOpenCloudProvider("https://opencloud.example.test/dav/spaces", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	p.BaseURL = server.URL + "/dav/spaces"
	p.HTTPClient = server.Client()

	content := []byte("Hello OpenCloud")
	progressChan := make(chan int64, 10)

	err = p.StreamUploadChunked(context.Background(), "files", "/sub/file.txt", bytes.NewReader(content), int64(len(content)), progressChan)
	if err != nil {
		t.Fatalf("StreamUploadChunked failed: %v", err)
	}

	if !postReceived {
		t.Error("expected TUS POST creation request")
	}
	if !patchReceived {
		t.Error("expected TUS PATCH upload request")
	}
	if !strings.Contains(postMetadata, "filename ") {
		t.Errorf("expected Upload-Metadata header with filename, got %q", postMetadata)
	}
	if patchOffset != "0" {
		t.Errorf("expected initial Upload-Offset 0, got %q", patchOffset)
	}
}

func TestOpenCloudTUSFailureAfterReadDoesNotReuseStream(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case "POST":
			w.Header().Set("Location", "/upload-session")
			w.WriteHeader(http.StatusCreated)
		case "PATCH":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected fallback request %s", r.Method)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	p, err := NewOpenCloudProvider("https://opencloud.example.test/dav/spaces", "user", "pass")
	if err != nil {
		t.Fatalf("NewOpenCloudProvider: %v", err)
	}
	p.BaseURL = server.URL + "/dav/spaces"
	p.HTTPClient = server.Client()

	err = p.StreamUploadChunked(context.Background(), "files", "/sub/file.txt", bytes.NewReader([]byte("content")), 7, nil)
	if err == nil {
		t.Fatal("StreamUploadChunked unexpectedly succeeded")
	}
	if requests != 3 {
		t.Fatalf("request count = %d, want 3 without DAV fallback", requests)
	}
}

func TestOpenCloudProviderNonFilesRejected(t *testing.T) {
	p, err := NewOpenCloudProvider("https://opencloud.example.com", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	invalidTypes := []string{"calendars", "contacts", "invalid"}

	for _, resourceType := range invalidTypes {
		if _, err := p.GetDirectoryListing(ctx, resourceType, "/"); err == nil {
			t.Errorf("GetDirectoryListing: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.InspectResource(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("InspectResource: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.StreamDownload(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("StreamDownload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUpload(ctx, resourceType, "/test.txt", nil, 0); err == nil {
			t.Errorf("StreamUpload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUploadChunked(ctx, resourceType, "/test.txt", nil, 0, nil); err == nil {
			t.Errorf("StreamUploadChunked: expected error for resourceType %q, got nil", resourceType)
		}
		if _, _, err := p.FileExists(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("FileExists: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.DeleteFile(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("DeleteFile: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.RenameFile(ctx, resourceType, "/old.txt", "/new.txt"); err == nil {
			t.Errorf("RenameFile: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.GetFileHash(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("GetFileHash: expected error for resourceType %q, got nil", resourceType)
		}
	}
}
