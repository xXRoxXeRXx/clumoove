package storage

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// LocalProvider implements StorageProvider for a server-wide sandbox directory
// defined by the LOCAL_STORAGE_ROOT env var. It supports only "files" and
// carries no credentials. All access is rooted at LOCAL_STORAGE_ROOT; relative
// paths supplied by the user are joined to the root and verified to stay within
// it (rejecting ".." traversal and symlink-based escapes).
type LocalProvider struct {
	root string
}

func NewLocalProvider() (*LocalProvider, error) {
	root := os.Getenv("LOCAL_STORAGE_ROOT")
	if root == "" {
		return nil, fmt.Errorf("LOCAL_STORAGE_ROOT is not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve LOCAL_STORAGE_ROOT: %w", err)
	}
	// Canonicalize the root so that later EvalSymlinks results (which always
	// return a canonical path) compare cleanly against it via string prefix.
	if canon, cerr := filepath.EvalSymlinks(abs); cerr == nil {
		abs = canon
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("LOCAL_STORAGE_ROOT is not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("LOCAL_STORAGE_ROOT is not a directory: %s", abs)
	}
	return &LocalProvider{root: abs}, nil
}

// resolve joins a user-supplied relative path to the storage root and verifies
// it stays within the root. It rejects ".." traversal and symlink escapes.
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

func (p *LocalProvider) Close() error { return nil }

func (p *LocalProvider) Connect(ctx context.Context) (bool, error) {
	if _, err := os.Stat(p.root); err != nil {
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
	resolved, err := p.resolve(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	tmp := resolved + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, stream); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, resolved)
}

func (p *LocalProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.resolve(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return err
	}
	tmp := resolved + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := make([]byte, 32*1024)
	var written int64
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				os.Remove(tmp)
				return werr
			}
			written += int64(n)
			if progressChan != nil {
				progressChan <- int64(n)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			os.Remove(tmp)
			return rerr
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, resolved)
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
	resolved, err := p.resolve(filePath)
	if err != nil {
		return err
	}
	if resolved == p.root {
		return fmt.Errorf("cannot delete the storage root")
	}
	if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
	resolved, err := p.resolve(filePath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(resolved)
	if dir == p.root {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func (p *LocalProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	resolved, err := p.resolve(dirPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(resolved, 0o755); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func (p *LocalProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by local provider", resourceType)
	}
	oldResolved, err := p.resolve(oldPath)
	if err != nil {
		return err
	}
	if oldResolved == p.root {
		return fmt.Errorf("cannot rename the storage root")
	}
	newResolved, err := p.resolve(newPath)
	if err != nil {
		return err
	}
	if newResolved == p.root {
		return fmt.Errorf("cannot rename into the storage root")
	}
	if err := os.MkdirAll(filepath.Dir(newResolved), 0o755); err != nil {
		return err
	}
	return os.Rename(oldResolved, newResolved)
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
