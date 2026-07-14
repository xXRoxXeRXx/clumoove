package storage

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type WebDAVProvider struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
}

type webdavProgressReader struct {
	reader       io.Reader
	progressChan chan<- int64
}

func (pr *webdavProgressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 && pr.progressChan != nil {
		pr.progressChan <- int64(n)
	}
	return n, err
}

func NewWebDAVProvider(rawURL, username, password string) (*WebDAVProvider, error) {
	baseURL := strings.TrimSuffix(rawURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid URL format: must be an absolute URL with scheme and host")
	}

	tr := &http.Transport{
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		// Pin egress to a re-validated IP on every connection so a DNS
		// rebind between validation and connect cannot reach internal/metadata
		// addresses. The original hostname stays in the request URL, preserving
		// SNI / certificate validation.
		DialContext: egressDialer(parsed.Hostname()),
	}

	return &WebDAVProvider{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Transport: tr,
			Timeout:   0,
		},
	}, nil
}

func (p *WebDAVProvider) Close() error {
	p.HTTPClient.CloseIdleConnections()
	return nil
}

func (p *WebDAVProvider) buildResourceURL(resourceType string, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	if cleanPath == "" {
		return p.BaseURL
	}
	escapedPath := (&url.URL{Path: cleanPath}).String()
	return fmt.Sprintf("%s/%s", p.BaseURL, escapedPath)
}

func (p *WebDAVProvider) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.Username, p.Password)
	req.Header.Set("User-Agent", "WebDAV-Migration-Worker/1.0")
	return req, nil
}

func (p *WebDAVProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := p.buildResourceURL("files", "/")
	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:">
			<d:prop>
				<d:resourcetype/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return false, fmt.Errorf("webdav connect: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("connection failed with status code: %d", resp.StatusCode)
	}

	return true, nil
}

func (p *WebDAVProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, resourcePath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<d:getetag/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return CloudResource{}, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return CloudResource{}, fmt.Errorf("webdav inspect: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CloudResource{}, fmt.Errorf("inspect failed with status: %d", resp.StatusCode)
	}

	var multistatus XMLMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return CloudResource{}, err
	}

	if len(multistatus.Responses) == 0 {
		return CloudResource{}, fmt.Errorf("no resource found at path: %s", resourcePath)
	}

	r := multistatus.Responses[0]
	var res CloudResource
	res.Path = resourcePath
	res.Name = path.Base(resourcePath)

	for _, pstat := range r.Propstat {
		if strings.Contains(pstat.Status, "200 OK") {
			prop := pstat.Prop
			res.IsDir = prop.ResourceType.Collection != nil

			if !res.IsDir {
				if size, err := strconv.ParseInt(prop.GetContentLength, 10, 64); err == nil {
					res.Size = size
				}
			}

			if prop.GetLastModified != "" {
				if t, err := time.Parse(time.RFC1123, prop.GetLastModified); err == nil {
					res.LastModified = t
				}
			}
		}
	}

	return res, nil
}

func (p *WebDAVProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	u := p.buildResourceURL(resourceType, dirPath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<d:getetag/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := doPropfind(ctx, p.HTTPClient, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("webdav listing: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("PROPFIND failed with status: %d", resp.StatusCode)
	}

	var multistatus XMLMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return nil, err
	}

	var resources []CloudResource

	uParsed, parseErr := url.Parse(p.BaseURL)
	var prefixPath string
	if parseErr == nil {
		prefixPath = strings.TrimSuffix(uParsed.Path, "/")
	}

	for _, r := range multistatus.Responses {
		decodedHref, err := url.PathUnescape(r.Href)
		if err != nil {
			decodedHref = r.Href
		}

		if strings.HasPrefix(decodedHref, "http://") || strings.HasPrefix(decodedHref, "https://") {
			if parsed, parseErr := url.Parse(decodedHref); parseErr == nil {
				decodedHref = parsed.Path
			}
		}

		if !strings.HasPrefix(decodedHref, prefixPath) {
			continue
		}

		relativeHref := strings.TrimPrefix(decodedHref, prefixPath)
		if relativeHref == "" {
			relativeHref = "/"
		}

		// Skip the directory itself
		cleanDirPath := "/" + strings.Trim(dirPath, "/")
		cleanRelHref := "/" + strings.Trim(relativeHref, "/")
		if cleanRelHref == cleanDirPath || (cleanDirPath == "/" && cleanRelHref == "") {
			continue
		}

		var res CloudResource
		res.Path = relativeHref
		res.Name = path.Base(relativeHref)

		for _, pstat := range r.Propstat {
			if strings.Contains(pstat.Status, "200 OK") {
				prop := pstat.Prop
				res.IsDir = prop.ResourceType.Collection != nil

				if !res.IsDir {
					if size, err := strconv.ParseInt(prop.GetContentLength, 10, 64); err == nil {
						res.Size = size
					}
				}

				if prop.GetLastModified != "" {
					if t, err := time.Parse(time.RFC1123, prop.GetLastModified); err == nil {
						res.LastModified = t
					}
				}
			}
		}
		resources = append(resources, res)
	}

	return resources, nil
}

func (p *WebDAVProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	u := p.buildResourceURL(resourceType, filePath)
	req, err := p.newRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, fmt.Errorf("webdav download: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	return resp.Body, nil
}

func (p *WebDAVProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	u := p.buildResourceURL(resourceType, filePath)

	err := p.CreateParentDirectories(ctx, resourceType, filePath)
	if err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	// Apply a dynamic per-request timeout scaled by file size (same policy as
	// NextcloudProvider.StreamUpload) so a stalled WebDAV server cannot block a
	// worker thread for the full 10-minute http.Client deadline.
	baseTimeout := 5 * time.Minute
	if size > 0 {
		baseTimeout += time.Duration(size/(50*1024*1024)) * time.Minute
	}
	if baseTimeout > 12*time.Hour {
		baseTimeout = 12 * time.Hour
	}
	uploadCtx, cancel := context.WithTimeout(ctx, baseTimeout)
	defer cancel()

	req, err := p.newRequest("PUT", u, stream)
	if err != nil {
		return err
	}
	req = req.WithContext(uploadCtx)
	if size > 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("webdav upload: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	return nil
}

func (p *WebDAVProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	progressReader := &webdavProgressReader{
		reader:       stream,
		progressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, progressReader, size)
}

func (p *WebDAVProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, filePath)
	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
<d:propfind xmlns:d="DAV:">
	<d:prop>
		<d:getcontentlength/>
		<d:resourcetype/>
	</d:prop>
</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return false, 0, fmt.Errorf("webdav file-exists: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, 0, fmt.Errorf("PROPFIND check failed with status: %d", resp.StatusCode)
	}

	var multistatus XMLMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return false, 0, err
	}

	if len(multistatus.Responses) == 0 {
		return false, 0, nil
	}

	r := multistatus.Responses[0]
	for _, pstat := range r.Propstat {
		if strings.Contains(pstat.Status, "200 OK") {
			prop := pstat.Prop
			isDir := prop.ResourceType.Collection != nil
			if isDir {
				return true, 0, nil
			}
			size, _ := strconv.ParseInt(prop.GetContentLength, 10, 64)
			return true, size, nil
		}
	}

	return false, 0, nil
}

func (p *WebDAVProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, filePath)
	req, err := p.newRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("webdav delete: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete failed with status: %d", resp.StatusCode)
	}
	return nil
}

func (p *WebDAVProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, oldPath)
	req, err := p.newRequest("MOVE", u, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	destURL := p.buildResourceURL(resourceType, newPath)
	req.Header.Set("Destination", destURL)
	req.Header.Set("Overwrite", "T")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("webdav move: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("move failed with status: %d", resp.StatusCode)
	}
	return nil
}

func (p *WebDAVProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return "", fmt.Errorf("checksum not available")
}

func (p *WebDAVProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	parts := strings.Split(strings.Trim(dir, "/"), "/")

	currentPath := ""
	for _, part := range parts {
		currentPath = currentPath + "/" + part
		u := p.buildResourceURL(resourceType, currentPath)

		req, err := p.newRequest("MKCOL", u, nil)
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("webdav mkdir: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("failed to create directory %s, status: %d", currentPath, resp.StatusCode)
		}
	}
	return nil
}

// CreateDirectory creates the given directory path (and all intermediate parents) on
// the WebDAV server using MKCOL requests. It is idempotent — 405 Method Not Allowed
// (already exists) is treated as success.
func (p *WebDAVProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Ensure all ancestor directories exist first.
	if err := p.CreateParentDirectories(ctx, resourceType, dirPath); err != nil {
		return err
	}

	// MKCOL the target directory itself.
	u := p.buildResourceURL(resourceType, dirPath)
	req, err := p.newRequest("MKCOL", u, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("webdav mkdir: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("failed to create directory %s, status: %d", dirPath, resp.StatusCode)
	}
	return nil
}

func cleanETag(etag string) string {
	etag = strings.TrimPrefix(etag, "W/")
	return strings.Trim(etag, "\"")
}

func (p *WebDAVProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}

	u := p.buildResourceURL(resourceType, filePath)
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" ?>
<d:propertyupdate xmlns:d="DAV:">
  <d:set>
    <d:prop>
      <d:lastmodified>%s</d:lastmodified>
    </d:prop>
  </d:set>
</d:propertyupdate>`, meta.ModifiedTime.UTC().Format(time.RFC3339))

	req, err := p.newRequest("PROPPATCH", u, strings.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	return nil
}
