package db

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestBackupJobMarshalJSONProjectsNullableFields(t *testing.T) {
	runAt := time.Date(2026, time.August, 21, 13, 24, 6, 0, time.UTC)
	srcExpiry := time.Date(2026, time.August, 22, 10, 0, 0, 0, time.UTC)
	tgtExpiry := time.Date(2026, time.August, 22, 11, 0, 0, 0, time.UTC)

	jobWithValues := BackupJob{
		ID:                   "job-1",
		SourceProfileID:      sql.NullString{String: "prof-src", Valid: true},
		TargetProfileID:      sql.NullString{String: "prof-tgt", Valid: true},
		SourceTokenExpiresAt: sql.NullTime{Time: srcExpiry, Valid: true},
		TargetTokenExpiresAt: sql.NullTime{Time: tgtExpiry, Valid: true},
		LastRunAt:            sql.NullTime{Time: runAt, Valid: true},
		LastRunStatus:        sql.NullString{String: "COMPLETED", Valid: true},
		ErrorCode:            sql.NullString{String: "BACKUP_CONNECTION_FAILED", Valid: true},
	}

	payload, err := json.Marshal(jobWithValues)
	if err != nil {
		t.Fatalf("Marshal(jobWithValues) error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if got, want := decoded["source_profile_id"], "prof-src"; got != want {
		t.Errorf("source_profile_id = %#v, want %q", got, want)
	}
	if got, want := decoded["target_profile_id"], "prof-tgt"; got != want {
		t.Errorf("target_profile_id = %#v, want %q", got, want)
	}
	if got, want := decoded["source_token_expires_at"], srcExpiry.Format(time.RFC3339); got != want {
		t.Errorf("source_token_expires_at = %#v, want %q", got, want)
	}
	if got, want := decoded["target_token_expires_at"], tgtExpiry.Format(time.RFC3339); got != want {
		t.Errorf("target_token_expires_at = %#v, want %q", got, want)
	}
	if got, want := decoded["last_run_at"], runAt.Format(time.RFC3339); got != want {
		t.Errorf("last_run_at = %#v, want %q", got, want)
	}
	if got, want := decoded["last_run_status"], "COMPLETED"; got != want {
		t.Errorf("last_run_status = %#v, want %q", got, want)
	}
	if got, want := decoded["error_code"], "BACKUP_CONNECTION_FAILED"; got != want {
		t.Errorf("error_code = %#v, want %q", got, want)
	}

	// Test null/empty values serialize as nil / omitted and not raw struct objects
	jobNull := BackupJob{
		ID: "job-2",
	}

	payloadNull, err := json.Marshal(jobNull)
	if err != nil {
		t.Fatalf("Marshal(jobNull) error: %v", err)
	}

	var decodedNull map[string]any
	if err := json.Unmarshal(payloadNull, &decodedNull); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	for _, key := range []string{"source_profile_id", "target_profile_id", "source_token_expires_at", "target_token_expires_at", "last_run_at", "last_run_status", "error_code"} {
		if val, exists := decodedNull[key]; exists && val != nil {
			t.Errorf("key %q = %#v, want nil/missing", key, val)
		}
	}
}

func TestBackupRunMarshalJSONProjectsNullableFields(t *testing.T) {
	startedAt := time.Date(2026, time.August, 21, 13, 20, 23, 0, time.UTC)
	finishedAt := time.Date(2026, time.August, 21, 13, 24, 6, 0, time.UTC)

	runWithValues := BackupRun{
		ID:                "run-1",
		ScheduledLocalKey: sql.NullString{String: "2026-08-21T02:00:00", Valid: true},
		ErrorCode:         sql.NullString{String: "BACKUP_RUN_FAILED", Valid: true},
		StartedAt:         sql.NullTime{Time: startedAt, Valid: true},
		FinishedAt:        sql.NullTime{Time: finishedAt, Valid: true},
	}

	payload, err := json.Marshal(runWithValues)
	if err != nil {
		t.Fatalf("Marshal(runWithValues) error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if got, want := decoded["scheduled_local_key"], "2026-08-21T02:00:00"; got != want {
		t.Errorf("scheduled_local_key = %#v, want %q", got, want)
	}
	if got, want := decoded["error_code"], "BACKUP_RUN_FAILED"; got != want {
		t.Errorf("error_code = %#v, want %q", got, want)
	}
	if got, want := decoded["started_at"], startedAt.Format(time.RFC3339); got != want {
		t.Errorf("started_at = %#v, want %q", got, want)
	}
	if got, want := decoded["finished_at"], finishedAt.Format(time.RFC3339); got != want {
		t.Errorf("finished_at = %#v, want %q", got, want)
	}

	runNull := BackupRun{
		ID: "run-2",
	}

	payloadNull, err := json.Marshal(runNull)
	if err != nil {
		t.Fatalf("Marshal(runNull) error: %v", err)
	}

	var decodedNull map[string]any
	if err := json.Unmarshal(payloadNull, &decodedNull); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	for _, key := range []string{"scheduled_local_key", "error_code", "started_at", "finished_at"} {
		if val, exists := decodedNull[key]; exists && val != nil {
			t.Errorf("key %q = %#v, want nil/missing", key, val)
		}
	}
}
