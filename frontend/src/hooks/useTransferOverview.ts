import { useCallback, useEffect, useRef } from 'react';
import type { BackupJob, Migration, SyncJob } from '../types';
import { backupStreamUrl } from '../api/backups';
import { syncStreamUrl } from '../api/sync';
import { useLiveTransferCollection } from './useLiveTransferCollection';

interface UseTransferOverviewOptions {
  apiUrl: string;
  token: string;
  backupActive?: boolean;
}

interface TransferOverview {
  migrations: Migration[];
  migrationLoading: boolean;
  migrationError: string;
  syncJobs: SyncJob[];
  syncLoading: boolean;
  syncError: string;
  backupJobs: BackupJob[];
  backupLoading: boolean;
  backupError: string;
  removeMigration: (id: string) => void;
  removeMigrations: (ids: Iterable<string>) => void;
  setMigrationStatus: (id: string, status: Migration['status']) => void;
  removeSyncJob: (id: string) => void;
  removeSyncJobs: (ids: Iterable<string>) => void;
  setSyncJobStatus: (id: string, status: SyncJob['status']) => void;
  setBackupJobStatus: (id: string, status: BackupJob['status']) => void;
}

/** Stable overview data for every transfer kind. */
export function useTransferOverview({ apiUrl, token, backupActive = false }: UseTransferOverviewOptions): TransferOverview {
  const migrations = useLiveTransferCollection<Migration>({
    apiUrl, token, snapshotPath: '/api/migration', streamUrl: `${apiUrl}/api/migration/stream`,
    eventName: 'migrations', snapshotFailureKey: 'migrations.loadFailed', unknownSnapshotFailureKey: 'migrations.connectionError', streamFailureKey: 'migrations.connectionError', includeNamedStreamErrors: true,
  });
  const syncJobs = useLiveTransferCollection<SyncJob>({
    apiUrl, token, snapshotPath: '/api/sync', streamUrl: syncStreamUrl(apiUrl),
    eventName: 'sync_jobs', snapshotFailureKey: 'sync.loadFailed', streamFailureKey: 'sync.loadFailed',
  });
  const backupJobs = useLiveTransferCollection<BackupJob>({
    apiUrl, token, snapshotPath: '/api/backup', streamUrl: backupStreamUrl(apiUrl),
    eventName: 'backup_jobs', snapshotFailureKey: 'backup.loadFailed', streamFailureKey: 'backup.loadFailed', includeNamedStreamErrors: true,
  });
  const { refresh: refreshBackupJobs } = backupJobs;
  const { updateItems: updateMigrations } = migrations;
  const { updateItems: updateSyncJobs } = syncJobs;
  const { updateItems: updateBackupJobs } = backupJobs;
  const previousBackupActiveRef = useRef(backupActive);

  useEffect(() => {
    if (backupActive && !previousBackupActiveRef.current) refreshBackupJobs();
    previousBackupActiveRef.current = backupActive;
  }, [backupActive, refreshBackupJobs]);

  const removeMigration = useCallback((id: string) => updateMigrations((current) => current.filter((item) => item.id !== id)), [updateMigrations]);
  const removeMigrations = useCallback((ids: Iterable<string>) => {
    const removed = new Set(ids);
    updateMigrations((current) => current.filter((item) => !removed.has(item.id)));
  }, [updateMigrations]);
  const setMigrationStatus = useCallback((id: string, status: Migration['status']) => updateMigrations((current) => current.map((item) => item.id === id ? { ...item, status } : item)), [updateMigrations]);
  const removeSyncJob = useCallback((id: string) => updateSyncJobs((current) => current.filter((item) => item.id !== id)), [updateSyncJobs]);
  const removeSyncJobs = useCallback((ids: Iterable<string>) => {
    const removed = new Set(ids);
    updateSyncJobs((current) => current.filter((item) => !removed.has(item.id)));
  }, [updateSyncJobs]);
  const setSyncJobStatus = useCallback((id: string, status: SyncJob['status']) => updateSyncJobs((current) => current.map((item) => item.id === id ? { ...item, status } : item)), [updateSyncJobs]);
  const setBackupJobStatus = useCallback((id: string, status: BackupJob['status']) => updateBackupJobs((current) => current.map((item) => item.id === id ? { ...item, status } : item)), [updateBackupJobs]);

  return {
    migrations: migrations.items, migrationLoading: migrations.loading, migrationError: migrations.error,
    syncJobs: syncJobs.items, syncLoading: syncJobs.loading, syncError: syncJobs.error,
    backupJobs: backupJobs.items, backupLoading: backupJobs.loading, backupError: backupJobs.error,
    removeMigration, removeMigrations, setMigrationStatus,
    removeSyncJob, removeSyncJobs, setSyncJobStatus, setBackupJobStatus,
  };
}
