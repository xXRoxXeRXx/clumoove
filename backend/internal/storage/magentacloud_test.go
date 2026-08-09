package storage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewMagentacloudProviderValid(t *testing.T) {
	p, err := NewMagentacloudProvider("user@telekom.de", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil MagentacloudProvider")
	}
	if p.BaseURL != magentaCloudBaseURL {
		t.Errorf("expected BaseURL %s, got %s", magentaCloudBaseURL, p.BaseURL)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestMagentacloudApplyMetadataUsesProviderName(t *testing.T) {
	p, err := NewMagentacloudProvider("user@telekom.de", "pass")
	if err != nil {
		t.Fatal(err)
	}
	p.HTTPClient = &http.Client{Transport: failingRoundTripper{}}

	err = p.ApplyMetadata(context.Background(), "files", "/file.txt", FileMetadata{ModifiedTime: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "magentacloud apply metadata") {
		t.Fatalf("ApplyMetadata() error = %v, want magentacloud provider name", err)
	}
}

func TestMagentacloudProviderNonFilesRejected(t *testing.T) {
	p, err := NewMagentacloudProvider("user@telekom.de", "pass")
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
		if err := p.CreateParentDirectories(ctx, resourceType, "/dir/test.txt"); err == nil {
			t.Errorf("CreateParentDirectories: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.CreateDirectory(ctx, resourceType, "/dir"); err == nil {
			t.Errorf("CreateDirectory: expected error for resourceType %q, got nil", resourceType)
		}
	}
}

func TestMagentaPaths(t *testing.T) {
	mp := magentaPaths{}
	urlStr := mp.resourceURL("https://magentacloud.de/remote.php/webdav", "user", "files", "/my folder/file.txt")
	expected := "https://magentacloud.de/remote.php/webdav/my%20folder/file.txt"
	if urlStr != expected {
		t.Errorf("resourceURL = %s, want %s", urlStr, expected)
	}

	withAmp := mp.resourceURL("https://magentacloud.de/remote.php/webdav", "user", "files", "/Kinder & Jugend/a+b.txt")
	wantAmp := "https://magentacloud.de/remote.php/webdav/Kinder%20%26%20Jugend/a%2Bb.txt"
	if withAmp != wantAmp {
		t.Errorf("resourceURL = %s, want %s", withAmp, wantAmp)
	}

	uploadURL := mp.uploadsURL("https://magentacloud.de/remote.php/webdav", "user+tag@telekom.de", "/upload-123")
	wantUploadURL := "https://magentacloud.de/remote.php/dav/uploads/user%2Btag%40telekom.de/upload-123"
	if uploadURL != wantUploadURL {
		t.Errorf("uploadsURL = %s, want %s", uploadURL, wantUploadURL)
	}
}

func TestMagentacloudChunkedUploadUsesDAVUploadsEndpoint(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "53477376")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	p, err := NewMagentacloudProvider("user+tag@telekom.de", "pass")
	if err != nil {
		t.Fatal(err)
	}
	p.BaseURL = server.URL + "/remote.php/webdav"
	p.HTTPClient = server.Client()

	const largeFileSize = 51 * 1024 * 1024
	if err := p.StreamUploadChunked(context.Background(), "files", "/large.bin", bytes.NewReader([]byte("x")), largeFileSize, nil); err != nil {
		t.Fatalf("StreamUploadChunked: %v", err)
	}

	want := []string{
		"MKCOL /remote.php/dav/uploads/user%2Btag%40telekom.de/upload-", // transfer ID is time-derived
		"PUT /remote.php/dav/uploads/user%2Btag%40telekom.de/upload-",
		"MOVE /remote.php/dav/uploads/user%2Btag%40telekom.de/upload-",
		"HEAD /remote.php/webdav/large.bin",
	}
	if len(requests) != len(want) {
		t.Fatalf("received %d requests, want %d: %v", len(requests), len(want), requests)
	}
	for i, prefix := range want {
		if len(requests[i]) < len(prefix) || requests[i][:len(prefix)] != prefix {
			t.Errorf("request %d = %q, want prefix %q", i, requests[i], prefix)
		}
	}
}
