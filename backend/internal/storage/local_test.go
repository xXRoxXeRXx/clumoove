package storage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func newTestLocalProvider(t *testing.T) (*LocalProvider, func()) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_ROOT", root)
	p, err := NewLocalProvider()
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	return p, func() {}
}

func TestLocalProviderTraversalRejected(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	if _, err := p.resolve("../escape"); err == nil {
		t.Fatalf("expected traversal rejection, got nil")
	}
	if _, err := p.resolve("a/../../escape"); err == nil {
		t.Fatalf("expected nested traversal rejection, got nil")
	}
}

func TestLocalProviderUploadDownloadRoundtrip(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()
	content := []byte("hello local provider")
	if err := p.StreamUpload(ctx, "files", "sub/dir/file.txt", bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("StreamUpload: %v", err)
	}

	rc, err := p.StreamDownload(ctx, "files", "sub/dir/file.txt")
	if err != nil {
		t.Fatalf("StreamDownload: %v", err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		t.Fatalf("read download: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), content) {
		t.Fatalf("content mismatch: got %q", buf.Bytes())
	}

	sum := sha1.Sum(content)
	expect := "SHA1:" + hex.EncodeToString(sum[:])
	got, err := p.GetFileHash(ctx, "files", "sub/dir/file.txt")
	if err != nil {
		t.Fatalf("GetFileHash: %v", err)
	}
	if got != expect {
		t.Fatalf("hash mismatch: got %q want %q", got, expect)
	}
}

func TestLocalProviderListingExistsDeleteRenameMkdir(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()

	if err := p.CreateDirectory(ctx, "files", "docs"); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}
	// idempotent
	if err := p.CreateDirectory(ctx, "files", "docs"); err != nil {
		t.Fatalf("CreateDirectory idempotent: %v", err)
	}

	if err := p.StreamUpload(ctx, "files", "docs/a.txt", bytes.NewReader([]byte("a")), 1); err != nil {
		t.Fatalf("upload: %v", err)
	}

	exists, size, err := p.FileExists(ctx, "files", "docs/a.txt")
	if err != nil || !exists || size != 1 {
		t.Fatalf("FileExists: exists=%v size=%d err=%v", exists, size, err)
	}

	list, err := p.GetDirectoryListing(ctx, "files", "docs")
	if err != nil {
		t.Fatalf("GetDirectoryListing: %v", err)
	}
	if len(list) != 1 || list[0].Name != "a.txt" {
		t.Fatalf("unexpected listing: %+v", list)
	}

	if err := p.RenameFile(ctx, "files", "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatalf("RenameFile: %v", err)
	}
	exists, _, _ = p.FileExists(ctx, "files", "docs/a.txt")
	if exists {
		t.Fatalf("old name should not exist after rename")
	}
	exists, _, _ = p.FileExists(ctx, "files", "docs/b.txt")
	if !exists {
		t.Fatalf("new name should exist after rename")
	}

	if err := p.DeleteFile(ctx, "files", "docs/b.txt"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	exists, _, _ = p.FileExists(ctx, "files", "docs/b.txt")
	if exists {
		t.Fatalf("file should be deleted")
	}
}

func TestLocalProviderNonFilesRejected(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()
	if _, err := p.GetDirectoryListing(ctx, "calendars", ""); err == nil {
		t.Fatalf("expected calendars rejection")
	}
	if _, err := p.InspectResource(ctx, "contacts", "x"); err == nil {
		t.Fatalf("expected contacts rejection")
	}
}

func TestNewLocalProviderUnconfigured(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_ROOT", "")
	if _, err := NewLocalProvider(); err == nil {
		t.Fatalf("expected error when LOCAL_STORAGE_ROOT unset")
	}
}

func TestLocalProviderSymlinkEscapeRejected(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	outside := t.TempDir()
	linkPath := filepath.Join(p.root, "link")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	if _, err := p.resolve("link/secret"); err == nil {
		t.Fatalf("expected symlink escape rejection")
	}
}

func TestLocalProviderSymlinkInsideRootAllowed(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	// Create a subdir, symlink it from the root, write a file via the symlink.
	sub := filepath.Join(p.root, "real")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(p.root, "link")
	if err := os.Symlink(sub, linkPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	if _, err := p.resolve("link"); err != nil {
		t.Fatalf("symlink inside root should be allowed: %v", err)
	}
}

func TestLocalProviderRootListing(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.StreamUpload(ctx, "files", "rootfile.txt", bytes.NewReader([]byte("x")), 1); err != nil {
		t.Fatalf("upload to root: %v", err)
	}
	list, err := p.GetDirectoryListing(ctx, "files", "")
	if err != nil {
		t.Fatalf("root listing: %v", err)
	}
	if len(list) != 1 || list[0].Name != "rootfile.txt" {
		t.Fatalf("unexpected root listing: %+v", list)
	}
}

func TestLocalProviderCreateParentDirectories(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.CreateParentDirectories(ctx, "files", "a/b/c/file.txt"); err != nil {
		t.Fatalf("CreateParentDirectories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.root, "a", "b", "c")); err != nil {
		t.Fatalf("parent dirs not created: %v", err)
	}
}

func TestLocalProviderDeleteRootRejected(t *testing.T) {
	p, _ := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.DeleteFile(ctx, "files", ""); err == nil {
		t.Fatalf("expected root deletion rejection")
	}
}
