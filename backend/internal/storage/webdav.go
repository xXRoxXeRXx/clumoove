package storage

import (
	"bytes"
	"context"
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

	return &WebDAVProvider{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Timeout: 0,
		},
	}, nil
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
		return false, fmt.Errorf("authentication failed: unauthorized (401)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("connection failed with status code: %d", resp.StatusCode)
	}

	return true, nil
}

func (p *WebDAVProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
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
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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

	req, err := p.newRequest("PUT", u, stream)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	if size > 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

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
	u := p.buildResourceURL(resourceType, filePath)
	req, err := p.newRequest("HEAD", u, nil)
	if err != nil {
		return false, 0, err
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, 0, err
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		contentLength, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		return true, contentLength, nil
	}

	return false, 0, fmt.Errorf("HEAD check failed with status: %d", resp.StatusCode)
}

func (p *WebDAVProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
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

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete failed with status: %d", resp.StatusCode)
	}
	return nil
}

func (p *WebDAVProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	return "", fmt.Errorf("checksum not available")
}

func (p *WebDAVProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
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
		resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("failed to create directory %s, status: %d", currentPath, resp.StatusCode)
		}
	}
	return nil
}

func cleanETag(etag string) string {
	etag = strings.TrimPrefix(etag, "W/")
	return strings.Trim(etag, "\"")
}
