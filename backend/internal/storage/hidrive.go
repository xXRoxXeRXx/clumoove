package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type HiDriveProvider struct {
	AccessToken string
	BaseURL     string
	HTTPClient  *http.Client
}

const defaultHiDriveAPIBase = "https://api.hidrive.strato.com/2.1"

func NewHiDriveProvider(token string) (*HiDriveProvider, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("hidrive provider requires non-empty access token: %w", ErrAuth)
	}
	tr := &http.Transport{
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
	}
	return &HiDriveProvider{
		AccessToken: token,
		BaseURL:     defaultHiDriveAPIBase,
		HTTPClient: &http.Client{
			Transport: newLoggingTransport(tr),
			Timeout:   0,
		},
	}, nil
}

func (p *HiDriveProvider) Close() error {
	if p.HTTPClient != nil {
		p.HTTPClient.CloseIdleConnections()
	}
	return nil
}

func (p *HiDriveProvider) validateResourceType(resourceType string) error {
	if resourceType != "files" {
		return fmt.Errorf("hidrive provider only supports files resource type, got %q", resourceType)
	}
	return nil
}

func (p *HiDriveProvider) cleanPath(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || filePath == "/" {
		return "/"
	}
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	return path.Clean(filePath)
}

// HiDrive serializes path and name fields URL-escaped in JSON responses. Paths
// must be decoded before being handed back to the indexer; otherwise the next
// request escapes '%' again (%2B -> %252B) and the API reports a false 404.
func decodeHiDrivePath(value string) (string, error) {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return "", fmt.Errorf("decode hidrive path: %w", err)
	}
	return decoded, nil
}

func (p *HiDriveProvider) apiURL(endpoint string) string {
	base := p.BaseURL
	if base == "" {
		base = defaultHiDriveAPIBase
	}
	return strings.TrimSuffix(base, "/") + endpoint
}

func (p *HiDriveProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/user/me"), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("fields", "account,alias,home")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, fmt.Errorf("hidrive connect: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hidrive connect failed, status: %d", resp.StatusCode)
	}
	return true, nil
}

type hidriveDirResponse struct {
	Path    string             `json:"path"`
	Name    string             `json:"name"`
	Members []hidriveDirMember `json:"members"`
}

type hidriveDirMember struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
	ID       string `json:"id,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	// chash is HiDrive's native content hash.  It is not a SHA-1 digest even
	// though it is 40 characters long, so preserve its algorithm identity.
	ContentHash string `json:"chash,omitempty"`
}

func (p *HiDriveProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := p.validateResourceType(resourceType); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	hdPath := p.cleanPath(dirPath)

	var allMembers []hidriveDirMember
	offset := 0
	const pageSize = 5000

	for {
		req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/dir"), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
		q := req.URL.Query()
		q.Set("path", hdPath)
		q.Set("members", "file,dir")
		// HiDrive v2.1 exposes its native content checksum as chash.  sha1 is
		// not a valid field and makes the complete listing request fail with 400.
		q.Set("fields", "path,name,members.name,members.type,members.size,members.mtime,members.readable,members.writable,members.id,members.mime_type,members.chash")
		q.Set("limit", fmt.Sprintf("%d,%d", offset, pageSize))
		q.Set("sort", "name")
		req.URL.RawQuery = q.Encode()

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return nil, fmt.Errorf("hidrive listing: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("hidrive listing failed, status: %d", resp.StatusCode)
		}

		var dirResp hidriveDirResponse
		if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		allMembers = append(allMembers, dirResp.Members...)

		if len(dirResp.Members) < pageSize {
			break
		}
		offset += pageSize
	}

	var resources []CloudResource
	for _, m := range allMembers {
		name, err := decodeHiDrivePath(m.Name)
		if err != nil {
			return nil, err
		}
		res := CloudResource{
			Path:  strings.TrimSuffix(hdPath, "/") + "/" + name,
			Name:  name,
			IsDir: m.Type == "dir",
			Size:  m.Size,
			Hash:  hidriveHash(m.ContentHash),
		}
		if m.Mtime > 0 {
			res.LastModified = time.Unix(m.Mtime, 0)
		}
		resources = append(resources, res)
	}

	return resources, nil
}

type hidriveMetaResponse struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Size        int64  `json:"size,omitempty"`
	Mtime       int64  `json:"mtime,omitempty"`
	Readable    bool   `json:"readable"`
	Writable    bool   `json:"writable"`
	ID          string `json:"id,omitempty"`
	ContentHash string `json:"chash,omitempty"`
}

func hidriveHash(contentHash string) string {
	if contentHash == "" {
		return ""
	}
	return "HIDRIVE:" + contentHash
}

func (p *HiDriveProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	if err := p.validateResourceType(resourceType); err != nil {
		return CloudResource{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	hdPath := p.cleanPath(resourcePath)
	if hdPath == "/" {
		return CloudResource{Path: "/", Name: "", IsDir: true, Size: 0}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/meta"), nil)
	if err != nil {
		return CloudResource{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	q.Set("fields", "path,name,type,size,mtime,readable,writable,id,chash")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return CloudResource{}, fmt.Errorf("hidrive inspect: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		return CloudResource{}, fmt.Errorf("hidrive inspect: %w", ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return CloudResource{}, fmt.Errorf("hidrive inspect failed, status: %d", resp.StatusCode)
	}

	var meta hidriveMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return CloudResource{}, err
	}
	metaPath, err := decodeHiDrivePath(meta.Path)
	if err != nil {
		return CloudResource{}, err
	}
	metaName, err := decodeHiDrivePath(meta.Name)
	if err != nil {
		return CloudResource{}, err
	}

	res := CloudResource{
		Path:  metaPath,
		Name:  metaName,
		IsDir: meta.Type == "dir",
		Size:  meta.Size,
		Hash:  hidriveHash(meta.ContentHash),
	}
	if meta.Mtime > 0 {
		res.LastModified = time.Unix(meta.Mtime, 0)
	}
	return res, nil
}

func (p *HiDriveProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := p.validateResourceType(resourceType); err != nil {
		return nil, err
	}

	hdPath := p.cleanPath(filePath)

	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/file"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, fmt.Errorf("hidrive download: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("hidrive download failed, status: %d", resp.StatusCode)
	}

	return resp.Body, nil
}

func (p *HiDriveProvider) uploadFile(ctx context.Context, filePath string, stream io.Reader, size int64) error {
	filePath = p.cleanPath(filePath)
	dir := path.Dir(filePath)
	name := path.Base(filePath)

	baseTimeout := 5 * time.Minute
	if size > 0 {
		baseTimeout += time.Duration(size/(50*1024*1024)) * time.Minute
	}
	if baseTimeout > 12*time.Hour {
		baseTimeout = 12 * time.Hour
	}
	uploadCtx, cancel := context.WithTimeout(ctx, baseTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(uploadCtx, "POST", p.apiURL("/file"), stream)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	if size > 0 {
		req.ContentLength = size
	}
	q := req.URL.Query()
	q.Set("dir", p.cleanPath(dir))
	q.Set("name", name)
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("hidrive upload: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("hidrive upload failed, status: %d", resp.StatusCode)
	}

	return nil
}

func (p *HiDriveProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return err
	}

	// POST /file is create-only: HiDrive returns 409 if the name appeared
	// after the processor's conflict check. Do not pre-delete it here; doing
	// so would turn SKIP and RENAME races into unintended overwrites.
	return p.uploadFile(ctx, filePath, stream, size)
}

func (p *HiDriveProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	const chunkSize int64 = 50 * 1024 * 1024 // 50 MB per chunk

	if size <= chunkSize {
		return p.StreamUpload(ctx, resourceType, filePath, stream, size)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return err
	}

	filePath = p.cleanPath(filePath)
	dir := path.Dir(filePath)
	name := path.Base(filePath)

	buf := make([]byte, chunkSize)
	var uploaded int64
	chunkIndex := 0
	partialUploadCreated := false
	cleanupPartialUpload := func() {
		if !partialUploadCreated {
			return
		}
		// Preserve cleanup ability when the transfer context was cancelled. A
		// completed first chunk proves this invocation created the target, so
		// removing it cannot delete a pre-existing conflict-race target.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		_ = p.DeleteFile(cleanupCtx, resourceType, filePath)
	}
	fail := func(err error) error {
		cleanupPartialUpload()
		return err
	}

	for uploaded < size {
		n, readErr := io.ReadFull(stream, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return fail(fmt.Errorf("hidrive chunked read: %w", readErr))
		}
		chunkData := buf[:n]
		chunkSizeActual := int64(n)

		timeout := 10 * time.Minute
		uploadCtx, cancel := context.WithTimeout(ctx, timeout)

		body := bytes.NewReader(chunkData)
		method := http.MethodPatch
		q := url.Values{}
		if uploaded == 0 {
			// POST creates the object; following chunks update it at an explicit
			// byte offset. Content-Range is not part of the HiDrive API.
			method = http.MethodPost
			q.Set("dir", p.cleanPath(dir))
			q.Set("name", name)
		} else {
			q.Set("path", filePath)
			q.Set("offset", fmt.Sprintf("%d", uploaded))
		}
		req, err := http.NewRequestWithContext(uploadCtx, method, p.apiURL("/file"), body)
		if err != nil {
			cancel()
			return fail(err)
		}
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = chunkSizeActual
		req.URL.RawQuery = q.Encode()

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			cancel()
			return fail(fmt.Errorf("hidrive chunked upload chunk %d: %w", chunkIndex, err))
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			cancel()
			return fail(fmt.Errorf("hidrive chunked upload: %w", ErrAuth))
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			cancel()
			return fail(fmt.Errorf("hidrive chunked upload chunk %d conflict, status: %d", chunkIndex, resp.StatusCode))
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
			resp.Body.Close()
			cancel()
			return fail(fmt.Errorf("hidrive chunked upload chunk %d failed, status: %d", chunkIndex, resp.StatusCode))
		}
		resp.Body.Close()
		cancel()
		if uploaded == 0 {
			partialUploadCreated = true
		}

		uploaded += chunkSizeActual
		chunkIndex++

		if progressChan != nil {
			progressChan <- chunkSizeActual
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			if uploaded != size {
				return fail(io.ErrUnexpectedEOF)
			}
			break
		}
	}

	return nil
}

func (p *HiDriveProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if err := p.validateResourceType(resourceType); err != nil {
		return false, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	hdPath := p.cleanPath(filePath)

	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/meta"), nil)
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	q.Set("fields", "type,size")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, 0, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, 0, fmt.Errorf("hidrive file exists: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return false, 0, fmt.Errorf("hidrive file exists failed, status: %d", resp.StatusCode)
	}

	var meta hidriveMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, 0, err
	}

	return true, meta.Size, nil
}

func (p *HiDriveProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	hdPath := p.cleanPath(filePath)

	meta, mErr := p.InspectResource(ctx, resourceType, hdPath)
	if mErr != nil {
		return mErr
	}

	endpoint := "/file"
	qParams := url.Values{}
	qParams.Set("path", hdPath)
	if meta.IsDir {
		endpoint = "/dir"
		qParams.Set("recursive", "true")
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", p.apiURL(endpoint), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	req.URL.RawQuery = qParams.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("hidrive delete: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hidrive delete failed, status: %d", resp.StatusCode)
	}

	return nil
}

func (p *HiDriveProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if err := p.validateResourceType(resourceType); err != nil {
		return "", err
	}

	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return "", err
	}
	if res.Hash != "" {
		return res.Hash, nil
	}
	return "", ErrChecksumNotAvailable
}

func (p *HiDriveProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	dir := path.Dir(filePath)
	if dir == "." || dir == "/" {
		return nil
	}
	return p.CreateDirectory(ctx, resourceType, dir)
}

func (p *HiDriveProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	parts := strings.Split(strings.Trim(dirPath, "/"), "/")
	accumulated := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		accumulated = accumulated + "/" + part

		exists, _, err := p.FileExists(ctx, resourceType, accumulated)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, "POST", p.apiURL("/dir"), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
		q := req.URL.Query()
		q.Set("path", accumulated)
		req.URL.RawQuery = q.Encode()

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("hidrive create dir: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusConflict {
			return fmt.Errorf("hidrive create dir failed for %s, status: %d", accumulated, resp.StatusCode)
		}
	}

	return nil
}

func (p *HiDriveProvider) VerificationMode() VerificationMode { return VerificationCryptographicHash }

func (p *HiDriveProvider) SupportsAtomicRename() bool {
	return true
}

func (p *HiDriveProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if err := p.validateResourceType(resourceType); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	srcPath := p.cleanPath(oldPath)
	dstPath := p.cleanPath(newPath)

	endpoint := "/file/move"
	meta, err := p.InspectResource(ctx, resourceType, srcPath)
	if err == nil && meta.IsDir {
		endpoint = "/dir/move"
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.apiURL(endpoint), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("src", srcPath)
	q.Set("dst", dstPath)
	// The processor's overwrite flow uploads to a temporary name and then
	// atomically moves it into place. Ask HiDrive to replace a raced existing
	// destination instead of failing the finalisation.
	q.Set("on_exist", "overwrite")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("hidrive rename: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("hidrive rename failed, status: %d", resp.StatusCode)
	}

	return nil
}
