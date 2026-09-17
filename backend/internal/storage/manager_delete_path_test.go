package storage

import (
	"context"
	"errors"
	"testing"
)

type deleteTrackingProvider struct {
	*mockStorageProvider
	deletedFiles []string
}

func (p *deleteTrackingProvider) DeleteFile(_ context.Context, _ string, filePath string) error {
	p.deletedFiles = append(p.deletedFiles, filePath)
	return nil
}

func TestDeleteManagerPathItemWithDirectoryDeleter(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		locator       ManagerLocator
		resource      CloudResource
		children      []CloudResource
		recursive     bool
		wantErr       error
		wantFiles     []string
		wantDirectory bool
	}{
		{
			name:      "file uses file deletion",
			locator:   ManagerLocator{Path: "/report.txt"},
			resource:  CloudResource{Path: "/report.txt", IsDir: false},
			wantFiles: []string{"/report.txt"},
		},
		{
			name:          "empty directory uses directory deletion",
			locator:       ManagerLocator{Path: "/empty"},
			resource:      CloudResource{Path: "/empty", IsDir: true},
			wantDirectory: true,
		},
		{
			name:     "non-empty directory is rejected",
			locator:  ManagerLocator{Path: "/full"},
			resource: CloudResource{Path: "/full", IsDir: true},
			children: []CloudResource{{Path: "/full/file.txt"}},
			wantErr:  ErrManagerDirectoryNotEmpty,
		},
		{
			name:          "recursive directory bypasses empty check",
			locator:       ManagerLocator{Path: "/full"},
			resource:      CloudResource{Path: "/full", IsDir: true},
			children:      []CloudResource{{Path: "/full/file.txt"}},
			recursive:     true,
			wantDirectory: true,
		},
		{
			name:    "root is rejected",
			locator: ManagerLocator{Path: "/"},
			wantErr: ErrManagerUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &deleteTrackingProvider{mockStorageProvider: &mockStorageProvider{
				inspectFn: func(context.Context, string, string) (CloudResource, error) { return tt.resource, nil },
				listingFn: func(context.Context, string, string) ([]CloudResource, error) { return tt.children, nil },
			}}
			var directoryPath string
			var directoryRecursive bool
			err := deleteManagerPathItemWithDirectoryDeleter(ctx, provider, tt.locator, tt.recursive, func(_ context.Context, path string, recursive bool) error {
				directoryPath = path
				directoryRecursive = recursive
				return nil
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if len(provider.deletedFiles) != len(tt.wantFiles) {
				t.Fatalf("deleted files = %v, want %v", provider.deletedFiles, tt.wantFiles)
			}
			for i := range tt.wantFiles {
				if provider.deletedFiles[i] != tt.wantFiles[i] {
					t.Fatalf("deleted files = %v, want %v", provider.deletedFiles, tt.wantFiles)
				}
			}
			if tt.wantDirectory != (directoryPath != "") {
				t.Fatalf("directory deletion called for %q, want %t", directoryPath, tt.wantDirectory)
			}
			if tt.wantDirectory && directoryRecursive != tt.recursive {
				t.Fatalf("directory recursive = %t, want %t", directoryRecursive, tt.recursive)
			}
		})
	}
}
