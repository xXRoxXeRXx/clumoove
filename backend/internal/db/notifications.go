package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

var NotificationTypes = map[string]bool{"email": true, "gotify": true, "ntfy": true, "telegram": true, "discord": true}

type NotificationChannel struct {
	Type            string `json:"type"`
	Enabled         bool   `json:"enabled"`
	ConfigEncrypted string `json:"-"`
}
type NotificationDelivery struct {
	ID, ChannelType, ConfigEncrypted, RecipientEmail, Language string
	Payload                                                    json.RawMessage
	Attempts                                                   int
}

func GetNotificationChannels(database *sql.DB, userID string) ([]NotificationChannel, error) {
	rows, err := database.Query(`SELECT type, enabled, config_encrypted FROM notification_channels WHERE user_id = $1 ORDER BY type`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.Type, &c.Enabled, &c.ConfigEncrypted); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetNotificationChannel(database *sql.DB, userID, typ string) (*NotificationChannel, error) {
	var c NotificationChannel
	err := database.QueryRow(`SELECT type, enabled, config_encrypted FROM notification_channels WHERE user_id=$1 AND type=$2`, userID, typ).Scan(&c.Type, &c.Enabled, &c.ConfigEncrypted)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func UpsertNotificationChannel(database *sql.DB, userID, typ string, enabled bool, encrypted string) error {
	_, err := database.Exec(`INSERT INTO notification_channels (user_id,type,enabled,config_encrypted) VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id,type) DO UPDATE SET enabled=EXCLUDED.enabled, config_encrypted=EXCLUDED.config_encrypted, updated_at=CURRENT_TIMESTAMP`, userID, typ, enabled, encrypted)
	return err
}

// createNotificationDeliveries snapshots enabled channels for an event. Email
// defaults to enabled when the instance mailer exists, unless the user has
// explicitly saved an email preference (including an opt-out).
func createNotificationDeliveries(tx *sql.Tx, eventID, userID string) error {
	_, err := tx.Exec(`INSERT INTO notification_deliveries (event_id,channel_type,config_encrypted)
		SELECT $1::uuid, type, CASE WHEN type='email' THEN '' ELSE config_encrypted END
		FROM notification_channels
		WHERE user_id=$2::uuid AND enabled=TRUE
		UNION ALL
		SELECT $1::uuid, 'email', ''
		WHERE EXISTS (SELECT 1 FROM instance_smtp_settings WHERE id=1)
		  AND NOT EXISTS (SELECT 1 FROM notification_channels WHERE user_id=$2::uuid AND type='email')
		ON CONFLICT (event_id,channel_type) DO NOTHING`, eventID, userID)
	return err
}

// CreateMigrationNotificationEvent snapshots active channel configuration.
// The unique migration constraint makes terminal state updates idempotent.
func CreateMigrationNotificationEvent(database *sql.DB, migrationID string) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := createMigrationNotificationEventTx(tx, migrationID); err != nil {
		return err
	}
	return tx.Commit()
}

// createMigrationNotificationEventTx writes the immutable completion event
// using the same transaction that finalized the migration.
func createMigrationNotificationEventTx(tx *sql.Tx, migrationID string) error {
	var userID, status string
	var generation int
	var total, processed, failed, skipped int
	var bytes int64
	var errMsg sql.NullString
	err := tx.QueryRow(`SELECT user_id,status,notification_generation,total_files,processed_files,failed_files,skipped_files,processed_bytes,error_message FROM migrations WHERE id=$1 FOR UPDATE`, migrationID).Scan(&userID, &status, &generation, &total, &processed, &failed, &skipped, &bytes, &errMsg)
	if err != nil {
		return err
	}
	if !isTerminalMigrationStatus(status) {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"kind": "migration", "name": migrationID, "status": status, "total": total, "processed": processed, "failed": failed, "skipped": skipped, "bytes": bytes, "error_message": nullString(errMsg)})
	var eventID string
	err = tx.QueryRow(`INSERT INTO notification_events (user_id,kind,migration_id,run_generation,run_at,payload) VALUES ($1,'migration',$2,$3,CURRENT_TIMESTAMP,$4)
		ON CONFLICT (migration_id,run_generation) WHERE migration_id IS NOT NULL DO NOTHING RETURNING id`, userID, migrationID, generation, payload).Scan(&eventID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err = createNotificationDeliveries(tx, eventID, userID); err != nil {
		return err
	}
	return nil
}

// RepairMissingMigrationNotificationEvents repairs legacy or interrupted
// terminal transitions. Normal paths write state and the event together.
func RepairMissingMigrationNotificationEvents(database *sql.DB, limit int) (int, error) {
	rows, err := database.Query(`SELECT m.id FROM migrations m
		WHERE m.status IN ('COMPLETED','COMPLETED_WITH_ERRORS','FAILED')
		  AND NOT EXISTS (SELECT 1 FROM notification_events e WHERE e.migration_id=m.id AND e.run_generation=m.notification_generation)
		ORDER BY m.updated_at LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := CreateMigrationNotificationEvent(database, id); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// CreateSyncNotificationEvent creates one immutable event per completed pass.
func CreateSyncNotificationEvent(database *sql.DB, syncJobID string) error {
	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID, status string
	var runAt time.Time
	var total, processed, changed, deleted, failed int
	var bytes int64
	var errMsg sql.NullString
	err = tx.QueryRow(`SELECT user_id,last_run_status,last_run_at,total_files,processed_files,changed_files,deleted_files,failed_files,processed_bytes,error_message FROM sync_jobs WHERE id=$1 FOR UPDATE`, syncJobID).Scan(&userID, &status, &runAt, &total, &processed, &changed, &deleted, &failed, &bytes, &errMsg)
	if err != nil {
		return err
	}
	if status == "" {
		return tx.Commit()
	}
	payload, _ := json.Marshal(map[string]any{"kind": "sync", "name": syncJobID, "status": status, "total": total, "processed": processed, "failed": failed, "skipped": 0, "changed": changed, "deleted": deleted, "bytes": bytes, "error_message": nullString(errMsg)})
	var eventID string
	err = tx.QueryRow(`INSERT INTO notification_events (user_id,kind,sync_job_id,run_at,payload) VALUES ($1,'sync',$2,$3,$4) ON CONFLICT (sync_job_id,run_at) DO NOTHING RETURNING id`, userID, syncJobID, runAt, payload).Scan(&eventID)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if err = createNotificationDeliveries(tx, eventID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func nullString(v sql.NullString) any {
	if v.Valid {
		return v.String
	}
	return nil
}

func ClaimNotificationDeliveries(database *sql.DB, limit int) ([]NotificationDelivery, error) {
	_, _ = database.Exec(`UPDATE notification_deliveries
		SET attempts = attempts + 1,
		    state = CASE WHEN attempts + 1 >= 3 THEN 'FAILED' ELSE 'PENDING' END,
		    next_retry_at = CASE WHEN attempts + 1 >= 3 THEN next_retry_at ELSE NOW() END,
		    last_error_code = 'NOTIFICATION_LEASE_EXPIRED', updated_at=CURRENT_TIMESTAMP
		WHERE state='RUNNING' AND updated_at < NOW() - INTERVAL '10 minutes'`)
	rows, err := database.Query(`WITH due AS (SELECT id FROM notification_deliveries WHERE state='PENDING' AND next_retry_at <= NOW() ORDER BY next_retry_at FOR UPDATE SKIP LOCKED LIMIT $1)
		UPDATE notification_deliveries d SET state='RUNNING',updated_at=CURRENT_TIMESTAMP FROM due, notification_events e, users u
		WHERE d.id=due.id AND e.id=d.event_id AND u.id=e.user_id
		RETURNING d.id,d.channel_type,d.config_encrypted,e.payload,u.email,u.language,d.attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationDelivery
	for rows.Next() {
		var d NotificationDelivery
		if err := rows.Scan(&d.ID, &d.ChannelType, &d.ConfigEncrypted, &d.Payload, &d.RecipientEmail, &d.Language, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func CompleteNotificationDelivery(database *sql.DB, id string, sent bool, code string) error {
	if sent {
		_, err := database.Exec(`UPDATE notification_deliveries SET state='SENT',sent_at=CURRENT_TIMESTAMP,last_error_code=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state='RUNNING'`, id)
		return err
	}
	_, err := database.Exec(`UPDATE notification_deliveries SET attempts=attempts+1, state=CASE WHEN attempts+1>=3 THEN 'FAILED' ELSE 'PENDING' END,
		next_retry_at=CASE WHEN attempts+1>=3 THEN next_retry_at ELSE NOW() + (ARRAY[INTERVAL '10 seconds',INTERVAL '30 seconds',INTERVAL '90 seconds'])[attempts+1] END,
		last_error_code=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND state='RUNNING'`, id, code)
	return err
}
