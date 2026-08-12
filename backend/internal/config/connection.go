// Package config contains process-wide configuration defaults and resolution
// helpers shared by the API and worker entrypoints.
package config

const (
	DefaultDatabaseURL = "postgres://postgres:postgres@localhost:5432/cloud_migration_db?sslmode=require"
	DefaultRedisURL    = "localhost:6379"
)

// DatabaseURL returns the configured PostgreSQL URL and whether the safe local
// default was used.
func DatabaseURL(value string) (url string, defaulted bool) {
	if value == "" {
		return DefaultDatabaseURL, true
	}
	return value, false
}

// RedisURL returns the configured Redis URL and whether the local default was
// used.
func RedisURL(value string) (url string, defaulted bool) {
	if value == "" {
		return DefaultRedisURL, true
	}
	return value, false
}
