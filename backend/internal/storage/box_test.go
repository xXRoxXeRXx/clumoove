package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBoxItem struct {
	ID                string
	Type              string // "folder" or "file"
	Name              string
	ParentID          string
	Size              int64
	SHA1              string
	ModifiedAt        string
	ContentModifiedAt string
	URL               string
}

type fakeBoxFault struct {
	status     int
	retryAfter string
	body       string
}

type fakeBoxServer struct {
	server *httptest.Server
	token  string

	mu          sync.Mutex
	items       map[string]fakeBoxItem
	children    map[string][]string
	faults      []fakeBoxFault
	requests    []string
	nextID      int
	maxPageSize int
}

func newFakeBoxServer(token string) *fakeBoxServer {
	f := &fakeBoxServer{
		token:    token,
		items:    make(map[string]fakeBoxItem),
		children: make(map[string][]string),
		nextID:   100,
	}
	f.items["0"] = fakeBoxItem{ID: "0", Type: "folder", Name: "All Files"}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeBoxServer) close() {
	f.server.Close()
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
		w.WriteHeader(fault.status)
		if fault.body != "" {
			_, _ = w.Write([]byte(fault.body))
		}
		return
	}
	f.mu.Unlock()

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
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/folders/"):
		folderID := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
		f.handleGetItem(w, folderID, "folder")
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/2.0/files/"):
		fileID := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
		f.handleGetItem(w, fileID, "file")
	default:
		http.NotFound(w, r)
	}
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
