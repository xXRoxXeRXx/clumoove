package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"backend/internal/db"
)

// fakeBackupStore overrides only the persistence the scheduler touches; calling
// anything else panics through the nil embedded interface, which keeps these
// tests honest about the scheduler's real dependencies.
type fakeBackupStore struct {
	db.BackupStore
	info       db.BackupScheduleInfo
	infoErr    error
	claim      db.BackupPassClaim
	claimErr   error
	claimCalls int
	claimedJob string
	claimedKey *string
	trigger    string
}

func (f *fakeBackupStore) GetBackupScheduleInfoContext(_ context.Context, _ string) (db.BackupScheduleInfo, error) {
	return f.info, f.infoErr
}

func (f *fakeBackupStore) ClaimBackupJobPassContext(_ context.Context, jobID, trigger string, scheduledLocalKey *string) (db.BackupPassClaim, error) {
	f.claimCalls++
	f.claimedJob, f.trigger, f.claimedKey = jobID, trigger, scheduledLocalKey
	return f.claim, f.claimErr
}

func newBackupTriggerScheduler(store db.BackupStore) *Scheduler {
	return &Scheduler{backups: store}
}

func backupSchedule(nextRun time.Time) *db.Schedule {
	return &db.Schedule{
		TaskType:  "backup",
		TaskID:    "backup-1",
		NextRunAt: sql.NullTime{Time: nextRun, Valid: true},
	}
}

func TestTriggerBackupClaimsPassWithLocalOccurrenceKey(t *testing.T) {
	store := &fakeBackupStore{
		info:  db.BackupScheduleInfo{Status: "IDLE", Timezone: "Europe/Berlin"},
		claim: db.BackupPassClaim{Outcome: db.BackupClaimed},
	}
	s := newBackupTriggerScheduler(store)

	// 2026-01-15 23:30 UTC is 00:30 on the 16th in Berlin (UTC+1).
	if err := s.triggerBackup(context.Background(), backupSchedule(time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC))); err != nil {
		t.Fatalf("triggerBackup() error: %v", err)
	}

	if store.claimedJob != "backup-1" || store.trigger != "schedule" {
		t.Errorf("claim = job %q trigger %q, want backup-1 / schedule", store.claimedJob, store.trigger)
	}
	if store.claimedKey == nil || *store.claimedKey != "2026-01-16T00:30" {
		t.Errorf("occurrence key = %v, want 2026-01-16T00:30", store.claimedKey)
	}
}

func TestTriggerBackupTreatsOverlapAndDuplicateAsSuccess(t *testing.T) {
	for _, outcome := range []db.BackupClaimOutcome{db.BackupClaimOverlap, db.BackupClaimDuplicate} {
		store := &fakeBackupStore{
			info:  db.BackupScheduleInfo{Status: "RUNNING", Timezone: "UTC"},
			claim: db.BackupPassClaim{Outcome: outcome},
		}
		if err := newBackupTriggerScheduler(store).triggerBackup(context.Background(), backupSchedule(time.Now())); err != nil {
			t.Errorf("outcome %q: triggerBackup() error: %v", outcome, err)
		}
	}
}

func TestTriggerBackupDoesNotClaimAdministrativelyBlockedJobs(t *testing.T) {
	for _, status := range []string{"PAUSED", "DELETING"} {
		store := &fakeBackupStore{info: db.BackupScheduleInfo{Status: status, Timezone: "UTC"}}

		err := newBackupTriggerScheduler(store).triggerBackup(context.Background(), backupSchedule(time.Now()))

		if !errors.Is(err, errScheduleTargetNotRunnable) {
			t.Errorf("status %s: error = %v, want errScheduleTargetNotRunnable", status, err)
		}
		if store.claimCalls != 0 {
			t.Errorf("status %s: claim attempted for a blocked job", status)
		}
	}
}

func TestTriggerBackupMapsBlockedClaimToNonRunnable(t *testing.T) {
	store := &fakeBackupStore{
		info:  db.BackupScheduleInfo{Status: "IDLE", Timezone: "UTC"},
		claim: db.BackupPassClaim{Outcome: db.BackupClaimBlocked},
	}

	err := newBackupTriggerScheduler(store).triggerBackup(context.Background(), backupSchedule(time.Now()))

	if !errors.Is(err, errScheduleTargetNotRunnable) {
		t.Errorf("error = %v, want errScheduleTargetNotRunnable", err)
	}
}

func TestTriggerBackupInfrastructureErrorsKeepTheScheduleRetryable(t *testing.T) {
	boom := errors.New("connection reset")

	loadErr := newBackupTriggerScheduler(&fakeBackupStore{infoErr: boom}).triggerBackup(context.Background(), backupSchedule(time.Now()))
	claimErr := newBackupTriggerScheduler(&fakeBackupStore{
		info:     db.BackupScheduleInfo{Status: "IDLE", Timezone: "UTC"},
		claimErr: boom,
	}).triggerBackup(context.Background(), backupSchedule(time.Now()))

	for name, err := range map[string]error{"load": loadErr, "claim": claimErr} {
		if !errors.Is(err, boom) {
			t.Errorf("%s: error = %v, want it to wrap the store error", name, err)
		}
		if errors.Is(err, errScheduleTargetNotRunnable) {
			t.Errorf("%s: infrastructure failure must not be marked non-runnable", name)
		}
	}
}

func TestTriggerBackupRejectsUnknownClaimOutcome(t *testing.T) {
	store := &fakeBackupStore{
		info:  db.BackupScheduleInfo{Status: "IDLE", Timezone: "UTC"},
		claim: db.BackupPassClaim{Outcome: "mystery"},
	}

	if err := newBackupTriggerScheduler(store).triggerBackup(context.Background(), backupSchedule(time.Now())); err == nil {
		t.Error("triggerBackup() succeeded for an unknown claim outcome")
	}
}
