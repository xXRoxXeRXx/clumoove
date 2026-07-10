package storage

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Provider struct {
	client *s3.Client
	bucket string
}

// Ensure S3Provider implements StorageProvider
var _ StorageProvider = (*S3Provider)(nil)

func NewS3Provider(rawURL, accessKey, secretKey string) (*S3Provider, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 URL: %w", err)
	}

	if u.Scheme != "s3" {
		return nil, fmt.Errorf("invalid scheme %q, expected s3", u.Scheme)
	}

	bucket := u.Host
	if bucket == "" {
		return nil, fmt.Errorf("missing bucket name in S3 URL")
	}

	endpoint := u.Query().Get("endpoint")
	region := u.Query().Get("region")
	if region == "" {
		region = "us-east-1"
	}
	insecure := u.Query().Get("insecure") == "true"

	if endpoint != "" {
		epURL, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid endpoint URL: %w", err)
		}

		if epURL.Scheme != "http" && epURL.Scheme != "https" {
			return nil, fmt.Errorf("endpoint URL must specify an explicit http or https scheme")
		}

		if epURL.Scheme == "http" {
			if !insecure {
				return nil, fmt.Errorf("insecure connection (HTTP) is not allowed for public endpoints")
			}
			host := epURL.Hostname()
			if !isLocalOrPrivateHost(host) {
				return nil, fmt.Errorf("insecure connection (HTTP) is only allowed for local or private endpoints")
			}
		}
	}

	// Load default AWS SDK config.
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		}
	})

	return &S3Provider{
		client: client,
		bucket: bucket,
	}, nil
}

func isLocalOrPrivateHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 10 {
			return true
		}
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	} else if ip6 := ip.To16(); ip6 != nil {
		if ip6[0] == 0xfc || ip6[0] == 0xfd {
			return true
		}
		if ip6[0] == 0xfe && (ip6[1]&0xc0) == 0x80 {
			return true
		}
	}
	return false
}

func (p *S3Provider) cleanKey(filePath string) string {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	filePath = path.Clean("/" + filePath)
	filePath = strings.TrimPrefix(filePath, "/")
	return filePath
}

func isS3AuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "accessdenied") ||
		strings.Contains(errStr, "invalidaccesskeyid") ||
		strings.Contains(errStr, "signaturedoesnotmatch") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "401")
}

func (p *S3Provider) Close() error {
	return nil
}

func (p *S3Provider) Connect(ctx context.Context) (bool, error) {
	_, err := p.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(p.bucket),
	})
	if err != nil {
		if isS3AuthError(err) {
			return false, ErrAuth
		}
		log.Printf("S3 HeadBucket failed: %v", err)
		return false, fmt.Errorf("failed to connect to S3 bucket %s", p.bucket)
	}
	return true, nil
}

func (p *S3Provider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanDir := p.cleanKey(dirPath)
	prefix := ""
	if cleanDir != "" {
		prefix = cleanDir + "/"
	}

	var resources []CloudResource
	paginator := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(p.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			if isS3AuthError(err) {
				return nil, ErrAuth
			}
			log.Printf("S3 ListObjectsV2 failed for path %q: %v", dirPath, err)
			return nil, fmt.Errorf("failed to list directory contents")
		}

		for _, cp := range page.CommonPrefixes {
			if cp.Prefix == nil {
				continue
			}
			rawPrefix := *cp.Prefix
			trimmed := strings.TrimSuffix(rawPrefix, "/")
			name := path.Base(trimmed)
			resources = append(resources, CloudResource{
				Path:  "/" + trimmed,
				Name:  name,
				IsDir: true,
			})
		}

		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			key := *obj.Key
			if key == prefix {
				continue
			}
			name := path.Base(key)
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			var lastModified time.Time
			if obj.LastModified != nil {
				lastModified = *obj.LastModified
			}

			resources = append(resources, CloudResource{
				Path:         "/" + key,
				Name:         name,
				Size:         size,
				IsDir:        false,
				LastModified: lastModified,
			})
		}
	}

	return resources, nil
}

func decodeBase64ToHex(b64 string) (string, error) {
	bytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func (p *S3Provider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)
	if cleanPath == "" {
		return CloudResource{
			Path:  "/",
			Name:  "",
			IsDir: true,
		}, nil
	}

	head, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(p.bucket),
		Key:          aws.String(cleanPath),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err == nil {
		var size int64
		if head.ContentLength != nil {
			size = *head.ContentLength
		}
		var lastMod time.Time
		if head.LastModified != nil {
			lastMod = *head.LastModified
		}
		hashVal := ""
		if head.ChecksumSHA256 != nil && *head.ChecksumSHA256 != "" && !strings.Contains(*head.ChecksumSHA256, "-") {
			if hexStr, err := decodeBase64ToHex(*head.ChecksumSHA256); err == nil {
				hashVal = "SHA256:" + hexStr
			}
		}
		if hashVal == "" && head.Metadata != nil {
			if sha, ok := head.Metadata["sha256"]; ok {
				hashVal = "SHA256:" + sha
			}
		}
		if hashVal == "" && head.ETag != nil {
			etag := strings.Trim(*head.ETag, `"`)
			if !strings.Contains(etag, "-") {
				hashVal = "MD5:" + etag
			}
		}

		return CloudResource{
			Path:         "/" + cleanPath,
			Name:         path.Base(cleanPath),
			Size:         size,
			IsDir:        false,
			Hash:         hashVal,
			LastModified: lastMod,
		}, nil
	}

	listResp, errList := p.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(p.bucket),
		Prefix:  aws.String(cleanPath + "/"),
		MaxKeys: aws.Int32(1),
	})
	if errList == nil && ((listResp.KeyCount != nil && *listResp.KeyCount > 0) || len(listResp.Contents) > 0 || len(listResp.CommonPrefixes) > 0) {
		return CloudResource{
			Path:  "/" + cleanPath,
			Name:  path.Base(cleanPath),
			IsDir: true,
		}, nil
	}

	if isS3AuthError(err) {
		return CloudResource{}, ErrAuth
	}
	return CloudResource{}, os.ErrNotExist
}

func (p *S3Provider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)
	resp, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(cleanPath),
	})
	if err != nil {
		if isS3AuthError(err) {
			return nil, ErrAuth
		}
		return nil, fmt.Errorf("s3 get object failed: %w", err)
	}

	return resp.Body, nil
}

type progressTrackingReader struct {
	reader       io.Reader
	progressChan chan<- int64
}

func (pr *progressTrackingReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 && pr.progressChan != nil {
		pr.progressChan <- int64(n)
	}
	return n, err
}

func (p *S3Provider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)

	uploader := manager.NewUploader(p.client, func(u *manager.Uploader) {
		partSize := int64(8 * 1024 * 1024) // 8MB default
		if size > 0 {
			calculated := size / 9000
			if calculated > partSize {
				partSize = calculated
			}
		}
		u.PartSize = partSize
		u.Concurrency = 3
	})

	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:            aws.String(p.bucket),
		Key:               aws.String(cleanPath),
		Body:              stream,
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	})
	if err != nil {
		if isS3AuthError(err) {
			return ErrAuth
		}
		return fmt.Errorf("s3 upload failed: %w", err)
	}

	return nil
}

func (p *S3Provider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	progressReader := &progressTrackingReader{
		reader:       stream,
		progressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, progressReader, size)
}

func (p *S3Provider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if resourceType != "files" {
		return false, 0, fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)
	head, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(cleanPath),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NotFound") {
			return false, 0, nil
		}
		if isS3AuthError(err) {
			return false, 0, ErrAuth
		}
		return false, 0, fmt.Errorf("s3 head object failed: %w", err)
	}

	var size int64
	if head.ContentLength != nil {
		size = *head.ContentLength
	}
	return true, size, nil
}

func (p *S3Provider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(cleanPath),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "NotFound") {
			return nil
		}
		if isS3AuthError(err) {
			return ErrAuth
		}
		return fmt.Errorf("s3 delete object failed: %w", err)
	}

	return nil
}

func (p *S3Provider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	cleanPath := p.cleanKey(filePath)
	head, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(p.bucket),
		Key:          aws.String(cleanPath),
		ChecksumMode: types.ChecksumModeEnabled,
	})
	if err != nil {
		if isS3AuthError(err) {
			return "", ErrAuth
		}
		return "", fmt.Errorf("s3 head object failed: %w", err)
	}

	if head.ChecksumSHA256 != nil && *head.ChecksumSHA256 != "" && !strings.Contains(*head.ChecksumSHA256, "-") {
		if hexStr, err := decodeBase64ToHex(*head.ChecksumSHA256); err == nil {
			return "SHA256:" + hexStr, nil
		}
	}

	if head.Metadata != nil {
		if sha, ok := head.Metadata["sha256"]; ok {
			return "SHA256:" + sha, nil
		}
	}

	if head.ETag != nil {
		etag := strings.Trim(*head.ETag, `"`)
		if !strings.Contains(etag, "-") {
			return "MD5:" + etag, nil
		}
	}

	return "", nil
}

func (p *S3Provider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}

	oldKey := p.cleanKey(oldPath)
	newKey := p.cleanKey(newPath)

	head, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(oldKey),
	})
	if err != nil {
		if isS3AuthError(err) {
			return ErrAuth
		}
		return fmt.Errorf("s3 head object failed during rename: %w", err)
	}

	var size int64
	if head.ContentLength != nil {
		size = *head.ContentLength
	}

	if size <= 5*1024*1024*1024 {
		copySrc := url.PathEscape(p.bucket + "/" + oldKey)
		_, err = p.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(p.bucket),
			CopySource: aws.String(copySrc),
			Key:        aws.String(newKey),
		})
		if err != nil {
			if isS3AuthError(err) {
				return ErrAuth
			}
			return fmt.Errorf("s3 copy object failed during rename: %w", err)
		}
	} else {
		err = p.multipartCopy(ctx, oldKey, newKey, size)
		if err != nil {
			if isS3AuthError(err) {
				return ErrAuth
			}
			return fmt.Errorf("s3 multipart copy failed during rename: %w", err)
		}
	}

	_, err = p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(oldKey),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object failed during rename cleanup: %w", err)
	}

	return nil
}

func (p *S3Provider) multipartCopy(ctx context.Context, srcKey, dstKey string, size int64) error {
	createResp, err := p.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(dstKey),
	})
	if err != nil {
		return err
	}
	uploadID := createResp.UploadId

	var partSize int64 = 5 * 1024 * 1024 * 1024
	var partNumber int32 = 1
	var completedParts []types.CompletedPart

	for offset := int64(0); offset < size; offset += partSize {
		end := offset + partSize - 1
		if end >= size {
			end = size - 1
		}

		copySourceRange := fmt.Sprintf("bytes=%d-%d", offset, end)
		copySrc := url.PathEscape(p.bucket + "/" + srcKey)

		partResp, err := p.client.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket:          aws.String(p.bucket),
			Key:             aws.String(dstKey),
			PartNumber:      aws.Int32(partNumber),
			UploadId:        uploadID,
			CopySource:      aws.String(copySrc),
			CopySourceRange: aws.String(copySourceRange),
		})
		if err != nil {
			p.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(p.bucket),
				Key:      aws.String(dstKey),
				UploadId: uploadID,
			})
			return err
		}

		completedParts = append(completedParts, types.CompletedPart{
			ETag:       partResp.CopyPartResult.ETag,
			PartNumber: aws.Int32(partNumber),
		})
		partNumber++
	}

	_, err = p.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(p.bucket),
		Key:      aws.String(dstKey),
		UploadId: uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	return err
}

func (p *S3Provider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}
	return nil
}

func (p *S3Provider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by S3 provider", resourceType)
	}
	return nil
}
