package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/auth"
	"backend/internal/storage"
)

type allowAllFileRateLimiter struct{}

func (allowAllFileRateLimiter) Allow(context.Context, string, string, int, time.Duration) bool {
	return true
}

type blockAllFileRateLimiter struct{}

func (blockAllFileRateLimiter) Allow(context.Context, string, string, int, time.Duration) bool {
	return false
}

type legacyFileManagerTestProvider struct {
	listings map[string][]storage.CloudResource
}

func (p *legacyFileManagerTestProvider) Close() error { return nil }
func (p *legacyFileManagerTestProvider) Connect(context.Context) (bool, error) {
	return true, nil
}
func (p *legacyFileManagerTestProvider) GetDirectoryListing(_ context.Context, _ string, directory string) ([]storage.CloudResource, error) {
	return append([]storage.CloudResource(nil), p.listings[directory]...), nil
}
func (p *legacyFileManagerTestProvider) InspectResource(_ context.Context, _ string, value string) (storage.CloudResource, error) {
	for _, items := range p.listings {
		for _, item := range items {
			if item.Path == value {
				return item, nil
			}
		}
	}
	return storage.CloudResource{}, storage.ErrNotFound
}
func (p *legacyFileManagerTestProvider) StreamDownload(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("content")), nil
}
func (p *legacyFileManagerTestProvider) StreamUpload(context.Context, string, string, io.Reader, int64) error {
	return nil
}
func (p *legacyFileManagerTestProvider) StreamUploadChunked(context.Context, string, string, io.Reader, int64, chan<- int64) error {
	return nil
}
func (p *legacyFileManagerTestProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	return false, 0, nil
}
func (p *legacyFileManagerTestProvider) DeleteFile(context.Context, string, string) error {
	return nil
}
func (p *legacyFileManagerTestProvider) GetFileHash(context.Context, string, string) (string, error) {
	return "", nil
}
func (p *legacyFileManagerTestProvider) CreateParentDirectories(context.Context, string, string) error {
	return nil
}
func (p *legacyFileManagerTestProvider) CreateDirectory(context.Context, string, string) error {
	return nil
}
func (p *legacyFileManagerTestProvider) RenameFile(context.Context, string, string, string) error {
	return nil
}
func (p *legacyFileManagerTestProvider) SupportsAtomicRename() bool { return false }
func (p *legacyFileManagerTestProvider) VerificationMode() storage.VerificationMode {
	return storage.VerificationSizeOnly
}

func TestFileReferenceRoundTripBindsUserAndProfile(t *testing.T) {
	const key = "file-manager-test-key"
	reference, err := sealFileReference(fileReference{
		UserID:       "user-a",
		ProfileID:    "profile-a",
		ResourceType: "files",
		Kind:         "file",
		Locator:      storage.ManagerLocator{Path: "/report.pdf", NativeID: "drive-file-id"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}

	opened, err := openFileReference(reference, key, "user-a", "profile-a")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Locator.NativeID != "drive-file-id" || opened.Kind != "file" {
		t.Fatalf("opened reference = %#v", opened)
	}
	if _, err := openFileReference(reference, key, "user-b", "profile-a"); err == nil {
		t.Fatal("cross-user reference replay was accepted")
	}
	if _, err := openFileReference(reference, key, "user-a", "profile-b"); err == nil {
		t.Fatal("cross-profile reference replay was accepted")
	}
	if _, err := openFileReference(reference+"00", key, "user-a", "profile-a"); err == nil {
		t.Fatal("tampered reference was accepted")
	}
}

func TestHandleFileUploadRequiresKnownLength(t *testing.T) {
	server := &APIServer{rateLimiter: allowAllFileRateLimiter{}}
	request := httptest.NewRequest(http.MethodPut, "/api/files/profiles/profile-a/content", strings.NewReader("body"))
	request.ContentLength = -1
	request.SetPathValue("profileID", "profile-a")
	request = request.WithContext(context.WithValue(request.Context(), auth.ClaimsKey, &auth.Claims{UserID: "user-a"}))
	recorder := httptest.NewRecorder()

	server.handleFileUpload(recorder, request)

	if recorder.Code != http.StatusLengthRequired {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusLengthRequired)
	}
	if body := recorder.Body.String(); !strings.Contains(body, string(ErrFilesUploadLengthRequired)) {
		t.Fatalf("body = %s, want machine-readable length error", body)
	}
}

func TestDecodeUploadFileNameAcceptsPaddedBase64URL(t *testing.T) {
	encoded := base64.URLEncoding.EncodeToString([]byte("report ä.txt"))
	name, err := decodeUploadFileName(encoded)
	if err != nil || name != "report ä.txt" {
		t.Fatalf("decodeUploadFileName() = (%q, %v)", name, err)
	}
}

func TestValidManagerUploadName(t *testing.T) {
	for _, name := range []string{"report.txt", "überblick.pdf", "spaces are valid.txt"} {
		if !validManagerUploadName(name) {
			t.Errorf("validManagerUploadName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", "nested/file.txt", "nested\\file.txt", "nul\x00.txt", "line\nbreak.txt"} {
		if validManagerUploadName(name) {
			t.Errorf("validManagerUploadName(%q) = true", name)
		}
	}
}

func TestFileCursorRoundTripBindsDirectory(t *testing.T) {
	const key = "file-manager-test-key"
	cursor, err := sealFileCursor(fileCursor{
		UserID:         "user-a",
		ProfileID:      "profile-a",
		ResourceType:   "files",
		Parent:         storage.ManagerLocator{Path: "/documents", NativeID: "folder-id"},
		ProviderCursor: "next-page-token",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openFileCursor(cursor, key, "user-a", "profile-a")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Parent.NativeID != "folder-id" || opened.ProviderCursor != "next-page-token" {
		t.Fatalf("opened cursor = %#v", opened)
	}
	if _, err := openFileCursor(cursor, key, "user-a", "profile-b"); err == nil {
		t.Fatal("cross-profile cursor replay was accepted")
	}
}

func TestValidManagedPath(t *testing.T) {
	for _, value := range []string{"/", "/documents/report.pdf", "/nested/directory"} {
		if !validManagedPath(value) {
			t.Errorf("validManagedPath(%q) = false", value)
		}
	}
	for _, value := range []string{"", "relative", "/../secret", "/nested/../secret", "/windows\\path", "/nul\x00path"} {
		if validManagedPath(value) {
			t.Errorf("validManagedPath(%q) = true", value)
		}
	}
}

func TestSortManagedEntriesUsesStableNativeIdentity(t *testing.T) {
	resources := []storage.ManagerItem{
		{Name: "report", Locator: storage.ManagerLocator{NativeID: "b"}},
		{Name: "report", Locator: storage.ManagerLocator{NativeID: "a"}},
	}
	sortManagedEntries(resources)
	if got := resources[0].Locator.NativeID; got != "a" {
		t.Fatalf("first stable ID = %q, want a", got)
	}
}

func TestLegacyManagerPaginatesAndDetectsDirectoryChanges(t *testing.T) {
	provider := &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{
		"/": {
			{Path: "/c.txt", Name: "c.txt"},
			{Path: "/a.txt", Name: "a.txt"},
			{Path: "/b.txt", Name: "b.txt"},
		},
	}}
	page, err := listLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Name != "a.txt" || page.Items[1].Name != "b.txt" || page.NextCursor == "" {
		t.Fatalf("first page = %#v", page)
	}
	next, err := listLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, page.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.Items[0].Name != "c.txt" {
		t.Fatalf("next page = %#v", next)
	}
	provider.listings["/"] = append(provider.listings["/"], storage.CloudResource{Path: "/d.txt", Name: "d.txt"})
	if _, err := listLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, page.NextCursor, 2); !errors.Is(err, errManagerDirectoryChanged) {
		t.Fatalf("changed page error = %v, want directory changed", err)
	}
}

func TestLegacyManagerPathResolveFallsBackToExistingParent(t *testing.T) {
	provider := &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{
		"/":     {{Path: "/docs", Name: "docs", IsDir: true}},
		"/docs": {{Path: "/docs/current", Name: "current", IsDir: true}},
	}}
	locator, breadcrumbs, fallback, err := resolveLegacyManagerPath(context.Background(), provider, "/docs/missing")
	if err != nil {
		t.Fatal(err)
	}
	if !fallback || locator.Path != "/docs" || len(breadcrumbs) != 1 || breadcrumbs[0].Name != "docs" {
		t.Fatalf("resolve result = (%#v, %#v, %t)", locator, breadcrumbs, fallback)
	}
}

func TestLegacyManagerUpload(t *testing.T) {
	provider := &legacyFileManagerTestProvider{listings: map[string][]storage.CloudResource{
		"/": {
			{Path: "/existing.txt", Name: "existing.txt"},
		},
	}}

	// SKIP
	res, err := uploadLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, "existing.txt", strings.NewReader("content"), 7, storage.ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || res.Status != "skipped" || res.FinalName != "existing.txt" {
		t.Fatalf("uploadLegacyManager SKIP = (%#v, %v), want skipped", res, err)
	}

	// OVERWRITE
	res, err = uploadLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, "existing.txt", strings.NewReader("content"), 7, storage.ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
	if err != nil || res.Status != "uploaded" || res.FinalName != "existing.txt" {
		t.Fatalf("uploadLegacyManager OVERWRITE = (%#v, %v), want uploaded", res, err)
	}

	// RENAME
	res, err = uploadLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, "existing.txt", strings.NewReader("content"), 7, storage.ManagerUploadOptions{ConflictStrategy: "RENAME"})
	if err != nil || res.Status != "renamed" || res.FinalName != "existing (1).txt" {
		t.Fatalf("uploadLegacyManager RENAME = (%#v, %v), want renamed to existing (1).txt", res, err)
	}

	// NEW FILE
	res, err = uploadLegacyManager(context.Background(), provider, storage.ManagerLocator{Path: "/"}, "new.txt", strings.NewReader("content"), 7, storage.ManagerUploadOptions{ConflictStrategy: "SKIP"})
	if err != nil || res.Status != "uploaded" || res.FinalName != "new.txt" {
		t.Fatalf("uploadLegacyManager NEW = (%#v, %v), want uploaded", res, err)
	}
}

func TestHandleFileCapabilitiesAuthAndRateLimit(t *testing.T) {
	server := &APIServer{rateLimiter: allowAllFileRateLimiter{}}

	// 1. Missing claims -> 401 Unauthorized
	req := httptest.NewRequest(http.MethodGet, "/api/files/profiles/p1/capabilities", nil)
	rec := httptest.NewRecorder()
	server.handleFileCapabilities(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// 2. Rate limited -> 429 ErrRateLimited
	serverBlocked := &APIServer{rateLimiter: blockAllFileRateLimiter{}}
	reqBlocked := httptest.NewRequest(http.MethodGet, "/api/files/profiles/p1/capabilities", nil)
	reqBlocked = reqBlocked.WithContext(context.WithValue(reqBlocked.Context(), auth.ClaimsKey, &auth.Claims{UserID: "u1"}))
	recBlocked := httptest.NewRecorder()
	serverBlocked.handleFileCapabilities(recBlocked, reqBlocked)
	if recBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recBlocked.Code, http.StatusTooManyRequests)
	}
}

func TestHandleFileEntriesListValidation(t *testing.T) {
	server := &APIServer{
		rateLimiter:   allowAllFileRateLimiter{},
		encryptionKey: "test-encryption-key-for-files!!",
	}

	tests := []struct {
		name       string
		claims     *auth.Claims
		rateLimit  rateLimiter
		body       string
		wantStatus int
		wantCode   APIErrorCode
	}{
		{
			name:       "missing claims",
			claims:     nil,
			body:       `{"resource_type":"files"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrUnauthorized,
		},
		{
			name:       "rate limited",
			claims:     &auth.Claims{UserID: "u1"},
			rateLimit:  blockAllFileRateLimiter{},
			body:       `{"resource_type":"files"}`,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   ErrRateLimited,
		},
		{
			name:       "invalid body json",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{not-valid-json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrInvalidBody,
		},
		{
			name:       "invalid resource type",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"calendars"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrInvalidResourceType,
		},
		{
			name:       "limit exceeds maximum",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"files","limit":500}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrInvalidBody,
		},
		{
			name:       "profile not found",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"files","limit":50}`,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrProfileNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := server
			if tt.rateLimit != nil {
				s = &APIServer{rateLimiter: tt.rateLimit, encryptionKey: server.encryptionKey}
			}
			req := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p1/entries:list", strings.NewReader(tt.body))
			req.SetPathValue("profileID", "p1")
			if tt.claims != nil {
				req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, tt.claims))
			}
			rec := httptest.NewRecorder()
			s.handleFileEntriesList(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), string(tt.wantCode)) {
				t.Fatalf("body = %s, want code %s", rec.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestHandleFileEntriesResolveValidation(t *testing.T) {
	server := &APIServer{
		rateLimiter:   allowAllFileRateLimiter{},
		encryptionKey: "test-encryption-key-for-files!!",
	}

	tests := []struct {
		name       string
		claims     *auth.Claims
		rateLimit  rateLimiter
		body       string
		wantStatus int
		wantCode   APIErrorCode
	}{
		{
			name:       "missing claims",
			claims:     nil,
			body:       `{"resource_type":"files","path":"/test"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrUnauthorized,
		},
		{
			name:       "rate limited",
			claims:     &auth.Claims{UserID: "u1"},
			rateLimit:  blockAllFileRateLimiter{},
			body:       `{"resource_type":"files","path":"/test"}`,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   ErrRateLimited,
		},
		{
			name:       "invalid resource type",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"contacts","path":"/test"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrInvalidResourceType,
		},
		{
			name:       "path traversal",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"files","path":"/docs/../secret"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrFilesInvalidRef,
		},
		{
			name:       "windows backslash path",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"files","path":"\\windows\\path"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   ErrFilesInvalidRef,
		},
		{
			name:       "profile not found",
			claims:     &auth.Claims{UserID: "u1"},
			body:       `{"resource_type":"files","path":"/docs"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrProfileNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := server
			if tt.rateLimit != nil {
				s = &APIServer{rateLimiter: tt.rateLimit, encryptionKey: server.encryptionKey}
			}
			req := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p1/entries:resolve", strings.NewReader(tt.body))
			req.SetPathValue("profileID", "p1")
			if tt.claims != nil {
				req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, tt.claims))
			}
			rec := httptest.NewRecorder()
			s.handleFileEntriesResolve(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), string(tt.wantCode)) {
				t.Fatalf("body = %s, want code %s", rec.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestHandleFileDownloadTicketValidation(t *testing.T) {
	server := &APIServer{
		rateLimiter:   allowAllFileRateLimiter{},
		encryptionKey: "test-encryption-key-for-files!!",
	}

	// 1. Missing claims
	req := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p1/download-tickets", strings.NewReader(`{"ref":"abc"}`))
	rec := httptest.NewRecorder()
	server.handleFileDownloadTicket(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// 2. Rate limited
	serverBlocked := &APIServer{rateLimiter: blockAllFileRateLimiter{}, encryptionKey: server.encryptionKey}
	reqBlocked := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p1/download-tickets", strings.NewReader(`{"ref":"abc"}`))
	reqBlocked = reqBlocked.WithContext(context.WithValue(reqBlocked.Context(), auth.ClaimsKey, &auth.Claims{UserID: "u1"}))
	recBlocked := httptest.NewRecorder()
	serverBlocked.handleFileDownloadTicket(recBlocked, reqBlocked)
	if recBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recBlocked.Code, http.StatusTooManyRequests)
	}

	// 3. Directory reference instead of file reference
	dirRef, _ := sealFileReference(fileReference{
		UserID:       "u1",
		ProfileID:    "p1",
		ResourceType: "files",
		Kind:         "directory",
		Locator:      storage.ManagerLocator{Path: "/docs"},
	}, server.encryptionKey)

	reqDir := httptest.NewRequest(http.MethodPost, "/api/files/profiles/p1/download-tickets", strings.NewReader(`{"ref":"`+dirRef+`"}`))
	reqDir.SetPathValue("profileID", "p1")
	reqDir = reqDir.WithContext(context.WithValue(reqDir.Context(), auth.ClaimsKey, &auth.Claims{UserID: "u1"}))
	recDir := httptest.NewRecorder()
	server.handleFileDownloadTicket(recDir, reqDir)
	if recDir.Code != http.StatusBadRequest || !strings.Contains(recDir.Body.String(), string(ErrFilesInvalidRef)) {
		t.Fatalf("status = %d, body = %s, want 400 ErrFilesInvalidRef", recDir.Code, recDir.Body.String())
	}
}

func TestHandleFileUploadHeaderValidations(t *testing.T) {
	server := &APIServer{
		rateLimiter:   allowAllFileRateLimiter{},
		encryptionKey: "test-encryption-key-for-files!!",
	}

	validFileNameEncoded := base64.RawURLEncoding.EncodeToString([]byte("report.txt"))

	tests := []struct {
		name       string
		claims     *auth.Claims
		rateLimit  rateLimiter
		length     int64
		fileName   string
		strategy   string
		parentRef  string
		wantStatus int
		wantCode   APIErrorCode
	}{
		{
			name:       "missing claims",
			claims:     nil,
			length:     10,
			fileName:   validFileNameEncoded,
			wantStatus: http.StatusUnauthorized,
			wantCode:   ErrUnauthorized,
		},
		{
			name:       "rate limited",
			claims:     &auth.Claims{UserID: "u1"},
			rateLimit:  blockAllFileRateLimiter{},
			length:     10,
			fileName:   validFileNameEncoded,
			wantStatus: http.StatusTooManyRequests,
			wantCode:   ErrRateLimited,
		},
		{
			name:       "profile not found",
			claims:     &auth.Claims{UserID: "u1"},
			length:     10,
			fileName:   validFileNameEncoded,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrProfileNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := server
			if tt.rateLimit != nil {
				s = &APIServer{rateLimiter: tt.rateLimit, encryptionKey: server.encryptionKey}
			}
			req := httptest.NewRequest(http.MethodPut, "/api/files/profiles/p1/content", strings.NewReader("hello file"))
			req.ContentLength = tt.length
			req.SetPathValue("profileID", "p1")
			if tt.fileName != "" {
				req.Header.Set("X-Clumoove-File-Name", tt.fileName)
			}
			if tt.strategy != "" {
				req.Header.Set("X-Clumoove-Conflict-Strategy", tt.strategy)
			}
			if tt.parentRef != "" {
				req.Header.Set("X-Clumoove-Parent-Ref", tt.parentRef)
			}
			if tt.claims != nil {
				req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, tt.claims))
			}
			rec := httptest.NewRecorder()
			s.handleFileUpload(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), string(tt.wantCode)) {
				t.Fatalf("body = %s, want code %s", rec.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestHandleFileDownloadTicketConsumption(t *testing.T) {
	server := &APIServer{rateLimiter: allowAllFileRateLimiter{}}

	// Rate limited
	serverBlocked := &APIServer{rateLimiter: blockAllFileRateLimiter{}}
	reqBlocked := httptest.NewRequest(http.MethodGet, "/api/files/download/ticket123", nil)
	recBlocked := httptest.NewRecorder()
	serverBlocked.handleFileDownload(recBlocked, reqBlocked)
	if recBlocked.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recBlocked.Code, http.StatusTooManyRequests)
	}

	// Empty ticket in path
	reqEmpty := httptest.NewRequest(http.MethodGet, "/api/files/download/", nil)
	recEmpty := httptest.NewRecorder()
	server.handleFileDownload(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusNotFound || !strings.Contains(recEmpty.Body.String(), string(ErrFilesDownloadTicketInvalid)) {
		t.Fatalf("status = %d, want 404 ErrFilesDownloadTicketInvalid", recEmpty.Code)
	}
}

func TestFileProviderErrorMapping(t *testing.T) {
	server := &APIServer{}

	tests := []struct {
		err        error
		wantStatus int
		wantCode   APIErrorCode
	}{
		{
			err:        errManagerDirectoryChanged,
			wantStatus: http.StatusConflict,
			wantCode:   ErrFilesDirectoryChanged,
		},
		{
			err:        errManagerDirectoryTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   ErrFilesDirectoryTooLarge,
		},
		{
			err:        storage.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantCode:   ErrFilesNotFound,
		},
		{
			err:        storage.ErrAmbiguousPath,
			wantStatus: http.StatusConflict,
			wantCode:   ErrFilesPathAmbiguous,
		},
		{
			err:        errors.New("network disconnect"),
			wantStatus: http.StatusBadGateway,
			wantCode:   ErrFilesProviderUnavailable,
		},
	}

	for _, tt := range tests {
		rec := httptest.NewRecorder()
		server.writeFileProviderError(rec, tt.err)
		if rec.Code != tt.wantStatus {
			t.Errorf("writeFileProviderError(%v) status = %d, want %d", tt.err, rec.Code, tt.wantStatus)
		}
		if !strings.Contains(rec.Body.String(), string(tt.wantCode)) {
			t.Errorf("writeFileProviderError(%v) body = %s, want %s", tt.err, rec.Body.String(), tt.wantCode)
		}
	}
}

func TestParseManagerUploadOptions(t *testing.T) {
	allCaps := storage.ManagerCapabilities{
		ConflictSkip:      true,
		ConflictOverwrite: true,
		ConflictRename:    true,
	}
	skipOnly := storage.ManagerCapabilities{
		ConflictSkip: true,
	}

	// Defaults to SKIP when empty
	opt, err := parseManagerUploadOptions("", allCaps)
	if err != nil || opt.ConflictStrategy != "SKIP" {
		t.Fatalf("parseManagerUploadOptions empty = (%#v, %v), want SKIP", opt, err)
	}

	// OVERWRITE supported
	opt, err = parseManagerUploadOptions("OVERWRITE", allCaps)
	if err != nil || opt.ConflictStrategy != "OVERWRITE" {
		t.Fatalf("parseManagerUploadOptions OVERWRITE = (%#v, %v), want OVERWRITE", opt, err)
	}

	// OVERWRITE rejected when unsupported
	_, err = parseManagerUploadOptions("OVERWRITE", skipOnly)
	if err == nil {
		t.Fatal("expected error for OVERWRITE with skipOnly capabilities")
	}

	// RENAME supported
	opt, err = parseManagerUploadOptions("rename", allCaps)
	if err != nil || opt.ConflictStrategy != "RENAME" {
		t.Fatalf("parseManagerUploadOptions rename = (%#v, %v), want RENAME", opt, err)
	}

	// Unknown strategy rejected
	_, err = parseManagerUploadOptions("INVALID", allCaps)
	if err == nil {
		t.Fatal("expected error for invalid strategy")
	}
}

func TestAllowedFileActions(t *testing.T) {
	downloadAndUpload := storage.ManagerCapabilities{Download: true, Upload: true}
	downloadOnly := storage.ManagerCapabilities{Download: true, Upload: false}

	// File with download enabled
	fileActions := allowedFileActions(downloadAndUpload, false)
	if len(fileActions) != 1 || fileActions[0] != "download" {
		t.Fatalf("fileActions = %#v, want [download]", fileActions)
	}

	// Directory with upload enabled
	dirActions := allowedFileActions(downloadAndUpload, true)
	if len(dirActions) != 1 || dirActions[0] != "upload" {
		t.Fatalf("dirActions = %#v, want [upload]", dirActions)
	}

	// Directory with upload disabled
	noUploadDirActions := allowedFileActions(downloadOnly, true)
	if len(noUploadDirActions) != 0 {
		t.Fatalf("noUploadDirActions = %#v, want []", noUploadDirActions)
	}
}

