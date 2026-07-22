package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

type AuditAction string

const (
	AuditLoginSuccess       AuditAction = "LOGIN_SUCCESS"
	AuditLoginFailed        AuditAction = "LOGIN_FAILED"
	AuditRegistration       AuditAction = "REGISTRATION"
	AuditUserCreated        AuditAction = "USER_CREATED"
	AuditMigrationCreated   AuditAction = "MIGRATION_CREATED"
	AuditMigrationStarted   AuditAction = "MIGRATION_STARTED"
	AuditMigrationCompleted AuditAction = "MIGRATION_COMPLETED"
	AuditMigrationFailed    AuditAction = "MIGRATION_FAILED"
	AuditMigrationPaused    AuditAction = "MIGRATION_PAUSED"
	AuditMigrationResumed   AuditAction = "MIGRATION_RESUMED"
	AuditMigrationCancelled AuditAction = "MIGRATION_CANCELLED"
	AuditMigrationDeleted   AuditAction = "MIGRATION_DELETED"
	AuditSettingUpdated     AuditAction = "SETTING_UPDATED"
	AuditUserSuspended      AuditAction = "USER_SUSPENDED"
	AuditUserReactivated    AuditAction = "USER_REACTIVATED"
	AuditUserDeleted        AuditAction = "USER_DELETED"
	AuditUserRoleChanged    AuditAction = "USER_ROLE_CHANGED"
	Audit2FAEnabled         AuditAction = "2FA_ENABLED"
	Audit2FADisabled        AuditAction = "2FA_DISABLED"
	AuditSyncCreated        AuditAction = "SYNC_CREATED"
	AuditSyncStarted        AuditAction = "SYNC_STARTED"
	AuditSyncCompleted      AuditAction = "SYNC_COMPLETED"
	AuditSyncFailed         AuditAction = "SYNC_FAILED"
	AuditSyncPaused         AuditAction = "SYNC_PAUSED"
	AuditSyncResumed        AuditAction = "SYNC_RESUMED"
	AuditSyncDeleted        AuditAction = "SYNC_DELETED"
)

type AuditEntry struct {
	UserID  sql.NullString
	Action  AuditAction
	Target  string
	IP      string
	Details json.RawMessage
}

type AuditLogRow struct {
	ID        int64           `json:"id"`
	UserID    string          `json:"user_id"`
	Action    AuditAction     `json:"action"`
	Target    string          `json:"target"`
	IP        string          `json:"ip"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

type AuditLogParams struct {
	Page   int
	Limit  int
	Action string
	UserID string
	Target string
	From   string
	To     string
}

func WriteAuditLog(database queryExecer, e AuditEntry) {
	if database == nil {
		return
	}
	var details interface{}
	if len(e.Details) > 0 {
		details = e.Details
	}
	query := `
		INSERT INTO audit_log (user_id, action, target, ip, details)
		VALUES ($1, $2, $3, $4, $5)
	`
	if _, err := database.Exec(query, e.UserID, string(e.Action), e.Target, e.IP, details); err != nil {
		log.Printf("WARNING: failed to write audit log (action=%s target=%s): %v", e.Action, e.Target, err)
	}
}

func ListAuditLog(database *sql.DB, p AuditLogParams) ([]AuditLogRow, int, error) {
	where := "TRUE"
	args := []interface{}{}
	idx := 1
	if p.Action != "" {
		where += fmt.Sprintf(" AND action = $%d", idx)
		args = append(args, p.Action)
		idx++
	}
	if p.UserID != "" {
		where += fmt.Sprintf(" AND user_id = $%d", idx)
		args = append(args, p.UserID)
		idx++
	}
	if p.Target != "" {
		where += fmt.Sprintf(" AND target = $%d", idx)
		args = append(args, p.Target)
		idx++
	}
	if p.From != "" {
		ft, ferr := parseAuditTime(p.From)
		if ferr != nil {
			return nil, 0, ferr
		}
		where += fmt.Sprintf(" AND created_at >= $%d", idx)
		args = append(args, ft)
		idx++
	}
	if p.To != "" {
		tt, terr := parseAuditTime(p.To)
		if terr != nil {
			return nil, 0, terr
		}
		where += fmt.Sprintf(" AND created_at <= $%d", idx)
		args = append(args, tt)
		idx++
	}

	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	offset := (p.Page - 1) * p.Limit
	if offset < 0 {
		offset = 0
	}
	listArgs := append(append([]interface{}{}, args...), p.Limit, offset)
	query := `
		SELECT id, user_id, action, target, ip, details, created_at
		FROM audit_log WHERE ` + where + `
		ORDER BY created_at DESC
		LIMIT $` + fmt.Sprintf("%d", idx) + ` OFFSET $` + fmt.Sprintf("%d", idx+1)
	rows, err := database.Query(query, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	entries := []AuditLogRow{}
	for rows.Next() {
		var e AuditLogRow
		var uid sql.NullString
		var details []byte
		if err := rows.Scan(&e.ID, &uid, &e.Action, &e.Target, &e.IP, &details, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		if uid.Valid {
			e.UserID = uid.String
		}
		if len(details) > 0 {
			e.Details = details
		} else {
			e.Details = json.RawMessage("null")
		}
		entries = append(entries, e)
	}
	return entries, total, nil
}

func parseAuditTime(s string) (string, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid time filter value %q", s)
}
