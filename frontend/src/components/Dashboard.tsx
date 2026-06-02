import React, { useEffect, useState, useRef } from 'react';
import { RefreshCw, CheckCircle2, AlertTriangle, XCircle, FileText, Download, Clock, HardDrive } from 'lucide-react';

interface DashboardProps {
  migrationId: string;
  apiUrl: string;
  onReset: () => void;
}

interface ProgressData {
  id: string;
  status: string; // INDEXING, RUNNING, PAUSED_CONNECTION_LOSS, COMPLETED, FAILED
  total_files: number;
  total_bytes: number;
  processed_files: number;
  processed_bytes: number;
  skipped_files: number;
  failed_files: number;
  error_message: string;
  active_file: string;
}

export const Dashboard: React.FC<DashboardProps> = ({ migrationId, apiUrl, onReset }) => {
  const [data, setData] = useState<ProgressData | null>(null);
  const [speed, setSpeed] = useState<number>(0); // Bytes per second
  const [eta, setEta] = useState<string>('Berechnung...');
  
  const prevBytes = useRef<number>(0);
  const prevTime = useRef<number>(Date.now());
  const startTime = useRef<number>(Date.now());

  useEffect(() => {
    // Construct WebSocket URL
    const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    // Remove port or replace http/https from API URL
    const cleanApiUrl = apiUrl.replace(/^https?:\/\//, '');
    const wsUrl = `${wsProto}://${cleanApiUrl}/api/migration/${migrationId}/ws`;

    let ws = new WebSocket(wsUrl);

    ws.onopen = () => {
      startTime.current = Date.now();
      prevTime.current = Date.now();
    };

    ws.onmessage = (event) => {
      const payload: ProgressData = JSON.parse(event.data);
      setData(payload);

      // Speed calculation
      const now = Date.now();
      const timeDiffSec = (now - prevTime.current) / 1000;
      
      if (timeDiffSec >= 0.8) { // Update speed at least every 800ms
        const bytesDiff = payload.processed_bytes - prevBytes.current;
        const currentSpeed = bytesDiff / timeDiffSec;
        
        setSpeed(currentSpeed > 0 ? currentSpeed : 0);

        // ETA calculation
        const remainingBytes = payload.total_bytes - payload.processed_bytes;
        if (remainingBytes <= 0) {
          setEta('Fertig');
        } else if (currentSpeed > 0) {
          const etaSec = remainingBytes / currentSpeed;
          setEta(formatDuration(etaSec));
        } else {
          setEta('Berechnung...');
        }

        prevBytes.current = payload.processed_bytes;
        prevTime.current = now;
      }
    };

    ws.onerror = (err) => {
      console.error('WS Error:', err);
    };

    return () => {
      ws.close();
    };
  }, [migrationId, apiUrl]);

  const formatDuration = (seconds: number) => {
    if (seconds === Infinity || isNaN(seconds)) return 'Berechnung...';
    if (seconds < 60) return `${Math.round(seconds)}s`;
    const mins = Math.floor(seconds / 60);
    const secs = Math.round(seconds % 60);
    if (mins < 60) return `${mins}m ${secs}s`;
    const hrs = Math.floor(mins / 60);
    const remMins = mins % 60;
    return `${hrs}h ${remMins}m`;
  };

  const formatSize = (bytes: number) => {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  };

  if (!data) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <RefreshCw className="w-10 h-10 text-blue-500 animate-spin" />
        <p className="text-slate-400 text-sm">Dashboard wird geladen...</p>
      </div>
    );
  }

  // Calculated stats
  const byteProgressPercent = data.total_bytes > 0 
    ? Math.min(Math.round((data.processed_bytes / data.total_bytes) * 100), 100) 
    : 0;

  const successFiles = Math.max(0, data.processed_files - data.failed_files - data.skipped_files);

  return (
    <div className="w-full max-w-4xl mx-auto py-8 px-4">
      {/* Background Mode Guarantee Alert */}
      <div className="mb-6 p-4 bg-blue-500/10 border border-blue-500/20 rounded-xl flex items-center justify-between text-xs text-blue-300 font-medium">
        <div className="flex items-center gap-2">
          <Clock className="w-4 h-4 shrink-0" />
          <span>Hintergrund-Garantie: Die Migration läuft serverseitig. Sie können diesen Browsertab bedenkenlos schließen.</span>
        </div>
        <span className="hidden sm:inline px-2 py-0.5 rounded-full bg-blue-500/20 text-blue-400 font-mono text-[10px] uppercase">Aktiv</span>
      </div>

      {/* PAUSED CONNECTION LOSS WARNING */}
      {data.status === 'PAUSED_CONNECTION_LOSS' && (
        <div className="mb-6 p-4 bg-amber-500/10 border border-amber-500/20 rounded-xl flex items-start gap-3 text-amber-300 shadow-glow">
          <AlertTriangle className="w-6 h-6 shrink-0 text-amber-400 animate-pulse" />
          <div>
            <h4 className="font-bold text-sm">Verbindung vorübergehend verloren</h4>
            <p className="text-xs text-amber-400/80 mt-1">
              Eine Nextcloud-Instanz antwortet nicht. Das System pausiert automatisch und prüft die Erreichbarkeit alle 60 Sekunden. Sobald die Server wieder antworten, wird der Transfer exakt am Abbruchpunkt fortgesetzt.
            </p>
          </div>
        </div>
      )}

      {/* Main Grid */}
      <div className="grid md:grid-cols-3 gap-6">
        {/* Progress & Metrics */}
        <div className="md:col-span-2 space-y-6">
          <div className="glass p-6 rounded-2xl border border-slate-800 relative overflow-hidden shadow-glow">
            <div className="flex items-center justify-between mb-6">
              <div>
                <span className="text-xs font-semibold text-slate-500 uppercase tracking-wider">Fortschritt (Datenmenge)</span>
                <h3 className="text-3xl font-extrabold text-slate-100 mt-1">{byteProgressPercent}%</h3>
              </div>
              <div className="text-right">
                <span className="text-xs font-semibold text-slate-500 uppercase tracking-wider">Geschwindigkeit</span>
                <p className="text-lg font-bold text-blue-400 mt-1">{formatSize(speed)}/s</p>
              </div>
            </div>

            {/* Progress Bar (Bytes) */}
            <div className="w-full bg-slate-950 rounded-full h-3 mb-6 overflow-hidden border border-slate-800">
              <div
                className="bg-gradient-to-r from-blue-500 to-indigo-600 h-full rounded-full transition-all duration-500 ease-out"
                style={{ width: `${byteProgressPercent}%` }}
              ></div>
            </div>

            <div className="grid grid-cols-2 gap-4 text-xs font-medium text-slate-400">
              <div className="flex items-center gap-2">
                <HardDrive className="w-4 h-4 text-slate-500" />
                <span>Übertragen: <strong>{formatSize(data.processed_bytes)}</strong> / {formatSize(data.total_bytes)}</span>
              </div>
              <div className="flex items-center gap-2 justify-end">
                <Clock className="w-4 h-4 text-slate-500" />
                <span>Restlaufzeit: <strong>{eta}</strong></span>
              </div>
            </div>
          </div>

          {/* Active File status */}
          <div className="glass p-6 rounded-2xl border border-slate-800">
            <h4 className="text-xs font-semibold text-slate-500 uppercase tracking-wider mb-3">Aktive Datei im Transfer</h4>
            {data.status === 'RUNNING' && data.active_file ? (
              <div className="flex items-center gap-3 p-3 bg-slate-900/60 border border-slate-850 rounded-xl">
                <FileText className="w-5 h-5 text-blue-400 shrink-0" />
                <span className="text-sm font-mono text-slate-200 truncate flex-grow">{data.active_file}</span>
                <span className="text-[10px] font-bold text-blue-400 bg-blue-500/10 px-2 py-0.5 rounded-full uppercase animate-pulse">Coping</span>
              </div>
            ) : data.status === 'INDEXING' ? (
              <p className="text-sm text-slate-400 italic">Quellordner werden indiziert...</p>
            ) : data.status === 'COMPLETED' ? (
              <p className="text-sm text-slate-400 italic text-emerald-400">Migration erfolgreich abgeschlossen!</p>
            ) : (
              <p className="text-sm text-slate-400 italic">Keine aktive Übertragung.</p>
            )}
          </div>
        </div>

        {/* Status card & Sidebar */}
        <div className="space-y-6">
          <div className="glass p-6 rounded-2xl border border-slate-800 flex flex-col items-center text-center">
            <span className="text-xs font-semibold text-slate-500 uppercase tracking-wider mb-4">Migrations-Status</span>
            
            {/* Status Icons */}
            {data.status === 'COMPLETED' ? (
              <div className="p-4 bg-emerald-500/10 border border-emerald-500/20 text-emerald-400 rounded-full mb-4">
                <CheckCircle2 className="w-12 h-12" />
              </div>
            ) : data.status === 'FAILED' ? (
              <div className="p-4 bg-rose-500/10 border border-rose-500/20 text-rose-400 rounded-full mb-4">
                <XCircle className="w-12 h-12" />
              </div>
            ) : data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <div className="p-4 bg-amber-500/10 border border-amber-500/20 text-amber-400 rounded-full mb-4 animate-pulse">
                <AlertTriangle className="w-12 h-12" />
              </div>
            ) : (
              <div className="p-4 bg-blue-500/10 border border-blue-500/20 text-blue-400 rounded-full mb-4">
                <RefreshCw className="w-12 h-12 animate-spin" />
              </div>
            )}

            <h4 className="text-lg font-bold text-slate-100 uppercase tracking-wide">
              {data.status === 'INDEXING' && 'Indizierung'}
              {data.status === 'RUNNING' && 'Übertragung'}
              {data.status === 'PAUSED_CONNECTION_LOSS' && 'Verbindungsabbruch'}
              {data.status === 'COMPLETED' && 'Abgeschlossen'}
              {data.status === 'FAILED' && 'Fehlgeschlagen'}
            </h4>

            {data.error_message && (
              <p className="text-xs text-rose-400 font-medium mt-2 bg-rose-500/10 border border-rose-500/20 p-2.5 rounded-lg max-w-full truncate">
                {data.error_message}
              </p>
            )}

            {/* Counters List */}
            <div className="w-full mt-6 space-y-3 text-xs border-t border-slate-900 pt-5 text-slate-400">
              <div className="flex justify-between items-center">
                <span>Dateien gesamt:</span>
                <span className="font-bold text-slate-200">{data.total_files}</span>
              </div>
              <div className="flex justify-between items-center">
                <span>Erfolgreich übertragen:</span>
                <span className="font-bold text-emerald-400">{successFiles}</span>
              </div>
              <div className="flex justify-between items-center">
                <span>Übersprungen:</span>
                <span className="font-bold text-slate-200">{data.skipped_files}</span>
              </div>
              <div className="flex justify-between items-center">
                <span>Fehlgeschlagen:</span>
                <span className="font-bold text-rose-400">{data.failed_files}</span>
              </div>
            </div>
          </div>

          {/* Action buttons */}
          <div className="space-y-3">
            {/* Report Download */}
            {data.failed_files > 0 && (
              <a
                href={`${apiUrl}/api/migration/${migrationId}/report`}
                download
                className="w-full flex items-center justify-center gap-2 py-3.5 bg-slate-900 hover:bg-slate-850 border border-slate-800 hover:border-slate-700 text-slate-200 rounded-xl font-semibold transition-all duration-300 shadow-sm"
              >
                <Download className="w-4 h-4" />
                Fehler-Protokoll (.CSV)
              </a>
            )}

            {/* Finish/Back button */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED') && (
              <button
                onClick={onReset}
                className="w-full flex items-center justify-center gap-2 py-3.5 bg-gradient-to-r from-blue-500 to-indigo-600 hover:from-blue-600 hover:to-indigo-700 text-slate-100 rounded-xl font-semibold shadow-md hover:shadow-indigo-500/10 transition-all duration-300"
              >
                Neue Migration starten
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
