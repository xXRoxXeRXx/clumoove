package main

import (
	"database/sql"
	"net/http"
	"path"
	"strings"

	"backend/internal/db"
	"backend/internal/storage"
)

// fileManagerTransferMarker keeps ad-hoc manager transfers out of the normal
// migration picker/list. The task queue still owns their durable execution.
const fileManagerTransferMarker = "file-manager-transfer:"

type fileTransferRequest struct {
	Refs             []string `json:"refs"`
	TargetProfileID  string   `json:"target_profile_id"`
	TargetParentRef  string   `json:"target_parent_ref,omitempty"`
	Operation        string   `json:"operation"`
	ConflictStrategy string   `json:"conflict_strategy,omitempty"`
}

type fileTransferSummary struct {
	ID                string `json:"id"`
	Operation         string `json:"operation"`
	Status            string `json:"status"`
	SourceProfileName string `json:"source_profile_name"`
	TargetProfileName string `json:"target_profile_name"`
	TotalFiles        int    `json:"total_files"`
	ProcessedFiles    int    `json:"processed_files"`
	FailedFiles       int    `json:"failed_files"`
	SkippedFiles      int    `json:"skipped_files"`
}

func (api *fileAPI) handleFileTransfersList(w http.ResponseWriter, r *http.Request) {
	userID, ok := api.requireUserID(w, r)
	if !ok {
		return
	}
	profileID := r.PathValue("profileID")
	if _, err := api.loadOwnedFileProfile(r.Context(), userID, profileID); err != nil {
		api.writeFileProfileError(w, err)
		return
	}
	rows, err := api.migrations.ListFileManagerTransfers(r.Context(), userID, profileID, fileManagerTransferMarker)
	if err != nil {
		api.logf(r, "file transfer list: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	transfers := make([]fileTransferSummary, 0, len(rows))
	for _, row := range rows {
		transfers = append(transfers, fileTransferSummary{
			ID:                row.ID,
			Operation:         strings.TrimPrefix(row.Marker, fileManagerTransferMarker),
			Status:            row.Status,
			SourceProfileName: row.SourceProfileName,
			TargetProfileName: row.TargetProfileName,
			TotalFiles:        row.TotalFiles,
			ProcessedFiles:    row.ProcessedFiles,
			FailedFiles:       row.FailedFiles,
			SkippedFiles:      row.SkippedFiles,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "transfers": transfers})
}

func (api *fileAPI) handleFileTransferCancel(w http.ResponseWriter, r *http.Request) {
	userID, ok := api.requireUserID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	migration, err := api.migrations.GetMigrationContext(r.Context(), id)
	if err != nil || !migration.UserID.Valid || migration.UserID.String != userID || !strings.HasPrefix(migration.PickerSessionID, fileManagerTransferMarker) {
		writeError(w, http.StatusNotFound, ErrFilesNotFound)
		return
	}
	applied, err := api.migrations.TransitionMigration(r.Context(), id, db.MigrationEventCancel, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if !applied {
		writeError(w, http.StatusConflict, ErrMigrationInvalidState)
		return
	}
	if err := api.migrations.CancelPendingTasks(id); err != nil {
		api.logf(r, "file transfer cancel pending tasks %s: %v", id, err)
	}
	if api.publishCancelEvent != nil {
		if err := api.publishCancelEvent(r.Context(), id); err != nil {
			api.logf(r, "file transfer cancel event %s: %v", id, err)
		}
	}
	api.writeAudit(r, db.AuditMigrationCancelled, id, userID, map[string]interface{}{"origin": "file_manager"})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *fileAPI) handleFileTransferCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := api.requireUserID(w, r)
	if !ok {
		return
	}
	if !api.allowFileRequest(r, userID, "files-transfer", fileMutationRateLimit) {
		writeError(w, http.StatusTooManyRequests, ErrRateLimited)
		return
	}
	var req fileTransferRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	operation := strings.ToUpper(strings.TrimSpace(req.Operation))
	if operation != "COPY" && operation != "MOVE" {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	strategy := strings.ToUpper(strings.TrimSpace(req.ConflictStrategy))
	if strategy == "" {
		strategy = "SKIP"
	}
	if !db.ValidConflictStrategy(strategy) {
		writeValidationError(w, ErrConflictStrategyInvalid)
		return
	}
	sourceProfileID := r.PathValue("profileID")
	if req.TargetProfileID == "" || req.TargetProfileID == sourceProfileID {
		writeValidationError(w, ErrInvalidBody)
		return
	}
	items, err := api.openBatchReferences(req.Refs, userID, sourceProfileID)
	if err != nil {
		writeValidationError(w, ErrFilesInvalidRef)
		return
	}
	for _, item := range items {
		if item.reference.Locator.Path == "" || item.reference.Locator.Path == "/" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
	}
	sourceSnap, err := api.profiles.SnapshotOwned(r.Context(), userID, sourceProfileID)
	if err != nil {
		api.writeFileProfileError(w, mapFileProfileError(err))
		return
	}
	targetSnap, err := api.profiles.SnapshotOwned(r.Context(), userID, req.TargetProfileID)
	if err != nil {
		api.writeFileProfileError(w, mapFileProfileError(err))
		return
	}
	source, sourcePersisted := sourceSnap.Profile, sourceSnap.Creds
	target, targetPersisted := targetSnap.Profile, targetSnap.Creds
	if !storage.ManagerCapabilitiesFor(source.Provider).Browse || !storage.ManagerCapabilitiesFor(target.Provider).Browse {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	targetDir := managedRootPath()
	if req.TargetParentRef != "" {
		reference, refErr := openFileReference(req.TargetParentRef, api.encryptionKey, userID, req.TargetProfileID)
		if refErr != nil || reference.Kind != "directory" || reference.Locator.Path == "" {
			writeValidationError(w, ErrFilesInvalidRef)
			return
		}
		targetDir = path.Clean(reference.Locator.Path)
	}
	selectedPaths := make([]string, 0, len(items))
	for _, item := range items {
		selectedPaths = append(selectedPaths, path.Clean(item.reference.Locator.Path))
	}
	migration := &db.Migration{
		UserID:          sql.NullString{String: userID, Valid: true},
		SourceProfileID: sql.NullString{String: source.ID, Valid: true}, TargetProfileID: sql.NullString{String: target.ID, Valid: true},
		SourceURL: source.URL, SourceUsername: source.Username, SourcePasswordEncrypted: sourcePersisted.PasswordEncrypted, SourceProvider: source.Provider,
		SourceRefreshTokenEncrypted: sourcePersisted.RefreshTokenEncrypted, SourceTokenExpiresAt: source.TokenExpiresAt,
		SourceMegaSessionIDEncrypted: sourcePersisted.MegaSessionIDEncrypted, SourceMegaMasterKeyEncrypted: sourcePersisted.MegaMasterKeyEncrypted,
		TargetURL: target.URL, TargetUsername: target.Username, TargetPasswordEncrypted: targetPersisted.PasswordEncrypted, TargetProvider: target.Provider,
		TargetRefreshTokenEncrypted: targetPersisted.RefreshTokenEncrypted, TargetTokenExpiresAt: target.TokenExpiresAt,
		TargetMegaSessionIDEncrypted: targetPersisted.MegaSessionIDEncrypted, TargetMegaMasterKeyEncrypted: targetPersisted.MegaMasterKeyEncrypted,
		Status: "INDEXING", ConflictStrategy: strategy, TargetDir: targetDir, SelectedPaths: db.StringArray(selectedPaths), Threads: 4,
		PickerSessionID: fileManagerTransferMarker + strings.ToLower(operation),
	}
	id, err := api.migrations.CreateMigration(migration)
	if err != nil {
		api.logf(r, "file transfer create: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	api.writeAudit(r, db.AuditMigrationCreated, id, userID, map[string]interface{}{"origin": "file_manager", "operation": strings.ToLower(operation), "source_profile": source.ID, "target_profile": target.ID})
	if api.startTransferIndexer != nil {
		go api.startTransferIndexer(api.backgroundCtx, id)
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true, "transfer_id": id})
}
