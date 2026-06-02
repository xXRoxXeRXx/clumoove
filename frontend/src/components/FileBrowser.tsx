import React, { useState } from 'react';
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, Check, Play, ArrowLeft, RefreshCw, HardDrive, AlertTriangle } from 'lucide-react';

interface CloudFile {
  path: string;
  name: string;
  size: number;
  is_dir: boolean;
  hash: string;
  last_modified: string;
}

interface FileBrowserProps {
  initialFiles: CloudFile[];
  credentials: any;
  apiUrl: string;
  onBack: () => void;
  onStartSuccess: (migrationId: string) => void;
}

export const FileBrowser: React.FC<FileBrowserProps> = ({
  initialFiles,
  credentials,
  apiUrl,
  onBack,
  onStartSuccess,
}) => {
  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>({});
  const [directoryContents, setDirectoryContents] = useState<Record<string, CloudFile[]>>({
    '/': initialFiles,
  });
  const [selectedPaths, setSelectedPaths] = useState<Record<string, boolean>>({});
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [conflictStrategy, setConflictStrategy] = useState('SKIP');
  const [targetDir] = useState('/');
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchChildren = async (folderPath: string) => {
    if (directoryContents[folderPath] || loadingPaths[folderPath]) return;

    setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
    try {
      const response = await fetch(`${apiUrl}/api/migration/connect`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...credentials,
          path: folderPath,
        }),
      });

      if (!response.ok) throw new Error('Fehler beim Laden des Verzeichnisses');

      const data = await response.json();
      if (data.success) {
        setDirectoryContents((prev) => ({ ...prev, [folderPath]: data.files || [] }));
      }
    } catch (err: any) {
      console.error(err);
    } finally {
      setLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
    }
  };

  const toggleExpand = (folderPath: string) => {
    const isExpanded = !!expandedPaths[folderPath];
    setExpandedPaths((prev) => ({ ...prev, [folderPath]: !isExpanded }));
    if (!isExpanded) {
      fetchChildren(folderPath);
    }
  };

  const toggleSelect = (filePath: string) => {
    setSelectedPaths((prev) => ({ ...prev, [filePath]: !prev[filePath] }));
  };

  const handleStartMigration = async () => {
    const pathsToMigrate = Object.keys(selectedPaths).filter((p) => selectedPaths[p]);
    if (pathsToMigrate.length === 0) {
      setError('Bitte wähle mindestens ein Verzeichnis oder eine Datei aus.');
      return;
    }

    setStarting(true);
    setError(null);

    try {
      const response = await fetch(`${apiUrl}/api/migration/start`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          ...credentials,
          conflict_strategy: conflictStrategy,
          paths: pathsToMigrate,
        }),
      });

      if (!response.ok) {
        throw new Error(`Migration konnte nicht gestartet werden. Status ${response.status}`);
      }

      const data = await response.json();
      if (data.success && data.migration_id) {
        onStartSuccess(data.migration_id);
      } else {
        setError(data.error || 'Fehler beim Starten der Migration.');
      }
    } catch (err: any) {
      setError(err.message || 'Verbindungsfehler beim Starten der Übertragung.');
    } finally {
      setStarting(false);
    }
  };

  const formatSize = (bytes: number) => {
    if (bytes === 0) return '0 B';
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
  };

  // Render tree node recursively
  const renderNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!expandedPaths[file.path];
    const isSelected = !!selectedPaths[file.path];
    const isLoading = !!loadingPaths[file.path];
    const children = directoryContents[file.path] || [];

    return (
      <div key={file.path} className="select-none font-mono text-xs">
        {/* Row */}
        <div
          className={`flex items-center gap-3 py-3 px-4 border-b border-dashed border-slate-300 hover:bg-bauhaus-sand/40 cursor-pointer transition-colors ${
            isSelected ? 'bg-bauhaus-rust/5 font-bold' : ''
          }`}
          style={{ paddingLeft: `${depth * 20 + 16}px` }}
          onClick={() => (file.is_dir ? toggleExpand(file.path) : toggleSelect(file.path))}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <span className="w-5 h-5 flex items-center justify-center text-bauhaus-ink hover:text-bauhaus-rust transition-colors">
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-bauhaus-rust" />
              ) : isExpanded ? (
                <ChevronDown className="w-4.5 h-4.5 stroke-[2.5]" />
              ) : (
                <ChevronRight className="w-4.5 h-4.5 stroke-[2.5]" />
              )}
            </span>
          ) : (
            <span className="w-5" />
          )}

          {/* Sharp, Flat Checkbox */}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              toggleSelect(file.path);
            }}
            className="focus:outline-none flex items-center justify-center"
          >
            <div className={`w-5.5 h-5.5 border-2 border-bauhaus-ink rounded-none flex items-center justify-center transition-all ${
              isSelected 
                ? 'bg-bauhaus-rust text-white border-bauhaus-ink' 
                : 'bg-white border-bauhaus-ink hover:bg-slate-100'
            }`}>
              {isSelected && <Check className="w-4 h-4 text-white stroke-[3.5]" />}
            </div>
          </button>

          {/* Simple ink-outlined Folder/File Icon */}
          <span className="text-bauhaus-ink">
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-4.5 h-4.5" />
              ) : (
                <Folder className="w-4.5 h-4.5" />
              )
            ) : (
              <File className="w-4.5 h-4.5 text-slate-500" />
            )}
          </span>

          {/* Name & Size */}
          <span className={`text-[12px] truncate flex-grow leading-none ${
            isSelected ? 'text-bauhaus-ink font-bold' : 'text-slate-800'
          }`}>
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="text-[10px] font-bold text-slate-500 border border-slate-305 px-2 py-0.5 bg-bauhaus-sand">
              {formatSize(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="relative pl-2">
            {/* Visual connector left track */}
            <div className="absolute left-6 top-0 bottom-4 w-0.5 bg-bauhaus-ink/20"></div>
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div
            className="text-[10px] text-slate-400 italic py-2.5 pl-14"
          >
            // LEERES VERZEICHNIS
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-2">
      
      {/* Header */}
      <div className="flex items-center gap-5 mb-8">
        <button
          onClick={onBack}
          className="px-4 py-2 border-2 border-bauhaus-ink bg-white font-mono text-[11px] font-bold uppercase tracking-wider shadow-flat active:translate-x-[2px] active:translate-y-[2px] active:shadow-flat-active transition-all cursor-pointer"
        >
          <span className="flex items-center gap-1.5">
            <ArrowLeft className="w-4 h-4 stroke-[3]" />
            Zurück
          </span>
        </button>
        <div>
          <h1 className="font-serif font-black text-3xl uppercase tracking-tight text-bauhaus-ink leading-tight">
            Pfade selektieren
          </h1>
          <p className="text-xs font-medium text-slate-650 mt-1">
            Wähle die Dateibestände für den Übertragungslauf aus.
          </p>
        </div>
      </div>

      <div className="grid md:grid-cols-3 gap-8">
        
        {/* Ledger Browser Tree Card */}
        <div className="md:col-span-2 border-2 border-bauhaus-ink bg-white shadow-flat rounded-none min-h-[400px] max-h-[600px] overflow-y-auto scrollbar-bauhaus p-0">
          <div className="bg-bauhaus-sand border-b-2 border-bauhaus-ink px-4 py-2.5 font-mono text-[10px] font-black uppercase text-slate-600 tracking-wider">
            [ VERZEICHNIS-INDEX ]
          </div>
          <div className="p-2">
            {directoryContents['/']?.length > 0 ? (
              directoryContents['/'].map((file) => renderNode(file, 0))
            ) : (
              <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-2">
                <Folder className="w-10 h-10 text-slate-350" />
                <p className="font-mono text-xs italic">// KEINE DATEIEN GEFUNDEN</p>
              </div>
            )}
          </div>
        </div>

        {/* Configurations Sidebar (Bauhaus Control Column) */}
        <div className="space-y-6">
          <div className="border-2 border-bauhaus-ink bg-bauhaus-sand p-6 shadow-flat rounded-none space-y-6">
            
            <div className="flex items-center gap-2 border-b border-bauhaus-ink pb-3 mb-1">
              <h3 className="font-serif font-black text-lg uppercase tracking-tight">Konfiguration</h3>
            </div>

            {/* Target Path (Disabled/Standard in MVP) */}
            <div className="space-y-2 font-mono text-xs">
              <label className="block font-bold text-slate-500 uppercase tracking-widest">Ziel-Stammverzeichnis</label>
              <div className="relative">
                <HardDrive className="absolute left-3top-1/2 -translate-y-1/2 w-4 h-4 text-bauhaus-ink hidden" />
                <input
                  type="text"
                  value={targetDir}
                  className="w-full bg-slate-200/50 border-1.5 border-bauhaus-ink rounded-none py-2 px-3 text-slate-500 cursor-not-allowed"
                  disabled
                />
              </div>
              <p className="text-[10px] text-slate-550 leading-relaxed">
                Kopiert Bestände direkt in das Hauptverzeichnis (/) des Zielsystems.
              </p>
            </div>

            {/* Conflict Strategy block selector */}
            <div className="space-y-3 font-mono text-xs">
              <label className="block font-bold text-slate-500 uppercase tracking-widest">Kollisions-Behandlung</label>
              <div className="space-y-2.5">
                {/* SKIP card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('SKIP')}
                  className={`w-full text-left p-3.5 border-1.5 transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'SKIP'
                      ? 'bg-bauhaus-ink text-white border-bauhaus-ink shadow-none'
                      : 'bg-white text-bauhaus-ink border-bauhaus-ink hover:bg-slate-100'
                  }`}
                >
                  <div className="flex items-center justify-between font-bold text-xs">
                    <span>ÜBERSCHREIBEN VERMEIDEN</span>
                    {conflictStrategy === 'SKIP' && <Check className="w-4 h-4 text-bauhaus-rust stroke-[3.5]" />}
                  </div>
                  <p className={`text-[10px] mt-1.5 leading-normal ${conflictStrategy === 'SKIP' ? 'text-slate-300' : 'text-slate-500'}`}>
                    Vorhandene Dateien bleiben unberührt (SKIP).
                  </p>
                </button>

                {/* OVERWRITE card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('OVERWRITE')}
                  className={`w-full text-left p-3.5 border-1.5 transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'OVERWRITE'
                      ? 'bg-bauhaus-ink text-white border-bauhaus-ink shadow-none'
                      : 'bg-white text-bauhaus-ink border-bauhaus-ink hover:bg-slate-100'
                  }`}
                >
                  <div className="flex items-center justify-between font-bold text-xs">
                    <span>ZIEL ERSETZEN</span>
                    {conflictStrategy === 'OVERWRITE' && <Check className="w-4 h-4 text-bauhaus-rust stroke-[3.5]" />}
                  </div>
                  <p className={`text-[10px] mt-1.5 leading-normal ${conflictStrategy === 'OVERWRITE' ? 'text-slate-300' : 'text-slate-500'}`}>
                    Ersetzt doppelte Dateinamen im Ziel bedingungslos.
                  </p>
                </button>

                {/* RENAME card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('RENAME')}
                  className={`w-full text-left p-3.5 border-1.5 transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'RENAME'
                      ? 'bg-bauhaus-ink text-white border-bauhaus-ink shadow-none'
                      : 'bg-white text-bauhaus-ink border-bauhaus-ink hover:bg-slate-100'
                  }`}
                >
                  <div className="flex items-center justify-between font-bold text-xs">
                    <span>AUTO-NUMMERIERUNG</span>
                    {conflictStrategy === 'RENAME' && <Check className="w-4 h-4 text-bauhaus-rust stroke-[3.5]" />}
                  </div>
                  <p className={`text-[10px] mt-1.5 leading-normal ${conflictStrategy === 'RENAME' ? 'text-slate-300' : 'text-slate-500'}`}>
                    Fügt Suffixe hinzu (z. B. datei(1).pdf).
                  </p>
                </button>
              </div>
            </div>
          </div>

          {error && (
            <div className="p-4 bg-white border-2 border-bauhaus-rust shadow-flat-rust text-xs font-mono font-bold text-bauhaus-rust leading-relaxed flex gap-2">
              <AlertTriangle className="w-4 h-4 shrink-0 text-bauhaus-rust" />
              <span>{error}</span>
            </div>
          )}

          {/* Action submit button */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="w-full flex items-center justify-center gap-3 py-5 bg-bauhaus-moss text-white border-2 border-bauhaus-ink shadow-flat hover:translate-x-[2px] hover:translate-y-[2px] hover:shadow-flat-active active:translate-x-[4px] active:translate-y-[4px] active:shadow-none transition-all duration-150 font-serif text-lg font-black uppercase tracking-wider cursor-pointer disabled:opacity-50"
          >
            {starting ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Indexierung läuft...</span>
              </>
            ) : (
              <>
                <Play className="w-5 h-5 fill-current stroke-[3]" />
                <span>Transfer starten</span>
              </>
            )}
          </button>
        </div>
      </div>
    </div>
  );
};
