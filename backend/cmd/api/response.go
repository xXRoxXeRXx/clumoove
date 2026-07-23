package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"backend/internal/db"
)

// Helpers
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// APIErrorCode is a machine-readable error identifier sent to the client.
// The frontend localizes it via its own translation tables; the backend
// never sends localized text.
type APIErrorCode string

const (
	ErrInvalidBody              APIErrorCode = "INVALID_BODY"
	ErrUnauthorized             APIErrorCode = "UNAUTHORIZED"
	ErrForbidden                APIErrorCode = "FORBIDDEN"
	ErrCredentialsInvalid       APIErrorCode = "CREDENTIALS_INVALID"
	ErrRefreshTokenMissing      APIErrorCode = "REFRESH_TOKEN_MISSING"
	ErrRefreshTokenInvalid      APIErrorCode = "REFRESH_TOKEN_INVALID"
	ErrRegistrationDisabled     APIErrorCode = "REGISTRATION_DISABLED"
	ErrMissingRequiredFields    APIErrorCode = "MISSING_REQUIRED_FIELDS"
	ErrEmailAlreadyExists       APIErrorCode = "EMAIL_ALREADY_EXISTS"
	ErrRateLimited              APIErrorCode = "RATE_LIMITED"
	ErrTotpRequired             APIErrorCode = "TOTP_REQUIRED"
	ErrTotpCodeRequired         APIErrorCode = "TOTP_CODE_REQUIRED"
	ErrTotpSessionInvalid       APIErrorCode = "TOTP_SESSION_INVALID"
	ErrTotpNotEnabled           APIErrorCode = "TOTP_NOT_ENABLED"
	ErrTotpInvalidCode          APIErrorCode = "TOTP_INVALID_CODE"
	ErrTotpAlreadyEnabled       APIErrorCode = "TOTP_ALREADY_ENABLED"
	ErrTotpNoPendingSetup       APIErrorCode = "TOTP_NO_PENDING_SETUP"
	ErrPasswordRequired         APIErrorCode = "PASSWORD_REQUIRED"
	ErrPasswordInvalid          APIErrorCode = "PASSWORD_INVALID"
	ErrMigrationIdMissing       APIErrorCode = "MIGRATION_ID_MISSING"
	ErrMigrationNotOwned        APIErrorCode = "MIGRATION_NOT_OWNED"
	ErrMigrationInvalidState    APIErrorCode = "MIGRATION_INVALID_STATE"
	ErrMigrationReindexConflict APIErrorCode = "MIGRATION_REINDEX_CONFLICT"
	ErrTooManyActiveMigrations  APIErrorCode = "TOO_MANY_ACTIVE_MIGRATIONS"
	ErrMigrationNotFound        APIErrorCode = "MIGRATION_NOT_FOUND"
	ErrThreadsOutOfRange        APIErrorCode = "THREADS_OUT_OF_RANGE"
	ErrBandwidthOutOfRange      APIErrorCode = "BANDWIDTH_OUT_OF_RANGE"
	ErrNoSourcePaths            APIErrorCode = "NO_SOURCE_PATHS"
	ErrEncryptionFailed         APIErrorCode = "ENCRYPTION_FAILED"
	ErrInvalidScheduledTime     APIErrorCode = "INVALID_SCHEDULED_TIME"
	ErrScheduledTimePast        APIErrorCode = "SCHEDULED_TIME_PAST"
	ErrSourceUrlInvalid         APIErrorCode = "SOURCE_URL_INVALID"
	ErrTargetUrlInvalid         APIErrorCode = "TARGET_URL_INVALID"
	ErrSourceConnectionFailed   APIErrorCode = "SOURCE_CONNECTION_FAILED"
	ErrTargetConnectionFailed   APIErrorCode = "TARGET_CONNECTION_FAILED"
	ErrListFailed               APIErrorCode = "LIST_FAILED"
	ErrProviderUnsupported      APIErrorCode = "PROVIDER_UNSUPPORTED"
	ErrFolderPathInvalid        APIErrorCode = "FOLDER_PATH_INVALID"
	ErrFolderCreateFailed       APIErrorCode = "FOLDER_CREATE_FAILED"
	ErrInvalidResourceType      APIErrorCode = "INVALID_RESOURCE_TYPE"
	ErrOauthProviderMissing     APIErrorCode = "OAUTH_PROVIDER_MISSING"
	ErrOauthOriginMissing       APIErrorCode = "OAUTH_ORIGIN_MISSING"
	ErrOauthOriginInvalid       APIErrorCode = "OAUTH_ORIGIN_INVALID"
	ErrOauthOriginUntrusted     APIErrorCode = "OAUTH_ORIGIN_UNTRUSTED"
	ErrOauthGenerationFailed    APIErrorCode = "OAUTH_GENERATION_FAILED"
	ErrDisplayNameRequired      APIErrorCode = "DISPLAY_NAME_REQUIRED"
	ErrPasswordMismatch         APIErrorCode = "PASSWORD_MISMATCH"
	ErrPasswordTooShort         APIErrorCode = "PASSWORD_TOO_SHORT"
	ErrAvatarInvalid            APIErrorCode = "AVATAR_INVALID"
	ErrAvatarTypeUnsupported    APIErrorCode = "AVATAR_TYPE_UNSUPPORTED"
	ErrAvatarTooLarge           APIErrorCode = "AVATAR_TOO_LARGE"
	ErrAdminOnly                APIErrorCode = "ADMIN_ONLY"
	ErrSettingForbidden         APIErrorCode = "SETTING_FORBIDDEN"
	ErrSettingInvalid           APIErrorCode = "SETTING_INVALID"
	ErrScheduleIdMissing        APIErrorCode = "SCHEDULE_ID_MISSING"
	ErrScheduleNotFound         APIErrorCode = "SCHEDULE_NOT_FOUND"
	ErrSmtpConfigIncomplete     APIErrorCode = "SMTP_CONFIG_INCOMPLETE"
	ErrSmtpPortInvalid          APIErrorCode = "SMTP_PORT_INVALID"
	ErrSmtpEncryptionInvalid    APIErrorCode = "SMTP_ENCRYPTION_INVALID"
	ErrSmtpPasswordRequired     APIErrorCode = "SMTP_PASSWORD_REQUIRED"
	ErrMailNotConfigured        APIErrorCode = "MAIL_NOT_CONFIGURED"
	ErrSmtpNotConfigured        APIErrorCode = "SMTP_NOT_CONFIGURED"
	ErrSmtpDecryptFailed        APIErrorCode = "SMTP_DECRYPT_FAILED"
	ErrSmtpTestFailed           APIErrorCode = "SMTP_TEST_FAILED"
	ErrResetFieldsRequired      APIErrorCode = "RESET_FIELDS_REQUIRED"
	ErrResetTokenInvalid        APIErrorCode = "RESET_TOKEN_INVALID"
	ErrEmailInvalid             APIErrorCode = "EMAIL_INVALID"
	ErrEmailUnchanged           APIErrorCode = "EMAIL_UNCHANGED"
	ErrEmailChangeTokenInvalid  APIErrorCode = "EMAIL_CHANGE_TOKEN_INVALID"
	ErrCorsOriginUntrusted      APIErrorCode = "CORS_ORIGIN_UNTRUSTED"
	ErrWsTokenInsecure          APIErrorCode = "WS_TOKEN_INSECURE"
	ErrWsTokenMissing           APIErrorCode = "WS_TOKEN_MISSING"
	ErrWsTokenInvalid           APIErrorCode = "WS_TOKEN_INVALID"
	ErrSetupAlreadyCompleted    APIErrorCode = "SETUP_ALREADY_COMPLETED"
	ErrInternalError            APIErrorCode = "INTERNAL_ERROR"

	ErrUserDisabled                       APIErrorCode = "USER_DISABLED"
	ErrUserNotFound                       APIErrorCode = "USER_NOT_FOUND"
	ErrCannotModifySelf                   APIErrorCode = "CANNOT_MODIFY_SELF"
	ErrLastAdmin                          APIErrorCode = "LAST_ADMIN"
	ErrInvalidRole                        APIErrorCode = "INVALID_ROLE"
	ErrPasswordChangeRequired             APIErrorCode = "PASSWORD_CHANGE_REQUIRED"

	// Sync Engine
	ErrSyncIdMissing      APIErrorCode = "SYNC_ID_MISSING"
	ErrSyncNotFound       APIErrorCode = "SYNC_NOT_FOUND"
	ErrSyncNotOwned       APIErrorCode = "SYNC_NOT_OWNED"
	ErrSyncAlreadyRunning APIErrorCode = "SYNC_ALREADY_RUNNING"
	ErrSyncInvalidState   APIErrorCode = "SYNC_INVALID_STATE"

	// Connection profiles
	ErrProfileNotFound        APIErrorCode = "PROFILE_NOT_FOUND"
	ErrProfileNameExists      APIErrorCode = "PROFILE_NAME_EXISTS"
	ErrProfileInvalidProvider APIErrorCode = "PROFILE_INVALID_PROVIDER"
	ErrProfileURLRequired     APIErrorCode = "PROFILE_URL_REQUIRED"
)

// writeError emits a structured error response carrying only a machine-readable
// code. It deliberately omits any localized message (the frontend translates).
func writeError(w http.ResponseWriter, status int, code APIErrorCode) {
	writeJSON(w, status, map[string]any{"success": false, "error_code": string(code)})
}

func writeValidationError(w http.ResponseWriter, code APIErrorCode) {
	writeError(w, http.StatusBadRequest, code)
}

func writeConflictError(w http.ResponseWriter, code APIErrorCode) {
	writeError(w, http.StatusConflict, code)
}

// clientIP returns a stable per-client key for rate limiting. When a trusted
// reverse proxy is configured, the leftmost X-Forwarded-For address is used;
// otherwise the connection's remote address (port stripped) is used.
func (s *APIServer) clientIP(r *http.Request) string {
	var raw string
	if s.trustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if idx := strings.IndexByte(xff, ','); idx >= 0 {
				raw = strings.TrimSpace(xff[:idx])
			} else {
				raw = strings.TrimSpace(xff)
			}
		}
	}
	if raw == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			raw = r.RemoteAddr
		} else {
			raw = host
		}
	}
	return sanitizeAuditToken(raw)
}

// sanitizeAuditToken removes CR/LF and all control characters (C0 + DEL) from a
// value that will be persisted into structured/audit logs or used as a rate
// limiting key.
func sanitizeAuditToken(s string) string {
	const maxTokenLen = 254
	if len(s) > maxTokenLen {
		s = s[:maxTokenLen]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r <= 0x1f || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isSecure reports whether the request arrived over HTTPS.
func (s *APIServer) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.trustedProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// writeAudit appends an audit-log entry for the current request.
func (s *APIServer) writeAudit(r *http.Request, action db.AuditAction, target string, actor string, details map[string]interface{}) {
	var uid sql.NullString
	if actor != "" {
		uid = sql.NullString{String: actor, Valid: true}
	}
	var d json.RawMessage
	if details != nil {
		if b, err := json.Marshal(details); err == nil {
			d = b
		}
	}
	db.WriteAuditLog(s.db, db.AuditEntry{
		UserID:  uid,
		Action:  action,
		Target:  target,
		IP:      s.clientIP(r),
		Details: d,
	})
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

func (s *APIServer) getRedirectURI(r *http.Request) string {
	if envRedirect := os.Getenv("OAUTH_REDIRECT_URI"); envRedirect != "" {
		return envRedirect
	}
	if envBase := os.Getenv("OAUTH_PUBLIC_BASE_URL"); envBase != "" {
		scheme := "https"
		if strings.HasPrefix(envBase, "http://") {
			scheme = "http"
		}
		envBase = strings.TrimPrefix(strings.TrimPrefix(envBase, "https://"), "http://")
		envBase = strings.TrimRight(envBase, "/")
		return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, envBase)
	}
	scheme := "http"
	if s.isSecure(r) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/oauth/callback", scheme, r.Host)
}

func csvCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	default:
		return s
	}
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func stripScriptTerminator(s string) string {
	s = strings.ReplaceAll(s, "</script>", "")
	s = strings.ReplaceAll(s, "</SCRIPT>", "")
	return s
}
