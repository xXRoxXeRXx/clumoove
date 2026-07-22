import React, { useEffect, useState, useRef } from 'react';
import { RefreshCw, AlertTriangle, Download, Clock, HardDrive, Pause, Play, XCircle, Loader2, ArrowLeft, ArrowRight, Folder, CheckCircle2 } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useFormat, formatDuration, type TFunc } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { SelectedPathsViewer } from './SelectedPathsViewer';

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
  const success = Math.max(0, stats.processed - stats.failed - stats.skipped);
  return (
    <div className="w-full mt-4 first:mt-0 first:border-t-0 first:pt-0 border-t border-[var(--color-border-light)] pt-4 text-[var(--color-text-muted)] text-left">
      <h5 className="font-bold text-[var(--color-text-secondary)] mb-2 uppercase tracking-wider text-[10px]">{title}</h5>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.total')}:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.total}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.success')}:</span>
        <span className="font-bold text-emerald-600 font-mono">{success}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>{t('dashboard.skipped')}:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.skipped}</span>
      </div>
      <div className="flex justify-between items-center py-1">
        <span>{t('dashboard.failed')}:</span>
        <span className={`font-bold font-mono ${stats.failed > 0 ? 'text-rose-600' : 'text-[var(--color-text-secondary)]'}`}>
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

  const [data, setData] = useState<ProgressData | null>(null);
  const [controlLoading, setControlLoading] = useState<string | null>(null);
  const [speed, setSpeed] = useState<number>(0); // Bytes per second
  const [eta, setEta] = useState<string>(t('dashboard.eta.computing'));
  const [serverUnreachable, setServerUnreachable] = useState<boolean>(false);
  const [reconnectNonce, setReconnectNonce] = useState<number>(0);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(0);
  const [bandwidthLoading, setBandwidthLoading] = useState<boolean>(false);
  const [threads, setThreads] = useState<number>(4);
  const [threadsLoading, setThreadsLoading] = useState<boolean>(false);

  const handleDownloadReport = async (e: React.MouseEvent) => {
    e.preventDefault();
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/report`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        throw new Error(t('dashboard.downloadFailed'));
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
      console.error(err);
      alert(t('dashboard.downloadFailed'));
    }
  };

  const handleMigrationControl = async (action: 'pause' | 'resume' | 'cancel') => {
    if (action === 'cancel' && !window.confirm(t('dashboard.cancelConfirm'))) {
      return;
    }

    setControlLoading(action);
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/${action}`, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        const body = (await response.json().catch(() => ({}))) as { error_code?: string };
        throw new Error(translateApiError(body.error_code));
      }
      // Status will automatically update via WebSocket
    } catch (err) {
      console.error(err);
      alert(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }));
    } finally {
      setControlLoading(null);
    }
  };

  const commitBandwidthChange = async (value: number) => {
    setBandwidthLoading(true);
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/bandwidth`, {
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
      console.error(err);
      alert(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }));
    } finally {
      setBandwidthLoading(false);
    }
  };

  const commitThreadsChange = async (value: number) => {
    setThreadsLoading(true);
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/threads`, {
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
      console.error(err);
      alert(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }));
    } finally {
      setThreadsLoading(false);
    }
  };

  const progressHistory = useRef<{ timestamp: number; bytes: number }[]>([]);

  const handleRetryFailed = async () => {
    if (!window.confirm(t('dashboard.retryConfirm'))) {
      return;
    }

    setControlLoading('retry');
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/retry-failed`, {
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
        alert(t('dashboard.noFailed'));
      }
    } catch (err) {
      console.error(err);
      alert(t('dashboard.actionFailed', { msg: err instanceof Error ? err.message : String(err) }));
    } finally {
      setControlLoading(null);
    }
  };

  const lastActiveSpeed = useRef<number>(0);
  const lastActiveTime = useRef<number>(0);

  const prevStatusRef = useRef<string>('');
  const threadsDraggingRef = useRef<boolean>(false);


  useEffect(() => {
    progressHistory.current = [];
    lastActiveSpeed.current = 0;
    lastActiveTime.current = 0;
    prevStatusRef.current = '';

    // Construct WebSocket URL. The backend authenticates the socket by accepting
    // the JWT either as a query parameter (HTTP only) or as a WebSocket
    // subprotocol (works over both HTTP and HTTPS). On HTTPS the query-param path
    // is explicitly rejected (see handleWebSocket / ErrWsTokenInsecure), so we
    // must pass the token via the Subprotocol argument to keep the socket
    // authenticated over wss://. The backend echoes it back in the handshake.
    const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const apiUrlObj = new URL(apiUrl.startsWith('http') ? apiUrl : `${window.location.origin}${apiUrl}`);
    const wsUrl = `${wsProto}://${apiUrlObj.host}/api/migration/${migrationId}/ws`;

    let isMounted = true;

    const sanitizeErrorMsg = (val: unknown): string => {
      if (typeof val === 'string') return val;
      if (val && typeof val === 'object' && 'String' in val && (val as { Valid?: boolean }).Valid) {
        return String((val as { String?: unknown }).String || '');
      }
      return '';
    };

    // Fetch initial migration details immediately to avoid waiting for initial WS tick
    fetch(`${apiUrl}/api/migration/${migrationId}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then((res) => (res.ok ? res.json() : null))
      .then((initialData) => {
        if (isMounted && initialData) {
          initialData.error_message = sanitizeErrorMsg(initialData.error_message);
          setData((prev) => (prev ? { ...initialData, ...prev } : initialData));
        }
      })
      .catch((err) => console.error('Initial migration fetch error:', err));

    // Pass the JWT as the subprotocol (2nd arg) so the secure HTTPS path works.
    let ws = new WebSocket(wsUrl, token);

    ws.onopen = () => {
      // Connection established
    };

    ws.onmessage = (event) => {
      let payload: ProgressData;
      try {
        payload = JSON.parse(event.data);
      } catch (err) {
        console.error("Failed to parse progress data:", err);
        return;
      }
      payload.error_message = sanitizeErrorMsg(payload.error_message);
      setData((prev) => (prev ? { ...prev, ...payload } : payload));

      if (payload.bandwidth_limit_mbps !== undefined) {
        setBandwidthLimit(payload.bandwidth_limit_mbps);
      }
      if (payload.threads !== undefined && !threadsDraggingRef.current) {
        setThreads(payload.threads);
      }

      // Reset progress history if status changes to avoid calculations across states
      if (payload.status !== prevStatusRef.current) {
        progressHistory.current = [];
        lastActiveSpeed.current = 0;
        lastActiveTime.current = 0;
      }

      prevStatusRef.current = payload.status;

      // Speed and ETA calculation
      if (payload.status === 'COMPLETED' || payload.status === 'COMPLETED_WITH_ERRORS') {
        setSpeed(0);
        setEta(t('dashboard.eta.done'));
      } else if (payload.status === 'FAILED') {
        setSpeed(0);
        setEta(t('dashboard.eta.failed'));
      } else if (payload.status === 'INDEXING') {
        setSpeed(0);
        setEta(t('dashboard.eta.indexing'));
      } else if (payload.status === 'PENDING') {
        setSpeed(0);
        setEta(t('dashboard.eta.pending'));
      } else if (payload.status === 'PAUSED_CONNECTION_LOSS') {
        setSpeed(0);
        setEta(t('dashboard.eta.waitingConn'));
      } else {
        // RUNNING or other states.
        // Speed/ETA use the frequent live_bytes counter (driven by the
        // streaming progress channel); the "transferred X / Y" byte display
        // uses processed_bytes, which can never exceed total_bytes.
        const liveBytes = typeof payload.live_bytes === 'number' ? payload.live_bytes : payload.processed_bytes;
        const now = Date.now();
        progressHistory.current.push({ timestamp: now, bytes: liveBytes });

        // Keep last 15 seconds of history to smooth speed
        const windowLimit = now - 15000;
        progressHistory.current = progressHistory.current.filter(item => item.timestamp >= windowLimit);

        if (progressHistory.current.length >= 2) {
          const oldest = progressHistory.current[0];
          const newest = progressHistory.current[progressHistory.current.length - 1];
          const timeDiffSec = (newest.timestamp - oldest.timestamp) / 1000;

          if (timeDiffSec > 0.5) {
            const bytesDiff = newest.bytes - oldest.bytes;

            let calculatedSpeed: number;

            if (bytesDiff > 0) {
              calculatedSpeed = bytesDiff / timeDiffSec;
              lastActiveSpeed.current = calculatedSpeed;
              lastActiveTime.current = now;
            } else {
              // No progress in this window. Check if we are in the grace period
              const timeSinceLastActive = now - lastActiveTime.current;
              if (lastActiveSpeed.current > 0 && timeSinceLastActive < 15000) {
                calculatedSpeed = lastActiveSpeed.current;
              } else {
                calculatedSpeed = 0;
              }
            }

            setSpeed(calculatedSpeed);

            // ETA calculation using effective bytes (in-flight live bytes reduce remaining bytes)
            const effectiveBytes = Math.min(payload.total_bytes, Math.max(payload.processed_bytes, liveBytes));
            const remainingBytes = Math.max(0, payload.total_bytes - effectiveBytes);
            if (remainingBytes <= 0) {
              setEta(t('dashboard.eta.done'));
            } else if (calculatedSpeed > 0) {
              const etaSec = remainingBytes / calculatedSpeed;
              setEta(formatDuration(etaSec, t));
            } else {
              setEta(t('dashboard.eta.computing'));
            }
          }
        } else {
          setSpeed(0);
          setEta(t('dashboard.eta.computing'));
        }
      }
    };

    ws.onerror = (err) => {
      if (!isMounted) return;
      console.error('WS Error:', err);
    };

    // Reconnect with exponential backoff (cap 30 s). If the migration ID came from
    // a bookmarked URL and the server is temporarily down, we surface a clear banner
    // instead of leaving the user on a frozen loading spinner.
    let reconnectDelay = 1000;
    let reconnectTimeout: ReturnType<typeof setTimeout>;
    ws.onclose = () => {
      if (!isMounted) return;
      if (prevStatusRef.current === 'COMPLETED' || prevStatusRef.current === 'COMPLETED_WITH_ERRORS' || prevStatusRef.current === 'FAILED') {
        return;
      }

      // Ping API to trigger token refresh if it expired during WebSocket connection (I4 WS fix)
      fetch(`${apiUrl}/api/auth/me`, {
        headers: { 'Authorization': `Bearer ${token}` }
      }).catch(err => console.error("WS connection loss auth check failed:", err));

      if (reconnectDelay > 15000) {
        setServerUnreachable(true);
        return;
      }
      reconnectTimeout = setTimeout(() => {
        reconnectDelay = Math.min(reconnectDelay * 2, 30000);
        const wsProtoR = window.location.protocol === 'https:' ? 'wss' : 'ws';
        const apiUrlObjR = new URL(apiUrl.startsWith('http') ? apiUrl : `${window.location.origin}${apiUrl}`);
        const wsUrlR = `${wsProtoR}://${apiUrlObjR.host}/api/migration/${migrationId}/ws`;
        const wsR = new WebSocket(wsUrlR, token);
        wsR.onopen = ws.onopen;
        wsR.onmessage = ws.onmessage;
        wsR.onerror = ws.onerror;
        wsR.onclose = ws.onclose;
        ws = wsR;
      }, reconnectDelay);
    };

    return () => {
      isMounted = false;
      clearTimeout(reconnectTimeout);
      ws.close();
    };
  }, [migrationId, apiUrl, token, reconnectNonce, t]);

  if (serverUnreachable) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <AlertTriangle className="w-10 h-10 text-amber-500" />
        <p className="font-sans text-sm font-semibold text-[var(--color-text-secondary)]">{t('dashboard.serverUnreachable')}</p>
        <p className="font-sans text-xs text-[var(--color-text-muted)] text-center max-w-sm">
          {t('dashboard.serverUnreachableText')}
        </p>
        <button
          onClick={() => window.location.reload()}
          className="mt-2 px-4 py-2 bg-portal-orange text-white text-xs font-bold rounded-lg hover:bg-portal-orange-hover transition-colors cursor-pointer"
        >
          {t('dashboard.reload')}
        </button>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <RefreshCw className="w-10 h-10 text-[var(--color-portal-navy-themed)] animate-spin" />
        <p className="font-sans text-xs italic text-[var(--color-text-muted)]">{t('dashboard.loadingInfo')}</p>
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

  const getStatusBadge = (status: string) => {
    switch (status) {
      case 'COMPLETED':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-emerald-50 text-emerald-700 border border-emerald-200">
            <CheckCircle2 className="w-3.5 h-3.5" />
            {t('status.completed')}
          </span>
        );
      case 'FAILED':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-rose-50 text-rose-700 border border-rose-200">
            <XCircle className="w-3.5 h-3.5" />
            {t('status.failed')}
          </span>
        );
      case 'COMPLETED_WITH_ERRORS':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-amber-50 text-amber-700 border border-amber-200">
            <AlertTriangle className="w-3.5 h-3.5" />
            {t('status.completedWithErrors')}
          </span>
        );
      case 'PAUSED':
      case 'PAUSED_CONNECTION_LOSS':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-amber-50 text-amber-750 border border-amber-250 animate-pulse">
            <Pause className="w-3.5 h-3.5" />
            {t('status.paused')}
          </span>
        );
      case 'CANCELLED':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-rose-50 text-rose-700 border border-rose-200">
            <XCircle className="w-3.5 h-3.5" />
            {t('status.cancelled')}
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-blue-50 text-blue-700 border border-blue-200 animate-pulse">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            {t('status.transfer')}
          </span>
        );
    }
  };

  return (
    <div className="w-full space-y-6 animate-fade-in">
      {/* Back Button on its own line */}
      <div>
        <button
          onClick={onReset}
          className="flex items-center gap-2 px-3.5 py-2 rounded-xl border border-[var(--color-border)] text-xs font-bold text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] transition-colors cursor-pointer"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('common.back')}
        </button>
      </div>

      {/* Main Glass Panel containing all content */}
      <div className="glass-panel border border-[var(--color-glass-border)] rounded-3xl p-6 shadow-portal space-y-6">
        {/* Top Badges Row (Above Title & Action Buttons) */}
        <div className="flex items-center justify-end gap-2.5 pb-2">
          {/* Status Info Badge */}
          {getStatusBadge(data.status)}

          {/* Direction Info Badge (rechtsbündig) */}
          <span className="inline-flex items-center gap-1.5 text-xs font-bold text-orange-700 px-3 py-1 rounded-full bg-orange-50 border border-orange-200">
            <ArrowRight className="w-3.5 h-3.5" />
            <span>{t('sync.oneWay')}</span>
          </span>
        </div>

        {/* Title & Action Controls */}
        <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 border-b border-[var(--color-border)] pb-6">
          <div className="space-y-1">
            <h1 className="font-display font-extrabold text-2xl text-[var(--color-portal-navy-themed)]">
              {t('migrations.migrationJobDetail')}
            </h1>
            <p className="text-xs text-[var(--color-text-muted)] font-mono">
              ID: {data.id}
            </p>
          </div>

          <div className="flex items-center gap-2.5 w-full md:w-auto justify-start md:justify-end flex-wrap">
            {data.failed_files > 0 && (
              <button
                onClick={handleDownloadReport}
                className="flex items-center gap-2 px-3.5 py-2 rounded-xl bg-rose-50 text-rose-700 border border-rose-200 text-xs font-bold hover:bg-rose-100 transition-colors cursor-pointer"
              >
                <Download className="w-4 h-4" />
                {t('sync.downloadReport')}
              </button>
            )}

            {data.status === 'PAUSED' || data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <button
                onClick={() => handleMigrationControl('resume')}
                disabled={controlLoading !== null}
                className="flex items-center gap-2 bg-emerald-600 hover:bg-emerald-700 text-white px-4 py-2 rounded-xl text-xs font-bold shadow-xs cursor-pointer disabled:opacity-50 transition-colors"
              >
                <Play className="w-4 h-4 fill-white" />
                {t('dashboard.resume')}
              </button>
            ) : (
              <button
                onClick={() => handleMigrationControl('pause')}
                disabled={controlLoading !== null || data.status === 'COMPLETED' || data.status === 'FAILED' || data.status === 'CANCELLED'}
                className="flex items-center gap-2 bg-[var(--color-bg-tertiary)] hover:bg-[var(--color-border)] text-[var(--color-text-primary)] px-4 py-2 rounded-xl text-xs font-bold border border-[var(--color-border)] cursor-pointer disabled:opacity-50 transition-colors"
              >
                <Pause className="w-4 h-4" />
                {t('dashboard.pause')}
              </button>
            )}

            {(data.status === 'COMPLETED' || data.status === 'COMPLETED_WITH_ERRORS' || data.status === 'FAILED' || data.status === 'CANCELLED') && data.failed_files > 0 ? (
              <button
                onClick={handleRetryFailed}
                disabled={controlLoading !== null}
                className="flex items-center gap-2 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-white px-4 py-2 rounded-xl text-xs font-bold shadow-xs cursor-pointer disabled:opacity-50 transition-all"
              >
                {controlLoading === 'retry' ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
                {t('dashboard.retryFailed')}
              </button>
            ) : (
              <button
                onClick={onReset}
                className="flex items-center gap-2 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-white px-4 py-2 rounded-xl text-xs font-bold shadow-xs cursor-pointer transition-all"
              >
                <RefreshCw className="w-4 h-4" />
                {t('dashboard.newMigration')}
              </button>
            )}
          </div>
        </div>

        {/* Source & Target Connection Cards Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          {/* Source Card */}
          <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <Folder className="w-4 h-4 text-portal-orange" />
              <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                {t('migrations.source')}
              </h3>
            </div>
            
            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">
                {data.source_provider || 'nextcloud'}
              </div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
                {data.source_url || t('migrations.oauth')}
              </div>
              <SelectedPathsViewer paths={data.selected_paths} />
            </div>
          </div>

          {/* Target Card */}
          <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <Folder className="w-4 h-4 text-emerald-600" />
              <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                {t('migrations.target')}
              </h3>
            </div>

            <div className="space-y-2">
              <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">
                {data.target_provider || 'nextcloud'}
              </div>
              <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
                {data.target_url || t('migrations.oauth')}
              </div>
              <div className="flex flex-wrap gap-1.5 pt-1">
                <span className="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg bg-white border border-[var(--color-border)] text-[10px] font-mono text-portal-navy shadow-2xs">
                  <Folder className="w-3.5 h-3.5 text-emerald-500 shrink-0" />
                  <span>{data.target_dir || '/'}</span>
                </span>
              </div>
            </div>
          </div>
        </div>

        {/* Live Transfer Progress & Active Files (ONLY rendered when RUNNING or INDEXING) */}
        {(data.status === 'RUNNING' || data.status === 'INDEXING') && (
          <div className="space-y-4 pt-2">
            <div className="glass-panel border border-[var(--color-glass-border)] p-6 shadow-portal rounded-3xl relative overflow-hidden flex flex-col group">
              <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-orange to-orange-500" />

              <div className="flex items-end justify-between mb-6 border-b border-[var(--color-border-light)] pb-4.5">
                <div>
                  <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('dashboard.progress')}</span>
                  <h3 className="font-display font-extrabold text-5xl text-[var(--color-portal-navy-themed)] mt-1.5 leading-none">
                    {byteProgressPercent}%
                  </h3>
                </div>
                <div className="text-right flex flex-col items-end">
                  <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('dashboard.transferRate')}</span>
                  <p className="text-base font-extrabold text-emerald-600 mt-1.5 font-mono">
                    {formatBytes(speed)}/s
                  </p>
                </div>
              </div>

              {/* Glowing Rounded Progress Bar */}
              <div className="w-full bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] h-5 p-0.5 mb-6 rounded-full shadow-inner relative overflow-hidden">
                <div
                  className="bg-gradient-to-r from-portal-orange to-orange-500 h-full rounded-full transition-all duration-500 ease-out relative"
                  style={{ width: `${byteProgressPercent}%` }}
                >
                  <div className="absolute inset-0 bg-[linear-gradient(45deg,rgba(255,255,255,0.15)_25%,transparent_25%,transparent_50%,rgba(255,255,255,0.15)_50%,rgba(255,255,255,0.15)_75%,transparent_75%,transparent)] bg-[length:16px_16px] animate-pulse" />
                </div>
              </div>

              <div className="grid grid-cols-2 gap-4 text-[10px] font-mono font-bold text-[var(--color-text-muted)] uppercase tracking-wider">
                <div className="flex items-center gap-2">
                  <HardDrive className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
                  <span>{t('dashboard.transferred')}: <strong className="text-[var(--color-text-primary)]">{formatBytes(effectiveBytesDisplay)}</strong> / {formatBytes(data.total_bytes)}</span>
                </div>
                <div className="flex items-center gap-2 justify-end">
                  <Clock className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
                  <span>{t('dashboard.remaining')}: <strong className="text-[var(--color-text-primary)]">{eta}</strong></span>
                </div>
              </div>
            </div>

            {/* Active Transfers List */}
            {data.active_files && data.active_files.length > 0 && (
              <div className="glass-panel border border-[var(--color-glass-border)] p-5 shadow-portal rounded-3xl flex flex-col">
                <div className="flex items-center gap-2 mb-4 pb-3 border-b border-[var(--color-border-light)]">
                  <RefreshCw className="w-4 h-4 text-portal-orange animate-spin" />
                  <h4 className="font-mono font-bold text-[var(--color-text-muted)] text-[10px] uppercase tracking-widest text-left">
                    {t('dashboard.activeTransfers', { count: data.active_files.length, threads: threads })}
                  </h4>
                </div>
                <div className="space-y-2">
                  {data.active_files.map((file, i) => {
                    const fileName = file.split('/').pop() || file;
                    return (
                      <div key={i} className="flex items-center justify-between text-xs py-2.5 px-3.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-xl font-mono text-[var(--color-text-secondary)] min-w-0">
                        <span className="truncate pr-4" title={file}>{fileName}</span>
                        <span className="text-[10px] text-emerald-600 font-semibold uppercase animate-pulse shrink-0 bg-emerald-50 border border-emerald-200 px-2 py-0.5 rounded-md">{t('dashboard.running')}</span>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        )}

        {/* Configuration / Performance & Summary Grid */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6 pt-2">
          {/* Column 1: Progress & Status */}
          <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4">
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
              <Clock className="w-4 h-4 text-portal-orange" />
              <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                {t('migrations.status')} & {t('dashboard.progress')}
              </h3>
            </div>

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
                    <span className="font-bold text-emerald-600 font-mono">{successFiles}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>{t('dashboard.skipped')}:</span>
                    <span className="font-bold text-[var(--color-text-primary)] font-mono">{data.skipped_files}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5">
                    <span>{t('dashboard.failed')}:</span>
                    <span className={`font-bold font-mono ${data.failed_files > 0 ? 'text-rose-600' : 'text-[var(--color-text-muted)]'}`}>
                      {data.failed_files}
                    </span>
                  </div>
                </>
              )}
            </div>
          </div>

          {/* Column 2: Performance & Sliders */}
          <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4 flex flex-col justify-between">
            <div className="space-y-4">
              <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-2.5">
                <HardDrive className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
                <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                  {t('dashboard.bandwidthLimit')} & {t('dashboard.threads')}
                </h3>
              </div>

              {(data.status === 'RUNNING' || data.status === 'INDEXING') && (
                <div className="p-3.5 rounded-xl bg-[var(--color-bg-primary)] border border-[var(--color-border)] space-y-2">
                  <div className="flex items-center justify-between">
                    <label className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                      {t('dashboard.bandwidthLimit')}
                    </label>
                    <span className="text-[11px] font-bold text-portal-orange font-mono">
                      {bandwidthLimit === 0 ? t('dashboard.unlimited') : `${bandwidthLimit} Mbps`}
                    </span>
                  </div>
                  <input
                    type="range"
                    min={0}
                    max={1000}
                    step={1}
                    value={bandwidthLimit}
                    disabled={bandwidthLoading}
                    onChange={(e) => setBandwidthLimit(Number(e.target.value))}
                    onPointerUp={(e) => commitBandwidthChange(Number((e.target as HTMLInputElement).value))}
                    className="w-full"
                  />
                </div>
              )}
            </div>

            {/* Integrated Threads Slider */}
            <div className="p-3.5 rounded-xl bg-[var(--color-bg-primary)] border border-[var(--color-border)] space-y-2 mt-auto">
              <div className="flex items-center justify-between">
                <label className="text-[11px] font-semibold text-[var(--color-text-secondary)]">
                  {t('dashboard.threads')}
                </label>
                <span className="text-[11px] font-bold text-portal-orange font-mono">{threads}</span>
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
                className="w-full"
              />
              <p className="text-[9px] text-[var(--color-text-muted)] leading-relaxed">
                {t('dashboard.threadsHint')}
              </p>
            </div>
          </div>
        </div>

        {typeof data.error_message === 'string' && data.error_message.trim() !== '' && (
          <div className="p-4 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] rounded-2xl text-xs font-mono text-rose-700 flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600 mt-0.5" />
            <span>{data.error_message}</span>
          </div>
        )}
      </div>
    </div>
  );
};
