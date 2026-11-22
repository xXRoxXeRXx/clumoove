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

func TestBoxFileManagerCreateDirectory(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFile("0", "11", "readme.txt", 12, "")

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// Root creation via Path="/" and via NativeID="0".
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{Path: "/"}, "Projects"); err != nil {
		t.Fatalf("CreateManagerDirectory(root /): %v", err)
	}
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{NativeID: "0"}, "Archive"); err != nil {
		t.Fatalf("CreateManagerDirectory(root 0): %v", err)
	}

	// Nested creation by parent NativeID (even if display Path is empty or stale).
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{NativeID: docsID}, "2026"); err != nil {
		t.Fatalf("CreateManagerDirectory(nested): %v", err)
	}

	page, err := fm.ListManager(ctx, ManagerLocator{NativeID: docsID}, ManagerListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListManager(Docs): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "2026" || !page.Items[0].IsDir {
		t.Fatalf("Docs items = %+v, want [2026 dir]", page.Items)
	}

	// Case-insensitive conflict with existing folder or file returns ErrManagerConflict.
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{NativeID: "0"}, "docs"); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CreateManagerDirectory(case-insensitive folder conflict) = %v, want ErrManagerConflict", err)
	}
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{NativeID: "0"}, "readme.txt"); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CreateManagerDirectory(file conflict) = %v, want ErrManagerConflict", err)
	}

	// Path-only non-root parent and invalid names fail closed.
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{Path: "/Docs"}, "Sub"); !errors.Is(err, ErrManagerInvalidDestination) {
		t.Fatalf("CreateManagerDirectory(path-only parent) = %v, want ErrManagerInvalidDestination", err)
	}
	if err := fm.CreateManagerDirectory(ctx, ManagerLocator{NativeID: "0"}, "bad/name"); !errors.Is(err, ErrManagerInvalidDestination) {
		t.Fatalf("CreateManagerDirectory(invalid name) = %v, want ErrManagerInvalidDestination", err)
	}
}

type boxFailingReader struct {
	err error
}

func (r boxFailingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestBoxFileManagerUpload(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	nestedID := srv.addFolder(docsID, "11", "Nested")
	existingID := srv.addFileContent(docsID, "12", "Report.txt", []byte("original-content"))

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// 1. Upload new file to root and nested folder.
	resRoot, err := fm.UploadManager(ctx, ManagerLocator{Path: "/"}, "hello.txt", strings.NewReader("root-bytes"), 10, ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || resRoot.Status != "uploaded" || resRoot.FinalName != "hello.txt" {
		t.Fatalf("UploadManager(root) = (%+v, %v)", resRoot, err)
	}
	resNested, err := fm.UploadManager(ctx, ManagerLocator{NativeID: nestedID}, "nested.txt", strings.NewReader("nested-bytes"), 12, ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || resNested.Status != "uploaded" || resNested.FinalName != "nested.txt" {
		t.Fatalf("UploadManager(nested) = (%+v, %v)", resNested, err)
	}

	// 2. SKIP on case-insensitive match does not consume stream or overwrite.
	resSkip, err := fm.UploadManager(ctx, ManagerLocator{NativeID: docsID}, "report.txt", boxFailingReader{err: errors.New("stream must not be read on SKIP")}, 5, ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || resSkip.Status != "skipped" || resSkip.FinalName != "report.txt" {
		t.Fatalf("UploadManager(SKIP) = (%+v, %v)", resSkip, err)
	}

	// 3. Failed OVERWRITE leaves original intact.
	if _, err := fm.UploadManager(ctx, ManagerLocator{NativeID: docsID}, "Report.txt", boxFailingReader{err: errors.New("stream drop")}, 10, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err == nil {
		t.Fatal("expected error on failing overwrite stream")
	}
	dlOrig, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: existingID})
	if err != nil {
		t.Fatalf("DownloadManager after failed overwrite: %v", err)
	}
	origBytes, _ := io.ReadAll(dlOrig.Stream)
	dlOrig.Stream.Close()
	if string(origBytes) != "original-content" {
		t.Fatalf("content after failed overwrite = %q, want original-content", origBytes)
	}

	// 4. OVERWRITE uploads a new version to existing file ID (via POST /files/{id}/content).
	resOverwrite, err := fm.UploadManager(ctx, ManagerLocator{NativeID: docsID}, "Report.txt", strings.NewReader("updated-v2"), 10, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
	if err != nil || resOverwrite.Status != "uploaded" || resOverwrite.FinalName != "Report.txt" {
		t.Fatalf("UploadManager(OVERWRITE) = (%+v, %v)", resOverwrite, err)
	}
	dlUpdated, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: existingID})
	if err != nil {
		t.Fatalf("DownloadManager after overwrite: %v", err)
	}
	updatedBytes, _ := io.ReadAll(dlUpdated.Stream)
	dlUpdated.Stream.Close()
	if string(updatedBytes) != "updated-v2" {
		t.Fatalf("content after overwrite = %q, want updated-v2", updatedBytes)
	}

	// 5. OVERWRITE colliding with a folder returns ErrManagerConflict.
	if _, err := fm.UploadManager(ctx, ManagerLocator{NativeID: docsID}, "Nested", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("UploadManager(OVERWRITE folder) = %v, want ErrManagerConflict", err)
	}

	// 6. RENAME picks next non-colliding suffix (case-insensitive).
	srv.addFileContent(docsID, "", "Report (1).txt", []byte("r1"))
	resRename, err := fm.UploadManager(ctx, ManagerLocator{NativeID: docsID}, "report.txt", strings.NewReader("r2"), 2, ManagerUploadOptions{ConflictStrategy: "RENAME"})
	if err != nil || resRename.Status != "renamed" || resRename.FinalName != "report (2).txt" {
		t.Fatalf("UploadManager(RENAME) = (%+v, %v)", resRename, err)
	}

	// 7. Chunked upload (>50MB) and chunked overwrite via UploadManager.
	bigPayload := make([]byte, 50_000_001)
	copy(bigPayload, "HEAD")
	resChunked, err := fm.UploadManager(ctx, ManagerLocator{NativeID: nestedID}, "big.bin", strings.NewReader(string(bigPayload)), int64(len(bigPayload)), ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || resChunked.Status != "uploaded" || resChunked.FinalName != "big.bin" {
		t.Fatalf("UploadManager(chunked) = (%+v, %v)", resChunked, err)
	}
	resChunkedOverwrite, err := fm.UploadManager(ctx, ManagerLocator{NativeID: nestedID}, "big.bin", strings.NewReader(string(bigPayload)), int64(len(bigPayload)), ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
	if err != nil || resChunkedOverwrite.Status != "uploaded" || resChunkedOverwrite.FinalName != "big.bin" {
		t.Fatalf("UploadManager(chunked overwrite) = (%+v, %v)", resChunkedOverwrite, err)
	}

	// 8. Path-only non-root parent rejected.
	if _, err := fm.UploadManager(ctx, ManagerLocator{Path: "/Docs"}, "a.txt", strings.NewReader("a"), 1, ManagerUploadOptions{ConflictStrategy: "SKIP"}); !errors.Is(err, ErrManagerInvalidDestination) {
		t.Fatalf("UploadManager(path-only parent) = %v, want ErrManagerInvalidDestination", err)
	}
}

func TestBoxFileManagerRenameAndMove(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	subID := srv.addFolder(docsID, "11", "2026")
	archiveID := srv.addFolder("0", "12", "Archive")
	reportID := srv.addFileContent(subID, "20", "report.txt", []byte("report-body"))
	conflictFileID := srv.addFileContent(archiveID, "21", "existing.txt", []byte("old-archive"))
	srv.addFileContent(archiveID, "22", "keep.txt", []byte("keep-body"))

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// Warm pathCache with nested folder hierarchy before mutating.
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/2026"); err != nil {
		t.Fatalf("warm GetDirectoryListing(/Docs/2026): %v", err)
	}

	// 1. Rename file with empty parent retains its nested parent folder.
	resRename, err := fm.RenameManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{}, "summary.txt", ManagerMutationOptions{})
	if err != nil || resRename != (ManagerMutationResult{Status: "renamed", FinalName: "summary.txt", Native: true}) {
		t.Fatalf("RenameManagerItem(file) = (%+v, %v)", resRename, err)
	}
	// Case-only rename in the same folder is valid (not a no-op).
	resCase, err := fm.RenameManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{}, "Summary.txt", ManagerMutationOptions{})
	if err != nil || resCase != (ManagerMutationResult{Status: "renamed", FinalName: "Summary.txt", Native: true}) {
		t.Fatalf("RenameManagerItem(case-only) = (%+v, %v)", resCase, err)
	}
	// Exact same name in same folder is ErrManagerNoop.
	if _, err := fm.RenameManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{}, "Summary.txt", ManagerMutationOptions{}); !errors.Is(err, ErrManagerNoop) {
		t.Fatalf("RenameManagerItem(noop) = %v, want ErrManagerNoop", err)
	}

	// 2. Rename nested folder invalidates pathCache and updates subsequent listings/resolves.
	resDirRename, err := fm.RenameManagerItem(ctx, ManagerLocator{NativeID: docsID}, ManagerLocator{}, "Documents", ManagerMutationOptions{})
	if err != nil || resDirRename != (ManagerMutationResult{Status: "renamed", FinalName: "Documents", Native: true}) {
		t.Fatalf("RenameManagerItem(folder) = (%+v, %v)", resDirRename, err)
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/2026"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDirectoryListing(/Docs/2026) after rename = %v, want ErrNotFound", err)
	}
	itemsAfterRename, err := p.GetDirectoryListing(ctx, "files", "/Documents/2026")
	if err != nil || len(itemsAfterRename) != 1 || itemsAfterRename[0].Name != "Summary.txt" {
		t.Fatalf("GetDirectoryListing(/Documents/2026) = (%+v, %v)", itemsAfterRename, err)
	}

	// 3. Move nested folder into Archive and verify pathCache invalidation & cycle guard.
	if _, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: docsID}, ManagerLocator{NativeID: subID}, "Documents", ManagerMutationOptions{}); !errors.Is(err, ErrManagerDirectoryCycle) {
		t.Fatalf("MoveManagerItem(into descendant) = %v, want ErrManagerDirectoryCycle", err)
	}
	if _, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: subID}, ManagerLocator{NativeID: subID}, "2026", ManagerMutationOptions{}); !errors.Is(err, ErrManagerDirectoryCycle) {
		t.Fatalf("MoveManagerItem(into self) = %v, want ErrManagerDirectoryCycle", err)
	}
	resMoveDir, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: subID}, ManagerLocator{NativeID: archiveID}, "2026-Moved", ManagerMutationOptions{})
	if err != nil || resMoveDir != (ManagerMutationResult{Status: "moved", FinalName: "2026-Moved", Native: true}) {
		t.Fatalf("MoveManagerItem(folder) = (%+v, %v)", resMoveDir, err)
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "/Documents/2026"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDirectoryListing(/Documents/2026) after move = %v, want ErrNotFound", err)
	}
	locMoved, _, fallback, err := fm.ResolveManagerPath(ctx, "/Archive/2026-Moved")
	if err != nil || fallback || locMoved.NativeID != subID {
		t.Fatalf("ResolveManagerPath(/Archive/2026-Moved) = (%+v, fallback=%v, err=%v)", locMoved, fallback, err)
	}

	// 4. Move file with conflict strategies: empty -> ErrManagerConflict, SKIP, RENAME, OVERWRITE, and move to root via Path="/".
	if _, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{NativeID: archiveID}, "keep.txt", ManagerMutationOptions{}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("MoveManagerItem(conflict) = %v, want ErrManagerConflict", err)
	}
	resSkip, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{NativeID: archiveID}, "keep.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictSkip})
	if err != nil || resSkip.Status != "skipped" || resSkip.FinalName != "keep.txt" {
		t.Fatalf("MoveManagerItem(SKIP) = (%+v, %v)", resSkip, err)
	}
	resRenameConflict, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{NativeID: archiveID}, "keep.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictRename})
	if err != nil || resRenameConflict.Status != "renamed_on_conflict" || resRenameConflict.FinalName != "keep (1).txt" {
		t.Fatalf("MoveManagerItem(RENAME) = (%+v, %v)", resRenameConflict, err)
	}
	resOverwrite, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{NativeID: archiveID}, "existing.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite})
	if err != nil || resOverwrite.Status != "moved" || resOverwrite.FinalName != "existing.txt" {
		t.Fatalf("MoveManagerItem(OVERWRITE) = (%+v, %v)", resOverwrite, err)
	}
	// Verify overwritten file was replaced and reportID content moved.
	if _, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: conflictFileID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old conflictFileID should be deleted after overwrite, got %v", err)
	}
	resToRoot, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: reportID}, ManagerLocator{Path: "/"}, "root-report.txt", ManagerMutationOptions{})
	if err != nil || resToRoot.Status != "moved" || resToRoot.FinalName != "root-report.txt" {
		t.Fatalf("MoveManagerItem(to root) = (%+v, %v)", resToRoot, err)
	}

	// Folder overwrite must be refused.
	if _, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: subID}, ManagerLocator{NativeID: archiveID}, "keep.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("MoveManagerItem(folder overwrite) = %v, want ErrManagerConflict", err)
	}

	// Root mutation rejected by adapter.
	if _, err := fm.RenameManagerItem(ctx, ManagerLocator{NativeID: "0"}, ManagerLocator{}, "x", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
		t.Fatalf("RenameManagerItem(root) = %v, want ErrManagerUnsupported", err)
	}
}

func TestBoxFileManagerCopy(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	srcDirID := srv.addFolder("0", "10", "Source")
	nestedDirID := srv.addFolder(srcDirID, "11", "Nested")
	fileID := srv.addFileContent(nestedDirID, "12", "deck.pdf", []byte("native-copy-bytes"))
	destDirID := srv.addFolder("0", "20", "Target")
	targetConflictID := srv.addFileContent(destDirID, "21", "existing.pdf", []byte("stale-target"))

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// 1. Native file copy into Target (must not download file bytes).
	before := srv.requestCount()
	resFile, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{NativeID: destDirID}, "deck-copy.pdf", ManagerMutationOptions{})
	if err != nil || resFile != (ManagerMutationResult{Status: "copied", FinalName: "deck-copy.pdf", Native: true}) {
		t.Fatalf("CopyManagerItem(file) = (%+v, %v)", resFile, err)
	}
	srv.mu.Lock()
	reqs := append([]string(nil), srv.requests[before:]...)
	srv.mu.Unlock()
	sawNativeCopy := false
	for _, r := range reqs {
		if strings.HasPrefix(r, "POST /2.0/files/"+fileID+"/copy") {
			sawNativeCopy = true
		}
		if strings.Contains(r, "/content") {
			t.Fatalf("native CopyManagerItem hit /content (%s)", r)
		}
	}
	if !sawNativeCopy {
		t.Fatalf("expected POST /2.0/files/%s/copy in %v", fileID, reqs)
	}

	// 2. Native nested folder copy clones entire subtree server-side.
	resFolder, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: srcDirID}, ManagerLocator{NativeID: destDirID}, "SourceCopy", ManagerMutationOptions{})
	if err != nil || resFolder != (ManagerMutationResult{Status: "copied", FinalName: "SourceCopy", Native: true}) {
		t.Fatalf("CopyManagerItem(folder) = (%+v, %v)", resFolder, err)
	}
	copiedStream, err := p.StreamDownload(ctx, "files", "/Target/SourceCopy/Nested/deck.pdf")
	if err != nil {
		t.Fatalf("StreamDownload(copied nested file): %v", err)
	}
	copiedBytes, _ := io.ReadAll(copiedStream)
	copiedStream.Close()
	if string(copiedBytes) != "native-copy-bytes" {
		t.Fatalf("copied nested content = %q, want native-copy-bytes", copiedBytes)
	}

	// 3. Cycle detection on folder copy.
	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: srcDirID}, ManagerLocator{NativeID: nestedDirID}, "Loop", ManagerMutationOptions{}); !errors.Is(err, ErrManagerDirectoryCycle) {
		t.Fatalf("CopyManagerItem(cycle) = %v, want ErrManagerDirectoryCycle", err)
	}

	// 4. Conflict strategies: empty, SKIP, RENAME, OVERWRITE (file), OVERWRITE (folder refused).
	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{NativeID: destDirID}, "deck-copy.pdf", ManagerMutationOptions{}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CopyManagerItem(conflict) = %v, want ErrManagerConflict", err)
	}
	if resSkip, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{NativeID: destDirID}, "deck-copy.pdf", ManagerMutationOptions{ConflictStrategy: ManagerConflictSkip}); err != nil || resSkip.Status != "skipped" || !resSkip.Native {
		t.Fatalf("CopyManagerItem(SKIP) = (%+v, %v)", resSkip, err)
	}
	if resRename, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{NativeID: destDirID}, "deck-copy.pdf", ManagerMutationOptions{ConflictStrategy: ManagerConflictRename}); err != nil || resRename.Status != "renamed_on_conflict" || resRename.FinalName != "deck-copy (1).pdf" || !resRename.Native {
		t.Fatalf("CopyManagerItem(RENAME) = (%+v, %v)", resRename, err)
	}
	if resOverwrite, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{NativeID: destDirID}, "existing.pdf", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}); err != nil || resOverwrite.Status != "overwritten" || resOverwrite.FinalName != "existing.pdf" || !resOverwrite.Native {
		t.Fatalf("CopyManagerItem(OVERWRITE) = (%+v, %v)", resOverwrite, err)
	}
	if _, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: targetConflictID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overwritten target file ID should be deleted, got %v", err)
	}
	overwrittenStream, err := p.StreamDownload(ctx, "files", "/Target/existing.pdf")
	if err != nil {
		t.Fatalf("StreamDownload(/Target/existing.pdf): %v", err)
	}
	overwrittenBytes, _ := io.ReadAll(overwrittenStream)
	overwrittenStream.Close()
	if string(overwrittenBytes) != "native-copy-bytes" {
		t.Fatalf("overwritten content = %q, want native-copy-bytes", overwrittenBytes)
	}

	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: srcDirID}, ManagerLocator{NativeID: destDirID}, "SourceCopy", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CopyManagerItem(folder overwrite) = %v, want ErrManagerConflict", err)
	}
	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: "0"}, ManagerLocator{NativeID: destDirID}, "Root", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
		t.Fatalf("CopyManagerItem(root) = %v, want ErrManagerUnsupported", err)
	}
	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: fileID}, ManagerLocator{Path: "/Target"}, "x.pdf", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
		t.Fatalf("CopyManagerItem(path-only dest) = %v, want ErrManagerInvalidDestination", err)
	}
}

func TestBoxFileManagerDelete(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	emptyID := srv.addFolder(docsID, "11", "Empty")
	nestedID := srv.addFolder(docsID, "12", "Nested")
	fileID := srv.addFileContent(nestedID, "13", "note.txt", []byte("bye"))
	rootFileID := srv.addFileContent("0", "14", "root.txt", []byte("root"))

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// Warm pathCache before deletions.
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/Nested"); err != nil {
		t.Fatalf("warm GetDirectoryListing(/Docs/Nested): %v", err)
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/Empty"); err != nil {
		t.Fatalf("warm GetDirectoryListing(/Docs/Empty): %v", err)
	}

	// 1. Root and path-only locators fail closed.
	for _, rootLoc := range []ManagerLocator{{NativeID: "0"}, {Path: "/"}, {}} {
		if err := fm.DeleteManagerItem(ctx, rootLoc, true); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("DeleteManagerItem(%+v) = %v, want ErrManagerUnsupported", rootLoc, err)
		}
	}
	if err := fm.DeleteManagerItem(ctx, ManagerLocator{Path: "/Docs/Nested/note.txt"}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteManagerItem(path-only) = %v, want ErrNotFound", err)
	}

	// 2. Delete file updates subsequent listings.
	if err := fm.DeleteManagerItem(ctx, ManagerLocator{NativeID: rootFileID}, false); err != nil {
		t.Fatalf("DeleteManagerItem(file): %v", err)
	}
	rootPage, err := fm.ListManager(ctx, ManagerLocator{NativeID: "0"}, ManagerListOptions{Limit: 50})
	if err != nil || len(rootPage.Items) != 1 || rootPage.Items[0].Name != "Docs" {
		t.Fatalf("rootPage after file delete = (%+v, %v)", rootPage, err)
	}

	// 3. Non-recursive delete on non-empty folder returns ErrManagerDirectoryNotEmpty and preserves contents.
	if err := fm.DeleteManagerItem(ctx, ManagerLocator{NativeID: docsID}, false); !errors.Is(err, ErrManagerDirectoryNotEmpty) {
		t.Fatalf("DeleteManagerItem(non-empty non-recursive) = %v, want ErrManagerDirectoryNotEmpty", err)
	}
	if _, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: fileID}); err != nil {
		t.Fatalf("nested file should still exist after rejected non-recursive delete: %v", err)
	}

	// 4. Non-recursive delete on empty folder succeeds and invalidates pathCache.
	if err := fm.DeleteManagerItem(ctx, ManagerLocator{NativeID: emptyID}, false); err != nil {
		t.Fatalf("DeleteManagerItem(empty folder): %v", err)
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/Empty"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDirectoryListing(/Docs/Empty) after delete = %v, want ErrNotFound", err)
	}

	// 5. Recursive delete removes folder tree and invalidates descendant pathCache.
	if err := fm.DeleteManagerItem(ctx, ManagerLocator{NativeID: docsID}, true); err != nil {
		t.Fatalf("DeleteManagerItem(recursive): %v", err)
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "/Docs/Nested"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDirectoryListing(/Docs/Nested) after recursive delete = %v, want ErrNotFound", err)
	}
	rootAfter, err := fm.ListManager(ctx, ManagerLocator{NativeID: "0"}, ManagerListOptions{Limit: 50})
	if err != nil || len(rootAfter.Items) != 0 {
		t.Fatalf("rootAfter recursive delete = (%+v, %v), want empty", rootAfter, err)
	}

	// Recreating /Docs/Nested after deletion must resolve to the new IDs, not stale pathCache entries.
	if err := p.CreateDirectory(ctx, "files", "/Docs/Nested"); err != nil {
		t.Fatalf("CreateDirectory(/Docs/Nested) after delete: %v", err)
	}
	recreatedLoc, _, fallback, err := fm.ResolveManagerPath(ctx, "/Docs/Nested")
	if err != nil || fallback || recreatedLoc.NativeID == "" || recreatedLoc.NativeID == nestedID {
		t.Fatalf("ResolveManagerPath(/Docs/Nested) = (%+v, fallback=%v, err=%v), want new ID != %q", recreatedLoc, fallback, err, nestedID)
	}
}

func TestBoxFileManagerOverwriteFailurePreservesOriginal(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	srcID := srv.addFolder("0", "10", "Source")
	dstID := srv.addFolder("0", "20", "Target")
	srcFileID := srv.addFileContent(srcID, "11", "new.txt", []byte("new-payload"))
	dstFileID := srv.addFileContent(dstID, "21", "target.txt", []byte("original-payload"))

	p := srv.provider()
	defer p.Close()
	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	ctx := context.Background()

	// 1. Copy overwrite fails during POST /copy -> original file stays untouched.
	srv.mu.Lock()
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/copy") {
			return &fakeBoxFault{status: http.StatusForbidden, body: `{"type":"error","status":403,"code":"forbidden_by_policy"}`}
		}
		return nil
	}
	srv.mu.Unlock()

	if _, err := fm.CopyManagerItem(ctx, ManagerLocator{NativeID: srcFileID}, ManagerLocator{NativeID: dstID}, "target.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}); err == nil {
		t.Fatal("expected CopyManagerItem(OVERWRITE) error when copy fails")
	}
	dlAfterCopyFail, err := fm.DownloadManager(ctx, ManagerLocator{NativeID: dstFileID})
	if err != nil {
		t.Fatalf("original file missing after failed copy overwrite: %v", err)
	}
	gotCopyBytes, _ := io.ReadAll(dlAfterCopyFail.Stream)
	dlAfterCopyFail.Stream.Close()
	if string(gotCopyBytes) != "original-payload" {
		t.Fatalf("original file content after failed copy = %q, want original-payload", gotCopyBytes)
	}

	// 2. Move overwrite fails on moving source (second PUT) -> backup is rolled back cleanly.
	srv.mu.Lock()
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPut && r.URL.Path == "/2.0/files/"+srcFileID {
			return &fakeBoxFault{status: http.StatusForbidden, body: `{"type":"error","status":403,"code":"forbidden_by_policy"}`}
		}
		return nil
	}
	srv.mu.Unlock()

	if _, err := fm.MoveManagerItem(ctx, ManagerLocator{NativeID: srcFileID}, ManagerLocator{NativeID: dstID}, "target.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}); err == nil {
		t.Fatal("expected MoveManagerItem(OVERWRITE) error when move fails")
	}
	srv.mu.Lock()
	srv.faultHook = nil
	srv.mu.Unlock()

	dlAfterMoveFail, err := p.StreamDownload(ctx, "files", "/Target/target.txt")
	if err != nil {
		t.Fatalf("StreamDownload(/Target/target.txt) after failed move overwrite: %v", err)
	}
	gotMoveBytes, _ := io.ReadAll(dlAfterMoveFail)
	dlAfterMoveFail.Close()
	if string(gotMoveBytes) != "original-payload" {
		t.Fatalf("original file content after failed move = %q, want original-payload", gotMoveBytes)
	}
}
