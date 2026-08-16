package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSeafileManager(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api2/auth-token/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"token": "test-seafile-token"}`))
	})

	mux.HandleFunc("/api2/repos/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{
				"id": "repo-123",
				"name": "MyLibrary",
				"mtime": 1700000000
			}
		]`))
	})

	mux.HandleFunc("/api2/repos/repo-123/dir/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			op := r.FormValue("operation")
			if op == "mkdir" {
				w.WriteHeader(http.StatusCreated)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
			{
				"type": "dir",
				"id": "dir-1",
				"name": "Documents",
				"size": 0,
				"mtime": 1700000000
			},
			{
				"type": "file",
				"id": "file-1",
				"name": "picture.jpg",
				"size": 34567,
				"mtime": 1700000000
			}
		]`))
	})

	mux.HandleFunc("/api/v2.1/repos/repo-123/thumbnail/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if p == "/not_found.jpg" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if p == "/unsupported.zip" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-seafile-thumbnail-png"))
	})

	mux.HandleFunc("/api2/repos/repo-123/file/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"http://` + r.Host + `/fake-download/picture.jpg"`))
	})

	mux.HandleFunc("/api2/repos/repo-123/upload-link/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`"http://` + r.Host + `/fake-upload"`))
	})

	mux.HandleFunc("/fake-download/picture.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("downloaded-picture-content"))
	})

	mux.HandleFunc("/fake-upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name": "new.txt"}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	originalIssuedClient := newSeafileIssuedLinkHTTPClient
	newSeafileIssuedLinkHTTPClient = func(rawURL string) (*http.Client, error) {
		return server.Client(), nil
	}
	defer func() { newSeafileIssuedLinkHTTPClient = originalIssuedClient }()

	provider := &SeafileProvider{
		BaseURL:    server.URL,
		Username:   "testuser@example.com",
		Password:   "testpassword",
		Token:      "test-seafile-token",
		HTTPClient: server.Client(),
		repoCache:  map[string]string{"MyLibrary": "repo-123"},
	}

	t.Run("ConnectManager", func(t *testing.T) {
		ok, err := provider.ConnectManager(context.Background())
		if err != nil || !ok {
			t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("ListManager Root Repos", func(t *testing.T) {
		page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager() error = %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Name != "MyLibrary" || !page.Items[0].IsDir {
			t.Fatalf("root repos = %+v, want MyLibrary dir", page.Items)
		}
	})

	t.Run("ListManager Inside Repo", func(t *testing.T) {
		page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager() error = %v", err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("len(page.Items) = %d, want 2", len(page.Items))
		}
		if page.Items[0].Name != "Documents" || !page.Items[0].IsDir {
			t.Errorf("item 0 = %+v, want dir Documents", page.Items[0])
		}
		if page.Items[1].Name != "picture.jpg" || page.Items[1].IsDir || page.Items[1].Size != 34567 {
			t.Errorf("item 1 = %+v, want file picture.jpg 34567 bytes", page.Items[1])
		}
	})

	t.Run("ListManager Unauthorized", func(t *testing.T) {
		unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer unauthServer.Close()

		unauthProvider := &SeafileProvider{
			BaseURL:    unauthServer.URL,
			Token:      "bad-token",
			HTTPClient: unauthServer.Client(),
			repoCache:  map[string]string{"MyLibrary": "repo-123"},
		}
		_, err := unauthProvider.ListManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, ManagerListOptions{Limit: 10})
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ListManager() error = %v, want ErrAuth", err)
		}
	})

	t.Run("ThumbnailManager Success", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/MyLibrary/picture.jpg"}, 128, 128)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/png" {
			t.Errorf("contentType = %q, want image/png", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-seafile-thumbnail-png" {
			t.Errorf("body = %q, want fake-seafile-thumbnail-png", string(body))
		}
	})

	t.Run("ThumbnailManager NotFound", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/MyLibrary/not_found.jpg"}, 128, 128)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ThumbnailManager UnsupportedMedia", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/MyLibrary/unsupported.zip"}, 128, 128)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Errorf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("ResolveManagerPath", func(t *testing.T) {
		loc, crumbs, _, err := provider.ResolveManagerPath(context.Background(), "/MyLibrary/Documents/Work")
		if err != nil {
			t.Fatalf("ResolveManagerPath() error = %v", err)
		}
		if loc.Path != "/MyLibrary/Documents/Work" {
			t.Errorf("loc.Path = %q, want /MyLibrary/Documents/Work", loc.Path)
		}
		if len(crumbs) != 4 {
			t.Errorf("len(crumbs) = %d, want 4", len(crumbs))
		}
	})

	t.Run("DownloadManager", func(t *testing.T) {
		dl, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/MyLibrary/picture.jpg"})
		if err != nil {
			t.Fatalf("DownloadManager() error = %v", err)
		}
		defer dl.Stream.Close()

		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "downloaded-picture-content" {
			t.Errorf("dl.Stream = %q, want downloaded-picture-content", string(body))
		}
	})

	t.Run("CreateManagerDirectory", func(t *testing.T) {
		// Root "/" directory creation is forbidden (root is library level)
		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "NewLib")
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("CreateManagerDirectory(/) error = %v, want ErrManagerConflict", err)
		}

		// Creating inside repository succeeds
		err = provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "NewFolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory(/MyLibrary) error = %v", err)
		}
	})

	t.Run("UploadManager New File", func(t *testing.T) {
		content := []byte("new file content")
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "new.txt", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager() error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "new.txt" {
			t.Errorf("UploadManager() res = %+v, want uploaded new.txt", res)
		}
	})

	t.Run("UploadManager Conflict Strategies", func(t *testing.T) {
		content := []byte("file content")

		// 1. SKIP on existing file
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "picture.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatalf("UploadManager(SKIP) error = %v", err)
		}
		if res.Status != "skipped" || res.FinalName != "picture.jpg" {
			t.Errorf("UploadManager(SKIP) = %+v, want skipped picture.jpg", res)
		}

		// 2. OVERWRITE on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "picture.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager(OVERWRITE) error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "picture.jpg" {
			t.Errorf("UploadManager(OVERWRITE) = %+v, want uploaded picture.jpg", res)
		}

		// 3. OVERWRITE on existing directory -> ErrManagerConflict
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "Documents", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("UploadManager(OVERWRITE on dir) error = %v, want ErrManagerConflict", err)
		}

		// 4. RENAME on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "picture.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatalf("UploadManager(RENAME) error = %v", err)
		}
		if res.Status != "renamed" || res.FinalName != "picture (1).jpg" {
			t.Errorf("UploadManager(RENAME) = %+v, want renamed picture (1).jpg", res)
		}

		// 5. Invalid strategy on existing file -> error
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/MyLibrary"}, "picture.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "invalid"})
		if err == nil {
			t.Errorf("UploadManager(invalid) expected error, got nil")
		}
	})
}
