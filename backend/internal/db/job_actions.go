package db

// JobAction is a user-initiated lifecycle action on a migration, sync job or
// backup job. The API exposes the actions a job currently allows as
// allowed_actions so clients never re-derive them from status literals.
type JobAction string

const (
	JobActionStart  JobAction = "start"
	JobActionPause  JobAction = "pause"
	JobActionResume JobAction = "resume"
	JobActionCancel JobAction = "cancel"
	// JobActionEditScope is changing what a sync job transfers (paths, target,
	// strategy). It is only offered while no pass is, or is about to be, active.
	JobActionEditScope JobAction = "edit_scope"
)

// jobActionOrder fixes the order actions are listed in, so responses are stable.
var jobActionOrder = []JobAction{JobActionStart, JobActionPause, JobActionResume, JobActionCancel, JobActionEditScope}

// actionSources maps every user action to the statuses it may start from.
// The same slices are bound into the SQL predicate that guards each
// transition, so allowed_actions and the write-time guard cannot drift apart.
type actionSources map[JobAction][]string

// allowed lists the actions whose source set contains status. The result is
// never nil, so it always serializes as a JSON array.
func (s actionSources) allowed(status string) []JobAction {
	actions := []JobAction{}
	for _, action := range jobActionOrder {
		for _, source := range s[action] {
			if source == status {
				actions = append(actions, action)
				break
			}
		}
	}
	return actions
}

// migrationActionSources is the user-facing slice of the migration transition
// table. Pause and cancel are shared with migrationTransitions; resume is the
// only action whose target state depends on the row (RUNNING or INDEXING), so
// it lives in resumeMigration. A user may resume PAUSED_CONNECTION_LOSS early
// instead of waiting for the connection-recovery scheduler.
var migrationActionSources = actionSources{
	JobActionPause:  migrationTransitions[MigrationEventPause].from,
	JobActionResume: {"PAUSED", "PAUSED_CONNECTION_LOSS"},
	JobActionCancel: migrationTransitions[MigrationEventCancel].from,
}

// syncActionSources is the sync job transition table.
//
// PAUSED_CONNECTION_LOSS deliberately has no user action. It is owned by the
// worker's connection recovery, which releases the job to IDLE and makes its
// schedule due; a user pause would be meaningless (it is already paused) and a
// user resume would race that recovery and start a pass before the endpoint is
// known to be reachable. Triggers likewise treat the status as an active pass.
// Migrations differ on purpose: resuming one keeps its task queue and verified
// progress, so an early user resume is safe there.
var syncActionSources = actionSources{
	JobActionStart:  {"IDLE", "FAILED"},
	JobActionPause:  {"IDLE", "INDEXING", "RUNNING", "VERIFYING"},
	JobActionResume: {"PAUSED"},
	// updateSyncJobScope binds this set into its SQL guard. PAUSED_CONNECTION_LOSS
	// is excluded for the same reason it has no pause or resume.
	JobActionEditScope: {"IDLE", "PAUSED", "FAILED"},
}

// backupActionSources is the backup job transition table.
var backupActionSources = actionSources{
	JobActionStart:  {"IDLE", "FAILED"},
	JobActionPause:  {"IDLE", "FAILED", "QUEUED", "SCANNING", "RUNNING", "VERIFYING"},
	JobActionResume: {"PAUSED"},
}

// AllowedMigrationActions lists the actions a migration in status allows.
func AllowedMigrationActions(status string) []JobAction {
	return migrationActionSources.allowed(status)
}

// AllowedSyncActions lists the actions a sync job in status allows.
func AllowedSyncActions(status string) []JobAction { return syncActionSources.allowed(status) }

// AllowedBackupActions lists the actions a backup job in status allows.
func AllowedBackupActions(status string) []JobAction { return backupActionSources.allowed(status) }

// BackupActionSources returns the statuses action may start from.
func BackupActionSources(action JobAction) []string { return backupActionSources[action] }
