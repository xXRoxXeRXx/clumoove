package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBoxAPIBase    = "https://api.box.com/2.0"
	defaultBoxUploadBase = "https://upload.box.com/api/2.0"
	boxRootFolderID      = "0"
	boxListPageLimit     = 1000
	boxMaxRetries        = 3
	boxItemFields        = "id,type,name,size,sha1,modified_at,content_modified_at,permissions,url"
	maxBoxJSONBodyBytes  = 16 << 20
)

// BoxProvider accesses a Box account via the Box Content API v2.
type BoxProvider struct {
	catalogueTraits
	accessToken string
	apiBase     string
	uploadBase  string
	httpClient  *http.Client
	pathCache   *boundedStringCache
}

var (
	_ StorageProvider = (*BoxProvider)(nil)
	_ RangeDownloader = (*BoxProvider)(nil)
)

// NewBoxProvider constructs a BoxProvider using the given OAuth2 access token.
func NewBoxProvider(token string) (*BoxProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("box provider requires an oauth token: %w", ErrAuth)
	}
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       120 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	return newBoxProvider(token, defaultBoxAPIBase, defaultBoxUploadBase, &http.Client{Transport: newUserAgentTransport(transport)}), nil
}

// NewBoxTestProvider constructs a BoxProvider targeting local loopback httptest
// endpoints for cross-package integration tests. Non-local endpoints are rejected.
func NewBoxTestProvider(token, apiBase, uploadBase string, client *http.Client) (*BoxProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("box provider requires an oauth token: %w", ErrAuth)
	}
	if !isBoxLocalTestingURL(apiBase) || (uploadBase != "" && !isBoxLocalTestingURL(uploadBase)) {
		return nil, fmt.Errorf("box test provider requires local loopback endpoints")
	}
	if client == nil {
		client = &http.Client{}
	}
	return newBoxProvider(token, apiBase, uploadBase, client), nil
}

// newBoxProvider is package-private so tests can target a local httptest Box
// server without exposing configurable API endpoints on the public constructor.
func newBoxProvider(token, apiBase, uploadBase string, client *http.Client) *BoxProvider {
	return &BoxProvider{
		accessToken: token,
		apiBase:     strings.TrimRight(apiBase, "/"),
		uploadBase:  strings.TrimRight(uploadBase, "/"),
		httpClient:  client,
		pathCache:   newBoundedStringCache(5000),
	}
}

func (p *BoxProvider) Close() error {
	if p != nil && p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}

func (p *BoxProvider) SupportsAtomicRename() bool {
	if p == nil {
		return (*catalogueTraits)(nil).supportsAtomicRename("box")
	}
	return p.catalogueTraits.supportsAtomicRename("box")
}

func (p *BoxProvider) VerificationMode() VerificationMode {
	if p == nil {
		return (*catalogueTraits)(nil).verificationMode("box")
	}
	return p.catalogueTraits.verificationMode("box")
}

func (p *BoxProvider) newRequest(ctx context.Context, method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.accessToken)
	return req, nil
}

func boxRetryDelay(retryAfter string, attempt int) time.Duration {
	trimmed := strings.TrimSpace(retryAfter)
	if trimmed != "" {
		if secs, err := strconv.Atoi(trimmed); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if retryAt, err := http.ParseTime(trimmed); err == nil {
			if delay := time.Until(retryAt); delay > 0 {
				return delay
			}
			return 0
		}
	}
	return 200 * time.Millisecond * (1 << attempt)
}

func boxWait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *BoxProvider) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	return p.doWithClient(ctx, p.httpClient, req)
}

func (p *BoxProvider) doWithClient(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		return nil, fmt.Errorf("box client is not configured")
	}
	replayable := req.Body == nil || req.GetBody != nil
	for attempt := 0; attempt <= boxMaxRetries; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		shouldRetry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if !shouldRetry || !replayable || attempt == boxMaxRetries {
			return resp, nil
		}
		retryAfter := resp.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if err := boxWait(ctx, boxRetryDelay(retryAfter, attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("box request failed after %d retries", boxMaxRetries)
}

func boxAPIError(op string, statusCode int) error {
	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("box %s: %w", op, ErrAuth)
	case http.StatusForbidden:
		return fmt.Errorf("box %s: %w", op, ErrPermission)
	case http.StatusNotFound:
		return fmt.Errorf("box %s: %w", op, ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("box %s: %w", op, ErrManagerConflict)
	default:
		return fmt.Errorf("box %s failed with status %d", op, statusCode)
	}
}

func (p *BoxProvider) Connect(ctx context.Context) (bool, error) {
	if p == nil || p.accessToken == "" {
		return false, fmt.Errorf("box connect: %w", ErrAuth)
	}
	req, err := p.newRequest(ctx, http.MethodGet, p.apiBase+"/users/me", nil)
	if err != nil {
		return false, err
	}
	resp, err := p.do(ctx, req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, boxAPIError("connect", resp.StatusCode)
}

type boxPermissions struct {
	CanDownload bool `json:"can_download"`
}

type boxItem struct {
	ID                string          `json:"id"`
	Type              string          `json:"type"`
	Name              string          `json:"name"`
	Size              int64           `json:"size"`
	SHA1              string          `json:"sha1"`
	ModifiedAt        string          `json:"modified_at"`
	ContentModifiedAt string          `json:"content_modified_at"`
	URL               string          `json:"url"`
	Permissions       *boxPermissions `json:"permissions,omitempty"`
}

type boxFolderItemsPage struct {
	Entries    []boxItem `json:"entries"`
	NextMarker string    `json:"next_marker"`
	Limit      int       `json:"limit"`
}

func (p *BoxProvider) ensurePathCache() *boundedStringCache {
	if p.pathCache == nil {
		p.pathCache = newBoundedStringCache(5000)
	}
	return p.pathCache
}

func boxCleanPath(raw string) (string, error) {
	if err := validateStoragePath(raw); err != nil {
		return "", err
	}
	normalized := strings.ReplaceAll(raw, "\\", "/")
	parts := strings.Split(strings.Trim(normalized, "/"), "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		clean = append(clean, part)
	}
	return "/" + strings.Join(clean, "/"), nil
}

func boxChildPath(parentPath, name string) string {
	if parentPath == "" || parentPath == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(parentPath, "/") + "/" + name
}

func parseBoxTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}

func boxItemModTime(item boxItem) time.Time {
	if t := parseBoxTime(item.ContentModifiedAt); !t.IsZero() {
		return t
	}
	return parseBoxTime(item.ModifiedAt)
}

func boxCloudResource(item boxItem, fullPath string) CloudResource {
	hash := ""
	if item.SHA1 != "" {
		hash = "SHA1:" + strings.ToLower(item.SHA1)
	}
	return CloudResource{
		Path:         fullPath,
		Name:         item.Name,
		Size:         item.Size,
		IsDir:        item.Type == "folder",
		Hash:         hash,
		LastModified: boxItemModTime(item),
	}
}

func (p *BoxProvider) doJSON(ctx context.Context, method, rawURL string, body io.Reader, dst any, op string) error {
	req, err := p.newRequest(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	resp, err := p.do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return boxAPIError(op, resp.StatusCode)
	}
	if dst == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBoxJSONBodyBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode box %s response: %w", op, err)
	}
	return nil
}

func (p *BoxProvider) listFolderPage(ctx context.Context, folderID, marker string, limit int) (boxFolderItemsPage, error) {
	if limit <= 0 || limit > boxListPageLimit {
		limit = boxListPageLimit
	}
	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("usemarker", "true")
	params.Set("fields", boxItemFields)
	if marker != "" {
		params.Set("marker", marker)
	}
	endpoint := p.apiBase + "/folders/" + url.PathEscape(folderID) + "/items?" + params.Encode()
	var page boxFolderItemsPage
	if err := p.doJSON(ctx, http.MethodGet, endpoint, nil, &page, "list folder"); err != nil {
		return boxFolderItemsPage{}, err
	}
	return page, nil
}

func (p *BoxProvider) listAllFolderItems(ctx context.Context, folderID string) ([]boxItem, error) {
	var all []boxItem
	marker := ""
	seen := make(map[string]struct{})
	for {
		page, err := p.listFolderPage(ctx, folderID, marker, boxListPageLimit)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Entries...)
		if page.NextMarker == "" {
			return all, nil
		}
		if _, dup := seen[page.NextMarker]; dup {
			return nil, fmt.Errorf("box folder %s returned repeating pagination marker", folderID)
		}
		seen[page.NextMarker] = struct{}{}
		marker = page.NextMarker
	}
}

func (p *BoxProvider) findChildByName(ctx context.Context, parentID, name string) (boxItem, error) {
	items, err := p.listAllFolderItems(ctx, parentID)
	if err != nil {
		return boxItem{}, err
	}
	var match *boxItem
	for i := range items {
		if strings.EqualFold(items[i].Name, name) {
			if match != nil {
				return boxItem{}, fmt.Errorf("box path segment %q: %w", name, ErrAmbiguousPath)
			}
			match = &items[i]
		}
	}
	if match == nil {
		return boxItem{}, fmt.Errorf("box item %q: %w", name, ErrNotFound)
	}
	return *match, nil
}

func (p *BoxProvider) resolveFolderID(ctx context.Context, cleanDirPath string) (string, error) {
	if cleanDirPath == "/" {
		return boxRootFolderID, nil
	}
	cache := p.ensurePathCache()
	if id, ok := cache.Get(strings.ToLower(cleanDirPath)); ok && id != "" {
		return id, nil
	}

	parts := strings.Split(strings.Trim(cleanDirPath, "/"), "/")
	currentID := boxRootFolderID
	currentPath := "/"
	startIdx := 0

	// Walk from the deepest cached ancestor to minimize folder item lookups.
	for i := len(parts) - 1; i >= 0; i-- {
		prefix := "/" + strings.Join(parts[:i+1], "/")
		if id, ok := cache.Get(strings.ToLower(prefix)); ok && id != "" {
			currentID = id
			currentPath = prefix
			startIdx = i + 1
			break
		}
	}

	for _, seg := range parts[startIdx:] {
		child, err := p.findChildByName(ctx, currentID, seg)
		if err != nil {
			return "", err
		}
		if child.Type != "folder" {
			return "", fmt.Errorf("box path %q is not a folder: %w", boxChildPath(currentPath, seg), ErrNotFound)
		}
		currentPath = boxChildPath(currentPath, child.Name)
		currentID = child.ID
		cache.Set(strings.ToLower(currentPath), currentID)
	}
	return currentID, nil
}

func (p *BoxProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if resourceType != "files" {
		return nil, ErrUnsupportedResourceType
	}
	cleanDirPath, err := boxCleanPath(dirPath)
	if err != nil {
		return nil, err
	}
	folderID, err := p.resolveFolderID(ctx, cleanDirPath)
	if err != nil {
		return nil, err
	}
	items, err := p.listAllFolderItems(ctx, folderID)
	if err != nil {
		return nil, err
	}
	cache := p.ensurePathCache()
	resources := make([]CloudResource, 0, len(items))
	for _, item := range items {
		childPath := boxChildPath(cleanDirPath, item.Name)
		if item.Type == "folder" {
			cache.Set(strings.ToLower(childPath), item.ID)
		}
		resources = append(resources, boxCloudResource(item, childPath))
	}
	return resources, nil
}

func (p *BoxProvider) resolvePathItem(ctx context.Context, resourcePath string) (boxItem, string, error) {
	cleanPath, err := boxCleanPath(resourcePath)
	if err != nil {
		return boxItem{}, "", err
	}
	if cleanPath == "/" {
		return boxItem{ID: boxRootFolderID, Type: "folder", Name: "/"}, "/", nil
	}
	parentPath := path.Dir(cleanPath)
	baseName := path.Base(cleanPath)
	parentID, err := p.resolveFolderID(ctx, parentPath)
	if err != nil {
		return boxItem{}, "", err
	}
	item, err := p.findChildByName(ctx, parentID, baseName)
	if err != nil {
		return boxItem{}, "", err
	}
	canonicalPath := boxChildPath(parentPath, item.Name)
	if item.Type == "folder" {
		p.ensurePathCache().Set(strings.ToLower(canonicalPath), item.ID)
	}
	return item, canonicalPath, nil
}

func (p *BoxProvider) resolveFileID(ctx context.Context, filePath string) (string, error) {
	item, canonicalPath, err := p.resolvePathItem(ctx, filePath)
	if err != nil {
		return "", err
	}
	if item.Type == "folder" {
		return "", fmt.Errorf("box path %q is a folder: %w", canonicalPath, ErrNotFound)
	}
	if item.Type != "file" {
		return "", fmt.Errorf("box path %q has non-file type %q: %w", canonicalPath, item.Type, ErrPermanentTransfer)
	}
	return item.ID, nil
}

func (p *BoxProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	if resourceType != "files" {
		return CloudResource{}, ErrUnsupportedResourceType
	}
	cleanPath, err := boxCleanPath(resourcePath)
	if err != nil {
		return CloudResource{}, err
	}
	if cleanPath == "/" {
		var root boxItem
		if err := p.doJSON(ctx, http.MethodGet, p.apiBase+"/folders/"+boxRootFolderID+"?fields="+url.QueryEscape(boxItemFields), nil, &root, "inspect root"); err != nil {
			return CloudResource{}, err
		}
		return CloudResource{Path: "/", Name: "/", IsDir: true, LastModified: parseBoxTime(root.ModifiedAt)}, nil
	}

	item, canonicalPath, err := p.resolvePathItem(ctx, cleanPath)
	if err != nil {
		return CloudResource{}, err
	}
	return boxCloudResource(item, canonicalPath), nil
}

func isBoxLocalTestingURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (p *BoxProvider) checkDownloadRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("box download: stopped after 10 redirects")
	}
	// Strip Box OAuth bearer credentials before following pre-signed redirects
	// (e.g. dl.boxcloud.com) so tokens never leak to external download hosts.
	req.Header.Del("Authorization")

	if isBoxLocalTestingURL(p.apiBase) && isBoxLocalTestingURL(req.URL.String()) {
		return nil
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("box redirect scheme %q rejected: HTTPS required", req.URL.Scheme)
	}
	if err := ValidateEgressHostContext(req.Context(), req.URL.Hostname()); err != nil {
		return fmt.Errorf("box redirect host rejected: %w", err)
	}
	return nil
}

func (p *BoxProvider) downloadHTTPClient() *http.Client {
	if p == nil || p.httpClient == nil {
		return &http.Client{CheckRedirect: p.checkDownloadRedirect}
	}
	client := *p.httpClient
	client.CheckRedirect = p.checkDownloadRedirect
	return &client
}

func (p *BoxProvider) openDownloadResponse(ctx context.Context, fileID, rangeHeader string) (*http.Response, error) {
	endpoint := p.apiBase + "/files/" + url.PathEscape(fileID) + "/content"
	req, err := p.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return p.doWithClient(ctx, p.downloadHTTPClient(), req)
}

func (p *BoxProvider) streamDownloadByID(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := p.openDownloadResponse(ctx, fileID, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, boxAPIError("download", resp.StatusCode)
	}
	return resp.Body, nil
}

func (p *BoxProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, ErrUnsupportedResourceType
	}
	fileID, err := p.resolveFileID(ctx, filePath)
	if err != nil {
		return nil, err
	}
	return p.streamDownloadByID(ctx, fileID)
}

// StreamDownloadRange implements RangeDownloader for BoxProvider.
func (p *BoxProvider) StreamDownloadRange(ctx context.Context, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, ErrUnsupportedResourceType
	}
	rangeHeader, err := FormatByteRangeHeader(offset, length)
	if err != nil {
		return nil, err
	}
	fileID, err := p.resolveFileID(ctx, filePath)
	if err != nil {
		return nil, err
	}
	resp, err := p.openDownloadResponse(ctx, fileID, rangeHeader)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, boxAPIError("range download", resp.StatusCode)
	}
	return ValidateHTTPRangeResponse(resp, offset, length)
}

func (p *BoxProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return fmt.Errorf("box upload not implemented: %w", ErrPermanentTransfer)
}

func (p *BoxProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	return fmt.Errorf("box chunked upload not implemented: %w", ErrPermanentTransfer)
}

func (p *BoxProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	return fmt.Errorf("box mkdir not implemented: %w", ErrPermanentTransfer)
}

func (p *BoxProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return fmt.Errorf("box rename not implemented: %w", ErrPermanentTransfer)
}

func (p *BoxProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return fmt.Errorf("box delete not implemented: %w", ErrPermanentTransfer)
}

func (p *BoxProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, 0, nil
		}
		return false, 0, err
	}
	return true, res.Size, nil
}

func (p *BoxProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return "", err
	}
	if res.Hash == "" {
		return "", ErrChecksumNotAvailable
	}
	return res.Hash, nil
}

func (p *BoxProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	return fmt.Errorf("box create parent directories not implemented: %w", ErrPermanentTransfer)
}
