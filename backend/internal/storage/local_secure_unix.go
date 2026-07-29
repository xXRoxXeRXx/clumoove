//go:build !windows

package storage

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// localRoot anchors local-provider writes at an already-open tenant directory.
// openat(2) with O_NOFOLLOW is used for every intermediate component, so a
// concurrent rename or symlink replacement cannot redirect a write outside it.
type localRoot struct{ fd int }

func openLocalRoot(path string) (*localRoot, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return &localRoot{fd: fd}, nil
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
	if r == nil || r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	return err
}

func (r *localRoot) healthy() error {
	if r == nil || r.fd < 0 {
		return fmt.Errorf("local provider is closed")
	}
	var stat unix.Stat_t
	return unix.Fstat(r.fd, &stat)
}

func (r *localRoot) directory(parts []string, create bool) (int, error) {
	fd, err := unix.Dup(r.fd)
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

func (r *localRoot) upload(parts []string, stream io.Reader, progress chan<- int64) error {
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
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return err
			}
			if progress != nil {
				progress <- int64(n)
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
