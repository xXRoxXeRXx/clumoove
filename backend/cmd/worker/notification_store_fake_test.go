package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"backend/internal/db"
	"backend/internal/notify"
)

// fakeNotificationStore overrides only the db.NotificationStore methods the
// worker uses; any other call panics through the nil embedded interface.
type fakeNotificationStore struct {
	db.NotificationStore

	mu         sync.Mutex
	claimed    []db.NotificationDelivery
	claimErr   error
	claimLimit int
	complete   func(id string, success bool, errCode string) error

	repairErr   error
	repairLimit int
	repairCalls int
}

func (f *fakeNotificationStore) ClaimNotificationDeliveries(limit int) ([]db.NotificationDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimLimit = limit
	return f.claimed, f.claimErr
}

func (f *fakeNotificationStore) CompleteNotificationDelivery(id string, sent bool, code string) error {
	f.mu.Lock()
	complete := f.complete
	f.mu.Unlock()
	if complete == nil {
		return nil
	}
	return complete(id, sent, code)
}

func (f *fakeNotificationStore) RepairMissingMigrationNotificationEvents(limit int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repairCalls++
	f.repairLimit = limit
	return 0, f.repairErr
}

func TestNotifierSendsEveryClaimedDeliveryAndCompletesIt(t *testing.T) {
	var mu sync.Mutex
	completed := map[string]bool{}
	store := &fakeNotificationStore{
		claimed: []db.NotificationDelivery{
			{ID: "d1", ChannelType: "ntfy", ConfigEncrypted: "corrupt"},
			{ID: "d2", ChannelType: "ntfy", ConfigEncrypted: "corrupt"},
			{ID: "d3", ChannelType: "ntfy", ConfigEncrypted: "corrupt"},
			{ID: "d4", ChannelType: "ntfy", ConfigEncrypted: "corrupt"},
		},
		complete: func(id string, success bool, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			completed[id] = success
			return nil
		},
	}
	n := newNotifier(nil, "key")
	n.notifications = store
	n.send = func(context.Context, string, notify.Config, json.RawMessage, string, string) error { return nil }

	n.sendPendingNotifications(context.Background())

	if store.claimLimit != 10 {
		t.Fatalf("claim limit = %d, want 10", store.claimLimit)
	}
	if len(completed) != 4 {
		t.Fatalf("completed = %v, want all four claimed deliveries completed", completed)
	}
	for id, success := range completed {
		if success {
			t.Fatalf("delivery %s reported success despite an undecryptable config", id)
		}
	}
}

func TestNotifierClaimFailureCompletesNothing(t *testing.T) {
	store := &fakeNotificationStore{
		claimErr: errors.New("boom"),
		complete: func(string, bool, string) error {
			t.Fatal("a failed claim must not complete any delivery")
			return nil
		},
	}
	n := newNotifier(nil, "key")
	n.notifications = store

	n.sendPendingNotifications(context.Background())
}

func TestWorkerMaintenanceRepairsMigrationNotificationsThroughTheStore(t *testing.T) {
	store := &fakeNotificationStore{}
	m := &workerMaintenance{notifications: store}

	m.repairMissingMigrationNotifications()

	if store.repairCalls != 1 || store.repairLimit != 100 {
		t.Fatalf("repair calls = %d (limit %d), want one call with limit 100", store.repairCalls, store.repairLimit)
	}

	store.repairErr = errors.New("boom")
	m.repairMissingMigrationNotifications() // logs, never panics
}
