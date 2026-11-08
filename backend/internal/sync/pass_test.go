package sync

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"backend/internal/db"
)

func TestPassFencesPauseBeforeStartingSuccessor(t *testing.T) {
	store := &fakePassStore{
		status: "IDLE",
		pause:  7,
	}
	runner := &fakePassRunner{}
	signals := &fakePassSignals{}
	var pass Pass = newPass(store, runner, signals)

	started, err := pass.Start(context.Background(), "sync-1")
	if err != nil || !started.Started {
		t.Fatalf("Start() = (%+v, %v), want a claimed pass", started, err)
	}

	paused, err := pass.Pause(context.Background(), "sync-1")
	if err != nil || !paused {
		t.Fatalf("Pause() = (%v, %v), want (true, nil)", paused, err)
	}
	if signals.cancelledGeneration != 7 || signals.publishedGeneration != 7 {
		t.Fatalf("pause signals = local:%d remote:%d, want generation 7", signals.cancelledGeneration, signals.publishedGeneration)
	}

	resumed, err := pass.Resume(context.Background(), context.Background(), "sync-1")
	if err != nil || !resumed.Resumed || !resumed.Started {
		t.Fatalf("Resume() = (%+v, %v), want a resumed and started pass", resumed, err)
	}
	if runner.drains != 1 {
		t.Fatalf("predecessor drains = %d, want 1", runner.drains)
	}
	if len(runner.starts) != 2 {
		t.Fatalf("starts = %d, want initial pass plus successor", len(runner.starts))
	}
}

func TestPassStartTreatsADeletedJobAsNotStarted(t *testing.T) {
	store := &fakePassStore{statusErr: sql.ErrNoRows}
	runner := &fakePassRunner{startResult: false, startConfigured: true}
	pass := newPass(store, runner, &fakePassSignals{})

	result, err := pass.Start(context.Background(), "deleted-sync")
	if err != nil {
		t.Fatalf("Start() error = %v, want nil for a deleted job", err)
	}
	if result != (StartResult{}) {
		t.Fatalf("Start() = %+v, want an empty not-started result", result)
	}
}

func TestPassTriggerPropagatesDeletedJobForSchedulerDeactivation(t *testing.T) {
	pass := newPass(&fakePassStore{statusErr: sql.ErrNoRows}, &fakePassRunner{}, &fakePassSignals{})

	_, err := pass.Trigger(context.Background(), "deleted-sync")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Trigger() error = %v, want sql.ErrNoRows", err)
	}
}

func TestPassResumeDrainsBeforePersistingAndStarting(t *testing.T) {
	events := []string{}
	store := &fakePassStore{events: &events}
	runner := &fakePassRunner{events: &events}
	pass := newPass(store, runner, &fakePassSignals{})

	result, err := pass.Resume(context.Background(), context.Background(), "sync-1")
	if err != nil || !result.Started {
		t.Fatalf("Resume() = (%+v, %v), want a started successor", result, err)
	}
	if want := []string{"drain", "store.resume", "start"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("resume ordering = %v, want %v", events, want)
	}
}

func TestPassResumeDoesNotPersistOrStartWhenDrainFails(t *testing.T) {
	events := []string{}
	runner := &fakePassRunner{events: &events, drainErr: errors.New("drain failed")}
	pass := newPass(&fakePassStore{events: &events}, runner, &fakePassSignals{})

	_, err := pass.Resume(context.Background(), context.Background(), "sync-1")
	if err == nil {
		t.Fatal("Resume() error = nil, want drain failure")
	}
	if want := []string{"drain"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("resume ordering = %v, want %v", events, want)
	}
}

func TestPassResumeDoesNotStartWhenTransitionIsRejected(t *testing.T) {
	events := []string{}
	store := &fakePassStore{events: &events, resume: false, resumeConfigured: true}
	pass := newPass(store, &fakePassRunner{events: &events}, &fakePassSignals{})

	result, err := pass.Resume(context.Background(), context.Background(), "sync-1")
	if err != nil || result.Resumed || result.Started {
		t.Fatalf("Resume() = (%+v, %v), want rejected transition", result, err)
	}
	if want := []string{"drain", "store.resume"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("resume ordering = %v, want %v", events, want)
	}
}

func TestPassPauseDoesNotSignalWhenTransitionIsRejected(t *testing.T) {
	store := &fakePassStore{pauseOK: false}
	signals := &fakePassSignals{}
	pass := newPass(store, &fakePassRunner{}, signals)

	paused, err := pass.Pause(context.Background(), "sync-1")
	if err != nil || paused {
		t.Fatalf("Pause() = (%v, %v), want (false, nil)", paused, err)
	}
	if signals.cancelledGeneration != 0 || signals.publishedGeneration != 0 {
		t.Fatalf("rejected pause emitted signals: %+v", signals)
	}
}

func TestPassSchedulesRecoveryAndDeletesThroughFacade(t *testing.T) {
	store := &fakePassStore{status: "RUNNING", recovered: []RecoveredPass{{ID: "sync-1", UserID: "user-1"}}}
	runner := &fakePassRunner{}
	signals := &fakePassSignals{recoveryLock: true}
	var pass Pass = newPass(store, runner, signals)

	recovered, err := pass.Recover(context.Background())
	if err != nil || len(recovered) != 1 || recovered[0].ID != "sync-1" {
		t.Fatalf("Recover() = (%+v, %v), want sync-1", recovered, err)
	}
	if !store.recoveryAdvanced || !store.recoveryAudited {
		t.Fatal("recovery did not advance the schedule and write its audit record")
	}

	if err := pass.Delete(context.Background(), "sync-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !signals.cancelledAll || !signals.publishedAll || store.deleted != "sync-1" {
		t.Fatalf("Delete() did not cancel both coordinators and workers before deletion: %+v", signals)
	}
}

func TestPassTriggerReportsOverlapAndNonRunnableStates(t *testing.T) {
	tests := []struct {
		status string
		want   TriggerOutcome
	}{
		{status: "RUNNING", want: TriggerOverlap},
		{status: "PAUSED", want: TriggerNotRunnable},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			pass := newPass(&fakePassStore{status: tt.status}, &fakePassRunner{}, &fakePassSignals{})
			result, err := pass.Trigger(context.Background(), "sync-1")
			if err != nil || result.Outcome != tt.want {
				t.Fatalf("Trigger() = (%+v, %v), want outcome %v", result, err, tt.want)
			}
		})
	}
}

func TestPassFinalizesCountersAndBaselineThroughInterface(t *testing.T) {
	store := &fakePassStore{}
	var pass Pass = newPass(store, &fakePassRunner{}, &fakePassSignals{})
	finalization := Finalization{
		JobID:        "sync-1",
		Generation:   7,
		Status:       "SUCCESS",
		Total:        4,
		Processed:    4,
		Changed:      2,
		StateUpserts: []*db.SyncState{{SyncJobID: "sync-1", Side: "source", RelPath: "/report.txt"}},
	}

	finalized, err := pass.Finalize(finalization)
	if err != nil || !finalized {
		t.Fatalf("Finalize() = (%v, %v), want (true, nil)", finalized, err)
	}
	if store.finalization == nil || store.finalization.Generation != 7 || store.finalization.Processed != 4 || len(store.finalization.StateUpserts) != 1 {
		t.Fatalf("finalization = %+v, want counters and baseline in one transition", store.finalization)
	}
}

type fakePassStore struct {
	status           string
	statusErr        error
	pause            int
	pauseOK          bool
	resume           bool
	resumeConfigured bool
	deleted          string
	recovered        []RecoveredPass
	recoveryAdvanced bool
	recoveryAudited  bool
	finalization     *Finalization
	events           *[]string
}

func (s *fakePassStore) Status(context.Context, string) (string, error) { return s.status, s.statusErr }
func (s *fakePassStore) Pause(context.Context, string) (int, bool, error) {
	return s.pause, s.pauseOK || s.pause != 0, nil
}
func (s *fakePassStore) Resume(context.Context, string, time.Time) (bool, error) {
	if s.events != nil {
		*s.events = append(*s.events, "store.resume")
	}
	return s.resume || !s.resumeConfigured, nil
}
func (s *fakePassStore) Delete(_ context.Context, id string) error        { s.deleted = id; return nil }
func (s *fakePassStore) Recover(context.Context) ([]RecoveredPass, error) { return s.recovered, nil }
func (s *fakePassStore) AdvanceRecovered(context.Context, string) error {
	s.recoveryAdvanced = true
	return nil
}
func (s *fakePassStore) AuditRecovered(context.Context, RecoveredPass) error {
	s.recoveryAudited = true
	return nil
}
func (s *fakePassStore) Finalize(finalization Finalization) (bool, error) {
	s.finalization = &finalization
	return true, nil
}

type fakePassRunner struct {
	starts          []string
	drains          int
	startResult     bool
	startConfigured bool
	drainErr        error
	events          *[]string
}

func (r *fakePassRunner) startSyncPass(_ context.Context, id string) (bool, error) {
	r.starts = append(r.starts, id)
	if r.events != nil {
		*r.events = append(*r.events, "start")
	}
	return r.startResult || !r.startConfigured, nil
}
func (r *fakePassRunner) waitForPassDrain(context.Context, string) error {
	r.drains++
	if r.events != nil {
		*r.events = append(*r.events, "drain")
	}
	return r.drainErr
}

type fakePassSignals struct {
	cancelledGeneration int
	publishedGeneration int
	cancelledAll        bool
	publishedAll        bool
	recoveryLock        bool
}

func (s *fakePassSignals) CancelGeneration(_ string, generation int) {
	s.cancelledGeneration = generation
}
func (s *fakePassSignals) PublishGeneration(_ context.Context, _ string, generation int) error {
	s.publishedGeneration = generation
	return nil
}
func (s *fakePassSignals) CancelAll(string) { s.cancelledAll = true }
func (s *fakePassSignals) PublishAll(context.Context, string) error {
	s.publishedAll = true
	return nil
}
func (s *fakePassSignals) ClaimRecoveryLock(context.Context) (bool, error) {
	return s.recoveryLock, nil
}

var _ passStore = (*fakePassStore)(nil)
var _ passRunner = (*fakePassRunner)(nil)
var _ passSignals = (*fakePassSignals)(nil)
