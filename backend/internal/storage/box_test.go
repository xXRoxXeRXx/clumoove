package storage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBoxFileVersion struct {
	Size              int64
	SHA1              string
	Content           []byte
	ModifiedAt        string
	ContentModifiedAt string
}

type fakeBoxItem struct {
	ID                string
	Type              string // "folder" or "file"
	Name              string
	ParentID          string
	Size              int64
	SHA1              string
	Content           []byte
	ModifiedAt        string
	ContentModifiedAt string
	Versions          []fakeBoxFileVersion
	URL               string
	DownloadBlocked   bool
	BlockedCode       string
	UnsupportedMedia  bool
	Thumbnail         []byte
	ThumbnailType     string
	ThumbnailRedirect bool
}

type fakeBoxFault struct {
	status     int
	retryAfter string
	location   string
	body       string
}

type fakeBoxServer struct {
	server         *httptest.Server
	downloadServer *httptest.Server
	token          string

	mu                  sync.Mutex
	items               map[string]fakeBoxItem
	children            map[string][]string
	faults              []fakeBoxFault
	requests            []string
	downloadAuthHeaders []string
	downloadRanges      []string
	redirectOverride        string
	rangeResponder          func(w http.ResponseWriter, r *http.Request, item fakeBoxItem)
	faultHook               func(r *http.Request) *fakeBoxFault
	trapHit                 bool
	nextID                  int
	maxPageSize             int
	singleUploadHeaders     []http.Header
	singleUploadOrder       [][]string
	sessions                map[string]*fakeBoxUploadSession
	abortedSessions         []string
	chunkPartSize           int64
	commitAcceptedRemaining int
	commitRetryAfter        string
	commitDigests           []string
	partDigests             []string
	omitConflictContext     bool
}

type fakeBoxUploadSession struct {
	ID       string
	FileID   string
	FolderID string
	FileName string
	FileSize int64
	PartSize int64
	Parts    map[int64][]byte
}

func newFakeBoxServer(token string) *fakeBoxServer {
	f := &fakeBoxServer{
		token:    token,
		items:    make(map[string]fakeBoxItem),
		children: make(map[string][]string),
		sessions: make(map[string]*fakeBoxUploadSession),
		nextID:   100,
	}
	f.items["0"] = fakeBoxItem{ID: "0", Type: "folder", Name: "All Files"}
	f.downloadServer = httptest.NewServer(http.HandlerFunc(f.handleDownload))
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeBoxServer) close() {
	f.server.Close()
	if f.downloadServer != nil {
		f.downloadServer.Close()
	}
}

func (f *fakeBoxServer) provider() *BoxProvider {
	return newBoxProvider(f.token, f.server.URL+"/2.0", f.server.URL+"/api/2.0", f.server.Client())
}

func (f *fakeBoxServer) addFolder(parentID, id, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "" {
		f.nextID++
		id = strconv.Itoa(f.nextID)
	}
	f.items[id] = fakeBoxItem{
		ID:         id,
		Type:       "folder",
		Name:       name,
		ParentID:   parentID,
		ModifiedAt: "2026-01-15T10:00:00Z",
	}
	f.children[parentID] = append(f.children[parentID], id)
	return id
}

func (f *fakeBoxServer) addFile(parentID, id, name string, size int64, sha1Hex string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "" {
		f.nextID++
		id = strconv.Itoa(f.nextID)
	}
	f.items[id] = fakeBoxItem{
		ID:                id,
		Type:              "file",
		Name:              name,
		ParentID:          parentID,
		Size:              size,
		SHA1:              sha1Hex,
		ModifiedAt:        "2026-01-16T12:00:00Z",
		ContentModifiedAt: "2026-01-16T11:30:00Z",
	}
	f.children[parentID] = append(f.children[parentID], id)
	return id
}

func (f *fakeBoxServer) addFileContent(parentID, id, name string, content []byte) string {
	sum := sha1.Sum(content)
	sha1Hex := hex.EncodeToString(sum[:])
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "" {
		f.nextID++
		id = strconv.Itoa(f.nextID)
	}
	copied := append([]byte(nil), content...)
	f.items[id] = fakeBoxItem{
		ID:                id,
		Type:              "file",
		Name:              name,
		ParentID:          parentID,
		Size:              int64(len(content)),
		SHA1:              sha1Hex,
		Content:           copied,
		ModifiedAt:        "2026-01-16T12:00:00Z",
		ContentModifiedAt: "2026-01-16T11:30:00Z",
		Versions: []fakeBoxFileVersion{{
			Size:              int64(len(content)),
			SHA1:              sha1Hex,
			Content:           append([]byte(nil), copied...),
			ModifiedAt:        "2026-01-16T12:00:00Z",
			ContentModifiedAt: "2026-01-16T11:30:00Z",
		}},
	}
	f.children[parentID] = append(f.children[parentID], id)
	return id
}

func (f *fakeBoxServer) addWebLink(parentID, id, name, rawURL string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "" {
		f.nextID++
		id = strconv.Itoa(f.nextID)
	}
	f.items[id] = fakeBoxItem{
		ID:         id,
		Type:       "web_link",
		Name:       name,
		ParentID:   parentID,
		URL:        rawURL,
		ModifiedAt: "2026-01-16T12:00:00Z",
	}
	f.children[parentID] = append(f.children[parentID], id)
	return id
}

func (f *fakeBoxServer) enqueueFaults(faults ...fakeBoxFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, faults...)
}

func (f *fakeBoxServer) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeBoxServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	if len(f.faults) > 0 {
		fault := f.faults[0]
		f.faults = f.faults[1:]
		f.mu.Unlock()
		if fault.retryAfter != "" {
			w.Header().Set("Retry-After", fault.retryAfter)
		}
		if fault.location != "" {
			w.Header().Set("Location", fault.location)
		}
		w.WriteHeader(fault.status)
		if fault.body != "" {
			_, _ = w.Write([]byte(fault.body))
		}
		return
	}
	hook := f.faultHook
	f.mu.Unlock()
	if hook != nil {
		if fault := hook(r); fault != nil {
			if fault.retryAfter != "" {
				w.Header().Set("Retry-After", fault.retryAfter)
			}
			w.WriteHeader(fault.status)
			if fault.body != "" {
				_, _ = w.Write([]byte(fault.body))
			}
			return
		}
	}

	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","status":401,"code":"unauthorized"}`))
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/2.0/users/me":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"11446498","type":"user","name":"Ada Box","login":"ada@example.test"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/") && strings.HasSuffix(r.URL.Path, "/items"):
		folderID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/folders/"), "/items")
		f.handleListFolderItems(w, r, folderID)
	case r.Method == http.MethodPost && r.URL.Path == "/2.0/folders":
		f.handleCreateFolder(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/"):
		folderID := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
		f.handleGetItem(w, folderID, "folder")
	case r.Method == http.MethodGet && r.URL.Path == "/trap-placeholder":
		f.mu.Lock()
		f.trapHit = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/content")
		f.handleDownloadRedirect(w, r, fileID)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/") &&
		(strings.HasSuffix(r.URL.Path, "/thumbnail.png") || strings.HasSuffix(r.URL.Path, "/thumbnail.jpg")):
		trimmed := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		fileID := strings.SplitN(trimmed, "/", 2)[0]
		f.handleThumbnail(w, r, fileID)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/"):
		fileID := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		f.handleGetItem(w, fileID, "file")
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/content":
		f.handleSingleUpload(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/") && strings.HasSuffix(r.URL.Path, "/content"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/"), "/content")
		f.handleSingleUploadVersion(w, r, fileID)
	case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/upload_sessions":
		f.handleCreateUploadSession(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/") && strings.HasSuffix(r.URL.Path, "/upload_sessions"):
		fileID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/"), "/upload_sessions")
		f.handleCreateNewVersionUploadSession(w, r, fileID)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") && strings.HasSuffix(r.URL.Path, "/commit"):
		sessionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"), "/commit")
		f.handleCommitUploadSession(w, r, sessionID)
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"):
		sessionID := strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/")
		f.handleUploadPart(w, r, sessionID)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/"):
		sessionID := strings.TrimPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/")
		f.handleAbortUploadSession(w, r, sessionID)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeBoxServer) handleDownloadRedirect(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	item, ok := f.items[id]
	redirectOverride := f.redirectOverride
	f.mu.Unlock()
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	if item.DownloadBlocked {
		code := item.BlockedCode
		if code == "" {
			code = "forbidden_by_policy"
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(fmt.Sprintf(`{"type":"error","status":403,"code":%q}`, code)))
		return
	}
	target := f.downloadServer.URL + "/dl/" + id
	if redirectOverride != "" {
		target = redirectOverride
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

func (f *fakeBoxServer) handleThumbnail(w http.ResponseWriter, r *http.Request, id string) {
	f.mu.Lock()
	item, ok := f.items[id]
	redirectOverride := f.redirectOverride
	f.mu.Unlock()
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	if item.UnsupportedMedia {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		_, _ = w.Write([]byte(`{"type":"error","status":415,"code":"unsupported_media_type"}`))
		return
	}
	if item.ThumbnailRedirect || redirectOverride != "" {
		target := f.downloadServer.URL + "/thumb/" + id
		if redirectOverride != "" {
			target = redirectOverride
		}
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
		return
	}
	thumb := item.Thumbnail
	if len(thumb) == 0 {
		thumb = []byte("fake-box-thumb")
	}
	cType := item.ThumbnailType
	if cType == "" {
		if strings.HasSuffix(r.URL.Path, ".jpg") {
			cType = "image/jpeg"
		} else {
			cType = "image/png"
		}
	}
	w.Header().Set("Content-Type", cType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(thumb)
}

func (f *fakeBoxServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	isThumb := strings.HasPrefix(r.URL.Path, "/thumb/")
	id := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/dl/"), "/thumb/")
	f.mu.Lock()
	f.downloadAuthHeaders = append(f.downloadAuthHeaders, r.Header.Get("Authorization"))
	f.downloadRanges = append(f.downloadRanges, r.Header.Get("Range"))
	item, ok := f.items[id]
	responder := f.rangeResponder
	f.mu.Unlock()

	if r.Header.Get("Authorization") != "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("authorization header must not be forwarded to download host"))
		return
	}
	if !ok || item.Type != "file" {
		http.NotFound(w, r)
		return
	}
	if isThumb {
		cType := item.ThumbnailType
		if cType == "" {
			cType = "image/png"
		}
		thumb := item.Thumbnail
		if len(thumb) == 0 {
			thumb = []byte("fake-redirect-thumb")
		}
		w.Header().Set("Content-Type", cType)
		w.Header().Set("Content-Length", strconv.Itoa(len(thumb)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(thumb)
		return
	}
	if responder != nil && r.Header.Get("Range") != "" {
		responder(w, r, item)
		return
	}
	if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
		if !strings.HasPrefix(rangeHdr, "bytes=") {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(rangeHdr, "bytes="), "-", 2)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start, err1 := strconv.ParseInt(parts[0], 10, 64)
		end, err2 := strconv.ParseInt(parts[1], 10, 64)
		total := int64(len(item.Content))
		if err1 != nil || err2 != nil || start < 0 || end < start || start >= total {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= total {
			end = total - 1
		}
		slice := item.Content[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(item.Content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(item.Content)
}

func (f *fakeBoxServer) handleGetItem(w http.ResponseWriter, id, wantType string) {
	f.mu.Lock()
	item, ok := f.items[id]
	f.mu.Unlock()
	if !ok || item.Type != wantType {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":                  item.ID,
		"type":                item.Type,
		"name":                item.Name,
		"size":                item.Size,
		"sha1":                item.SHA1,
		"modified_at":         item.ModifiedAt,
		"content_modified_at": item.ContentModifiedAt,
	})
}

func (f *fakeBoxServer) handleListFolderItems(w http.ResponseWriter, r *http.Request, folderID string) {
	f.mu.Lock()
	parent, exists := f.items[folderID]
	childIDs := append([]string(nil), f.children[folderID]...)
	entries := make([]map[string]any, 0, len(childIDs))
	for _, cid := range childIDs {
		it := f.items[cid]
		entries = append(entries, map[string]any{
			"id":                  it.ID,
			"type":                it.Type,
			"name":                it.Name,
			"size":                it.Size,
			"sha1":                it.SHA1,
			"modified_at":         it.ModifiedAt,
			"content_modified_at": it.ContentModifiedAt,
			"url":                 it.URL,
		})
	}
	f.mu.Unlock()

	if !exists || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}

	limit := 1000
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	f.mu.Lock()
	maxPageSize := f.maxPageSize
	f.mu.Unlock()
	if maxPageSize > 0 && limit > maxPageSize {
		limit = maxPageSize
	}
	offset := 0
	if marker := r.URL.Query().Get("marker"); marker != "" {
		if parsed, err := strconv.Atoi(marker); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := offset + limit
	nextMarker := ""
	if end < len(entries) {
		nextMarker = strconv.Itoa(end)
	} else {
		end = len(entries)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"entries":     entries[offset:end],
		"limit":       limit,
		"next_marker": nextMarker,
	})
}

func TestBoxConnectSuccessAndAuthFailure(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	okProvider := srv.provider()
	defer okProvider.Close()

	connected, err := okProvider.Connect(context.Background())
	if err != nil || !connected {
		t.Fatalf("Connect(valid) = (%v, %v), want (true, nil)", connected, err)
	}

	badProvider := newBoxProvider("wrong-token", srv.server.URL+"/2.0", srv.server.URL+"/api/2.0", srv.server.Client())
	defer badProvider.Close()

	connected, err = badProvider.Connect(context.Background())
	if connected || !errors.Is(err, ErrAuth) {
		t.Fatalf("Connect(invalid) = (%v, %v), want (false, ErrAuth)", connected, err)
	}
}

func TestBoxRetriesRateLimitsHonoringRetryAfter(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	srv.enqueueFaults(fakeBoxFault{
		status:     http.StatusTooManyRequests,
		retryAfter: "0",
		body:       `{"type":"error","status":429,"code":"rate_limit_exceeded"}`,
	})

	p := srv.provider()
	defer p.Close()

	connected, err := p.Connect(context.Background())
	if err != nil || !connected {
		t.Fatalf("Connect after 429 = (%v, %v), want (true, nil)", connected, err)
	}
	if got := srv.requestCount(); got != 2 {
		t.Fatalf("requestCount = %d, want 2 (1 initial + 1 retry)", got)
	}

	// Verify Retry-After wait is honored and cancelled cleanly by context.
	srv.enqueueFaults(fakeBoxFault{
		status:     http.StatusTooManyRequests,
		retryAfter: "5",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = p.Connect(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect during 5s Retry-After with 50ms timeout = %v, want context.DeadlineExceeded", err)
	}
}

func TestBoxRetriesTransient5xxUpToThreeTimes(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	// 3 transient 503 responses followed by 200 on the 4th attempt should succeed.
	srv.enqueueFaults(
		fakeBoxFault{status: http.StatusServiceUnavailable},
		fakeBoxFault{status: http.StatusBadGateway},
		fakeBoxFault{status: http.StatusInternalServerError},
	)

	p := srv.provider()
	defer p.Close()

	connected, err := p.Connect(context.Background())
	if err != nil || !connected {
		t.Fatalf("Connect after 3 transient 5xx = (%v, %v), want (true, nil)", connected, err)
	}
	if got := srv.requestCount(); got != 4 {
		t.Fatalf("requestCount after 3 retries = %d, want 4", got)
	}

	// 4 consecutive 503 responses exceed the 3-retry budget and must fail without a 5th call.
	before := srv.requestCount()
	srv.enqueueFaults(
		fakeBoxFault{status: http.StatusServiceUnavailable},
		fakeBoxFault{status: http.StatusServiceUnavailable},
		fakeBoxFault{status: http.StatusServiceUnavailable},
		fakeBoxFault{status: http.StatusServiceUnavailable},
		fakeBoxFault{status: http.StatusOK},
	)
	connected, err = p.Connect(context.Background())
	if connected || err == nil {
		t.Fatalf("Connect after 4 consecutive 503s = (%v, %v), want failure", connected, err)
	}
	if got := srv.requestCount() - before; got != 4 {
		t.Fatalf("requestCount for exhausted retries = %d, want 4 (1 initial + 3 retries)", got)
	}
}

func TestBoxProviderNameLeakGuard(t *testing.T) {
	srv := newFakeBoxServer("super-secret-box-oauth-token")
	defer srv.close()
	bad := newBoxProvider("super-secret-box-oauth-token", srv.server.URL+"/2.0", srv.server.URL+"/api/2.0", srv.server.Client())
	srv.token = "different-token"
	_, err := bad.Connect(context.Background())
	if err == nil || strings.Contains(err.Error(), "super-secret-box-oauth-token") {
		t.Fatalf("error leaked token or unexpected nil: %v", err)
	}
	_ = fmt.Sprintf("checked")
}

func TestBoxGetDirectoryListingPaginatedAndAbsolutePaths(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.maxPageSize = 2

	docsID := srv.addFolder("0", "10", "Docs")
	nestedID := srv.addFolder(docsID, "20", "2026")
	srv.addFile("0", "11", "readme.txt", 42, "AAF4C61DDCC5E8A2DABEDE0F3B482CD9AEA9434D")
	srv.addFile(nestedID, "21", "a.pdf", 100, "2FD4E1C67A2D28FCED849EE1BB76E7391B93EB12")
	srv.addFile(nestedID, "22", "b.pdf", 200, "DE9F2C7FD25E1B3AFAD3E85A0BD17D9B100DB4B3")
	srv.addFile(nestedID, "23", "c.pdf", 300, "")
	srv.addFolder(nestedID, "24", "Archive")

	p := srv.provider()
	defer p.Close()

	rootItems, err := p.GetDirectoryListing(context.Background(), "files", "/")
	if err != nil {
		t.Fatalf("GetDirectoryListing(/): %v", err)
	}
	if len(rootItems) != 2 {
		t.Fatalf("root len = %d, want 2: %+v", len(rootItems), rootItems)
	}
	if rootItems[0].Path != "/Docs" || !rootItems[0].IsDir {
		t.Fatalf("rootItems[0] = %+v, want dir /Docs", rootItems[0])
	}
	if rootItems[1].Path != "/readme.txt" || rootItems[1].IsDir || rootItems[1].Size != 42 || rootItems[1].Hash != "SHA1:aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Fatalf("rootItems[1] = %+v", rootItems[1])
	}

	nestedItems, err := p.GetDirectoryListing(context.Background(), "files", "/Docs/2026")
	if err != nil {
		t.Fatalf("GetDirectoryListing(/Docs/2026): %v", err)
	}
	if len(nestedItems) != 4 {
		t.Fatalf("nested len = %d, want 4: %+v", len(nestedItems), nestedItems)
	}
	wantPaths := map[string]bool{
		"/Docs/2026/a.pdf":   false,
		"/Docs/2026/b.pdf":   false,
		"/Docs/2026/c.pdf":   false,
		"/Docs/2026/Archive": true,
	}
	for _, it := range nestedItems {
		wantDir, ok := wantPaths[it.Path]
		if !ok {
			t.Fatalf("unexpected nested path %q", it.Path)
		}
		if it.IsDir != wantDir {
			t.Fatalf("path %q IsDir = %v, want %v", it.Path, it.IsDir, wantDir)
		}
		delete(wantPaths, it.Path)
	}
	if len(wantPaths) != 0 {
		t.Fatalf("missing nested paths: %v", wantPaths)
	}

	// Verify Box marker pagination and fields query parameters were sent.
	srv.mu.Lock()
	reqs := append([]string(nil), srv.requests...)
	srv.mu.Unlock()
	sawUseMarker := false
	sawLimit1000 := false
	sawFields := false
	for _, req := range reqs {
		if strings.Contains(req, "usemarker=true") {
			sawUseMarker = true
		}
		if strings.Contains(req, "limit=1000") {
			sawLimit1000 = true
		}
		if strings.Contains(req, "fields=") && strings.Contains(req, "sha1") {
			sawFields = true
		}
	}
	if !sawUseMarker || !sawLimit1000 || !sawFields {
		t.Fatalf("missing required Box listing params (usemarker=%v, limit=1000=%v, fields=%v) in %v", sawUseMarker, sawLimit1000, sawFields, reqs)
	}

	// Second listing of /Docs/2026 should reuse cached folder IDs rather than re-walking from root.
	beforeSecond := srv.requestCount()
	if _, err := p.GetDirectoryListing(context.Background(), "files", "/Docs/2026"); err != nil {
		t.Fatalf("second GetDirectoryListing(/Docs/2026): %v", err)
	}
	// 4 items with pageSize=2 takes 2 requests for folder 20; without cache it would also list 0 and 10.
	if got := srv.requestCount() - beforeSecond; got != 2 {
		t.Fatalf("cached nested listing made %d requests, want 2", got)
	}

	if _, err := p.GetDirectoryListing(context.Background(), "files", "/Docs/../secret"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("traversal error = %v, want ErrPathEscapesRoot", err)
	}
	if _, err := p.GetDirectoryListing(context.Background(), "calendars", "/"); !errors.Is(err, ErrUnsupportedResourceType) {
		t.Fatalf("unsupported resource type error = %v, want ErrUnsupportedResourceType", err)
	}
}

func TestBoxInspectResourceFileExistsAndHash(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFile(docsID, "12", "Report.pdf", 1234, "AAF4C61DDCC5E8A2DABEDE0F3B482CD9AEA9434D")
	srv.addFile(docsID, "13", "NoHash.bin", 99, "")

	p := srv.provider()
	defer p.Close()

	res, err := p.InspectResource(context.Background(), "files", "/docs/report.pdf")
	if err != nil {
		t.Fatalf("InspectResource: %v", err)
	}
	if res.Size != 1234 || res.IsDir || res.Hash != "SHA1:aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Fatalf("InspectResource = %+v", res)
	}

	if _, err := p.InspectResource(context.Background(), "files", "/docs/missing.pdf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("InspectResource(missing) = %v, want ErrNotFound", err)
	}

	exists, size, err := p.FileExists(context.Background(), "files", "/Docs/Report.pdf")
	if err != nil || !exists || size != 1234 {
		t.Fatalf("FileExists(existing) = (%v, %d, %v), want (true, 1234, nil)", exists, size, err)
	}

	exists, size, err = p.FileExists(context.Background(), "files", "/Docs/Missing.pdf")
	if err != nil || exists || size != 0 {
		t.Fatalf("FileExists(missing) = (%v, %d, %v), want (false, 0, nil)", exists, size, err)
	}

	hash, err := p.GetFileHash(context.Background(), "files", "/Docs/Report.pdf")
	if err != nil || hash != "SHA1:aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d" {
		t.Fatalf("GetFileHash = (%q, %v)", hash, err)
	}

	if _, err := p.GetFileHash(context.Background(), "files", "/Docs/NoHash.bin"); !errors.Is(err, ErrChecksumNotAvailable) {
		t.Fatalf("GetFileHash(empty) = %v, want ErrChecksumNotAvailable", err)
	}
}

func TestBoxStreamDownloadFollowsRedirectAndStripsAuthorization(t *testing.T) {
	srv := newFakeBoxServer("secret-box-token")
	defer srv.close()

	payload := []byte("hello box download stream with SHA-1 verification")
	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFileContent(docsID, "12", "hello.txt", payload)

	p := srv.provider()
	defer p.Close()

	stream, err := p.StreamDownload(context.Background(), "files", "/Docs/hello.txt")
	if err != nil {
		t.Fatalf("StreamDownload: %v", err)
	}
	defer stream.Close()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("downloaded = %q, want %q", got, payload)
	}

	srv.mu.Lock()
	auths := append([]string(nil), srv.downloadAuthHeaders...)
	srv.mu.Unlock()
	if len(auths) != 1 || auths[0] != "" {
		t.Fatalf("download host Authorization headers = %q, want single empty header", auths)
	}

	// Downloading a folder or missing file returns ErrNotFound.
	if _, err := p.StreamDownload(context.Background(), "files", "/Docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StreamDownload(folder) = %v, want ErrNotFound", err)
	}
	if _, err := p.StreamDownload(context.Background(), "files", "/Docs/missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StreamDownload(missing) = %v, want ErrNotFound", err)
	}
	if _, err := p.StreamDownload(context.Background(), "calendars", "/Docs/hello.txt"); !errors.Is(err, ErrUnsupportedResourceType) {
		t.Fatalf("StreamDownload(calendars) = %v, want ErrUnsupportedResourceType", err)
	}
}

type boxRoundTripFunc func(*http.Request) (*http.Response, error)

func (f boxRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestBoxStreamDownloadRejectsDisallowedRedirects(t *testing.T) {
	srv := newFakeBoxServer("secret-box-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFileContent(docsID, "12", "hello.txt", []byte("payload"))
	p := srv.provider()
	defer p.Close()

	// Even from a local fake API server, non-local disallowed targets must be rejected.
	for _, target := range []string{
		"http://dl.boxcloud.com/dl/12",
		"https://169.254.169.254/latest/meta-data",
		"http://localhost.evil.test/dl/12",
	} {
		srv.mu.Lock()
		srv.redirectOverride = target
		srv.mu.Unlock()

		stream, err := p.StreamDownload(context.Background(), "files", "/Docs/hello.txt")
		if err == nil {
			stream.Close()
			t.Fatalf("expected redirect to %q to be rejected", target)
		}
		if !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("expected redirect rejection error for %q, got: %v", target, err)
		}
		if strings.Contains(err.Error(), "secret-box-token") {
			t.Fatalf("error leaked token: %v", err)
		}
	}

	// Production endpoint (non-local apiBase) must reject loopback, metadata, and plaintext HTTP redirects.
	for _, redirectURL := range []string{
		"https://127.0.0.1/dl/12",
		"https://169.254.169.254/latest/meta-data",
		"http://dl.boxcloud.com/dl/12",
	} {
		redirectedToDownload := false
		client := &http.Client{
			Transport: boxRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "api.box.com" && req.URL.Path == "/2.0/folders/0/items" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"entries":[{"id":"12","type":"file","name":"hello.txt","size":7}]}`)),
						Request:    req,
					}, nil
				}
				if req.URL.Host == "api.box.com" && req.URL.Path == "/2.0/files/12/content" {
					return &http.Response{
						StatusCode: http.StatusFound,
						Header:     http.Header{"Location": []string{redirectURL}},
						Body:       io.NopCloser(strings.NewReader("")),
						Request:    req,
					}, nil
				}
				redirectedToDownload = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("should not reach")),
					Request:    req,
				}, nil
			}),
		}
		prodLike := newBoxProvider("secret-box-token", defaultBoxAPIBase, defaultBoxUploadBase, client)
		stream, err := prodLike.StreamDownload(context.Background(), "files", "/hello.txt")
		if err == nil {
			stream.Close()
			t.Fatalf("prodLike redirect to %q succeeded, want rejection", redirectURL)
		}
		if !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("prodLike expected redirect rejection error for %q, got: %v", redirectURL, err)
		}
		if redirectedToDownload {
			t.Fatalf("prodLike followed disallowed redirect to %q", redirectURL)
		}
	}
}

func TestBoxStreamDownloadRangeAndMalformedRejection(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	payload := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFileContent(docsID, "12", "pack.bin", payload)

	p := srv.provider()
	defer p.Close()

	rd, ok := StorageProvider(p).(RangeDownloader)
	if !ok {
		t.Fatal("*BoxProvider does not implement RangeDownloader")
	}

	// Exact slice read across redirect without forwarding Authorization.
	stream, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 10, 6)
	if err != nil {
		t.Fatalf("StreamDownloadRange(10, 6): %v", err)
	}
	got, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		t.Fatalf("ReadAll(range): %v", err)
	}
	if string(got) != "ABCDEF" {
		t.Fatalf("range bytes = %q, want %q", got, "ABCDEF")
	}
	srv.mu.Lock()
	auths := append([]string(nil), srv.downloadAuthHeaders...)
	ranges := append([]string(nil), srv.downloadRanges...)
	srv.mu.Unlock()
	if len(auths) != 1 || auths[0] != "" {
		t.Fatalf("range download Authorization headers = %q, want empty", auths)
	}
	if len(ranges) != 1 || ranges[0] != "bytes=10-15" {
		t.Fatalf("range download Range headers = %q, want [bytes=10-15]", ranges)
	}

	// Invalid inputs fail locally before hitting the network.
	for _, tc := range []struct {
		offset int64
		length int64
	}{
		{-1, 5},
		{0, 0},
		{0, -4},
	} {
		if _, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", tc.offset, tc.length); !errors.Is(err, ErrInvalidByteRange) {
			t.Fatalf("StreamDownloadRange(%d, %d) = %v, want ErrInvalidByteRange", tc.offset, tc.length, err)
		}
	}

	if _, err := rd.StreamDownloadRange(context.Background(), "calendars", "/Docs/pack.bin", 0, 5); !errors.Is(err, ErrUnsupportedResourceType) {
		t.Fatalf("StreamDownloadRange(calendars) = %v, want ErrUnsupportedResourceType", err)
	}

	// Malformed 200 OK instead of 206 Partial Content must be rejected.
	srv.mu.Lock()
	srv.rangeResponder = func(w http.ResponseWriter, _ *http.Request, item fakeBoxItem) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(item.Content)
	}
	srv.mu.Unlock()
	if _, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 2, 4); !errors.Is(err, ErrRangeHeaderMismatch) {
		t.Fatalf("200 OK range response = %v, want ErrRangeHeaderMismatch", err)
	}

	// Mismatched Content-Range must be rejected.
	srv.mu.Lock()
	srv.rangeResponder = func(w http.ResponseWriter, _ *http.Request, item fakeBoxItem) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-3/%d", len(item.Content)))
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(item.Content[:4])
	}
	srv.mu.Unlock()
	if _, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 2, 4); !errors.Is(err, ErrRangeHeaderMismatch) {
		t.Fatalf("mismatched Content-Range = %v, want ErrRangeHeaderMismatch", err)
	}

	// Mismatched Content-Length must be rejected.
	srv.mu.Lock()
	srv.rangeResponder = func(w http.ResponseWriter, _ *http.Request, item fakeBoxItem) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 2-5/%d", len(item.Content)))
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(item.Content[2:12])
	}
	srv.mu.Unlock()
	if _, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 2, 4); !errors.Is(err, ErrRangeHeaderMismatch) {
		t.Fatalf("mismatched Content-Length = %v, want ErrRangeHeaderMismatch", err)
	}

	// 416 Range Not Satisfiable maps to ErrRangeNotSatisfied.
	srv.mu.Lock()
	srv.rangeResponder = func(w http.ResponseWriter, _ *http.Request, _ fakeBoxItem) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}
	srv.mu.Unlock()
	if _, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 2, 4); !errors.Is(err, ErrRangeNotSatisfied) {
		t.Fatalf("416 range response = %v, want ErrRangeNotSatisfied", err)
	}

	// Truncated 206 stream must return ErrUnexpectedEOF when read.
	srv.mu.Lock()
	srv.rangeResponder = func(w http.ResponseWriter, _ *http.Request, item fakeBoxItem) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 2-5/%d", len(item.Content)))
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(item.Content[2:4]) // 2 bytes instead of declared 4
	}
	srv.mu.Unlock()
	truncStream, err := rd.StreamDownloadRange(context.Background(), "files", "/Docs/pack.bin", 2, 4)
	if err != nil {
		t.Fatalf("StreamDownloadRange(truncated): %v", err)
	}
	_, readErr := io.ReadAll(truncStream)
	truncStream.Close()
	if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated range ReadAll error = %v, want ErrUnexpectedEOF", readErr)
	}
}

func TestBoxVerificationModeAndTestProviderGuard(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	p := srv.provider()
	defer p.Close()

	if got := p.VerificationMode(); got != VerificationCryptographicHash {
		t.Fatalf("VerificationMode() = %q, want %q", got, VerificationCryptographicHash)
	}
	if _, err := NewBoxTestProvider("", srv.server.URL+"/2.0", srv.server.URL+"/api/2.0", srv.server.Client()); !errors.Is(err, ErrAuth) {
		t.Fatalf("NewBoxTestProvider(empty token) = %v, want ErrAuth", err)
	}
	for _, badBase := range []string{
		"https://api.box.com/2.0",
		"http://localhost.evil.test/2.0",
		"http://127.0.0.1.evil.test/2.0",
	} {
		if _, err := NewBoxTestProvider("token", badBase, badBase, nil); err == nil {
			t.Fatalf("NewBoxTestProvider(%q) succeeded, want rejection", badBase)
		}
	}

	docsID := srv.addFolder("0", "10", "Docs")
	blockedID := srv.addFileContent(docsID, "13", "blocked.pdf", []byte("blocked"))
	srv.mu.Lock()
	it := srv.items[blockedID]
	it.DownloadBlocked = true
	srv.items[blockedID] = it
	srv.mu.Unlock()

	if _, err := p.StreamDownload(context.Background(), "files", "/Docs/blocked.pdf"); !errors.Is(err, ErrPermission) {
		t.Fatalf("StreamDownload(blocked) = %v, want ErrPermission", err)
	}
}

func TestBoxProviderMarksNonTransferableItemsAndMapsBlockedDownloads(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	p := srv.provider()
	defer p.Close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFileContent(docsID, "11", "Meeting.BoxNote", []byte(`{"doc":"note"}`))
	srv.addFileContent(docsID, "12", "Diagram.boxcanvas", []byte(`{"canvas":1}`))
	srv.addWebLink(docsID, "13", "Portal", "https://example.test/portal")
	srv.addFileContent(docsID, "14", "report.txt", []byte("plain"))
	blockedID := srv.addFileContent(docsID, "15", "blocked.pdf", []byte("secret"))
	srv.mu.Lock()
	it := srv.items[blockedID]
	it.DownloadBlocked = true
	srv.items[blockedID] = it
	srv.mu.Unlock()

	ctx := context.Background()
	listed, err := p.GetDirectoryListing(ctx, "files", "/Docs")
	if err != nil {
		t.Fatalf("GetDirectoryListing(/Docs): %v", err)
	}
	byName := make(map[string]CloudResource, len(listed))
	for _, res := range listed {
		byName[res.Name] = res
	}

	for _, name := range []string{"Meeting.BoxNote", "Diagram.boxcanvas", "Portal"} {
		res, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s from listing", name)
		}
		reason, skip := res.BoxSkipReason()
		if !skip || reason == "" {
			t.Fatalf("listing %s: BoxSkipReason() = (%q, %v), want non-empty reason and true", name, reason, skip)
		}
		inspected, err := p.InspectResource(ctx, "files", "/Docs/"+name)
		if err != nil {
			t.Fatalf("InspectResource(%s): %v", name, err)
		}
		insReason, insSkip := inspected.BoxSkipReason()
		if !insSkip || insReason != reason {
			t.Fatalf("InspectResource(%s).BoxSkipReason() = (%q, %v), want (%q, true)", name, insReason, insSkip, reason)
		}
		if _, err := p.StreamDownload(ctx, "files", "/Docs/"+name); !errors.Is(err, ErrPermanentTransfer) {
			t.Fatalf("StreamDownload(%s) = %v, want ErrPermanentTransfer", name, err)
		}
	}

	for _, name := range []string{"report.txt", "blocked.pdf"} {
		res, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s from listing", name)
		}
		if reason, skip := res.BoxSkipReason(); skip || reason != "" {
			t.Fatalf("listing %s: BoxSkipReason() = (%q, %v), want (\"\", false)", name, reason, skip)
		}
	}

	_, err = p.StreamDownload(ctx, "files", "/Docs/blocked.pdf")
	if !errors.Is(err, ErrPermission) || !errors.Is(err, ErrPermanentTransfer) {
		t.Fatalf("StreamDownload(blocked.pdf) = %v, want ErrPermission wrapping ErrPermanentTransfer", err)
	}
	if !strings.Contains(err.Error(), "forbidden_by_policy") {
		t.Fatalf("StreamDownload(blocked.pdf) error = %q, want Box error code forbidden_by_policy", err.Error())
	}

	deniedID := srv.addFileContent(docsID, "16", "denied.pdf", []byte("denied"))
	srv.mu.Lock()
	denied := srv.items[deniedID]
	denied.DownloadBlocked = true
	denied.BlockedCode = "access_denied_insufficient_permissions"
	srv.items[deniedID] = denied
	srv.mu.Unlock()
	_, err = p.StreamDownload(ctx, "files", "/Docs/denied.pdf")
	if !errors.Is(err, ErrPermission) || !errors.Is(err, ErrPermanentTransfer) || !strings.Contains(err.Error(), "access_denied_insufficient_permissions") {
		t.Fatalf("StreamDownload(denied.pdf) = %v, want ErrPermission with access_denied_insufficient_permissions", err)
	}
}

func (f *fakeBoxServer) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil || reqBody.Name == "" || reqBody.Parent.ID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_request"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	parent, ok := f.items[reqBody.Parent.ID]
	if !ok || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	for _, cid := range f.children[reqBody.Parent.ID] {
		existing := f.items[cid]
		if strings.EqualFold(existing.Name, reqBody.Name) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":   "error",
				"status": 409,
				"code":   "item_name_in_use",
				"context_info": map[string]any{
					"conflicts": map[string]any{
						"type": existing.Type,
						"id":   existing.ID,
						"name": existing.Name,
					},
				},
			})
			return
		}
	}

	f.nextID++
	id := strconv.Itoa(f.nextID)
	now := "2026-02-01T10:00:00Z"
	item := fakeBoxItem{
		ID:         id,
		Type:       "folder",
		Name:       reqBody.Name,
		ParentID:   reqBody.Parent.ID,
		ModifiedAt: now,
	}
	f.items[id] = item
	f.children[reqBody.Parent.ID] = append(f.children[reqBody.Parent.ID], id)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          item.ID,
		"type":        item.Type,
		"name":        item.Name,
		"modified_at": item.ModifiedAt,
	})
}

func TestBoxCreateDirectoryAndParentDirectories(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	p := srv.provider()
	defer p.Close()

	ctx := context.Background()
	if err := p.CreateDirectory(ctx, "files", "/Projects/2026/Q1"); err != nil {
		t.Fatalf("CreateDirectory(/Projects/2026/Q1): %v", err)
	}

	res, err := p.InspectResource(ctx, "files", "/Projects/2026/Q1")
	if err != nil || !res.IsDir {
		t.Fatalf("InspectResource(created dir) = (%+v, %v), want IsDir=true", res, err)
	}

	// Second call and CreateParentDirectories for a child file must hit pathCache with 0 HTTP requests.
	before := srv.requestCount()
	if err := p.CreateParentDirectories(ctx, "files", "/Projects/2026/Q1/report.pdf"); err != nil {
		t.Fatalf("CreateParentDirectories(cached): %v", err)
	}
	if got := srv.requestCount() - before; got != 0 {
		t.Fatalf("CreateParentDirectories on cached hierarchy made %d requests, want 0", got)
	}

	// Fresh provider with empty cache must handle 409 item_name_in_use idempotently and reuse folder IDs.
	p2 := srv.provider()
	defer p2.Close()
	if err := p2.CreateDirectory(ctx, "files", "/projects/2026/q1/Sub"); err != nil {
		t.Fatalf("CreateDirectory(existing case-insensitive prefix + new leaf): %v", err)
	}
	subRes, err := p2.InspectResource(ctx, "files", "/Projects/2026/Q1/Sub")
	if err != nil || !subRes.IsDir {
		t.Fatalf("InspectResource(Sub) = (%+v, %v), want IsDir=true", subRes, err)
	}

	// File blocking a folder path must fail with ErrManagerConflict.
	srv.addFileContent("0", "", "blocker.txt", []byte("not a folder"))
	if err := p2.CreateDirectory(ctx, "files", "/blocker.txt/nested"); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("CreateDirectory(blocked by file) = %v, want ErrManagerConflict", err)
	}

	if err := p2.CreateDirectory(ctx, "files", "/"); err != nil {
		t.Fatalf("CreateDirectory(/): %v", err)
	}
	if err := p2.CreateDirectory(ctx, "calendars", "/Projects"); !errors.Is(err, ErrUnsupportedResourceType) {
		t.Fatalf("CreateDirectory(calendars) = %v, want ErrUnsupportedResourceType", err)
	}
	if err := p2.CreateParentDirectories(ctx, "files", "/Projects/../secret.txt"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("CreateParentDirectories(traversal) = %v, want ErrPathEscapesRoot", err)
	}
}

type fakeBoxSingleUploadPayload struct {
	name              string
	parentID          string
	contentModifiedAt string
	fileData          []byte
	sha1Hex           string
}

func (f *fakeBoxServer) parseSingleUploadMultipart(w http.ResponseWriter, r *http.Request, requireParent bool) (fakeBoxSingleUploadPayload, bool) {
	mr, err := r.MultipartReader()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_request"}`))
		return fakeBoxSingleUploadPayload{}, false
	}

	var partOrder []string
	var attr struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
		ContentModifiedAt string `json:"content_modified_at"`
	}
	var fileData []byte
	gotAttrs := false
	gotFile := false

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return fakeBoxSingleUploadPayload{}, false
		}
		name := part.FormName()
		partOrder = append(partOrder, name)
		switch name {
		case "attributes":
			if gotFile {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"metadata_after_file_contents"}`))
				return fakeBoxSingleUploadPayload{}, false
			}
			if err := json.NewDecoder(part).Decode(&attr); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return fakeBoxSingleUploadPayload{}, false
			}
			gotAttrs = true
		case "file":
			if !gotAttrs {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"metadata_after_file_contents"}`))
				return fakeBoxSingleUploadPayload{}, false
			}
			fileData, err = io.ReadAll(part)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return fakeBoxSingleUploadPayload{}, false
			}
			gotFile = true
		}
		part.Close()
	}

	f.mu.Lock()
	f.singleUploadHeaders = append(f.singleUploadHeaders, r.Header.Clone())
	f.singleUploadOrder = append(f.singleUploadOrder, partOrder)
	f.mu.Unlock()

	if !gotAttrs || !gotFile || (requireParent && (attr.Name == "" || attr.Parent.ID == "")) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_request"}`))
		return fakeBoxSingleUploadPayload{}, false
	}

	sum := sha1.Sum(fileData)
	sha1Hex := hex.EncodeToString(sum[:])
	if contentMD5 := r.Header.Get("content-md5"); contentMD5 != "" && !strings.EqualFold(contentMD5, sha1Hex) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_digest"}`))
		return fakeBoxSingleUploadPayload{}, false
	}

	return fakeBoxSingleUploadPayload{
		name:              attr.Name,
		parentID:          attr.Parent.ID,
		contentModifiedAt: attr.ContentModifiedAt,
		fileData:          fileData,
		sha1Hex:           sha1Hex,
	}, true
}

func writeFakeBoxUploadEntry(w http.ResponseWriter, status int, item fakeBoxItem) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total_count": 1,
		"entries": []map[string]any{
			{
				"id":                  item.ID,
				"type":                item.Type,
				"name":                item.Name,
				"size":                item.Size,
				"sha1":                item.SHA1,
				"modified_at":         item.ModifiedAt,
				"content_modified_at": item.ContentModifiedAt,
			},
		},
	})
}

func (f *fakeBoxServer) handleSingleUpload(w http.ResponseWriter, r *http.Request) {
	payload, ok := f.parseSingleUploadMultipart(w, r, true)
	if !ok {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	parent, ok := f.items[payload.parentID]
	if !ok || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	for _, cid := range f.children[payload.parentID] {
		existing := f.items[cid]
		if strings.EqualFold(existing.Name, payload.name) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type":   "error",
				"status": 409,
				"code":   "item_name_in_use",
				"context_info": map[string]any{
					"conflicts": map[string]any{
						"type": existing.Type,
						"id":   existing.ID,
						"name": existing.Name,
					},
				},
			})
			return
		}
	}

	f.nextID++
	id := strconv.Itoa(f.nextID)
	now := "2026-02-05T12:00:00Z"
	modAt := payload.contentModifiedAt
	if modAt == "" {
		modAt = now
	}
	copied := append([]byte(nil), payload.fileData...)
	item := fakeBoxItem{
		ID:                id,
		Type:              "file",
		Name:              payload.name,
		ParentID:          payload.parentID,
		Size:              int64(len(copied)),
		SHA1:              payload.sha1Hex,
		Content:           copied,
		ModifiedAt:        now,
		ContentModifiedAt: modAt,
		Versions: []fakeBoxFileVersion{{
			Size:              int64(len(copied)),
			SHA1:              payload.sha1Hex,
			Content:           append([]byte(nil), copied...),
			ModifiedAt:        now,
			ContentModifiedAt: modAt,
		}},
	}
	f.items[id] = item
	f.children[payload.parentID] = append(f.children[payload.parentID], id)
	writeFakeBoxUploadEntry(w, http.StatusCreated, item)
}

func (f *fakeBoxServer) handleSingleUploadVersion(w http.ResponseWriter, r *http.Request, fileID string) {
	payload, ok := f.parseSingleUploadMultipart(w, r, false)
	if !ok {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	item, exists := f.items[fileID]
	if !exists || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}

	now := "2026-02-06T12:00:00Z"
	modAt := payload.contentModifiedAt
	if modAt == "" {
		modAt = now
	}
	copied := append([]byte(nil), payload.fileData...)
	if payload.name != "" {
		item.Name = payload.name
	}
	item.Size = int64(len(copied))
	item.SHA1 = payload.sha1Hex
	item.Content = copied
	item.ModifiedAt = now
	item.ContentModifiedAt = modAt
	item.Versions = append(item.Versions, fakeBoxFileVersion{
		Size:              int64(len(copied)),
		SHA1:              payload.sha1Hex,
		Content:           append([]byte(nil), copied...),
		ModifiedAt:        now,
		ContentModifiedAt: modAt,
	})
	f.items[fileID] = item
	writeFakeBoxUploadEntry(w, http.StatusCreated, item)
}

func TestBoxStreamUploadSingleRequestAttributesOrderSHA1AndMtime(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	p := srv.provider()
	defer p.Close()

	if _, ok := StorageProvider(p).(TransferUploader); !ok {
		t.Fatal("*BoxProvider does not implement TransferUploader")
	}

	payload := []byte("Box single-request multipart upload payload!")
	sum := sha1.Sum(payload)
	wantHex := hex.EncodeToString(sum[:])
	wantMtime := time.Date(2025, 11, 4, 8, 30, 45, 0, time.FixedZone("UTC+2", 2*3600))

	ctx := context.Background()
	res, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/2026/note.txt", bytes.NewReader(payload), int64(len(payload)), UploadOptions{
		Metadata: FileMetadata{ModifiedTime: wantMtime},
		Checksum: "SHA1:" + strings.ToUpper(wantHex),
	})
	if err != nil {
		t.Fatalf("StreamUploadWithOptions: %v", err)
	}
	if res.Receipt.TargetResourceID == "" {
		t.Fatal("UploadResult.Receipt.TargetResourceID is empty")
	}

	srv.mu.Lock()
	orders := append([][]string(nil), srv.singleUploadOrder...)
	hdrs := append([]http.Header(nil), srv.singleUploadHeaders...)
	srv.mu.Unlock()
	if len(orders) != 1 || len(orders[0]) != 2 || orders[0][0] != "attributes" || orders[0][1] != "file" {
		t.Fatalf("multipart part order = %v, want [attributes file]", orders)
	}
	if gotDigest := hdrs[0].Get("content-md5"); gotDigest != wantHex {
		t.Fatalf("content-md5 header = %q, want %q", gotDigest, wantHex)
	}

	inspected, err := p.InspectResource(ctx, "files", "/Docs/2026/note.txt")
	if err != nil {
		t.Fatalf("InspectResource: %v", err)
	}
	if inspected.Size != int64(len(payload)) || inspected.Hash != "SHA1:"+wantHex {
		t.Fatalf("inspected = %+v, want size=%d hash=SHA1:%s", inspected, len(payload), wantHex)
	}
	if !inspected.LastModified.Equal(wantMtime.UTC()) {
		t.Fatalf("LastModified = %v, want %v", inspected.LastModified, wantMtime.UTC())
	}

	// Plain StreamUpload (without options) also succeeds and preserves bytes.
	if err := p.StreamUpload(ctx, "files", "/Docs/2026/plain.txt", strings.NewReader("plain"), 5); err != nil {
		t.Fatalf("StreamUpload: %v", err)
	}
	dl, err := p.StreamDownload(ctx, "files", "/Docs/2026/plain.txt")
	if err != nil {
		t.Fatalf("StreamDownload(plain): %v", err)
	}
	got, _ := io.ReadAll(dl)
	dl.Close()
	if string(got) != "plain" {
		t.Fatalf("downloaded = %q, want plain", got)
	}
	_ = base64.StdEncoding // reserved for chunked slice
}

func TestBoxStreamUploadBadDigestAndConflict(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	p := srv.provider()
	defer p.Close()

	ctx := context.Background()
	wrongSHA1 := strings.Repeat("a", 40)
	_, err := p.StreamUploadWithOptions(ctx, "files", "/bad.txt", strings.NewReader("hello"), 5, UploadOptions{
		Checksum: "SHA1:" + wrongSHA1,
	})
	if err == nil {
		t.Fatal("expected bad_digest error on mismatched SHA-1, got nil")
	}
	exists, _, err := p.FileExists(ctx, "files", "/bad.txt")
	if err != nil || exists {
		t.Fatalf("FileExists(/bad.txt) = (%v, %v), want (false, nil)", exists, err)
	}

	srv.addFileContent("0", "555", "existing.txt", []byte("keep-me"))
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/existing.txt", strings.NewReader("overwrite"), 9, UploadOptions{}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("duplicate upload err = %v, want ErrManagerConflict", err)
	}
	dl, err := p.StreamDownload(ctx, "files", "/existing.txt")
	if err != nil {
		t.Fatalf("StreamDownload(existing): %v", err)
	}
	got, _ := io.ReadAll(dl)
	dl.Close()
	if string(got) != "keep-me" {
		t.Fatalf("existing file mutated to %q, want keep-me", got)
	}
}

func TestBoxStreamUploadDoesNotRetryStreamedBodyOn429Or5xx(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	p := srv.provider()
	defer p.Close()

	ctx := context.Background()
	// Warm root parent path (root "0" needs no folder creation).
	srv.enqueueFaults(fakeBoxFault{
		status:     http.StatusTooManyRequests,
		retryAfter: "17",
		body:       `{"type":"error","status":429,"code":"rate_limit_exceeded"}`,
	})
	before := srv.requestCount()
	err := p.StreamUpload(ctx, "files", "/rate.txt", strings.NewReader("stream-body"), 11)
	var retryErr *RetryAfterError
	if !errors.As(err, &retryErr) || retryErr.After != 17*time.Second {
		t.Fatalf("StreamUpload on 429 = %v, want *RetryAfterError(17s)", err)
	}
	if got := srv.requestCount() - before; got != 1 {
		t.Fatalf("StreamUpload on 429 made %d requests, want 1 (streamed body must not retry internally)", got)
	}

	srv.enqueueFaults(fakeBoxFault{status: http.StatusServiceUnavailable})
	before = srv.requestCount()
	err = p.StreamUpload(ctx, "files", "/outage.txt", strings.NewReader("stream-body"), 11)
	if err == nil {
		t.Fatal("expected error on 503 during upload, got nil")
	}
	if got := srv.requestCount() - before; got != 1 {
		t.Fatalf("StreamUpload on 503 made %d requests, want 1 (streamed body must not retry internally)", got)
	}
}

func (f *fakeBoxServer) handleCreateUploadSession(w http.ResponseWriter, r *http.Request) {
	var reqBody struct {
		FolderID string `json:"folder_id"`
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil || reqBody.FolderID == "" || reqBody.FileName == "" || reqBody.FileSize <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_request"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	parent, ok := f.items[reqBody.FolderID]
	if !ok || parent.Type != "folder" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	for _, cid := range f.children[reqBody.FolderID] {
		existing := f.items[cid]
		if strings.EqualFold(existing.Name, reqBody.FileName) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			resp := map[string]any{
				"type":   "error",
				"status": 409,
				"code":   "item_name_in_use",
			}
			if !f.omitConflictContext {
				resp["context_info"] = map[string]any{
					"conflicts": []map[string]any{{
						"type": existing.Type,
						"id":   existing.ID,
						"name": existing.Name,
					}},
				}
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
	}

	f.createAndWriteSession(w, "", reqBody.FolderID, reqBody.FileName, reqBody.FileSize)
}

func (f *fakeBoxServer) handleCreateNewVersionUploadSession(w http.ResponseWriter, r *http.Request, fileID string) {
	var reqBody struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil || reqBody.FileSize <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_request"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	item, ok := f.items[fileID]
	if !ok || item.Type != "file" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","status":404,"code":"not_found"}`))
		return
	}
	name := reqBody.FileName
	if name == "" {
		name = item.Name
	}
	f.createAndWriteSession(w, fileID, item.ParentID, name, reqBody.FileSize)
}

func (f *fakeBoxServer) createAndWriteSession(w http.ResponseWriter, fileID, folderID, fileName string, fileSize int64) {
	partSize := f.chunkPartSize
	if partSize <= 0 {
		partSize = 8 * 1024 * 1024
	}
	f.nextID++
	sessionID := "sess-" + strconv.Itoa(f.nextID)
	f.sessions[sessionID] = &fakeBoxUploadSession{
		ID:       sessionID,
		FileID:   fileID,
		FolderID: folderID,
		FileName: fileName,
		FileSize: fileSize,
		PartSize: partSize,
		Parts:    make(map[int64][]byte),
	}
	totalParts := int((fileSize + partSize - 1) / partSize)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":          sessionID,
		"type":        "upload_session",
		"part_size":   partSize,
		"total_parts": totalParts,
		"session_endpoints": map[string]string{
			"upload_part": f.server.URL + "/api/2.0/files/upload_sessions/" + sessionID,
			"commit":      f.server.URL + "/api/2.0/files/upload_sessions/" + sessionID + "/commit",
			"abort":       f.server.URL + "/api/2.0/files/upload_sessions/" + sessionID,
		},
	})
}

func (f *fakeBoxServer) handleUploadPart(w http.ResponseWriter, r *http.Request, sessionID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	rangeHdr := r.Header.Get("Content-Range")
	if !strings.HasPrefix(rangeHdr, "bytes ") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"invalid_range"}`))
		return
	}
	rangeParts := strings.SplitN(strings.TrimPrefix(rangeHdr, "bytes "), "/", 2)
	if len(rangeParts) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	bounds := strings.SplitN(rangeParts[0], "-", 2)
	if len(bounds) != 2 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	start, err1 := strconv.ParseInt(bounds[0], 10, 64)
	end, err2 := strconv.ParseInt(bounds[1], 10, 64)
	total, err3 := strconv.ParseInt(rangeParts[1], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || start < 0 || end < start || end >= total || int64(len(body)) != end-start+1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sum := sha1.Sum(body)
	wantDigest := "sha=" + base64.StdEncoding.EncodeToString(sum[:])
	gotDigest := r.Header.Get("Digest")
	if gotDigest != wantDigest {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_digest"}`))
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	sess, ok := f.sessions[sessionID]
	if !ok || total != sess.FileSize {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if end+1 < total && int64(len(body)) != sess.PartSize {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"invalid_part_size"}`))
		return
	}
	sess.Parts[start] = append([]byte(nil), body...)
	f.partDigests = append(f.partDigests, gotDigest)

	sha1Hex := hex.EncodeToString(sum[:])
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"part": map[string]any{
			"part_id": fmt.Sprintf("P%d", start),
			"offset":  start,
			"size":    len(body),
			"sha1":    sha1Hex,
		},
	})
}

func (f *fakeBoxServer) handleCommitUploadSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var reqBody struct {
		Parts []struct {
			PartID string `json:"part_id"`
			Offset int64  `json:"offset"`
			Size   int64  `json:"size"`
			SHA1   string `json:"sha1"`
		} `json:"parts"`
		Attributes struct {
			ContentModifiedAt string `json:"content_modified_at"`
		} `json:"attributes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	sess, ok := f.sessions[sessionID]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if f.commitAcceptedRemaining > 0 {
		f.commitAcceptedRemaining--
		retryAfter := f.commitRetryAfter
		if retryAfter == "" {
			retryAfter = "0"
		}
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	assembled := make([]byte, 0, sess.FileSize)
	var expectedOffset int64
	for _, partMeta := range reqBody.Parts {
		if partMeta.Offset != expectedOffset {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		partBytes, exists := sess.Parts[partMeta.Offset]
		if !exists || int64(len(partBytes)) != partMeta.Size {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assembled = append(assembled, partBytes...)
		expectedOffset += partMeta.Size
	}
	if expectedOffset != sess.FileSize {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	wholeSum := sha1.Sum(assembled)
	wantDigest := "sha=" + base64.StdEncoding.EncodeToString(wholeSum[:])
	gotDigest := r.Header.Get("Digest")
	f.commitDigests = append(f.commitDigests, gotDigest)
	if gotDigest != wantDigest {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","status":400,"code":"bad_digest"}`))
		return
	}

	now := "2026-02-05T12:00:00Z"
	modAt := reqBody.Attributes.ContentModifiedAt
	if modAt == "" {
		modAt = now
	}
	sha1Hex := hex.EncodeToString(wholeSum[:])
	copied := append([]byte(nil), assembled...)
	ver := fakeBoxFileVersion{
		Size:              sess.FileSize,
		SHA1:              sha1Hex,
		Content:           append([]byte(nil), copied...),
		ModifiedAt:        now,
		ContentModifiedAt: modAt,
	}

	var item fakeBoxItem
	if sess.FileID != "" {
		existing, exists := f.items[sess.FileID]
		if !exists || existing.Type != "file" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		existing.Size = sess.FileSize
		existing.SHA1 = sha1Hex
		existing.Content = copied
		existing.ModifiedAt = now
		existing.ContentModifiedAt = modAt
		existing.Versions = append(existing.Versions, ver)
		f.items[sess.FileID] = existing
		item = existing
	} else {
		for _, cid := range f.children[sess.FolderID] {
			existing := f.items[cid]
			if strings.EqualFold(existing.Name, sess.FileName) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"type":"error","status":409,"code":"item_name_in_use"}`))
				return
			}
		}

		f.nextID++
		id := strconv.Itoa(f.nextID)
		item = fakeBoxItem{
			ID:                id,
			Type:              "file",
			Name:              sess.FileName,
			ParentID:          sess.FolderID,
			Size:              sess.FileSize,
			SHA1:              sha1Hex,
			Content:           copied,
			ModifiedAt:        now,
			ContentModifiedAt: modAt,
			Versions:          []fakeBoxFileVersion{ver},
		}
		f.items[id] = item
		f.children[sess.FolderID] = append(f.children[sess.FolderID], id)
	}
	delete(f.sessions, sessionID)
	writeFakeBoxUploadEntry(w, http.StatusCreated, item)
}

func (f *fakeBoxServer) handleAbortUploadSession(w http.ResponseWriter, _ *http.Request, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	delete(f.sessions, sessionID)
	f.abortedSessions = append(f.abortedSessions, sessionID)
	w.WriteHeader(http.StatusNoContent)
}

func TestBoxUploadThreshold50MBSingleVsChunked(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	p := srv.provider()
	defer p.Close()

	ctx := context.Background()

	// Exactly 50,000,000 bytes (50 MB decimal) must use single-request upload.
	exact50MB := bytes.Repeat([]byte("A"), 50_000_000)
	sum50 := sha1.Sum(exact50MB)
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/exact-50mb.bin", bytes.NewReader(exact50MB), int64(len(exact50MB)), UploadOptions{
		Checksum: "SHA1:" + hex.EncodeToString(sum50[:]),
	}); err != nil {
		t.Fatalf("StreamUploadWithOptions(50MB): %v", err)
	}

	// 50,000,001 bytes must delegate to chunked upload session even through StreamUploadWithOptions.
	over50MB := bytes.Repeat([]byte("B"), 50_000_001)
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/over-50mb.bin", bytes.NewReader(over50MB), int64(len(over50MB)), UploadOptions{}); err != nil {
		t.Fatalf("StreamUploadWithOptions(50MB+1): %v", err)
	}

	srv.mu.Lock()
	reqs := append([]string(nil), srv.requests...)
	srv.mu.Unlock()

	var singlePosts, sessionCreates, sessionCommits int
	for _, r := range reqs {
		switch {
		case r == "POST /api/2.0/files/content":
			singlePosts++
		case r == "POST /api/2.0/files/upload_sessions":
			sessionCreates++
		case strings.HasPrefix(r, "POST /api/2.0/files/upload_sessions/") && strings.HasSuffix(r, "/commit"):
			sessionCommits++
		}
	}
	if singlePosts != 1 || sessionCreates != 1 || sessionCommits != 1 {
		t.Fatalf("upload path counts = (single=%d, sessionCreate=%d, commit=%d), want (1, 1, 1)", singlePosts, sessionCreates, sessionCommits)
	}
}

func TestBoxStreamUploadChunkedCommit202MtimeAndProgress(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.chunkPartSize = 1024
	srv.commitAcceptedRemaining = 2
	srv.commitRetryAfter = "0"

	p := srv.provider()
	defer p.Close()

	payload := bytes.Repeat([]byte("chunk-data-"), 250) // 2750 bytes -> 3 parts (1024, 1024, 702)
	wholeSum := sha1.Sum(payload)
	wantSHA1 := "SHA1:" + hex.EncodeToString(wholeSum[:])
	wantDigest := "sha=" + base64.StdEncoding.EncodeToString(wholeSum[:])
	wantMtime := time.Date(2025, 9, 12, 14, 15, 16, 0, time.UTC)

	// Buffer size 1 intentionally tests that a full progress channel never stalls the upload.
	progressCh := make(chan int64, 1)

	ctx := context.Background()
	res, err := p.StreamUploadChunkedWithOptions(ctx, "files", "/Archives/big.bin", bytes.NewReader(payload), int64(len(payload)), progressCh, UploadOptions{
		Metadata: FileMetadata{ModifiedTime: wantMtime},
	})
	if err != nil {
		t.Fatalf("StreamUploadChunkedWithOptions: %v", err)
	}
	if res.Receipt.TargetResourceID == "" {
		t.Fatal("expected TargetResourceID in chunked UploadResult")
	}

	srv.mu.Lock()
	partDigests := append([]string(nil), srv.partDigests...)
	commitDigests := append([]string(nil), srv.commitDigests...)
	remaining202 := srv.commitAcceptedRemaining
	srv.mu.Unlock()

	if len(partDigests) != 3 {
		t.Fatalf("partDigests count = %d, want 3", len(partDigests))
	}
	if len(commitDigests) != 1 || commitDigests[0] != wantDigest {
		t.Fatalf("commitDigests = %v, want [%s]", commitDigests, wantDigest)
	}
	if remaining202 != 0 {
		t.Fatalf("commitAcceptedRemaining = %d, want 0", remaining202)
	}

	inspected, err := p.InspectResource(ctx, "files", "/Archives/big.bin")
	if err != nil {
		t.Fatalf("InspectResource: %v", err)
	}
	if inspected.Hash != wantSHA1 || inspected.Size != int64(len(payload)) {
		t.Fatalf("inspected = %+v, want size=%d hash=%s", inspected, len(payload), wantSHA1)
	}
	if !inspected.LastModified.Equal(wantMtime) {
		t.Fatalf("LastModified = %v, want %v", inspected.LastModified, wantMtime)
	}
}

func TestBoxStreamUploadChunkedMidwayFailureAbortsAndRestartsCleanly(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.chunkPartSize = 1024

	// Fail on the 2nd PUT part with a non-retryable 400 (simulating unrecoverable midway failure).
	var putCount int
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			putCount++
			if putCount == 2 {
				return &fakeBoxFault{status: http.StatusBadRequest, body: `{"type":"error","status":400,"code":"bad_request"}`}
			}
		}
		return nil
	}

	p := srv.provider()
	defer p.Close()

	payload := bytes.Repeat([]byte("Z"), 2500) // 3 parts
	ctx := context.Background()

	if err := p.StreamUploadChunked(ctx, "files", "/Large/video.mp4", bytes.NewReader(payload), int64(len(payload)), nil); err == nil {
		t.Fatal("expected chunked upload failure on part 2, got nil")
	}

	srv.mu.Lock()
	aborted := append([]string(nil), srv.abortedSessions...)
	activeSessions := len(srv.sessions)
	srv.faultHook = nil
	srv.mu.Unlock()

	if len(aborted) != 1 || activeSessions != 0 {
		t.Fatalf("abortedSessions=%v activeSessions=%d, want 1 aborted and 0 active", aborted, activeSessions)
	}
	exists, _, err := p.FileExists(ctx, "files", "/Large/video.mp4")
	if err != nil || exists {
		t.Fatalf("FileExists after aborted chunked upload = (%v, %v), want (false, nil)", exists, err)
	}

	// Retry restarts the whole file from offset 0 and commits cleanly.
	if err := p.StreamUploadChunked(ctx, "files", "/Large/video.mp4", bytes.NewReader(payload), int64(len(payload)), nil); err != nil {
		t.Fatalf("retry StreamUploadChunked: %v", err)
	}
	dl, err := p.StreamDownload(ctx, "files", "/Large/video.mp4")
	if err != nil {
		t.Fatalf("StreamDownload after retry: %v", err)
	}
	got, _ := io.ReadAll(dl)
	dl.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(payload))
	}
}

func TestBoxStreamUploadChunkedRetriesSessionCreateAndCommitOn429And5xx(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.chunkPartSize = 1024

	var createAttempts, commitAttempts int
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/upload_sessions" {
			createAttempts++
			if createAttempts == 1 {
				return &fakeBoxFault{status: http.StatusTooManyRequests, retryAfter: "0"}
			}
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commit") {
			commitAttempts++
			if commitAttempts == 1 {
				return &fakeBoxFault{status: http.StatusServiceUnavailable}
			}
		}
		return nil
	}

	p := srv.provider()
	defer p.Close()

	payload := bytes.Repeat([]byte("R"), 1500)
	if err := p.StreamUploadChunked(context.Background(), "files", "/retry.bin", bytes.NewReader(payload), int64(len(payload)), nil); err != nil {
		t.Fatalf("StreamUploadChunked with transient 429/503 on JSON endpoints: %v", err)
	}
	if createAttempts != 2 || commitAttempts != 2 {
		t.Fatalf("attempts = (create=%d, commit=%d), want (2, 2)", createAttempts, commitAttempts)
	}
}

type failingAfterNReader struct {
	data []byte
	fail error
	pos  int
	cap  int
}

func (r *failingAfterNReader) Read(p []byte) (int, error) {
	if r.pos >= r.cap {
		return 0, r.fail
	}
	limit := r.cap - r.pos
	if len(p) < limit {
		limit = len(p)
	}
	n := copy(p[:limit], r.data[r.pos:r.pos+limit])
	r.pos += n
	return n, nil
}

func TestBoxStreamUploadChunkedStreamErrorAnd5xxExhaustionAbortSession(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.chunkPartSize = 1024

	p := srv.provider()
	defer p.Close()
	ctx := context.Background()

	// 1. Source stream fails midway -> session must be aborted, no file committed.
	srcErr := errors.New("source stream dropped")
	failingReader := &failingAfterNReader{
		data: bytes.Repeat([]byte("S"), 2500),
		cap:  1200,
		fail: srcErr,
	}
	err := p.StreamUploadChunked(ctx, "files", "/stream-drop.bin", failingReader, 2500, nil)
	if !errors.Is(err, srcErr) {
		t.Fatalf("StreamUploadChunked(stream drop) = %v, want %v", err, srcErr)
	}
	srv.mu.Lock()
	abortedCount := len(srv.abortedSessions)
	activeCount := len(srv.sessions)
	srv.mu.Unlock()
	if abortedCount != 1 || activeCount != 0 {
		t.Fatalf("after stream drop: aborted=%d active=%d, want (1, 0)", abortedCount, activeCount)
	}

	// 2. Part PUT hits 4 consecutive 503s -> exhausts internal retries, aborts session, leaves no file.
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			return &fakeBoxFault{status: http.StatusServiceUnavailable}
		}
		return nil
	}
	payload := bytes.Repeat([]byte("F"), 2500)
	if err := p.StreamUploadChunked(ctx, "files", "/part-503.bin", bytes.NewReader(payload), int64(len(payload)), nil); err == nil {
		t.Fatal("expected error when part PUT hits persistent 503")
	}
	srv.mu.Lock()
	abortedCount = len(srv.abortedSessions)
	activeCount = len(srv.sessions)
	srv.faultHook = nil
	srv.mu.Unlock()
	if abortedCount != 2 || activeCount != 0 {
		t.Fatalf("after part 503 exhaustion: aborted=%d active=%d, want (2, 0)", abortedCount, activeCount)
	}
	if exists, _, _ := p.FileExists(ctx, "files", "/part-503.bin"); exists {
		t.Fatal("aborted 503 upload left a file in target")
	}
}

type trackingReader struct {
	r    io.Reader
	read bool
}

func (t *trackingReader) Read(p []byte) (int, error) {
	t.read = true
	return t.r.Read(p)
}

func TestBoxStreamUploadOverwriteCreatesNewVersionAndPreservesOnFailure(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "200", "Docs")
	v1Payload := []byte("version-1-original-content")
	v1Sum := sha1.Sum(v1Payload)
	v1Hex := hex.EncodeToString(v1Sum[:])
	fileID := srv.addFileContent(docsID, "501", "report.txt", v1Payload)

	p := srv.provider()
	defer p.Close()

	if p.SupportsAtomicRename() {
		t.Fatal("BoxProvider.SupportsAtomicRename() = true, want false")
	}

	ctx := context.Background()

	// 1. Overwrite=false must reject existing file with ErrManagerConflict and leave v1 intact.
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/report.txt", strings.NewReader("blocked"), 7, UploadOptions{Overwrite: false}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("StreamUploadWithOptions(Overwrite=false) = %v, want ErrManagerConflict", err)
	}

	// 2. Overwrite=true on a folder name must fail with ErrManagerConflict without consuming stream.
	srv.addFolder(docsID, "201", "FolderCollision")
	folderStream := &trackingReader{r: strings.NewReader("never-read")}
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/FolderCollision", folderStream, 10, UploadOptions{Overwrite: true}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("StreamUploadWithOptions(Overwrite=true on folder) = %v, want ErrManagerConflict", err)
	}
	if folderStream.read {
		t.Fatal("stream was consumed even though target resolved to a folder")
	}

	// 3. Overwrite=true uploads a new version to POST /files/{id}/content with SHA-1 and mtime.
	v2Payload := []byte("version-2-updated-box-payload")
	v2Sum := sha1.Sum(v2Payload)
	v2Hex := hex.EncodeToString(v2Sum[:])
	v2Mtime := time.Date(2026, 6, 15, 10, 20, 30, 0, time.UTC)

	res, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/report.txt", bytes.NewReader(v2Payload), int64(len(v2Payload)), UploadOptions{
		Metadata:  FileMetadata{ModifiedTime: v2Mtime},
		Checksum:  "SHA1:" + v2Hex,
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("StreamUploadWithOptions(Overwrite=true): %v", err)
	}
	if res.Receipt.TargetResourceID != fileID {
		t.Fatalf("TargetResourceID = %q, want %q", res.Receipt.TargetResourceID, fileID)
	}

	inspected, err := p.InspectResource(ctx, "files", "/Docs/report.txt")
	if err != nil {
		t.Fatalf("InspectResource after overwrite: %v", err)
	}
	if inspected.Size != int64(len(v2Payload)) || inspected.Hash != "SHA1:"+v2Hex || !inspected.LastModified.Equal(v2Mtime) {
		t.Fatalf("inspected = %+v, want size=%d hash=SHA1:%s mtime=%v", inspected, len(v2Payload), v2Hex, v2Mtime)
	}

	dl, err := p.StreamDownload(ctx, "files", "/Docs/report.txt")
	if err != nil {
		t.Fatalf("StreamDownload after overwrite: %v", err)
	}
	gotV2, _ := io.ReadAll(dl)
	dl.Close()
	if !bytes.Equal(gotV2, v2Payload) {
		t.Fatalf("downloaded = %q, want %q", gotV2, v2Payload)
	}

	srv.mu.Lock()
	versions := append([]fakeBoxFileVersion(nil), srv.items[fileID].Versions...)
	srv.mu.Unlock()
	if len(versions) != 2 {
		t.Fatalf("version count = %d, want 2", len(versions))
	}
	if !bytes.Equal(versions[0].Content, v1Payload) || versions[0].SHA1 != v1Hex {
		t.Fatalf("version 1 corrupted: %+v", versions[0])
	}
	if !bytes.Equal(versions[1].Content, v2Payload) || versions[1].SHA1 != v2Hex {
		t.Fatalf("version 2 mismatch: %+v", versions[1])
	}

	// 4. Failed overwrite (503 during POST /files/{id}/content or stream failure) must leave v2 intact and not add a version.
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPost && r.URL.Path == "/api/2.0/files/"+fileID+"/content" {
			return &fakeBoxFault{status: http.StatusServiceUnavailable}
		}
		return nil
	}
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/report.txt", strings.NewReader("v3-fail"), 7, UploadOptions{Overwrite: true}); err == nil {
		t.Fatal("expected error when new-version upload hits 503")
	}
	srv.faultHook = nil
	if _, err := p.StreamUploadWithOptions(ctx, "files", "/Docs/report.txt", &failingAfterNReader{data: []byte("v3-truncated"), cap: 4, fail: errors.New("stream drop")}, 12, UploadOptions{Overwrite: true}); err == nil {
		t.Fatal("expected error when new-version upload stream drops")
	}
	srv.mu.Lock()
	versionsAfterFail := append([]fakeBoxFileVersion(nil), srv.items[fileID].Versions...)
	itemAfterFail := srv.items[fileID]
	srv.mu.Unlock()
	if len(versionsAfterFail) != 2 || !bytes.Equal(itemAfterFail.Content, v2Payload) || itemAfterFail.SHA1 != v2Hex {
		t.Fatalf("failed overwrite mutated existing version: count=%d sha1=%s", len(versionsAfterFail), itemAfterFail.SHA1)
	}
}

func TestBoxStreamUploadChunkedOverwriteCreatesNewVersionAndAbortsOnFailure(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()
	srv.chunkPartSize = 1024

	archivesID := srv.addFolder("0", "300", "Archives")
	v1Payload := bytes.Repeat([]byte("V1"), 1200) // 2400 bytes
	v1Sum := sha1.Sum(v1Payload)
	v1Hex := hex.EncodeToString(v1Sum[:])
	fileID := srv.addFileContent(archivesID, "701", "big.bin", v1Payload)

	p := srv.provider()
	defer p.Close()

	ctx := context.Background()

	// 1. Overwrite=false must fail with ErrManagerConflict without consuming stream or creating a session.
	noOverwriteStream := &trackingReader{r: bytes.NewReader([]byte("unused"))}
	if _, err := p.StreamUploadChunkedWithOptions(ctx, "files", "/Archives/big.bin", noOverwriteStream, 6, nil, UploadOptions{Overwrite: false}); !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("StreamUploadChunkedWithOptions(Overwrite=false) = %v, want ErrManagerConflict", err)
	}
	if noOverwriteStream.read {
		t.Fatal("stream was consumed when chunked session creation returned 409 without Overwrite")
	}

	// 2. Midway failure during Overwrite=true chunked upload must abort the session and leave v1 untouched.
	var putCount int
	srv.faultHook = func(r *http.Request) *fakeBoxFault {
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/2.0/files/upload_sessions/") {
			putCount++
			if putCount == 2 {
				return &fakeBoxFault{status: http.StatusBadRequest, body: `{"type":"error","status":400,"code":"bad_request"}`}
			}
		}
		return nil
	}
	v2Payload := bytes.Repeat([]byte("V2-NEW-"), 400) // 2800 bytes -> 3 parts (1024, 1024, 752)
	v2Sum := sha1.Sum(v2Payload)
	v2Hex := hex.EncodeToString(v2Sum[:])
	v2Mtime := time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC)

	if _, err := p.StreamUploadChunkedWithOptions(ctx, "files", "/Archives/big.bin", bytes.NewReader(v2Payload), int64(len(v2Payload)), nil, UploadOptions{
		Metadata:  FileMetadata{ModifiedTime: v2Mtime},
		Overwrite: true,
	}); err == nil {
		t.Fatal("expected chunked overwrite failure on part 2, got nil")
	}

	srv.mu.Lock()
	abortedCount := len(srv.abortedSessions)
	activeCount := len(srv.sessions)
	versionsAfterFail := append([]fakeBoxFileVersion(nil), srv.items[fileID].Versions...)
	itemAfterFail := srv.items[fileID]
	srv.faultHook = nil
	srv.mu.Unlock()

	if abortedCount != 1 || activeCount != 0 {
		t.Fatalf("after failed chunked overwrite: aborted=%d active=%d, want (1, 0)", abortedCount, activeCount)
	}
	if len(versionsAfterFail) != 1 || !bytes.Equal(itemAfterFail.Content, v1Payload) || itemAfterFail.SHA1 != v1Hex {
		t.Fatalf("failed chunked overwrite corrupted v1: versions=%d sha1=%s", len(versionsAfterFail), itemAfterFail.SHA1)
	}

	// 3. Successful Overwrite=true creates v2 via POST /files/{file_id}/upload_sessions and preserves v1 in history.
	res, err := p.StreamUploadChunkedWithOptions(ctx, "files", "/Archives/big.bin", bytes.NewReader(v2Payload), int64(len(v2Payload)), nil, UploadOptions{
		Metadata:  FileMetadata{ModifiedTime: v2Mtime},
		Checksum:  "SHA1:" + v2Hex,
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("StreamUploadChunkedWithOptions(Overwrite=true): %v", err)
	}
	if res.Receipt.TargetResourceID != fileID {
		t.Fatalf("TargetResourceID = %q, want %q", res.Receipt.TargetResourceID, fileID)
	}

	inspected, err := p.InspectResource(ctx, "files", "/Archives/big.bin")
	if err != nil {
		t.Fatalf("InspectResource: %v", err)
	}
	if inspected.Size != int64(len(v2Payload)) || inspected.Hash != "SHA1:"+v2Hex || !inspected.LastModified.Equal(v2Mtime) {
		t.Fatalf("inspected = %+v, want size=%d hash=SHA1:%s mtime=%v", inspected, len(v2Payload), v2Hex, v2Mtime)
	}
	dl, err := p.StreamDownload(ctx, "files", "/Archives/big.bin")
	if err != nil {
		t.Fatalf("StreamDownload: %v", err)
	}
	gotV2, _ := io.ReadAll(dl)
	dl.Close()
	if !bytes.Equal(gotV2, v2Payload) {
		t.Fatalf("downloaded %d bytes, want %d matching v2", len(gotV2), len(v2Payload))
	}

	srv.mu.Lock()
	versions := append([]fakeBoxFileVersion(nil), srv.items[fileID].Versions...)
	srv.omitConflictContext = true
	srv.mu.Unlock()
	if len(versions) != 2 || !bytes.Equal(versions[0].Content, v1Payload) || !bytes.Equal(versions[1].Content, v2Payload) {
		t.Fatalf("version history mismatch: len=%d", len(versions))
	}

	// 4. Even when 409 omits context_info.conflicts, fallback lookup resolves file ID and overwrites via > 50MB StreamUploadWithOptions.
	srv.chunkPartSize = 16 * 1024 * 1024
	srv.addFileContent(archivesID, "702", "huge.bin", []byte("huge-v1"))
	hugeV2 := bytes.Repeat([]byte("H"), 50_000_001)
	hugeSum := sha1.Sum(hugeV2)
	hugeHex := hex.EncodeToString(hugeSum[:])
	hugeMtime := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

	resHuge, err := p.StreamUploadWithOptions(ctx, "files", "/Archives/huge.bin", bytes.NewReader(hugeV2), int64(len(hugeV2)), UploadOptions{
		Metadata:  FileMetadata{ModifiedTime: hugeMtime},
		Checksum:  "SHA1:" + hugeHex,
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("StreamUploadWithOptions(>50MB Overwrite=true): %v", err)
	}
	if resHuge.Receipt.TargetResourceID != "702" {
		t.Fatalf("huge TargetResourceID = %q, want 702", resHuge.Receipt.TargetResourceID)
	}
	srv.mu.Lock()
	hugeVersions := len(srv.items["702"].Versions)
	hugeStoredHash := srv.items["702"].SHA1
	srv.mu.Unlock()
	if hugeVersions != 2 || hugeStoredHash != hugeHex {
		t.Fatalf("huge.bin after overwrite: versions=%d sha1=%s, want 2 and %s", hugeVersions, hugeStoredHash, hugeHex)
	}
}
