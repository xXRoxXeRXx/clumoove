package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOneDrivePath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
		err  error
	}{
		{name: "root", path: "/", want: "/"},
		{name: "normalized", path: "//projects/./report.txt", want: "/projects/report.txt"},
		{name: "traversal", path: "/projects/../secret", err: ErrPathEscapesRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := oneDrivePath(tt.path)
			if !errors.Is(err, tt.err) || got != tt.want {
				t.Fatalf("oneDrivePath(%q) = %q, %v; want %q, %v", tt.path, got, err, tt.want, tt.err)
			}
		})
	}
}

func TestOneDriveProviderListingPaginationAndEncoding(t *testing.T) {
	var requests []string
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("Graph request did not carry bearer token")
		}
		if r.URL.EscapedPath() == "/v1.0/me/drive/root:/folder%20one:" {
			_, _ = io.WriteString(w, `{"id":"folder-id"}`)
			return
		}
		if r.URL.EscapedPath() == "/v1.0/me/drive/root:/folder%20one:/children" {
			_, _ = io.WriteString(w, `{"value":[{"name":"first.txt","size":5,"eTag":"etag-1","lastModifiedDateTime":"2026-01-02T03:04:05Z"}],"@odata.nextLink":"`+server.URL+`/next"}`)
			return
		}
		if r.URL.Path == "/next" {
			_, _ = io.WriteString(w, `{"value":[{"name":"second","folder":{},"eTag":"etag-2"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	resources, err := p.GetDirectoryListing(context.Background(), "files", "/folder one")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 || resources[0].Path != "/folder one/first.txt" || resources[0].ETag != "etag-1" || !resources[1].IsDir {
		t.Fatalf("unexpected resources: %#v", resources)
	}
	if len(requests) != 3 || requests[0] != "/v1.0/me/drive/root:/folder%20one:" || requests[1] != "/v1.0/me/drive/root:/folder%20one:/children" {
		t.Fatalf("unexpected Graph request paths: %v", requests)
	}
}

func TestOneDriveProviderUsesRemoteDriveForSharedShortcut(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/v1.0/me/drive/root:/Shared%20folder:":
			_, _ = io.WriteString(w, `{"id":"shortcut-id","remoteItem":{"id":"remote-item-id","parentReference":{"driveId":"remote-drive-id"}}}`)
		case "/v1.0/drives/remote-drive-id/items/remote-item-id:/notes.txt:/content", "/v1.0/drives/remote-drive-id/items/remote-item-id:/agenda.txt:/content":
			_, _ = io.WriteString(w, "shared content")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	stream, err := p.StreamDownload(context.Background(), "files", "/Shared folder/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	content, err := io.ReadAll(stream)
	if err != nil || string(content) != "shared content" {
		t.Fatalf("download = %q, %v", content, err)
	}
	secondStream, err := p.StreamDownload(context.Background(), "files", "/Shared folder/agenda.txt")
	if err != nil {
		t.Fatal(err)
	}
	secondStream.Close()
	if len(requests) != 3 || requests[0] != "/v1.0/me/drive/root:/Shared%20folder:" {
		t.Fatalf("requests = %v, want one shortcut lookup and two remote downloads", requests)
	}
}

func TestOneDriveProviderMarksPersonalVault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/v1.0/me/drive/root/children" {
			_, _ = io.WriteString(w, `{"value":[{"id":"vault-id","name":"Persönlicher Tresor","folder":{},"specialFolder":{"name":"vault"}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	items, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].IsPersonalVault() {
		t.Fatalf("items = %#v, want marked Personal Vault", items)
	}
}

func TestOneDriveProviderRejectsExternalPaginationLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"value":[],"@odata.nextLink":"https://example.test/next"}`)
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	if _, err := p.GetDirectoryListing(context.Background(), "files", "/"); err == nil {
		t.Fatal("expected external pagination URL to be rejected")
	}
}

func TestOneDriveProviderAuthAndMissingResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "expired") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	if _, err := p.InspectResource(context.Background(), "files", "/denied"); errors.Is(err, ErrAuth) {
		t.Fatal("inspect 403 error must not be classified as ErrAuth")
	}
	if _, err := p.InspectResource(context.Background(), "files", "/expired"); !errors.Is(err, ErrAuth) {
		t.Fatalf("inspect 401 error = %v, want ErrAuth", err)
	}
	exists, _, err := p.FileExists(context.Background(), "files", "/missing")
	if err != nil || exists {
		t.Fatalf("FileExists missing = %v, %v, want false, nil", exists, err)
	}
	if _, err := p.InspectResource(context.Background(), "files", "/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inspect missing error = %v, want ErrNotFound", err)
	}
	if _, err := p.GetFileHash(context.Background(), "files", "/missing"); !errors.Is(err, ErrHashNotSupported) {
		t.Fatalf("GetFileHash error = %v, want ErrHashNotSupported", err)
	}
}

func TestOneDriveProviderNestedUploadCreatesParents(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "nested"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/children"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "file.txt:/content"):
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	if err := p.StreamUpload(context.Background(), "files", "/nested/child/file.txt", bytes.NewBufferString("body"), 4); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 5 || !strings.HasPrefix(requests[0], "GET ") || !strings.HasPrefix(requests[1], "POST ") || !strings.HasPrefix(requests[4], "PUT ") {
		t.Fatalf("expected parent creation followed by upload, got %v", requests)
	}
}

func TestOneDriveProviderSameDirectoryRenameOmitsParentReference(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "root", old: "/temporary.tmp", new: "/final.txt"},
		{name: "nested", old: "/photos/temporary.tmp", new: "/photos/final.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPatch {
					http.NotFound(w, r)
					return
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["name"] != "final.txt" {
					t.Fatalf("rename payload name = %#v", payload["name"])
				}
				if _, exists := payload["parentReference"]; exists {
					t.Fatalf("same-directory rename must omit parentReference: %#v", payload)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
			if err := p.RenameFile(context.Background(), "files", tt.old, tt.new); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOneDriveProviderCrossDirectoryRenameUsesParentID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if strings.Contains(r.URL.Path, "new-parent") {
				_, _ = io.WriteString(w, `{"id":"parent-id","folder":{}}`)
				return
			}
			http.NotFound(w, r)
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case http.MethodPatch:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			parent, ok := payload["parentReference"].(map[string]any)
			if !ok || parent["id"] != "parent-id" {
				t.Fatalf("cross-directory rename payload = %#v, want parent ID", payload)
			}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	if err := p.RenameFile(context.Background(), "files", "/old/file.tmp", "/new-parent/file.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestOneDriveProviderSimpleUploadProgress(t *testing.T) {
	var contentRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentRange = r.Header.Get("Content-Range")
		if r.Method != http.MethodPut || !strings.Contains(r.URL.Path, "file.txt:/content") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	progress := make(chan int64, 1)
	// Root uploads do not require a directory lookup.
	if err := p.StreamUploadChunked(context.Background(), "files", "/file.txt", bytes.NewBufferString("hello"), 5, progress); err != nil {
		t.Fatal(err)
	}
	if got := <-progress; got != 5 {
		t.Fatalf("progress = %d, want 5", got)
	}
	if contentRange != "" {
		t.Fatalf("simple upload Content-Range = %q, want empty", contentRange)
	}
}

func TestOneDriveProviderSessionUpload(t *testing.T) {
	var ranges []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createUploadSession"):
			_, _ = io.WriteString(w, `{"uploadUrl":"`+server.URL+`/upload-session"}`)
		case r.URL.Path == "/upload-session":
			ranges = append(ranges, r.Header.Get("Content-Range"))
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := newOneDriveProvider("token", server.URL+"/v1.0/me/drive", server.Client())
	size := oneDriveSimpleUploadLimit + 1
	payload := bytes.Repeat([]byte("x"), int(size))
	progress := make(chan int64, 2)
	if err := p.StreamUploadChunked(context.Background(), "files", "/large.bin", bytes.NewReader(payload), size, progress); err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 1 || ranges[0] != "bytes 0-4194304/4194305" {
		t.Fatalf("unexpected upload ranges: %v", ranges)
	}
	if got := <-progress; got != size {
		t.Fatalf("progress = %d, want %d", got, size)
	}
}
