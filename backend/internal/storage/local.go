package storage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// LocalProvider implements StorageProvider for one tenant's sandbox directory
// at LOCAL_STORAGE_ROOT/users/<user-id>. It supports only "files" and carries
// no credentials. Relative paths supplied by the user are joined to that
// tenant root and verified to stay within it (rejecting ".." traversal and
// symlink-based escapes).
type LocalProvider struct {
	root string
	// rootHandle keeps an open descriptor for the tenant root. Mutating
	// operations are resolved relative to it, never by re-opening p.root.
	rootHandle *localRoot
}

var _ StorageProvider = (*LocalProvider)(nil)

func NewLocalProvider(userID string) (*LocalProvider, error) {
	if userID == "" || userID == "." || filepath.Base(userID) != userID || strings.Contains(userID, "..") {
		log.Printf("local provider creation rejected: missing or invalid user scope")
		return nil, fmt.Errorf("local provider requires an authenticated user scope")
	}
	root := os.Getenv("LOCAL_STORAGE_ROOT")
	if root == "" {
		return nil, fmt.Errorf("LOCAL_STORAGE_ROOT is not configured")
	}
	// The server-wide root is only a container. Each tenant is restricted to an
	// immutable, server-derived directory below it; callers must never be able
	// to choose this component through a request path or a connection profile.
	container, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve LOCAL_STORAGE_ROOT: %w", err)
	}
	// Canonicalize the root so that later EvalSymlinks results (which always
	// return a canonical path) compare cleanly against it via string prefix.
	if canon, cerr := filepath.EvalSymlinks(container); cerr == nil {
		container = canon
	}
	abs := filepath.Join(container, "users", userID)
	rootHandle, err := ensureLocalRoot(container, userID)
	if err != nil {
		return nil, fmt.Errorf("local user storage is not accessible: %w", err)
	}
	return &LocalProvider{root: abs, rootHandle: rootHandle}, nil
}

func (p *LocalProvider) Close() error {
	if p.rootHandle == nil {
		return nil
	}
	return p.rootHandle.close()
}

func (p *LocalProvider) Connect(ctx context.Context) (bool, error) {
	root, err := p.localRoot()
	if err != nil {
		return false, err
	}
	if err := root.healthy(); err != nil {
		return false, err
	}
	return true, nil
}

func (p *LocalProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return nil, err
	}
	parts, err := localPathComponents(dirPath)
	if err != nil {
		return nil, err
	}
	dir, err := root.openDirectory(parts)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.Readdir(-1)
	if err != nil {
		return nil, err
	}
	cleanDir := strings.Join(parts, "/")
	var resources []CloudResource
	for _, info := range entries {
		relPath := info.Name()
		if cleanDir != "" {
			relPath = cleanDir + "/" + info.Name()
		}
		res := CloudResource{
			Path:  relPath,
			Name:  info.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		}
		res.LastModified = info.ModTime()
		resources = append(resources, res)
	}
	return resources, nil
}

func (p *LocalProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return CloudResource{}, err
	}
	parts, err := localPathComponents(resourcePath)
	if err != nil {
		return CloudResource{}, err
	}
	f, err := root.open(parts)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CloudResource{}, fmt.Errorf("local inspect: %w", ErrNotFound)
		}
		return CloudResource{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return CloudResource{}, err
	}
	res := CloudResource{
		Path:         filepath.ToSlash(resourcePath),
		Name:         info.Name(),
		IsDir:        info.IsDir(),
		Size:         info.Size(),
		LastModified: info.ModTime(),
	}
	if !info.IsDir() {
		if h, herr := hashReader(f); herr == nil {
			res.Hash = "SHA1:" + h
		}
	}
	return res, nil
}

func (p *LocalProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return nil, err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return nil, err
	}
	f, err := root.open(parts)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// StreamDownloadRange implements RangeDownloader for the local provider. The
// already-open, root-anchored file descriptor preserves the provider's
// symlink-race protections while seeking, so no second path resolution occurs.
func (p *LocalProvider) StreamDownloadRange(ctx context.Context, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	if _, err := ValidateByteRange(offset, length); err != nil {
		return nil, err
	}
	root, err := p.localRoot()
	if err != nil {
		return nil, err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return nil, err
	}
	f, err := root.open(parts)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() || offset > info.Size() || length > info.Size()-offset {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidByteRange
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return newRangedReadCloser(f, length), nil
}

func (p *LocalProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	return root.upload(ctx, parts, stream, nil)
}

func (p *LocalProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	return root.upload(ctx, parts, stream, progressChan)
}

func (p *LocalProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return false, 0, err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return false, 0, err
	}
	f, err := root.open(parts)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, 0, err
	}
	return true, info.Size(), nil
}

func (p *LocalProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("cannot delete the storage root")
	}
	return root.remove(parts)
}

func (p *LocalProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return "", err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return "", err
	}
	f, err := root.open(parts)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h, err := hashReader(f)
	if err != nil {
		return "", err
	}
	return "SHA1:" + h, nil
}

func (p *LocalProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	if len(parts) < 2 {
		return nil
	}
	return root.mkdirAll(parts[:len(parts)-1])
}

func (p *LocalProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(dirPath)
	if err != nil {
		return err
	}
	return root.mkdirAll(parts)
}

func (p *LocalProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("%w: resource type %s not supported by local provider", ErrUnsupportedResourceType, resourceType)
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	oldParts, err := localPathComponents(oldPath)
	if err != nil {
		return err
	}
	if len(oldParts) == 0 {
		return fmt.Errorf("cannot rename the storage root")
	}
	newParts, err := localPathComponents(newPath)
	if err != nil {
		return err
	}
	if len(newParts) == 0 {
		return fmt.Errorf("cannot rename into the storage root")
	}
	return root.rename(oldParts, newParts)
}

func (p *LocalProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	// Local provider supports only "files"; short-circuit for other resource types.
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}
	root, err := p.localRoot()
	if err != nil {
		return err
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	return root.chtimes(parts, meta.ModifiedTime)
}

// localPathComponents accepts only a relative path and returns components for
// descriptor-relative operations. Unlike resolve, it never returns a pathname
// that a caller could later re-resolve through a swapped symlink. On Unix,
// filepath.Clean output uses '/', matching os.PathSeparator; Windows mutations
// are unavailable until they can use an equivalent handle-relative API.
func localPathComponents(rel string) ([]string, error) {
	clean := filepath.Clean(strings.TrimPrefix(rel, "/"))
	if clean == "." || clean == string(os.PathSeparator) {
		return nil, nil
	}
	if filepath.IsAbs(clean) {
		return nil, fmt.Errorf("%w", ErrPathEscapesRoot)
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("%w", ErrPathEscapesRoot)
		}
	}
	return parts, nil
}

// SupportsAtomicRename is true: the local provider can rename files.
func (p *LocalProvider) VerificationMode() VerificationMode { return VerificationCryptographicHash }
func (p *LocalProvider) SupportsAtomicRename() bool {
	return true
}

func (p *LocalProvider) localRoot() (*localRoot, error) {
	if p.rootHandle == nil {
		return nil, fmt.Errorf("local provider is closed")
	}
	return p.rootHandle, nil
}

func hashReader(reader io.Reader) (string, error) {
	h := sha1.New()
	if _, err := io.Copy(h, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
