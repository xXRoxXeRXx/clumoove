package db

import "database/sql"

// NotificationStore is the persistence seam for notifications: the per-user
// channel configuration, the delivery outbox the worker drains, and the repair
// of terminal migrations whose event was never written. It delegates to the
// package-level functions without adding queries.
//
// Channel configuration is stored already encrypted by the caller; the store
// never sees the encryption key. Outbox events are created by the same
// transactions that write terminal job state, so there is deliberately no
// event-creation method here.
//
// Delivery claims and completions are by delivery id alone: the claim moves a
// delivery to RUNNING and completion only applies to a RUNNING delivery, so
// that fencing stays in the package-level functions.
type NotificationStore interface {
	GetNotificationChannels(userID string) ([]NotificationChannel, error)
	GetNotificationChannel(userID, typ string) (*NotificationChannel, error)
	UpsertNotificationChannel(userID, typ string, enabled bool, encrypted string) error

	ClaimNotificationDeliveries(limit int) ([]NotificationDelivery, error)
	CompleteNotificationDelivery(id string, sent bool, code string) error

	RepairMissingMigrationNotificationEvents(limit int) (int, error)
}

type postgresNotificationStore struct{ db *sql.DB }

// NewNotificationStore returns the PostgreSQL-backed NotificationStore. It
// panics on a nil handle so a wiring mistake fails at startup instead of on the
// first query.
func NewNotificationStore(database *sql.DB) NotificationStore {
	if database == nil {
		panic("db: NewNotificationStore requires a database handle")
	}
	return postgresNotificationStore{db: database}
}

func (s postgresNotificationStore) GetNotificationChannels(userID string) ([]NotificationChannel, error) {
	return getNotificationChannels(s.db, userID)
}

func (s postgresNotificationStore) GetNotificationChannel(userID, typ string) (*NotificationChannel, error) {
	return getNotificationChannel(s.db, userID, typ)
}

func (s postgresNotificationStore) UpsertNotificationChannel(userID, typ string, enabled bool, encrypted string) error {
	return upsertNotificationChannel(s.db, userID, typ, enabled, encrypted)
}

func (s postgresNotificationStore) ClaimNotificationDeliveries(limit int) ([]NotificationDelivery, error) {
	return claimNotificationDeliveries(s.db, limit)
}

func (s postgresNotificationStore) CompleteNotificationDelivery(id string, sent bool, code string) error {
	return completeNotificationDelivery(s.db, id, sent, code)
}

func (s postgresNotificationStore) RepairMissingMigrationNotificationEvents(limit int) (int, error) {
	return repairMissingMigrationNotificationEvents(s.db, limit)
}
