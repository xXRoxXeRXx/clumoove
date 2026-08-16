package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	_ ManagerConnector    = (*ImmichProvider)(nil)
	_ ManagerLister       = (*ImmichProvider)(nil)
	_ ManagerDownloader   = (*ImmichProvider)(nil)
	_ ManagerPathResolver = (*ImmichProvider)(nil)
	_ ManagerThumbnailer  = (*ImmichProvider)(nil)
)

// ConnectManager verifies connectivity to Immich.
func (p *ImmichProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists assets from Immich with pagination support.
func (p *ImmichProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	if locator.Path != "" && locator.Path != "/" {
		return ManagerPage{}, fmt.Errorf("immich manager list: %w", ErrNotFound)
	}

	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	page := 1
	if options.Cursor != "" {
		if parsed, err := strconv.Atoi(options.Cursor); err == nil && parsed > 0 {
			page = parsed
		}
	}

	query := map[string]any{
		"page":         page,
		"size":         limit,
		"withArchived": false,
		"withDeleted":  false,
		"withExif":     true,
	}
	body, err := json.Marshal(query)
	if err != nil {
		return ManagerPage{}, err
	}

	resp, err := p.requestJSON(ctx, "POST", "/search/metadata", body)
	if err != nil {
		return ManagerPage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ManagerPage{}, fmt.Errorf("immich manager list: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ManagerPage{}, fmt.Errorf("immich manager list: %w", ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return ManagerPage{}, fmt.Errorf("immich manager list failed, status: %d", resp.StatusCode)
	}

	var out struct {
		Assets struct {
			Items    []immichAsset   `json:"items"`
			NextPage json.RawMessage `json:"nextPage"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ManagerPage{}, err
	}

	items := make([]ManagerItem, 0, len(out.Assets.Items))
	for _, a := range out.Assets.Items {
		if a.IsTrashed {
			continue
		}
		origName := a.OriginalFileName
		if origName == "" {
			origName = a.ID
		}
		var modTime time.Time
		if a.FileModifiedAt != "" {
			modTime = parseImmichTime(a.FileModifiedAt)
		}

		items = append(items, ManagerItem{
			Locator: ManagerLocator{
				Path:     "/" + a.ID,
				NativeID: a.ID,
			},
			Name:     origName,
			IsDir:    false,
			Size:     a.ExifInfo.FileSizeInByte,
			Modified: modTime,
			MIMEType: a.OriginalMimeType,
		})
	}

	nextCursor := ""
	if len(out.Assets.NextPage) > 0 && string(out.Assets.NextPage) != "null" && string(out.Assets.NextPage) != `""` && len(out.Assets.Items) > 0 {
		nextCursor = strconv.Itoa(page + 1)
	}

	return ManagerPage{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

// DownloadManager retrieves an original asset stream directly from Immich.
func (p *ImmichProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	assetID := locator.NativeID
	if assetID == "" {
		assetID = immichAssetID(locator.Path)
	}
	if assetID == "" || assetID == "/" {
		return ManagerDownload{}, fmt.Errorf("immich manager download: %w", ErrNotFound)
	}

	asset, err := p.getAssetByID(ctx, assetID)
	if err != nil {
		return ManagerDownload{}, err
	}

	stream, err := p.StreamDownload(ctx, "files", "/"+asset.ID)
	if err != nil {
		return ManagerDownload{}, err
	}

	name := asset.OriginalFileName
	if name == "" {
		name = asset.ID
	}

	return ManagerDownload{
		Item: ManagerItem{
			Locator:  ManagerLocator{Path: "/" + asset.ID, NativeID: asset.ID},
			Name:     name,
			IsDir:    false,
			Size:     asset.ExifInfo.FileSizeInByte,
			Modified: parseImmichTime(asset.FileModifiedAt),
			MIMEType: asset.OriginalMimeType,
		},
		Stream: stream,
	}, nil
}

// ResolveManagerPath resolves an asset path or root to manager breadcrumbs.
func (p *ImmichProvider) ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error) {
	clean := strings.TrimSpace(value)
	if clean == "" || clean == "/" {
		return ManagerLocator{Path: "/"}, []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}, false, nil
	}

	assetID := immichAssetID(clean)
	_, err = p.getAssetByID(ctx, assetID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ManagerLocator{Path: "/"}, []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}, true, nil
		}
		return ManagerLocator{}, nil, false, err
	}

	// Since Immich is a flat assets library without folders, resolve non-root values to root locator with fallback
	return ManagerLocator{Path: "/"}, []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}, true, nil
}

// ThumbnailManager retrieves a thumbnail image stream from Immich for the specified asset.
func (p *ImmichProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	assetID := locator.NativeID
	if assetID == "" {
		assetID = immichAssetID(locator.Path)
	}
	if assetID == "" || assetID == "/" {
		return nil, "", fmt.Errorf("immich thumbnail: %w", ErrNotFound)
	}

	sizeParam := "thumbnail"
	if width > 256 || height > 256 {
		sizeParam = "preview"
	}

	endpoint := fmt.Sprintf("/assets/%s/thumbnail?size=%s", url.PathEscape(assetID), sizeParam)
	resp, err := p.request(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("immich thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("immich thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnsupportedMediaType {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("immich thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("immich thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
