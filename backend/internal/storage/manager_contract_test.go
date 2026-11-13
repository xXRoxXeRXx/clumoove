package storage

import (
	"context"
	"io"
	"testing"
)

// managerOnlyProvider exposes only the operations needed by the manager.
type managerTestOperations interface {
	Connect(context.Context) (bool, error)
	GetDirectoryListing(context.Context, string, string) ([]CloudResource, error)
	InspectResource(context.Context, string, string) (CloudResource, error)
	StreamDownload(context.Context, string, string) (io.ReadCloser, error)
	StreamUpload(context.Context, string, string, io.Reader, int64) error
	StreamUploadChunked(context.Context, string, string, io.Reader, int64, chan<- int64) error
	DeleteFile(context.Context, string, string) error
	CreateDirectory(context.Context, string, string) error
	RenameFile(context.Context, string, string, string) error
}
type managerOnlyProvider struct{ managerTestOperations }

func TestFileManagerAcceptsManagerOnlyProvider(t *testing.T) {
	p := newAdapterTestProvider()
	p.listings["/"] = []CloudResource{{Path: "/file.txt", Name: "file.txt"}}
	manager, err := NewFileManager("nextcloud", managerOnlyProvider{p})
	if err != nil {
		t.Fatal(err)
	}
	page, err := manager.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Name != "file.txt" {
		t.Fatalf("unexpected page: %+v", page)
	}
}
