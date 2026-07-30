//go:build windows

package storage

import (
	"fmt"
	"io"
	"os"
)

// Windows does not expose an openat-style API through the Go standard library.
// Refuse local-provider mutations there until a handle-relative implementation
// is available; falling back to path-based operations would reintroduce the
// symlink race this provider is intended to prevent.
type localRoot struct{}

func openLocalRoot(path string) (*localRoot, error) { return &localRoot{}, nil }
func ensureLocalRoot(container, userID string) (*localRoot, error) {
	return nil, fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) close() error { return nil }
func (r *localRoot) healthy() error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) mkdirAll(parts []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) openDirectory(parts []string) (*os.File, error) {
	return nil, fmt.Errorf("local provider reads are unsupported on Windows")
}
func (r *localRoot) open(parts []string) (*os.File, error) {
	return nil, fmt.Errorf("local provider reads are unsupported on Windows")
}
func (r *localRoot) upload(parts []string, stream io.Reader, progress chan<- int64) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) remove(parts []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) rename(oldParts, newParts []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
