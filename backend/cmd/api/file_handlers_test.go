package main

import (
	"testing"

	"backend/internal/storage"
)

func TestFileReferenceRoundTripBindsUserAndProfile(t *testing.T) {
	const key = "file-manager-test-key"
	reference, err := sealFileReference(fileReference{
		UserID:       "user-a",
		ProfileID:    "profile-a",
		ResourceType: "files",
		Kind:         "file",
		Locator:      storage.ManagerLocator{Path: "/report.pdf", NativeID: "drive-file-id"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}

	opened, err := openFileReference(reference, key, "user-a", "profile-a")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Locator.NativeID != "drive-file-id" || opened.Kind != "file" {
		t.Fatalf("opened reference = %#v", opened)
	}
	if _, err := openFileReference(reference, key, "user-b", "profile-a"); err == nil {
		t.Fatal("cross-user reference replay was accepted")
	}
	if _, err := openFileReference(reference, key, "user-a", "profile-b"); err == nil {
		t.Fatal("cross-profile reference replay was accepted")
	}
	if _, err := openFileReference(reference+"00", key, "user-a", "profile-a"); err == nil {
		t.Fatal("tampered reference was accepted")
	}
}

func TestFileCursorRoundTripBindsDirectory(t *testing.T) {
	const key = "file-manager-test-key"
	cursor, err := sealFileCursor(fileCursor{
		UserID:         "user-a",
		ProfileID:      "profile-a",
		ResourceType:   "files",
		Parent:         storage.ManagerLocator{Path: "/documents", NativeID: "folder-id"},
		ProviderCursor: "next-page-token",
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openFileCursor(cursor, key, "user-a", "profile-a")
	if err != nil {
		t.Fatal(err)
	}
	if opened.Parent.NativeID != "folder-id" || opened.ProviderCursor != "next-page-token" {
		t.Fatalf("opened cursor = %#v", opened)
	}
	if _, err := openFileCursor(cursor, key, "user-a", "profile-b"); err == nil {
		t.Fatal("cross-profile cursor replay was accepted")
	}
}

func TestValidManagedPath(t *testing.T) {
	for _, value := range []string{"/", "/documents/report.pdf", "/nested/directory"} {
		if !validManagedPath(value) {
			t.Errorf("validManagedPath(%q) = false", value)
		}
	}
	for _, value := range []string{"", "relative", "/../secret", "/nested/../secret", "/windows\\path", "/nul\x00path"} {
		if validManagedPath(value) {
			t.Errorf("validManagedPath(%q) = true", value)
		}
	}
}

func TestSortManagedEntriesUsesStableNativeIdentity(t *testing.T) {
	resources := []storage.ManagerItem{
		{Name: "report", Locator: storage.ManagerLocator{NativeID: "b"}},
		{Name: "report", Locator: storage.ManagerLocator{NativeID: "a"}},
	}
	sortManagedEntries(resources)
	if got := resources[0].Locator.NativeID; got != "a" {
		t.Fatalf("first stable ID = %q, want a", got)
	}
}
