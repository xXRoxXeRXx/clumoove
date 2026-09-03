package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func TestS3StreamUploadHeaders(t *testing.T) {
	type reqRecord struct {
		method          string
		rawQuery        string
		transferEnc     string
		contentLen      int64
		checksumAlgoHdr string
		trailerHdr      string
	}

	var mu sync.Mutex
	var records []reqRecord

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		records = append(records, reqRecord{
			method:          r.Method,
			rawQuery:        r.URL.RawQuery,
			transferEnc:     r.Header.Get("Transfer-Encoding"),
			contentLen:      r.ContentLength,
			checksumAlgoHdr: r.Header.Get("x-amz-sdk-checksum-algorithm"),
			trailerHdr:      r.Header.Get("x-amz-trailer"),
		})
		mu.Unlock()

		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()

		if r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploads") {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Bucket>my-bucket</Bucket>
  <Key>multipart.bin</Key>
  <UploadId>test-upload-id</UploadId>
</InitiateMultipartUploadResult>`))
			return
		}

		if r.Method == http.MethodPut && strings.Contains(r.URL.RawQuery, "uploadId=") {
			w.Header().Set("ETag", `"part-etag"`)
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodPost && strings.Contains(r.URL.RawQuery, "uploadId=") {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Location>http://example.com/my-bucket/multipart.bin</Location>
  <Bucket>my-bucket</Bucket>
  <Key>multipart.bin</Key>
  <ETag>"final-etag"</ETag>
</CompleteMultipartUploadResult>`))
			return
		}

		if r.Method == http.MethodPut {
			w.Header().Set("ETag", `"single-etag"`)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("acc", "sec", "")),
		config.WithHTTPClient(server.Client()),
		config.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		config.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
	)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(server.URL)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})

	p := &S3Provider{
		client: client,
		bucket: "my-bucket",
	}

	// 1. Test single-part upload (e.g. 1 KB)
	smallPayload := []byte("hello s3 world")
	err = p.StreamUpload(context.Background(), "files", "/single.txt", bytes.NewReader(smallPayload), int64(len(smallPayload)))
	if err != nil {
		t.Fatalf("StreamUpload single-part failed: %v", err)
	}

	// 2. Test multipart upload (> 16MB default threshold to trigger multiUploader)
	payloadSize := int64(17 * 1024 * 1024)
	payload := bytes.Repeat([]byte("a"), int(payloadSize))
	err = p.StreamUpload(context.Background(), "files", "/multipart.bin", bytes.NewReader(payload), payloadSize)
	if err != nil {
		t.Fatalf("StreamUpload multipart failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var singlePartFound, partUploadFound bool
	for _, rec := range records {
		if rec.method == http.MethodPut && !strings.Contains(rec.rawQuery, "uploadId=") {
			singlePartFound = true
			if strings.Contains(rec.transferEnc, "aws-chunked") {
				t.Errorf("PutObject used unexpected aws-chunked Transfer-Encoding: %s", rec.transferEnc)
			}
			if rec.checksumAlgoHdr != "" {
				t.Errorf("PutObject sent unexpected x-amz-sdk-checksum-algorithm: %s", rec.checksumAlgoHdr)
			}
			if rec.trailerHdr != "" {
				t.Errorf("PutObject sent unexpected x-amz-trailer: %s", rec.trailerHdr)
			}
		}
		if rec.method == http.MethodPut && strings.Contains(rec.rawQuery, "uploadId=") {
			partUploadFound = true
			if strings.Contains(rec.transferEnc, "aws-chunked") {
				t.Errorf("UploadPart used unexpected aws-chunked Transfer-Encoding: %s", rec.transferEnc)
			}
			if rec.checksumAlgoHdr != "" {
				t.Errorf("UploadPart sent unexpected x-amz-sdk-checksum-algorithm: %s", rec.checksumAlgoHdr)
			}
			if rec.trailerHdr != "" {
				t.Errorf("UploadPart sent unexpected x-amz-trailer: %s", rec.trailerHdr)
			}
			if rec.contentLen <= 0 {
				t.Errorf("UploadPart expected positive Content-Length, got %d", rec.contentLen)
			}
		}
	}
	if !singlePartFound {
		t.Fatal("expected at least one PutObject request during single-part upload")
	}
	if !partUploadFound {
		t.Fatal("expected at least one UploadPart request during multipart upload")
	}
}
