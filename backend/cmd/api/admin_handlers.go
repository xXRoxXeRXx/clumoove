package main

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"backend/internal/auth"
	"backend/internal/db"
	"backend/internal/oauth"
)

func (api *adminAPI) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	val, err := db.GetSetting(api.db, "registrations_enabled")
	if err != nil {
		api.logf(r, "handleGetSettings: failed to fetch registrations_enabled: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	if val == "" {
		val = "false"
	}

	needsSetup, err := db.IsSetupRequired(api.db)
	if err != nil {
		api.logf(r, "handleGetSettings: failed to check setup status: %v\n", err)
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

func (api *adminAPI) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
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

	if err := db.SetSetting(api.db, req.Key, req.Value); err != nil {
		api.logf(r, "handleUpdateSetting: failed to set setting: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditSettingUpdated, req.Key, actor, map[string]interface{}{"value": req.Value})

	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (api *adminAPI) adminActorID(w http.ResponseWriter, r *http.Request) (string, bool) {
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

func (api *adminAPI) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
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

	if _, err := db.GetUserByEmail(r.Context(), api.db, req.Email); err == nil {
		writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
		return
	}

	passHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	u, err := db.CreateUserWithRole(api.db, req.Email, passHash, req.DisplayName, req.Role, false, "en")
	if err != nil {
		if db.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, ErrEmailAlreadyExists)
			return
		}
		api.logf(r, "Admin create user error: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditUserCreated, u.ID, actor, map[string]interface{}{
		"email": req.Email,
		"role":  req.Role,
	})

	writeJSON(w, http.StatusCreated, userResponse(u))
}

func (api *adminAPI) handleAdminSuspendUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
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

	syncJobIDs, err := db.SuspendUser(api.db, id)
	if err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		api.logf(r, "Admin suspend %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	var failedSyncCancelEvents []string
	for _, syncJobID := range syncJobIDs {
		if err := api.cancelSyncJob(r.Context(), syncJobID); err != nil {
			api.logf(r, "Warning: failed to publish cancel event for suspended user's sync job %s: %v", syncJobID, err)
			failedSyncCancelEvents = append(failedSyncCancelEvents, syncJobID)
		}
	}

	auditDetails := map[string]interface{}(nil)
	if len(failedSyncCancelEvents) > 0 {
		auditDetails = map[string]interface{}{"sync_cancel_event_failures": failedSyncCancelEvents}
	}
	api.writeAudit(r, db.AuditUserSuspended, id, actor, auditDetails)
	response := map[string]interface{}{"success": true, "active": false}
	if len(failedSyncCancelEvents) > 0 {
		// Suspension is committed, but a worker may need manual intervention if
		// it did not receive the cancellation event while Redis was unavailable.
		response["partial"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (api *adminAPI) handleAdminReactivateUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrUserNotFound)
		return
	}

	if err := db.UpdateUserActive(api.db, id, true); err != nil {
		api.logf(r, "Admin reactivate %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditUserReactivated, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "active": true})
}

func (api *adminAPI) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
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

	if err := db.DeleteUser(api.db, id); err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		api.logf(r, "Admin delete %s error: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditUserDeleted, id, actor, nil)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

type AdminUpdateRoleRequest struct {
	Role string `json:"role"`
}

func (api *adminAPI) handleAdminUpdateRole(w http.ResponseWriter, r *http.Request) {
	actor, ok := api.adminActorID(w, r)
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

	target, err := db.GetUserByIDContext(r.Context(), api.db, id)
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

	if err := db.UpdateUserRole(api.db, id, req.Role); err != nil {
		if errors.Is(err, db.ErrLastActiveAdmin) {
			writeError(w, http.StatusConflict, ErrLastAdmin)
			return
		}
		api.logf(r, "Admin role change %s: %v\n", id, err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}

	api.writeAudit(r, db.AuditUserRoleChanged, id, actor, map[string]interface{}{
		"from": target.Role,
		"to":   req.Role,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "role": req.Role})
}

func (api *adminAPI) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.adminActorID(w, r); !ok {
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

	users, total, err := db.ListUsersContext(r.Context(), api.db, db.UserListParams{
		Page:   page,
		Limit:  limit,
		Role:   role,
		Active: active,
		Query:  search,
	})
	if err != nil {
		api.logf(r, "Admin list users: %v\n", err)
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

func (api *adminAPI) handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.adminActorID(w, r); !ok {
		return
	}

	stats, err := db.GetGlobalStats(api.db)
	if err != nil {
		api.logf(r, "Admin stats: %v\n", err)
		writeError(w, http.StatusInternalServerError, ErrInternalError)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (api *adminAPI) handleAdminListMigrations(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.adminActorID(w, r); !ok {
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

	migrations, total, err := api.migrations.ListAllMigrationsContext(r.Context(), db.MigrationListParams{Page: page, Limit: limit})
	if err != nil {
		api.logf(r, "Admin list migrations: %v\n", err)
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

func (api *adminAPI) handleAdminListSyncs(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.adminActorID(w, r); !ok {
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

	syncs, total, err := api.syncs.ListAllSyncJobsContext(r.Context(), db.SyncListParams{Page: page, Limit: limit})
	if err != nil {
		api.logf(r, "Admin list syncs: %v\n", err)
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

func (api *adminAPI) handleAdminAuditLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := api.adminActorID(w, r); !ok {
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

	entries, total, err := db.ListAuditLog(api.db, db.AuditLogParams{
		Page:   page,
		Limit:  limit,
		Action: q.Get("action"),
		UserID: q.Get("user_id"),
		Target: q.Get("target"),
		From:   q.Get("from"),
		To:     q.Get("to"),
	})
	if err != nil {
		api.logf(r, "Admin audit log: %v\n", err)
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
