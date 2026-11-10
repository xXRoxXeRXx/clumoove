import type { BackupJob, Migration, SyncJob } from '../types';
import { backupStreamUrl } from '../api/backups';
import { syncStreamUrl } from '../api/sync';
import type { LiveTransferAdapter } from './useLiveTransferCollection';

function decodeTransferFrame<T>(data: string): T[] | null {
  try {
    const items = JSON.parse(data) as T[];
    return Array.isArray(items) ? items : null;
  } catch {
    return null;
  }
}

export const migrationOverviewAdapter: LiveTransferAdapter<Migration> = {
  snapshotPath: '/api/migration',
  streamUrl: (apiUrl) => `${apiUrl}/api/migration/stream`,
  eventName: 'migrations',
  snapshotFailureKey: 'migrations.loadFailed',
  unknownSnapshotFailureKey: 'migrations.connectionError',
  streamFailureKey: 'migrations.connectionError',
  includeNamedStreamErrors: true,
  decodeFrame: decodeTransferFrame,
};

export const syncOverviewAdapter: LiveTransferAdapter<SyncJob> = {
  snapshotPath: '/api/sync',
  streamUrl: syncStreamUrl,
  eventName: 'sync_jobs',
  snapshotFailureKey: 'sync.loadFailed',
  streamFailureKey: 'sync.loadFailed',
  decodeFrame: decodeTransferFrame,
};

export const backupOverviewAdapter: LiveTransferAdapter<BackupJob> = {
  snapshotPath: '/api/backup',
  streamUrl: backupStreamUrl,
  eventName: 'backup_jobs',
  snapshotFailureKey: 'backup.loadFailed',
  streamFailureKey: 'backup.loadFailed',
  includeNamedStreamErrors: true,
  decodeFrame: decodeTransferFrame,
};
