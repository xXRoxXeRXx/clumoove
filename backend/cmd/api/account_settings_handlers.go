package main

import (
	"encoding/base64"
	"net/http"
	"strings"

	"backend/internal/auth"
	"backend/internal/db"
)

type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

type UpdateLanguageRequest struct {
	Language string `json:"language"`
}

func (api *accountAPI) handleUpdateLanguage(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
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
	if err := db.UpdateUserLanguage(api.db, userID, language); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidBody)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"language": language})
}

func (api *accountAPI) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
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

	if err := db.UpdateUserDisplayName(api.db, userID, req.DisplayName); err != nil {
		api.logf(r, "handleUpdateProfile: failed to update display name: %v\n", err)
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

func (api *accountAPI) handleChangePassword(w http.ResponseWriter, r *http.Request) {
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

	u, err := db.GetUserByIDContext(r.Context(), api.db, userID)
	if err != nil {
		api.logf(r, "handleChangePassword: user not found: %v\n", err)
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
		api.logf(r, "handleChangePassword: hash error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if _, err := api.db.Exec(`UPDATE users SET password_hash = $1, must_change_password = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = $2`, newHash, userID); err != nil {
		api.logf(r, "handleChangePassword: update error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	if err := db.DeleteAllRefreshTokensForUser(api.db, userID); err != nil {
		api.logf(r, "handleChangePassword: failed to revoke refresh tokens for user %s: %v\n", userID, err)
	}

	api.writeAudit(r, db.AuditSettingUpdated, "password", userID, map[string]interface{}{"type": "password_change", "forced": mustChange})

	if mustChange {
		rotated, lerr := db.GetUserByIDContext(r.Context(), api.db, userID)
		if lerr != nil {
			api.logf(r, "handleChangePassword: failed to load user for token rotation: %v\n", lerr)
			writeError(w, http.StatusInternalServerError, ErrInternalError)
			return
		}
		rotated.MustChangePassword = false

		if rotated.TotpEnabled {
			tempToken, terr := auth.Generate2FATempToken(rotated, api.jwtSecret)
			if terr != nil {
				api.logf(r, "handleChangePassword: failed to issue 2FA temp token: %v\n", terr)
				writeError(w, http.StatusInternalServerError, ErrInternalError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"totp_required": true,
				"temp_session":  tempToken,
			})
			return
		}

		api.issueTokens(w, r, rotated)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type SetAvatarRequest struct {
	Avatar string `json:"avatar"`
}

func (api *accountAPI) handleSetAvatar(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
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

	if err := db.UpdateUserAvatar(api.db, userID, data, mime); err != nil {
		api.logf(r, "handleSetAvatar: failed to update avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"avatar":  req.Avatar,
	})
}

func (api *accountAPI) handleDeleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID, authenticated := api.requireUserID(w, r)
	if !authenticated {
		return
	}

	if err := db.DeleteUserAvatar(api.db, userID); err != nil {
		api.logf(r, "handleDeleteAvatar: failed to delete avatar: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
