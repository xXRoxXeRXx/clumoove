package storage

import (
	"context"
	"testing"
)

// managerOnlyProvider exposes the manager operations without transfer capabilities.
type managerOnlyProvider struct{ ManagerProvider }

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
