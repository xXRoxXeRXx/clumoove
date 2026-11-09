import { useCallback, useEffect, useRef, useState } from 'react';
import type { BackupJob, Migration, SyncJob } from '../types';
import { backupStreamUrl } from '../api/backups';
import { syncStreamUrl } from '../api/sync';
import { useApiError } from '../utils/apiError';
import { apiErrorMessage, apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';
import { useTranslation } from 'react-i18next';

interface UseTransferOverviewOptions {
  apiUrl: string;
  token: string;
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

/**
 * Stable overview data for every transfer kind. Snapshot/stream ordering and
 * request lifecycles are intentionally hidden from the views that render it.
 */
export function useTransferOverview({ apiUrl, token }: UseTransferOverviewOptions): TransferOverview {
  const { t } = useTranslation();
  const translateApiError = useApiError();
  const [migrations, setMigrations] = useState<Migration[]>([]);
  const [migrationLoading, setMigrationLoading] = useState(true);
  const [migrationError, setMigrationError] = useState('');
  const [syncJobs, setSyncJobs] = useState<SyncJob[]>([]);
  const [syncLoading, setSyncLoading] = useState(true);
  const [syncError, setSyncError] = useState('');
  const [backupJobs, setBackupJobs] = useState<BackupJob[]>([]);
  const [backupLoading, setBackupLoading] = useState(true);
  const [backupError, setBackupError] = useState('');
  const generationRef = useRef(0);
  const hasMigrationStreamDataRef = useRef(false);
  const hasSyncStreamDataRef = useRef(false);
  const hasBackupStreamDataRef = useRef(false);
  const tRef = useRef(t);
  const translateApiErrorRef = useRef(translateApiError);

  useEffect(() => {
    tRef.current = t;
    translateApiErrorRef.current = translateApiError;
  }, [t, translateApiError]);

  useEffect(() => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    hasMigrationStreamDataRef.current = false;
    hasSyncStreamDataRef.current = false;
    hasBackupStreamDataRef.current = false;
    const migrationSnapshotController = new AbortController();
    const syncSnapshotController = new AbortController();
    const backupSnapshotController = new AbortController();
    const migrationStreamController = new AbortController();
    const syncStreamController = new AbortController();
    const backupStreamController = new AbortController();

    function acceptsMigrationSnapshot(): boolean {
      return !migrationSnapshotController.signal.aborted
        && generationRef.current === generation
        && !hasMigrationStreamDataRef.current;
    }

    async function loadMigrations(): Promise<void> {
      try {
        const result = await apiJson<Migration[]>(`${apiUrl}/api/migration`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: migrationSnapshotController.signal,
        });
        if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current('migrations.loadFailed')));
        if (acceptsMigrationSnapshot()) setMigrations(result.data || []);
      } catch (error: unknown) {
        if (acceptsMigrationSnapshot()) setMigrationError(error instanceof Error ? error.message : tRef.current('migrations.connectionError'));
      } finally {
        if (acceptsMigrationSnapshot()) setMigrationLoading(false);
      }
    }

    function acceptsSyncSnapshot(): boolean {
      return !syncSnapshotController.signal.aborted
        && generationRef.current === generation
        && !hasSyncStreamDataRef.current;
    }

    async function loadSyncJobs(): Promise<void> {
      try {
        const result = await apiJson<SyncJob[]>(`${apiUrl}/api/sync`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: syncSnapshotController.signal,
        });
        if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current('sync.loadFailed')));
        if (acceptsSyncSnapshot()) setSyncJobs(result.data || []);
      } catch (error: unknown) {
        if (acceptsSyncSnapshot()) setSyncError(error instanceof Error ? error.message : tRef.current('sync.loadFailed'));
      } finally {
        if (acceptsSyncSnapshot()) setSyncLoading(false);
      }
    }

    function acceptsBackupSnapshot(): boolean {
      return !backupSnapshotController.signal.aborted
        && generationRef.current === generation
        && !hasBackupStreamDataRef.current;
    }

    async function loadBackupJobs(): Promise<void> {
      try {
        const result = await apiJson<BackupJob[]>(`${apiUrl}/api/backup`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: backupSnapshotController.signal,
        });
        if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current('backup.loadFailed')));
        if (acceptsBackupSnapshot()) {
          setBackupJobs(result.data || []);
          // Keep the existing backup-list behavior: a successful refresh dismisses a prior load error.
          setBackupError('');
        }
      } catch (error: unknown) {
        if (acceptsBackupSnapshot()) setBackupError(error instanceof Error ? error.message : tRef.current('backup.loadFailed'));
      } finally {
        if (acceptsBackupSnapshot()) setBackupLoading(false);
      }
    }

    // Defer the reset to avoid a cascading render from this synchronization effect;
    // it runs before any asynchronous snapshot continuation can update the state.
    queueMicrotask(() => {
      if (generation !== generationRef.current) return;
      setMigrationLoading(true);
      setMigrationError('');
      setSyncLoading(true);
      setSyncError('');
      setBackupLoading(true);
      setBackupError('');
    });
    void loadMigrations();
    void loadSyncJobs();
    void loadBackupJobs();

    void connectSseLoop({
      url: `${apiUrl}/api/migration/stream`,
      signal: migrationStreamController.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (generation !== generationRef.current) return;
          if (event === 'migrations' && data) {
            try {
              const updatedMigrations = JSON.parse(data) as Migration[];
              if (!Array.isArray(updatedMigrations)) return;
              hasMigrationStreamDataRef.current = true;
              migrationSnapshotController.abort();
              setMigrations(updatedMigrations);
              setMigrationError('');
              setMigrationLoading(false);
            } catch {
              // Ignore malformed stream frames and retain the last stable state.
            }
          } else if (event === 'error') {
            setMigrationError(tRef.current('migrations.connectionError'));
            setMigrationLoading(false);
          }
        },
        onError: () => {
          if (generation !== generationRef.current) return;
          setMigrationError(tRef.current('migrations.connectionError'));
          setMigrationLoading(false);
        },
      },
    });

    void connectSseLoop({
      url: syncStreamUrl(apiUrl),
      signal: syncStreamController.signal,
      fetchImpl: apiFetch,
      handlers: {
        // Preserve the existing sync-list contract: named frames only carry jobs;
        // transport failures are handled by onError below.
        onEvent: (event, data) => {
          if (generation !== generationRef.current || event !== 'sync_jobs' || !data) return;
          try {
            const updatedSyncJobs = JSON.parse(data) as SyncJob[];
            if (!Array.isArray(updatedSyncJobs)) return;
            hasSyncStreamDataRef.current = true;
            syncSnapshotController.abort();
            setSyncJobs(updatedSyncJobs);
            setSyncError('');
            setSyncLoading(false);
          } catch {
            // Ignore malformed stream frames and retain the last stable state.
          }
        },
        onError: () => {
          if (generation !== generationRef.current) return;
          setSyncError(tRef.current('sync.loadFailed'));
          setSyncLoading(false);
        },
      },
    });

    void connectSseLoop({
      url: backupStreamUrl(apiUrl),
      signal: backupStreamController.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (generation !== generationRef.current) return;
          if (event === 'backup_jobs' && data) {
            try {
              const updatedBackupJobs = JSON.parse(data) as BackupJob[];
              if (!Array.isArray(updatedBackupJobs)) return;
              hasBackupStreamDataRef.current = true;
              backupSnapshotController.abort();
              setBackupJobs(updatedBackupJobs);
              setBackupError('');
              setBackupLoading(false);
            } catch {
              // Ignore malformed stream frames and retain the last stable state.
            }
          } else if (event === 'error') {
            setBackupError(tRef.current('backup.loadFailed'));
            setBackupLoading(false);
          }
        },
        onError: () => {
          if (generation !== generationRef.current) return;
          setBackupError(tRef.current('backup.loadFailed'));
          setBackupLoading(false);
        },
      },
    });

    return () => {
      if (generationRef.current === generation) generationRef.current += 1;
      migrationSnapshotController.abort();
      syncSnapshotController.abort();
      backupSnapshotController.abort();
      migrationStreamController.abort();
      syncStreamController.abort();
      backupStreamController.abort();
    };
  }, [apiUrl, token]);

  const removeMigration = useCallback((id: string) => {
    setMigrations((current) => current.filter((migration) => migration.id !== id));
  }, []);
  const removeMigrations = useCallback((ids: Iterable<string>) => {
    const idsToRemove = new Set(ids);
    setMigrations((current) => current.filter((migration) => !idsToRemove.has(migration.id)));
  }, []);
  const setMigrationStatus = useCallback((id: string, status: Migration['status']) => {
    setMigrations((current) => current.map((migration) => migration.id === id ? { ...migration, status } : migration));
  }, []);
  const removeSyncJob = useCallback((id: string) => {
    setSyncJobs((current) => current.filter((syncJob) => syncJob.id !== id));
  }, []);
  const removeSyncJobs = useCallback((ids: Iterable<string>) => {
    const idsToRemove = new Set(ids);
    setSyncJobs((current) => current.filter((syncJob) => !idsToRemove.has(syncJob.id)));
  }, []);
  const setSyncJobStatus = useCallback((id: string, status: SyncJob['status']) => {
    setSyncJobs((current) => current.map((syncJob) => syncJob.id === id ? { ...syncJob, status } : syncJob));
  }, []);
  const setBackupJobStatus = useCallback((id: string, status: BackupJob['status']) => {
    setBackupJobs((current) => current.map((backupJob) => backupJob.id === id ? { ...backupJob, status } : backupJob));
  }, []);

  return {
    migrations,
    migrationLoading,
    migrationError,
    syncJobs,
    syncLoading,
    syncError,
    backupJobs,
    backupLoading,
    backupError,
    removeMigration,
    removeMigrations,
    setMigrationStatus,
    removeSyncJob,
    removeSyncJobs,
    setSyncJobStatus,
    setBackupJobStatus,
  };
}
