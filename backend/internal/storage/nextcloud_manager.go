package storage

import (
	"bytes"
	"context"
	"encoding/xml"
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
	_ ManagerConnector        = (*NextcloudProvider)(nil)
	_ ManagerLister           = (*NextcloudProvider)(nil)
	_ ManagerDownloader       = (*NextcloudProvider)(nil)
	_ ManagerUploader         = (*NextcloudProvider)(nil)
	_ ManagerDirectoryCreator = (*NextcloudProvider)(nil)
	_ ManagerPathResolver     = (*NextcloudProvider)(nil)
	_ ManagerThumbnailer      = (*NextcloudProvider)(nil)
)

type xmlNextcloudManagerMultistatus struct {
	XMLName   xml.Name                      `xml:"multistatus"`
	Responses []xmlNextcloudManagerResponse `xml:"response"`
}

type xmlNextcloudManagerResponse struct {
	Href     string                        `xml:"href"`
	Status   string                        `xml:"status"`
	Propstat []xmlNextcloudManagerPropstat `xml:"propstat"`
}

type xmlNextcloudManagerPropstat struct {
	Prop   xmlNextcloudManagerProp `xml:"prop"`
	Status string                  `xml:"status"`
}

type xmlNextcloudManagerProp struct {
	GetLastModified  string          `xml:"getlastmodified"`
	GetContentLength string          `xml:"getcontentlength"`
	GetContentType   string          `xml:"getcontenttype"`
	ResourceType     XMLResourceType `xml:"resourcetype"`
	GetETag          string          `xml:"getetag"`
	FileID           string          `xml:"fileid"`
	ID               string          `xml:"id"`
	Checksums        *XMLChecksums   `xml:"checksums"`
	HasPreviewNC     string          `xml:"has-preview"`
	Size             string          `xml:"size"`
}

// ConnectManager verifies connectivity to Nextcloud via PROPFIND.
func (p *NextcloudProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from Nextcloud with native limit/offset pagination and file metadata.
func (p *NextcloudProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
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

	cleanPath := cleanDAVEndpointPath(locator.Path)
	u := p.pb.resourceURL(p.baseURL(), p.Username, "files", cleanPath)

	body := []byte(`<?xml version="1.0" encoding="utf-8" ?>
		<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
			<d:prop>
				<d:getlastmodified/>
				<d:getcontentlength/>
				<d:getcontenttype/>
				<d:resourcetype/>
				<d:getetag/>
				<oc:fileid/>
				<oc:id/>
				<oc:checksums/>
				<nc:has-preview/>
				<oc:has-preview/>
				<oc:size/>
			</d:prop>
		</d:propfind>`)

	req, err := p.newRequest("PROPFIND", u, bytes.NewReader(body))
	if err != nil {
		return ManagerPage{}, err
	}
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := doPropfind(ctx, p.HTTPClient, req)
	if err != nil {
		return ManagerPage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ManagerPage{}, fmt.Errorf("nextcloud manager list: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ManagerPage{}, fmt.Errorf("nextcloud manager list: %w", ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManagerPage{}, fmt.Errorf("nextcloud manager list failed, status: %d", resp.StatusCode)
	}

	var multistatus xmlNextcloudManagerMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return ManagerPage{}, err
	}

	uParsed, parseErr := url.Parse(p.baseURL())
	var basePath string
	if parseErr == nil {
		basePath = strings.TrimSuffix(uParsed.Path, "/")
	} else {
		basePath = "/remote.php/dav"
	}

	prefixPath := p.pb.listingPrefix(basePath, p.Username, "files")
	prefixLower := strings.ToLower(prefixPath)

	var items []ManagerItem
	for _, r := range multistatus.Responses {
		decodedHref := decodeDAVHref(r.Href)
		hrefLower := strings.ToLower(decodedHref)
		if !strings.HasPrefix(hrefLower, prefixLower) {
			continue
		}

		relativeHref := decodedHref[len(prefixPath):]
		if relativeHref == "" {
			relativeHref = "/"
		}

		cleanDirPath := "/" + strings.Trim(cleanPath, "/")
		cleanRelHref := "/" + strings.Trim(relativeHref, "/")
		if cleanRelHref == cleanDirPath || (cleanDirPath == "/" && cleanRelHref == "") {
			continue
		}

		name := path.Base(relativeHref)
		if cleanDirPath == "/" && IsSystemOrAppGeneratedCollection(name) {
			continue
		}

		for _, pstat := range r.Propstat {
			if strings.Contains(pstat.Status, "200 OK") {
				prop := pstat.Prop
				isDir := prop.ResourceType.Collection != nil

				var size int64
				if !isDir {
					if s, err := strconv.ParseInt(prop.GetContentLength, 10, 64); err == nil {
						size = s
					}
				} else if prop.Size != "" {
					if s, err := strconv.ParseInt(prop.Size, 10, 64); err == nil {
						size = s
					}
				}

				var modified time.Time
				if prop.GetLastModified != "" {
					if t, err := http.ParseTime(prop.GetLastModified); err == nil {
						modified = t
					}
				}

				fileID := prop.FileID
				if fileID == "" {
					fileID = prop.ID
				}

				childPath := cleanRelHref
				item := ManagerItem{
					Locator:  ManagerLocator{Path: childPath, NativeID: fileID},
					Name:     name,
					IsDir:    isDir,
					Size:     size,
					Modified: modified,
					MIMEType: prop.GetContentType,
				}
				items = append(items, item)
				break
			}
		}
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

	return ManagerPage{Items: pageItems, NextCursor: nextCursor}, nil
}

// DownloadManager retrieves the specified file directly from Nextcloud.
func (p *NextcloudProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	cleanPath := cleanDAVEndpointPath(locator.Path)
	res, err := p.InspectResource(ctx, "files", cleanPath)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("nextcloud manager download: %w", ErrNotFound)
	}

	stream, err := p.StreamDownload(ctx, "files", cleanPath)
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
func (p *NextcloudProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	parentPath := cleanDAVEndpointPath(parent.Path)
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

	return p.CreateDirectory(ctx, "files", dirPath)
}

// UploadManager streams content to Nextcloud respecting conflict strategies.
func (p *NextcloudProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	parentPath := cleanDAVEndpointPath(parent.Path)
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
			// Stage to .tmp and promote atomically
			tmpPath := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
			var uploadErr error
			if size > 50*1024*1024 {
				uploadErr = p.StreamUploadChunked(ctx, "files", tmpPath, stream, size, nil)
			} else {
				uploadErr = p.StreamUpload(ctx, "files", tmpPath, stream, size)
			}
			if uploadErr != nil {
				return ManagerUploadResult{}, uploadErr
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
	if size > 50*1024*1024 {
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

// ResolveManagerPath turns a path string into ManagerBreadcrumbs for navigation.
func (p *NextcloudProvider) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
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

// ThumbnailManager retrieves a thumbnail image stream from Nextcloud for the specified file.
func (p *NextcloudProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
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

	instanceRoot := strings.TrimSuffix(p.baseURL(), "/remote.php/dav")
	previewURL := fmt.Sprintf("%s/index.php/core/preview?x=%d&y=%d&a=1", instanceRoot, width, height)

	if locator.NativeID != "" {
		previewURL += "&fileId=" + url.QueryEscape(locator.NativeID)
	} else if locator.Path != "" {
		cleanPath := cleanDAVEndpointPath(locator.Path)
		if !strings.HasPrefix(cleanPath, "/") {
			cleanPath = "/" + cleanPath
		}
		previewURL += "&file=" + url.QueryEscape(cleanPath)
	} else {
		return nil, "", fmt.Errorf("nextcloud thumbnail: %w", ErrNotFound)
	}

	req, err := p.newRequest(http.MethodGet, previewURL, nil)
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(ctx)

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("nextcloud thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("nextcloud thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == http.StatusBadRequest {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("nextcloud thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("nextcloud thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
