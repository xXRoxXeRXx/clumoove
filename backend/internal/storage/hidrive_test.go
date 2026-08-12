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

func TestNewHiDriveProvider(t *testing.T) {
	_, err := NewHiDriveProvider("")
	if err == nil {
		t.Fatal("expected error when initializing HiDriveProvider with empty token")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("expected ErrAuth, got %v", err)
	}

	p, err := NewHiDriveProvider("valid-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() == true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestHiDriveProviderNonFilesTypeRejected(t *testing.T) {
	p, err := NewHiDriveProvider("token")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	invalidTypes := []string{"calendars", "contacts", "invalid"}

	for _, resourceType := range invalidTypes {
		if _, err := p.GetDirectoryListing(ctx, resourceType, "/"); err == nil {
			t.Errorf("GetDirectoryListing: expected error for %q", resourceType)
		}
		if _, err := p.InspectResource(ctx, resourceType, "/file.txt"); err == nil {
			t.Errorf("InspectResource: expected error for %q", resourceType)
		}
		if _, err := p.StreamDownload(ctx, resourceType, "/file.txt"); err == nil {
			t.Errorf("StreamDownload: expected error for %q", resourceType)
		}
		if err := p.StreamUpload(ctx, resourceType, "/file.txt", nil, 0); err == nil {
			t.Errorf("StreamUpload: expected error for %q", resourceType)
		}
		if err := p.StreamUploadChunked(ctx, resourceType, "/file.txt", nil, 0, nil); err == nil {
			t.Errorf("StreamUploadChunked: expected error for %q", resourceType)
		}
		if _, _, err := p.FileExists(ctx, resourceType, "/file.txt"); err == nil {
			t.Errorf("FileExists: expected error for %q", resourceType)
		}
		if err := p.DeleteFile(ctx, resourceType, "/file.txt"); err == nil {
			t.Errorf("DeleteFile: expected error for %q", resourceType)
		}
		if err := p.RenameFile(ctx, resourceType, "/old.txt", "/new.txt"); err == nil {
			t.Errorf("RenameFile: expected error for %q", resourceType)
		}
		if _, err := p.GetFileHash(ctx, resourceType, "/file.txt"); err == nil {
			t.Errorf("GetFileHash: expected error for %q", resourceType)
		}
		if err := p.CreateParentDirectories(ctx, resourceType, "/dir/file.txt"); err == nil {
			t.Errorf("CreateParentDirectories: expected error for %q", resourceType)
		}
		if err := p.CreateDirectory(ctx, resourceType, "/dir"); err == nil {
			t.Errorf("CreateDirectory: expected error for %q", resourceType)
		}
	}
}

func TestHiDriveProviderConnect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/user/me" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"account":"testuser","alias":"testuser"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL

	ctx := context.Background()
	ok, err := p.Connect(ctx)
	if err != nil || !ok {
		t.Fatalf("Connect failed: ok=%v, err=%v", ok, err)
	}

	// Test invalid token
	pInvalid, _ := NewHiDriveProvider("invalid-token")
	pInvalid.BaseURL = ts.URL
	ok, err = pInvalid.Connect(ctx)
	if ok || err == nil || !errors.Is(err, ErrAuth) {
		t.Errorf("expected ErrAuth for invalid token, got ok=%v err=%v", ok, err)
	}
}

func TestHiDriveChunkedUploadCleansPartialTargetAfterLaterChunkFailure(t *testing.T) {
	const chunkSize = 50 * 1024 * 1024
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/file":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && r.URL.Path == "/file":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/meta":
			_ = json.NewEncoder(w).Encode(hidriveMetaResponse{Path: "/partial.bin", Name: "partial.bin", Type: "file", Size: chunkSize})
		case r.Method == http.MethodDelete && r.URL.Path == "/file":
			if got := r.URL.Query().Get("path"); got != "/partial.bin" {
				t.Errorf("cleanup path = %q, want /partial.bin", got)
			}
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p, err := NewHiDriveProvider("token")
	if err != nil {
		t.Fatal(err)
	}
	p.BaseURL = server.URL
	stream := io.MultiReader(bytes.NewReader(make([]byte, chunkSize)), strings.NewReader("x"))
	err = p.StreamUploadChunked(context.Background(), "files", "/partial.bin", stream, chunkSize+1, nil)
	if err == nil {
		t.Fatal("StreamUploadChunked unexpectedly succeeded")
	}
	if !deleted {
		t.Fatal("partial target was not deleted after later chunk failure")
	}
}

func TestHiDriveProviderDirectoryListing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dir" {
			fields := r.URL.Query().Get("fields")
			if strings.Contains(fields, "sha1") || !strings.Contains(fields, "members.chash") {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			resp := hidriveDirResponse{
				Path: "/",
				Name: "",
				Members: []hidriveDirMember{
					{Name: "folder1", Type: "dir"},
					{Name: "file1.txt", Type: "file", Size: 1024, Mtime: 1600000000, ContentHash: "abc123hidrivehash"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL

	resources, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatalf("GetDirectoryListing failed: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resources))
	}
	if !resources[0].IsDir || resources[0].Name != "folder1" {
		t.Errorf("unexpected first item: %+v", resources[0])
	}
	if resources[1].IsDir || resources[1].Name != "file1.txt" || resources[1].Size != 1024 || resources[1].Hash != "HIDRIVE:abc123hidrivehash" {
		t.Errorf("unexpected second item: %+v", resources[1])
	}
}

func TestHiDriveProviderDirectoryListingDecodesResponseNames(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dir" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests++
		switch requests {
		case 1:
			_ = json.NewEncoder(w).Encode(hidriveDirResponse{Members: []hidriveDirMember{{Name: "folder%2Bname", Type: "dir"}}})
		case 2:
			if got := r.URL.Query().Get("path"); got != "/folder+name" {
				t.Errorf("path = %q, want decoded name", got)
			}
			_ = json.NewEncoder(w).Encode(hidriveDirResponse{})
		}
	}))
	defer ts.Close()
	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL
	entries, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil || len(entries) != 1 {
		t.Fatalf("first listing = %#v, %v", entries, err)
	}
	if entries[0].Path != "/folder+name" {
		t.Fatalf("decoded path = %q", entries[0].Path)
	}
	_, err = p.GetDirectoryListing(context.Background(), "files", entries[0].Path)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHiDriveProviderFileExistsAndInspect(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			p := r.URL.Query().Get("path")
			if p == "/exists.txt" {
				meta := hidriveMetaResponse{
					Path:        "/exists.txt",
					Name:        "exists.txt",
					Type:        "file",
					Size:        2048,
					ContentHash: "deadbeef",
					Mtime:       1600000000,
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(meta)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL
	ctx := context.Background()

	exists, size, err := p.FileExists(ctx, "files", "/exists.txt")
	if err != nil || !exists || size != 2048 {
		t.Errorf("FileExists failed: exists=%v, size=%d, err=%v", exists, size, err)
	}

	notExists, _, err := p.FileExists(ctx, "files", "/nonexistent.txt")
	if err != nil || notExists {
		t.Errorf("FileExists expected false, got exists=%v, err=%v", notExists, err)
	}

	res, err := p.InspectResource(ctx, "files", "/exists.txt")
	if err != nil {
		t.Fatalf("InspectResource failed: %v", err)
	}
	if res.Name != "exists.txt" || res.Size != 2048 || res.Hash != "HIDRIVE:deadbeef" {
		t.Errorf("InspectResource returned unexpected resource: %+v", res)
	}

	hash, err := p.GetFileHash(ctx, "files", "/exists.txt")
	if err != nil || hash != "HIDRIVE:deadbeef" {
		t.Errorf("GetFileHash failed: hash=%q, err=%v", hash, err)
	}
}

func TestHiDriveProviderDownloadUploadDeleteRename(t *testing.T) {
	storedFiles := make(map[string][]byte)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/file":
			if r.Method == "GET" {
				p := q.Get("path")
				data, ok := storedFiles[p]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
			if r.Method == "POST" {
				dir := q.Get("dir")
				name := q.Get("name")
				filePath := dir + "/" + name
				if dir == "/" {
					filePath = "/" + name
				}
				data, _ := io.ReadAll(r.Body)
				storedFiles[filePath] = data
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == "DELETE" {
				p := q.Get("path")
				delete(storedFiles, p)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		case "/file/move", "/dir/move":
			if r.Method == "POST" {
				src := q.Get("src")
				dst := q.Get("dst")
				if data, ok := storedFiles[src]; ok {
					storedFiles[dst] = data
					delete(storedFiles, src)
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}
		case "/meta":
			p := q.Get("path")
			if _, ok := storedFiles[p]; ok {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(hidriveMetaResponse{Path: p, Name: "file", Type: "file"})
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL
	ctx := context.Background()

	// 1. Upload
	content := []byte("Hello HiDrive")
	err := p.StreamUpload(ctx, "files", "/hello.txt", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("StreamUpload failed: %v", err)
	}

	// 2. Download
	rc, err := p.StreamDownload(ctx, "files", "/hello.txt")
	if err != nil {
		t.Fatalf("StreamDownload failed: %v", err)
	}
	downloaded, _ := io.ReadAll(rc)
	rc.Close()
	if string(downloaded) != string(content) {
		t.Errorf("downloaded content mismatch: got %q, want %q", string(downloaded), string(content))
	}

	// 3. Rename
	err = p.RenameFile(ctx, "files", "/hello.txt", "/renamed.txt")
	if err != nil {
		t.Fatalf("RenameFile failed: %v", err)
	}

	// 4. Delete
	err = p.DeleteFile(ctx, "files", "/renamed.txt")
	if err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}
}

func TestHiDriveProviderUploadDoesNotPreflightOrDeleteExistingFile(t *testing.T) {
	var metaRequests, deleteRequests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta":
			metaRequests++
			w.WriteHeader(http.StatusInternalServerError)
		case "/file":
			switch r.Method {
			case http.MethodDelete:
				deleteRequests++
				w.WriteHeader(http.StatusNoContent)
			case http.MethodPost:
				// POST /file is create-only according to the HiDrive API.
				w.WriteHeader(http.StatusConflict)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL
	err := p.StreamUpload(context.Background(), "files", "/already-there.txt", bytes.NewReader([]byte("new")), 3)
	if err == nil {
		t.Fatal("StreamUpload succeeded despite HiDrive create conflict")
	}
	if metaRequests != 0 || deleteRequests != 0 {
		t.Fatalf("upload made preflight requests: meta=%d delete=%d", metaRequests, deleteRequests)
	}
}

func TestHiDriveInspectResourceNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	p, _ := NewHiDriveProvider("mock-token")
	p.BaseURL = ts.URL
	_, err := p.InspectResource(context.Background(), "files", "/missing.txt")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectResource missing error = %v, want ErrNotFound", err)
	}
}
