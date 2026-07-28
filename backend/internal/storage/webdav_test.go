package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewWebDAVProviderValid(t *testing.T) {
	p, err := NewWebDAVProvider("https://example.com/dav", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil WebDAVProvider")
	}
	if p.BaseURL != "https://example.com/dav" {
		t.Errorf("expected BaseURL https://example.com/dav, got %s", p.BaseURL)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestNewWebDAVProviderInvalidURL(t *testing.T) {
	_, err := NewWebDAVProvider("not-a-url", "user", "pass")
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}

func TestWebDAVProviderNonFilesRejected(t *testing.T) {
	p, err := NewWebDAVProvider("https://example.com/dav", "user", "pass")
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

func TestWebDAVProviderSupportsAtomicRename(t *testing.T) {
	p, err := NewWebDAVProvider("https://example.com/dav", "user", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
}

func TestWebDAVProviderErrAuth(t *testing.T) {
	if !errors.Is(ErrAuth, ErrAuth) {
		t.Error("ErrAuth mismatch")
	}
}

func TestWebDAVCreateDirectoryVerifiesMKCOLMethodNotAllowed(t *testing.T) {
	collectionResponse := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	fileResponse := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	cases := []struct {
		name           string
		propfindStatus int
		propfindBody   string
		wantErr        bool
	}{
		{"existing collection", http.StatusMultiStatus, collectionResponse, false},
		{"existing file", http.StatusMultiStatus, fileResponse, true},
		{"missing collection", http.StatusNotFound, "", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case "MKCOL":
					w.WriteHeader(http.StatusMethodNotAllowed)
				case "PROPFIND":
					w.WriteHeader(test.propfindStatus)
					if test.propfindBody != "" {
						_, _ = w.Write([]byte(test.propfindBody))
					}
				default:
					t.Errorf("unexpected method %s", r.Method)
				}
			}))
			defer server.Close()

			p := &WebDAVProvider{BaseURL: server.URL, HTTPClient: server.Client()}
			err := p.CreateDirectory(context.Background(), "files", "/folder")
			if (err != nil) != test.wantErr {
				t.Fatalf("CreateDirectory() error = %v, wantErr %v", err, test.wantErr)
			}
			if !test.wantErr {
				if _, ok := p.createdDirs.Load("/folder"); !ok {
					t.Error("confirmed directory was not cached")
				}
			}
		})
	}
}

func TestWebDAVCreateParentDirectoriesVerifiesMKCOLMethodNotAllowed(t *testing.T) {
	collectionResponse := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(collectionResponse))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	p := &WebDAVProvider{BaseURL: server.URL, HTTPClient: server.Client()}
	if err := p.CreateParentDirectories(context.Background(), "files", "/folder/file.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.createdDirs.Load("/folder"); !ok {
		t.Error("confirmed parent directory was not cached")
	}
}
