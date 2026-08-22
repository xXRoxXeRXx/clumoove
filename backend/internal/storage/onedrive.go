package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
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

const (
	defaultOneDriveAPIBase          = "https://graph.microsoft.com/v1.0/me/drive"
	oneDriveSimpleUploadLimit int64 = 4 * 1024 * 1024
	oneDriveUploadChunkSize         = 10 * 1024 * 1024 // Graph requires multiples of 320 KiB.
	sharedShortcutCacheTTL          = 5 * time.Minute
	sharedShortcutErrorTTL          = 15 * time.Second
)

// OneDriveProvider accesses a personal OneDrive through Microsoft Graph.
type OneDriveProvider struct {
	accessToken string
	apiBase     string
	httpClient  *http.Client
	// shortcutCache maps a top-level folder name to its remote-drive item URL.
	// An empty string is cached for ordinary folders, preventing one Graph
	// shortcut probe per indexed/downloaded file.
	shortcutCache sync.Map
	// confirmedDirectories caches directories created or inspected by this
	// short-lived provider. It avoids a Graph GET for every parent component of
	// every upload without retaining paths beyond the task lifetime.
	confirmedDirectories sync.Map
}

// sharedShortcutResolutionCache shares only the resolved remote Graph item URL
// between short-lived provider instances. Processor workers intentionally create
// a provider per task so decrypted credentials are not retained, but doing that
// must not make every parallel file transfer re-inspect the same shortcut.
// Cache keys contain a SHA-256 digest of the token, never the token itself.
var sharedShortcutResolutionCache = struct {
	sync.Mutex
	entries map[string]*oneDriveShortcutCacheEntry
}{entries: make(map[string]*oneDriveShortcutCacheEntry)}

type oneDriveShortcutCacheEntry struct {
	remotePrefix string
	err          error
	expiresAt    time.Time
	ready        chan struct{}
}

var _ StorageProvider = (*OneDriveProvider)(nil)

func NewOneDriveProvider(token string) (*OneDriveProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("onedrive provider requires an oauth token: %w", ErrAuth)
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
	return newOneDriveProvider(token, defaultOneDriveAPIBase, &http.Client{Transport: transport}), nil
}

// newOneDriveProvider is intentionally package-private so HTTP behavior can be
// tested against a local Graph fixture without making the endpoint configurable.
func newOneDriveProvider(token, apiBase string, client *http.Client) *OneDriveProvider {
	return &OneDriveProvider{accessToken: token, apiBase: strings.TrimRight(apiBase, "/"), httpClient: client}
}

func (p *OneDriveProvider) Close() error {
	if p.httpClient != nil {
		p.httpClient.CloseIdleConnections()
	}
	return nil
}

func oneDrivePath(filePath string) (string, error) {
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	parts := strings.Split(strings.Trim(filePath, "/"), "/")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", ErrPathEscapesRoot
		}
		clean = append(clean, part)
	}
	return "/" + strings.Join(clean, "/"), nil
}

func oneDriveEscapedPath(filePath string) (string, error) {
	clean, err := oneDrivePath(filePath)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return "", nil
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	for i, part := range parts {
		parts[i] = oneDriveEscapeSegment(part)
	}
	return strings.Join(parts, "/"), nil
}

func oneDriveEscapeSegment(segment string) string {
	escaped := url.PathEscape(segment)
	// Graph uses colon-delimited path addressing; net/url deliberately leaves
	// ':' and brackets unescaped in path segments, but they are data here.
	escaped = strings.ReplaceAll(escaped, ":", "%3A")
	escaped = strings.ReplaceAll(escaped, "[", "%5B")
	return strings.ReplaceAll(escaped, "]", "%5D")
}

func (p *OneDriveProvider) itemURL(filePath string) (string, error) {
	escaped, err := oneDriveEscapedPath(filePath)
	if err != nil {
		return "", err
	}
	if escaped == "" {
		return p.apiBase + "/root", nil
	}
	return p.apiBase + "/root:/" + escaped + ":", nil
}

// resourceURL resolves a path that starts with a shared OneDrive shortcut to
// the owning drive and item. Graph exposes the shortcut in the user's drive,
// but its descendants must be addressed through the remote drive; resolving
// them again via /me/drive/root:/... causes the later transfer to return 404.
func (p *OneDriveProvider) resourceURL(ctx context.Context, filePath string) (string, error) {
	clean, err := oneDrivePath(filePath)
	if err != nil || clean == "/" {
		return p.itemURL(clean)
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	rootFolder := parts[0]
	remotePrefix := ""
	if cached, ok := p.shortcutCache.Load(rootFolder); ok {
		remotePrefix = cached.(string)
	} else {
		var resolveErr error
		remotePrefix, resolveErr = p.resolveSharedShortcut(ctx, rootFolder)
		if resolveErr != nil {
			return "", resolveErr
		}
		p.shortcutCache.Store(rootFolder, remotePrefix)
	}
	if remotePrefix == "" {
		return p.itemURL(clean)
	}
	if len(parts) == 1 {
		return remotePrefix, nil
	}
	for i, part := range parts[1:] {
		parts[i+1] = oneDriveEscapeSegment(part)
	}
	return remotePrefix + ":/" + strings.Join(parts[1:], "/") + ":", nil
}

func (p *OneDriveProvider) sharedShortcutCacheKey(rootFolder string) string {
	tokenDigest := sha256.Sum256([]byte(p.accessToken))
	return p.apiBase + "\x00" + fmt.Sprintf("%x", tokenDigest) + "\x00" + rootFolder
}

func (p *OneDriveProvider) resolveSharedShortcut(ctx context.Context, rootFolder string) (string, error) {
	key := p.sharedShortcutCacheKey(rootFolder)
	for {
		sharedShortcutResolutionCache.Lock()
		entry, found := sharedShortcutResolutionCache.entries[key]
		if found && time.Now().Before(entry.expiresAt) {
			ready := entry.ready
			sharedShortcutResolutionCache.Unlock()
			if ready != nil {
				select {
				case <-ready:
					continue
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return entry.remotePrefix, entry.err
		}
		if found {
			delete(sharedShortcutResolutionCache.entries, key)
		}
		for staleKey, staleEntry := range sharedShortcutResolutionCache.entries {
			if time.Now().After(staleEntry.expiresAt) {
				delete(sharedShortcutResolutionCache.entries, staleKey)
			}
		}
		entry = &oneDriveShortcutCacheEntry{ready: make(chan struct{}), expiresAt: time.Now().Add(sharedShortcutCacheTTL)}
		sharedShortcutResolutionCache.entries[key] = entry
		sharedShortcutResolutionCache.Unlock()

		remotePrefix, err := p.inspectSharedShortcut(ctx, rootFolder)
		sharedShortcutResolutionCache.Lock()
		entry.remotePrefix = remotePrefix
		// A 404 means this top-level name is not a shortcut. Cache that fact for
		// later tasks, while returning the original missing-item result to this
		// first caller so its normal create/missing path remains unchanged.
		entry.err = err
		if errors.Is(err, ErrNotFound) {
			entry.err = nil
		}
		entry.expiresAt = time.Now().Add(sharedShortcutCacheTTL)
		if entry.err != nil {
			entry.expiresAt = time.Now().Add(sharedShortcutErrorTTL)
		}
		close(entry.ready)
		entry.ready = nil
		sharedShortcutResolutionCache.Unlock()
		return remotePrefix, err
	}
}

func (p *OneDriveProvider) inspectSharedShortcut(ctx context.Context, rootFolder string) (string, error) {
	rootURL, err := p.itemURL("/" + rootFolder)
	if err != nil {
		return "", err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := p.request(ctx, http.MethodGet, rootURL+"?$select=id,remoteItem", nil, true)
		if err != nil {
			return "", err
		}
		resp, err := p.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var root oneDriveItem
			decodeErr := json.NewDecoder(resp.Body).Decode(&root)
			resp.Body.Close()
			if decodeErr != nil {
				return "", fmt.Errorf("decode onedrive shared root: %w", decodeErr)
			}
			if root.RemoteItem == nil || root.RemoteItem.ID == "" || root.RemoteItem.ParentReference.DriveID == "" {
				return "", nil
			}
			graphBase := strings.TrimSuffix(p.apiBase, "/me/drive")
			return graphBase + "/drives/" + url.PathEscape(root.RemoteItem.ParentReference.DriveID) + "/items/" + url.PathEscape(root.RemoteItem.ID), nil
		}
		lastErr = oneDriveError("inspect shared root", resp.StatusCode)
		retryAfter := resp.Header.Get("Retry-After")
		resp.Body.Close()
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
			return "", lastErr
		}
		if attempt < 2 {
			if err := oneDriveWait(ctx, retryAfter, attempt); err != nil {
				return "", err
			}
		}
	}
	return "", lastErr
}

func (p *OneDriveProvider) request(ctx context.Context, method, rawURL string, body io.Reader, authorize bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if authorize {
		req.Header.Set("Authorization", "Bearer "+p.accessToken)
	}
	return req, nil
}

func oneDriveError(operation string, status int) error {
	if status == http.StatusUnauthorized {
		return fmt.Errorf("onedrive %s: %w", operation, ErrAuth)
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("onedrive %s: %w", operation, ErrNotFound)
	}
	return fmt.Errorf("onedrive %s failed with status %d", operation, status)
}

func (p *OneDriveProvider) validPaginationURL(rawURL string) (string, error) {
	next, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid onedrive pagination URL")
	}
	base, err := url.Parse(p.apiBase)
	if err != nil || next.Scheme != "https" || next.Host != base.Host {
		return "", errors.New("invalid onedrive pagination URL")
	}
	// Production instances are fixed to Graph. The configurable base only exists
	// for package-local TLS fixtures and must still retain its exact host.
	if p.apiBase == defaultOneDriveAPIBase && next.Host != "graph.microsoft.com" {
		return "", errors.New("invalid onedrive pagination URL")
	}
	return next.String(), nil
}

func oneDriveFilesOnly(resourceType string) error {
	if resourceType != "files" {
		return ErrUnsupportedResourceType
	}
	return nil
}

type oneDriveItem struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Size                 int64     `json:"size"`
	ETag                 string    `json:"eTag"`
	LastModifiedDateTime string    `json:"lastModifiedDateTime"`
	Folder               *struct{} `json:"folder"`
	RemoteItem           *struct {
		ID              string `json:"id"`
		ParentReference struct {
			DriveID string `json:"driveId"`
		} `json:"parentReference"`
	} `json:"remoteItem"`
	SpecialFolder *struct {
		Name string `json:"name"`
	} `json:"specialFolder"`
	File *struct {
		Hashes struct {
			QuickXorHash string `json:"quickXorHash"`
		} `json:"hashes"`
	} `json:"file"`
}

func oneDriveResource(item oneDriveItem, parentPath string) CloudResource {
	resourcePath := path.Join(parentPath, item.Name)
	if !strings.HasPrefix(resourcePath, "/") {
		resourcePath = "/" + resourcePath
	}
	modified, _ := time.Parse(time.RFC3339, item.LastModifiedDateTime)
	metadata := FileMetadata{}
	if item.SpecialFolder != nil && item.SpecialFolder.Name != "" {
		metadata.CustomProps = map[string]string{"onedrive_special_folder": item.SpecialFolder.Name}
	}
	hash := ""
	if item.File != nil && item.File.Hashes.QuickXorHash != "" {
		hash = "QUICKXOR:" + item.File.Hashes.QuickXorHash
	}
	return CloudResource{Path: resourcePath, Name: item.Name, Size: item.Size, IsDir: item.Folder != nil, Hash: hash, ETag: item.ETag, LastModified: modified, Metadata: metadata}
}

func (p *OneDriveProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := p.request(ctx, http.MethodGet, p.apiBase+"/root?$select=id", nil, true)
	if err != nil {
		return false, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, oneDriveError("connect", resp.StatusCode)
}

func (p *OneDriveProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	clean, err := oneDrivePath(dirPath)
	if err != nil {
		return nil, err
	}
	itemURL, err := p.resourceURL(ctx, clean)
	if err != nil {
		return nil, err
	}
	nextURL := itemURL + "/children?$select=id,name,size,eTag,lastModifiedDateTime,folder,specialFolder,file"
	var result []CloudResource
	for nextURL != "" {
		req, err := p.request(ctx, http.MethodGet, nextURL, nil, true)
		if err != nil {
			return nil, err
		}
		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, oneDriveError("list directory", resp.StatusCode)
		}
		var page struct {
			Value    []oneDriveItem `json:"value"`
			NextLink string         `json:"@odata.nextLink"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode onedrive directory listing: %w", decodeErr)
		}
		for _, item := range page.Value {
			result = append(result, oneDriveResource(item, clean))
		}
		if page.NextLink == "" {
			nextURL = ""
			continue
		}
		nextURL, err = p.validPaginationURL(page.NextLink)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (p *OneDriveProvider) InspectResource(ctx context.Context, resourceType, filePath string) (CloudResource, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return CloudResource{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	clean, err := oneDrivePath(filePath)
	if err != nil {
		return CloudResource{}, err
	}
	itemURL, err := p.resourceURL(ctx, clean)
	if err != nil {
		return CloudResource{}, err
	}
	req, err := p.request(ctx, http.MethodGet, itemURL+"?$select=id,name,size,eTag,lastModifiedDateTime,folder,specialFolder,file", nil, true)
	if err != nil {
		return CloudResource{}, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CloudResource{}, oneDriveError("inspect", resp.StatusCode)
	}
	var item oneDriveItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return CloudResource{}, fmt.Errorf("decode onedrive item: %w", err)
	}
	if clean == "/" {
		return CloudResource{Path: "/", IsDir: true, ETag: item.ETag}, nil
	}
	return oneDriveResource(item, path.Dir(clean)), nil
}

func (p *OneDriveProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return nil, err
	}
	itemURL, err := p.resourceURL(ctx, filePath)
	if err != nil {
		return nil, err
	}
	// Graph redirects to a pre-authorized host. Do not forward the bearer token.
	client := *p.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Authorization")
		return nil
	}
	req, err := p.request(ctx, http.MethodGet, itemURL+"/content", nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, oneDriveError("download", resp.StatusCode)
	}
	return resp.Body, nil
}

// StreamDownloadRange implements RangeDownloader for OneDriveProvider.
func (p *OneDriveProvider) StreamDownloadRange(ctx context.Context, resourceType, filePath string, offset, length int64) (io.ReadCloser, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return nil, err
	}
	rangeHeader, err := FormatByteRangeHeader(offset, length)
	if err != nil {
		return nil, err
	}
	itemURL, err := p.resourceURL(ctx, filePath)
	if err != nil {
		return nil, err
	}
	client := *p.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Authorization")
		return nil
	}
	req, err := p.request(ctx, http.MethodGet, itemURL+"/content", nil, true)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", rangeHeader)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrAuth
	}
	return ValidateHTTPRangeResponse(resp, offset, length)
}

func (p *OneDriveProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return p.StreamUploadChunked(ctx, resourceType, filePath, stream, size, nil)
}

func (p *OneDriveProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	if err := p.CreateParentDirectories(ctx, resourceType, filePath); err != nil {
		return fmt.Errorf("create onedrive parent directories: %w", err)
	}
	if size <= oneDriveSimpleUploadLimit {
		return p.simpleUpload(ctx, filePath, stream, size, progressChan)
	}
	return p.sessionUpload(ctx, filePath, stream, size, progressChan)
}

func (p *OneDriveProvider) simpleUpload(ctx context.Context, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	itemURL, err := p.itemURL(filePath)
	if err != nil {
		return err
	}
	var body io.Reader = stream
	if progressChan != nil {
		body = &ProgressReader{Reader: stream, ProgressChan: progressChan}
	}
	req, err := p.request(ctx, http.MethodPut, itemURL+"/content", body, true)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oneDriveError("upload", resp.StatusCode)
	}
	return nil
}

func (p *OneDriveProvider) sessionUpload(ctx context.Context, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	itemURL, err := p.itemURL(filePath)
	if err != nil {
		return err
	}
	req, err := p.request(ctx, http.MethodPost, itemURL+"/createUploadSession", strings.NewReader(`{"item":{"@microsoft.graph.conflictBehavior":"replace"}}`), true)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return oneDriveError("create upload session", resp.StatusCode)
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if decodeErr != nil {
		return fmt.Errorf("decode onedrive upload session: %w", decodeErr)
	}
	if session.UploadURL == "" {
		return errors.New("onedrive upload session did not return an upload URL")
	}

	buf := make([]byte, oneDriveUploadChunkSize)
	var offset int64
	for offset < size {
		remaining := size - offset
		chunkLen := len(buf)
		if remaining < int64(chunkLen) {
			chunkLen = int(remaining)
		}
		n, readErr := io.ReadFull(stream, buf[:chunkLen])
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return fmt.Errorf("read onedrive upload chunk: %w", readErr)
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		if err := p.uploadChunk(ctx, session.UploadURL, buf[:n], offset, size); err != nil {
			return err
		}
		offset += int64(n)
		if progressChan != nil {
			progressChan <- int64(n)
		}
		if readErr != nil {
			break
		}
	}
	if offset != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (p *OneDriveProvider) uploadChunk(ctx context.Context, uploadURL string, chunk []byte, offset, total int64) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := p.request(ctx, http.MethodPut, uploadURL, bytes.NewReader(chunk), false)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+int64(len(chunk))-1, total))
		resp, err := p.httpClient.Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusAccepted {
				resp.Body.Close()
				return nil
			}
			lastErr = oneDriveError("upload chunk", resp.StatusCode)
			retryAfter := resp.Header.Get("Retry-After")
			resp.Body.Close()
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return lastErr
			}
			if err := oneDriveWait(ctx, retryAfter, attempt); err != nil {
				return err
			}
			continue
		}
		lastErr = err
		if err := oneDriveWait(ctx, "", attempt); err != nil {
			return err
		}
	}
	return lastErr
}

func oneDriveWait(ctx context.Context, retryAfter string, attempt int) error {
	delay := time.Duration(attempt+1) * time.Second
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil && retryAfter != "" {
		delay = seconds
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

func (p *OneDriveProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return false, 0, err
	}
	resource, err := p.InspectResource(ctx, resourceType, filePath)
	if err == nil {
		return true, resource.Size, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, 0, nil
	}
	return false, 0, err
}

func (p *OneDriveProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	itemURL, err := p.itemURL(filePath)
	if err != nil {
		return err
	}
	req, err := p.request(ctx, http.MethodDelete, itemURL, nil, true)
	if err != nil {
		return err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return oneDriveError("delete", resp.StatusCode)
}

func (p *OneDriveProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return "", err
	}
	res, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return "", err
	}
	if res.Hash == "" {
		return "", ErrChecksumNotAvailable
	}
	return res.Hash, nil
}

func (p *OneDriveProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	return p.CreateDirectory(ctx, resourceType, path.Dir(filePath))
}

func (p *OneDriveProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	clean, err := oneDrivePath(dirPath)
	if err != nil || clean == "/" {
		return err
	}
	current := ""
	for _, component := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		current += "/" + component
		if _, ok := p.confirmedDirectories.Load(current); ok {
			continue
		}
		resource, inspectErr := p.InspectResource(ctx, resourceType, current)
		if inspectErr == nil {
			if !resource.IsDir {
				return fmt.Errorf("onedrive path component %q is a file", current)
			}
			p.confirmedDirectories.Store(current, struct{}{})
			continue
		}
		if !errors.Is(inspectErr, ErrNotFound) {
			return inspectErr
		}
		parentURL, err := p.itemURL(path.Dir(current))
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]any{"name": component, "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "fail"})
		if err != nil {
			return err
		}
		req, err := p.request(ctx, http.MethodPost, parentURL+"/children", bytes.NewReader(body), true)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.httpClient.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			existing, err := p.InspectResource(ctx, resourceType, current)
			if err != nil {
				return err
			}
			if !existing.IsDir {
				return fmt.Errorf("onedrive path component %q is a file", current)
			}
			p.confirmedDirectories.Store(current, struct{}{})
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return oneDriveError("create directory", resp.StatusCode)
		}
		resp.Body.Close()
		p.confirmedDirectories.Store(current, struct{}{})
	}
	return nil
}

func (p *OneDriveProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	oldURL, err := p.itemURL(oldPath)
	if err != nil {
		return err
	}
	cleanNew, err := oneDrivePath(newPath)
	if err != nil {
		return err
	}
	cleanOld, err := oneDrivePath(oldPath)
	if err != nil {
		return err
	}
	payload := map[string]any{"name": path.Base(cleanNew)}
	if oldParent, newParent := path.Dir(cleanOld), path.Dir(cleanNew); oldParent != newParent {
		if err := p.CreateDirectory(ctx, resourceType, newParent); err != nil {
			return err
		}
		parentID, err := p.itemID(ctx, newParent)
		if err != nil {
			return err
		}
		payload["parentReference"] = map[string]string{"id": parentID}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := p.request(ctx, http.MethodPatch, oldURL, bytes.NewReader(body), true)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oneDriveError("rename", resp.StatusCode)
	}
	return nil
}

func (p *OneDriveProvider) ApplyMetadata(ctx context.Context, resourceType, filePath string, meta FileMetadata) error {
	if err := oneDriveFilesOnly(resourceType); err != nil {
		return err
	}
	if meta.ModifiedTime.IsZero() {
		return nil
	}
	itemURL, err := p.itemURL(filePath)
	if err != nil {
		return err
	}
	// Note: createdDateTime is intentionally omitted; we only preserve the
	// source's last-modified time. Graph will default createdDateTime to the
	// upload time.
	payload := map[string]any{
		"fileSystemInfo": map[string]string{
			"lastModifiedDateTime": meta.ModifiedTime.UTC().Format(time.RFC3339),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := p.request(ctx, http.MethodPatch, itemURL, bytes.NewReader(body), true)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oneDriveError("apply metadata", resp.StatusCode)
	}
	return nil
}

func (p *OneDriveProvider) itemID(ctx context.Context, filePath string) (string, error) {
	itemURL, err := p.itemURL(filePath)
	if err != nil {
		return "", err
	}
	req, err := p.request(ctx, http.MethodGet, itemURL+"?$select=id", nil, true)
	if err != nil {
		return "", err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", oneDriveError("inspect parent", resp.StatusCode)
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return "", fmt.Errorf("decode onedrive parent: %w", err)
	}
	if item.ID == "" {
		return "", errors.New("onedrive parent item did not return an ID")
	}
	return item.ID, nil
}

func (p *OneDriveProvider) VerificationMode() VerificationMode { return VerificationCryptographicHash }
func (p *OneDriveProvider) SupportsAtomicRename() bool         { return true }
