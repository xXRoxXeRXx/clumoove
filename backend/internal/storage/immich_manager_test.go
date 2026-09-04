package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImmichManager(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api-keys/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id": "user-123"}`))
	})

	mux.HandleFunc("/search/metadata", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("x-api-key")
		if auth == "unauthorized-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		page, _ := req["page"].(float64)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if int(page) == 2 {
			_, _ = w.Write([]byte(`{
				"assets": {
					"items": [
						{
							"id": "asset-102",
							"originalFileName": "dog.jpg",
							"originalMimeType": "image/jpeg",
							"fileModifiedAt": "2026-08-15T13:00:00Z",
							"exifInfo": {
								"fileSizeInByte": 34567
							}
						}
					],
					"nextPage": null
				}
			}`))
			return
		}

		_, _ = w.Write([]byte(`{
			"assets": {
				"items": [
					{
						"id": "asset-101",
						"originalFileName": "cat.jpg",
						"originalMimeType": "image/jpeg",
						"fileModifiedAt": "2026-08-15T12:00:00Z",
						"exifInfo": {
							"fileSizeInByte": 23456
						}
					}
				],
				"nextPage": 2
			}
		}`))
	})

	mux.HandleFunc("/assets/asset-101", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "unauthorized-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "asset-101",
			"originalFileName": "cat.jpg",
			"originalMimeType": "image/jpeg",
			"fileModifiedAt": "2026-08-15T12:00:00Z",
			"exifInfo": {
				"fileSizeInByte": 23456
			}
		}`))
	})

	mux.HandleFunc("/assets/asset-101/original", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("original-image-content"))
	})

	mux.HandleFunc("/assets/asset-101/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		size := q.Get("size")
		if size != "thumbnail" && size != "preview" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.WriteHeader(http.StatusOK)
		if size == "preview" {
			_, _ = w.Write([]byte("fake-immich-preview-webp"))
		} else {
			_, _ = w.Write([]byte("fake-immich-thumbnail-webp"))
		}
	})

	mux.HandleFunc("/assets/asset-404/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	mux.HandleFunc("/assets/asset-401/thumbnail", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	provider := &ImmichProvider{
		BaseURL:    server.URL,
		APIKey:     "test-api-key",
		HTTPClient: server.Client(),
	}

	t.Run("ConnectManager", func(t *testing.T) {
		ok, err := provider.ConnectManager(context.Background())
		if err != nil || !ok {
			t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("ListManager Pagination", func(t *testing.T) {
		page1, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager() error = %v", err)
		}
		if len(page1.Items) != 1 {
			t.Fatalf("len(page1.Items) = %d, want 1", len(page1.Items))
		}
		if page1.Items[0].Name != "cat.jpg" || page1.Items[0].Locator.NativeID != "asset-101" {
			t.Errorf("item 0 = %+v, want cat.jpg asset-101", page1.Items[0])
		}
		if page1.NextCursor != "2" {
			t.Errorf("NextCursor = %q, want 2", page1.NextCursor)
		}

		page2, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Cursor: page1.NextCursor})
		if err != nil {
			t.Fatalf("ListManager(page 2) error = %v", err)
		}
		if len(page2.Items) != 1 || page2.Items[0].Name != "dog.jpg" {
			t.Errorf("page 2 item = %+v, want dog.jpg", page2.Items[0])
		}
		if page2.NextCursor != "" {
			t.Errorf("page 2 NextCursor = %q, want empty", page2.NextCursor)
		}
	})

	t.Run("ListManager NonRoot Rejected", func(t *testing.T) {
		_, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/subfolder"}, ManagerListOptions{})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ListManager(/subfolder) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListManager Auth Failure", func(t *testing.T) {
		unauthProvider := &ImmichProvider{
			BaseURL:    server.URL,
			APIKey:     "unauthorized-key",
			HTTPClient: server.Client(),
		}
		_, err := unauthProvider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{})
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ListManager(auth_fail) error = %v, want ErrAuth", err)
		}
	})

	t.Run("DownloadManager", func(t *testing.T) {
		dl, err := provider.DownloadManager(context.Background(), ManagerLocator{NativeID: "asset-101"})
		if err != nil {
			t.Fatalf("DownloadManager() error = %v", err)
		}
		defer dl.Stream.Close()

		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "original-image-content" {
			t.Errorf("dl.Stream = %q, want original-image-content", string(body))
		}
	})

	t.Run("ThumbnailManager Success Thumbnail", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "asset-101"}, 128, 128)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/webp" {
			t.Errorf("contentType = %q, want image/webp", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-immich-thumbnail-webp" {
			t.Errorf("body = %q, want fake-immich-thumbnail-webp", string(body))
		}
	})

	t.Run("ThumbnailManager Success Preview", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "asset-101"}, 512, 512)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/webp" {
			t.Errorf("contentType = %q, want image/webp", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-immich-preview-webp" {
			t.Errorf("body = %q, want fake-immich-preview-webp", string(body))
		}
	})

	t.Run("ThumbnailManager NotFound", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "asset-404"}, 128, 128)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ThumbnailManager Auth Failure", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "asset-401"}, 128, 128)
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ThumbnailManager(401) error = %v, want ErrAuth", err)
		}
	})

	t.Run("ResolveManagerPath", func(t *testing.T) {
		loc, crumbs, fallback, err := provider.ResolveManagerPath(context.Background(), "/asset-101")
		if err != nil {
			t.Fatalf("ResolveManagerPath() error = %v", err)
		}
		if !fallback {
			t.Errorf("fallback = false, want true (Immich flat library)")
		}
		if loc.Path != "/" {
			t.Errorf("loc.Path = %q, want /", loc.Path)
		}
		if len(crumbs) != 1 || crumbs[0].Locator.Path != "/" {
			t.Errorf("crumbs = %+v, want root only", crumbs)
		}
	})
}

func TestImmichManagerDeleteUsesNativeAssetID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/assets" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct{ IDs []string `json:"ids"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) != 1 || body.IDs[0] != "asset-id" {
			t.Fatalf("delete body = %#v, err = %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	provider := &ImmichProvider{BaseURL: server.URL, APIKey: "key", HTTPClient: server.Client()}
	if err := provider.DeleteManagerItem(context.Background(), ManagerLocator{NativeID: "asset-id", Path: "/untrusted-name.jpg"}, false); err != nil {
		t.Fatal(err)
	}
	if err := provider.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/asset-id"}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("path-only deletion error = %v, want ErrNotFound", err)
	}
}
