package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const googlePhotosAPIBase = "https://photoslibrary.googleapis.com/v1"

// GooglePhotosProvider implements StorageProvider for the Google Photos Library API.
// Albums are mapped to directories (is_dir=true) and media items to files (resourceType "files").
// The API exposes no server-side folder hierarchy, no rename, and no content hash, so uploads
// target albums derived from the path's first segment and integrity falls back to size comparison.
//
// The Photos Library API does NOT deduplicate albums by title: albums.create returns 200 even
// when an album with the same title already exists. To avoid album proliferation on the target
// we maintain an in-memory title<->ID cache populated lazily from listAlbums / resolveAlbum, so
// repeated uploads into the same album reuse the existing album instead of creating duplicates.
type GooglePhotosProvider struct {
	AccessToken string
	HTTPClient  *http.Client
	BaseURL     string

	albumMu       sync.Mutex
	albumTitleToID map[string]string // album title -> album id
	albumIDToTitle map[string]string // album id   -> album title
}

func NewGooglePhotosProvider(ctx context.Context, token string) (*GooglePhotosProvider, error) {
	if token == "" {
		return nil, fmt.Errorf("googlephotos provider requires an oauth token")
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := oauth2.NewClient(ctx, ts)

	return &GooglePhotosProvider{
		AccessToken:    token,
		HTTPClient:     client,
		BaseURL:        googlePhotosAPIBase,
		albumTitleToID: make(map[string]string),
		albumIDToTitle: make(map[string]string),
	}, nil
}

// cacheAlbum records the title<->ID mapping in both directions.
func (p *GooglePhotosProvider) cacheAlbum(id, title string) {
	if id == "" {
		return
	}
	p.albumMu.Lock()
	defer p.albumMu.Unlock()
	p.albumIDToTitle[id] = title
	if title != "" {
		p.albumTitleToID[title] = id
	}
}

// lookupAlbumByTitle returns the cached album id for a title, if known.
func (p *GooglePhotosProvider) lookupAlbumByTitle(title string) (string, bool) {
	p.albumMu.Lock()
	defer p.albumMu.Unlock()
	id, ok := p.albumTitleToID[title]
	return id, ok
}

func (p *GooglePhotosProvider) apiURL(suffix string) string {
	return p.BaseURL + suffix
}

func (p *GooglePhotosProvider) Close() error {
	p.HTTPClient.CloseIdleConnections()
	return nil
}

func (p *GooglePhotosProvider) newRequest(ctx context.Context, method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	req.Header.Set("User-Agent", "GooglePhotos-Migration-Worker/1.0")
	return req, nil
}

// googlePhotosError models the standard Google Photos API error envelope.
type googlePhotosError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (p *GooglePhotosProvider) errorFromResponse(resp *http.Response) error {
	var gErr googlePhotosError
	// Bound the body we decode so a malformed/large error payload can't exhaust memory.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&gErr)
	if gErr.Error.Code != 0 {
		return fmt.Errorf("google photos api error %d: %s", gErr.Error.Code, gErr.Error.Message)
	}
	return fmt.Errorf("google photos api error with status: %d", resp.StatusCode)
}

// Connect probes Photos Library access by listing albums and media items.
func (p *GooglePhotosProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := p.newRequest(ctx, "GET", p.apiURL("/albums?pageSize=1"), nil)
	if err != nil {
		return false, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return false, fmt.Errorf("google photos connect: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("google photos connect: %w", p.errorFromResponse(resp))
	}

	req2, err := p.newRequest(ctx, "GET", p.apiURL("/mediaItems?pageSize=1"), nil)
	if err != nil {
		return false, err
	}
	resp2, err := p.HTTPClient.Do(req2)
	if err != nil {
		return false, err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized {
		return false, fmt.Errorf("google photos connect: %w", ErrAuth)
	}
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return false, fmt.Errorf("google photos connect: %w", p.errorFromResponse(resp2))
	}

	return true, nil
}

type googlePhotosAlbum struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProductURL  string `json:"productUrl"`
	IsWriteable bool   `json:"isWriteable"`
}

type googlePhotosAlbumsResponse struct {
	Albums       []googlePhotosAlbum `json:"albums"`
	NextPageToken string             `json:"nextPageToken"`
}

type googlePhotosMediaItem struct {
	ID            string `json:"id"`
	Description   string `json:"description"`
	ProductURL    string `json:"productUrl"`
	BaseURL       string `json:"baseUrl"`
	MimeType      string `json:"mimeType"`
	Filename      string `json:"filename"`
	// size is populated out-of-band via a HEAD on BaseURL (the API JSON itself
	// does not include the byte size of the original).
	size          int64  `json:"-"`
	MediaMetadata struct {
		CreationTime string                 `json:"creationTime"`
		Width        string                 `json:"width"`
		Height       string                 `json:"height"`
		Photo        map[string]interface{} `json:"photo"`
		Video        map[string]interface{} `json:"video"`
	} `json:"mediaMetadata"`
}

type googlePhotosMediaItemsResponse struct {
	MediaItems   []googlePhotosMediaItem `json:"mediaItems"`
	NextPageToken string                 `json:"nextPageToken"`
}

// cleanPath normalises a path to remove leading/trailing slashes.
func (p *GooglePhotosProvider) cleanPath(filePath string) string {
	return strings.Trim(filePath, "/")
}

// GetDirectoryListing maps albums (root) and media items (within an album) to CloudResources.
// Only the "files" resource type is supported.
func (p *GooglePhotosProvider) GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return nil, nil
	}

	clean := p.cleanPath(dirPath)
	if clean == "" {
		return p.listAlbums(ctx)
	}

	// dirPath is "/<albumId>" → list media items in that album.
	albumID := clean
	return p.listAlbumMedia(ctx, albumID)
}

func (p *GooglePhotosProvider) listAlbums(ctx context.Context) ([]CloudResource, error) {
	var resources []CloudResource
	pageToken := ""
	for {
		urlStr := p.apiURL("/albums?pageSize=50")
		if pageToken != "" {
			urlStr += "&pageToken=" + pageToken
		}
		req, err := p.newRequest(ctx, "GET", urlStr, nil)
		if err != nil {
			return nil, err
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, fmt.Errorf("google photos listing: %w", ErrAuth)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody := p.errorFromResponse(resp)
			resp.Body.Close()
			return nil, errBody
		}
		var listResp googlePhotosAlbumsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, a := range listResp.Albums {
			title := a.Title
			if title == "" {
				title = a.ID
			}
			p.cacheAlbum(a.ID, title)
			resources = append(resources, CloudResource{
				Path:  "/" + a.ID,
				Name:  title,
				IsDir: true,
				Size:  0,
			})
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	return resources, nil
}

func (p *GooglePhotosProvider) listAlbumMedia(ctx context.Context, albumID string) ([]CloudResource, error) {
	var resources []CloudResource
	pageToken := ""
	for {
		body, err := json.Marshal(map[string]interface{}{
			"albumId":   albumID,
			"pageSize":  100,
			"pageToken": pageToken,
		})
		if err != nil {
			return nil, err
		}
		req, err := p.newRequest(ctx, "POST", p.apiURL("/mediaItems:search"), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			return nil, fmt.Errorf("google photos listing: %w", ErrAuth)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody := p.errorFromResponse(resp)
			resp.Body.Close()
			return nil, errBody
		}
		var listResp googlePhotosMediaItemsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, m := range listResp.MediaItems {
			resources = append(resources, p.mediaItemToResource(albumID, m))
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	return resources, nil
}

func (p *GooglePhotosProvider) mediaItemToResource(albumID string, m googlePhotosMediaItem) CloudResource {
	name := m.Filename
	if name == "" {
		name = m.ID
	}
	modTime, _ := time.Parse(time.RFC3339, m.MediaMetadata.CreationTime)
	return CloudResource{
		Path:         "/" + albumID + "/" + m.ID + extForMime(m.MimeType, name),
		Name:         name,
		Size:         m.size,
		IsDir:        false,
		Hash:         "",
		LastModified: modTime,
	}
}

func extForMime(mime, name string) string {
	if strings.Contains(name, ".") {
		return ""
	}
	switch {
	case strings.HasPrefix(mime, "image/"):
		return ".jpg"
	case strings.HasPrefix(mime, "video/"):
		return ".mp4"
	default:
		return ""
	}
}

// InspectResource returns metadata for an album (directory) or a media item (file).
func (p *GooglePhotosProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}

	clean := p.cleanPath(resourcePath)
	if clean == "" {
		return CloudResource{Path: "/", Name: "", IsDir: true, Size: 0}, nil
	}

	parts := strings.Split(clean, "/")
	if len(parts) == 1 {
		// Album
		return CloudResource{
			Path:  "/" + parts[0],
			Name:  parts[0],
			IsDir: true,
			Size:  0,
		}, nil
	}

	// Media item → fetch fresh metadata (baseUrl is short-lived).
	albumID := parts[0]
	mediaID := parts[1]
	return p.getMediaItem(ctx, albumID, mediaID)
}

func (p *GooglePhotosProvider) getMediaItem(ctx context.Context, albumID, mediaID string) (CloudResource, error) {
	req, err := p.newRequest(ctx, "GET", p.apiURL("/mediaItems/"+mediaID), nil)
	if err != nil {
		return CloudResource{}, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return CloudResource{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return CloudResource{}, fmt.Errorf("google photos inspect: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody := p.errorFromResponse(resp)
		resp.Body.Close()
		return CloudResource{}, errBody
	}
	var m googlePhotosMediaItem
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		resp.Body.Close()
		return CloudResource{}, err
	}
	resp.Body.Close()

	// Photos does not return a size in the media item metadata. The download URL
	// (baseUrl) serves the original bytes, so a HEAD request yields the real
	// Content-Length. This lets the processor's size-based integrity fallback
	// actually compare something meaningful instead of always reporting 0.
	if m.BaseURL != "" {
		if size, serr := p.fetchMediaSize(ctx, m.BaseURL); serr == nil {
			m.size = size
		}
	}
	return p.mediaItemToResource(albumID, m), nil
}

// fetchMediaSize issues a HEAD against the (fresh) baseUrl and returns Content-Length.
func (p *GooglePhotosProvider) fetchMediaSize(ctx context.Context, baseURL string) (int64, error) {
	req, err := p.newRequest(ctx, "HEAD", baseURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("google photos size head failed with status %d", resp.StatusCode)
	}
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("google photos size unknown")
	}
	return resp.ContentLength, nil
}

// StreamDownload fetches the original bytes via the (fresh) baseUrl with the "=d" download suffix.
func (p *GooglePhotosProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}

	parts := strings.Split(p.cleanPath(filePath), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid google photos media path: %s", filePath)
	}
	mediaID := parts[1]

	// Fetch fresh baseUrl by re-fetching the media item metadata.
	req, err := p.newRequest(ctx, "GET", p.apiURL("/mediaItems/"+mediaID), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("google photos download: failed to fetch media item, status %d", resp.StatusCode)
	}
	var fetched googlePhotosMediaItem
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		resp.Body.Close()
		return nil, err
	}
	resp.Body.Close()

	if fetched.BaseURL == "" {
		return nil, fmt.Errorf("google photos download: no baseUrl for media item %s", mediaID)
	}

	dlReq, err := p.newRequest(ctx, "GET", fetched.BaseURL+"=d", nil)
	if err != nil {
		return nil, err
	}
	dlResp, err := p.HTTPClient.Do(dlReq)
	if err != nil {
		return nil, err
	}
	if dlResp.StatusCode < 200 || dlResp.StatusCode >= 300 {
		dlResp.Body.Close()
		return nil, fmt.Errorf("google photos download: failed with status %d", dlResp.StatusCode)
	}
	return dlResp.Body, nil
}

// StreamUpload uploads a single media item to the album derived from path segment 1.
func (p *GooglePhotosProvider) StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error {
	return p.StreamUploadChunked(ctx, resourceType, filePath, stream, size, nil)
}

// StreamUploadChunked uploads one media item per call. The albumId is the first path segment;
// the album is created (if missing) before the media item is added to it.
func (p *GooglePhotosProvider) StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}

	parts := strings.Split(p.cleanPath(filePath), "/")
	if len(parts) < 1 {
		return fmt.Errorf("invalid google photos upload path: %s", filePath)
	}
	// The first path segment is the target album. The processor may append a
	// ".tmp" suffix for its atomic-rename pattern, but Google Photos has no
	// rename operation — strip it so the album is resolved correctly and the
	// media item is not left with a ".tmp" name on the target (findings #1/#5).
	albumSegment := strings.TrimSuffix(parts[0], ".tmp")

	// Resolve/create the target album (deduplicated via the local title<->ID cache).
	resolvedAlbumID, err := p.resolveAlbum(ctx, albumSegment)
	if err != nil {
		return err
	}

	// 1) Upload the binary bytes to obtain an uploadToken.
	uploadToken, err := p.uploadBytes(ctx, stream, size, progressChan)
	if err != nil {
		return err
	}

	// 2) batchCreate the media item referencing the uploadToken + album.
	// The original filename (sans any ".tmp" suffix) is carried as the
	// media item description; the actual filename comes from the uploaded bytes.
	originalName := deriveOriginalName(filePath)
	return p.batchCreateMedia(ctx, uploadToken, resolvedAlbumID, originalName)
}

// deriveOriginalName returns the last path segment without any ".tmp" suffix
// that the processor's atomic-rename pattern may have appended.
func deriveOriginalName(filePath string) string {
	name := path.Base(filePath)
	return strings.TrimSuffix(name, ".tmp")
}

// resolveAlbum turns the first path segment of an upload into an existing album
// id. The segment may be either a human album title (typical for non-Photos
// sources) or an album id (when migrating Photos -> Photos, where the source
// path is "/<albumId>/<mediaId>"). We:
//   1. check the title->id cache (populated by listAlbums / prior creates);
//   2. if the segment matches an existing album's id, reuse that album (so a
//      Photos->Photos run keeps the original album title instead of creating a
//      new album literally named after the id — finding #3);
//   3. otherwise create a new album with the segment as its title and cache it
//      (deduplicated on subsequent uploads — finding #2).
func (p *GooglePhotosProvider) resolveAlbum(ctx context.Context, albumSegment string) (string, error) {
	if id, ok := p.lookupAlbumByTitle(albumSegment); ok {
		return id, nil
	}

	// Does an album with this exact id already exist on the target?
	if cachedTitle, ok := p.albumIDToTitle[albumSegment]; ok && cachedTitle != "" {
		return albumSegment, nil
	}
	if existingID, found := p.findAlbumByID(ctx, albumSegment); found {
		return existingID, nil
	}

	return p.createAlbum(ctx, albumSegment)
}

func (p *GooglePhotosProvider) createAlbum(ctx context.Context, title string) (string, error) {
	body, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"title": title,
		},
	})
	if err != nil {
		return "", err
	}
	req, err := p.newRequest(ctx, "POST", p.apiURL("/albums"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("google photos create album: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google photos create album: %w", p.errorFromResponse(resp))
	}
	var created googlePhotosAlbum
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	p.cacheAlbum(created.ID, title)
	return created.ID, nil
}

// findAlbumByID reports whether an album with the given id already exists,
// returning its id if so. Google Photos does not dedupe by title, but it does
// keep stable ids, so a Photos->Photos migration whose path carries the source
// album id can be mapped onto the already-existing target album.
func (p *GooglePhotosProvider) findAlbumByID(ctx context.Context, id string) (string, bool) {
	pageToken := ""
	for {
		urlStr := p.apiURL("/albums?pageSize=50")
		if pageToken != "" {
			urlStr += "&pageToken=" + pageToken
		}
		req, err := p.newRequest(ctx, "GET", urlStr, nil)
		if err != nil {
			return "", false
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return "", false
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return "", false
		}
		var listResp googlePhotosAlbumsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return "", false
		}
		resp.Body.Close()
		for _, a := range listResp.Albums {
			p.cacheAlbum(a.ID, a.Title)
			if a.ID == id {
				return a.ID, true
			}
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	return "", false
}

func (p *GooglePhotosProvider) findAlbumByTitle(ctx context.Context, title string) (string, error) {
	pageToken := ""
	for {
		urlStr := p.apiURL("/albums?pageSize=50")
		if pageToken != "" {
			urlStr += "&pageToken=" + pageToken
		}
		req, err := p.newRequest(ctx, "GET", urlStr, nil)
		if err != nil {
			return "", err
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return "", fmt.Errorf("google photos find album: %w", p.errorFromResponse(resp))
		}
		var listResp googlePhotosAlbumsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()
		for _, a := range listResp.Albums {
			p.cacheAlbum(a.ID, a.Title)
			if a.Title == title {
				return a.ID, nil
			}
		}
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	// Fall back to creating again (title may differ in casing).
	return p.createAlbumUnique(ctx, title)
}

func (p *GooglePhotosProvider) createAlbumUnique(ctx context.Context, title string) (string, error) {
	uniqueTitle := fmt.Sprintf("%s %d", title, time.Now().UnixNano())
	body, err := json.Marshal(map[string]interface{}{
		"album": map[string]interface{}{
			"title": uniqueTitle,
		},
	})
	if err != nil {
		return "", err
	}
	req, err := p.newRequest(ctx, "POST", p.apiURL("/albums"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google photos create album: %w", p.errorFromResponse(resp))
	}
	var created googlePhotosAlbum
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	p.cacheAlbum(created.ID, uniqueTitle)
	return created.ID, nil
}

// uploadBytes performs mediaItems:upload (multipart/related) and returns the uploadToken.
func (p *GooglePhotosProvider) uploadBytes(ctx context.Context, stream io.Reader, size int64, progressChan chan<- int64) (string, error) {
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	// Wrap the source stream to report progress.
	var src io.Reader = stream
	if progressChan != nil {
		src = &googlePhotosProgressReader{r: stream, progressChan: progressChan}
	}

	go func() {
		metaPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type": {"application/json"},
		})
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		_ = json.NewEncoder(metaPart).Encode(map[string]interface{}{
			"description": "",
		})

		binPart, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type": {"application/octet-stream"},
		})
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(binPart, src); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := writer.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	req, err := p.newRequest(ctx, "POST", p.apiURL("/mediaItems:upload"), pr)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// The multipart wrapper makes the real body length larger than the source
	// byte count, and the pipe is consumed lazily, so we cannot set an accurate
	// Content-Length here. The HTTP client falls back to chunked transfer
	// encoding, which the Photos endpoint accepts. (size is kept for callers
	// that may need it but is intentionally not advertised as Content-Length.)
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("google photos upload: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google photos upload: %w", p.errorFromResponse(resp))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var upResp struct {
		UploadToken string `json:"uploadToken"`
	}
	if err := json.Unmarshal(body, &upResp); err != nil {
		return "", err
	}
	if upResp.UploadToken == "" {
		return "", fmt.Errorf("google photos upload: empty upload token")
	}
	return upResp.UploadToken, nil
}

func (p *GooglePhotosProvider) batchCreateMedia(ctx context.Context, uploadToken, albumID, fileName string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"albumId": albumID,
		"newMediaItems": []map[string]interface{}{
			{
				"description": fileName,
				"simpleMediaItem": map[string]interface{}{
					"uploadToken": uploadToken,
				},
			},
		},
	})
	if err != nil {
		return err
	}

	req, err := p.newRequest(ctx, "POST", p.apiURL("/mediaItems:batchCreate"), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("google photos batchCreate: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("google photos batchCreate: %w", p.errorFromResponse(resp))
	}
	return nil
}

// googlePhotosProgressReader reports bytes read to the progress channel.
type googlePhotosProgressReader struct {
	r            io.Reader
	progressChan chan<- int64
}

func (pr *googlePhotosProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.r.Read(p)
	if n > 0 && pr.progressChan != nil {
		pr.progressChan <- int64(n)
	}
	return n, err
}

func (p *GooglePhotosProvider) FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return false, 0, nil
	}
	_, err := p.InspectResource(ctx, resourceType, filePath)
	if err != nil {
		return false, 0, nil
	}
	return true, 0, nil
}

func (p *GooglePhotosProvider) DeleteFile(ctx context.Context, resourceType, filePath string) error {
	return fmt.Errorf("delete is not supported by googlephotos")
}

func (p *GooglePhotosProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
	// Google Photos API exposes no content hash; the processor falls back to size comparison.
	return "", nil
}

func (p *GooglePhotosProvider) CreateParentDirectories(ctx context.Context, resourceType, filePath string) error {
	// Albums are created on-demand during upload; nothing to pre-create.
	return nil
}

func (p *GooglePhotosProvider) CreateDirectory(ctx context.Context, resourceType, dirPath string) error {
	if resourceType != "files" {
		return fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}
	_, err := p.createAlbum(ctx, p.cleanPath(dirPath))
	return err
}

func (p *GooglePhotosProvider) RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error {
	return fmt.Errorf("rename is not supported by googlephotos")
}
