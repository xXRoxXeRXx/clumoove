package main

import (
	"context"
	"testing"
	"time"
)

// A user can cancel a migration while the daemon is still refreshing its
// token. A late refresh failure must not overwrite that decision.
func TestRotationDaemonFailMigrationDoesNotOverwriteTerminalOrCancelled(t *testing.T) {
	for _, tc := range []struct {
		from, want string
	}{
		{"RUNNING", "FAILED"},
		{"INDEXING", "FAILED"},
		{"CANCELLED", "CANCELLED"},
		{"COMPLETED", "COMPLETED"},
		{"COMPLETED_WITH_ERRORS", "COMPLETED_WITH_ERRORS"},
		{"FAILED", "FAILED"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			database, state := newMigrationModuleTestDB(t, nil)
			now := time.Now().UTC()
			state.setMigration(&migrationModuleRecord{id: "m-1", userID: "user-1", status: tc.from, createdAt: now, updatedAt: now})
			d := &oauthRotationDaemon{db: database}

			d.failMigrationAfterRefreshFailure(context.Background(), "m-1", "dropbox")

			if got := state.getStatus("m-1"); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}
