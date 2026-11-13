package sanitize

import (
	"context"

	"backend/internal/storage"
)

// mockProvider is the shared directory-listing implementation for this package's
// tests. Individual tests can embed it to override the operation under test.
type mockProvider struct {
	files map[string][]storage.CloudResource
}

func (m *mockProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	return m.files[dirPath], nil
}
