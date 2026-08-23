package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func setupNotificationsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping notifications DB test")
	}
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TEMP TABLE users (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			email TEXT,
			language TEXT NOT NULL DEFAULT 'de'
		)`,
		`CREATE TEMP TABLE migrations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			status TEXT NOT NULL,
			notification_generation INT NOT NULL DEFAULT 0,
			total_files INT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			skipped_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			live_bytes BIGINT NOT NULL DEFAULT 0,
			error_message TEXT,
			verification_generation INT NOT NULL DEFAULT 0,
			verification_lease_until TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE sync_jobs (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			source_url TEXT NOT NULL DEFAULT '',
			source_username TEXT NOT NULL DEFAULT '',
			source_password_encrypted TEXT NOT NULL DEFAULT '',
			target_url TEXT NOT NULL DEFAULT '',
			target_username TEXT NOT NULL DEFAULT '',
			target_password_encrypted TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'IDLE',
			run_generation INT NOT NULL DEFAULT 0,
			target_dir TEXT NOT NULL DEFAULT '/',
			last_run_at TIMESTAMPTZ,
			last_run_status TEXT,
			error_message TEXT,
			total_files INT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			changed_files INT NOT NULL DEFAULT 0,
			deleted_files INT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE backup_jobs (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			lock_id BIGSERIAL UNIQUE,
			source_url TEXT NOT NULL DEFAULT '',
			source_username TEXT NOT NULL DEFAULT '',
			source_password_encrypted TEXT NOT NULL DEFAULT '',
			target_url TEXT NOT NULL DEFAULT '',
			target_username TEXT NOT NULL DEFAULT '',
			target_password_encrypted TEXT NOT NULL DEFAULT '',
			source_provider TEXT NOT NULL DEFAULT 'webdav',
			target_provider TEXT NOT NULL DEFAULT 'webdav',
			target_dir TEXT NOT NULL DEFAULT '/',
			repository_root TEXT NOT NULL DEFAULT '/',
			cron_expression TEXT NOT NULL DEFAULT '* * * * *',
			timezone TEXT NOT NULL DEFAULT 'UTC',
			status TEXT NOT NULL DEFAULT 'IDLE',
			run_generation INT NOT NULL DEFAULT 0,
			last_run_at TIMESTAMPTZ,
			last_run_status TEXT,
			error_message TEXT,
			total_files INT NOT NULL DEFAULT 0,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			deduplicated_bytes BIGINT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			error_code TEXT,
			deletion_state TEXT NOT NULL DEFAULT 'ACTIVE',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE backup_runs (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			backup_job_id UUID NOT NULL,
			generation INT NOT NULL DEFAULT 1,
			trigger TEXT NOT NULL DEFAULT 'manual',
			scheduled_local_key TEXT,
			state TEXT NOT NULL DEFAULT 'QUEUED',
			total_files INT NOT NULL DEFAULT 0,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			deduplicated_bytes BIGINT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			error_code TEXT,
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE backup_snapshots (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			backup_job_id UUID NOT NULL,
			backup_run_id UUID NOT NULL,
			state TEXT NOT NULL DEFAULT 'PUBLISHING',
			total_files INT NOT NULL DEFAULT 0,
			total_dirs INT NOT NULL DEFAULT 0,
			total_bytes BIGINT NOT NULL DEFAULT 0,
			omitted_unstable_count INT NOT NULL DEFAULT 0,
			omitted_error_count INT NOT NULL DEFAULT 0,
			integrity_state TEXT NOT NULL DEFAULT 'VALID',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE backup_maintenance (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			backup_job_id UUID NOT NULL,
			kind TEXT NOT NULL
		)`,
		`CREATE TEMP TABLE notification_channels (
			user_id UUID NOT NULL,
			type TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			config_encrypted TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (user_id, type)
		)`,
		`CREATE TEMP TABLE notification_events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL,
			kind TEXT NOT NULL,
			sync_job_id UUID,
			migration_id UUID,
			restore_run_id UUID,
			backup_run_id UUID,
			run_generation INT NOT NULL DEFAULT 0,
			run_at TIMESTAMPTZ NOT NULL,
			payload JSONB NOT NULL,
			UNIQUE (sync_job_id, run_at)
		)`,
		// Partial unique indexes cannot be declared inline on TEMP TABLEs;
		// create them separately. This mirrors the production constraint that
		// CreateMigrationNotificationEvent relies on for ON CONFLICT idempotency.
		`CREATE UNIQUE INDEX notification_events_migration_gen
			ON notification_events (migration_id, run_generation)
			WHERE migration_id IS NOT NULL`,
		`CREATE UNIQUE INDEX notification_events_backup_uniq
			ON notification_events (backup_run_id)
			WHERE backup_run_id IS NOT NULL`,
		`CREATE TEMP TABLE notification_deliveries (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			event_id UUID NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
			channel_type TEXT NOT NULL,
			config_encrypted TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'PENDING',
			attempts INT NOT NULL DEFAULT 0,
			next_retry_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_error_code TEXT,
			sent_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (event_id, channel_type)
		)`,
		`CREATE TEMP TABLE instance_smtp_settings (id INT PRIMARY KEY)`,
		`CREATE TEMP TABLE schedules (
			task_type TEXT NOT NULL,
			task_id UUID NOT NULL,
			is_active BOOLEAN NOT NULL DEFAULT TRUE,
			next_run_at TIMESTAMPTZ
		)`,
		`CREATE TEMP TABLE tasks (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(), migration_id UUID NOT NULL, status TEXT NOT NULL,
			claim_epoch BIGINT NOT NULL DEFAULT 0, file_size BIGINT NOT NULL DEFAULT 0,
			next_retry_at TIMESTAMPTZ, error_message TEXT, checksum_verified BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TEMP TABLE indexing_errors (migration_id UUID NOT NULL)`,
	}
	for _, stmt := range statements {
		if _, err := database.Exec(stmt); err != nil {
			t.Fatalf("setup statement failed: %v\nstmt: %s", err, stmt)
		}
	}
	return database
}

func TestCreateSyncNotificationEvent_SuccessfulRun(t *testing.T) {
	db := setupNotificationsTestDB(t)

	// Create user
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}

	// Enable Gotify channel for user
	if err := UpsertNotificationChannel(db, testUserUUID, "gotify", true, "enc_config"); err != nil {
		t.Fatalf("UpsertNotificationChannel failed: %v", err)
	}

	// Create a completed sync job pass
	syncJobID := "00000000-0000-0000-0000-000000000010"
	runAt := time.Now().Truncate(time.Millisecond)
	if _, err := db.Exec(`INSERT INTO sync_jobs (id, user_id, status, last_run_status, last_run_at, total_files, processed_files, changed_files)
		VALUES ($1, $2, 'IDLE', 'COMPLETED', $3, 10, 10, 2)`, syncJobID, testUserUUID, runAt); err != nil {
		t.Fatal(err)
	}

	// Invoke CreateSyncNotificationEvent
	if err := CreateSyncNotificationEvent(db, syncJobID); err != nil {
		t.Fatalf("CreateSyncNotificationEvent failed for successful run: %v", err)
	}

	// Verify notification event created
	var eventID string
	var payloadStr string
	err := db.QueryRow(`SELECT id, payload::text FROM notification_events WHERE sync_job_id = $1`, syncJobID).Scan(&eventID, &payloadStr)
	if err != nil {
		t.Fatalf("expected notification event created, got error: %v", err)
	}
	if eventID == "" {
		t.Fatal("expected non-empty eventID")
	}

	// Verify delivery entry created for gotify
	var count int
	var channelType string
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(d.channel_type), '') FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.sync_job_id = $1`, syncJobID).Scan(&count, &channelType)
	if err != nil {
		t.Fatalf("error querying notification_deliveries: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 delivery entry, got %d", count)
	}
	if channelType != "gotify" {
		t.Fatalf("expected channel_type 'gotify', got %q", channelType)
	}
}

func TestCreateSyncNotificationEvent_FailedRun(t *testing.T) {
	db := setupNotificationsTestDB(t)

	// Create user
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}

	// Enable Telegram channel for user
	if err := UpsertNotificationChannel(db, testUserUUID, "telegram", true, "enc_telegram_config"); err != nil {
		t.Fatalf("UpsertNotificationChannel failed: %v", err)
	}

	// Create a failed sync job pass
	syncJobID := "00000000-0000-0000-0000-000000000011"
	runAt := time.Now().Truncate(time.Millisecond)
	if _, err := db.Exec(`INSERT INTO sync_jobs (id, user_id, status, last_run_status, last_run_at, total_files, processed_files, failed_files, error_message)
		VALUES ($1, $2, 'FAILED', 'FAILED', $3, 5, 2, 3, 'Network connection reset')`, syncJobID, testUserUUID, runAt); err != nil {
		t.Fatal(err)
	}

	// Invoke CreateSyncNotificationEvent
	if err := CreateSyncNotificationEvent(db, syncJobID); err != nil {
		t.Fatalf("CreateSyncNotificationEvent failed for failed run: %v", err)
	}

	// Verify notification event created with correct payload fields
	var eventID string
	var payloadStr string
	err := db.QueryRow(`SELECT id, payload::text FROM notification_events WHERE sync_job_id = $1`, syncJobID).Scan(&eventID, &payloadStr)
	if err != nil {
		t.Fatalf("expected notification event created for failed run: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["status"] != "FAILED" {
		t.Errorf("payload status = %v, want FAILED", payload["status"])
	}
	if payload["error_message"] != "Network connection reset" {
		t.Errorf("payload error_message = %v, want 'Network connection reset'", payload["error_message"])
	}

	// Verify delivery entry created for telegram
	var count int
	var channelType string
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(d.channel_type), '') FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.sync_job_id = $1`, syncJobID).Scan(&count, &channelType)
	if err != nil {
		t.Fatalf("error querying notification_deliveries: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 delivery entry, got %d", count)
	}
	if channelType != "telegram" {
		t.Fatalf("expected channel_type 'telegram', got %q", channelType)
	}
}

func TestCreateSyncNotificationEvent_Idempotency(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "discord", true, "enc_discord_config"); err != nil {
		t.Fatal(err)
	}

	syncJobID := "00000000-0000-0000-0000-000000000012"
	runAt := time.Now().Truncate(time.Millisecond)
	if _, err := db.Exec(`INSERT INTO sync_jobs (id, user_id, status, last_run_status, last_run_at)
		VALUES ($1, $2, 'IDLE', 'COMPLETED', $3)`, syncJobID, testUserUUID, runAt); err != nil {
		t.Fatal(err)
	}

	// First call
	if err := CreateSyncNotificationEvent(db, syncJobID); err != nil {
		t.Fatalf("first CreateSyncNotificationEvent call failed: %v", err)
	}

	// Second call (same run_at) — must be idempotent
	if err := CreateSyncNotificationEvent(db, syncJobID); err != nil {
		t.Fatalf("second CreateSyncNotificationEvent call failed: %v", err)
	}

	// Verify only 1 event and 1 delivery exist
	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE sync_job_id = $1`, syncJobID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly 1 notification_event, got %d", eventCount)
	}

	var deliveryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.sync_job_id = $1`, syncJobID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 {
		t.Fatalf("expected exactly 1 notification_delivery, got %d", deliveryCount)
	}
}

// TestCreateMigrationNotificationEvent_Idempotency verifies that calling
// CreateMigrationNotificationEvent twice for the same terminal state (same
// notification_generation) produces exactly one event and one delivery row,
// exercising the partial unique index on (migration_id, run_generation).
func TestCreateMigrationNotificationEvent_Idempotency(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "gotify", true, "enc_config"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migrations (id, user_id, status, notification_generation) VALUES ($1, $2, 'COMPLETED', 1)`, testMigrationID, testUserUUID); err != nil {
		t.Fatal(err)
	}

	// First call
	if err := CreateMigrationNotificationEvent(db, testMigrationID); err != nil {
		t.Fatalf("first CreateMigrationNotificationEvent call failed: %v", err)
	}

	// Second call — must not insert a duplicate event (same run_generation)
	if err := CreateMigrationNotificationEvent(db, testMigrationID); err != nil {
		t.Fatalf("second CreateMigrationNotificationEvent call failed: %v", err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE migration_id = $1`, testMigrationID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly 1 notification_event, got %d", eventCount)
	}

	var deliveryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.migration_id = $1`, testMigrationID).Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 {
		t.Fatalf("expected exactly 1 notification_delivery, got %d", deliveryCount)
	}
}

func TestFailMigrationForAuthentication_FinalizesCountersBeforeOutbox(t *testing.T) {
	db := setupNotificationsTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "gotify", true, "enc"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migrations (id,user_id,status,total_files) VALUES ($1,$2,'RUNNING',3)`, testMigrationID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id,migration_id,status,claim_epoch,file_size) VALUES
		($1,$2,'RUNNING',7,10), ('00000000-0000-0000-0000-000000000004',$2,'PENDING',0,20), ('00000000-0000-0000-0000-000000000005',$2,'COMPLETED',0,30)`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}

	finalized, err := FailMigrationForAuthentication(db, context.Background(), &Task{ID: testTaskID, MigrationID: testMigrationID, ClaimEpoch: 7}, "authentication failed")
	if err != nil || !finalized {
		t.Fatalf("FailMigrationForAuthentication() = (%v, %v)", finalized, err)
	}
	var payloadText string
	if err := db.QueryRow(`SELECT payload::text FROM notification_events WHERE migration_id=$1`, testMigrationID).Scan(&payloadText); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["processed"] != float64(3) || payload["failed"] != float64(2) || payload["bytes"] != float64(30) {
		t.Fatalf("payload has stale final counters: %s", payloadText)
	}
}

func TestFailMigrationForAuthentication_DoesNotOverwriteCancellation(t *testing.T) {
	db := setupNotificationsTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migrations (id,user_id,status) VALUES ($1,$2,'CANCELLED')`, testMigrationID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id,migration_id,status,claim_epoch) VALUES ($1,$2,'RUNNING',7)`, testTaskID, testMigrationID); err != nil {
		t.Fatal(err)
	}
	finalized, err := FailMigrationForAuthentication(db, context.Background(), &Task{ID: testTaskID, MigrationID: testMigrationID, ClaimEpoch: 7}, "authentication failed")
	if err != nil || finalized {
		t.Fatalf("FailMigrationForAuthentication() = (%v, %v), want (false, nil)", finalized, err)
	}
	var migrationStatus, taskStatus string
	if err := db.QueryRow(`SELECT status FROM migrations WHERE id=$1`, testMigrationID).Scan(&migrationStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id=$1`, testTaskID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if migrationStatus != "CANCELLED" || taskStatus != "RUNNING" {
		t.Fatalf("rollback failed: migration=%s task=%s", migrationStatus, taskStatus)
	}
}

func TestReconcileMigrationProgress_TerminalStateIsSticky(t *testing.T) {
	db := setupNotificationsTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migrations (id,user_id,status) VALUES ($1,$2,'FAILED')`, testMigrationID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (migration_id,status,file_size,checksum_verified) VALUES ($1,'COMPLETED',10,FALSE)`, testMigrationID); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileMigrationProgress(db, testMigrationID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM migrations WHERE id=$1`, testMigrationID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "FAILED" {
		t.Fatalf("status=%s, want FAILED", status)
	}
}

func TestRepairMissingMigrationNotificationEvents(t *testing.T) {
	db := setupNotificationsTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO migrations (id,user_id,status,notification_generation) VALUES ($1,$2,'COMPLETED',4)`, testMigrationID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	count, err := RepairMissingMigrationNotificationEvents(db, 10)
	if err != nil || count != 1 {
		t.Fatalf("RepairMissingMigrationNotificationEvents() = (%d, %v)", count, err)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE migration_id=$1 AND run_generation=4`, testMigrationID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("events = %d, want 1", events)
	}
}

func TestFinalizeAndFailSyncJobPass_NotificationEvents(t *testing.T) {
	// Each sub-test gets its own isolated DB connection and temp tables so
	// state from one scenario cannot bleed into the other.
	t.Run("FinalizeSyncJobPass", func(t *testing.T) {
		db := setupNotificationsTestDB(t)

		if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
			t.Fatal(err)
		}
		if err := UpsertNotificationChannel(db, testUserUUID, "ntfy", true, "enc_ntfy_config"); err != nil {
			t.Fatal(err)
		}

		syncJob1 := "00000000-0000-0000-0000-000000000021"
		if _, err := db.Exec(`INSERT INTO sync_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'RUNNING', 1)`, syncJob1, testUserUUID); err != nil {
			t.Fatal(err)
		}

		finalized, err := FinalizeSyncJobPass(db, syncJob1, 1, "COMPLETED", nil, 5, 5, 1, 0, 0)
		if err != nil || !finalized {
			t.Fatalf("FinalizeSyncJobPass failed = (%v, %v), want (true, nil)", finalized, err)
		}

		var eventCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE sync_job_id = $1`, syncJob1).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		if eventCount != 1 {
			t.Fatalf("expected 1 notification_event after FinalizeSyncJobPass, got %d", eventCount)
		}

		var deliveryCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.sync_job_id = $1`, syncJob1).Scan(&deliveryCount); err != nil {
			t.Fatal(err)
		}
		if deliveryCount != 1 {
			t.Fatalf("expected 1 notification_delivery after FinalizeSyncJobPass, got %d", deliveryCount)
		}
	})

	t.Run("FailSyncJobPass", func(t *testing.T) {
		db := setupNotificationsTestDB(t)

		if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
			t.Fatal(err)
		}
		if err := UpsertNotificationChannel(db, testUserUUID, "ntfy", true, "enc_ntfy_config"); err != nil {
			t.Fatal(err)
		}

		syncJob2 := "00000000-0000-0000-0000-000000000022"
		if _, err := db.Exec(`INSERT INTO sync_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'RUNNING', 1)`, syncJob2, testUserUUID); err != nil {
			t.Fatal(err)
		}

		failed, err := FailSyncJobPass(db, syncJob2, 1, "disk quota exceeded")
		if err != nil || !failed {
			t.Fatalf("FailSyncJobPass failed = (%v, %v), want (true, nil)", failed, err)
		}

		var eventCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE sync_job_id = $1`, syncJob2).Scan(&eventCount); err != nil {
			t.Fatal(err)
		}
		if eventCount != 1 {
			t.Fatalf("expected 1 notification_event after FailSyncJobPass, got %d", eventCount)
		}

		var deliveryCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries d JOIN notification_events e ON d.event_id = e.id WHERE e.sync_job_id = $1`, syncJob2).Scan(&deliveryCount); err != nil {
			t.Fatal(err)
		}
		if deliveryCount != 1 {
			t.Fatalf("expected 1 notification_delivery after FailSyncJobPass, got %d", deliveryCount)
		}
	})
}

func TestCreateBackupNotificationEvent_SuccessfulRun(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'backup-user@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "gotify", true, "enc_gotify"); err != nil {
		t.Fatalf("UpsertNotificationChannel failed: %v", err)
	}

	backupJobID := "00000000-0000-0000-0000-000000000030"
	backupRunID := "00000000-0000-0000-0000-000000000031"
	if _, err := db.Exec(`INSERT INTO backup_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'IDLE', 1)`, backupJobID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs (id, backup_job_id, generation, state, total_files, total_bytes, processed_files, processed_bytes, deduplicated_bytes, failed_files)
		VALUES ($1, $2, 1, 'COMPLETED', 100, 1048576, 100, 1048576, 524288, 0)`, backupRunID, backupJobID); err != nil {
		t.Fatal(err)
	}

	if err := CreateBackupNotificationEvent(db, backupRunID); err != nil {
		t.Fatalf("CreateBackupNotificationEvent failed: %v", err)
	}

	var eventID string
	var payloadStr string
	err := db.QueryRow(`SELECT id, payload::text FROM notification_events WHERE backup_run_id = $1`, backupRunID).Scan(&eventID, &payloadStr)
	if err != nil {
		t.Fatalf("expected notification event created for backup run, got: %v", err)
	}
	if eventID == "" {
		t.Fatal("expected non-empty eventID")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["kind"] != "backup" {
		t.Errorf("payload kind = %v, want backup", payload["kind"])
	}
	if payload["status"] != "COMPLETED" {
		t.Errorf("payload status = %v, want COMPLETED", payload["status"])
	}
	if payload["processed"] != float64(100) || payload["total"] != float64(100) {
		t.Errorf("payload counts = processed:%v total:%v", payload["processed"], payload["total"])
	}

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE event_id = $1`, eventID).Scan(&count)
	if err != nil {
		t.Fatalf("error querying deliveries: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 delivery entry, got %d", count)
	}
}

func TestCreateBackupNotificationEvent_FailedRun(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'backup-user@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "discord", true, "enc_discord"); err != nil {
		t.Fatalf("UpsertNotificationChannel failed: %v", err)
	}

	backupJobID := "00000000-0000-0000-0000-000000000032"
	backupRunID := "00000000-0000-0000-0000-000000000033"
	if _, err := db.Exec(`INSERT INTO backup_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'FAILED', 2)`, backupJobID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs (id, backup_job_id, generation, state, total_files, processed_files, failed_files, error_code)
		VALUES ($1, $2, 2, 'FAILED', 50, 10, 5, 'BACKUP_TARGET_UNAVAILABLE')`, backupRunID, backupJobID); err != nil {
		t.Fatal(err)
	}

	if err := CreateBackupNotificationEvent(db, backupRunID); err != nil {
		t.Fatalf("CreateBackupNotificationEvent failed: %v", err)
	}

	var payloadStr string
	err := db.QueryRow(`SELECT payload::text FROM notification_events WHERE backup_run_id = $1`, backupRunID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("query event: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["status"] != "FAILED" {
		t.Errorf("status = %v, want FAILED", payload["status"])
	}
	if payload["error_message"] != "BACKUP_TARGET_UNAVAILABLE" {
		t.Errorf("error_message = %v, want BACKUP_TARGET_UNAVAILABLE", payload["error_message"])
	}
}

func TestCreateBackupNotificationEvent_Idempotency(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'backup-user@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "telegram", true, "enc_tg"); err != nil {
		t.Fatal(err)
	}

	backupJobID := "00000000-0000-0000-0000-000000000034"
	backupRunID := "00000000-0000-0000-0000-000000000035"
	if _, err := db.Exec(`INSERT INTO backup_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'IDLE', 1)`, backupJobID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs (id, backup_job_id, generation, state) VALUES ($1, $2, 1, 'COMPLETED')`, backupRunID, backupJobID); err != nil {
		t.Fatal(err)
	}

	if err := CreateBackupNotificationEvent(db, backupRunID); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if err := CreateBackupNotificationEvent(db, backupRunID); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE backup_run_id = $1`, backupRunID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 event, got %d", eventCount)
	}
}

func TestPublishBackupSnapshotAndFinalizeContext_CreatesNotification(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'backup-user@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "gotify", true, "enc_gotify"); err != nil {
		t.Fatal(err)
	}

	jobID := "00000000-0000-0000-0000-000000000036"
	runID := "00000000-0000-0000-0000-000000000037"
	snapID := "00000000-0000-0000-0000-000000000038"

	if _, err := db.Exec(`INSERT INTO backup_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'VERIFYING', 1)`, jobID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs (id, backup_job_id, generation, state) VALUES ($1, $2, 1, 'VERIFYING')`, runID, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_snapshots (id, backup_job_id, backup_run_id, state) VALUES ($1, $2, $3, 'PUBLISHING')`, snapID, jobID, runID); err != nil {
		t.Fatal(err)
	}

	ok, err := PublishBackupSnapshotAndFinalizeContext(context.Background(), db, jobID, 1, runID, snapID, "READY", "COMPLETED", 10, 2, 1024, 10, 1024, 0, 0, 0)
	if err != nil || !ok {
		t.Fatalf("PublishBackupSnapshotAndFinalizeContext = (%v, %v), want (true, nil)", ok, err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE backup_run_id = $1`, runID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 notification_event after PublishBackupSnapshotAndFinalizeContext, got %d", eventCount)
	}
}

func TestFailBackupRunContext_CreatesNotification(t *testing.T) {
	db := setupNotificationsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ($1, 'backup-user@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if err := UpsertNotificationChannel(db, testUserUUID, "ntfy", true, "enc_ntfy"); err != nil {
		t.Fatal(err)
	}

	jobID := "00000000-0000-0000-0000-000000000039"
	runID := "00000000-0000-0000-0000-000000000040"

	if _, err := db.Exec(`INSERT INTO backup_jobs (id, user_id, status, run_generation) VALUES ($1, $2, 'RUNNING', 1)`, jobID, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs (id, backup_job_id, generation, state) VALUES ($1, $2, 1, 'RUNNING')`, runID, jobID); err != nil {
		t.Fatal(err)
	}

	ok, err := FailBackupRunContext(context.Background(), db, jobID, 1, runID, "RUNNING", "BACKUP_SCAN_FAILED")
	if err != nil || !ok {
		t.Fatalf("FailBackupRunContext = (%v, %v), want (true, nil)", ok, err)
	}

	var eventCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE backup_run_id = $1`, runID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 notification_event after FailBackupRunContext, got %d", eventCount)
	}
}
