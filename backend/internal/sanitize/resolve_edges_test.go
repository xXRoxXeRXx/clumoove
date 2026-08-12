package sanitize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"backend/internal/storage"
)

// mockProviderWithErrors layers a deterministic listing error on the shared
// mock so ResolveCollision's target-directory lookup error is covered.
type mockProviderWithErrors struct {
	*mockProvider
	listingErr error
}

func (m *mockProviderWithErrors) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	if m.listingErr != nil {
		return nil, m.listingErr
	}
	return m.mockProvider.GetDirectoryListing(ctx, resourceType, dirPath)
}

func TestResolveCollision_ExhaustedAfter100(t *testing.T) {
	files := map[string][]storage.CloudResource{}
	var listing []storage.CloudResource
	for i := 1; i <= 100; i++ {
		p := fmt.Sprintf("/target/f_%d.txt", i)
		listing = append(listing, storage.CloudResource{Path: p, Name: fmt.Sprintf("f_%d.txt", i)})
	}
	files["/target"] = listing

	mock := &mockProvider{files: files}
	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "f.txt", "s3")
	if err == nil {
		t.Errorf("expected error after 100 exhausted attempts, got resolved=%q", resolved)
	}
}

func TestResolveCollision_DirectoryListingError(t *testing.T) {
	base := &mockProvider{files: map[string][]storage.CloudResource{}}
	mock := &mockProviderWithErrors{
		mockProvider: base,
		listingErr:   errors.New("boom"),
	}
	_, err := ResolveCollision(context.Background(), mock, "files", "/target", "report.pdf", "s3")
	if err == nil {
		t.Errorf("expected error when GetDirectoryListing returns error, got nil")
	}
}

func TestResolveCollision_ConstrainsLongCandidateAndListsOnce(t *testing.T) {
	base := &mockProvider{files: map[string][]storage.CloudResource{"/target": {}}}
	mock := &countingListingProvider{mockProvider: base}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", strings.Repeat("a", 251)+".txt", "smb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf8.RuneCountInString(resolved) > 255 {
		t.Errorf("candidate exceeds filename limit: %d runes", utf8.RuneCountInString(resolved))
	}
	if !strings.HasSuffix(resolved, "_1.txt") {
		t.Errorf("expected collision suffix and extension to be preserved, got %q", resolved)
	}
	if mock.listingCalls != 1 {
		t.Errorf("expected one directory listing, got %d", mock.listingCalls)
	}
}

func TestResolveCollision_UsesUnicodeCaseFolding(t *testing.T) {
	mock := &mockProvider{files: map[string][]storage.CloudResource{
		"/target": {{Path: "/target/aς_1.txt", Name: "aς_1.txt"}},
	}}

	resolved, err := ResolveCollision(context.Background(), mock, "files", "/target", "aσ.txt", "dropbox")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "aσ_2.txt" {
		t.Errorf("expected Unicode case-folded collision to use _2, got %q", resolved)
	}
}

type countingListingProvider struct {
	*mockProvider
	listingCalls int
}

func (m *countingListingProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	m.listingCalls++
	return m.mockProvider.GetDirectoryListing(ctx, resourceType, dirPath)
}
