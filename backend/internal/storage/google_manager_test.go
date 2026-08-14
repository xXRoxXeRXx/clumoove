package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	return &GoogleProvider{driveService: service}
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
