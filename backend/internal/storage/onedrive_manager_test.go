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

func newOneDriveManagerTestProvider(t *testing.T, handler http.Handler) *OneDriveProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newOneDriveProvider("test-token", server.URL+"/v1.0/me/drive", server.Client())
}

func TestOneDriveManagerList(t *testing.T) {
	provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("auth = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		if strings.HasSuffix(r.URL.Path, "/root") {
			_, _ = io.WriteString(w, `{"id":"root-id"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/children") {
			if r.URL.Query().Get("$top") != "2" {
				t.Fatalf("top = %q, want 2", r.URL.Query().Get("$top"))
			}
			_, _ = io.WriteString(w, `{
				"value": [
					{
						"id": "file-1",
						"name": "photo.jpg",
						"size": 1024,
						"eTag": "etag-1",
						"lastModifiedDateTime": "2026-08-16T12:00:00Z",
						"file": {
							"mimeType": "image/jpeg"
						}
					},
					{
						"id": "dir-1",
						"name": "Documents",
						"size": 4096,
						"eTag": "etag-2",
						"lastModifiedDateTime": "2026-08-16T12:05:00Z",
						"folder": {}
					}
				],
				"@odata.nextLink": "https://graph.microsoft.com/v1.0/me/drive/root/children?$top=2&$skiptoken=next-token"
			}`)
			return
		}
		http.NotFound(w, r)
	}))

	page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListManager() error = %v", err)
	}

	if len(page.Items) != 2 {
		t.Fatalf("len(page.Items) = %d, want 2", len(page.Items))
	}

	// First item: photo.jpg
	item0 := page.Items[0]
	if item0.Name != "photo.jpg" || item0.MIMEType != "image/jpeg" || item0.Locator.NativeID != "file-1" || item0.Size != 1024 || item0.IsDir {
		t.Fatalf("unexpected item 0: %#v", item0)
	}

	// Second item: Documents
	item1 := page.Items[1]
	if item1.Name != "Documents" || !item1.IsDir || item1.Locator.NativeID != "dir-1" || item1.Size != 4096 {
		t.Fatalf("unexpected item 1: %#v", item1)
	}

	// Next cursor check
	if page.NextCursor != "https://graph.microsoft.com/v1.0/me/drive/root/children?$top=2&$skiptoken=next-token" {
		t.Fatalf("NextCursor = %q", page.NextCursor)
	}
}

func TestOneDriveManagerConnect(t *testing.T) {
	provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/root") {
			_, _ = io.WriteString(w, `{"id":"root-id"}`)
			return
		}
		http.NotFound(w, r)
	}))

	ok, err := provider.ConnectManager(context.Background())
	if err != nil || !ok {
		t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestOneDriveManagerDownload(t *testing.T) {
	provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/root:/hello.txt:") {
			_, _ = io.WriteString(w, `{"id":"hello-id","name":"hello.txt","size":12,"lastModifiedDateTime":"2026-08-16T10:00:00Z","file":{}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			_, _ = w.Write([]byte("hello onedrive"))
			return
		}
		http.NotFound(w, r)
	}))

	download, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/hello.txt"})
	if err != nil {
		t.Fatalf("DownloadManager() error = %v", err)
	}
	defer download.Stream.Close()

	if download.Item.Name != "hello.txt" || download.Item.Size != 12 {
		t.Fatalf("download item = %#v", download.Item)
	}
	body, _ := io.ReadAll(download.Stream)
	if string(body) != "hello onedrive" {
		t.Fatalf("download body = %q, want 'hello onedrive'", string(body))
	}
}

func TestOneDriveManagerCreateDirectory(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/newfolder:") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/children") {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"new-dir-id","name":"newfolder","folder":{}}`)
				return
			}
			http.NotFound(w, r)
		}))

		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "newfolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory() error = %v", err)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/existing:") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"existing-id","name":"existing","folder":{}}`)
				return
			}
			http.NotFound(w, r)
		}))

		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "existing")
		if !errors.Is(err, ErrManagerConflict) {
			t.Fatalf("CreateManagerDirectory() error = %v, want ErrManagerConflict", err)
		}
	})
}

func TestOneDriveManagerUploadConflictStrategies(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/existing.txt:") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"existing-id","name":"existing.txt","size":5,"file":{}}`)
				return
			}
			http.NotFound(w, r)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "existing.txt", strings.NewReader("hello"), 5, ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatalf("UploadManager() error = %v", err)
		}
		if res.Status != "skipped" || res.FinalName != "existing.txt" {
			t.Fatalf("res = %#v, want skipped", res)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/target.txt:") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"target-id","name":"target.txt","size":5,"file":{}}`)
				return
			}
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "target.txt.tmp.") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"tmp-id","name":"target.txt.tmp","size":11}`)
				return
			}
			if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "target.txt.tmp.") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"target-id","name":"target.txt","size":11}`)
				return
			}
			http.NotFound(w, r)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "target.txt", strings.NewReader("new-content"), 11, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager() error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "target.txt" {
			t.Fatalf("res = %#v, want uploaded", res)
		}
	})

	t.Run("rename", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/report.pdf:") {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"id":"report-id","name":"report.pdf","size":5,"file":{}}`)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/root:/report (1).pdf:") || strings.HasSuffix(r.URL.EscapedPath(), "/root:/report%20%281%29.pdf:") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == http.MethodPut && (strings.Contains(r.URL.Path, "report (1).pdf") || strings.Contains(r.URL.EscapedPath(), "report%20%281%29.pdf")) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"id":"report-1-id","name":"report (1).pdf","size":6}`)
				return
			}
			http.NotFound(w, r)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "report.pdf", strings.NewReader("report"), 6, ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatalf("UploadManager() error = %v", err)
		}
		if res.Status != "renamed" || res.FinalName != "report (1).pdf" {
			t.Fatalf("res = %#v, want renamed to 'report (1).pdf'", res)
		}
	})
}

func TestOneDriveManagerResolvePath(t *testing.T) {
	provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/root:/Photos:") {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"id":"photos-id","name":"Photos","folder":{}}`)
			return
		}
		http.NotFound(w, r)
	}))

	// Existing path
	locator, breadcrumbs, fallback, err := provider.ResolveManagerPath(context.Background(), "/Photos")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/Photos) error = %v", err)
	}
	if fallback || locator.Path != "/Photos" || len(breadcrumbs) != 1 || breadcrumbs[0].Name != "Photos" {
		t.Fatalf("unexpected resolve result: locator = %#v, breadcrumbs = %#v, fallback = %v", locator, breadcrumbs, fallback)
	}

	// Missing subpath fallback
	locator2, breadcrumbs2, fallback2, err := provider.ResolveManagerPath(context.Background(), "/Photos/2026/Vacation")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/Photos/2026/Vacation) error = %v", err)
	}
	if !fallback2 || locator2.Path != "/Photos" || len(breadcrumbs2) != 1 {
		t.Fatalf("unexpected fallback resolve result: locator = %#v, breadcrumbs = %#v, fallback = %v", locator2, breadcrumbs2, fallback2)
	}
}

func TestOneDriveManagerThumbnail(t *testing.T) {
	t.Run("success with NativeID", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Path, "/items/item-123/thumbnails/0/c256x256/content") {
				t.Fatalf("path = %s, want .../items/item-123/thumbnails/0/c256x256/content", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-onedrive-jpeg-preview"))
		}))

		stream, contentType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "item-123", Path: "/photo.jpg"}, 256, 256)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if contentType != "image/jpeg" {
			t.Fatalf("contentType = %q, want image/jpeg", contentType)
		}
		data, _ := io.ReadAll(stream)
		if !bytes.Equal(data, []byte("fake-onedrive-jpeg-preview")) {
			t.Fatalf("thumbnail content = %q", string(data))
		}
	})

	t.Run("success with Path", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/root:/sunset.png:") {
				_, _ = io.WriteString(w, `{"id":"sunset-id","name":"sunset.png"}`)
				return
			}
			if strings.Contains(r.URL.Path, "/thumbnails/0/c128x128/content") {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("fake-onedrive-png-preview"))
				return
			}
			http.NotFound(w, r)
		}))

		stream, contentType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/sunset.png"}, 128, 128)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if contentType != "image/png" {
			t.Fatalf("contentType = %q, want image/png", contentType)
		}
	})

	t.Run("unsupported media", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnsupportedMediaType)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "item-999"}, 256, 256)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "item-404"}, 256, 256)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		provider := newOneDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "item-123"}, 256, 256)
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrAuth", err)
		}
	})
}
