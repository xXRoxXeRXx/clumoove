package storage

import (
	"context"
	"io"
	"time"
)

type CloudResource struct {
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	IsDir        bool      `json:"is_dir"`
	Hash         string    `json:"hash"`
	LastModified time.Time `json:"last_modified"`
}

type StorageProvider interface {
	Connect(ctx context.Context) (bool, error)
	GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error)
	InspectResource(ctx context.Context, resourceType, path string) (CloudResource, error)
	StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error)
	StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error
	StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error
	FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error)
	DeleteFile(ctx context.Context, resourceType, filePath string) error
	GetFileHash(ctx context.Context, resourceType, filePath string) (string, error)
	CreateParentDirectories(ctx context.Context, resourceType, filePath string) error
	CreateDirectory(ctx context.Context, resourceType, dirPath string) error
	RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error
}
