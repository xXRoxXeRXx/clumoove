package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"backend/internal/auth"
	"backend/internal/crypto"
	"backend/internal/db"
	"backend/internal/email"
	"backend/internal/oauth"
)

type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

func (s *APIServer) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req UpdateProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrDisplayNameRequired)
		return
	}

	if err := db.UpdateUserDisplayName(s.db, userID, req.DisplayName); err != nil {
		log.Printf("handleUpdateProfile: failed to update display name: %v\n", err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.NewPassword != req.ConfirmPassword {
		writeError(w, http.StatusBadRequest, ErrPasswordMismatch)
		return
	}

	if len(req.NewPassword) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	u, err := db.GetUserByID(s.db, userID)
	if err != nil {
		log.Printf("handleChangePassword: user not found: %v\n", err)
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
		log.Printf("handleChangePassword: hash error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if _, err := s.db.Exec(`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, newHash, userID); err != nil {
		log.Printf("handleChangePassword: update error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.DeleteAllRefreshTokensForUser(s.db, userID); err != nil {
		log.Printf("handleChangePassword: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	s.writeAudit(r, db.AuditSettingUpdated, "password", userID, map[string]interface{}{"type": "password_change", "forced": mustChange})

	if mustChange {
		rotated, lerr := db.GetUserByID(s.db, userID)
		if lerr != nil {
			log.Printf("handleChangePassword: failed to load user for token rotation: %v\n", lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		rotated.MustChangePassword = false
		accessToken, terr := auth.GenerateAccessToken(rotated, s.jwtSecret)
		if terr != nil {
			log.Printf("handleChangePassword: failed to issue rotated token: %v\n", terr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success":      true,
			"access_token": accessToken,
			"user":         userResponse(rotated),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type SetAvatarRequest struct {
	Avatar string `json:"avatar"`
}

func (s *APIServer) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req SetAvatarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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

	if err := db.UpdateUserAvatar(s.db, userID, data, mime); err != nil {
		log.Printf("handleSetAvatar: failed to update avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"avatar":  req.Avatar,
	})
}

func (s *APIServer) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	if err := db.DeleteUserAvatar(s.db, userID); err != nil {
		log.Printf("handleDeleteAvatar: failed to delete avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	val, err := db.GetSetting(s.db, "registrations_enabled")
	if err != nil {
		log.Printf("handleGetSettings: failed to fetch registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if val == "" {
		val = "true"
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"registrations_enabled": val,
		"local_storage_enabled": os.Getenv("LOCAL_STORAGE_ROOT") != "",
		"oauth_providers":       oauth.ConfiguredProviders(),
	})
}

type UpdateSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *APIServer) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	if claims.Role != "ADMIN" {
		writeError(w, http.StatusForbidden, ErrAdminOnly)
		return
	}

	var req UpdateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
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
		log.Printf("handleUpdateSetting: failed to set setting: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditSettingUpdated, req.Key, claims.UserID, map[string]interface{}{"value": req.Value})

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) adminActorID(w http.ResponseWriter, r *http.Request) (string, bool) {
	claims, ok := r.Context().Value(auth.ClaimsKey).(*auth.Claims)
	if !ok || claims == nil || claims.Role != "ADMIN" {
		writeError(w, http.StatusForbidden, ErrAdminOnly)
		return "", false
	}
	return claims.UserID, true
}

func (s *APIServer) wouldRemoveLastActiveAdmin(targetID string) (bool, error) {
	u, err := db.GetUserByID(s.db, targetID)
	if err != nil {
		return false, err
	}
	if u.Role != "ADMIN" || !u.Active {
		return false, nil
	}
	count, err := db.CountActiveAdmins(s.db)
	if err != nil {
		return false, err
	}
	return count <= 1, nil
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Email == "" || req.Password == "" || req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, ErrMissingRequiredFields)
		return
	}

	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, ErrPasswordTooShort)
		return
	}

	if req.Role == "" {
		req.Role = "USER"
	}
	if req.Role != "USER" && req.Role != "ADMIN" {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	if _, err := db.GetUserByEmail(s.db, req.Email); err == nil {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUserWithRole(s.db, req.Email, passHash, req.DisplayName, req.Role, false)
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		log.Printf("Admin create user error: %v\n", err)
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

	last, err := s.wouldRemoveLastActiveAdmin(id)
	if err != nil {
		log.Printf("Admin suspend %s error checking last admin: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if last {
		writeError(w, http.StatusConflict, ErrLastAdmin)
		return
	}

	if err := db.UpdateUserActive(s.db, id, false); err != nil {
		log.Printf("Admin suspend %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	s.writeAudit(r, db.AuditUserSuspended, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "active": false})
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
		log.Printf("Admin reactivate %s error: %v\n", id, err)
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

	last, err := s.wouldRemoveLastActiveAdmin(id)
	if err != nil {
		log.Printf("Admin delete %s error checking last admin: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if last {
		writeError(w, http.StatusConflict, ErrLastAdmin)
		return
	}

	if err := db.DeleteUser(s.db, id); err != nil {
		log.Printf("Admin delete %s error: %v\n", id, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.Role != "USER" && req.Role != "ADMIN" {
		writeError(w, http.StatusBadRequest, ErrInvalidRole)
		return
	}

	target, err := db.GetUserByID(s.db, id)
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
		last, err := s.wouldRemoveLastActiveAdmin(id)
		if err != nil {
			log.Printf("Admin role change %s error checking last admin: %v\n", id, err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if last {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
	}

	if err := db.UpdateUserRole(s.db, id, req.Role); err != nil {
		log.Printf("Admin role change %s: %v\n", id, err)
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

	users, total, err := db.ListUsers(s.db, db.UserListParams{
		Page:   page,
		Limit:  limit,
		Role:   role,
		Active: active,
		Query:  search,
	})
	if err != nil {
		log.Printf("Admin list users: %v\n", err)
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
		log.Printf("Admin stats: %v\n", err)
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

	migrations, total, err := db.ListAllMigrations(s.db, db.MigrationListParams{Page: page, Limit: limit})
	if err != nil {
		log.Printf("Admin list migrations: %v\n", err)
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

	syncs, total, err := db.ListAllSyncJobs(s.db, db.SyncListParams{Page: page, Limit: limit})
	if err != nil {
		log.Printf("Admin list syncs: %v\n", err)
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
		log.Printf("Admin audit log: %v\n", err)
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

func (s *APIServer) handleGetSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		log.Printf("handleGetSMTPSettings: error fetching settings: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"smtp_host":            settings.SMTPHost,
		"smtp_port":            settings.SMTPPort,
		"smtp_username":        settings.SMTPUsername,
		"smtp_password_set":    true,
		"smtp_from_email":      settings.SMTPFromEmail,
		"smtp_from_name":       settings.SMTPFromName,
		"smtp_encryption":      settings.SMTPEncryption,
		"notify_on_completion": settings.NotifyOnCompletion,
	})
}

func (s *APIServer) handleUpdateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	var req struct {
		SMTPHost           string `json:"smtp_host"`
		SMTPPort           int    `json:"smtp_port"`
		SMTPUsername       string `json:"smtp_username"`
		SMTPPassword       string `json:"smtp_password"`
		PasswordChanged    bool   `json:"password_changed"`
		SMTPFromEmail      string `json:"smtp_from_email"`
		SMTPFromName       string `json:"smtp_from_name"`
		SMTPEncryption     string `json:"smtp_encryption"`
		NotifyOnCompletion *bool  `json:"notify_on_completion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}

	if req.SMTPHost == "" || req.SMTPUsername == "" || req.SMTPFromEmail == "" {
		writeError(w, http.StatusBadRequest, ErrSmtpConfigIncomplete)
		return
	}

	if err := email.ValidateSMTPHost(req.SMTPHost); err != nil {
		writeError(w, http.StatusBadRequest, ErrSettingInvalid)
		return
	}

	if req.SMTPPort < 1 || req.SMTPPort > 65535 {
		writeError(w, http.StatusBadRequest, ErrSmtpPortInvalid)
		return
	}

	switch req.SMTPEncryption {
	case "tls", "starttls", "none":
	default:
		writeError(w, http.StatusBadRequest, ErrSmtpEncryptionInvalid)
		return
	}

	notify := true
	if req.NotifyOnCompletion != nil {
		notify = *req.NotifyOnCompletion
	}

	var encryptedPassword string
	passwordProvided := req.PasswordChanged || req.SMTPPassword != ""
	if !passwordProvided {
		existing, err := db.GetUserSMTPSettings(s.db, userID)
		if err != nil && err != sql.ErrNoRows {
			log.Printf("handleUpdateSMTPSettings: error fetching existing settings: %v\n", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		if existing != nil {
			encryptedPassword = existing.SMTPPasswordEnc
		} else {
			writeError(w, http.StatusBadRequest, ErrSmtpPasswordRequired)
			return
		}
	} else {
		if req.SMTPPassword == "" {
			writeError(w, http.StatusBadRequest, ErrSmtpPasswordRequired)
			return
		}
		enc, err := crypto.Encrypt(req.SMTPPassword, s.encryptionKey)
		if err != nil {
			log.Printf("handleUpdateSMTPSettings: error encrypting password: %v\n", err)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		encryptedPassword = enc
	}

	settings := &db.UserSMTPSettings{
		UserID:             userID,
		SMTPHost:           req.SMTPHost,
		SMTPPort:           req.SMTPPort,
		SMTPUsername:       req.SMTPUsername,
		SMTPPasswordEnc:    encryptedPassword,
		SMTPFromEmail:      req.SMTPFromEmail,
		SMTPFromName:       req.SMTPFromName,
		SMTPEncryption:     req.SMTPEncryption,
		NotifyOnCompletion: notify,
	}

	if err := db.UpsertUserSMTPSettings(s.db, settings); err != nil {
		log.Printf("handleUpdateSMTPSettings: error upserting settings: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *APIServer) handleTestSMTP(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return
	}

	settings, err := db.GetUserSMTPSettings(s.db, userID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpNotConfigured})
			return
		}
		log.Printf("handleTestSMTP: error fetching settings: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrInternalError})
		return
	}

	password, err := crypto.Decrypt(settings.SMTPPasswordEnc, s.encryptionKey)
	if err != nil {
		log.Printf("handleTestSMTP: error decrypting password: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpDecryptFailed})
		return
	}

	user, err := db.GetUserByID(s.db, userID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrMailNotConfigured})
		return
	}

	smtpCfg := email.SMTPConfig{
		Host:       settings.SMTPHost,
		Port:       strconv.Itoa(settings.SMTPPort),
		Username:   settings.SMTPUsername,
		Password:   password,
		FromEmail:  settings.SMTPFromEmail,
		FromName:   settings.SMTPFromName,
		Encryption: settings.SMTPEncryption,
	}

	if err := email.SendMail(smtpCfg, user.Email, "Clumoove — SMTP-Test erfolgreich", email.BuildTestEmail()); err != nil {
		log.Printf("handleTestSMTP: send failed: %v\n", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error_code": ErrSmtpTestFailed})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
