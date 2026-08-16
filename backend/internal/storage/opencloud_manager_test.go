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

func TestOpenCloudManager(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/dav/spaces/space-1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			if strings.Contains(r.URL.Path, "NewFolder") || strings.Contains(r.URL.Path, "new.txt") || strings.Contains(r.URL.Path, "missing") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			depth := r.Header.Get("Depth")
			if depth == "0" {
				if strings.HasSuffix(r.URL.Path, "/holiday.jpg") {
					w.Header().Set("Content-Type", "application/xml; charset=utf-8")
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
	<d:response>
		<d:href>/dav/spaces/space-1/holiday.jpg</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype/>
				<d:getcontentlength>12345</d:getcontentlength>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
</d:multistatus>`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/Docs/") || strings.HasSuffix(r.URL.Path, "/Docs") {
					w.Header().Set("Content-Type", "application/xml; charset=utf-8")
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
	<d:response>
		<d:href>/dav/spaces/space-1/Docs/</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype><d:collection/></d:resourcetype>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
</d:multistatus>`))
					return
				}
				if r.URL.Path == "/dav/spaces/space-1/" || r.URL.Path == "/dav/spaces/space-1" {
					w.Header().Set("Content-Type", "application/xml; charset=utf-8")
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
	<d:response>
		<d:href>/dav/spaces/space-1/</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype><d:collection/></d:resourcetype>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
</d:multistatus>`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
	<d:response>
		<d:href>/dav/spaces/space-1/</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype><d:collection/></d:resourcetype>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/dav/spaces/space-1/Docs/</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype><d:collection/></d:resourcetype>
				<oc:fileid>2001</oc:fileid>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
	<d:response>
		<d:href>/dav/spaces/space-1/holiday.jpg</d:href>
		<d:propstat>
			<d:prop>
				<d:resourcetype/>
				<d:getcontentlength>12345</d:getcontentlength>
				<d:getcontenttype>image/jpeg</d:getcontenttype>
				<d:getlastmodified>Sun, 06 Nov 1994 08:49:37 GMT</d:getlastmodified>
				<oc:fileid>2002</oc:fileid>
			</d:prop>
			<d:status>HTTP/1.1 200 OK</d:status>
		</d:propstat>
	</d:response>
</d:multistatus>`))
			return
		}
		if r.Method == "MKCOL" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == "MOVE" {
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == http.MethodGet {
			if r.URL.Query().Get("preview") == "1" {
				if strings.Contains(r.URL.Path, "not_found") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if strings.Contains(r.URL.Path, "unsupported") {
					w.WriteHeader(http.StatusUnsupportedMediaType)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("fake-opencloud-preview-bytes"))
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("image content"))
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusCreated)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	provider := &OpenCloudProvider{
		davProvider: &davProvider{
			providerName:           "opencloud",
			BaseURL:                server.URL + "/dav/spaces/space-1",
			Username:               "testuser",
			Password:               "testpass",
			HTTPClient:             server.Client(),
			pb:                     openCloudPaths{},
			supportedResourceTypes: map[string]bool{"files": true},
		},
	}

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
			t.Errorf("first item = %+v, want dir Docs", page.Items[0])
		}
		if page.Items[1].Name != "holiday.jpg" || page.Items[1].IsDir || page.Items[1].Size != 12345 {
			t.Errorf("second item = %+v, want file holiday.jpg (12345 bytes)", page.Items[1])
		}
	})

	t.Run("ListManager Unauthorized", func(t *testing.T) {
		unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer unauthServer.Close()

		unauthProvider := &OpenCloudProvider{
			davProvider: &davProvider{
				providerName: "opencloud",
				BaseURL:      unauthServer.URL + "/dav/spaces/space-1",
				HTTPClient:   unauthServer.Client(),
				pb:           openCloudPaths{},
			},
		}
		_, err := unauthProvider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if !errors.Is(err, ErrAuth) {
			t.Errorf("ListManager() error = %v, want ErrAuth", err)
		}
	})

	t.Run("ThumbnailManager Success", func(t *testing.T) {
		stream, cType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{Path: "/holiday.jpg"}, 128, 128)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if cType != "image/png" {
			t.Errorf("contentType = %q, want image/png", cType)
		}
		body, _ := io.ReadAll(stream)
		if string(body) != "fake-opencloud-preview-bytes" {
			t.Errorf("body = %q, want fake-opencloud-preview-bytes", string(body))
		}
	})

	t.Run("ResolveManagerPath", func(t *testing.T) {
		loc, crumbs, fallback, err := provider.ResolveManagerPath(context.Background(), "/Docs")
		if err != nil {
			t.Fatalf("ResolveManagerPath() error = %v", err)
		}
		if fallback {
			t.Errorf("fallback = true, want false")
		}
		if loc.Path != "/Docs" {
			t.Errorf("loc.Path = %q, want /Docs", loc.Path)
		}
		if len(crumbs) != 1 {
			t.Errorf("len(crumbs) = %d, want 1", len(crumbs))
		}

		// Non-existent segment should set fallback: true
		locMissing, _, fallbackMissing, err := provider.ResolveManagerPath(context.Background(), "/Docs/missing")
		if err != nil {
			t.Fatalf("ResolveManagerPath(missing) error = %v", err)
		}
		if !fallbackMissing {
			t.Errorf("fallbackMissing = false, want true")
		}
		if locMissing.Path != "/Docs" {
			t.Errorf("locMissing.Path = %q, want /Docs", locMissing.Path)
		}
	})

	t.Run("DownloadManager", func(t *testing.T) {
		dl, err := provider.DownloadManager(context.Background(), ManagerLocator{Path: "/holiday.jpg"})
		if err != nil {
			t.Fatalf("DownloadManager() error = %v", err)
		}
		defer dl.Stream.Close()
		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "image content" {
			t.Errorf("body = %q, want 'image content'", string(body))
		}
	})

	t.Run("CreateManagerDirectory", func(t *testing.T) {
		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "NewFolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory() error = %v", err)
		}

		// Conflict on existing
		err = provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "Docs")
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("CreateManagerDirectory(Docs) error = %v, want ErrManagerConflict", err)
		}
	})

	t.Run("UploadManager New File", func(t *testing.T) {
		content := []byte("upload content")
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "new.txt", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager() error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "new.txt" {
			t.Errorf("UploadManager() res = %+v, want status uploaded and finalName new.txt", res)
		}
	})

	t.Run("UploadManager Conflict Strategies", func(t *testing.T) {
		content := []byte("conflict content")

		// 1. SKIP on existing
		res, err := provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "holiday.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil {
			t.Fatalf("UploadManager(SKIP) error = %v", err)
		}
		if res.Status != "skipped" || res.FinalName != "holiday.jpg" {
			t.Errorf("UploadManager(SKIP) = %+v, want skipped holiday.jpg", res)
		}

		// 2. OVERWRITE on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "holiday.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil {
			t.Fatalf("UploadManager(OVERWRITE) error = %v", err)
		}
		if res.Status != "uploaded" || res.FinalName != "holiday.jpg" {
			t.Errorf("UploadManager(OVERWRITE) = %+v, want uploaded holiday.jpg", res)
		}

		// 3. OVERWRITE on existing directory -> ErrManagerConflict
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "Docs", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if !errors.Is(err, ErrManagerConflict) {
			t.Errorf("UploadManager(OVERWRITE on dir) error = %v, want ErrManagerConflict", err)
		}

		// 4. RENAME on existing file
		res, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "holiday.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil {
			t.Fatalf("UploadManager(RENAME) error = %v", err)
		}
		if res.Status != "renamed" || res.FinalName != "holiday (1).jpg" {
			t.Errorf("UploadManager(RENAME) = %+v, want renamed holiday (1).jpg", res)
		}

		// 5. Invalid strategy -> error
		_, err = provider.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "holiday.jpg", bytes.NewReader(content), int64(len(content)), ManagerUploadOptions{ConflictStrategy: "invalid"})
		if err == nil {
			t.Errorf("UploadManager(invalid) expected error, got nil")
		}
	})
}
