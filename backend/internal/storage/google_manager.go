package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
)

const googleDriveFolderMIME = "application/vnd.google-apps.folder"

// ListManager is an ID-based, native-paginated view of Google Drive. The
// migration listing remains path-oriented for backwards compatibility, but a
// manager reference must never fall back to a same-name sibling.
func (p *GoogleProvider) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	if options.Limit <= 0 {
		return ManagerPage{}, fmt.Errorf("invalid Google manager page size")
	}
	parentID := "root"
	parentPath := "/"
	if locator.NativeID != "" {
		parentID = locator.NativeID
		if locator.Path != "" {
			parentPath = locator.Path
		}
	}
	query := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
	call := p.driveService.Files.List().
		Q(query).
		PageSize(int64(options.Limit)).
		OrderBy("folder,name").
		Fields("nextPageToken, files(id, name, mimeType, size, modifiedTime)").
		Context(ctx)
	if options.Cursor != "" {
		call = call.PageToken(options.Cursor)
	}
	page, err := call.Do()
	if err != nil {
		return ManagerPage{}, wrapGoogleError("google manager listing", err)
	}
	items := make([]ManagerItem, 0, len(page.Files))
	for _, file := range page.Files {
		items = append(items, googleManagerItem(file, parentPath))
	}
	return ManagerPage{Items: items, NextCursor: page.NextPageToken}, nil
}

// DownloadManager addresses the selected Drive file directly by its immutable
// file ID, including for duplicate sibling names and Workspace exports.
func (p *GoogleProvider) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	if locator.NativeID == "" {
		return ManagerDownload{}, fmt.Errorf("Google manager item ID is required")
	}
	file, err := p.driveService.Files.Get(locator.NativeID).
		Fields("id, name, mimeType, size, modifiedTime").
		Context(ctx).
		Do()
	if err != nil {
		return ManagerDownload{}, wrapGoogleNotFound(err)
	}
	if file.MimeType == googleDriveFolderMIME {
		return ManagerDownload{}, fmt.Errorf("Google manager download: %w", ErrNotFound)
	}
	item := googleManagerItem(file, locator.Path)
	item.Locator = locator
	var stream ManagerDownload
	stream.Item = item
	if exportMIME, _ := googleDocsExtension(file.MimeType); exportMIME != "" {
		response, downloadErr := p.driveService.Files.Export(locator.NativeID, exportMIME).Context(ctx).Download()
		if downloadErr != nil {
			return ManagerDownload{}, wrapGoogleError("google manager export download", downloadErr)
		}
		stream.Stream = response.Body
		return stream, nil
	}
	response, downloadErr := p.driveService.Files.Get(locator.NativeID).Context(ctx).Download()
	if downloadErr != nil {
		return ManagerDownload{}, wrapGoogleError("google manager download", downloadErr)
	}
	stream.Stream = response.Body
	return stream, nil
}

// ResolveManagerPath turns a server-stored path into ID-based breadcrumbs.
// Google permits duplicate names, so ambiguous segments deliberately fail
// closed instead of selecting an arbitrary Drive file.
func (p *GoogleProvider) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	clean := strings.Trim(value, "/")
	if clean == "" {
		return ManagerLocator{Path: "/"}, nil, false, nil
	}
	parentID := "root"
	currentPath := "/"
	breadcrumbs := make([]ManagerBreadcrumb, 0, strings.Count(clean, "/")+1)
	for _, segment := range strings.Split(clean, "/") {
		matches, err := p.googleManagerChildrenByName(ctx, parentID, segment)
		if err != nil {
			return ManagerLocator{}, nil, false, err
		}
		if len(matches) == 0 {
			return ManagerLocator{Path: currentPath, NativeID: managerParentID(parentID)}, breadcrumbs, true, nil
		}
		if len(matches) != 1 {
			return ManagerLocator{}, nil, false, ErrAmbiguousPath
		}
		file := matches[0]
		if file.MimeType != googleDriveFolderMIME {
			return ManagerLocator{Path: currentPath, NativeID: managerParentID(parentID)}, breadcrumbs, true, nil
		}
		currentPath = managerChildPath(currentPath, file.Name)
		locator := ManagerLocator{Path: currentPath, NativeID: file.Id}
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{Name: file.Name, Locator: locator})
		parentID = file.Id
	}
	return ManagerLocator{Path: currentPath, NativeID: parentID}, breadcrumbs, false, nil
}

func (p *GoogleProvider) googleManagerChildrenByName(ctx context.Context, parentID, name string) ([]*drive.File, error) {
	query := fmt.Sprintf("'%s' in parents and name = '%s' and trashed = false", parentID, escapeDriveQuery(name))
	result, err := p.driveService.Files.List().Q(query).PageSize(2).Fields("files(id, name, mimeType)").Context(ctx).Do()
	if err != nil {
		return nil, wrapGoogleError("google manager path resolution", err)
	}
	return result.Files, nil
}

func googleManagerItem(file *drive.File, parentPath string) ManagerItem {
	name := file.Name
	size := file.Size
	if _, extension := googleDocsExtension(file.MimeType); extension != "" {
		if !strings.HasSuffix(name, extension) {
			name += extension
		}
		size = 0
	}
	modified, _ := time.Parse(time.RFC3339, file.ModifiedTime)
	return ManagerItem{
		Locator:  ManagerLocator{Path: managerChildPath(parentPath, name), NativeID: file.Id},
		Name:     name,
		IsDir:    file.MimeType == googleDriveFolderMIME,
		Size:     size,
		Modified: modified,
		MIMEType: file.MimeType,
	}
}

func managerChildPath(parent, name string) string {
	if parent == "" || parent == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(parent, "/") + "/" + name
}

func managerParentID(value string) string {
	if value == "root" {
		return ""
	}
	return value
}
