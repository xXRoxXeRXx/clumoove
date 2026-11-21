package storage

import (
	"context"
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
	_ ManagerConnector    = (*BoxProvider)(nil)
	_ ManagerLister       = (*BoxProvider)(nil)
	_ ManagerPathResolver = (*BoxProvider)(nil)
	_ ManagerDownloader   = (*BoxProvider)(nil)
	_ ManagerThumbnailer  = (*BoxProvider)(nil)
)

func (p *BoxProvider) ConnectManager(ctx context.Context) (bool, error) {
	return p.Connect(ctx)
}

func (p *BoxProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	folderID := boxRootFolderID
	parentPath := "/"
	if locator.NativeID != "" {
		folderID = locator.NativeID
		if locator.Path != "" {
			parentPath = CanonicalManagerPath(locator.Path)
		}
	} else if locator.Path != "" {
		cleanPath, err := boxCleanPath(locator.Path)
		if err != nil {
			return ManagerPage{}, err
		}
		parentPath = cleanPath
		if cleanPath != "/" {
			resolvedID, err := p.resolveFolderID(ctx, cleanPath)
			if err != nil {
				return ManagerPage{}, err
			}
			folderID = resolvedID
		}
	}

	limit := options.Limit
	if limit <= 0 {
		limit = defaultManagerPageSize
	} else if limit > boxListPageLimit {
		limit = boxListPageLimit
	}

	page, err := p.listFolderPage(ctx, folderID, options.Cursor, limit)
	if err != nil {
		return ManagerPage{}, err
	}
	if page.NextMarker != "" && page.NextMarker == options.Cursor {
		return ManagerPage{}, fmt.Errorf("box manager listing returned repeating cursor")
	}

	items := make([]ManagerItem, 0, len(page.Entries))
	for _, entry := range page.Entries {
		items = append(items, ManagerItem{
			Locator: ManagerLocator{
				Path:     boxChildPath(parentPath, entry.Name),
				NativeID: entry.ID,
			},
			Name:     entry.Name,
			IsDir:    entry.Type == "folder",
			Size:     entry.Size,
			Modified: boxItemModTime(entry),
		})
	}
	return ManagerPage{
		Items:          items,
		NextCursor:     page.NextMarker,
		PaginationMode: "native",
	}, nil
}

func (p *BoxProvider) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	cleanPath, err := boxCleanPath(value)
	if err != nil {
		return ManagerLocator{}, nil, false, err
	}
	trimmed := strings.Trim(cleanPath, "/")
	if trimmed == "" {
		return ManagerLocator{Path: "/"}, nil, false, nil
	}

	parentID := boxRootFolderID
	currentPath := "/"
	segments := strings.Split(trimmed, "/")
	breadcrumbs := make([]ManagerBreadcrumb, 0, len(segments))
	cache := p.ensurePathCache()

	for _, segment := range segments {
		child, findErr := p.findChildByName(ctx, parentID, segment)
		if findErr != nil {
			if errors.Is(findErr, ErrNotFound) {
				return ManagerLocator{Path: currentPath, NativeID: boxManagerParentID(parentID)}, breadcrumbs, true, nil
			}
			return ManagerLocator{}, nil, false, findErr
		}
		if child.Type != "folder" {
			return ManagerLocator{Path: currentPath, NativeID: boxManagerParentID(parentID)}, breadcrumbs, true, nil
		}
		currentPath = boxChildPath(currentPath, child.Name)
		cache.Set(strings.ToLower(currentPath), child.ID)
		locator := ManagerLocator{Path: currentPath, NativeID: child.ID}
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{Name: child.Name, Locator: locator})
		parentID = child.ID
	}

	return ManagerLocator{Path: currentPath, NativeID: parentID}, breadcrumbs, false, nil
}

func boxManagerParentID(value string) string {
	if value == boxRootFolderID {
		return ""
	}
	return value
}

// DownloadManager retrieves a Box file stream directly by its immutable NativeID.
func (p *BoxProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	if p == nil || locator.NativeID == "" || locator.NativeID == boxRootFolderID {
		return ManagerDownload{}, fmt.Errorf("box manager download: %w", ErrNotFound)
	}
	var item boxItem
	endpoint := p.apiBase + "/files/" + url.PathEscape(locator.NativeID) + "?fields=" + url.QueryEscape(boxItemFields)
	if err := p.doJSON(ctx, http.MethodGet, endpoint, nil, &item, "manager inspect file"); err != nil {
		return ManagerDownload{}, err
	}
	if item.Type != "file" {
		return ManagerDownload{}, fmt.Errorf("box manager download: %w", ErrNotFound)
	}
	stream, err := p.streamDownloadByID(ctx, locator.NativeID)
	if err != nil {
		return ManagerDownload{}, err
	}
	return ManagerDownload{
		Item: ManagerItem{
			Locator:  locator,
			Name:     item.Name,
			IsDir:    false,
			Size:     item.Size,
			Modified: boxItemModTime(item),
		},
		Stream: stream,
	}, nil
}

// mapBoxThumbnailSpec maps requested dimensions to Box /thumbnail.{ext} supported
// sizes (.png: 32, 64, 128, 256; .jpg: 32, 160, 320), capped at 320 px.
func mapBoxThumbnailSpec(width, height int) (string, int) {
	maxDim := width
	if height > maxDim {
		maxDim = height
	}
	if maxDim <= 0 {
		maxDim = 256
	}
	switch {
	case maxDim <= 32:
		return "png", 32
	case maxDim <= 64:
		return "png", 64
	case maxDim <= 128:
		return "png", 128
	case maxDim <= 256:
		return "png", 256
	default:
		return "jpg", 320
	}
}

// ThumbnailManager retrieves a thumbnail stream for a Box file by NativeID,
// mapping requested dimensions to Box-supported sizes, waiting briefly once on
// 202 Accepted, and validating 302 redirects without forwarding Authorization.
func (p *BoxProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	if p == nil || locator.NativeID == "" || locator.NativeID == boxRootFolderID {
		return nil, "", fmt.Errorf("box thumbnail: %w", ErrNotFound)
	}

	ext, size := mapBoxThumbnailSpec(width, height)
	sizeStr := strconv.Itoa(size)
	params := url.Values{}
	params.Set("min_width", sizeStr)
	params.Set("min_height", sizeStr)
	params.Set("max_width", sizeStr)
	params.Set("max_height", sizeStr)
	endpoint := p.apiBase + "/files/" + url.PathEscape(locator.NativeID) + "/thumbnail." + ext + "?" + params.Encode()

	client := p.downloadHTTPClient()
	maxWait := p.thumbnailMaxWait
	if maxWait <= 0 {
		maxWait = boxDefaultThumbnailMaxWait
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := p.newRequest(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, "", err
		}
		resp, err := p.doWithClient(ctx, client, req)
		if err != nil {
			return nil, "", err
		}

		if resp.StatusCode == http.StatusOK {
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				if ext == "jpg" {
					contentType = "image/jpeg"
				} else {
					contentType = "image/png"
				}
			}
			return resp.Body, contentType, nil
		}

		retryAfterHeader := resp.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode == http.StatusAccepted {
			serverDelay, hasRetryAfter := parseBoxRetryAfter(retryAfterHeader)
			if attempt == 0 {
				waitDelay := maxWait
				if hasRetryAfter && serverDelay < waitDelay {
					waitDelay = serverDelay
				}
				if err := boxWait(ctx, waitDelay); err != nil {
					return nil, "", err
				}
				continue
			}
			after := maxWait
			if hasRetryAfter && serverDelay > 0 {
				after = serverDelay
			} else if hasRetryAfter {
				after = time.Second
			}
			return nil, "", &RetryAfterError{After: after}
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			after, ok := parseBoxRetryAfter(retryAfterHeader)
			if !ok || after <= 0 {
				after = time.Second
			}
			return nil, "", &RetryAfterError{After: after}
		}

		return nil, "", boxAPIError("thumbnail", resp.StatusCode)
	}

	return nil, "", &RetryAfterError{After: maxWait}
}
