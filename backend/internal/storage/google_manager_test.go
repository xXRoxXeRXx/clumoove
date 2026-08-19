package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func newGoogleManagerTestProvider(t *testing.T, handler http.Handler) *GoogleProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	service, err := drive.NewService(context.Background(), option.WithHTTPClient(server.Client()), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return &GoogleProvider{driveService: service, httpClient: server.Client()}
}

func TestGoogleManagerListKeepsDriveFileID(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Fatalf("path = %s, want /files", r.URL.Path)
		}
		if r.URL.Query().Get("pageSize") != "1" {
			t.Fatalf("pageSize = %q, want 1", r.URL.Query().Get("pageSize"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"nextPageToken": "next-page",
			"files": []map[string]any{{
				"id": "drive-file-a", "name": "report.pdf", "mimeType": "application/pdf", "size": "12", "modifiedTime": "2026-01-02T03:04:05Z",
			}},
		})
	}))

	page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Locator.NativeID != "drive-file-a" {
		t.Fatalf("manager items = %#v, want immutable Drive ID", page.Items)
	}
	if page.NextCursor != "next-page" {
		t.Fatalf("next cursor = %q", page.NextCursor)
	}
}

func TestGoogleManagerConnectChecksOnlyDrive(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/about" {
			t.Fatalf("path = %s, want /about", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"displayName": "manager"}})
	}))

	connected, err := provider.ConnectManager(context.Background())
	if err != nil || !connected {
		t.Fatalf("ConnectManager() = (%t, %v), want (true, nil)", connected, err)
	}
}

func TestGoogleManagerDownloadUsesNativeID(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files/drive-file-b" {
			t.Fatalf("path = %s, want native file endpoint", r.URL.Path)
		}
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write([]byte("content"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "drive-file-b", "name": "duplicate.txt", "mimeType": "text/plain", "size": "7", "modifiedTime": "2026-01-02T03:04:05Z",
		})
	}))

	download, err := provider.DownloadManager(context.Background(), ManagerLocator{NativeID: "drive-file-b", Path: "/duplicate.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer download.Stream.Close()
	if download.Item.Locator.NativeID != "drive-file-b" || download.Item.Name != "duplicate.txt" {
		t.Fatalf("download item = %#v", download.Item)
	}
}

func TestGoogleManagerResolveRejectsAmbiguousSibling(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{"id": "first", "name": "same-name", "mimeType": googleDriveFolderMIME},
				{"id": "second", "name": "same-name", "mimeType": googleDriveFolderMIME},
			},
		})
	}))

	_, _, _, err := provider.ResolveManagerPath(context.Background(), "/same-name")
	if !errors.Is(err, ErrAmbiguousPath) {
		t.Fatalf("ResolveManagerPath() error = %v, want ErrAmbiguousPath", err)
	}
}

func TestGoogleManagerUploadSkipsExistingFileByParentID(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			t.Fatalf("path = %s, want /files", r.URL.Path)
		}
		if query := r.URL.Query().Get("q"); query != "'parent-id' in parents and name = 'report.txt' and trashed = false" {
			t.Fatalf("query = %q", query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{{"id": "existing", "name": "report.txt", "mimeType": "text/plain"}},
		})
	}))

	result, err := provider.UploadManager(context.Background(), ManagerLocator{NativeID: "parent-id", Path: "/display-only"}, "report.txt", strings.NewReader("unused"), 6, ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped" || result.FinalName != "report.txt" {
		t.Fatalf("UploadManager() = %#v", result)
	}
}

func TestGoogleManagerUploadCreatesInParentID(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
		case "/upload/drive/v3/files":
			body, err := io.ReadAll(r.Body)
			if err != nil || !strings.Contains(string(body), "parent-id") || !strings.Contains(string(body), "new.txt") {
				t.Fatalf("upload payload = %q, err = %v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "new-file"})
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))

	result, err := provider.UploadManager(context.Background(), ManagerLocator{NativeID: "parent-id"}, "new.txt", strings.NewReader("content"), 7, ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "uploaded" || result.FinalName != "new.txt" {
		t.Fatalf("UploadManager() = %#v", result)
	}
}

func TestGoogleManagerCreateDirectorySuccessAndConflict(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if query := r.URL.Query().Get("q"); strings.Contains(query, "existing-dir") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"files": []map[string]any{{"id": "dir-1", "name": "existing-dir", "mimeType": googleDriveFolderMIME}},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{}})
			}
		case http.MethodPost:
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["name"] != "new-dir" || req["mimeType"] != googleDriveFolderMIME {
				t.Fatalf("unexpected folder creation body: %#v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "created-dir-id", "name": "new-dir"})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))

	// Test conflict
	err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{NativeID: "parent-id"}, "existing-dir")
	if !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CreateManagerDirectory() conflict error = %v, want ErrManagerConflict", err)
	}

	// Test success
	err = provider.CreateManagerDirectory(context.Background(), ManagerLocator{NativeID: "parent-id"}, "new-dir")
	if err != nil {
		t.Fatalf("CreateManagerDirectory() error = %v, want nil", err)
	}
}

type googleTestTransport func(req *http.Request) (*http.Response, error)

func (f googleTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGoogleManagerThumbnailSuccess(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/files/photo-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":            "photo-1",
				"mimeType":      "image/jpeg",
				"thumbnailLink": "https://lh3.googleusercontent.com/cdn-thumb=s220",
			})
		case r.URL.Path == "/cdn-thumb=s300":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-jpeg-thumbnail-bytes"))
		default:
			t.Fatalf("unexpected request to: %s", r.URL.String())
		}
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	testClient := &http.Client{
		Transport: googleTestTransport(func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Host, "googleusercontent.com") {
				req.URL.Scheme = "http"
				req.URL.Host = strings.TrimPrefix(server.URL, "http://")
			}
			return server.Client().Transport.RoundTrip(req)
		}),
	}

	service, err := drive.NewService(context.Background(), option.WithHTTPClient(server.Client()), option.WithEndpoint(server.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	provider := &GoogleProvider{driveService: service, httpClient: testClient}

	stream, contentType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "photo-1"}, 300, 300)
	if err != nil {
		t.Fatalf("ThumbnailManager() error = %v", err)
	}
	defer stream.Close()

	if contentType != "image/jpeg" {
		t.Fatalf("contentType = %q, want image/jpeg", contentType)
	}

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-jpeg-thumbnail-bytes" {
		t.Fatalf("data = %q, want fake-jpeg-thumbnail-bytes", string(data))
	}
}

func TestGoogleManagerThumbnailUnsupported(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":            "doc-1",
			"mimeType":      "application/pdf",
			"thumbnailLink": "",
		})
	}))

	_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "doc-1"}, 256, 256)
	if !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
	}
}

func TestGoogleManagerThumbnailNotFound(t *testing.T) {
	provider := newGoogleManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": 404, "message": "File not found"},
		})
	}))

	_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "missing-id"}, 256, 256)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("ThumbnailManager() error = %v, want ErrNotFound", err)
	}
}
