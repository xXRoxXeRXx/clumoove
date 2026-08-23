import { useState, useEffect, useRef, useCallback } from 'react';
import type { BackupJob, BackupRun } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat, formatDuration } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { useToast } from '../contexts/useToast';
import { useConfirm } from '../contexts/useConfirm';
import { useTransferMetrics } from '../hooks/useTransferMetrics';
import { StatusBadge } from './StatusBadge';
import { apiErrorMessage, apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';
import { logger } from '../utils/logger';
import { TransferDetailHeader } from './TransferDetailHeader';
import { TransferProgress } from './TransferProgress';
import { TransferEndpoints } from './TransferEndpoints';
import { LoadingIndicator } from './LoadingIndicator';
import { Tabs, type TabItem } from './Tabs';
import { EditBackupModal } from './EditBackupModal';
import { BackupSnapshotBrowser } from './BackupSnapshotBrowser';
import { formatCronHuman } from '../utils/cronFormatter';
import {
  ArrowPathIcon,
  CalendarDaysIcon,
  CircleStackIcon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  TrashIcon,
} from './icons';

interface BackupDashboardProps {
  backupId: string;
  apiUrl: string;
  token: string;
  onBack: () => void;
}

type BackupDashboardTab = 'overview' | 'runs' | 'snapshots';

export function BackupDashboard({ backupId, apiUrl, token, onBack }: BackupDashboardProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime, formatPercent } = useFormat();
  const translateApiError = useApiError();
  const toast = useToast();
  const confirm = useConfirm();

  const [job, setJob] = useState<BackupJob | null>(null);
  const [runs, setRuns] = useState<BackupRun[]>([]);
  const [runsLoading, setRunsLoading] = useState<boolean>(false);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [isEditing, setIsEditing] = useState<boolean>(false);
  const [activeTab, setActiveTab] = useState<BackupDashboardTab>('overview');

  const { speed, eta, updateMetrics } = useTransferMetrics();

  const tRef = useRef(t);
  const translateApiErrorRef = useRef(translateApiError);

  useEffect(() => {
    tRef.current = t;
    translateApiErrorRef.current = translateApiError;
  }, [t, translateApiError]);

  const fetchRuns = useCallback(async () => {
    setRunsLoading(true);
    try {
      const result = await apiJson<BackupRun[]>(`${apiUrl}/api/backup/${backupId}/runs`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) {
        throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current('backup.loadFailed')));
      }
      setRuns(result.data || []);
    } catch (err: unknown) {
      logger.error('Failed to load backup runs', err);
    } finally {
      setRunsLoading(false);
    }
  }, [apiUrl, backupId, token]);

  useEffect(() => {
    let cancelled = false;
    let hasStreamData = false;
    const snapshotController = new AbortController();

    async function fetchJob(): Promise<void> {
      try {
        const result = await apiJson<BackupJob>(`${apiUrl}/api/backup/${backupId}`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: snapshotController.signal,
        });
        if (result.ok === false) {
          throw new Error(apiErrorMessage(result, translateApiErrorRef.current, tRef.current('backup.loadFailed')));
        }
        const data = result.data;
        if (!cancelled && !hasStreamData) {
          setJob(data);
          updateMetrics({
            processed_bytes: data.processed_bytes,
            total_bytes: data.total_bytes,
            status: data.status,
          });
          setLoading(false);
        }
      } catch (err: unknown) {
        if (!cancelled && !hasStreamData) {
          setError(err instanceof Error ? err.message : tRef.current('backup.loadFailed'));
          setLoading(false);
        }
      }
    }

    async function loadInitialRuns(): Promise<void> {
      try {
        const result = await apiJson<BackupRun[]>(`${apiUrl}/api/backup/${backupId}/runs`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!cancelled && result.ok !== false) {
          setRuns(result.data || []);
        }
      } catch (err: unknown) {
        logger.error('Failed to load initial backup runs', err);
      }
    }

    void fetchJob();
    void loadInitialRuns();

    const streamController = new AbortController();
    void connectSseLoop({
      url: `${apiUrl}/api/backup/stream`,
      signal: streamController.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (cancelled) return;
          if (event === 'error') {
            setError(tRef.current('backup.loadFailed'));
            setLoading(false);
            return;
          }
          if (event !== 'backup_jobs' || !data) return;
          try {
            const jobs: BackupJob[] = JSON.parse(data);
            const updatedJob = jobs.find((j) => j.id === backupId);
            hasStreamData = true;
            snapshotController.abort();
            setError('');
            setLoading(false);
            if (updatedJob) {
              setJob(updatedJob);
              updateMetrics({
                processed_bytes: updatedJob.processed_bytes,
                total_bytes: updatedJob.total_bytes,
                status: updatedJob.status,
              });
            } else {
              setJob(null);
            }
          } catch {
            // ignore JSON parse errors on stream frame
          }
        },
        onError: () => {
          // Reconnect loop handled by connectSseLoop
        },
      },
    });

    return () => {
      cancelled = true;
      snapshotController.abort();
      streamController.abort();
    };
  }, [apiUrl, backupId, token, updateMetrics]);

  const handleAction = async (action: 'run' | 'pause' | 'resume') => {
    setActionLoading(action);
    try {
      const result = await apiJson(`${apiUrl}/api/backup/${backupId}/${action}`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) {
        throw new Error(apiErrorMessage(result, translateApiError, t('backup.actionFailed')));
      }
      setJob((current) => current ? {
        ...current,
        status: action === 'pause' ? 'PAUSED' : action === 'resume' ? 'IDLE' : 'QUEUED',
      } : current);
      void fetchRuns();
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('backup.actionFailed'), 'error');
    } finally {
      setActionLoading(null);
    }
  };

  const handleDelete = async () => {
    const accepted = await confirm({
      title: t('backup.deleteTitle'),
      message: t('backup.deleteConfirm'),
      confirmLabel: t('backup.deleteRepository'),
    });
    if (!accepted) return;

    setActionLoading('delete');
    try {
      const result = await apiJson(`${apiUrl}/api/backup/${backupId}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (result.ok === false) {
        throw new Error(apiErrorMessage(result, translateApiError, t('backup.actionFailed')));
      }
      toast(t('backup.deleteRepository'), 'success');
      onBack();
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('backup.actionFailed'), 'error');
      setActionLoading(null);
    }
  };

  if (loading) {
    return (
      <div className="flex min-h-[50vh] items-center justify-center p-8">
        <LoadingIndicator label={t('common.loading')} />
      </div>
    );
  }

  if (error || !job) {
    return (
      <div className="space-y-4 p-6 text-center">
        <div className="ui-alert ui-alert-error p-4 text-sm" role="alert">
          {error || t('backup.loadFailed')}
        </div>
        <button type="button" onClick={onBack} className="ui-button-secondary px-4 py-2 text-sm">
          {t('common.back')}
        </button>
      </div>
    );
  }

  const isPaused = job.status === 'PAUSED';
  const isRunning = ['QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING'].includes(job.status);
  const badgeStatus = job.status === 'QUEUED' ? 'PENDING' : job.status === 'SCANNING' ? 'INDEXING' : job.status;

  const totalBytes = job.total_bytes || 0;
  const processedBytes = job.processed_bytes || 0;
  const deduplicatedBytes = job.deduplicated_bytes || 0;
  const newlyUploadedBytes = Math.max(0, processedBytes - deduplicatedBytes);
  const dedupPercentage = processedBytes > 0 ? Math.min(100, Math.round((deduplicatedBytes / processedBytes) * 100)) : 0;
  const byteProgressPercent = totalBytes > 0
    ? Math.min(Math.round((processedBytes / totalBytes) * 100), 100)
    : (job.total_files > 0 ? Math.min(Math.round((job.processed_files / job.total_files) * 100), 100) : 0);

  const tabs: readonly TabItem<BackupDashboardTab>[] = [
    { value: 'overview', label: t('backup.tabOverview') },
    { value: 'runs', label: `${t('backup.tabRuns')} (${runs.length})` },
    { value: 'snapshots', label: t('backup.tabSnapshots') },
  ];

  return (
    <div className="space-y-6">
      {/* Header */}
      <TransferDetailHeader
        backLabel={t('nav.overview')}
        onBack={onBack}
        title={t('backup.dashboardTitle')}
        id={job.id}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => void handleAction('run')}
              disabled={isRunning || actionLoading !== null}
              className="ui-button-primary flex items-center gap-2 px-3 py-1.5 text-xs font-semibold disabled:opacity-40"
            >
              {actionLoading === 'run' ? (
                <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
              ) : (
                <PlayIcon className="size-4" aria-hidden="true" />
              )}
              <span>{t('backup.run')}</span>
            </button>

            <button
              type="button"
              onClick={() => void handleAction(isPaused ? 'resume' : 'pause')}
              disabled={actionLoading !== null || (!isPaused && !['IDLE', 'FAILED', 'QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING'].includes(job.status))}
              className="ui-button-secondary flex items-center gap-2 px-3 py-1.5 text-xs disabled:opacity-40"
            >
              {actionLoading === (isPaused ? 'resume' : 'pause') ? (
                <ArrowPathIcon className="size-4 animate-spin" aria-hidden="true" />
              ) : isPaused ? (
                <PlayIcon className="size-4" aria-hidden="true" />
              ) : (
                <PauseIcon className="size-4" aria-hidden="true" />
              )}
              <span>{isPaused ? t('backup.resume') : t('backup.pause')}</span>
            </button>

            <button
              type="button"
              onClick={() => setIsEditing(true)}
              className="ui-button-secondary flex items-center gap-2 px-3 py-1.5 text-xs"
            >
              <PencilIcon className="size-4" aria-hidden="true" />
              <span>{t('backup.edit')}</span>
            </button>

            <button
              type="button"
              onClick={() => void handleDelete()}
              disabled={actionLoading !== null || job.status === 'DELETING'}
              className="ui-button-secondary flex items-center gap-2 px-3 py-1.5 text-xs text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)] disabled:opacity-40"
            >
              <TrashIcon className="size-4" aria-hidden="true" />
              <span>{t('backup.deleteRepository')}</span>
            </button>
          </div>
        }
      />

      {/* Endpoints representation */}
      <TransferEndpoints
        sourceLabel={t('migrations.source')}
        targetLabel={t('migrations.target')}
        oauthLabel={t('migrations.oauth')}
        sourceProvider={job.source_provider}
        sourceUrl={job.source_url || undefined}
        selectedPaths={job.selected_paths}
        targetProvider={job.target_provider}
        targetUrl={job.target_url || undefined}
        targetDir={job.target_dir}
      />

      {/* Tabs */}
      <Tabs
        label={t('backup.dashboardTitle')}
        items={tabs}
        value={activeTab}
        onChange={(tab) => {
          setActiveTab(tab);
          if (tab === 'runs') void fetchRuns();
        }}
      >
        {/* Tab 1: Overview */}
        {activeTab === 'overview' && (
          <div className="space-y-6 pt-4">
            {/* Live Progress Card (shown when active) */}
            {isRunning && (
              <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <StatusBadge status={badgeStatus} size="sm" />
                    <h3 className="text-sm font-bold text-[var(--color-text-primary)]">
                      {job.status === 'SCANNING'
                        ? t('backup.scanningPhase')
                        : job.status === 'VERIFYING'
                        ? t('backup.verifyingPhase')
                        : t('backup.runningPhase')}
                    </h3>
                  </div>
                </div>

                <TransferProgress
                  progress={byteProgressPercent}
                  rate={`${formatBytes(speed)}/s`}
                  transferred={totalBytes > 0 ? `${formatBytes(processedBytes)} / ${formatBytes(totalBytes)}` : `${job.processed_files} / ${job.total_files}`}
                  remaining={eta}
                  labels={{
                    progress: t('dashboard.progress'),
                    transferRate: t('dashboard.transferRate'),
                    transferred: t('dashboard.transferred'),
                    remaining: t('dashboard.remaining'),
                  }}
                />
              </div>
            )}

            {/* Error Message */}
            {typeof job.error_code === 'string' && job.error_code.trim() !== '' && (
              <div className="ui-alert ui-alert-error p-4 text-sm" role="alert">
                {translateApiError(job.error_code)}
              </div>
            )}

            {/* Info Grid */}
            <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
              {/* Schedule Card */}
              <div className="ui-card space-y-4 p-5">
                <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
                  <CalendarDaysIcon className="size-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                  <h3 className="font-display text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">
                    {t('backup.nextSchedule')}
                  </h3>
                </div>

                <div className="space-y-3 text-xs">
                  <div>
                    <span className="text-[var(--color-text-muted)]">{t('backup.scheduleSummary')}:</span>
                    <p className="mt-0.5 text-sm font-semibold text-[var(--color-text-primary)]">
                      {formatCronHuman(job.cron_expression, t)}
                    </p>
                    <p className="mt-0.5 font-mono text-[10px] text-[var(--color-text-muted)]">
                      {job.timezone} · {job.cron_expression}
                    </p>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2.5">
                    <span className="text-[var(--color-text-muted)]">{t('backup.lastRun')}:</span>
                    <span className="font-mono font-medium text-[var(--color-text-primary)]">
                      {job.last_run_at ? formatDateTime(job.last_run_at) : t('common.never')}
                    </span>
                  </div>

                  {job.last_run_status && (
                    <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2.5">
                      <span className="text-[var(--color-text-muted)]">{t('migrations.status')}:</span>
                      <StatusBadge status={job.last_run_status} size="sm" />
                    </div>
                  )}
                </div>
              </div>

              {/* Repository Statistics Card */}
              <div className="ui-card space-y-4 p-5">
                <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
                  <CircleStackIcon className="size-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                  <h3 className="font-display text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)]">
                    {t('backup.repositoryStats')}
                  </h3>
                </div>

                <div className="space-y-2.5 text-xs">
                  <div className="flex justify-between">
                    <span className="text-[var(--color-text-muted)]">{t('backup.totalFilesCount')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-text-primary)]">
                      {job.total_files}
                    </span>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2">
                    <span className="text-[var(--color-text-muted)]">{t('backup.snapshotVolume')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-text-primary)]">
                      {formatBytes(processedBytes)}
                    </span>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2">
                    <span className="text-[var(--color-text-muted)]">{t('backup.dedupSavings')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-success-text)]">
                      {formatBytes(deduplicatedBytes)} ({formatPercent(dedupPercentage)})
                    </span>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2">
                    <span className="text-[var(--color-text-muted)]">{t('backup.newlyUploaded')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-text-primary)]">
                      {formatBytes(newlyUploadedBytes)}
                    </span>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2">
                    <span className="text-[var(--color-text-muted)]">{t('backup.retentionLimit')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-text-primary)]">
                      {t('backup.retentionCountValue', { count: job.retention_count })}
                    </span>
                  </div>

                  <div className="flex justify-between border-t border-[var(--color-border-light)] pt-2">
                    <span className="text-[var(--color-text-muted)]">{t('backup.threads')}:</span>
                    <span className="font-mono font-semibold text-[var(--color-text-primary)]">
                      {job.threads}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* Tab 2: Runs History */}
        {activeTab === 'runs' && (
          <div className="space-y-4 pt-4">
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-bold text-[var(--color-text-primary)]">{t('backup.runsTitle')}</h3>
              <button
                type="button"
                onClick={() => void fetchRuns()}
                disabled={runsLoading}
                className="ui-button-secondary flex items-center gap-1.5 px-2.5 py-1 text-xs"
                title={t('common.refresh')}
              >
                <ArrowPathIcon className={`size-3.5 ${runsLoading ? 'animate-spin' : ''}`} aria-hidden="true" />
                <span>{t('common.refresh')}</span>
              </button>
            </div>

            {runsLoading && runs.length === 0 ? (
              <div className="py-12 text-center text-xs text-[var(--color-text-muted)]">
                {t('common.loading')}
              </div>
            ) : runs.length === 0 ? (
              <div className="ui-card border-2 border-dashed bg-[var(--color-bg-tertiary)] py-12 text-center text-xs text-[var(--color-text-muted)]">
                {t('backup.noRuns')}
              </div>
            ) : (
              <div className="overflow-x-auto">
                <table className="ui-responsive-table w-full border-collapse text-left">
                  <thead>
                    <tr className="border-b border-[var(--color-border)]/60 text-[10px] font-bold uppercase tracking-wider text-[var(--color-text-muted)]">
                      <th className="px-3 py-3">#</th>
                      <th className="px-3 py-3">{t('backup.trigger')}</th>
                      <th className="px-3 py-3">{t('migrations.status')}</th>
                      <th className="px-3 py-3">{t('backup.duration')}</th>
                      <th className="px-3 py-3">{t('migrations.files')}</th>
                      <th className="px-3 py-3">{t('backup.snapshotVolume')}</th>
                      <th className="px-3 py-3">{t('backup.savedDeduplication')}</th>
                      <th className="px-3 py-3">{t('backup.newlyUploaded')}</th>
                      <th className="px-3 py-3">{t('migrations.createdAt')}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-[var(--color-border-light)] text-xs">
                    {runs.map((run) => {
                      const startTime = run.started_at ? new Date(run.started_at).getTime() : 0;
                      const endTime = run.finished_at ? new Date(run.finished_at).getTime() : 0;
                      const durationMs = startTime > 0 && endTime > startTime ? endTime - startTime : 0;
                      const newlyUploaded = Math.max(0, (run.processed_bytes || 0) - (run.deduplicated_bytes || 0));
                      const triggerLabel = run.trigger === 'manual'
                        ? t('backup.triggerManual')
                        : run.trigger === 'schedule'
                        ? t('backup.triggerSchedule')
                        : run.trigger === 'catch_up'
                        ? t('backup.triggerCatchUp')
                        : run.trigger;

                      return (
                        <tr key={run.id} className="hover:bg-[var(--color-bg-tertiary)]">
                          <td data-label="#" className="px-3 py-3 font-mono text-[var(--color-text-muted)]">
                            #{run.generation}
                          </td>
                          <td data-label={t('backup.trigger')} className="px-3 py-3 font-medium text-[var(--color-text-primary)]">
                            {triggerLabel}
                          </td>
                          <td data-label={t('migrations.status')} className="px-3 py-3">
                            <StatusBadge status={run.state} size="sm" />
                            {run.error_code && (
                              <p className="mt-1 max-w-48 truncate text-[10px] text-[var(--color-error-text)]" title={translateApiError(run.error_code)}>
                                {translateApiError(run.error_code)}
                              </p>
                            )}
                          </td>
                          <td data-label={t('backup.duration')} className="px-3 py-3 font-mono text-[var(--color-text-muted)]">
                            {durationMs > 0 ? formatDuration(durationMs / 1000, t) : '—'}
                          </td>
                          <td data-label={t('migrations.files')} className="px-3 py-3 font-mono text-[var(--color-text-secondary)]">
                            {t('migrations.filesCount', { processed: run.processed_files, total: run.total_files })}
                            {run.failed_files > 0 && (
                              <span className="ml-1 text-[var(--color-error-text)]">
                                ({run.failed_files} {t('dashboard.failed')})
                              </span>
                            )}
                          </td>
                          <td data-label={t('backup.snapshotVolume')} className="px-3 py-3 font-mono text-[var(--color-text-primary)]">
                            {formatBytes(run.processed_bytes)}
                          </td>
                          <td data-label={t('backup.savedDeduplication')} className="px-3 py-3 font-mono text-[var(--color-success-text)]">
                            {formatBytes(run.deduplicated_bytes)}
                          </td>
                          <td data-label={t('backup.newlyUploaded')} className="px-3 py-3 font-mono text-[var(--color-text-secondary)]">
                            {formatBytes(newlyUploaded)}
                          </td>
                          <td data-label={t('migrations.createdAt')} className="px-3 py-3 text-[10px] text-[var(--color-text-muted)]">
                            {formatDateTime(run.created_at)}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}

        {/* Tab 3: Snapshots Browser */}
        {activeTab === 'snapshots' && (
          <div className="pt-4">
            <BackupSnapshotBrowser
              apiUrl={apiUrl}
              token={token}
              jobID={backupId}
              onBack={() => setActiveTab('overview')}
            />
          </div>
        )}
      </Tabs>

      {/* Edit Modal */}
      {isEditing && (
        <EditBackupModal
          job={job}
          apiUrl={apiUrl}
          token={token}
          onClose={() => setIsEditing(false)}
          onSuccess={(updated) => {
            setJob(updated);
            void fetchRuns();
          }}
        />
      )}
    </div>
  );
}
