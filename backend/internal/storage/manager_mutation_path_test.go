package storage

import (
	"context"
	"errors"
	"io"
	"testing"
)

type mockStorageProvider struct {
	inspectFn    func(ctx context.Context, resourceType, path string) (CloudResource, error)
	listingFn    func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error)
	renameFn     func(ctx context.Context, resourceType, oldPath, newPath string) error
	inspectCount int
}

func (m *mockStorageProvider) Close() error { return nil }
func (m *mockStorageProvider) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockStorageProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if m.listingFn != nil {
		return m.listingFn(ctx, resourceType, dirPath)
	}
	return nil, nil
}
func (m *mockStorageProvider) InspectResource(ctx context.Context, resourceType, path string) (CloudResource, error) {
	m.inspectCount++
	if m.inspectFn != nil {
		return m.inspectFn(ctx, resourceType, path)
	}
	if path == "/backup" {
		return CloudResource{Path: "/backup", Name: "backup", IsDir: true}, nil
	}
	return CloudResource{Path: path, Name: "file.txt", Size: 100, IsDir: false}, nil
}
func (m *mockStorageProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	return nil, nil
}
func (m *mockStorageProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return nil
}
func (m *mockStorageProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return nil
}
func (m *mockStorageProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	return false, 0, nil
}
func (m *mockStorageProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return nil
}
func (m *mockStorageProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", nil
}
func (m *mockStorageProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return nil
}
func (m *mockStorageProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	return nil
}
func (m *mockStorageProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if m.renameFn != nil {
		return m.renameFn(ctx, resourceType, oldPath, newPath)
	}
	return nil
}
func (m *mockStorageProvider) SupportsAtomicRename() bool         { return true }
func (m *mockStorageProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }

func TestCopyNativePathManagerItem_Success(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{}
	opCalled := false

	res, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "file.txt", ManagerMutationOptions{}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		opCalled = true
		if source.Path != "/source/file.txt" {
			t.Errorf("source.Path = %q, want /source/file.txt", source.Path)
		}
		if target != "/backup/file.txt" {
			t.Errorf("target = %q, want /backup/file.txt", target)
		}
		if overwrite {
			t.Errorf("overwrite = %t, want false", overwrite)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opCalled {
		t.Error("expected operation to be called")
	}
	if res.Status != "copied" || res.FinalName != "file.txt" || !res.Native {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCopyNativePathManagerItem_ConflictSkip(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{
		listingFn: func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
			return []CloudResource{{Path: "/backup/file.txt", Name: "file.txt", IsDir: false}}, nil
		},
	}
	opCalled := false

	res, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "file.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictSkip}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		opCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opCalled {
		t.Error("operation must not be called when skipped")
	}
	if res.Status != "skipped" || res.FinalName != "file.txt" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCopyNativePathManagerItem_ConflictRename(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{
		listingFn: func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
			return []CloudResource{{Path: "/backup/file.txt", Name: "file.txt", IsDir: false}}, nil
		},
	}
	opTarget := ""

	res, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "file.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictRename}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		opTarget = target
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opTarget != "/backup/file (1).txt" {
		t.Errorf("opTarget = %q, want /backup/file (1).txt", opTarget)
	}
	if res.Status != "renamed_on_conflict" || res.FinalName != "file (1).txt" || !res.Native {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCopyNativePathManagerItem_ConflictOverwrite(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{
		listingFn: func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
			return []CloudResource{{Path: "/backup/file.txt", Name: "file.txt", IsDir: false}}, nil
		},
	}
	opOverwrite := false

	res, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "file.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		opOverwrite = overwrite
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !opOverwrite {
		t.Error("expected overwrite = true")
	}
	if res.Status != "copied" || res.FinalName != "file.txt" || !res.Native {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestCopyNativePathManagerItem_DirectoryOverwriteRefusal(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{
		listingFn: func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
			return []CloudResource{{Path: "/backup/folder", Name: "folder", IsDir: true}}, nil
		},
	}

	_, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "folder", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		t.Error("operation must not be called when directory overwrite is refused")
		return nil
	})
	if !errors.Is(err, ErrManagerConflict) {
		t.Fatalf("err = %v, want ErrManagerConflict", err)
	}
}

func TestCopyNativePathManagerItem_DirectoryCycleRefusal(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{
		inspectFn: func(ctx context.Context, resourceType, path string) (CloudResource, error) {
			return CloudResource{Path: path, Name: "dir", IsDir: true}, nil
		},
	}

	_, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/parent"}, ManagerLocator{Path: "/parent/child"}, "parent", ManagerMutationOptions{}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		t.Error("operation must not be called on directory cycle")
		return nil
	})
	if !errors.Is(err, ErrManagerDirectoryCycle) {
		t.Fatalf("err = %v, want ErrManagerDirectoryCycle", err)
	}
}

func TestCopyNativePathManagerItem_RootRefusal(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{}

	_, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/"}, ManagerLocator{Path: "/backup"}, "root", ManagerMutationOptions{}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		return nil
	})
	if !errors.Is(err, ErrManagerInvalidDestination) {
		t.Fatalf("err = %v, want ErrManagerInvalidDestination", err)
	}
}

func TestCopyNativePathManagerItem_NoStreamingFallbackOnError(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{}
	sentinel := errors.New("provider native failure")

	_, err := copyNativePathManagerItem(ctx, provider, ManagerLocator{Path: "/source/file.txt"}, ManagerLocator{Path: "/backup"}, "file.txt", ManagerMutationOptions{}, func(ctx context.Context, source CloudResource, target string, overwrite bool) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestCopyNativePathManagerItemWithSource_DoesNotReinspect(t *testing.T) {
	ctx := context.Background()
	provider := &mockStorageProvider{}
	source := CloudResource{Path: "/source/prevalidated.txt", Name: "prevalidated.txt", Size: 50, IsDir: false}

	res, err := copyNativePathManagerItemWithSource(ctx, provider, source, ManagerLocator{Path: "/source/prevalidated.txt"}, ManagerLocator{Path: "/backup"}, "prevalidated.txt", ManagerMutationOptions{}, func(ctx context.Context, s CloudResource, target string, overwrite bool) error {
		if s.Path != source.Path {
			t.Errorf("s.Path = %q, want %q", s.Path, source.Path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.inspectCount != 0 {
		t.Errorf("InspectResource was called %d times, want 0", provider.inspectCount)
	}
	if res.Status != "copied" || !res.Native {
		t.Errorf("unexpected result: %+v", res)
	}
}
