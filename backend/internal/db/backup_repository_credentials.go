package db

import (
	"context"
	"database/sql"
	"time"
)

// BackupRepositoryCredentials is the persistence shared by the backup and
// restore domains: the stored connection to a backup job's pack repository and
// the conditional rotation of its credentials. Restore runs read these but never
// copy the source secrets into their own rows.
type BackupRepositoryCredentials interface {
	GetBackupRepositoryConnectionContext(ctx context.Context, jobID string) (*BackupRepositoryConnection, error)
	UpdateBackupRepositoryOAuthTokens(ctx context.Context, backupJobID, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error
	UpdateBackupRepositoryMegaSessionContext(ctx context.Context, backupJobID, sessionIDEncrypted, masterKeyEncrypted string) error
}

type backupRepositoryCredentials struct{ db *sql.DB }

func (s backupRepositoryCredentials) GetBackupRepositoryConnectionContext(ctx context.Context, jobID string) (*BackupRepositoryConnection, error) {
	return GetBackupRepositoryConnectionContext(ctx, s.db, jobID)
}

func (s backupRepositoryCredentials) UpdateBackupRepositoryOAuthTokens(ctx context.Context, backupJobID, accessEncrypted, refreshEncrypted string, expiresAt time.Time, expectedRefreshEncrypted string) error {
	return UpdateBackupRepositoryOAuthTokens(ctx, s.db, backupJobID, accessEncrypted, refreshEncrypted, expiresAt, expectedRefreshEncrypted)
}

func (s backupRepositoryCredentials) UpdateBackupRepositoryMegaSessionContext(ctx context.Context, backupJobID, sessionIDEncrypted, masterKeyEncrypted string) error {
	return UpdateBackupRepositoryMegaSessionContext(ctx, s.db, backupJobID, sessionIDEncrypted, masterKeyEncrypted)
}
