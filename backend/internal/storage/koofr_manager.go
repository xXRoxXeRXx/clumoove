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

var (
	_ ManagerConnector        = (*KoofrProvider)(nil)
	_ ManagerLister           = (*KoofrProvider)(nil)
	_ ManagerDownloader       = (*KoofrProvider)(nil)
	_ ManagerUploader         = (*KoofrProvider)(nil)
	_ ManagerDirectoryCreator = (*KoofrProvider)(nil)
	_ ManagerPathResolver     = (*KoofrProvider)(nil)
	_ ManagerThumbnailer      = (*KoofrProvider)(nil)
)

// ConnectManager verifies connectivity to Koofr.
func (p *KoofrProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from Koofr with pagination support.
func (p *KoofrProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	if _, err := p.Connect(ctx); err != nil {
		return ManagerPage{}, err
	}

	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := 0
	if options.Cursor != "" {
		if parsed, err := strconv.Atoi(options.Cursor); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	dirPath, err := normalizeKoofrPath(locator.Path)
	if err != nil {
		return ManagerPage{}, err
	}

	req, err := p.mountRequest(ctx, http.MethodGet, "/files/list", url.Values{"path": {dirPath}}, nil)
	if err != nil {
		return ManagerPage{}, err
	}

	resp, err := p.do(req, http.StatusOK)
	if err != nil {
		return ManagerPage{}, err
	}
	defer resp.Body.Close()

	var listing koofrFileList
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		return ManagerPage{}, fmt.Errorf("decode koofr directory listing: %w", err)
	}

	items := make([]ManagerItem, 0, len(listing.Files))
	for _, file := range listing.Files {
		itemPath, childErr := koofrChildPath(dirPath, file.Name)
		if childErr != nil {
			continue
		}
		isDir := file.Type == "dir"
		var modTime time.Time
		if file.Modified > 0 {
			modTime = time.UnixMilli(file.Modified)
		}

		items = append(items, ManagerItem{
			Locator: ManagerLocator{
				Path: itemPath,
			},
			Name:     file.Name,
			IsDir:    isDir,
			Size:     file.Size,
			Modified: modTime,
		})
	}

	if len(items) > 10000 {
		return ManagerPage{}, ErrManagerDirectoryTooLarge
	}

	totalItems := len(items)
	if offset > totalItems {
		return ManagerPage{Items: nil, NextCursor: ""}, nil
	}

	end := offset + limit
	if end > totalItems {
		end = totalItems
	}

	pageItems := items[offset:end]
	var nextCursor string
	if end < totalItems {
		nextCursor = strconv.Itoa(end)
	}

	return ManagerPage{
		Items:      pageItems,
		NextCursor: nextCursor,
	}, nil
}

// DownloadManager retrieves a file stream directly from Koofr.
func (p *KoofrProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	res, err := p.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("koofr manager download: %w", ErrNotFound)
	}

	stream, err := p.StreamDownload(ctx, "files", locator.Path)
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

// UploadManager uploads a file stream into Koofr with conflict resolution.
func (p *KoofrProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	cleanParent, err := normalizeKoofrPath(parent.Path)
	if err != nil {
		return ManagerUploadResult{}, err
	}
	targetPath, err := koofrChildPath(cleanParent, name)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	existing, err := p.InspectResource(ctx, "files", targetPath)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ManagerUploadResult{}, err
	}

	finalName := name
	if err == nil {
		if existing.IsDir {
			return ManagerUploadResult{}, ErrManagerConflict
		}
		strategy := strings.ToUpper(options.ConflictStrategy)
		switch strategy {
		case "SKIP":
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		case "OVERWRITE":
			// Overwrite continues with targetPath
		case "RENAME":
			ext := path.Ext(name)
			base := strings.TrimSuffix(name, ext)
			foundCandidate := false
			for i := 1; i <= 100; i++ {
				candidateName := fmt.Sprintf("%s (%d)%s", base, i, ext)
				candidatePath, candPathErr := koofrChildPath(cleanParent, candidateName)
				if candPathErr != nil {
					return ManagerUploadResult{}, candPathErr
				}
				_, candErr := p.InspectResource(ctx, "files", candidatePath)
				if errors.Is(candErr, ErrNotFound) {
					targetPath = candidatePath
					finalName = candidateName
					foundCandidate = true
					break
				}
				if candErr != nil {
					return ManagerUploadResult{}, candErr
				}
			}
			if !foundCandidate {
				return ManagerUploadResult{}, fmt.Errorf("could not find unique candidate name after 100 iterations: %w", ErrManagerConflict)
			}
		default:
			return ManagerUploadResult{}, errors.New("invalid manager upload conflict strategy")
		}
	}

	var uploadErr error
	if size > 50*1024*1024 {
		uploadErr = p.StreamUploadChunked(ctx, "files", targetPath, stream, size, nil)
	} else {
		uploadErr = p.StreamUpload(ctx, "files", targetPath, stream, size)
	}

	if uploadErr != nil {
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

// CreateManagerDirectory creates a new folder in Koofr.
func (p *KoofrProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	cleanParent, err := normalizeKoofrPath(parent.Path)
	if err != nil {
		return err
	}
	targetPath, err := koofrChildPath(cleanParent, name)
	if err != nil {
		return err
	}

	_, err = p.InspectResource(ctx, "files", targetPath)
	if err == nil {
		return fmt.Errorf("directory already exists: %w", ErrManagerConflict)
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	return p.CreateDirectory(ctx, "files", targetPath)
}

// ResolveManagerPath resolves a path into stable breadcrumbs and locator.
func (p *KoofrProvider) ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error) {
	clean, err := normalizeKoofrPath(value)
	if err != nil || clean == "/" || clean == "" {
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

// ThumbnailManager retrieves a thumbnail image stream from Koofr for the specified file.
func (p *KoofrProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	if _, err := p.Connect(ctx); err != nil {
		return nil, "", err
	}

	filePath, err := normalizeKoofrFilePath(locator.Path)
	if err != nil {
		return nil, "", err
	}

	thumbSize := "large"
	if width <= 64 && height <= 64 {
		thumbSize = "small"
	} else if width <= 128 && height <= 128 {
		thumbSize = "medium"
	} else if width > 256 || height > 256 {
		thumbSize = "huge"
	}

	query := url.Values{
		"path":  {filePath},
		"thumb": {thumbSize},
	}

	req, err := p.contentMountRequest(ctx, http.MethodGet, "/files/get", query, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "*/*")

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch koofr thumbnail: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("koofr thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("koofr thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnsupportedMediaType {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("koofr thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("koofr thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
