package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/observability"
)

const (
	defaultManagerPageSize   = 100
	managerMaxDirectoryItems = 10000
	managerChunkedThreshold  = 50 * 1024 * 1024
	managerCleanupTimeout    = 15 * time.Second
)

type emulatedManagerCursor struct {
	Offset      int    `json:"offset"`
	Fingerprint string `json:"fingerprint"`
}

type fileManagerAdapter struct {
	providerType   string
	provider       StorageProvider
	capabilities   ManagerCapabilities
	pathSafe       bool
	cleanupTimeout time.Duration
}

// NewFileManager wraps a constructed StorageProvider in a unified, storage-owned
// file-manager adapter that enforces provider capabilities, locator safety, and
// consistent fallback behavior.
func NewFileManager(providerType string, provider StorageProvider) (FileManager, error) {
	if provider == nil || !IsValidProvider(providerType) {
		return nil, ErrManagerUnsupported
	}
	return &fileManagerAdapter{
		providerType:   providerType,
		provider:       provider,
		capabilities:   ManagerCapabilitiesFor(providerType),
		pathSafe:       managerPathMutationProviders[providerType],
		cleanupTimeout: managerCleanupTimeout,
	}, nil
}

// CanonicalManagerPath normalizes a manager path string to a clean forward-slash
// path anchored at "/".
func CanonicalManagerPath(value string) string {
	if value == "" {
		return "/"
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	return path.Clean(normalized)
}

// ValidManagerEntryName validates that name is a safe single path segment for
// file-manager uploads, directory creation, and item mutations.
func ValidManagerEntryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return false
	}
	if len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, ch := range name {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	return true
}

func (m *fileManagerAdapter) Capabilities() ManagerCapabilities {
	return m.capabilities
}

func (m *fileManagerAdapter) IsRootLocator(locator ManagerLocator) bool {
	if locator.NativeID == "" {
		return locator.Path == "" || CanonicalManagerPath(locator.Path) == "/"
	}
	return m.providerType == "google" && locator.NativeID == "root"
}

func (m *fileManagerAdapter) InspectManagerItem(ctx context.Context, locator ManagerLocator) (ManagerItem, error) {
	if !m.capabilities.Browse || !m.pathSafe {
		return ManagerItem{}, ErrManagerUnsupported
	}
	itemPath, err := m.validatePathDirectory(locator)
	if err != nil {
		return ManagerItem{}, err
	}
	if itemPath == "/" {
		return ManagerItem{Locator: ManagerLocator{Path: "/"}, IsDir: true}, nil
	}
	resource, err := m.provider.InspectResource(ctx, "files", itemPath)
	if err != nil {
		return ManagerItem{}, err
	}
	return managerItemFromCloudResource(resource), nil
}

func (m *fileManagerAdapter) ConnectManager(ctx context.Context) (bool, error) {
	if connector, ok := m.provider.(ManagerConnector); ok {
		return connector.ConnectManager(ctx)
	}
	return m.provider.Connect(ctx)
}

func (m *fileManagerAdapter) ListManager(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	if !m.capabilities.Browse {
		return ManagerPage{}, ErrManagerUnsupported
	}
	if err := validateLocatorPath(locator); err != nil {
		return ManagerPage{}, err
	}
	if options.Limit <= 0 {
		options.Limit = defaultManagerPageSize
	}
	lister, hasLister := m.provider.(ManagerLister)
	if !hasLister && !m.pathSafe {
		return ManagerPage{}, ErrManagerUnsupported
	}
	if err := m.validateDirectoryTarget(locator, ErrNotFound); err != nil {
		return ManagerPage{}, err
	}
	if hasLister {
		if m.capabilities.NativePagination {
			page, err := lister.ListManager(ctx, locator, options)
			if err != nil {
				return ManagerPage{}, err
			}
			sortManagerItems(page.Items)
			if page.PaginationMode == "" {
				page.PaginationMode = "native"
			}
			return page, nil
		}
		items, err := listAllNativeItems(ctx, lister, locator)
		if err != nil {
			return ManagerPage{}, err
		}
		return paginateEmulatedManagerItems(items, options)
	}
	return m.listPathFallback(ctx, locator, options)
}

func (m *fileManagerAdapter) ResolveManagerPath(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	if !m.capabilities.Browse {
		return ManagerLocator{}, nil, false, ErrManagerUnsupported
	}
	if err := validateStoragePath(value); err != nil {
		return ManagerLocator{}, nil, false, err
	}
	if resolver, ok := m.provider.(ManagerPathResolver); ok {
		return resolver.ResolveManagerPath(ctx, value)
	}
	if !m.pathSafe {
		return ManagerLocator{}, nil, false, ErrManagerUnsupported
	}
	return m.resolvePathFallback(ctx, value)
}

func (m *fileManagerAdapter) DownloadManager(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	if !m.capabilities.Download {
		return ManagerDownload{}, ErrManagerUnsupported
	}
	if err := validateLocatorPath(locator); err != nil {
		return ManagerDownload{}, err
	}
	downloader, hasDownloader := m.provider.(ManagerDownloader)
	if !hasDownloader && !m.pathSafe {
		return ManagerDownload{}, ErrManagerUnsupported
	}
	if m.IsRootLocator(locator) {
		return ManagerDownload{}, ErrNotFound
	}
	if !m.pathSafe && locator.NativeID == "" {
		return ManagerDownload{}, ErrNotFound
	}
	if hasDownloader {
		if m.pathSafe && locator.Path == "" && m.providerType != "dropbox" {
			return ManagerDownload{}, ErrNotFound
		}
		return downloader.DownloadManager(ctx, locator)
	}
	return m.downloadPathFallback(ctx, locator)
}

func (m *fileManagerAdapter) UploadManager(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	if !m.capabilities.Upload {
		return ManagerUploadResult{}, ErrManagerUnsupported
	}
	if err := m.validateUploadConflictStrategy(options.ConflictStrategy); err != nil {
		return ManagerUploadResult{}, err
	}
	if !ValidManagerEntryName(name) {
		return ManagerUploadResult{}, fmt.Errorf("invalid manager upload name: %w", ErrManagerInvalidDestination)
	}
	if err := validateLocatorPath(parent); err != nil {
		return ManagerUploadResult{}, err
	}
	uploader, hasUploader := m.provider.(ManagerUploader)
	if !hasUploader && !m.pathSafe {
		return ManagerUploadResult{}, ErrManagerUnsupported
	}
	if err := m.validateDirectoryTarget(parent, ErrManagerInvalidDestination); err != nil {
		return ManagerUploadResult{}, err
	}
	if hasUploader {
		return uploader.UploadManager(ctx, parent, name, stream, size, options)
	}
	return m.uploadPathFallback(ctx, parent, name, stream, size, options)
}

func (m *fileManagerAdapter) CreateManagerDirectory(ctx context.Context, parent ManagerLocator, name string) error {
	if !m.capabilities.Mkdir {
		return ErrManagerUnsupported
	}
	if !ValidManagerEntryName(name) {
		return fmt.Errorf("invalid manager directory name: %w", ErrManagerInvalidDestination)
	}
	if err := validateLocatorPath(parent); err != nil {
		return err
	}
	creator, hasCreator := m.provider.(ManagerDirectoryCreator)
	if !hasCreator && !m.pathSafe {
		return ErrManagerUnsupported
	}
	if err := m.validateDirectoryTarget(parent, ErrManagerInvalidDestination); err != nil {
		return err
	}
	if hasCreator {
		return creator.CreateManagerDirectory(ctx, parent, name)
	}
	return m.createDirectoryPathFallback(ctx, parent, name)
}

func (m *fileManagerAdapter) DeleteManagerItem(ctx context.Context, locator ManagerLocator, recursive bool) error {
	if !m.capabilities.DeleteFile {
		return ErrManagerUnsupported
	}
	if err := validateLocatorPath(locator); err != nil {
		return err
	}
	deleter, hasDeleter := m.provider.(ManagerDeleter)
	if !hasDeleter {
		return ErrManagerUnsupported
	}
	if m.IsRootLocator(locator) {
		return ErrManagerUnsupported
	}
	if !m.pathSafe && locator.NativeID == "" {
		return fmt.Errorf("virtual provider delete requires native ID: %w", ErrNotFound)
	}
	if m.pathSafe && locator.Path == "" {
		return ErrManagerUnsupported
	}
	return deleter.DeleteManagerItem(ctx, locator, recursive)
}

func (m *fileManagerAdapter) RenameManagerItem(ctx context.Context, locator, parent ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	if !m.capabilities.Rename {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := validateMutationPaths(locator, parent); err != nil {
		return ManagerMutationResult{}, err
	}
	renamer, ok := NewManagerRenamer(m.providerType, m.provider)
	if !ok {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := m.validateMutationTargets(locator, parent, name); err != nil {
		return ManagerMutationResult{}, err
	}
	return renamer.RenameManagerItem(ctx, locator, parent, name, options)
}

func (m *fileManagerAdapter) MoveManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	if !m.capabilities.Move {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := validateMutationPaths(locator, destination); err != nil {
		return ManagerMutationResult{}, err
	}
	mover, ok := NewManagerMover(m.providerType, m.provider)
	if !ok {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := m.validateMutationTargets(locator, destination, name); err != nil {
		return ManagerMutationResult{}, err
	}
	return mover.MoveManagerItem(ctx, locator, destination, name, options)
}

func (m *fileManagerAdapter) CopyManagerItem(ctx context.Context, locator, destination ManagerLocator, name string, options ManagerMutationOptions) (ManagerMutationResult, error) {
	if !m.capabilities.Copy {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := validateMutationPaths(locator, destination); err != nil {
		return ManagerMutationResult{}, err
	}
	copier, ok := NewManagerCopier(m.providerType, m.provider)
	if !ok {
		return ManagerMutationResult{}, ErrManagerUnsupported
	}
	if err := m.validateMutationTargets(locator, destination, name); err != nil {
		return ManagerMutationResult{}, err
	}
	return copier.CopyManagerItem(ctx, locator, destination, name, options)
}

func (m *fileManagerAdapter) ThumbnailManager(ctx context.Context, locator ManagerLocator, width, height int) (io.ReadCloser, string, error) {
	if !m.capabilities.Thumbnails {
		return nil, "", ErrManagerUnsupported
	}
	if err := validateLocatorPath(locator); err != nil {
		return nil, "", err
	}
	thumbnailer, ok := m.provider.(ManagerThumbnailer)
	if !ok {
		return nil, "", ErrManagerUnsupported
	}
	if m.IsRootLocator(locator) {
		return nil, "", ErrNotFound
	}
	if !m.pathSafe && locator.NativeID == "" {
		return nil, "", ErrNotFound
	}
	if m.pathSafe && locator.Path == "" {
		return nil, "", ErrNotFound
	}
	return thumbnailer.ThumbnailManager(ctx, locator, width, height)
}

func validateLocatorPath(locator ManagerLocator) error {
	if locator.Path == "" {
		return nil
	}
	return validateStoragePath(locator.Path)
}

func validateMutationPaths(locator, destination ManagerLocator) error {
	if err := validateLocatorPath(locator); err != nil {
		return err
	}
	return validateLocatorPath(destination)
}

func (m *fileManagerAdapter) validateDirectoryTarget(locator ManagerLocator, missingVirtualErr error) error {
	if !m.pathSafe {
		if !m.IsRootLocator(locator) && locator.NativeID == "" {
			return missingVirtualErr
		}
		return nil
	}
	if locator.NativeID != "" && locator.Path == "" {
		return missingVirtualErr
	}
	return nil
}

func (m *fileManagerAdapter) validateMutationTargets(locator, destination ManagerLocator, name string) error {
	if m.IsRootLocator(locator) {
		return ErrManagerUnsupported
	}
	if !ValidManagerEntryName(name) {
		return ErrManagerInvalidDestination
	}
	if !m.pathSafe {
		if locator.NativeID == "" {
			return ErrManagerInvalidDestination
		}
		if !m.IsRootLocator(destination) && destination.NativeID == "" {
			return ErrManagerInvalidDestination
		}
		return nil
	}
	if locator.Path == "" || destination.Path == "" {
		return ErrManagerInvalidDestination
	}
	return nil
}

func (m *fileManagerAdapter) validateUploadConflictStrategy(strategy string) error {
	switch ManagerConflictStrategy(strategy) {
	case ManagerConflictSkip:
		if !m.capabilities.ConflictSkip {
			return ErrManagerUnsupported
		}
	case ManagerConflictOverwrite:
		if !m.capabilities.ConflictOverwrite {
			return ErrManagerUnsupported
		}
	case ManagerConflictRename:
		if !m.capabilities.ConflictRename {
			return ErrManagerUnsupported
		}
	default:
		return fmt.Errorf("invalid manager upload conflict strategy: %w", ErrManagerInvalidDestination)
	}
	return nil
}

func (m *fileManagerAdapter) validatePathDirectory(locator ManagerLocator) (string, error) {
	if locator.NativeID != "" && locator.Path == "" {
		return "", ErrNotFound
	}
	if err := validateStoragePath(locator.Path); err != nil {
		return "", err
	}
	return CanonicalManagerPath(locator.Path), nil
}

func listAllNativeItems(ctx context.Context, lister ManagerLister, locator ManagerLocator) ([]ManagerItem, error) {
	items := make([]ManagerItem, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := lister.ListManager(ctx, locator, ManagerListOptions{
			Cursor: cursor,
			Limit:  managerMaxDirectoryItems + 1,
		})
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if len(items) > managerMaxDirectoryItems {
			return nil, ErrManagerDirectoryTooLarge
		}
		if page.NextCursor == "" {
			return items, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return nil, fmt.Errorf("emulated native lister returned repeating cursor")
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

func (m *fileManagerAdapter) listPathFallback(ctx context.Context, locator ManagerLocator, options ManagerListOptions) (ManagerPage, error) {
	dirPath, err := m.validatePathDirectory(locator)
	if err != nil {
		return ManagerPage{}, err
	}
	resources, err := m.provider.GetDirectoryListing(ctx, "files", dirPath)
	if err != nil {
		return ManagerPage{}, err
	}
	if len(resources) > managerMaxDirectoryItems {
		return ManagerPage{}, ErrManagerDirectoryTooLarge
	}

	items := make([]ManagerItem, 0, len(resources))
	for _, resource := range resources {
		items = append(items, managerItemFromCloudResource(resource))
	}
	return paginateEmulatedManagerItems(items, options)
}

func paginateEmulatedManagerItems(items []ManagerItem, options ManagerListOptions) (ManagerPage, error) {
	if len(items) > managerMaxDirectoryItems {
		return ManagerPage{}, ErrManagerDirectoryTooLarge
	}
	sortManagerItems(items)

	fingerprint := managerListingFingerprint(items)
	offset := 0
	if options.Cursor != "" {
		var decoded emulatedManagerCursor
		if json.Unmarshal([]byte(options.Cursor), &decoded) != nil || decoded.Offset < 0 || decoded.Fingerprint == "" {
			return ManagerPage{}, ErrManagerDirectoryChanged
		}
		if decoded.Fingerprint != fingerprint || decoded.Offset > len(items) {
			return ManagerPage{}, ErrManagerDirectoryChanged
		}
		offset = decoded.Offset
	}

	end := offset + options.Limit
	if end > len(items) {
		end = len(items)
	}
	page := ManagerPage{
		Items:          items[offset:end],
		PaginationMode: "emulated",
	}
	if end < len(items) {
		encoded, err := json.Marshal(emulatedManagerCursor{Offset: end, Fingerprint: fingerprint})
		if err != nil {
			return ManagerPage{}, fmt.Errorf("encode manager cursor: %w", err)
		}
		page.NextCursor = string(encoded)
	}
	return page, nil
}

func (m *fileManagerAdapter) resolvePathFallback(ctx context.Context, value string) (ManagerLocator, []ManagerBreadcrumb, bool, error) {
	clean := strings.Trim(CanonicalManagerPath(value), "/")
	if clean == "" {
		return ManagerLocator{Path: "/"}, nil, false, nil
	}

	current := ManagerLocator{Path: "/"}
	breadcrumbs := make([]ManagerBreadcrumb, 0, strings.Count(clean, "/")+1)
	for _, segment := range strings.Split(clean, "/") {
		resources, err := m.provider.GetDirectoryListing(ctx, "files", current.Path)
		if err != nil {
			return ManagerLocator{}, nil, false, err
		}
		matches := make([]CloudResource, 0, 1)
		for _, resource := range resources {
			if resource.IsDir && resource.Name == segment {
				matches = append(matches, resource)
			}
		}
		if len(matches) == 0 {
			return current, breadcrumbs, true, nil
		}
		if len(matches) > 1 {
			return ManagerLocator{}, nil, false, ErrAmbiguousPath
		}
		current = ManagerLocator{Path: CanonicalManagerPath(matches[0].Path)}
		breadcrumbs = append(breadcrumbs, ManagerBreadcrumb{Name: segment, Locator: current})
	}
	return current, breadcrumbs, false, nil
}

func managerItemFromCloudResource(resource CloudResource) ManagerItem {
	itemPath := CanonicalManagerPath(resource.Path)
	name := resource.Name
	if name == "" && itemPath != "/" {
		name = path.Base(itemPath)
	}
	return ManagerItem{
		Locator:  ManagerLocator{Path: itemPath},
		Name:     name,
		IsDir:    resource.IsDir,
		Size:     resource.Size,
		Modified: resource.LastModified,
	}
}

func managerListingFingerprint(items []ManagerItem) string {
	type fingerprintItem struct {
		Locator  ManagerLocator `json:"locator"`
		Name     string         `json:"name"`
		IsDir    bool           `json:"is_dir"`
		Size     int64          `json:"size"`
		Modified time.Time      `json:"modified"`
	}
	values := make([]fingerprintItem, 0, len(items))
	for _, item := range items {
		values = append(values, fingerprintItem{
			Locator:  item.Locator,
			Name:     item.Name,
			IsDir:    item.IsDir,
			Size:     item.Size,
			Modified: item.Modified.UTC(),
		})
	}
	payload, _ := json.Marshal(values)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (m *fileManagerAdapter) downloadPathFallback(ctx context.Context, locator ManagerLocator) (ManagerDownload, error) {
	if locator.Path == "" || m.IsRootLocator(locator) {
		return ManagerDownload{}, ErrNotFound
	}
	cleanPath := CanonicalManagerPath(locator.Path)
	resource, err := m.provider.InspectResource(ctx, "files", cleanPath)
	if err != nil {
		return ManagerDownload{}, err
	}
	if resource.IsDir {
		return ManagerDownload{}, ErrNotFound
	}
	stream, err := m.provider.StreamDownload(ctx, "files", cleanPath)
	if err != nil {
		return ManagerDownload{}, err
	}
	return ManagerDownload{Item: managerItemFromCloudResource(resource), Stream: stream}, nil
}

func (m *fileManagerAdapter) uploadPathFallback(ctx context.Context, parent ManagerLocator, name string, stream io.Reader, size int64, options ManagerUploadOptions) (ManagerUploadResult, error) {
	parentPath, err := m.validatePathDirectory(parent)
	if err != nil {
		return ManagerUploadResult{}, err
	}
	resources, err := m.provider.GetDirectoryListing(ctx, "files", parentPath)
	if err != nil {
		return ManagerUploadResult{}, err
	}

	existingNames := make(map[string]struct{}, len(resources))
	var existing *CloudResource
	for i := range resources {
		existingNames[resources[i].Name] = struct{}{}
		if resources[i].Name == name {
			existing = &resources[i]
		}
	}

	finalName := name
	status := "uploaded"
	if existing != nil {
		switch ManagerConflictStrategy(options.ConflictStrategy) {
		case ManagerConflictSkip:
			return ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		case ManagerConflictOverwrite:
			if existing.IsDir {
				return ManagerUploadResult{}, ErrManagerConflict
			}
		case ManagerConflictRename:
			finalName = findRenamedUploadTarget(name, existingNames)
			if finalName == "" {
				return ManagerUploadResult{}, ErrManagerConflict
			}
			status = "renamed"
		}
	}

	finalPath := path.Join(parentPath, finalName)
	if ManagerConflictStrategy(options.ConflictStrategy) == ManagerConflictOverwrite && existing != nil && m.provider.SupportsAtomicRename() {
		if err := m.uploadAtomicOverwrite(ctx, finalPath, stream, size); err != nil {
			return ManagerUploadResult{}, err
		}
		return ManagerUploadResult{Status: status, FinalName: finalName}, nil
	}
	if err := m.streamUploadBySize(ctx, finalPath, stream, size); err != nil {
		return ManagerUploadResult{}, err
	}
	return ManagerUploadResult{Status: status, FinalName: finalName}, nil
}

func (m *fileManagerAdapter) uploadAtomicOverwrite(ctx context.Context, finalPath string, stream io.Reader, size int64) error {
	tmpPath, backupPath := boundedSiblingPaths(finalPath, time.Now().UnixNano())

	if err := m.streamUploadBySize(ctx, tmpPath, stream, size); err != nil {
		_ = m.runCleanup(ctx, func(c context.Context) error {
			return m.provider.DeleteFile(c, "files", tmpPath)
		})
		return err
	}
	if err := m.provider.RenameFile(ctx, "files", finalPath, backupPath); err != nil {
		_ = m.runCleanup(ctx, func(c context.Context) error {
			return m.provider.DeleteFile(c, "files", tmpPath)
		})
		return err
	}
	if err := m.provider.RenameFile(ctx, "files", tmpPath, finalPath); err != nil {
		_ = m.runCleanup(ctx, func(c context.Context) error {
			return m.provider.DeleteFile(c, "files", tmpPath)
		})
		if rollbackErr := m.runCleanup(ctx, func(c context.Context) error {
			return m.provider.RenameFile(c, "files", backupPath, finalPath)
		}); rollbackErr != nil {
			return fmt.Errorf("promote upload failed: %w; rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	if delErr := m.runCleanup(ctx, func(c context.Context) error {
		return m.provider.DeleteFile(c, "files", backupPath)
	}); delErr != nil {
		slog.WarnContext(context.WithoutCancel(ctx), "failed to remove atomic overwrite backup",
			"provider", m.providerType,
			"error_kind", classifyManagerBackupCleanupError(delErr),
		)
	}
	return nil
}

func classifyManagerBackupCleanupError(err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "authentication"
	case errors.Is(err, ErrPermission):
		return "authorization"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	default:
		return observability.ErrorKind(err)
	}
}

func (m *fileManagerAdapter) runCleanup(ctx context.Context, op func(context.Context) error) error {
	timeout := m.cleanupTimeout
	if timeout <= 0 {
		timeout = managerCleanupTimeout
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return op(cleanupCtx)
}

func boundedSiblingPaths(finalPath string, suffix int64) (tmpPath, backupPath string) {
	dir, base := path.Split(finalPath)
	if dir == "" {
		dir = "/"
	}
	tmpSuffix := fmt.Sprintf(".tmp.%d", suffix)
	bakSuffix := fmt.Sprintf(".bak.%d", suffix)
	maxSuffixLen := len(tmpSuffix)
	if len(bakSuffix) > maxSuffixLen {
		maxSuffixLen = len(bakSuffix)
	}
	maxBaseLen := 255 - maxSuffixLen
	if len(base) > maxBaseLen {
		sum := sha256.Sum256([]byte(base))
		hashTag := "." + hex.EncodeToString(sum[:6])
		base = truncateUTF8Bytes(base, maxBaseLen-len(hashTag)) + hashTag
	}
	return path.Join(dir, base+tmpSuffix), path.Join(dir, base+bakSuffix)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		if size <= 0 {
			return s[:maxBytes]
		}
		s = s[:len(s)-size]
	}
	return s
}

func (m *fileManagerAdapter) streamUploadBySize(ctx context.Context, targetPath string, stream io.Reader, size int64) error {
	if size > managerChunkedThreshold {
		return m.provider.StreamUploadChunked(ctx, "files", targetPath, stream, size, nil)
	}
	return m.provider.StreamUpload(ctx, "files", targetPath, stream, size)
}

func (m *fileManagerAdapter) createDirectoryPathFallback(ctx context.Context, parent ManagerLocator, name string) error {
	parentPath, err := m.validatePathDirectory(parent)
	if err != nil {
		return err
	}
	resources, err := m.provider.GetDirectoryListing(ctx, "files", parentPath)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if resource.Name == name {
			return ErrManagerConflict
		}
	}
	return m.provider.CreateDirectory(ctx, "files", path.Join(parentPath, name))
}

func findRenamedUploadTarget(name string, existingNames map[string]struct{}) string {
	for i := 1; i <= 100; i++ {
		candidate := managerRenamedName(name, i)
		if _, exists := existingNames[candidate]; !exists {
			return candidate
		}
	}
	return ""
}
