package storage

import (
	"bytes"
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
	_ ManagerConnector        = (*BoxProvider)(nil)
	_ ManagerLister           = (*BoxProvider)(nil)
	_ ManagerPathResolver     = (*BoxProvider)(nil)
	_ ManagerDownloader       = (*BoxProvider)(nil)
	_ ManagerThumbnailer      = (*BoxProvider)(nil)
	_ ManagerDirectoryCreator = (*BoxProvider)(nil)
	_ ManagerUploader         = (*BoxProvider)(nil)
	_ ManagerRenamer          = (*BoxProvider)(nil)
	_ ManagerMover            = (*BoxProvider)(nil)
	_ ManagerCopier           = (*BoxProvider)(nil)
	_ ManagerDeleter          = (*BoxProvider)(nil)
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

func boxManagerResolveParentID(parent ManagerLocator) (string, error) {
	if parent.NativeID != "" {
		return parent.NativeID, nil
	}
	if parent.Path == "" || CanonicalManagerPath(parent.Path) == "/" {
		return boxRootFolderID, nil
	}
	return "", ErrManagerInvalidDestination
}

// CreateManagerDirectory creates a directory directly inside parent by NativeID
// without swallowing conflicts with existing items.
func (p *BoxProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	if p == nil {
		return ErrManagerUnsupported
	}
	if !ValidManagerEntryName(name) {
		return ErrManagerInvalidDestination
	}
	parentID, err := boxManagerResolveParentID(parent)
	if err != nil {
		return err
	}
	created, err := p.createFolderInParentWithConflict(ctx, parentID, name, false)
	if err != nil {
		return err
	}
	if parentID == boxRootFolderID && created.ID != "" {
		p.ensurePathCache().Set("/"+strings.ToLower(created.Name), created.ID)
	}
	return nil
}

// UploadManager uploads a file into parent by immutable NativeID, honoring SKIP,
// OVERWRITE (new version upload), and RENAME conflict strategies.
func (p *BoxProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	if p == nil {
		return ManagerUploadResult{}, ErrManagerUnsupported
	}
	if !ValidManagerEntryName(name) {
		return ManagerUploadResult{}, ErrManagerInvalidDestination
	}
	parentID, err := boxManagerResolveParentID(parent)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	items, err := p.listAllFolderItems(ctx, parentID)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	existingNames := make(map[string]struct{}, len(items))
	var conflict *boxItem
	for i := range items {
		existingNames[strings.ToLower(items[i].Name)] = struct{}{}
		if strings.EqualFold(items[i].Name, name) {
			conflict = &items[i]
		}
	}

	finalName := name
	status := "uploaded"
	existingFileID := ""

	switch ManagerConflictStrategy(options.ConflictStrategy) {
	case ManagerConflictSkip:
		if conflict != nil {
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		}
	case ManagerConflictOverwrite:
		if conflict != nil {
			if conflict.Type != "file" {
				return ManagerUploadResult{}, ErrManagerConflict
			}
			existingFileID = conflict.ID
		}
	case ManagerConflictRename:
		if conflict != nil {
			found := ""
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := managerRenamedName(name, suffix)
				if _, used := existingNames[strings.ToLower(candidate)]; !used {
					found = candidate
					break
				}
			}
			if found == "" {
				return ManagerUploadResult{}, ErrManagerConflict
			}
			finalName = found
			status = "renamed"
		}
	default:
		return ManagerUploadResult{}, fmt.Errorf("invalid manager upload conflict strategy: %w", ErrManagerInvalidDestination)
	}

	if size > boxDirectUploadMaxBytes {
		if _, err := p.uploadChunkedByID(ctx, parentID, existingFileID, finalName, stream, size, nil, UploadOptions{}); err != nil {
			return ManagerUploadResult{}, err
		}
	} else {
		if _, err := p.uploadDirectByID(ctx, parentID, existingFileID, finalName, stream, size, UploadOptions{}); err != nil {
			return ManagerUploadResult{}, err
		}
	}
	return ManagerUploadResult{Status: status, FinalName: finalName}, nil
}

// RenameManagerItem renames a Box file or folder by immutable NativeID, retaining
// its existing parent folder when parent is empty.
func (p *BoxProvider) RenameManagerItem(ctx context.Context, locator, parent ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return p.mutateManagerItem(ctx, locator, parent, name, options, "renamed", true)
}

// MoveManagerItem moves (and optionally renames) a Box file or folder to
// destination by immutable NativeID.
func (p *BoxProvider) MoveManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return p.mutateManagerItem(ctx, locator, destination, name, options, "moved", false)
}

// CopyManagerItem copies a Box file or folder server-side using Box's native
// copy endpoint without streaming bytes through this service.
func (p *BoxProvider) CopyManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	if p == nil {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if locator.NativeID == "" || locator.NativeID == boxRootFolderID || !ValidManagerEntryName(name) {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}

	source, err := p.getManagerItemByID(ctx, locator.NativeID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	destinationID, err := resolveBoxMutationDestinationID(destination, "", false)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	parentFolder, err := p.getManagerFolderByID(ctx, destinationID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	destinationID = parentFolder.ID

	if source.Type == "folder" {
		cycle, cycleErr := p.boxManagerWouldCreateCycle(ctx, source.ID, destinationID)
		if cycleErr != nil {
			return ManagerMutationResult{}, cycleErr
		}
		if cycle {
			return ManagerMutationResult{}, ErrManagerDirectoryCycle
		}
	}

	children, err := p.listAllFolderItems(ctx, destinationID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	finalName, resultStatus, conflict, err := boxManagerResolveConflict(name, source.Type == "folder", children, "", options)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	if resultStatus == "skipped" {
		return ManagerMutationResult{Status: "skipped", FinalName: finalName, Native: true}, nil
	}
	if conflict != nil {
		if conflict.ID == source.ID {
			return ManagerMutationResult{}, ErrManagerNoop
		}
		if err := p.deleteBoxFileByID(ctx, conflict.ID); err != nil {
			return ManagerMutationResult{}, err
		}
	}
	if err := p.copyBoxItem(ctx, source.Type, source.ID, finalName, destinationID); err != nil {
		return ManagerMutationResult{}, err
	}
	if conflict != nil {
		return ManagerMutationResult{Status: "overwritten", FinalName: finalName, Native: true}, nil
	}
	if resultStatus == "renamed_on_conflict" {
		return ManagerMutationResult{Status: "renamed_on_conflict", FinalName: finalName, Native: true}, nil
	}
	return ManagerMutationResult{Status: "copied", FinalName: finalName, Native: true}, nil
}

// DeleteManagerItem deletes a Box file or folder by immutable NativeID.
// Root folder "0" is always rejected; non-empty folders require recursive=true.
func (p *BoxProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	if p == nil {
		return ErrManagerUnsupported
	}
	if locator.NativeID == "" || locator.NativeID == boxRootFolderID {
		return fmt.Errorf("box manager delete: %w", ErrNotFound)
	}

	item, err := p.getManagerItemByID(ctx, locator.NativeID)
	if err != nil {
		return err
	}
	if item.Type == "file" {
		return p.deleteBoxFileByID(ctx, item.ID)
	}
	if item.Type != "folder" {
		return fmt.Errorf("box manager delete unsupported type %q: %w", item.Type, ErrNotFound)
	}

	if !recursive {
		page, listErr := p.listFolderPage(ctx, item.ID, "", 1)
		if listErr != nil {
			return listErr
		}
		if len(page.Entries) > 0 {
			return ErrManagerDirectoryNotEmpty
		}
	}

	if err := p.deleteBoxFolderByID(ctx, item.ID, recursive); err != nil {
		return err
	}
	p.invalidateFolderCache(item.ID, locator.Path)
	return nil
}

func (p *BoxProvider) deleteBoxFolderByID(ctx context.Context, folderID string, recursive bool) error {
	endpoint := p.apiBase + "/folders/" + url.PathEscape(folderID)
	if recursive {
		endpoint += "?recursive=true"
	}
	req, err := p.newRequest(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusBadRequest {
		if code, _ := parseBoxConflict(bodyBytes); code == "folder_not_empty" {
			return ErrManagerDirectoryNotEmpty
		}
	}
	return boxResponseError("delete folder", resp)
}

func (p *BoxProvider) mutateManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions, status string, retainParentWhenEmpty bool) (ManagerMutationResult, error) {
	if p == nil {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if locator.NativeID == "" || locator.NativeID == boxRootFolderID || !ValidManagerEntryName(name) {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}

	source, err := p.getManagerItemByID(ctx, locator.NativeID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	if source.Parent == nil || source.Parent.ID == "" {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}

	destinationID, err := resolveBoxMutationDestinationID(destination, source.Parent.ID, retainParentWhenEmpty)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	parentFolder, err := p.getManagerFolderByID(ctx, destinationID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	destinationID = parentFolder.ID

	if source.Type == "folder" {
		cycle, cycleErr := p.boxManagerWouldCreateCycle(ctx, source.ID, destinationID)
		if cycleErr != nil {
			return ManagerMutationResult{}, cycleErr
		}
		if cycle {
			return ManagerMutationResult{}, ErrManagerDirectoryCycle
		}
	}

	children, err := p.listAllFolderItems(ctx, destinationID)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	finalName, resultStatus, conflict, err := boxManagerResolveConflict(name, source.Type == "folder", children, source.ID, options)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	if resultStatus == "skipped" {
		return ManagerMutationResult{Status: "skipped", FinalName: finalName, Native: true}, nil
	}
	if source.Parent.ID == destinationID && source.Name == finalName {
		return ManagerMutationResult{}, ErrManagerNoop
	}

	if conflict != nil {
		if err := p.deleteBoxFileByID(ctx, conflict.ID); err != nil {
			return ManagerMutationResult{}, err
		}
	}
	if err := p.updateBoxItem(ctx, source.Type, source.ID, finalName, destinationID); err != nil {
		return ManagerMutationResult{}, err
	}
	if source.Type == "folder" {
		p.invalidateFolderCache(source.ID, locator.Path)
	}
	if resultStatus == "renamed_on_conflict" {
		status = resultStatus
	}
	return ManagerMutationResult{Status: status, FinalName: finalName, Native: true}, nil
}

func resolveBoxMutationDestinationID(destination ManagerLocator, currentParentID string, retainParentWhenEmpty bool) (string, error) {
	if destination.NativeID != "" {
		return destination.NativeID, nil
	}
	if retainParentWhenEmpty && destination.Path == "" {
		return currentParentID, nil
	}
	if destination.Path == "/" {
		return boxRootFolderID, nil
	}
	return "", ErrManagerInvalidDestination
}

func (p *BoxProvider) getManagerItemByID(ctx context.Context, id string) (boxItem, error) {
	var item boxItem
	fileEndpoint := p.apiBase + "/files/" + url.PathEscape(id) + "?fields=" + url.QueryEscape(boxItemFields)
	err := p.doJSON(ctx, http.MethodGet, fileEndpoint, nil, &item, "inspect file")
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return boxItem{}, err
	}
	folderEndpoint := p.apiBase + "/folders/" + url.PathEscape(id) + "?fields=" + url.QueryEscape(boxItemFields)
	if folderErr := p.doJSON(ctx, http.MethodGet, folderEndpoint, nil, &item, "inspect folder"); folderErr != nil {
		return boxItem{}, folderErr
	}
	return item, nil
}

func (p *BoxProvider) getManagerFolderByID(ctx context.Context, folderID string) (boxItem, error) {
	var folder boxItem
	endpoint := p.apiBase + "/folders/" + url.PathEscape(folderID) + "?fields=" + url.QueryEscape(boxItemFields)
	err := p.doJSON(ctx, http.MethodGet, endpoint, nil, &folder, "inspect folder")
	if err == nil {
		if folder.ID == "" {
			folder.ID = folderID
		}
		return folder, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return boxItem{}, err
	}
	var file boxItem
	fileEndpoint := p.apiBase + "/files/" + url.PathEscape(folderID) + "?fields=id,type"
	if fileErr := p.doJSON(ctx, http.MethodGet, fileEndpoint, nil, &file, "inspect file"); fileErr == nil {
		return boxItem{}, ErrManagerInvalidDestination
	}
	return boxItem{}, err
}

func (p *BoxProvider) boxManagerWouldCreateCycle(ctx context.Context, sourceID, destinationID string) (bool, error) {
	visited := make(map[string]struct{}, 16)
	cur := destinationID
	for depth := 0; cur != "" && cur != boxRootFolderID && depth < 100; depth++ {
		if cur == sourceID {
			return true, nil
		}
		if _, seen := visited[cur]; seen {
			return true, nil
		}
		visited[cur] = struct{}{}
		var folder boxItem
		endpoint := p.apiBase + "/folders/" + url.PathEscape(cur) + "?fields=id,type,parent"
		if err := p.doJSON(ctx, http.MethodGet, endpoint, nil, &folder, "inspect ancestor folder"); err != nil {
			return false, err
		}
		if folder.Parent == nil || folder.Parent.ID == "" {
			return false, nil
		}
		cur = folder.Parent.ID
	}
	return cur != "" && cur != boxRootFolderID, nil
}

func boxManagerResolveConflict(name string, sourceIsDir bool, children []boxItem, excludeID string, options ManagerMutationOptions) (string, string, *boxItem, error) {
	existingNames := make(map[string]struct{}, len(children))
	var conflict *boxItem
	for i := range children {
		if excludeID != "" && children[i].ID == excludeID {
			continue
		}
		existingNames[strings.ToLower(children[i].Name)] = struct{}{}
		if strings.EqualFold(children[i].Name, name) && conflict == nil {
			conflict = &children[i]
		}
	}
	if conflict == nil {
		return name, "", nil, nil
	}
	switch options.ConflictStrategy {
	case ManagerConflictSkip:
		return name, "skipped", nil, nil
	case ManagerConflictRename:
		for suffix := 1; suffix <= 100; suffix++ {
			candidate := managerRenamedName(name, suffix)
			if _, used := existingNames[strings.ToLower(candidate)]; !used {
				return candidate, "renamed_on_conflict", nil, nil
			}
		}
		return "", "", nil, ErrManagerConflict
	case ManagerConflictOverwrite:
		if sourceIsDir || conflict.Type != "file" {
			return "", "", nil, ErrManagerConflict
		}
		return name, "", conflict, nil
	default:
		return "", "", nil, ErrManagerConflict
	}
}

func (p *BoxProvider) deleteBoxFileByID(ctx context.Context, fileID string) error {
	endpoint := p.apiBase + "/files/" + url.PathEscape(fileID)
	return p.doJSON(ctx, http.MethodDelete, endpoint, nil, nil, "delete file")
}

func (p *BoxProvider) updateBoxItem(ctx context.Context, itemType, id, name, parentID string) error {
	return p.mutateBoxItemPayload(ctx, http.MethodPut, boxItemResourcePath(itemType, id), name, parentID, "update "+itemType)
}

func (p *BoxProvider) copyBoxItem(ctx context.Context, itemType, id, name, parentID string) error {
	return p.mutateBoxItemPayload(ctx, http.MethodPost, boxItemResourcePath(itemType, id)+"/copy", name, parentID, "copy "+itemType)
}

func boxItemResourcePath(itemType, id string) string {
	resource := "files"
	if itemType == "folder" {
		resource = "folders"
	}
	return "/" + resource + "/" + url.PathEscape(id)
}

func (p *BoxProvider) mutateBoxItemPayload(ctx context.Context, method, relPath, name, parentID, op string) error {
	payload, err := json.Marshal(struct {
		Name   string          `json:"name"`
		Parent boxUploadParent `json:"parent"`
	}{
		Name:   name,
		Parent: boxUploadParent{ID: parentID},
	})
	if err != nil {
		return err
	}
	return p.doJSON(ctx, method, p.apiBase+relPath, bytes.NewReader(payload), nil, op)
}

func (p *BoxProvider) invalidateFolderCache(folderID, knownPath string) {
	cache := p.ensurePathCache()
	cache.InvalidateValue(folderID)
	if knownPath != "" {
		if clean, err := boxCleanPath(knownPath); err == nil && clean != "/" {
			cache.InvalidatePrefix(strings.ToLower(clean))
		}
	}
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
