import React, { useEffect, useState, useRef } from 'react';
import { RefreshCw, AlertTriangle, Download, Clock, HardDrive, Coffee, Link, Copy, Check, Pause, Play, XCircle, Loader2 } from 'lucide-react';

// formatSize is defined at module level so it is not recreated on every render.
const formatSize = (bytes: number): string => {
  if (!bytes || bytes <= 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

const formatDuration = (seconds: number): string => {
  if (seconds === Infinity || isNaN(seconds)) return 'Berechnung...';
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const mins = Math.floor(seconds / 60);
  const secs = Math.round(seconds % 60);
  if (mins < 60) return `${mins}m ${secs}s`;
  const hrs = Math.floor(mins / 60);
  const remMins = mins % 60;
  return `${hrs}h ${remMins}m`;
};

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
  skipped_files: number;
  failed_files: number;
  error_message: string;
  active_file: string;
  active_files?: string[];
  threads?: number;
  bandwidth_limit_mbps?: number;
  resource_stats?: MigrationResourceStats;
}

const renderResourceSection = (title: string, stats: ResourceStats | undefined) => {
  if (!stats || stats.total === 0) return null;
  const success = Math.max(0, stats.processed - stats.failed - stats.skipped);
  return (
    <div className="w-full mt-4 first:mt-0 first:border-t-0 first:pt-0 border-t border-[var(--color-border-light)] pt-4 text-[var(--color-text-muted)] text-left">
      <h5 className="font-bold text-[var(--color-text-secondary)] mb-2 uppercase tracking-wider text-[10px]">{title}</h5>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>Gesamt:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.total}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>Übertragen:</span>
        <span className="font-bold text-emerald-600 font-mono">{success}</span>
      </div>
      <div className="flex justify-between items-center py-1 border-b border-[var(--color-border-light)]">
        <span>Übersprungen:</span>
        <span className="font-bold text-[var(--color-text-primary)] font-mono">{stats.skipped}</span>
      </div>
      <div className="flex justify-between items-center py-1">
        <span>Fehlgeschlagen:</span>
        <span className={`font-bold font-mono ${stats.failed > 0 ? 'text-rose-600' : 'text-[var(--color-text-secondary)]'}`}>
          {stats.failed}
        </span>
      </div>
    </div>
  );
};

export const Dashboard: React.FC<DashboardProps> = ({ migrationId, apiUrl, onReset, token }) => {
  const [data, setData] = useState<ProgressData | null>(null);
  const [controlLoading, setControlLoading] = useState<string | null>(null);
  const [speed, setSpeed] = useState<number>(0); // Bytes per second
  const [eta, setEta] = useState<string>('Berechnung...');
  const [copied, setCopied] = useState<boolean>(false);
  const [serverUnreachable, setServerUnreachable] = useState<boolean>(false);
  const [reconnectNonce, setReconnectNonce] = useState<number>(0);
  const [bandwidthLimit, setBandwidthLimit] = useState<number>(0);
  const [bandwidthLoading, setBandwidthLoading] = useState<boolean>(false);

  const directLink = `${window.location.origin}${window.location.pathname}?migration=${migrationId}`;

  const handleCopyLink = () => {
    navigator.clipboard.writeText(directLink)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      })
      .catch((err) => {
        console.error('Kopieren fehlgeschlagen:', err);
      });
  };

  const handleDownloadReport = async (e: React.MouseEvent) => {
    e.preventDefault();
    try {
      const response = await fetch(`${apiUrl}/api/migration/${migrationId}/report`, {
        headers: {
          'Authorization': `Bearer ${token}`,
        },
      });
      if (!response.ok) {
        throw new Error('Fehlerbericht konnte nicht geladen werden.');
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
      alert('Bericht konnte nicht heruntergeladen werden.');
    }
  };

  const handleMigrationControl = async (action: 'pause' | 'resume' | 'cancel') => {
    if (action === 'cancel' && !window.confirm('Möchtest du diese Migration wirklich abbrechen? Dies kann nicht rückgängig gemacht werden.')) {
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
        throw new Error(`Aktion ${action} fehlgeschlagen.`);
      }
      // Status will automatically update via WebSocket
    } catch (err) {
      console.error(err);
      alert(`Fehler: ${err instanceof Error ? err.message : String(err)}`);
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
        throw new Error('Bandbreitenlimit konnte nicht aktualisiert werden.');
      }
    } catch (err) {
      console.error(err);
      alert(`Fehler: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setBandwidthLoading(false);
    }
  };

  const progressHistory = useRef<{ timestamp: number; bytes: number }[]>([]);

  const handleRetryFailed = async () => {
    if (!window.confirm('Möchtest du die fehlgeschlagenen Elemente erneut migrieren?')) {
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
        throw new Error('Erneuter Versuch fehlgeschlagen.');
      }
      const resData = await response.json();
      if (resData.success && resData.retried > 0) {
        setReconnectNonce((n) => n + 1);
      } else {
        alert('Keine fehlgeschlagenen Elemente zum erneuten Versuch gefunden.');
      }
    } catch (err) {
      console.error(err);
      alert(`Fehler: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setControlLoading(null);
    }
  };

  const lastActiveSpeed = useRef<number>(0);
  const lastActiveTime = useRef<number>(0);

  const prevStatusRef = useRef<string>('');


  useEffect(() => {
    progressHistory.current = [];
    lastActiveSpeed.current = 0;
    lastActiveTime.current = 0;
    prevStatusRef.current = '';

    // Construct WebSocket URL
    const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const apiUrlObj = new URL(apiUrl.startsWith('http') ? apiUrl : `${window.location.origin}${apiUrl}`);
    const wsUrl = `${wsProto}://${apiUrlObj.host}/api/migration/${migrationId}/ws`;

    let isMounted = true;
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
      setData(payload);

      if (payload.bandwidth_limit_mbps !== undefined) {
        setBandwidthLimit(payload.bandwidth_limit_mbps);
      }

      // Reset progress history if status changes to avoid calculations across states
      if (payload.status !== prevStatusRef.current) {
        progressHistory.current = [];
        lastActiveSpeed.current = 0;
        lastActiveTime.current = 0;
      }

      prevStatusRef.current = payload.status;

      // Speed and ETA calculation
      if (payload.status === 'COMPLETED') {
        setSpeed(0);
        setEta('Fertig');
      } else if (payload.status === 'FAILED') {
        setSpeed(0);
        setEta('Fehlgeschlagen');
      } else if (payload.status === 'INDEXING') {
        setSpeed(0);
        setEta('Indexierung...');
      } else if (payload.status === 'PENDING') {
        setSpeed(0);
        setEta('Warte auf Start...');
      } else if (payload.status === 'PAUSED_CONNECTION_LOSS') {
        setSpeed(0);
        setEta('Warte auf Verbindung...');
      } else {
        // RUNNING or other states
        const now = Date.now();
        progressHistory.current.push({ timestamp: now, bytes: payload.processed_bytes });

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

            // ETA calculation
            const remainingBytes = payload.total_bytes - payload.processed_bytes;
            if (remainingBytes <= 0) {
              setEta('Fertig');
            } else if (calculatedSpeed > 0) {
              const etaSec = remainingBytes / calculatedSpeed;
              setEta(formatDuration(etaSec));
            } else {
              setEta('Berechnung...');
            }
          }
        } else {
          setSpeed(0);
          setEta('Berechnung...');
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
      if (prevStatusRef.current === 'COMPLETED' || prevStatusRef.current === 'FAILED') {
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
  }, [migrationId, apiUrl, token, reconnectNonce]);

  if (serverUnreachable) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <AlertTriangle className="w-10 h-10 text-amber-500" />
        <p className="font-sans text-sm font-semibold text-[var(--color-text-secondary)]">Server nicht erreichbar</p>
        <p className="font-sans text-xs text-[var(--color-text-muted)] text-center max-w-sm">
          Die Verbindung zum Migrations-Server konnte nicht hergestellt werden.
          Bitte stelle sicher, dass der Server läuft, und lade die Seite neu.
        </p>
        <button
          onClick={() => window.location.reload()}
          className="mt-2 px-4 py-2 bg-portal-orange text-white text-xs font-bold rounded-lg hover:bg-portal-orange-hover transition-colors cursor-pointer"
        >
          Seite neu laden
        </button>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <RefreshCw className="w-10 h-10 text-[var(--color-portal-navy-themed)] animate-spin" />
        <p className="font-sans text-xs italic text-[var(--color-text-muted)]">// INITIALISIERE PROZESS-MONITOR</p>
      </div>
    );
  }

  // Calculated stats
  const byteProgressPercent = data.total_bytes > 0 
    ? Math.min(Math.round((data.processed_bytes / data.total_bytes) * 100), 100) 
    : 0;

  const successFiles = Math.max(0, data.processed_files - data.failed_files - data.skipped_files);

  return (
    <div className="w-full max-w-4xl mx-auto py-2 animate-fade-in text-left">
      
      {/* Privacy-First Bookmarkable Direct Link Card */}
      <div className="mb-6 p-5 glass-panel border border-[var(--color-glass-border)] rounded-3xl shadow-portal flex flex-col md:flex-row md:items-center justify-between gap-4">
        <div className="flex items-start md:items-center gap-3.5">
          <div className="p-2.5 bg-orange-50 text-portal-orange rounded-xl shrink-0">
            <Link className="w-4 h-4" />
          </div>
          <div className="flex flex-col text-left">
            <span className="text-[10px] font-bold text-[var(--color-portal-navy-themed)] uppercase tracking-widest font-mono">Direktlink zu dieser Migration</span>
            <span className="text-[11px] text-[var(--color-text-muted)] mt-0.5">Speichere diesen Link als Lesezeichen, um den Fortschritt später wieder aufzurufen.</span>
          </div>
        </div>
        <div className="flex items-center gap-2 bg-[var(--color-bg-tertiary)]/80 border border-[var(--color-border)] rounded-2xl px-3 py-2 shrink-0 max-w-full overflow-hidden shadow-inner">
          <span className="font-mono text-[10.5px] text-[var(--color-text-secondary)] truncate select-all pr-1" title={directLink}>
            {directLink}
          </span>
          <button
            onClick={handleCopyLink}
            className="p-1.5 bg-[var(--color-bg-secondary)] hover:bg-[var(--color-border)] rounded-lg text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] border border-[var(--color-border)] transition-colors shrink-0 cursor-pointer"
            title="Link kopieren"
          >
            {copied ? (
              <Check className="w-3.5 h-3.5 text-emerald-650 animate-pulse" />
            ) : (
              <Copy className="w-3.5 h-3.5" />
            )}
          </button>
        </div>
      </div>

      {/* Background Mode Guarantee Stamp (Grab a coffee) */}
      <div className="mb-6 p-4.5 bg-gradient-to-r from-portal-navy to-portal-navy-light text-white border border-white/10 rounded-2xl shadow-md flex items-center justify-between text-xs">
        <div className="flex items-center gap-3">
          <Coffee className="w-4 h-4 text-portal-orange shrink-0 animate-bounce" />
          <span className="leading-snug">Der Migrationstransfer läuft serverseitig. Du kannst diese Seite bedenkenlos schließen.</span>
        </div>
      </div>

      {/* PAUSED CONNECTION LOSS WARNING */}
      {data.status === 'PAUSED_CONNECTION_LOSS' && (
        <div className="mb-6 p-5 border border-amber-250 bg-amber-50/70 backdrop-blur-md rounded-2xl flex items-start gap-4 animate-pulse-glow">
          <AlertTriangle className="w-6 h-6 shrink-0 text-amber-600 mt-0.5" />
          <div className="text-xs leading-relaxed text-[var(--color-text-secondary)] text-left">
            <h4 className="font-display font-extrabold text-amber-900 uppercase tracking-wide">Verbindungsabbruch zur Instanz</h4>
            <p className="text-[var(--color-text-secondary)] mt-1.5 leading-relaxed">
              Eine Instanz antwortet nicht. Das System pausiert temporär und prüft die Erreichbarkeit selbstständig alle 60 Sekunden. Sobald die Server wieder antworten, wird der Transfer exakt am Abbruchpunkt fortgesetzt.
            </p>
          </div>
        </div>
      )}

      {/* Main Grid */}
      <div className="grid md:grid-cols-3 gap-8">
        
        {/* Progress & Metrics */}
        <div className="md:col-span-2 space-y-8">
          
          {/* Main metric card */}
          <div className="glass-panel border border-[var(--color-glass-border)] p-6 shadow-portal rounded-3xl relative overflow-hidden flex flex-col group">
            <div className="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-portal-orange to-orange-500" />
            
            <div className="flex items-end justify-between mb-6 border-b border-[var(--color-border-light)] pb-4.5">
              <div>
                <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">Fortschritt</span>
                <h3 className="font-display font-extrabold text-5xl text-[var(--color-portal-navy-themed)] mt-1.5 leading-none">
                  {byteProgressPercent}%
                </h3>
              </div>
              <div className="text-right flex flex-col items-end">
                <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">Übertragungsrate</span>
                <p className="text-base font-extrabold text-emerald-600 mt-1.5 font-mono">
                  {formatSize(speed)}/s
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
                <span>Übertragen: <strong className="text-[var(--color-text-primary)]">{formatSize(data.processed_bytes)}</strong> / {formatSize(data.total_bytes)}</span>
              </div>
              <div className="flex items-center gap-2 justify-end">
                <Clock className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
                <span>Restzeit: <strong className="text-[var(--color-text-primary)]">{eta}</strong></span>
              </div>
            </div>
          </div>

          {/* Active Transfers Card */}
          {data.status === 'RUNNING' && data.active_files && data.active_files.length > 0 && (
            <div className="glass-panel border border-[var(--color-glass-border)] p-5 shadow-portal rounded-3xl flex flex-col">
              <div className="flex items-center gap-2 mb-4 pb-3 border-b border-[var(--color-border-light)]">
                <RefreshCw className="w-4 h-4 text-portal-orange animate-spin" />
                <h4 className="font-mono font-bold text-[var(--color-text-muted)] text-[10px] uppercase tracking-widest text-left">
                  Aktive Übertragungen ({data.active_files.length} von {data.threads || 4} Threads)
                </h4>
              </div>
              <div className="space-y-2">
                {data.active_files.map((file, i) => {
                  const fileName = file.split('/').pop() || file;
                  return (
                    <div key={i} className="flex items-center justify-between text-xs py-2.5 px-3.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-xl font-mono text-[var(--color-text-secondary)] min-w-0">
                      <span className="truncate pr-4" title={file}>{fileName}</span>
                      <span className="text-[10px] text-emerald-600 font-semibold uppercase animate-pulse shrink-0 bg-emerald-50 border border-emerald-200 px-2 py-0.5 rounded-md">Läuft...</span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>

        {/* Status card & Sidebar Column */}
        <div className="space-y-6">
          <div className="glass-panel border border-[var(--color-glass-border)] p-6 shadow-portal rounded-3xl flex flex-col items-center text-center">
            <span className="text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-4">STATUS</span>
            
            {/* Status Stamp capsule */}
            {data.status === 'COMPLETED' ? (
              <div className="bg-emerald-50 text-emerald-700 border border-emerald-200 px-5 py-2 font-mono font-bold text-xs rounded-full shadow-xs mb-5">
                ABGESCHLOSSEN
              </div>
            ) : data.status === 'FAILED' ? (
              <div className="bg-rose-50 text-rose-700 border border-rose-200 px-5 py-2 font-mono font-bold text-xs rounded-full shadow-xs mb-5">
                FEHLGESCHLAGEN
              </div>
            ) : data.status === 'PAUSED_CONNECTION_LOSS' || data.status === 'PAUSED' ? (
              <div className="bg-amber-50 text-amber-750 border border-amber-250 px-5 py-2 font-mono font-bold text-xs rounded-full shadow-xs mb-5 animate-pulse">
                PAUSIERT
              </div>
            ) : data.status === 'CANCELLED' ? (
              <div className="bg-rose-50 text-rose-700 border border-rose-200 px-5 py-2 font-mono font-bold text-xs rounded-full shadow-xs mb-5">
                ABGEBROCHEN
              </div>
            ) : (
              <div className="bg-blue-50 text-[var(--color-portal-navy-themed)] border border-blue-200 px-5 py-2 font-mono font-bold text-xs rounded-full shadow-xs mb-5 animate-pulse">
                ÜBERTRAGUNG
              </div>
            )}

            <h4 className="font-mono font-bold text-[var(--color-text-muted)] text-[10px] tracking-wider uppercase mt-1">
              Job: {data.status}
            </h4>

            {data.error_message && (
              <p className="font-mono text-[10px] text-rose-700 mt-4 bg-rose-50/80 border border-rose-250 p-3 rounded-2xl leading-normal text-left max-w-full overflow-hidden">
                FEHLER: {data.error_message}
              </p>
            )}

            <div className="w-full mt-6 space-y-2 font-sans text-xs border-t border-[var(--color-border-light)] pt-5 text-[var(--color-text-muted)]">
              {data.resource_stats ? (
                <>
                  {renderResourceSection("Dateien", data.resource_stats.files)}
                  {renderResourceSection("Kalender", data.resource_stats.calendars)}
                  {renderResourceSection("Kontakte", data.resource_stats.contacts)}
                </>
              ) : (
                <>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>Dateien gesamt:</span>
                    <span className="font-bold text-[var(--color-text-primary)] font-mono">{data.total_files}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>Übertragen:</span>
                    <span className="font-bold text-emerald-600 font-mono">{successFiles}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5 border-b border-[var(--color-border-light)]">
                    <span>Übersprungen:</span>
                    <span className="font-bold text-[var(--color-text-primary)] font-mono">{data.skipped_files}</span>
                  </div>
                  <div className="flex justify-between items-center py-1.5">
                    <span>Fehlgeschlagen:</span>
                    <span className={`font-bold font-mono ${data.failed_files > 0 ? 'text-rose-600' : 'text-[var(--color-text-muted)]'}`}>
                      {data.failed_files}
                    </span>
                  </div>
                </>
              )}
            </div>
          </div>

          {/* Bandwidth Limit Slider */}
          {(data.status === 'RUNNING' || data.status === 'INDEXING') && (
            <div className="glass-panel border border-[var(--color-glass-border)] p-5 shadow-portal rounded-3xl">
          <div className="flex items-center justify-between mb-3">
            <label className="text-xs font-semibold text-[var(--color-text-secondary)]">
              Bandbreitenlimit (Mbps)
            </label>
            <span className="text-xs font-bold text-portal-orange font-mono">
              {bandwidthLimit === 0 ? 'Unbegrenzt' : `${bandwidthLimit} Mbps`}
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
              <div className="flex justify-between text-[9px] text-[var(--color-text-muted)] font-mono mt-2">
                <span>0</span>
                <span>10</span>
                <span>20</span>
                <span>30</span>
                <span>40</span>
                <span>50</span>
              </div>
            </div>
          )}

          {/* Action buttons */}
          <div className="space-y-3">
            {/* Report Download */}
            {data.failed_files > 0 && (
              <button
                onClick={handleDownloadReport}
                className="w-full flex items-center justify-center gap-2 py-3 px-4 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-2xl shadow-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:border-[var(--color-border)] transition-all font-mono text-[11px] font-bold uppercase tracking-wider text-center cursor-pointer"
              >
                <Download className="w-4 h-4 text-portal-orange" />
                <span>Fehlerbericht (.CSV)</span>
              </button>
            )}

            {/* Retry Failed Elements */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED') && data.failed_files > 0 && (
              <button
                onClick={handleRetryFailed}
                disabled={controlLoading !== null}
                className="w-full flex items-center justify-center gap-2 py-3.5 px-4 bg-gradient-to-r from-portal-orange to-orange-500 text-white rounded-2xl font-mono text-[11px] font-bold uppercase tracking-wider shadow-xs hover:shadow-md hover:scale-[1.01] active:scale-99 transition-all cursor-pointer disabled:opacity-50"
              >
                {controlLoading === 'retry' ? (
                  <Loader2 className="w-4 h-4 animate-spin text-white" />
                ) : (
                  <RefreshCw className="w-4 h-4 text-white" />
                )}
                <span>Fehlerhafte wiederholen</span>
              </button>
            )}

            {/* Migration Controls */}
            {(data.status === 'RUNNING' || data.status === 'INDEXING') && (
              <button
                onClick={() => handleMigrationControl('pause')}
                disabled={controlLoading !== null}
                className="w-full flex items-center justify-center gap-2 py-3.5 px-4 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-2xl shadow-xs text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)] hover:border-[var(--color-border)] transition-all font-mono text-[11px] font-bold uppercase tracking-wider text-center cursor-pointer disabled:opacity-50"
              >
                {controlLoading === 'pause' ? <Loader2 className="w-4 h-4 animate-spin text-amber-500" /> : <Pause className="w-4 h-4 text-amber-500" />}
                <span>Pausieren</span>
              </button>
            )}

            {(data.status === 'PAUSED' || data.status === 'PAUSED_CONNECTION_LOSS') && (
              <button
                onClick={() => handleMigrationControl('resume')}
                disabled={controlLoading !== null}
                className="w-full flex items-center justify-center gap-2 py-3.5 px-4 bg-emerald-50 border border-emerald-250 rounded-2xl shadow-xs text-emerald-750 hover:bg-emerald-100 hover:border-emerald-350 transition-all font-mono text-[11px] font-bold uppercase tracking-wider text-center cursor-pointer disabled:opacity-50"
              >
                {controlLoading === 'resume' ? <Loader2 className="w-4 h-4 animate-spin" /> : <Play className="w-4 h-4 text-emerald-600" />}
                <span>Fortsetzen</span>
              </button>
            )}

            {(data.status === 'RUNNING' || data.status === 'INDEXING' || data.status === 'PAUSED' || data.status === 'PAUSED_CONNECTION_LOSS') && (
              <button
                onClick={() => handleMigrationControl('cancel')}
                disabled={controlLoading !== null}
                className="w-full flex items-center justify-center gap-2 py-3 px-4 bg-[var(--color-bg-secondary)] border border-rose-200 rounded-2xl shadow-xs text-rose-600 hover:bg-rose-50 hover:border-rose-300 transition-colors font-mono text-[11px] font-bold uppercase tracking-wider text-center cursor-pointer disabled:opacity-50 mt-2"
              >
                {controlLoading === 'cancel' ? <Loader2 className="w-4 h-4 animate-spin" /> : <XCircle className="w-4 h-4" />}
                <span>Abbrechen</span>
              </button>
            )}

            {/* Reset Button */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED' || data.status === 'CANCELLED') && (
              <button
                onClick={onReset}
                className="w-full flex items-center justify-center gap-2 py-3.5 px-4 bg-gradient-to-r from-portal-orange to-orange-500 text-white rounded-2xl font-mono text-[11px] font-bold uppercase tracking-wider shadow-xs hover:shadow-md hover:scale-[1.01] active:scale-99 transition-all cursor-pointer"
              >
                <span>Neue Migration starten</span>
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
