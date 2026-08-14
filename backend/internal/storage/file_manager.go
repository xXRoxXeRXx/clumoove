package storage

import (
	"context"
	"io"
	"time"
)

// ManagerCapabilities are intentionally independent from StorageProvider. A
// transfer primitive existing on a provider is not evidence that its interactive
// file-manager equivalent is safe or semantically supported.
type ManagerCapabilities struct {
	Browse                   bool `json:"browse"`
	NativePagination         bool `json:"native_pagination"`
	Download                 bool `json:"download"`
	Upload                   bool `json:"upload"`
	Mkdir                    bool `json:"mkdir"`
	Rename                   bool `json:"rename"`
	Move                     bool `json:"move"`
	DeleteFile               bool `json:"delete_file"`
	DeleteEmptyDirectory     bool `json:"delete_empty_directory"`
	DeleteRecursiveDirectory bool `json:"delete_recursive_directory"`
	ConflictSkip             bool `json:"conflict_skip"`
	ConflictOverwrite        bool `json:"conflict_overwrite"`
	ConflictRename           bool `json:"conflict_rename"`
	NativeCopy               bool `json:"native_copy"`
	RangeDownload            bool `json:"range_download"`
	Thumbnails               bool `json:"thumbnails"`
}

type ManagerLocator struct {
	Path     string `json:"path,omitempty"`
	NativeID string `json:"native_id,omitempty"`
	Library  string `json:"library,omitempty"`
}

type ManagerItem struct {
	Locator  ManagerLocator
	Name     string
	IsDir    bool
	Size     int64
	Modified time.Time
	MIMEType string
}

type ManagerListOptions struct {
	Cursor string
	Limit  int
}

type ManagerPage struct {
	Items      []ManagerItem
	NextCursor string
}

// ManagerDownload carries both a verified item description and its content
// stream. The manager handler must not reconstruct an item from a display path
// after the user has selected it.
type ManagerDownload struct {
	Item   ManagerItem
	Stream io.ReadCloser
}

type ManagerBreadcrumb struct {
	Name    string
	Locator ManagerLocator
}

// The optional contracts below deliberately remain separate from the
// migration-oriented StorageProvider interface. Providers opt in only after the
// corresponding manager operation has dedicated behavior and tests.
type ManagerLister interface {
	ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error)
}

type ManagerDownloader interface {
	DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error)
}

// ManagerPathResolver resolves a server-provided path to stable manager
// locators. It is intentionally separate from listing: path-based resolution
// is unsafe for providers that permit ambiguous sibling names unless the
// provider can reject that ambiguity itself.
type ManagerPathResolver interface {
	ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error)
}

type ManagerUploader interface {
	UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64) error
}

type ManagerDirectoryCreator interface {
	CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error
}

type ManagerMover interface {
	MoveManagerItem(ctx context.Context, locator, destination ManagerLocator, newName string) error
}

type ManagerDeleter interface {
	DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error
}
