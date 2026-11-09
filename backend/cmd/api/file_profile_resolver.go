package main

import (
	"context"
	"errors"
	"time"

	"backend/internal/db"
	"backend/internal/storage"
)

var (
	errFileProfileNotFound     = errors.New("file profile not found")
	errFileProviderUnavailable = errors.New("file provider unavailable")
)

type resolvedFileProfile struct {
	profile  *db.ConnectionProfile
	provider storage.StorageProvider
	ctx      context.Context
	close    func()
}

func mapFileProfileError(err error) error {
	if errors.Is(err, errProfileNotFound) || errors.Is(err, errProfileNotOwned) {
		return errFileProfileNotFound
	}
	return errFileProviderUnavailable
}

// loadOwnedFileProfile keeps the file-manager read-only profile lookup seam
// while delegating ownership resolution to the shared module.
func (s *APIServer) loadOwnedFileProfile(ctx context.Context, userID, profileID string) (*db.ConnectionProfile, error) {
	profile, err := s.resolveOwnedProfile(ctx, userID, profileID)
	if err != nil {
		return nil, mapFileProfileError(err)
	}
	return profile, nil
}

// resolveFileProfile is intentionally profile-only: credentials from a request
// are never accepted by file-manager endpoints. The shared connection module
// owns profile ownership, credential lifecycles, provider construction, and
// plaintext cleanup; this adapter only adds file-manager connection semantics.
func (s *APIServer) resolveFileProfile(ctx context.Context, userID, profileID string) (*resolvedFileProfile, error) {
	connection, err := s.openOwnedProfileConnection(ctx, userID, profileID)
	if err != nil {
		return nil, mapFileProfileError(err)
	}
	defer func() {
		if connection != nil {
			connection.close()
		}
	}()

	connectCtx, cancel := context.WithTimeout(connection.ctx, 30*time.Second)
	connected := false
	if managerConnector, ok := connection.provider.(storage.ManagerConnector); ok {
		connected, err = managerConnector.ConnectManager(connectCtx)
	} else {
		connected, err = connection.provider.Connect(connectCtx)
	}
	cancel()
	if err != nil || !connected {
		return nil, errFileProviderUnavailable
	}
	if err := s.persistMegaProfileSession(ctx, connection); err != nil {
		return nil, errFileProviderUnavailable
	}

	resolved := &resolvedFileProfile{
		profile:  connection.profile,
		provider: connection.provider,
		ctx:      connection.ctx,
		close:    connection.close,
	}
	connection = nil
	return resolved, nil
}
