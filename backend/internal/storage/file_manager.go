package storage

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
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
	ConflictOverwriteAtomic  bool `json:"conflict_overwrite_atomic"`
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

// ManagerConnector performs the smallest connection check needed by the file
// manager. OAuth providers that expose files independently from calendars or
// contacts must not reject a files-only manager request for missing unrelated
// scopes.
type ManagerConnector interface {
	ConnectManager(ctx context.Context) (bool, error)
}

// ManagerPathResolver resolves a server-provided path to stable manager
// locators. It is intentionally separate from listing: path-based resolution
// is unsafe for providers that permit ambiguous sibling names unless the
// provider can reject that ambiguity itself.
type ManagerPathResolver interface {
	ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error)
}

type ManagerUploadOptions struct {
	ConflictStrategy string
}

type ManagerUploadResult struct {
	// Status is one of uploaded, skipped, or renamed.
	Status    string
	FinalName string
}

// ManagerUploader is deliberately separate from StorageProvider's migration
// upload methods. A provider implements it only after its interactive upload
// semantics, conflict handling, and locator safety have been tested.
type ManagerUploader interface {
	UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error)
}

var (
	ErrManagerConflict          = errors.New("file manager conflict")
	ErrManagerUnsupported       = errors.New("file manager operation not supported")
	ErrUploadSizeMismatch       = errors.New("upload size mismatch")
	ErrUnsupportedMedia         = errors.New("unsupported media type")
	ErrManagerDirectoryTooLarge = errors.New("directory too large")
)

// managerLocatorKey returns a stable sort key for a ManagerLocator, consistent
// with the handler-side managedLocatorIdentity used by sortManagedEntries.
func managerLocatorKey(loc ManagerLocator) string {
	if loc.NativeID != "" {
		return "id:" + loc.NativeID
	}
	return "path:" + loc.Library + ":" + loc.Path
}

// sortManagerItems sorts a slice of ManagerItem consistently: directories
// first, then alphabetical by lower-cased trimmed name, then by locator key.
// Uses sort.Slice (O(n log n)) rather than a bubble sort.
func sortManagerItems(items []ManagerItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		left := strings.ToLower(strings.TrimSpace(items[i].Name))
		right := strings.ToLower(strings.TrimSpace(items[j].Name))
		if left != right {
			return left < right
		}
		return managerLocatorKey(items[i].Locator) < managerLocatorKey(items[j].Locator)
	})
}

// ExactSizeReader forwards a stream without buffering while enforcing its
// declared length. Call Verify after the consumer returns to detect a short
// body or trailing bytes when the consumer stopped exactly at its boundary.
type ExactSizeReader struct {
	reader    io.Reader
	remaining int64
	checked   bool
	trailing  error
}

func NewExactSizeReader(reader io.Reader, size int64) *ExactSizeReader {
	return &ExactSizeReader{reader: reader, remaining: size}
}

func (r *ExactSizeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		if err := r.checkTrailing(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.remaining -= int64(n)
	}
	return n, err
}

func (r *ExactSizeReader) Verify() error {
	if r.remaining != 0 {
		return ErrUploadSizeMismatch
	}
	return r.checkTrailing()
}

func (r *ExactSizeReader) checkTrailing() error {
	if r.checked {
		return r.trailing
	}
	r.checked = true
	var extra [1]byte
	n, err := r.reader.Read(extra[:])
	if n > 0 {
		r.trailing = ErrUploadSizeMismatch
		return r.trailing
	}
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	r.trailing = err
	return r.trailing
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

// ManagerThumbnailer generates or fetches a visual thumbnail for a file.
type ManagerThumbnailer interface {
	ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error)
}
