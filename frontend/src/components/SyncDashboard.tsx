import { useState, useEffect, useRef, useCallback } from 'react';
import { Play, Pause, ArrowLeft, RefreshCw, Download, CheckCircle2, XCircle, AlertTriangle, Loader2, HardDrive, Clock } from 'lucide-react';
import type { SyncJob } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat, formatBytes, formatDuration } from '../utils/format';
import { useApiError } from '../utils/apiError';

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
  const [threads, setThreads] = useState<number>(4);
  const [threadsLoading, setThreadsLoading] = useState<boolean>(false);
  const [speed, setSpeed] = useState<number>(0);
  const [eta, setEta] = useState<string>('');
  const threadsDraggingRef = useRef<boolean>(false);
  const progressHistory = useRef<{ timestamp: number; bytes: number }[]>([]);
  const lastActiveSpeed = useRef<number>(0);
  const lastActiveTime = useRef<number>(0);

  const { t } = useTranslation();
  const { formatDateTime } = useFormat();
  const translateApiError = useApiError();

  useEffect(() => {
    if (job?.threads !== undefined && !threadsDraggingRef.current) {
      setThreads(job.threads);
    }
  }, [job?.threads]);

  // Live speed and ETA calculation helper (called on fetch/SSE updates)
  const updateMetrics = useCallback((data: SyncJob) => {
    if (data.status === 'COMPLETED') {
      setSpeed(0);
      setEta(t('dashboard.eta.done'));
    } else if (data.status === 'PAUSED' || data.status === 'IDLE') {
      setSpeed(0);
      setEta('-');
    } else if (data.status === 'PAUSED_CONNECTION_LOSS') {
      setSpeed(0);
      setEta(t('dashboard.eta.waitingConn'));
    } else {
      const processedBytes = data.processed_bytes || 0;
      const liveBytes = typeof data.live_bytes === 'number' ? data.live_bytes : processedBytes;
      const totalBytes = data.total_bytes || 0;
      const now = Date.now();

      progressHistory.current.push({ timestamp: now, bytes: liveBytes });
      const windowLimit = now - 15000;
      progressHistory.current = progressHistory.current.filter((item) => item.timestamp >= windowLimit);

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
            const timeSinceLastActive = now - lastActiveTime.current;
            if (lastActiveSpeed.current > 0 && timeSinceLastActive < 15000) {
              calculatedSpeed = lastActiveSpeed.current;
            } else {
              calculatedSpeed = 0;
            }
          }

          setSpeed(calculatedSpeed);

          const effectiveBytes = Math.min(totalBytes, Math.max(processedBytes, liveBytes));
          const remainingBytes = Math.max(0, totalBytes - effectiveBytes);
          if (remainingBytes <= 0 && totalBytes > 0) {
            setEta(t('dashboard.eta.done'));
          } else if (calculatedSpeed > 0 && totalBytes > 0) {
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
  }, [t]);

  const commitThreadsChange = async (value: number) => {
    setThreadsLoading(true);
    try {
      const response = await fetch(`${apiUrl}/api/sync/${syncId}/threads`, {
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
        alert(msg);
        if (job?.threads) setThreads(job.threads);
      }
    } catch (err) {
      console.error(err);
      alert(t('dashboard.threadsFailed'));
      if (job?.threads) setThreads(job.threads);
    } finally {
      setThreadsLoading(false);
    }
  };

  useEffect(() => {
    let cancelled = false;
    const fetchJob = async () => {
      try {
        const res = await fetch(`${apiUrl}/api/sync/${syncId}`, {
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

    // SSE Stream for live updates
    const controller = new AbortController();
    const connectSSE = async () => {
      try {
        const response = await fetch(`${apiUrl}/api/sync/stream`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        });
        if (!response.ok || !response.body) return;

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (!cancelled) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });

          let idx: number;
          while ((idx = buffer.indexOf('\n\n')) !== -1) {
            const frame = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);

            let event = 'message';
            let data = '';
            for (const line of frame.split('\n')) {
              if (line.startsWith('event:')) event = line.slice(6).trim();
              else if (line.startsWith('data:')) data += (data ? '\n' : '') + line.slice(5).trim();
            }

            if (event === 'sync_jobs' && data) {
              try {
                const jobs: SyncJob[] = JSON.parse(data);
                const updated = jobs.find((j) => j.id === syncId);
                if (updated && !cancelled) {
                  setJob(updated);
                  updateMetrics(updated);
                }
              } catch { /* ignore */ }
            }
          }
        }
      } catch { /* ignore */ }
    };

    connectSSE();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [apiUrl, syncId, token, t, translateApiError, updateMetrics]);

  const handleTriggerStart = async () => {
    setActionLoading(true);
    try {
      const res = await fetch(`${apiUrl}/api/sync/${syncId}/start`, {
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
      alert(err instanceof Error ? err.message : t('sync.startFailed'));
    } finally {
      setActionLoading(false);
    }
  };

  const handlePause = async () => {
    setActionLoading(true);
    try {
      await fetch(`${apiUrl}/api/sync/${syncId}/pause`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch { /* ignore */ }
    finally { setActionLoading(false); }
  };

  const handleResume = async () => {
    setActionLoading(true);
    try {
      await fetch(`${apiUrl}/api/sync/${syncId}/resume`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch { /* ignore */ }
    finally { setActionLoading(false); }
  };

  const handleDownloadReport = async (e?: React.MouseEvent) => {
    if (e) e.preventDefault();
    try {
      const response = await fetch(`${apiUrl}/api/sync/${syncId}/report`, {
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
      alert(t('dashboard.downloadFailed'));
    }
  };

  const getStatusBadge = (status: string) => {
    switch (status) {
      case 'IDLE':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-emerald-50 text-emerald-700 border border-emerald-200">
            <CheckCircle2 className="w-4 h-4" />
            {t('sync.statusIdle')}
          </span>
        );
      case 'RUNNING':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-[var(--color-info-bg)] text-blue-700 border border-[var(--color-info-border)] animate-pulse">
            <Loader2 className="w-4 h-4 animate-spin" />
            {t('status.active')}
          </span>
        );
      case 'INDEXING':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-amber-50 text-amber-700 border border-amber-200">
            <Loader2 className="w-4 h-4 animate-spin" />
            {t('status.indexing')}
          </span>
        );
      case 'PAUSED':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)] border border-[var(--color-border)]">
            <Pause className="w-4 h-4" />
            {t('status.paused')}
          </span>
        );
      case 'FAILED':
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-[var(--color-error-bg)] text-rose-700 border border-[var(--color-error-border)]">
            <XCircle className="w-4 h-4" />
            {t('status.failed')}
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)]">
            {status}
          </span>
        );
    }
  };

  if (loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-4">
        <Loader2 className="w-8 h-8 text-[var(--color-portal-orange-themed)] animate-spin" />
        <p className="text-xs font-mono text-[var(--color-text-muted)]">{t('common.loading')}</p>
      </div>
    );
  }

  if (error || !job) {
    return (
      <div className="space-y-4">
        <button onClick={onBack} className="flex items-center gap-2 text-xs font-bold text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)]">
          <ArrowLeft className="w-4 h-4" /> {t('common.back')}
        </button>
        <div className="p-4 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] text-rose-700 rounded-xl text-sm font-mono text-center">
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
    : (job?.total_files && job.total_files > 0 ? Math.min(Math.round((job.processed_files / job.total_files) * 100), 100) : 0);

  return (
    <div className="w-full space-y-6 animate-fade-in">
      {/* Top Bar */}
      <div className="flex items-center justify-between">
        <button
          onClick={onBack}
          className="flex items-center gap-2 px-3.5 py-2 rounded-xl border border-[var(--color-border)] text-xs font-bold text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] transition-colors cursor-pointer"
        >
          <ArrowLeft className="w-4 h-4" />
          {t('common.back')}
        </button>

        <div className="flex items-center gap-3">
          {job.status === 'PAUSED' ? (
            <button
              onClick={handleResume}
              disabled={actionLoading}
              className="flex items-center gap-2 bg-emerald-600 hover:bg-emerald-700 text-white px-4 py-2 rounded-xl text-xs font-bold shadow-sm cursor-pointer disabled:opacity-50"
            >
              <Play className="w-4 h-4 fill-white" />
              {t('sync.resume')}
            </button>
          ) : (
            <button
              onClick={handlePause}
              disabled={actionLoading || job.status === 'INDEXING' || job.status === 'RUNNING'}
              className="flex items-center gap-2 bg-[var(--color-bg-tertiary)] hover:bg-[var(--color-border)] text-[var(--color-text-primary)] px-4 py-2 rounded-xl text-xs font-bold border border-[var(--color-border)] cursor-pointer disabled:opacity-50"
            >
              <Pause className="w-4 h-4" />
              {t('sync.pause')}
            </button>
          )}

          <button
            onClick={handleTriggerStart}
            disabled={actionLoading || job.status === 'INDEXING' || job.status === 'RUNNING'}
            className="flex items-center gap-2 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-white px-4 py-2 rounded-xl text-xs font-bold shadow-sm cursor-pointer disabled:opacity-50"
          >
            {actionLoading ? <Loader2 className="w-4 h-4 animate-spin" /> : <RefreshCw className="w-4 h-4" />}
            {t('sync.syncNow')}
          </button>
        </div>
      </div>

      {/* Main Header Panel */}
      <div className="glass-panel border border-[var(--color-glass-border)] rounded-3xl p-6 shadow-portal space-y-6">
        <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 border-b border-[var(--color-border)] pb-6">
          <div className="space-y-1">
            <div className="flex items-center gap-3">
              <h1 className="font-display font-extrabold text-2xl text-[var(--color-portal-navy-themed)]">
                {t('sync.syncJobDetail')}
              </h1>
              {getStatusBadge(job.status)}
            </div>
            <p className="text-xs text-[var(--color-text-muted)] font-mono">
              ID: {job.id}
            </p>
          </div>

          {(job.failed_files > 0 || job.last_run_status === 'PARTIAL' || job.last_run_status === 'FAILED') && (
            <button
              onClick={handleDownloadReport}
              className="flex items-center gap-2 px-3.5 py-2 rounded-xl bg-rose-50 text-rose-700 border border-rose-200 text-xs font-bold hover:bg-rose-100 transition-colors cursor-pointer"
            >
              <Download className="w-4 h-4" />
              {t('sync.downloadReport')}
            </button>
          )}
        </div>

        {/* Source -> Target Connection Banner */}
        <div className="p-4 rounded-2xl bg-[var(--color-bg-tertiary)]/60 border border-[var(--color-border)] flex flex-col sm:flex-row items-center justify-between gap-4">
          <div className="flex items-center gap-3 w-full sm:w-auto">
            <div className="flex flex-col text-left">
              <span className="text-[10px] font-mono uppercase text-[var(--color-text-muted)]">{t('migrations.source')}</span>
              <span className="font-bold text-sm text-[var(--color-text-primary)] capitalize">{job.source_provider}</span>
              <span className="text-xs text-[var(--color-text-muted)] truncate max-w-[180px]">{job.source_url || t('migrations.oauth')}</span>
            </div>
          </div>

          <div className="flex items-center gap-2 font-mono text-xs font-bold text-[var(--color-portal-orange-themed)] uppercase px-3 py-1.5 rounded-full bg-white shadow-xs border border-[var(--color-border)]">
            {job.direction === 'two_way' ? '↔ ' + t('sync.twoWay') : '→ ' + t('sync.oneWay')}
          </div>

          <div className="flex items-center gap-3 w-full sm:w-auto justify-end">
            <div className="flex flex-col text-right">
              <span className="text-[10px] font-mono uppercase text-[var(--color-text-muted)]">{t('migrations.target')}</span>
              <span className="font-bold text-sm text-[var(--color-text-primary)] capitalize">{job.target_provider}</span>
              <span className="text-xs text-[var(--color-text-muted)] truncate max-w-[180px]">{job.target_url || t('migrations.oauth')}</span>
            </div>
          </div>
        </div>

        {/* Config Summary Grid */}
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 text-xs">
          <div className="p-3.5 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)]">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.interval')}</span>
            <span className="font-bold text-sm text-[var(--color-text-primary)] mt-1 block">
              {job.interval_minutes} {t('sync.minutes')}
            </span>
          </div>

          <div className="p-3.5 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)]">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.conflictStrategy')}</span>
            <span className="font-bold text-sm text-[var(--color-text-primary)] mt-1 block">
              {job.conflict_strategy}
            </span>
          </div>

          <div className="p-3.5 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)]">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.deletePropagation')}</span>
            <span className={`font-bold text-sm mt-1 block ${job.delete_propagation ? 'text-rose-600' : 'text-emerald-600'}`}>
              {job.delete_propagation ? t('common.enabled') : t('common.disabled')}
            </span>
          </div>

          <div className="p-3.5 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)]">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase block">{t('sync.lastRun')}</span>
            <span className="font-bold text-xs text-[var(--color-text-primary)] mt-1 block truncate">
              {job.last_run_at ? formatDateTime(job.last_run_at) : '-'}
            </span>
          </div>
        </div>

        {/* Threads Slider */}
        <div className="p-4 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-primary)] space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-xs font-semibold text-[var(--color-text-secondary)]">
              {t('dashboard.threads')}
            </label>
            <span className="text-xs font-bold text-portal-orange font-mono">{threads}</span>
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

        {/* Main metric card for progress, speed, bytes, and ETA */}
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
              <span>
                {t('dashboard.transferred')}:{' '}
                <strong className="text-[var(--color-text-primary)]">
                  {totalBytes > 0 ? formatBytes(effectiveBytesDisplay) : `${job.processed_files}`}
                </strong>
                {totalBytes > 0 ? ` / ${formatBytes(totalBytes)}` : ` / ${job.total_files}`}
              </span>
            </div>
            <div className="flex items-center gap-2 justify-end">
              <Clock className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
              <span>{t('dashboard.remaining')}: <strong className="text-[var(--color-text-primary)]">{eta}</strong></span>
            </div>
          </div>
        </div>

        {/* Active Transfers Card */}
        {(job.status === 'RUNNING' || job.status === 'INDEXING') && job.active_files && job.active_files.length > 0 && (
          <div className="glass-panel border border-[var(--color-glass-border)] p-5 shadow-portal rounded-3xl flex flex-col">
            <div className="flex items-center gap-2 mb-4 pb-3 border-b border-[var(--color-border-light)]">
              <RefreshCw className="w-4 h-4 text-portal-orange animate-spin" />
              <h4 className="font-mono font-bold text-[var(--color-text-muted)] text-[10px] uppercase tracking-widest text-left">
                {t('dashboard.activeTransfers', { count: job.active_files.length, threads: job.threads || 4 })}
              </h4>
            </div>
            <div className="space-y-2">
              {job.active_files.map((file, i) => {
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

        {/* Live / Last Run Statistics */}
        <div className="space-y-3 pt-2">
          <h3 className="font-display font-extrabold text-sm text-[var(--color-portal-navy-themed)]">
            {t('sync.runStats')}
          </h3>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
            <div className="p-4 rounded-2xl border border-[var(--color-glass-border)] bg-slate-50 flex flex-col">
              <span className="text-[10px] font-mono uppercase text-[var(--color-text-muted)]">{t('sync.processedFiles')}</span>
              <span className="font-display font-extrabold text-xl text-[var(--color-text-primary)] mt-1">
                {job.processed_files} / {job.total_files}
              </span>
            </div>

            <div className="p-4 rounded-2xl border border-[var(--color-glass-border)] bg-blue-50 flex flex-col">
              <span className="text-[10px] font-mono uppercase text-blue-600 font-bold">{t('sync.changedFiles')}</span>
              <span className="font-display font-extrabold text-xl text-blue-700 mt-1">
                {job.changed_files}
              </span>
            </div>

            <div className="p-4 rounded-2xl border border-[var(--color-glass-border)] bg-purple-50 flex flex-col">
              <span className="text-[10px] font-mono uppercase text-brand-violet font-bold">{t('sync.deletedFiles')}</span>
              <span className="font-display font-extrabold text-xl text-purple-700 mt-1">
                {job.deleted_files}
              </span>
            </div>

            <div className="p-4 rounded-2xl border border-[var(--color-glass-border)] bg-rose-50 flex flex-col">
              <span className="text-[10px] font-mono uppercase text-rose-600 font-bold">{t('sync.failedFiles')}</span>
              <span className="font-display font-extrabold text-xl text-rose-700 mt-1">
                {job.failed_files}
              </span>
            </div>
          </div>
        </div>

        {job.error_message && (
          <div className="p-4 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] rounded-2xl text-xs font-mono text-rose-700 flex items-start gap-2">
            <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600 mt-0.5" />
            <span>{job.error_message}</span>
          </div>
        )}
      </div>
    </div>
  );
}
