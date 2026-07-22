package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// DropboxHasher implements hash.Hash for Dropbox's custom content_hash algorithm.
type DropboxHasher struct {
	currentBlockHasher hash.Hash
	concatenatedHashes []byte
	currentBlockSize   int
}

func NewDropboxHasher() *DropboxHasher {
	return &DropboxHasher{
		currentBlockHasher: sha256.New(),
	}
}

func (dh *DropboxHasher) Write(p []byte) (n int, err error) {
	const blockSize = 4 * 1024 * 1024 // 4MB
	totalWritten := 0
	for len(p) > 0 {
		bytesToNextBlock := blockSize - dh.currentBlockSize
		toWrite := len(p)
		if toWrite > bytesToNextBlock {
			toWrite = bytesToNextBlock
		}

		nBlock, _ := dh.currentBlockHasher.Write(p[:toWrite])
		totalWritten += nBlock
		dh.currentBlockSize += nBlock
		p = p[toWrite:]

		if dh.currentBlockSize == blockSize {
			dh.concatenatedHashes = append(dh.concatenatedHashes, dh.currentBlockHasher.Sum(nil)...)
			dh.currentBlockHasher.Reset()
			dh.currentBlockSize = 0
		}
	}
	return totalWritten, nil
}

func (dh *DropboxHasher) Sum(b []byte) []byte {
	if len(dh.concatenatedHashes) == 0 && dh.currentBlockSize == 0 {
		emptyHash := sha256.Sum256([]byte{})
		return append(b, emptyHash[:]...)
	}

	concat := make([]byte, len(dh.concatenatedHashes))
	copy(concat, dh.concatenatedHashes)

	if dh.currentBlockSize > 0 {
		blockSum := dh.currentBlockHasher.Sum(nil)
		concat = append(concat, blockSum...)
	}

	finalHash := sha256.Sum256(concat)
	return append(b, finalHash[:]...)
}

func (dh *DropboxHasher) Reset() {
	dh.currentBlockHasher.Reset()
	dh.concatenatedHashes = nil
	dh.currentBlockSize = 0
}

func (dh *DropboxHasher) Size() int {
	return sha256.Size
}

func (dh *DropboxHasher) BlockSize() int {
	return sha256.BlockSize
}

// DropboxProvider implements StorageProvider for Dropbox.
type DropboxProvider struct {
	AccessToken string
	HTTPClient  *http.Client
}

func NewDropboxProvider(token string) (*DropboxProvider, error) {
	tr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
	}
	return &DropboxProvider{
		AccessToken: token,
		HTTPClient: &http.Client{
			Transport: tr,
			Timeout:   0,
		},
	}, nil
}

func (p *DropboxProvider) Close() error {
	return nil
}

func (p *DropboxProvider) cleanPath(filePath string) string {
	clean := "/" + strings.Trim(filePath, "/")
	if clean == "/" {
		return ""
	}
	return clean
}

func escapeAPIArg(arg interface{}) (string, error) {
	b, err := json.Marshal(arg)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for _, r := range string(b) {
		if r < 128 {
			buf.WriteRune(r)
		} else {
			buf.WriteString(fmt.Sprintf("\\u%04x", r))
		}
	}
	return buf.String(), nil
}

func (p *DropboxProvider) newRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	req.Header.Set("User-Agent", "Dropbox-Migration-Worker/1.0")
	return req, nil
}

func (p *DropboxProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/users/get_current_account", bytes.NewReader([]byte("null")))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return false, fmt.Errorf("dropbox connect: %w", ErrAuth)
	}

	return false, fmt.Errorf("connection failed with status code: %d", resp.StatusCode)
}

type dbxEntry struct {
	Tag            string `json:".tag"`
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	Size           int64  `json:"size,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	ServerModified string `json:"server_modified,omitempty"`
}

type dbxListFolderResponse struct {
	Entries []dbxEntry `json:"entries"`
	Cursor  string     `json:"cursor"`
	HasMore bool       `json:"has_more"`
}

func (p *DropboxProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return nil, nil // Dropbox only supports files
	}

	pathArg := p.cleanPath(dirPath)

	reqBody, err := json.Marshal(map[string]interface{}{
		"path":            pathArg,
		"recursive":       false,
		"include_deleted": false,
		"include_mounted": true,
	})
	if err != nil {
		return nil, err
	}

	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/list_folder", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("dropbox listing: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list folder, status: %d", resp.StatusCode)
	}

	var listResp dbxListFolderResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, err
	}

	var resources []CloudResource
	for _, entry := range listResp.Entries {
		res := CloudResource{
			Path:  entry.PathDisplay,
			Name:  entry.Name,
			IsDir: entry.Tag == "folder",
			Size:  entry.Size,
			Hash:  entry.ContentHash,
		}
		if entry.ServerModified != "" {
			if t, err := time.Parse(time.RFC3339, entry.ServerModified); err == nil {
				res.LastModified = t
			}
		}
		resources = append(resources, res)
	}

	cursor := listResp.Cursor
	hasMore := listResp.HasMore

	for hasMore {
		contBody, err := json.Marshal(map[string]interface{}{
			"cursor": cursor,
		})
		if err != nil {
			return nil, err
		}

		contReq, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/list_folder/continue", bytes.NewReader(contBody))
		if err != nil {
			return nil, err
		}
		contReq.Header.Set("Content-Type", "application/json")
		contReq = contReq.WithContext(ctx)

		contResp, err := p.HTTPClient.Do(contReq)
		if err != nil {
			return nil, err
		}

		if contResp.StatusCode == http.StatusUnauthorized {
			contResp.Body.Close()
			return nil, fmt.Errorf("dropbox listing continue: %w", ErrAuth)
		}
		if contResp.StatusCode != http.StatusOK {
			contResp.Body.Close()
			return nil, fmt.Errorf("failed to continue folder listing, status: %d", contResp.StatusCode)
		}

		var contListResp dbxListFolderResponse
		decodeErr := json.NewDecoder(contResp.Body).Decode(&contListResp)
		contResp.Body.Close() // close immediately, not deferred, to avoid connection leak over many pages
		if decodeErr != nil {
			return nil, decodeErr
		}

		for _, entry := range contListResp.Entries {
			res := CloudResource{
				Path:  entry.PathDisplay,
				Name:  entry.Name,
				IsDir: entry.Tag == "folder",
				Size:  entry.Size,
				Hash:  entry.ContentHash,
			}
			if entry.ServerModified != "" {
				if t, err := time.Parse(time.RFC3339, entry.ServerModified); err == nil {
					res.LastModified = t
				}
			}
			resources = append(resources, res)
		}

		cursor = contListResp.Cursor
		hasMore = contListResp.HasMore
	}

	return resources, nil
}

func (p *DropboxProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	pathArg := p.cleanPath(resourcePath)
	if pathArg == "" {
		return CloudResource{
			Path:  "/",
			Name:  "",
			IsDir: true,
			Size:  0,
		}, nil
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"path": pathArg,
	})
	if err != nil {
		return CloudResource{}, err
	}

	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/get_metadata", bytes.NewReader(reqBody))
	if err != nil {
		return CloudResource{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return CloudResource{}, fmt.Errorf("dropbox inspect: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return CloudResource{}, fmt.Errorf("inspect resource failed, status: %d", resp.StatusCode)
	}

	var entry dbxEntry
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return CloudResource{}, err
	}

	res := CloudResource{
		Path:  entry.PathDisplay,
		Name:  entry.Name,
		IsDir: entry.Tag == "folder",
		Size:  entry.Size,
		Hash:  entry.ContentHash,
	}
	if entry.ServerModified != "" {
		if t, err := time.Parse(time.RFC3339, entry.ServerModified); err == nil {
			res.LastModified = t
		}
	}

	return res, nil
}

func (p *DropboxProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	pathArg := p.cleanPath(filePath)
	apiArg, err := escapeAPIArg(map[string]string{"path": pathArg})
	if err != nil {
		return nil, err
	}

	req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Dropbox-API-Arg", apiArg)
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, fmt.Errorf("dropbox download: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	return resp.Body, nil
}

func (p *DropboxProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	pathArg := p.cleanPath(filePath)
	apiArg, err := escapeAPIArg(map[string]interface{}{
		"path":       pathArg,
		"mode":       "overwrite",
		"autorename": false,
		"mute":       false,
	})
	if err != nil {
		return err
	}

	req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/upload", stream)
	if err != nil {
		return err
	}
	req.Header.Set("Dropbox-API-Arg", apiArg)
	req.Header.Set("Content-Type", "application/octet-stream")
	if size > 0 {
		req.ContentLength = size
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("dropbox upload: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upload failed with status: %d", resp.StatusCode)
	}

	return nil
}

func (p *DropboxProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	cleanPath := p.cleanPath(filePath)

	chunkSize := int64(10 * 1024 * 1024) // 10MB chunks
	buffer := make([]byte, chunkSize)

	bytesRead, err := io.ReadFull(stream, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}

	if bytesRead == 0 {
		return p.StreamUpload(ctx, resourceType, filePath, bytes.NewReader(nil), 0)
	}

	sessionID, err := p.startUploadSession(ctx, buffer[:bytesRead])
	if err != nil {
		return fmt.Errorf("failed to start upload session: %w", err)
	}

	var offset int64 = int64(bytesRead)
	if progressChan != nil {
		progressChan <- int64(bytesRead)
	}

	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return p.finishUploadSession(ctx, sessionID, offset, cleanPath, nil)
	}

	for {
		bytesRead, err = io.ReadFull(stream, buffer)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}

		if bytesRead == 0 {
			break
		}

		errAppend := p.appendUploadSessionWithRetry(ctx, sessionID, offset, buffer[:bytesRead])
		if errAppend != nil {
			return fmt.Errorf("failed to append chunk at offset %d: %w", offset, errAppend)
		}

		offset += int64(bytesRead)
		if progressChan != nil {
			progressChan <- int64(bytesRead)
		}

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
	}

	return p.finishUploadSession(ctx, sessionID, offset, cleanPath, nil)
}

func (p *DropboxProvider) startUploadSession(ctx context.Context, chunk []byte) (string, error) {
	apiArg, err := escapeAPIArg(map[string]interface{}{
		"close": false,
	})
	if err != nil {
		return "", err
	}

	req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/upload_session/start", bytes.NewReader(chunk))
	if err != nil {
		return "", err
	}
	req.Header.Set("Dropbox-API-Arg", apiArg)
	req.Header.Set("Content-Type", "application/octet-stream")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("dropbox upload session start: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to start upload session, status: %d", resp.StatusCode)
	}

	var res struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	return res.SessionID, nil
}

func (p *DropboxProvider) appendUploadSessionWithRetry(ctx context.Context, sessionID string, offset int64, chunk []byte) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		apiArg, err := escapeAPIArg(map[string]interface{}{
			"close": false,
			"cursor": map[string]interface{}{
				"session_id": sessionID,
				"offset":     offset,
			},
		})
		if err != nil {
			return err
		}

		req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/upload_session/append_v2", bytes.NewReader(chunk))
		if err != nil {
			return err
		}
		req.Header.Set("Dropbox-API-Arg", apiArg)
		req.Header.Set("Content-Type", "application/octet-stream")
		req = req.WithContext(ctx)

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("dropbox upload session append: %w", ErrAuth)
		}
		lastErr = fmt.Errorf("append session failed, status: %d", resp.StatusCode)
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return lastErr
}

func (p *DropboxProvider) finishUploadSession(ctx context.Context, sessionID string, offset int64, cleanPath string, chunk []byte) error {
	apiArg, err := escapeAPIArg(map[string]interface{}{
		"cursor": map[string]interface{}{
			"session_id": sessionID,
			"offset":     offset,
		},
		"commit": map[string]interface{}{
			"path":            cleanPath,
			"mode":            "overwrite",
			"autorename":      false,
			"mute":            false,
			"strict_conflict": false,
		},
	})
	if err != nil {
		return err
	}

	var body io.Reader
	if len(chunk) > 0 {
		body = bytes.NewReader(chunk)
	}

	req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/upload_session/finish", body)
	if err != nil {
		return err
	}
	req.Header.Set("Dropbox-API-Arg", apiArg)
	req.Header.Set("Content-Type", "application/octet-stream")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("dropbox upload session finish: %w", ErrAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to finish upload session, status: %d", resp.StatusCode)
	}

	return nil
}

func (p *DropboxProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return false, 0, nil
	}

	pathArg := p.cleanPath(filePath)
	if pathArg == "" {
		return true, 0, nil
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"path": pathArg,
	})
	if err != nil {
		return false, 0, err
	}

	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/get_metadata", bytes.NewReader(reqBody))
	if err != nil {
		return false, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var entry dbxEntry
		if err := json.NewDecoder(resp.Body).Decode(&entry); err == nil && entry.Tag == "file" {
			return true, entry.Size, nil
		}
		return true, 0, nil
	}

	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusNotFound {
		var errResp struct {
			ErrorSummary string `json:"error_summary"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
			if strings.Contains(errResp.ErrorSummary, "not_found") {
				return false, 0, nil
			}
		}
		return false, 0, nil
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return false, 0, fmt.Errorf("dropbox file-exists: %w", ErrAuth)
	}
	return false, 0, fmt.Errorf("failed to check file existence, status: %d", resp.StatusCode)
}

func (p *DropboxProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	pathArg := p.cleanPath(filePath)
	reqBody, err := json.Marshal(map[string]interface{}{
		"path": pathArg,
	})
	if err != nil {
		return err
	}

	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/delete_v2", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	if resp.StatusCode == http.StatusConflict {
		var errResp struct {
			ErrorSummary string `json:"error_summary"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
			if strings.Contains(errResp.ErrorSummary, "not_found") {
				return nil
			}
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("dropbox delete: %w", ErrAuth)
	}
	return fmt.Errorf("delete failed with status: %d", resp.StatusCode)
}

func (p *DropboxProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	arg := map[string]interface{}{
		"from_path":                p.cleanPath(oldPath),
		"to_path":                  p.cleanPath(newPath),
		"allow_shared_folder":      true,
		"autorename":               false,
		"allow_ownership_transfer": true,
	}
	body, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/move_v2", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("dropbox move: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("dropbox move failed with status: %d", resp.StatusCode)
	}
	return nil
}

// SupportsAtomicRename is true: Dropbox move_v2 is supported.
func (p *DropboxProvider) SupportsAtomicRename() bool {
	return true
}

func (p *DropboxProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return "", fmt.Errorf("resource type %s not supported by Dropbox", resourceType)
	}

	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return "", err
	}
	if res.Hash == "" {
		return "", fmt.Errorf("hash not found in Dropbox metadata")
	}
	return "DROPBOX:" + res.Hash, nil
}

var globalDropboxCreatedDirs sync.Map

func (p *DropboxProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	dir := path.Dir(filePath)
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	cleanDir := p.cleanPath(dir)
	if cleanDir == "" {
		return nil
	}

	globalDirKey := p.AccessToken + "|" + cleanDir
	if _, exists := globalDropboxCreatedDirs.Load(globalDirKey); exists {
		return nil
	}

	reqBody, err := json.Marshal(map[string]interface{}{
		"path":       cleanDir,
		"autorename": false,
	})
	if err != nil {
		return err
	}

	req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/create_folder_v2", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		globalDropboxCreatedDirs.Store(globalDirKey, true)
		return nil
	}

	if resp.StatusCode == http.StatusConflict {
		var errResp struct {
			ErrorSummary string `json:"error_summary"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err == nil {
			if strings.Contains(errResp.ErrorSummary, "path/conflict") {
				globalDropboxCreatedDirs.Store(globalDirKey, true)
				return nil
			}
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("dropbox mkdir: %w", ErrAuth)
	}
	return fmt.Errorf("failed to create directory, status: %d", resp.StatusCode)
}

// CreateDirectory creates the given directory path in Dropbox (including all intermediate
// parents). Dropbox's create_folder_v2 endpoint handles nested paths natively.
func (p *DropboxProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// CreateParentDirectories already calls create_folder_v2 with the full dir path.
	// Pass a synthetic child so the parent-extraction yields dirPath itself.
	return p.CreateParentDirectories(ctx, resourceType, path.Join(dirPath, "_placeholder"))
}
