import { useState, useEffect, useRef } from 'react';
import type { SyncJob, Provider } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat, formatBytes, formatDuration } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { useToast } from '../contexts/useToast';
import { useTransferMetrics } from '../hooks/useTransferMetrics';
import { Badge, StatusBadge } from './StatusBadge';
import { apiFetch } from '../utils/apiClient';
import { connectSseLoop } from '../utils/sse';
import { useOAuthPopup } from '../hooks/useOAuthPopup';
import { ErrorOverview } from './ErrorOverview';
import { TransferDetailHeader } from './TransferDetailHeader';
import { TransferProgress } from './TransferProgress';
import { TransferEndpoints } from './TransferEndpoints';
import { ActiveTransfersPanel, TransferStatusPanel } from './TransferRunSummary';
import { LoadingIndicator } from './LoadingIndicator';
import { FileBrowser } from './FileBrowser';
import { BANDWIDTH_OPTIONS, bandwidthIndexToValue, getBandwidthLabel, valueToBandwidthIndex } from '../utils/bandwidth';
import {
  AdjustmentsHorizontalIcon,
  ArrowLeftIcon,
  ClockIcon,
  PencilIcon,
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
  const [isEditing, setIsEditing] = useState<boolean>(false);
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
  const { openOAuthPopup } = useOAuthPopup(apiUrl);
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

  const handleReauth = () => {
    if (!job) return;
    const oauthProviders = ['dropbox', 'google', 'onedrive', 'hidrive'];
    const role = oauthProviders.includes(job.source_provider) ? 'source' : 'target';
    const provider = role === 'source' ? job.source_provider : job.target_provider;
    if (!oauthProviders.includes(provider)) return;
    setActionLoading(true);
    openOAuthPopup(provider, `sync-reauth-${syncId}-${role}`, {
      onSuccess: async (msg) => {
        try {
          const res = await apiFetch(`${apiUrl}/api/sync/${syncId}/reauth`, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` }, body: JSON.stringify({ role, access_token: msg.token, refresh_token: msg.refreshToken, expires_in: msg.expiresIn }) });
          if (!res.ok) { const body = await res.json().catch(() => ({})); throw new Error(body.error_code ? translateApiError(body.error_code) : t('sync.startFailed')); }
          await handleTriggerStart();
        } catch (err) { toast(err instanceof Error ? err.message : t('sync.startFailed')); } finally { setActionLoading(false); }
      },
      onError: (code) => { toast(translateApiError(code)); setActionLoading(false); },
    });
  };

  const handlePause = async () => {
    setActionLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/sync/${syncId}/pause`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({} as { error_code?: string }));
        throw new Error(body.error_code ? translateApiError(body.error_code) : t('sync.pauseFailed'));
      }
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('sync.pauseFailed'));
    }

    finally { setActionLoading(false); }
  };

  const handleResume = async () => {
    setActionLoading(true);
    try {
      const res = await apiFetch(`${apiUrl}/api/sync/${syncId}/resume`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({} as { error_code?: string }));
        throw new Error(body.error_code ? translateApiError(body.error_code) : t('sync.resumeFailed'));
      }
    } catch (err: unknown) {
      toast(err instanceof Error ? err.message : t('sync.resumeFailed'));
    }
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
        <LoadingIndicator label={t('common.loading')} />
      </div>
    );
  }

  if (error || !job) {
    return (
      <div className="space-y-4">
        <button onClick={onBack} className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]">
          <ArrowLeftIcon className="h-4 w-4" aria-hidden="true" />
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
      <div className="ui-card p-6 space-y-6">
        <TransferDetailHeader backLabel={t('common.back')} onBack={onBack} title={t('sync.syncJobDetail')} id={job.id} actions={
          <>
            {['IDLE', 'PAUSED', 'FAILED'].includes(job.status) && (
              <button
                onClick={() => setIsEditing(true)}
                disabled={actionLoading}
                className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-xs font-bold hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
                aria-label={t('sync.editScope')}
                title={t('sync.editScope')}
              >
                <PencilIcon className="h-4 w-4" aria-hidden="true" />
                <span>{t('sync.edit')}</span>
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
            ) : canPause ? (
              <button
                onClick={handlePause}
                disabled={actionLoading || !canPause}
                className="ui-button-secondary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
              >
                {t('sync.pause')}
              </button>
            ) : null}

            {canStart && (
              <button
                onClick={job.status === 'FAILED' && /authentication failed|oauth token refresh failed/i.test(job.error_message || '') ? handleReauth : handleTriggerStart}
                disabled={actionLoading}
                className="ui-button-primary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:opacity-90 disabled:opacity-50"
              >
                {actionLoading && `${t('common.loading')} `}
                {job.status === 'FAILED' && /authentication failed|oauth token refresh failed/i.test(job.error_message || '') ? t('settings.connections.reauthenticate') : t('sync.syncNow')}
              </button>
            )}
          </>
        } />

        <div className="flex items-center gap-2.5">
          <StatusBadge status={job.status} />
          <Badge variant="muted" label={job.direction === 'two_way' ? t('sync.twoWay') : t('sync.oneWay')} />
        </div>

        {/* Live Transfer Progress (only shown while a run is active) */}
        {(job.status === 'RUNNING' || job.status === 'INDEXING') && <TransferProgress progress={byteProgressPercent} rate={`${formatBytes(speed)}/s`} transferred={totalBytes > 0 ? `${formatBytes(effectiveBytesDisplay)} / ${formatBytes(totalBytes)}` : `${job.processed_files} / ${job.total_files}`} remaining={eta} labels={{ progress: t('dashboard.progress'), transferRate: t('dashboard.transferRate'), transferred: t('dashboard.transferred'), remaining: t('dashboard.remaining') }} />}

        <TransferEndpoints sourceLabel={t('migrations.source')} targetLabel={t('migrations.target')} oauthLabel={t('migrations.oauth')} sourceProvider={job.source_provider} sourceUrl={job.source_url} selectedPaths={job.selected_paths} targetProvider={job.target_provider} targetUrl={job.target_url} targetDir={job.target_dir} />

        {/* Active transfers and run status follow the migration-detail layout. */}
        <div className="grid grid-cols-1 gap-6 pt-2 md:grid-cols-2">
          <ActiveTransfersPanel title={t('sync.activeTransfersTitle', { count: job.active_files?.length || 0, threads })} activeFiles={job.active_files} runningLabel={t('dashboard.running')} emptyLabel={t('dashboard.noActiveTransfers')} />
          <TransferStatusPanel title={`${t('migrations.status')} & ${t('dashboard.progress')}`} rows={[{ label: t('dashboard.filesTotal'), value: job.total_files }, { label: t('sync.changedFiles'), value: job.changed_files, tone: 'success' }, { label: t('sync.deletedFiles'), value: job.deleted_files }, { label: t('dashboard.failed'), value: job.failed_files, tone: job.failed_files > 0 ? 'error' : 'default' }]} />
        </div>

        {/* Timing, Schedule & Configuration Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 pt-2">
          {/* Column 1: Schedule & Timing */}
          {(() => {
            let nextRunLabel = t('sync.neverRun');
            if (job.status === 'PAUSED') {
              nextRunLabel = t('sync.statusPaused');
            } else if (job.next_run_at) {
              const nextRunMs = new Date(job.next_run_at).getTime();
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
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed">
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
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed">
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
          onDownloadReport={job.failed_files > 0 || job.last_run_status === 'PARTIAL' || job.last_run_status === 'FAILED' ? handleDownloadReport : undefined}
        />

        {job.error_message && (
          <div className="ui-card p-4 bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-xs font-mono text-[var(--color-error-text)] flex items-start gap-2">
            <span>{job.error_message}</span>
          </div>
        )}
      </div>

      {isEditing && job && (
        <div className="fixed inset-0 bg-[var(--color-overlay)] z-[var(--layer-dialog)] flex items-center justify-center p-4 overflow-y-auto">
          <div className="ui-card w-full max-w-5xl p-6 bg-[var(--color-bg-primary)] border-[var(--color-border)] shadow-xl relative max-h-[90vh] overflow-y-auto text-left">
            {/*
              In edit mode (existingSyncJob provided), password and token fields pass empty strings.
              The server-side handler GET /api/sync/{id}/browse uses stored encrypted credentials from the database.
            */}
            <FileBrowser
              initialFiles={[]}
              credentials={{
                source_provider: (job.source_provider || "nextcloud") as Provider,
                target_provider: (job.target_provider || "nextcloud") as Provider,
                source_url: job.source_url || "",
                target_url: job.target_url || "",
                source_username: job.source_username || "",
                target_username: job.target_username || "",
                source_password: "",
                target_password: "",
                source_refresh_token: "",
                source_token_expires_in: 0,
                target_refresh_token: "",
                target_token_expires_in: 0,
              }}
              apiUrl={apiUrl}
              token={token}
              existingSyncJob={job}
              onBack={() => setIsEditing(false)}
              onStartSuccess={async () => {
                setIsEditing(false);
                toast(t('sync.scopeUpdated'));
                try {
                  const res = await apiFetch(`${apiUrl}/api/sync/${syncId}`, {
                    headers: { Authorization: `Bearer ${token}` },
                  });
                  if (res.ok) {
                    const updated = await res.json();
                    setJob(updated);
                  }
                } catch (err) {
                  console.error('Failed to re-fetch sync job details after edit:', err);
                }
              }}
            />
          </div>
        </div>
      )}
    </div>
  );
}
