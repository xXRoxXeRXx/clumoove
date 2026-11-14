package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	// DefaultIndexingTimeout is the maximum duration of one indexing run when
	// INDEXING_TIMEOUT_MINUTES is not set.
	DefaultIndexingTimeout = 20 * time.Minute

	// MaxIndexingTimeoutMinutes bounds INDEXING_TIMEOUT_MINUTES (one week) so a
	// typo cannot overflow the duration or effectively disable the timeout.
	MaxIndexingTimeoutMinutes = 7 * 24 * 60
)

// IndexingTimeout resolves INDEXING_TIMEOUT_MINUTES. An unset value yields the
// default; anything other than a whole number of minutes between 1 and
// MaxIndexingTimeoutMinutes is an error.
func IndexingTimeout(value string) (time.Duration, error) {
	if value == "" {
		return DefaultIndexingTimeout, nil
	}
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes < 1 || minutes > MaxIndexingTimeoutMinutes {
		return 0, fmt.Errorf("INDEXING_TIMEOUT_MINUTES must be an integer between 1 and %d", MaxIndexingTimeoutMinutes)
	}
	return time.Duration(minutes) * time.Minute, nil
}
