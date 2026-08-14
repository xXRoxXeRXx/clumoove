package db

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestMigrationMarshalJSONProjectsProfileReferences(t *testing.T) {
	payload, err := json.Marshal(Migration{
		SourceProfileID: sql.NullString{String: "source-profile", Valid: true},
		TargetProfileID: sql.NullString{},
	})
	if err != nil {
		t.Fatalf("MarshalJSON() error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if got, want := decoded["source_profile_id"], "source-profile"; got != want {
		t.Errorf("source_profile_id = %#v, want %q", got, want)
	}
	if got := decoded["target_profile_id"]; got != nil {
		t.Errorf("target_profile_id = %#v, want nil", got)
	}
}
