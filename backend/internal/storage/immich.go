package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// ImmichProvider implements the stable v2 Immich API subset. It deliberately
// uses asset IDs, never filenames, for source operations.
type ImmichProvider struct {
	BaseURL      string
	APIKey       string
	HTTPClient   *http.Client
	albumsMu     sync.RWMutex
	albums       map[string]string // ID -> display name
	albumIDs     map[string]string // display name -> ID
	albumsLoaded bool
}

func NewImmichProvider(baseURL, apiKey string) (*ImmichProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("immich API key required: %w", ErrAuth)
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme == "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid Immich URL")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api") + "/api"
	u.RawQuery, u.Fragment = "", ""
	tr := &http.Transport{DialContext: egressDialer(u.Hostname()), ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	return &ImmichProvider{BaseURL: strings.TrimSuffix(u.String(), "/"), APIKey: apiKey, HTTPClient: &http.Client{Transport: tr, CheckRedirect: validateEgressRedirect}, albums: make(map[string]string), albumIDs: make(map[string]string)}, nil
}

func (p *ImmichProvider) Close() error                       { p.HTTPClient.CloseIdleConnections(); return nil }
func (p *ImmichProvider) SupportsAtomicRename() bool         { return false }
func (p *ImmichProvider) UsesNativeDuplicateDetection() bool { return true }
func (p *ImmichProvider) checkType(t string) error {
	if t != "files" {
		return fmt.Errorf("immich only supports files")
	}
	return nil
}
func (p *ImmichProvider) request(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.APIKey)
	return p.HTTPClient.Do(req)
}
func (p *ImmichProvider) requestJSON(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.BaseURL+endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	return p.HTTPClient.Do(req)
}
func immichStatus(resp *http.Response, action string) error {
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("immich %s: %w", action, ErrAuth)
	}
	return fmt.Errorf("immich %s failed with status %d", action, resp.StatusCode)
}
func (p *ImmichProvider) Connect(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := p.request(ctx, "GET", "/api-keys/me", nil)
	if err != nil {
		return false, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return false, immichStatus(r, "connect")
	}
	return true, nil
}

type immichAsset struct {
	ID               string `json:"id"`
	OriginalFileName string `json:"originalFileName"`
	OriginalMimeType string `json:"originalMimeType"`
	Checksum         string `json:"checksum"`
	FileCreatedAt    string `json:"fileCreatedAt"`
	FileModifiedAt   string `json:"fileModifiedAt"`
	OriginalPath     string `json:"originalPath"`
	IsTrashed        bool   `json:"isTrashed"`
	ExifInfo         struct {
		FileSizeInByte int64 `json:"fileSizeInByte"`
	} `json:"exifInfo"`
}
type immichAlbum struct {
	ID        string `json:"id"`
	AlbumName string `json:"albumName"`
}

func parseImmichTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
func immichHash(encoded string) string {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return "SHA1:" + fmt.Sprintf("%x", b)
}
func resourceForAsset(a immichAsset, virtualPath, albumID, albumName string) CloudResource {
	props := map[string]string{"immich_asset_id": a.ID, "immich_filename": a.OriginalFileName, "immich_mime_type": a.OriginalMimeType, "immich_file_created_at": a.FileCreatedAt, "immich_file_modified_at": a.FileModifiedAt}
	if albumID != "" {
		props["immich_album_id"] = albumID
		props["immich_album_name"] = albumName
	}
	return CloudResource{Path: virtualPath, Name: a.OriginalFileName, Size: a.ExifInfo.FileSizeInByte, Hash: immichHash(a.Checksum), LastModified: parseImmichTime(a.FileModifiedAt), Metadata: FileMetadata{ModifiedTime: parseImmichTime(a.FileModifiedAt), CustomProps: props}}
}
func (p *ImmichProvider) search(ctx context.Context, albumID string) ([]immichAsset, error) {
	var all []immichAsset
	for page := 1; page <= 10000; page++ {
		query := map[string]any{"page": page, "size": 500, "withArchived": false, "withDeleted": false, "withExif": true}
		if albumID != "" {
			query["albumId"] = albumID
		}
		body, err := json.Marshal(query)
		if err != nil {
			return nil, err
		}
		r, err := p.requestJSON(ctx, "POST", "/search/metadata", body)
		if err != nil {
			return nil, err
		}
		if r.StatusCode != 200 {
			r.Body.Close()
			return nil, immichStatus(r, "search")
		}
		var out struct {
			Assets struct {
				Items    []immichAsset   `json:"items"`
				NextPage json.RawMessage `json:"nextPage"`
			} `json:"assets"`
		}
		err = json.NewDecoder(r.Body).Decode(&out)
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, a := range out.Assets.Items {
			if !a.IsTrashed {
				all = append(all, a)
			}
		}
		if len(out.Assets.NextPage) == 0 || string(out.Assets.NextPage) == "null" || string(out.Assets.NextPage) == `""` || len(out.Assets.Items) == 0 {
			return all, nil
		}
	}
	return nil, fmt.Errorf("immich search pagination limit exceeded")
}
func (p *ImmichProvider) listAlbums(ctx context.Context) ([]immichAlbum, error) {
	r, err := p.request(ctx, "GET", "/albums", nil)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, immichStatus(r, "albums")
	}
	var a []immichAlbum
	err = json.NewDecoder(r.Body).Decode(&a)
	return a, err
}
func (p *ImmichProvider) refreshAlbums(ctx context.Context) error {
	p.albumsMu.Lock()
	defer p.albumsMu.Unlock()
	if p.albumsLoaded {
		return nil
	}
	albums, err := p.listAlbums(ctx)
	if err != nil {
		return err
	}
	for _, album := range albums {
		p.albums[album.ID] = album.AlbumName
		p.albumIDs[album.AlbumName] = album.ID
	}
	p.albumsLoaded = true
	return nil
}

func (p *ImmichProvider) rememberAlbum(id, name string) {
	p.albumsMu.Lock()
	p.albums[id] = name
	p.albumIDs[name] = id
	p.albumsMu.Unlock()
}
func (p *ImmichProvider) GetDirectoryListing(ctx context.Context, typ, dir string) ([]CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return nil, err
	}
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		dir = "/"
	}
	if dir == "/" {
		return []CloudResource{{Path: "/All Assets", Name: "All Assets", IsDir: true}, {Path: "/Albums", Name: "Albums", IsDir: true}}, nil
	}
	if dir == "/Albums" {
		if err := p.refreshAlbums(ctx); err != nil {
			return nil, err
		}
		p.albumsMu.RLock()
		out := make([]CloudResource, 0, len(p.albums))
		for id, name := range p.albums {
			out = append(out, CloudResource{Path: "/Albums/" + id, Name: name, IsDir: true, Metadata: FileMetadata{CustomProps: map[string]string{"immich_album_id": id, "immich_album_name": name}}})
		}
		p.albumsMu.RUnlock()
		return out, nil
	}
	var albumID string
	switch {
	case dir == "/All Assets":
		// All assets is a valid virtual directory without an album filter.
	case strings.HasPrefix(dir, "/Albums/") && len(strings.Split(strings.Trim(dir, "/"), "/")) == 2:
		albumID = path.Base(dir)
	default:
		return nil, fmt.Errorf("invalid Immich virtual path")
	}
	assets, err := p.search(ctx, albumID)
	if err != nil {
		return nil, err
	}
	out := make([]CloudResource, 0, len(assets))
	for _, a := range assets {
		vp := "/All Assets/" + a.ID
		if albumID != "" {
			vp = "/Albums/" + albumID + "/" + a.ID
		}
		p.albumsMu.RLock()
		albumName := p.albums[albumID]
		p.albumsMu.RUnlock()
		out = append(out, resourceForAsset(a, vp, albumID, albumName))
	}
	return out, nil
}
func (p *ImmichProvider) InspectResource(ctx context.Context, typ, resourcePath string) (CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return CloudResource{}, err
	}
	if resourcePath == "/" || resourcePath == "/All Assets" || resourcePath == "/Albums" {
		return CloudResource{Path: resourcePath, Name: path.Base(resourcePath), IsDir: true}, nil
	}
	if strings.HasPrefix(resourcePath, "/Albums/") && len(strings.Split(strings.Trim(resourcePath, "/"), "/")) == 2 {
		// Listing /Albums warms this cache during normal BFS indexing; refreshAlbums
		// returns without I/O when it is already loaded.
		if err := p.refreshAlbums(ctx); err != nil {
			return CloudResource{}, err
		}
		albumID := path.Base(resourcePath)
		p.albumsMu.RLock()
		albumName := p.albums[albumID]
		p.albumsMu.RUnlock()
		return CloudResource{Path: resourcePath, Name: albumName, IsDir: true, Metadata: FileMetadata{CustomProps: map[string]string{"immich_album_id": albumID, "immich_album_name": albumName}}}, nil
	}
	parent := path.Dir(resourcePath)
	items, err := p.GetDirectoryListing(ctx, typ, parent)
	if err != nil {
		return CloudResource{}, err
	}
	for _, i := range items {
		if i.Path == resourcePath {
			return i, nil
		}
	}
	return CloudResource{}, fmt.Errorf("asset not found")
}
func immichAssetID(filePath string) string { return path.Base(strings.TrimSuffix(filePath, "/")) }
func (p *ImmichProvider) StreamDownload(ctx context.Context, typ, filePath string) (io.ReadCloser, error) {
	if err := p.checkType(typ); err != nil {
		return nil, err
	}
	r, err := p.request(ctx, "GET", "/assets/"+url.PathEscape(immichAssetID(filePath))+"/original?edited=false", nil)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != 200 {
		r.Body.Close()
		return nil, immichStatus(r, "download")
	}
	return r.Body, nil
}
func (p *ImmichProvider) StreamUpload(ctx context.Context, typ, filePath string, stream io.Reader, size int64) error {
	if err := p.checkType(typ); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	meta, _ := TransferMetadataFromContext(ctx)
	createdAt := meta.CustomProps["immich_file_created_at"]
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	modifiedAt := meta.CustomProps["immich_file_modified_at"]
	if modifiedAt == "" {
		modifiedAt = createdAt
	}
	filename := meta.CustomProps["immich_filename"]
	if filename == "" {
		filename = path.Base(filePath)
	}
	go func() {
		defer pw.Close()
		defer mw.Close()
		_ = mw.WriteField("fileCreatedAt", createdAt)
		_ = mw.WriteField("fileModifiedAt", modifiedAt)
		_ = mw.WriteField("filename", filename)
		part, err := mw.CreateFormFile("assetData", filename)
		if err == nil {
			_, err = io.Copy(part, stream)
		}
		if err != nil {
			_ = pw.CloseWithError(err)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+"/assets", pr)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if checksum, _ := ctx.Value("oc-checksum").(string); strings.HasPrefix(checksum, "SHA1:") {
		req.Header.Set("x-immich-checksum", strings.TrimPrefix(checksum, "SHA1:"))
	}
	r, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 201 {
		return immichStatus(r, "upload")
	}
	var asset struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&asset); err != nil {
		return err
	}
	if asset.ID != "" {
		if err := p.assignTargetAlbum(ctx, path.Dir(filePath), asset.ID); err != nil {
			return err
		}
	}
	if r.StatusCode == 200 {
		return ErrNativeDuplicate
	}
	return nil
}

func (p *ImmichProvider) assignTargetAlbum(ctx context.Context, dir, assetID string) error {
	name := strings.Trim(dir, "/")
	if name == "" {
		return nil
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("Immich albums cannot be nested")
	}
	if err := p.refreshAlbums(ctx); err != nil {
		return err
	}
	p.albumsMu.RLock()
	albumID := p.albumIDs[name]
	p.albumsMu.RUnlock()
	if albumID == "" {
		body, _ := json.Marshal(map[string]string{"albumName": name})
		resp, err := p.requestJSON(ctx, "POST", "/albums", body)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 && resp.StatusCode != 201 {
			resp.Body.Close()
			return immichStatus(resp, "create album")
		}
		var created immichAlbum
		err = json.NewDecoder(resp.Body).Decode(&created)
		resp.Body.Close()
		if err != nil {
			return err
		}
		albumID = created.ID
		p.rememberAlbum(created.ID, name)
	}
	body, _ := json.Marshal(map[string][]string{"ids": {assetID}})
	resp, err := p.requestJSON(ctx, "PUT", "/albums/"+url.PathEscape(albumID)+"/assets", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
		return immichStatus(resp, "add album asset")
	}
	return nil
}
func (p *ImmichProvider) StreamUploadChunked(ctx context.Context, t, filePath string, stream io.Reader, size int64, progress chan<- int64) error {
	return p.StreamUpload(ctx, t, filePath, &ProgressReader{Reader: stream, ProgressChan: progress}, size)
}
func (p *ImmichProvider) FileExists(context.Context, string, string) (bool, int64, error) {
	return false, 0, nil
}
func (p *ImmichProvider) DeleteFile(context.Context, string, string) error {
	return errors.New("Immich does not support filename deletion")
}
func (p *ImmichProvider) GetFileHash(context.Context, string, string) (string, error) {
	return "", ErrHashNotSupported
}
func (p *ImmichProvider) CreateParentDirectories(context.Context, string, string) error { return nil }
func (p *ImmichProvider) CreateDirectory(ctx context.Context, typ, dir string) error {
	if err := p.checkType(typ); err != nil {
		return err
	}
	name := strings.Trim(strings.TrimSpace(dir), "/")
	if name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("Immich albums cannot be nested")
	}
	body, _ := json.Marshal(map[string]string{"albumName": name})
	r, err := p.requestJSON(ctx, "POST", "/albums", body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 201 {
		return immichStatus(r, "create album")
	}
	var created immichAlbum
	if err := json.NewDecoder(r.Body).Decode(&created); err == nil && created.ID != "" {
		p.rememberAlbum(created.ID, name)
	}
	return nil
}
func (p *ImmichProvider) RenameFile(context.Context, string, string, string) error {
	return errors.New("Immich does not support asset rename")
}
