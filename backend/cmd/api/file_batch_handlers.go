package main

import (
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"

	"backend/internal/db"
	"backend/internal/storage"
)

const fileBatchMaximumItems = 200

type fileBatchDeleteItem struct {
	Ref       string `json:"ref"`
	Recursive bool   `json:"recursive"`
}

type fileBatchDeleteRequest struct {
	Items []fileBatchDeleteItem `json:"items"`
}

type fileBatchMutationRequest struct {
	Refs                 []string `json:"refs"`
	DestinationParentRef string   `json:"destination_parent_ref,omitempty"`
	ConflictStrategy     string   `json:"conflict_strategy,omitempty"`
}

type fileBatchItemResult struct {
	Ref       string       `json:"ref"`
	Status    string       `json:"status"`
	Native    bool         `json:"native"`
	ErrorCode APIErrorCode `json:"error_code,omitempty"`
}

type fileBatchResponse struct {
	Results            []fileBatchItemResult             `json:"results"`
	ConflictStrategies []storage.ManagerConflictStrategy `json:"conflict_strategies,omitempty"`
}

type batchReference struct {
	ref       string
	reference fileReference
	recursive bool
}

func (s *APIServer) openBatchReferences(refs []string, userID, profileID string) ([]batchReference, error) {
	if len(refs) == 0 || len(refs) > fileBatchMaximumItems {
		return nil, errors.New("invalid batch size")
	}
	seen := make(map[string]struct{}, len(refs))
	seenLocators := make(map[string]struct{}, len(refs))
	items := make([]batchReference, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			return nil, errors.New("empty reference")
		}
		if _, ok := seen[ref]; ok {
			return nil, errors.New("duplicate reference")
		}
		seen[ref] = struct{}{}
		reference, err := openFileReference(ref, s.encryptionKey, userID, profileID)
		if err != nil {
			return nil, err
		}
		identity := managedLocatorIdentity(reference.Locator)
		if _, ok := seenLocators[identity]; ok {
			return nil, errors.New("duplicate locator")
		}
		seenLocators[identity] = struct{}{}
		items = append(items, batchReference{ref: ref, reference: reference})
	}
	return normalizeBatchReferences(items), nil
}

// normalizeBatchReferences drops descendants of selected path-backed directories.
// Native locators cannot be compared safely without provider-specific hierarchy APIs.
func normalizeBatchReferences(items []batchReference) []batchReference {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].reference.Locator, items[j].reference.Locator
		if left.NativeID != "" || right.NativeID != "" {
			return left.NativeID < right.NativeID
		}
		return len(left.Path) < len(right.Path)
	})
	result := make([]batchReference, 0, len(items))
	for _, item := range items {
		descendant := false
		for _, parent := range result {
			if parent.reference.Kind != "directory" || parent.reference.Locator.NativeID != "" || item.reference.Locator.NativeID != "" {
				continue
			}
			parentPath := strings.TrimSuffix(canonicalManagedPath(parent.reference.Locator.Path), "/")
			if strings.HasPrefix(canonicalManagedPath(item.reference.Locator.Path), parentPath+"/") {
				descendant = true
				break
			}
		}
		if !descendant {
			result = append(result, item)
		}
	}
	return result
}

func fileBatchError(err error) APIErrorCode {
	switch {
	case errors.Is(err, storage.ErrManagerConflict):
		return ErrFilesConflict
	case errors.Is(err, storage.ErrManagerDirectoryCycle), errors.Is(err, storage.ErrManagerInvalidDestination):
		return ErrFilesInvalidDestination
	case errors.Is(err, storage.ErrManagerNoop):
		return ErrFilesNoop
	case errors.Is(err, storage.ErrManagerPartial):
		return ErrFilesPartialOperation
	case errors.Is(err, storage.ErrManagerDirectoryNotEmpty):
		return ErrFilesDirectoryNotEmpty
	case errors.Is(err, storage.ErrNotFound):
		return ErrFilesNotFound
	default:
		return ErrFilesProviderUnavailable
	}
}

func (s *APIServer) handleFileEntriesBatchDelete(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileBatchDeleteRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	refs := make([]string, len(request.Items))
	recursiveByRef := make(map[string]bool, len(request.Items))
	for i := range request.Items {
		refs[i] = request.Items[i].Ref
		recursiveByRef[request.Items[i].Ref] = request.Items[i].Recursive
	}
	profileID := r.PathValue("profileID")
	items, err := s.openBatchReferences(refs, userID, profileID)
	if err != nil {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	for i := range items {
		items[i].recursive = recursiveByRef[items[i].ref]
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	capabilities := storage.ManagerCapabilitiesFor(resolved.profile.Provider)
	deleter, supported := resolved.provider.(storage.ManagerDeleter)
	if !supported {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	for _, item := range items {
		allowed := (item.reference.Kind == "file" && capabilities.DeleteFile) || (item.reference.Kind == "directory" && item.recursive && capabilities.DeleteRecursiveDirectory) || (item.reference.Kind == "directory" && !item.recursive && capabilities.DeleteEmptyDirectory)
		if isManagedRootLocator(resolved.profile.Provider, item.reference.Locator) || !allowed {
			writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
			return
		}
	}
	results := make([]fileBatchItemResult, 0, len(items))
	for i, item := range items {
		if r.Context().Err() != nil {
			for _, remaining := range items[i:] {
				results = append(results, fileBatchItemResult{Ref: remaining.ref, Status: "not_attempted"})
			}
			break
		}
		result := fileBatchItemResult{Ref: item.ref, Status: "deleted"}
		if err := deleter.DeleteManagerItem(resolved.ctx, item.reference.Locator, item.recursive); err != nil {
			result.Status, result.ErrorCode = "failed", fileBatchError(err)
		}
		s.writeAudit(r, db.AuditFileItemDeleted, profileID, userID, map[string]interface{}{"provider": resolved.profile.Provider, "item_kind": item.reference.Kind, "recursive": item.recursive, "outcome": result.Status})
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, fileBatchResponse{Results: results})
}

func (s *APIServer) handleFileEntriesBatchCopy(w http.ResponseWriter, r *http.Request) {
	s.handleFileEntriesBatchMutation(w, r, "copy")
}
func (s *APIServer) handleFileEntriesBatchMove(w http.ResponseWriter, r *http.Request) {
	s.handleFileEntriesBatchMutation(w, r, "move")
}

func (s *APIServer) handleFileEntriesBatchMutation(w http.ResponseWriter, r *http.Request, operation string) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-mutation", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var request fileBatchMutationRequest
	if !decodeJSONBody(w, r, &request, normalJSONBodyLimit) {
		return
	}
	profileID := r.PathValue("profileID")
	items, err := s.openBatchReferences(request.Refs, userID, profileID)
	if err != nil {
		writeValidationError(w, ErrFilesInvalidRef)
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
	strategy := storage.ManagerConflictStrategy(strings.ToUpper(strings.TrimSpace(request.ConflictStrategy)))
	if strategy != "" && strategy != storage.ManagerConflictSkip && strategy != storage.ManagerConflictOverwrite && strategy != storage.ManagerConflictRename {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	resolved, err := s.resolveFileProfile(r.Context(), userID, profileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	defer resolved.close()
	capabilities := storage.ManagerCapabilitiesFor(resolved.profile.Provider)
	if !capabilities.Move {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	for _, item := range items {
		if isManagedRootLocator(resolved.profile.Provider, item.reference.Locator) {
			writeValidationError(w, ErrFilesRootMutationForbidden)
			return
		}
		if item.reference.Kind == "directory" && item.reference.Locator.NativeID == "" && strings.HasPrefix(canonicalManagedPath(destination.Path)+"/", strings.TrimSuffix(canonicalManagedPath(item.reference.Locator.Path), "/")+"/") {
			writeValidationError(w, ErrFilesInvalidDestination)
			return
		}
	}
	streamID := generateRandomString(16)
	if streamID == "" || !s.acquireFileStream(r.Context(), userID, "batch-mutation", streamID, fileStreamLease) {
		writeError(w, http.StatusTooManyRequests, ErrFilesStreamLimitReached)
		return
	}
	defer s.releaseFileStream(r.Context(), userID, "batch-mutation", streamID)
	var mover storage.ManagerMover
	var copier storage.ManagerCopier
	if operation == "copy" {
		copier, _ = storage.NewManagerCopier(resolved.profile.Provider, resolved.provider)
		if copier == nil {
			writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
			return
		}
	} else {
		mover, _ = storage.NewManagerMover(resolved.profile.Provider, resolved.provider)
		if mover == nil {
			writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
			return
		}
	}
	results := make([]fileBatchItemResult, 0, len(items))
	for i, item := range items {
		if r.Context().Err() != nil {
			for _, remaining := range items[i:] {
				results = append(results, fileBatchItemResult{Ref: remaining.ref, Status: "not_attempted"})
			}
			break
		}
		name := path.Base(item.reference.Locator.Path)
		if item.reference.Locator.NativeID != "" && item.reference.Locator.Path == "" {
			results = append(results, fileBatchItemResult{Ref: item.ref, Status: "failed", ErrorCode: ErrInvalidBody})
			continue
		}
		if sameManagedLocator(item.reference.Locator, destination) {
			results = append(results, fileBatchItemResult{Ref: item.ref, Status: "failed", ErrorCode: ErrFilesNoop})
			continue
		}
		var mutationErr error
		var mutationResult storage.ManagerMutationResult
		if operation == "copy" {
			mutationResult, mutationErr = copier.CopyManagerItem(resolved.ctx, item.reference.Locator, destination, name, storage.ManagerMutationOptions{ConflictStrategy: strategy})
		} else {
			mutationResult, mutationErr = mover.MoveManagerItem(resolved.ctx, item.reference.Locator, destination, name, storage.ManagerMutationOptions{ConflictStrategy: strategy})
		}
		status := "copied"
		if operation == "move" {
			status = "moved"
		}
		result := fileBatchItemResult{Ref: item.ref, Status: status, Native: mutationResult.Native}
		if mutationErr != nil {
			result.ErrorCode = fileBatchError(mutationErr)
			if result.ErrorCode == ErrFilesConflict && strategy == "" {
				result.Status = "conflict"
			} else {
				result.Status = "failed"
			}
		} else if mutationResult.Status != "" {
			result.Status = mutationResult.Status
		}
		action := db.AuditFileItemCopied
		if operation == "move" {
			action = db.AuditFileItemMoved
		}
		s.writeAudit(r, action, profileID, userID, map[string]interface{}{"provider": resolved.profile.Provider, "source_item_kind": item.reference.Kind, "destination_item_kind": "directory", "conflict_strategy": strategy, "native": result.Native, "outcome": result.Status})
		results = append(results, result)
	}
	response := fileBatchResponse{Results: results}
	for _, result := range results {
		if result.Status == "conflict" {
			if capabilities.ConflictSkip {
				response.ConflictStrategies = append(response.ConflictStrategies, storage.ManagerConflictSkip)
			}
			if capabilities.ConflictOverwrite {
				response.ConflictStrategies = append(response.ConflictStrategies, storage.ManagerConflictOverwrite)
			}
			if capabilities.ConflictRename {
				response.ConflictStrategies = append(response.ConflictStrategies, storage.ManagerConflictRename)
			}
			break
		}
	}
	writeJSON(w, http.StatusOK, response)
}
