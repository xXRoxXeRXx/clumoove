package storage

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestBoxFileManagerConnectAndPaginatedBrowse(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFolder("0", "11", "Archive")
	srv.addFile("0", "12", "notes.txt", 15, "AAF4C61DDCC5E8A2DABEDE0F3B482CD9AEA9434D")
	nestedID := srv.addFolder(docsID, "20", "2026")
	srv.addFile(nestedID, "21", "q1.pdf", 512, "2FD4E1C67A2D28FCED849EE1BB76E7391B93EB12")

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	ok, err := fm.ConnectManager(context.Background())
	if err != nil || !ok {
		t.Fatalf("ConnectManager = (%v, %v), want (true, nil)", ok, err)
	}

	page1, err := fm.ListManager(context.Background(), ManagerLocator{Path: "/"}, ManagerListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListManager(root page 1): %v", err)
	}
	if len(page1.Items) != 2 || page1.NextCursor == "" || page1.PaginationMode != "native" {
		t.Fatalf("page1 = %+v, want 2 items, non-empty NextCursor, native mode", page1)
	}

	page2, err := fm.ListManager(context.Background(), ManagerLocator{NativeID: "0"}, ManagerListOptions{Cursor: page1.NextCursor, Limit: 2})
	if err != nil {
		t.Fatalf("ListManager(root page 2): %v", err)
	}
	if len(page2.Items) != 1 || page2.NextCursor != "" {
		t.Fatalf("page2 = %+v, want 1 item and empty NextCursor", page2)
	}

	// Verify nested browsing using resolved locator.
	loc, breadcrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/docs/2026")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/docs/2026): %v", err)
	}
	if fallback || loc.NativeID != nestedID || loc.Path != "/Docs/2026" {
		t.Fatalf("ResolveManagerPath = (%+v, fallback=%v), want NativeID=%q Path=/Docs/2026", loc, fallback, nestedID)
	}
	if len(breadcrumbs) != 2 || breadcrumbs[0].Name != "Docs" || breadcrumbs[0].Locator.NativeID != docsID ||
		breadcrumbs[1].Name != "2026" || breadcrumbs[1].Locator.NativeID != nestedID {
		t.Fatalf("breadcrumbs = %+v", breadcrumbs)
	}

	nestedPage, err := fm.ListManager(context.Background(), loc, ManagerListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListManager(nested): %v", err)
	}
	if len(nestedPage.Items) != 1 || nestedPage.Items[0].Name != "q1.pdf" ||
		nestedPage.Items[0].Locator.Path != "/Docs/2026/q1.pdf" ||
		nestedPage.Items[0].Locator.NativeID != "21" ||
		nestedPage.Items[0].Size != 512 {
		t.Fatalf("nestedPage.Items = %+v", nestedPage.Items)
	}
}

func TestBoxFileManagerResolvePathEdgeCases(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	docsID := srv.addFolder("0", "10", "Docs")
	srv.addFile(docsID, "11", "readme.txt", 10, "")

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}

	rootLoc, rootCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/")
	if err != nil {
		t.Fatalf("ResolveManagerPath(/): %v", err)
	}
	if fallback || !fm.IsRootLocator(rootLoc) || len(rootCrumbs) != 0 {
		t.Fatalf("root resolve = (%+v, %+v, fallback=%v)", rootLoc, rootCrumbs, fallback)
	}

	// Missing folder falls back to deepest valid parent directory.
	fbLoc, fbCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/Docs/NonExistent")
	if err != nil {
		t.Fatalf("ResolveManagerPath(missing): %v", err)
	}
	if !fallback || fbLoc.NativeID != docsID || fbLoc.Path != "/Docs" || len(fbCrumbs) != 1 {
		t.Fatalf("missing fallback = (%+v, %+v, fallback=%v), want /Docs (%s)", fbLoc, fbCrumbs, fallback, docsID)
	}

	// Path pointing to a file falls back to its parent folder.
	fileLoc, fileCrumbs, fallback, err := fm.ResolveManagerPath(context.Background(), "/Docs/readme.txt")
	if err != nil {
		t.Fatalf("ResolveManagerPath(file): %v", err)
	}
	if !fallback || fileLoc.NativeID != docsID || len(fileCrumbs) != 1 {
		t.Fatalf("file fallback = (%+v, %+v, fallback=%v), want /Docs (%s)", fileLoc, fileCrumbs, fallback, docsID)
	}

	if _, _, _, err := fm.ResolveManagerPath(context.Background(), "/Docs/../secret"); !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("traversal error = %v, want ErrPathEscapesRoot", err)
	}
}

func TestBoxFileManagerDownload(t *testing.T) {
	srv := newFakeBoxServer("valid-token")
	defer srv.close()

	payload := []byte("manager downloaded box bytes")
	docsID := srv.addFolder("0", "10", "Docs")
	fileID := srv.addFileContent(docsID, "12", "report.pdf", payload)
	blockedID := srv.addFileContent(docsID, "13", "classified.pdf", []byte("secret"))
	srv.mu.Lock()
	it := srv.items[blockedID]
	it.DownloadBlocked = true
	srv.items[blockedID] = it
	srv.mu.Unlock()

	p := srv.provider()
	defer p.Close()

	fm, err := NewFileManager("box", p)
	if err != nil {
		t.Fatalf("NewFileManager(box): %v", err)
	}
	if !fm.Capabilities().Download {
		t.Fatalf("fm.Capabilities().Download = false, want true")
	}

	dl, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: fileID, Path: "/Docs/report.pdf"})
	if err != nil {
		t.Fatalf("DownloadManager(id+path): %v", err)
	}
	body, readErr := io.ReadAll(dl.Stream)
	dl.Stream.Close()
	if readErr != nil || string(body) != string(payload) {
		t.Fatalf("downloaded = (%q, %v), want %q", body, readErr, payload)
	}
	if dl.Item.Name != "report.pdf" || dl.Item.Size != int64(len(payload)) || dl.Item.IsDir || dl.Item.Locator.NativeID != fileID {
		t.Fatalf("dl.Item = %+v", dl.Item)
	}

	// ID-only locator works without Path.
	dlID, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: fileID})
	if err != nil {
		t.Fatalf("DownloadManager(id-only): %v", err)
	}
	bodyID, readErr := io.ReadAll(dlID.Stream)
	dlID.Stream.Close()
	if readErr != nil || string(bodyID) != string(payload) {
		t.Fatalf("id-only downloaded = (%q, %v), want %q", bodyID, readErr, payload)
	}

	srv.mu.Lock()
	auths := append([]string(nil), srv.downloadAuthHeaders...)
	srv.mu.Unlock()
	if len(auths) != 2 || auths[0] != "" || auths[1] != "" {
		t.Fatalf("manager download Authorization headers = %q, want empty", auths)
	}

	// Path-only without NativeID, root, folder, and missing ID must fail with ErrNotFound.
	for _, loc := range []ManagerLocator{
		{Path: "/Docs/report.pdf"},
		{NativeID: "0"},
		{NativeID: docsID},
		{NativeID: "99999"},
	} {
		if _, err := fm.DownloadManager(context.Background(), loc); !errors.Is(err, ErrNotFound) {
			t.Fatalf("DownloadManager(%+v) = %v, want ErrNotFound", loc, err)
		}
	}

	// Policy-blocked download surfaces ErrPermission.
	if _, err := fm.DownloadManager(context.Background(), ManagerLocator{NativeID: blockedID}); !errors.Is(err, ErrPermission) {
		t.Fatalf("DownloadManager(blocked) = %v, want ErrPermission", err)
	}
}
