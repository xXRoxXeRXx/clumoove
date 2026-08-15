import React, { useEffect, useState, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useFormat, type TFunc } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { useConfirm } from '../contexts/useConfirm';
import { useToast } from '../contexts/useToast';
import { useTransferMetrics } from '../hooks/useTransferMetrics';
import { Badge } from './Badge';
import { StatusBadge } from './StatusBadge';
import { BANDWIDTH_OPTIONS, valueToBandwidthIndex, bandwidthIndexToValue, getBandwidthLabel } from '../utils/bandwidth';
import { apiFetch } from '../utils/apiClient';
import { ErrorOverview } from './ErrorOverview';
import { LoadingIndicator } from './LoadingIndicator';
import { TransferDetailHeader } from './TransferDetailHeader';
import { TransferProgress } from './TransferProgress';
import { TransferEndpoints } from './TransferEndpoints';
import { ActiveTransfersPanel, TransferStatusPanel } from './TransferRunSummary';
import { connectSseLoop } from '../utils/sse';
import { useOAuthPopup } from '../hooks/useOAuthPopup';
import { logger } from '../utils/logger';
import { isAuthFailureError } from '../utils/authFailure';
import {
  QueueListIcon,
  SignalIcon,
} from './icons';

interface DashboardProps {
  migrationId: string;
  apiUrl: string;
  onReset: () => void;
  token: string;
}

interface ResourceStats {
  total: number;
  processed: number;
  failed: number;
  skipped: number;
}

interface MigrationResourceStats {
  files?: ResourceStats;
  calendars?: ResourceStats;
  contacts?: ResourceStats;
}

interface ProgressData {
  id: string;
  status: string;
  total_files: number;
  total_bytes: number;
  processed_files: number;
  processed_bytes: number;
  live_bytes?: number;
  skipped_files: number;
  failed_files: number;
  error_message: string;
  active_file: string;
  active_files?: string[];
  threads?: number;
  bandwidth_limit_mbps?: number;
  resource_stats?: MigrationResourceStats;
  source_provider?: string;
  source_url?: string | null;
  target_provider?: string;
  target_url?: string | null;
  target_dir?: string;
  selected_paths?: string[];
  selected_calendars?: string[];
  selected_contacts?: string[];
  created_at?: string;
}

const renderResourceSection = (title: string, stats: ResourceStats | undefined, t: TFunc) => {
  if (!stats || stats.total === 0) return null;
  const success = stats.processed;
  return (
    <div className="w-full mt-4 first:mt-0 first:border-t-0 first:pt-0 border-t border-[var(--color-border-light)] pt-4 text-[var(--color-text-muted)] text-left">
      <h5 className="font-bold text-[var(--color-text-secondary)] mb-2 uppercase tracking-wider text-[10px]">{title}</h5>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.total')}:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.total}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.success')}:</span>
        <span className="font-bold text-[var(--color-success-text)] font-mono">{success}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.skipped')}:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.skipped}</span>
      </div>
      <div className="flex justify-between items-center py-1">
        <span>{t('dashboard.failed')}:</span>
        <span className={`font-bold font-mono ${stats.failed > 0 ? 'text-[var(--color-error-text)]' : 'text-[var(--color-text-secondary)]'}`}>
          {stats.failed}
        </span>
      </div>
    </div>
  );
};

export const Dashboard: React.FC<DashboardProps> = ({ migrationId, apiUrl, onReset, token }) => {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();
  const confirm = useConfirm();
  const toast = useToast();
  const { openOAuthPopup } = useOAuthPopup(apiUrl);
  const { speed, eta, updateMetrics, reset: resetMetrics, prevStatusRef } = useTransferMetrics();

  const [data, setData] = useState<ProgressData | null>(null);
  const [controlLoading, setControlLoading] = useState<string | null>(null);
  const [serverUnreachable, setServerUnreachable] = useState<boolean>(false);
  const [reconnectNonce, setReconnectNonce] = useState<number>(0);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(0);
  const [bandwidthLoading, setBandwidthLoading] = useState<boolean>(false);
  const [threads, setThreads] = useState<number>(8);
  const [threadsLoading, setThreadsLoading] = useState<boolean>(false);

  const handleDownloadReport = async (e?: React.MouseEvent) => {
    e?.preventDefault();
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/report`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
      const blob = await response.blob();
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `migration_report_${migrationId}.csv`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.URL.revokeObjectURL(url);
    } catch (err) {
      logger.error('Failed to download migration report', err);
      toast(err instanceof Error ? err.message : t('dashboard.downloadFailed'), 'error');
    }
  };

  const handleMigrationControl = async (action: 'pause' | 'resume' | 'cancel') => {
    if (action === 'cancel') {
      const ok = await confirm({ message: t('dashboard.cancelConfirm') });
      if (!ok) return;
    }

    setControlLoading(action);
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/${action}`, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
      // Status is reflected by the migration SSE stream.
    } catch (err) {
      logger.error(`Failed to ${action} migration`, err);
      toast(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  const commitBandwidthChange = async (value: number) => {
    setBandwidthLoading(true);
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/bandwidth`, {
        method: 'PUT',
        headers: {
          'Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ limit_mbps: value }),
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
    } catch (err) {
      logger.error('Failed to update migration bandwidth limit', err);
      toast(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }), 'error');
    } finally {
      setBandwidthLoading(false);
    }
  };

  const commitThreadsChange = async (value: number) => {
    setThreadsLoading(true);
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/threads`, {
        method: 'PUT',
        headers: {
          'Authorization': `Bearer ${token}`,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ threads: value }),
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
    } catch (err) {
      logger.error('Failed to update migration thread count', err);
      toast(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }), 'error');
    } finally {
      setThreadsLoading(false);
    }
  };

  const handleRetryFailed = async () => {
    const oauthProviders = ['dropbox', 'google', 'onedrive', 'hidrive'];
    const authFailed = data?.status === 'FAILED' && isAuthFailureError(data.error_message);
    const role = data?.source_provider && oauthProviders.includes(data.source_provider) ? 'source' : 'target';
    const provider = role === 'source' ? data?.source_provider : data?.target_provider;
    if (authFailed && provider && oauthProviders.includes(provider)) {
      setControlLoading('retry');
      openOAuthPopup(provider, `migration-reauth-${migrationId}-${role}`, {
        onSuccess: async (msg) => {
          try {
            const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/reauth`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${token}` }, body: JSON.stringify({ role, access_token: msg.token, refresh_token: msg.refreshToken, expires_in: msg.expiresIn }) });
            if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(translateApiError(body.error_code)); }
            setReconnectNonce((n) => n + 1);
          } catch (err) { toast(err instanceof Error ? err.message : t('dashboard.actionFailedMsg', { action: 'reauth' }), 'error'); } finally { setControlLoading(null); }
        }, onError: (code) => { toast(translateApiError(code), 'error'); setControlLoading(null); },
      });
      return;
    }
    const ok = await confirm({ message: t('dashboard.retryConfirm') });
    if (!ok) return;

    setControlLoading('retry');
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/${migrationId}/retry-failed`, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
      const resData = await response.json();
      if (resData.success && resData.retried > 0) {
        setReconnectNonce((n) => n + 1);
      } else {
        toast(t('dashboard.noFailed'), 'info');
      }
    } catch (err) {
      logger.error('Failed to retry migration tasks', err);
      toast(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }), 'error');
    } finally {
      setControlLoading(null);
    }
  };

  const threadsDraggingRef = useRef<boolean>(false);


  useEffect(() => {
    resetMetrics();
    let isMounted = true;
    const controller = new AbortController();

    const sanitizeErrorMsg = (val: unknown): string => {
      if (typeof val === 'string') return val;
      if (val && typeof val === 'object' && 'String' in val && (val as { Valid?: boolean }).Valid) {
        return String((val as { String?: unknown }).String || '');
      }
      return '';
    };

    // Keep the direct fetch so details are visible while the SSE connection opens.
    apiFetch(`${apiUrl}/api/migration/${migrationId}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((res) => (res.ok ? res.json() : null))
      .then((initialData) => {
        if (isMounted && initialData) {
          initialData.error_message = sanitizeErrorMsg(initialData.error_message);
          setData((prev) => (prev ? { ...initialData, ...prev } : initialData));
        }
      })
      .catch((err) => logger.error('Initial migration fetch error', err));

    void connectSseLoop({
      url: `${apiUrl}/api/migration/${migrationId}/stream`,
      signal: controller.signal,
      fetchImpl: apiFetch,
      handlers: {
        onEvent: (event, eventData) => {
          if (event === 'error') {
            if (isMounted) setServerUnreachable(true);
            controller.abort();
            return;
          }
          if (event !== 'migration' || !eventData) return;
          let payload: ProgressData;
          try {
            payload = JSON.parse(eventData);
          } catch (err) {
            logger.error('Failed to parse migration SSE data', err);
            return;
          }
          payload.error_message = sanitizeErrorMsg(payload.error_message);
          setServerUnreachable(false);
          setData((prev) => (prev ? { ...prev, ...payload } : payload));

          if (payload.bandwidth_limit_mbps !== undefined) {
            setBandwidthLimit(payload.bandwidth_limit_mbps);
          }
          if (payload.threads !== undefined && !threadsDraggingRef.current) {
            setThreads(payload.threads);
          }

          updateMetrics(payload);
          if (prevStatusRef.current === 'COMPLETED' || prevStatusRef.current === 'COMPLETED_WITH_ERRORS' || prevStatusRef.current === 'FAILED') {
            controller.abort();
          }
        },
        onError: () => {
          // Retrying is handled by connectSseLoop; the banner is deferred until
          // its bounded exponential backoff has been exhausted.
        },
      },
      onRetryScheduled: (delayMs) => {
        if (delayMs >= 16000 && isMounted) {
          setServerUnreachable(true);
          controller.abort();
        }
      },
    });

    return () => {
      isMounted = false;
      controller.abort();
    };
  // prevStatusRef is stable, so it is intentionally omitted.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [migrationId, apiUrl, token, reconnectNonce, resetMetrics, updateMetrics]);

  if (serverUnreachable) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <p className="font-sans text-sm font-semibold text-[var(--color-text-secondary)]">{t('dashboard.serverUnreachable')}</p>
        <p className="font-sans text-xs text-[var(--color-text-muted)] text-center max-w-sm">
          {t('dashboard.serverUnreachableText')}
        </p>
        <button
          type="button"
          onClick={() => {
            setServerUnreachable(false);
            setReconnectNonce((n) => n + 1);
          }}
          className="ui-button-primary mt-2 px-4 py-2 text-xs font-bold hover:opacity-90"
        >
          {t('common.retry')}
        </button>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <LoadingIndicator label={t('common.loading')} />
      </div>
    );
  }

  // Calculated stats using live stream bytes for smooth progress
  const effectiveBytesDisplay = typeof data.live_bytes === 'number'
    ? Math.min(data.total_bytes, Math.max(data.processed_bytes, data.live_bytes))
    : data.processed_bytes;

  const byteProgressPercent = data.total_bytes > 0
    ? Math.min(Math.round((effectiveBytesDisplay / data.total_bytes) * 100), 100)
    : 0;

  const successFiles = Math.max(0, data.processed_files - data.failed_files - data.skipped_files);

  return (
    <div className="w-full space-y-6">
      <div className="ui-card p-6 space-y-6">
        <TransferDetailHeader backLabel={t('common.back')} onBack={onReset} title={t('migrations.migrationJobDetail')} id={data.id} actions={
          <>
            {data.status === 'PAUSED' || data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <button
                onClick={() => handleMigrationControl('resume')}
                disabled={controlLoading !== null}
                className="ui-button-primary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:opacity-90 disabled:opacity-50"
              >
                {t('dashboard.resume')}
              </button>
            ) : (
              <button
                onClick={() => handleMigrationControl('pause')}
                disabled={controlLoading !== null || data.status === 'COMPLETED' || data.status === 'FAILED' || data.status === 'CANCELLED'}
                className="ui-button-secondary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
              >
                {t('dashboard.pause')}
              </button>
            )}

            {(data.status === 'COMPLETED' || data.status === 'COMPLETED_WITH_ERRORS' || data.status === 'FAILED') && (data.failed_files > 0 || data.processed_files < data.total_files) && (
              <button
                onClick={handleRetryFailed}
                disabled={controlLoading !== null}
                className="ui-button-primary flex items-center gap-2 px-4 py-2 text-xs font-bold hover:opacity-90 disabled:opacity-50"
              >
                {controlLoading === 'retry' && `${t('common.loading')} `}
                {data.status === 'FAILED' && isAuthFailureError(data.error_message) ? t('settings.connections.reauthenticate') : t('dashboard.retryFailed')}
              </button>
            )}
          </>
        } />

        <div className="flex items-center gap-2.5">
          <StatusBadge status={data.status} />
          <Badge variant="muted" label={t('sync.oneWay')} />
        </div>

        {/* Live Transfer Progress (ONLY rendered when RUNNING or INDEXING) */}
        {(data.status === 'RUNNING' || data.status === 'INDEXING') && <TransferProgress progress={byteProgressPercent} rate={`${formatBytes(speed)}/s`} transferred={`${formatBytes(effectiveBytesDisplay)} / ${formatBytes(data.total_bytes)}`} remaining={eta} labels={{ progress: t('dashboard.progress'), transferRate: t('dashboard.transferRate'), transferred: t('dashboard.transferred'), remaining: t('dashboard.remaining') }} />}

        <TransferEndpoints sourceLabel={t('migrations.source')} targetLabel={t('migrations.target')} oauthLabel={t('migrations.oauth')} sourceProvider={data.source_provider} sourceUrl={data.source_url} selectedPaths={data.selected_paths} targetProvider={data.target_provider} targetUrl={data.target_url} targetDir={data.target_dir} />

        {/* Active Transfers & Status / Summary 2-Column Grid */}
        <div className="grid grid-cols-1 gap-6 pt-2 md:grid-cols-2">
          <ActiveTransfersPanel title={t('dashboard.activeTransfers', { count: data.active_files?.length || 0, threads })} activeFiles={data.active_files} runningLabel={t('dashboard.running')} emptyLabel={t('dashboard.noActiveTransfers')} />
          <TransferStatusPanel title={`${t('migrations.status')} & ${t('dashboard.progress')}`}>
            <div className="space-y-2 font-sans text-xs text-[var(--color-text-muted)]">
              {data.resource_stats ? (
                <>
                  {renderResourceSection(t('dashboard.files'), data.resource_stats.files, t)}
                  {renderResourceSection(t('dashboard.calendars'), data.resource_stats.calendars, t)}
                  {renderResourceSection(t('dashboard.contacts'), data.resource_stats.contacts, t)}
                </>
              ) : (
                <>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>{t('dashboard.filesTotal')}</span>
                    <span className="font-bold text-[var(--color-text-primary)] font-mono">{data.total_files}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>{t('dashboard.success')}:</span>
                    <span className="font-bold text-[var(--color-success-text)] font-mono">{successFiles}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>{t('dashboard.skipped')}:</span>
                    <span className="font-bold text-[var(--color-text-primary)] font-mono">{data.skipped_files}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5">
                    <span>{t('dashboard.failed')}:</span>
                    <span className={`font-bold font-mono ${data.failed_files > 0 ? 'text-[var(--color-error-text)]' : 'text-[var(--color-text-muted)]'}`}>
                      {data.failed_files}
                    </span>
                  </div>
                </>
              )}
            </div>
          </TransferStatusPanel>
        </div>

        {/* Performance Controls Grid: Bandwidth Limit & Threads side by side */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 pt-2">
          {/* Bandwidth Limit Box */}
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <SignalIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                {t('dashboard.bandwidthLimit')}
              </h3>
            </div>

            <div className="ui-card p-3.5 bg-[var(--color-bg-primary)] space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="migration-bandwidth-limit" className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                  {t('dashboard.bandwidthLimit')}
                </label>
                <span className="text-[11px] font-bold text-[var(--color-text-primary)] font-mono">
                  {getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited'))}
                </span>
              </div>
              <input
                id="migration-bandwidth-limit"
                type="range"
                min={0}
                max={BANDWIDTH_OPTIONS.length - 1}
                step={1}
                value={valueToBandwidthIndex(bandwidthLimit)}
                disabled={bandwidthLoading}
                onChange={(e) => setBandwidthLimit(bandwidthIndexToValue(Number(e.target.value)))}
                onPointerUp={(e) => {
                  const idx = Number((e.target as HTMLInputElement).value);
                  commitBandwidthChange(bandwidthIndexToValue(idx));
                }}
                className="w-full cursor-pointer accent-[var(--color-text-primary)]"
              />
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-sans">
                {bandwidthLimit === 0 ? (
                  t('fileBrowser.bandwidthUnlimited')
                ) : (
                  t('fileBrowser.bandwidthHint', { limit: getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited')) })
                )}
              </p>
            </div>
          </div>

          {/* Threads / Simultaneous Transfers Box */}
          <div className="ui-card p-5 space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <QueueListIcon className="h-4 w-4 text-[var(--color-text-muted)]" aria-hidden="true" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                {t('dashboard.threads')}
              </h3>
            </div>

            <div className="ui-card p-3.5 bg-[var(--color-bg-primary)] space-y-2">
              <div className="flex items-center justify-between">
                <label htmlFor="migration-threads" className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                  {t('dashboard.threads')}
                </label>
                <span className="text-[11px] font-bold text-[var(--color-text-primary)] font-mono">{threads}</span>
              </div>
              <input
                id="migration-threads"
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
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-mono">
                {t('dashboard.threadsHint')}
              </p>
            </div>
          </div>
        </div>

        <ErrorOverview
          endpoint={`${apiUrl}/api/migration/${migrationId}/errors`}
          token={token}
          refreshKey={`${data.failed_files}-${data.status}`}
          onDownloadReport={data.failed_files > 0 ? handleDownloadReport : undefined}
        />

        {data.error_message.trim() !== '' && (
          <div className="ui-card p-4 bg-[var(--color-error-bg)] border-[var(--color-error-border)] text-xs font-mono text-[var(--color-error-text)] flex items-start gap-2">
            <span>{data.error_message}</span>
          </div>
        )}
      </div>
    </div>
  );
};
