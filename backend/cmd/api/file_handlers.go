package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/storage"
	"github.com/redis/go-redis/v9"
)

const (
	fileBrowseRateLimit       = 60
	fileMutationRateLimit     = 30
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
	return userID + ":" + s.clientIP(r)
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

func validManagedLocator(locator storage.ManagerLocator) bool {
	return locator.NativeID != "" && !strings.ContainsRune(locator.NativeID, 0)
}

func validManagedCursorParent(locator storage.ManagerLocator) bool {
	return (locator.NativeID == "" && locator.Path == managedRootPath()) || validManagedLocator(locator)
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
		return resources[i].Locator.NativeID < resources[j].Locator.NativeID
	})
}

func allowedFileActions(capabilities storage.ManagerCapabilities, isDir bool) []string {
	actions := make([]string, 0, 2)
	if !isDir && capabilities.Download {
		actions = append(actions, "download")
	}
	return actions
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

	lister, supported := resolved.provider.(storage.ManagerLister)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	page, listErr := lister.ListManager(resolved.ctx, parent, storage.ManagerListOptions{Cursor: providerCursor, Limit: request.Limit})
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
		"pagination_mode": "native",
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
	resolver, supported := resolved.provider.(storage.ManagerPathResolver)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	locator, breadcrumbs, fallback, resolveErr := resolver.ResolveManagerPath(resolved.ctx, request.Path)
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
	if locator.NativeID != "" {
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
	defer s.releaseFileStream(r.Context(), downloadTicket.UserID, "download", downloadTicket.StreamID)
	if !s.renewFileStream(r.Context(), downloadTicket.UserID, "download", downloadTicket.StreamID, fileStreamLease) {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	stopLeaseRenewal := make(chan struct{})
	defer close(stopLeaseRenewal)
	go s.renewFileStreamLease(stopLeaseRenewal, downloadTicket.UserID, "download", downloadTicket.StreamID)
	reference, referenceErr := openFileReference(downloadTicket.Ref, s.encryptionKey, downloadTicket.UserID, downloadTicket.ProfileID)
	if referenceErr != nil || reference.Kind != "file" {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	resolved, resolveErr := s.resolveFileProfile(r.Context(), downloadTicket.UserID, downloadTicket.ProfileID)
	if resolveErr != nil {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	defer resolved.close()
	downloader, supported := resolved.provider.(storage.ManagerDownloader)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	download, downloadErr := downloader.DownloadManager(resolved.ctx, reference.Locator)
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
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrFilesNotFound)
	case errors.Is(err, storage.ErrAmbiguousPath):
		writeConflictError(w, ErrFilesPathAmbiguous)
	default:
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
	}
}
