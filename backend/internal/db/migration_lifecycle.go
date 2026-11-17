package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lib/pq"
)

// MigrationEvent is a user- or daemon-initiated lifecycle event.
type MigrationEvent string

const (
	MigrationEventPause  MigrationEvent = "PAUSE"
	MigrationEventCancel MigrationEvent = "CANCEL"
	MigrationEventFail   MigrationEvent = "FAIL"
)

type migrationTransition struct {
	to   string
	from []string
}

// migrationTransitions is the allowed source state for every event. The
// predicate is enforced by the UPDATE itself: a state read in a preceding query
// can be stale by the time the write happens (a worker may finish the migration
// in between).
var migrationTransitions = map[MigrationEvent]migrationTransition{
	MigrationEventPause:  {to: "PAUSED", from: []string{"RUNNING", "INDEXING"}},
	MigrationEventCancel: {to: "CANCELLED", from: unfinishedMigrationStates},
	MigrationEventFail:   {to: "FAILED", from: unfinishedMigrationStates},
}

// unfinishedMigrationStates are every state a migration can be in before it
// finishes or is cancelled. Cancelling and failing are legal from any of them,
// and from none of the terminal states: because no source state is terminal,
// a transition never needs to bump notification_generation.
var unfinishedMigrationStates = []string{"PENDING", "SCHEDULED", "INDEXING", "RUNNING", "VERIFYING", "PAUSED", "PAUSED_CONNECTION_LOSS"}

// TransitionMigration applies event to the migration if, and only if, its
// current status allows it. It reports whether the transition applied; a
// migration in any other state is left untouched.
func TransitionMigration(ctx context.Context, database *sql.DB, id string, event MigrationEvent, errMsg *string) (bool, error) {
	transition, ok := migrationTransitions[event]
	if !ok {
		return false, fmt.Errorf("unknown migration event %q", event)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE migrations
		SET status = $1, error_message = $2, updated_at = CURRENT_TIMESTAMP
		WHERE id = $3 AND status = ANY($4::text[])
	`, transition.to, errMsg, id, pq.Array(transition.from))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	// The terminal-status outbox record commits atomically with the transition.
	if isTerminalMigrationStatus(transition.to) {
		if err := createMigrationNotificationEventTx(tx, id); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
