package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var (
	_ ManagerConnector    = (*BoxProvider)(nil)
	_ ManagerLister       = (*BoxProvider)(nil)
	_ ManagerPathResolver = (*BoxProvider)(nil)
	_ ManagerDownloader   = (*BoxProvider)(nil)
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
