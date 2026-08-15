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
