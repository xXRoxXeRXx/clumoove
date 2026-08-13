package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

const koofrBaseURL = "https://app.koofr.net"

// KoofrProvider implements the public Koofr API using its fixed endpoint.
// It is deliberately task-scoped: credentials and the selected primary mount
// live only for the lifetime of the provider instance.
type KoofrProvider struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client

	mu      sync.RWMutex
	mountID string
}

var _ StorageProvider = (*KoofrProvider)(nil)

// globalKoofrCreatedDirs avoids repeated existence and create requests across
// short-lived provider instances constructed for individual tasks.
var globalKoofrCreatedDirs = newBoundedDirCache(5000)

// ErrKoofrPrimaryMountNotFound indicates that the account has no usable
// primary mount configured.
var ErrKoofrPrimaryMountNotFound = errors.New("koofr primary mount not found")

type koofrMount struct {
	ID        string `json:"id"`
	IsPrimary bool   `json:"isPrimary"`
}

type koofrMountList struct {
	Mounts []koofrMount `json:"mounts"`
}

type koofrFileInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Modified int64  `json:"modified"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
	Hash     string `json:"hash"`
}

type koofrFileList struct {
	Files []koofrFileInfo `json:"files"`
}

type koofrMoveRequest struct {
	ToMountID string `json:"toMountId"`
	ToPath    string `json:"toPath"`
}

// NewKoofrProvider creates a provider for the public Koofr endpoint only.
func NewKoofrProvider(username, password string) (*KoofrProvider, error) {
	return &KoofrProvider{
		BaseURL:  koofrBaseURL,
		Username: username,
		Password: password,
		HTTPClient: &http.Client{
			Transport:     newLoggingTransport(newDAVTransport("app.koofr.net")),
			Timeout:       0,
			CheckRedirect: rejectEgressRedirect,
		},
	}, nil
}

func (p *KoofrProvider) Close() error {
	if p.HTTPClient != nil {
		p.HTTPClient.CloseIdleConnections()
	}
	return nil
}

func (p *KoofrProvider) Connect(ctx context.Context) (bool, error) {
	req, err := p.newRequest(ctx, http.MethodGet, "/api/v2/mounts", nil)
	if err != nil {
		return false, err
	}
	resp, err := p.do(req, http.StatusOK)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("read koofr mounts: %w", err)
	}

	var mounts []koofrMount
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var list koofrMountList
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return false, fmt.Errorf("decode koofr mounts: %w", err)
		}
		mounts = list.Mounts
	} else {
		if err := json.Unmarshal(trimmed, &mounts); err != nil {
			return false, fmt.Errorf("decode koofr mounts: %w", err)
		}
	}
	for _, mount := range mounts {
		if mount.IsPrimary && mount.ID != "" {
			p.mu.Lock()
			p.mountID = mount.ID
			p.mu.Unlock()
			return true, nil
		}
	}
	return false, ErrKoofrPrimaryMountNotFound
}

func (p *KoofrProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return nil, err
	}
	dirPath, err := normalizeKoofrPath(dirPath)
	if err != nil {
		return nil, err
	}
	req, err := p.mountRequest(ctx, http.MethodGet, "/files/list", url.Values{"path": {dirPath}}, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var listing koofrFileList
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return nil, fmt.Errorf("decode koofr directory listing: %w", err)
	}
	resources := make([]CloudResource, 0, len(listing.Files))
	for _, item := range listing.Files {
		itemPath, err := koofrChildPath(dirPath, item.Name)
		if err != nil {
			// A malformed server entry must not make an otherwise valid directory
			// unbrowsable or prevent the indexer from processing its other files.
			continue
		}
		resources = append(resources, koofrCloudResource(item, itemPath))
	}
	return resources, nil
}

func (p *KoofrProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return CloudResource{}, err
	}
	filePath, err := normalizeKoofrPath(filePath)
	if err != nil {
		return CloudResource{}, err
	}
	if filePath == "/" {
		return CloudResource{Path: "/", IsDir: true}, nil
	}
	info, err := p.fileInfo(ctx, filePath)
	if err != nil {
		return CloudResource{}, err
	}
	return koofrCloudResource(info, filePath), nil
}

func (p *KoofrProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return nil, err
	}
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return nil, err
	}
	req, err := p.contentMountRequest(ctx, http.MethodGet, "/files/get", url.Values{"path": {filePath}}, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(req, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (p *KoofrProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	return p.upload(ctx, filePath, stream, size)
}

func (p *KoofrProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	return p.upload(ctx, filePath, &ProgressReader{Reader: stream, ProgressChan: progressChan}, size)
}

func (p *KoofrProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return false, 0, err
	}
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return false, 0, err
	}
	info, err := p.fileInfo(ctx, filePath)
	if errors.Is(err, ErrNotFound) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, info.Size, nil
}

func (p *KoofrProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return err
	}
	req, err := p.mountRequest(ctx, http.MethodDelete, "/files/remove", url.Values{"path": {filePath}}, nil)
	if err != nil {
		return err
	}
	resp, err := p.do(req, http.StatusOK)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		globalKoofrCreatedDirs.Remove(p.BaseURL + "|" + p.Username + "|" + filePath)
	}
	return err
}

func (p *KoofrProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if err := p.requireFiles(resourceType); err != nil {
		return "", err
	}
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return "", err
	}
	info, err := p.fileInfo(ctx, filePath)
	if err != nil {
		return "", err
	}
	if info.Type == "dir" || strings.TrimSpace(info.Hash) == "" {
		return "", ErrChecksumNotAvailable
	}
	return "MD5:" + strings.ToLower(info.Hash), nil
}

func (p *KoofrProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return err
	}
	parent := path.Dir(filePath)
	if parent == "/" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(parent, "/"), "/")
	current := "/"
	for _, name := range parts {
		if err := p.createFolder(ctx, current, name); err != nil {
			return err
		}
		current = path.Join(current, name)
	}
	return nil
}

func (p *KoofrProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	dirPath, err := normalizeKoofrFilePath(dirPath)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(dirPath, "/"), "/")
	current := "/"
	for _, name := range parts {
		if err := p.createFolder(ctx, current, name); err != nil {
			return err
		}
		current = path.Join(current, name)
	}
	return nil
}

func (p *KoofrProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if err := p.requireFiles(resourceType); err != nil {
		return err
	}
	oldPath, err := normalizeKoofrFilePath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = normalizeKoofrFilePath(newPath)
	if err != nil {
		return err
	}
	mountID := p.connectedMountID()
	if mountID == "" {
		return ErrNotConnected
	}
	body, err := json.Marshal(koofrMoveRequest{ToMountID: mountID, ToPath: newPath})
	if err != nil {
		return fmt.Errorf("encode koofr move request: %w", err)
	}
	req, err := p.mountRequest(ctx, http.MethodPut, "/files/move", url.Values{"path": {oldPath}}, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(req, http.StatusOK)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}

func (p *KoofrProvider) SupportsAtomicRename() bool { return false }

func (p *KoofrProvider) VerificationMode() VerificationMode { return VerificationCryptographicHash }

func (p *KoofrProvider) requireFiles(resourceType string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	return nil
}

func (p *KoofrProvider) fileInfo(ctx context.Context, filePath string) (koofrFileInfo, error) {
	req, err := p.mountRequest(ctx, http.MethodGet, "/files/info", url.Values{"path": {filePath}}, nil)
	if err != nil {
		return koofrFileInfo{}, err
	}
	resp, err := p.do(req, http.StatusOK, http.StatusNotFound, http.StatusBadRequest)
	if err != nil {
		return koofrFileInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return koofrFileInfo{}, ErrNotFound
	}
	var info koofrFileInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return koofrFileInfo{}, fmt.Errorf("decode koofr file info: %w", err)
	}
	return info, nil
}

func (p *KoofrProvider) upload(ctx context.Context, filePath string, stream io.Reader, size int64) error {
	filePath, err := normalizeKoofrFilePath(filePath)
	if err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("koofr upload size must not be negative")
	}
	boundary := "clumoove-koofr-upload"
	header := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"file\"\r\nContent-Type: application/octet-stream\r\n\r\n"
	trailer := "\r\n--" + boundary + "--\r\n"
	parent, filename := path.Dir(filePath), path.Base(filePath)
	query := url.Values{
		"path":                       {parent},
		"filename":                   {filename},
		"info":                       {"true"},
		"overwrite":                  {"true"},
		"autorename":                 {"false"},
		"overwriteIgnoreNonexisting": {""},
	}
	if meta, ok := TransferMetadataFromContext(ctx); ok && !meta.ModifiedTime.IsZero() {
		query.Set("modified", fmt.Sprintf("%d", meta.ModifiedTime.UnixMilli()))
	}
	req, err := p.contentMountRequest(ctx, http.MethodPost, "/files/put", query, io.MultiReader(strings.NewReader(header), stream, strings.NewReader(trailer)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	req.ContentLength = int64(len(header)+len(trailer)) + size
	resp, err := p.do(req, http.StatusOK)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var info koofrFileInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("decode koofr upload response: %w", err)
	}
	return nil
}

func (p *KoofrProvider) createFolder(ctx context.Context, parent, name string) error {
	fullPath, err := koofrChildPath(parent, name)
	if err != nil {
		return err
	}
	cacheKey := p.BaseURL + "|" + p.Username + "|" + fullPath
	if globalKoofrCreatedDirs.Contains(cacheKey) {
		return nil
	}
	if info, err := p.fileInfo(ctx, fullPath); err == nil {
		if info.Type == "dir" {
			globalKoofrCreatedDirs.Add(cacheKey)
			return nil
		}
		return fmt.Errorf("koofr path %q exists and is not a directory", fullPath)
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}

	reqBody, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return fmt.Errorf("encode koofr folder request: %w", err)
	}
	req, err := p.mountRequest(ctx, http.MethodPost, "/files/folder", url.Values{"path": {parent}}, strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(req, http.StatusOK, http.StatusCreated, http.StatusBadRequest, http.StatusConflict)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		globalKoofrCreatedDirs.Add(cacheKey)
		return nil
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
		info, err := p.fileInfo(ctx, fullPath)
		if err == nil {
			if info.Type == "dir" {
				globalKoofrCreatedDirs.Add(cacheKey)
				return nil
			}
			return fmt.Errorf("koofr path %q exists and is not a directory", fullPath)
		}
		globalKoofrCreatedDirs.Add(cacheKey)
		return nil
	}
	return koofrHTTPError(resp.StatusCode)
}

func (p *KoofrProvider) mountRequest(ctx context.Context, method, suffix string, query url.Values, body io.Reader) (*http.Request, error) {
	mountID := p.connectedMountID()
	if mountID == "" {
		return nil, ErrNotConnected
	}
	req, err := p.newMountedRequest(ctx, method, "/api/v2/mounts/", suffix, body)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = query.Encode()
	return req, nil
}

func (p *KoofrProvider) contentMountRequest(ctx context.Context, method, suffix string, query url.Values, body io.Reader) (*http.Request, error) {
	req, err := p.newMountedRequest(ctx, method, "/content/api/v2/mounts/", suffix, body)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = query.Encode()
	return req, nil
}

func (p *KoofrProvider) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" {
		return nil, fmt.Errorf("invalid Koofr endpoint")
	}
	base.Path = strings.TrimRight(base.Path, "/") + endpoint
	req, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create koofr request: %w", err)
	}
	req.SetBasicAuth(p.Username, p.Password)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (p *KoofrProvider) newMountedRequest(ctx context.Context, method, prefix, suffix string, body io.Reader) (*http.Request, error) {
	mountID := p.connectedMountID()
	if mountID == "" {
		return nil, ErrNotConnected
	}
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" {
		return nil, fmt.Errorf("invalid Koofr endpoint")
	}
	basePath := strings.TrimRight(base.EscapedPath(), "/")
	base.Path = strings.TrimRight(base.Path, "/") + prefix + mountID + suffix
	base.RawPath = basePath + prefix + url.PathEscape(mountID) + suffix
	req, err := http.NewRequestWithContext(ctx, method, base.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create koofr request: %w", err)
	}
	req.SetBasicAuth(p.Username, p.Password)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (p *KoofrProvider) do(req *http.Request, statuses ...int) (*http.Response, error) {
	replayable := req.Body == nil || req.GetBody != nil
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("reopen koofr request body: %w", err)
			}
			req.Body = body
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("koofr request: %w", err)
		}
		for _, status := range statuses {
			if resp.StatusCode == status {
				return resp, nil
			}
		}
		if replayable && attempt < 2 && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError) {
			delay := koofrRetryDelay(resp.Header.Get("Retry-After"))
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if err := waitForRetry(req.Context(), delay); err != nil {
				return nil, err
			}
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
		return nil, koofrHTTPError(resp.StatusCode)
	}
	return nil, fmt.Errorf("koofr request retries exhausted")
}

func koofrRetryDelay(value string) time.Duration {
	if retry := parseRetryAfter(value, time.Now()); retry != nil {
		return retry.After
	}
	return time.Second
}

func koofrHTTPError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("koofr request returned status %d", status)
	}
}

func (p *KoofrProvider) connectedMountID() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mountID
}

func normalizeKoofrPath(value string) (string, error) {
	if err := validateStoragePath(value); err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" || value == "/" {
		return "/", nil
	}
	return path.Clean("/" + strings.TrimPrefix(value, "/")), nil
}

func koofrChildPath(parent, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", ErrPathEscapesRoot
	}
	return normalizeKoofrPath(path.Join(parent, name))
}

func normalizeKoofrFilePath(value string) (string, error) {
	clean, err := normalizeKoofrPath(value)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return "", fmt.Errorf("koofr file path must not be root")
	}
	return clean, nil
}

func koofrCloudResource(info koofrFileInfo, resourcePath string) CloudResource {
	resource := CloudResource{
		Path:  resourcePath,
		Name:  info.Name,
		Size:  info.Size,
		IsDir: info.Type == "dir",
	}
	if info.Modified > 0 {
		resource.LastModified = time.UnixMilli(info.Modified).UTC()
	}
	if !resource.IsDir && info.Hash != "" {
		resource.Hash = "MD5:" + strings.ToLower(info.Hash)
	}
	return resource
}
