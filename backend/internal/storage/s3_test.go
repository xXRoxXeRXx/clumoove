package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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
	if p.httpClient == nil {
		t.Error("expected provider to retain its HTTP client for cleanup")
	}
	if p.SupportsAtomicRename() {
		t.Error("expected SupportsAtomicRename() = false: S3 rename is copy-and-delete")
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
		{&types.AccessDenied{}, true},
		{s3StatusError{status: 401}, true},
		{s3StatusError{status: 403}, true},
		{errors.New("dial tcp 10.0.0.1:4032: connect: connection refused"), false},
		{errors.New("NoSuchKey: The specified key does not exist"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isS3AuthError(c.err); got != c.want {
			t.Errorf("isS3AuthError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

type s3StatusError struct{ status int }

func (e s3StatusError) Error() string       { return "s3 response error" }
func (e s3StatusError) HTTPStatusCode() int { return e.status }

func TestIsS3NotFoundError(t *testing.T) {
	if !isS3NotFoundError(&types.NotFound{}) {
		t.Fatal("typed S3 NotFound must be recognized")
	}
	if !isS3NotFoundError(s3StatusError{status: 404}) {
		t.Fatal("HTTP 404 must be recognized")
	}
	if isS3NotFoundError(errors.New("dial tcp 10.0.0.1:4040: connect: connection refused")) {
		t.Fatal("a port number must not be treated as a not-found response")
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

func TestS3UploadTargetPartsLeavesMultipartHeadroom(t *testing.T) {
	if s3UploadTargetParts >= 10000 {
		t.Fatalf("s3UploadTargetParts = %d, must leave room below S3's 10,000-part limit", s3UploadTargetParts)
	}
}
