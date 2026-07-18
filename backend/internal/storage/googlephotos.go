package storage

import (
	"bytes"
	"errors"

	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const googlePhotosAPIBase = "https://photoslibrary.googleapis.com/v1"

// photoPickerAPIBase is the Google Photos Picker API. The Picker is the only
// remaining surface that can read a user's entire Google Photos library after
// Google removed the account-wide Library API `mediaItems.list` scope on
// 2025-03-31. A Picker session is created, the user selects media in a Google-
// hosted UI, and the items are then listed via this endpoint.
const photoPickerAPIBase = "https://photospicker.googleapis.com/v1"

// ErrPickerSessionExpired is returned by GetPickerMediaItems when the Google
// Photos Picker session can no longer be read (HTTP 400/404). Picker sessions
// are short-lived and single-use; when indexing runs much later than the
// connect-time selection (e.g. a scheduled migration), the indexer uses this
// sentinel to create a fresh session with the current OAuth token.
var ErrPickerSessionExpired = errors.New("google photos picker session expired or invalid")

// pickerPathPrefix marks a task FilePath that was produced from a Picker
// selection rather than the Library API. The path is encoded as
//   /picker/<mediaID><ext>?base_url=<url-escaped download URL>
// The `base_url` is re-read at download time (it is valid for ~60 minutes and
// requires the OAuth bearer header) so we never store long-lived secrets.
//
// The /picker/ prefix is a SOURCE-SIDE transport handle only. It must never be
// used as the upload destination: the processor derives the clean user-visible
// target filename from PickerTargetName (below) so media is written under the
// user's target directory with a real name rather than a "/picker/<id>?base_url=…"
// path that would otherwise create a literal "picker" folder and embed a
// credentialed query string in target filenames.
const pickerPathPrefix = "/picker/"

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

	// pickerSessionID is the Google Photos Picker session id, set from
	// Connect()/the migration row so the indexer reuses the same session that
	// the user selected media in.
	pickerSessionID string

	// uploadSem bounds concurrent write requests to the Photos Library API,
	// which enforces a tight "concurrent write request" quota. Without it, the
	// worker's parallel threads exhaust the quota (HTTP 429) within milliseconds.
	uploadSem chan struct{}
}

// googlePhotosMaxConcurrentWrites is the cap on parallel album/media write
// calls. Google's Photos Library API throttles concurrent writes aggressively;
// staying at 2 keeps well under the limit while still pipelining uploads.
const googlePhotosMaxConcurrentWrites = 2

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
		uploadSem:      make(chan struct{}, googlePhotosMaxConcurrentWrites),
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

// SetPickerSession stores the Picker session id on the provider instance.
func (p *GooglePhotosProvider) SetPickerSession(sessionID string) {
	p.pickerSessionID = sessionID
}

// PickerSessionID returns the currently configured Picker session id (if any).
// The indexer reads it to enumerate the user selection and to detect when a
// persisted session has expired and must be refreshed.
func (p *GooglePhotosProvider) PickerSessionID() string {
	return p.pickerSessionID
}

// pickerSessionURL returns the full URL for a Picker API endpoint.
func (p *GooglePhotosProvider) pickerSessionURL(suffix string) string {
	return photoPickerAPIBase + suffix
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

// PickerSession models the response of POST /v1/sessions.
type PickerSession struct {
	SessionID string `json:"sessionId"`
}

// CreatePickerSession creates a Google Photos Picker session. The caller (the
// frontend) then uses the returned session id together with the user's OAuth
// access token to render the embedded Picker UI. The session is valid for a
// limited time and should be re-created if indexing is deferred far into the
// future.
func (p *GooglePhotosProvider) CreatePickerSession(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	body, err := json.Marshal(map[string]interface{}{
		"mediaTypes": []string{"ALL_MEDIA"},
	})
	if err != nil {
		return "", err
	}
	req, err := p.newRequest(ctx, "POST", p.pickerSessionURL("/sessions"), bytes.NewReader(body))
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
		return "", fmt.Errorf("google photos picker session: %w", ErrAuth)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("google photos picker session: %w", p.errorFromResponse(resp))
	}
	var s PickerSession
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return "", err
	}
	if s.SessionID == "" {
		return "", fmt.Errorf("google photos picker session: empty session id")
	}
	p.pickerSessionID = s.SessionID
	return s.SessionID, nil
}

// PickerMediaItem models one media item returned by the Picker API. The Picker
// baseUrl is the download URL (valid for ~60 minutes, served only with the
// OAuth bearer header). There is no content hash or size in the Picker payload,
// so size is discovered at download/inspect time via a HEAD request.
type PickerMediaItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	MimeType string `json:"mimeType"`
	Size    int64  `json:"-"`
}

type pickerMediaItemsResponse struct {
	MediaItems   []PickerMediaItem `json:"mediaItems"`
	NextPageToken string           `json:"nextPageToken"`
}

// GetPickerMediaItems lists every media item the user selected in the given
// Picker session. It paginates on nextPageToken. The returned items carry the
// download baseUrl + mime type + a stable id used to build the task path.
func (p *GooglePhotosProvider) GetPickerMediaItems(ctx context.Context, sessionID string) ([]PickerMediaItem, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var items []PickerMediaItem
	pageToken := ""
	for {
		urlStr := p.pickerSessionURL("/mediaItems?sessionId=" + url.QueryEscape(sessionID))
		if pageToken != "" {
			urlStr += "&pageToken=" + url.QueryEscape(pageToken)
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
			return nil, fmt.Errorf("google photos picker media: %w", ErrAuth)
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
			// A 400/404 almost always means the session expired or was already
			// consumed. The caller (indexer) treats this sentinel as a signal to
			// create a fresh session and retry, so deferred migrations survive.
			resp.Body.Close()
			return nil, fmt.Errorf("%w: %v", ErrPickerSessionExpired, p.errorFromResponse(resp))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errBody := p.errorFromResponse(resp)
			resp.Body.Close()
			return nil, errBody
		}
		var listResp pickerMediaItemsResponse
		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		items = append(items, listResp.MediaItems...)
		// NOTE: the Picker payload carries no byte size, and resolving it would
		// require a HEAD on every item's baseUrl. For large selections (thousands
		// of items) that is N sequential requests and risks blowing the overall
		// index timeout while stalling the whole migration. We therefore do NOT
		// size items here; the processor discovers size at transfer time via
		// InspectResource/StreamDownload (HEAD on demand), and total_bytes is
		// reported as it is learned. The picker baseUrl is valid ~60 minutes, so
		// it is still fresh when the worker downloads each item.
		if listResp.NextPageToken == "" {
			break
		}
		pageToken = listResp.NextPageToken
	}
	return items, nil
}

// pickerPath builds a task FilePath for a Picker media item. The format is
//   /picker/<mediaID><ext>?base_url=<url-escaped download URL>&mime=<mime>
// so the processor can recover the exact download URL at transfer time without
// persisting it beyond the task lifetime. The FilePath is a SOURCE-SIDE
// transport handle only; the processor derives the clean target filename via
// PickerTargetName.
func PickerPath(item PickerMediaItem) string {
	return pickerPathPrefix + item.ID + extForMime(item.MimeType, item.Name) +
		"?base_url=" + url.QueryEscape(item.BaseURL) +
		"&mime=" + url.QueryEscape(item.MimeType)
}

// pickerMimeFromPath extracts the mime type stored in a Picker task path.
func pickerMimeFromPath(filePath string) string {
	q := strings.IndexByte(filePath, '?')
	if q < 0 {
		return ""
	}
	values, err := url.ParseQuery(filePath[q+1:])
	if err != nil {
		return ""
	}
	return values.Get("mime")
}

// parsePickerPath extracts the media id and download baseUrl from a Picker task
// path produced by PickerPath. It returns (mediaID, baseURL, error).
func parsePickerPath(filePath string) (string, string, error) {
	clean := strings.TrimPrefix(filePath, "/")
	if !strings.HasPrefix(clean, strings.TrimPrefix(pickerPathPrefix, "/")) {
		return "", "", fmt.Errorf("not a picker path: %s", filePath)
	}
	rest := strings.TrimPrefix(clean, strings.TrimPrefix(pickerPathPrefix, "/"))
	q := strings.IndexByte(rest, '?')
	if q < 0 {
		return "", "", fmt.Errorf("picker path missing query: %s", filePath)
	}
	mediaIDWithExt := rest[:q]
	// Strip the extension from the media id segment.
	mediaID := mediaIDWithExt
	if dot := strings.LastIndexByte(mediaID, '.'); dot > 0 {
		mediaID = mediaID[:dot]
	}

	values, err := url.ParseQuery(rest[q+1:])
	if err != nil {
		return "", "", fmt.Errorf("picker path invalid query: %w", err)
	}
	baseURL, err := url.QueryUnescape(values.Get("base_url"))
	if err != nil {
		return "", "", fmt.Errorf("picker path invalid base_url: %w", err)
	}
	if mediaID == "" || baseURL == "" {
		return "", "", fmt.Errorf("picker path missing media id or base_url: %s", filePath)
	}
	return mediaID, baseURL, nil
}

// IsPickerPath reports whether filePath is a Picker-sourced task path.
func IsPickerPath(filePath string) bool {
	return strings.HasPrefix(filePath, pickerPathPrefix)
}

// PickerHandle is the serialisable transport handle for a Picker-sourced media
// item. It is stored in the task's Metadata so the source download can recover
// the exact download baseUrl at transfer time, while the task FilePath stays a
// clean, user-visible name (no embedded credentialed URL).
type PickerHandle struct {
	ID      string `json:"picker_id"`
	BaseURL string `json:"base_url"`
	Mime    string `json:"mime"`
	Name    string `json:"name"`
}

// PickerHandleFromMetadata decodes a PickerHandle from a task metadata blob.
// It returns ok=false when the metadata does not describe a Picker item.
func PickerHandleFromMetadata(raw json.RawMessage) (PickerHandle, bool) {
	if len(raw) == 0 {
		return PickerHandle{}, false
	}
	var h PickerHandle
	if err := json.Unmarshal(raw, &h); err != nil {
		return PickerHandle{}, false
	}
	if h.ID == "" || h.BaseURL == "" {
		return PickerHandle{}, false
	}
	return h, true
}

// PickerTargetName returns the clean, user-visible target filename (basename)
// for a Picker-sourced task FilePath. The transport handle encodes the media
// id and mime type, so we derive a unique, stable "<id>.<ext>" name. This keeps
// the media out of a literal "/picker/" folder and free of the "?base_url=…"
// query string that the transport handle carries, so the upload destination is
// a normal filename under the user's target directory.
func PickerTargetName(filePath string) string {
	mediaID, baseURL, err := parsePickerPath(filePath)
	if err != nil || mediaID == "" {
		// Fall back to a generic, still-unique name derived from the path.
		clean := strings.TrimPrefix(filePath, pickerPathPrefix)
		clean = strings.Split(clean, "?")[0]
		if clean == "" {
			return "google-photos-item"
		}
		return "google-photos-" + clean
	}
	_ = baseURL
	return "google-photos-" + mediaID + extForMime(pickerMimeFromPath(filePath), "")
}

// Connect validates the OAuth token.
//
// As a SOURCE, Google Photos now uses the Picker API (`photospicker.mediaitems.
// readonly`), which does not grant the Library `albums.list` scope (that scope
// was removed on 2025-03-31). Probing `albums.list` would therefore 403 and make
// every connect fail. We instead validate the token against the OAuth userinfo
// endpoint, which is covered by the `userinfo.email` scope that is always
// requested. As a TARGET the Library `appendonly` scope is used for uploads and
// is unaffected by this read-side check.
func (p *GooglePhotosProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := p.newRequest(ctx, "GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
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

// downloadSuffix returns the Google Photos baseUrl download parameter for a
// given mime type. Images use "=d"; videos require "=dv" to fetch the real
// video bytes (otherwise an error/scaled thumbnail is returned).
func downloadSuffix(mimeType string) string {
	if strings.HasPrefix(mimeType, "video/") {
		return "=dv"
	}
	return "=d"
}

// InspectResource returns metadata for an album (directory) or a media item (file).
func (p *GooglePhotosProvider) InspectResource(ctx context.Context, resourceType, resourcePath string) (CloudResource, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if resourceType != "files" {
		return CloudResource{}, fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}

	// Picker-sourced paths carry their own download baseUrl. Inspect the size
	// via a HEAD on that URL (no "=d" suffix — that is Library-API-only).
	if IsPickerPath(resourcePath) {
		mediaID, baseURL, err := parsePickerPath(resourcePath)
		if err != nil {
			return CloudResource{}, err
		}
		var size int64
		if s, serr := p.fetchMediaSize(ctx, baseURL); serr == nil {
			size = s
		}
		// The picker path only carries the opaque media id (no human name), so
		return CloudResource{
			Path:  resourcePath,
			Name:  mediaID,
			Size:  size,
			IsDir: false,
			Hash:  "",
		}, nil
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

// StreamDownload fetches the original bytes via the (fresh) baseUrl. Picker-
// sourced paths carry their own baseUrl (valid ~60 min, served only with the
// OAuth bearer header) and must be downloaded verbatim — no "=d"/"=dv" suffix,
// which is a Library-API-only construct that does not apply to Picker URLs.
func (p *GooglePhotosProvider) StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error) {
	if resourceType != "files" {
		return nil, fmt.Errorf("resource type %s not supported by googlephotos", resourceType)
	}

	if IsPickerPath(filePath) {
		mediaID, baseURL, err := parsePickerPath(filePath)
		if err != nil {
			return nil, err
		}
		_ = mediaID
		req, err := p.newRequest(ctx, "GET", baseURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("google photos picker download: failed with status %d", resp.StatusCode)
		}
		return resp.Body, nil
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

	dlReq, err := p.newRequest(ctx, "GET", fetched.BaseURL+downloadSuffix(fetched.MimeType), nil)
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

	// Bound concurrent writes to respect the Photos "concurrent write request" quota.
	p.uploadSem <- struct{}{}
	defer func() { <-p.uploadSem }()

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

	// The original filename (sans any ".tmp" suffix) is carried as the media
	// item fileName; the actual filename comes from the uploaded bytes.
	originalName := deriveOriginalName(filePath)

	// 1) Upload the binary bytes to obtain an uploadToken. The upload content
	// type is derived from the original filename so the Photos API receives the
	// correct X-Goog-Upload-Content-Type header.
	uploadMime := mimeFromName(originalName)
	uploadToken, err := p.uploadBytes(ctx, stream, size, uploadMime, progressChan)
	if err != nil {
		return err
	}

	// 2) batchCreate the media item referencing the uploadToken + album.
	return p.batchCreateMedia(ctx, uploadToken, resolvedAlbumID, originalName)
}

// mimeFromName derives a best-effort MIME type from a filename's extension,
// defaulting to application/octet-stream. It is used as the value of the
// X-Goog-Upload-Content-Type header when uploading raw bytes to Photos.
func mimeFromName(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	default:
		return "application/octet-stream"
	}
}
// deriveOriginalName returns the file name portion of filePath, stripping any
// ".tmp" suffix the processor's atomic-rename pattern may have appended (see
// processor.go). Google Photos ignores the suffix anyway, but keeping the name
// clean avoids a ".tmp" media item name on the target.
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

// uploadBytes performs a raw-binary upload to POST /v1/uploads and returns the
// upload token as plain text. The Photos Library upload endpoint expects:
//   - Content-Type: application/octet-stream
//   - X-Goog-Upload-Protocol: raw
//   - X-Goog-Upload-Content-Type: <mime>
//   - the raw binary bytes as the request body
// The response body is the upload token as plain text (NOT JSON).
func (p *GooglePhotosProvider) uploadBytes(ctx context.Context, stream io.Reader, size int64, mime string, progressChan chan<- int64) (string, error) {
	var src io.Reader = stream
	if progressChan != nil {
		src = &googlePhotosProgressReader{r: stream, progressChan: progressChan}
	}

	req, err := p.newRequest(ctx, "POST", p.apiURL("/uploads"), src)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Goog-Upload-Protocol", "raw")
	if mime != "" {
		req.Header.Set("X-Goog-Upload-Content-Type", mime)
	}
	if size > 0 {
		req.ContentLength = size
	}

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
	uploadToken := strings.TrimSpace(string(body))
	if uploadToken == "" {
		return "", fmt.Errorf("google photos upload: empty upload token")
	}
	return uploadToken, nil
}

func (p *GooglePhotosProvider) batchCreateMedia(ctx context.Context, uploadToken, albumID, fileName string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"albumId": albumID,
		"newMediaItems": []map[string]interface{}{
			{
				"simpleMediaItem": map[string]interface{}{
					"uploadToken": uploadToken,
					"fileName":    fileName,
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

	// The batchCreate endpoint returns 200 with a per-item result array even when
	// individual items fail. Inspect newMediaItemResults and surface a real error
	// for any non-zero per-item status code.
	var batchResp struct {
		NewMediaItemResults []struct {
			UploadToken string `json:"uploadToken"`
			Status      struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"status"`
		} `json:"newMediaItemResults"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&batchResp); err != nil {
		return fmt.Errorf("google photos batchCreate: failed to decode response: %w", err)
	}
	for _, r := range batchResp.NewMediaItemResults {
		if r.Status.Code != 0 {
			return fmt.Errorf("google photos batchCreate: item failed with status %d: %s", r.Status.Code, r.Status.Message)
		}
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

// SupportsAtomicRename is false: Google Photos has no rename or delete
// operation. The media item is written directly to its final album + filename
// during StreamUpload(Chunked) (the processor's ".tmp" suffix is stripped
// there), so the processor must NOT attempt the upload-to-.tmp-then-rename
// pattern — doing so would always fail. See processor.go deleteAfterUpload.
func (p *GooglePhotosProvider) SupportsAtomicRename() bool {
	return false
}

// GetPickerMediaItems is a package-level accessor used by the indexer. Only the
// Google Photos provider implements Picker enumeration; other providers return
// an error so the caller can fail fast with a clear message. This keeps the
// StorageProvider interface unchanged for the other nine providers.
func GetPickerMediaItems(ctx context.Context, provider StorageProvider, sessionID string) ([]PickerMediaItem, error) {
	gp, ok := provider.(*GooglePhotosProvider)
	if !ok {
		return nil, fmt.Errorf("picker enumeration is only supported for the googlephotos provider")
	}
	return gp.GetPickerMediaItems(ctx, sessionID)
}

