package sanitize

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"backend/internal/storage"
)

// mockProviderWithErrors reuses the existing mockProvider but layers
// deterministic FileExists errors on top, to exercise the error path in
// ResolveCollision without redeclaring the shared mock.
type mockProviderWithErrors struct {
	*mockProvider
	existsErr map[string]error
}

func (m *mockProviderWithErrors) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if e, ok := m.existsErr[filePath]; ok && e != nil {
		return false, 0, e
	}
	return m.mockProvider.FileExists(ctx, resourceType, filePath)
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

func TestResolveCollision_FileExistsError(t *testing.T) {
	base := &mockProvider{files: map[string][]storage.CloudResource{}}
	mock := &mockProviderWithErrors{
		mockProvider: base,
		existsErr: map[string]error{
			"/target/report_1.pdf": errors.New("boom"),
		},
	}
	_, err := ResolveCollision(context.Background(), mock, "files", "/target", "report.pdf", "s3")
	if err == nil {
		t.Errorf("expected error when FileExists returns error, got nil")
	}
}
