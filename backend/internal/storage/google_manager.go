package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
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

var (
	_ ManagerUploader         = (*GoogleProvider)(nil)
	_ ManagerDirectoryCreator = (*GoogleProvider)(nil)
	_ ManagerDeleter          = (*GoogleProvider)(nil)
	_ ManagerThumbnailer      = (*GoogleProvider)(nil)
	_ ManagerRenamer          = (*GoogleProvider)(nil)
	_ ManagerMover            = (*GoogleProvider)(nil)
)

// RenameManagerItem changes a Drive item's name and, when an explicit parent
// is supplied, moves it by immutable ID. An empty parent retains its current
// parent, which is used by ordinary rename requests.
func (p *GoogleProvider) RenameManagerItem(ctx context.Context, locator, parent ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return p.mutateManagerItem(ctx, locator, parent, name, options, "renamed", true)
}

// MoveManagerItem moves a Drive item to a destination parent by immutable ID.
func (p *GoogleProvider) MoveManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	return p.mutateManagerItem(ctx, locator, destination, name, options, "moved", false)
}

func (p *GoogleProvider) mutateManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions, status string, retainParentWhenEmpty bool) (ManagerMutationResult, error) {
	if locator.NativeID == "" || locator.NativeID == "root" {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}
	source, err := p.driveService.Files.Get(locator.NativeID).
		Fields("id,name,mimeType,parents").
		Context(ctx).
		Do()
	if err != nil {
		return ManagerMutationResult{}, wrapGoogleNotFound(err)
	}
	if len(source.Parents) == 0 {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}

	destinationID := destination.NativeID
	if destinationID == "" {
		if retainParentWhenEmpty {
			destinationID = source.Parents[0]
		} else if destination.Path == "/" {
			destinationID = "root"
		} else {
			return ManagerMutationResult{}, ErrManagerInvalidDestination
		}
	}
	// Resolve the root alias to its immutable ID as well. This makes no-op
	// detection and parent replacement consistent for explicit root locators.
	parent, parentErr := p.driveService.Files.Get(destinationID).Fields("id,mimeType,parents").Context(ctx).Do()
	if parentErr != nil {
		return ManagerMutationResult{}, wrapGoogleNotFound(parentErr)
	}
	if parent.MimeType != googleDriveFolderMIME || parent.Id == "" {
		return ManagerMutationResult{}, ErrManagerInvalidDestination
	}
	destinationID = parent.Id
	if source.MimeType == googleDriveFolderMIME {
		cycle, cycleErr := p.googleManagerWouldCreateCycle(ctx, source.Id, destinationID)
		if cycleErr != nil {
			return ManagerMutationResult{}, cycleErr
		}
		if cycle {
			return ManagerMutationResult{}, ErrManagerDirectoryCycle
		}
	}

	matches, err := p.googleManagerChildrenByName(ctx, destinationID, name)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	matches = googleManagerWithoutID(matches, source.Id)
	finalName, resultStatus, conflict, err := p.googleManagerMutationName(ctx, destinationID, name, source.MimeType == googleDriveFolderMIME, matches, options)
	if err != nil {
		return ManagerMutationResult{}, err
	}
	if resultStatus == "skipped" {
		return ManagerMutationResult{Status: resultStatus, FinalName: finalName, Native: true}, nil
	}
	if len(source.Parents) == 1 && source.Parents[0] == destinationID && source.Name == finalName {
		return ManagerMutationResult{}, ErrManagerNoop
	}
	if conflict != nil {
		if _, trashErr := p.driveService.Files.Update(conflict.Id, &drive.File{Trashed: true}).Context(ctx).Do(); trashErr != nil {
			return ManagerMutationResult{}, wrapGoogleError("google manager overwrite", trashErr)
		}
	}
	update := p.driveService.Files.Update(source.Id, &drive.File{Name: finalName}).Context(ctx)
	if destinationID != source.Parents[0] || len(source.Parents) > 1 {
		update = update.AddParents(destinationID).RemoveParents(strings.Join(source.Parents, ","))
	}
	if _, err := update.Do(); err != nil {
		return ManagerMutationResult{}, wrapGoogleError("google manager move", err)
	}
	if resultStatus == "renamed_on_conflict" {
		status = resultStatus
	}
	return ManagerMutationResult{Status: status, FinalName: finalName, Native: true}, nil
}

func (p *GoogleProvider) googleManagerWouldCreateCycle(ctx context.Context, sourceID, destinationID string) (bool, error) {
	visited := make(map[string]struct{}, 16)
	for depth := 0; destinationID != "root" && depth < 100; depth++ {
		if destinationID == sourceID {
			return true, nil
		}
		if _, seen := visited[destinationID]; seen {
			return true, nil
		}
		visited[destinationID] = struct{}{}
		item, err := p.driveService.Files.Get(destinationID).Fields("id,parents").Context(ctx).Do()
		if err != nil {
			return false, wrapGoogleNotFound(err)
		}
		if len(item.Parents) == 0 {
			return false, nil
		}
		destinationID = item.Parents[0]
	}
	return destinationID != "root", nil
}

func (p *GoogleProvider) googleManagerMutationName(ctx context.Context, parentID, name string, sourceIsDir bool, matches []*drive.File, options ManagerMutationOptions) (string, string, *drive.File, error) {
	if len(matches) == 0 {
		return name, "", nil, nil
	}
	switch options.ConflictStrategy {
	case ManagerConflictSkip:
		return name, "skipped", nil, nil
	case ManagerConflictRename:
		for suffix := 1; suffix <= 100; suffix++ {
			candidate := managerRenamedName(name, suffix)
			candidateMatches, err := p.googleManagerChildrenByName(ctx, parentID, candidate)
			if err != nil {
				return "", "", nil, err
			}
			if len(candidateMatches) == 0 {
				return candidate, "renamed_on_conflict", nil, nil
			}
		}
		return "", "", nil, ErrManagerConflict
	case ManagerConflictOverwrite:
		if sourceIsDir || len(matches) != 1 || matches[0].MimeType == googleDriveFolderMIME {
			return "", "", nil, ErrManagerConflict
		}
		return name, "", matches[0], nil
	default:
		return "", "", nil, ErrManagerConflict
	}
}

func googleManagerWithoutID(items []*drive.File, id string) []*drive.File {
	filtered := items[:0]
	for _, item := range items {
		if item.Id != id {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// DeleteManagerItem moves the selected Drive item to trash by immutable ID.
// It intentionally does not use the migration-oriented path deletion method:
// Drive permits duplicate sibling names.
func (p *GoogleProvider) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	if locator.NativeID == "" || locator.NativeID == "root" {
		return fmt.Errorf("google manager delete: %w", ErrNotFound)
	}
	item, err := p.driveService.Files.Get(locator.NativeID).Fields("id,mimeType").Context(ctx).Do()
	if err != nil {
		return wrapGoogleNotFound(err)
	}
	if item.MimeType == googleDriveFolderMIME && !recursive {
		children, childErr := p.driveService.Files.List().
			Q(fmt.Sprintf("'%s' in parents and trashed = false", locator.NativeID)).
			PageSize(1).
			Fields("files(id)").
			Context(ctx).
			Do()
		if childErr != nil {
			return wrapGoogleNotFound(childErr)
		}
		if len(children.Files) > 0 {
			return ErrManagerDirectoryNotEmpty
		}
	}
	if _, err := p.driveService.Files.Update(locator.NativeID, &drive.File{Trashed: true}).Context(ctx).Do(); err != nil {
		return wrapGoogleNotFound(err)
	}
	return nil
}

// CreateManagerDirectory creates a new directory in the selected Drive parent by immutable ID.
func (p *GoogleProvider) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	parentID := "root"
	if parent.NativeID != "" {
		parentID = parent.NativeID
	}

	matches, err := p.googleManagerChildrenByName(ctx, parentID, name)
	if err != nil {
		return err
	}
	if len(matches) > 0 {
		return ErrManagerConflict
	}

	file := &drive.File{
		Name:     name,
		MimeType: googleDriveFolderMIME,
		Parents:  []string{parentID},
	}
	if _, err := p.driveService.Files.Create(file).Context(ctx).Do(); err != nil {
		return wrapGoogleError("google manager mkdir", err)
	}
	return nil
}

// UploadManager uploads into the selected Drive parent by immutable ID. It
// never resolves a display path, which prevents duplicate sibling names from
// selecting an arbitrary target.
func (p *GoogleProvider) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	parentID := "root"
	if parent.NativeID != "" {
		parentID = parent.NativeID
	}

	matches, err := p.googleManagerChildrenByName(ctx, parentID, name)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	switch options.ConflictStrategy {
	case "SKIP":
		if len(matches) > 0 {
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		}
	case "OVERWRITE":
		switch len(matches) {
		case 0:
			// Create below.
		case 1:
			if matches[0].MimeType == googleDriveFolderMIME {
				return ManagerUploadResult{}, ErrManagerConflict
			}
			call := p.driveService.Files.Update(matches[0].Id, &drive.File{Name: name}).Context(ctx)
			if size > 50*1024*1024 {
				call = call.Media(stream, googleapi.ChunkSize(googleapi.DefaultUploadChunkSize))
			} else {
				call = call.Media(stream)
			}
			if _, err := call.Do(); err != nil {
				return ManagerUploadResult{}, wrapGoogleError("google manager overwrite", err)
			}
			return ManagerUploadResult{Status: "uploaded", FinalName: name}, nil
		default:
			return ManagerUploadResult{}, ErrAmbiguousPath
		}
	case "RENAME":
		if len(matches) > 0 {
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := managerRenamedName(name, suffix)
				candidateMatches, candidateErr := p.googleManagerChildrenByName(ctx, parentID, candidate)
				if candidateErr != nil {
					return ManagerUploadResult{}, candidateErr
				}
				if len(candidateMatches) == 0 {
					name = candidate
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

	file := &drive.File{Name: name, Parents: []string{parentID}}
	call := p.driveService.Files.Create(file).Context(ctx)
	if size > 50*1024*1024 {
		call = call.Media(stream, googleapi.ChunkSize(googleapi.DefaultUploadChunkSize))
	} else {
		call = call.Media(stream)
	}
	if _, err := call.Do(); err != nil {
		return ManagerUploadResult{}, wrapGoogleError("google manager upload", err)
	}
	status := "uploaded"
	if name != "" && options.ConflictStrategy == "RENAME" && len(matches) > 0 {
		status = "renamed"
	}
	return ManagerUploadResult{Status: status, FinalName: name}, nil
}

func managerRenamedName(name string, suffix int) string {
	lastDot := strings.LastIndex(name, ".")
	if lastDot > 0 {
		return fmt.Sprintf("%s (%d)%s", name[:lastDot], suffix, name[lastDot:])
	}
	return fmt.Sprintf("%s (%d)", name, suffix)
}

// ConnectManager verifies only Drive access. The generic provider connection
// also probes Calendar and People, which is appropriate for migrations that
// use those resource types but incorrectly rejects files-only manager access.
func (p *GoogleProvider) ConnectManager(ctx context.Context) (bool, error) {
	if _, err := p.driveService.About.Get().Fields("user").Context(ctx).Do(); err != nil {
		return false, wrapGoogleError("google drive manager connect", err)
	}
	return true, nil
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

// ThumbnailManager fetches a thumbnail image from Google Drive for the given file locator.
func (p *GoogleProvider) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	if locator.NativeID == "" {
		return nil, "", fmt.Errorf("Google manager item ID is required")
	}

	file, err := p.driveService.Files.Get(locator.NativeID).
		Fields("id, mimeType, thumbnailLink").
		Context(ctx).
		Do()
	if err != nil {
		return nil, "", wrapGoogleNotFound(err)
	}

	if file.MimeType == googleDriveFolderMIME {
		return nil, "", fmt.Errorf("Google manager thumbnail: %w", ErrNotFound)
	}

	if file.ThumbnailLink == "" {
		return nil, "", ErrUnsupportedMedia
	}

	thumbnailURL := file.ThumbnailLink
	if width > 0 || height > 0 {
		maxDim := width
		if height > maxDim {
			maxDim = height
		}
		if maxDim < 32 {
			maxDim = 32
		} else if maxDim > 1024 {
			maxDim = 1024
		}
		if idx := strings.LastIndex(thumbnailURL, "=s"); idx != -1 {
			thumbnailURL = thumbnailURL[:idx] + fmt.Sprintf("=s%d", maxDim)
		}
	}

	// Validate the thumbnail URL host before fetching: the link comes from the
	// Google Drive API and should only ever point to Google CDN domains. Reject
	// anything else to prevent SSRF through a provider-supplied URL.
	parsedURL, parseErr := url.Parse(thumbnailURL)
	if parseErr != nil {
		return nil, "", fmt.Errorf("google thumbnail: invalid url: %w", parseErr)
	}
	host := strings.ToLower(parsedURL.Hostname())
	if !isGoogleTrustedThumbnailHost(host) {
		return nil, "", fmt.Errorf("google thumbnail: untrusted host %q: %w", host, ErrUnsupportedMedia)
	}

	client := p.httpClient
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, thumbnailURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("google thumbnail request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("google thumbnail fetch: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", fmt.Errorf("google thumbnail: %w", ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, "", fmt.Errorf("google thumbnail unauthorized: %w", ErrAuth)
		}
		return nil, "", fmt.Errorf("google thumbnail unexpected status: %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return resp.Body, contentType, nil
}

func isGoogleTrustedThumbnailHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "google.com" || strings.HasSuffix(host, ".google.com") ||
		host == "googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com") ||
		host == "ggpht.com" || strings.HasSuffix(host, ".ggpht.com")
}
