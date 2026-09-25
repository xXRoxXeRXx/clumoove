package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

func TestUpdateNextRunAtIfUnchangedDoesNotOverwriteReschedule(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping schedule conditional-update DB test")
	}

	database, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TEMP TABLE schedules (
			id TEXT PRIMARY KEY,
			next_run_at TIMESTAMP WITH TIME ZONE,
			updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		t.Fatalf("create temp schedules: %v", err)
	}

	t.Run("advances unchanged schedule", func(t *testing.T) {
		initialNextRun := time.Now().UTC().Add(-time.Minute)
		if _, err := database.Exec(`INSERT INTO schedules (id, next_run_at) VALUES ($1, $2)`, "unchanged-schedule", initialNextRun); err != nil {
			t.Fatalf("insert schedule: %v", err)
		}
		var observedNextRun time.Time
		if err := database.QueryRow(`SELECT next_run_at FROM schedules WHERE id = $1`, "unchanged-schedule").Scan(&observedNextRun); err != nil {
			t.Fatalf("read schedule: %v", err)
		}

		wantNextRun := time.Now().UTC().Add(time.Hour)
		advanced, err := UpdateNextRunAtIfUnchangedContext(context.Background(), database, "unchanged-schedule", observedNextRun, wantNextRun)
		if err != nil {
			t.Fatalf("conditionally advance unchanged schedule: %v", err)
		}
		if !advanced {
			t.Fatal("unchanged schedule was not advanced")
		}

		var got time.Time
		if err := database.QueryRow(`SELECT next_run_at FROM schedules WHERE id = $1`, "unchanged-schedule").Scan(&got); err != nil {
			t.Fatalf("read advanced next run: %v", err)
		}
		if !got.Equal(wantNextRun) {
			t.Errorf("next_run_at = %s, want advanced value %s", got, wantNextRun)
		}
	})

	t.Run("does not overwrite reschedule", func(t *testing.T) {
		staleNextRun := time.Now().UTC().Add(-time.Minute)
		if _, err := database.Exec(`INSERT INTO schedules (id, next_run_at) VALUES ($1, $2)`, "rescheduled-schedule", staleNextRun); err != nil {
			t.Fatalf("insert schedule: %v", err)
		}
		var observedNextRun time.Time
		if err := database.QueryRow(`SELECT next_run_at FROM schedules WHERE id = $1`, "rescheduled-schedule").Scan(&observedNextRun); err != nil {
			t.Fatalf("read schedule: %v", err)
		}

		rescheduledNextRun := time.Now().UTC().Add(5 * time.Minute)
		if err := UpdateNextRunAtContext(context.Background(), database, "rescheduled-schedule", rescheduledNextRun); err != nil {
			t.Fatalf("reschedule: %v", err)
		}
		advanced, err := UpdateNextRunAtIfUnchangedContext(context.Background(), database, "rescheduled-schedule", observedNextRun, time.Now().UTC().Add(24*time.Hour))
		if err != nil {
			t.Fatalf("conditionally advance stale schedule: %v", err)
		}
		if advanced {
			t.Fatal("stale schedule advancement overwrote a newer reschedule")
		}

		var got time.Time
		if err := database.QueryRow(`SELECT next_run_at FROM schedules WHERE id = $1`, "rescheduled-schedule").Scan(&got); err != nil {
			t.Fatalf("read rescheduled next run: %v", err)
		}
		if !got.Equal(rescheduledNextRun) {
			t.Errorf("next_run_at = %s, want rescheduled value %s", got, rescheduledNextRun)
		}
	})
}
