package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"backend/internal/auth"
	"backend/internal/db"
	"backend/internal/oauth"
)

type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

type UpdateLanguageRequest struct {
	Language string `json:"language"`
}

func (s *APIServer) handleUpdateLanguage(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}
	var req UpdateLanguageRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}
	language := strings.ToLower(strings.TrimSpace(req.Language))
	if language != "de" && language != "en" {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}
	if err := db.UpdateUserLanguage(s.db, userID, language); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"language": language})
}

func (s *APIServer) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	var req UpdateProfileRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrDisplayNameRequired)
		return
	}

	if err := db.UpdateUserDisplayName(s.db, userID, req.DisplayName); err != nil {
		s.logf(r, "handleUpdateProfile: failed to update display name: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "display_name": req.DisplayName})
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *APIServer) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}
	userID := claims.UserID
	mustChange := claims.MustChangePassword

	var req ChangePasswordRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.NewPassword != req.ConfirmPassword {
		writeError(w, http.StatusBadRequest, ErrPasswordMismatch)
		return
	}

	if !validatePasswordLength(w, req.NewPassword) {
		return
	}

	u, err := db.GetUserByIDContext(r.Context(), s.db, userID)
	if err != nil {
		s.logf(r, "handleChangePassword: user not found: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if !mustChange {
		if !auth.CheckPasswordHash(req.CurrentPassword, u.PasswordHash) {
			writeError(w, http.StatusUnauthorized, ErrPasswordInvalid)
			return
		}
	}

	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.logf(r, "handleChangePassword: hash error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if _, err := s.db.Exec(`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, newHash, userID); err != nil {
		s.logf(r, "handleChangePassword: update error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.DeleteAllRefreshTokensForUser(s.db, userID); err != nil {
		s.logf(r, "handleChangePassword: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	s.writeAudit(r, db.AuditSettingUpdated, "password", userID, map[string]interface{}{"type": "password_change", "forced": mustChange})

	if mustChange {
		rotated, lerr := db.GetUserByIDContext(r.Context(), s.db, userID)
		if lerr != nil {
			s.logf(r, "handleChangePassword: failed to load user for token rotation: %v\n", lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		rotated.MustChangePassword = false

		if rotated.TotpEnabled {
			tempToken, terr := auth.Generate2FATempToken(rotated, s.jwtSecret)
			if terr != nil {
				s.logf(r, "handleChangePassword: failed to issue 2FA temp token: %v\n", terr)
				writeError(w, http.StatusInternalServerError, ErrInternalError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"totp_required": true,
				"temp_session":  tempToken,
			})
			return
		}

		s.issueTokens(w, r, rotated)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type SetAvatarRequest struct {
	Avatar string `json:"avatar"`
}

func (s *APIServer) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	var req SetAvatarRequest
	if !decodeJSONBody(w, r, &req, avatarJSONBodyLimit) {
		return
	}

	if !strings.HasPrefix(req.Avatar, "data:") {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	parts := strings.SplitN(req.Avatar, ",", 2)
	if len(parts) != 2 {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	header := parts[0]
	payload := parts[1]

	if !strings.HasSuffix(header, ";base64") {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	validMimes := map[string]bool{
		"image/png":  true,
		"image/jpeg": true,
		"image/webp": true,
		"image/gif":  true,
	}
	if !validMimes[mime] {
		writeError(w, http.StatusBadRequest, ErrAvatarTypeUnsupported)
		return
	}

	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrAvatarInvalid)
		return
	}

	if len(data) > 2*1024*1024 {
		writeError(w, http.StatusBadRequest, ErrAvatarTooLarge)
		return
	}
	if http.DetectContentType(data) != mime {
		writeError(w, http.StatusBadRequest, ErrAvatarTypeUnsupported)
		return
	}

	if err := db.UpdateUserAvatar(s.db, userID, data, mime); err != nil {
		s.logf(r, "handleSetAvatar: failed to update avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"avatar":  req.Avatar,
	})
}

func (s *APIServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := s.requireUserID(w, r)
	if !authenticated {
		return
	}

	if err := db.DeleteUserAvatar(s.db, userID); err != nil {
		s.logf(r, "handleDeleteAvatar: failed to delete avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	val, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		s.logf(r, "handleGetSettings: failed to fetch registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if val == "" {
		val = "false"
	}

	needsSetup, err := db.IsSetupRequired(s.db)
	if err != nil {
		s.logf(r, "handleGetSettings: failed to check setup status: %v\n", err)
		needsSetup = false
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"registrations_enabled": val,
		"needs_setup":           needsSetup,
		"local_storage_enabled": os.Getenv("LOCAL_STORAGE_ROOT") != "",
		"oauth_providers":       oauth.ConfiguredProviders(),
	})
}

type UpdateSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *APIServer) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	var req UpdateSettingRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.Key != "registrations_enabled" {
		writeError(w, http.StatusForbidden, ErrSettingForbidden)
		return
	}

	if req.Value != "true" && req.Value != "false" {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}

	if err := db.SetSetting(s.db, req.Key, req.Value); err != nil {
		s.logf(r, "handleUpdateSetting: failed to set setting: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSettingUpdated, req.Key, actor, map[string]interface{}{"value": req.Value})

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) adminActorID(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	// Admin routes are wrapped by adminMiddleware; this repeats the role check
	// as defense in depth for direct handler use or future route changes.
	if !ok || claims == nil || claims.Role != "ADMIN" {
		writeError(w, http.StatusForbidden, ErrAdminOnly)
		return "", false
	}
	return claims.UserID, true
}

type AdminCreateUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

func (s *APIServer) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	var req AdminCreateUserRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	if !validatePasswordLength(w, req.Password) {
		return
	}

	if req.Role == "" {
		req.Role = "USER"
	}
	if req.Role != "USER" && req.Role != "ADMIN" {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	if _, err := db.GetUserByEmail(r.Context(), s.db, req.Email); err == nil {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUserWithRole(s.db, req.Email, passHash, req.DisplayName, req.Role, false, "en")
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		s.logf(r, "Admin create user error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserCreated, u.ID, actor, map[string]interface{}{
		"email": req.Email,
		"role":  req.Role,
	})

	writeJSON(w, http.StatusCreated, userResponse(u))
}

func (s *APIServer) handleAdminSuspendUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}

	if id == actor {
		writeError(w, http.StatusBadRequest, ErrCannotModifySelf)
		return
	}

	syncJobIDs, err := db.SuspendUser(s.db, id)
	if err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		s.logf(r, "Admin suspend %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	var failedSyncCancelEvents []string
	for _, syncJobID := range syncJobIDs {
		s.syncEngine.CancelPass(syncJobID)
		if err := s.queue.PublishSyncCancelEvent(r.Context(), syncJobID); err != nil {
			s.logf(r, "Warning: failed to publish cancel event for suspended user's sync job %s: %v", syncJobID, err)
			failedSyncCancelEvents = append(failedSyncCancelEvents, syncJobID)
		}
	}

	auditDetails := map[string]interface{}(nil)
	if len(failedSyncCancelEvents) > 0 {
		auditDetails = map[string]interface{}{"sync_cancel_event_failures": failedSyncCancelEvents}
	}
	s.writeAudit(r, db.AuditUserSuspended, id, actor, auditDetails)
	response := map[string]interface{}{"success": true, "active": false}
	if len(failedSyncCancelEvents) > 0 {
		// Suspension is committed, but a worker may need manual intervention if
		// it did not receive the cancellation event while Redis was unavailable.
		response["partial"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *APIServer) handleAdminReactivateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}

	if err := db.UpdateUserActive(s.db, id, true); err != nil {
		s.logf(r, "Admin reactivate %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserReactivated, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "active": true})
}

func (s *APIServer) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}

	if id == actor {
		writeError(w, http.StatusBadRequest, ErrCannotModifySelf)
		return
	}

	if err := db.DeleteUser(s.db, id); err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		s.logf(r, "Admin delete %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserDeleted, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type AdminUpdateRoleRequest struct {
	Role string `json:"role"`
}

func (s *APIServer) handleAdminUpdateRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActorID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}

	var req AdminUpdateRoleRequest
	if !decodeJSONBody(w, r, &req, normalJSONBodyLimit) {
		return
	}

	if req.Role != "USER" && req.Role != "ADMIN" {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	target, err := db.GetUserByIDContext(r.Context(), s.db, id)
	if err != nil {
		writeError(w, http.StatusNotFound, ErrUserNotFound)
		return
	}

	if target.Role == req.Role {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "role": req.Role})
		return
	}

	if req.Role != "ADMIN" {
		if id == actor {
			writeError(w, http.StatusBadRequest, ErrCannotModifySelf)
			return
		}
	}

	if err := db.UpdateUserRole(s.db, id, req.Role); err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		s.logf(r, "Admin role change %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserRoleChanged, id, actor, map[string]interface{}{
		"from": target.Role,
		"to":   req.Role,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "role": req.Role})
}

func (s *APIServer) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	role := q.Get("role")
	var active *bool
	if v := q.Get("active"); v != "" {
		b := v == "true" || v == "1"
		active = &b
	}
	search := strings.TrimSpace(q.Get("q"))

	users, total, err := db.ListUsersContext(r.Context(), s.db, db.UserListParams{
		Page:   page,
		Limit:  limit,
		Role:   role,
		Active: active,
		Query:  search,
	})
	if err != nil {
		s.logf(r, "Admin list users: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"users": users,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func (s *APIServer) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	stats, err := db.GetGlobalStats(s.db)
	if err != nil {
		s.logf(r, "Admin stats: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *APIServer) handleAdminListMigrations(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	migrations, total, err := db.ListAllMigrationsContext(r.Context(), s.db, db.MigrationListParams{Page: page, Limit: limit})
	if err != nil {
		s.logf(r, "Admin list migrations: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"migrations": migrations,
		"total":      total,
		"page":       page,
		"limit":      limit,
	})
}

func (s *APIServer) handleAdminListSyncs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	syncs, total, err := db.ListAllSyncJobsContext(r.Context(), s.db, db.SyncListParams{Page: page, Limit: limit})
	if err != nil {
		s.logf(r, "Admin list syncs: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"syncs": syncs,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func (s *APIServer) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActorID(w, r); !ok {
		return
	}

	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	entries, total, err := db.ListAuditLog(s.db, db.AuditLogParams{
		Page:   page,
		Limit:  limit,
		Action: q.Get("action"),
		UserID: q.Get("user_id"),
		Target: q.Get("target"),
		From:   q.Get("from"),
		To:     q.Get("to"),
	})
	if err != nil {
		s.logf(r, "Admin audit log: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   total,
		"page":    page,
		"limit":   limit,
	})
}
