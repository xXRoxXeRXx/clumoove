import React, { useEffect, useState, useRef } from 'react';
import { RefreshCw, CheckCircle2, AlertTriangle, XCircle, Download, Clock, HardDrive, Coffee, Terminal } from 'lucide-react';

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
  const [logs, setLogs] = useState<string[]>([
    '🔌 Verbindung zum Migrations-Server aufgebaut...',
    '📡 Empfange Echtzeit-Datenstrom...'
  ]);
  
  const prevBytes = useRef<number>(0);
  const prevTime = useRef<number>(Date.now());
  const startTime = useRef<number>(Date.now());

  // Log tracking refs
  const prevActiveFileRef = useRef<string>('');
  const prevStatusRef = useRef<string>('');
  const prevProcessedFilesRef = useRef<number>(0);
  const logsEndRef = useRef<HTMLDivElement | null>(null);

  const formatSize = (bytes: number) => {
    if (!bytes || bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  };

  useEffect(() => {
    // Construct WebSocket URL
    const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws';
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

      // Add friendly logs dynamically based on state changes
      const newLogs: string[] = [];

      if (payload.status === 'INDEXING' && prevStatusRef.current !== 'INDEXING') {
        newLogs.push('🔍 Starte Datei- und Ordnerindexierung auf der Quell-Instanz...');
      }

      if (payload.status === 'RUNNING' && prevStatusRef.current !== 'RUNNING') {
        newLogs.push('⚡ Datenstrom gestartet. Kopiere Dateien direkt durch den RAM...');
      }

      if (payload.active_file && payload.active_file !== prevActiveFileRef.current) {
        const fileName = payload.active_file.split('/').pop() || payload.active_file;
        newLogs.push(`🚀 Kopiere: ${fileName}`);
        prevActiveFileRef.current = payload.active_file;
      }

      if (payload.processed_files > prevProcessedFilesRef.current) {
        if (payload.status === 'RUNNING') {
          newLogs.push(`✔ ${payload.processed_files} von ${payload.total_files} Dateien übertragen.`);
        }
        prevProcessedFilesRef.current = payload.processed_files;
      }

      if (payload.status === 'COMPLETED' && prevStatusRef.current !== 'COMPLETED') {
        newLogs.push('🎉 Fertig! Alle Dateien wurden erfolgreich übertragen.');
        newLogs.push('🔒 Verschlüsselte Session-RAM-Puffer wurden bereinigt.');
      }

      if (payload.status === 'FAILED' && prevStatusRef.current !== 'FAILED') {
        newLogs.push(`❌ Migration fehlgeschlagen: ${payload.error_message || 'Unbekannter Verbindungsfehler.'}`);
      }

      if (payload.status === 'PAUSED_CONNECTION_LOSS' && prevStatusRef.current !== 'PAUSED_CONNECTION_LOSS') {
        newLogs.push('⚠️ Verbindung zu einer Instanz verloren. Pausiere Transfer und versuche erneuten Handshake...');
      }

      if (newLogs.length > 0) {
        setLogs((prev) => [...prev, ...newLogs]);
      }

      prevStatusRef.current = payload.status;

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
      setLogs((prev) => [...prev, '⚠️ Websocket-Verbindungsfehler. Versuche automatischen Reconnect...']);
    };

    return () => {
      ws.close();
    };
  }, [migrationId, apiUrl]);

  // Auto-scroll logs
  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs]);

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

  if (!data) {
    return (
      <div className="flex flex-col items-center justify-center min-h-[400px] gap-4">
        <RefreshCw className="w-10 h-10 text-cozy-indigo animate-spin" />
        <p className="text-slate-400 text-sm font-medium">Dashboard wird vorbereitet...</p>
      </div>
    );
  }

  // Calculated stats
  const byteProgressPercent = data.total_bytes > 0 
    ? Math.min(Math.round((data.processed_bytes / data.total_bytes) * 100), 100) 
    : 0;

  const successFiles = Math.max(0, data.processed_files - data.failed_files - data.skipped_files);

  return (
    <div className="w-full max-w-4xl mx-auto py-4 px-2">
      {/* Background Mode Guarantee Alert (Grab a coffee) */}
      <div className="mb-6 p-4 bg-cozy-indigo/10 border border-cozy-indigo/25 rounded-2xl flex items-center justify-between text-xs text-cozy-peach font-semibold shadow-md shadow-cozy-indigo/5">
        <div className="flex items-center gap-3">
          <div className="p-2 bg-cozy-indigo/20 rounded-xl text-white animate-float">
            <Coffee className="w-4 h-4 text-cozy-peach fill-cozy-peach/20" />
          </div>
          <span>Gönn dir einen Kaffee! Die Migration läuft sicher auf dem Server weiter. Du kannst diesen Tab schließen.</span>
        </div>
        <span className="hidden sm:inline px-3 py-1 rounded-full bg-cozy-mint/15 text-cozy-mint-light font-bold text-[9px] uppercase tracking-wider animate-pulse">Aktiv</span>
      </div>

      {/* PAUSED CONNECTION LOSS WARNING */}
      {data.status === 'PAUSED_CONNECTION_LOSS' && (
        <div className="mb-6 p-5 bg-amber-500/10 border border-amber-500/25 rounded-2xl flex items-start gap-4 text-amber-300 shadow-lg">
          <AlertTriangle className="w-6 h-6 shrink-0 text-amber-400 animate-bounce" />
          <div>
            <h4 className="font-display font-bold text-sm">Verbindung vorübergehend verloren</h4>
            <p className="text-xs text-amber-400/80 mt-1 leading-relaxed">
              Eine Nextcloud-Instanz antwortet gerade nicht. Keine Sorge: Das System pausiert und prüft die Erreichbarkeit alle 60 Sekunden. Sobald die Server wieder antworten, wird der Transfer exakt am Abbruchpunkt fortgesetzt.
            </p>
          </div>
        </div>
      )}

      {/* Main Grid */}
      <div className="grid md:grid-cols-3 gap-6">
        {/* Progress & Metrics */}
        <div className="md:col-span-2 space-y-6">
          <div className="cozy-glass p-6 rounded-3xl border border-slate-850 relative overflow-hidden">
            <div className="flex items-center justify-between mb-5">
              <div>
                <span className="text-[11px] font-display font-semibold text-slate-405 uppercase tracking-wider">Fortschritt (Datenmenge)</span>
                <h3 className="text-4xl font-display font-extrabold text-slate-100 mt-1">{byteProgressPercent}%</h3>
              </div>
              <div className="text-right">
                <span className="text-[11px] font-display font-semibold text-slate-405 uppercase tracking-wider">Geschwindigkeit</span>
                <p className="text-lg font-bold text-cozy-mint-light mt-1 font-mono">{formatSize(speed)}/s</p>
              </div>
            </div>

            {/* Glowing Progress Bar (Bytes) */}
            <div className="w-full bg-slate-950/70 rounded-full h-4 mb-5 overflow-hidden border border-slate-850 p-0.5">
              <div
                className="bg-gradient-to-r from-cozy-indigo via-cozy-coral to-cozy-peach h-full rounded-full transition-all duration-500 ease-out shadow-sm"
                style={{ width: `${byteProgressPercent}%` }}
              ></div>
            </div>

            <div className="grid grid-cols-2 gap-4 text-xs font-semibold text-slate-400">
              <div className="flex items-center gap-2">
                <HardDrive className="w-4 h-4 text-cozy-indigo" />
                <span>Übertragen: <strong className="text-slate-200 font-mono">{formatSize(data.processed_bytes)}</strong> / {formatSize(data.total_bytes)}</span>
              </div>
              <div className="flex items-center gap-2 justify-end">
                <Clock className="w-4 h-4 text-cozy-coral" />
                <span>Restlaufzeit: <strong className="text-slate-200">{eta}</strong></span>
              </div>
            </div>
          </div>

          {/* Live Messaging Log Console */}
          <div className="cozy-glass p-5 rounded-3xl border border-slate-850 flex flex-col h-[280px]">
            <div className="flex items-center gap-2 mb-3 pb-2.5 border-b border-slate-900/60">
              <Terminal className="w-4.5 h-4.5 text-cozy-indigo" />
              <h4 className="text-xs font-display font-bold text-slate-300 uppercase tracking-wider">Live-Protokoll</h4>
            </div>
            
            <div className="flex-grow overflow-y-auto scrollbar-cozy space-y-2.5 pr-1 font-mono text-[11px]">
              {logs.map((log, index) => (
                <div 
                  key={index} 
                  className={`py-1.5 px-3 rounded-lg border leading-relaxed break-all ${
                    log.startsWith('✔') 
                      ? 'bg-cozy-mint/5 border-cozy-mint/15 text-cozy-mint-light' 
                      : log.startsWith('🚀') || log.startsWith('⚡')
                      ? 'bg-cozy-indigo/5 border-cozy-indigo/15 text-slate-205'
                      : log.startsWith('❌')
                      ? 'bg-rose-500/5 border-rose-550/15 text-rose-300'
                      : log.startsWith('⚠️')
                      ? 'bg-amber-500/5 border-amber-500/15 text-amber-300'
                      : 'bg-slate-950/20 border-slate-900 text-slate-400'
                  }`}
                >
                  {log}
                </div>
              ))}
              <div ref={logsEndRef} />
            </div>
          </div>
        </div>

        {/* Status card & Sidebar */}
        <div className="space-y-6">
          <div className="cozy-glass p-6 rounded-3xl border border-slate-850 flex flex-col items-center text-center">
            <span className="text-[11px] font-display font-semibold text-slate-450 uppercase tracking-wider mb-4">Status</span>
            
            {/* Pulsing Status Glow Circle */}
            {data.status === 'COMPLETED' ? (
              <div className="p-4 bg-cozy-mint/10 border border-cozy-mint/25 text-cozy-mint-light rounded-full mb-4 shadow-lg shadow-cozy-mint/10">
                <CheckCircle2 className="w-12 h-12" />
              </div>
            ) : data.status === 'FAILED' ? (
              <div className="p-4 bg-rose-500/10 border border-rose-500/25 text-rose-450 rounded-full mb-4 shadow-lg shadow-rose-500/10">
                <XCircle className="w-12 h-12" />
              </div>
            ) : data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <div className="p-4 bg-amber-500/10 border border-amber-500/25 text-amber-450 rounded-full mb-4 animate-pulse shadow-lg shadow-amber-500/10">
                <AlertTriangle className="w-12 h-12" />
              </div>
            ) : (
              <div className="p-4 bg-cozy-indigo/10 border border-cozy-indigo/25 text-cozy-indigo rounded-full mb-4 animate-bounce shadow-lg shadow-cozy-indigo/10">
                <RefreshCw className="w-12 h-12 animate-spin" />
              </div>
            )}

            <h4 className="text-base font-display font-extrabold text-slate-100 uppercase tracking-wider">
              {data.status === 'INDEXING' && 'Indexierung'}
              {data.status === 'RUNNING' && 'Kopiere Dateien'}
              {data.status === 'PAUSED_CONNECTION_LOSS' && 'Pausiert'}
              {data.status === 'COMPLETED' && 'Erfolgreich beendet'}
              {data.status === 'FAILED' && 'Fehlgeschlagen'}
            </h4>

            {data.error_message && (
              <p className="text-xs text-rose-400 font-medium mt-3 bg-rose-500/10 border border-rose-500/20 p-2.5 rounded-xl max-w-full break-all leading-normal">
                {data.error_message}
              </p>
            )}

            {/* Counters List */}
            <div className="w-full mt-6 space-y-3.5 text-xs border-t border-slate-900/60 pt-5 text-slate-400">
              <div className="flex justify-between items-center">
                <span className="font-medium">Dateien gesamt:</span>
                <span className="font-bold text-slate-200 font-mono">{data.total_files}</span>
              </div>
              <div className="flex justify-between items-center">
                <span className="font-medium">Kopiert:</span>
                <span className="font-bold text-cozy-mint-light font-mono">{successFiles}</span>
              </div>
              <div className="flex justify-between items-center">
                <span className="font-medium">Übersprungen:</span>
                <span className="font-bold text-slate-305 font-mono">{data.skipped_files}</span>
              </div>
              <div className="flex justify-between items-center">
                <span className="font-medium">Fehlerhaft:</span>
                <span className={`font-bold font-mono ${data.failed_files > 0 ? 'text-rose-455' : 'text-slate-400'}`}>
                  {data.failed_files}
                </span>
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
                className="w-full flex items-center justify-center gap-2 py-4 bg-slate-900/40 hover:bg-slate-850/60 border border-slate-800 hover:border-slate-700 text-slate-200 rounded-2xl font-display font-bold transition-all duration-300 shadow-sm text-sm"
              >
                <Download className="w-4 h-4 text-cozy-peach" />
                Fehler-Protokoll (.CSV)
              </a>
            )}

            {/* Finish/Back button */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED') && (
              <button
                onClick={onReset}
                className="w-full flex items-center justify-center gap-2 py-4 bg-gradient-to-r from-cozy-indigo via-cozy-coral to-cozy-peach text-white rounded-2xl font-display font-bold shadow-lg hover:shadow-cozy-coral/15 transition-all duration-300 hover:scale-102 cursor-pointer text-sm"
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
