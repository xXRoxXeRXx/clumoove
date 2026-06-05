import React, { useEffect, useState, useRef } from 'react';
import { RefreshCw, AlertTriangle, Download, Clock, HardDrive, Coffee, Terminal } from 'lucide-react';

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
        <RefreshCw className="w-10 h-10 text-portal-navy animate-spin" />
        <p className="font-sans text-xs italic text-slate-500">// INITIALISIERE PROZESS-MONITOR</p>
      </div>
    );
  }

  // Calculated stats
  const byteProgressPercent = data.total_bytes > 0 
    ? Math.min(Math.round((data.processed_bytes / data.total_bytes) * 100), 100) 
    : 0;

  const successFiles = Math.max(0, data.processed_files - data.failed_files - data.skipped_files);

  return (
    <div className="w-full max-w-4xl mx-auto py-2">
      
      {/* Background Mode Guarantee Stamp (Grab a coffee) */}
      <div className="mb-6 p-4 bg-[#f0f4f8] border border-[#d2d9e0] rounded-lg flex items-center justify-between text-xs text-[#002f6c] font-semibold">
        <div className="flex items-center gap-3">
          <Coffee className="w-4 h-4 text-portal-orange shrink-0" />
          <span>Der Migrationstransfer läuft serverseitig. Du kannst diesen Tab bedenkenlos schließen.</span>
        </div>
        <span className="hidden sm:inline border border-[#002f6c]/20 px-2.5 py-0.5 bg-white text-[9px] tracking-widest font-black text-slate-500 rounded-full">
          STAMP.RUN
        </span>
      </div>

      {/* PAUSED CONNECTION LOSS WARNING */}
      {data.status === 'PAUSED_CONNECTION_LOSS' && (
        <div className="mb-6 p-5 border border-amber-200 bg-amber-50 rounded-lg flex items-start gap-4">
          <AlertTriangle className="w-6 h-6 shrink-0 text-amber-600 animate-bounce" />
          <div className="text-xs leading-relaxed text-slate-700">
            <h4 className="font-display font-bold text-amber-900 uppercase tracking-wide">Verbindungsabbruch zur Instanz</h4>
            <p className="text-slate-600 mt-1.5 leading-relaxed">
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
          <div className="border border-portal-border bg-white p-6 shadow-portal rounded-lg relative overflow-hidden">
            <div className="flex items-end justify-between mb-5 border-b border-portal-border pb-4">
              <div>
                <span className="font-display font-bold text-[10px] text-slate-450 uppercase tracking-wider">Fortschritt</span>
                <h3 className="font-display font-extrabold text-5xl text-portal-navy mt-1.5 leading-none">
                  {byteProgressPercent}%
                </h3>
              </div>
              <div className="text-right font-sans">
                <span className="font-display font-bold text-[10px] text-slate-450 uppercase tracking-wider">Übertragungs-Rate</span>
                <p className="text-sm font-bold text-emerald-600 mt-1.5 font-mono">
                  {formatSize(speed)}/s
                </p>
              </div>
            </div>

            {/* Rounded Progress Bar */}
            <div className="w-full bg-slate-100 border border-portal-border h-5 p-0.5 mb-5 rounded-full">
              <div
                className="bg-portal-orange h-full rounded-full transition-all duration-300 ease-out"
                style={{ width: `${byteProgressPercent}%` }}
              ></div>
            </div>

            <div className="grid grid-cols-2 gap-4 text-[10.5px] font-semibold text-slate-500 uppercase tracking-wider">
              <div className="flex items-center gap-2">
                <HardDrive className="w-4 h-4 text-portal-navy" />
                <span>Übertragen: <strong className="text-slate-800 font-mono">{formatSize(data.processed_bytes)}</strong> / {formatSize(data.total_bytes)}</span>
              </div>
              <div className="flex items-center gap-2 justify-end">
                <Clock className="w-4 h-4 text-portal-navy" />
                <span>Restlaufzeit: <strong className="text-slate-800">{eta}</strong></span>
              </div>
            </div>
          </div>

          {/* Typewriter-Style Live Protocol Feed */}
          <div className="border border-portal-border bg-white shadow-portal rounded-lg p-5 flex flex-col h-[280px]">
            <div className="flex items-center gap-2 mb-3 pb-3 border-b border-portal-border">
              <Terminal className="w-4.5 h-4.5 text-portal-orange" />
              <h4 className="font-display font-bold text-slate-450 text-[10px] uppercase tracking-wider">Live-Protokoll</h4>
            </div>
            
            <div className="flex-grow overflow-y-auto scrollbar-portal space-y-2 pr-1 font-mono text-[11px]">
              {logs.map((log, index) => (
                <div 
                  key={index} 
                  className={`py-1.5 px-3 border border-slate-100 rounded-lg leading-relaxed break-all ${
                    log.startsWith('✔') 
                      ? 'bg-emerald-50 text-emerald-700 font-semibold border-emerald-100' 
                      : log.startsWith('🚀') || log.startsWith('⚡')
                      ? 'bg-slate-50 text-slate-700 font-semibold border-slate-150'
                      : log.startsWith('❌')
                      ? 'bg-rose-50 text-rose-700 font-semibold border-rose-100'
                      : log.startsWith('⚠️')
                      ? 'bg-amber-50 text-amber-700 font-semibold border-amber-100'
                      : 'bg-slate-50 text-slate-500 border-slate-100'
                  }`}
                >
                  {log}
                </div>
              ))}
              <div ref={logsEndRef} />
            </div>
          </div>
        </div>

        {/* Status card & Sidebar Column */}
        <div className="space-y-6">
          <div className="border border-portal-border bg-white p-6 shadow-portal rounded-lg flex flex-col items-center text-center">
            <span className="font-display font-bold text-[10px] text-slate-450 uppercase tracking-widest mb-4">STATUS</span>
            
            {/* Stamp status badge */}
            {data.status === 'COMPLETED' ? (
              <div className="bg-emerald-50 text-emerald-700 border border-emerald-200 px-5 py-2 font-display font-bold text-sm rounded-full shadow-sm mb-5">
                ERFOLGREICH BEENDET
              </div>
            ) : data.status === 'FAILED' ? (
              <div className="bg-rose-50 text-rose-700 border border-rose-200 px-5 py-2 font-display font-bold text-sm rounded-full shadow-sm mb-5">
                FEHLGESCHLAGEN
              </div>
            ) : data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <div className="bg-amber-50 text-amber-700 border border-amber-250 px-5 py-2 font-display font-bold text-sm rounded-full shadow-sm mb-5 animate-pulse">
                VORÜBERGEHEND PAUSIERT
              </div>
            ) : (
              <div className="bg-slate-50 text-portal-navy border border-portal-navy/20 px-5 py-2 font-display font-bold text-sm rounded-full shadow-sm mb-5 animate-pulse">
                DATEI-TRANSFER
              </div>
            )}

            <h4 className="font-display font-bold text-slate-700 text-xs tracking-wider uppercase mt-1">
              Migration: {data.status}
            </h4>

            {data.error_message && (
              <p className="font-sans text-[11px] text-rose-600 mt-3 bg-rose-50 border border-rose-200 p-2.5 rounded-lg leading-normal uppercase">
                Fehlermeldung: {data.error_message}
              </p>
            )}

            {/* Invoiced Counters table */}
            <div className="w-full mt-6 space-y-2 font-sans text-xs border-t border-slate-100 pt-5 text-slate-500">
              <div className="flex justify-between items-center py-1 border-b border-slate-100">
                <span>Dateien gesamt:</span>
                <span className="font-bold text-slate-800 font-mono">{data.total_files}</span>
              </div>
              <div className="flex justify-between items-center py-1 border-b border-slate-100">
                <span>Übertragen:</span>
                <span className="font-bold text-emerald-600 font-mono">{successFiles}</span>
              </div>
              <div className="flex justify-between items-center py-1 border-b border-slate-100">
                <span>Übersprungen:</span>
                <span className="font-bold text-slate-800 font-mono">{data.skipped_files}</span>
              </div>
              <div className="flex justify-between items-center py-1">
                <span>Fehlgeschlagen:</span>
                <span className={`font-bold font-mono ${data.failed_files > 0 ? 'text-rose-600' : 'text-slate-600'}`}>
                  {data.failed_files}
                </span>
              </div>
            </div>
          </div>

          {/* Action buttons */}
          <div className="space-y-4">
            {/* Report Download */}
            {data.failed_files > 0 && (
              <a
                href={`${apiUrl}/api/migration/${migrationId}/report`}
                download
                className="w-full flex items-center justify-center gap-2 py-4 bg-white border border-portal-border rounded-lg shadow-sm text-slate-700 hover:bg-slate-50 transition-colors font-display text-xs font-bold uppercase tracking-wider text-center"
              >
                <Download className="w-4 h-4 text-portal-orange" />
                Fehlerbericht (.CSV)
              </a>
            )}

            {/* Reset Button */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED') && (
              <button
                onClick={onReset}
                className="w-full flex items-center justify-center gap-2 py-4 bg-portal-orange text-white rounded-lg font-display text-sm font-bold shadow-sm hover:bg-portal-orange-hover hover:scale-101 active:scale-99 transition-all duration-200 cursor-pointer"
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
