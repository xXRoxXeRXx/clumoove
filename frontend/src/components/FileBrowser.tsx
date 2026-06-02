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
      setError('Bitte wähle mindestens eine Datei oder einen Ordner für den Umzug aus.');
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
        throw new Error(`Fehler: Server antwortete mit Status ${response.status}`);
      }

      const data = await response.json();
      if (data.success && data.migration_id) {
        onStartSuccess(data.migration_id);
      } else {
        setError(data.error || 'Es gab einen Fehler beim Starten der Migration.');
      }
    } catch (err: any) {
      setError(err.message || 'Netzwerkfehler beim Starten der Migration. Bitte erneut versuchen.');
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
      <div key={file.path} className="select-none transition-all duration-200">
        {/* Row */}
        <div
          className={`flex items-center gap-3 py-2 px-3 rounded-xl hover:bg-slate-900/40 cursor-pointer border border-transparent transition-all duration-200 ${
            isSelected 
              ? 'bg-cozy-indigo/10 border-cozy-indigo/20 shadow-sm shadow-cozy-indigo/5' 
              : 'hover:border-slate-800'
          }`}
          style={{ marginLeft: `${depth * 18}px` }}
          onClick={() => (file.is_dir ? toggleExpand(file.path) : toggleSelect(file.path))}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <span className="w-5 h-5 flex items-center justify-center text-slate-500 hover:text-slate-350 transition-colors">
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-cozy-indigo" />
              ) : isExpanded ? (
                <ChevronDown className="w-4.5 h-4.5" />
              ) : (
                <ChevronRight className="w-4.5 h-4.5" />
              )}
            </span>
          ) : (
            <span className="w-5" />
          )}

          {/* Bouncy Custom Checkbox */}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              toggleSelect(file.path);
            }}
            className="focus:outline-none flex items-center justify-center"
          >
            <div className={`w-5 h-5 rounded-lg border flex items-center justify-center transition-all duration-300 ${
              isSelected 
                ? 'bg-gradient-to-tr from-cozy-indigo to-cozy-coral border-transparent scale-105 shadow-md shadow-cozy-coral/20' 
                : 'border-slate-700 hover:border-slate-500 bg-slate-950/40'
            }`}>
              {isSelected && <Check className="w-3.5 h-3.5 text-white stroke-[3px] animate-pulse" />}
            </div>
          </button>

          {/* Folder/File Icon */}
          <span className={`transition-transform duration-200 ${file.is_dir ? 'text-amber-400 group-hover:scale-105' : 'text-slate-450'}`}>
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-5 h-5 fill-amber-400/10" />
              ) : (
                <Folder className="w-5 h-5 fill-amber-400/10" />
              )
            ) : (
              <File className="w-5 h-5" />
            )}
          </span>

          {/* Name & Size */}
          <span className={`text-sm font-medium truncate flex-grow transition-colors ${
            isSelected ? 'text-white' : 'text-slate-200'
          }`}>
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="text-xs text-slate-500 font-semibold font-mono bg-slate-900/50 px-2 py-0.5 rounded-md border border-slate-850">
              {formatSize(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="mt-1 space-y-1 relative pl-3">
            {/* Visual connector vertical track line */}
            <div className="absolute left-4 top-0 bottom-2.5 w-0.5 bg-slate-850 rounded-full"></div>
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div
            className="text-xs text-slate-500 italic py-2 pl-12"
          >
            Ordner ist leer
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-4 px-2">
      {/* Header with Back Button */}
      <div className="flex items-center gap-4 mb-6">
        <button
          onClick={onBack}
          className="p-3 bg-slate-900/40 border border-slate-800 rounded-2xl hover:bg-cozy-indigo/10 hover:border-cozy-indigo/25 hover:text-cozy-indigo text-slate-400 transition-all duration-300 hover:scale-105 cursor-pointer"
        >
          <ArrowLeft className="w-5 h-5" />
        </button>
        <div>
          <h1 className="text-3xl font-display font-extrabold text-slate-100 bg-gradient-to-r from-white to-slate-300 bg-clip-text text-transparent">
            Dateien auswählen
          </h1>
          <p className="text-sm text-slate-400">
            Wähle die Ordner und Dateien aus, die du umziehen möchtest.
          </p>
        </div>
      </div>

      <div className="grid md:grid-cols-3 gap-6">
        {/* File Browser Tree Panel */}
        <div className="md:col-span-2 cozy-glass rounded-3xl p-5 min-h-[400px] max-h-[600px] overflow-y-auto scrollbar-cozy border border-slate-850">
          <div className="space-y-1">
            {directoryContents['/']?.length > 0 ? (
              directoryContents['/'].map((file) => renderNode(file, 0))
            ) : (
              <div className="flex flex-col items-center justify-center py-20 text-slate-500 gap-2">
                <Folder className="w-12 h-12 text-slate-650" />
                <p className="text-sm italic">Keine Dateien im Stammverzeichnis gefunden.</p>
              </div>
            )}
          </div>
        </div>

        {/* Configurations Sidebar (Assistant Control Box) */}
        <div className="space-y-6">
          <div className="cozy-glass p-5.5 rounded-3xl border border-slate-850 space-y-6">
            <div className="flex items-center gap-2 mb-1">
              <span className="text-lg">⚙️</span>
              <h3 className="font-display font-bold text-slate-100 text-base">Konfiguration</h3>
            </div>

            {/* Target Path (Disabled/Standard in MVP) */}
            <div className="space-y-2">
              <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider">Zielverzeichnis</label>
              <div className="relative">
                <HardDrive className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500" />
                <input
                  type="text"
                  value={targetDir}
                  className="w-full bg-slate-950/60 border border-slate-850 rounded-xl py-3 pl-10 pr-4 text-sm text-slate-500 focus:outline-none"
                  disabled
                />
              </div>
              <p className="text-[10px] text-slate-550 leading-relaxed">
                Im aktuellen Release werden Daten standardmäßig in das Stammverzeichnis (/) der Ziel-Instanz kopiert.
              </p>
            </div>

            {/* Conflict Strategy Tiles */}
            <div className="space-y-3">
              <label className="block text-[11px] font-display font-semibold text-slate-400 uppercase tracking-wider">Bei Namenskonflikten</label>
              <div className="space-y-2">
                {/* SKIP tile */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('SKIP')}
                  className={`w-full text-left p-3 rounded-xl border transition-all duration-300 cursor-pointer ${
                    conflictStrategy === 'SKIP'
                      ? 'bg-cozy-indigo/10 border-cozy-indigo/50 shadow-md shadow-cozy-indigo/5'
                      : 'bg-slate-950/30 border-slate-850 hover:border-slate-800'
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <span className={`text-xs font-bold font-display ${conflictStrategy === 'SKIP' ? 'text-cozy-indigo' : 'text-slate-300'}`}>
                      Überspringen
                    </span>
                    {conflictStrategy === 'SKIP' && <Check className="w-4 h-4 text-cozy-indigo" />}
                  </div>
                  <p className="text-[10px] text-slate-450 mt-1 leading-normal">
                    Bereits existierende Dateien werden im Ziel nicht überschrieben.
                  </p>
                </button>

                {/* OVERWRITE tile */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('OVERWRITE')}
                  className={`w-full text-left p-3 rounded-xl border transition-all duration-300 cursor-pointer ${
                    conflictStrategy === 'OVERWRITE'
                      ? 'bg-cozy-coral/10 border-cozy-coral/50 shadow-md shadow-cozy-coral/5'
                      : 'bg-slate-950/30 border-slate-850 hover:border-slate-800'
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <span className={`text-xs font-bold font-display ${conflictStrategy === 'OVERWRITE' ? 'text-cozy-peach' : 'text-slate-300'}`}>
                      Überschreiben
                    </span>
                    {conflictStrategy === 'OVERWRITE' && <Check className="w-4 h-4 text-cozy-peach" />}
                  </div>
                  <p className="text-[10px] text-slate-450 mt-1 leading-normal">
                    Dateien mit gleichem Namen werden im Ziel bedingungslos ersetzt.
                  </p>
                </button>

                {/* RENAME tile */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('RENAME')}
                  className={`w-full text-left p-3 rounded-xl border transition-all duration-300 cursor-pointer ${
                    conflictStrategy === 'RENAME'
                      ? 'bg-cozy-mint/10 border-cozy-mint/50 shadow-md shadow-cozy-mint/5'
                      : 'bg-slate-950/30 border-slate-850 hover:border-slate-800'
                  }`}
                >
                  <div className="flex items-center justify-between">
                    <span className={`text-xs font-bold font-display ${conflictStrategy === 'RENAME' ? 'text-cozy-mint-light' : 'text-slate-300'}`}>
                      Umbenennen
                    </span>
                    {conflictStrategy === 'RENAME' && <Check className="w-4 h-4 text-cozy-mint-light" />}
                  </div>
                  <p className="text-[10px] text-slate-450 mt-1 leading-normal">
                    Kopien erhalten automatisch eine Nummerierung (z.B. datei(1).pdf).
                  </p>
                </button>
              </div>
            </div>
          </div>

          {error && (
            <div className="p-4.5 bg-rose-500/10 border border-rose-550/20 rounded-2xl text-xs text-rose-250 leading-relaxed flex gap-2">
              <AlertTriangle className="w-4 h-4 shrink-0 text-rose-400" />
              <span>{error}</span>
            </div>
          )}

          {/* Action button */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="w-full flex items-center justify-center gap-2.5 py-4.5 bg-gradient-to-r from-cozy-mint to-cozy-mint-light text-slate-950 rounded-2xl font-display font-bold shadow-lg hover:shadow-cozy-mint/20 hover:scale-102 transition-all duration-300 disabled:opacity-50 cursor-pointer"
          >
            {starting ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Indexiere Dateien...</span>
              </>
            ) : (
              <>
                <Play className="w-5 h-5 fill-current" />
                <span>Migration starten</span>
              </>
            )}
          </button>
        </div>
      </div>
    </div>
  );
};
