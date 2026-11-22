package db

import (
	"context"
	"database/sql"
)

// BackupRepositoryCredentials is the persistence shared by the backup and
// restore domains: the stored connection to a backup job's pack repository and
// the conditional rotation of its credentials. Restore runs read these but never
// copy the source secrets into their own rows.
type BackupRepositoryCredentials interface {
	GetBackupRepositoryConnectionContext(ctx context.Context, jobID string) (*BackupRepositoryConnection, error)
	BackupRepositoryTokenSink(backupJobID string) OAuthTokenSink
	UpdateBackupRepositoryMegaSessionContext(ctx context.Context, backupJobID, sessionIDEncrypted, masterKeyEncrypted string) error
}

type backupRepositoryCredentials struct{ db *sql.DB }

func (s backupRepositoryCredentials) GetBackupRepositoryConnectionContext(ctx context.Context, jobID string) (*BackupRepositoryConnection, error) {
	return GetBackupRepositoryConnectionContext(ctx, s.db, jobID)
}

func (s backupRepositoryCredentials) BackupRepositoryTokenSink(backupJobID string) OAuthTokenSink {
	return NewBackupRepositoryTokenSink(s.db, backupJobID)
}

func (s backupRepositoryCredentials) UpdateBackupRepositoryMegaSessionContext(ctx context.Context, backupJobID, sessionIDEncrypted, masterKeyEncrypted string) error {
	return UpdateBackupRepositoryMegaSessionContext(ctx, s.db, backupJobID, sessionIDEncrypted, masterKeyEncrypted)
}
