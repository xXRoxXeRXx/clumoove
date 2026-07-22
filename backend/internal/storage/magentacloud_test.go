package storage

import (
	"context"
	"testing"
)

func TestNewMagentacloudProviderValid(t *testing.T) {
	p, err := NewMagentacloudProvider("user@telekom.de", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil MagentacloudProvider")
	}
	if p.BaseURL != magentaCloudBaseURL {
		t.Errorf("expected BaseURL %s, got %s", magentaCloudBaseURL, p.BaseURL)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestMagentacloudProviderNonFilesRejected(t *testing.T) {
	p, err := NewMagentacloudProvider("user@telekom.de", "pass")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx := context.Background()
	invalidTypes := []string{"calendars", "contacts", "invalid"}

	for _, resourceType := range invalidTypes {
		if _, err := p.GetDirectoryListing(ctx, resourceType, "/"); err == nil {
			t.Errorf("GetDirectoryListing: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.InspectResource(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("InspectResource: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.StreamDownload(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("StreamDownload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUpload(ctx, resourceType, "/test.txt", nil, 0); err == nil {
			t.Errorf("StreamUpload: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.StreamUploadChunked(ctx, resourceType, "/test.txt", nil, 0, nil); err == nil {
			t.Errorf("StreamUploadChunked: expected error for resourceType %q, got nil", resourceType)
		}
		if _, _, err := p.FileExists(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("FileExists: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.DeleteFile(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("DeleteFile: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.RenameFile(ctx, resourceType, "/old.txt", "/new.txt"); err == nil {
			t.Errorf("RenameFile: expected error for resourceType %q, got nil", resourceType)
		}
		if _, err := p.GetFileHash(ctx, resourceType, "/test.txt"); err == nil {
			t.Errorf("GetFileHash: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.CreateParentDirectories(ctx, resourceType, "/dir/test.txt"); err == nil {
			t.Errorf("CreateParentDirectories: expected error for resourceType %q, got nil", resourceType)
		}
		if err := p.CreateDirectory(ctx, resourceType, "/dir"); err == nil {
			t.Errorf("CreateDirectory: expected error for resourceType %q, got nil", resourceType)
		}
	}
}

func TestMagentaPaths(t *testing.T) {
	mp := magentaPaths{}
	urlStr := mp.resourceURL("https://magentacloud.de/remote.php/webdav", "user", "files", "/my folder/file.txt")
	expected := "https://magentacloud.de/remote.php/webdav/my%20folder/file.txt"
	if urlStr != expected {
		t.Errorf("resourceURL = %s, want %s", urlStr, expected)
	}
}
