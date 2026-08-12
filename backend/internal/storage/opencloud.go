package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"backend/internal/observability"
)

// openCloudPaths implements pathBuilder for OpenCloud instances.
// OpenCloud exposes WebDAV endpoints at dav/spaces/ or /remote.php/webdav/.
type openCloudPaths struct{}

func (openCloudPaths) resourceURL(baseURL, username, resourceType, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := escapeDAVPath(cleanPath)
	if baseURL == "" {
		return escapedPath
	}
	return fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), escapedPath)
}

// uploadsURL returns the upload endpoint path. The username parameter is accepted for pathBuilder interface compatibility,
// while OpenCloud embeds user/space identity in the BaseURL.
func (openCloudPaths) uploadsURL(baseURL, username, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := escapeDAVPath(cleanPath)
	return fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), escapedPath)
}

func (openCloudPaths) listingPrefix(basePath, username, resourceType string) string {
	return basePath
}

// OpenCloudProvider provides storage provider integration for OpenCloud
// (ownCloud Infinite Scale / WebDAV cloud instances).
type OpenCloudProvider struct {
	*davProvider
}

var _ StorageProvider = (*OpenCloudProvider)(nil)

// NewOpenCloudProvider initializes an OpenCloudProvider with host URL and credentials.
func NewOpenCloudProvider(rawURL, username, password string) (*OpenCloudProvider, error) {
	baseURL := strings.TrimSuffix(rawURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid URL format: must be an absolute HTTPS URL with host")
	}

	// Default base path to /dav/spaces if no specific DAV path provided
	if parsed.Path == "" || parsed.Path == "/" {
		if parsed.Port() != "" {
			baseURL = fmt.Sprintf("%s://%s:%s/dav/spaces", parsed.Scheme, parsed.Hostname(), parsed.Port())
		} else {
			baseURL = fmt.Sprintf("%s://%s/dav/spaces", parsed.Scheme, parsed.Hostname())
		}
	}

	dp := &davProvider{
		providerName: "opencloud",
		BaseURL:      baseURL,
		Username:     username,
		Password:     password,
		HTTPClient: &http.Client{
			Transport:     newLoggingTransport(newDAVTransport(parsed.Hostname())),
			Timeout:       0,
			CheckRedirect: rejectEgressRedirect,
		},
		Threads:                8,
		UserAgent:              "OpenCloud-Migration-Worker/1.0",
		pb:                     openCloudPaths{},
		useBearerToken:         strings.HasPrefix(password, "Bearer ") || (username == "" && password != ""),
		supportedResourceTypes: map[string]bool{"files": true},
	}

	return &OpenCloudProvider{
		davProvider: dp,
	}, nil
}

// Connect verifies server connectivity and attempts fallback to /remote.php/webdav if needed.
// On successful fallback, BaseURL is permanently updated to the legacy path for all subsequent operations on this provider instance.
func (p *OpenCloudProvider) Connect(ctx context.Context) (bool, error) {
	ok, err := p.davProvider.Connect(ctx)
	if ok && err == nil {
		return true, nil
	}

	// If connecting to /dav/spaces failed, attempt fallback using a copy so an
	// unsuccessful probe never changes the URL concurrently used by operations.
	baseURL := p.baseURL()
	if strings.Contains(baseURL, "/dav/spaces") {
		fallbackURL := strings.Replace(baseURL, "/dav/spaces", "/remote.php/webdav", 1)
		fallback := &davProvider{
			providerName:           p.providerName,
			BaseURL:                fallbackURL,
			Username:               p.Username,
			Password:               p.Password,
			HTTPClient:             p.HTTPClient,
			Threads:                p.Threads,
			UserAgent:              p.UserAgent,
			pb:                     p.pb,
			disableChunkedUpload:   p.disableChunkedUpload,
			useBearerToken:         p.useBearerToken,
			supportedResourceTypes: p.supportedResourceTypes,
		}
		okFallback, errFallback := fallback.Connect(ctx)
		if okFallback && errFallback == nil {
			p.setBaseURL(fallbackURL)
			return true, nil
		}
	}

	return ok, err
}

// StreamUploadChunked performs a resumable upload using TUS 1.0.0 protocol if supported,
// falling back to standard WebDAV chunked upload.
func (p *OpenCloudProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	if size > 0 {
		consumed, err := p.uploadTUS(ctx, resourceType, filePath, stream, size, progressChan)
		if err == nil {
			return nil
		}
		if consumed {
			return fmt.Errorf("TUS upload failed after consuming the source stream: %w", err)
		}
		slog.WarnContext(ctx, "opencloud_tus_upload_fallback",
			slog.String("component", "storage"),
			observability.Error(err),
		)
	}

	return p.davProvider.StreamUploadChunked(ctx, resourceType, filePath, stream, size, progressChan)
}

// uploadTUS implements TUS 1.0.0 protocol POST session creation and PATCH chunk uploads.
func (p *OpenCloudProvider) uploadTUS(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) (bool, error) {
	uploadURL := p.pb.resourceURL(p.baseURL(), p.Username, resourceType, filePath)
	fileName := path.Base(filePath)
	metaVal := base64.StdEncoding.EncodeToString([]byte(fileName))

	req, err := p.newRequest("POST", uploadURL, nil)
	if err != nil {
		return false, err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.FormatInt(size, 10))
	req.Header.Set("Upload-Metadata", fmt.Sprintf("filename %s", metaVal))
	req.Header.Set("Content-Length", "0")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("TUS creation failed with status: %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		loc = resp.Header.Get("Upload-Url")
	}
	if loc == "" {
		return false, fmt.Errorf("TUS creation response missing Location header")
	}

	reqParsed, err := url.Parse(uploadURL)
	if err != nil {
		return false, err
	}
	locParsed, err := url.Parse(loc)
	if err != nil {
		return false, err
	}
	targetURL := reqParsed.ResolveReference(locParsed).String()

	var offset int64
	consumed := false
	chunkSize := int64(5 * 1024 * 1024)
	buf := make([]byte, chunkSize)

	for offset < size {
		select {
		case <-ctx.Done():
			return consumed, ctx.Err()
		default:
		}

		n, readErr := io.ReadFull(stream, buf)
		if n == 0 {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			return consumed, readErr
		}
		consumed = true

		chunkData := buf[:n]
		patchReq, err := p.newRequest("PATCH", targetURL, bytes.NewReader(chunkData))
		if err != nil {
			return consumed, err
		}
		patchReq = patchReq.WithContext(ctx)
		patchReq.Header.Set("Tus-Resumable", "1.0.0")
		patchReq.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
		patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
		patchReq.Header.Set("Content-Length", strconv.FormatInt(int64(n), 10))

		patchResp, err := p.HTTPClient.Do(patchReq)
		if err != nil {
			return consumed, err
		}
		patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusNoContent && patchResp.StatusCode != http.StatusOK {
			return consumed, fmt.Errorf("TUS patch failed with status: %d", patchResp.StatusCode)
		}

		offset += int64(n)
		if progressChan != nil {
			progressChan <- int64(n)
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return consumed, nil
}
