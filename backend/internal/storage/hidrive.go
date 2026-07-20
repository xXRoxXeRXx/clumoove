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
	HTTPClient  *http.Client
}

const hidriveAPIBase = "https://api.hidrive.strato.com/2.1"

func NewHiDriveProvider(token string) (*HiDriveProvider, error) {
	tr := &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	return &HiDriveProvider{
		AccessToken: token,
		HTTPClient: &http.Client{
			Transport: tr,
			Timeout:   0,
		},
	}, nil
}

func (p *HiDriveProvider) Close() error {
	p.HTTPClient.CloseIdleConnections()
	return nil
}

func (p *HiDriveProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", hidriveAPIBase+"/user/me", nil)
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

func (p *HiDriveProvider) cleanPath(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || filePath == "/" {
		return "/"
	}
	decoded, err := url.PathUnescape(filePath)
	if err == nil {
		filePath = decoded
	}
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	return path.Clean(filePath)
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
}

func (p *HiDriveProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	hdPath := p.cleanPath(dirPath)

	var allMembers []hidriveDirMember
	offset := 0
	const pageSize = 5000

	for {
		req, err := http.NewRequestWithContext(ctx, "GET", hidriveAPIBase+"/dir", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
		q := req.URL.Query()
		q.Set("path", hdPath)
		q.Set("members", "file,dir")
		q.Set("fields", "path,name,members.name,members.type,members.size,members.mtime,members.readable,members.writable,members.id,members.mime_type")
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
		res := CloudResource{
			Path:  strings.TrimSuffix(hdPath, "/") + "/" + m.Name,
			Name:  m.Name,
			IsDir: m.Type == "dir",
			Size:  m.Size,
		}
		if m.Mtime > 0 {
			res.LastModified = time.Unix(m.Mtime, 0)
		}
		resources = append(resources, res)
	}

	return resources, nil
}

type hidriveMetaResponse struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	Mtime    int64  `json:"mtime,omitempty"`
	Readable bool   `json:"readable"`
	Writable bool   `json:"writable"`
	ID       string `json:"id,omitempty"`
}

func (p *HiDriveProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	hdPath := p.cleanPath(resourcePath)
	if hdPath == "/" {
		return CloudResource{Path: "/", Name: "", IsDir: true, Size: 0}, nil
	}

	req, err := http.NewRequestWithContext(ctx, "GET", hidriveAPIBase+"/meta", nil)
	if err != nil {
		return CloudResource{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	q.Set("fields", "path,name,type,size,mtime,readable,writable,id")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return CloudResource{}, fmt.Errorf("hidrive inspect: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return CloudResource{}, fmt.Errorf("hidrive inspect failed, status: %d", resp.StatusCode)
	}

	var meta hidriveMetaResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return CloudResource{}, err
	}

	res := CloudResource{
		Path:  meta.Path,
		Name:  meta.Name,
		IsDir: meta.Type == "dir",
		Size:  meta.Size,
	}
	if meta.Mtime > 0 {
		res.LastModified = time.Unix(meta.Mtime, 0)
	}
	return res, nil
}

func (p *HiDriveProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	hdPath := p.cleanPath(filePath)

	req, err := http.NewRequestWithContext(ctx, "GET", hidriveAPIBase+"/file", nil)
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

func (p *HiDriveProvider) deleteIfExists(ctx context.Context, filePath string) error {
	exists, _, err := p.FileExists(ctx, "files", filePath)
	if err != nil {
		return err
	}
	if exists {
		if err := p.DeleteFile(ctx, "files", filePath); err != nil {
			return err
		}
	}
	return nil
}

func (p *HiDriveProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return err
	}

	if err := p.deleteIfExists(ctx, filePath); err != nil {
		return err
	}

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

	req, err := http.NewRequestWithContext(uploadCtx, "POST", hidriveAPIBase+"/file", stream)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("hidrive upload failed, status: %d, body: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (p *HiDriveProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if progressChan != nil {
		defer close(progressChan)
	}

	const chunkSize int64 = 50 * 1024 * 1024 // 50 MB per chunk

	if size <= chunkSize {
		return p.StreamUpload(ctx, resourceType, filePath, stream, size)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return err
	}

	if err := p.deleteIfExists(ctx, filePath); err != nil {
		return err
	}

	dir := path.Dir(filePath)
	name := path.Base(filePath)

	buf := make([]byte, chunkSize)
	var uploaded int64
	chunkIndex := 0

	for uploaded < size {
		n, readErr := io.ReadFull(stream, buf)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return fmt.Errorf("hidrive chunked read: %w", readErr)
		}
		chunkData := buf[:n]
		chunkStart := uploaded
		chunkEnd := uploaded + int64(n) - 1
		chunkSizeActual := int64(n)

		timeout := 10 * time.Minute
		uploadCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		body := bytes.NewReader(chunkData)
		req, err := http.NewRequestWithContext(uploadCtx, "POST", hidriveAPIBase+"/file", body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+p.AccessToken)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = chunkSizeActual
		contentRange := fmt.Sprintf("bytes %d-%d/%d", chunkStart, chunkEnd, size)
		req.Header.Set("Content-Range", contentRange)

		q := req.URL.Query()
		q.Set("dir", p.cleanPath(dir))
		q.Set("name", name)
		req.URL.RawQuery = q.Encode()

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			cancel()
			return fmt.Errorf("hidrive chunked upload chunk %d: %w", chunkIndex, err)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			cancel()
			return fmt.Errorf("hidrive chunked upload: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			cancel()
			return fmt.Errorf("hidrive chunked upload chunk %d failed, status: %d, body: %s", chunkIndex, resp.StatusCode, string(bodyBytes))
		}
		resp.Body.Close()

		uploaded += chunkSizeActual
		chunkIndex++

		if progressChan != nil {
			progressChan <- uploaded
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return nil
}

func (p *HiDriveProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	hdPath := p.cleanPath(filePath)

	req, err := http.NewRequestWithContext(ctx, "GET", hidriveAPIBase+"/meta", nil)
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
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	hdPath := p.cleanPath(filePath)

	var req *http.Request
	var err error

	meta, mErr := p.InspectResource(ctx, resourceType, hdPath)
	if mErr != nil {
		return mErr
	}

	if meta.IsDir {
		req, err = http.NewRequestWithContext(ctx, "DELETE", hidriveAPIBase+"/dir", nil)
		if err != nil {
			return err
		}
		q := req.URL.Query()
		q.Set("path", hdPath)
		q.Set("recursive", "true")
		req.URL.RawQuery = q.Encode()
	} else {
		req, err = http.NewRequestWithContext(ctx, "DELETE", hidriveAPIBase+"/file", nil)
		if err != nil {
			return err
		}
		q := req.URL.Query()
		q.Set("path", hdPath)
		req.URL.RawQuery = q.Encode()
	}

	req.Header.Set("Authorization", "Bearer "+p.AccessToken)

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
	return "", nil
}

func (p *HiDriveProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" {
		return nil
	}
	return p.CreateDirectory(ctx, resourceType, dir)
}

func (p *HiDriveProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
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

		req, err := http.NewRequestWithContext(ctx, "POST", hidriveAPIBase+"/dir", nil)
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

func (p *HiDriveProvider) SupportsAtomicRename() bool {
	return true
}

func (p *HiDriveProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	srcPath := p.cleanPath(oldPath)
	dstPath := p.cleanPath(newPath)

	req, err := http.NewRequestWithContext(ctx, "POST", hidriveAPIBase+"/dir/move", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("src", srcPath)
	q.Set("dst", dstPath)
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("hidrive rename: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hidrive rename failed, status: %d", resp.StatusCode)
	}

	return nil
}
