import React, { useState } from 'react';
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, CheckSquare, Square, Play, ArrowLeft, RefreshCw } from 'lucide-react';

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
  const [targetDir, setTargetDir] = useState('/');
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
      setError('Bitte wählen Sie mindestens eine Datei oder einen Ordner aus.');
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
          // Target dir could be passed here if backend supported it, for now standard root/target is /
        }),
      });

      if (!response.ok) {
        throw new Error(`Fehler: Server antwortete mit Status ${response.status}`);
      }

      const data = await response.json();
      if (data.success && data.migration_id) {
        onStartSuccess(data.migration_id);
      } else {
        setError(data.error || 'Fehler beim Starten der Migration.');
      }
    } catch (err: any) {
      setError(err.message || 'Netzwerkfehler beim Starten der Migration.');
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
      <div key={file.path} className="select-none">
        {/* Row */}
        <div
          className={`flex items-center gap-2 py-2 px-3 rounded-lg hover:bg-slate-800/40 cursor-pointer transition-all ${
            isSelected ? 'bg-blue-500/5 border border-blue-500/10' : 'border border-transparent'
          }`}
          style={{ paddingLeft: `${depth * 20 + 12}px` }}
          onClick={() => (file.is_dir ? toggleExpand(file.path) : toggleSelect(file.path))}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <span className="w-5 h-5 flex items-center justify-center text-slate-500 hover:text-slate-200">
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin" />
              ) : isExpanded ? (
                <ChevronDown className="w-4 h-4" />
              ) : (
                <ChevronRight className="w-4 h-4" />
              )}
            </span>
          ) : (
            <span className="w-5" />
          )}

          {/* Custom Checkbox */}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              toggleSelect(file.path);
            }}
            className="text-slate-400 hover:text-blue-400 transition-colors"
          >
            {isSelected ? (
              <CheckSquare className="w-5 h-5 text-blue-500" />
            ) : (
              <Square className="w-5 h-5 text-slate-600" />
            )}
          </button>

          {/* Node Icon */}
          <span className={file.is_dir ? 'text-blue-400' : 'text-slate-400'}>
            {file.is_dir ? (
              isExpanded ? <FolderOpen className="w-5 h-5" /> : <Folder className="w-5 h-5" />
            ) : (
              <File className="w-5 h-5" />
            )}
          </span>

          {/* Name & Size */}
          <span className="text-sm font-medium text-slate-200 truncate flex-grow">
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="text-xs text-slate-500 font-mono">
              {formatSize(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="mt-1 space-y-1">
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div
            className="text-xs text-slate-600 italic py-2"
            style={{ paddingLeft: `${(depth + 1) * 20 + 36}px` }}
          >
            Ordner ist leer
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-8 px-4">
      {/* Header */}
      <div className="flex items-center gap-4 mb-8">
        <button
          onClick={onBack}
          className="p-2.5 bg-slate-900 border border-slate-800 rounded-xl hover:bg-slate-800 transition-colors text-slate-400 hover:text-slate-200"
        >
          <ArrowLeft className="w-5 h-5" />
        </button>
        <div>
          <h1 className="text-3xl font-bold text-slate-100">Dateien auswählen</h1>
          <p className="text-sm text-slate-400">Selektieren Sie die Pfade für den Datenumzug.</p>
        </div>
      </div>

      <div className="grid md:grid-cols-3 gap-6">
        {/* File Browser Tree */}
        <div className="md:col-span-2 glass rounded-2xl p-4 min-h-[400px] max-h-[600px] overflow-y-auto shadow-inner border border-slate-800">
          <div className="space-y-1">
            {directoryContents['/']?.map((file) => renderNode(file, 0))}
          </div>
        </div>

        {/* Configurations Sidebar */}
        <div className="space-y-6">
          <div className="glass p-5 rounded-2xl border border-slate-800 space-y-5">
            <h3 className="font-bold text-slate-100 text-lg">Konfiguration</h3>

            {/* Target Path (Disabled/Standard in MVP) */}
            <div>
              <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Zielverzeichnis</label>
              <input
                type="text"
                value={targetDir}
                onChange={(e) => setTargetDir(e.target.value)}
                className="w-full bg-slate-950 border border-slate-800 rounded-xl py-3 px-4 text-sm text-slate-400 focus:outline-none"
                disabled
              />
              <p className="text-[10px] text-slate-500 mt-1">Im MVP wird standardmäßig in das Root-Verzeichnis (/) migriert.</p>
            </div>

            {/* Conflict Strategy */}
            <div>
              <label className="block text-xs font-semibold text-slate-400 uppercase tracking-wider mb-2">Konflikt-Management</label>
              <select
                value={conflictStrategy}
                onChange={(e) => setConflictStrategy(e.target.value)}
                className="w-full bg-slate-950 border border-slate-800 rounded-xl py-3 px-4 text-sm text-slate-200 focus:outline-none focus:border-blue-500/50"
              >
                <option value="SKIP">SKIP (Überspringen)</option>
                <option value="OVERWRITE">OVERWRITE (Überschreiben)</option>
                <option value="RENAME">RENAME (Automatisch umbenennen)</option>
              </select>
              <p className="text-[10px] text-slate-500 mt-1">Verhalten bei Namenskollisionen im Ziel.</p>
            </div>
          </div>

          {error && (
            <div className="p-4 bg-rose-500/10 border border-rose-500/20 rounded-xl text-sm text-rose-300">
              {error}
            </div>
          )}

          {/* Action button */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="w-full flex items-center justify-center gap-2 py-4 bg-gradient-to-r from-emerald-500 to-teal-600 hover:from-emerald-600 hover:to-teal-700 text-slate-100 rounded-xl font-bold shadow-lg hover:shadow-emerald-500/10 disabled:opacity-50 transition-all duration-300"
          >
            {starting ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                Indexierung läuft...
              </>
            ) : (
              <>
                <Play className="w-5 h-5 fill-current" />
                Migration starten
              </>
            )}
          </button>
        </div>
      </div>
    </div>
  );
};
