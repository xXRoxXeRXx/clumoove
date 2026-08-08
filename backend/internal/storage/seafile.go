package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// SeafileProvider implements StorageProvider for Seafile Web API v2.1.
type SeafileProvider struct {
	BaseURL    string
	Username   string
	Password   string
	Token      string
	HTTPClient *http.Client

	mu        sync.RWMutex
	repoCache map[string]string // maps repo name -> repo ID
}

type seafileRepo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Mtime int64  `json:"mtime"`
}

type seafileDirItem struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// NewSeafileProvider creates a new Seafile storage provider instance.
func NewSeafileProvider(urlStr, username, password string) (*SeafileProvider, error) {
	urlStr = strings.TrimRight(urlStr, "/")
	if urlStr == "" {
		return nil, fmt.Errorf("seafile provider requires a valid URL")
	}

	if err := validateEgressURL(urlStr); err != nil {
		return nil, fmt.Errorf("invalid seafile server URL: %w", err)
	}

	client, err := NewEgressHTTPClient(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client for seafile: %w", err)
	}

	return &SeafileProvider{
		BaseURL:    urlStr,
		Username:   username,
		Password:   password,
		HTTPClient: client,
		repoCache:  make(map[string]string),
	}, nil
}

func (p *SeafileProvider) Close() error {
	if p.HTTPClient != nil {
		p.HTTPClient.CloseIdleConnections()
	}
	return nil
}

func (p *SeafileProvider) invalidateToken() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Token = ""
}

func (p *SeafileProvider) getToken(ctx context.Context) (string, error) {
	p.mu.RLock()
	if p.Token != "" {
		token := p.Token
		p.mu.RUnlock()
		return token, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.Token != "" {
		return p.Token, nil
	}

	// If username is empty and password is provided, treat password as API token.
	if p.Username == "" && p.Password != "" {
		p.Token = p.Password
		return p.Token, nil
	}

	tokenURL := fmt.Sprintf("%s/api2/auth-token/", p.BaseURL)
	form := url.Values{}
	form.Set("username", p.Username)
	form.Set("password", p.Password)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: seafile auth request failed", ErrAuth)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest {
		return "", fmt.Errorf("%w: invalid seafile credentials", ErrAuth)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("seafile auth failed with status %d", resp.StatusCode)
	}

	var authResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil || authResp.Token == "" {
		return "", fmt.Errorf("%w: failed to parse auth token response", ErrAuth)
	}

	p.Token = authResp.Token
	return p.Token, nil
}

func (p *SeafileProvider) newAuthRequest(ctx context.Context, method, reqURL string, body io.Reader) (*http.Request, error) {
	token, err := p.getToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (p *SeafileProvider) listRepos(ctx context.Context) ([]seafileRepo, error) {
	reqURL := fmt.Sprintf("%s/api2/repos/", p.BaseURL)
	req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list seafile repos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seafile list repos returned status %d", resp.StatusCode)
	}

	var repos []seafileRepo
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("failed to decode seafile repos: %w", err)
	}

	p.mu.Lock()
	for _, repo := range repos {
		p.repoCache[repo.Name] = repo.ID
		p.repoCache[repo.ID] = repo.ID
	}
	p.mu.Unlock()

	return repos, nil
}

func (p *SeafileProvider) resolveRepoAndPath(ctx context.Context, pth string) (repoID string, repoPath string, err error) {
	clean := strings.Trim(pth, "/")
	if clean == "" {
		return "", "", nil
	}

	parts := strings.Split(clean, "/")
	targetRepo := parts[0]

	p.mu.RLock()
	cachedID, found := p.repoCache[targetRepo]
	p.mu.RUnlock()

	if found {
		relPath := "/" + strings.Join(parts[1:], "/")
		return cachedID, relPath, nil
	}

	repos, err := p.listRepos(ctx)
	if err != nil {
		return "", "", err
	}

	for _, r := range repos {
		if r.Name == targetRepo || r.ID == targetRepo {
			relPath := "/" + strings.Join(parts[1:], "/")
			return r.ID, relPath, nil
		}
	}

	return "", "", ErrNotFound
}

func (p *SeafileProvider) Connect(ctx context.Context) (bool, error) {
	if _, err := p.getToken(ctx); err != nil {
		return false, err
	}
	if _, err := p.listRepos(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (p *SeafileProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, ErrUnsupportedResourceType
	}

	repoID, repoPath, err := p.resolveRepoAndPath(ctx, dirPath)
	if err != nil {
		return nil, err
	}

	if repoID == "" {
		repos, err := p.listRepos(ctx)
		if err != nil {
			return nil, err
		}
		var resources []CloudResource
		for _, repo := range repos {
			resources = append(resources, CloudResource{
				Path:         "/" + repo.Name,
				Name:         repo.Name,
				IsDir:        true,
				LastModified: time.Unix(repo.Mtime, 0),
			})
		}
		return resources, nil
	}

	reqURL := fmt.Sprintf("%s/api2/repos/%s/dir/?p=%s", p.BaseURL, repoID, url.QueryEscape(repoPath))
	req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list seafile directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seafile list dir returned status %d", resp.StatusCode)
	}

	var items []seafileDirItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("failed to decode seafile directory listing: %w", err)
	}

	cleanDir := strings.TrimRight(dirPath, "/")
	var resources []CloudResource
	for _, item := range items {
		itemPath := cleanDir + "/" + item.Name
		if !strings.HasPrefix(itemPath, "/") {
			itemPath = "/" + itemPath
		}

		cr := CloudResource{
			Path:         itemPath,
			Name:         item.Name,
			Size:         item.Size,
			IsDir:        item.Type == "dir",
			LastModified: time.Unix(item.Mtime, 0),
		}
		resources = append(resources, cr)
	}

	return resources, nil
}

func (p *SeafileProvider) InspectResource(ctx context.Context, resourceType, pth string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, ErrUnsupportedResourceType
	}

	clean := strings.Trim(pth, "/")
	if clean == "" {
		return CloudResource{Path: "/", Name: "", IsDir: true}, nil
	}

	repoID, repoPath, err := p.resolveRepoAndPath(ctx, pth)
	if err != nil {
		return CloudResource{}, err
	}

	if repoPath == "/" {
		repos, err := p.listRepos(ctx)
		if err != nil {
			return CloudResource{}, err
		}
		for _, repo := range repos {
			if repo.ID == repoID {
				return CloudResource{
					Path:         "/" + repo.Name,
					Name:         repo.Name,
					IsDir:        true,
					LastModified: time.Unix(repo.Mtime, 0),
				}, nil
			}
		}
		return CloudResource{}, ErrNotFound
	}

	parentPath := path.Dir(pth)
	items, err := p.GetDirectoryListing(ctx, resourceType, parentPath)
	if err != nil {
		return CloudResource{}, err
	}

	baseName := path.Base(pth)
	for _, item := range items {
		if item.Name == baseName {
			return item, nil
		}
	}

	return CloudResource{}, ErrNotFound
}

func (p *SeafileProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, ErrUnsupportedResourceType
	}

	repoID, repoPath, err := p.resolveRepoAndPath(ctx, filePath)
	if err != nil {
		return nil, err
	}

	reqURL := fmt.Sprintf("%s/api2/repos/%s/file/?p=%s&reuse=1", p.BaseURL, repoID, url.QueryEscape(repoPath))
	req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get seafile download link: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return nil, ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seafile download link request status %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read download link response: %w", err)
	}

	downloadURL := strings.Trim(string(bodyBytes), `"`)
	if err := validateEgressURL(downloadURL); err != nil {
		return nil, fmt.Errorf("seafile download URL failed egress check: %w", err)
	}

	downloadReq, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create download stream request: %w", err)
	}

	dlResp, err := p.HTTPClient.Do(downloadReq)
	if err != nil {
		return nil, fmt.Errorf("failed to execute seafile download stream: %w", err)
	}

	if dlResp.StatusCode != http.StatusOK {
		dlResp.Body.Close()
		return nil, fmt.Errorf("seafile download stream status %d", dlResp.StatusCode)
	}

	return dlResp.Body, nil
}

func (p *SeafileProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}

	repoID, repoPath, err := p.resolveRepoAndPath(ctx, filePath)
	if err != nil && err != ErrNotFound {
		return err
	}

	if repoID == "" {
		clean := strings.Trim(filePath, "/")
		parts := strings.Split(clean, "/")
		if len(parts) < 2 {
			return fmt.Errorf("cannot upload to root without a target library")
		}
		repoName := parts[0]
		if err := p.CreateDirectory(ctx, "files", "/"+repoName); err != nil {
			return fmt.Errorf("failed to create target library %s: %w", repoName, err)
		}
		repoID, repoPath, err = p.resolveRepoAndPath(ctx, filePath)
		if err != nil {
			return fmt.Errorf("failed to resolve newly created target library %s: %w", repoName, err)
		}
	}

	parentDir := path.Dir(repoPath)
	if parentDir == "." || parentDir == "" {
		parentDir = "/"
	}
	fileName := path.Base(repoPath)

	reqURL := fmt.Sprintf("%s/api2/repos/%s/upload-link/?p=%s", p.BaseURL, repoID, url.QueryEscape(parentDir))
	req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get seafile upload link: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seafile get upload link status %d", resp.StatusCode)
	}

	linkBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read upload link: %w", err)
	}
	uploadURL := strings.Trim(string(linkBytes), `"`)

	if err := validateEgressURL(uploadURL); err != nil {
		return fmt.Errorf("seafile upload URL failed egress check: %w", err)
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		var writeErr error
		defer func() {
			_ = writer.Close()
			pw.CloseWithError(writeErr)
		}()

		if err := writer.WriteField("parent_dir", parentDir); err != nil {
			writeErr = err
			return
		}
		if err := writer.WriteField("replace", "1"); err != nil {
			writeErr = err
			return
		}

		part, err := writer.CreateFormFile("file", fileName)
		if err != nil {
			writeErr = err
			return
		}

		if _, err := io.Copy(part, stream); err != nil {
			writeErr = err
			return
		}
	}()

	uploadReq, err := http.NewRequestWithContext(ctx, "POST", uploadURL, pr)
	if err != nil {
		_ = pr.Close()
		return fmt.Errorf("failed to create upload request: %w", err)
	}
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())

	token, _ := p.getToken(ctx)
	if token != "" {
		uploadReq.Header.Set("Authorization", "Token "+token)
	}

	upResp, err := p.HTTPClient.Do(uploadReq)
	if err != nil {
		_ = pr.Close()
		return fmt.Errorf("failed to execute seafile upload: %w", err)
	}
	defer upResp.Body.Close()

	if upResp.StatusCode == http.StatusUnauthorized || upResp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return ErrAuth
	}

	if upResp.StatusCode != http.StatusOK && upResp.StatusCode != http.StatusCreated {
		return fmt.Errorf("seafile upload failed with status %d", upResp.StatusCode)
	}

	return nil
}

func (p *SeafileProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	pr := &ProgressReader{
		Reader:       stream,
		ProgressChan: progressChan,
	}
	return p.StreamUpload(ctx, resourceType, filePath, pr, size)
}

func (p *SeafileProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		if err == ErrNotFound {
			return false, 0, nil
		}
		return false, 0, err
	}
	return !res.IsDir, res.Size, nil
}

func (p *SeafileProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}

	repoID, repoPath, err := p.resolveRepoAndPath(ctx, filePath)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}

	reqURL := fmt.Sprintf("%s/api2/repos/%s/file/?p=%s", p.BaseURL, repoID, url.QueryEscape(repoPath))
	req, err := p.newAuthRequest(ctx, "DELETE", reqURL, nil)
	if err != nil {
		return err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete seafile item: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return ErrAuth
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seafile delete item status %d", resp.StatusCode)
	}

	return nil
}

func (p *SeafileProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}

	clean := strings.Trim(dirPath, "/")
	if clean == "" {
		return nil
	}

	parts := strings.Split(clean, "/")
	repoName := parts[0]

	p.mu.RLock()
	cachedID, found := p.repoCache[repoName]
	p.mu.RUnlock()

	var repoID string
	if found {
		repoID = cachedID
	} else {
		repos, err := p.listRepos(ctx)
		if err != nil {
			return err
		}
		for _, r := range repos {
			if r.Name == repoName || r.ID == repoName {
				repoID = r.ID
				break
			}
		}
	}

	if repoID == "" {
		// Create library via API
		reqURL := fmt.Sprintf("%s/api2/repos/", p.BaseURL)
		form := url.Values{}
		form.Set("name", repoName)

		req, err := p.newAuthRequest(ctx, "POST", reqURL, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to create seafile library: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			p.invalidateToken()
			return ErrAuth
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("seafile create repo failed status %d", resp.StatusCode)
		}

		var created struct {
			RepoID string `json:"repo_id"`
			ID     string `json:"id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&created)
		if created.RepoID != "" {
			repoID = created.RepoID
		} else {
			repoID = created.ID
		}
		if repoID != "" {
			p.mu.Lock()
			p.repoCache[repoName] = repoID
			p.repoCache[repoID] = repoID
			p.mu.Unlock()
		}
	}

	if len(parts) == 1 {
		return nil
	}

	relPath := "/" + strings.Join(parts[1:], "/")
	reqURL := fmt.Sprintf("%s/api2/repos/%s/dir/?p=%s", p.BaseURL, repoID, url.QueryEscape(relPath))
	form := url.Values{}
	form.Set("operation", "mkdir")

	req, err := p.newAuthRequest(ctx, "POST", reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create seafile directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return ErrAuth
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("seafile mkdir status %d", resp.StatusCode)
	}

	return nil
}

func (p *SeafileProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	parentDir := path.Dir(filePath)
	if parentDir == "." || parentDir == "/" || parentDir == "" {
		return nil
	}
	return p.CreateDirectory(ctx, resourceType, parentDir)
}

func (p *SeafileProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}

	repoID, oldRepoPath, err := p.resolveRepoAndPath(ctx, oldPath)
	if err != nil {
		return err
	}

	newName := path.Base(newPath)
	reqURL := fmt.Sprintf("%s/api2/repos/%s/file/?p=%s", p.BaseURL, repoID, url.QueryEscape(oldRepoPath))
	form := url.Values{}
	form.Set("operation", "rename")
	form.Set("newname", newName)

	req, err := p.newAuthRequest(ctx, "POST", reqURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to rename seafile file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		p.invalidateToken()
		return ErrAuth
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("seafile rename file status %d", resp.StatusCode)
	}

	return nil
}

func (p *SeafileProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if resourceType != "files" {
		return "", ErrUnsupportedResourceType
	}
	return "", ErrChecksumNotAvailable
}

func (p *SeafileProvider) SupportsAtomicRename() bool {
	return false
}

func (p *SeafileProvider) VerificationMode() VerificationMode {
	return VerificationSizeOnly
}
