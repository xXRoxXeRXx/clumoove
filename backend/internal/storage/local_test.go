//go:build !windows

package storage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestLocalProvider(t *testing.T) *LocalProvider {
	t.Helper()
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_ROOT", root)
	p, err := NewLocalProvider("user-a")
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestLocalProviderTraversalRejected(t *testing.T) {
	for _, path := range []string{"../escape", "a/../../escape"} {
		if _, err := localPathComponents(path); err == nil {
			t.Fatalf("expected traversal rejection for %q, got nil", path)
		}
	}
}

func TestLocalProviderUploadDownloadRoundtrip(t *testing.T) {
	p := newTestLocalProvider(t)
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
	p := newTestLocalProvider(t)
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
	list, err = p.GetDirectoryListing(ctx, "files", "/docs//")
	if err != nil || len(list) != 1 || list[0].Path != "docs/a.txt" {
		t.Fatalf("normalized listing: %+v, err=%v", list, err)
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
	p := newTestLocalProvider(t)
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
	if _, err := NewLocalProvider("user-a"); err == nil {
		t.Fatalf("expected error when LOCAL_STORAGE_ROOT unset")
	}
}

func TestLocalProviderIsolatesUserNamespaces(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_ROOT", root)
	userA, err := NewLocalProvider("user-a")
	if err != nil {
		t.Fatalf("NewLocalProvider user-a: %v", err)
	}
	userB, err := NewLocalProvider("user-b")
	if err != nil {
		t.Fatalf("NewLocalProvider user-b: %v", err)
	}
	ctx := context.Background()
	content := []byte("tenant-a-only")
	if err := userA.StreamUpload(ctx, "files", "private/data.txt", bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatalf("user A upload: %v", err)
	}
	if _, err := userB.StreamDownload(ctx, "files", "private/data.txt"); !os.IsNotExist(err) {
		t.Fatalf("user B accessed user A file, err=%v", err)
	}
	if err := userB.DeleteFile(ctx, "files", "../user-a/private/data.txt"); err == nil {
		t.Fatal("user B cross-tenant delete attempt was not rejected")
	}
	if exists, _, err := userA.FileExists(ctx, "files", "private/data.txt"); err != nil || !exists {
		t.Fatalf("user A file was affected by user B: exists=%v err=%v", exists, err)
	}
}

func TestNewLocalProviderRejectsUnsafeUserScope(t *testing.T) {
	t.Setenv("LOCAL_STORAGE_ROOT", t.TempDir())
	for _, userID := range []string{"", "..", "../other", "a/b"} {
		if _, err := NewLocalProvider(userID); err == nil {
			t.Fatalf("expected user scope %q to be rejected", userID)
		}
	}
}

func TestLocalProviderRejectsSymlinkedWriteParent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_ROOT", root)
	p, err := NewLocalProvider("user-a")
	if err != nil {
		t.Fatalf("NewLocalProvider: %v", err)
	}
	defer p.Close()

	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(p.root, "swapped")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := p.StreamUpload(context.Background(), "files", "swapped/outside.txt", bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("StreamUpload unexpectedly followed a symlinked parent")
	}
	if _, err := os.Stat(filepath.Join(escape, "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("write escaped local storage root: %v", err)
	}
}

func TestLocalProviderSymlinkEscapeRejected(t *testing.T) {
	p := newTestLocalProvider(t)
	outside := t.TempDir()
	linkPath := filepath.Join(p.root, "link")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	if _, err := p.StreamDownload(context.Background(), "files", "link/secret"); err == nil {
		t.Fatalf("expected symlink escape rejection")
	}
}

func TestLocalProviderSymlinkParentRejected(t *testing.T) {
	p := newTestLocalProvider(t)
	sub := filepath.Join(p.root, "real")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(p.root, "link")
	if err := os.Symlink(sub, linkPath); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}
	if _, err := p.StreamDownload(context.Background(), "files", "link/missing"); err == nil {
		t.Fatal("StreamDownload unexpectedly followed an in-root symlink")
	}
}

func TestLocalProviderReadOperationsRejectReplacedParent(t *testing.T) {
	p := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.CreateDirectory(ctx, "files", "safe"); err != nil {
		t.Fatalf("CreateDirectory: %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Remove(filepath.Join(p.root, "safe")); err != nil {
		t.Fatalf("remove original directory: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(p.root, "safe")); err != nil {
		t.Skipf("symlink not supported on this platform: %v", err)
	}

	if _, err := p.StreamDownload(ctx, "files", "safe/secret.txt"); err == nil {
		t.Fatal("StreamDownload followed a replaced symlink")
	}
	if _, err := p.InspectResource(ctx, "files", "safe/secret.txt"); err == nil {
		t.Fatal("InspectResource followed a replaced symlink")
	}
	if _, _, err := p.FileExists(ctx, "files", "safe/secret.txt"); err == nil {
		t.Fatal("FileExists followed a replaced symlink")
	}
	if _, err := p.GetFileHash(ctx, "files", "safe/secret.txt"); err == nil {
		t.Fatal("GetFileHash followed a replaced symlink")
	}
	if _, err := p.GetDirectoryListing(ctx, "files", "safe"); err == nil {
		t.Fatal("GetDirectoryListing followed a replaced symlink")
	}
}

func TestLocalProviderRootListing(t *testing.T) {
	p := newTestLocalProvider(t)
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
	p := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.CreateParentDirectories(ctx, "files", "a/b/c/file.txt"); err != nil {
		t.Fatalf("CreateParentDirectories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.root, "a", "b", "c")); err != nil {
		t.Fatalf("parent dirs not created: %v", err)
	}
}

func TestLocalProviderDeleteRootRejected(t *testing.T) {
	p := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.DeleteFile(ctx, "files", ""); err == nil {
		t.Fatalf("expected root deletion rejection")
	}
}

func TestLocalProviderOperationsAfterClose(t *testing.T) {
	p := newTestLocalProvider(t)
	ctx := context.Background()
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	operations := []struct {
		name string
		run  func() error
	}{
		{"GetDirectoryListing", func() error {
			_, err := p.GetDirectoryListing(ctx, "files", "")
			return err
		}},
		{"InspectResource", func() error {
			_, err := p.InspectResource(ctx, "files", "file.txt")
			return err
		}},
		{"StreamDownload", func() error {
			_, err := p.StreamDownload(ctx, "files", "file.txt")
			return err
		}},
		{"StreamUpload", func() error {
			return p.StreamUpload(ctx, "files", "file.txt", bytes.NewReader(nil), 0)
		}},
		{"StreamUploadChunked", func() error {
			return p.StreamUploadChunked(ctx, "files", "file.txt", bytes.NewReader(nil), 0, nil)
		}},
		{"FileExists", func() error {
			_, _, err := p.FileExists(ctx, "files", "file.txt")
			return err
		}},
		{"DeleteFile", func() error {
			return p.DeleteFile(ctx, "files", "file.txt")
		}},
		{"GetFileHash", func() error {
			_, err := p.GetFileHash(ctx, "files", "file.txt")
			return err
		}},
		{"CreateParentDirectories", func() error {
			return p.CreateParentDirectories(ctx, "files", "dir/file.txt")
		}},
		{"CreateDirectory", func() error {
			return p.CreateDirectory(ctx, "files", "dir")
		}},
		{"RenameFile", func() error {
			return p.RenameFile(ctx, "files", "old.txt", "new.txt")
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err == nil || err.Error() != "local provider is closed" {
				t.Fatalf("error = %v, want local provider is closed", err)
			}
		})
	}
	if connected, err := p.Connect(ctx); connected || err == nil || err.Error() != "local provider is closed" {
		t.Fatalf("Connect = (%v, %v), want (false, local provider is closed)", connected, err)
	}
}

func TestLocalProviderConcurrentClose(t *testing.T) {
	p := newTestLocalProvider(t)
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range 100 {
			_, _, _ = p.FileExists(ctx, "files", "file.txt")
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = p.Close()
	}()
	close(start)
	wg.Wait()
}
