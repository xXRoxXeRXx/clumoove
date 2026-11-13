package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
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
	fileDefaultPageSize    = 500
	fileMaximumPageSize    = 1000
	fileTicketTTL          = time.Minute
	fileStreamLease           = 2 * time.Minute
	fileMaxStreamsPerUser     = 4
)

type fileReference struct {
	UserID       string                 `json:"user_id"`
	ProfileID    string                 `json:"profile_id"`
	ResourceType string                 `json:"resource_type"`
	Kind         string                 `json:"kind"`
	Name         string                 `json:"name,omitempty"`
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
	return storage.CanonicalManagerPath(value)
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
		actions = append(actions, "move")
	}
	if capabilities.Copy {
		actions = append(actions, "copy")
	}
	return actions
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
	if resolved.manager.IsRootLocator(reference.Locator) {
		writeValidationError(w, ErrFilesRootMutationForbidden)
		return
	}
	capabilities := resolved.manager.Capabilities()
	supported := (reference.Kind == "file" && capabilities.DeleteFile) ||
		(reference.Kind == "directory" && !request.Recursive && capabilities.DeleteEmptyDirectory) ||
		(reference.Kind == "directory" && request.Recursive && capabilities.DeleteRecursiveDirectory)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	if err := resolved.manager.DeleteManagerItem(resolved.ctx, reference.Locator, request.Recursive); err != nil {
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
	name := strings.TrimSpace(request.NewName)
	if name == "" {
		name = path.Base(source.Locator.Path)
		if source.Locator.NativeID != "" && source.Locator.Path == "" {
			name = source.Name
		}
	}
	if !validManagerUploadName(name) {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	destination := storage.ManagerLocator{Path: managedRootPath()}
	if request.DestinationParentRef != "" {
		destinationRef, destinationErr := openFileReference(request.DestinationParentRef, s.encryptionKey, userID, profileID)
		if destinationErr != nil || destinationRef.Kind != "directory" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		destination = destinationRef.Locator
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	if resolved.manager.IsRootLocator(source.Locator) {
		writeValidationError(w, ErrFilesRootMutationForbidden)
		return
	}
	if operation == "rename" {
		if resolved.profile.Provider == "google" {
			// Google rename requests without an explicit parent retain the source's
			// immutable parent instead of deriving it from a display path.
			if request.DestinationParentRef == "" {
				destination = storage.ManagerLocator{}
			}
		} else {
			// Renames keep the item in its existing folder. For path-based providers,
			// we derive destination from the source's parent directory, intentionally
			// overriding any client-supplied DestinationParentRef.
			destination = storage.ManagerLocator{Path: path.Dir(canonicalManagedPath(source.Locator.Path))}
		}
	}
	capabilities := resolved.manager.Capabilities()
	if (operation == "rename" && !capabilities.Rename) || (operation == "move" && !capabilities.Move) || (operation == "copy" && !capabilities.Copy) {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	strategy := strings.ToUpper(strings.TrimSpace(request.ConflictStrategy))
	if strategy != "" && strategy != "SKIP" && strategy != "OVERWRITE" && strategy != "RENAME" {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	options := storage.ManagerMutationOptions{ConflictStrategy: storage.ManagerConflictStrategy(strategy)}
	var result storage.ManagerMutationResult
	if operation == "copy" && !capabilities.NativeCopy {
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
		result, err = resolved.manager.RenameManagerItem(resolved.ctx, source.Locator, destination, name, options)
	case "copy":
		result, err = resolved.manager.CopyManagerItem(resolved.ctx, source.Locator, destination, name, options)
	case "move":
		result, err = resolved.manager.MoveManagerItem(resolved.ctx, source.Locator, destination, name, options)
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

	page, listErr := resolved.manager.ListManager(resolved.ctx, parent, storage.ManagerListOptions{Cursor: providerCursor, Limit: request.Limit})
	if listErr != nil {
		s.writeFileProviderError(w, listErr)
		return
	}
	if len(page.Items) > request.Limit {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	paginationMode := page.PaginationMode
	entries := make([]fileEntry, 0, len(page.Items))
	for _, resource := range page.Items {
		kind := "file"
		if resource.IsDir {
			kind = "directory"
		}
		ref, sealErr := sealFileReference(fileReference{UserID: userID, ProfileID: profileID, ResourceType: "files", Kind: kind, Name: resource.Name, Locator: resource.Locator}, s.encryptionKey)
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
	locator, breadcrumbs, fallback, resolveErr := resolved.manager.ResolveManagerPath(resolved.ctx, request.Path)
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

	width, height := computeThumbnailDimensions(request.Width, request.Height)

	stream, contentType, thumbnailErr := resolved.manager.ThumbnailManager(resolved.ctx, reference.Locator, width, height)
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

func computeThumbnailDimensions(requestedWidth, requestedHeight int) (int, int) {
	width := requestedWidth
	height := requestedHeight
	if width <= 0 && height <= 0 {
		width = 256
		height = 256
	} else if width <= 0 {
		width = (height * 16) / 9
		if width <= 0 {
			width = height
		}
	} else if height <= 0 {
		height = (width * 9) / 16
		if height <= 0 {
			height = width
		}
	}
	if width > 2048 {
		width = 2048
	}
	if height > 2048 {
		height = 2048
	}
	return width, height
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
	result, uploadErr := resolved.manager.UploadManager(resolved.ctx, parent, name, stream, r.ContentLength, options)
	if uploadErr == nil && result.Status != "skipped" {
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

	createErr := resolved.manager.CreateManagerDirectory(resolved.ctx, parent, name)

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
	return storage.ValidManagerEntryName(name)
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
	download, downloadErr := resolved.manager.DownloadManager(resolved.ctx, reference.Locator)
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
	if errors.Is(err, storage.ErrManagerUnsupported) {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
}

func (s *APIServer) writeFileProviderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrManagerDirectoryChanged):
		writeConflictError(w, ErrFilesDirectoryChanged)
	case errors.Is(err, storage.ErrManagerDirectoryTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, ErrFilesDirectoryTooLarge)
	case errors.Is(err, storage.ErrManagerUnsupported):
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
	case errors.Is(err, storage.ErrManagerDirectoryNotEmpty):
		writeConflictError(w, ErrFilesDirectoryNotEmpty)
	case errors.Is(err, storage.ErrManagerInvalidDestination):
		writeValidationError(w, ErrFilesInvalidDestination)
	case errors.Is(err, storage.ErrPathEscapesRoot):
		writeValidationError(w, ErrFilesInvalidRef)
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrFilesNotFound)
	case errors.Is(err, storage.ErrAmbiguousPath):
		writeConflictError(w, ErrFilesPathAmbiguous)
	default:
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
	}
}
