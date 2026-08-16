package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

var (
	_ ManagerConnector        = (*DropboxProvider)(nil)
	_ ManagerLister           = (*DropboxProvider)(nil)
	_ ManagerDownloader       = (*DropboxProvider)(nil)
	_ ManagerUploader         = (*DropboxProvider)(nil)
	_ ManagerDirectoryCreator = (*DropboxProvider)(nil)
	_ ManagerPathResolver     = (*DropboxProvider)(nil)
	_ ManagerThumbnailer      = (*DropboxProvider)(nil)
)

type dbxManagerEntry struct {
	Tag            string `json:".tag"`
	ID             string `json:"id,omitempty"`
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	Size           int64  `json:"size,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	ServerModified string `json:"server_modified,omitempty"`
}

type dbxManagerListFolderResponse struct {
	Entries []dbxManagerEntry `json:"entries"`
	Cursor  string            `json:"cursor"`
	HasMore bool              `json:"has_more"`
}

// ConnectManager verifies connectivity to Dropbox.
func (p *DropboxProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from Dropbox with native cursor pagination.
func (p *DropboxProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}

	var listResp dbxManagerListFolderResponse
	if options.Cursor != "" {
		reqBody, err := json.Marshal(map[string]any{
			"cursor": options.Cursor,
		})
		if err != nil {
			return ManagerPage{}, err
		}

		req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/list_folder/continue", bytes.NewReader(reqBody))
		if err != nil {
			return ManagerPage{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(ctx)

		resp, err := p.do(ctx, req)
		if err != nil {
			return ManagerPage{}, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			return ManagerPage{}, fmt.Errorf("dropbox manager list continue: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			bodyStr := string(bodyBytes)
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict || strings.Contains(bodyStr, "path/not_found") || strings.Contains(bodyStr, "not_found") {
				return ManagerPage{}, fmt.Errorf("dropbox manager list continue: %w", ErrNotFound)
			}
			return ManagerPage{}, fmt.Errorf("failed to continue folder listing, status: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			return ManagerPage{}, err
		}
	} else {
		pathArg := p.cleanPath(locator.Path)
		reqBody, err := json.Marshal(map[string]any{
			"path":            pathArg,
			"recursive":       false,
			"include_deleted": false,
			"include_mounted": true,
			"limit":           limit,
		})
		if err != nil {
			return ManagerPage{}, err
		}

		req, err := p.newRequest("POST", "https://api.dropboxapi.com/2/files/list_folder", bytes.NewReader(reqBody))
		if err != nil {
			return ManagerPage{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(ctx)

		resp, err := p.do(ctx, req)
		if err != nil {
			return ManagerPage{}, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			return ManagerPage{}, fmt.Errorf("dropbox manager list: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			bodyStr := string(bodyBytes)
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict || strings.Contains(bodyStr, "path/not_found") || strings.Contains(bodyStr, "not_found") {
				return ManagerPage{}, fmt.Errorf("dropbox manager list: %w", ErrNotFound)
			}
			return ManagerPage{}, fmt.Errorf("failed to list folder, status: %d", resp.StatusCode)
		}

		if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
			return ManagerPage{}, err
		}
	}

	items := make([]ManagerItem, 0, len(listResp.Entries))
	for _, entry := range listResp.Entries {
		isDir := entry.Tag == "folder"
		var modTime time.Time
		if entry.ServerModified != "" {
			if t, err := time.Parse(time.RFC3339, entry.ServerModified); err == nil {
				modTime = t
			}
		}

		items = append(items, ManagerItem{
			Locator: ManagerLocator{
				Path:     entry.PathDisplay,
				NativeID: entry.ID,
			},
			Name:     entry.Name,
			IsDir:    isDir,
			Size:     entry.Size,
			Modified: modTime,
		})
	}

	if len(items) > limit {
		items = items[:limit]
	}

	nextCursor := ""
	if listResp.HasMore {
		nextCursor = listResp.Cursor
	}

	return ManagerPage{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

// DownloadManager retrieves a file stream directly from Dropbox.
func (p *DropboxProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	filePath := locator.Path
	if filePath == "" && locator.NativeID != "" {
		filePath = locator.NativeID
	}
	if filePath == "" || filePath == "/" {
		return ManagerDownload{}, fmt.Errorf("dropbox manager download: %w", ErrNotFound)
	}

	var cleanTarget string
	if strings.HasPrefix(filePath, "id:") || strings.HasPrefix(filePath, "rev:") || strings.HasPrefix(filePath, "ns:") {
		cleanTarget = filePath
	} else {
		cleanTarget = p.cleanPath(filePath)
	}

	res, err := p.InspectResource(ctx, "files", cleanTarget)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("dropbox manager download: %w", ErrNotFound)
	}

	stream, err := p.StreamDownload(ctx, "files", cleanTarget)
	if err != nil {
		return ManagerDownload{}, err
	}

	return ManagerDownload{
		Item: ManagerItem{
			Locator:  locator,
			Name:     res.Name,
			IsDir:    false,
			Size:     res.Size,
			Modified: res.LastModified,
		},
		Stream: stream,
	}, nil
}

// uploadManagerStream uploads a file to Dropbox with explicit mode ("add" or "overwrite").
func (p *DropboxProvider) uploadManagerStream(ctx context.Context, filePath string, stream io.Reader, size int64, mode string) error {
	if err := p.CreateParentDirectories(ctx, "files", filePath); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}

	pathArg := p.cleanPath(filePath)
	if size <= 150*1024*1024 {
		uploadArgs := map[string]interface{}{
			"path":       pathArg,
			"mode":       mode,
			"autorename": false,
			"mute":       false,
		}
		if meta, ok := TransferMetadataFromContext(ctx); ok && !meta.ModifiedTime.IsZero() {
			uploadArgs["client_modified"] = meta.ModifiedTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		apiArg, err := escapeAPIArg(uploadArgs)
		if err != nil {
			return err
		}

		req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/upload", stream)
		if err != nil {
			return err
		}
		req.Header.Set("Dropbox-API-Arg", apiArg)
		req.Header.Set("Content-Type", "application/octet-stream")
		if size > 0 {
			req.ContentLength = size
		}
		req = req.WithContext(ctx)

		resp, err := p.do(ctx, req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("dropbox upload: %w", ErrAuth)
		}
		if resp.StatusCode == http.StatusConflict {
			bodyBytes, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(bodyBytes), "path/conflict") {
				return ErrManagerConflict
			}
			return fmt.Errorf("upload conflict: %s", string(bodyBytes))
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("upload failed with status: %d", resp.StatusCode)
		}
		return nil
	}

	return p.StreamUploadChunked(ctx, "files", filePath, stream, size, nil)
}

// UploadManager uploads a file stream into Dropbox with conflict resolution.
func (p *DropboxProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	cleanParent := p.cleanPath(parent.Path)
	targetPath := path.Join(cleanParent, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	existing, err := p.InspectResource(ctx, "files", targetPath)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ManagerUploadResult{}, err
	}

	finalName := name
	finalPath := targetPath
	uploadMode := "add"

	if err == nil {
		if existing.IsDir {
			return ManagerUploadResult{}, ErrManagerConflict
		}
		switch options.ConflictStrategy {
		case "SKIP":
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		case "OVERWRITE":
			uploadMode = "overwrite"
		case "RENAME":
			ext := path.Ext(name)
			base := strings.TrimSuffix(name, ext)
			foundCandidate := false
			for i := 1; i <= 100; i++ {
				candidateName := fmt.Sprintf("%s (%d)%s", base, i, ext)
				candidatePath := path.Join(cleanParent, candidateName)
				if !strings.HasPrefix(candidatePath, "/") {
					candidatePath = "/" + candidatePath
				}
				_, candErr := p.InspectResource(ctx, "files", candidatePath)
				if errors.Is(candErr, ErrNotFound) {
					finalPath = candidatePath
					finalName = candidateName
					foundCandidate = true
					break
				}
				if candErr != nil {
					return ManagerUploadResult{}, candErr
				}
			}
			if !foundCandidate {
				return ManagerUploadResult{}, ErrManagerConflict
			}
		default:
			return ManagerUploadResult{}, errors.New("invalid manager upload conflict strategy")
		}
	}

	if uploadErr := p.uploadManagerStream(ctx, finalPath, stream, size, uploadMode); uploadErr != nil {
		return ManagerUploadResult{}, uploadErr
	}

	status := "uploaded"
	if finalName != name {
		status = "renamed"
	}

	return ManagerUploadResult{
		Status:    status,
		FinalName: finalName,
	}, nil
}

// CreateManagerDirectory creates a new folder in Dropbox.
func (p *DropboxProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	cleanParent := p.cleanPath(parent.Path)
	targetPath := path.Join(cleanParent, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	_, err := p.InspectResource(ctx, "files", targetPath)
	if err == nil {
		return fmt.Errorf("directory already exists: %w", ErrManagerConflict)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	return p.CreateDirectory(ctx, "files", targetPath)
}

// ResolveManagerPath resolves a path to stable breadcrumbs and locator.
func (p *DropboxProvider) ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error) {
	clean := p.cleanPath(value)
	if clean == "" || clean == "/" {
		return ManagerLocator{Path: "/"}, []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}, false, nil
	}

	parts := strings.Split(strings.Trim(clean, "/"), "/")
	breadcrumbs = []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}
	currentPath := ""
	for _, segment := range parts {
		if segment == "" {
			continue
		}
		currentPath += "/" + segment
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{
			Name:    segment,
			Locator: ManagerLocator{Path: currentPath},
		})
	}
	return ManagerLocator{Path: currentPath}, breadcrumbs, false, nil
}

// mapDropboxThumbnailSize maps requested dimensions to the closest Dropbox thumbnail size string.
func mapDropboxThumbnailSize(width, height int) string {
	maxDim := width
	if height > maxDim {
		maxDim = height
	}
	switch {
	case maxDim <= 32:
		return "w32h32"
	case maxDim <= 64:
		return "w64h64"
	case maxDim <= 128:
		return "w128h128"
	case maxDim <= 256:
		return "w256h256"
	case maxDim <= 480:
		return "w480h320"
	case maxDim <= 640:
		return "w640h480"
	case maxDim <= 960:
		return "w960h640"
	default:
		return "w1024h768"
	}
}

// ThumbnailManager retrieves a thumbnail image stream from Dropbox for the specified file.
func (p *DropboxProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	if width <= 0 {
		width = 256
	}
	if height <= 0 {
		height = 256
	}
	if width > 1024 {
		width = 1024
	}
	if height > 1024 {
		height = 1024
	}

	sizeStr := mapDropboxThumbnailSize(width, height)

	targetResource := locator.Path
	if targetResource == "" && locator.NativeID != "" {
		targetResource = locator.NativeID
	}
	if targetResource == "" || targetResource == "/" {
		return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrNotFound)
	}

	var cleanTarget string
	if strings.HasPrefix(targetResource, "id:") || strings.HasPrefix(targetResource, "rev:") || strings.HasPrefix(targetResource, "ns:") {
		cleanTarget = targetResource
	} else {
		cleanTarget = p.cleanPath(targetResource)
	}

	argJSON, err := escapeAPIArg(map[string]any{
		"resource": map[string]any{
			".tag": "path",
			"path": cleanTarget,
		},
		"format": "jpeg",
		"size":   sizeStr,
		"mode":   "strict",
	})
	if err != nil {
		return nil, "", err
	}

	req, err := p.newRequest("POST", "https://content.dropboxapi.com/2/files/get_thumbnail_v2", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Dropbox-API-Arg", argJSON)
	req = req.WithContext(ctx)

	resp, err := p.do(ctx, req)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusBadRequest {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		bodyStr := string(bodyBytes)
		if strings.Contains(bodyStr, "unsupported_extension") || strings.Contains(bodyStr, "conversion_error") || strings.Contains(bodyStr, "unsupported_image") {
			return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrUnsupportedMedia)
		}
		if strings.Contains(bodyStr, "not_found") {
			return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrNotFound)
		}
		return nil, "", fmt.Errorf("dropbox thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("dropbox thumbnail failed, status: %d", resp.StatusCode)
	}

	return resp.Body, "image/jpeg", nil
}
