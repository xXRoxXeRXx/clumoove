package storage

import (
	"context"
	"errors"
	"testing"
)

func TestNewS3ProviderValidURL(t *testing.T) {
	p, err := NewS3Provider("s3://my-bucket?region=us-west-2", "accessKey", "secretKey")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil S3Provider")
	}
	if p.bucket != "my-bucket" {
		t.Errorf("expected bucket my-bucket, got %s", p.bucket)
	}
	if !p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = true")
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestNewS3ProviderInvalidURL(t *testing.T) {
	cases := []string{
		"http://my-bucket",
		"s3://",
		"invalid-url",
	}
	for _, u := range cases {
		if _, err := NewS3Provider(u, "acc", "sec"); err == nil {
			t.Errorf("NewS3Provider(%q): expected error, got nil", u)
		}
	}
}

func TestS3ProviderCleanKey(t *testing.T) {
	p, err := NewS3Provider("s3://my-bucket", "acc", "sec")
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	cases := []struct {
		input    string
		expected string
	}{
		{"/path/to/file.txt", "path/to/file.txt"},
		{"\\path\\to\\file.txt", "path/to/file.txt"},
		{"file.txt", "file.txt"},
		{"/", ""},
	}
	for _, c := range cases {
		actual := p.cleanKey(c.input)
		if actual != c.expected {
			t.Errorf("cleanKey(%q) = %q, want %q", c.input, actual, c.expected)
		}
	}
}

func TestIsS3AuthError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("operation error S3: HeadBucket, https response error StatusCode: 403, RequestID: 123, api error AccessDenied: Access Denied"), true},
		{errors.New("InvalidAccessKeyId: The AWS Access Key Id you provided does not exist"), true},
		{errors.New("SignatureDoesNotMatch: The request signature we calculated does not match"), true},
		{errors.New("NoSuchKey: The specified key does not exist"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isS3AuthError(c.err); got != c.want {
			t.Errorf("isS3AuthError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestS3ProviderNonFilesRejected(t *testing.T) {
	p, err := NewS3Provider("s3://my-bucket", "acc", "sec")
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
