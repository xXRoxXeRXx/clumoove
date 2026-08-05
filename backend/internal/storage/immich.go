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
	"strings"
	"time"
)

// ImmichProvider implements the stable v2 Immich API subset. It deliberately
// uses asset IDs, never filenames, for source operations.
type ImmichProvider struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
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
	return &ImmichProvider{BaseURL: strings.TrimSuffix(u.String(), "/"), APIKey: apiKey, HTTPClient: &http.Client{Transport: tr, CheckRedirect: rejectEgressRedirect}}, nil
}

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
func resourceForAsset(a immichAsset, virtualPath string) CloudResource {
	props := map[string]string{"immich_asset_id": a.ID, "immich_filename": a.OriginalFileName, "immich_mime_type": a.OriginalMimeType, "immich_file_created_at": a.FileCreatedAt, "immich_file_modified_at": a.FileModifiedAt}
	return CloudResource{Path: virtualPath, Name: a.OriginalFileName, Size: a.ExifInfo.FileSizeInByte, Hash: immichHash(a.Checksum), LastModified: parseImmichTime(a.FileModifiedAt), Metadata: FileMetadata{ModifiedTime: parseImmichTime(a.FileModifiedAt), CustomProps: props}}
}
func (p *ImmichProvider) search(ctx context.Context) ([]immichAsset, error) {
	var all []immichAsset
	// Hard cap of 10000 pages × 500 assets = 5M assets. This is the only way to
	// browse the flat library (no folder hierarchy), so for libraries larger than
	// the cap the indexing warning surfaces rather than a silent truncation.
	for page := 1; page <= 10000; page++ {
		query := map[string]any{"page": page, "size": 500, "withArchived": false, "withDeleted": false, "withExif": true}
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
func (p *ImmichProvider) GetDirectoryListing(ctx context.Context, typ, dir string) ([]CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return nil, err
	}
	dir = strings.TrimSuffix(dir, "/")
	if dir != "" && dir != "/" {
		// Immich exposes a single flat library; a non-root path is a single asset
		// fetched directly, avoiding a full-library scan.
		asset, err := p.getAssetByID(ctx, immichAssetID(dir))
		if err != nil {
			return nil, err
		}
		res := resourceForAsset(asset, "/"+asset.ID)
		res.Name = asset.OriginalFileName
		return []CloudResource{res}, nil
	}
	assets, err := p.search(ctx)
	if err != nil {
		return nil, err
	}
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

		res := resourceForAsset(a, "/"+a.ID)
		res.Name = resolvedName
		if res.Metadata.CustomProps != nil {
			res.Metadata.CustomProps["immich_filename"] = resolvedName
		}
		out = append(out, res)
	}
	return out, nil
}
func (p *ImmichProvider) InspectResource(ctx context.Context, typ, resourcePath string) (CloudResource, error) {
	if err := p.checkType(typ); err != nil {
		return CloudResource{}, err
	}
	if resourcePath == "/" || resourcePath == "" {
		return CloudResource{Path: "/", Name: "", IsDir: true}, nil
	}
	// Immich is a flat library keyed by asset ID; a non-root path is an asset
	// fetched directly via GET /assets/{id}, never a recursive library scan.
	asset, err := p.getAssetByID(ctx, immichAssetID(resourcePath))
	if err != nil {
		return CloudResource{}, err
	}
	res := resourceForAsset(asset, "/"+asset.ID)
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
		return immichAsset{}, fmt.Errorf("asset not found")
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
