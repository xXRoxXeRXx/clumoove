package webdav

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
	"time"
)

type CloudFile struct {
	Path         string    `json:"path"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	IsDir        bool      `json:"is_dir"`
	Hash         string    `json:"hash"`
	LastModified time.Time `json:"last_modified"`
}

type Client struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
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
}

type XMLResourceType struct {
	Collection *struct{} `xml:"collection"`
}

type XMLChecksums struct {
	Checksum []string `xml:"checksum"`
}

func NewClient(rawURL, username, password string) (*Client, error) {
	// Normalize URL: ensure it ends with /remote.php/dav
	baseURL := strings.TrimSuffix(rawURL, "/")
	if !strings.Contains(baseURL, "/remote.php/dav") {
		baseURL = baseURL + "/remote.php/dav"
	}

	return &Client{
		BaseURL:  baseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Timeout: 0, // No timeout for large transfers, stream timeouts will be handled at read/write level
		},
	}, nil
}

func (c *Client) buildURL(endpointPath string, isUploads bool) string {
	var ns string
	if isUploads {
		ns = "uploads"
	} else {
		ns = "files"
	}

	// Nextcloud WebDAV path format: /remote.php/dav/files/username/path
	cleanPath := strings.TrimPrefix(endpointPath, "/")
	escapedPath := &url.URL{Path: cleanPath}
	
	return fmt.Sprintf("%s/%s/%s/%s", c.BaseURL, ns, c.Username, escapedPath.String())
}

func (c *Client) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Set("User-Agent", "Nextcloud-Migration-Worker/1.0")
	return req, nil
}

// Connect checks connection credentials by querying the root folder
func (c *Client) Connect(ctx context.Context) (bool, error) {
	u := c.buildURL("/", false)
	
	// Send PROPFIND request with Depth: 0
	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:">
			<d:prop>
				<d:resourcetype/>
			</d:prop>
		</d:propfind>`)
	
	req, err := c.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
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

// GetDirectoryListing lists files/folders in the specified path (asynchronously rendered in frontend)
func (c *Client) GetDirectoryListing(ctx context.Context, dirPath string) ([]CloudFile, error) {
	u := c.buildURL(dirPath, false)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:resourcetype/>
				<oc:checksums/>
			</d:prop>
		</d:propfind>`)

	req, err := c.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
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

	var files []CloudFile
	
	// Helper to extract clean path from url-decoded href
	prefixPath := fmt.Sprintf("/remote.php/dav/files/%s", c.Username)

	for _, r := range multistatus.Responses {
		decodedHref, err := url.PathUnescape(r.Href)
		if err != nil {
			decodedHref = r.Href
		}

		// Ensure we only look at paths inside the user's directory
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

		var file CloudFile
		file.Path = relativeHref
		file.Name = path.Base(relativeHref)

		// Parse properties
		for _, pstat := range r.Propstat {
			// Find the successful propstat
			if strings.Contains(pstat.Status, "200 OK") {
				prop := pstat.Prop
				file.IsDir = prop.ResourceType.Collection != nil
				
				if !file.IsDir {
					if size, err := strconv.ParseInt(prop.GetContentLength, 10, 64); err == nil {
						file.Size = size
					}
				}

				if prop.GetLastModified != "" {
					if t, err := time.Parse(time.RFC1123, prop.GetLastModified); err == nil {
						file.LastModified = t
					}
				}

				// Parse checksums
				if prop.Checksums != nil {
					for _, checksum := range prop.Checksums.Checksum {
						// Format: SHA1:xxx or MD5:xxx
						file.Hash = checksum
						break
					}
				}
				if file.Hash == "" && prop.GetContentHash != "" {
					file.Hash = prop.GetContentHash
				}
			}
		}
		files = append(files, file)
	}

	return files, nil
}

// StreamDownload returns a Reader containing the file stream
func (c *Client) StreamDownload(ctx context.Context, filePath string) (io.ReadCloser, http.Header, error) {
	u := c.buildURL(filePath, false)
	req, err := c.newRequest("GET", u, nil)
	if err != nil {
		return nil, nil, err
	}
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	return resp.Body, resp.Header, nil
}

// StreamUpload uploads a simple (non-chunked) file stream directly
func (c *Client) StreamUpload(ctx context.Context, filePath string, stream io.Reader, size int64) error {
	u := c.buildURL(filePath, false)
	
	// Make sure parent directories exist
	err := c.CreateParentDirectories(ctx, filePath)
	if err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	req, err := c.newRequest("PUT", u, stream)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	if size > 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	return nil
}

// StreamUploadChunked performs a Chunked Upload v2 for large files (> 50 MB)
func (c *Client) StreamUploadChunked(ctx context.Context, filePath string, stream io.Reader, fileSize int64, progressChan chan<- int64) error {
	// Create parent directories for final file
	if err := c.CreateParentDirectories(ctx, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	// Generate a unique transfer ID based on file path
	transferID := fmt.Sprintf("upload-%x", time.Now().UnixNano())
	
	// Create upload folder: MKCOL /remote.php/dav/uploads/username/transferID
	uploadsFolderURL := c.buildURL("/"+transferID, true)
	req, err := c.newRequest("MKCOL", uploadsFolderURL, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("failed to create upload directory, status: %d", resp.StatusCode)
	}

	// Buffer size for chunks: 10MB
	chunkSize := int64(10 * 1024 * 1024)
	buffer := make([]byte, chunkSize)
	var chunkIndex int
	var totalUploaded int64

	for {
		// Read exact chunk size from stream
		bytesRead, err := io.ReadFull(stream, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}

		if bytesRead == 0 {
			break // EOF reached
		}

		chunkData := buffer[:bytesRead]
		chunkURL := fmt.Sprintf("%s/%08d", uploadsFolderURL, chunkIndex)
		
		// Upload the chunk
		err = c.uploadChunkWithRetry(ctx, chunkURL, chunkData)
		if err != nil {
			return fmt.Errorf("failed to upload chunk %d: %w", chunkIndex, err)
		}

		totalUploaded += int64(bytesRead)
		if progressChan != nil {
			progressChan <- int64(bytesRead)
		}

		chunkIndex++
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break // Stream finished
		}
	}

	// Commit upload: MOVE /remote.php/dav/uploads/username/transferID/.file
	// Header: Destination: /remote.php/dav/files/username/filePath
	commitURL := fmt.Sprintf("%s/.file", uploadsFolderURL)
	req, err = c.newRequest("MOVE", commitURL, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	
	destURL := c.buildURL(filePath, false)
	req.Header.Set("Destination", destURL)

	resp, err = c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("failed to commit chunked upload, status: %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) uploadChunkWithRetry(ctx context.Context, chunkURL string, data []byte) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := c.newRequest("PUT", chunkURL, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(data))

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("status code %d", resp.StatusCode)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return lastErr
}

// CreateParentDirectories recursively creates folder structures if they do not exist
func (c *Client) CreateParentDirectories(ctx context.Context, filePath string) error {
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}

	// Split parts and create folders sequentially
	parts := strings.Split(strings.Trim(dir, "/"), "/")
	currentPath := ""
	for _, part := range parts {
		currentPath = currentPath + "/" + part
		u := c.buildURL(currentPath, false)
		
		// MKCOL request
		req, err := c.newRequest("MKCOL", u, nil)
		if err != nil {
			return err
		}
		req = req.WithContext(ctx)
		
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()

		// 201 Created or 405 Method Not Allowed (means folder already exists) are fine
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("failed to create directory %s, status: %d", currentPath, resp.StatusCode)
		}
	}
	return nil
}

// GetFileHash retrieves the checksum of a file directly from Nextcloud
func (c *Client) GetFileHash(ctx context.Context, filePath string) (string, error) {
	u := c.buildURL(filePath, false)
	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
			<d:prop>
				<oc:checksums/>
				<d:getcontenthash/>
			</d:prop>
		</d:propfind>`)

	req, err := c.newRequest("PROPFIND", u, bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

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
	headReq, err := c.newRequest("HEAD", u, nil)
	if err == nil {
		headReq = headReq.WithContext(ctx)
		if headResp, err := c.HTTPClient.Do(headReq); err == nil {
			headResp.Body.Close()
			if chk := headResp.Header.Get("OC-Checksum"); chk != "" {
				return chk, nil
			}
			if chk := headResp.Header.Get("ETag"); chk != "" {
				// Etag contains hash in double quotes sometimes, e.g., "da39a3ee5e6b4b0d3255bfef95601890afd80709"
				etag := strings.Trim(chk, "\"")
				// Nextcloud Etags are sometimes suffix-hash or simple MD5
				if len(etag) >= 32 {
					return etag, nil
				}
			}
		}
	}

	return "", fmt.Errorf("checksum not available")
}

// FileExists checks if a file exists on WebDAV and returns its size
func (c *Client) FileExists(ctx context.Context, filePath string) (bool, int64, error) {
	u := c.buildURL(filePath, false)
	req, err := c.newRequest("HEAD", u, nil)
	if err != nil {
		return false, 0, err
	}
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
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

// DeleteFile deletes a file on WebDAV (e.g. for overwrite conflict strategy)
func (c *Client) DeleteFile(ctx context.Context, filePath string) error {
	u := c.buildURL(filePath, false)
	req, err := c.newRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete failed with status: %d", resp.StatusCode)
	}
	return nil
}

// Helper function to extract hash clean format
// Returns e.g. "SHA1", "da39a3ee5e6b4b0d3255bfef95601890afd80709"
func ParseHashString(hashStr string) (string, string) {
	hashStr = strings.Trim(hashStr, "\"")
	parts := strings.SplitN(hashStr, ":", 2)
	if len(parts) == 2 {
		return strings.ToUpper(parts[0]), strings.ToLower(parts[1])
	}
	
	// If it matches MD5 (32 hex chars) or SHA1 (40 hex chars) without prefix
	matchHex, _ := regexp.MatchString("^[0-9a-fA-F]+$", hashStr)
	if matchHex {
		if len(hashStr) == 32 {
			return "MD5", strings.ToLower(hashStr)
		}
		if len(hashStr) == 40 {
			return "SHA1", strings.ToLower(hashStr)
		}
	}
	return "UNKNOWN", hashStr
}
