package db

import (
	"context"
	"database/sql"
	"testing"
)

// newLifecycleMigration inserts one migration with the given status and returns
// the database handle. The migration's owner and notification channel exist so
// terminal transitions can create their outbox rows.
func newLifecycleMigration(t *testing.T, status string) *sql.DB {
	t.Helper()
	database := setupNotificationsTestDB(t)
	if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO notification_channels (user_id, type, enabled, config_encrypted) VALUES ($1, 'gotify', TRUE, 'enc_config')`, testUserUUID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO migrations (id, user_id, status) VALUES ($1, $2, $3)`, testMigrationID, testUserUUID, status); err != nil {
		t.Fatal(err)
	}
	return database
}

func lifecycleStatus(t *testing.T, database *sql.DB) (status string, generation int) {
	t.Helper()
	if err := database.QueryRow(`SELECT status, notification_generation FROM migrations WHERE id = $1`, testMigrationID).Scan(&status, &generation); err != nil {
		t.Fatal(err)
	}
	return status, generation
}

// A worker can finish a migration between the handler's read and its write.
// The transition must be decided by the row's status at write time.
func TestTransitionMigration_PauseDoesNotOverwriteConcurrentCompletion(t *testing.T) {
	database := newLifecycleMigration(t, "COMPLETED")

	applied, err := TransitionMigration(context.Background(), database, testMigrationID, MigrationEventPause, nil)
	if err != nil {
		t.Fatalf("TransitionMigration() error = %v", err)
	}
	if applied {
		t.Fatal("pause of a COMPLETED migration reported applied")
	}
	if status, _ := lifecycleStatus(t, database); status != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", status)
	}
}

func TestTransitionMigration_Pause(t *testing.T) {
	for _, tc := range []struct {
		from        string
		wantApplied bool
		wantStatus  string
	}{
		{"RUNNING", true, "PAUSED"},
		{"INDEXING", true, "PAUSED"},
		{"PENDING", false, "PENDING"},
		{"PAUSED", false, "PAUSED"},
		{"COMPLETED", false, "COMPLETED"},
		{"COMPLETED_WITH_ERRORS", false, "COMPLETED_WITH_ERRORS"},
		{"FAILED", false, "FAILED"},
		{"CANCELLED", false, "CANCELLED"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			database := newLifecycleMigration(t, tc.from)
			applied, err := TransitionMigration(context.Background(), database, testMigrationID, MigrationEventPause, nil)
			if err != nil {
				t.Fatalf("TransitionMigration() error = %v", err)
			}
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			if status, _ := lifecycleStatus(t, database); status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
		})
	}
}

func TestTransitionMigration_Cancel(t *testing.T) {
	for _, tc := range []struct {
		from        string
		wantApplied bool
		wantStatus  string
	}{
		{"PENDING", true, "CANCELLED"},
		{"SCHEDULED", true, "CANCELLED"},
		{"INDEXING", true, "CANCELLED"},
		{"RUNNING", true, "CANCELLED"},
		{"VERIFYING", true, "CANCELLED"},
		{"PAUSED", true, "CANCELLED"},
		{"PAUSED_CONNECTION_LOSS", true, "CANCELLED"},
		{"COMPLETED", false, "COMPLETED"},
		{"COMPLETED_WITH_ERRORS", false, "COMPLETED_WITH_ERRORS"},
		{"FAILED", false, "FAILED"},
		{"CANCELLED", false, "CANCELLED"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			database := newLifecycleMigration(t, tc.from)
			_, generationBefore := lifecycleStatus(t, database)
			applied, err := TransitionMigration(context.Background(), database, testMigrationID, MigrationEventCancel, nil)
			if err != nil {
				t.Fatalf("TransitionMigration() error = %v", err)
			}
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			status, generation := lifecycleStatus(t, database)
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if generation != generationBefore {
				t.Fatalf("notification_generation = %d, want unchanged %d", generation, generationBefore)
			}
		})
	}
}

func TestTransitionMigration_Fail(t *testing.T) {
	for _, tc := range []struct {
		from        string
		wantApplied bool
		wantStatus  string
	}{
		{"PENDING", true, "FAILED"},
		{"SCHEDULED", true, "FAILED"},
		{"INDEXING", true, "FAILED"},
		{"RUNNING", true, "FAILED"},
		{"VERIFYING", true, "FAILED"},
		{"PAUSED", true, "FAILED"},
		{"PAUSED_CONNECTION_LOSS", true, "FAILED"},
		{"COMPLETED", false, "COMPLETED"},
		{"COMPLETED_WITH_ERRORS", false, "COMPLETED_WITH_ERRORS"},
		{"FAILED", false, "FAILED"},
		{"CANCELLED", false, "CANCELLED"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			database := newLifecycleMigration(t, tc.from)
			errMsg := "OAuth token refresh failed (dropbox)"
			applied, err := TransitionMigration(context.Background(), database, testMigrationID, MigrationEventFail, &errMsg)
			if err != nil {
				t.Fatalf("TransitionMigration() error = %v", err)
			}
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			if status, _ := lifecycleStatus(t, database); status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			var events int
			if err := database.QueryRow(`SELECT COUNT(*) FROM notification_events WHERE migration_id = $1`, testMigrationID).Scan(&events); err != nil {
				t.Fatal(err)
			}
			wantEvents := 0
			if tc.wantApplied {
				wantEvents = 1
			}
			if events != wantEvents {
				t.Fatalf("notification events = %d, want %d", events, wantEvents)
			}
			var stored sql.NullString
			if err := database.QueryRow(`SELECT error_message FROM migrations WHERE id = $1`, testMigrationID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if tc.wantApplied && stored.String != errMsg {
				t.Fatalf("error_message = %q, want %q", stored.String, errMsg)
			}
			if !tc.wantApplied && stored.Valid {
				t.Fatalf("error_message = %q, want untouched NULL", stored.String)
			}
		})
	}
}

func TestFailSyncJobIfActive(t *testing.T) {
	for _, tc := range []struct {
		from        string
		wantApplied bool
		wantStatus  string
	}{
		{"INDEXING", true, "FAILED"},
		{"RUNNING", true, "FAILED"},
		{"VERIFYING", true, "FAILED"},
		{"IDLE", false, "IDLE"},
		{"PAUSED", false, "PAUSED"},
		{"FAILED", false, "FAILED"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			database := setupNotificationsTestDB(t)
			if _, err := database.Exec(`INSERT INTO users (id, email) VALUES ($1, 'test@example.com')`, testUserUUID); err != nil {
				t.Fatal(err)
			}
			const syncJobID = "00000000-0000-0000-0000-000000000010"
			if _, err := database.Exec(`INSERT INTO sync_jobs (id, user_id, status) VALUES ($1, $2, $3)`, syncJobID, testUserUUID, tc.from); err != nil {
				t.Fatal(err)
			}
			errMsg := "OAuth token refresh failed (dropbox)"
			applied, err := FailSyncJobIfActive(context.Background(), database, syncJobID, errMsg)
			if err != nil {
				t.Fatalf("FailSyncJobIfActive() error = %v", err)
			}
			if applied != tc.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tc.wantApplied)
			}
			var status string
			var stored sql.NullString
			if err := database.QueryRow(`SELECT status, error_message FROM sync_jobs WHERE id = $1`, syncJobID).Scan(&status, &stored); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if tc.wantApplied && stored.String != errMsg {
				t.Fatalf("error_message = %q, want %q", stored.String, errMsg)
			}
			if !tc.wantApplied && stored.Valid {
				t.Fatalf("error_message = %q, want untouched NULL", stored.String)
			}
		})
	}
}

func TestTransitionMigration_UnknownEventIsAnError(t *testing.T) {
	database := newLifecycleMigration(t, "RUNNING")
	applied, err := TransitionMigration(context.Background(), database, testMigrationID, MigrationEvent("BOGUS"), nil)
	if err == nil || applied {
		t.Fatalf("TransitionMigration(unknown) = (%v, %v), want (false, error)", applied, err)
	}
	if status, _ := lifecycleStatus(t, database); status != "RUNNING" {
		t.Fatalf("status = %q, want RUNNING", status)
	}
}
