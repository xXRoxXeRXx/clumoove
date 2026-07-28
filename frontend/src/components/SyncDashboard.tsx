import { useState, useEffect, useRef } from 'react';
import type { SyncJob } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat, formatBytes, formatDuration } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { useToast } from '../contexts/useToast';
import { useTransferMetrics } from '../hooks/useTransferMetrics';
import { SelectedPathsViewer } from './SelectedPathsViewer';
import { StatusBadge } from './StatusBadge';
import { apiFetch } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';
import { ErrorOverview } from './ErrorOverview';
import { BANDWIDTH_OPTIONS, bandwidthIndexToValue, getBandwidthLabel, valueToBandwidthIndex } from '../utils/bandwidth';
import {
  AdjustmentsHorizontalIcon,
  ArrowsRightLeftIcon,
  ChartBarIcon,
  ClockIcon,
  CloudArrowDownIcon,
  CloudArrowUpIcon,
} from '@heroicons/react/24/outline';

interface SyncDashboardProps {
  syncId: string;
  apiUrl: string;
  token: string;
  onBack: () => void;
}

export function SyncDashboard({ syncId, apiUrl, token, onBack }: SyncDashboardProps) {
  const [job, setJob] = useState<SyncJob | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [actionLoading, setActionLoading] = useState<boolean>(false);
  const [threads, setThreads] = useState<number>(8);
  const [threadsLoading, setThreadsLoading] = useState<boolean>(false);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(0);
  const [bandwidthLoading, setBandwidthLoading] = useState<boolean>(false);
  const [now, setNow] = useState<number>(() => Date.now());
  const threadsDraggingRef = useRef<boolean>(false);
  const bandwidthDraggingRef = useRef<boolean>(false);

  const { t } = useTranslation();
  const { formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const toast = useToast();
  const { speed, eta, updateMetrics } = useTransferMetrics();

  useEffect(() => {
    const timer = setInterval(() => {
      setNow(Date.now());
    }, 10000);
    return () => clearInterval(timer);
  }, []);

  useEffect(() => {
    if (job?.threads !== undefined && !threadsDraggingRef.current) {
      setThreads(job.threads);
    }
  }, [job?.threads]);

  useEffect(() => {
    if (job?.bandwidth_limit_mbps !== undefined && !bandwidthDraggingRef.current) {
      setBandwidthLimit(job.bandwidth_limit_mbps);
    }
  }, [job?.bandwidth_limit_mbps]);

  const commitThreadsChange = async (value: number) => {
    setThreadsLoading(true);
    try {
      const response = await apiFetch(`${apiUrl}/api/sync/${syncId}/threads`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${token}`,
        },
        body: JSON.stringify({ threads: value }),
      });
      if (!response.ok) {
        let msg = t('dashboard.threadsFailed');
        try {
          const body = await response.json();
          if (body?.error_code) msg = translateApiError(body.error_code);
        } catch { /* ignore */ }
        toast(msg);
        if (job?.threads) setThreads(job.threads);
      }
    } catch (err) {
      console.error(err);
      toast(t('dashboard.threadsFailed'));
      if (job?.threads) setThreads(job.threads);
    } finally {
      setThreadsLoading(false);
    }
  };

  const commitBandwidthChange = async (value: number) => {
    setBandwidthLoading(true);
    try {
      const response = await apiFetch(`${apiUrl}/api/sync/${syncId}/bandwidth`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
        body: JSON.stringify({ limit_mbps: value }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        toast(body?.error_code ? translateApiError(body.error_code) : t('dashboard.bandwidthFailed'));
        setBandwidthLimit(job?.bandwidth_limit_mbps ?? 0);
      }
    } catch (err) {
      console.error(err);
      toast(t('dashboard.bandwidthFailed'));
      setBandwidthLimit(job?.bandwidth_limit_mbps ?? 0);
    } finally {
      setBandwidthLoading(false);
    }
  };

  useEffect(() => {
    let cancelled = false;
    const fetchJob = async () => {
      try {
        const res = await apiFetch(`${apiUrl}/api/sync/${syncId}`, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!res.ok) {
          let msg = t('sync.loadFailed');
          try {
            const body = await res.json();
            if (body?.error_code) msg = translateApiError(body.error_code);
          } catch { /* ignore */ }
          throw new Error(msg);
        }
        const data: SyncJob = await res.json();
        if (!cancelled) {
          setJob(data);
          updateMetrics(data);
          setLoading(false);
        }
      } catch (err: unknown) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : t('sync.loadFailed'));
          setLoading(false);
        }
      }
    };

    fetchJob();

    const controller = new AbortController();
    void connectSseLoop({
      url: `${apiUrl}/api/sync/stream`,
      token,
      signal: controller.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, data) => {
          if (event !== 'sync_jobs' || !data || cancelled) return;
          try {
            const jobs: SyncJob[] = JSON.parse(data);
            const updated = jobs.find((j) => j.id === syncId);
            if (updated) {
              setJob(updated);
              updateMetrics(updated);
            }
          } catch { /* ignore */ }
        },
      },
    });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [apiUrl, syncId, token, t, translateApiError, updateMetrics]);

  const handleTriggerStart = async () => {
    setActionLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/sync/${syncId}/start`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) {
        let msg = t('sync.startFailed');
        try {
          const body = await res.json();
          if (body?.error_code) msg = translateApiError(body.error_code);
        } catch { /* ignore */ }
        throw new Error(msg);
      }
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('sync.startFailed'));
    } finally {
      setActionLoading(false);
    }
  };

  const handlePause = async () => {
    setActionLoading(true);
    try {
      await apiFetch(`${apiUrl}/api/sync/${syncId}/pause`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch { /* ignore */ }
    finally { setActionLoading(false); }
  };

  const handleResume = async () => {
    setActionLoading(true);
    try {
      await apiFetch(`${apiUrl}/api/sync/${syncId}/resume`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch { /* ignore */ }
    finally { setActionLoading(false); }
  };

  const handleDownloadReport = async (e?: React.MouseEvent) => {
    if (e) e.preventDefault();
    try {
      const response = await apiFetch(`${apiUrl}/api/sync/${syncId}/report`, {
        headers: {
          Authorization: `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        throw new Error(t('dashboard.downloadFailed'));
      }
      const blob = await response.blob();
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `sync_report_${syncId}.csv`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.URL.revokeObjectURL(url);
    } catch (err) {
      console.error(err);
      toast(t('dashboard.downloadFailed'));
    }
  };

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-4">
        <p className="text-xs font-mono text-[var(--color-text-muted)]">{t('common.loading')}</p>
      </div>
    );
  }

  if (error || !job) {
    return (
      <div className="space-y-4">
        <button onClick={onBack} className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-xs font-bold hover:bg-[var(--color-bg-tertiary)]">
          {t('common.back')}
        </button>
        <div className="ui-card p-4 bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-[var(--color-error-text)] text-sm font-mono text-center">
          {error || t('sync.notFound')}
        </div>
      </div>
    );
  }

  const totalBytes = job?.total_bytes || 0;
  const processedBytes = job?.processed_bytes || 0;
  const liveBytes = typeof job?.live_bytes === 'number' ? job.live_bytes : processedBytes;
  const effectiveBytesDisplay = totalBytes > 0
    ? Math.min(totalBytes, Math.max(processedBytes, liveBytes))
    : processedBytes;

  const byteProgressPercent = totalBytes > 0
    ? Math.min(Math.round((effectiveBytesDisplay / totalBytes) * 100), 100)
    : (job?.total_files && job.total_files > 0
        ? Math.min(Math.round((job.processed_files / job.total_files) * 100), 100)
        : (job?.status === 'IDLE' || job?.status === 'COMPLETED' ? 100 : 0));
  const canPause = ['IDLE', 'INDEXING', 'RUNNING', 'VERIFYING'].includes(job.status);
  const canStart = ['IDLE', 'FAILED'].includes(job.status);

  return (
    <div className="w-full space-y-6">
      {/* Back Button Header */}
      <div className="flex items-center justify-between">
        <button
          onClick={onBack}
          className="ui-button-secondary flex items-center gap-2 px-4 py-2 text-xs font-mono font-bold hover:bg-[var(--color-bg-tertiary)]"
        >
          {t('common.back')}
        </button>
      </div>

      <div className="ui-card p-6 space-y-6">
        {/* Top Badges Row (Above Title & Action Buttons) */}
        <div className="flex items-center justify-end gap-2.5 pb-2">
          {/* Status Info Badge */}
          <StatusBadge status={job.status} />

          {/* Direction Info Badge (rechtsbündig) */}
          {job.direction === 'two_way' ? (
            <span className="inline-flex items-center gap-1.5 text-xs font-bold text-[var(--color-info-text)] px-3 py-1 bg-[var(--color-info-bg)] border border-[var(--color-info-border)]">
              <span>{t('sync.twoWay')}</span>
            </span>
          ) : (
            <span className="inline-flex items-center gap-1.5 text-xs font-bold text-[var(--color-info-text)] px-3 py-1 bg-[var(--color-info-bg)] border border-[var(--color-info-border)]">
              <span>{t('sync.oneWay')}</span>
            </span>
          )}
        </div>

        {/* Title & Action Controls */}
        <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 border-b border-[var(--color-border)] pb-6">
          <div className="space-y-1">
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-text-primary)]">
              {t('sync.syncJobDetail')}
            </h1>
            <p className="text-xs text-[var(--color-text-muted)] font-mono">
              ID: {job.id}
            </p>
          </div>

          <div className="flex items-center gap-2.5 w-full md:w-auto justify-start md:justify-end flex-wrap">
            {(job.failed_files > 0 || job.last_run_status === 'PARTIAL' || job.last_run_status === 'FAILED') && (
              <button
                onClick={handleDownloadReport}
                className="ui-button-secondary flex items-center gap-2 px-3.5 py-2 text-xs font-bold text-[var(--color-error-text)] hover:bg-[var(--color-error-bg)]"
              >
                {t('sync.downloadReport')}
              </button>
            )}

            {job.status === 'PAUSED' ? (
              <button
                onClick={handleResume}
                disabled={actionLoading}
                className="ui-button-primary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:opacity-90 disabled:opacity-50"
              >
                {t('sync.resume')}
              </button>
            ) : (
              <button
                onClick={handlePause}
                disabled={actionLoading || !canPause}
                className="ui-button-secondary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
              >
                {t('sync.pause')}
              </button>
            )}

            <button
              onClick={handleTriggerStart}
              disabled={actionLoading || !canStart}
                className="ui-button-primary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:opacity-90 disabled:opacity-50"
            >
              {actionLoading && `${t('common.loading')} `}
              {t('sync.syncNow')}
            </button>
          </div>
        </div>

        {/* Live Transfer Progress (only shown while a run is active) */}
        {(job.status === 'RUNNING' || job.status === 'INDEXING') && (
          <div className="ui-card p-6 flex flex-col">
              <div className="flex items-end justify-between mb-6 border-b border-[var(--color-border-light)] pb-4.5">
                <div>
                  <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('dashboard.progress')}</span>
                  <h3 className="font-display font-extrabold text-5xl text-[var(--color-text-primary)] mt-1.5 leading-none">
                    {byteProgressPercent}%
                  </h3>
                </div>
                <div className="text-right flex flex-col items-end">
                  <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('dashboard.transferRate')}</span>
                  <p className="text-base font-extrabold text-[var(--color-success-text)] mt-1.5 font-mono">
                    {formatBytes(speed)}/s
                  </p>
                </div>
              </div>

              <div className="w-full bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] h-5 p-0.5 mb-6 overflow-hidden">
                <div
                  className="bg-[var(--color-bg-inverse)] h-full transition-all duration-500 ease-out"
                  style={{ width: `${byteProgressPercent}%` }}
                >
                </div>
              </div>

              <div className="grid grid-cols-2 gap-4 text-[10px] font-mono font-bold text-[var(--color-text-muted)] uppercase tracking-wider">
                <div className="flex items-center gap-2">
                  <span>
                    {t('dashboard.transferred')}:{' '}
                    <strong className="text-[var(--color-text-primary)]">
                      {totalBytes > 0 ? formatBytes(effectiveBytesDisplay) : `${job.processed_files}`}
                    </strong>
                    {totalBytes > 0 ? ` / ${formatBytes(totalBytes)}` : ` / ${job.total_files}`}
                  </span>
                </div>
                <div className="flex items-center gap-2 justify-end">
                  <span>{t('dashboard.remaining')}: <strong className="text-[var(--color-text-primary)]">{eta}</strong></span>
                </div>
              </div>
          </div>
        )}

        {/* Source & Target Connection Cards Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <CloudArrowDownIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">{t('migrations.source')}</h3>
            </div>
            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">{job.source_provider}</div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">{job.source_url || t('migrations.oauth')}</div>
              <SelectedPathsViewer paths={job.selected_paths} />
            </div>
          </div>
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <CloudArrowUpIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">{t('migrations.target')}</h3>
            </div>
            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">{job.target_provider}</div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">{job.target_url || t('migrations.oauth')}</div>
              <div className="flex flex-wrap gap-1.5 pt-1">
                <span className="ui-card inline-flex items-center gap-1 px-2.5 py-1 text-[10px] font-mono text-[var(--color-text-secondary)]">
                  <span>{job.target_dir || '/'}</span>
                </span>
              </div>
            </div>
          </div>
        </div>

        {/* Active transfers and run status follow the migration-detail layout. */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 pt-2">
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <ArrowsRightLeftIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">{t('sync.activeTransfersTitle', { count: job.active_files?.length || 0, threads })}</h3>
            </div>
            {job.active_files?.length ? (
              <div className="space-y-2 max-h-[465px] overflow-y-auto pr-1">
                {job.active_files.map((file, i) => {
                  const fileName = file.split('/').pop() || file;
                  return (
                    <div key={i} className="ui-card flex items-center justify-between text-xs py-2.5 px-3.5 bg-[var(--color-bg-tertiary)] font-mono text-[var(--color-text-secondary)] min-w-0">
                      <span className="truncate pr-4" title={file}>{fileName}</span>
                      <span className="text-[10px] text-[var(--color-success-text)] font-semibold uppercase animate-pulse shrink-0 bg-[var(--color-success-bg)] border border-[var(--color-success-border)] px-2 py-0.5">
                        {t('dashboard.running')}
                      </span>
                    </div>
                  );
                })}
              </div>
            ) : (
              <div className="py-4 text-xs text-[var(--color-text-muted)] font-mono">
                {t('dashboard.noActiveTransfers')}
              </div>
            )}
          </div>
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <ChartBarIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">{t('migrations.status')} & {t('dashboard.progress')}</h3>
            </div>
            <div className="space-y-2 font-sans text-xs text-[var(--color-text-muted)]">
              <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]"><span>{t('dashboard.filesTotal')}</span><span className="font-bold text-[var(--color-text-primary)] font-mono">{job.total_files}</span></div>
              <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]"><span>{t('sync.changedFiles')}</span><span className="font-bold text-[var(--color-success-text)] font-mono">{job.changed_files}</span></div>
              <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]"><span>{t('sync.deletedFiles')}</span><span className="font-bold text-[var(--color-text-primary)] font-mono">{job.deleted_files}</span></div>
              <div className="flex justify-between items-center py-1.5"><span>{t('dashboard.failed')}</span><span className={`font-bold font-mono ${job.failed_files > 0 ? 'text-[var(--color-error-text)]' : 'text-[var(--color-text-muted)]'}`}>{job.failed_files}</span></div>
            </div>
          </div>
        </div>

        {/* Timing, Schedule & Configuration Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 pt-2">
          {/* Column 1: Schedule & Timing */}
          {(() => {
            let nextRunLabel = t('sync.neverRun');
            if (job.status === 'PAUSED') {
              nextRunLabel = t('sync.statusPaused');
            } else if (job.last_run_at && job.interval_minutes > 0) {
              const lastRunMs = new Date(job.last_run_at).getTime();
              const nextRunMs = lastRunMs + job.interval_minutes * 60 * 1000;
              const diffMs = nextRunMs - now;

              if (diffMs <= 0) {
                nextRunLabel = t('sync.nextRunDueNow');
              } else {
                const diffSec = Math.round(diffMs / 1000);
                const formattedDuration = formatDuration(diffSec, t);
                nextRunLabel = t('sync.nextRunDueIn', { duration: formattedDuration });
              }
            }

            return (
              <div className="ui-card p-5 space-y-4">
                <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
                  <ClockIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                  <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                    {t('sync.lastRun')} & {t('sync.nextRun')}
                  </h3>
                </div>

                <div className="space-y-3">
                  <div className="ui-card p-3.5 bg-[var(--color-bg-primary)]">
                    <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.nextRun')}</span>
                    <span className="font-display font-extrabold text-base text-[var(--color-text-primary)] mt-0.5 block">
                      {nextRunLabel}
                    </span>
                    <span className="text-[10px] text-[var(--color-text-secondary)] mt-0.5 block">
                      {t('sync.interval')}: {job.interval_minutes >= 60 && job.interval_minutes % 60 === 0 ? `${job.interval_minutes / 60} ${job.interval_minutes / 60 === 1 ? t('sync.hour') : t('sync.hours')}` : `${job.interval_minutes} ${t('sync.minutes')}`}
                    </span>
                  </div>

                  <div className="ui-card p-3.5 bg-[var(--color-bg-primary)]">
                    <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.lastRun')}</span>
                    <span className="font-display font-extrabold text-xs text-[var(--color-text-primary)] mt-0.5 block">
                      {job.last_run_at ? formatDateTime(job.last_run_at) : t('sync.neverRun')}
                    </span>
                    {job.last_run_at && (
                      <span className="text-[10px] text-[var(--color-text-secondary)] mt-0.5 block">
                        {job.failed_files > 0
                          ? t('sync.lastRunErrors', { count: job.failed_files })
                          : job.changed_files > 0
                          ? t('sync.lastRunUpdated', { count: job.changed_files })
                          : t('sync.lastRunNoChanges')}
                      </span>
                    )}
                  </div>
                </div>
              </div>
            );
          })()}

          {/* Column 2: Configuration Rules & Performance */}
          <div className="ui-card p-5 space-y-4 flex flex-col justify-between">
            <div className="space-y-4">
              <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
                <AdjustmentsHorizontalIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
                <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                  {t('sync.conflictStrategy')} & {t('dashboard.threads')}
                </h3>
              </div>

              <div className="grid grid-cols-2 gap-3 text-[11px]">
                <div className="ui-card p-3 bg-[var(--color-bg-primary)]">
                  <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.conflictStrategy')}</span>
                  <span className="font-bold text-[var(--color-text-primary)] mt-0.5 block truncate">
                    {job.direction === 'one_way'
                      ? t('sync.conflictSourceWins')
                      : job.conflict_strategy === 'OVERWRITE'
                      ? t('sync.conflictSourceWins')
                      : job.conflict_strategy === 'RENAME'
                      ? t('sync.conflictKeepBoth')
                      : job.conflict_strategy === 'SKIP'
                      ? t('sync.conflictSkip')
                      : job.conflict_strategy}
                  </span>
                </div>

                <div className="ui-card p-3 bg-[var(--color-bg-primary)]">
                  <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.deletePropagation')}</span>
                  <span className={`font-bold mt-0.5 block ${job.delete_propagation ? 'text-[var(--color-error-text)]' : 'text-[var(--color-success-text)]'}`}>
                    {job.delete_propagation ? t('common.enabled') : t('common.disabled')}
                  </span>
                </div>
              </div>
            </div>

            {/* Integrated Threads Slider */}
            <div className="ui-card p-3.5 bg-[var(--color-bg-primary)] space-y-2 mt-auto">
              <div className="flex items-center justify-between">
                <label className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                  {t('dashboard.threads')}
                </label>
                <span className="text-[11px] font-bold text-[var(--color-text-primary)] font-mono">{threads}</span>
              </div>
              <input
                type="range"
                min={1}
                max={16}
                step={1}
                value={threads}
                disabled={threadsLoading}
                onChange={(e) => setThreads(Number(e.target.value))}
                onPointerDown={() => { threadsDraggingRef.current = true; }}
                onPointerUp={(e) => {
                  threadsDraggingRef.current = false;
                  commitThreadsChange(Number((e.target as HTMLInputElement).value));
                }}
                onKeyDown={() => { threadsDraggingRef.current = true; }}
                onKeyUp={(e) => {
                  threadsDraggingRef.current = false;
                  commitThreadsChange(Number((e.target as HTMLInputElement).value));
                }}
                className="w-full accent-[var(--color-text-primary)]"
              />
              <p className="text-[9px] text-[var(--color-text-muted)] leading-relaxed">
                {t('dashboard.threadsHint')}
              </p>
            </div>

            <div className="ui-card p-3.5 bg-[var(--color-bg-primary)] space-y-2">
              <div className="flex items-center justify-between">
                <label className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                  {t('dashboard.bandwidthLimit')}
                </label>
                <span className="text-[11px] font-bold text-[var(--color-text-primary)] font-mono">
                  {getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited'))}
                </span>
              </div>
              <input
                type="range"
                min={0}
                max={BANDWIDTH_OPTIONS.length - 1}
                step={1}
                value={valueToBandwidthIndex(bandwidthLimit)}
                disabled={bandwidthLoading}
                onChange={(e) => setBandwidthLimit(bandwidthIndexToValue(Number(e.target.value)))}
                onPointerDown={() => { bandwidthDraggingRef.current = true; }}
                onPointerUp={(e) => {
                  bandwidthDraggingRef.current = false;
                  commitBandwidthChange(bandwidthIndexToValue(Number((e.target as HTMLInputElement).value)));
                }}
                onKeyDown={() => { bandwidthDraggingRef.current = true; }}
                onKeyUp={(e) => {
                  bandwidthDraggingRef.current = false;
                  commitBandwidthChange(bandwidthIndexToValue(Number((e.target as HTMLInputElement).value)));
                }}
                className="w-full accent-[var(--color-text-primary)]"
              />
              <p className="text-[9px] text-[var(--color-text-muted)] leading-relaxed">
                {bandwidthLimit === 0
                  ? t('fileBrowser.bandwidthUnlimited')
                  : t('fileBrowser.bandwidthHint', { limit: getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited')) })}
              </p>
            </div>
          </div>
        </div>

        <ErrorOverview
          endpoint={`${apiUrl}/api/sync/${syncId}/errors`}
          token={token}
          refreshKey={`${job.failed_files}-${job.last_run_at}-${job.status}`}
        />

        {job.error_message && (
          <div className="ui-card p-4 bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-xs font-mono text-[var(--color-error-text)] flex items-start gap-2">
            <span>{job.error_message}</span>
          </div>
        )}
      </div>
    </div>
  );
}
