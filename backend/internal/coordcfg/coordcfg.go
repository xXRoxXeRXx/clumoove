// Package coordcfg holds the construction settings shared by the worker-side
// backup and restore coordinators.
package coordcfg

import (
	"errors"
	"strings"

	"backend/internal/oauth"
)

// Common is the explicit configuration every worker-side coordinator needs.
type Common struct {
	EncryptionKey string
	// WorkerID binds claims and heartbeats to the process liveness identity. A
	// blank value selects the coordinator's default for narrow unit tests.
	WorkerID string
	// OAuthLocker is the distributed OAuth refresh lock backend. It is required.
	OAuthLocker oauth.TokenLocker
}

// Resolve validates the settings and returns the effective worker ID: the
// configured one, or defaultWorkerID when it is blank.
func (c Common) Resolve(defaultWorkerID string) (workerID string, err error) {
	if c.OAuthLocker == nil {
		return "", errors.New("OAuth locker is required")
	}
	if strings.TrimSpace(c.WorkerID) == "" {
		return defaultWorkerID, nil
	}
	return c.WorkerID, nil
}
