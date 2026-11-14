package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"backend/internal/storage"
)

const (
	fileArchiveMaximumFiles = 10000
	fileArchiveMaximumDepth = 20
	fileArchiveMaximumBytes = int64(20 * 1024 * 1024 * 1024)
	fileArchiveMaximumName  = 256
)

type fileArchiveTicketRequest struct {
	Refs []string `json:"refs"`
}
type fileArchiveTicket struct {
	UserID    string   `json:"user_id"`
	ProfileID string   `json:"profile_id"`
	Refs      []string `json:"refs"`
	StreamID  string   `json:"stream_id"`
}
type fileArchiveMember struct {
	locator storage.ManagerLocator
	name    string
	size    int64
}

func archiveRootName(reference fileReference) string {
	if reference.Locator.Path != "" {
		return path.Base(canonicalManagedPath(reference.Locator.Path))
	}
	return "item"
}

func validArchiveName(name string) bool {
	return validManagerUploadName(name) && len(name) <= fileArchiveMaximumName
}

func listArchiveDirectory(ctx *resolvedFileProfile, locator storage.ManagerLocator) (storage.ManagerPage, error) {
	items := make([]storage.ManagerItem, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := ctx.manager.ListManager(ctx.ctx, locator, storage.ManagerListOptions{Cursor: cursor, Limit: fileMaximumPageSize})
		if err != nil {
			return storage.ManagerPage{}, err
		}
		if len(page.Items) == 0 && page.NextCursor != "" {
			return storage.ManagerPage{}, storage.ErrManagerDirectoryTooLarge
		}
		if len(items) > fileArchiveMaximumFiles-len(page.Items) {
			return storage.ManagerPage{}, storage.ErrManagerDirectoryTooLarge
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			return storage.ManagerPage{Items: items}, nil
		}
		if _, seen := seenCursors[page.NextCursor]; seen {
			return storage.ManagerPage{}, storage.ErrManagerDirectoryTooLarge
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
}

// planFileArchive walks manager locators breadth-first. All limits are checked
// before a ticket is created so a ticket cannot authorize an unbounded stream.
func planFileArchive(resolved *resolvedFileProfile, items []batchReference) ([]fileArchiveMember, error) {
	members := make([]fileArchiveMember, 0)
	visited := make(map[string]struct{})
	usedNames := make(map[string]struct{})
	var totalSize int64
	addFile := func(locator storage.ManagerLocator, name string, size int64) error {
		if !validArchiveName(path.Base(name)) || len(name) > fileArchiveMaximumName || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
			return errors.New("invalid archive name")
		}
		base, candidate := name, name
		for suffix := 2; ; suffix++ {
			if _, exists := usedNames[candidate]; !exists {
				break
			}
			candidate = strings.TrimSuffix(base, path.Ext(base)) + " (" + strconv.Itoa(suffix) + ")" + path.Ext(base)
		}
		if len(candidate) > fileArchiveMaximumName || size < 0 || totalSize > fileArchiveMaximumBytes-size || len(members) >= fileArchiveMaximumFiles {
			return storage.ErrManagerDirectoryTooLarge
		}
		usedNames[candidate] = struct{}{}
		totalSize += size
		members = append(members, fileArchiveMember{locator: locator, name: candidate, size: size})
		return nil
	}
	type directory struct {
		locator storage.ManagerLocator
		prefix  string
		depth   int
	}
	queue := make([]directory, 0)
	for _, item := range items {
		rootName := archiveRootName(item.reference)
		if !validArchiveName(rootName) {
			return nil, errors.New("invalid archive root")
		}
		if item.reference.Kind == "file" {
			archiveItem, err := inspectArchiveFile(resolved, item.reference.Locator)
			if err != nil {
				return nil, err
			}
			if archiveItem.Name != "" {
				rootName = archiveItem.Name
			}
			if err := addFile(item.reference.Locator, rootName, archiveItem.Size); err != nil {
				return nil, err
			}
		} else {
			queue = append(queue, directory{locator: item.reference.Locator, prefix: rootName, depth: 1})
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth > fileArchiveMaximumDepth {
			return nil, storage.ErrManagerDirectoryTooLarge
		}
		key := storage.ManagerLocatorKey(current.locator)
		if _, exists := visited[key]; exists {
			return nil, storage.ErrManagerDirectoryCycle
		}
		visited[key] = struct{}{}
		page, err := listArchiveDirectory(resolved, current.locator)
		if err != nil || page.NextCursor != "" {
			if err == nil {
				err = storage.ErrManagerDirectoryTooLarge
			}
			return nil, err
		}
		storage.SortManagerItems(page.Items)
		for _, child := range page.Items {
			if !validArchiveName(child.Name) {
				return nil, errors.New("invalid archive item name")
			}
			childName := current.prefix + "/" + child.Name
			if len(childName) > fileArchiveMaximumName {
				return nil, storage.ErrManagerDirectoryTooLarge
			}
			if child.IsDir {
				queue = append(queue, directory{locator: child.Locator, prefix: childName, depth: current.depth + 1})
				continue
			}
			if err := addFile(child.Locator, childName, child.Size); err != nil {
				return nil, err
			}
		}
	}
	return members, nil
}

func inspectArchiveFile(resolved *resolvedFileProfile, locator storage.ManagerLocator) (storage.ManagerItem, error) {
	if locator.NativeID == "" {
		item, err := resolved.manager.InspectManagerItem(resolved.ctx, locator)
		if err != nil {
			return storage.ManagerItem{}, err
		}
		if item.IsDir {
			return storage.ManagerItem{}, storage.ErrNotFound
		}
		return item, nil
	}
	download, err := downloadArchiveMember(resolved, locator)
	if err != nil {
		return storage.ManagerItem{}, err
	}
	if download.Stream != nil {
		_ = download.Stream.Close()
	}
	if download.Item.IsDir {
		return storage.ManagerItem{}, storage.ErrNotFound
	}
	return download.Item, nil
}

func (api *fileAPI) handleFileArchiveTicket(w http.ResponseWriter, r *http.Request) {
	userID, ok := api.requireUserID(w, r)
	if !ok {
		return
	}
	if !api.allowFileRequest(r, userID, "files-archive", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileArchiveTicketRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	items, err := api.openBatchReferences(request.Refs, userID, profileID)
	if err != nil {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	resolved, err := api.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		api.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	if !resolved.manager.Capabilities().Archive {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	for _, item := range items {
		if resolved.manager.IsRootLocator(item.reference.Locator) {
			writeValidationError(w, ErrFilesRootMutationForbidden)
			return
		}
	}
	if _, err := planFileArchive(resolved, items); err != nil {
		if errors.Is(err, storage.ErrManagerDirectoryTooLarge) || errors.Is(err, storage.ErrManagerDirectoryCycle) {
			writeError(w, http.StatusRequestEntityTooLarge, ErrFilesArchiveLimitExceeded)
		} else {
			api.writeFileProviderError(w, err)
		}
		return
	}
	ticket, streamID := generateRandomString(32), generateRandomString(16)
	if ticket == "" || streamID == "" || !api.acquireFileStream(r.Context(), userID, "archive", streamID, fileTicketTTL) {
		writeError(w, http.StatusTooManyRequests, ErrFilesStreamLimitReached)
		return
	}
	payload, err := json.Marshal(fileArchiveTicket{UserID: userID, ProfileID: profileID, Refs: request.Refs, StreamID: streamID})
	if err != nil || api.setFileTicket(r.Context(), fileArchiveTicketRedisKey(ticket), payload, fileTicketTTL) != nil {
		api.releaseFileStream(r.Context(), userID, "archive", streamID)
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"download_url": "/api/files/archive/" + ticket})
}

func (api *fileAPI) handleFileArchive(w http.ResponseWriter, r *http.Request) {
	if !api.allowRateLimit(r, "files-archive-ticket-consume", api.clientIP(r), fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	payload, err := api.consumeFileTicket(r.Context(), fileArchiveTicketRedisKey(r.PathValue("ticket")))
	if err != nil {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	var ticket fileArchiveTicket
	if json.Unmarshal(payload, &ticket) != nil || ticket.UserID == "" || ticket.ProfileID == "" || ticket.StreamID == "" {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	items, err := api.openBatchReferences(ticket.Refs, ticket.UserID, ticket.ProfileID)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrFilesDownloadTicketInvalid)
		return
	}
	defer api.releaseFileStream(r.Context(), ticket.UserID, "archive", ticket.StreamID)
	if !api.renewFileStream(r.Context(), ticket.UserID, "archive", ticket.StreamID, fileStreamLease) {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	resolved, err := api.resolveFileProfile(r.Context(), ticket.UserID, ticket.ProfileID)
	if err != nil {
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	defer resolved.close()
	members, err := planFileArchive(resolved, items)
	if err != nil {
		if errors.Is(err, storage.ErrManagerDirectoryTooLarge) || errors.Is(err, storage.ErrManagerDirectoryCycle) {
			writeError(w, http.StatusRequestEntityTooLarge, ErrFilesArchiveLimitExceeded)
			return
		}
		writeError(w, http.StatusBadGateway, ErrFilesProviderUnavailable)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "clumoove-archive.zip"}))
	h.Set("Content-Type", "application/zip")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = writeFileArchive(w, resolved, members)
}

// writeFileArchive streams each member directly into ZIP entries. Individual
// provider failures are represented in the manifest after streaming begins.
func writeFileArchive(output io.Writer, resolved *resolvedFileProfile, members []fileArchiveMember) error {
	archive := zip.NewWriter(output)
	failures := make([]map[string]string, 0)
	for _, member := range members {
		download, downloadErr := downloadArchiveMember(resolved, member.locator)
		if downloadErr != nil || download.Stream == nil || download.Item.IsDir {
			failures = append(failures, map[string]string{"error_code": string(ErrFilesProviderUnavailable)})
			continue
		}
		entry, createErr := archive.Create(member.name)
		if createErr == nil {
			_, createErr = io.Copy(entry, download.Stream)
		}
		_ = download.Stream.Close()
		if createErr != nil {
			failures = append(failures, map[string]string{"error_code": string(ErrFilesProviderUnavailable)})
		}
	}
	if len(failures) > 0 {
		if manifest, marshalErr := json.Marshal(map[string]any{"failures": failures}); marshalErr == nil {
			if entry, createErr := archive.Create("_clumoove-failures.json"); createErr == nil {
				_, _ = entry.Write(manifest)
			}
		}
	}
	return archive.Close()
}

func downloadArchiveMember(resolved *resolvedFileProfile, locator storage.ManagerLocator) (storage.ManagerDownload, error) {
	return resolved.manager.DownloadManager(resolved.ctx, locator)
}

func fileArchiveTicketRedisKey(ticket string) string {
	return "files:archive-ticket:" + hashToken(ticket)
}
