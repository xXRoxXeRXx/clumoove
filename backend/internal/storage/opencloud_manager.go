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
	_ ManagerConnector        = (*OpenCloudProvider)(nil)
	_ ManagerLister           = (*OpenCloudProvider)(nil)
	_ ManagerDownloader       = (*OpenCloudProvider)(nil)
	_ ManagerUploader         = (*OpenCloudProvider)(nil)
	_ ManagerDirectoryCreator = (*OpenCloudProvider)(nil)
	_ ManagerPathResolver     = (*OpenCloudProvider)(nil)
	_ ManagerThumbnailer      = (*OpenCloudProvider)(nil)
)

type xmlOpenCloudManagerMultistatus struct {
	XMLName   xml.Name                      `xml:"multistatus"`
	Responses []xmlOpenCloudManagerResponse `xml:"response"`
}

type xmlOpenCloudManagerResponse struct {
	Href     string                        `xml:"href"`
	Status   string                        `xml:"status"`
	Propstat []xmlOpenCloudManagerPropstat `xml:"propstat"`
}

type xmlOpenCloudManagerPropstat struct {
	Prop   xmlOpenCloudManagerProp `xml:"prop"`
	Status string                  `xml:"status"`
}

type xmlOpenCloudManagerProp struct {
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

// ConnectManager verifies connectivity to OpenCloud via PROPFIND.
func (p *OpenCloudProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

// ListManager lists directory contents from OpenCloud with limit/offset pagination and file metadata.
func (p *OpenCloudProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
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
		return ManagerPage{}, fmt.Errorf("opencloud manager list: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ManagerPage{}, fmt.Errorf("opencloud manager list: %w", ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ManagerPage{}, fmt.Errorf("opencloud manager list failed, status: %d", resp.StatusCode)
	}

	var multistatus xmlOpenCloudManagerMultistatus
	decoder := xml.NewDecoder(resp.Body)
	if err := decoder.Decode(&multistatus); err != nil {
		return ManagerPage{}, err
	}

	uParsed, parseErr := url.Parse(p.baseURL())
	var basePath string
	if parseErr == nil {
		basePath = strings.TrimSuffix(uParsed.Path, "/")
	} else {
		basePath = "/dav/spaces"
	}
	prefixPath := p.pb.listingPrefix(basePath, p.Username, "files")

	var items []ManagerItem
	for _, r := range multistatus.Responses {
		decodedHref := decodeDAVHref(r.Href)
		if len(decodedHref) < len(prefixPath) || !strings.EqualFold(decodedHref[:len(prefixPath)], prefixPath) {
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

	if len(items) > 10000 {
		return ManagerPage{}, ErrManagerDirectoryTooLarge
	}

	sortManagerItems(items)

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

// DownloadManager retrieves the specified file directly from OpenCloud.
func (p *OpenCloudProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	cleanPath := cleanDAVEndpointPath(locator.Path)
	res, err := p.InspectResource(ctx, "files", cleanPath)
	if err != nil {
		return ManagerDownload{}, err
	}
	if res.IsDir {
		return ManagerDownload{}, fmt.Errorf("opencloud manager download: %w", ErrNotFound)
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

// UploadManager uploads a file stream into OpenCloud with conflict resolution.
func (p *OpenCloudProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	cleanParent := cleanDAVEndpointPath(parent.Path)
	targetPath := path.Join(cleanParent, name)
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
				candidatePath := path.Join(cleanParent, candidate)
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

// CreateManagerDirectory creates a new directory in OpenCloud.
func (p *OpenCloudProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	cleanParent := cleanDAVEndpointPath(parent.Path)
	targetPath := path.Join(cleanParent, name)
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

// ResolveManagerPath resolves a human-readable path to stable breadcrumbs and locator.
func (p *OpenCloudProvider) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
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

// ThumbnailManager retrieves a thumbnail image stream from OpenCloud for the specified file.
func (p *OpenCloudProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
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

	cleanPath := cleanDAVEndpointPath(locator.Path)
	if cleanPath == "" || cleanPath == "/" {
		return nil, "", fmt.Errorf("opencloud thumbnail: %w", ErrNotFound)
	}

	previewURL := fmt.Sprintf("%s?preview=1&x=%d&y=%d&a=1&processor=fit", p.pb.resourceURL(p.baseURL(), p.Username, "files", cleanPath), width, height)

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
		return nil, "", fmt.Errorf("opencloud thumbnail: %w", ErrAuth)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("opencloud thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == http.StatusBadRequest {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("opencloud thumbnail: %w", ErrUnsupportedMedia)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("opencloud thumbnail failed, status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return resp.Body, contentType, nil
}
