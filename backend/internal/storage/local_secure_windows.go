//go:build windows

package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// Windows does not expose an openat-style API through the Go standard library.
// Refuse local-provider mutations there until a handle-relative implementation
// is available; falling back to path-based operations would reintroduce the
// symlink race this provider is intended to prevent.
type localRoot struct{}

func openLocalRoot(_ string) (*localRoot, error) { return &localRoot{}, nil }
func ensureLocalRoot(_, _ string) (*localRoot, error) {
	return nil, fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) close() error { return nil }
func (r *localRoot) healthy() error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) mkdirAll(_ []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) openDirectory(_ []string) (*os.File, error) {
	return nil, fmt.Errorf("local provider reads are unsupported on Windows")
}
func (r *localRoot) open(_ []string) (*os.File, error) {
	return nil, fmt.Errorf("local provider reads are unsupported on Windows")
}
func (r *localRoot) upload(_ context.Context, _ []string, _ io.Reader, _ chan<- int64) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) remove(_ []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) rename(_, _ []string) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows")
}
func (r *localRoot) chtimes(_ []string, _ time.Time) error {
	return fmt.Errorf("local provider mutations are unsupported on Windows: %w", ErrUnsupportedOnPlatform)
}
