package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKoofrManager(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v2/mounts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"mounts": [{"id": "mount-primary-1", "isPrimary": true}]}`))
	})

	mux.HandleFunc("/api/v2/mounts/mount-primary-1/files/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"files": [
				{
					"name": "Docs",
					"type": "dir",
					"modified": 1700000000000,
					"size": 0
				},
				{
					"name": "sample.jpg",
					"type": "file",
					"modified": 1700000000000,
					"size": 54321
				}
			]
		}`))
	})

	mux.HandleFunc("/api/v2/mounts/mount-primary-1/files/info", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if strings.Contains(p, "not_found") || strings.Contains(p, "NewFolder") || strings.Contains(p, "new.txt") || strings.Contains(p, "sample (1).jpg") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(p, "Docs") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"name": "Docs",
				"type": "dir",
				"modified": 1700000000000,
				"size": 0
			}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"name": "sample.jpg",
			"type": "file",
			"modified": 1700000000000,
			"size": 54321
		}`))
	})

	mux.HandleFunc("/content/api/v2/mounts/mount-primary-1/files/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if strings.Contains(p, "not_found") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(p, "unsupported") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-koofr-thumbnail-jpeg"))
	})

	mux.HandleFunc("/content/api/v2/mounts/mount-primary-1/files/get", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("downloaded-sample-content"))
	})

	mux.HandleFunc("/api/v2/mounts/mount-primary-1/files/folder", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/content/api/v2/mounts/mount-primary-1/files/put", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name": "new.txt", "type": "file", "size": 22, "modified": 1700000000000}`))
	})

	server := httptest.NewTLSServer(mux)
	defer server.Close()

	provider := newTestKoofrProvider(server)
	provider.mountID = "mount-primary-1"

	t.Run("ConnectManager", func(t *testing.T) {
		ok, err := provider.ConnectManager(context.Background())
		if err != nil || !ok {
			t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("ListManager", func(t *testing.T) {
		page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager() error = %v", err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("len(page.Items) = %d, want 2", len(page.Items))
		}
		if page.Items[0].Name != "Docs" || !page.Items[0].IsDir {
			t.Errorf("item 0 = %+v, want dir Docs", page.Items[0])
		}
		if page.Items[1].Name != "sample.jpg" || page.Items[1].IsDir || page.Items[1].Size != 54321 {
			t.Errorf("item 1 = %+v, want file sample.jpg 54321 bytes", page.Items[1])
		}
	})

	t.Run("ListManager Unauthorized", func(t *testing.T) {
		unauthServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer unauthServer.Close()

		unauthProvider := newTestKoofrProvider(unauthServer)
		_, err := unauthProvider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ListManager() error = %v, want ErrAuth", err)
		}
	})

	t.Run("ThumbnailManager Success", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/sample.jpg"}, 256, 256)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/jpeg" {
			t.Errorf("contentType = %q, want image/jpeg", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-koofr-thumbnail-jpeg" {
			t.Errorf("body = %q, want fake-koofr-thumbnail-jpeg", string(body))
		}
	})

	t.Run("ThumbnailManager NotFound", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/not_found.jpg"}, 256, 256)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ThumbnailManager UnsupportedMedia", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/unsupported.zip"}, 256, 256)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Errorf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("ResolveManagerPath", func(t *testing.T) {
		loc, crumbs, _, err := provider.ResolveManagerPath(context.Background(), "/Docs/2026/Reports")
		if err != nil {
			t.Fatalf("ResolveManagerPath() error = %v", err)
		}
		if loc.Path != "/Docs/2026/Reports" {
			t.Errorf("loc.Path = %q, want /Docs/2026/Reports", loc.Path)
		}
		if len(crumbs) != 4 {
			t.Errorf("len(crumbs) = %d, want 4", len(crumbs))
		}
	})

	t.Run("DownloadManager", func(t *testing.T) {
		dl, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/sample.jpg"})
		if err != nil {
			t.Fatalf("DownloadManager() error = %v", err)
		}
		defer dl.Stream.Close()

		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "downloaded-sample-content" {
			t.Errorf("dl.Stream = %q, want downloaded-sample-content", string(body))
		}
	})

	t.Run("CreateManagerDirectory", func(t *testing.T) {
		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "NewFolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory() error = %v", err)
		}
	})

	t.Run("UploadManager New File", func(t *testing.T) {
		content := []byte("koofr new file content")
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "new.txt", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
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
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "sample.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatalf("UploadManager(SKIP) error = %v", err)
		}
		if res.Status != "skipped" || res.FinalName != "sample.jpg" {
			t.Errorf("UploadManager(SKIP) = %+v, want skipped sample.jpg", res)
		}

		// 2. OVERWRITE on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "sample.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager(OVERWRITE) error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "sample.jpg" {
			t.Errorf("UploadManager(OVERWRITE) = %+v, want uploaded sample.jpg", res)
		}

		// 3. OVERWRITE on existing directory -> ErrManagerConflict
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "Docs", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("UploadManager(OVERWRITE on dir) error = %v, want ErrManagerConflict", err)
		}

		// 4. RENAME on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "sample.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatalf("UploadManager(RENAME) error = %v", err)
		}
		if res.Status != "renamed" || res.FinalName != "sample (1).jpg" {
			t.Errorf("UploadManager(RENAME) = %+v, want renamed sample (1).jpg", res)
		}

		// 5. Invalid strategy on existing file -> error
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "sample.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "invalid"})
		if err == nil {
			t.Errorf("UploadManager(invalid) expected error, got nil")
		}
	})
}
