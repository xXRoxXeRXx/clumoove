package db

import (
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
			error_message TEXT
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
