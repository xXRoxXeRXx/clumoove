package db

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

// setupProgressTestDB connects to a real PostgreSQL (via DATABASE_URL) and
// creates the minimal schema needed to exercise IncrementMigrationProgress.
// The test is skipped when no DATABASE_URL is configured.
func setupProgressTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping IncrementMigrationProgress DB test")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	schema := []string{
		`CREATE TABLE IF NOT EXISTS migrations (
			id TEXT PRIMARY KEY,
			user_id TEXT,
			status TEXT NOT NULL DEFAULT 'RUNNING',
			total_files INT NOT NULL DEFAULT 0,
			processed_files INT NOT NULL DEFAULT 0,
			failed_files INT NOT NULL DEFAULT 0,
			processed_bytes BIGINT NOT NULL DEFAULT 0,
			skipped_files INT NOT NULL DEFAULT 0,
			error_message TEXT,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id BIGSERIAL PRIMARY KEY,
			user_id TEXT,
			action TEXT,
			target TEXT,
			ip TEXT,
			details JSONB
		)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	if _, err := db.Exec(`DELETE FROM audit_log; DELETE FROM migrations;`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM audit_log; DELETE FROM migrations;`)
		_ = db.Close()
	})
	return db
}

func insertProgressMigration(t *testing.T, db *sql.DB, id, owner string, total, processed, failed int) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO migrations (id, user_id, status, total_files, processed_files, failed_files)
		 VALUES ($1, $2, 'RUNNING', $3, $4, $5)`,
		id, owner, total, processed, failed,
	)
	if err != nil {
		t.Fatalf("insert migration %q: %v", id, err)
	}
}

func migrationStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM migrations WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("get status: %v", err)
	}
	return status
}

func TestIncrementMigrationProgress_Transition(t *testing.T) {
	db := setupProgressTestDB(t)

	t.Run("failed==0 -> COMPLETED", func(t *testing.T) {
		insertProgressMigration(t, db, "m-completed", "u1", 100, 99, 0)
		if err := IncrementMigrationProgress(db, context.Background(), "m-completed", 1, 0, 0, 0); err != nil {
			t.Fatalf("IncrementMigrationProgress: %v", err)
		}
		if got := migrationStatus(t, db, "m-completed"); got != "COMPLETED" {
			t.Errorf("status = %q, want COMPLETED", got)
		}
	})

	t.Run("0<failed<total -> COMPLETED_WITH_ERRORS", func(t *testing.T) {
		insertProgressMigration(t, db, "m-partial", "u1", 100, 99, 20)
		if err := IncrementMigrationProgress(db, context.Background(), "m-partial", 1, 0, 0, 0); err != nil {
			t.Fatalf("IncrementMigrationProgress: %v", err)
		}
		if got := migrationStatus(t, db, "m-partial"); got != "COMPLETED_WITH_ERRORS" {
			t.Errorf("status = %q, want COMPLETED_WITH_ERRORS", got)
		}
	})

	t.Run("failed==total -> FAILED", func(t *testing.T) {
		insertProgressMigration(t, db, "m-failed", "u1", 100, 99, 100)
		if err := IncrementMigrationProgress(db, context.Background(), "m-failed", 1, 0, 0, 0); err != nil {
			t.Fatalf("IncrementMigrationProgress: %v", err)
		}
		if got := migrationStatus(t, db, "m-failed"); got != "FAILED" {
			t.Errorf("status = %q, want FAILED", got)
		}
	})
}

func TestClaimScheduledMigrationForIndexing(t *testing.T) {
	db := setupProgressTestDB(t)

	tests := []struct {
		name        string
		id          string
		initial     string
		insert      bool
		wantClaimed bool
		wantStatus  string
	}{
		{name: "scheduled migration", id: "m-scheduled", initial: "SCHEDULED", insert: true, wantClaimed: true, wantStatus: "INDEXING"},
		{name: "indexing migration cannot be claimed twice", id: "m-indexing", initial: "INDEXING", insert: true, wantStatus: "INDEXING"},
		{name: "running migration is rejected", id: "m-running", initial: "RUNNING", insert: true, wantStatus: "RUNNING"},
		{name: "failed migration is rejected", id: "m-failed", initial: "FAILED", insert: true, wantStatus: "FAILED"},
		{name: "missing migration is rejected", id: "m-missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.insert {
				if _, err := db.Exec(`INSERT INTO migrations (id, user_id, status) VALUES ($1, $2, $3)`, tt.id, "u1", tt.initial); err != nil {
					t.Fatalf("insert migration: %v", err)
				}
			}

			claimed, err := ClaimScheduledMigrationForIndexing(db, tt.id)
			if err != nil {
				t.Fatalf("ClaimScheduledMigrationForIndexing() error = %v", err)
			}
			if claimed != tt.wantClaimed {
				t.Errorf("ClaimScheduledMigrationForIndexing() claimed = %v, want %v", claimed, tt.wantClaimed)
			}
			if tt.insert && migrationStatus(t, db, tt.id) != tt.wantStatus {
				t.Errorf("status = %q, want %q", migrationStatus(t, db, tt.id), tt.wantStatus)
			}
		})
	}
}

func TestClaimSucceeded(t *testing.T) {
	tests := []struct {
		rowsAffected int64
		want         bool
	}{
		{rowsAffected: 0, want: false},
		{rowsAffected: 1, want: true},
		{rowsAffected: 2, want: false},
	}

	for _, tt := range tests {
		if got := claimSucceeded(tt.rowsAffected); got != tt.want {
			t.Errorf("claimSucceeded(%d) = %v, want %v", tt.rowsAffected, got, tt.want)
		}
	}
}

func TestTransitionMigrationIndexingToRunning(t *testing.T) {
	db := setupProgressTestDB(t)

	tests := []struct {
		name       string
		id         string
		initial    string
		insert     bool
		wantErr    bool
		wantStatus string
	}{
		{name: "indexing migration advances", id: "m-indexing-running", initial: "INDEXING", insert: true, wantStatus: "RUNNING"},
		{name: "scheduled migration is rejected", id: "m-scheduled-running", initial: "SCHEDULED", insert: true, wantErr: true, wantStatus: "SCHEDULED"},
		{name: "missing migration is rejected", id: "m-missing-running", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.insert {
				if _, err := db.Exec(`INSERT INTO migrations (id, user_id, status) VALUES ($1, $2, $3)`, tt.id, "u1", tt.initial); err != nil {
					t.Fatalf("insert migration: %v", err)
				}
			}

			err := TransitionMigrationIndexingToRunning(db, tt.id)
			if (err != nil) != tt.wantErr {
				t.Fatalf("TransitionMigrationIndexingToRunning() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.insert && migrationStatus(t, db, tt.id) != tt.wantStatus {
				t.Errorf("status = %q, want %q", migrationStatus(t, db, tt.id), tt.wantStatus)
			}
		})
	}
}
