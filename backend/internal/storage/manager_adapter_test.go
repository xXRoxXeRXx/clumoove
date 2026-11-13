package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"path"
	"runtime"
	"strings"
	"testing"
	"time"
)

type adapterTestProvider struct {
	connectFn         func(ctx context.Context) (bool, error)
	listingFn         func(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error)
	inspectFn         func(ctx context.Context, resourceType, filePath string) (CloudResource, error)
	downloadFn        func(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error)
	uploadFn          func(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error
	uploadChunkedFn   func(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error
	deleteFileFn      func(ctx context.Context, resourceType, filePath string) error
	createDirFn       func(ctx context.Context, resourceType, dirPath string) error
	renameFn          func(ctx context.Context, resourceType, oldPath, newPath string) error
	atomicRename      bool
	uploadCalls       []string
	chunkedCalls      []string
	renameCalls       [][2]string
	deleteFileCalls   []string
	createDirCalls    []string
	listings          map[string][]CloudResource
	files             map[string][]byte
	listingErr        error
	inspectCalledWith []string
}

func newAdapterTestProvider() *adapterTestProvider {
	return &adapterTestProvider{
		listings: make(map[string][]CloudResource),
		files:    make(map[string][]byte),
	}
}

func (p *adapterTestProvider) Close() error { return nil }
func (p *adapterTestProvider) Connect(ctx context.Context) (bool, error) {
	if p.connectFn != nil {
		return p.connectFn(ctx)
	}
	return true, nil
}
func (p *adapterTestProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if p.listingErr != nil {
		return nil, p.listingErr
	}
	if p.listingFn != nil {
		return p.listingFn(ctx, resourceType, dirPath)
	}
	items := p.listings[dirPath]
	out := make([]CloudResource, len(items))
	copy(out, items)
	return out, nil
}
func (p *adapterTestProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	p.inspectCalledWith = append(p.inspectCalledWith, filePath)
	if p.inspectFn != nil {
		return p.inspectFn(ctx, resourceType, filePath)
	}
	for _, items := range p.listings {
		for _, item := range items {
			if item.Path == filePath {
				return item, nil
			}
		}
	}
	if content, ok := p.files[filePath]; ok {
		return CloudResource{Path: filePath, Name: filePath[strings.LastIndex(filePath, "/")+1:], Size: int64(len(content)), LastModified: time.Unix(1700000000, 0).UTC()}, nil
	}
	return CloudResource{}, ErrNotFound
}
func (p *adapterTestProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if p.downloadFn != nil {
		return p.downloadFn(ctx, resourceType, filePath)
	}
	if content, ok := p.files[filePath]; ok {
		return io.NopCloser(strings.NewReader(string(content))), nil
	}
	return nil, ErrNotFound
}
func (p *adapterTestProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	p.uploadCalls = append(p.uploadCalls, filePath)
	if p.uploadFn != nil {
		return p.uploadFn(ctx, resourceType, filePath, stream, size)
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		return err
	}
	p.files[filePath] = data
	return nil
}
func (p *adapterTestProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, _ chan<- int64) error {
	p.chunkedCalls = append(p.chunkedCalls, filePath)
	if p.uploadChunkedFn != nil {
		return p.uploadChunkedFn(ctx, resourceType, filePath, stream, size)
	}
	data, err := io.ReadAll(stream)
	if err != nil {
		return err
	}
	p.files[filePath] = data
	return nil
}
func (p *adapterTestProvider) FileExists(_ context.Context, _ string, filePath string) (bool, int64, error) {
	if content, ok := p.files[filePath]; ok {
		return true, int64(len(content)), nil
	}
	return false, 0, nil
}
func (p *adapterTestProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	p.deleteFileCalls = append(p.deleteFileCalls, filePath)
	if p.deleteFileFn != nil {
		return p.deleteFileFn(ctx, resourceType, filePath)
	}
	delete(p.files, filePath)
	return nil
}
func (p *adapterTestProvider) GetFileHash(context.Context, string, string) (string, error) {
	return "", ErrChecksumNotAvailable
}
func (p *adapterTestProvider) CreateParentDirectories(context.Context, string, string) error {
	return nil
}
func (p *adapterTestProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	p.createDirCalls = append(p.createDirCalls, dirPath)
	if p.createDirFn != nil {
		return p.createDirFn(ctx, resourceType, dirPath)
	}
	return nil
}
func (p *adapterTestProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	p.renameCalls = append(p.renameCalls, [2]string{oldPath, newPath})
	if p.renameFn != nil {
		return p.renameFn(ctx, resourceType, oldPath, newPath)
	}
	if data, ok := p.files[oldPath]; ok {
		p.files[newPath] = data
		delete(p.files, oldPath)
	}
	return nil
}
func (p *adapterTestProvider) SupportsAtomicRename() bool         { return p.atomicRename }
func (p *adapterTestProvider) VerificationMode() VerificationMode { return VerificationSizeOnly }

func TestNewFileManagerRejectsInvalidInputs(t *testing.T) {
	if _, err := NewFileManager("unknown", newAdapterTestProvider()); !errors.Is(err, ErrManagerUnsupported) {
		t.Fatalf("NewFileManager(unknown) error = %v, want ErrManagerUnsupported", err)
	}
	if _, err := NewFileManager("s3", nil); !errors.Is(err, ErrManagerUnsupported) {
		t.Fatalf("NewFileManager(nil) error = %v, want ErrManagerUnsupported", err)
	}
}

func TestFileManagerSafetyAndUnsupportedOperations(t *testing.T) {
	t.Run("virtual providers never fall back to path operations", func(t *testing.T) {
		for _, providerType := range []string{"immich", "google"} {
			stub := newAdapterTestProvider()
			fm, err := NewFileManager(providerType, stub)
			if err != nil {
				t.Fatalf("NewFileManager(%q) error = %v", providerType, err)
			}

			if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s ListManager error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, _, _, err := fm.ResolveManagerPath(context.Background(), "/docs"); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s ResolveManagerPath error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.DownloadManager(context.Background(), ManagerLocator{Path: "/a.txt"}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s DownloadManager error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.InspectManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s InspectManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "a.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s UploadManager error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if err := fm.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "dir"); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s CreateManagerDirectory error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if err := fm.DeleteManagerItem(context.Background(), ManagerLocator{NativeID: "id1"}, false); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s DeleteManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.RenameManagerItem(context.Background(), ManagerLocator{Path: "/a"}, ManagerLocator{Path: "/"}, "b", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s RenameManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s MoveManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
			}
			if _, err := fm.CopyManagerItem(context.Background(), ManagerLocator{Path: "/a"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
				t.Errorf("%s CopyManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
			}
		}
	})

	t.Run("virtual providers enforce native IDs even when native interfaces exist", func(t *testing.T) {
		immichFM, err := NewFileManager("immich", &ImmichProvider{})
		if err != nil {
			t.Fatalf("NewFileManager(immich) error = %v", err)
		}
		if _, err := immichFM.DownloadManager(context.Background(), ManagerLocator{Path: "/asset-101"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("immich DownloadManager(path-only) error = %v, want ErrNotFound", err)
		}
		if _, _, err := immichFM.ThumbnailManager(context.Background(), ManagerLocator{Path: "/asset-101"}, 128, 128); !errors.Is(err, ErrNotFound) {
			t.Errorf("immich ThumbnailManager(path-only) error = %v, want ErrNotFound", err)
		}
		if err := immichFM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/asset-101"}, false); !errors.Is(err, ErrNotFound) {
			t.Errorf("immich DeleteManagerItem(path-only) error = %v, want ErrNotFound", err)
		}
		if _, err := immichFM.ListManager(context.Background(), ManagerLocator{Path: "/subfolder"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrNotFound) {
			t.Errorf("immich ListManager(non-root path-only) error = %v, want ErrNotFound", err)
		}
		if _, _, _, err := immichFM.ResolveManagerPath(context.Background(), "/../secret"); !errors.Is(err, ErrPathEscapesRoot) {
			t.Errorf("immich ResolveManagerPath(/../secret) error = %v, want ErrPathEscapesRoot", err)
		}

		googleFM, err := NewFileManager("google", &GoogleProvider{})
		if err != nil {
			t.Fatalf("NewFileManager(google) error = %v", err)
		}
		if _, err := googleFM.ListManager(context.Background(), ManagerLocator{Path: "/folder"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrNotFound) {
			t.Errorf("google ListManager(path-only folder) error = %v, want ErrNotFound", err)
		}
		if _, err := googleFM.UploadManager(context.Background(), ManagerLocator{Path: "/folder"}, "a.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Errorf("google UploadManager(path-only parent) error = %v, want ErrManagerInvalidDestination", err)
		}
		if err := googleFM.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/folder"}, "sub"); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Errorf("google CreateManagerDirectory(path-only parent) error = %v, want ErrManagerInvalidDestination", err)
		}
		if _, err := googleFM.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a"}, ManagerLocator{NativeID: "dest"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Errorf("google MoveManagerItem(missing source NativeID) error = %v, want ErrManagerInvalidDestination", err)
		}
		if _, err := googleFM.MoveManagerItem(context.Background(), ManagerLocator{NativeID: "src"}, ManagerLocator{Path: "/folder"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Errorf("google MoveManagerItem(path-only destination) error = %v, want ErrManagerInvalidDestination", err)
		}
	})

	t.Run("root locators and path traversal fail closed", func(t *testing.T) {
		fm, err := NewFileManager("s3", newAdapterTestProvider())
		if err != nil {
			t.Fatalf("NewFileManager(s3) error = %v", err)
		}
		if !fm.IsRootLocator(ManagerLocator{Path: "/"}) || !fm.IsRootLocator(ManagerLocator{}) {
			t.Fatal("expected root locator checks to return true")
		}
		if fm.IsRootLocator(ManagerLocator{Path: "/docs"}) {
			t.Fatal("expected /docs not to be root locator")
		}

		googleFM, err := NewFileManager("google", newAdapterTestProvider())
		if err != nil {
			t.Fatalf("NewFileManager(google) error = %v", err)
		}
		if !googleFM.IsRootLocator(ManagerLocator{NativeID: "root"}) {
			t.Fatal("expected google root native ID to be root locator")
		}
		if googleFM.IsRootLocator(ManagerLocator{NativeID: "folder-1", Path: "/"}) {
			t.Fatal("expected non-root native ID not to be root locator")
		}

		if err := fm.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/"}, false); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("DeleteManagerItem(/) error = %v, want ErrManagerUnsupported", err)
		}
		if err := fm.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/../.."}, false); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("DeleteManagerItem(/../..) error = %v, want ErrPathEscapesRoot", err)
		}

		ftpFM, err := NewFileManager("ftp", newAdapterTestProvider())
		if err != nil {
			t.Fatalf("NewFileManager(ftp) error = %v", err)
		}
		if _, err := ftpFM.RenameManagerItem(context.Background(), ManagerLocator{Path: "/"}, ManagerLocator{Path: "/"}, "x", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("RenameManagerItem(/) error = %v, want ErrManagerUnsupported", err)
		}
		if _, err := ftpFM.MoveManagerItem(context.Background(), ManagerLocator{Path: "/"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("MoveManagerItem(/) error = %v, want ErrManagerUnsupported", err)
		}
		if _, err := ftpFM.CopyManagerItem(context.Background(), ManagerLocator{Path: "/"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("CopyManagerItem(/) error = %v, want ErrManagerUnsupported", err)
		}
		if _, err := ftpFM.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a"}, ManagerLocator{Path: "/../dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("MoveManagerItem(traversal destination) error = %v, want ErrPathEscapesRoot", err)
		}
		if _, err := ftpFM.CopyManagerItem(context.Background(), ManagerLocator{Path: "/../.."}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("CopyManagerItem(/../.. source) error = %v, want ErrPathEscapesRoot", err)
		}

		if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/../escape"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrPathEscapesRoot) {
			t.Fatalf("s3 ListManager(/../escape) error = %v, want ErrPathEscapesRoot", err)
		}

		nextcloudNative := &nativeTransferStub{adapterTestProvider: newAdapterTestProvider()}
		nextcloudFM, err := NewFileManager("nextcloud", nextcloudNative)
		if err != nil {
			t.Fatalf("NewFileManager(nextcloud) error = %v", err)
		}
		if _, err := nextcloudFM.DownloadManager(context.Background(), ManagerLocator{Path: "/../escape"}); !errors.Is(err, ErrPathEscapesRoot) || nextcloudNative.downloaded {
			t.Fatalf("nextcloud DownloadManager(traversal) = (%v, downloaded=%v), want ErrPathEscapesRoot without delegating", err, nextcloudNative.downloaded)
		}
		if _, err := nextcloudFM.DownloadManager(context.Background(), ManagerLocator{Path: "/../.."}); !errors.Is(err, ErrPathEscapesRoot) || nextcloudNative.downloaded {
			t.Fatalf("nextcloud DownloadManager(/../..) = (%v, downloaded=%v), want ErrPathEscapesRoot without delegating", err, nextcloudNative.downloaded)
		}
		if _, err := nextcloudFM.DownloadManager(context.Background(), ManagerLocator{Path: "/"}); !errors.Is(err, ErrNotFound) || nextcloudNative.downloaded {
			t.Fatalf("nextcloud DownloadManager(/) = (%v, downloaded=%v), want ErrNotFound without delegating", err, nextcloudNative.downloaded)
		}

		dropboxThumb := &nativeThumbDeleteStub{adapterTestProvider: newAdapterTestProvider()}
		dropboxFM, err := NewFileManager("dropbox", dropboxThumb)
		if err != nil {
			t.Fatalf("NewFileManager(dropbox) error = %v", err)
		}
		if _, _, err := dropboxFM.ThumbnailManager(context.Background(), ManagerLocator{Path: "/../img.png"}, 64, 64); !errors.Is(err, ErrPathEscapesRoot) || dropboxThumb.thumbRead {
			t.Fatalf("dropbox ThumbnailManager(traversal) = (%v, read=%v), want ErrPathEscapesRoot", err, dropboxThumb.thumbRead)
		}
		if _, _, err := dropboxFM.ThumbnailManager(context.Background(), ManagerLocator{Path: "/"}, 64, 64); !errors.Is(err, ErrNotFound) || dropboxThumb.thumbRead {
			t.Fatalf("dropbox ThumbnailManager(/) = (%v, read=%v), want ErrNotFound", err, dropboxThumb.thumbRead)
		}

		if runtime.GOOS != "windows" {
			localFM, err := NewFileManager("local", newAdapterTestProvider())
			if err != nil {
				t.Fatalf("NewFileManager(local) error = %v", err)
			}
			if _, err := localFM.ListManager(context.Background(), ManagerLocator{Path: "/../escape"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrPathEscapesRoot) {
				t.Fatalf("ListManager(/../escape) error = %v, want ErrPathEscapesRoot", err)
			}
		} else {
			localFM, err := NewFileManager("local", newAdapterTestProvider())
			if err != nil {
				t.Fatalf("NewFileManager(local) error = %v", err)
			}
			if localFM.Capabilities() != (ManagerCapabilities{}) {
				t.Fatalf("expected empty capabilities on Windows local, got %+v", localFM.Capabilities())
			}
			if _, err := localFM.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrManagerUnsupported) {
				t.Fatalf("Windows local ListManager error = %v, want ErrManagerUnsupported", err)
			}
		}
	})
}

type nativeListerResolverStub struct {
	*adapterTestProvider
	listed   bool
	resolved bool
}

func (n *nativeListerResolverStub) ConnectManager(context.Context) (bool, error) {
	return true, nil
}

func (n *nativeListerResolverStub) ListManager(context.Context, ManagerLocator, ManagerListOptions) (ManagerPage, error) {
	n.listed = true
	return ManagerPage{
		Items: []ManagerItem{
			{Locator: ManagerLocator{Path: "/b.txt", NativeID: "id-b"}, Name: "b.txt", Size: 10},
			{Locator: ManagerLocator{Path: "/alpha", NativeID: "id-a"}, Name: "alpha", IsDir: true},
		},
	}, nil
}

func (n *nativeListerResolverStub) ResolveManagerPath(context.Context, string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	n.resolved = true
	return ManagerLocator{Path: "/docs", NativeID: "id-docs"}, []ManagerBreadcrumb{{Name: "docs", Locator: ManagerLocator{NativeID: "id-docs", Path: "/docs"}}}, false, nil
}

type nativeTransferStub struct {
	*adapterTestProvider
	downloaded bool
	uploaded   bool
	createdDir bool
}

func (n *nativeTransferStub) DownloadManager(context.Context, ManagerLocator) (ManagerDownload, error) {
	n.downloaded = true
	return ManagerDownload{
		Item:   ManagerItem{Locator: ManagerLocator{Path: "/doc.txt", NativeID: "id-doc"}, Name: "doc.txt", Size: 6},
		Stream: io.NopCloser(strings.NewReader("native")),
	}, nil
}

func (n *nativeTransferStub) UploadManager(context.Context, ManagerLocator, string, io.Reader, int64, ManagerUploadOptions) (ManagerUploadResult, error) {
	n.uploaded = true
	return ManagerUploadResult{Status: "uploaded", FinalName: "n.txt"}, nil
}

func (n *nativeTransferStub) CreateManagerDirectory(context.Context, ManagerLocator, string) error {
	n.createdDir = true
	return nil
}

type nativeThumbDeleteStub struct {
	*adapterTestProvider
	deleted   bool
	thumbRead bool
}

func (n *nativeThumbDeleteStub) DeleteManagerItem(context.Context, ManagerLocator, bool) error {
	n.deleted = true
	return nil
}

func (n *nativeThumbDeleteStub) ThumbnailManager(context.Context, ManagerLocator, int, int) (io.ReadCloser, string, error) {
	n.thumbRead = true
	return io.NopCloser(strings.NewReader("png")), "image/png", nil
}

func TestFileManagerConnectListAndResolvePath(t *testing.T) {
	t.Run("fallback connects lists sorts paginates and detects directory changes", func(t *testing.T) {
		stub := newAdapterTestProvider()
		stub.listings["/"] = []CloudResource{
			{Path: "/zebra.txt", Name: "zebra.txt", Size: 20, LastModified: time.Unix(1700000002, 0).UTC()},
			{Path: "/docs", Name: "docs", IsDir: true, LastModified: time.Unix(1700000001, 0).UTC()},
			{Path: "/alpha.txt", Name: "alpha.txt", Size: 10, LastModified: time.Unix(1700000000, 0).UTC()},
		}
		fm, err := NewFileManager("webdav", stub)
		if err != nil {
			t.Fatalf("NewFileManager(webdav) error = %v", err)
		}

		connected, err := fm.ConnectManager(context.Background())
		if err != nil || !connected {
			t.Fatalf("ConnectManager() = (%v, %v), want (true, nil)", connected, err)
		}

		page1, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2})
		if err != nil {
			t.Fatalf("ListManager(page1) error = %v", err)
		}
		if page1.PaginationMode != "emulated" {
			t.Errorf("page1.PaginationMode = %q, want emulated", page1.PaginationMode)
		}
		if len(page1.Items) != 2 || page1.Items[0].Name != "docs" || !page1.Items[0].IsDir || page1.Items[1].Name != "alpha.txt" {
			t.Fatalf("unexpected page1 items order: %+v", page1.Items)
		}
		if page1.NextCursor == "" {
			t.Fatal("expected non-empty NextCursor after page1")
		}

		page2, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2, Cursor: page1.NextCursor})
		if err != nil {
			t.Fatalf("ListManager(page2) error = %v", err)
		}
		if page2.PaginationMode != "emulated" || page2.NextCursor != "" {
			t.Fatalf("unexpected page2 metadata: mode=%q cursor=%q", page2.PaginationMode, page2.NextCursor)
		}
		if len(page2.Items) != 1 || page2.Items[0].Name != "zebra.txt" {
			t.Fatalf("unexpected page2 items: %+v", page2.Items)
		}

		// Mutate directory -> cursor must fail with ErrManagerDirectoryChanged.
		stub.listings["/"] = append(stub.listings["/"], CloudResource{Path: "/new.txt", Name: "new.txt"})
		if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2, Cursor: page1.NextCursor}); !errors.Is(err, ErrManagerDirectoryChanged) {
			t.Fatalf("ListManager(mutated dir) error = %v, want ErrManagerDirectoryChanged", err)
		}
		if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2, Cursor: "invalid-json"}); !errors.Is(err, ErrManagerDirectoryChanged) {
			t.Fatalf("ListManager(bad cursor) error = %v, want ErrManagerDirectoryChanged", err)
		}

		// Directory too large (> 10000 items).
		tooMany := make([]CloudResource, managerMaxDirectoryItems+1)
		stub.listings["/big"] = tooMany
		if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/big"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrManagerDirectoryTooLarge) {
			t.Fatalf("ListManager(/big) error = %v, want ErrManagerDirectoryTooLarge", err)
		}

		// Locator safety: non-empty NativeID with empty Path rejected on path-only fallback provider.
		if _, err := fm.ListManager(context.Background(), ManagerLocator{NativeID: "only-id"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ListManager(id-only on path provider) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("fallback resolves paths handles missing segments and rejects ambiguous siblings", func(t *testing.T) {
		stub := newAdapterTestProvider()
		stub.listings["/"] = []CloudResource{
			{Path: "/docs", Name: "docs", IsDir: true},
			{Path: "/dup", Name: "dup", IsDir: true},
			{Path: "/dup-2", Name: "dup", IsDir: true},
		}
		stub.listings["/docs"] = []CloudResource{
			{Path: "/docs/2026", Name: "2026", IsDir: true},
			{Path: "/docs/readme.txt", Name: "readme.txt", IsDir: false},
		}

		fm, err := NewFileManager("mega", stub)
		if err != nil {
			t.Fatalf("NewFileManager(mega) error = %v", err)
		}

		loc, crumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/docs/2026")
		if err != nil {
			t.Fatalf("ResolveManagerPath(/docs/2026) error = %v", err)
		}
		if fallback || loc.Path != "/docs/2026" || len(crumbs) != 2 || crumbs[0].Name != "docs" || crumbs[1].Name != "2026" {
			t.Fatalf("ResolveManagerPath(/docs/2026) = (%+v, %+v, %v)", loc, crumbs, fallback)
		}

		loc, crumbs, fallback, err = fm.ResolveManagerPath(context.Background(), "/docs/missing/sub")
		if err != nil {
			t.Fatalf("ResolveManagerPath(/docs/missing/sub) error = %v", err)
		}
		if !fallback || loc.Path != "/docs" || len(crumbs) != 1 || crumbs[0].Name != "docs" {
			t.Fatalf("ResolveManagerPath(missing) = (%+v, %+v, %v)", loc, crumbs, fallback)
		}

		if _, _, _, err := fm.ResolveManagerPath(context.Background(), "/dup/sub"); !errors.Is(err, ErrAmbiguousPath) {
			t.Fatalf("ResolveManagerPath(/dup/sub) error = %v, want ErrAmbiguousPath", err)
		}
	})

	t.Run("native ManagerLister and ManagerPathResolver take precedence", func(t *testing.T) {
		stub := &nativeListerResolverStub{adapterTestProvider: newAdapterTestProvider()}
		fm, err := NewFileManager("dropbox", stub)
		if err != nil {
			t.Fatalf("NewFileManager(dropbox) error = %v", err)
		}
		page, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("native ListManager error = %v", err)
		}
		if !stub.listed || page.PaginationMode != "native" {
			t.Fatalf("expected native ManagerLister to run, listed=%v mode=%q", stub.listed, page.PaginationMode)
		}
		if len(page.Items) != 2 || page.Items[0].Name != "alpha" {
			t.Fatalf("expected sorted native items, got %+v", page.Items)
		}
		_, _, _, err = fm.ResolveManagerPath(context.Background(), "/docs")
		if err != nil || !stub.resolved {
			t.Fatalf("expected native ResolveManagerPath to run, resolved=%v err=%v", stub.resolved, err)
		}

		// Native lister with NativePagination=false (opencloud) uses emulated fingerprint pagination & global sort.
		openCloudItems := []ManagerItem{
			{Locator: ManagerLocator{Path: "/z.txt", NativeID: "3"}, Name: "z.txt", Size: 3},
			{Locator: ManagerLocator{Path: "/docs", NativeID: "1"}, Name: "docs", IsDir: true},
			{Locator: ManagerLocator{Path: "/a.txt", NativeID: "2"}, Name: "a.txt", Size: 1},
		}
		openCloudLister := &nativeEmulatedListerStub{adapterTestProvider: newAdapterTestProvider(), items: &openCloudItems}
		openCloudFM, err := NewFileManager("opencloud", openCloudLister)
		if err != nil {
			t.Fatalf("NewFileManager(opencloud) error = %v", err)
		}
		ocPage1, err := openCloudFM.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2})
		if err != nil {
			t.Fatalf("opencloud ListManager page 1 error = %v", err)
		}
		if ocPage1.PaginationMode != "emulated" || len(ocPage1.Items) != 2 || ocPage1.Items[0].Name != "docs" || ocPage1.Items[1].Name != "a.txt" || ocPage1.NextCursor == "" {
			t.Fatalf("unexpected emulated native lister page 1: %+v", ocPage1)
		}
		openCloudItems = append(openCloudItems, ManagerItem{Locator: ManagerLocator{Path: "/m.txt", NativeID: "4"}, Name: "m.txt"})
		if _, err := openCloudFM.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Cursor: ocPage1.NextCursor, Limit: 2}); !errors.Is(err, ErrManagerDirectoryChanged) {
			t.Fatalf("opencloud ListManager(mutated) error = %v, want ErrManagerDirectoryChanged", err)
		}
		if _, err := openCloudFM.ListManager(context.Background(), ManagerLocator{NativeID: "only-id"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("opencloud ListManager(id-only) error = %v, want ErrNotFound", err)
		}
	})
}

type nativeEmulatedListerStub struct {
	*adapterTestProvider
	items *[]ManagerItem
}

func (n *nativeEmulatedListerStub) ListManager(context.Context, ManagerLocator, ManagerListOptions) (ManagerPage, error) {
	out := make([]ManagerItem, len(*n.items))
	copy(out, *n.items)
	return ManagerPage{Items: out}, nil
}

func TestFileManagerInspectDownloadUploadAndMkdir(t *testing.T) {
	t.Run("inspect and download fallback enforce locator and dir safety", func(t *testing.T) {
		stub := newAdapterTestProvider()
		stub.files["/hello.txt"] = []byte("hello world")
		stub.listings["/"] = []CloudResource{
			{Path: "/docs", Name: "docs", IsDir: true},
			{Path: "/hello.txt", Name: "hello.txt", Size: 11},
		}

		fm, err := NewFileManager("webdav", stub)
		if err != nil {
			t.Fatalf("NewFileManager(webdav) error = %v", err)
		}

		rootItem, err := fm.InspectManagerItem(context.Background(), ManagerLocator{Path: "/"})
		if err != nil || !rootItem.IsDir {
			t.Fatalf("InspectManagerItem(/) = (%+v, %v), want IsDir=true", rootItem, err)
		}

		item, err := fm.InspectManagerItem(context.Background(), ManagerLocator{Path: "/hello.txt"})
		if err != nil || item.Name != "hello.txt" || item.Size != 11 || item.IsDir {
			t.Fatalf("InspectManagerItem(/hello.txt) = (%+v, %v)", item, err)
		}

		if _, err := fm.DownloadManager(context.Background(), ManagerLocator{Path: "/docs"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DownloadManager(dir) error = %v, want ErrNotFound", err)
		}
		if _, err := fm.DownloadManager(context.Background(), ManagerLocator{Path: "/"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DownloadManager(/) error = %v, want ErrNotFound", err)
		}
		if _, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: "id-only"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DownloadManager(id-only on path provider) error = %v, want ErrNotFound", err)
		}

		dl, err := fm.DownloadManager(context.Background(), ManagerLocator{Path: "/hello.txt"})
		if err != nil {
			t.Fatalf("DownloadManager(/hello.txt) error = %v", err)
		}
		defer dl.Stream.Close()
		body, _ := io.ReadAll(dl.Stream)
		if string(body) != "hello world" || dl.Item.Name != "hello.txt" {
			t.Fatalf("DownloadManager(/hello.txt) = (%q, %+v)", string(body), dl.Item)
		}
	})

	t.Run("upload handles skip overwrite rename atomic promotion rollback and chunked threshold", func(t *testing.T) {
		// Non-atomic provider (s3) uploads directly on OVERWRITE.
		s3Stub := newAdapterTestProvider()
		s3Stub.atomicRename = false
		s3Stub.listings["/"] = []CloudResource{
			{Path: "/folder", Name: "folder", IsDir: true},
			{Path: "/note.txt", Name: "note.txt", Size: 3},
		}
		s3Stub.files["/note.txt"] = []byte("old")
		s3FM, err := NewFileManager("s3", s3Stub)
		if err != nil {
			t.Fatalf("NewFileManager(s3) error = %v", err)
		}

		resSkip, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "note.txt", strings.NewReader("new"), 3, ManagerUploadOptions{ConflictStrategy: "SKIP"})
		if err != nil || resSkip.Status != "skipped" || string(s3Stub.files["/note.txt"]) != "old" {
			t.Fatalf("Upload(SKIP) = (%+v, %v, content=%q)", resSkip, err, string(s3Stub.files["/note.txt"]))
		}

		if _, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "folder", strings.NewReader("new"), 3, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerConflict) {
			t.Fatalf("Upload(OVERWRITE dir) error = %v, want ErrManagerConflict", err)
		}

		resOver, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "note.txt", strings.NewReader("new"), 3, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"})
		if err != nil || resOver.Status != "uploaded" || string(s3Stub.files["/note.txt"]) != "new" || len(s3Stub.renameCalls) != 0 {
			t.Fatalf("non-atomic Upload(OVERWRITE) = (%+v, %v, content=%q, renames=%v)", resOver, err, string(s3Stub.files["/note.txt"]), s3Stub.renameCalls)
		}

		resRename, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "note.txt", strings.NewReader("copy"), 4, ManagerUploadOptions{ConflictStrategy: "RENAME"})
		if err != nil || resRename.Status != "renamed" || resRename.FinalName != "note (1).txt" || string(s3Stub.files["/note (1).txt"]) != "copy" {
			t.Fatalf("Upload(RENAME) = (%+v, %v, content=%q)", resRename, err, string(s3Stub.files["/note (1).txt"]))
		}

		// Chunked threshold (>50MiB).
		if _, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "huge.bin", strings.NewReader("data"), managerChunkedThreshold+1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err != nil {
			t.Fatalf("chunked Upload error = %v", err)
		}
		if len(s3Stub.chunkedCalls) != 1 || s3Stub.chunkedCalls[0] != "/huge.bin" {
			t.Fatalf("expected StreamUploadChunked call for >50MiB, got %v", s3Stub.chunkedCalls)
		}

		// Atomic overwrite (ftp) stages tmp, rotates backup, promotes tmp, and deletes backup.
		ftpStub := newAdapterTestProvider()
		ftpStub.atomicRename = true
		ftpStub.listings["/"] = []CloudResource{{Path: "/note.txt", Name: "note.txt", Size: 3}}
		ftpStub.files["/note.txt"] = []byte("v1")
		ftpFM, err := NewFileManager("ftp", ftpStub)
		if err != nil {
			t.Fatalf("NewFileManager(ftp) error = %v", err)
		}
		if _, err := ftpFM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "note.txt", strings.NewReader("v2"), 2, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err != nil {
			t.Fatalf("atomic Upload(OVERWRITE) error = %v", err)
		}
		if string(ftpStub.files["/note.txt"]) != "v2" || len(ftpStub.renameCalls) != 2 || len(ftpStub.deleteFileCalls) != 1 {
			t.Fatalf("expected atomic promote sequence, content=%q renames=%v deletes=%v", string(ftpStub.files["/note.txt"]), ftpStub.renameCalls, ftpStub.deleteFileCalls)
		}

		// Atomic rollback restores original file even when caller ctx cancels or upload exceeds cleanupTimeout.
		rollbackStub := newAdapterTestProvider()
		rollbackStub.atomicRename = true
		rollbackStub.listings["/"] = []CloudResource{{Path: "/note.txt", Name: "note.txt", Size: 2}}
		rollbackStub.files["/note.txt"] = []byte("v1")
		rollbackStub.uploadFn = func(context.Context, string, string, io.Reader, int64) error {
			time.Sleep(60 * time.Millisecond)
			return nil
		}
		cancelCtx, cancelUpload := context.WithCancel(context.Background())
		var rollbackHadActiveCtx bool
		rollbackStub.renameFn = func(rCtx context.Context, _ string, oldPath, newPath string) error {
			if strings.Contains(oldPath, ".tmp.") && newPath == "/note.txt" {
				cancelUpload()
				return errors.New("promote failed")
			}
			if strings.Contains(oldPath, ".bak.") && newPath == "/note.txt" {
				rollbackHadActiveCtx = rCtx.Err() == nil
			}
			if data, ok := rollbackStub.files[oldPath]; ok {
				rollbackStub.files[newPath] = data
				delete(rollbackStub.files, oldPath)
			}
			return nil
		}
		rollbackFM, err := NewFileManager("mega", rollbackStub)
		if err != nil {
			t.Fatalf("NewFileManager(mega) error = %v", err)
		}
		rollbackFM.(*fileManagerAdapter).cleanupTimeout = 30 * time.Millisecond
		if _, err := rollbackFM.UploadManager(cancelCtx, ManagerLocator{Path: "/"}, "note.txt", strings.NewReader("v2"), 2, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err == nil {
			t.Fatal("expected promote failure to return error")
		}
		if !rollbackHadActiveCtx || string(rollbackStub.files["/note.txt"]) != "v1" {
			t.Fatalf("expected original file restored with fresh rollback ctx (active=%v content=%q)", rollbackHadActiveCtx, string(rollbackStub.files["/note.txt"]))
		}

		// Overwriting valid long 255-byte ASCII & multi-byte UTF-8 filenames uses bounded sibling names (<=255 bytes).
		for _, longName := range []string{
			strings.Repeat("a", 251) + ".txt",
			strings.Repeat("é", 127),
		} {
			longStub := newAdapterTestProvider()
			longStub.atomicRename = true
			longStub.listings["/"] = []CloudResource{{Path: "/" + longName, Name: longName, Size: 2}}
			longStub.files["/"+longName] = []byte("v1")
			longFM, err := NewFileManager("ftp", longStub)
			if err != nil {
				t.Fatalf("NewFileManager(ftp) error = %v", err)
			}
			if _, err := longFM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, longName, strings.NewReader("v2"), 2, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err != nil {
				t.Fatalf("long atomic Upload(len=%d) error = %v", len(longName), err)
			}
			if string(longStub.files["/"+longName]) != "v2" {
				t.Fatalf("long filename content = %q, want v2", string(longStub.files["/"+longName]))
			}
			for _, stagedPath := range append(append([]string(nil), longStub.uploadCalls...), longStub.deleteFileCalls...) {
				base := path.Base(stagedPath)
				if len(base) > 255 || !ValidManagerEntryName(base) {
					t.Fatalf("staged sibling name invalid or too long: len=%d name=%q", len(base), base)
				}
			}
		}

		// Backup-delete error after promote is non-fatal and WARN logs only emit a safe error classification (never raw URLs/paths).
		var logBuf bytes.Buffer
		prevLogger := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(prevLogger) })

		backupErrStub := newAdapterTestProvider()
		backupErrStub.atomicRename = true
		backupErrStub.listings["/"] = []CloudResource{{Path: "/secret-note.txt", Name: "secret-note.txt", Size: 2}}
		backupErrStub.files["/secret-note.txt"] = []byte("v1")
		backupErrStub.deleteFileFn = func(_ context.Context, _ string, filePath string) error {
			if strings.Contains(filePath, ".bak.") {
				return &url.Error{
					Op:  "DELETE",
					URL: "https://dav.example.test/remote.php/dav/files/alice" + filePath,
					Err: context.DeadlineExceeded,
				}
			}
			delete(backupErrStub.files, filePath)
			return nil
		}
		backupErrFM, err := NewFileManager("webdav", backupErrStub)
		if err != nil {
			t.Fatalf("NewFileManager(webdav) error = %v", err)
		}
		if _, err := backupErrFM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "secret-note.txt", strings.NewReader("v2"), 2, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err != nil || string(backupErrStub.files["/secret-note.txt"]) != "v2" {
			t.Fatalf("expected committed overwrite despite backup delete failure, got err=%v content=%q", err, string(backupErrStub.files["/secret-note.txt"]))
		}
		logOut := logBuf.String()
		if !strings.Contains(logOut, `"error_kind":"timeout"`) {
			t.Fatalf("expected WARN log to include safe error_kind classification, got: %s", logOut)
		}
		if strings.Contains(logOut, "secret-note.txt") || strings.Contains(logOut, ".bak.") || strings.Contains(logOut, "dav.example.test") {
			t.Fatalf("expected WARN log to omit file path and URL, got: %s", logOut)
		}

		// RENAME exhaustion (>100 existing suffixed siblings) -> ErrManagerConflict.
		renameCapStub := newAdapterTestProvider()
		capListings := []CloudResource{{Path: "/dup.txt", Name: "dup.txt", Size: 1}}
		for i := 1; i <= 100; i++ {
			cand := managerRenamedName("dup.txt", i)
			capListings = append(capListings, CloudResource{Path: "/" + cand, Name: cand, Size: 1})
		}
		renameCapStub.listings["/"] = capListings
		renameCapFM, err := NewFileManager("s3", renameCapStub)
		if err != nil {
			t.Fatalf("NewFileManager(s3) error = %v", err)
		}
		if _, err := renameCapFM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "dup.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "RENAME"}); !errors.Is(err, ErrManagerConflict) {
			t.Fatalf("RENAME >100 collisions error = %v, want ErrManagerConflict", err)
		}

		// Invalid strategy and invalid filename return typed sentinel errors.
		if _, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "a.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "BAD"}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("UploadManager(BAD strategy) error = %v, want ErrManagerInvalidDestination", err)
		}
		if _, err := s3FM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "../bad.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("UploadManager(invalid filename) error = %v, want ErrManagerInvalidDestination", err)
		}
	})

	t.Run("mkdir fallback checks conflicts and native implementations win", func(t *testing.T) {
		stub := newAdapterTestProvider()
		stub.listings["/"] = []CloudResource{{Path: "/existing", Name: "existing", IsDir: true}}
		fm, err := NewFileManager("smb", stub)
		if err != nil {
			t.Fatalf("NewFileManager(smb) error = %v", err)
		}

		if err := fm.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "existing"); !errors.Is(err, ErrManagerConflict) {
			t.Fatalf("CreateManagerDirectory(existing) error = %v, want ErrManagerConflict", err)
		}
		if err := fm.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "bad/name"); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("CreateManagerDirectory(bad/name) error = %v, want ErrManagerInvalidDestination", err)
		}
		if err := fm.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "newdir"); err != nil {
			t.Fatalf("CreateManagerDirectory(newdir) error = %v", err)
		}
		if len(stub.createDirCalls) != 1 || stub.createDirCalls[0] != "/newdir" {
			t.Fatalf("unexpected CreateDirectory calls: %v", stub.createDirCalls)
		}

		native := &nativeTransferStub{adapterTestProvider: newAdapterTestProvider()}
		nativeFM, err := NewFileManager("nextcloud", native)
		if err != nil {
			t.Fatalf("NewFileManager(nextcloud) error = %v", err)
		}
		if _, err := nativeFM.DownloadManager(context.Background(), ManagerLocator{NativeID: "1"}); !errors.Is(err, ErrNotFound) || native.downloaded {
			t.Fatalf("nextcloud DownloadManager(id-only) = (%v, downloaded=%v), want ErrNotFound", err, native.downloaded)
		}
		if _, err := nativeFM.DownloadManager(context.Background(), ManagerLocator{Path: "/n.txt"}); err != nil || !native.downloaded {
			t.Fatalf("expected native DownloadManager, downloaded=%v err=%v", native.downloaded, err)
		}
		if _, err := nativeFM.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "n.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); err != nil || !native.uploaded {
			t.Fatalf("expected native UploadManager, uploaded=%v err=%v", native.uploaded, err)
		}
		if err := nativeFM.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "n"); err != nil || !native.createdDir {
			t.Fatalf("expected native CreateManagerDirectory, createdDir=%v err=%v", native.createdDir, err)
		}
	})
}

type nativeFileOnlyDeleterStub struct {
	*adapterTestProvider
}

func (n *nativeFileOnlyDeleterStub) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerFileOnly(ctx, n.adapterTestProvider, locator, recursive)
}

type nativePathDeleterStub struct {
	*adapterTestProvider
}

func (n *nativePathDeleterStub) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	return deleteManagerPathItem(ctx, n.adapterTestProvider, locator, recursive)
}

func TestFileManagerDeleteMutateThumbnailAndProviderMatrix(t *testing.T) {
	t.Run("delete requires ManagerDeleter and enforces capability and root refusal", func(t *testing.T) {
		noDeleterStub := newAdapterTestProvider()
		noDeleterStub.files["/file.txt"] = []byte("data")
		noDeleterFM, err := NewFileManager("s3", noDeleterStub)
		if err != nil {
			t.Fatalf("NewFileManager(s3 without ManagerDeleter) error = %v", err)
		}
		if err := noDeleterFM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/file.txt"}, false); !errors.Is(err, ErrManagerUnsupported) || len(noDeleterStub.deleteFileCalls) != 0 {
			t.Fatalf("DeleteManagerItem(without ManagerDeleter) = (%v, deletes=%v), want ErrManagerUnsupported with 0 deletes", err, noDeleterStub.deleteFileCalls)
		}

		s3Stub := &nativeFileOnlyDeleterStub{adapterTestProvider: newAdapterTestProvider()}
		s3Stub.listings["/"] = []CloudResource{
			{Path: "/folder", Name: "folder", IsDir: true},
			{Path: "/file.txt", Name: "file.txt", Size: 4},
		}
		s3Stub.files["/file.txt"] = []byte("data")
		s3FM, err := NewFileManager("s3", s3Stub)
		if err != nil {
			t.Fatalf("NewFileManager(s3) error = %v", err)
		}

		if err := s3FM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/folder"}, true); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("s3 DeleteManagerItem(dir) error = %v, want ErrManagerUnsupported", err)
		}
		if err := s3FM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/file.txt"}, false); err != nil {
			t.Fatalf("s3 DeleteManagerItem(file) error = %v", err)
		}
		if len(s3Stub.deleteFileCalls) != 1 || s3Stub.deleteFileCalls[0] != "/file.txt" {
			t.Fatalf("unexpected s3 delete calls: %v", s3Stub.deleteFileCalls)
		}

		native := &nativeThumbDeleteStub{adapterTestProvider: newAdapterTestProvider()}
		nextcloudFM, err := NewFileManager("nextcloud", native)
		if err != nil {
			t.Fatalf("NewFileManager(nextcloud) error = %v", err)
		}
		if err := nextcloudFM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/"}, false); !errors.Is(err, ErrManagerUnsupported) || native.deleted {
			t.Fatalf("nextcloud DeleteManagerItem(/) = (%v, deleted=%v), want ErrManagerUnsupported", err, native.deleted)
		}
		if err := nextcloudFM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/item.txt"}, false); err != nil || !native.deleted {
			t.Fatalf("expected native DeleteManagerItem, deleted=%v err=%v", native.deleted, err)
		}

		smbStub := &nativePathDeleterStub{adapterTestProvider: newAdapterTestProvider()}
		smbStub.listings["/"] = []CloudResource{{Path: "/nonempty", Name: "nonempty", IsDir: true}}
		smbStub.listings["/nonempty"] = []CloudResource{{Path: "/nonempty/child.txt", Name: "child.txt", Size: 1}}
		smbFM, err := NewFileManager("smb", smbStub)
		if err != nil {
			t.Fatalf("NewFileManager(smb) error = %v", err)
		}
		if err := smbFM.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/nonempty"}, false); !errors.Is(err, ErrManagerDirectoryNotEmpty) {
			t.Fatalf("smb DeleteManagerItem(nonempty dir, recursive=false) error = %v, want ErrManagerDirectoryNotEmpty", err)
		}
	})

	t.Run("rename move copy delegate safely and block invalid names", func(t *testing.T) {
		stub := newAdapterTestProvider()
		stub.files["/a.txt"] = []byte("hello")
		stub.listings["/"] = []CloudResource{
			{Path: "/a.txt", Name: "a.txt", Size: 5},
			{Path: "/dst", Name: "dst", IsDir: true},
		}
		fm, err := NewFileManager("ftp", stub)
		if err != nil {
			t.Fatalf("NewFileManager(ftp) error = %v", err)
		}

		if _, err := fm.RenameManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}, ManagerLocator{Path: "/"}, "bad/name", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("RenameManagerItem(bad/name) error = %v, want ErrManagerInvalidDestination", err)
		}
		if _, err := fm.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}, ManagerLocator{Path: "/dst"}, "", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("MoveManagerItem(empty name) error = %v, want ErrManagerInvalidDestination", err)
		}
		if _, err := fm.CopyManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}, ManagerLocator{Path: "/dst"}, "", ManagerMutationOptions{}); !errors.Is(err, ErrManagerInvalidDestination) {
			t.Fatalf("CopyManagerItem(empty name) error = %v, want ErrManagerInvalidDestination", err)
		}

		res, err := fm.CopyManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}, ManagerLocator{Path: "/dst"}, "a.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite})
		if err != nil || res.Status != "copied" || res.FinalName != "a.txt" || string(stub.files["/dst/a.txt"]) != "hello" {
			t.Fatalf("CopyManagerItem() = (%+v, %v, dst=%q), want copied a.txt", res, err, string(stub.files["/dst/a.txt"]))
		}

		moveRes, err := fm.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a.txt"}, ManagerLocator{Path: "/dst"}, "moved.txt", ManagerMutationOptions{ConflictStrategy: ManagerConflictOverwrite})
		if err != nil || moveRes.Status != "moved" || moveRes.FinalName != "moved.txt" {
			t.Fatalf("MoveManagerItem() = (%+v, %v), want moved", moveRes, err)
		}
	})

	t.Run("thumbnails enforce capability gate", func(t *testing.T) {
		openCloudStub := &nativeThumbDeleteStub{adapterTestProvider: newAdapterTestProvider()}
		openCloudFM, err := NewFileManager("opencloud", openCloudStub)
		if err != nil {
			t.Fatalf("NewFileManager(opencloud) error = %v", err)
		}
		if _, _, err := openCloudFM.ThumbnailManager(context.Background(), ManagerLocator{Path: "/img.png"}, 128, 128); !errors.Is(err, ErrManagerUnsupported) {
			t.Fatalf("opencloud ThumbnailManager error = %v, want ErrManagerUnsupported", err)
		}

		dropboxStub := &nativeThumbDeleteStub{adapterTestProvider: newAdapterTestProvider()}
		dropboxFM, err := NewFileManager("dropbox", dropboxStub)
		if err != nil {
			t.Fatalf("NewFileManager(dropbox) error = %v", err)
		}
		stream, mimeType, err := dropboxFM.ThumbnailManager(context.Background(), ManagerLocator{Path: "/img.png"}, 128, 128)
		if err != nil {
			t.Fatalf("dropbox ThumbnailManager error = %v", err)
		}
		stream.Close()
		if !dropboxStub.thumbRead || mimeType != "image/png" {
			t.Fatalf("expected dropbox thumbnail delegation, read=%v mime=%q", dropboxStub.thumbRead, mimeType)
		}
	})

	t.Run("every ValidProvider constructs a FileManager with matching capabilities and explicit unsupported gates", func(t *testing.T) {
		providers := map[string]ManagerProvider{
			"nextcloud":    &NextcloudProvider{},
			"opencloud":    &OpenCloudProvider{},
			"webdav":       &WebDAVProvider{},
			"dropbox":      &DropboxProvider{},
			"google":       &GoogleProvider{},
			"onedrive":     &OneDriveProvider{},
			"hidrive":      &HiDriveProvider{},
			"smb":          &SMBProvider{},
			"s3":           &S3Provider{},
			"sftp":         &SFTPProvider{},
			"ftp":          &FTPProvider{},
			"magentacloud": &MagentacloudProvider{},
			"koofr":        &KoofrProvider{},
			"local":        &LocalProvider{},
			"immich":       &ImmichProvider{},
			"seafile":      &SeafileProvider{},
			"mega":         &MegaProvider{},
		}
		if len(providers) != len(ValidProviders) {
			t.Fatalf("provider matrix count = %d, want %d", len(providers), len(ValidProviders))
		}
		for _, providerType := range ValidProviders {
			p, ok := providers[providerType]
			if !ok {
				t.Fatalf("missing provider %q in matrix", providerType)
			}
			fm, err := NewFileManager(providerType, p)
			if err != nil {
				t.Fatalf("NewFileManager(%q) error = %v", providerType, err)
			}
			wantCaps := ManagerCapabilitiesFor(providerType)
			if fm.Capabilities() != wantCaps {
				t.Errorf("%s Capabilities() = %+v, want %+v", providerType, fm.Capabilities(), wantCaps)
			}
			if !fm.IsRootLocator(ManagerLocator{Path: "/"}) {
				t.Errorf("%s IsRootLocator(/) = false", providerType)
			}
			if !wantCaps.Browse {
				if _, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s ListManager error = %v, want ErrManagerUnsupported", providerType, err)
				}
				if _, _, _, err := fm.ResolveManagerPath(context.Background(), "/docs"); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s ResolveManagerPath error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Download {
				if _, err := fm.DownloadManager(context.Background(), ManagerLocator{Path: "/a.txt", NativeID: "1"}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s DownloadManager error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.DeleteFile {
				if err := fm.DeleteManagerItem(context.Background(), ManagerLocator{Path: "/a.txt", NativeID: "1"}, false); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s DeleteManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Thumbnails {
				if _, _, err := fm.ThumbnailManager(context.Background(), ManagerLocator{Path: "/a.jpg"}, 64, 64); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s ThumbnailManager error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Upload {
				if _, err := fm.UploadManager(context.Background(), ManagerLocator{Path: "/"}, "a.txt", strings.NewReader("x"), 1, ManagerUploadOptions{ConflictStrategy: "OVERWRITE"}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s UploadManager error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Mkdir {
				if err := fm.CreateManagerDirectory(context.Background(), ManagerLocator{Path: "/"}, "dir"); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s CreateManagerDirectory error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Rename {
				if _, err := fm.RenameManagerItem(context.Background(), ManagerLocator{Path: "/a", NativeID: "1"}, ManagerLocator{Path: "/"}, "b", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s RenameManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Move {
				if _, err := fm.MoveManagerItem(context.Background(), ManagerLocator{Path: "/a", NativeID: "1"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s MoveManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
			if !wantCaps.Copy {
				if _, err := fm.CopyManagerItem(context.Background(), ManagerLocator{Path: "/a", NativeID: "1"}, ManagerLocator{Path: "/dst"}, "a", ManagerMutationOptions{}); !errors.Is(err, ErrManagerUnsupported) {
					t.Errorf("%s CopyManagerItem error = %v, want ErrManagerUnsupported", providerType, err)
				}
			}
		}
	})
}
