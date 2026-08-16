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
	_ ManagerConnector        = (*OneDriveProvider)(nil)
	_ ManagerLister           = (*OneDriveProvider)(nil)
	_ ManagerDownloader       = (*OneDriveProvider)(nil)
	_ ManagerUploader         = (*OneDriveProvider)(nil)
	_ ManagerDirectoryCreator = (*OneDriveProvider)(nil)
	_ ManagerPathResolver     = (*OneDriveProvider)(nil)
	_ ManagerThumbnailer      = (*OneDriveProvider)(nil)
)

// ConnectManager verifies connectivity to OneDrive via /me/drive/root.
func (p *OneDriveProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from OneDrive with native Graph cursor pagination and file metadata.
func (p *OneDriveProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 100
	}

	clean, err := oneDrivePath(locator.Path)
	if err != nil {
		return ManagerPage{}, err
	}

	var reqURL string
	if options.Cursor != "" {
		validURL, err := p.validPaginationURL(options.Cursor)
		if err != nil {
			return ManagerPage{}, err
		}
		reqURL = validURL
	} else {
		itemURL, err := p.resourceURL(ctx, clean)
		if err != nil {
			return ManagerPage{}, err
		}
		reqURL = itemURL + "/children?$select=id,name,size,eTag,lastModifiedDateTime,folder,specialFolder,file&$top=" + strconv.Itoa(limit)
	}

	req, err := p.request(ctx, http.MethodGet, reqURL, nil, true)
	if err != nil {
		return ManagerPage{}, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return ManagerPage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManagerPage{}, oneDriveError("onedrive manager list", resp.StatusCode)
	}

	var page struct {
		Value []struct {
			ID                   string    `json:"id"`
			Name                 string    `json:"name"`
			Size                 int64     `json:"size"`
			ETag                 string    `json:"eTag"`
			LastModifiedDateTime string    `json:"lastModifiedDateTime"`
			Folder               *struct{} `json:"folder"`
			SpecialFolder        *struct {
				Name string `json:"name"`
			} `json:"specialFolder"`
			File *struct {
				MimeType string `json:"mimeType"`
				Hashes   struct {
					QuickXorHash string `json:"quickXorHash"`
				} `json:"hashes"`
			} `json:"file"`
		} `json:"value"`
		NextLink string `json:"@odata.nextLink"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return ManagerPage{}, fmt.Errorf("decode onedrive manager listing: %w", err)
	}

	items := make([]ManagerItem, 0, len(page.Value))
	for _, item := range page.Value {
		if item.SpecialFolder != nil && strings.EqualFold(item.SpecialFolder.Name, "vault") {
			continue
		}
		childPath := managerChildPath(locator.Path, item.Name)
		modified, _ := time.Parse(time.RFC3339, item.LastModifiedDateTime)
		mimeType := ""
		if item.File != nil {
			mimeType = item.File.MimeType
		}
		items = append(items, ManagerItem{
			Locator:  ManagerLocator{Path: childPath, NativeID: item.ID},
			Name:     item.Name,
			IsDir:    item.Folder != nil,
			Size:     item.Size,
			Modified: modified,
			MIMEType: mimeType,
		})
	}

	return ManagerPage{Items: items, NextCursor: page.NextLink}, nil
}

// DownloadManager retrieves the specified file directly from OneDrive.
func (p *OneDriveProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	clean, err := oneDrivePath(locator.Path)
	if err != nil {
		return ManagerDownload{}, err
	}

	res, err := p.InspectResource(ctx, "files", clean)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("onedrive manager download: %w", ErrNotFound)
	}

	stream, err := p.StreamDownload(ctx, "files", clean)
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

// UploadManager uploads a file directly to OneDrive, supporting skip, overwrite, and rename.
func (p *OneDriveProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	parentPath, err := oneDrivePath(parent.Path)
	if err != nil {
		return ManagerUploadResult{}, err
	}
	targetPath := path.Join(parentPath, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	exists, _, err := p.FileExists(ctx, "files", targetPath)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	finalName := name
	finalPath := targetPath

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

			// Stage to .tmp, backup target, promote, and remove backup
			nowNano := time.Now().UnixNano()
			tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, nowNano)
			bakPath := fmt.Sprintf("%s.bak.%d", targetPath, nowNano)

			var uploadErr error
			if size > oneDriveSimpleUploadLimit {
				uploadErr = p.StreamUploadChunked(ctx, "files", tmpPath, stream, size, nil)
			} else {
				uploadErr = p.StreamUpload(ctx, "files", tmpPath, stream, size)
			}
			if uploadErr != nil {
				_ = p.DeleteFile(ctx, "files", tmpPath)
				return ManagerUploadResult{}, uploadErr
			}

			// Preserve existing target to backup
			if err := p.RenameFile(ctx, "files", targetPath, bakPath); err != nil {
				_ = p.DeleteFile(ctx, "files", tmpPath)
				return ManagerUploadResult{}, fmt.Errorf("failed to backup existing file before overwrite: %w", err)
			}

			// Promote temporary upload to target
			if err := p.RenameFile(ctx, "files", tmpPath, targetPath); err != nil {
				_ = p.DeleteFile(ctx, "files", tmpPath)
				_ = p.RenameFile(ctx, "files", bakPath, targetPath)
				return ManagerUploadResult{}, fmt.Errorf("failed to promote temporary upload: %w", err)
			}

			// Remove backup
			if err := p.DeleteFile(ctx, "files", bakPath); err != nil {
				return ManagerUploadResult{}, fmt.Errorf("failed to delete overwrite backup: %w", err)
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
					finalPath = candidatePath
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

	var uploadErr error
	if size > oneDriveSimpleUploadLimit {
		uploadErr = p.StreamUploadChunked(ctx, "files", finalPath, stream, size, nil)
	} else {
		uploadErr = p.StreamUpload(ctx, "files", finalPath, stream, size)
	}
	if uploadErr != nil {
		return ManagerUploadResult{}, uploadErr
	}

	status := "uploaded"
	if finalName != name && options.ConflictStrategy == "RENAME" && exists {
		status = "renamed"
	}
	return ManagerUploadResult{Status: status, FinalName: finalName}, nil
}

// CreateManagerDirectory creates a new folder in OneDrive under the parent locator.
func (p *OneDriveProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	parentPath, err := oneDrivePath(parent.Path)
	if err != nil {
		return err
	}
	targetPath := path.Join(parentPath, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	exists, _, err := p.FileExists(ctx, "files", targetPath)
	if err != nil {
		return err
	}
	if exists {
		return ErrManagerConflict
	}

	return p.CreateDirectory(ctx, "files", targetPath)
}

// ResolveManagerPath turns a path string into ManagerBreadcrumbs for navigation.
func (p *OneDriveProvider) ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error) {
	clean, err := oneDrivePath(value)
	if err != nil {
		return ManagerLocator{}, nil, false, err
	}

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
		nextPath := currentPath + "/" + segment
		exists, _, err := p.FileExists(ctx, "files", nextPath)
		if err != nil {
			return ManagerLocator{}, nil, false, err
		}
		if !exists {
			if currentPath == "" {
				currentPath = "/"
			}
			return ManagerLocator{Path: currentPath}, breadcrumbs, true, nil
		}
		currentPath = nextPath
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{
			Name:    segment,
			Locator: ManagerLocator{Path: currentPath},
		})
	}
	return ManagerLocator{Path: currentPath}, breadcrumbs, false, nil
}

// ThumbnailManager retrieves a thumbnail image stream from OneDrive for the specified file.
func (p *OneDriveProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
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

	var itemURL string
	if locator.Path != "" && locator.Path != "/" {
		clean, err := oneDrivePath(locator.Path)
		if err != nil {
			return nil, "", err
		}
		itemURL, err = p.resourceURL(ctx, clean)
		if err != nil {
			return nil, "", err
		}
	} else if locator.NativeID != "" {
		itemURL = p.apiBase + "/items/" + url.PathEscape(locator.NativeID)
	} else {
		return nil, "", fmt.Errorf("onedrive thumbnail: missing item ID or path: %w", ErrNotFound)
	}

	thumbnailURL := fmt.Sprintf("%s/thumbnails/0/c%dx%d/content", itemURL, width, height)
	req, err := p.request(ctx, http.MethodGet, thumbnailURL, nil, true)
	if err != nil {
		return nil, "", err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("onedrive thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("onedrive thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == http.StatusBadRequest {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("onedrive thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("onedrive thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
