package sanitize

import (
	"context"
	"fmt"
	"io"

	"backend/internal/storage"
)

// mockProvider is the shared StorageProvider implementation for this package's
// tests. Individual tests can embed it to override the operation under test.
type mockProvider struct {
	files map[string][]storage.CloudResource
}

func (m *mockProvider) Close() error                              { return nil }
func (m *mockProvider) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockProvider) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	return storage.CloudResource{}, fmt.Errorf("not implemented")
}
func (m *mockProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
func (m *mockProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return fmt.Errorf("not implemented")
}
func (m *mockProvider) SupportsAtomicRename() bool { return true }
func (m *mockProvider) VerificationMode() storage.VerificationMode {
	return storage.VerificationSizeOnly
}
func (m *mockProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	return m.files[dirPath], nil
}
func (m *mockProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	for _, files := range m.files {
		for _, file := range files {
			if file.Path == filePath {
				return true, file.Size, nil
			}
		}
	}
	return false, 0, nil
}
