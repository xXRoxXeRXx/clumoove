package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"testing"

	"backend/internal/backuprepo"
	"backend/internal/storage"
)

func TestPreviewTargetPath(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		relative string
		want     string
		wantErr  bool
	}{
		{name: "root slash", root: "/", relative: "file.txt", want: "/file.txt"},
		{name: "nested under target", root: "/target", relative: "sub/file.txt", want: "/target/sub/file.txt"},
		{name: "trailing slash on target", root: "/target/", relative: "sub/file.txt", want: "/target/sub/file.txt"},
		{name: "empty relative", root: "/target", relative: "", want: "/target"},
		{name: "root empty relative", root: "/", relative: "", want: "/"},
		{name: "clean sub traversal safe", root: "/target", relative: "a/../b/file.txt", want: "/target/b/file.txt"},
		{name: "escape target root", root: "/target", relative: "../../etc/passwd", wantErr: true},
		{name: "escape target parent", root: "/target", relative: "..", wantErr: true},
		{name: "escape nested parent", root: "/target/folder", relative: "../../other", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := previewTargetPath(tt.root, tt.relative)
			if tt.wantErr {
				if err == nil || !errors.Is(err, storage.ErrPathEscapesRoot) {
					t.Fatalf("previewTargetPath(%q, %q) error = %v, want ErrPathEscapesRoot", tt.root, tt.relative, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("previewTargetPath(%q, %q) unexpected error: %v", tt.root, tt.relative, err)
			}
			if got != tt.want {
				t.Fatalf("previewTargetPath(%q, %q) = %q, want %q", tt.root, tt.relative, got, tt.want)
			}
		})
	}
}

func TestTrimExtension(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"file.txt", "file"},
		{"archive.tar.gz", "archive.tar"},
		{"noext", "noext"},
		{".hidden", ""},
		{"file.with.many.dots.pdf", "file.with.many.dots"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := trimExtension(tt.input); got != tt.want {
				t.Fatalf("trimExtension(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShortRestoreID(t *testing.T) {
	if got := shortRestoreID("12345678-90ab-cdef-1234-567890abcdef"); got != "12345678" {
		t.Fatalf("shortRestoreID() = %q, want 12345678", got)
	}
	if got := shortRestoreID("short"); got != "short" {
		t.Fatalf("shortRestoreID() = %q, want short", got)
	}
}

func TestIsPermanentRestoreError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{ErrRepositoryCorrupt, true},
		{ErrRestoreTypeConflict, true},
		{storage.ErrAuth, true},
		{storage.ErrPermanentTransfer, true},
		{storage.ErrUnsupportedResourceType, true},
		{storage.ErrPathEscapesRoot, true},
		{storage.ErrUnsupportedOnPlatform, true},
		{errors.New("transient network timeout"), false},
		{io.ErrUnexpectedEOF, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%v", tt.err), func(t *testing.T) {
			if got := isPermanentRestoreError(tt.err); got != tt.want {
				t.Fatalf("isPermanentRestoreError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type mockRestoreTarget struct {
	files            map[string][]byte
	dirs             map[string]bool
	atomicRename     bool
	verificationMode storage.VerificationMode
}

func newMockRestoreTarget() *mockRestoreTarget {
	return &mockRestoreTarget{
		files:            make(map[string][]byte),
		dirs:             map[string]bool{"/": true},
		atomicRename:     true,
		verificationMode: storage.VerificationCryptographicHash,
	}
}

func (m *mockRestoreTarget) Close() error                              { return nil }
func (m *mockRestoreTarget) Connect(ctx context.Context) (bool, error) { return true, nil }
func (m *mockRestoreTarget) InspectResource(ctx context.Context, resourceType, path string) (storage.CloudResource, error) {
	if m.dirs[path] {
		return storage.CloudResource{Path: path, IsDir: true}, nil
	}
	if data, ok := m.files[path]; ok {
		return storage.CloudResource{Path: path, IsDir: false, Size: int64(len(data))}, nil
	}
	return storage.CloudResource{}, errors.New("not found")
}
func (m *mockRestoreTarget) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	data, ok := m.files[filePath]
	if !ok {
		return nil, errors.New("file not found")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *mockRestoreTarget) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, stream); err != nil {
		return err
	}
	m.files[filePath] = buf.Bytes()
	return nil
}
func (m *mockRestoreTarget) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return m.StreamUpload(ctx, resourceType, filePath, stream, size)
}
func (m *mockRestoreTarget) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	delete(m.files, filePath)
	return nil
}
func (m *mockRestoreTarget) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	data, ok := m.files[filePath]
	if !ok {
		return "", errors.New("not found")
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("SHA256:%x", h), nil
}
func (m *mockRestoreTarget) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return nil
}
func (m *mockRestoreTarget) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	m.dirs[dirPath] = true
	return nil
}
func (m *mockRestoreTarget) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if data, ok := m.files[oldPath]; ok {
		delete(m.files, oldPath)
		m.files[newPath] = data
		return nil
	}
	if m.dirs[oldPath] {
		delete(m.dirs, oldPath)
		m.dirs[newPath] = true
		return nil
	}
	return errors.New("rename source not found")
}
func (m *mockRestoreTarget) SupportsAtomicRename() bool { return m.atomicRename }
func (m *mockRestoreTarget) VerificationMode() storage.VerificationMode {
	return m.verificationMode
}
func (m *mockRestoreTarget) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]storage.CloudResource, error) {
	var results []storage.CloudResource
	for p := range m.dirs {
		if p != dirPath && (dirPath == "/" || p != "/") {
			results = append(results, storage.CloudResource{Path: p, IsDir: true})
		}
	}
	for p, data := range m.files {
		results = append(results, storage.CloudResource{Path: p, IsDir: false, Size: int64(len(data))})
	}
	return results, nil
}
func (m *mockRestoreTarget) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if m.dirs[filePath] {
		return true, 0, nil
	}
	if data, ok := m.files[filePath]; ok {
		return true, int64(len(data)), nil
	}
	return false, 0, nil
}

func TestPromoteRestoreUpload(t *testing.T) {
	ctx := context.Background()

	t.Run("upload new file promotes directly", func(t *testing.T) {
		target := newMockRestoreTarget()
		uploadPath := "/dest.txt.clumoove-restore-12345678"
		targetPath := "/dest.txt"
		backupPath := "/dest.txt.clumoove-restore-backup-12345678"

		target.files[uploadPath] = []byte("new file content")

		if err := promoteRestoreUpload(ctx, target, uploadPath, targetPath, backupPath, false); err != nil {
			t.Fatalf("promoteRestoreUpload() error = %v", err)
		}
		if _, ok := target.files[uploadPath]; ok {
			t.Fatal("staged upload path was not renamed")
		}
		if data, ok := target.files[targetPath]; !ok || string(data) != "new file content" {
			t.Fatalf("target file data = %q, want 'new file content'", string(data))
		}
	})

	t.Run("overwrite existing file backs up and promotes", func(t *testing.T) {
		target := newMockRestoreTarget()
		uploadPath := "/existing.txt.clumoove-restore-12345678"
		targetPath := "/existing.txt"
		backupPath := "/existing.txt.clumoove-restore-backup-12345678"

		target.files[targetPath] = []byte("old content")
		target.files[uploadPath] = []byte("new content")

		if err := promoteRestoreUpload(ctx, target, uploadPath, targetPath, backupPath, true); err != nil {
			t.Fatalf("promoteRestoreUpload() error = %v", err)
		}
		if _, ok := target.files[backupPath]; ok {
			t.Fatal("backup file was not cleaned up after successful overwrite")
		}
		if data, ok := target.files[targetPath]; !ok || string(data) != "new content" {
			t.Fatalf("target file data = %q, want 'new content'", string(data))
		}
	})
}

func TestReconstructAndValidateFullFlow(t *testing.T) {
	blockData := []byte("restore full flow block")
	blockHash := sha256.Sum256(blockData)

	var packBuf bytes.Buffer
	pack, err := backuprepo.EncodePack(&packBuf, []backuprepo.Entry{{Hash: blockHash, Data: blockData}})
	if err != nil {
		t.Fatal(err)
	}

	var offset int64
	_, err = backuprepo.ValidatePack(bytes.NewReader(packBuf.Bytes()), pack.ID, func(off int64, _ backuprepo.Entry) error {
		offset = off
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	fileHash := sha256.Sum256(blockData)
	var reconstructed bytes.Buffer
	err = ReconstructFile(context.Background(), &reconstructed, []BlockRecipe{
		{PackPath: "/packs/test.pack", PackSHA256: pack.ID, PayloadOffset: offset, PayloadLength: len(blockData), PlaintextSize: len(blockData), BlockSHA256: blockHash},
	}, int64(len(blockData)), fileHash, func(_ context.Context, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packBuf.Bytes())), nil
	})
	if err != nil {
		t.Fatalf("ReconstructFile() error = %v", err)
	}
	if !bytes.Equal(reconstructed.Bytes(), blockData) {
		t.Fatalf("reconstructed = %q, want %q", reconstructed.Bytes(), blockData)
	}
}
