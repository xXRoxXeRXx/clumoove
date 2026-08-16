package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

var (
	_ ManagerConnector        = (*HiDriveProvider)(nil)
	_ ManagerLister           = (*HiDriveProvider)(nil)
	_ ManagerDownloader       = (*HiDriveProvider)(nil)
	_ ManagerUploader         = (*HiDriveProvider)(nil)
	_ ManagerDirectoryCreator = (*HiDriveProvider)(nil)
	_ ManagerPathResolver     = (*HiDriveProvider)(nil)
	_ ManagerThumbnailer      = (*HiDriveProvider)(nil)
)

// ConnectManager verifies connectivity to HiDrive via /user/me.
func (p *HiDriveProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from HiDrive with native limit/offset pagination.
func (p *HiDriveProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := 0
	if options.Cursor != "" {
		parsed, err := strconv.Atoi(options.Cursor)
		if err != nil || parsed < 0 {
			return ManagerPage{}, fmt.Errorf("invalid cursor: %q", options.Cursor)
		}
		offset = parsed
	}

	hdPath := p.cleanPath(locator.Path)
	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/dir"), nil)
	if err != nil {
		return ManagerPage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	q.Set("members", "file,dir")
	q.Set("fields", "path,name,members.name,members.type,members.size,members.mtime,members.readable,members.writable,members.id,members.mime_type,members.chash")
	q.Set("limit", fmt.Sprintf("%d,%d", offset, limit))
	q.Set("sort", "name")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return ManagerPage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ManagerPage{}, fmt.Errorf("hidrive manager list: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ManagerPage{}, fmt.Errorf("hidrive manager list: %w", ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return ManagerPage{}, fmt.Errorf("hidrive manager list failed, status: %d", resp.StatusCode)
	}

	var dirResp hidriveDirResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
		return ManagerPage{}, err
	}

	items := make([]ManagerItem, 0, len(dirResp.Members))
	for _, m := range dirResp.Members {
		name, err := decodeHiDrivePath(m.Name)
		if err != nil {
			return ManagerPage{}, err
		}
		childPath := strings.TrimSuffix(hdPath, "/") + "/" + name
		item := ManagerItem{
			Locator:  ManagerLocator{Path: childPath, NativeID: m.ID},
			Name:     name,
			IsDir:    m.Type == "dir",
			Size:     m.Size,
			MIMEType: m.MimeType,
		}
		if m.Mtime > 0 {
			item.Modified = time.Unix(m.Mtime, 0)
		}
		items = append(items, item)
	}

	var nextCursor string
	if len(dirResp.Members) == limit {
		nextCursor = strconv.Itoa(offset + len(dirResp.Members))
	}

	return ManagerPage{Items: items, NextCursor: nextCursor}, nil
}

// DownloadManager retrieves the specified file directly from HiDrive.
func (p *HiDriveProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	hdPath := p.cleanPath(locator.Path)
	res, err := p.InspectResource(ctx, "files", hdPath)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("hidrive manager download: %w", ErrNotFound)
	}
	stream, err := p.StreamDownload(ctx, "files", hdPath)
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

// CreateManagerDirectory creates a directory under the given parent locator.
func (p *HiDriveProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	parentPath := p.cleanPath(parent.Path)
	dirPath := path.Join(parentPath, name)
	if !strings.HasPrefix(dirPath, "/") {
		dirPath = "/" + dirPath
	}
	exists, _, err := p.FileExists(ctx, "files", dirPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrManagerConflict
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.apiURL("/dir"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", dirPath)
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("hidrive mkdir: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusConflict {
		return ErrManagerConflict
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hidrive mkdir failed: %d", resp.StatusCode)
	}
	return nil
}

// UploadManager streams content to HiDrive respecting conflict strategies.
func (p *HiDriveProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	parentPath := p.cleanPath(parent.Path)
	targetPath := path.Join(parentPath, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	exists, _, err := p.FileExists(ctx, "files", targetPath)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	finalName := name
	switch options.ConflictStrategy {
	case "SKIP":
		if exists {
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		}
	case "OVERWRITE":
		if exists {
			meta, mErr := p.InspectResource(ctx, "files", targetPath)
			if mErr != nil {
				return ManagerUploadResult{}, mErr
			}
			if meta.IsDir {
				return ManagerUploadResult{}, ErrManagerConflict
			}
			tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
			if err := p.StreamUploadChunked(ctx, "files", tmpPath, stream, size, nil); err != nil {
				_ = p.DeleteFile(ctx, "files", tmpPath)
				return ManagerUploadResult{}, err
			}
			if err := p.RenameFile(ctx, "files", tmpPath, targetPath); err != nil {
				_ = p.DeleteFile(ctx, "files", tmpPath)
				return ManagerUploadResult{}, err
			}
			return ManagerUploadResult{Status: "uploaded", FinalName: name}, nil
		}
	case "RENAME":
		if exists {
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := managerRenamedName(name, suffix)
				candidatePath := path.Join(parentPath, candidate)
				if !strings.HasPrefix(candidatePath, "/") {
					candidatePath = "/" + candidatePath
				}
				cExists, _, cErr := p.FileExists(ctx, "files", candidatePath)
				if cErr != nil {
					return ManagerUploadResult{}, cErr
				}
				if !cExists {
					finalName = candidate
					targetPath = candidatePath
					break
				}
				if suffix == 100 {
					return ManagerUploadResult{}, ErrManagerConflict
				}
			}
		}
	default:
		return ManagerUploadResult{}, errors.New("invalid manager upload conflict strategy")
	}

	if err := p.StreamUploadChunked(ctx, "files", targetPath, stream, size, nil); err != nil {
		return ManagerUploadResult{}, err
	}

	status := "uploaded"
	if finalName != name && options.ConflictStrategy == "RENAME" && exists {
		status = "renamed"
	}
	return ManagerUploadResult{Status: status, FinalName: finalName}, nil
}

// ResolveManagerPath turns a path string into ManagerBreadcrumbs for navigation.
func (p *HiDriveProvider) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	clean := strings.Trim(value, "/")
	if clean == "" {
		return ManagerLocator{Path: "/"}, nil, false, nil
	}
	currentPath := ""
	segments := strings.Split(clean, "/")
	breadcrumbs := make([]ManagerBreadcrumb, 0, len(segments))
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		currentPath = currentPath + "/" + segment
		exists, _, err := p.FileExists(ctx, "files", currentPath)
		if err != nil {
			return ManagerLocator{}, nil, false, err
		}
		if !exists {
			return ManagerLocator{Path: path.Dir(currentPath)}, breadcrumbs, true, nil
		}
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{
			Name:    segment,
			Locator: ManagerLocator{Path: currentPath},
		})
	}
	return ManagerLocator{Path: currentPath}, breadcrumbs, false, nil
}

// ThumbnailManager retrieves a thumbnail image stream from HiDrive for the specified file.
func (p *HiDriveProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	hdPath := p.cleanPath(locator.Path)
	if width <= 0 {
		width = 256
	}
	if height <= 0 {
		height = 256
	}
	if width > 2048 {
		width = 2048
	}
	if height > 2048 {
		height = 2048
	}

	req, err := http.NewRequestWithContext(ctx, "GET", p.apiURL("/file/thumbnail"), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.AccessToken)
	q := req.URL.Query()
	q.Set("path", hdPath)
	q.Set("width", strconv.Itoa(width))
	q.Set("height", strconv.Itoa(height))
	q.Set("mode", "contain")
	req.URL.RawQuery = q.Encode()

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, "", fmt.Errorf("hidrive thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, "", fmt.Errorf("hidrive thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusUnsupportedMediaType {
		resp.Body.Close()
		return nil, "", fmt.Errorf("hidrive thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("hidrive thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
