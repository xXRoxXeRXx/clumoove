package db

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The expected values below are written out per status so the test is an
// independent statement of the lifecycle rules, not a re-derivation from the
// transition tables.
func TestAllowedMigrationActions(t *testing.T) {
	cases := map[string][]JobAction{
		"PENDING":                {JobActionCancel},
		"SCHEDULED":              {JobActionCancel},
		"INDEXING":               {JobActionPause, JobActionCancel},
		"RUNNING":                {JobActionPause, JobActionCancel},
		"VERIFYING":              {JobActionCancel},
		"PAUSED":                 {JobActionResume, JobActionCancel},
		"PAUSED_CONNECTION_LOSS": {JobActionResume, JobActionCancel},
		"COMPLETED":              {},
		"COMPLETED_WITH_ERRORS":  {},
		"FAILED":                 {},
		"CANCELLED":              {},
		"UNKNOWN":                {},
	}
	for status, want := range cases {
		if got := AllowedMigrationActions(status); !reflect.DeepEqual(got, want) {
			t.Errorf("AllowedMigrationActions(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestAllowedSyncActions(t *testing.T) {
	cases := map[string][]JobAction{
		"IDLE":      {JobActionStart, JobActionPause},
		"FAILED":    {JobActionStart},
		"INDEXING":  {JobActionPause},
		"RUNNING":   {JobActionPause},
		"VERIFYING": {JobActionPause},
		"PAUSED":    {JobActionResume},
		// Deliberate: PAUSED_CONNECTION_LOSS is owned by the worker's connection
		// recovery, which releases the job to IDLE and makes its schedule due.
		// A user cannot pause or resume it, and the API treats the job as an
		// active pass so a manual start cannot overlap the recovery.
		"PAUSED_CONNECTION_LOSS": {},
		"COMPLETED":              {},
		"UNKNOWN":                {},
	}
	for status, want := range cases {
		if got := AllowedSyncActions(status); !reflect.DeepEqual(got, want) {
			t.Errorf("AllowedSyncActions(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestAllowedBackupActions(t *testing.T) {
	cases := map[string][]JobAction{
		"IDLE":      {JobActionStart, JobActionPause},
		"FAILED":    {JobActionStart, JobActionPause},
		"QUEUED":    {JobActionPause},
		"SCANNING":  {JobActionPause},
		"RUNNING":   {JobActionPause},
		"VERIFYING": {JobActionPause},
		"PAUSED":    {JobActionResume},
		"DELETING":  {},
		"UNKNOWN":   {},
	}
	for status, want := range cases {
		if got := AllowedBackupActions(status); !reflect.DeepEqual(got, want) {
			t.Errorf("AllowedBackupActions(%q) = %v, want %v", status, got, want)
		}
	}
}

// The SQL guards of the user-facing transitions read the same sources the
// allowed actions are computed from, so they cannot disagree.
func TestActionSourcesAgreeWithAllowedActions(t *testing.T) {
	statuses := []string{"PENDING", "SCHEDULED", "IDLE", "QUEUED", "SCANNING", "INDEXING", "RUNNING", "VERIFYING", "PAUSED", "PAUSED_CONNECTION_LOSS", "COMPLETED", "COMPLETED_WITH_ERRORS", "FAILED", "CANCELLED", "DELETING"}
	kinds := []struct {
		name    string
		sources func(JobAction) []string
		allowed func(string) []JobAction
	}{
		{"migration", func(a JobAction) []string { return migrationActionSources[a] }, AllowedMigrationActions},
		{"sync", func(a JobAction) []string { return syncActionSources[a] }, AllowedSyncActions},
		{"backup", BackupActionSources, AllowedBackupActions},
	}
	for _, kind := range kinds {
		for _, status := range statuses {
			allowed := map[JobAction]bool{}
			for _, action := range kind.allowed(status) {
				allowed[action] = true
			}
			for _, action := range []JobAction{JobActionStart, JobActionPause, JobActionResume, JobActionCancel} {
				inSources := false
				for _, source := range kind.sources(action) {
					if source == status {
						inSources = true
					}
				}
				if inSources != allowed[action] {
					t.Errorf("%s %q %s: in sources = %v, allowed = %v", kind.name, status, action, inSources, allowed[action])
				}
			}
		}
	}
}

func TestJobJSONIncludesAllowedActions(t *testing.T) {
	decode := func(v any) []string {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			AllowedActions *[]string `json:"allowed_actions"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.AllowedActions == nil {
			t.Fatalf("allowed_actions missing from %s", raw)
		}
		return *out.AllowedActions
	}

	if got := decode(Migration{Status: "PAUSED_CONNECTION_LOSS"}); !reflect.DeepEqual(got, []string{"resume", "cancel"}) {
		t.Errorf("migration allowed_actions = %v", got)
	}
	if got := decode(SyncJob{Status: "PAUSED_CONNECTION_LOSS"}); len(got) != 0 {
		t.Errorf("sync allowed_actions = %v, want empty array", got)
	}
	if got := decode(SyncJob{Status: "IDLE"}); !reflect.DeepEqual(got, []string{"start", "pause"}) {
		t.Errorf("sync allowed_actions = %v", got)
	}
	if got := decode(BackupJob{Status: "PAUSED"}); !reflect.DeepEqual(got, []string{"resume"}) {
		t.Errorf("backup allowed_actions = %v", got)
	}
	if got := decode(AdminMigrationView{Migration: Migration{Status: "RUNNING"}}); !reflect.DeepEqual(got, []string{"pause", "cancel"}) {
		t.Errorf("admin migration allowed_actions = %v", got)
	}
}
