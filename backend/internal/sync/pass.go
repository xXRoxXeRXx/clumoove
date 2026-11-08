package sync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/db"
	"backend/internal/queue"
)

// Pass is the Sync Job lifecycle interface used by HTTP and scheduling
// callers. It owns the ordering that makes a pass safe: generation-fenced
// cancellation, predecessor drain, claiming, and recovery.
type Pass interface {
	Start(context.Context, string) (StartResult, error)
	Pause(context.Context, string) (bool, error)
	Resume(context.Context, context.Context, string) (ResumeResult, error)
	Delete(context.Context, string) error
	Cancel(context.Context, string) error
	Trigger(context.Context, string) (TriggerResult, error)
	Recover(context.Context) ([]RecoveredPass, error)
	Finalize(Finalization) (bool, error)
}

type StartResult struct {
	Started bool
	Status  string
}

type ResumeResult struct {
	Resumed    bool
	Started    bool
	Deferred   bool
	StartError error
}

type TriggerOutcome uint8

const (
	TriggerStarted TriggerOutcome = iota
	TriggerOverlap
	TriggerNotRunnable
)

type TriggerResult struct {
	Outcome TriggerOutcome
	Status  string
}

type RecoveredPass struct {
	ID     string
	UserID string
}

// Finalization is the fenced terminal transition for a Sync Job pass. The
// counters and next sync-state baseline must commit as one database change.
type Finalization struct {
	JobID        string
	Generation   int
	Status       string
	Error        *string
	Total        int
	Processed    int
	Changed      int
	Deleted      int
	Failed       int
	StateUpserts []*db.SyncState
	StateDeletes []db.SyncStateDelete
	Empty        bool
}

type passStore interface {
	Status(context.Context, string) (string, error)
	Pause(context.Context, string) (int, bool, error)
	Resume(context.Context, string, time.Time) (bool, error)
	Delete(context.Context, string) error
	Recover(context.Context) ([]RecoveredPass, error)
	AdvanceRecovered(context.Context, string) error
	AuditRecovered(context.Context, RecoveredPass) error
	Finalize(Finalization) (bool, error)
}

type passRunner interface {
	startSyncPass(context.Context, string) (bool, error)
	waitForPassDrain(context.Context, string) error
}

type passSignals interface {
	CancelGeneration(string, int)
	PublishGeneration(context.Context, string, int) error
	CancelAll(string)
	PublishAll(context.Context, string) error
	ClaimRecoveryLock(context.Context) (bool, error)
}

// newPass is an internal constructor for lifecycle tests. Production uses the
// Engine directly, ensuring this interface and the scan/finalization
// implementation are one module.
func newPass(store passStore, runner passRunner, signals passSignals) Pass {
	return &Engine{passStore: store, passRunner: runner, passSignals: signals}
}

func (e *Engine) Start(ctx context.Context, id string) (StartResult, error) {
	started, err := e.passRunner.startSyncPass(ctx, id)
	if err != nil || started {
		return StartResult{Started: started}, err
	}
	status, err := e.passStore.Status(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StartResult{}, nil
		}
		return StartResult{}, err
	}
	return StartResult{Status: status}, nil
}

func (e *Engine) Pause(ctx context.Context, id string) (bool, error) {
	generation, paused, err := e.passStore.Pause(ctx, id)
	if err != nil || !paused {
		return paused, err
	}
	// The generation returned by the transaction fences a delayed pause from
	// cancelling a successor created by a concurrent resume.
	e.passSignals.CancelGeneration(id, generation)
	if err := e.passSignals.PublishGeneration(ctx, id, generation); err != nil {
		slog.Warn("sync pass cancellation publication failed", "sync_job_id", id, "generation", generation, "error", err)
	}
	return true, nil
}

func (e *Engine) Resume(ctx, passCtx context.Context, id string) (ResumeResult, error) {
	// A resume never revives an abandoned coordinator. Drain its predecessor
	// before publishing the new claim so stale scans cannot write afterwards.
	if err := e.passRunner.waitForPassDrain(ctx, id); err != nil {
		return ResumeResult{}, err
	}
	resumed, err := e.passStore.Resume(ctx, id, time.Now())
	if err != nil || !resumed {
		return ResumeResult{Resumed: resumed}, err
	}
	started, startErr := e.passRunner.startSyncPass(passCtx, id)
	// The resume transaction already made the schedule due. An immediate claim
	// race or temporary failure is deliberately deferred to the next tick.
	return ResumeResult{Resumed: true, Started: started, Deferred: startErr != nil || !started, StartError: startErr}, nil
}

func (e *Engine) Delete(ctx context.Context, id string) error {
	// Stop local and remote coordinators before deleting their state so neither
	// can operate on a removed job.
	if err := e.Cancel(ctx, id); err != nil {
		slog.Warn("sync pass deletion cancellation publication failed", "sync_job_id", id, "error", err)
	}
	return e.passStore.Delete(ctx, id)
}

// Cancel stops a coordinator on this API process and signals all workers. It
// is used by account suspension as well as deletion.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	e.passSignals.CancelAll(id)
	return e.passSignals.PublishAll(ctx, id)
}

func (e *Engine) Trigger(ctx context.Context, id string) (TriggerResult, error) {
	status, err := e.passStore.Status(ctx, id)
	if err != nil {
		return TriggerResult{}, err
	}
	if IsActivePassStatus(status) {
		return TriggerResult{Outcome: TriggerOverlap, Status: status}, nil
	}
	if status != "IDLE" && status != "FAILED" {
		return TriggerResult{Outcome: TriggerNotRunnable, Status: status}, nil
	}
	started, err := e.passRunner.startSyncPass(ctx, id)
	if err != nil {
		return TriggerResult{}, err
	}
	if !started {
		return TriggerResult{Outcome: TriggerOverlap, Status: status}, nil
	}
	return TriggerResult{Outcome: TriggerStarted, Status: status}, nil
}

func (e *Engine) Recover(ctx context.Context) ([]RecoveredPass, error) {
	claimed, err := e.passSignals.ClaimRecoveryLock(ctx)
	if err != nil || !claimed {
		return nil, err
	}
	recovered, err := e.passStore.Recover(ctx)
	if err != nil {
		return nil, err
	}
	for _, pass := range recovered {
		if err := e.passStore.AdvanceRecovered(ctx, pass.ID); err != nil {
			slog.Error("recovered sync pass schedule advance failed", "sync_job_id", pass.ID, "error", err)
		}
		if err := e.passStore.AuditRecovered(ctx, pass); err != nil {
			slog.Error("recovered sync pass audit failed", "sync_job_id", pass.ID, "error", err)
		}
	}
	return recovered, nil
}

// Finalize commits a generation-fenced terminal state with its replacement
// baseline. Callers cannot persist counters without the corresponding state.
func (e *Engine) Finalize(finalization Finalization) (bool, error) {
	return e.passStore.Finalize(finalization)
}

// IsActivePassStatus reports a status that reserves a Sync Job pass from a
// concurrent trigger. Schedulers use it for the shared migration/sync overlap
// check; Trigger owns the sync-specific decision itself.
func IsActivePassStatus(status string) bool {
	return status == "RUNNING" || status == "INDEXING" || status == "VERIFYING" || status == "PAUSED_CONNECTION_LOSS"
}

type postgresPassStore struct{ db *sql.DB }

func (s postgresPassStore) Status(ctx context.Context, id string) (string, error) {
	job, err := db.GetSyncJobContext(ctx, s.db, id)
	if err != nil {
		return "", err
	}
	return job.Status, nil
}

func (s postgresPassStore) Pause(ctx context.Context, id string) (int, bool, error) {
	emptyError := ""
	return db.PauseSyncJobAndDeactivateSchedules(ctx, s.db, id, &emptyError)
}

func (s postgresPassStore) Resume(ctx context.Context, id string, nextRunAt time.Time) (bool, error) {
	emptyError := ""
	return db.ResumeSyncJobAndReactivateSchedules(ctx, s.db, id, &emptyError, nextRunAt)
}

func (s postgresPassStore) Delete(_ context.Context, id string) error {
	return db.DeleteSyncJobCascade(s.db, id)
}

func (s postgresPassStore) Recover(ctx context.Context) ([]RecoveredPass, error) {
	const recoveryMsg = "Recovered from stale INDEXING/RUNNING/VERIFYING (coordinator lost)"
	rows, err := s.db.QueryContext(ctx, `
		UPDATE sync_jobs sj
		SET status = 'IDLE', verification_lease_until = NULL, error_message = $1, updated_at = CURRENT_TIMESTAMP
		WHERE (
		        sj.status = 'INDEXING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		  )
		   OR (
		        sj.status = 'RUNNING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		    AND NOT EXISTS (
		        SELECT 1 FROM tasks t
		        WHERE t.sync_job_id = sj.id
		          AND (t.status IN ('PENDING', 'RUNNING') OR (t.status = 'FAILED' AND t.next_retry_at IS NOT NULL))
		          AND t.updated_at > NOW() - INTERVAL '10 minutes'
		    )
		  )
		   OR (
		        sj.status = 'VERIFYING'
		    AND sj.updated_at < NOW() - INTERVAL '30 minutes'
		    AND (sj.verification_lease_until IS NULL OR sj.verification_lease_until <= NOW())
		  )
		RETURNING sj.id, sj.user_id
	`, recoveryMsg)
	if err != nil {
		return nil, fmt.Errorf("recover orphaned sync passes: %w", err)
	}
	defer rows.Close()

	var recovered []RecoveredPass
	for rows.Next() {
		var pass RecoveredPass
		var userID sql.NullString
		if err := rows.Scan(&pass.ID, &userID); err != nil {
			return nil, fmt.Errorf("scan recovered sync pass: %w", err)
		}
		pass.UserID = userID.String
		recovered = append(recovered, pass)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovered sync passes: %w", err)
	}
	return recovered, nil
}

func (s postgresPassStore) AdvanceRecovered(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET next_run_at = NOW() WHERE task_type = 'sync' AND task_id = $1 AND is_active = TRUE`, id)
	return err
}

func (s postgresPassStore) AuditRecovered(ctx context.Context, pass RecoveredPass) error {
	db.WriteAuditLogContext(ctx, s.db, db.AuditEntry{
		UserID: sql.NullString{String: pass.UserID, Valid: pass.UserID != ""},
		Action: db.AuditSyncRecovered,
		Target: pass.ID,
	})
	return nil
}

func (s postgresPassStore) Finalize(finalization Finalization) (bool, error) {
	if finalization.Empty {
		return db.FinalizeEmptySyncJobPassWithStates(s.db, finalization.JobID, finalization.Generation, finalization.Status, finalization.Error, finalization.Total, finalization.Processed, finalization.Changed, finalization.Deleted, finalization.Failed, finalization.StateUpserts, finalization.StateDeletes)
	}
	return db.FinalizeSyncJobPassWithStates(s.db, finalization.JobID, finalization.Generation, finalization.Status, finalization.Error, finalization.Total, finalization.Processed, finalization.Changed, finalization.Deleted, finalization.Failed, finalization.StateUpserts, finalization.StateDeletes)
}

type enginePassSignals struct {
	engine *Engine
	queue  *queue.Queue
}

func (s enginePassSignals) CancelGeneration(id string, generation int) {
	s.engine.CancelPassForGeneration(id, generation)
}
func (s enginePassSignals) PublishGeneration(ctx context.Context, id string, generation int) error {
	if s.queue == nil {
		return nil
	}
	return s.queue.PublishSyncCancelEventForGeneration(ctx, id, generation)
}
func (s enginePassSignals) CancelAll(id string) { s.engine.CancelPass(id) }
func (s enginePassSignals) PublishAll(ctx context.Context, id string) error {
	if s.queue == nil {
		return nil
	}
	return s.queue.PublishSyncCancelEvent(ctx, id)
}
func (s enginePassSignals) ClaimRecoveryLock(ctx context.Context) (bool, error) {
	if s.queue == nil {
		return true, nil
	}
	return s.queue.TryClaimOrphanedSyncRecoveryLock(ctx, 4*time.Minute)
}
