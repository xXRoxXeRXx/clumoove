// Package config contains process-wide configuration defaults and resolution
// helpers shared by the API and worker entrypoints.
package config

import (
	"fmt"
	"strconv"
	"time"
)

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

// DefaultIndexingTimeout is the maximum duration of one indexing run when
// INDEXING_TIMEOUT_MINUTES is not set.
const DefaultIndexingTimeout = 20 * time.Minute

// IndexingTimeout resolves INDEXING_TIMEOUT_MINUTES. An unset value yields the
// default; anything other than a positive whole number of minutes is an error.
func IndexingTimeout(value string) (time.Duration, error) {
	if value == "" {
		return DefaultIndexingTimeout, nil
	}
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes < 1 {
		return 0, fmt.Errorf("INDEXING_TIMEOUT_MINUTES must be a positive integer")
	}
	return time.Duration(minutes) * time.Minute, nil
}
