package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBoxFileManagerConnectAndPaginatedBrowse(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFolder("0", "11", "Archive")
	srv.addFile("0", "12", "notes.txt", 15, "AAF4C61DDCC5E8A2DABEDE0F3B482CD9AEA9434D")
	nestedID := srv.addFolder(docsID, "20", "2026")
	srv.addFile(nestedID, "21", "q1.pdf", 512, "2FD4E1C67A2D28FCED849EE1BB76E7391B93EB12")

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	ok, err := fm.ConnectManager(context.Background())
	if err != nil || !ok {
		t.Fatalf("ConnectManager = (%v, %v), want (true, nil)", ok, err)
	}

	page1, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListManager(root page 1): %v", err)
	}
	if len(page1.Items) != 2 || page1.NextCursor == "" || page1.PaginationMode != "native" {
		t.Fatalf("page1 = %+v, want 2 items, non-empty NextCursor, native mode", page1)
	}

	page2, err := fm.ListManager(context.Background(), ManagerLocator{NativeID: "0"}, ManagerListOptions{Cursor: page1.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("ListManager(root page 2): %v", err)
	}
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("page2 = %+v, want 1 item and empty NextCursor", page2)
	}

	// Verify nested browsing using resolved locator.
	loc, breadcrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/docs/2026")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/docs/2026): %v", err)
	}
	if fallback || loc.NativeID != nestedID || loc.Path != "/Docs/2026" {
		t.Fatalf("ResolveManagerPath = (%+v, fallback=%v), want NativeID=%q Path=/Docs/2026", loc, fallback, nestedID)
	}
	if len(breadcrumbs) != 2 || breadcrumbs[0].Name != "Docs" || breadcrumbs[0].Locator.NativeID != docsID ||
		breadcrumbs[1].Name != "2026" || breadcrumbs[1].Locator.NativeID != nestedID {
		t.Fatalf("breadcrumbs = %+v", breadcrumbs)
	}

	nestedPage, err := fm.ListManager(context.Background(), loc, ManagerListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListManager(nested): %v", err)
	}
	if len(nestedPage.Items) != 1 || nestedPage.Items[0].Name != "q1.pdf" ||
		nestedPage.Items[0].Locator.Path != "/Docs/2026/q1.pdf" ||
		nestedPage.Items[0].Locator.NativeID != "21" ||
		nestedPage.Items[0].Size != 512 {
		t.Fatalf("nestedPage.Items = %+v", nestedPage.Items)
	}
}

func TestBoxFileManagerResolvePathEdgeCases(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFile(docsID, "11", "readme.txt", 10, "")

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	rootLoc, rootCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/): %v", err)
	}
	if fallback || !fm.IsRootLocator(rootLoc) || len(rootCrumbs) != 0 {
		t.Fatalf("root resolve = (%+v, %+v, fallback=%v)", rootLoc, rootCrumbs, fallback)
	}

	// Missing folder falls back to deepest valid parent directory.
	fbLoc, fbCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/Docs/NonExistent")
	if err != nil {
		t.Fatalf("ResolveManagerPath(missing): %v", err)
	}
	if !fallback || fbLoc.NativeID != docsID || fbLoc.Path != "/Docs" || len(fbCrumbs) != 1 {
		t.Fatalf("missing fallback = (%+v, %+v, fallback=%v), want /Docs (%s)", fbLoc, fbCrumbs, fallback, docsID)
	}

	// Path pointing to a file falls back to its parent folder.
	fileLoc, fileCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/Docs/readme.txt")
	if err != nil {
		t.Fatalf("ResolveManagerPath(file): %v", err)
	}
	if !fallback || fileLoc.NativeID != docsID || len(fileCrumbs) != 1 {
		t.Fatalf("file fallback = (%+v, %+v, fallback=%v), want /Docs (%s)", fileLoc, fileCrumbs, fallback, docsID)
	}

	if _, _, _, err := fm.ResolveManagerPath(context.Background(), "/Docs/../secret"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("traversal error = %v, want ErrPathEscapesRoot", err)
	}
}

func TestBoxFileManagerDownload(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	payload := []byte("manager downloaded box bytes")
	docsID := srv.addFolder("0", "10", "Docs")
	fileID := srv.addFileContent(docsID, "12", "report.pdf", payload)
	blockedID := srv.addFileContent(docsID, "13", "classified.pdf", []byte("secret"))
	srv.mu.Lock()
	it := srv.items[blockedID]
	it.DownloadBlocked = true
	srv.items[blockedID] = it
	srv.mu.Unlock()

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	if !fm.Capabilities().Download {
		t.Fatalf("fm.Capabilities().Download = false, want true")
	}

	dl, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: fileID, Path: "/Docs/report.pdf"})
	if err != nil {
		t.Fatalf("DownloadManager(id+path): %v", err)
	}
	body, readErr := io.ReadAll(dl.Stream)
	dl.Stream.Close()
	if readErr != nil || string(body) != string(payload) {
		t.Fatalf("downloaded = (%q, %v), want %q", body, readErr, payload)
	}
	if dl.Item.Name != "report.pdf" || dl.Item.Size != int64(len(payload)) || dl.Item.IsDir || dl.Item.Locator.NativeID != fileID {
		t.Fatalf("dl.Item = %+v", dl.Item)
	}

	// ID-only locator works without Path.
	dlID, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: fileID})
	if err != nil {
		t.Fatalf("DownloadManager(id-only): %v", err)
	}
	bodyID, readErr := io.ReadAll(dlID.Stream)
	dlID.Stream.Close()
	if readErr != nil || string(bodyID) != string(payload) {
		t.Fatalf("id-only downloaded = (%q, %v), want %q", bodyID, readErr, payload)
	}

	srv.mu.Lock()
	auths := append([]string(nil), srv.downloadAuthHeaders...)
	srv.mu.Unlock()
	if len(auths) != 2 || auths[0] != "" || auths[1] != "" {
		t.Fatalf("manager download Authorization headers = %q, want empty", auths)
	}

	// Path-only without NativeID, root, folder, and missing ID must fail with ErrNotFound.
	for _, loc := range []ManagerLocator{
		{Path: "/Docs/report.pdf"},
		{NativeID: "0"},
		{NativeID: docsID},
		{NativeID: "99999"},
	} {
		if _, err := fm.DownloadManager(context.Background(), loc); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DownloadManager(%+v) = %v, want ErrNotFound", loc, err)
		}
	}

	// Policy-blocked download surfaces ErrPermission.
	if _, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: blockedID}); !errors.Is(err, ErrPermission) {
		t.Fatalf("DownloadManager(blocked) = %v, want ErrPermission", err)
	}
}

func TestBoxFileManagerThumbnailSizeMappingAndRedirect(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	photoID := srv.addFileContent(docsID, "12", "photo.png", []byte("png-data"))
	redirID := srv.addFileContent(docsID, "13", "video.mp4", []byte("mp4-data"))
	srv.mu.Lock()
	ph := srv.items[photoID]
	ph.Thumbnail = []byte("thumb-bytes-ok")
	srv.items[photoID] = ph
	rd := srv.items[redirID]
	rd.Thumbnail = []byte("redirected-thumb-bytes")
	rd.ThumbnailType = "image/png"
	rd.ThumbnailRedirect = true
	srv.items[redirID] = rd
	srv.mu.Unlock()

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	if !fm.Capabilities().Thumbnails {
		t.Fatalf("fm.Capabilities().Thumbnails = false, want true")
	}

	cases := []struct {
		name     string
		w, h     int
		wantPath string
		wantSize string
		wantMime string
	}{
		{"small clamped to 32 png", 16, 16, "/2.0/files/12/thumbnail.png", "32", "image/png"},
		{"64 png", 64, 48, "/2.0/files/12/thumbnail.png", "64", "image/png"},
		{"94 maps to 128 png (never 94)", 94, 94, "/2.0/files/12/thumbnail.png", "128", "image/png"},
		{"128 png", 128, 128, "/2.0/files/12/thumbnail.png", "128", "image/png"},
		{"256 png", 256, 256, "/2.0/files/12/thumbnail.png", "256", "image/png"},
		{"large capped at 320 jpg", 1024, 768, "/2.0/files/12/thumbnail.jpg", "320", "image/jpeg"},
		{"zero defaults to 256 png", 0, 0, "/2.0/files/12/thumbnail.png", "256", "image/png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.mu.Lock()
			beforeReqs := len(srv.requests)
			srv.mu.Unlock()

			stream, cType, err := fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: photoID}, tc.w, tc.h)
			if err != nil {
				t.Fatalf("ThumbnailManager(%d, %d): %v", tc.w, tc.h, err)
			}
			gotBytes, readErr := io.ReadAll(stream)
			stream.Close()
			if readErr != nil || string(gotBytes) != "thumb-bytes-ok" {
				t.Fatalf("body = (%q, %v), want thumb-bytes-ok", gotBytes, readErr)
			}
			if cType != tc.wantMime {
				t.Fatalf("contentType = %q, want %q", cType, tc.wantMime)
			}

			srv.mu.Lock()
			reqLine := srv.requests[beforeReqs]
			srv.mu.Unlock()
			parts := strings.SplitN(reqLine, " ", 2)
			u, parseErr := url.Parse(parts[1])
			if parseErr != nil {
				t.Fatalf("parse recorded request %q: %v", reqLine, parseErr)
			}
			if u.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", u.Path, tc.wantPath)
			}
			q := u.Query()
			for _, key := range []string{"min_width", "min_height", "max_width", "max_height"} {
				if q.Get(key) != tc.wantSize {
					t.Fatalf("query %s = %q, want %q (full=%q)", key, q.Get(key), tc.wantSize, u.RawQuery)
				}
			}
		})
	}

	// 302 redirect strips Authorization header before hitting download host.
	stream, cType, err := fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: redirID}, 128, 128)
	if err != nil {
		t.Fatalf("ThumbnailManager(redirect): %v", err)
	}
	gotRedir, readErr := io.ReadAll(stream)
	stream.Close()
	if readErr != nil || string(gotRedir) != "redirected-thumb-bytes" || cType != "image/png" {
		t.Fatalf("redirected = (%q, %q, %v)", gotRedir, cType, readErr)
	}
	srv.mu.Lock()
	auths := append([]string(nil), srv.downloadAuthHeaders...)
	srv.mu.Unlock()
	if len(auths) != 1 || auths[0] != "" {
		t.Fatalf("thumbnail redirect Authorization headers = %q, want empty", auths)
	}
}

func TestBoxFileManagerThumbnail202WaitAndRetryAfter(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	photoID := srv.addFileContent(docsID, "12", "photo.jpg", []byte("jpg-data"))
	srv.mu.Lock()
	ph := srv.items[photoID]
	ph.Thumbnail = []byte("generated-thumb-bytes")
	srv.items[photoID] = ph
	srv.mu.Unlock()

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	// 202 Accepted with placeholder Location followed by 200 OK after brief wait.
	srv.enqueueFaults(fakeBoxFault{
		status:     http.StatusAccepted,
		retryAfter: "0",
		location:   srv.server.URL + "/trap-placeholder",
	})
	before := srv.requestCount()
	stream, cType, err := fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: photoID}, 256, 256)
	if err != nil {
		t.Fatalf("ThumbnailManager after 202: %v", err)
	}
	body, _ := io.ReadAll(stream)
	stream.Close()
	if string(body) != "generated-thumb-bytes" || cType != "image/png" {
		t.Fatalf("body=%q cType=%q", body, cType)
	}
	if got := srv.requestCount() - before; got != 2 {
		t.Fatalf("requestCount = %d, want 2 (1 initial 202 + 1 retry)", got)
	}
	srv.mu.Lock()
	trapHit := srv.trapHit
	srv.mu.Unlock()
	if trapHit {
		t.Fatal("followed 202 placeholder Location header, must not follow 202 Location")
	}

	// Two consecutive 202 responses wait at most thumbnailMaxWait and return *RetryAfterError.
	p.thumbnailMaxWait = 15 * time.Millisecond
	srv.enqueueFaults(
		fakeBoxFault{status: http.StatusAccepted, retryAfter: "4", location: srv.server.URL + "/trap-placeholder"},
		fakeBoxFault{status: http.StatusAccepted, retryAfter: "4", location: srv.server.URL + "/trap-placeholder"},
	)
	start := time.Now()
	_, _, err = fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: photoID}, 256, 256)
	elapsed := time.Since(start)
	var retryErr *RetryAfterError
	if !errors.As(err, &retryErr) {
		t.Fatalf("double 202 err = %v, want *RetryAfterError", err)
	}
	if retryErr.After != 4*time.Second {
		t.Fatalf("RetryAfterError.After = %s, want 4s", retryErr.After)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("double 202 waited %s, expected <= thumbnailMaxWait (15ms)", elapsed)
	}

	// Default max wait is 3s and respects context cancellation.
	if boxDefaultThumbnailMaxWait != 3*time.Second {
		t.Fatalf("boxDefaultThumbnailMaxWait = %s, want 3s", boxDefaultThumbnailMaxWait)
	}
	// Missing Retry-After on double 202 falls back to maxWait on RetryAfterError.
	srv.enqueueFaults(
		fakeBoxFault{status: http.StatusAccepted, location: srv.server.URL + "/trap-placeholder"},
		fakeBoxFault{status: http.StatusAccepted, location: srv.server.URL + "/trap-placeholder"},
	)
	_, _, err = fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: photoID}, 256, 256)
	var fallbackRetryErr *RetryAfterError
	if !errors.As(err, &fallbackRetryErr) || fallbackRetryErr.After != 15*time.Millisecond {
		t.Fatalf("double 202 without Retry-After = %v, want RetryAfterError(15ms)", err)
	}

	pDefault := srv.provider()
	defer pDefault.Close()
	srv.enqueueFaults(fakeBoxFault{status: http.StatusAccepted, retryAfter: "5"})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, _, err = pDefault.ThumbnailManager(ctx, ManagerLocator{NativeID: photoID}, 256, 256)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("202 wait with cancelled context = %v, want context.DeadlineExceeded", err)
	}
}

func TestBoxFileManagerThumbnailErrorsAndRedirectSSRF(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	unsupportedID := srv.addFileContent(docsID, "12", "archive.zip", []byte("zip"))
	srv.mu.Lock()
	it := srv.items[unsupportedID]
	it.UnsupportedMedia = true
	srv.items[unsupportedID] = it
	srv.mu.Unlock()

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	if _, _, err := fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: unsupportedID}, 128, 128); !errors.Is(err, ErrUnsupportedMedia) {
		t.Fatalf("415 err = %v, want ErrUnsupportedMedia", err)
	}
	if _, _, err := fm.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "9999"}, 128, 128); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404 err = %v, want ErrNotFound", err)
	}
	for _, loc := range []ManagerLocator{{}, {NativeID: "0"}, {Path: "/Docs/archive.zip"}} {
		if _, _, err := fm.ThumbnailManager(context.Background(), loc, 128, 128); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ThumbnailManager(%+v) = %v, want ErrNotFound", loc, err)
		}
	}

	badToken := newBoxProvider("wrong-token", srv.server.URL+"/2.0", srv.server.URL+"/api/2.0", srv.server.Client())
	defer badToken.Close()
	if _, _, err := badToken.ThumbnailManager(context.Background(), ManagerLocator{NativeID: unsupportedID}, 128, 128); !errors.Is(err, ErrAuth) {
		t.Fatalf("401 err = %v, want ErrAuth", err)
	}

	// SSRF: disallow plaintext HTTP and metadata redirects.
	photoID := srv.addFileContent(docsID, "14", "pic.jpg", []byte("jpg"))
	for _, target := range []string{
		"http://dl.boxcloud.com/thumb/14",
		"https://169.254.169.254/latest/meta-data",
	} {
		srv.mu.Lock()
		srv.redirectOverride = target
		srv.mu.Unlock()
		stream, _, err := p.ThumbnailManager(context.Background(), ManagerLocator{NativeID: photoID}, 128, 128)
		if err == nil {
			stream.Close()
			t.Fatalf("expected redirect to %q to be rejected", target)
		}
		if !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("redirect rejection for %q = %v, want redirect error", target, err)
		}
	}

	// Production apiBase rejects loopback HTTPS redirect targets as SSRF.
	redirectFollowed := false
	prodClient := &http.Client{
		Transport: boxRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == "api.box.com" {
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"https://127.0.0.1/thumb/14"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			}
			redirectFollowed = true
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}),
	}
	prodBox := newBoxProvider("valid-token", defaultBoxAPIBase, defaultBoxUploadBase, prodClient)
	if stream, _, err := prodBox.ThumbnailManager(context.Background(), ManagerLocator{NativeID: "14"}, 128, 128); err == nil {
		stream.Close()
		t.Fatal("expected prod loopback thumbnail redirect to be rejected")
	} else if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("prod loopback thumbnail redirect error = %v, want redirect error", err)
	}
	if redirectFollowed {
		t.Fatal("prod loopback thumbnail redirect was followed")
	}
}
