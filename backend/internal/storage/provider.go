package storage

import (
	"context"
	"errors"
	"io"
	"time"
)

var ErrDuplicateUID = errors.New("sabredav: duplicate UID index violation")

// ErrAuth is returned by any provider when the server rejects credentials (HTTP 401).
// Use errors.Is to detect it rather than substring-matching error strings.
var ErrAuth = errors.New("authentication failed: invalid credentials")

type FileMetadata struct {
	ModifiedTime time.Time         `json:"modified_time,omitempty"`
	Description  string            `json:"description,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Starred      bool              `json:"starred,omitempty"`
	CustomProps  map[string]string `json:"custom_props,omitempty"`
}

type CloudResource struct {
	Path         string       `json:"path"`
	Name         string       `json:"name"`
	Size         int64        `json:"size"`
	IsDir        bool         `json:"is_dir"`
	Hash         string       `json:"hash"`
	LastModified time.Time    `json:"last_modified"`
	Metadata     FileMetadata `json:"metadata"`
}

type MetadataApplier interface {
	ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error
}

type StorageProvider interface {
	// Close releases any idle connections held by the provider's HTTP client.
	// It must be called when the provider is no longer needed.
	Close() error
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
