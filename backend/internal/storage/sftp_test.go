package storage

import (
	"context"
	"errors"
	"testing"
)

func TestNewSFTPProviderValid(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com:2222/", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil SFTPProvider")
	}
	if p.Host != "example.com" || p.Port != "2222" {
		t.Errorf("expected host example.com port 2222, got host %s port %s", p.Host, p.Port)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestNewSFTPProviderDefaultPort(t *testing.T) {
	p, err := NewSFTPProvider("sftp://10.0.0.1/", "user", "pass")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.Port != "22" {
		t.Errorf("expected default port 22, got %s", p.Port)
	}
}

func TestNewSFTPProviderPrivateKey(t *testing.T) {
	mockKey := "-----BEGIN OPENSSH PRIVATE KEY-----\nmock\n-----END OPENSSH PRIVATE KEY-----"
	p, err := NewSFTPProvider("sftp://example.com/", "user", mockKey)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p.PrivateKey != mockKey {
		t.Errorf("expected PrivateKey to be populated")
	}
	if p.Password != "" {
		t.Errorf("expected Password to be emptied when PrivateKey is provided")
	}
}

func TestIsSFTPAuthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]"), true},
		{errors.New("permission denied"), true},
		{errors.New("sftp connect: authentication failed"), true},
		{errors.New("file does not exist"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isSFTPAuthError(c.err); got != c.want {
			t.Errorf("isSFTPAuthError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestSFTPProviderNonFilesRejected(t *testing.T) {
	p, err := NewSFTPProvider("sftp://example.com/", "user", "pass")
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
