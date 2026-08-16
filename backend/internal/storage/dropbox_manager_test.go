package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDropboxManager(t *testing.T) {
	mux := http.NewServeMux()

	var lastUploadArg map[string]any
	var uploadMu sync.Mutex

	mux.HandleFunc("/2/users/get_current_account", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer unauth-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"account_id": "dbid:123"}`))
	})

	mux.HandleFunc("/2/files/list_folder", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer unauth-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = jsonDecode(r.Body, &body)
		p, _ := body["path"].(string)
		if strings.Contains(p, "not_found") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error_summary": "path/not_found/...", "error": {".tag": "path", "path": {".tag": "not_found"}}}`))
			return
		}
		if strings.Contains(p, "overflow") {
			// Return 12 items to test limit capping
			entries := make([]map[string]any, 0, 12)
			for i := 1; i <= 12; i++ {
				entries = append(entries, map[string]any{
					".tag":            "file",
					"id":              fmt.Sprintf("id:overflow_%d", i),
					"name":            fmt.Sprintf("overflow_%d.txt", i),
					"path_display":    fmt.Sprintf("/overflow_%d.txt", i),
					"size":            100,
					"server_modified": "2026-08-15T12:00:00Z",
				})
			}
			respBytes, _ := json.Marshal(map[string]any{
				"entries":  entries,
				"cursor":   "cursor_overflow",
				"has_more": false,
			})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(respBytes)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"entries": [
				{
					".tag": "folder",
					"id": "id:folder1",
					"name": "Pictures",
					"path_display": "/Pictures"
				},
				{
					".tag": "file",
					"id": "id:file1",
					"name": "photo.jpg",
					"path_display": "/photo.jpg",
					"size": 45678,
					"server_modified": "2026-08-15T12:00:00Z"
				}
			],
			"cursor": "cursor_page_1",
			"has_more": true
		}`))
	})

	mux.HandleFunc("/2/files/list_folder/continue", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = jsonDecode(r.Body, &body)
		c, _ := body["cursor"].(string)
		if strings.Contains(c, "not_found") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error_summary": "path/not_found/...", "error": {".tag": "path", "path": {".tag": "not_found"}}}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"entries": [
				{
					".tag": "file",
					"id": "id:file2",
					"name": "video.mp4",
					"path_display": "/video.mp4",
					"size": 99999,
					"server_modified": "2026-08-15T13:00:00Z"
				}
			],
			"cursor": "cursor_page_2",
			"has_more": false
		}`))
	})

	mux.HandleFunc("/2/files/get_metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		_ = jsonDecode(r.Body, &body)
		p, _ := body["path"].(string)
		if strings.Contains(p, "not_found") || strings.Contains(p, "NewFolder") || strings.Contains(p, "new.txt") || strings.Contains(p, "photo (1).jpg") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_summary": "path/not_found", "error": {".tag": "path", "path": {".tag": "not_found"}}}`))
			return
		}
		if p == "/Pictures" || p == "id:folder1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				".tag": "folder",
				"id": "id:folder1",
				"name": "Pictures",
				"path_display": "/Pictures"
			}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			".tag": "file",
			"id": "id:file1",
			"name": "photo.jpg",
			"path_display": "/photo.jpg",
			"size": 45678,
			"server_modified": "2026-08-15T12:00:00Z"
		}`))
	})

	mux.HandleFunc("/2/files/get_thumbnail_v2", func(w http.ResponseWriter, r *http.Request) {
		arg := r.Header.Get("Dropbox-API-Arg")
		if strings.Contains(arg, "not_found") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error_summary": "path/not_found/..."}`))
			return
		}
		if strings.Contains(arg, "unsupported") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error_summary": "unsupported_extension/..."}`))
			return
		}
		if strings.Contains(arg, "auth_fail") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Content endpoints in Dropbox respond with application/octet-stream
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fake-dropbox-thumbnail-jpeg"))
	})

	mux.HandleFunc("/2/files/create_folder_v2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"metadata": {".tag": "folder", "name": "NewFolder", "path_display": "/NewFolder"}}`))
	})

	mux.HandleFunc("/2/files/upload", func(w http.ResponseWriter, r *http.Request) {
		argStr := r.Header.Get("Dropbox-API-Arg")
		var arg map[string]any
		_ = json.Unmarshal([]byte(argStr), &arg)
		uploadMu.Lock()
		lastUploadArg = arg
		uploadMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name": "uploaded.txt", "path_display": "/uploaded.txt", "size": 14}`))
	})

	mux.HandleFunc("/2/files/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("download-stream-content"))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	provider := &DropboxProvider{
		AccessToken: "test-token",
		HTTPClient: &http.Client{
			Transport: &testRewriteTransport{
				targetBaseURL: server.URL,
				underlying:    server.Client().Transport,
			},
		},
	}

	t.Run("ConnectManager", func(t *testing.T) {
		ok, err := provider.ConnectManager(context.Background())
		if err != nil || !ok {
			t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("ListManager Initial & Continue", func(t *testing.T) {
		page1, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager() error = %v", err)
		}
		if len(page1.Items) != 2 {
			t.Fatalf("len(page1.Items) = %d, want 2", len(page1.Items))
		}
		if page1.Items[0].Name != "Pictures" || !page1.Items[0].IsDir {
			t.Errorf("item 0 = %+v, want dir Pictures", page1.Items[0])
		}
		if page1.NextCursor != "cursor_page_1" {
			t.Errorf("NextCursor = %q, want cursor_page_1", page1.NextCursor)
		}

		page2, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Cursor: page1.NextCursor})
		if err != nil {
			t.Fatalf("ListManager(continue) error = %v", err)
		}
		if len(page2.Items) != 1 {
			t.Fatalf("len(page2.Items) = %d, want 1", len(page2.Items))
		}
		if page2.Items[0].Name != "video.mp4" {
			t.Errorf("item 0 = %+v, want video.mp4", page2.Items[0])
		}
		if page2.NextCursor != "" {
			t.Errorf("NextCursor = %q, want empty", page2.NextCursor)
		}
	})

	t.Run("ListManager Limit Cap", func(t *testing.T) {
		page, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/overflow"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListManager(overflow) error = %v", err)
		}
		if len(page.Items) > 10 {
			t.Errorf("len(page.Items) = %d, want <= 10", len(page.Items))
		}
	})

	t.Run("ListManager 409 NotFound", func(t *testing.T) {
		_, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/not_found_dir"}, ManagerListOptions{})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ListManager(not_found) error = %v, want ErrNotFound", err)
		}

		_, err = provider.ListManager(context.Background(), ManagerLocator{}, ManagerListOptions{Cursor: "cursor_not_found"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ListManager(continue not_found) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ListManager Auth Failure", func(t *testing.T) {
		unauth := &DropboxProvider{
			AccessToken: "unauth-token",
			HTTPClient:  provider.HTTPClient,
		}
		_, err := unauth.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{})
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ListManager(unauth) error = %v, want ErrAuth", err)
		}
	})

	t.Run("DownloadManager", func(t *testing.T) {
		// Download file by path
		dl, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/photo.jpg"})
		if err != nil {
			t.Fatalf("DownloadManager(/photo.jpg) error = %v", err)
		}
		defer dl.Stream.Close()
		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "download-stream-content" {
			t.Errorf("dl body = %q, want download-stream-content", string(body))
		}

		// Download file by NativeID
		dlID, err := provider.DownloadManager(context.Background(), ManagerLocator{NativeID: "id:file1"})
		if err != nil {
			t.Fatalf("DownloadManager(id:file1) error = %v", err)
		}
		defer dlID.Stream.Close()

		// Download directory rejected
		_, err = provider.DownloadManager(context.Background(), ManagerLocator{Path: "/Pictures"})
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("DownloadManager(/Pictures dir) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ThumbnailManager Success", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/photo.jpg", NativeID: "id:file1"}, 256, 256)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/jpeg" {
			t.Errorf("contentType = %q, want image/jpeg", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-dropbox-thumbnail-jpeg" {
			t.Errorf("body = %q, want fake-dropbox-thumbnail-jpeg", string(body))
		}

		// NativeID only
		streamID, cTypeID, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "id:file1"}, 256, 256)
		if err != nil {
			t.Fatalf("ThumbnailManager(id:file1) error = %v", err)
		}
		defer streamID.Close()
		if cTypeID != "image/jpeg" {
			t.Errorf("contentTypeID = %q, want image/jpeg", cTypeID)
		}
	})

	t.Run("ThumbnailManager AuthFail", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/auth_fail.jpg"}, 256, 256)
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ThumbnailManager(auth_fail) error = %v, want ErrAuth", err)
		}
	})

	t.Run("ThumbnailManager UnsupportedMedia", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/unsupported.bin"}, 256, 256)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Errorf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("ThumbnailManager NotFound", func(t *testing.T) {
		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/not_found.jpg"}, 256, 256)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("ResolveManagerPath", func(t *testing.T) {
		// Root
		rootLoc, rootCrumbs, fallback, err := provider.ResolveManagerPath(context.Background(), "/")
		if err != nil {
			t.Fatalf("ResolveManagerPath(/) error = %v", err)
		}
		if fallback || rootLoc.Path != "/" || len(rootCrumbs) != 1 || rootCrumbs[0].Locator.Path != "/" {
			t.Errorf("root ResolveManagerPath = %+v, crumbs = %+v", rootLoc, rootCrumbs)
		}

		// Deep path
		loc, crumbs, fallback, err := provider.ResolveManagerPath(context.Background(), "/Pictures/2026")
		if err != nil {
			t.Fatalf("ResolveManagerPath() error = %v", err)
		}
		if fallback {
			t.Errorf("fallback = true, want false")
		}
		if loc.Path != "/Pictures/2026" {
			t.Errorf("loc.Path = %q, want /Pictures/2026", loc.Path)
		}
		if len(crumbs) != 3 {
			t.Errorf("len(crumbs) = %d, want 3", len(crumbs))
		}
		for i, c := range crumbs {
			if !strings.HasPrefix(c.Locator.Path, "/") {
				t.Errorf("crumb[%d].Locator.Path = %q does not start with /", i, c.Locator.Path)
			}
		}
	})

	t.Run("CreateManagerDirectory", func(t *testing.T) {
		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "NewFolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory() error = %v", err)
		}
	})

	t.Run("UploadManager Conflict Strategies", func(t *testing.T) {
		content := []byte("upload content")

		// 1. SKIP on existing photo.jpg
		resSkip, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "photo.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatalf("UploadManager(SKIP) error = %v", err)
		}
		if resSkip.Status != "skipped" || resSkip.FinalName != "photo.jpg" {
			t.Errorf("resSkip = %+v, want status skipped, finalName photo.jpg", resSkip)
		}

		// 2. OVERWRITE on existing photo.jpg -> verify mode overwrite
		resOver, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "photo.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager(OVERWRITE) error = %v", err)
		}
		if resOver.Status != "uploaded" || resOver.FinalName != "photo.jpg" {
			t.Errorf("resOver = %+v, want status uploaded", resOver)
		}
		uploadMu.Lock()
		if lastUploadArg["mode"] != "overwrite" {
			t.Errorf("upload mode for OVERWRITE = %v, want overwrite", lastUploadArg["mode"])
		}
		uploadMu.Unlock()

		// 3. RENAME on existing photo.jpg -> candidate photo (1).jpg not found -> renamed with mode add
		resRename, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "photo.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatalf("UploadManager(RENAME) error = %v", err)
		}
		if resRename.Status != "renamed" || resRename.FinalName != "photo (1).jpg" {
			t.Errorf("resRename = %+v, want status renamed, finalName photo (1).jpg", resRename)
		}
		uploadMu.Lock()
		if lastUploadArg["mode"] != "add" {
			t.Errorf("upload mode for RENAME = %v, want add", lastUploadArg["mode"])
		}
		uploadMu.Unlock()

		// 4. OVERWRITE on directory /Pictures -> ErrManagerConflict
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "Pictures", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("UploadManager(OVERWRITE dir) error = %v, want ErrManagerConflict", err)
		}

		// 5. Invalid strategy -> error
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "photo.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "invalid"})
		if err == nil {
			t.Errorf("UploadManager(invalid) expected error, got nil")
		}
	})
}

type testRewriteTransport struct {
	targetBaseURL string
	underlying    http.RoundTripper
}

func (t *testRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	targetURL := strings.TrimPrefix(t.targetBaseURL, "http://")
	newReq.URL.Host = targetURL
	return t.underlying.RoundTrip(newReq)
}

func jsonDecode(r io.Reader, v any) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}
