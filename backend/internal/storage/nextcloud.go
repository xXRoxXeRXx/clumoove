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
	"regexp"
	"strconv"
	"strings"
	"time"
)

type NextcloudProvider struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
	Threads    int
}

// XML structures for PROPFIND
type XMLMultistatus struct {
	XMLName   xml.Name      `xml:"multistatus"`
	Responses []XMLResponse `xml:"response"`
}

type XMLResponse struct {
	Href     string        `xml:"href"`
	Propstat []XMLPropstat `xml:"propstat"`
}

type XMLPropstat struct {
	Prop   XMLProp `xml:"prop"`
	Status string  `xml:"status"`
}

type XMLProp struct {
	GetLastModified  string          `xml:"getlastmodified"`
	GetContentLength string          `xml:"getcontentlength"`
	ResourceType     XMLResourceType `xml:"resourcetype"`
	Checksums        *XMLChecksums   `xml:"checksums"`
	GetContentHash   string          `xml:"getcontenthash"`
	GetETag          string          `xml:"getetag"`
}

type XMLResourceType struct {
	Collection *struct{} `xml:"collection"`
}

type XMLChecksums struct {
	Checksum []string `xml:"checksum"`
}

func NewNextcloudProvider(rawURL, username, password string) (*NextcloudProvider, error) {
	baseURL := strings.TrimSuffix(rawURL, "/")
	if !strings.Contains(baseURL, "/remote.php/dav") {
		baseURL = baseURL + "/remote.php/dav"
	}

	tr := &http.Transport{
		ForceAttemptHTTP2:     false,
		TLSNextProto:          make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}

	return &NextcloudProvider{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Transport: tr,
			Timeout:   0,
		},
		Threads: 4,
	}, nil
}

func (p *NextcloudProvider) Close() error {
	p.HTTPClient.CloseIdleConnections()
	return nil
}

func (p *NextcloudProvider) buildResourceURL(resourceType string, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := &url.URL{Path: cleanPath}
	escapedUser := url.PathEscape(p.Username)

	switch resourceType {
	case "calendars":
		return fmt.Sprintf("%s/calendars/%s/%s", p.BaseURL, escapedUser, escapedPath.String())
	case "contacts":
		return fmt.Sprintf("%s/addressbooks/users/%s/%s", p.BaseURL, escapedUser, escapedPath.String())
	default: // "files"
		return fmt.Sprintf("%s/files/%s/%s", p.BaseURL, escapedUser, escapedPath.String())
	}
}

func (p *NextcloudProvider) buildUploadsURL(endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := &url.URL{Path: cleanPath}
	escapedUser := url.PathEscape(p.Username)
	return fmt.Sprintf("%s/uploads/%s/%s", p.BaseURL, escapedUser, escapedPath.String())
}

func (p *NextcloudProvider) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.Username, p.Password)
	req.Header.Set("User-Agent", "Nextcloud-Migration-Worker/1.0")
	return req, nil
}

func (p *NextcloudProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Query root folders via PROPFIND to test credentials
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
		return false, fmt.Errorf("nextcloud connect: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("connection failed with status code: %d", resp.StatusCode)
	}

	return true, nil
}

func (p *NextcloudProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, resourcePath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<oc:checksums/>
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
		return CloudResource{}, fmt.Errorf("nextcloud inspect: %w", ErrAuth)
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

			// Parse checksums
			if prop.Checksums != nil {
				for _, checksum := range prop.Checksums.Checksum {
					res.Hash = checksum
					break
				}
			}
			if res.Hash == "" && prop.GetContentHash != "" {
				res.Hash = prop.GetContentHash
			}
		}
	}

	return res, nil
}

func (p *NextcloudProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	u := p.buildResourceURL(resourceType, dirPath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<oc:checksums/>
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
		return nil, fmt.Errorf("nextcloud listing: %w", ErrAuth)
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
	var basePath string
	if parseErr == nil {
		basePath = strings.TrimSuffix(uParsed.Path, "/")
	} else {
		basePath = "/remote.php/dav"
	}

	var prefixPath string
	switch resourceType {
	case "calendars":
		prefixPath = fmt.Sprintf("%s/calendars/%s", basePath, p.Username)
	case "contacts":
		prefixPath = fmt.Sprintf("%s/addressbooks/users/%s", basePath, p.Username)
	default:
		prefixPath = fmt.Sprintf("%s/files/%s", basePath, p.Username)
	}

	for _, r := range multistatus.Responses {
		decodedHref, err := url.PathUnescape(r.Href)
		if err != nil {
			decodedHref = r.Href
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

		// Parse properties
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

				// Parse checksums
				if prop.Checksums != nil {
					for _, checksum := range prop.Checksums.Checksum {
						res.Hash = checksum
						break
					}
				}
				if res.Hash == "" && prop.GetContentHash != "" {
					res.Hash = prop.GetContentHash
				}
			}
		}
		resources = append(resources, res)
	}

	return resources, nil
}

func (p *NextcloudProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
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
		return nil, fmt.Errorf("nextcloud download: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	return resp.Body, nil
}

func (p *NextcloudProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	u := p.buildResourceURL(resourceType, filePath)

	err := p.CreateParentDirectories(ctx, resourceType, filePath)
	if err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	// CalDAV and CardDAV entries are small (typically a few KB). Use a tighter
	// per-request timeout so a hanging server fails fast and can be retried via
	// the normal backoff path rather than blocking a thread for 10 minutes.
	// For regular files scale the timeout by size: allow at least 5 min plus
	// 1 extra minute per 50 MB, capped at 30 minutes. This prevents a slow
	// Nextcloud target from holding threads indefinitely while still giving
	// large files (e.g. several GB) enough headroom.
	uploadCtx := ctx
	var uploadCancel context.CancelFunc
	if resourceType == "calendars" || resourceType == "contacts" {
		uploadCtx, uploadCancel = context.WithTimeout(ctx, 2*time.Minute)
	} else {
		baseTimeout := 5 * time.Minute
		if size > 0 {
			extraMinutes := time.Duration(size/(50*1024*1024)) * time.Minute
			baseTimeout += extraMinutes
		}
		if baseTimeout > 12*time.Hour {
			baseTimeout = 12 * time.Hour
		}
		uploadCtx, uploadCancel = context.WithTimeout(ctx, baseTimeout)
	}
	defer uploadCancel()

	req, err := p.newRequest("PUT", u, stream)
	if err != nil {
		return err
	}
	req = req.WithContext(uploadCtx)
	if size > 0 {
		req.ContentLength = size
	}
	contentType := "application/octet-stream"
	switch resourceType {
	case "calendars":
		contentType = "text/calendar; charset=utf-8"
	case "contacts":
		contentType = "text/vcard; charset=utf-8"
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("nextcloud upload: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		respBody := string(bodyBytes)
		if strings.Contains(respBody, "calobjects_by_uid_index") || strings.Contains(respBody, "unique constraint") {
			return ErrDuplicateUID
		}
		return fmt.Errorf("upload failed with status: %d, response: %s", resp.StatusCode, respBody)
	}

	return nil
}

func (p *NextcloudProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, fileSize int64, progressChan chan<- int64) error {
	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	transferID := fmt.Sprintf("upload-%x", time.Now().UnixNano())
	uploadsFolderURL := p.buildUploadsURL("/" + transferID)
	destURL := p.buildResourceURL(resourceType, filePath)

	req, err := p.newRequest("MKCOL", uploadsFolderURL, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header.Set("Destination", destURL)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("nextcloud mkdir (chunked upload): %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("failed to create upload directory, status: %d", resp.StatusCode)
	}

	// Dynamically calculate chunk size so total memory limit is 256MB across all threads
	threads := p.Threads
	if threads < 1 {
		threads = 4
	}
	megabytes := 256 / threads
	if megabytes < 5 {
		megabytes = 5 // Safety bound
	}
	chunkSize := int64(megabytes * 1024 * 1024)
	buffer := make([]byte, chunkSize)
	var chunkIndex int
	var totalUploaded int64

	for {
		bytesRead, readErr := io.ReadFull(stream, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}

		if bytesRead == 0 {
			break
		}

		chunkData := buffer[:bytesRead]
		// Naming of chunks is limited to be between 1 and 10000. Padded to 5 digits.
		chunkURL := fmt.Sprintf("%s/%05d", uploadsFolderURL, chunkIndex+1)

		err := p.uploadChunkWithRetry(ctx, chunkURL, chunkData, destURL, fileSize)
		if err != nil {
			return fmt.Errorf("failed to upload chunk %d: %w", chunkIndex+1, err)
		}

		totalUploaded += int64(bytesRead)
		if progressChan != nil {
			progressChan <- int64(bytesRead)
		}

		chunkIndex++
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	commitURL := fmt.Sprintf("%s/.file", uploadsFolderURL)
	req, err = p.newRequest("MOVE", commitURL, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)

	req.Header.Set("Destination", destURL)
	req.Header.Set("Overwrite", "T")
	req.Header.Set("OC-Total-Length", strconv.FormatInt(fileSize, 10))

	resp, err = p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
		resp.StatusCode == http.StatusGatewayTimeout ||
		resp.StatusCode == http.StatusBadGateway {

		// Poll to verify if the file eventually appears with the correct size.
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		timeout := time.After(2 * time.Minute)

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeout:
				return fmt.Errorf("failed to commit chunked upload, status: %d (verification timed out)", resp.StatusCode)
			case <-ticker.C:
				exists, size, errExists := p.FileExists(ctx, resourceType, filePath)
				if errExists == nil && exists && size == fileSize {
					return nil // Successfully committed and verified!
				}
			}
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("nextcloud chunked upload commit: %w", ErrAuth)
	}
	return fmt.Errorf("failed to commit chunked upload, status: %d", resp.StatusCode)
}

func (p *NextcloudProvider) uploadChunkWithRetry(ctx context.Context, chunkURL string, data []byte, destURL string, totalSize int64) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := p.newRequest("PUT", chunkURL, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(data))
		req.Header.Set("Destination", destURL)
		req.Header.Set("OC-Total-Length", strconv.FormatInt(totalSize, 10))

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("nextcloud upload chunk: %w", ErrAuth)
		}
		lastErr = fmt.Errorf("status code %d", resp.StatusCode)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return lastErr
}

func (p *NextcloudProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	parts := strings.Split(strings.Trim(dir, "/"), "/")

	// CalDAV and CardDAV collections are always flat — sub-collections do not exist per spec.
	// Only create the top-level collection (the calendar or addressbook itself).
	if (resourceType == "calendars" || resourceType == "contacts") && len(parts) > 1 {
		parts = parts[:1]
	}

	currentPath := ""
	for i, part := range parts {
		currentPath = currentPath + "/" + part
		u := p.buildResourceURL(resourceType, currentPath)

		var req *http.Request
		var err error

		if resourceType == "calendars" && i == 0 {
			// Create calendar folder using MKCALENDAR
			req, err = p.newRequest("MKCALENDAR", u, nil)
		} else if resourceType == "contacts" && i == 0 {
			// Create addressbook folder using CardDAV MKCOL XML body
			req, err = p.newRequest("MKCOL", u, bytes.NewBuffer(cardDAVMkcolBody()))
			if err == nil {
				req.Header.Set("Content-Type", "text/xml; charset=utf-8")
			}
		} else {
			// Normal folder creation
			req, err = p.newRequest("MKCOL", u, nil)
		}

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
			return fmt.Errorf("nextcloud mkdir: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("failed to create directory %s, status: %d", currentPath, resp.StatusCode)
		}
	}
	return nil
}

// cardDAVMkcolBody returns the XML body for creating a CardDAV addressbook collection.
// Centralised here so the XML stays in one place and won't drift between callers.
func cardDAVMkcolBody() []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
			<D:set>
				<D:prop>
					<D:resourcetype>
						<D:collection/>
						<C:addressbook/>
					</D:resourcetype>
				</D:prop>
			</D:set>
		</D:mkcol>`)
}

// mkcolRequest builds the correct MKCOL/MKCALENDAR request for dirPath based on resourceType.
// For calendars the first (and only) segment uses MKCALENDAR; for contacts it uses a
// CardDAV-typed MKCOL body; everything else uses a plain MKCOL.
func (p *NextcloudProvider) mkcolRequest(ctx context.Context, resourceType, dirPath string) (*http.Request, error) {
	u := p.buildResourceURL(resourceType, dirPath)
	var req *http.Request
	var err error
	switch resourceType {
	case "calendars":
		req, err = p.newRequest("MKCALENDAR", u, nil)
	case "contacts":
		req, err = p.newRequest("MKCOL", u, bytes.NewBuffer(cardDAVMkcolBody()))
		if err == nil {
			req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		}
	default:
		req, err = p.newRequest("MKCOL", u, nil)
	}
	if err != nil {
		return nil, err
	}
	return req.WithContext(ctx), nil
}

// CreateDirectory creates the given directory path and all intermediate parent directories.
// Uses the correct method per resource type (MKCALENDAR, CardDAV MKCOL, or plain MKCOL).
// 405 Method Not Allowed (already exists) is treated as success.
func (p *NextcloudProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// Ensure all ancestor directories exist first.
	if err := p.CreateParentDirectories(ctx, resourceType, dirPath); err != nil {
		return err
	}

	// Create the target directory itself with the correct method.
	req, err := p.mkcolRequest(ctx, resourceType, dirPath)
	if err != nil {
		return err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("nextcloud mkdir: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("failed to create directory %s, status: %d", dirPath, resp.StatusCode)
	}
	return nil
}

func (p *NextcloudProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.buildResourceURL(resourceType, filePath)
	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<oc:checksums/>
				<d:getcontenthash/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("nextcloud get-hash: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("PROPFIND for hash failed: status %d", resp.StatusCode)
	}

	var multistatus XMLMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return "", err
	}

	if len(multistatus.Responses) > 0 {
		r := multistatus.Responses[0]
		for _, pstat := range r.Propstat {
			if strings.Contains(pstat.Status, "200 OK") {
				if pstat.Prop.Checksums != nil {
					for _, checksum := range pstat.Prop.Checksums.Checksum {
						return checksum, nil
					}
				}
				if pstat.Prop.GetContentHash != "" {
					return pstat.Prop.GetContentHash, nil
				}
			}
		}
	}

	// Try extracting checksum from HEAD response headers if PROPFIND didn't yield it
	headReq, err := p.newRequest("HEAD", u, nil)
	if err == nil {
		headReq = headReq.WithContext(ctx)
		if headResp, err := p.HTTPClient.Do(headReq); err == nil {
			headResp.Body.Close()
			if chk := headResp.Header.Get("OC-Checksum"); chk != "" {
				return chk, nil
			}
		}
	}

	return "", fmt.Errorf("checksum not available")
}

func (p *NextcloudProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
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
		return false, 0, fmt.Errorf("nextcloud file-exists: %w", ErrAuth)
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

func (p *NextcloudProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
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
		return fmt.Errorf("nextcloud delete: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete failed with status: %d", resp.StatusCode)
	}
	return nil
}

func (p *NextcloudProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
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
		return fmt.Errorf("nextcloud move: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("move failed with status: %d", resp.StatusCode)
	}
	return nil
}

func (p *NextcloudProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
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

var hexRegexp = regexp.MustCompile("^[0-9a-fA-F]+$")

func ParseHashString(hashStr string) (string, string) {
	hashStr = strings.Trim(hashStr, "\"")
	parts := strings.SplitN(hashStr, ":", 2)
	if len(parts) == 2 {
		algo := strings.ToUpper(parts[0])
		if algo == "SHA-256" || algo == "SHA256" {
			algo = "SHA256"
		}
		if algo == "SHA-1" || algo == "SHA1" {
			algo = "SHA1"
		}
		if algo == "MD-5" || algo == "MD5" {
			algo = "MD5"
		}
		return algo, strings.ToLower(parts[1])
	}

	if hexRegexp.MatchString(hashStr) {
		if len(hashStr) == 32 {
			return "MD5", strings.ToLower(hashStr)
		}
		if len(hashStr) == 40 {
			return "SHA1", strings.ToLower(hashStr)
		}
		if len(hashStr) == 64 {
			return "SHA256", strings.ToLower(hashStr)
		}
	}
	return "UNKNOWN", hashStr
}
