package storage

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBoxAPIBase          = "https://api.box.com/2.0"
	defaultBoxUploadBase       = "https://upload.box.com/api/2.0"
	boxRootFolderID            = "0"
	boxListPageLimit           = 1000
	boxMaxRetries              = 3
	boxMaxInlineRetryWait      = 10 * time.Second
	boxMaxCommitPolls          = 10
	boxDefaultThumbnailMaxWait = 3 * time.Second
	boxItemFields              = "id,type,name,size,sha1,modified_at,content_modified_at,permissions,url,parent"
	maxBoxJSONBodyBytes        = 16 << 20
	boxMaxPartSizeBytes        = 256 << 20
	boxDirectUploadMaxBytes    = int64(50_000_000)
	boxEmptySHA1Hex            = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
)

// BoxProvider accesses a Box account via the Box Content API v2.
type BoxProvider struct {
	catalogueTraits
	accessToken      string
	apiBase          string
	uploadBase       string
	httpClient       *http.Client
	pathCache        *boundedStringCache
	thumbnailMaxWait time.Duration
}

var (
	_ StorageProvider  = (*BoxProvider)(nil)
	_ RangeDownloader  = (*BoxProvider)(nil)
	_ TransferUploader = (*BoxProvider)(nil)
)

// NewBoxProvider constructs a BoxProvider using the given OAuth2 access token.
func NewBoxProvider(token string) (*BoxProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("box provider requires an oauth token: %w", ErrAuth)
	}
	client := newFixedEndpointClient(providerHTTPOptions{
		BaseTransport: compactProviderTransport(),
		WrapTransport: newUserAgentTransport,
	})
	return newBoxProvider(token, defaultBoxAPIBase, defaultBoxUploadBase, client), nil
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

func parseBoxRetryAfter(retryAfter string) (time.Duration, bool) {
	trimmed := strings.TrimSpace(retryAfter)
	if trimmed == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(trimmed); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if retryAt, err := http.ParseTime(trimmed); err == nil {
		if delay := time.Until(retryAt); delay > 0 {
			return delay, true
		}
		return 0, true
	}
	return 0, false
}

func boxRetryWait(retryAfter string, attempt int) (time.Duration, bool) {
	if delay, ok := parseBoxRetryAfter(retryAfter); ok {
		return delay, true
	}
	return 200 * time.Millisecond * (1 << attempt), false
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
		delay, explicit := boxRetryWait(retryAfter, attempt)
		if explicit && delay > boxMaxInlineRetryWait {
			// Hand long server-requested backoffs back to the queue instead of
			// sleeping inline on a worker goroutine.
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if err := boxWait(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("box request failed after %d retries", boxMaxRetries)
}

func boxAPIError(op string, statusCode int) error {
	return boxAPIErrorWithCode(op, statusCode, "")
}

func boxAPIErrorWithCode(op string, statusCode int, code string) error {
	prefix := "box " + op
	if code != "" {
		prefix = fmt.Sprintf("box %s (%s)", op, code)
	}
	switch statusCode {
	case http.StatusBadRequest:
		return fmt.Errorf("%s rejected with HTTP 400: %w", prefix, ErrPermanentTransfer)
	case http.StatusUnauthorized:
		return fmt.Errorf("%s: %w", prefix, ErrAuth)
	case http.StatusForbidden:
		return fmt.Errorf("%s: %w", prefix, ErrPermission)
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", prefix, ErrNotFound)
	case http.StatusUnsupportedMediaType:
		return fmt.Errorf("%s: %w", prefix, ErrUnsupportedMedia)
	case http.StatusConflict:
		return fmt.Errorf("%s: %w", prefix, ErrManagerConflict)
	default:
		return fmt.Errorf("%s failed with HTTP %d", prefix, statusCode)
	}
}

func boxResponseError(op string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("box %s failed: nil response", op)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		after, ok := parseBoxRetryAfter(resp.Header.Get("Retry-After"))
		if !ok {
			after = time.Second
		}
		return fmt.Errorf("box %s: %w", op, &RetryAfterError{After: after})
	}
	var code string
	if resp.Body != nil {
		var body struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err == nil {
			if isSafeBoxErrorCode(body.Code) {
				code = body.Code
			}
		}
	}
	return boxAPIErrorWithCode(op, resp.StatusCode, code)
}

func isSafeBoxErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
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
	return false, boxResponseError("connect", resp)
}

type boxPermissions struct {
	CanDownload bool `json:"can_download"`
}

type boxItemRef struct {
	ID   string `json:"id"`
	Type string `json:"type,omitempty"`
	Name string `json:"name,omitempty"`
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
	Parent            *boxItemRef     `json:"parent,omitempty"`
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

func boxNonTransferableKind(item boxItem) string {
	switch item.Type {
	case "web_link":
		return boxNonTransferableWebLink
	case "file":
		lowerName := strings.ToLower(item.Name)
		switch {
		case strings.HasSuffix(lowerName, ".boxnote"):
			return boxNonTransferableNote
		case strings.HasSuffix(lowerName, ".boxcanvas"):
			return boxNonTransferableCanvas
		}
	}
	return ""
}

func boxCloudResource(item boxItem, fullPath string) CloudResource {
	hash := ""
	if item.SHA1 != "" {
		hash = "SHA1:" + strings.ToLower(item.SHA1)
	}
	var metadata FileMetadata
	if kind := boxNonTransferableKind(item); kind != "" {
		metadata.CustomProps = map[string]string{
			boxNonTransferablePropKey: kind,
		}
	}
	return CloudResource{
		Path:         fullPath,
		Name:         item.Name,
		Size:         item.Size,
		IsDir:        item.Type == "folder",
		Hash:         hash,
		LastModified: boxItemModTime(item),
		Metadata:     metadata,
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
		return boxResponseError(op, resp)
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

func (p *BoxProvider) deepestCachedAncestor(cleanDirPath string) (currentID, currentPath string, remaining []string) {
	cache := p.ensurePathCache()
	parts := strings.Split(strings.Trim(cleanDirPath, "/"), "/")
	// Walk from the deepest cached ancestor to minimize folder API calls.
	for i := len(parts) - 1; i >= 0; i-- {
		prefix := "/" + strings.Join(parts[:i+1], "/")
		if id, ok := cache.Get(strings.ToLower(prefix)); ok && id != "" {
			return id, prefix, parts[i+1:]
		}
	}
	return boxRootFolderID, "/", parts
}

func (p *BoxProvider) resolveFolderID(ctx context.Context, cleanDirPath string) (string, error) {
	if cleanDirPath == "/" {
		return boxRootFolderID, nil
	}
	cache := p.ensurePathCache()
	currentID, currentPath, remaining := p.deepestCachedAncestor(cleanDirPath)

	for _, seg := range remaining {
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
	if reason, skip := boxCloudResource(item, canonicalPath).BoxSkipReason(); skip {
		return "", fmt.Errorf("box path %q (%s): %w", canonicalPath, reason, ErrPermanentTransfer)
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
		err := boxResponseError("download", resp)
		resp.Body.Close()
		return nil, err
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
		err := boxResponseError("range download", resp)
		resp.Body.Close()
		return nil, err
	}
	return ValidateHTTPRangeResponse(resp, offset, length)
}

type boxUploadAttributes struct {
	Name              string           `json:"name"`
	Parent            *boxUploadParent `json:"parent,omitempty"`
	ContentModifiedAt string           `json:"content_modified_at,omitempty"`
}

type boxUploadParent struct {
	ID string `json:"id"`
}

type boxUploadResponse struct {
	Entries []boxItem `json:"entries"`
}

func parseBoxSHA1Checksum(checksum string) (string, bool) {
	algo, val := ParseHashString(checksum)
	if algo != "SHA1" || len(val) != 40 {
		return "", false
	}
	if _, err := hex.DecodeString(val); err != nil {
		return "", false
	}
	return val, true
}

func boxUploadParentAndName(filePath string) (string, string, string, error) {
	cleanPath, err := boxCleanPath(filePath)
	if err != nil {
		return "", "", "", err
	}
	if cleanPath == "/" {
		return "", "", "", fmt.Errorf("box upload target cannot be root: %w", ErrPermanentTransfer)
	}
	return cleanPath, path.Dir(cleanPath), path.Base(cleanPath), nil
}

func (p *BoxProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	_, err := p.StreamUploadWithOptions(ctx, resourceType, filePath, stream, size, UploadOptions{})
	return err
}

func (p *BoxProvider) StreamUploadWithOptions(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, opts UploadOptions) (UploadResult, error) {
	if resourceType != "files" {
		return UploadResult{}, ErrUnsupportedResourceType
	}
	cleanPath, parentPath, fileName, err := boxUploadParentAndName(filePath)
	if err != nil {
		return UploadResult{}, err
	}
	if size > boxDirectUploadMaxBytes {
		return p.StreamUploadChunkedWithOptions(ctx, resourceType, cleanPath, stream, size, nil, opts)
	}
	if err := p.CreateParentDirectories(ctx, resourceType, cleanPath); err != nil {
		return UploadResult{}, err
	}
	parentID, err := p.resolveFolderID(ctx, parentPath)
	if err != nil {
		return UploadResult{}, err
	}

	var targetFileID string
	if opts.Overwrite {
		existing, findErr := p.findChildByName(ctx, parentID, fileName)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			return UploadResult{}, findErr
		}
		if findErr == nil {
			if existing.Type != "file" {
				return UploadResult{}, fmt.Errorf("box upload target %q conflicts with %s: %w", cleanPath, existing.Type, ErrManagerConflict)
			}
			targetFileID = existing.ID
		}
	}
	return p.uploadDirectByID(ctx, parentID, targetFileID, fileName, stream, size, opts)
}

func (p *BoxProvider) uploadDirectByID(ctx context.Context, parentID, existingFileID, fileName string, stream io.Reader, size int64, opts UploadOptions) (UploadResult, error) {
	attrs := boxUploadAttributes{
		Name: fileName,
	}
	endpoint := p.uploadBase + "/files/content"
	if existingFileID != "" {
		endpoint = p.uploadBase + "/files/" + url.PathEscape(existingFileID) + "/content"
	} else {
		attrs.Parent = &boxUploadParent{ID: parentID}
	}
	if !opts.Metadata.ModifiedTime.IsZero() {
		attrs.ContentModifiedAt = opts.Metadata.ModifiedTime.UTC().Format(time.RFC3339)
	}
	attrBytes, err := json.Marshal(attrs)
	if err != nil {
		return UploadResult{}, err
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	errCh := make(chan error, 1)
	go func() {
		var writeErr error
		defer func() {
			if writeErr != nil {
				_ = pw.CloseWithError(writeErr)
			} else {
				_ = pw.Close()
			}
			errCh <- writeErr
		}()

		if err := mw.WriteField("attributes", string(attrBytes)); err != nil {
			writeErr = err
			return
		}
		part, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			writeErr = err
			return
		}
		n, err := io.Copy(part, stream)
		if err != nil {
			writeErr = err
			return
		}
		if size >= 0 && n != size {
			writeErr = fmt.Errorf("box upload stream size mismatch: got %d bytes, expected %d: %w", n, size, io.ErrUnexpectedEOF)
			return
		}
		if err := mw.Close(); err != nil {
			writeErr = err
			return
		}
	}()

	req, err := p.newRequest(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.Close()
		<-errCh
		return UploadResult{}, err
	}
	req.Header.Set("Content-Type", contentType)
	if sha1Hex, ok := parseBoxSHA1Checksum(opts.Checksum); ok {
		req.Header.Set("content-md5", sha1Hex)
	} else if size == 0 && opts.Checksum == "" {
		req.Header.Set("content-md5", boxEmptySHA1Hex)
	}

	resp, doErr := p.do(ctx, req)
	if doErr != nil {
		_ = pr.Close()
		if writeErr := <-errCh; writeErr != nil {
			return UploadResult{}, writeErr
		}
		return UploadResult{}, doErr
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = pr.Close()
		<-errCh
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return UploadResult{}, boxResponseError("upload", resp)
	}

	var uploaded boxUploadResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBoxJSONBodyBytes)).Decode(&uploaded); err != nil {
		<-errCh
		return UploadResult{}, fmt.Errorf("decode box upload response: %w", err)
	}
	if writeErr := <-errCh; writeErr != nil {
		return UploadResult{}, writeErr
	}
	if len(uploaded.Entries) == 0 || uploaded.Entries[0].ID == "" {
		return UploadResult{}, fmt.Errorf("box upload response missing file entry")
	}
	return UploadResult{Receipt: UploadReceipt{TargetResourceID: uploaded.Entries[0].ID}}, nil
}

func (p *BoxProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	_, err := p.StreamUploadChunkedWithOptions(ctx, resourceType, filePath, stream, size, progressChan, UploadOptions{})
	return err
}

type boxUploadSession struct {
	ID               string `json:"id"`
	PartSize         int64  `json:"part_size"`
	TotalParts       int    `json:"total_parts"`
	SessionEndpoints struct {
		UploadPart string `json:"upload_part"`
		Commit     string `json:"commit"`
		Abort      string `json:"abort"`
	} `json:"session_endpoints"`
}

type boxUploadPart struct {
	PartID string `json:"part_id"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA1   string `json:"sha1"`
}

type boxCommitAttributes struct {
	ContentModifiedAt string `json:"content_modified_at,omitempty"`
}

func (p *BoxProvider) validateSessionEndpoint(ctx context.Context, rawURL string) error {
	if isBoxLocalTestingURL(p.uploadBase) && isBoxLocalTestingURL(rawURL) {
		return nil
	}
	u, err := parseEgressURL(rawURL)
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("box upload session endpoint scheme %q rejected: HTTPS required", u.Scheme)
	}
	return validateEgressURLContext(ctx, rawURL)
}

func (p *BoxProvider) StreamUploadChunkedWithOptions(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64, opts UploadOptions) (UploadResult, error) {
	if resourceType != "files" {
		return UploadResult{}, ErrUnsupportedResourceType
	}
	cleanPath, parentPath, fileName, err := boxUploadParentAndName(filePath)
	if err != nil {
		return UploadResult{}, err
	}
	if size < 0 {
		return UploadResult{}, fmt.Errorf("box chunked upload requires non-negative size, got %d: %w", size, ErrPermanentTransfer)
	}
	if size == 0 {
		return p.StreamUploadWithOptions(ctx, resourceType, cleanPath, &ProgressReader{Reader: stream, ProgressChan: progressChan}, 0, opts)
	}
	if err := p.CreateParentDirectories(ctx, resourceType, cleanPath); err != nil {
		return UploadResult{}, err
	}
	parentID, err := p.resolveFolderID(ctx, parentPath)
	if err != nil {
		return UploadResult{}, err
	}
	return p.uploadChunkedByID(ctx, parentID, "", fileName, stream, size, progressChan, opts)
}

func (p *BoxProvider) uploadChunkedByID(ctx context.Context, parentID, existingFileID, fileName string, stream io.Reader, size int64, progressChan chan<- int64, opts UploadOptions) (UploadResult, error) {
	if size < 0 {
		return UploadResult{}, fmt.Errorf("box chunked upload requires non-negative size, got %d: %w", size, ErrPermanentTransfer)
	}
	if size == 0 {
		return p.uploadDirectByID(ctx, parentID, existingFileID, fileName, &ProgressReader{Reader: stream, ProgressChan: progressChan}, 0, opts)
	}

	var (
		session boxUploadSession
		err     error
	)
	if existingFileID != "" {
		session, err = p.createNewVersionUploadSession(ctx, existingFileID, fileName, size)
	} else {
		session, err = p.createUploadSession(ctx, parentID, fileName, size, opts.Overwrite)
	}
	if err != nil {
		return UploadResult{}, err
	}

	uploadPartURL := session.SessionEndpoints.UploadPart
	if uploadPartURL == "" {
		uploadPartURL = p.uploadBase + "/files/upload_sessions/" + url.PathEscape(session.ID)
	}
	commitURL := session.SessionEndpoints.Commit
	if commitURL == "" {
		commitURL = p.uploadBase + "/files/upload_sessions/" + url.PathEscape(session.ID) + "/commit"
	}
	abortURL := session.SessionEndpoints.Abort
	if abortURL == "" {
		abortURL = p.uploadBase + "/files/upload_sessions/" + url.PathEscape(session.ID)
	}

	for _, ep := range []string{uploadPartURL, commitURL, abortURL} {
		if err := p.validateSessionEndpoint(ctx, ep); err != nil {
			return UploadResult{}, err
		}
	}

	committed := false
	defer func() {
		if !committed {
			p.abortUploadSession(ctx, abortURL)
		}
	}()

	bufLen := session.PartSize
	if size < bufLen {
		bufLen = size
	}
	buf := make([]byte, bufLen)
	fileHasher := sha1.New()
	parts := make([]boxUploadPart, 0, (size+session.PartSize-1)/session.PartSize)

	var offset int64
	for offset < size {
		remaining := size - offset
		wantLen := session.PartSize
		if remaining < wantLen {
			wantLen = remaining
		}
		if _, err := io.ReadFull(stream, buf[:wantLen]); err != nil {
			return UploadResult{}, fmt.Errorf("read box upload part at offset %d: %w", offset, err)
		}

		partData := buf[:wantLen]
		partSum := sha1.Sum(partData)
		_, _ = fileHasher.Write(partData)

		partResp, err := p.uploadSessionPart(ctx, uploadPartURL, partData, offset, size, partSum[:])
		if err != nil {
			return UploadResult{}, err
		}
		parts = append(parts, partResp)
		offset += wantLen

		if progressChan != nil {
			select {
			case progressChan <- wantLen:
			default:
			}
		}
	}

	wholeSum := fileHasher.Sum(nil)
	if expectedHex, ok := parseBoxSHA1Checksum(opts.Checksum); ok {
		gotHex := hex.EncodeToString(wholeSum)
		if gotHex != expectedHex {
			return UploadResult{}, fmt.Errorf("box chunked upload sha1 mismatch: got %s, expected %s", gotHex, expectedHex)
		}
	}

	result, err := p.commitUploadSession(ctx, commitURL, parts, wholeSum, opts.Metadata)
	if err != nil {
		return UploadResult{}, err
	}
	committed = true
	return result, nil
}

func (p *BoxProvider) createUploadSession(ctx context.Context, folderID, fileName string, size int64, overwrite bool) (boxUploadSession, error) {
	body, err := json.Marshal(struct {
		FolderID string `json:"folder_id"`
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}{
		FolderID: folderID,
		FileName: fileName,
		FileSize: size,
	})
	if err != nil {
		return boxUploadSession{}, err
	}
	session, conflictBody, statusCode, err := p.postUploadSession(ctx, p.uploadBase+"/files/upload_sessions", body, "create upload session")
	if err == nil {
		return session, nil
	}
	if statusCode != http.StatusConflict || !overwrite {
		return boxUploadSession{}, err
	}

	_, conflict := parseBoxConflict(conflictBody)
	if conflict == nil || conflict.ID == "" {
		existing, findErr := p.findChildByName(ctx, folderID, fileName)
		if findErr != nil {
			return boxUploadSession{}, findErr
		}
		conflict = &existing
	}
	if conflict.Type != "file" {
		return boxUploadSession{}, fmt.Errorf("box upload target %q conflicts with %s: %w", fileName, conflict.Type, ErrManagerConflict)
	}
	return p.createNewVersionUploadSession(ctx, conflict.ID, fileName, size)
}

func (p *BoxProvider) createNewVersionUploadSession(ctx context.Context, fileID, fileName string, size int64) (boxUploadSession, error) {
	body, err := json.Marshal(struct {
		FileName string `json:"file_name"`
		FileSize int64  `json:"file_size"`
	}{
		FileName: fileName,
		FileSize: size,
	})
	if err != nil {
		return boxUploadSession{}, err
	}
	endpoint := p.uploadBase + "/files/" + url.PathEscape(fileID) + "/upload_sessions"
	session, _, _, err := p.postUploadSession(ctx, endpoint, body, "create new version upload session")
	return session, err
}

func (p *BoxProvider) postUploadSession(ctx context.Context, endpoint string, body []byte, op string) (boxUploadSession, []byte, int, error) {
	req, err := p.newRequest(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return boxUploadSession{}, nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.do(ctx, req)
	if err != nil {
		return boxUploadSession{}, nil, 0, err
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBoxJSONBodyBytes))
	if readErr != nil {
		return boxUploadSession{}, nil, resp.StatusCode, fmt.Errorf("read box %s response: %w", op, readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return boxUploadSession{}, bodyBytes, resp.StatusCode, boxResponseError(op, resp)
	}

	var session boxUploadSession
	if err := json.Unmarshal(bodyBytes, &session); err != nil {
		return boxUploadSession{}, nil, resp.StatusCode, fmt.Errorf("decode box %s: %w", op, err)
	}
	if session.ID == "" || session.PartSize <= 0 || session.PartSize > boxMaxPartSizeBytes {
		return boxUploadSession{}, nil, resp.StatusCode, fmt.Errorf("box upload session returned invalid id or part_size (%d)", session.PartSize)
	}
	return session, nil, resp.StatusCode, nil
}

func (p *BoxProvider) uploadSessionPart(ctx context.Context, uploadPartURL string, data []byte, offset, total int64, partSHA1 []byte) (boxUploadPart, error) {
	req, err := p.newRequest(ctx, http.MethodPut, uploadPartURL, bytes.NewReader(data))
	if err != nil {
		return boxUploadPart{}, err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(len(data))-1, total))
	req.Header.Set("Digest", "sha="+base64.StdEncoding.EncodeToString(partSHA1))

	resp, err := p.do(ctx, req)
	if err != nil {
		return boxUploadPart{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return boxUploadPart{}, boxResponseError("upload session part", resp)
	}

	var partWrapper struct {
		Part boxUploadPart `json:"part"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBoxJSONBodyBytes)).Decode(&partWrapper); err != nil {
		return boxUploadPart{}, fmt.Errorf("decode box upload part response: %w", err)
	}
	if partWrapper.Part.PartID == "" {
		return boxUploadPart{}, fmt.Errorf("box upload part response missing part_id")
	}
	return partWrapper.Part, nil
}

func (p *BoxProvider) commitUploadSession(ctx context.Context, commitURL string, parts []boxUploadPart, wholeSHA1 []byte, meta FileMetadata) (UploadResult, error) {
	payload := struct {
		Parts      []boxUploadPart      `json:"parts"`
		Attributes *boxCommitAttributes `json:"attributes,omitempty"`
	}{
		Parts: parts,
	}
	if !meta.ModifiedTime.IsZero() {
		payload.Attributes = &boxCommitAttributes{
			ContentModifiedAt: meta.ModifiedTime.UTC().Format(time.RFC3339),
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return UploadResult{}, err
	}
	digestHeader := "sha=" + base64.StdEncoding.EncodeToString(wholeSHA1)

	for poll := 0; poll < boxMaxCommitPolls; poll++ {
		req, err := p.newRequest(ctx, http.MethodPost, commitURL, bytes.NewReader(body))
		if err != nil {
			return UploadResult{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Digest", digestHeader)

		resp, err := p.do(ctx, req)
		if err != nil {
			return UploadResult{}, err
		}

		if resp.StatusCode == http.StatusAccepted {
			retryAfter := resp.Header.Get("Retry-After")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()

			delay, ok := parseBoxRetryAfter(retryAfter)
			if !ok {
				delay = time.Second
			}
			if delay > boxMaxInlineRetryWait {
				return UploadResult{}, fmt.Errorf("box commit upload session: %w", &RetryAfterError{After: delay})
			}
			if err := boxWait(ctx, delay); err != nil {
				return UploadResult{}, err
			}
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			return UploadResult{}, boxResponseError("commit upload session", resp)
		}

		var uploaded boxUploadResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBoxJSONBodyBytes)).Decode(&uploaded); err != nil {
			return UploadResult{}, fmt.Errorf("decode box commit response: %w", err)
		}
		if len(uploaded.Entries) == 0 || uploaded.Entries[0].ID == "" {
			return UploadResult{}, fmt.Errorf("box commit response missing file entry")
		}
		return UploadResult{Receipt: UploadReceipt{TargetResourceID: uploaded.Entries[0].ID}}, nil
	}
	return UploadResult{}, fmt.Errorf("box upload commit remained 202 Accepted after %d polls", boxMaxCommitPolls)
}

func (p *BoxProvider) abortUploadSession(ctx context.Context, abortURL string) {
	abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()

	req, err := p.newRequest(abortCtx, http.MethodDelete, abortURL, nil)
	if err != nil {
		return
	}
	resp, err := p.do(abortCtx, req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func (p *BoxProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	cleanDir, err := boxCleanPath(dirPath)
	if err != nil {
		return err
	}
	if cleanDir == "/" {
		return nil
	}

	cache := p.ensurePathCache()
	currentID, currentPath, remaining := p.deepestCachedAncestor(cleanDir)

	for _, seg := range remaining {
		created, err := p.createFolderInParent(ctx, currentID, seg)
		if err != nil {
			return err
		}
		currentID = created.ID
		currentPath = boxChildPath(currentPath, created.Name)
		cache.Set(strings.ToLower(currentPath), currentID)
	}
	return nil
}

func (p *BoxProvider) createFolderInParent(ctx context.Context, parentID, name string) (boxItem, error) {
	return p.createFolderInParentWithConflict(ctx, parentID, name, true)
}

func (p *BoxProvider) createFolderInParentWithConflict(ctx context.Context, parentID, name string, reuseExistingFolder bool) (boxItem, error) {
	payload, err := json.Marshal(struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}{
		Name: name,
		Parent: struct {
			ID string `json:"id"`
		}{ID: parentID},
	})
	if err != nil {
		return boxItem{}, err
	}

	req, err := p.newRequest(ctx, http.MethodPost, p.apiBase+"/folders", bytes.NewReader(payload))
	if err != nil {
		return boxItem{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.do(ctx, req)
	if err != nil {
		return boxItem{}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBoxJSONBodyBytes))
	if err != nil {
		return boxItem{}, fmt.Errorf("read box create folder response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var created boxItem
		if err := json.Unmarshal(bodyBytes, &created); err != nil {
			return boxItem{}, fmt.Errorf("decode box create folder response: %w", err)
		}
		if created.ID == "" {
			return boxItem{}, fmt.Errorf("box create folder response missing id")
		}
		if created.Name == "" {
			created.Name = name
		}
		return created, nil
	}

	if resp.StatusCode == http.StatusConflict {
		if !reuseExistingFolder {
			return boxItem{}, boxAPIError("create folder", http.StatusConflict)
		}
		_, conflict := parseBoxConflict(bodyBytes)
		if conflict == nil {
			existing, lookupErr := p.findChildByName(ctx, parentID, name)
			if lookupErr != nil {
				return boxItem{}, boxAPIError("create folder", http.StatusConflict)
			}
			conflict = &existing
		}
		if conflict.Type != "folder" {
			return boxItem{}, fmt.Errorf("box path %q conflicts with %s: %w", name, conflict.Type, ErrManagerConflict)
		}
		if conflict.ID == "" {
			return boxItem{}, boxAPIError("create folder", http.StatusConflict)
		}
		if conflict.Name == "" {
			conflict.Name = name
		}
		return *conflict, nil
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return boxItem{}, boxResponseError("create folder", resp)
	}
	return boxItem{}, boxAPIError("create folder", resp.StatusCode)
}

func parseBoxConflict(body []byte) (string, *boxItem) {
	var errResp struct {
		Code        string `json:"code"`
		ContextInfo struct {
			Conflicts json.RawMessage `json:"conflicts"`
		} `json:"context_info"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return "", nil
	}
	raw := bytes.TrimSpace(errResp.ContextInfo.Conflicts)
	if len(raw) == 0 || string(raw) == "null" {
		return errResp.Code, nil
	}
	if raw[0] == '[' {
		var items []boxItem
		if err := json.Unmarshal(raw, &items); err == nil && len(items) > 0 {
			return errResp.Code, &items[0]
		}
		return errResp.Code, nil
	}
	var item boxItem
	if err := json.Unmarshal(raw, &item); err == nil && item.ID != "" {
		return errResp.Code, &item
	}
	return errResp.Code, nil
}

func (p *BoxProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	if p == nil {
		return ErrNotConnected
	}
	cleanOld, err := boxCleanPath(oldPath)
	if err != nil {
		return err
	}
	cleanNew, err := boxCleanPath(newPath)
	if err != nil {
		return err
	}
	if cleanOld == "/" || cleanNew == "/" {
		return fmt.Errorf("box rename root path not allowed: %w", ErrPathEscapesRoot)
	}

	item, canonicalOld, err := p.resolvePathItem(ctx, cleanOld)
	if err != nil {
		return err
	}
	if item.Type != "file" && item.Type != "folder" {
		return fmt.Errorf("box rename unsupported item type %q: %w", item.Type, ErrPermanentTransfer)
	}
	if cleanOld == cleanNew {
		return nil
	}

	newParentPath := path.Dir(cleanNew)
	if newParentPath != "/" {
		if err := p.CreateDirectory(ctx, resourceType, newParentPath); err != nil {
			return err
		}
	}
	destParentID, err := p.resolveFolderID(ctx, newParentPath)
	if err != nil {
		return err
	}

	if err := p.updateBoxItem(ctx, item.Type, item.ID, path.Base(cleanNew), destParentID); err != nil {
		return err
	}
	if item.Type == "folder" {
		p.invalidateFolderCache(item.ID, canonicalOld)
		p.invalidateFolderCache(item.ID, cleanNew)
	}
	return nil
}

func (p *BoxProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	if p == nil {
		return ErrNotConnected
	}
	cleanPath, err := boxCleanPath(filePath)
	if err != nil {
		return err
	}
	if cleanPath == "/" {
		return fmt.Errorf("box delete root path not allowed: %w", ErrPathEscapesRoot)
	}
	item, canonicalPath, err := p.resolvePathItem(ctx, cleanPath)
	if err != nil {
		return err
	}
	switch item.Type {
	case "file":
		return p.deleteBoxFileByID(ctx, item.ID)
	case "folder":
		if err := p.deleteBoxFolderByID(ctx, item.ID, true); err != nil {
			return err
		}
		p.invalidateFolderCache(item.ID, canonicalPath)
		return nil
	default:
		return fmt.Errorf("box delete unsupported item type %q: %w", item.Type, ErrPermanentTransfer)
	}
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
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	cleanPath, err := boxCleanPath(filePath)
	if err != nil {
		return err
	}
	if cleanPath == "/" {
		return nil
	}
	return p.CreateDirectory(ctx, resourceType, path.Dir(cleanPath))
}
