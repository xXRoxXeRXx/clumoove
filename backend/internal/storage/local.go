package storage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

// resolve is used only for read-only operations (listing, download, hash, and
// stat). Mutating operations use localPathComponents and rootHandle to avoid
// TOCTOU races. It joins a user-supplied relative path to the storage root and
// rejects ".." traversal and symlink escapes.
func (p *LocalProvider) resolve(rel string) (string, error) {
	// Reject any explicit parent-directory references up front.
	if strings.Contains(rel, "..") {
		return "", fmt.Errorf("path escapes storage root")
	}
	clean := filepath.Clean(strings.TrimPrefix(rel, "/"))
	if clean == "." || clean == "/" || clean == string(os.PathSeparator) {
		clean = ""
	}
	joined := filepath.Join(p.root, clean)
	if joined != p.root && !strings.HasPrefix(joined, p.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes storage root")
	}

	// Prevent sandbox escape via symlinked intermediate directories: evaluate
	// each existing ancestor against the storage root. Missing components are
	// permitted (they will be created by the upload/mkdir operations).
	cur := p.root
	for _, comp := range strings.Split(clean, string(os.PathSeparator)) {
		if comp == "" || comp == "." {
			continue
		}
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		if err != nil {
			break // not yet created; remaining components cannot be checked
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, rerr := filepath.EvalSymlinks(cur)
			if rerr != nil || (resolved != p.root && !strings.HasPrefix(resolved, p.root+string(os.PathSeparator))) {
				return "", fmt.Errorf("path escapes storage root")
			}
		}
	}
	return joined, nil
}

// secureResolve validates a path like resolve, then re-checks containment after
// fully resolving any symlinks in the final path to close the TOCTOU window
// between resolve() and the actual I/O.
func (p *LocalProvider) secureResolve(rel string) (string, error) {
	joined, err := p.resolve(rel)
	if err != nil {
		return "", err
	}
	resolved, rerr := filepath.EvalSymlinks(joined)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			// Final element may not exist yet (upload target); validate the
			// existing parent directory instead so the new file stays in root.
			parent := filepath.Dir(joined)
			parentResolved, perr := filepath.EvalSymlinks(parent)
			if perr == nil {
				if parentResolved != p.root && !strings.HasPrefix(parentResolved, p.root+string(os.PathSeparator)) {
					return "", fmt.Errorf("path escapes storage root")
				}
			}
			return joined, nil
		}
		return "", fmt.Errorf("path escapes storage root")
	}
	if resolved != p.root && !strings.HasPrefix(resolved, p.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes storage root")
	}
	return joined, nil
}

func (p *LocalProvider) Close() error {
	if p.rootHandle == nil {
		return nil
	}
	err := p.rootHandle.close()
	p.rootHandle = nil
	return err
}

func (p *LocalProvider) Connect(ctx context.Context) (bool, error) {
	if p.rootHandle == nil {
		return false, fmt.Errorf("local provider is closed")
	}
	if err := p.rootHandle.healthy(); err != nil {
		return false, err
	}
	return true, nil
}

func (p *LocalProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.resolve(dirPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	var resources []CloudResource
	for _, e := range entries {
		full := filepath.Join(resolved, e.Name())
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		rel, rerr := filepath.Rel(p.root, full)
		if rerr != nil {
			rel = e.Name()
		}
		res := CloudResource{
			Path:  filepath.ToSlash(rel),
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		}
		res.LastModified = info.ModTime()
		resources = append(resources, res)
	}
	return resources, nil
}

func (p *LocalProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.secureResolve(resourcePath)
	if err != nil {
		return CloudResource{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return CloudResource{}, err
	}
	rel, rerr := filepath.Rel(p.root, resolved)
	if rerr != nil {
		rel = resourcePath
	}
	res := CloudResource{
		Path:         filepath.ToSlash(rel),
		Name:         info.Name(),
		IsDir:        info.IsDir(),
		Size:         info.Size(),
		LastModified: info.ModTime(),
	}
	if !info.IsDir() {
		if h, herr := p.hashFile(resolved); herr == nil {
			res.Hash = "SHA1:" + h
		}
	}
	return res, nil
}

func (p *LocalProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.secureResolve(filePath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (p *LocalProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	return p.rootHandle.upload(parts, stream, nil)
}

func (p *LocalProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	return p.rootHandle.upload(parts, stream, progressChan)
}

func (p *LocalProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.resolve(filePath)
	if err != nil {
		return false, 0, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, info.Size(), nil
}

func (p *LocalProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		return fmt.Errorf("cannot delete the storage root")
	}
	return p.rootHandle.remove(parts)
}

func (p *LocalProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.secureResolve(filePath)
	if err != nil {
		return "", err
	}
	h, err := p.hashFile(resolved)
	if err != nil {
		return "", err
	}
	return "SHA1:" + h, nil
}

func (p *LocalProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	parts, err := localPathComponents(filePath)
	if err != nil {
		return err
	}
	if len(parts) < 2 {
		return nil
	}
	return p.rootHandle.mkdirAll(parts[:len(parts)-1])
}

func (p *LocalProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	parts, err := localPathComponents(dirPath)
	if err != nil {
		return err
	}
	return p.rootHandle.mkdirAll(parts)
}

func (p *LocalProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
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
	return p.rootHandle.rename(oldParts, newParts)
}

// localPathComponents accepts only a relative path and returns components for
// descriptor-relative operations. Unlike resolve, it never returns a pathname
// that a caller could later re-resolve through a swapped symlink. On Unix,
// filepath.Clean output uses '/', matching os.PathSeparator; Windows mutations
// are unavailable until they can use an equivalent handle-relative API.
func localPathComponents(rel string) ([]string, error) {
	if strings.Contains(rel, "..") {
		return nil, fmt.Errorf("path escapes storage root")
	}
	clean := filepath.Clean(strings.TrimPrefix(rel, "/"))
	if clean == "." || clean == string(os.PathSeparator) {
		return nil, nil
	}
	if filepath.IsAbs(clean) {
		return nil, fmt.Errorf("path escapes storage root")
	}
	parts := strings.Split(clean, string(os.PathSeparator))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("path escapes storage root")
		}
	}
	return parts, nil
}

// SupportsAtomicRename is true: the local provider can rename files.
func (p *LocalProvider) SupportsAtomicRename() bool {
	return true
}

func (p *LocalProvider) hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
