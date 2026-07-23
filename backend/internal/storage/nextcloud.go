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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pathBuilder abstracts the per-provider WebDAV path scheme so the shared
// davProvider protocol logic can serve multiple providers without embedding
// (which would break Go's method dispatch). Nextcloud appends
// /files/<user>, /calendars/<user>, ... segments; MagentaCLOUD exposes the
// WebDAV root directly and needs none of that.
type pathBuilder interface {
	resourceURL(baseURL, username, resourceType, p string) string
	uploadsURL(baseURL, username, p string) string
	listingPrefix(basePath, username, resourceType string) string
}

// davProvider holds the shared WebDAV/Nextcloud protocol implementation.
// Concrete providers (NextcloudProvider, MagentacloudProvider) embed it and
// supply a pathBuilder plus transport configuration. Behaviour of
// NextcloudProvider is unchanged by this extraction.
type davProvider struct {
	BaseURL              string
	Username             string
	Password             string
	HTTPClient           *http.Client
	Threads              int
	UserAgent            string
	pb                   pathBuilder
	disableChunkedUpload bool
	// supportedResourceTypes lists the resource types the provider can handle.
	// Nextcloud supports files/calendars/contacts; files-only providers (e.g.
	// MagentaCLOUD) declare only "files".
	supportedResourceTypes map[string]bool
	createdDirs            sync.Map
}

// assertResourceType returns an error if the provider does not support the
// given resource type (e.g. calendars/contacts on a files-only provider such
// as MagentaCLOUD).
func (p *davProvider) assertResourceType(resourceType string) error {
	if p.supportedResourceTypes != nil && p.supportedResourceTypes[resourceType] {
		return nil
	}
	return fmt.Errorf("resource type %q not supported by provider", resourceType)
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

func newDAVTransport(host string) *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1000,
		MaxIdleConnsPerHost:   500,
		MaxConnsPerHost:       500,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ReadBufferSize:        256 * 1024,
		WriteBufferSize:       256 * 1024,
		// Pin egress to a re-validated IP on every connection so a DNS
		// rebind between validation and connect cannot reach internal/metadata
		// addresses. The original hostname stays in the request URL, preserving
		// SNI / certificate validation.
		DialContext: egressDialer(host),
	}
}

func NewNextcloudProvider(rawURL, username, password string) (*NextcloudProvider, error) {
	baseURL := strings.TrimSuffix(rawURL, "/")
	if idx := strings.Index(baseURL, "/remote.php/dav"); idx != -1 {
		baseURL = baseURL[:idx+len("/remote.php/dav")]
	} else if idx := strings.Index(baseURL, "/remote.php/webdav"); idx != -1 {
		baseURL = baseURL[:idx] + "/remote.php/dav"
	} else {
		baseURL = baseURL + "/remote.php/dav"
	}

	// Extract the host for the egress dialer (validates the resolved IP on
	// every connection to defeat DNS rebinding).
	host := rawURL
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
		host = parsed.Hostname()
	}

	return &NextcloudProvider{
		davProvider: &davProvider{
			BaseURL:  baseURL,
			Username: username,
			Password: password,
			HTTPClient: &http.Client{
				Transport: newLoggingTransport(newDAVTransport(host)),
				Timeout:   0,
			},
			Threads:   4,
			UserAgent: "Nextcloud-Migration-Worker/1.0",
			pb:        nextcloudPaths{},
			supportedResourceTypes: map[string]bool{"files": true, "calendars": true, "contacts": true},
		},
	}, nil
}

func (p *davProvider) Close() error {
	if p.HTTPClient != nil {
		p.HTTPClient.CloseIdleConnections()
	}
	return nil
}

func (p *davProvider) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.Username, p.Password)
	req.Header.Set("User-Agent", p.UserAgent)
	return req, nil
}

func (p *davProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Query root folders via PROPFIND to test credentials
	u := p.pb.resourceURL(p.BaseURL, p.Username, "files", "/")
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

func (p *davProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	if err := p.assertResourceType(resourceType); err != nil {
		return CloudResource{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, resourcePath)

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

			if prop.GetETag != "" {
				res.ETag = strings.Trim(prop.GetETag, `"`)
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

func (p *davProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := p.assertResourceType(resourceType); err != nil {
		return nil, err
	}
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, dirPath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<d:getetag/>
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

	prefixPath := p.pb.listingPrefix(basePath, p.Username, resourceType)
	prefixLower := strings.ToLower(prefixPath)

	for _, r := range multistatus.Responses {
		decodedHref, err := url.PathUnescape(r.Href)
		if err != nil {
			decodedHref = r.Href
		}

		hrefLower := strings.ToLower(decodedHref)
		if !strings.HasPrefix(hrefLower, prefixLower) {
			continue
		}

		relativeHref := decodedHref[len(prefixPath):]
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
				if resourceType == "calendars" || resourceType == "contacts" {
					res.IsDir = true
				}

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

				if prop.GetETag != "" {
					res.ETag = strings.Trim(prop.GetETag, `"`)
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

func (p *davProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := p.assertResourceType(resourceType); err != nil {
		return nil, err
	}
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)
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

func (p *davProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)

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

func (p *davProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, fileSize int64, progressChan chan<- int64) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
	// Some providers (e.g. MagentaCLOUD) do not support Nextcloud's /uploads
	// chunked-upload API. Fall back to a plain PUT so the transfer still works
	// over standard WebDAV; wrap the stream to keep progress reporting alive.
	if p.disableChunkedUpload {
		if progressChan != nil {
			stream = &ProgressReader{Reader: stream, ProgressChan: progressChan}
		}
		return p.StreamUpload(ctx, resourceType, filePath, stream, fileSize)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	transferID := fmt.Sprintf("upload-%x", time.Now().UnixNano())
	uploadsFolderURL := p.pb.uploadsURL(p.BaseURL, p.Username, "/"+transferID)
	destURL := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)

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

func (p *davProvider) uploadChunkWithRetry(ctx context.Context, chunkURL string, data []byte, destURL string, totalSize int64) error {
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

// globalNextcloudCreatedDirs caches directory existence across Nextcloud provider instances within the worker process.
// Key format: BaseURL + "|" + Username + "|" + resourceType + ":" + currentPath
var globalNextcloudCreatedDirs sync.Map

func (p *davProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
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
		localDirKey := resourceType + ":" + currentPath
		globalDirKey := p.BaseURL + "|" + p.Username + "|" + localDirKey

		if _, exists := p.createdDirs.Load(localDirKey); exists {
			continue
		}
		if _, exists := globalNextcloudCreatedDirs.Load(globalDirKey); exists {
			p.createdDirs.Store(localDirKey, true)
			continue
		}

		u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, currentPath)

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
		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusOK {
			p.createdDirs.Store(localDirKey, true)
			globalNextcloudCreatedDirs.Store(globalDirKey, true)
		} else {
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
func (p *davProvider) mkcolRequest(ctx context.Context, resourceType, dirPath string) (*http.Request, error) {
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, dirPath)
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
func (p *davProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
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
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusOK {
		localDirKey := resourceType + ":" + dirPath
		globalDirKey := p.BaseURL + "|" + p.Username + "|" + localDirKey
		p.createdDirs.Store(localDirKey, true)
		globalNextcloudCreatedDirs.Store(globalDirKey, true)
	} else {
		return fmt.Errorf("failed to create directory %s, status: %d", dirPath, resp.StatusCode)
	}
	return nil
}

func (p *davProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if err := p.assertResourceType(resourceType); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)

	// Try lightweight HEAD request first (Nextcloud returns OC-Checksum in HTTP header directly)
	var fallbackETag string
	headReq, err := p.newRequest("HEAD", u, nil)
	if err == nil {
		headReq = headReq.WithContext(ctx)
		if headResp, err := p.HTTPClient.Do(headReq); err == nil {
			headResp.Body.Close()
			if headResp.StatusCode == http.StatusOK {
				if chk := headResp.Header.Get("OC-Checksum"); chk != "" {
					return chk, nil
				}
				if etag := headResp.Header.Get("OC-ETag"); etag != "" {
					fallbackETag = "ETAG:" + strings.Trim(etag, `"`)
				} else if etag := headResp.Header.Get("ETag"); etag != "" {
					fallbackETag = "ETAG:" + strings.Trim(etag, `"`)
				}
				// 200 OK without OC-Checksum: fall through to PROPFIND for XML <oc:checksums/>
			} else if headResp.StatusCode == http.StatusNotFound {
				return "", fmt.Errorf("file not found: %s", filePath)
			} else if headResp.StatusCode == http.StatusUnauthorized {
				return "", fmt.Errorf("nextcloud get-hash: %w", ErrAuth)
			} else {
				return "", fmt.Errorf("HEAD for hash failed: status %d", headResp.StatusCode)
			}
		}
	}

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<oc:checksums/>
				<d:getcontenthash/>
				<d:getetag/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		if fallbackETag != "" {
			return fallbackETag, nil
		}
		return "", err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		if fallbackETag != "" {
			return fallbackETag, nil
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("file not found: %s", filePath)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("nextcloud get-hash: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if fallbackETag != "" {
			return fallbackETag, nil
		}
		return "", fmt.Errorf("PROPFIND for hash failed: status %d", resp.StatusCode)
	}

	var multistatus XMLMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		if fallbackETag != "" {
			return fallbackETag, nil
		}
		return "", err
	}

	if len(multistatus.Responses) > 0 {
		r := multistatus.Responses[0]
		for _, pstat := range r.Propstat {
			if strings.Contains(pstat.Status, "200 OK") {
				if pstat.Prop.Checksums != nil {
					for _, checksum := range pstat.Prop.Checksums.Checksum {
						if checksum != "" {
							return checksum, nil
						}
					}
				}
				if pstat.Prop.GetContentHash != "" {
					return pstat.Prop.GetContentHash, nil
				}
				if pstat.Prop.GetETag != "" {
					return "ETAG:" + strings.Trim(pstat.Prop.GetETag, `"`), nil
				}
			}
		}
	}

	if fallbackETag != "" {
		return fallbackETag, nil
	}

	return "", fmt.Errorf("checksum not available")
}

func (p *davProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if err := p.assertResourceType(resourceType); err != nil {
		return false, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)

	// Try lightweight HEAD request first (returns 200 OK with Content-Length or 404 Not Found directly)
	headReq, err := p.newRequest("HEAD", u, nil)
	if err == nil {
		headReq = headReq.WithContext(ctx)
		if resp, err := p.HTTPClient.Do(headReq); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true, resp.ContentLength, nil
			}
			if resp.StatusCode == http.StatusNotFound {
				return false, 0, nil
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return false, 0, fmt.Errorf("nextcloud file-exists: %w", ErrAuth)
			}
			if resp.StatusCode >= 400 {
				return false, 0, fmt.Errorf("HEAD check failed with status: %d", resp.StatusCode)
			}
		}
	}

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

func (p *davProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)
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

func (p *davProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, oldPath)
	req, err := p.newRequest("MOVE", u, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	destURL := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, newPath)
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

// SupportsAtomicRename is true: WebDAV/Nextcloud MOVE is supported.
func (p *davProvider) SupportsAtomicRename() bool {
	return true
}

func (p *davProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if err := p.assertResourceType(resourceType); err != nil {
		return err
	}
	if resourceType != "files" || meta.ModifiedTime.IsZero() {
		return nil
	}

	u := p.pb.resourceURL(p.BaseURL, p.Username, resourceType, filePath)
	unixSec := meta.ModifiedTime.Unix()
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" ?>
<d:propertyupdate xmlns:d="DAV:">
  <d:set>
    <d:prop>
      <d:lastmodified>%d</d:lastmodified>
    </d:prop>
  </d:set>
</d:propertyupdate>`, unixSec)

	req, err := p.newRequest("PROPPATCH", u, strings.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("X-OC-MTime", strconv.FormatInt(unixSec, 10))
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	return nil
}

// nextcloudPaths implements pathBuilder for a standard Nextcloud instance,
// appending /files/<user>, /calendars/<user>, /addressbooks/users/<user>
// resource-type segments.
type nextcloudPaths struct{}

func (nextcloudPaths) resourceURL(baseURL, username, resourceType, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := (&url.URL{Path: cleanPath}).String()
	escapedUser := url.PathEscape(username)

	switch resourceType {
	case "calendars":
		return fmt.Sprintf("%s/calendars/%s/%s", baseURL, escapedUser, escapedPath)
	case "contacts":
		return fmt.Sprintf("%s/addressbooks/users/%s/%s", baseURL, escapedUser, escapedPath)
	default: // "files"
		return fmt.Sprintf("%s/files/%s/%s", baseURL, escapedUser, escapedPath)
	}
}

func (nextcloudPaths) uploadsURL(baseURL, username, endpointPath string) string {
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := (&url.URL{Path: cleanPath}).String()
	escapedUser := url.PathEscape(username)
	return fmt.Sprintf("%s/uploads/%s/%s", baseURL, escapedUser, escapedPath)
}

func (nextcloudPaths) listingPrefix(basePath, username, resourceType string) string {
	switch resourceType {
	case "calendars":
		return fmt.Sprintf("%s/calendars/%s", basePath, username)
	case "contacts":
		return fmt.Sprintf("%s/addressbooks/users/%s", basePath, username)
	default:
		return fmt.Sprintf("%s/files/%s", basePath, username)
	}
}

// NextcloudProvider is a thin wrapper around the shared davProvider protocol
// implementation, configured with the Nextcloud path scheme.
type NextcloudProvider struct {
	*davProvider
}

var _ StorageProvider = (*NextcloudProvider)(nil)

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
