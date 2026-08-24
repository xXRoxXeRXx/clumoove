import { useState, useEffect, useCallback, useRef } from 'react';
import type { User, Migration, SyncJob, BackupJob } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { useConfirm } from '../contexts/useConfirm';
import { useToast } from '../contexts/useToast';
import { StatusBadge } from './StatusBadge';
import { LoadingIndicator } from './LoadingIndicator';
import { apiErrorMessage, apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';
import {
  ArrowPathIcon,
  CalendarDaysIcon,
  CloudArrowDownIcon,
  CloudArrowUpIcon,
  PauseIcon,
  PlayIcon,
  TrashIcon,
} from './icons';
import { ProgressBar } from './ProgressBar';
import { BackupSnapshotBrowser } from './BackupSnapshotBrowser';
import { formatCronHuman } from '../utils/cronFormatter';

interface MigrationsDashboardProps {
  apiUrl: string;
  token: string;
  user: User | null;
  onStartNewMigration: () => void;
  onSelectActiveMigration: (id: string) => void;
  onSelectActiveSync?: (id: string) => void;
  onSelectActiveBackup?: (id: string) => void;
  onOpenFileManager?: (profileId: string, path: string) => void;
  onOpenFilemanagerRoot?: () => void;
}

type DashboardTab = 'migrations' | 'sync' | 'backup' | 'fileManager';

function commonParentPath(paths: string[] | undefined): string {
  if (!paths?.length) return '/';
  const parents = paths.map((value) => value.split('/').filter(Boolean).slice(0, -1));
  const shared = [...parents[0]];
  for (const candidate of parents.slice(1)) {
    let index = 0;
    while (index < shared.length && index < candidate.length && shared[index] === candidate[index]) index++;
    shared.length = index;
  }
  return shared.length ? `/${shared.join('/')}` : '/';
}

export function MigrationsDashboard({
  apiUrl,
  token,
  user,
  onStartNewMigration,
  onSelectActiveMigration,
  onSelectActiveSync,
  onSelectActiveBackup,
  onOpenFileManager,
  onOpenFilemanagerRoot,
}: MigrationsDashboardProps) {
  const [activeTab, setActiveTab] = useState<DashboardTab>('migrations');
  const [migrations, setMigrations] = useState<Migration[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [deleteLoading, setDeleteLoading] = useState<string | null>(null);
  const [deleteAllLoading, setDeleteAllLoading] = useState<boolean>(false);
  const [controlLoading, setControlLoading] = useState<string | null>(null);

  const [syncJobs, setSyncJobs] = useState<SyncJob[]>([]);
  const [syncLoading, setSyncLoading] = useState<boolean>(true);
  const [syncError, setSyncError] = useState<string>('');
  const [backupJobs, setBackupJobs] = useState<BackupJob[]>([]);
  const [backupLoading, setBackupLoading] = useState<boolean>(true);
  const [backupError, setBackupError] = useState<string>('');

  const [searchTerm, setSearchTerm] = useState<string>('');
  const [statusFilter, setStatusFilter] = useState<string>('all');
  const tabRefs = useRef<Record<DashboardTab, HTMLButtonElement | null>>({
    migrations: null,
    sync: null,
    backup: null,
    fileManager: null,
  });
  // A live stream frame is newer than the initial HTTP snapshot, regardless of
  // which response happens to arrive first.
  const hasMigrationStreamDataRef = useRef(false);
  const hasSyncStreamDataRef = useRef(false);
  const snapshotGenerationRef = useRef(0);
  const migrationSnapshotAbortRef = useRef<AbortController | null>(null);
  const syncSnapshotAbortRef = useRef<AbortController | null>(null);
  const backupSnapshotAbortRef = useRef<AbortController | null>(null);

  const { t } = useTranslation();
  const { formatBytes, formatDateTime, formatPercent } = useFormat();
  const translateApiError = useApiError();
  const confirm = useConfirm();
  const toast = useToast();
  const tRef = useRef(t);

  useEffect(() => {
    tRef.current = t;
  }, [t]);

  const fetchSyncJobs = useCallback(async function fetchSyncJobs(signal: AbortSignal, snapshotGeneration: number): Promise<void> {
    function acceptsSnapshot(): boolean {
      return !signal.aborted
        && snapshotGenerationRef.current === snapshotGeneration
        && !hasSyncStreamDataRef.current;
    }

    try {
      const result = await apiJson<SyncJob[]>(`${apiUrl}/api/sync`, {
        headers: { Authorization: `Bearer ${token}` },
        signal,
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('sync.loadFailed')));
      if (acceptsSnapshot()) {
        setSyncJobs(result.data || []);
      }
    } catch (err: unknown) {
      if (acceptsSnapshot()) {
        setSyncError(err instanceof Error ? err.message : t('sync.loadFailed'));
      }
    } finally {
      if (acceptsSnapshot()) {
        setSyncLoading(false);
      }
    }
  }, [apiUrl, token, t, translateApiError]);

  const fetchMigrations = useCallback(async function fetchMigrations(signal: AbortSignal, snapshotGeneration: number): Promise<void> {
    function acceptsSnapshot(): boolean {
      return !signal.aborted
        && snapshotGenerationRef.current === snapshotGeneration
        && !hasMigrationStreamDataRef.current;
    }

    try {
      const result = await apiJson<Migration[]>(`${apiUrl}/api/migration`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
        signal,
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('migrations.loadFailed')));
      if (acceptsSnapshot()) {
        setMigrations(result.data || []);
      }
    } catch (err: unknown) {
      if (acceptsSnapshot()) {
        setError(err instanceof Error ? err.message : t('migrations.connectionError'));
      }
    } finally {
      if (acceptsSnapshot()) {
        setLoading(false);
      }
    }
  }, [apiUrl, token, t, translateApiError]);

  const fetchBackupJobs = useCallback(async function fetchBackupJobs(signal: AbortSignal, snapshotGeneration?: number): Promise<void> {
    function acceptsSnapshot(): boolean {
      return !signal.aborted
        && (snapshotGeneration === undefined || snapshotGenerationRef.current === snapshotGeneration);
    }

    try {
      const result = await apiJson<BackupJob[]>(`${apiUrl}/api/backup`, {
        headers: { Authorization: `Bearer ${token}` },
        signal,
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('backup.loadFailed')));
      if (acceptsSnapshot()) {
        setBackupJobs(result.data || []);
        setBackupError('');
      }
    } catch (err: unknown) {
      if (acceptsSnapshot()) {
        setBackupError(err instanceof Error ? err.message : t('backup.loadFailed'));
      }
    } finally {
      if (acceptsSnapshot()) {
        setBackupLoading(false);
      }
    }
  }, [apiUrl, token, t, translateApiError]);

  // Load initial lists immediately instead of waiting for the initial SSE frames.
  // The streams remain responsible for live updates after this first snapshot.
  useEffect(() => {
    const isInitialLoad = snapshotGenerationRef.current === 0;
    snapshotGenerationRef.current += 1;
    migrationSnapshotAbortRef.current?.abort();
    syncSnapshotAbortRef.current?.abort();
    backupSnapshotAbortRef.current?.abort();
    migrationSnapshotAbortRef.current = null;
    syncSnapshotAbortRef.current = null;
    backupSnapshotAbortRef.current = null;
    hasMigrationStreamDataRef.current = false;
    hasSyncStreamDataRef.current = false;
    if (isInitialLoad) return;

    const resetTimeoutId = window.setTimeout(() => {
      setLoading(true);
      setSyncLoading(true);
      setBackupLoading(true);
      setError('');
      setSyncError('');
      setBackupError('');
    }, 0);
    return () => window.clearTimeout(resetTimeoutId);
  }, [apiUrl, token]);

  useEffect(() => {
    const migrationController = new AbortController();
    const syncController = new AbortController();
    const backupController = new AbortController();
    const snapshotGeneration = snapshotGenerationRef.current;
    migrationSnapshotAbortRef.current = migrationController;
    syncSnapshotAbortRef.current = syncController;
    backupSnapshotAbortRef.current = backupController;
    function loadInitialSnapshot(): void {
      void fetchMigrations(migrationController.signal, snapshotGeneration);
      void fetchSyncJobs(syncController.signal, snapshotGeneration);
      void fetchBackupJobs(backupController.signal, snapshotGeneration);
    }
    const timeoutId = window.setTimeout(loadInitialSnapshot, 0);
    return () => {
      window.clearTimeout(timeoutId);
      migrationController.abort();
      syncController.abort();
      backupController.abort();
      if (migrationSnapshotAbortRef.current === migrationController) {
        migrationSnapshotAbortRef.current = null;
      }
      if (syncSnapshotAbortRef.current === syncController) {
        syncSnapshotAbortRef.current = null;
      }
      if (backupSnapshotAbortRef.current === backupController) {
        backupSnapshotAbortRef.current = null;
      }
    };
  }, [fetchMigrations, fetchSyncJobs, fetchBackupJobs]);

  useEffect(() => {
    if (activeTab !== 'backup') return;
    const controller = new AbortController();
    const timeoutId = window.setTimeout(() => {
      void fetchBackupJobs(controller.signal);
    }, 0);
    return () => {
      window.clearTimeout(timeoutId);
      controller.abort();
    };
  }, [activeTab, fetchBackupJobs]);

  useEffect(() => {
    const controller = new AbortController();
    const snapshotGeneration = snapshotGenerationRef.current;
    void connectSseLoop({
      url: `${apiUrl}/api/migration/stream`,
      signal: controller.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (snapshotGeneration !== snapshotGenerationRef.current) return;
          if (event === 'migrations' && data) {
            try {
              const migrations = JSON.parse(data) || [];
              hasMigrationStreamDataRef.current = true;
              migrationSnapshotAbortRef.current?.abort();
              setMigrations(migrations);
              setError('');
              setLoading(false);
            } catch {
              /* ignore malformed frame */
            }
          } else if (event === 'error') {
            setError(tRef.current('migrations.connectionError'));
            setLoading(false);
          }
        },
        onError: () => {
          if (snapshotGeneration !== snapshotGenerationRef.current) return;
          setError(tRef.current('migrations.connectionError'));
          setLoading(false);
        },
      },
    });
    return () => controller.abort();
  }, [apiUrl, token]);

  useEffect(() => {
    const controller = new AbortController();
    const snapshotGeneration = snapshotGenerationRef.current;
    void connectSseLoop({
      url: `${apiUrl}/api/sync/stream`,
      signal: controller.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (snapshotGeneration !== snapshotGenerationRef.current) return;
          if (event === 'sync_jobs' && data) {
            try {
              const jobs = JSON.parse(data) || [];
              hasSyncStreamDataRef.current = true;
              syncSnapshotAbortRef.current?.abort();
              setSyncJobs(jobs);
              setSyncError('');
              setSyncLoading(false);
            } catch {
              /* ignore */
            }
          }
        },
        onError: () => {
          if (snapshotGeneration !== snapshotGenerationRef.current) return;
          setSyncError(tRef.current('sync.loadFailed'));
          setSyncLoading(false);
        },
      },
    });
    return () => controller.abort();
  }, [apiUrl, token]);

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();

    const ok = await confirm({ message: t('migrations.deleteConfirm') });
    if (!ok) return;

    setDeleteLoading(id);
    try {
      const result = await apiJson(`${apiUrl}/api/migration/${id}`, {
        method: 'DELETE',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('migrations.deleteFailed')));
      setMigrations((prev) => prev.filter((m) => m.id !== id));
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('migrations.deleteError'), 'error');
    } finally {
      setDeleteLoading(null);
    }
  };

  const matchesMigrationFilter = useCallback((m: Migration) => {
    const matchSearch = !searchTerm || [m.id, m.source_provider, m.target_provider, m.source_url, m.target_url]
      .some((val) => val && String(val).toLowerCase().includes(searchTerm.toLowerCase()));
    if (!matchSearch) return false;

    if (statusFilter === 'active') return m.status === 'RUNNING' || m.status === 'INDEXING';
    if (statusFilter === 'completed') return m.status === 'COMPLETED' || m.status === 'COMPLETED_WITH_ERRORS';
    if (statusFilter === 'failed') return m.status === 'FAILED' || m.status === 'CANCELLED';
    if (statusFilter === 'paused') return m.status === 'PAUSED' || m.status === 'PAUSED_CONNECTION_LOSS';
    return true;
  }, [searchTerm, statusFilter]);

  const isDeletableMigration = (status: string) =>
    !['RUNNING', 'INDEXING', 'VERIFYING', 'SCHEDULED', 'PENDING'].includes(status);

  const handleDeleteAllMigrations = async () => {
    const deletable = migrations.filter(matchesMigrationFilter).filter((m) => isDeletableMigration(m.status));
    const ids = deletable.map((m) => m.id);

    if (ids.length === 0) return;

    const ok = await confirm({
      message: t('migrations.deleteAllConfirm', { count: ids.length }),
      confirmLabel: t('migrations.deleteAll'),
    });
    if (!ok) return;

    setDeleteAllLoading(true);
    let failCount = 0;
    let firstErrorCode: string | null = null;
    const successIds = new Set<string>();

    for (const id of ids) {
      try {
        const result = await apiJson(`${apiUrl}/api/migration/${id}`, {
          method: 'DELETE',
          headers: {
            Authorization: `Bearer ${token}`,
          },
        });
        if (result.ok === true) {
          successIds.add(id);
        } else {
          failCount++;
          if (
            !firstErrorCode
            && !result.networkError
            && result.errorCode
            && result.errorCode !== 'UNKNOWN'
          ) {
            firstErrorCode = result.errorCode;
          }
        }
      } catch {
        failCount++;
      }
    }

    if (successIds.size > 0) {
      setMigrations((prev) => prev.filter((m) => !successIds.has(m.id)));
    }

    if (failCount > 0) {
      toast(firstErrorCode ? translateApiError(firstErrorCode) : t('migrations.deleteError'), 'error');
    }

    setDeleteAllLoading(false);
  };

  const handleMigrationControl = async (migration: Migration, e: React.MouseEvent) => {
    e.stopPropagation();
    const action = ['PAUSED', 'PAUSED_CONNECTION_LOSS'].includes(migration.status) ? 'resume' : 'pause';
    setControlLoading(migration.id);
    try {
      const result = await apiJson(`${apiUrl}/api/migration/${migration.id}/${action}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('dashboard.actionFailedMsg', { action })));
      setMigrations((current) => current.map((item) => item.id === migration.id
        ? { ...item, status: action === 'pause' ? 'PAUSED' : 'RUNNING' }
        : item));
    } catch (err) {
      toast(err instanceof Error ? err.message : t('dashboard.actionFailedMsg', { action }), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  const totalMigrations = migrations.length;
  const totalSyncs = syncJobs.length;
  const totalBackups = backupJobs.length;
  const totalTransfers = totalMigrations + totalSyncs + totalBackups;
  const initialDataLoading = loading || syncLoading || backupLoading;

  const activeMigrations = migrations.filter(m => m.status === 'RUNNING' || m.status === 'INDEXING').length;
  const activeSyncs = syncJobs.filter(s => s.status === 'RUNNING' || s.status === 'INDEXING').length;
  const activeBackups = backupJobs.filter((job) => ['QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING'].includes(job.status)).length;
  const activeTotal = activeMigrations + activeSyncs + activeBackups;

  const completedMigrations = migrations.filter(m => m.status === 'COMPLETED' || m.status === 'COMPLETED_WITH_ERRORS').length;
  const failedMigrations = migrations.filter(m => m.status === 'FAILED' || m.status === 'CANCELLED').length;

  const completedSyncs = syncJobs.filter(s => s.status === 'COMPLETED' || (s.status === 'IDLE' && s.last_run_status !== 'FAILED')).length;
  const failedSyncs = syncJobs.filter(s => s.status === 'FAILED' || (s.status === 'IDLE' && s.last_run_status === 'FAILED')).length;

  const totalCompleted = completedMigrations + completedSyncs;
  const totalFailed = failedMigrations + failedSyncs;

  const successRate = (totalCompleted + totalFailed) > 0 
    ? Math.round((totalCompleted / (totalCompleted + totalFailed)) * 100) 
    : 100;

  const totalBytesMigrated = migrations.reduce((acc, m) => acc + (m.processed_bytes || 0), 0)
    + syncJobs.reduce((acc, s) => acc + (s.processed_bytes || 0), 0)
    + backupJobs.reduce((acc, job) => acc + (job.processed_bytes || 0), 0);

  // The shared Tabs component cannot yet preserve this tablist's count badges and adjacent filters.
  // Keep its horizontal keyboard contract in sync when those extension points are added.
  function selectTab(tab: DashboardTab): void {
    if (tab === 'fileManager') {
      onOpenFilemanagerRoot?.();
      return;
    }
    setActiveTab(tab);
    tabRefs.current[tab]?.focus();
  }

  function handleTabKeyDown(event: React.KeyboardEvent<HTMLButtonElement>, tab: DashboardTab): void {
    let nextTab: DashboardTab;

    switch (event.key) {
      case 'ArrowRight':
        nextTab = tab === 'migrations' ? 'sync' : tab === 'sync' ? 'backup' : tab === 'backup' ? 'fileManager' : 'migrations';
        break;
      case 'ArrowLeft':
        nextTab = tab === 'migrations' ? 'fileManager' : tab === 'sync' ? 'migrations' : tab === 'backup' ? 'sync' : 'backup';
        break;
      case 'Home':
        nextTab = 'migrations';
        break;
      case 'End':
        nextTab = 'fileManager';
        break;
      default:
        return;
    }

    event.preventDefault();
    selectTab(nextTab);
  }

  return (
    <div className="w-full space-y-6">
      
      {/* Welcome Banner */}
      <section className="ui-section-elevated p-6">
        <div className="flex flex-col items-start justify-between gap-6 md:flex-row md:items-center">
          <div className="space-y-2">
            <p className="text-xs font-medium text-[var(--color-text-muted)]">{t('migrations.tagline')}</p>
            <h1 className="font-display text-2xl font-semibold tracking-tight text-[var(--color-text-primary)]">
              {t('migrations.welcome', { name: user?.display_name || t('common.user') })}
            </h1>
            <p className="max-w-xl text-sm text-[var(--color-text-secondary)]">
              {t('migrations.welcomeSub')}
            </p>
          </div>
          <div className="shrink-0">
            <button
              onClick={onStartNewMigration}
              className="ui-button-primary px-4 py-2 text-sm font-medium hover:opacity-90"
            >
              {t('migrations.newMigration')}
            </button>
          </div>
        </div>
      </section>

      {initialDataLoading ? (
        <div className="flex flex-col items-center justify-center py-20 gap-4" aria-live="polite">
          <LoadingIndicator label={t('migrations.loadingData')} />
        </div>
      ) : <>
      {/* Stats Widgets Grid */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Total Bytes */}
        <div className="ui-card p-4 flex items-center gap-4">
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.dataTransferred')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {formatBytes(totalBytesMigrated)}
            </span>
          </div>
        </div>

        {/* Total Migrations + Sync Jobs */}
        <div className="ui-card p-4 flex items-center gap-4">
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.migrations')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {totalTransfers}
            </span>
          </div>
        </div>

        {/* Active Transits */}
        <div className="ui-card p-4 flex items-center gap-4">
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.active')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {activeTotal}
            </span>
          </div>
        </div>

        {/* Success Rate */}
        <div className="ui-card p-4 flex items-center gap-4">
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.successRate')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {formatPercent(successRate)}
            </span>
          </div>
        </div>
      </div>

      {/* Main Section with Segmented Pill Tabs & Search Filter Bar */}
      <div className="ui-card min-h-[560px] space-y-6 p-4 sm:p-6">

        {/* Navigation Tabs & Controls Header */}
        <div className="flex flex-col sm:flex-row items-stretch sm:items-center justify-between pb-4 gap-4">
          {/* Segmented Pill Tabs */}
          <div className="flex w-full max-w-full items-center gap-1 border-b border-[var(--color-border)] overflow-x-auto pb-0.5 sm:pb-0 -mb-px" role="tablist" aria-label={t('migrations.title')}>
            <button
                ref={(node) => { tabRefs.current.migrations = node; }}
                id="migrations-tab"
              onClick={() => setActiveTab('migrations')}
              role="tab"
              aria-selected={activeTab === 'migrations'}
              aria-controls="migrations-panel"
              tabIndex={activeTab === 'migrations' ? 0 : -1}
              onKeyDown={(event) => handleTabKeyDown(event, 'migrations')}
              className={`flex items-center gap-2 px-3 py-2 text-sm font-medium shrink-0 whitespace-nowrap -mb-px ${
                activeTab === 'migrations'
                  ? 'border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]'
                  : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
              }`}
            >
              <span>{t('sync.tabMigrations')}</span>
              <span className={`shrink-0 px-2 py-0.5 text-[10px] ${activeTab === 'migrations' ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]'}`}>
                {migrations.length}
              </span>
            </button>
            <button
                ref={(node) => { tabRefs.current.sync = node; }}
              id="sync-tab"
              onClick={() => setActiveTab('sync')}
              role="tab"
              aria-selected={activeTab === 'sync'}
              aria-controls="sync-panel"
              tabIndex={activeTab === 'sync' ? 0 : -1}
              onKeyDown={(event) => handleTabKeyDown(event, 'sync')}
              className={`flex items-center gap-2 px-3 py-2 text-sm font-medium shrink-0 whitespace-nowrap -mb-px ${
                activeTab === 'sync'
                  ? 'border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]'
                  : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
              }`}
            >
              <span>{t('sync.tabSyncs')}</span>
              <span className={`shrink-0 px-2 py-0.5 text-[10px] ${activeTab === 'sync' ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]'}`}>
                {syncJobs.length}
              </span>
            </button>
            <button
              ref={(node) => { tabRefs.current.backup = node; }}
              id="backup-tab"
              onClick={() => setActiveTab('backup')}
              role="tab"
              aria-selected={activeTab === 'backup'}
              aria-controls="backup-panel"
              tabIndex={activeTab === 'backup' ? 0 : -1}
              onKeyDown={(event) => handleTabKeyDown(event, 'backup')}
              className={`flex items-center gap-2 px-3 py-2 text-sm font-medium shrink-0 whitespace-nowrap -mb-px ${
                activeTab === 'backup'
                  ? 'border-b-2 border-[var(--color-text-primary)] text-[var(--color-text-primary)]'
                  : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
              }`}
            >
              <span>{t('backup.tab')}</span>
              <span className={`shrink-0 px-2 py-0.5 text-[10px] ${activeTab === 'backup' ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-muted)]'}`}>
                {backupJobs.length}
              </span>
            </button>
            <button
              ref={(node) => { tabRefs.current.fileManager = node; }}
              id="filemanager-tab"
              onClick={() => onOpenFilemanagerRoot?.()}
              role="tab"
              aria-selected={false}
              tabIndex={-1}
              onKeyDown={(event) => handleTabKeyDown(event, 'fileManager')}
              className="flex items-center gap-2 px-3 py-2 text-sm font-medium text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0 whitespace-nowrap -mb-px"
            >
              <span>{t('sync.tabFileManager')}</span>
            </button>
          </div>

          {/* Search Input & Status Filter Dropdown */}
          <div className="flex items-center gap-2.5 w-full sm:w-auto">
            <div className="relative flex-1 sm:w-64">
              <input
                type="text"
                value={searchTerm}
                onChange={(e) => setSearchTerm(e.target.value)}
                placeholder={t('migrations.searchPlaceholder')}
                aria-label={t('migrations.searchLabel')}
                className="ui-input h-9 w-full px-3 text-sm text-[var(--color-text-primary)]"
              />
            </div>

            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
              aria-label={t('migrations.statusFilterLabel')}
              className="ui-input h-9 cursor-pointer px-3 text-xs font-mono text-[var(--color-text-secondary)]"
            >
              <option value="all">{t('migrations.filterAll')}</option>
              <option value="active">{t('migrations.filterActive')}</option>
              <option value="completed">{t('migrations.filterCompleted')}</option>
              <option value="failed">{t('migrations.filterFailed')}</option>
              <option value="paused">{t('migrations.filterPaused')}</option>
            </select>

          </div>
        </div>

        {/* Filtered Data Rendering */}
        <div
          id={activeTab === 'sync' ? 'sync-panel' : activeTab === 'backup' ? 'backup-panel' : 'migrations-panel'}
          role="tabpanel"
          aria-labelledby={activeTab === 'sync' ? 'sync-tab' : activeTab === 'backup' ? 'backup-tab' : 'migrations-tab'}
          className="min-h-[360px]"
        >
        {(() => {
          const filteredMigrations = migrations.filter(matchesMigrationFilter);
          const deletableMigrations = filteredMigrations.filter((m) => isDeletableMigration(m.status));

          if (activeTab === 'sync') {
            return (
              <SyncList
                apiUrl={apiUrl}
                token={token}
                syncJobs={syncJobs}
                searchTerm={searchTerm}
                statusFilter={statusFilter}
                loading={syncLoading}
                error={syncError}
                setSyncJobs={setSyncJobs}
                onSelectActiveSync={onSelectActiveSync}
                onStartNewSync={onStartNewMigration}
                onOpenFileManager={onOpenFileManager}
              />
            );
          }

          if (activeTab === 'backup') {
            return (
              <BackupList
                apiUrl={apiUrl}
                token={token}
                backupJobs={backupJobs}
                loading={backupLoading}
                error={backupError}
                setBackupJobs={setBackupJobs}
                onSelectActiveBackup={onSelectActiveBackup}
                onOpenFileManager={onOpenFileManager}
              />
            );
          }

          if (loading) {
            return (
              <div className="flex flex-col items-center justify-center py-20 gap-4">
                <LoadingIndicator label={t('migrations.loadingData')} />
              </div>
            );
          }

          if (error) {
            return (
              <div className="ui-card p-4 bg-[var(--color-error-bg)] text-[var(--color-error-text)] border-[var(--color-error-border)] text-xs font-mono text-center">
                {error}
              </div>
            );
          }

          if (filteredMigrations.length === 0) {
            return (
              <div className="ui-card text-center py-16 border-2 border-dashed bg-[var(--color-bg-tertiary)]">
                <p className="font-display font-bold text-[var(--color-text-secondary)]">{t('migrations.noMigrations')}</p>
                <p className="text-xs text-[var(--color-text-muted)] mt-1 mb-5 leading-relaxed max-w-md mx-auto">{t('migrations.noMigrationsSub')}</p>
                <button
                  onClick={onStartNewMigration}
                  className="ui-button-primary px-5 py-2.5 text-xs font-bold font-mono uppercase tracking-wider hover:opacity-90"
                >
                  {t('migrations.startFirst')}
                </button>
              </div>
            );
          }

          return (
            <div className="overflow-x-auto">
              <table className="ui-responsive-table w-full text-left border-collapse">
                <thead>
                  <tr className="border-b border-[var(--color-border)]/60 text-[10px] font-bold text-[var(--color-text-muted)] uppercase font-mono tracking-wider">
                    <th className="py-4.5 px-4 font-semibold">{t('migrations.createdAt')}</th>
                    <th className="py-4.5 px-4 font-semibold">{t('migrations.sourceTarget')}</th>
                    <th className="py-4.5 px-4 font-semibold">{t('migrations.status')}</th>
                    <th className="py-4.5 px-4 font-semibold">{t('migrations.progress')}</th>
                    <th className="py-4.5 px-4 font-semibold text-right">{t('migrations.actions')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-[var(--color-border-light)]">
                  {filteredMigrations.map((mig) => {
                    const createdDate = formatDateTime(mig.created_at);

                  return (
                     <tr key={mig.id} className="transition-colors hover:bg-[var(--color-bg-tertiary)]">
                      {/* Date */}
                      <td data-label={t('migrations.createdAt')} className="py-4 px-4 whitespace-nowrap">
                        <div className="flex items-center gap-2 text-xs font-mono text-[var(--color-text-secondary)]">
                          <CalendarDaysIcon className="size-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                          {createdDate}
                        </div>
                      </td>

                      {/* Providers */}
                      <td data-label={t('migrations.sourceTarget')} className="py-4 px-4">
                         <button type="button" onClick={() => onSelectActiveMigration(mig.id)} className="flex w-full items-center gap-2.5 text-left" aria-label={t('migrations.sourceTarget')}>
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize leading-snug">
                              {mig.source_provider}
                            </span>
                            <span className="text-[10px] text-[var(--color-text-muted)] max-w-[120px] truncate block">
                              {mig.source_url || t('migrations.oauth')}
                            </span>
                            <span className="text-[10px] font-mono text-[var(--color-text-secondary)] max-w-[120px] truncate block" title={mig.selected_paths?.join(', ') || '/'}>
                              {t('sync.sourcePath')}: {mig.selected_paths?.length ? mig.selected_paths.join(', ') : '/'}
                            </span>
                          </div>
                           
                           <span className="text-[var(--color-text-muted)]" aria-hidden="true">→</span>
                          
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize leading-snug">
                              {mig.target_provider}
                            </span>
                            <span className="text-[10px] text-[var(--color-text-muted)] max-w-[120px] truncate block">
                              {mig.target_url || t('migrations.oauth')}
                            </span>
                            <span className="text-[10px] font-mono text-[var(--color-text-secondary)] max-w-[120px] truncate block" title={mig.target_dir || '/'}>
                              {t('sync.targetPath')}: {mig.target_dir || '/'}
                            </span>
                          </div>
                        </button>
                      </td>

                      {/* Status */}
                      <td data-label={t('migrations.status')} className="py-4 px-4 whitespace-nowrap">
                        <StatusBadge status={mig.status} size="sm" />
                      </td>

                      {/* Progress */}
                      <td data-label={t('migrations.progress')} className="py-4 px-4">
                        <div className="flex flex-col gap-1.5 min-w-[120px]">
                          <div className="flex items-center justify-between text-[10px] font-mono text-[var(--color-text-muted)]">
                            <span>
                              {t('migrations.filesCount', { processed: mig.processed_files, total: mig.total_files })}
                            </span>
                            {mig.total_bytes > 0 && (
                              <span>
                                {formatBytes(mig.processed_bytes)}
                              </span>
                            )}
                          </div>
                          
                          {/* Progress bar */}
                           <ProgressBar
                             label={t('migrations.progress')}
                             valueText={t('migrations.filesCount', { processed: mig.processed_files, total: mig.total_files })}
                             className="h-1.5"
                             value={mig.total_files > 0 ? (mig.processed_files / mig.total_files) * 100 : 0}
                              indicatorClassName={
                                mig.status === 'FAILED'
                                  ? 'ui-progress-error'
                                  : mig.status === 'COMPLETED_WITH_ERRORS'
                                    ? 'ui-progress-warning'
                                    : mig.status === 'COMPLETED'
                                      ? 'ui-progress-success'
                                      : 'bg-[var(--color-bg-inverse)]'
                              }
                           />
                        </div>
                      </td>

                      {/* Actions */}
                       <td data-label={t('migrations.actions')} className="py-4 px-4 text-right whitespace-nowrap">
                        <div className="flex justify-end items-center gap-2">
                          {(() => {
                            const isPaused = ['PAUSED', 'PAUSED_CONNECTION_LOSS'].includes(mig.status);
                            const label = isPaused ? t('dashboard.resume') : t('dashboard.pause');
                            return (
                          <button
                            onClick={(e) => handleMigrationControl(mig, e)}
                            disabled={controlLoading === mig.id || !['RUNNING', 'INDEXING', 'PAUSED', 'PAUSED_CONNECTION_LOSS'].includes(mig.status)}
                            className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-30"
                            aria-label={label}
                            title={label}
                          >
                            {controlLoading === mig.id
                              ? <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                              : isPaused
                                ? <PlayIcon className="size-4" aria-hidden="true" />
                              : <PauseIcon className="size-4" aria-hidden="true" />}
                          </button>
                            );
                          })()}
                          <button
                            onClick={(e) => handleDelete(mig.id, e)}
                            disabled={deleteLoading === mig.id || !isDeletableMigration(mig.status)}
                            className="ui-button-secondary p-2 text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] disabled:opacity-30"
                            aria-label={t('migrations.deleteMigration')}
                            title={t('migrations.deleteMigration')}
                          >
                            {deleteLoading === mig.id
                              ? <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                              : <TrashIcon className="size-4" aria-hidden="true" />}
                          </button>
                          {mig.source_profile_id && onOpenFileManager && (
                            <button
                              type="button"
                              onClick={(event) => { event.stopPropagation(); onOpenFileManager(mig.source_profile_id!, commonParentPath(mig.selected_paths)); }}
                              className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                              aria-label={t('files.openSource')}
                              title={t('files.openSource')}
                            >
                              <CloudArrowDownIcon className="size-4" aria-hidden="true" />
                            </button>
                          )}
                          {mig.target_profile_id && onOpenFileManager && (
                            <button
                              type="button"
                              onClick={(event) => { event.stopPropagation(); onOpenFileManager(mig.target_profile_id!, mig.target_dir || '/'); }}
                              className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                              aria-label={t('files.openTarget')}
                              title={t('files.openTarget')}
                            >
                              <CloudArrowUpIcon className="size-4" aria-hidden="true" />
                            </button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {deletableMigrations.length > 3 && (
              <div className="p-4 border-t border-[var(--color-border-subtle)] flex justify-end">
                <button
                  onClick={handleDeleteAllMigrations}
                  disabled={deleteAllLoading}
                  className="ui-button-secondary text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] flex items-center gap-2 px-3 py-1.5 text-xs font-medium disabled:opacity-30"
                  aria-label={t('migrations.deleteAll')}
                  title={t('migrations.deleteAll')}
                >
                  {deleteAllLoading ? (
                    <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                  ) : (
                    <TrashIcon className="size-4" aria-hidden="true" />
                  )}
                  <span>{t('migrations.deleteAll')}</span>
                </button>
              </div>
            )}
            </div>
          );
        })()}
        </div>

      </div>
      </>}
    </div>
  );
}

function SyncList({
  apiUrl,
  token,
  syncJobs,
  searchTerm = '',
  statusFilter = 'all',
  loading,
  error,
  setSyncJobs,
  onSelectActiveSync,
  onStartNewSync,
  onOpenFileManager,
}: {
  apiUrl: string;
  token: string;
  syncJobs: SyncJob[];
  searchTerm?: string;
  statusFilter?: string;
  loading: boolean;
  error: string;
  setSyncJobs: React.Dispatch<React.SetStateAction<SyncJob[]>>;
  onSelectActiveSync?: (id: string) => void;
  onStartNewSync: () => void;
  onOpenFileManager?: (profileId: string, path: string) => void;
}) {
  const [deleteLoading, setDeleteLoading] = useState<string | null>(null);
  const [deleteAllLoading, setDeleteAllLoading] = useState<boolean>(false);
  const [controlLoading, setControlLoading] = useState<string | null>(null);

  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const confirm = useConfirm();
  const toast = useToast();

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    const ok = await confirm({ message: t('sync.deleteConfirm') });
    if (!ok) return;

    setDeleteLoading(id);
    try {
      const result = await apiJson(`${apiUrl}/api/sync/${id}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('sync.deleteFailed')));
      setSyncJobs((prev) => prev.filter((j) => j.id !== id));
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('sync.deleteFailed'), 'error');
    } finally {
      setDeleteLoading(null);
    }
  };

  const isDeletableSync = (status: string) =>
    !['RUNNING', 'INDEXING', 'VERIFYING'].includes(status);

  const matchesSyncFilter = useCallback((job: SyncJob) => {
    const matchSearch = !searchTerm || [job.id, job.source_provider, job.target_provider, job.source_url, job.target_url]
      .some((val) => val && String(val).toLowerCase().includes(searchTerm.toLowerCase()));
    if (!matchSearch) return false;

    if (statusFilter === 'active') return job.status === 'RUNNING' || job.status === 'INDEXING';
    if (statusFilter === 'completed') return job.status === 'COMPLETED' || (job.status === 'IDLE' && job.last_run_status !== 'FAILED');
    if (statusFilter === 'failed') return job.status === 'FAILED' || (job.status === 'IDLE' && job.last_run_status === 'FAILED');
    if (statusFilter === 'paused') return job.status === 'PAUSED' || job.status === 'PAUSED_CONNECTION_LOSS';
    return true;
  }, [searchTerm, statusFilter]);

  const filteredSyncJobs = syncJobs.filter(matchesSyncFilter);
  const deletableSyncJobs = filteredSyncJobs.filter((j) => isDeletableSync(j.status));

  const handleDeleteAllSyncs = async () => {
    const ids = deletableSyncJobs.map((j) => j.id);
    if (ids.length === 0) return;

    const ok = await confirm({
      message: t('sync.deleteAllConfirm', { count: ids.length }),
      confirmLabel: t('sync.deleteAll'),
    });
    if (!ok) return;

    setDeleteAllLoading(true);
    let failCount = 0;
    let firstErrorCode: string | null = null;
    const successIds = new Set<string>();

    for (const id of ids) {
      try {
        const result = await apiJson(`${apiUrl}/api/sync/${id}`, {
          method: 'DELETE',
          headers: { Authorization: `Bearer ${token}` },
        });
        if (result.ok === true) {
          successIds.add(id);
        } else {
          failCount++;
          if (
            !firstErrorCode
            && !result.networkError
            && result.errorCode
            && result.errorCode !== 'UNKNOWN'
          ) {
            firstErrorCode = result.errorCode;
          }
        }
      } catch {
        failCount++;
      }
    }

    if (successIds.size > 0) {
      setSyncJobs((prev) => prev.filter((j) => !successIds.has(j.id)));
    }

    if (failCount > 0) {
      toast(firstErrorCode ? translateApiError(firstErrorCode) : t('sync.deleteFailed'), 'error');
    }

    setDeleteAllLoading(false);
  };

  const handleSyncControl = async (job: SyncJob, e: React.MouseEvent) => {
    e.stopPropagation();
    const action = job.status === 'PAUSED' ? 'resume' : 'pause';
    setControlLoading(job.id);
    try {
      const result = await apiJson(`${apiUrl}/api/sync/${job.id}/${action}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('dashboard.actionFailedMsg', { action })));
      if (action === 'pause') {
        setSyncJobs((current) => current.map((item) => item.id === job.id
          ? { ...item, status: 'PAUSED' }
          : item));
      }
    } catch (err) {
      toast(err instanceof Error ? err.message : t('dashboard.actionFailedMsg', { action }), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-4">
        <p className="text-xs font-mono text-[var(--color-text-muted)]">{t('common.loading')}</p>
      </div>
    );
  }

  if (error) {
    return (
      <div className="ui-card p-4 bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)] text-xs font-mono text-center">
        {error}
      </div>
    );
  }

  if (filteredSyncJobs.length === 0) {
    return (
      <div className="ui-card text-center py-16 border-2 border-dashed bg-[var(--color-bg-tertiary)]">
        <p className="font-display font-bold text-[var(--color-text-secondary)]">{t('sync.noSyncJobs')}</p>
        <p className="text-[10px] text-[var(--color-text-muted)] font-mono mt-1 mb-5">{t('sync.noSyncSub')}</p>
        <button
          onClick={onStartNewSync}
          className="ui-button-primary px-5 py-2.5 text-xs font-bold font-mono uppercase tracking-wider hover:opacity-90"
        >
          {t('sync.startFirst')}
        </button>
      </div>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left border-collapse min-w-[600px]">
        <thead>
          <tr className="border-b border-[var(--color-border)]/60 text-[10px] font-bold text-[var(--color-text-muted)] uppercase font-mono tracking-wider">
            <th className="py-4.5 px-4 font-semibold">{t('migrations.createdAt')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('migrations.sourceTarget')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('migrations.status')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('migrations.progress')}</th>
            <th className="py-4.5 px-4 font-semibold text-right">{t('migrations.actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--color-border-light)]">
          {filteredSyncJobs.map((job) => (
             <tr key={job.id} className="transition-colors hover:bg-[var(--color-bg-tertiary)]">
              <td className="py-4 px-4 whitespace-nowrap">
                <div className="flex items-center gap-2 text-xs font-mono text-[var(--color-text-secondary)]">
                  <CalendarDaysIcon className="size-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                  {formatDateTime(job.created_at)}
                </div>
              </td>
              <td className="py-4 px-4">
                 <button type="button" onClick={() => onSelectActiveSync?.(job.id)} className="flex w-full items-center gap-2.5 text-left" aria-label={t('migrations.sourceTarget')}>
                  <div className="flex flex-col text-left min-w-0">
                    <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize">
                      {job.source_provider}
                    </span>
                    <span className="text-[10px] text-[var(--color-text-muted)] max-w-[130px] truncate block">
                      {job.source_url || t('migrations.oauth')}
                    </span>
                    <span className="text-[10px] font-mono text-[var(--color-text-secondary)] max-w-[130px] truncate block" title={job.selected_paths?.join(', ') || '/'}>
                      {t('sync.sourcePath')}: {job.selected_paths && job.selected_paths.length > 0 ? job.selected_paths.join(', ') : '/'}
                    </span>
                  </div>
                  <span className="text-[var(--color-text-muted)]" aria-hidden="true">→</span>
                  <div className="flex flex-col text-left min-w-0">
                    <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize">
                      {job.target_provider}
                    </span>
                    <span className="text-[10px] text-[var(--color-text-muted)] max-w-[130px] truncate block">
                      {job.target_url || t('migrations.oauth')}
                    </span>
                    <span className="text-[10px] font-mono text-[var(--color-text-secondary)] max-w-[130px] truncate block" title={job.target_dir || '/'}>
                      {t('sync.targetPath')}: {job.target_dir || '/'}
                    </span>
                  </div>
                </button>
              </td>
              <td className="py-4 px-4 whitespace-nowrap">
                <StatusBadge status={job.status} size="sm" />
              </td>
              <td className="py-4 px-4 min-w-[140px]">
                {(() => {
                  const totalBytes = job.total_bytes ?? 0;
                  const processedBytes = job.processed_bytes ?? 0;
                  const liveBytes = job.live_bytes ?? processedBytes;
                  const displayedBytes = totalBytes > 0
                    ? Math.min(totalBytes, Math.max(processedBytes, liveBytes))
                    : processedBytes;
                  const progress = totalBytes > 0
                    ? Math.min(100, Math.round((displayedBytes / totalBytes) * 100))
                    : job.total_files > 0
                      ? Math.min(100, Math.round((job.processed_files / job.total_files) * 100))
                      : 0;
                  const color = job.status === 'FAILED' ? 'bg-[var(--color-error-text)]' : job.status === 'COMPLETED_WITH_ERRORS' ? 'bg-[var(--color-warning-border)]' : progress === 100 ? 'bg-[var(--color-success-text)]' : 'bg-[var(--color-bg-inverse)]';
                  return (
                    <div className="flex flex-col gap-1.5">
                      <div className="flex items-center justify-between text-[10px] font-mono text-[var(--color-text-muted)]">
                        <span>{t('migrations.filesCount', { processed: job.processed_files, total: job.total_files })}</span>
                        {totalBytes > 0 && <span>{formatBytes(displayedBytes)}</span>}
                      </div>
                      <ProgressBar label={t('migrations.progress')} value={progress} valueText={t('migrations.filesCount', { processed: job.processed_files, total: job.total_files })} className="h-1.5" indicatorClassName={color} />
                    </div>
                  );
                })()}
              </td>
              <td className="py-4 px-4 text-right whitespace-nowrap">
                <div className="flex justify-end items-center gap-2">
                  {(() => {
                    const isPaused = job.status === 'PAUSED';
                    const label = isPaused ? t('sync.resume') : t('sync.pause');
                    return (
                  <button
                    onClick={(e) => handleSyncControl(job, e)}
                    disabled={controlLoading === job.id || !['IDLE', 'INDEXING', 'RUNNING', 'VERIFYING', 'PAUSED'].includes(job.status)}
                    className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-30"
                    aria-label={label}
                    title={label}
                  >
                    {controlLoading === job.id
                      ? <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                      : isPaused
                        ? <PlayIcon className="size-4" aria-hidden="true" />
                        : <PauseIcon className="size-4" aria-hidden="true" />}
                  </button>
                    );
                  })()}
                  <button
                    onClick={(e) => handleDelete(job.id, e)}
                    disabled={deleteLoading === job.id}
                    className="ui-button-secondary p-2 text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] disabled:opacity-30"
                    aria-label={t('sync.deleteJob')}
                    title={t('sync.deleteJob')}
                  >
                    {deleteLoading === job.id
                      ? <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                      : <TrashIcon className="size-4" aria-hidden="true" />}
                  </button>
                  {job.source_profile_id && onOpenFileManager && (
                    <button
                      type="button"
                      onClick={(event) => { event.stopPropagation(); onOpenFileManager(job.source_profile_id!, commonParentPath(job.selected_paths)); }}
                      className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                      aria-label={t('files.openSource')}
                      title={t('files.openSource')}
                    >
                      <CloudArrowDownIcon className="size-4" aria-hidden="true" />
                    </button>
                  )}
                  {job.target_profile_id && onOpenFileManager && (
                    <button
                      type="button"
                      onClick={(event) => { event.stopPropagation(); onOpenFileManager(job.target_profile_id!, job.target_dir || '/'); }}
                      className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                      aria-label={t('files.openTarget')}
                      title={t('files.openTarget')}
                    >
                      <CloudArrowUpIcon className="size-4" aria-hidden="true" />
                    </button>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {deletableSyncJobs.length > 3 && (
        <div className="p-4 border-t border-[var(--color-border-subtle)] flex justify-end">
          <button
            onClick={handleDeleteAllSyncs}
            disabled={deleteAllLoading}
            className="ui-button-secondary text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] flex items-center gap-2 px-3 py-1.5 text-xs font-medium disabled:opacity-30"
            aria-label={t('sync.deleteAll')}
            title={t('sync.deleteAll')}
          >
            {deleteAllLoading ? (
              <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
            ) : (
              <TrashIcon className="size-4" aria-hidden="true" />
            )}
            <span>{t('sync.deleteAll')}</span>
          </button>
        </div>
      )}
    </div>
  );
}

function BackupList({
  apiUrl,
  token,
  backupJobs,
  loading,
  error,
  setBackupJobs,
  onSelectActiveBackup,
  onOpenFileManager,
}: {
  apiUrl: string;
  token: string;
  backupJobs: BackupJob[];
  loading: boolean;
  error: string;
  setBackupJobs: React.Dispatch<React.SetStateAction<BackupJob[]>>;
  onSelectActiveBackup?: (id: string) => void;
  onOpenFileManager?: (profileId: string, path: string) => void;
}) {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();
  const toast = useToast();
  const confirm = useConfirm();
  const [controlLoading, setControlLoading] = useState<string | null>(null);
  const [browseJobID, setBrowseJobID] = useState<string | null>(null);

  const handleAction = async (job: BackupJob, action: 'run' | 'pause' | 'resume') => {
    setControlLoading(`${job.id}:${action}`);
    try {
      const result = await apiJson(`${apiUrl}/api/backup/${job.id}/${action}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('backup.actionFailed')));
      setBackupJobs((current) => current.map((item) => item.id === job.id
        ? { ...item, status: action === 'pause' ? 'PAUSED' : action === 'resume' ? 'IDLE' : 'QUEUED' }
        : item));
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('backup.actionFailed'), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  const handleDelete = async (job: BackupJob) => {
    const accepted = await confirm({ title: t('backup.deleteTitle'), message: t('backup.deleteConfirm'), confirmLabel: t('backup.deleteRepository') });
    if (!accepted) return;
    setControlLoading(`${job.id}:delete`);
    try {
      const result = await apiJson(`${apiUrl}/api/backup/${job.id}`, { method: 'DELETE', headers: { Authorization: `Bearer ${token}` } });
      if (result.ok === false) throw new Error(apiErrorMessage(result, translateApiError, t('backup.actionFailed')));
      setBackupJobs((current) => current.map((item) => item.id === job.id ? { ...item, status: 'DELETING' } : item));
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('backup.actionFailed'), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  if (browseJobID) return <BackupSnapshotBrowser apiUrl={apiUrl} token={token} jobID={browseJobID} onBack={() => setBrowseJobID(null)} />;
  if (loading) return <div className="py-20 text-center text-xs text-[var(--color-text-muted)]">{t('common.loading')}</div>;
  if (error) return <div className="ui-alert ui-alert-error p-4 text-sm" role="alert">{error}</div>;
  if (backupJobs.length === 0) {
    return (
      <div className="ui-card border-2 border-dashed bg-[var(--color-bg-tertiary)] py-16 text-center">
        <p className="font-display font-bold text-[var(--color-text-secondary)]">{t('backup.none')}</p>
        <p className="mt-1 text-xs text-[var(--color-text-muted)]">{t('backup.noneHint')}</p>
      </div>
    );
  }

  return (
    <div className="overflow-x-auto">
      <table className="ui-responsive-table w-full border-collapse text-left">
        <thead>
          <tr className="border-b border-[var(--color-border)]/60 text-[10px] font-bold uppercase tracking-wider text-[var(--color-text-muted)]">
            <th className="px-4 py-4 font-semibold">{t('migrations.sourceTarget')}</th>
            <th className="px-4 py-4 font-semibold">{t('migrations.status')}</th>
            <th className="px-4 py-4 font-semibold">{t('backup.metrics')}</th>
            <th className="px-4 py-4 font-semibold">{t('backup.nextSchedule')}</th>
            <th className="px-4 py-4 text-right font-semibold">{t('migrations.actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-[var(--color-border-light)]">
          {backupJobs.map((job) => {
            const isPaused = job.status === 'PAUSED';
            const running = ['QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING'].includes(job.status);
            const badgeStatus = job.status === 'QUEUED' ? 'PENDING' : job.status === 'SCANNING' ? 'INDEXING' : job.status;
            const pauseLabel = isPaused ? t('backup.resume') : t('backup.pause');
            return (
              <tr key={job.id} className="hover:bg-[var(--color-bg-tertiary)]">
                <td data-label={t('migrations.sourceTarget')} className="px-4 py-4">
                  <button
                    type="button"
                    onClick={() => {
                      if (onSelectActiveBackup) {
                        onSelectActiveBackup(job.id);
                      } else {
                        setBrowseJobID(job.id);
                      }
                    }}
                    className="flex flex-col text-left group hover:opacity-80 transition-opacity"
                    title={t('backup.openDetail')}
                  >
                    <div className="flex items-center gap-2 text-xs font-semibold text-[var(--color-text-primary)] group-hover:text-[var(--color-accent)]">
                      <span className="capitalize">{job.source_provider}</span>
                      <span aria-hidden="true">→</span>
                      <span className="capitalize">{job.target_provider}</span>
                    </div>
                    <p className="mt-1 max-w-48 truncate text-[10px] font-mono text-[var(--color-text-muted)]" title={job.selected_paths.join(', ')}>
                      {job.selected_paths.join(', ')}
                    </p>
                  </button>
                </td>
                <td data-label={t('migrations.status')} className="px-4 py-4">
                  <StatusBadge status={badgeStatus} size="sm" />
                  {typeof job.error_code === 'string' && job.error_code.trim() !== '' && (
                    <p className="mt-2 max-w-64 text-xs text-[var(--color-error-text)]" role="alert">
                      {translateApiError(job.error_code)}
                    </p>
                  )}
                </td>
                <td data-label={t('backup.metrics')} className="px-4 py-4 text-xs text-[var(--color-text-secondary)]">
                  <div>{t('migrations.filesCount', { processed: job.processed_files, total: job.total_files })}</div>
                  <div className="mt-1 text-[10px] text-[var(--color-text-muted)]">{formatBytes(job.processed_bytes)} · {t('backup.deduplicated', { value: formatBytes(job.deduplicated_bytes) })}</div>
                </td>
                <td data-label={t('backup.nextSchedule')} className="px-4 py-4 text-xs text-[var(--color-text-secondary)]">
                  <div className="font-medium text-[var(--color-text-primary)]">{formatCronHuman(job.cron_expression, t)}</div>
                </td>
                <td data-label={t('migrations.actions')} className="px-4 py-4 text-right whitespace-nowrap">
                  <div className="flex justify-end items-center gap-2">
                    <button
                      type="button"
                      onClick={() => void handleAction(job, 'run')}
                      disabled={running || controlLoading !== null}
                      className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-30"
                      aria-label={t('backup.run')}
                      title={t('backup.run')}
                    >
                      {controlLoading === `${job.id}:run` ? (
                        <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                      ) : (
                        <ArrowPathIcon className="size-4" aria-hidden="true" />
                      )}
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleAction(job, isPaused ? 'resume' : 'pause')}
                      disabled={controlLoading !== null || (!isPaused && !['IDLE', 'FAILED', 'QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING'].includes(job.status))}
                      className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-30"
                      aria-label={pauseLabel}
                      title={pauseLabel}
                    >
                      {controlLoading?.startsWith(`${job.id}:pause`) || controlLoading?.startsWith(`${job.id}:resume`) ? (
                        <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                      ) : isPaused ? (
                        <PlayIcon className="size-4" aria-hidden="true" />
                      ) : (
                        <PauseIcon className="size-4" aria-hidden="true" />
                      )}
                    </button>
                    <button
                      type="button"
                      onClick={() => void handleDelete(job)}
                      disabled={controlLoading !== null || job.status === 'DELETING'}
                      className="ui-button-secondary p-2 text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] disabled:opacity-30"
                      aria-label={t('backup.deleteRepository')}
                      title={t('backup.deleteRepository')}
                    >
                      {controlLoading === `${job.id}:delete` ? (
                        <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
                      ) : (
                        <TrashIcon className="size-4" aria-hidden="true" />
                      )}
                    </button>
                    {job.source_profile_id && onOpenFileManager && (
                      <button
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          onOpenFileManager(job.source_profile_id!, commonParentPath(job.selected_paths));
                        }}
                        className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                        aria-label={t('files.openSource')}
                        title={t('files.openSource')}
                      >
                        <CloudArrowDownIcon className="size-4" aria-hidden="true" />
                      </button>
                    )}
                    {job.target_profile_id && onOpenFileManager && (
                      <button
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          onOpenFileManager(job.target_profile_id!, job.target_dir || '/');
                        }}
                        className="ui-button-secondary p-2 hover:bg-[var(--color-bg-tertiary)]"
                        aria-label={t('files.openTarget')}
                        title={t('files.openTarget')}
                      >
                        <CloudArrowUpIcon className="size-4" aria-hidden="true" />
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

