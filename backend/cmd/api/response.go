package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"backend/internal/auth"
	"backend/internal/db"
	"backend/internal/httpresp"
)

const (
	normalJSONBodyLimit = 1 << 20  // 1 MiB
	authJSONBodyLimit   = 64 << 10 // 64 KiB
	// Base64-encoded 2 MiB avatars need about 2.67 MiB plus JSON framing.
	avatarJSONBodyLimit = 3 << 20 // 3 MiB
)

// decodeJSONBody bounds every JSON request body and rejects trailing data.
// Reading through the end is intentional: it makes an oversized body fail even
// when its first JSON value is valid before the byte limit is reached.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if !decodeJSON(r, dst) {
		writeValidationError(w, ErrInvalidBody)
		return false
	}
	return true
}

// requireUserID turns a missing claims context into a 401. Protected handlers
// call it defensively so an accidentally unprotected route cannot be mistaken
// for an ownership failure.
func (s *APIServer) requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := auth.GetUserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized)
		return "", false
	}
	return userID, true
}

// decodeJSONBodySilent applies the same bounded decoding without exposing a
// parse failure. It is used only for anti-enumeration responses. The nil
// ResponseWriter intentionally suppresses MaxBytesReader's usual 413 write.
func decodeJSONBodySilent(r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(nil, r.Body, limit)
	return decodeJSON(r, dst)
}

func decodeJSON(r *http.Request, dst any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return false
	}
	return true
}

// Helpers delegate to internal/httpresp so auth middleware and API handlers
// always emit the same response envelope.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	httpresp.WriteJSON(w, status, data)
}

// APIErrorCode is a machine-readable error identifier sent to the client.
// The frontend localizes it via its own translation tables; the backend
// never sends localized text.
type APIErrorCode = httpresp.APIErrorCode

const (
	ErrInvalidBody                  APIErrorCode = "INVALID_BODY"
	ErrUnauthorized                              = httpresp.ErrUnauthorized
	ErrForbidden                    APIErrorCode = "FORBIDDEN"
	ErrCredentialsInvalid           APIErrorCode = "CREDENTIALS_INVALID"
	ErrRefreshTokenMissing          APIErrorCode = "REFRESH_TOKEN_MISSING"
	ErrRefreshTokenInvalid          APIErrorCode = "REFRESH_TOKEN_INVALID"
	ErrSessionNotFound              APIErrorCode = "SESSION_NOT_FOUND"
	ErrRegistrationDisabled         APIErrorCode = "REGISTRATION_DISABLED"
	ErrMissingRequiredFields        APIErrorCode = "MISSING_REQUIRED_FIELDS"
	ErrEmailAlreadyExists           APIErrorCode = "EMAIL_ALREADY_EXISTS"
	ErrRateLimited                  APIErrorCode = "RATE_LIMITED"
	ErrTotpRequired                 APIErrorCode = "TOTP_REQUIRED"
	ErrTotpCodeRequired             APIErrorCode = "TOTP_CODE_REQUIRED"
	ErrTotpSessionInvalid           APIErrorCode = "TOTP_SESSION_INVALID"
	ErrTotpNotEnabled               APIErrorCode = "TOTP_NOT_ENABLED"
	ErrTotpInvalidCode              APIErrorCode = "TOTP_INVALID_CODE"
	ErrTotpAlreadyEnabled           APIErrorCode = "TOTP_ALREADY_ENABLED"
	ErrTotpNoPendingSetup           APIErrorCode = "TOTP_NO_PENDING_SETUP"
	ErrPasswordRequired             APIErrorCode = "PASSWORD_REQUIRED"
	ErrPasswordInvalid              APIErrorCode = "PASSWORD_INVALID"
	ErrMigrationIdMissing           APIErrorCode = "MIGRATION_ID_MISSING"
	ErrMigrationNotOwned            APIErrorCode = "MIGRATION_NOT_OWNED"
	ErrMigrationInvalidState        APIErrorCode = "MIGRATION_INVALID_STATE"
	ErrMigrationReindexConflict     APIErrorCode = "MIGRATION_REINDEX_CONFLICT"
	ErrTooManyActiveMigrations      APIErrorCode = "TOO_MANY_ACTIVE_MIGRATIONS"
	ErrMigrationNotFound            APIErrorCode = "MIGRATION_NOT_FOUND"
	ErrThreadsOutOfRange            APIErrorCode = "THREADS_OUT_OF_RANGE"
	ErrBandwidthOutOfRange          APIErrorCode = "BANDWIDTH_OUT_OF_RANGE"
	ErrNoSourcePaths                APIErrorCode = "NO_SOURCE_PATHS"
	ErrConflictStrategyInvalid      APIErrorCode = "CONFLICT_STRATEGY_INVALID"
	ErrEncryptionFailed             APIErrorCode = "ENCRYPTION_FAILED"
	ErrInvalidScheduledTime         APIErrorCode = "INVALID_SCHEDULED_TIME"
	ErrScheduledTimePast            APIErrorCode = "SCHEDULED_TIME_PAST"
	ErrSourceUrlInvalid             APIErrorCode = "SOURCE_URL_INVALID"
	ErrTargetUrlInvalid             APIErrorCode = "TARGET_URL_INVALID"
	ErrSourceConnectionFailed       APIErrorCode = "SOURCE_CONNECTION_FAILED"
	ErrTargetConnectionFailed       APIErrorCode = "TARGET_CONNECTION_FAILED"
	ErrMegaMFAUnsupported           APIErrorCode = "MEGA_MFA_UNSUPPORTED"
	ErrListFailed                   APIErrorCode = "LIST_FAILED"
	ErrProviderUnsupported          APIErrorCode = "PROVIDER_UNSUPPORTED"
	ErrFolderPathInvalid            APIErrorCode = "FOLDER_PATH_INVALID"
	ErrFolderCreateFailed           APIErrorCode = "FOLDER_CREATE_FAILED"
	ErrInvalidResourceType          APIErrorCode = "INVALID_RESOURCE_TYPE"
	ErrOauthProviderMissing         APIErrorCode = "OAUTH_PROVIDER_MISSING"
	ErrOauthOriginMissing           APIErrorCode = "OAUTH_ORIGIN_MISSING"
	ErrOauthOriginInvalid           APIErrorCode = "OAUTH_ORIGIN_INVALID"
	ErrOauthOriginUntrusted         APIErrorCode = "OAUTH_ORIGIN_UNTRUSTED"
	ErrOauthGenerationFailed        APIErrorCode = "OAUTH_GENERATION_FAILED"
	ErrOauthExchangeFailed          APIErrorCode = "OAUTH_EXCHANGE_FAILED"
	ErrOauthPopupBlocked            APIErrorCode = "OAUTH_POPUP_BLOCKED" // Frontend-emitted OAuth flow outcome.
	ErrOauthCancelled               APIErrorCode = "OAUTH_CANCELLED"     // Frontend-emitted OAuth flow outcome.
	ErrSyncDirectionInvalid         APIErrorCode = "SYNC_DIRECTION_INVALID"
	ErrDisplayNameRequired          APIErrorCode = "DISPLAY_NAME_REQUIRED"
	ErrPasswordMismatch             APIErrorCode = "PASSWORD_MISMATCH"
	ErrPasswordTooShort             APIErrorCode = "PASSWORD_TOO_SHORT"
	ErrPasswordTooLong              APIErrorCode = "PASSWORD_TOO_LONG"
	ErrAvatarInvalid                APIErrorCode = "AVATAR_INVALID"
	ErrAvatarTypeUnsupported        APIErrorCode = "AVATAR_TYPE_UNSUPPORTED"
	ErrAvatarTooLarge               APIErrorCode = "AVATAR_TOO_LARGE"
	ErrAdminOnly                    APIErrorCode = "ADMIN_ONLY"
	ErrSettingForbidden             APIErrorCode = "SETTING_FORBIDDEN"
	ErrSettingInvalid               APIErrorCode = "SETTING_INVALID"
	ErrScheduleIdMissing            APIErrorCode = "SCHEDULE_ID_MISSING"
	ErrScheduleNotFound             APIErrorCode = "SCHEDULE_NOT_FOUND"
	ErrSmtpConfigIncomplete         APIErrorCode = "SMTP_CONFIG_INCOMPLETE"
	ErrSmtpPortInvalid              APIErrorCode = "SMTP_PORT_INVALID"
	ErrSmtpEncryptionInvalid        APIErrorCode = "SMTP_ENCRYPTION_INVALID"
	ErrSmtpPasswordRequired         APIErrorCode = "SMTP_PASSWORD_REQUIRED"
	ErrMailNotConfigured            APIErrorCode = "MAIL_NOT_CONFIGURED"
	ErrSmtpNotConfigured            APIErrorCode = "SMTP_NOT_CONFIGURED"
	ErrSmtpDecryptFailed            APIErrorCode = "SMTP_DECRYPT_FAILED"
	ErrSmtpTestFailed               APIErrorCode = "SMTP_TEST_FAILED"
	ErrOauthProviderUnknown         APIErrorCode = "OAUTH_PROVIDER_UNKNOWN"
	ErrOauthConfigIncomplete        APIErrorCode = "OAUTH_CONFIG_INCOMPLETE"
	ErrOauthSecretRequired          APIErrorCode = "OAUTH_SECRET_REQUIRED"
	ErrNotificationChannelInvalid   APIErrorCode = "NOTIFICATION_CHANNEL_INVALID"
	ErrNotificationConfigIncomplete APIErrorCode = "NOTIFICATION_CONFIG_INCOMPLETE"
	ErrNotificationURLInvalid       APIErrorCode = "NOTIFICATION_URL_INVALID"
	ErrNotificationPriorityInvalid  APIErrorCode = "NOTIFICATION_PRIORITY_INVALID"
	ErrNotificationTestFailed       APIErrorCode = "NOTIFICATION_TEST_FAILED"
	ErrNotificationDecryptFailed    APIErrorCode = "NOTIFICATION_DECRYPT_FAILED"
	ErrNotificationURLBlocked       APIErrorCode = "NOTIFICATION_URL_BLOCKED"
	ErrResetFieldsRequired          APIErrorCode = "RESET_FIELDS_REQUIRED"
	ErrResetTokenInvalid            APIErrorCode = "RESET_TOKEN_INVALID"
	ErrEmailInvalid                 APIErrorCode = "EMAIL_INVALID"
	ErrEmailUnchanged               APIErrorCode = "EMAIL_UNCHANGED"
	ErrEmailChangeTokenInvalid      APIErrorCode = "EMAIL_CHANGE_TOKEN_INVALID"
	ErrCorsOriginUntrusted          APIErrorCode = "CORS_ORIGIN_UNTRUSTED"
	ErrSetupAlreadyCompleted        APIErrorCode = "SETUP_ALREADY_COMPLETED"
	ErrSeafileAuthFailed            APIErrorCode = "SEAFILE_AUTH_FAILED"
	ErrInternalError                APIErrorCode = "INTERNAL_ERROR"

	ErrUserDisabled           APIErrorCode = "USER_DISABLED"
	ErrUserNotFound           APIErrorCode = "USER_NOT_FOUND"
	ErrCannotModifySelf       APIErrorCode = "CANNOT_MODIFY_SELF"
	ErrLastAdmin              APIErrorCode = "LAST_ADMIN"
	ErrInvalidRole            APIErrorCode = "INVALID_ROLE"
	ErrPasswordChangeRequired APIErrorCode = "PASSWORD_CHANGE_REQUIRED"

	// Sync Engine
	ErrSyncIdMissing       APIErrorCode = "SYNC_ID_MISSING"
	ErrSyncNotFound        APIErrorCode = "SYNC_NOT_FOUND"
	ErrSyncNotOwned        APIErrorCode = "SYNC_NOT_OWNED"
	ErrSyncAlreadyRunning  APIErrorCode = "SYNC_ALREADY_RUNNING"
	ErrSyncInvalidState    APIErrorCode = "SYNC_INVALID_STATE"
	ErrSyncIntervalInvalid APIErrorCode = "SYNC_INTERVAL_INVALID"

	// Backup
	ErrBackupIDMissing           APIErrorCode = "BACKUP_ID_MISSING"
	ErrBackupNotFound            APIErrorCode = "BACKUP_NOT_FOUND"
	ErrBackupInvalidState        APIErrorCode = "BACKUP_INVALID_STATE"
	ErrBackupCronInvalid         APIErrorCode = "BACKUP_CRON_INVALID"
	ErrBackupTimezoneInvalid     APIErrorCode = "BACKUP_TIMEZONE_INVALID"
	ErrBackupRetentionInvalid    APIErrorCode = "BACKUP_RETENTION_INVALID"
	ErrBackupPathsInvalid        APIErrorCode = "BACKUP_PATHS_INVALID"
	ErrBackupSourceTargetOverlap APIErrorCode = "BACKUP_SOURCE_TARGET_OVERLAP"
	ErrImmichBackupUnsupported   APIErrorCode = "IMMICH_BACKUP_UNSUPPORTED"
	ErrBackupLockUnavailable     APIErrorCode = "BACKUP_LOCK_UNAVAILABLE"
	ErrBackupFilesUnsupported    APIErrorCode = "BACKUP_FILES_UNSUPPORTED"
	ErrBackupVerifyInvalid       APIErrorCode = "BACKUP_VERIFY_INVALID"

	// Restore
	ErrRestoreNotFound            APIErrorCode = "RESTORE_NOT_FOUND"
	ErrRestorePreviewInvalidState APIErrorCode = "RESTORE_PREVIEW_INVALID_STATE"
	ErrRestoreSnapshotUnavailable APIErrorCode = "RESTORE_SNAPSHOT_UNAVAILABLE"
	ErrRestorePreviewExpired      APIErrorCode = "RESTORE_PREVIEW_EXPIRED"
	ErrRestorePreviewStale        APIErrorCode = "RESTORE_PREVIEW_STALE"
	ErrRestoreRepositoryOverlap   APIErrorCode = "RESTORE_REPOSITORY_OVERLAP"
	ErrRestoreTypeConflict        APIErrorCode = "RESTORE_TYPE_CONFLICT"
	ErrRestorePackCorrupt         APIErrorCode = "RESTORE_PACK_CORRUPT"
	ErrRestoreCredentialExpired   APIErrorCode = "RESTORE_CREDENTIAL_EXPIRED"
	ErrRestoreCancelled           APIErrorCode = "RESTORE_CANCELLED"
	ErrBackupVerifyLimit          APIErrorCode = "BACKUP_VERIFY_LIMIT_INVALID"
	ErrBackupVerifyCancelled      APIErrorCode = "BACKUP_VERIFY_CANCELLED"
	ErrBackupConnectionFailed     APIErrorCode = "BACKUP_CONNECTION_FAILED"
	ErrBackupTargetNotEmpty       APIErrorCode = "BACKUP_TARGET_NOT_EMPTY"
	ErrBackupFormatInvalid        APIErrorCode = "BACKUP_REPOSITORY_FORMAT_INVALID"
	ErrBackupTransitionFailed     APIErrorCode = "BACKUP_TRANSITION_FAILED"
	ErrBackupCatalogFailed        APIErrorCode = "BACKUP_CATALOG_FAILED"
	ErrBackupScanFailed           APIErrorCode = "BACKUP_SCAN_FAILED"
	ErrBackupRunFailed            APIErrorCode = "BACKUP_RUN_FAILED"
	ErrBackupVerificationFailed   APIErrorCode = "BACKUP_VERIFICATION_FAILED"
	ErrBackupPublicationFailed    APIErrorCode = "BACKUP_PUBLICATION_FAILED"
	ErrBackupMaintenanceFailed    APIErrorCode = "BACKUP_MAINTENANCE_FAILED"

	// Connection profiles
	ErrProfileNotFound        APIErrorCode = "PROFILE_NOT_FOUND"
	ErrProfileNameExists      APIErrorCode = "PROFILE_NAME_EXISTS"
	ErrProfileInvalidProvider APIErrorCode = "PROFILE_INVALID_PROVIDER"
	ErrProfileURLRequired     APIErrorCode = "PROFILE_URL_REQUIRED"
	ErrImmichSyncUnsupported  APIErrorCode = "IMMICH_SYNC_UNSUPPORTED"
	ErrImmichConflictStrategy APIErrorCode = "IMMICH_CONFLICT_STRATEGY_INVALID"

	// Cloud file manager
	ErrFilesInvalidRef            APIErrorCode = "FILES_INVALID_REF"
	ErrFilesInvalidCursor         APIErrorCode = "FILES_INVALID_CURSOR"
	ErrFilesDirectoryChanged      APIErrorCode = "FILES_DIRECTORY_CHANGED"
	ErrFilesDirectoryTooLarge     APIErrorCode = "FILES_DIRECTORY_TOO_LARGE"
	ErrFilesUnsupportedOperation  APIErrorCode = "FILES_UNSUPPORTED_OPERATION"
	ErrFilesRootMutationForbidden APIErrorCode = "FILES_ROOT_MUTATION_FORBIDDEN"
	ErrFilesDirectoryNotEmpty     APIErrorCode = "FILES_DIRECTORY_NOT_EMPTY"
	ErrFilesConflict              APIErrorCode = "FILES_CONFLICT"
	ErrFilesNotFound              APIErrorCode = "FILES_NOT_FOUND"
	ErrFilesPathAmbiguous         APIErrorCode = "FILES_PATH_AMBIGUOUS"
	ErrFilesUploadLengthRequired  APIErrorCode = "FILES_UPLOAD_LENGTH_REQUIRED"
	ErrFilesUploadSizeMismatch    APIErrorCode = "FILES_UPLOAD_SIZE_MISMATCH"
	ErrFilesStreamLimitReached    APIErrorCode = "FILES_STREAM_LIMIT_REACHED"
	ErrFilesDownloadTicketInvalid APIErrorCode = "FILES_DOWNLOAD_TICKET_INVALID"
	ErrFilesProviderUnavailable   APIErrorCode = "FILES_PROVIDER_UNAVAILABLE"
)

// writeError emits a structured error response carrying only a machine-readable
// code. It deliberately omits any localized message (the frontend translates).
func writeError(w http.ResponseWriter, status int, code APIErrorCode) {
	httpresp.WriteError(w, status, code)
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
	if !strings.ContainsAny(s, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f") {
		return s
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

// getRedirectURI derives the OAuth callback URL from the public host. It is never
// configurable: the provider console must be registered with exactly this value.
func (s *APIServer) getRedirectURI(r *http.Request) string {
	return fmt.Sprintf("%s://%s/api/oauth/callback", s.requestScheme(r), s.publicHost(r))
}

// requestScheme returns the externally visible scheme for the request. TLS
// termination at a reverse proxy is reported via X-Forwarded-Proto. When a
// trusted proxy sits in front of the API it is the TLS endpoint, so the public
// scheme is https unless X-Forwarded-Proto explicitly says otherwise. Honoring
// the header for the scheme (only) is safe because the host is resolved
// separately and gated behind TRUSTED_PROXY, so a spoofed header cannot redirect
// to an attacker-controlled host.
func (s *APIServer) requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if s.trustedProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			if strings.EqualFold(proto, "http") {
				return "http"
			}
			if strings.EqualFold(proto, "https") {
				return "https"
			}
		}
		return "https"
	}
	return "http"
}

// publicHost returns the public host (including any non-default port) for the
// OAuth callback. It trusts the X-Forwarded-Host header only when a trusted
// reverse proxy sits in front of the API, and validates the candidate to
// prevent header/redirect injection. An untrusted or malformed value falls back
// to the request's Host header, which is always safe and port-preserving.
func (s *APIServer) publicHost(r *http.Request) string {
	if s.trustedProxy {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			// Only the first (leftmost) value is authoritative; comma-separated
			// lists may carry client-supplied copies behind a proxy.
			candidate := strings.TrimSpace(fwd)
			if idx := strings.IndexByte(candidate, ','); idx >= 0 {
				candidate = strings.TrimSpace(candidate[:idx])
			}
			if host, ok := validPublicHost(candidate); ok {
				return host
			}
		}
	}
	return r.Host
}

// validPublicHost accepts a host candidate only when it has no path, no control
// characters, and is safe to embed in a URL. It returns the trimmed host.
func validPublicHost(candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	if strings.ContainsAny(candidate, "\r\n\t") {
		return "", false
	}
	u, err := url.Parse("//" + candidate)
	if err != nil || u.Host != candidate || u.Path != "" {
		return "", false
	}
	return u.Host, true
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
