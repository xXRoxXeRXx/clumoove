import { useState, useEffect, useCallback } from 'react';
import { Play, Trash2, ArrowRight, RefreshCw, Layers, Calendar, HardDrive, CheckCircle2, XCircle, AlertTriangle, Loader2 } from 'lucide-react';
import type { User, Migration, SyncJob } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { useApiError } from '../utils/apiError';

interface MigrationsDashboardProps {
  apiUrl: string;
  token: string;
  user: User | null;
  onStartNewMigration: () => void;
  onSelectActiveMigration: (id: string) => void;
  onSelectActiveSync?: (id: string) => void;
}

export function MigrationsDashboard({
  apiUrl,
  token,
  user,
  onStartNewMigration,
  onSelectActiveMigration,
  onSelectActiveSync,
}: MigrationsDashboardProps) {
  const [activeTab, setActiveTab] = useState<'migrations' | 'sync'>('migrations');
  const [migrations, setMigrations] = useState<Migration[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [deleteLoading, setDeleteLoading] = useState<string | null>(null);

  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();

  const fetchMigrations = useCallback(async () => {
    try {
      const response = await fetch(`${apiUrl}/api/migration`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        let message = t('migrations.loadFailed');
        try {
          const body = await response.json();
          if (body?.error_code) {
            message = translateApiError(body.error_code);
          }
        } catch {
          /* ignore non-JSON bodies */
        }
        throw new Error(message);
      }
      const data = await response.json();
      setMigrations(data || []);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('migrations.connectionError'));
    } finally {
      setLoading(false);
    }
  }, [apiUrl, token, t, translateApiError]);

  useEffect(() => {
    let cancelled = false;
    let retryDelay = 2000;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    const controller = new AbortController();

    const connect = async () => {
      if (cancelled) return;
      try {
        const response = await fetch(`${apiUrl}/api/migration/stream`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        });
        if (!response.ok || !response.body) {
          throw new Error('stream_unavailable');
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';
        let received = false;

        const read = async () => {
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
                if (line.startsWith('event:')) {
                  event = line.slice(6).trim();
                } else if (line.startsWith('data:')) {
                  data += (data ? '\n' : '') + line.slice(5).trim();
                }
              }

              if (event === 'migrations' && data) {
                try {
                  setMigrations(JSON.parse(data) || []);
                  setError('');
                  setLoading(false);
                  received = true;
                } catch {
                  /* ignore malformed frame */
                }
              } else if (event === 'error') {
                setError(t('migrations.connectionError'));
                setLoading(false);
              }
            }
          }
        };

        await read();
        reader.releaseLock();

        if (received) {
          retryDelay = 2000;
          setError('');
        }
      } catch {
        if (cancelled || controller.signal.aborted) return;
        setError(t('migrations.connectionError'));
        setLoading(false);
      }

      if (!cancelled) {
        retryTimer = setTimeout(connect, retryDelay);
        retryDelay = Math.min(retryDelay * 2, 30000);
      }
    };

    connect();

    return () => {
      cancelled = true;
      if (retryTimer) clearTimeout(retryTimer);
      controller.abort();
    };
  }, [apiUrl, token, t]);

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    
    if (!window.confirm(t('migrations.deleteConfirm'))) {
      return;
    }

    setDeleteLoading(id);
    try {
      const response = await fetch(`${apiUrl}/api/migration/${id}`, {
        method: 'DELETE',
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        let message = t('migrations.deleteFailed');
        try {
          const body = await response.json();
          if (body?.error_code) {
            message = translateApiError(body.error_code);
          }
        } catch {
          /* ignore non-JSON bodies */
        }
        throw new Error(message);
      }
      setMigrations((prev) => prev.filter((m) => m.id !== id));
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : t('migrations.deleteError'));
    } finally {
      setDeleteLoading(null);
    }
  };

  const getStatusBadge = (status: string) => {
    switch (status) {
      case 'COMPLETED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-emerald-50 text-emerald-700 border border-emerald-200">
            <CheckCircle2 className="w-3.5 h-3.5" />
            {t('status.completed')}
          </span>
        );
      case 'FAILED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-[var(--color-error-bg)] text-rose-700 border border-[var(--color-error-border)]">
            <XCircle className="w-3.5 h-3.5" />
            {t('status.failed')}
          </span>
        );
      case 'COMPLETED_WITH_ERRORS':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-amber-50 text-amber-700 border border-amber-200">
            <AlertTriangle className="w-3.5 h-3.5" />
            {t('status.completedWithErrors')}
          </span>
        );
      case 'CANCELLED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-[var(--color-error-bg)] text-rose-700 border border-[var(--color-error-border)]">
            <XCircle className="w-3.5 h-3.5" />
            {t('status.cancelled')}
          </span>
        );
      case 'RUNNING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-[var(--color-info-bg)] text-blue-700 border border-[var(--color-info-border)] animate-pulse">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            {t('status.active')}
          </span>
        );
      case 'INDEXING':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-amber-50 text-amber-700 border border-amber-200">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            {t('status.indexing')}
          </span>
        );
      case 'PAUSED_CONNECTION_LOSS':
      case 'PAUSED':
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)] border border-[var(--color-border)]">
            {t('status.paused')}
          </span>
        );
      default:
        return (
          <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)] border border-[var(--color-border)]">
            {status}
          </span>
        );
    }
  };

  const totalMigrations = migrations.length;
  const activeMigrations = migrations.filter(m => m.status === 'RUNNING' || m.status === 'INDEXING').length;
  const completedMigrations = migrations.filter(m => m.status === 'COMPLETED' || m.status === 'COMPLETED_WITH_ERRORS').length;
  const failedMigrations = migrations.filter(m => m.status === 'FAILED' || m.status === 'CANCELLED').length;
  
  const successRate = (completedMigrations + failedMigrations) > 0 
    ? Math.round((completedMigrations / (completedMigrations + failedMigrations)) * 100) 
    : 100;

  const totalBytesMigrated = migrations.reduce((acc, m) => acc + (m.processed_bytes || 0), 0);

  return (
    <div className="w-full space-y-6 animate-fade-in">
      
      {/* Welcome Banner */}
      <div className="relative rounded-3xl p-8 bg-gradient-to-r from-portal-navy via-slate-900 to-portal-navy-dark text-[var(--color-text-inverse)] overflow-hidden shadow-md">
        <div className="absolute inset-0 bg-[radial-gradient(circle_at_30%_20%,rgba(255,102,0,0.15),transparent_60%)] pointer-events-none" />
        <div className="absolute -right-20 -bottom-20 w-80 h-80 bg-portal-orange/10 rounded-full blur-3xl pointer-events-none" />
        
        <div className="relative z-10 flex flex-col md:flex-row justify-between items-start md:items-center gap-6">
          <div className="space-y-2">
            <p className="text-[9px] font-mono tracking-widest text-[var(--color-portal-orange-themed)] font-bold uppercase">{t('migrations.tagline')}</p>
            <h1 className="font-display font-extrabold text-3xl tracking-tight">
              {t('migrations.welcome', { name: user?.display_name || t('common.user') })}
            </h1>
            <p className="text-sm text-[var(--color-text-muted)] max-w-xl">
              {t('migrations.welcomeSub')}
            </p>
          </div>
          
          <button
            onClick={onStartNewMigration}
            className="group flex items-center gap-2 bg-gradient-to-r from-portal-orange to-orange-500 hover:from-orange-500 hover:to-portal-orange text-[var(--color-text-inverse)] px-5 py-3 rounded-2xl text-xs font-mono font-bold tracking-wider uppercase transition-all duration-300 shadow-sm hover:shadow-md hover:-translate-y-0.5 active:translate-y-0 cursor-pointer shrink-0"
          >
            <Play className="w-4 h-4 fill-white group-hover:scale-110 transition-transform" />
            <span>{t('migrations.newMigration')}</span>
          </button>
        </div>
      </div>

      {/* Stats Widgets Grid */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        {/* Total Bytes */}
        <div className="glass-panel border border-[var(--color-glass-border)]/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-[var(--color-info-bg)] text-[var(--color-portal-navy-themed)] rounded-xl">
            <HardDrive className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.dataTransferred')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {formatBytes(totalBytesMigrated)}
            </span>
          </div>
        </div>

        {/* Total Migrations */}
        <div className="glass-panel border border-[var(--color-glass-border)]/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-purple-50 text-brand-violet rounded-xl">
            <Layers className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.migrations')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {totalMigrations}
            </span>
          </div>
        </div>

        {/* Active Transits */}
        <div className="glass-panel border border-[var(--color-glass-border)]/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4 relative overflow-hidden">
          {activeMigrations > 0 && (
            <div className="absolute top-2 right-2 flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75"></span>
              <span className="relative inline-flex rounded-full h-2 w-2 bg-emerald-500"></span>
            </div>
          )}
          <div className="p-3 bg-emerald-50 text-emerald-600 rounded-xl">
            <RefreshCw className={`w-5 h-5 stroke-[2] ${activeMigrations > 0 ? 'animate-spin' : ''}`} />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.active')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {activeMigrations}
            </span>
          </div>
        </div>

        {/* Success Rate */}
        <div className="glass-panel border border-[var(--color-glass-border)]/50 rounded-2xl p-4.5 shadow-portal flex items-center gap-4">
          <div className="p-3 bg-amber-50 text-[var(--color-portal-orange-themed)] rounded-xl">
            <CheckCircle2 className="w-5 h-5 stroke-[2]" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-mono text-[var(--color-text-muted)] uppercase tracking-wider">{t('migrations.successRate')}</span>
            <span className="font-display font-extrabold text-lg text-[var(--color-text-primary)] leading-tight mt-0.5">
              {successRate}%
            </span>
          </div>
        </div>
      </div>

      {/* Main Section with Tabs */}
      <div className="glass-panel rounded-3xl border border-[var(--color-glass-border)]/50 shadow-portal p-6">
        
        {/* Navigation Tabs Header */}
        <div className="flex items-center justify-between border-b border-[var(--color-border)] mb-6 pb-2">
          <div className="flex items-center gap-6">
            <button
              onClick={() => setActiveTab('migrations')}
              className={`pb-2 text-sm font-display font-extrabold transition-all cursor-pointer border-b-2 ${
                activeTab === 'migrations'
                  ? 'border-portal-orange text-[var(--color-portal-navy-themed)]'
                  : 'border-transparent text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
              }`}
            >
              {t('sync.tabMigrations')}
            </button>
            <button
              onClick={() => setActiveTab('sync')}
              className={`pb-2 text-sm font-display font-extrabold transition-all cursor-pointer border-b-2 ${
                activeTab === 'sync'
                  ? 'border-portal-orange text-[var(--color-portal-navy-themed)]'
                  : 'border-transparent text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
              }`}
            >
              {t('sync.tabSyncs')}
            </button>
          </div>

          <button
            onClick={activeTab === 'migrations' ? fetchMigrations : undefined}
            className="p-2 border border-[var(--color-border)] rounded-xl text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] hover:bg-[var(--color-bg-tertiary)]/50 transition-all cursor-pointer"
            title={t('common.refresh')}
          >
            <RefreshCw className={`w-4 h-4 ${loading && activeTab === 'migrations' ? 'animate-spin' : ''}`} />
          </button>
        </div>

        {activeTab === 'sync' ? (
          <SyncList
            apiUrl={apiUrl}
            token={token}
            onSelectActiveSync={onSelectActiveSync}
            onStartNewSync={onStartNewMigration}
          />
        ) : loading ? (
          <div className="flex flex-col items-center justify-center py-20 gap-4">
            <Loader2 className="w-8 h-8 text-[var(--color-portal-orange-themed)] animate-spin" />
            <p className="text-[10px] font-mono text-[var(--color-text-muted)] tracking-wider">{t('migrations.loadingData')}</p>
          </div>
        ) : error ? (
          <div className="p-4 bg-[var(--color-error-bg)]/80 border border-[var(--color-error-border)] text-[var(--color-error-text)] rounded-xl text-xs font-mono text-center">
            {error}
          </div>
        ) : migrations.length === 0 ? (
          <div className="text-center py-16 border-2 border-dashed border-[var(--color-border)] rounded-2xl bg-[var(--color-bg-tertiary)]/30">
            <Layers className="w-10 h-10 text-[var(--color-text-muted)] mx-auto mb-4" />
            <p className="font-display font-bold text-[var(--color-text-secondary)]">{t('migrations.noMigrations')}</p>
            <p className="text-[10px] text-[var(--color-text-muted)] font-mono mt-1 mb-5">{t('migrations.dbEmpty')}</p>
            <button
              onClick={onStartNewMigration}
              className="bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-sm px-5 py-2.5 rounded-xl text-xs font-bold font-mono uppercase tracking-wider transition-all cursor-pointer"
            >
              {t('migrations.startFirst')}
            </button>
          </div>
        ) : (
          <div className="overflow-x-auto scrollbar-portal">
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
              <tbody className="divide-y divide-slate-100">
                {migrations.map((mig) => {
                  const createdDate = formatDateTime(mig.created_at);

                  return (
                    <tr
                      key={mig.id}
                      onClick={() => onSelectActiveMigration(mig.id)}
                      className="hover:bg-[var(--color-bg-tertiary)]/50 transition-all duration-200 cursor-pointer group"
                    >
                      {/* Date */}
                      <td className="py-4 px-4 whitespace-nowrap">
                        <div className="flex items-center gap-2 text-xs font-mono text-[var(--color-text-secondary)]">
                          <Calendar className="w-3.5 h-3.5 text-[var(--color-text-muted)] group-hover:text-[var(--color-portal-orange-themed)] transition-colors" />
                          {createdDate}
                        </div>
                      </td>

                      {/* Providers */}
                      <td className="py-4 px-4">
                        <div className="flex items-center gap-2.5">
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize leading-snug">
                              {mig.source_provider}
                            </span>
                            <span className="text-[10px] text-[var(--color-text-muted)] max-w-[120px] truncate block">
                              {mig.source_url || t('migrations.oauth')}
                            </span>
                          </div>
                          
                          <ArrowRight className="w-3 h-3 text-[var(--color-text-muted)] shrink-0 group-hover:translate-x-0.5 transition-transform" />
                          
                          <div className="flex flex-col text-left">
                            <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize leading-snug">
                              {mig.target_provider}
                            </span>
                            <span className="text-[10px] text-[var(--color-text-muted)] max-w-[120px] truncate block">
                              {mig.target_url || t('migrations.oauth')}
                            </span>
                          </div>
                        </div>
                      </td>

                      {/* Status */}
                      <td className="py-4 px-4 whitespace-nowrap">
                        {getStatusBadge(mig.status)}
                      </td>

                      {/* Progress */}
                      <td className="py-4 px-4">
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
                          <div className="w-full bg-[var(--color-bg-tertiary)] rounded-full h-1.5 overflow-hidden shadow-inner">
                            <div
                              className={`h-full rounded-full transition-all duration-500 ${
                                mig.status === 'FAILED'
                                  ? 'bg-[var(--color-error-bg)]'
                                  : mig.status === 'COMPLETED_WITH_ERRORS'
                                  ? 'bg-amber-500'
                                  : mig.status === 'COMPLETED'
                                  ? 'bg-emerald-500'
                                  : 'bg-portal-orange'
                              }`}
                              style={{
                                width: `${
                                  mig.total_files > 0
                                    ? (mig.processed_files / mig.total_files) * 100
                                    : 0
                                  }%`,
                              }}
                            />
                          </div>
                        </div>
                      </td>

                      {/* Actions */}
                      <td className="py-4 px-4 text-right whitespace-nowrap" onClick={(e) => e.stopPropagation()}>
                        <div className="flex justify-end items-center gap-2">
                          <button
                            onClick={() => onSelectActiveMigration(mig.id)}
                            className="p-1.5 bg-[var(--color-bg-tertiary)] hover:bg-portal-navy hover:text-[var(--color-text-inverse)] rounded-lg text-[var(--color-text-muted)] transition-all cursor-pointer"
                            title={t('migrations.openDashboard')}
                          >
                            <Play className="w-3.5 h-3.5 fill-current" />
                          </button>
                          <button
                            onClick={(e) => handleDelete(mig.id, e)}
                            disabled={deleteLoading === mig.id || mig.status === 'RUNNING' || mig.status === 'INDEXING'}
                            className="p-1.5 bg-[var(--color-bg-tertiary)] border border-transparent rounded-lg text-[var(--color-text-muted)] hover:text-[var(--color-error-text)] hover:border-rose-100 hover:bg-[var(--color-error-bg)]/50 transition-all focus:outline-none disabled:opacity-30 disabled:pointer-events-none cursor-pointer"
                            title={t('migrations.deleteMigration')}
                          >
                            {deleteLoading === mig.id ? (
                              <Loader2 className="w-3.5 h-3.5 animate-spin" />
                            ) : (
                              <Trash2 className="w-3.5 h-3.5" />
                            )}
                          </button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}

      </div>
    </div>
  );
}

function SyncList({
  apiUrl,
  token,
  onSelectActiveSync,
  onStartNewSync,
}: {
  apiUrl: string;
  token: string;
  onSelectActiveSync?: (id: string) => void;
  onStartNewSync: () => void;
}) {
  const [syncJobs, setSyncJobs] = useState<SyncJob[]>([]);
  const [loading, setLoading] = useState<boolean>(true);
  const [error, setError] = useState<string>('');
  const [deleteLoading, setDeleteLoading] = useState<string | null>(null);

  const { t } = useTranslation();
  const { formatDateTime } = useFormat();
  const translateApiError = useApiError();

  const fetchSyncJobs = useCallback(async () => {
    try {
      const res = await fetch(`${apiUrl}/api/sync`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) throw new Error(t('sync.loadFailed'));
      const data = await res.json();
      setSyncJobs(data || []);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('sync.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [apiUrl, token, t]);

  useEffect(() => {
    // SSE Stream
    const controller = new AbortController();
    const connectSSE = async () => {
      void fetchSyncJobs();
      try {
        const res = await fetch(`${apiUrl}/api/sync/stream`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: controller.signal,
        });
        if (!res.ok || !res.body) return;
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (true) {
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
                setSyncJobs(JSON.parse(data) || []);
                setLoading(false);
              } catch { /* ignore */ }
            }
          }
        }
      } catch { /* ignore */ }
    };

    connectSSE();
    return () => controller.abort();
  }, [apiUrl, token, fetchSyncJobs]);

  const handleDelete = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!window.confirm(t('sync.deleteConfirm'))) return;

    setDeleteLoading(id);
    try {
      const res = await fetch(`${apiUrl}/api/sync/${id}`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        const msg = body?.error_code ? translateApiError(body.error_code) : t('sync.deleteFailed');
        throw new Error(msg);
      }
      setSyncJobs((prev) => prev.filter((j) => j.id !== id));
    } catch (err: unknown) {
      alert(err instanceof Error ? err.message : t('sync.deleteFailed'));
    } finally {
      setDeleteLoading(null);
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

  if (error) {
    return (
      <div className="p-4 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] text-rose-700 rounded-xl text-xs font-mono text-center">
        {error}
      </div>
    );
  }

  if (syncJobs.length === 0) {
    return (
      <div className="text-center py-16 border-2 border-dashed border-[var(--color-border)] rounded-2xl bg-[var(--color-bg-tertiary)]/30">
        <Layers className="w-10 h-10 text-[var(--color-text-muted)] mx-auto mb-4" />
        <p className="font-display font-bold text-[var(--color-text-secondary)]">{t('sync.noSyncJobs')}</p>
        <p className="text-[10px] text-[var(--color-text-muted)] font-mono mt-1 mb-5">{t('sync.noSyncSub')}</p>
        <button
          onClick={onStartNewSync}
          className="bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-sm px-5 py-2.5 rounded-xl text-xs font-bold font-mono uppercase tracking-wider transition-all cursor-pointer"
        >
          {t('sync.startFirst')}
        </button>
      </div>
    );
  }

  return (
    <div className="overflow-x-auto scrollbar-portal">
      <table className="w-full text-left border-collapse min-w-[600px]">
        <thead>
          <tr className="border-b border-[var(--color-border)]/60 text-[10px] font-bold text-[var(--color-text-muted)] uppercase font-mono tracking-wider">
            <th className="py-4.5 px-4 font-semibold">{t('migrations.createdAt')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('migrations.sourceTarget')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('sync.direction')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('migrations.status')}</th>
            <th className="py-4.5 px-4 font-semibold">{t('sync.lastRun')}</th>
            <th className="py-4.5 px-4 font-semibold text-right">{t('migrations.actions')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {syncJobs.map((job) => (
            <tr
              key={job.id}
              onClick={() => onSelectActiveSync && onSelectActiveSync(job.id)}
              className="hover:bg-[var(--color-bg-tertiary)]/50 transition-all duration-200 cursor-pointer group"
            >
              <td className="py-4 px-4 whitespace-nowrap text-xs font-mono text-[var(--color-text-secondary)]">
                {formatDateTime(job.created_at)}
              </td>
              <td className="py-4 px-4">
                <div className="flex items-center gap-2.5">
                  <div className="flex flex-col text-left min-w-0">
                    <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize">
                      {job.source_provider}
                    </span>
                    <span className="text-[10px] text-[var(--color-text-muted)] max-w-[130px] truncate block">
                      {job.source_url || t('migrations.oauth')}
                    </span>
                    <span className="text-[10px] font-mono text-portal-navy max-w-[130px] truncate block" title={job.selected_paths?.join(', ') || '/'}>
                      {t('sync.sourcePath')}: {job.selected_paths && job.selected_paths.length > 0 ? job.selected_paths.join(', ') : '/'}
                    </span>
                  </div>
                  <ArrowRight className="w-3 h-3 text-[var(--color-text-muted)] shrink-0" />
                  <div className="flex flex-col text-left min-w-0">
                    <span className="text-xs font-bold text-[var(--color-text-primary)] capitalize">
                      {job.target_provider}
                    </span>
                    <span className="text-[10px] text-[var(--color-text-muted)] max-w-[130px] truncate block">
                      {job.target_url || t('migrations.oauth')}
                    </span>
                    <span className="text-[10px] font-mono text-portal-navy max-w-[130px] truncate block" title={job.target_dir || '/'}>
                      {t('sync.targetPath')}: {job.target_dir || '/'}
                    </span>
                  </div>
                </div>
              </td>
              <td className="py-4 px-4 whitespace-nowrap text-xs font-mono">
                {job.direction === 'two_way' ? t('sync.twoWay') : t('sync.oneWay')}
              </td>
              <td className="py-4 px-4 whitespace-nowrap">
                <span className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11px] font-bold border ${
                  job.status === 'RUNNING' || job.status === 'INDEXING'
                    ? 'bg-blue-50 text-blue-700 border-blue-200 animate-pulse'
                    : job.status === 'PAUSED' || job.status === 'PAUSED_CONNECTION_LOSS'
                    ? 'bg-slate-100 text-slate-700 border-slate-200'
                    : job.status === 'FAILED'
                    ? 'bg-rose-50 text-rose-700 border-rose-200'
                    : 'bg-emerald-50 text-emerald-700 border-emerald-200'
                }`}>
                  {job.status === 'RUNNING' || job.status === 'INDEXING' ? <Loader2 className="w-3 h-3 animate-spin" /> : null}
                  {job.status === 'IDLE' ? t('sync.statusIdle') :
                   job.status === 'RUNNING' ? t('status.active') :
                   job.status === 'INDEXING' ? t('status.indexing') :
                   job.status === 'PAUSED' ? t('status.paused') :
                   job.status === 'PAUSED_CONNECTION_LOSS' ? t('dashboard.eta.waitingConn') :
                   job.status === 'FAILED' ? t('status.failed') : job.status}
                </span>
              </td>
              <td className="py-4 px-4 whitespace-nowrap text-xs font-mono text-[var(--color-text-secondary)]">
                {job.last_run_at ? formatDateTime(job.last_run_at) : '-'}
              </td>
              <td className="py-4 px-4 text-right whitespace-nowrap" onClick={(e) => e.stopPropagation()}>
                <div className="flex justify-end items-center gap-2">
                  <button
                    onClick={() => onSelectActiveSync && onSelectActiveSync(job.id)}
                    className="p-1.5 bg-[var(--color-bg-tertiary)] hover:bg-portal-navy hover:text-[var(--color-text-inverse)] rounded-lg text-[var(--color-text-muted)] transition-all cursor-pointer"
                    title={t('sync.openDetail')}
                  >
                    <Play className="w-3.5 h-3.5 fill-current" />
                  </button>
                  <button
                    onClick={(e) => handleDelete(job.id, e)}
                    disabled={deleteLoading === job.id || job.status === 'RUNNING' || job.status === 'INDEXING'}
                    className="p-1.5 bg-[var(--color-bg-tertiary)] rounded-lg text-[var(--color-text-muted)] hover:text-rose-700 hover:bg-rose-50 transition-all disabled:opacity-30 cursor-pointer"
                    title={t('sync.deleteJob')}
                  >
                    {deleteLoading === job.id ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Trash2 className="w-3.5 h-3.5" />}
                  </button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

