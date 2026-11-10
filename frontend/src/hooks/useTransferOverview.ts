import { useCallback, useEffect, useMemo, useRef } from 'react';
import type { BackupJob, Migration, SyncJob } from '../types';
import { useLiveTransferCollection } from './useLiveTransferCollection';
import { backupOverviewAdapter, migrationOverviewAdapter, syncOverviewAdapter } from './transferOverviewAdapters';

interface UseTransferOverviewOptions {
  apiUrl: string;
  token: string;
  backupActive?: boolean;
}

interface TransferOverviewCollection<T extends { id: string; status: string }> {
  items: T[];
  loading: boolean;
  error: string;
  setStatus: (id: string, status: T['status']) => void;
}

interface RemovableTransferOverviewCollection<T extends { id: string; status: string }> extends TransferOverviewCollection<T> {
  remove: (id: string) => void;
  removeMany: (ids: Iterable<string>) => void;
}

interface TransferOverview {
  migrations: RemovableTransferOverviewCollection<Migration>;
  syncJobs: RemovableTransferOverviewCollection<SyncJob>;
  backups: TransferOverviewCollection<BackupJob>;
}

/** Stable overview data for every transfer kind. */
export function useTransferOverview({ apiUrl, token, backupActive = false }: UseTransferOverviewOptions): TransferOverview {
  const migrationCollection = useLiveTransferCollection({ apiUrl, token, adapter: migrationOverviewAdapter });
  const syncCollection = useLiveTransferCollection({ apiUrl, token, adapter: syncOverviewAdapter });
  const backupCollection = useLiveTransferCollection({ apiUrl, token, adapter: backupOverviewAdapter });
  const { refresh: refreshBackupJobs } = backupCollection;
  const { updateItems: updateMigrations } = migrationCollection;
  const { updateItems: updateSyncJobs } = syncCollection;
  const { updateItems: updateBackupJobs } = backupCollection;
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

  return useMemo(() => ({
    migrations: { items: migrationCollection.items, loading: migrationCollection.loading, error: migrationCollection.error, remove: removeMigration, removeMany: removeMigrations, setStatus: setMigrationStatus },
    syncJobs: { items: syncCollection.items, loading: syncCollection.loading, error: syncCollection.error, remove: removeSyncJob, removeMany: removeSyncJobs, setStatus: setSyncJobStatus },
    backups: { items: backupCollection.items, loading: backupCollection.loading, error: backupCollection.error, setStatus: setBackupJobStatus },
  }), [migrationCollection.items, migrationCollection.loading, migrationCollection.error, removeMigration, removeMigrations, setMigrationStatus, syncCollection.items, syncCollection.loading, syncCollection.error, removeSyncJob, removeSyncJobs, setSyncJobStatus, backupCollection.items, backupCollection.loading, backupCollection.error, setBackupJobStatus]);
}
