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

func (s *APIServer) handleFileTransfersList(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	profileID := r.PathValue("profileID")
	if _, err := s.loadOwnedFileProfile(r.Context(), userID, profileID); err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT m.id::text, m.picker_session_id, m.status, COALESCE(source.name, ''), COALESCE(target.name, ''),
		       m.total_files, m.processed_files, m.failed_files, m.skipped_files
		FROM migrations m
		LEFT JOIN connection_profiles source ON source.id = m.source_profile_id
		LEFT JOIN connection_profiles target ON target.id = m.target_profile_id
		WHERE m.user_id = $1 AND (m.source_profile_id = $2 OR m.target_profile_id = $2)
		  AND m.picker_session_id LIKE 'file-manager-transfer:%'
		  AND (
		       m.status IN ('PENDING', 'INDEXING', 'RUNNING', 'VERIFYING', 'PAUSED', 'PAUSED_CONNECTION_LOSS')
		       OR (m.status IN ('COMPLETED', 'COMPLETED_WITH_ERRORS', 'FAILED') AND m.updated_at > NOW() - INTERVAL '1 minute')
		  )
		ORDER BY m.created_at DESC LIMIT 20`, userID, profileID)
	if err != nil {
		s.logf(r, "file transfer list: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	defer rows.Close()
	transfers := make([]fileTransferSummary, 0)
	for rows.Next() {
		var transfer fileTransferSummary
		var marker string
		if err := rows.Scan(&transfer.ID, &marker, &transfer.Status, &transfer.SourceProfileName, &transfer.TargetProfileName, &transfer.TotalFiles, &transfer.ProcessedFiles, &transfer.FailedFiles, &transfer.SkippedFiles); err != nil {
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		transfer.Operation = strings.TrimPrefix(marker, fileManagerTransferMarker)
		transfers = append(transfers, transfer)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "transfers": transfers})
}

func (s *APIServer) handleFileTransferCancel(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	migration, err := db.GetMigrationContext(r.Context(), s.db, id)
	if err != nil || !migration.UserID.Valid || migration.UserID.String != userID || !strings.HasPrefix(migration.PickerSessionID, fileManagerTransferMarker) {
		writeError(w, http.StatusNotFound, ErrFilesNotFound)
		return
	}
	if err := db.UpdateMigrationStatus(s.db, id, "CANCELLED", nil); err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if err := db.CancelPendingTasks(s.db, id); err != nil {
		s.logf(r, "file transfer cancel pending tasks %s: %v", id, err)
	}
	if err := s.queue.PublishCancelEvent(r.Context(), id); err != nil {
		s.logf(r, "file transfer cancel event %s: %v", id, err)
	}
	s.writeAudit(r, db.AuditMigrationCancelled, id, userID, map[string]interface{}{"origin": "file_manager"})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleFileTransferCreate(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUserID(w, r)
	if !ok {
		return
	}
	if !s.allowFileRequest(r, userID, "files-transfer", fileMutationRateLimit) {
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
	items, err := s.openBatchReferences(req.Refs, userID, sourceProfileID)
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
	source, err := s.loadOwnedFileProfile(r.Context(), userID, sourceProfileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	target, err := s.loadOwnedFileProfile(r.Context(), userID, req.TargetProfileID)
	if err != nil {
		s.writeFileProfileError(w, err)
		return
	}
	if !storage.ManagerCapabilitiesFor(source.Provider).Browse || !storage.ManagerCapabilitiesFor(target.Provider).Browse {
		writeError(w, http.StatusNotImplemented, ErrFilesUnsupportedOperation)
		return
	}
	targetDir := managedRootPath()
	if req.TargetParentRef != "" {
		reference, refErr := openFileReference(req.TargetParentRef, s.encryptionKey, userID, req.TargetProfileID)
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
		SourceURL: source.URL, SourceUsername: source.Username, SourcePasswordEncrypted: source.PasswordEncrypted, SourceProvider: source.Provider,
		SourceRefreshTokenEncrypted: sql.NullString{String: source.RefreshTokenEncrypted, Valid: source.RefreshTokenEncrypted != ""}, SourceTokenExpiresAt: source.TokenExpiresAt,
		SourceMegaSessionIDEncrypted: source.MegaSessionIDEncrypted, SourceMegaMasterKeyEncrypted: source.MegaMasterKeyEncrypted,
		TargetURL: target.URL, TargetUsername: target.Username, TargetPasswordEncrypted: target.PasswordEncrypted, TargetProvider: target.Provider,
		TargetRefreshTokenEncrypted: sql.NullString{String: target.RefreshTokenEncrypted, Valid: target.RefreshTokenEncrypted != ""}, TargetTokenExpiresAt: target.TokenExpiresAt,
		TargetMegaSessionIDEncrypted: target.MegaSessionIDEncrypted, TargetMegaMasterKeyEncrypted: target.MegaMasterKeyEncrypted,
		Status: "INDEXING", ConflictStrategy: strategy, TargetDir: targetDir, SelectedPaths: db.StringArray(selectedPaths), Threads: 4,
		PickerSessionID: fileManagerTransferMarker + strings.ToLower(operation),
	}
	id, err := db.CreateMigration(s.db, migration)
	if err != nil {
		s.logf(r, "file transfer create: %v", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	s.writeAudit(r, db.AuditMigrationCreated, id, userID, map[string]interface{}{"origin": "file_manager", "operation": strings.ToLower(operation), "source_profile": source.ID, "target_profile": target.ID})
	go s.indexer.Start(s.backgroundCtx, id)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"success": true, "transfer_id": id})
}
