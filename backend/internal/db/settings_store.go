package db

import (
	"database/sql"
)

// SettingsStore is the persistence seam for instance-wide configuration: the
// key/value settings and the instance SMTP settings. It delegates to
// package-level functions without adding queries.
//
// The SMTP password is stored already encrypted by the caller; the store never
// sees the encryption key. Reads return that ciphertext as stored.
type SettingsStore interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error

	GetInstanceSMTPSettings() (*InstanceSMTPSettings, error)
	UpsertInstanceSMTPSettings(s *InstanceSMTPSettings) error
	DeleteInstanceSMTPSettings() error
	IsInstanceSMTPConfigured() (bool, error)
}

type postgresSettingsStore struct{ db *sql.DB }

// NewSettingsStore returns the PostgreSQL-backed SettingsStore. It panics on a
// nil handle so a wiring mistake fails at startup instead of on the first query.
func NewSettingsStore(database *sql.DB) SettingsStore {
	if database == nil {
		panic("db: NewSettingsStore requires a database handle")
	}
	return postgresSettingsStore{db: database}
}

func (s postgresSettingsStore) GetSetting(key string) (string, error) {
	return getSetting(s.db, key)
}

func (s postgresSettingsStore) SetSetting(key, value string) error {
	return setSetting(s.db, key, value)
}

func (s postgresSettingsStore) GetInstanceSMTPSettings() (*InstanceSMTPSettings, error) {
	return getInstanceSMTPSettings(s.db)
}

func (s postgresSettingsStore) UpsertInstanceSMTPSettings(settings *InstanceSMTPSettings) error {
	return upsertInstanceSMTPSettings(s.db, settings)
}

func (s postgresSettingsStore) DeleteInstanceSMTPSettings() error {
	return deleteInstanceSMTPSettings(s.db)
}

func (s postgresSettingsStore) IsInstanceSMTPConfigured() (bool, error) {
	return isInstanceSMTPConfigured(s.db)
}
