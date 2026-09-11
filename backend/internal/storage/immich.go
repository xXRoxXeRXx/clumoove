package storage

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"sort"
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
	albums       map[string]string // id -> albumName
	albumsLoaded bool
}

func NewImmichProvider(baseURL, apiKey string) (*ImmichProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("immich API key required: %w", ErrAuth)
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid Immich URL: must be an absolute HTTPS URL with a host")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api") + "/api"
	u.RawQuery, u.Fragment = "", ""
	tr := &http.Transport{DialContext: egressDialer(u.Hostname()), ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	return &ImmichProvider{
		BaseURL:    strings.TrimSuffix(u.String(), "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{Transport: newUserAgentTransport(tr), CheckRedirect: rejectEgressRedirect},
		albums:     make(map[string]string),
	}, nil
}

// Note: ImmichProvider intentionally does not implement MetadataApplier. Immich receives
// asset metadata (fileCreatedAt, fileModifiedAt) inline during StreamUpload (POST /assets),
// so no post-upload metadata step is required.
func (p *ImmichProvider) Close() error                       { p.HTTPClient.CloseIdleConnections(); return nil }
func (p *ImmichProvider) SupportsAtomicRename() bool         { return false }
func (p *ImmichProvider) VerificationMode() VerificationMode { return VerificationCryptographicHash }
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

func parseImmichTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
func immichHash(encoded string) string {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(b) != sha1.Size {
		return ""
	}
	return "SHA1:" + fmt.Sprintf("%x", b)
}

func (p *ImmichProvider) lookupVerificationAsset(ctx context.Context, typ string) (immichAsset, bool, error) {
	if err := p.checkType(typ); err != nil {
		return immichAsset{}, false, err
	}
	assetID, ok := TargetResourceIDFromContext(ctx)
	if !ok {
		return immichAsset{}, false, fmt.Errorf("immich target asset ID unavailable for verification")
	}
	r, err := p.request(ctx, http.MethodGet, "/assets/"+url.PathEscape(assetID), nil)
	if err != nil {
		return immichAsset{}, false, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return immichAsset{}, false, nil
	}
	if r.StatusCode != http.StatusOK {
		return immichAsset{}, false, immichStatus(r, "get asset")
	}
	var asset immichAsset
	if err := json.NewDecoder(r.Body).Decode(&asset); err != nil {
		return immichAsset{}, false, err
	}
	return asset, true, nil
}
type immichAlbum struct {
	ID        string `json:"id"`
	AlbumName string `json:"albumName"`
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

// refreshAlbums caches all albums for the lifetime of this provider.
// Providers are single-use per migration task.
func (p *ImmichProvider) refreshAlbums(ctx context.Context) error {
	p.albumsMu.RLock()
	loaded := p.albumsLoaded
	p.albumsMu.RUnlock()
	if loaded {
		return nil
	}

	albums, err := p.listAlbums(ctx)
	if err != nil {
		return err
	}

	p.albumsMu.Lock()
	defer p.albumsMu.Unlock()
	for _, album := range albums {
		p.albums[album.ID] = album.AlbumName
	}
	p.albumsLoaded = true
	return nil
}

func resourceForAsset(a immichAsset, virtualPath string) CloudResource {
	props := map[string]string{
		"immich_asset_id":         a.ID,
		"immich_filename":         a.OriginalFileName,
		"immich_mime_type":        a.OriginalMimeType,
		"immich_file_created_at":  a.FileCreatedAt,
		"immich_file_modified_at": a.FileModifiedAt,
	}
	return CloudResource{
		Path:         virtualPath,
		Name:         a.OriginalFileName,
		Size:         a.ExifInfo.FileSizeInByte,
		Hash:         immichHash(a.Checksum),
		LastModified: parseImmichTime(a.FileModifiedAt),
		Metadata:     FileMetadata{ModifiedTime: parseImmichTime(a.FileModifiedAt), CustomProps: props},
	}
}

func (p *ImmichProvider) search(ctx context.Context, albumID string) ([]immichAsset, error) {
	var all []immichAsset
	// Hard cap of 10000 pages × 500 assets = 5M assets. This is the only way to
	// browse the flat library (no folder hierarchy), so for libraries larger than
	// the cap the indexing warning surfaces rather than a silent truncation.
	for page := 1; page <= 10000; page++ {
		query := map[string]any{"page": page, "size": 500, "withArchived": false, "withDeleted": false, "withExif": true}
		if albumID != "" {
			query["albumIds"] = []string{albumID}
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

func resolveAssetResources(assets []immichAsset, kind, albumID, albumName string) []CloudResource {
	seenNames := make(map[string]int)
	for _, a := range assets {
		origName := a.OriginalFileName
		if origName == "" {
			origName = a.ID
		}
		seenNames[origName]++
	}

	out := make([]CloudResource, 0, len(assets))
	usedNames := make(map[string]bool)
	for _, a := range assets {
		origName := a.OriginalFileName
		if origName == "" {
			origName = a.ID
		}
		resolvedName := origName
		if seenNames[origName] > 1 {
			ext := path.Ext(origName)
			stem := strings.TrimSuffix(origName, ext)
			shortID := a.ID
			if len(shortID) > 8 {
				shortID = shortID[:8]
			}
			resolvedName = fmt.Sprintf("%s_%s%s", stem, shortID, ext)
		}

		if usedNames[resolvedName] {
			ext := path.Ext(resolvedName)
			stem := strings.TrimSuffix(resolvedName, ext)
			suffix := 1
			candidate := fmt.Sprintf("%s_%d%s", stem, suffix, ext)
			for usedNames[candidate] {
				suffix++
				candidate = fmt.Sprintf("%s_%d%s", stem, suffix, ext)
			}
			resolvedName = candidate
		}
		usedNames[resolvedName] = true

		virtualPath := "/Library/" + a.ID
		if kind == "album" {
			virtualPath = "/Albums/" + albumID + "/" + a.ID
		}

		res := resourceForAsset(a, virtualPath)
		res.Name = resolvedName
		if res.Metadata.CustomProps == nil {
			res.Metadata.CustomProps = make(map[string]string)
		}
		res.Metadata.CustomProps["immich_filename"] = resolvedName
		res.Metadata.CustomProps["immich_source_kind"] = kind

		if kind == "album" {
			res.Metadata.CustomProps["immich_album_id"] = albumID
			res.Metadata.CustomProps["immich_album_name"] = albumName
		} else {
			createdAt := parseImmichTime(a.FileCreatedAt)
			if createdAt.IsZero() {
				createdAt = parseImmichTime(a.FileModifiedAt)
			}
			if !createdAt.IsZero() {
				res.Metadata.CustomProps["immich_year"] = createdAt.Format("2006")
				res.Metadata.CustomProps["immich_month"] = createdAt.Format("01")
			}
			// When no date is available, omit both keys so the processor falls back
			// to a flat filename rather than creating an "Unknown/Unknown/" hierarchy.
		}

		out = append(out, res)
	}
	return out
}

func (p *ImmichProvider) GetDirectoryListing(ctx context.Context, typ, dir string) ([]CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return nil, err
	}
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || dir == "/" {
		return []CloudResource{
			{Path: "/Library", Name: "Gesamte Mediathek", IsDir: true},
			{Path: "/Albums", Name: "Alben", IsDir: true},
		}, nil
	}
	if dir == "/Albums" {
		if err := p.refreshAlbums(ctx); err != nil {
			return nil, err
		}
		p.albumsMu.RLock()
		out := make([]CloudResource, 0, len(p.albums))
		for id, name := range p.albums {
			out = append(out, CloudResource{
				Path:  "/Albums/" + id,
				Name:  name,
				IsDir: true,
				Metadata: FileMetadata{
					CustomProps: map[string]string{
						"immich_album_id":   id,
						"immich_album_name": name,
					},
				},
			})
		}
		p.albumsMu.RUnlock()
		sort.Slice(out, func(i, j int) bool {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
		return out, nil
	}
	if dir == "/Library" {
		assets, err := p.search(ctx, "")
		if err != nil {
			return nil, err
		}
		return resolveAssetResources(assets, "library", "", ""), nil
	}
	if strings.HasPrefix(dir, "/Albums/") && strings.Count(strings.Trim(dir, "/"), "/") == 1 {
		albumID := path.Base(dir)
		if err := p.refreshAlbums(ctx); err != nil {
			return nil, err
		}
		p.albumsMu.RLock()
		albumName := p.albums[albumID]
		p.albumsMu.RUnlock()
		if albumName == "" {
			albumName = albumID
		}
		assets, err := p.search(ctx, albumID)
		if err != nil {
			return nil, err
		}
		return resolveAssetResources(assets, "album", albumID, albumName), nil
	}

	// Immich exposes a single flat library; a non-root path is a single asset
	// fetched directly, avoiding a full-library scan.
	asset, err := p.getAssetByID(ctx, immichAssetID(dir))
	if err != nil {
		return nil, err
	}
	res := resourceForAsset(asset, dir)
	res.Name = asset.OriginalFileName
	return []CloudResource{res}, nil
}

func (p *ImmichProvider) InspectResource(ctx context.Context, typ, resourcePath string) (CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return CloudResource{}, err
	}
	if resourcePath == "/" || resourcePath == "" {
		return CloudResource{Path: "/", Name: "", IsDir: true}, nil
	}
	if resourcePath == "/Library" {
		return CloudResource{Path: "/Library", Name: "Gesamte Mediathek", IsDir: true}, nil
	}
	if resourcePath == "/Albums" {
		return CloudResource{Path: "/Albums", Name: "Alben", IsDir: true}, nil
	}
	if strings.HasPrefix(resourcePath, "/Albums/") && strings.Count(strings.Trim(resourcePath, "/"), "/") == 1 {
		albumID := path.Base(resourcePath)
		if err := p.refreshAlbums(ctx); err != nil {
			return CloudResource{}, err
		}
		p.albumsMu.RLock()
		albumName := p.albums[albumID]
		p.albumsMu.RUnlock()
		if albumName == "" {
			albumName = albumID
		}
		return CloudResource{
			Path:  resourcePath,
			Name:  albumName,
			IsDir: true,
			Metadata: FileMetadata{
				CustomProps: map[string]string{
					"immich_album_id":   albumID,
					"immich_album_name": albumName,
				},
			},
		}, nil
	}

	asset, err := p.getAssetByID(ctx, immichAssetID(resourcePath))
	if err != nil {
		return CloudResource{}, err
	}
	res := resourceForAsset(asset, resourcePath)
	res.Name = asset.OriginalFileName
	return res, nil
}

// getAssetByID fetches a single asset via the stable v2 endpoint. It is the
// cheap O(1) alternative to search() and is used by InspectResource and the
// non-root directory listing so callers never trigger a paginated library scan.
func (p *ImmichProvider) getAssetByID(ctx context.Context, assetID string) (immichAsset, error) {
	r, err := p.request(ctx, http.MethodGet, "/assets/"+url.PathEscape(assetID), nil)
	if err != nil {
		return immichAsset{}, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotFound {
		return immichAsset{}, fmt.Errorf("immich asset: %w", ErrNotFound)
	}
	if r.StatusCode != http.StatusOK {
		return immichAsset{}, immichStatus(r, "get asset")
	}
	var asset immichAsset
	if err := json.NewDecoder(r.Body).Decode(&asset); err != nil {
		return immichAsset{}, err
	}
	return asset, nil
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
	if createdAt == "" && !meta.ModifiedTime.IsZero() {
		createdAt = meta.ModifiedTime.UTC().Format(time.RFC3339)
	}
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	modifiedAt := meta.CustomProps["immich_file_modified_at"]
	if modifiedAt == "" && !meta.ModifiedTime.IsZero() {
		modifiedAt = meta.ModifiedTime.UTC().Format(time.RFC3339)
	}
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
		_ = pr.Close()
		return err
	}
	req.Header.Set("x-api-key", p.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if checksum := UploadChecksum(ctx); strings.HasPrefix(checksum, "SHA1:") {
		// This is an optional duplicate-detection hint. Immich v2 does not
		// require the header, so correctness never depends on it being honored.
		req.Header.Set("x-immich-checksum", strings.TrimPrefix(checksum, "SHA1:"))
	}
	r, err := p.HTTPClient.Do(req)
	if err != nil {
		// io.Pipe is unbuffered. Closing the read side releases the multipart
		// writer goroutine (and any source stream it would otherwise retain).
		_ = pr.Close()
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 201 {
		return immichStatus(r, "upload")
	}
	var asset struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&asset); err != nil {
		return err
	}
	asset.ID = strings.TrimSpace(asset.ID)
	if asset.ID == "" {
		return fmt.Errorf("immich upload response missing asset ID")
	}
	if r.StatusCode == http.StatusOK || asset.Status == "duplicate" {
		return ErrNativeDuplicate
	}
	if asset.Status != "" && asset.Status != "created" {
		return fmt.Errorf("immich upload returned unexpected status %q", asset.Status)
	}
	if receipt, ok := UploadReceiptFromContext(ctx); ok {
		receipt.TargetResourceID = asset.ID
	}
	return nil
}

func (p *ImmichProvider) StreamUploadChunked(ctx context.Context, t, filePath string, stream io.Reader, size int64, progress chan<- int64) error {
	return p.StreamUpload(ctx, t, filePath, &ProgressReader{Reader: stream, ProgressChan: progress}, size)
}
func (p *ImmichProvider) FileExists(ctx context.Context, typ, _ string) (bool, int64, error) {
	asset, found, err := p.lookupVerificationAsset(ctx, typ)
	if err != nil || !found || asset.IsTrashed {
		return false, 0, err
	}
	return true, asset.ExifInfo.FileSizeInByte, nil
}
func (p *ImmichProvider) DeleteFile(context.Context, string, string) error {
	return errors.New("Immich does not support filename deletion")
}
func (p *ImmichProvider) GetFileHash(ctx context.Context, typ, _ string) (string, error) {
	if err := p.checkType(typ); err != nil {
		return "", err
	}
	if _, ok := TargetResourceIDFromContext(ctx); !ok {
		return "", ErrChecksumNotAvailable
	}
	asset, found, err := p.lookupVerificationAsset(ctx, typ)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotFound
	}
	if hash := immichHash(asset.Checksum); hash != "" {
		return hash, nil
	}
	return "", ErrChecksumNotAvailable
}
func (p *ImmichProvider) CreateParentDirectories(context.Context, string, string) error { return nil }

// CreateDirectory is a no-op: Immich has no folder/album hierarchy and all assets
// land directly in the library root. The indexer/processor may still emit mkdir
// tasks for source subfolders, but the target never depends on such a path
// existing, so silently succeeding is correct here.
func (p *ImmichProvider) CreateDirectory(context.Context, string, string) error {
	return nil
}
func (p *ImmichProvider) RenameFile(context.Context, string, string, string) error {
	return errors.New("Immich does not support asset rename")
}
