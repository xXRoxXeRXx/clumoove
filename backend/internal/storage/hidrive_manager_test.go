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

func newHiDriveManagerTestProvider(t *testing.T, handler http.Handler) *HiDriveProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &HiDriveProvider{
		AccessToken: "test-token",
		BaseURL:     server.URL,
		HTTPClient:  server.Client(),
	}
}

func TestHiDriveManagerList(t *testing.T) {
	provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dir" {
			t.Fatalf("path = %s, want /dir", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "0,2" {
			t.Fatalf("limit = %q, want 0,2", r.URL.Query().Get("limit"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path": "/public",
			"name": "public",
			"members": []map[string]any{
				{
					"id":        "id-1",
					"name":      "photo.jpg",
					"type":      "file",
					"size":      1024,
					"mtime":     1700000000,
					"mime_type": "image/jpeg",
					"readable":  true,
					"writable":  true,
				},
				{
					"id":       "id-2",
					"name":     "docs",
					"type":     "dir",
					"mtime":    1700000001,
					"readable": true,
					"writable": true,
				},
			},
		})
	}))

	page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/public"}, ManagerListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("len(page.Items) = %d, want 2", len(page.Items))
	}
	if page.Items[0].Name != "photo.jpg" || page.Items[0].MIMEType != "image/jpeg" || page.Items[0].Locator.NativeID != "id-1" {
		t.Fatalf("unexpected item 0: %#v", page.Items[0])
	}
	if page.Items[1].Name != "docs" || !page.Items[1].IsDir {
		t.Fatalf("unexpected item 1: %#v", page.Items[1])
	}
	if page.NextCursor != "2" {
		t.Fatalf("NextCursor = %q, want 2", page.NextCursor)
	}
}

func TestHiDriveManagerConnect(t *testing.T) {
	provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/me" {
			t.Fatalf("path = %s, want /user/me", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"account": "user1", "home": "/users/user1"})
	}))

	ok, err := provider.ConnectManager(context.Background())
	if err != nil || !ok {
		t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestHiDriveManagerDownload(t *testing.T) {
	provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path": "/public/file.txt",
				"name": "file.txt",
				"type": "file",
				"size": 13,
			})
			return
		}
		if r.URL.Path == "/file" {
			_, _ = w.Write([]byte("hello hidrive"))
			return
		}
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))

	download, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/public/file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer download.Stream.Close()

	if download.Item.Name != "file.txt" || download.Item.Size != 13 {
		t.Fatalf("download item = %#v", download.Item)
	}
	content, _ := io.ReadAll(download.Stream)
	if string(content) != "hello hidrive" {
		t.Fatalf("download content = %q, want 'hello hidrive'", string(content))
	}
}

func TestHiDriveManagerCreateDirectory(t *testing.T) {
	provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/dir" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))

	err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/public"}, "new_folder")
	if err != nil {
		t.Fatalf("CreateManagerDirectory error: %v", err)
	}
}

func TestHiDriveManagerUploadConflictStrategies(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/meta" {
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": 100})
				return
			}
			t.Fatalf("unexpected call: %s", r.URL.Path)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/public"}, "file.txt", strings.NewReader("content"), 7, ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "skipped" || res.FinalName != "file.txt" {
			t.Fatalf("result = %#v, want skipped", res)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		renamedMoved := false
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/meta" {
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": 100})
				return
			}
			if r.URL.Path == "/dir" {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.URL.Path == "/file" && r.Method == http.MethodPost {
				// Uploading to temp file
				if !strings.Contains(r.URL.Query().Get("dir"), "public") || !strings.Contains(r.URL.Query().Get("name"), ".tmp.") {
					t.Fatalf("expected upload to temp file, got dir=%q name=%q", r.URL.Query().Get("dir"), r.URL.Query().Get("name"))
				}
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.URL.Path == "/file/move" && r.Method == http.MethodPost {
				if r.URL.Query().Get("on_exist") != "overwrite" || !strings.HasSuffix(r.URL.Query().Get("dst"), "public/file.txt") {
					t.Fatalf("expected move with on_exist=overwrite to public/file.txt, got query=%v", r.URL.Query())
				}
				renamedMoved = true
				w.WriteHeader(http.StatusOK)
				return
			}
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/public"}, "file.txt", strings.NewReader("content"), 7, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "uploaded" || res.FinalName != "file.txt" {
			t.Fatalf("result = %#v, want uploaded file.txt", res)
		}
		if !renamedMoved {
			t.Fatal("expected file to be moved with on_exist=overwrite")
		}
	})

	t.Run("overwrite upload failure cleans temp without deleting original", func(t *testing.T) {
		tempDeleted := false
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/meta" {
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": 100})
				return
			}
			if r.URL.Path == "/dir" {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.URL.Path == "/file" && r.Method == http.MethodPost {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.URL.Path == "/file" && r.Method == http.MethodDelete {
				if strings.Contains(r.URL.Query().Get("path"), ".tmp.") {
					tempDeleted = true
					w.WriteHeader(http.StatusOK)
					return
				}
				t.Fatalf("unexpected delete of path: %s", r.URL.Query().Get("path"))
			}
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}))

		_, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/public"}, "file.txt", strings.NewReader("content"), 7, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err == nil {
			t.Fatal("expected error on failed upload, got nil")
		}
		if !tempDeleted {
			t.Fatal("expected temp file to be deleted on upload failure")
		}
	})

	t.Run("rename", func(t *testing.T) {
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/meta" {
				if r.URL.Query().Get("path") == "public/file.txt" || r.URL.Query().Get("path") == "/public/file.txt" {
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "file", "size": 100})
					return
				}
				// file (1).txt does not exist
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Path == "/dir" {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.URL.Path == "/file" && r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				return
			}
			t.Fatalf("unexpected call: %s %s", r.Method, r.URL.Path)
		}))

		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/public"}, "file.txt", strings.NewReader("content"), 7, ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != "renamed" || res.FinalName != "file (1).txt" {
			t.Fatalf("result = %#v, want renamed", res)
		}
	})
}

func TestHiDriveManagerThumbnail(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/file/thumbnail" {
				t.Fatalf("path = %s, want /file/thumbnail", r.URL.Path)
			}
			if r.URL.Query().Get("path") != "/public/photo.jpg" {
				t.Fatalf("query path = %q", r.URL.Query().Get("path"))
			}
			if r.URL.Query().Get("width") != "128" || r.URL.Query().Get("height") != "128" {
				t.Fatalf("dimensions = %s x %s", r.URL.Query().Get("width"), r.URL.Query().Get("height"))
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-jpeg-binary-data"))
		}))

		stream, contentType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/public/photo.jpg"}, 128, 128)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()

		if contentType != "image/jpeg" {
			t.Fatalf("contentType = %q, want image/jpeg", contentType)
		}
		data, _ := io.ReadAll(stream)
		if !bytes.Equal(data, []byte("fake-jpeg-binary-data")) {
			t.Fatalf("thumbnail body = %q", string(data))
		}
	})

	t.Run("unsupported format", func(t *testing.T) {
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnsupportedMediaType)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/public/archive.zip"}, 128, 128)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Fatalf("err = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		provider := newHiDriveManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/public/missing.jpg"}, 128, 128)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}
