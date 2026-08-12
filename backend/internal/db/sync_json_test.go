package db

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func TestSyncJobMarshalJSONFormatsTokenExpiry(t *testing.T) {
	sourceExpiry := time.Date(2026, time.August, 12, 10, 30, 0, 0, time.UTC)
	targetExpiry := sourceExpiry.Add(time.Hour)

	payload, err := json.Marshal(SyncJob{
		SourceTokenExpiresAt: sql.NullTime{Time: sourceExpiry, Valid: true},
		TargetTokenExpiresAt: sql.NullTime{Time: targetExpiry, Valid: true},
	})
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if got, want := decoded["source_token_expires_at"], sourceExpiry.Format(time.RFC3339); got != want {
		t.Errorf("source_token_expires_at = %#v, want %q", got, want)
	}
	if got, want := decoded["target_token_expires_at"], targetExpiry.Format(time.RFC3339); got != want {
		t.Errorf("target_token_expires_at = %#v, want %q", got, want)
	}
}
