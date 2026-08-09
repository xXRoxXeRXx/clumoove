package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
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

func (p *OpenCloudProvider) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(p.Password, "Bearer ") {
		req.Header.Set("Authorization", p.Password)
	} else if p.Username == "" && p.Password != "" {
		req.Header.Set("Authorization", "Bearer "+p.Password)
	} else {
		req.SetBasicAuth(p.Username, p.Password)
	}
	req.Header.Set("User-Agent", p.UserAgent)
	return req, nil
}

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
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Transport:     newDAVTransport(parsed.Hostname()),
			Timeout:       0,
			CheckRedirect: rejectEgressRedirect,
		},
		Threads:                8,
		UserAgent:              "OpenCloud-Migration-Worker/1.0",
		pb:                     openCloudPaths{},
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

	// If connecting to /dav/spaces failed, attempt fallback to legacy /remote.php/webdav
	if strings.Contains(p.BaseURL, "/dav/spaces") {
		fallbackURL := strings.Replace(p.BaseURL, "/dav/spaces", "/remote.php/webdav", 1)
		origURL := p.BaseURL
		p.BaseURL = fallbackURL
		okFallback, errFallback := p.davProvider.Connect(ctx)
		if okFallback && errFallback == nil {
			return true, nil
		}
		p.BaseURL = origURL
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
		err := p.uploadTUS(ctx, resourceType, filePath, stream, size, progressChan)
		if err == nil {
			return nil
		}
		log.Printf("opencloud: TUS upload failed for %q (%v); falling back to DAV chunked", filePath, err)
	}

	return p.davProvider.StreamUploadChunked(ctx, resourceType, filePath, stream, size, progressChan)
}

// uploadTUS implements TUS 1.0.0 protocol POST session creation and PATCH chunk uploads.
func (p *OpenCloudProvider) uploadTUS(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	uploadURL := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)
	fileName := path.Base(filePath)
	metaVal := base64.StdEncoding.EncodeToString([]byte(fileName))

	req, err := p.newRequest("POST", uploadURL, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", strconv.FormatInt(size, 10))
	req.Header.Set("Upload-Metadata", fmt.Sprintf("filename %s", metaVal))
	req.Header.Set("Content-Length", "0")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TUS creation failed with status: %d", resp.StatusCode)
	}

	loc := resp.Header.Get("Location")
	if loc == "" {
		loc = resp.Header.Get("Upload-Url")
	}
	if loc == "" {
		return fmt.Errorf("TUS creation response missing Location header")
	}

	reqParsed, err := url.Parse(uploadURL)
	if err != nil {
		return err
	}
	locParsed, err := url.Parse(loc)
	if err != nil {
		return err
	}
	targetURL := reqParsed.ResolveReference(locParsed).String()

	var offset int64 = 0
	chunkSize := int64(5 * 1024 * 1024)
	buf := make([]byte, chunkSize)

	for offset < size {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, readErr := io.ReadFull(stream, buf)
		if n == 0 {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			return readErr
		}

		chunkData := buf[:n]
		patchReq, err := p.newRequest("PATCH", targetURL, bytes.NewReader(chunkData))
		if err != nil {
			return err
		}
		patchReq = patchReq.WithContext(ctx)
		patchReq.Header.Set("Tus-Resumable", "1.0.0")
		patchReq.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
		patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
		patchReq.Header.Set("Content-Length", strconv.FormatInt(int64(n), 10))

		patchResp, err := p.HTTPClient.Do(patchReq)
		if err != nil {
			return err
		}
		patchResp.Body.Close()

		if patchResp.StatusCode != http.StatusNoContent && patchResp.StatusCode != http.StatusOK {
			return fmt.Errorf("TUS patch failed with status: %d", patchResp.StatusCode)
		}

		offset += int64(n)
		if progressChan != nil {
			progressChan <- int64(n)
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return nil
}
