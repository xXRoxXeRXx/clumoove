package backup

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"backend/internal/coordcfg"
)

type stubLocker struct{}

func (stubLocker) TryClaimOAuthLock(context.Context, string, string, string, time.Duration) (string, bool, error) {
	return "", true, nil
}
func (stubLocker) ReleaseOAuthLock(context.Context, string, string, string, string) error { return nil }

func validBackupConfig() Config {
	return Config{
		Common:         coordcfg.Common{EncryptionKey: "key", WorkerID: "worker-1", OAuthLocker: stubLocker{}},
		MaxPackWriters: 1,
	}
}

func TestNewCoordinatorCarriesWorkerIdentityAndLocker(t *testing.T) {
	locker := stubLocker{}
	cfg := validBackupConfig()
	cfg.OAuthLocker = locker
	coordinator, err := NewCoordinator(&sql.DB{}, cfg)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	if coordinator.workerID != "worker-1" || coordinator.oauthLocker != locker {
		t.Fatalf("workerID = %q, locker = %v; want configured values", coordinator.workerID, coordinator.oauthLocker)
	}
}

func TestNewCoordinatorDefaultsBlankWorkerID(t *testing.T) {
	for _, blank := range []string{"", "   "} {
		cfg := validBackupConfig()
		cfg.WorkerID = blank
		coordinator, err := NewCoordinator(&sql.DB{}, cfg)
		if err != nil {
			t.Fatalf("NewCoordinator(%q) error = %v", blank, err)
		}
		if coordinator.workerID != "backup-worker" {
			t.Errorf("workerID for %q = %q, want backup-worker", blank, coordinator.workerID)
		}
	}
}

func TestNewCoordinatorRejectsMissingDependencies(t *testing.T) {
	if _, err := NewCoordinator(nil, validBackupConfig()); err == nil {
		t.Error("NewCoordinator(nil database) succeeded")
	}
	cfg := validBackupConfig()
	cfg.OAuthLocker = nil
	if _, err := NewCoordinator(&sql.DB{}, cfg); err == nil {
		t.Error("NewCoordinator(nil OAuth locker) succeeded")
	}
}

func validBackupConfigWithLimit(limit int) Config {
	cfg := validBackupConfig()
	cfg.MaxPackWriters = limit
	return cfg
}
