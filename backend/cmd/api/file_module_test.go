package main

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
)

func TestFileModuleRegistersAllRoutesWithExpectedAccessControls(t *testing.T) {
	api := newFileAPI(fileDependencies{
		encryptionKey: "01234567890123456789012345678901",
		rateLimiter:   allowAllFileRateLimiter{},
	})

	mux := http.NewServeMux()
	authCalls := 0
	api.registerRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authCalls++
			next.ServeHTTP(w, r)
		})
	})

	protectedRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/files/profiles/p1/capabilities"},
		{http.MethodPost, "/api/files/profiles/p1/entries:list"},
		{http.MethodPost, "/api/files/profiles/p1/entries:resolve"},
		{http.MethodDelete, "/api/files/profiles/p1/entries"},
		{http.MethodPost, "/api/files/profiles/p1/entries:rename"},
		{http.MethodPost, "/api/files/profiles/p1/entries:copy"},
		{http.MethodPost, "/api/files/profiles/p1/entries:move"},
		{http.MethodPut, "/api/files/profiles/p1/content"},
		{http.MethodPost, "/api/files/profiles/p1/directories"},
		{http.MethodPost, "/api/files/profiles/p1/download-tickets"},
		{http.MethodPost, "/api/files/profiles/p1/archive-tickets"},
		{http.MethodPost, "/api/files/profiles/p1/entries:batch-delete"},
		{http.MethodPost, "/api/files/profiles/p1/entries:batch-copy"},
		{http.MethodPost, "/api/files/profiles/p1/entries:batch-move"},
		{http.MethodPost, "/api/files/profiles/p1/transfers"},
		{http.MethodGet, "/api/files/profiles/p1/transfers"},
		{http.MethodPost, "/api/files/transfers/t1/cancel"},
		{http.MethodPost, "/api/files/profiles/p1/thumbnail"},
	}

	for _, route := range protectedRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			authCalls = 0
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))

			assertAPIResponseError(t, rec, http.StatusUnauthorized, ErrUnauthorized, route.method+" "+route.path)
			if authCalls != 1 {
				t.Fatalf("auth middleware calls = %d, want 1", authCalls)
			}
		})
	}

	publicRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/files/download/missing-ticket"},
		{http.MethodGet, "/api/files/archive/missing-ticket"},
	}

	for _, route := range publicRoutes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			authCalls = 0
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))

			assertAPIResponseError(t, rec, http.StatusNotFound, ErrFilesDownloadTicketInvalid, route.method+" "+route.path)
			if authCalls != 0 {
				t.Fatalf("auth middleware calls = %d, want 0 on public ticket route", authCalls)
			}
		})
	}
}

func TestFileModuleRejectsRequestsWhenRateLimiterIsMissing(t *testing.T) {
	api := newFileAPI(fileDependencies{
		encryptionKey: "01234567890123456789012345678901",
	})
	mux := http.NewServeMux()
	api.registerRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
		})
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files/download/missing-ticket", nil))
	assertAPIResponseError(t, rec, http.StatusTooManyRequests, ErrRateLimited, "public download without rate limiter")
}

type memoryFileSessionStore struct {
	mu      sync.Mutex
	tickets map[string][]byte
	streams map[string]map[string]struct{}
}

func newMemoryFileSessionStore() *memoryFileSessionStore {
	return &memoryFileSessionStore{
		tickets: make(map[string][]byte),
		streams: make(map[string]map[string]struct{}),
	}
}

func (s *memoryFileSessionStore) SetTicket(_ context.Context, key string, payload []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[key] = append([]byte(nil), payload...)
	return nil
}

func (s *memoryFileSessionStore) ConsumeTicket(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, ok := s.tickets[key]
	if !ok {
		return nil, errors.New("ticket not found")
	}
	delete(s.tickets, key)
	return payload, nil
}

func (s *memoryFileSessionStore) AcquireStream(_ context.Context, userID, kind, streamID string, _ time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := userID + ":" + kind
	set := s.streams[k]
	if set == nil {
		set = make(map[string]struct{})
		s.streams[k] = set
	}
	if len(set) >= fileMaxStreamsPerUser {
		return false
	}
	set[streamID] = struct{}{}
	return true
}

func (s *memoryFileSessionStore) RenewStream(_ context.Context, userID, kind, streamID string, _ time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.streams[userID+":"+kind]
	if set == nil {
		return false
	}
	_, ok := set[streamID]
	return ok
}

func (s *memoryFileSessionStore) ReleaseStream(_ context.Context, userID, kind, streamID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if set := s.streams[userID+":"+kind]; set != nil {
		delete(set, streamID)
	}
}

func (s *memoryFileSessionStore) activeStreamCount(userID, kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams[userID+":"+kind])
}

type featureFileProvider struct {
	*legacyFileManagerTestProvider
	createdDirs   []string
	uploadedNames []string
	copied        [][2]string
	thumbnailErr  error
}

func (p *featureFileProvider) CreateDirectory(_ context.Context, _ string, dirPath string) error {
	p.createdDirs = append(p.createdDirs, dirPath)
	return nil
}

func (p *featureFileProvider) StreamUpload(_ context.Context, _ string, filePath string, stream io.Reader, _ int64) error {
	p.uploadedNames = append(p.uploadedNames, filePath)
	_, err := io.Copy(io.Discard, stream)
	return err
}

func (p *featureFileProvider) CopyFile(_ context.Context, _ string, src, dst string) error {
	p.copied = append(p.copied, [2]string{src, dst})
	return nil
}

func (p *featureFileProvider) ThumbnailManager(_ context.Context, _ storage.ManagerLocator, _, _ int) (io.ReadCloser, string, error) {
	if p.thumbnailErr != nil {
		return nil, "", p.thumbnailErr
	}
	return io.NopCloser(strings.NewReader("jpeg-bytes")), "image/jpeg", nil
}

type scopedRateLimiter struct {
	blocked map[string]bool
}

func (l *scopedRateLimiter) Allow(_ context.Context, scope, _ string, _ int, _ time.Duration) bool {
	return !l.blocked[scope]
}

func headerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-Test-User")
		if userID == "" {
			writeError(w, http.StatusUnauthorized, ErrUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{UserID: userID, Role: "USER"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestFileModuleHTTPWorkflowsAndErrorSemantics(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	database, _ := newFileModuleTestDB(t, map[string][]driver.Value{
		"p-nc":    profileTestRow("p-nc", "user-1", "nextcloud", "https://nc.example.test", "alice", encSecret),
		"p-other": profileTestRow("p-other", "user-2", "nextcloud", "https://nc2.example.test", "bob", encSecret),
	})

	provider := &featureFileProvider{
		legacyFileManagerTestProvider: &legacyFileManagerTestProvider{
			downloadContent: "hello-clumoove",
			listings: map[string][]storage.CloudResource{
				"/": {
					{Path: "/docs", Name: "docs", IsDir: true},
					{Path: "/alpha.txt", Name: "alpha.txt", Size: 14},
					{Path: "/photo.jpg", Name: "photo.jpg", Size: 20},
				},
				"/docs": {
					{Path: "/docs/existing.txt", Name: "existing.txt", Size: 5},
				},
			},
		},
	}

	sessions := newMemoryFileSessionStore()
	limiter := &scopedRateLimiter{blocked: map[string]bool{}}
	var audits []db.AuditAction

	api := newFileAPI(fileDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   limiter,
		profiles: &ownedProfileAccess{
			db:            database,
			encryptionKey: key,
			providerFactory: func(context.Context, string, string, string, string, ...storage.ProviderOption) (profileProvider, error) {
				return provider, nil
			},
		},
		sessionStore: sessions,
		writeAudit: func(_ *http.Request, action db.AuditAction, _, _ string, _ map[string]interface{}) {
			audits = append(audits, action)
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	doReq := func(method, target, userID, body string, headers map[string]string) *httptest.ResponseRecorder {
		var bodyReader io.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, target, bodyReader)
		if userID != "" {
			req.Header.Set("X-Test-User", userID)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. Capabilities and anti-enumeration cross-user profile check.
	recCaps := doReq(http.MethodGet, "/api/files/profiles/p-nc/capabilities", "user-1", "", nil)
	if recCaps.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d, want 200 (body=%s)", recCaps.Code, recCaps.Body.String())
	}
	if strings.Contains(recCaps.Body.String(), "saved-secret") || strings.Contains(recCaps.Body.String(), "nc.example.test") {
		t.Fatalf("capabilities leaked sensitive profile fields: %s", recCaps.Body.String())
	}

	recCrossUser := doReq(http.MethodGet, "/api/files/profiles/p-other/capabilities", "user-1", "", nil)
	assertAPIResponseError(t, recCrossUser, http.StatusNotFound, ErrProfileNotFound, "cross-user capabilities")

	// 2. Paginated list & path resolution through HTTP.
	recList := doReq(http.MethodPost, "/api/files/profiles/p-nc/entries:list", "user-1", `{"resource_type":"files","limit":2}`, nil)
	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recList.Code, recList.Body.String())
	}
	var listResp struct {
		Entries    []fileEntry `json:"entries"`
		NextCursor string      `json:"next_cursor"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(listResp.Entries) != 2 || listResp.NextCursor == "" {
		t.Fatalf("unexpected page 1: %+v", listResp)
	}
	alphaRef := ""
	photoRef := ""
	docsRef := ""
	for _, e := range listResp.Entries {
		switch e.Name {
		case "alpha.txt":
			alphaRef = e.Ref
		case "docs":
			docsRef = e.Ref
		}
	}

	recListPage2 := doReq(http.MethodPost, "/api/files/profiles/p-nc/entries:list", "user-1", `{"resource_type":"files","limit":2,"cursor":"`+listResp.NextCursor+`"}`, nil)
	if recListPage2.Code != http.StatusOK {
		t.Fatalf("list page 2 status = %d, body = %s", recListPage2.Code, recListPage2.Body.String())
	}
	var listPage2 struct {
		Entries []fileEntry `json:"entries"`
	}
	_ = json.Unmarshal(recListPage2.Body.Bytes(), &listPage2)
	if len(listPage2.Entries) != 1 || listPage2.Entries[0].Name != "photo.jpg" {
		t.Fatalf("unexpected page 2: %+v", listPage2)
	}
	photoRef = listPage2.Entries[0].Ref

	recResolve := doReq(http.MethodPost, "/api/files/profiles/p-nc/entries:resolve", "user-1", `{"resource_type":"files","path":"/docs"}`, nil)
	if recResolve.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, body = %s", recResolve.Code, recResolve.Body.String())
	}

	// 3. Directory creation & file upload (plus size mismatch check).
	recMkdir := doReq(http.MethodPost, "/api/files/profiles/p-nc/directories", "user-1", `{"parent_ref":"`+docsRef+`","name":"reports"}`, nil)
	if recMkdir.Code != http.StatusCreated {
		t.Fatalf("mkdir status = %d, body = %s", recMkdir.Code, recMkdir.Body.String())
	}

	encodedName := base64.RawURLEncoding.EncodeToString([]byte("notes.txt"))
	recUpload := doReq(http.MethodPut, "/api/files/profiles/p-nc/content", "user-1", "upload-data", map[string]string{
		"X-Clumoove-File-Name": encodedName,
		"X-Clumoove-Parent-Ref": docsRef,
	})
	if recUpload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", recUpload.Code, recUpload.Body.String())
	}

	// Size mismatch: declare Content-Length larger than actual body.
	mismatchReq := httptest.NewRequest(http.MethodPut, "/api/files/profiles/p-nc/content", strings.NewReader("short"))
	mismatchReq.ContentLength = 999
	mismatchReq.Header.Set("X-Test-User", "user-1")
	mismatchReq.Header.Set("X-Clumoove-File-Name", encodedName)
	recMismatch := httptest.NewRecorder()
	mux.ServeHTTP(recMismatch, mismatchReq)
	assertAPIResponseError(t, recMismatch, http.StatusBadRequest, ErrFilesUploadSizeMismatch, "upload size mismatch")

	// 4. Download ticket creation, single-use public consumption, replay rejection, and stream cap.
	recTicket := doReq(http.MethodPost, "/api/files/profiles/p-nc/download-tickets", "user-1", `{"ref":"`+alphaRef+`"}`, nil)
	if recTicket.Code != http.StatusCreated {
		t.Fatalf("download ticket status = %d, body = %s", recTicket.Code, recTicket.Body.String())
	}
	var ticketResp struct {
		DownloadURL string `json:"download_url"`
	}
	_ = json.Unmarshal(recTicket.Body.Bytes(), &ticketResp)
	if !strings.HasPrefix(ticketResp.DownloadURL, "/api/files/download/") {
		t.Fatalf("unexpected download_url: %q", ticketResp.DownloadURL)
	}

	// Public consumer: no X-Test-User header!
	recDownload := doReq(http.MethodGet, ticketResp.DownloadURL, "", "", nil)
	if recDownload.Code != http.StatusOK || recDownload.Body.String() != "hello-clumoove" {
		t.Fatalf("download status = %d, body = %q", recDownload.Code, recDownload.Body.String())
	}
	if got := recDownload.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if sessions.activeStreamCount("user-1", "download") != 0 {
		t.Fatalf("expected download stream lease released after download")
	}

	// Single-use replay must fail with 404.
	recReplay := doReq(http.MethodGet, ticketResp.DownloadURL, "", "", nil)
	assertAPIResponseError(t, recReplay, http.StatusNotFound, ErrFilesDownloadTicketInvalid, "download ticket replay")

	// Stream cap enforcement.
	for i := 0; i < fileMaxStreamsPerUser; i++ {
		r := doReq(http.MethodPost, "/api/files/profiles/p-nc/download-tickets", "user-1", `{"ref":"`+alphaRef+`"}`, nil)
		if r.Code != http.StatusCreated {
			t.Fatalf("ticket %d status = %d, want 201", i, r.Code)
		}
	}
	recCap := doReq(http.MethodPost, "/api/files/profiles/p-nc/download-tickets", "user-1", `{"ref":"`+alphaRef+`"}`, nil)
	assertAPIResponseError(t, recCap, http.StatusTooManyRequests, ErrFilesStreamLimitReached, "stream limit reached")

	// 5. Archive ticket creation & public ZIP stream consumption + replay rejection.
	recArchTicket := doReq(http.MethodPost, "/api/files/profiles/p-nc/archive-tickets", "user-1", `{"refs":["`+docsRef+`"]}`, nil)
	if recArchTicket.Code != http.StatusCreated {
		t.Fatalf("archive ticket status = %d, body = %s", recArchTicket.Code, recArchTicket.Body.String())
	}
	var archResp struct {
		DownloadURL string `json:"download_url"`
	}
	_ = json.Unmarshal(recArchTicket.Body.Bytes(), &archResp)

	recArch := doReq(http.MethodGet, archResp.DownloadURL, "", "", nil)
	if recArch.Code != http.StatusOK {
		t.Fatalf("archive download status = %d, body = %s", recArch.Code, recArch.Body.String())
	}
	zipReader, err := zip.NewReader(bytes.NewReader(recArch.Body.Bytes()), int64(recArch.Body.Len()))
	if err != nil || len(zipReader.File) != 1 || zipReader.File[0].Name != "docs/existing.txt" {
		t.Fatalf("unexpected zip archive: err=%v files=%+v", err, zipReader)
	}
	recArchReplay := doReq(http.MethodGet, archResp.DownloadURL, "", "", nil)
	assertAPIResponseError(t, recArchReplay, http.StatusNotFound, ErrFilesDownloadTicketInvalid, "archive ticket replay")

	// 6. Thumbnail success & unsupported media mapping.
	recThumb := doReq(http.MethodPost, "/api/files/profiles/p-nc/thumbnail", "user-1", `{"ref":"`+photoRef+`","width":128}`, nil)
	if recThumb.Code != http.StatusOK || recThumb.Body.String() != "jpeg-bytes" {
		t.Fatalf("thumbnail status = %d, body = %q", recThumb.Code, recThumb.Body.String())
	}
	provider.thumbnailErr = storage.ErrUnsupportedMedia
	recThumbUnsupported := doReq(http.MethodPost, "/api/files/profiles/p-nc/thumbnail", "user-1", `{"ref":"`+photoRef+`"}`, nil)
	assertAPIResponseError(t, recThumbUnsupported, http.StatusUnsupportedMediaType, ErrFilesUnsupportedOperation, "thumbnail unsupported media")

	// 7. Single & batch mutations + typed error mapping.
	recCopy := doReq(http.MethodPost, "/api/files/profiles/p-nc/entries:copy", "user-1", `{"ref":"`+alphaRef+`","destination_parent_ref":"`+docsRef+`"}`, nil)
	if recCopy.Code != http.StatusOK {
		t.Fatalf("copy status = %d, body = %s", recCopy.Code, recCopy.Body.String())
	}
	recBatchMove := doReq(http.MethodPost, "/api/files/profiles/p-nc/entries:batch-move", "user-1", `{"refs":["`+alphaRef+`"],"destination_parent_ref":"`+docsRef+`"}`, nil)
	if recBatchMove.Code != http.StatusOK {
		t.Fatalf("batch move status = %d, body = %s", recBatchMove.Code, recBatchMove.Body.String())
	}
	recDelete := doReq(http.MethodDelete, "/api/files/profiles/p-nc/entries", "user-1", `{"ref":"`+alphaRef+`"}`, nil)
	if recDelete.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", recDelete.Code, recDelete.Body.String())
	}

	// 8. Rate-limit scopes preserved across all endpoints.
	for _, rlCase := range []struct {
		scope  string
		method string
		path   string
		user   string
		body   string
	}{
		{"files-browse", http.MethodGet, "/api/files/profiles/p-nc/capabilities", "user-1", ""},
		{"files-mutation", http.MethodPost, "/api/files/profiles/p-nc/directories", "user-1", `{"name":"x"}`},
		{"files-upload", http.MethodPut, "/api/files/profiles/p-nc/content", "user-1", "abc"},
		{"files-thumbnail", http.MethodPost, "/api/files/profiles/p-nc/thumbnail", "user-1", `{"ref":"` + photoRef + `"}`},
		{"files-ticket-consume", http.MethodGet, "/api/files/download/any", "", ""},
		{"files-archive", http.MethodPost, "/api/files/profiles/p-nc/archive-tickets", "user-1", `{"refs":["` + photoRef + `"]}`},
		{"files-archive-ticket-consume", http.MethodGet, "/api/files/archive/any", "", ""},
		{"files-transfer", http.MethodPost, "/api/files/profiles/p-nc/transfers", "user-1", `{}`},
	} {
		limiter.blocked[rlCase.scope] = true
		rec := doReq(rlCase.method, rlCase.path, rlCase.user, rlCase.body, nil)
		assertAPIResponseError(t, rec, http.StatusTooManyRequests, ErrRateLimited, "rate limit "+rlCase.scope)
		limiter.blocked[rlCase.scope] = false
	}

	if len(audits) == 0 {
		t.Fatal("expected audit actions recorded during file operations")
	}
}

func TestFileModuleTransferWorkflowOverHTTP(t *testing.T) {
	const key = "01234567890123456789012345678901"
	encSecret, err := crypto.EncryptWithDomain("saved-secret", key, crypto.ConnectionCredentialDomain(false))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	database, state := newFileModuleTestDB(t, map[string][]driver.Value{
		"p-src": profileTestRow("p-src", "user-1", "nextcloud", "https://src.example.test", "alice", encSecret),
		"p-dst": profileTestRow("p-dst", "user-1", "webdav", "https://dst.example.test", "bob", encSecret),
	})

	indexedCh := make(chan string, 1)
	var cancelledIDs []string

	api := newFileAPI(fileDependencies{
		db:            database,
		encryptionKey: key,
		rateLimiter:   allowAllFileRateLimiter{},
		startTransferIndexer: func(_ context.Context, migrationID string) {
			indexedCh <- migrationID
		},
		publishCancelEvent: func(_ context.Context, migrationID string) error {
			cancelledIDs = append(cancelledIDs, migrationID)
			return nil
		},
	})

	mux := http.NewServeMux()
	api.registerRoutes(mux, headerAuthMiddleware)

	fileRef, err := sealFileReference(fileReference{
		UserID:       "user-1",
		ProfileID:    "p-src",
		ResourceType: "files",
		Kind:         "file",
		Name:         "report.pdf",
		Locator:      storage.ManagerLocator{Path: "/report.pdf"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create transfer through HTTP.
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p-src/transfers", strings.NewReader(`{"refs":["`+fileRef+`"],"target_profile_id":"p-dst","operation":"COPY"}`))
	reqCreate.Header.Set("X-Test-User", "user-1")
	recCreate := httptest.NewRecorder()
	mux.ServeHTTP(recCreate, reqCreate)

	if recCreate.Code != http.StatusAccepted {
		t.Fatalf("create transfer status = %d, body = %s", recCreate.Code, recCreate.Body.String())
	}
	var createResp struct {
		Success    bool   `json:"success"`
		TransferID string `json:"transfer_id"`
	}
	if err := json.Unmarshal(recCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatal(err)
	}
	if createResp.TransferID == "" {
		t.Fatal("expected non-empty transfer_id")
	}
	select {
	case gotID := <-indexedCh:
		if gotID != createResp.TransferID {
			t.Fatalf("indexed migration id = %q, want %q", gotID, createResp.TransferID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected startTransferIndexer to be invoked")
	}

	// 2. List transfers through HTTP.
	reqList := httptest.NewRequest(http.MethodGet, "/api/files/profiles/p-src/transfers", nil)
	reqList.Header.Set("X-Test-User", "user-1")
	recList := httptest.NewRecorder()
	mux.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("list transfers status = %d, body = %s", recList.Code, recList.Body.String())
	}
	var listResp struct {
		Transfers []fileTransferSummary `json:"transfers"`
	}
	_ = json.Unmarshal(recList.Body.Bytes(), &listResp)
	if len(listResp.Transfers) != 1 || listResp.Transfers[0].ID != createResp.TransferID || listResp.Transfers[0].Operation != "copy" {
		t.Fatalf("unexpected transfers = %+v", listResp.Transfers)
	}

	// 3. Cancel transfer owned by another user -> 404 ErrFilesNotFound.
	reqCancelOther := httptest.NewRequest(http.MethodPost, "/api/files/transfers/"+createResp.TransferID+"/cancel", nil)
	reqCancelOther.Header.Set("X-Test-User", "user-2")
	recCancelOther := httptest.NewRecorder()
	mux.ServeHTTP(recCancelOther, reqCancelOther)
	assertAPIResponseError(t, recCancelOther, http.StatusNotFound, ErrFilesNotFound, "cross-user transfer cancel")

	// 4. Cancel non-file-manager migration -> 404 ErrFilesNotFound.
	state.setMigration("regular-migration", "user-1", "regular-picker", "RUNNING")
	reqCancelRegular := httptest.NewRequest(http.MethodPost, "/api/files/transfers/regular-migration/cancel", nil)
	reqCancelRegular.Header.Set("X-Test-User", "user-1")
	recCancelRegular := httptest.NewRecorder()
	mux.ServeHTTP(recCancelRegular, reqCancelRegular)
	assertAPIResponseError(t, recCancelRegular, http.StatusNotFound, ErrFilesNotFound, "non-file-manager migration cancel")

	// 5. Cancel owned transfer -> 200 OK & publishCancelEvent invoked.
	reqCancel := httptest.NewRequest(http.MethodPost, "/api/files/transfers/"+createResp.TransferID+"/cancel", nil)
	reqCancel.Header.Set("X-Test-User", "user-1")
	recCancel := httptest.NewRecorder()
	mux.ServeHTTP(recCancel, reqCancel)
	if recCancel.Code != http.StatusOK {
		t.Fatalf("cancel transfer status = %d, body = %s", recCancel.Code, recCancel.Body.String())
	}
	if len(cancelledIDs) != 1 || cancelledIDs[0] != createResp.TransferID {
		t.Fatalf("cancelledIDs = %v, want [%s]", cancelledIDs, createResp.TransferID)
	}
}

func TestFileModuleThroughGatewayHandler(t *testing.T) {
	server := newTestAPIGateway(t, map[string]*db.UserAuthState{
		"user-1": {Active: true, Role: "USER"},
	})
	server.rateLimiter = allowAllFileRateLimiter{}
	database, _ := newFileModuleTestDB(t, map[string][]driver.Value{
		"p-1": profileTestRow("p-1", "user-1", "nextcloud", "https://nc.example.test", "alice", ""),
	})
	server.db = database

	handler := server.Handler()
	token, err := auth.GenerateAccessToken(&db.User{ID: "user-1", Email: "user@example.com", Role: "USER", Active: true}, testGatewayJWTSecret)
	if err != nil {
		t.Fatalf("GenerateAccessToken: %v", err)
	}

	// Unauthenticated call to protected file route rejected by gateway JWT middleware.
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, httptest.NewRequest(http.MethodGet, "/api/files/profiles/p-1/capabilities", nil))
	assertAPIResponseError(t, recUnauth, http.StatusUnauthorized, ErrUnauthorized, "gateway unauthenticated file route")

	// Authenticated call passes through gateway into the file module.
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/files/profiles/p-1/capabilities", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+token)
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("gateway authenticated capabilities status = %d, body = %s", recAuth.Code, recAuth.Body.String())
	}

	// Public ticket route stays reachable without JWT through gateway.
	recPublic := httptest.NewRecorder()
	handler.ServeHTTP(recPublic, httptest.NewRequest(http.MethodGet, "/api/files/download/missing", nil))
	assertAPIResponseError(t, recPublic, http.StatusNotFound, ErrFilesDownloadTicketInvalid, "gateway public file ticket route")
}

// --- Test SQL driver for file-manager feature tests ---

var fileModuleDriverID atomic.Uint64

type fileModuleMigrationRecord struct {
	id        string
	userID    string
	marker    string
	status    string
	sourceID  string
	targetID  string
	createdAt time.Time
	updatedAt time.Time
}

type fileModuleTestDBState struct {
	mu         sync.Mutex
	profiles   map[string][]driver.Value
	migrations map[string]*fileModuleMigrationRecord
	nextID     int
}

func (s *fileModuleTestDBState) setMigration(id, userID, marker, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.migrations[id] = &fileModuleMigrationRecord{
		id:        id,
		userID:    userID,
		marker:    marker,
		status:    status,
		createdAt: now,
		updatedAt: now,
	}
}

func newFileModuleTestDB(t *testing.T, profiles map[string][]driver.Value) (*sql.DB, *fileModuleTestDBState) {
	t.Helper()
	state := &fileModuleTestDBState{
		profiles:   profiles,
		migrations: make(map[string]*fileModuleMigrationRecord),
	}
	name := "file-module-test-" + strconv.FormatUint(fileModuleDriverID.Add(1), 10)
	sql.Register(name, fileModuleTestDriver{state: state})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, state
}

type fileModuleTestDriver struct{ state *fileModuleTestDBState }

func (d fileModuleTestDriver) Open(string) (driver.Conn, error) {
	return fileModuleTestConn{state: d.state}, nil
}

type fileModuleTestConn struct{ state *fileModuleTestDBState }

func (c fileModuleTestConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c fileModuleTestConn) Close() error                        { return nil }
func (c fileModuleTestConn) Begin() (driver.Tx, error) {
	return fileModuleTestTx{state: c.state}, nil
}
func (c fileModuleTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return fileModuleTestTx{state: c.state}, nil
}

func (c fileModuleTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE migrations") && len(args) >= 3 {
		c.state.mu.Lock()
		if rec := c.state.migrations[args[2].Value.(string)]; rec != nil {
			rec.status = args[0].Value.(string)
			rec.updatedAt = time.Now().UTC()
		}
		c.state.mu.Unlock()
	}
	return driver.RowsAffected(1), nil
}

func (c fileModuleTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "connection_profiles") {
		id := args[0].Value.(string)
		userID := args[1].Value.(string)
		row, ok := c.state.profiles[id]
		owned := ok && len(row) > 1 && row[1] == userID
		return &backupProfileConnectionRows{rows: [][]driver.Value{{owned}}, columns: 1}, nil
	}
	if strings.Contains(query, "FROM connection_profiles WHERE id") {
		row, ok := c.state.profiles[args[0].Value.(string)]
		if !ok {
			return &backupProfileConnectionRows{columns: 14}, nil
		}
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 14}, nil
	}
	if strings.Contains(query, "INSERT INTO migrations") {
		c.state.mu.Lock()
		c.state.nextID++
		id := "transfer-" + strconv.Itoa(c.state.nextID)
		now := time.Now().UTC()
		userID, _ := args[0].Value.(string)
		srcID, _ := args[1].Value.(string)
		dstID, _ := args[2].Value.(string)
		status, _ := args[19].Value.(string)
		marker, _ := args[24].Value.(string)
		c.state.migrations[id] = &fileModuleMigrationRecord{
			id:        id,
			userID:    userID,
			marker:    marker,
			status:    status,
			sourceID:  srcID,
			targetID:  dstID,
			createdAt: now,
			updatedAt: now,
		}
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: [][]driver.Value{{id, now, now}}, columns: 3}, nil
	}
	if strings.Contains(query, "FROM migrations m") {
		userID := args[0].Value.(string)
		profileID := args[1].Value.(string)
		c.state.mu.Lock()
		var rows [][]driver.Value
		for _, m := range c.state.migrations {
			if m.userID == userID && (m.sourceID == profileID || m.targetID == profileID) && strings.HasPrefix(m.marker, fileManagerTransferMarker) {
				rows = append(rows, []driver.Value{m.id, m.marker, m.status, "src", "dst", int64(0), int64(0), int64(0), int64(0)})
			}
		}
		c.state.mu.Unlock()
		return &backupProfileConnectionRows{rows: rows, columns: 9}, nil
	}
	if strings.Contains(query, "FROM migrations WHERE id") {
		id := args[0].Value.(string)
		c.state.mu.Lock()
		m := c.state.migrations[id]
		c.state.mu.Unlock()
		if m == nil {
			return &backupProfileConnectionRows{columns: 43}, nil
		}
		row := make([]driver.Value, 43)
		for _, idx := range []int{4, 5, 6, 7, 10, 11, 12, 13, 14, 15, 18, 19, 36} {
			row[idx] = ""
		}
		row[0] = m.id
		row[1] = m.userID
		row[2] = m.sourceID
		row[3] = m.targetID
		row[20] = m.status
		row[21] = "SKIP"
		for _, idx := range []int{22, 23, 24, 25, 26, 27, 28, 29, 32, 37, 38} {
			row[idx] = int64(0)
		}
		row[31] = false
		row[34] = m.createdAt
		row[35] = m.updatedAt
		row[39] = m.marker
		return &backupProfileConnectionRows{rows: [][]driver.Value{row}, columns: 43}, nil
	}
	return nil, driver.ErrSkip
}

type fileModuleTestTx struct{ state *fileModuleTestDBState }

func (tx fileModuleTestTx) Commit() error   { return nil }
func (tx fileModuleTestTx) Rollback() error { return nil }
func (tx fileModuleTestTx) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return fileModuleTestConn{state: tx.state}.ExecContext(ctx, query, args)
}
func (tx fileModuleTestTx) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return fileModuleTestConn{state: tx.state}.QueryContext(ctx, query, args)
}
