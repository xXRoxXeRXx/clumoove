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
        <RefreshCw className="w-10 h-10 text-bauhaus-rust animate-spin" />
        <p className="font-mono text-xs italic">// INITIALISIERE PROZESS-MONITOR</p>
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
      <div className="mb-8 p-4 border-2 border-dashed border-bauhaus-ink bg-bauhaus-sand flex items-center justify-between text-xs font-mono font-bold uppercase tracking-wider text-slate-750">
        <div className="flex items-center gap-3">
          <Coffee className="w-4 h-4 text-bauhaus-rust shrink-0" />
          <span>Hintergrund-Lauf aktiv: Du kannst diesen Tab schließen. Der Transfer läuft server-seitig weiter.</span>
        </div>
        <span className="hidden sm:inline border border-bauhaus-ink px-2.5 py-0.5 bg-white text-[9px] tracking-widest font-black text-slate-500 uppercase">
          STAMP.RUN
        </span>
      </div>

      {/* PAUSED CONNECTION LOSS WARNING */}
      {data.status === 'PAUSED_CONNECTION_LOSS' && (
        <div className="mb-8 p-5 border-2 border-bauhaus-yellow bg-white shadow-flat rounded-none flex items-start gap-4">
          <AlertTriangle className="w-6 h-6 shrink-0 text-bauhaus-yellow animate-bounce" />
          <div className="font-mono text-xs">
            <h4 className="font-bold text-bauhaus-ink uppercase tracking-wide">// VERBINDUNGSABBRUCH ZUR INSTANZ</h4>
            <p className="text-slate-650 mt-1.5 leading-relaxed">
              Eine Instanz antwortet nicht. Das System pausiert temporär und prüft die Erreichbarkeit selbstständig alle 60 Sekunden. Sobald die Server wieder antworten, wird der Transfer exakt am Abbruchpunkt fortgesetzt.
            </p>
          </div>
        </div>
      )}

      {/* Main Print Grid */}
      <div className="grid md:grid-cols-3 gap-8">
        
        {/* Progress & Metrics */}
        <div className="md:col-span-2 space-y-8">
          
          {/* Main metric card */}
          <div className="border-2 border-bauhaus-ink bg-white p-6 shadow-flat rounded-none relative overflow-hidden">
            <div className="flex items-end justify-between mb-5 border-b border-bauhaus-ink pb-4">
              <div>
                <span className="font-mono text-[10px] font-bold text-slate-500 uppercase tracking-widest">Fortschritt</span>
                <h3 className="font-serif font-black text-6xl text-bauhaus-ink mt-1.5 leading-none">
                  {byteProgressPercent}%
                </h3>
              </div>
              <div className="text-right">
                <span className="font-mono text-[10px] font-bold text-slate-500 uppercase tracking-widest">Übertragungs-Rate</span>
                <p className="font-mono text-sm font-bold text-bauhaus-moss mt-1.5">
                  {formatSize(speed)}/Sek
                </p>
              </div>
            </div>

            {/* Flat block-fill Progress Bar */}
            <div className="w-full bg-bauhaus-sand border-2 border-bauhaus-ink h-7 p-0.5 mb-5 rounded-none">
              <div
                className="bg-bauhaus-rust h-full transition-all duration-300 ease-out"
                style={{ width: `${byteProgressPercent}%` }}
              ></div>
            </div>

            <div className="grid grid-cols-2 gap-4 font-mono text-[10px] font-bold uppercase tracking-wider text-slate-600">
              <div className="flex items-center gap-2">
                <HardDrive className="w-4 h-4 text-bauhaus-ink" />
                <span>Gesamtvolumen: <strong className="text-bauhaus-ink">{formatSize(data.processed_bytes)}</strong> / {formatSize(data.total_bytes)}</span>
              </div>
              <div className="flex items-center gap-2 justify-end">
                <Clock className="w-4 h-4 text-bauhaus-ink" />
                <span>Restlaufzeit: <strong className="text-bauhaus-ink">{eta}</strong></span>
              </div>
            </div>
          </div>

          {/* Typewriter-Style Live Protocol Feed */}
          <div className="border-2 border-bauhaus-ink bg-white shadow-flat rounded-none p-5 flex flex-col h-[280px]">
            <div className="flex items-center gap-2 mb-3 pb-3 border-b border-bauhaus-ink">
              <Terminal className="w-4.5 h-4.5 text-bauhaus-rust" />
              <h4 className="font-mono text-xs font-bold text-slate-500 uppercase tracking-widest">Live-Protokoll (Ticker)</h4>
            </div>
            
            <div className="flex-grow overflow-y-auto scrollbar-bauhaus space-y-2 pr-1 font-mono text-[11px]">
              {logs.map((log, index) => (
                <div 
                  key={index} 
                  className={`py-1.5 px-3 border border-slate-300 border-dashed leading-relaxed break-all ${
                    log.startsWith('✔') 
                      ? 'bg-bauhaus-moss/5 text-bauhaus-moss font-bold border-bauhaus-moss/30' 
                      : log.startsWith('🚀') || log.startsWith('⚡')
                      ? 'bg-bauhaus-rust/5 text-bauhaus-rust font-bold border-bauhaus-rust/30'
                      : log.startsWith('❌')
                      ? 'bg-rose-500/5 text-rose-700 font-bold border-rose-500/30'
                      : log.startsWith('⚠️')
                      ? 'bg-bauhaus-yellow/5 text-bauhaus-yellow font-bold border-bauhaus-yellow/30'
                      : 'bg-slate-50 text-slate-600 border-slate-200'
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
          <div className="border-2 border-bauhaus-ink bg-bauhaus-sand p-6 shadow-flat rounded-none flex flex-col items-center text-center">
            <span className="font-mono text-[10px] font-bold text-slate-550 uppercase tracking-widest mb-4">// ZUSTANDS-STAMP</span>
            
            {/* Stamp status badge */}
            {data.status === 'COMPLETED' ? (
              <div className="border-4 border-double border-bauhaus-moss text-bauhaus-moss px-5 py-2 font-serif font-black text-xl uppercase tracking-widest bg-white shadow-flat-moss mb-5 rotate-2">
                BEENDET
              </div>
            ) : data.status === 'FAILED' ? (
              <div className="border-4 border-double border-bauhaus-rust text-bauhaus-rust px-5 py-2 font-serif font-black text-xl uppercase tracking-widest bg-white shadow-flat-rust mb-5 -rotate-2">
                FEHLER
              </div>
            ) : data.status === 'PAUSED_CONNECTION_LOSS' ? (
              <div className="border-4 border-double border-bauhaus-yellow text-bauhaus-yellow px-5 py-2 font-serif font-black text-xl uppercase tracking-widest bg-white shadow-flat mb-5 animate-pulse">
                PAUSIERT
              </div>
            ) : (
              <div className="border-4 border-double border-bauhaus-ink text-bauhaus-ink px-5 py-2 font-serif font-black text-xl uppercase tracking-widest bg-white shadow-flat mb-5 animate-bounce">
                TRANSFER
              </div>
            )}

            <h4 className="font-mono text-[11px] font-black uppercase text-slate-500 tracking-wider">
              Systemstatus: {data.status}
            </h4>

            {data.error_message && (
              <p className="font-mono text-[10px] text-bauhaus-rust mt-3 bg-white border border-bauhaus-rust p-2.5 leading-normal uppercase">
                Fehlermeldung: {data.error_message}
              </p>
            )}

            {/* Invoiced Counters table */}
            <div className="w-full mt-6 space-y-2 font-mono text-[11px] border-t-2 border-dashed border-bauhaus-ink pt-5 text-slate-650">
              <div className="flex justify-between items-center py-1 border-b border-dashed border-slate-300">
                <span>Dateien gesamt:</span>
                <span className="font-bold text-bauhaus-ink">{data.total_files}</span>
              </div>
              <div className="flex justify-between items-center py-1 border-b border-dashed border-slate-300">
                <span>Übertragen:</span>
                <span className="font-bold text-bauhaus-moss">{successFiles}</span>
              </div>
              <div className="flex justify-between items-center py-1 border-b border-dashed border-slate-300">
                <span>Übersprungen:</span>
                <span className="font-bold text-bauhaus-ink">{data.skipped_files}</span>
              </div>
              <div className="flex justify-between items-center py-1">
                <span>Fehlgeschlagen:</span>
                <span className={`font-bold ${data.failed_files > 0 ? 'text-bauhaus-rust' : 'text-slate-600'}`}>
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
                className="w-full flex items-center justify-center gap-2 py-4 bg-white border-2 border-bauhaus-ink shadow-flat text-bauhaus-ink hover:translate-x-[2px] hover:translate-y-[2px] hover:shadow-flat-active active:translate-x-[4px] active:translate-y-[4px] active:shadow-none transition-all duration-150 font-mono text-xs font-bold uppercase tracking-wider text-center"
              >
                <Download className="w-4 h-4 text-bauhaus-rust" />
                Fehlerbericht (.CSV)
              </a>
            )}

            {/* Reset Button */}
            {(data.status === 'COMPLETED' || data.status === 'FAILED') && (
              <button
                onClick={onReset}
                className="w-full flex items-center justify-center gap-2 py-4 bg-bauhaus-rust text-white border-2 border-bauhaus-ink shadow-flat font-serif text-base font-black uppercase tracking-wider hover:translate-x-[2px] hover:translate-y-[2px] hover:shadow-flat-active active:translate-x-[4px] active:translate-y-[4px] active:shadow-none transition-all duration-150 cursor-pointer"
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
