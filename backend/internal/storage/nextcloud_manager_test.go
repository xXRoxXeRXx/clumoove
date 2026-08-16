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

func newNextcloudManagerTestProvider(t *testing.T, handler http.Handler) *NextcloudProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	p, err := NewNextcloudProvider("https://nextcloud.example.test", "testuser", "testpass")
	if err != nil {
		t.Fatalf("NewNextcloudProvider: %v", err)
	}
	p.BaseURL = server.URL + "/remote.php/dav"
	p.HTTPClient = server.Client()
	return p
}

func TestNextcloudManagerList(t *testing.T) {
	provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		if r.Header.Get("Depth") != "1" {
			t.Fatalf("Depth = %q, want 1", r.Header.Get("Depth"))
		}

		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>/remote.php/dav/files/testuser/</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/testuser/photo.jpg</d:href>
    <d:propstat>
      <d:prop>
        <d:getlastmodified>Sun, 06 Nov 1994 08:49:37 GMT</d:getlastmodified>
        <d:getcontentlength>2048</d:getcontentlength>
        <d:getcontenttype>image/jpeg</d:getcontenttype>
        <d:resourcetype/>
        <oc:fileid>101</oc:fileid>
        <nc:has-preview>true</nc:has-preview>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/testuser/Documents</d:href>
    <d:propstat>
      <d:prop>
        <d:getlastmodified>Sun, 06 Nov 1994 08:49:37 GMT</d:getlastmodified>
        <d:resourcetype><d:collection/></d:resourcetype>
        <oc:fileid>102</oc:fileid>
        <oc:size>4096</oc:size>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:response>
    <d:href>/remote.php/dav/files/testuser/trashbin</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
        <oc:fileid>103</oc:fileid>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
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
	if item0.Name != "photo.jpg" || item0.MIMEType != "image/jpeg" || item0.Locator.NativeID != "101" || item0.Size != 2048 || item0.IsDir {
		t.Fatalf("unexpected item 0: %#v", item0)
	}

	// Second item: Documents
	item1 := page.Items[1]
	if item1.Name != "Documents" || !item1.IsDir || item1.Locator.NativeID != "102" || item1.Size != 4096 {
		t.Fatalf("unexpected item 1: %#v", item1)
	}

	// Next cursor check
	if page.NextCursor != "" {
		t.Fatalf("NextCursor = %q, want empty since only 2 valid items exist", page.NextCursor)
	}

	// Test with limit 1 and cursor pagination
	pagePart, err := provider.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("ListManager() error = %v", err)
	}
	if len(pagePart.Items) != 1 || pagePart.NextCursor != "1" {
		t.Fatalf("pagePart items = %d, nextCursor = %q, want 1 and '1'", len(pagePart.Items), pagePart.NextCursor)
	}
}

func TestNextcloudManagerConnect(t *testing.T) {
	provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"></d:multistatus>`))
	}))

	ok, err := provider.ConnectManager(context.Background())
	if err != nil || !ok {
		t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", ok, err)
	}
}

func TestNextcloudManagerDownload(t *testing.T) {
	provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/remote.php/dav/files/testuser/hello.txt</d:href>
    <d:propstat>
      <d:prop>
        <d:getcontentlength>12</d:getcontentlength>
        <d:resourcetype/>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/hello.txt") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello world!"))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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
	if string(body) != "hello world!" {
		t.Fatalf("download body = %q, want 'hello world!'", string(body))
	}
}

func TestNextcloudManagerCreateDirectory(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == "MKCOL" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}))

		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "newfolder")
		if err != nil {
			t.Fatalf("CreateManagerDirectory() error = %v", err)
		}
	})

	t.Run("conflict", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusOK)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}))

		err := provider.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "existing")
		if !errors.Is(err, ErrManagerConflict) {
			t.Fatalf("CreateManagerDirectory() error = %v, want ErrManagerConflict", err)
		}
	})
}

func TestNextcloudManagerUploadConflictStrategies(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				w.Header().Set("Content-Length", "5")
				w.WriteHeader(http.StatusOK)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				w.Header().Set("Content-Length", "5")
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == "PROPFIND" {
				w.WriteHeader(http.StatusMultiStatus)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8" ?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/remote.php/dav/files/testuser/target.txt</d:href>
    <d:propstat>
      <d:prop><d:getcontentlength>5</d:getcontentlength></d:prop>
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
			if r.Method == "PUT" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == "MOVE" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "HEAD" {
				if strings.HasSuffix(r.URL.Path, "/report.pdf") {
					w.Header().Set("Content-Length", "5")
					w.WriteHeader(http.StatusOK)
					return
				}
				// report (1).pdf does not exist
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.Method == "MKCOL" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == "PUT" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.Method == "MOVE" {
				w.WriteHeader(http.StatusCreated)
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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

func TestNextcloudManagerResolvePath(t *testing.T) {
	provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			if strings.HasSuffix(r.URL.Path, "/Photos") {
				w.Header().Set("Content-Length", "0")
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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

func TestNextcloudManagerThumbnail(t *testing.T) {
	t.Run("success with fileId", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/index.php/core/preview" {
				t.Fatalf("path = %s, want /index.php/core/preview", r.URL.Path)
			}
			if r.URL.Query().Get("fileId") != "101" {
				t.Fatalf("fileId = %q, want 101", r.URL.Query().Get("fileId"))
			}
			if r.URL.Query().Get("x") != "256" || r.URL.Query().Get("y") != "256" {
				t.Fatalf("dimensions = %s x %s, want 256x256", r.URL.Query().Get("x"), r.URL.Query().Get("y"))
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-jpeg-preview-data"))
		}))

		stream, contentType, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "101", Path: "/photo.jpg"}, 256, 256)
		if err != nil {
			t.Fatalf("ThumbnailManager() error = %v", err)
		}
		defer stream.Close()

		if contentType != "image/jpeg" {
			t.Fatalf("contentType = %q, want image/jpeg", contentType)
		}
		data, _ := io.ReadAll(stream)
		if !bytes.Equal(data, []byte("fake-jpeg-preview-data")) {
			t.Fatalf("thumbnail content = %q", string(data))
		}
	})

	t.Run("success with path", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/index.php/core/preview" {
				t.Fatalf("path = %s, want /index.php/core/preview", r.URL.Path)
			}
			if r.URL.Query().Get("file") != "/sunset.png" {
				t.Fatalf("file = %q, want /sunset.png", r.URL.Query().Get("file"))
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("fake-png-preview-data"))
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
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnsupportedMediaType)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "999", Path: "/archive.zip"}, 256, 256)
		if !errors.Is(err, ErrUnsupportedMedia) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrUnsupportedMedia", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "404", Path: "/missing.jpg"}, 256, 256)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrNotFound", err)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		provider := newNextcloudManagerTestProvider(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))

		_, _, err := provider.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "101", Path: "/photo.jpg"}, 256, 256)
		if !errors.Is(err, ErrAuth) {
			t.Fatalf("ThumbnailManager() error = %v, want ErrAuth", err)
		}
	})
}
