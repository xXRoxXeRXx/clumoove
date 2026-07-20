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
	ETag         string       `json:"etag,omitempty"`
	LastModified time.Time    `json:"last_modified"`
	Metadata     FileMetadata `json:"metadata"`
}

type FileInfo struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	Hash    string    `json:"hash"`
	IsDir   bool      `json:"is_dir"`
}

type MetadataApplier interface {
	ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error
}

// StorageProvider is the contract every storage backend must satisfy. It is a
// required (non-optional) interface: adding or removing a method changes the
// signature for ALL implementers at once, so any new provider — and any test
// mock — must implement every method, including SupportsAtomicRename. Forgetting
// a method fails the build with "does not implement storage.StorageProvider
// (missing method <Name>)"; there is no default implementation.
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
	// SupportsAtomicRename reports whether the provider honours the
	// processor's "upload to <path>.tmp then atomically rename" overwrite
	// pattern. Providers that cannot rename (e.g. Google Photos, which has no
	// rename/delete operation and writes the media item to its final album+name
	// during upload) must return false. The processor then skips the delete+rename
	// step and relies on the provider having already written to the final name.
	SupportsAtomicRename() bool
}
