package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
	"github.com/redis/go-redis/v9"
)

const (
	fileBrowseRateLimit       = 120
	fileMutationRateLimit     = 120
	fileUploadRateLimit       = 600
	fileThumbnailRateLimit    = 3600
	fileRateWindow            = time.Minute
	fileDefaultPageSize       = 100
	fileMaximumPageSize       = 200
	fileMaximumDirectoryItems = 10000
	fileTicketTTL             = time.Minute
	fileStreamLease           = 2 * time.Minute
	fileMaxStreamsPerUser     = 4
)

type fileReference struct {
	UserID       string                 `json:"user_id"`
	ProfileID    string                 `json:"profile_id"`
	ResourceType string                 `json:"resource_type"`
	Kind         string                 `json:"kind"`
	Locator      storage.ManagerLocator `json:"locator"`
}

type fileCursor struct {
	UserID         string                 `json:"user_id"`
	ProfileID      string                 `json:"profile_id"`
	ResourceType   string                 `json:"resource_type"`
	Parent         storage.ManagerLocator `json:"parent"`
	ProviderCursor string                 `json:"provider_cursor"`
}

type fileListRequest struct {
	ResourceType string `json:"resource_type"`
	ParentRef    string `json:"parent_ref"`
	Cursor       string `json:"cursor"`
	Limit        int    `json:"limit"`
}

type fileEntry struct {
	Ref            string   `json:"ref"`
	ParentRef      string   `json:"parent_ref,omitempty"`
	Name           string   `json:"name"`
	DisplayPath    string   `json:"display_path"`
	Kind           string   `json:"kind"`
	Size           int64    `json:"size"`
	ModifiedAt     string   `json:"modified_at,omitempty"`
	MIMEType       string   `json:"mime_type,omitempty"`
	AllowedActions []string `json:"allowed_actions"`
}

type fileDownloadTicketRequest struct {
	Ref string `json:"ref"`
}

type fileThumbnailRequest struct {
	Ref    string `json:"ref"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type fileUploadResponse struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	Name    string `json:"name"`
}

type fileDirectoryCreateRequest struct {
	Name      string `json:"name"`
	ParentRef string `json:"parent_ref"`
}

type fileDeleteRequest struct {
	Ref       string `json:"ref"`
	Recursive bool   `json:"recursive"`
}

type fileMutationRequest struct {
	Ref                  string `json:"ref"`
	DestinationParentRef string `json:"destination_parent_ref,omitempty"`
	NewName              string `json:"new_name,omitempty"`
	ConflictStrategy     string `json:"conflict_strategy,omitempty"`
}

type fileMutationResponse struct {
	Success bool   `json:"success"`
	Status  string `json:"status"`
	Name    string `json:"name"`
	Native  bool   `json:"native"`
}

type fileDirectoryCreateResponse struct {
	Success bool   `json:"success"`
	Name    string `json:"name"`
}

type fileResolveRequest struct {
	ResourceType string `json:"resource_type"`
	Path         string `json:"path"`
}

type fileBreadcrumb struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type legacyManagerCursor struct {
	Offset      int    `json:"offset"`
	Fingerprint string `json:"fingerprint"`
}

// fileProfileSummary intentionally excludes endpoint and credential-adjacent
// profile fields. The file-manager API only needs identity and display data.
type fileProfileSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

func summarizeFileProfile(profile *db.ConnectionProfile) fileProfileSummary {
	return fileProfileSummary{
		ID:       profile.ID,
		Name:     profile.Name,
		Provider: profile.Provider,
	}
}

type fileDownloadTicket struct {
	UserID    string `json:"user_id"`
	ProfileID string `json:"profile_id"`
	Ref       string `json:"ref"`
	StreamID  string `json:"stream_id"`
}

var acquireFileStreamScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[3], ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return 1
`)

var renewFileStreamScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if not redis.call('ZSCORE', KEYS[1], ARGV[4]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`)

var (
	errManagerDirectoryChanged  = errors.New("file manager directory changed")
	errManagerDirectoryTooLarge = errors.New("file manager directory too large")
)

func (s *APIServer) fileRateKey(r *http.Request, userID string) string {
	return userID + "|" + s.clientIP(r)
}

func (s *APIServer) allowFileRequest(r *http.Request, userID, scope string, limit int) bool {
	return s.rateLimiter.Allow(r.Context(), scope, s.fileRateKey(r, userID), limit, fileRateWindow)
}

func sealFileReference(reference fileReference, encryptionKey string) (string, error) {
	encoded, err := json.Marshal(reference)
	if err != nil {
		return "", err
	}
	return crypto.EncryptWithDomain(string(encoded), encryptionKey, crypto.DomainFileManagerReference)
}

func openFileReference(value, encryptionKey, userID, profileID string) (fileReference, error) {
	plain, err := crypto.DecryptBytesWithDomain(value, encryptionKey, crypto.DomainFileManagerReference)
	if err != nil {
		return fileReference{}, err
	}
	defer clear(plain)
	var reference fileReference
	if err := json.Unmarshal(plain, &reference); err != nil || reference.UserID != userID || reference.ProfileID != profileID || reference.ResourceType != "files" || (reference.Kind != "file" && reference.Kind != "directory") || !validManagedLocator(reference.Locator) {
		return fileReference{}, errors.New("invalid file reference")
	}
	return reference, nil
}

func sealFileCursor(cursor fileCursor, encryptionKey string) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return crypto.EncryptWithDomain(string(encoded), encryptionKey, crypto.DomainFileManagerCursor)
}

func openFileCursor(value, encryptionKey, userID, profileID string) (fileCursor, error) {
	plain, err := crypto.DecryptBytesWithDomain(value, encryptionKey, crypto.DomainFileManagerCursor)
	if err != nil {
		return fileCursor{}, err
	}
	defer clear(plain)
	var cursor fileCursor
	if err := json.Unmarshal(plain, &cursor); err != nil || cursor.UserID != userID || cursor.ProfileID != profileID || cursor.ResourceType != "files" || !validManagedCursorParent(cursor.Parent) {
		return fileCursor{}, errors.New("invalid file cursor")
	}
	return cursor, nil
}

func validManagedPath(value string) bool {
	if value == "" || value[0] != '/' || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

func managedRootPath() string { return "/" }

func canonicalManagedPath(value string) string {
	if value == "" {
		return managedRootPath()
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return path.Clean(value)
}

func validManagedLocator(locator storage.ManagerLocator) bool {
	if locator.NativeID != "" {
		return !strings.ContainsRune(locator.NativeID, 0)
	}
	return validManagedPath(locator.Path)
}

func validManagedCursorParent(locator storage.ManagerLocator) bool {
	return validManagedLocator(locator)
}

func sameManagedLocator(left, right storage.ManagerLocator) bool {
	return left.NativeID == right.NativeID && left.Path == right.Path && left.Library == right.Library
}

func sortManagedEntries(resources []storage.ManagerItem) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].IsDir != resources[j].IsDir {
			return resources[i].IsDir
		}
		left, right := strings.ToLower(strings.TrimSpace(resources[i].Name)), strings.ToLower(strings.TrimSpace(resources[j].Name))
		if left != right {
			return left < right
		}
		return managedLocatorIdentity(resources[i].Locator) < managedLocatorIdentity(resources[j].Locator)
	})
}

func managedLocatorIdentity(locator storage.ManagerLocator) string {
	if locator.NativeID != "" {
		return "id:" + locator.NativeID
	}
	return "path:" + locator.Library + ":" + locator.Path
}

func managerItemFromCloudResource(resource storage.CloudResource) storage.ManagerItem {
	itemPath := canonicalManagedPath(resource.Path)
	if resource.Name == "" && itemPath != managedRootPath() {
		resource.Name = path.Base(itemPath)
	}
	return storage.ManagerItem{
		Locator:  storage.ManagerLocator{Path: itemPath},
		Name:     resource.Name,
		IsDir:    resource.IsDir,
		Size:     resource.Size,
		Modified: resource.LastModified,
		MIMEType: "",
	}
}

func managerListingFingerprint(items []storage.ManagerItem) string {
	type fingerprintItem struct {
		Locator  storage.ManagerLocator `json:"locator"`
		Name     string                 `json:"name"`
		IsDir    bool                   `json:"is_dir"`
		Size     int64                  `json:"size"`
		Modified time.Time              `json:"modified"`
	}
	values := make([]fingerprintItem, 0, len(items))
	for _, item := range items {
		values = append(values, fingerprintItem{Locator: item.Locator, Name: item.Name, IsDir: item.IsDir, Size: item.Size, Modified: item.Modified.UTC()})
	}
	payload, _ := json.Marshal(values)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func listLegacyManager(ctx context.Context, provider storage.StorageProvider, parent storage.ManagerLocator, cursor string, limit int) (storage.ManagerPage, error) {
	resources, err := provider.GetDirectoryListing(ctx, "files", parent.Path)
	if err != nil {
		return storage.ManagerPage{}, err
	}
	if len(resources) > fileMaximumDirectoryItems {
		return storage.ManagerPage{}, errManagerDirectoryTooLarge
	}
	items := make([]storage.ManagerItem, 0, len(resources))
	for _, resource := range resources {
		items = append(items, managerItemFromCloudResource(resource))
	}
	sortManagedEntries(items)
	fingerprint := managerListingFingerprint(items)
	offset := 0
	if cursor != "" {
		var decoded legacyManagerCursor
		if json.Unmarshal([]byte(cursor), &decoded) != nil || decoded.Offset < 0 || decoded.Fingerprint == "" {
			return storage.ManagerPage{}, errManagerDirectoryChanged
		}
		if decoded.Fingerprint != fingerprint || decoded.Offset > len(items) {
			return storage.ManagerPage{}, errManagerDirectoryChanged
		}
		offset = decoded.Offset
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := storage.ManagerPage{Items: items[offset:end]}
	if end < len(items) {
		encoded, _ := json.Marshal(legacyManagerCursor{Offset: end, Fingerprint: fingerprint})
		page.NextCursor = string(encoded)
	}
	return page, nil
}

func resolveLegacyManagerPath(ctx context.Context, provider storage.StorageProvider, value string) (storage.ManagerLocator, []storage.ManagerBreadcrumb, bool, error) {
	clean := strings.Trim(canonicalManagedPath(value), "/")
	if clean == "" {
		return storage.ManagerLocator{Path: managedRootPath()}, nil, false, nil
	}
	current := storage.ManagerLocator{Path: managedRootPath()}
	breadcrumbs := make([]storage.ManagerBreadcrumb, 0, strings.Count(clean, "/")+1)
	for _, segment := range strings.Split(clean, "/") {
		resources, err := provider.GetDirectoryListing(ctx, "files", current.Path)
		if err != nil {
			return storage.ManagerLocator{}, nil, false, err
		}
		matches := make([]storage.CloudResource, 0, 1)
		for _, resource := range resources {
			if resource.IsDir && resource.Name == segment {
				matches = append(matches, resource)
			}
		}
		switch len(matches) {
		case 0:
			return current, breadcrumbs, true, nil
		case 1:
			current = managerItemFromCloudResource(matches[0]).Locator
			breadcrumbs = append(breadcrumbs, storage.ManagerBreadcrumb{Name: matches[0].Name, Locator: current})
		default:
			return storage.ManagerLocator{}, nil, false, storage.ErrAmbiguousPath
		}
	}
	return current, breadcrumbs, false, nil
}

func downloadLegacyManager(ctx context.Context, provider storage.StorageProvider, locator storage.ManagerLocator) (storage.ManagerDownload, error) {
	if locator.Path == "" {
		return storage.ManagerDownload{}, storage.ErrNotFound
	}
	resource, err := provider.InspectResource(ctx, "files", locator.Path)
	if err != nil {
		return storage.ManagerDownload{}, err
	}
	if resource.IsDir {
		return storage.ManagerDownload{}, storage.ErrNotFound
	}
	stream, err := provider.StreamDownload(ctx, "files", locator.Path)
	if err != nil {
		return storage.ManagerDownload{}, err
	}
	return storage.ManagerDownload{Item: managerItemFromCloudResource(resource), Stream: stream}, nil
}

func managerRenamedName(name string, suffix int) string {
	lastDot := strings.LastIndex(name, ".")
	if lastDot > 0 {
		return fmt.Sprintf("%s (%d)%s", name[:lastDot], suffix, name[lastDot:])
	}
	return fmt.Sprintf("%s (%d)", name, suffix)
}

func uploadLegacyManager(ctx context.Context, provider storage.StorageProvider, parent storage.ManagerLocator, name string, stream io.Reader, size int64, options storage.ManagerUploadOptions) (storage.ManagerUploadResult, error) {
	parentPath := parent.Path
	if parentPath == "" {
		parentPath = managedRootPath()
	}
	resources, err := provider.GetDirectoryListing(ctx, "files", parentPath)
	if err != nil {
		return storage.ManagerUploadResult{}, err
	}
	var existing *storage.CloudResource
	existingNames := make(map[string]bool, len(resources))
	for i := range resources {
		existingNames[resources[i].Name] = true
		if resources[i].Name == name {
			existing = &resources[i]
		}
	}

	finalName := name
	switch options.ConflictStrategy {
	case "SKIP":
		if existing != nil {
			return storage.ManagerUploadResult{Status: "skipped", FinalName: name}, nil
		}
	case "OVERWRITE":
		if existing != nil && existing.IsDir {
			return storage.ManagerUploadResult{}, storage.ErrManagerConflict
		}
	case "RENAME":
		if existing != nil {
			found := false
			for suffix := 1; suffix <= 100; suffix++ {
				candidate := managerRenamedName(name, suffix)
				if !existingNames[candidate] {
					finalName = candidate
					found = true
					break
				}
			}
			if !found {
				return storage.ManagerUploadResult{}, storage.ErrManagerConflict
			}
		}
	default:
		return storage.ManagerUploadResult{}, errors.New("invalid manager upload conflict strategy")
	}

	cleanParent := strings.TrimSuffix(parentPath, "/")
	if cleanParent == "" {
		cleanParent = "/"
	}
	targetPath := path.Join(cleanParent, finalName)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	if err := provider.StreamUpload(ctx, "files", targetPath, stream, size); err != nil {
		return storage.ManagerUploadResult{}, err
	}
	status := "uploaded"
	if finalName != name {
		status = "renamed"
	}
	return storage.ManagerUploadResult{Status: status, FinalName: finalName}, nil
}

func createDirectoryLegacyManager(ctx context.Context, provider storage.StorageProvider, parent storage.ManagerLocator, name string) error {
	parentPath := parent.Path
	if parentPath == "" {
		parentPath = managedRootPath()
	}
	resources, err := provider.GetDirectoryListing(ctx, "files", parentPath)
	if err != nil {
		return err
	}
	for i := range resources {
		if resources[i].Name == name {
			return storage.ErrManagerConflict
		}
	}

	cleanParent := strings.TrimSuffix(parentPath, "/")
	if cleanParent == "" {
		cleanParent = "/"
	}
	targetPath := path.Join(cleanParent, name)
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}

	return provider.CreateDirectory(ctx, "files", targetPath)
}

func allowedFileActions(capabilities storage.ManagerCapabilities, isDir bool) []string {
	actions := make([]string, 0, 6)
	if !isDir && capabilities.Download {
		actions = append(actions, "download")
	}
	if isDir && capabilities.Upload {
		actions = append(actions, "upload")
	}
	if (!isDir && capabilities.DeleteFile) || (isDir && (capabilities.DeleteEmptyDirectory || capabilities.DeleteRecursiveDirectory)) {
		actions = append(actions, "delete")
	}
	if capabilities.Rename {
		actions = append(actions, "rename")
	}
	if capabilities.Move {
		actions = append(actions, "move", "copy")
	}
	return actions
}

func isManagedRootLocator(provider string, locator storage.ManagerLocator) bool {
	if locator.NativeID == "" {
		return locator.Path == "" || canonicalManagedPath(locator.Path) == managedRootPath()
	}
	return provider == "google" && locator.NativeID == "root"
}

func (s *APIServer) handleFileEntryDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileDeleteRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	reference, referenceErr := openFileReference(request.Ref, s.encryptionKey, userID, profileID)
	if referenceErr != nil {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	if isManagedRootLocator(resolved.profile.Provider, reference.Locator) {
		writeValidationError(w, ErrFilesRootMutationForbidden)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(resolved.profile.Provider)
	supported := (reference.Kind == "file" && capabilities.DeleteFile) ||
		(reference.Kind == "directory" && !request.Recursive && capabilities.DeleteEmptyDirectory) ||
		(reference.Kind == "directory" && request.Recursive && capabilities.DeleteRecursiveDirectory)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	deleter, ok := resolved.provider.(storage.ManagerDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	if err := deleter.DeleteManagerItem(resolved.ctx, reference.Locator, request.Recursive); err != nil {
		s.writeFileProviderError(w, err)
		return
	}
	s.writeAudit(r, db.AuditFileItemDeleted, profileID, userID, map[string]interface{}{
		"provider":  resolved.profile.Provider,
		"item_kind": reference.Kind,
		"recursive": request.Recursive,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *APIServer) handleFileEntryRename(w http.ResponseWriter, r *http.Request) {
	s.handleFileEntryMutation(w, r, "rename")
}

func (s *APIServer) handleFileEntryCopy(w http.ResponseWriter, r *http.Request) {
	s.handleFileEntryMutation(w, r, "copy")
}

func (s *APIServer) handleFileEntryMove(w http.ResponseWriter, r *http.Request) {
	s.handleFileEntryMutation(w, r, "move")
}

func (s *APIServer) handleFileEntryMutation(w http.ResponseWriter, r *http.Request, operation string) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileMutationRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	source, err := openFileReference(request.Ref, s.encryptionKey, userID, profileID)
	if err != nil {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	if source.Locator.Path == "" {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	if source.Locator.Path == "" {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	name := strings.TrimSpace(request.NewName)
	if name == "" {
		name = path.Base(source.Locator.Path)
	}
	if !validManagerUploadName(name) {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	destination := storage.ManagerLocator{Path: managedRootPath()}
	if operation == "rename" {
		destination = storage.ManagerLocator{Path: path.Dir(source.Locator.Path)}
	}
	if request.DestinationParentRef != "" {
		destinationRef, destinationErr := openFileReference(request.DestinationParentRef, s.encryptionKey, userID, profileID)
		if destinationErr != nil || destinationRef.Kind != "directory" || destinationRef.Locator.Path == "" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		destination = destinationRef.Locator
	}
	if destination.Path == "" {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	if isManagedRootLocator(resolved.profile.Provider, source.Locator) {
		writeValidationError(w, ErrFilesRootMutationForbidden)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(resolved.profile.Provider)
	if (operation == "rename" && !capabilities.Rename) || (operation != "rename" && !capabilities.Move) {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	strategy := strings.ToUpper(strings.TrimSpace(request.ConflictStrategy))
	if strategy != "" && strategy != "SKIP" && strategy != "OVERWRITE" && strategy != "RENAME" {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	options := storage.ManagerMutationOptions{ConflictStrategy: storage.ManagerConflictStrategy(strategy)}
	mutator := storage.NewPathManagerMutator(resolved.provider)
	var result storage.ManagerMutationResult
	if operation != "rename" {
		streamID := generateRandomString(16)
		if streamID == "" || !s.acquireFileStream(r.Context(), userID, "mutation", streamID, fileStreamLease) {
			writeError(w, http.StatusTooManyRequests, ErrFilesStreamLimitReached)
			return
		}
		defer s.releaseFileStream(r.Context(), userID, "mutation", streamID)
		stopLeaseRenewal := make(chan struct{})
		defer close(stopLeaseRenewal)
		go s.renewFileStreamLease(stopLeaseRenewal, userID, "mutation", streamID)
	}
	switch operation {
	case "rename":
		result, err = mutator.RenameManagerItem(resolved.ctx, source.Locator, destination, name, options)
	case "copy":
		result, err = mutator.CopyManagerItem(resolved.ctx, source.Locator, destination, name, options)
	case "move":
		result, err = mutator.MoveManagerItem(resolved.ctx, source.Locator, destination, name, options)
	}
	if err != nil {
		s.writeFileMutationError(w, err, capabilities)
		return
	}
	if result.FinalName == "" {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	action := db.AuditFileItemRenamed
	if operation == "copy" {
		action = db.AuditFileItemCopied
	} else if operation == "move" {
		action = db.AuditFileItemMoved
	}
	s.writeAudit(r, action, profileID, userID, map[string]interface{}{
		"provider":              resolved.profile.Provider,
		"source_item_kind":      source.Kind,
		"destination_item_kind": "directory",
		"conflict_strategy":     strategy,
		"native":                result.Native,
		"outcome":               result.Status,
	})
	writeJSON(w, http.StatusOK, fileMutationResponse{Success: true, Status: result.Status, Name: result.FinalName, Native: result.Native})
}

func (s *APIServer) writeFileMutationError(w http.ResponseWriter, err error, capabilities storage.ManagerCapabilities) {
	switch {
	case errors.Is(err, storage.ErrManagerConflict):
		strategies := make([]string, 0, 3)
		if capabilities.ConflictSkip {
			strategies = append(strategies, "SKIP")
		}
		if capabilities.ConflictOverwrite {
			strategies = append(strategies, "OVERWRITE")
		}
		if capabilities.ConflictRename {
			strategies = append(strategies, "RENAME")
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error_code": ErrFilesConflict, "conflict_strategies": strategies})
	case errors.Is(err, storage.ErrManagerDirectoryCycle), errors.Is(err, storage.ErrManagerInvalidDestination):
		writeValidationError(w, ErrFilesInvalidDestination)
	case errors.Is(err, storage.ErrManagerNoop):
		writeValidationError(w, ErrFilesNoop)
	case errors.Is(err, storage.ErrManagerPartial):
		writeError(w, http.StatusConflict, ErrFilesPartialOperation)
	default:
		s.writeFileProviderError(w, err)
	}
}

func (s *APIServer) handleFileCapabilities(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-browse", fileBrowseRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	profileID := r.PathValue("profileID")
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"profile":      summarizeFileProfile(profile),
		"capabilities": capabilities,
		// Transitional aliases keep the Phase-1 client small while the complete
		// capability object remains the public contract.
		"can_list":     capabilities.Browse,
		"can_download": capabilities.Download,
	})
}

func (s *APIServer) handleFileEntriesList(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-browse", fileBrowseRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileListRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	if request.ResourceType != "" && request.ResourceType != "files" {
		writeValidationError(w, ErrInvalidResourceType)
		return
	}
	if request.Limit <= 0 {
		request.Limit = fileDefaultPageSize
	}
	if request.Limit > fileMaximumPageSize {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	profileID := r.PathValue("profileID")
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	if !capabilities.Browse {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()

	parent := storage.ManagerLocator{Path: managedRootPath()}
	parentRef := request.ParentRef
	if request.ParentRef != "" {
		reference, referenceErr := openFileReference(request.ParentRef, s.encryptionKey, userID, profileID)
		if referenceErr != nil || reference.Kind != "directory" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		parent = reference.Locator
	}
	providerCursor := ""
	if request.Cursor != "" {
		cursor, cursorErr := openFileCursor(request.Cursor, s.encryptionKey, userID, profileID)
		if cursorErr != nil || !sameManagedLocator(cursor.Parent, parent) {
			writeValidationError(w, ErrFilesInvalidCursor)
			return
		}
		providerCursor = cursor.ProviderCursor
	}

	paginationMode := "emulated"
	var page storage.ManagerPage
	var listErr error
	if lister, supported := resolved.provider.(storage.ManagerLister); supported {
		page, listErr = lister.ListManager(resolved.ctx, parent, storage.ManagerListOptions{Cursor: providerCursor, Limit: request.Limit})
		paginationMode = "native"
	} else {
		page, listErr = listLegacyManager(resolved.ctx, resolved.provider, parent, providerCursor, request.Limit)
	}
	if listErr != nil {
		s.writeFileProviderError(w, listErr)
		return
	}
	if len(page.Items) > request.Limit {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	sortManagedEntries(page.Items)
	entries := make([]fileEntry, 0, len(page.Items))
	for _, resource := range page.Items {
		kind := "file"
		if resource.IsDir {
			kind = "directory"
		}
		ref, sealErr := sealFileReference(fileReference{UserID: userID, ProfileID: profileID, ResourceType: "files", Kind: kind, Locator: resource.Locator}, s.encryptionKey)
		if sealErr != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		entry := fileEntry{Ref: ref, ParentRef: parentRef, Name: resource.Name, DisplayPath: resource.Locator.Path, Kind: kind, Size: resource.Size, AllowedActions: allowedFileActions(capabilities, resource.IsDir)}
		if !resource.Modified.IsZero() {
			entry.ModifiedAt = resource.Modified.UTC().Format(time.RFC3339)
		}
		if !resource.IsDir {
			entry.MIMEType = resource.MIMEType
			if entry.MIMEType == "" {
				entry.MIMEType = mime.TypeByExtension(strings.ToLower(path.Ext(resource.Name)))
			}
		}
		entries = append(entries, entry)
	}
	nextCursor := ""
	if page.NextCursor != "" {
		nextCursor, err = sealFileCursor(fileCursor{UserID: userID, ProfileID: profileID, ResourceType: "files", Parent: parent, ProviderCursor: page.NextCursor}, s.encryptionKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":         true,
		"profile":         summarizeFileProfile(resolved.profile),
		"capabilities":    capabilities,
		"parent":          map[string]any{"ref": parentRef, "kind": "directory"},
		"entries":         entries,
		"next_cursor":     nextCursor,
		"pagination_mode": paginationMode,
	})
}

func (s *APIServer) handleFileEntriesResolve(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-browse", fileBrowseRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileResolveRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	if request.ResourceType != "" && request.ResourceType != "files" {
		writeValidationError(w, ErrInvalidResourceType)
		return
	}
	if request.Path == "" {
		request.Path = managedRootPath()
	}
	if !validManagedPath(request.Path) {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	request.Path = path.Clean(request.Path)
	profileID := r.PathValue("profileID")
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	if !capabilities.Browse {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	var locator storage.ManagerLocator
	var breadcrumbs []storage.ManagerBreadcrumb
	var fallback bool
	var resolveErr error
	if resolver, supported := resolved.provider.(storage.ManagerPathResolver); supported {
		locator, breadcrumbs, fallback, resolveErr = resolver.ResolveManagerPath(resolved.ctx, request.Path)
	} else {
		locator, breadcrumbs, fallback, resolveErr = resolveLegacyManagerPath(resolved.ctx, resolved.provider, request.Path)
	}
	if resolveErr != nil {
		s.writeFileProviderError(w, resolveErr)
		return
	}
	responseBreadcrumbs := make([]fileBreadcrumb, 0, len(breadcrumbs)+1)
	responseBreadcrumbs = append(responseBreadcrumbs, fileBreadcrumb{Name: resolved.profile.Name})
	for _, breadcrumb := range breadcrumbs {
		ref, sealErr := sealFileReference(fileReference{UserID: userID, ProfileID: profileID, ResourceType: "files", Kind: "directory", Locator: breadcrumb.Locator}, s.encryptionKey)
		if sealErr != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		responseBreadcrumbs = append(responseBreadcrumbs, fileBreadcrumb{Ref: ref, Name: breadcrumb.Name})
	}
	ref := ""
	if locator.NativeID != "" || locator.Path != managedRootPath() {
		var sealErr error
		ref, sealErr = sealFileReference(fileReference{UserID: userID, ProfileID: profileID, ResourceType: "files", Kind: "directory", Locator: locator}, s.encryptionKey)
		if sealErr != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"ref":         ref,
		"breadcrumbs": responseBreadcrumbs,
		"fallback":    fallback,
	})
}

func (s *APIServer) handleFileDownloadTicket(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileDownloadTicketRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	reference, referenceErr := openFileReference(request.Ref, s.encryptionKey, userID, profileID)
	if referenceErr != nil || reference.Kind != "file" {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	if !storage.ManagerCapabilitiesFor(profile.Provider).Download {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	ticket := generateRandomString(32)
	streamID := generateRandomString(16)
	if ticket == "" || streamID == "" || !s.acquireFileStream(r.Context(), userID, "download", streamID, fileTicketTTL) {
		writeError(w, http.StatusTooManyRequests, ErrFilesStreamLimitReached)
		return
	}
	payload, marshalErr := json.Marshal(fileDownloadTicket{UserID: userID, ProfileID: profileID, Ref: request.Ref, StreamID: streamID})
	if marshalErr != nil || s.queue.RedisClient().Set(r.Context(), fileTicketRedisKey(ticket), payload, fileTicketTTL).Err() != nil {
		s.releaseFileStream(r.Context(), userID, "download", streamID)
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"success":      true,
		"download_url": "/api/files/download/" + ticket,
	})
}

func (s *APIServer) handleFileThumbnail(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-thumbnail", fileThumbnailRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileThumbnailRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	reference, referenceErr := openFileReference(request.Ref, s.encryptionKey, userID, profileID)
	if referenceErr != nil || reference.Kind != "file" {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	if !capabilities.Thumbnails {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()

	thumbnailer, supported := resolved.provider.(storage.ManagerThumbnailer)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}

	width := request.Width
	height := request.Height
	if width <= 0 {
		width = 256
	}
	if height <= 0 {
		height = 256
	}
	if width > 1024 {
		width = 1024
	}
	if height > 1024 {
		height = 1024
	}

	stream, contentType, thumbnailErr := thumbnailer.ThumbnailManager(resolved.ctx, reference.Locator, width, height)
	if thumbnailErr != nil {
		if errors.Is(thumbnailErr, storage.ErrUnsupportedMedia) {
			writeError(w, http.StatusUnsupportedMediaType, ErrFilesUnsupportedOperation)
			return
		}
		s.writeFileProviderError(w, thumbnailErr)
		return
	}
	if stream == nil {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	defer stream.Close()

	if contentType == "" {
		contentType = "image/jpeg"
	}

	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, stream)
}

// handleFileUpload accepts only a raw file body. Parent locators remain sealed
// references and the filename is encoded in a header so no private location or
// credential-adjacent value is ever placed in a request URL.
func (s *APIServer) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-upload", fileUploadRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	if r.ContentLength < 0 {
		writeError(w, http.StatusLengthRequired, ErrFilesUploadLengthRequired)
		return
	}

	profileID := r.PathValue("profileID")
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	if !capabilities.Upload {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	name, nameErr := decodeUploadFileName(r.Header.Get("X-Clumoove-File-Name"))
	if nameErr != nil || !validManagerUploadName(name) {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	options, optionsErr := parseManagerUploadOptions(r.Header.Get("X-Clumoove-Conflict-Strategy"), capabilities)
	if optionsErr != nil {
		writeValidationError(w, ErrInvalidBody)
		return
	}

	parent := storage.ManagerLocator{Path: managedRootPath()}
	if parentRef := r.Header.Get("X-Clumoove-Parent-Ref"); parentRef != "" {
		reference, referenceErr := openFileReference(parentRef, s.encryptionKey, userID, profileID)
		if referenceErr != nil || reference.Kind != "directory" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		parent = reference.Locator
	}

	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()

	streamID := generateRandomString(16)
	if streamID == "" || !s.acquireFileStream(r.Context(), userID, "upload", streamID, fileStreamLease) {
		writeError(w, http.StatusTooManyRequests, ErrFilesStreamLimitReached)
		return
	}
	defer s.releaseFileStream(r.Context(), userID, "upload", streamID)
	stopLeaseRenewal := make(chan struct{})
	defer close(stopLeaseRenewal)
	go s.renewFileStreamLease(stopLeaseRenewal, userID, "upload", streamID)

	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	stream := storage.NewExactSizeReader(r.Body, r.ContentLength)
	var result storage.ManagerUploadResult
	var uploadErr error
	if uploader, supported := resolved.provider.(storage.ManagerUploader); supported {
		result, uploadErr = uploader.UploadManager(resolved.ctx, parent, name, stream, r.ContentLength, options)
	} else {
		result, uploadErr = uploadLegacyManager(resolved.ctx, resolved.provider, parent, name, stream, r.ContentLength, options)
	}
	if uploadErr == nil {
		uploadErr = stream.Verify()
	}
	if uploadErr != nil {
		switch {
		case errors.Is(uploadErr, storage.ErrUploadSizeMismatch):
			writeValidationError(w, ErrFilesUploadSizeMismatch)
		case errors.Is(uploadErr, storage.ErrManagerConflict), errors.Is(uploadErr, storage.ErrAmbiguousPath):
			writeConflictError(w, ErrFilesConflict)
		default:
			s.writeFileProviderError(w, uploadErr)
		}
		return
	}
	if result.Status != "uploaded" && result.Status != "skipped" && result.Status != "renamed" || result.FinalName == "" {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	s.writeAudit(r, db.AuditFileUploadCompleted, profileID, userID, map[string]interface{}{
		"provider":  profile.Provider,
		"item_kind": "file",
		"result":    result.Status,
	})
	status := http.StatusCreated
	if result.Status == "skipped" {
		status = http.StatusOK
	}
	writeJSON(w, status, fileUploadResponse{Success: true, Status: result.Status, Name: result.FinalName})
}

func (s *APIServer) handleFileDirectoryCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}

	var request fileDirectoryCreateRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	name := strings.TrimSpace(request.Name)
	if !validManagerUploadName(name) {
		writeValidationError(w, ErrInvalidBody)
		return
	}

	profileID := r.PathValue("profileID")
	profile, err := s.loadOwnedFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	capabilities := storage.ManagerCapabilitiesFor(profile.Provider)
	if !capabilities.Mkdir {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}

	parent := storage.ManagerLocator{Path: managedRootPath()}
	if request.ParentRef != "" {
		reference, referenceErr := openFileReference(request.ParentRef, s.encryptionKey, userID, profileID)
		if referenceErr != nil || reference.Kind != "directory" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		parent = reference.Locator
	}

	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()

	var createErr error
	if creator, supported := resolved.provider.(storage.ManagerDirectoryCreator); supported {
		createErr = creator.CreateManagerDirectory(resolved.ctx, parent, name)
	} else {
		createErr = createDirectoryLegacyManager(resolved.ctx, resolved.provider, parent, name)
	}

	if createErr != nil {
		switch {
		case errors.Is(createErr, storage.ErrManagerConflict), errors.Is(createErr, storage.ErrAmbiguousPath):
			writeConflictError(w, ErrFilesConflict)
		default:
			s.writeFileProviderError(w, createErr)
		}
		return
	}

	s.writeAudit(r, db.AuditFileDirectoryCreated, profileID, userID, map[string]interface{}{
		"provider":  profile.Provider,
		"item_kind": "directory",
	})

	writeJSON(w, http.StatusCreated, fileDirectoryCreateResponse{Success: true, Name: name})
}

func decodeUploadFileName(value string) (string, error) {
	if value == "" {
		return "", errors.New("missing upload filename")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil || !utf8.Valid(decoded) {
		return "", errors.New("invalid upload filename")
	}
	return string(decoded), nil
}

func validManagerUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return false
	}
	if len(name) > 255 {
		return false
	}
	for _, value := range name {
		if value < 0x20 || value == 0x7f {
			return false
		}
	}
	return utf8.ValidString(name)
}

func parseManagerUploadOptions(value string, capabilities storage.ManagerCapabilities) (storage.ManagerUploadOptions, error) {
	strategy := strings.ToUpper(strings.TrimSpace(value))
	if strategy == "" {
		strategy = "SKIP"
	}
	supported := (strategy == "SKIP" && capabilities.ConflictSkip) ||
		(strategy == "OVERWRITE" && capabilities.ConflictOverwrite) ||
		(strategy == "RENAME" && capabilities.ConflictRename)
	if !supported {
		return storage.ManagerUploadOptions{}, errors.New("unsupported upload conflict strategy")
	}
	return storage.ManagerUploadOptions{ConflictStrategy: strategy}, nil
}

func (s *APIServer) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	if !s.rateLimiter.Allow(r.Context(), "files-ticket-consume", s.clientIP(r), fileMutationRateLimit, fileRateWindow) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	ticket := r.PathValue("ticket")
	if ticket == "" {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	payload, err := s.queue.RedisClient().GetDel(r.Context(), fileTicketRedisKey(ticket)).Bytes()
	if err != nil {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	var downloadTicket fileDownloadTicket
	if json.Unmarshal(payload, &downloadTicket) != nil || downloadTicket.UserID == "" || downloadTicket.ProfileID == "" || downloadTicket.StreamID == "" {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	// Validate the reference before acquiring the stream slot so that a
	// malformed-reference ticket cannot exhaust the per-user slot budget.
	reference, referenceErr := openFileReference(downloadTicket.Ref, s.encryptionKey, downloadTicket.UserID, downloadTicket.ProfileID)
	if referenceErr != nil || reference.Kind != "file" {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	defer s.releaseFileStream(r.Context(), downloadTicket.UserID, "download", downloadTicket.StreamID)
	if !s.renewFileStream(r.Context(), downloadTicket.UserID, "download", downloadTicket.StreamID, fileStreamLease) {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	stopLeaseRenewal := make(chan struct{})
	defer close(stopLeaseRenewal)
	go s.renewFileStreamLease(stopLeaseRenewal, downloadTicket.UserID, "download", downloadTicket.StreamID)
	resolved, resolveErr := s.resolveFileProfile(r.Context(), downloadTicket.UserID, downloadTicket.ProfileID)
	if resolveErr != nil {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	defer resolved.close()
	var download storage.ManagerDownload
	var downloadErr error
	if downloader, supported := resolved.provider.(storage.ManagerDownloader); supported {
		download, downloadErr = downloader.DownloadManager(resolved.ctx, reference.Locator)
	} else {
		download, downloadErr = downloadLegacyManager(resolved.ctx, resolved.provider, reference.Locator)
	}
	if downloadErr != nil {
		s.writeFileProviderError(w, downloadErr)
		return
	}
	if download.Stream == nil || download.Item.IsDir {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	defer download.Stream.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	filename := download.Item.Name
	if filename == "" {
		filename = "download"
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	contentType := mime.TypeByExtension(strings.ToLower(path.Ext(filename)))
	if contentType == "" || strings.HasPrefix(strings.ToLower(contentType), "text/html") {
		contentType = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("X-Accel-Buffering", "no")
	if download.Item.Size > 0 {
		h.Set("Content-Length", strconv.FormatInt(download.Item.Size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, download.Stream)
}

func fileTicketRedisKey(ticket string) string       { return "files:download-ticket:" + hashToken(ticket) }
func fileStreamRedisKey(userID, kind string) string { return "files:stream:" + kind + ":" + userID }

func (s *APIServer) acquireFileStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool {
	now := time.Now()
	result, err := acquireFileStreamScript.Run(ctx, s.queue.RedisClient(), []string{fileStreamRedisKey(userID, kind)}, now.UnixMilli(), fileMaxStreamsPerUser, now.Add(ttl).UnixMilli(), streamID, ttl.Milliseconds()).Int()
	return err == nil && result == 1
}

func (s *APIServer) releaseFileStream(ctx context.Context, userID, kind, streamID string) {
	_ = s.queue.RedisClient().ZRem(ctx, fileStreamRedisKey(userID, kind), streamID).Err()
}

func (s *APIServer) renewFileStream(ctx context.Context, userID, kind, streamID string, ttl time.Duration) bool {
	now := time.Now()
	result, err := renewFileStreamScript.Run(ctx, s.queue.RedisClient(), []string{fileStreamRedisKey(userID, kind)}, now.UnixMilli(), now.Add(ttl).UnixMilli(), ttl.Milliseconds(), streamID).Int()
	return err == nil && result == 1
}

func (s *APIServer) renewFileStreamLease(stop <-chan struct{}, userID, kind, streamID string) {
	ticker := time.NewTicker(fileStreamLease / 2)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Renewal extends both the member score and the ZSET key TTL. If the
			// lease has already disappeared, the script refuses to recreate it.
			ctx := s.backgroundCtx
			if ctx == nil {
				ctx = context.Background()
			}
			_ = s.renewFileStream(ctx, userID, kind, streamID, fileStreamLease)
		}
	}
}

func (s *APIServer) writeFileProfileError(w http.ResponseWriter, err error) {
	if errors.Is(err, errFileProfileNotFound) {
		writeError(w, http.StatusNotFound, ErrProfileNotFound)
		return
	}
	writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
}

func (s *APIServer) writeFileProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errManagerDirectoryChanged):
		writeConflictError(w, ErrFilesDirectoryChanged)
	case errors.Is(err, errManagerDirectoryTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, ErrFilesDirectoryTooLarge)
	case errors.Is(err, storage.ErrManagerUnsupported):
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
	case errors.Is(err, storage.ErrManagerDirectoryNotEmpty):
		writeConflictError(w, ErrFilesDirectoryNotEmpty)
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrFilesNotFound)
	case errors.Is(err, storage.ErrAmbiguousPath):
		writeConflictError(w, ErrFilesPathAmbiguous)
	default:
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
	}
}
