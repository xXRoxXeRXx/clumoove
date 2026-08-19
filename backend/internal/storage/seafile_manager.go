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
	_ ManagerConnector        = (*SeafileProvider)(nil)
	_ ManagerLister           = (*SeafileProvider)(nil)
	_ ManagerDownloader       = (*SeafileProvider)(nil)
	_ ManagerUploader         = (*SeafileProvider)(nil)
	_ ManagerDirectoryCreator = (*SeafileProvider)(nil)
	_ ManagerPathResolver     = (*SeafileProvider)(nil)
	_ ManagerThumbnailer      = (*SeafileProvider)(nil)
)

// ConnectManager verifies connectivity to Seafile.
func (p *SeafileProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists repos or directory contents from Seafile with pagination.
func (p *SeafileProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
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

	repoID, repoPath, fullPath, err := p.resolveRepoAndPath(ctx, locator.Path)
	if err != nil {
		return ManagerPage{}, err
	}

	var items []ManagerItem

	if repoID == "" {
		repos, err := p.listRepos(ctx)
		if err != nil {
			return ManagerPage{}, err
		}
		for _, r := range repos {
			items = append(items, ManagerItem{
				Locator:  ManagerLocator{Path: "/" + r.Name, NativeID: r.ID},
				Name:     r.Name,
				IsDir:    true,
				Modified: time.Unix(r.Mtime, 0),
			})
		}
	} else {
		reqURL := fmt.Sprintf("%s/api2/repos/%s/dir/?p=%s", p.BaseURL, repoID, url.QueryEscape(repoPath))
		req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
		if err != nil {
			return ManagerPage{}, err
		}

		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			return ManagerPage{}, fmt.Errorf("failed to list seafile directory: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return ManagerPage{}, fmt.Errorf("seafile manager list: %w", ErrNotFound)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			p.invalidateToken()
			return ManagerPage{}, fmt.Errorf("seafile manager list: %w", ErrAuth)
		}
		if resp.StatusCode != http.StatusOK {
			return ManagerPage{}, fmt.Errorf("seafile list dir returned status %d", resp.StatusCode)
		}

		var dirItems []seafileDirItem
		if err := json.NewDecoder(resp.Body).Decode(&dirItems); err != nil {
			return ManagerPage{}, fmt.Errorf("failed to decode seafile directory listing: %w", err)
		}

		cleanDir := strings.TrimRight(fullPath, "/")
		for _, item := range dirItems {
			itemPath := cleanDir + "/" + item.Name
			if !strings.HasPrefix(itemPath, "/") {
				itemPath = "/" + itemPath
			}
			items = append(items, ManagerItem{
				Locator:  ManagerLocator{Path: itemPath, NativeID: item.ID},
				Name:     item.Name,
				IsDir:    item.Type == "dir",
				Size:     item.Size,
				Modified: time.Unix(item.Mtime, 0),
			})
		}
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

// DownloadManager retrieves a file stream directly from Seafile.
func (p *SeafileProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	res, err := p.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("seafile manager download: %w", ErrNotFound)
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

// UploadManager uploads a file stream into Seafile with conflict resolution.
func (p *SeafileProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	targetPath := path.Join(parent.Path, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
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
				candidatePath := path.Join(parent.Path, candidateName)
				if !strings.HasPrefix(candidatePath, "/") {
					candidatePath = "/" + candidatePath
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

// CreateManagerDirectory creates a new folder in Seafile.
// Creating a directory at the root level (i.e., a library) requires a
// separate Seafile API and is not supported in the file manager; return
// ErrManagerUnsupported so the handler can respond with 501.
func (p *SeafileProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	cleanParent := strings.Trim(parent.Path, "/")
	if cleanParent == "" {
		return ErrManagerUnsupported
	}

	targetPath := path.Join(parent.Path, name)
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

// ResolveManagerPath resolves a path into stable breadcrumbs and locator.
func (p *SeafileProvider) ResolveManagerPath(ctx context.Context, value string) (locator ManagerLocator, breadcrumbs []ManagerBreadcrumb, fallback bool, err error) {
	clean := strings.Trim(value, "/")
	if clean == "" {
		return ManagerLocator{Path: "/"}, []ManagerBreadcrumb{{Name: "/", Locator: ManagerLocator{Path: "/"}}}, false, nil
	}

	parts := strings.Split(clean, "/")
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

// ThumbnailManager retrieves a thumbnail image stream from Seafile for the specified file.
func (p *SeafileProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	repoID, repoPath, _, err := p.resolveRepoAndPath(ctx, locator.Path)
	if err != nil {
		return nil, "", err
	}
	if repoID == "" || repoPath == "" || repoPath == "/" {
		return nil, "", fmt.Errorf("seafile thumbnail: %w", ErrNotFound)
	}

	size := 256
	if width > 256 || height > 256 {
		size = 1024
	}

	reqURL := fmt.Sprintf("%s/api2/repos/%s/thumbnail/?p=%s&size=%d", p.BaseURL, repoID, url.QueryEscape(repoPath), size)
	req, err := p.newAuthRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch seafile thumbnail: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		p.invalidateToken()
		return nil, "", fmt.Errorf("seafile thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("seafile thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == 422 {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("seafile thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("seafile thumbnail returned status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		// Some Seafile versions return a JSON object with a thumbnail download URL
		var jsonResp struct {
			Thumbnail string `json:"thumbnail"`
			URL       string `json:"url"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&jsonResp)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, "", decodeErr
		}
		rawLink := jsonResp.Thumbnail
		if rawLink == "" {
			rawLink = jsonResp.URL
		}
		if rawLink == "" {
			return nil, "", fmt.Errorf("seafile thumbnail: %w", ErrUnsupportedMedia)
		}
		client, crossOrigin, linkErr := p.issuedLinkClient(rawLink)
		if linkErr != nil {
			return nil, "", linkErr
		}
		linkReq, linkReqErr := http.NewRequestWithContext(ctx, "GET", rawLink, nil)
		if linkReqErr != nil {
			return nil, "", linkReqErr
		}
		if !crossOrigin {
			token, _ := p.getToken(ctx)
			if token != "" {
				linkReq.Header.Set("Authorization", "Token "+token)
			}
		}
		linkResp, linkDoErr := client.Do(linkReq)
		if linkDoErr != nil {
			return nil, "", linkDoErr
		}
		if linkResp.StatusCode != http.StatusOK {
			_ = linkResp.Body.Close()
			return nil, "", fmt.Errorf("seafile thumbnail link returned status %d", linkResp.StatusCode)
		}
		cType := linkResp.Header.Get("Content-Type")
		if cType == "" {
			cType = "image/jpeg"
		}
		return linkResp.Body, cType, nil
	}

	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
