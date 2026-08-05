//go:build !windows

package storage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// localRoot anchors local-provider writes at an already-open tenant directory.
// openat(2) with O_NOFOLLOW is used for every intermediate component, so a
// concurrent rename or symlink replacement cannot redirect a write outside it.
type localRoot struct {
	mu sync.Mutex
	fd int
}

func openLocalRoot(path string) (*localRoot, error) {
	fd, err := openAbsoluteDirectory(path)
	if err != nil {
		return nil, err
	}
	return &localRoot{fd: fd}, nil
}

// openAbsoluteDirectory walks an absolute directory from the filesystem root,
// retaining a descriptor at every step.  unix.Open(path, O_NOFOLLOW) protects
// only path's final component: a rename that replaces an intermediate
// directory with a symlink can still be followed while the kernel resolves the
// pathname.  Resolving one component at a time relative to an already-open
// descriptor closes that TOCTOU window for the configured storage root too.
func openAbsoluteDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) {
		return -1, fmt.Errorf("local storage root must be absolute")
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(os.PathSeparator)), string(os.PathSeparator))
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if len(parts) == 1 && parts[0] == "" {
		return fd, nil
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return -1, fmt.Errorf("invalid local storage root")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

// ensureLocalRoot creates the server-derived users/<id> components while the
// parent is held by descriptor. This also avoids a race during provider setup.
func ensureLocalRoot(container, userID string) (*localRoot, error) {
	containerRoot, err := openLocalRoot(container)
	if err != nil {
		return nil, err
	}
	defer containerRoot.close()
	fd, err := containerRoot.directory([]string{"users", userID}, true)
	if err != nil {
		return nil, err
	}
	return &localRoot{fd: fd}, nil
}

func (r *localRoot) close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	return err
}

func (r *localRoot) healthy() error {
	if r == nil {
		return fmt.Errorf("local provider is closed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fd < 0 {
		return fmt.Errorf("local provider is closed")
	}
	var stat unix.Stat_t
	return unix.Fstat(r.fd, &stat)
}

func (r *localRoot) directory(parts []string, create bool) (int, error) {
	r.mu.Lock()
	if r.fd < 0 {
		r.mu.Unlock()
		return -1, fmt.Errorf("local provider is closed")
	}
	// Do not dup the tenant-root descriptor: dup(2) shares its directory
	// stream position, so ReadDir on a root listing can make future listings
	// appear empty. Opening "." through the anchored descriptor gives this
	// operation an independent file description without path re-resolution.
	fd, err := unix.Openat(r.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	r.mu.Unlock()
	if err != nil {
		return -1, err
	}
	for _, part := range parts {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil && create && openErr == unix.ENOENT {
			if err := unix.Mkdirat(fd, part, 0o755); err != nil && err != unix.EEXIST {
				unix.Close(fd)
				return -1, err
			}
			// Retry through the same parent descriptor. The current fd is closed
			// below whether this succeeds or fails.
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func (r *localRoot) mkdirAll(parts []string) error {
	fd, err := r.directory(parts, true)
	if err == nil {
		unix.Close(fd)
	}
	return err
}

func (r *localRoot) openDirectory(parts []string) (*os.File, error) {
	fd, err := r.directory(parts, false)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "local directory"), nil
}

func (r *localRoot) open(parts []string) (*os.File, error) {
	if len(parts) == 0 {
		return r.openDirectory(nil)
	}
	parent, err := r.directory(parts[:len(parts)-1], false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), parts[len(parts)-1]), nil
}

func (r *localRoot) upload(ctx context.Context, parts []string, stream io.Reader, progress chan<- int64) error {
	if len(parts) == 0 {
		return fmt.Errorf("cannot upload to the storage root")
	}
	parent, err := r.directory(parts[:len(parts)-1], true)
	if err != nil {
		return err
	}
	// Register parent close first: the cleanup defer below runs before it and
	// can therefore unlink a failed temporary file using this descriptor.
	defer unix.Close(parent)

	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmpName := fmt.Sprintf(".%s.tmp-%x", parts[len(parts)-1], nonce)
	fd, err := unix.Openat(parent, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	ok := false
	defer func() {
		if !ok {
			_ = unix.Unlinkat(parent, tmpName, 0)
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			tmp.Close()
			return err
		}
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return err
			}
			if progress != nil {
				select {
				case progress <- int64(n):
				case <-ctx.Done():
					tmp.Close()
					return ctx.Err()
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmp.Close()
			return readErr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := unix.Renameat(parent, tmpName, parent, parts[len(parts)-1]); err != nil {
		return err
	}
	ok = true
	return nil
}

func (r *localRoot) remove(parts []string) error {
	// DeleteFile is file-only; unlinkat deliberately does not remove directories.
	parent, err := r.directory(parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	err = unix.Unlinkat(parent, parts[len(parts)-1], 0)
	if err == unix.ENOENT {
		return nil
	}
	return err
}

func (r *localRoot) rename(oldParts, newParts []string) error {
	oldParent, err := r.directory(oldParts[:len(oldParts)-1], false)
	if err != nil {
		return err
	}
	defer unix.Close(oldParent)
	newParent, err := r.directory(newParts[:len(newParts)-1], true)
	if err != nil {
		return err
	}
	defer unix.Close(newParent)
	return unix.Renameat(oldParent, oldParts[len(oldParts)-1], newParent, newParts[len(newParts)-1])
}

func (r *localRoot) chtimes(parts []string, modTime time.Time) error {
	if len(parts) == 0 {
		return nil
	}
	parent, err := r.directory(parts[:len(parts)-1], false)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	ts := []unix.Timespec{
		unix.NsecToTimespec(time.Now().UnixNano()),
		unix.NsecToTimespec(modTime.UnixNano()),
	}
	return unix.UtimesNanoAt(parent, parts[len(parts)-1], ts, 0)
}
