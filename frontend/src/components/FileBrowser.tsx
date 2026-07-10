import React, { useState } from 'react';
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, Check, Play, ArrowLeft, RefreshCw, AlertTriangle, Calendar, BookOpen, FolderPlus, X } from 'lucide-react';

interface CloudFile {
  path: string;
  name: string;
  size: number;
  is_dir: boolean;
  hash: string;
  last_modified: string;
}

interface MigrationConfig {
  source_url: string;
  source_username: string;
  source_password: string;
  source_refresh_token: string;
  source_token_expires_in: number;
  target_url: string;
  target_username: string;
  target_password: string;
  target_refresh_token: string;
  target_token_expires_in: number;
  source_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3';
  target_provider: 'nextcloud' | 'dropbox' | 'webdav' | 'google' | 'smb' | 's3';
}

interface FileBrowserProps {
  initialFiles: CloudFile[];
  credentials: MigrationConfig;
  apiUrl: string;
  onBack: () => void;
  onStartSuccess: (migrationId: string) => void;
  token: string;
}

// formatSize is defined at module level so it is not recreated on every render.
const formatSize = (bytes: number): string => {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
};

export const FileBrowser: React.FC<FileBrowserProps> = ({
  initialFiles,
  credentials,
  apiUrl,
  onBack,
  onStartSuccess,
  token,
}) => {
  const [activeTab, setActiveTab] = useState<'files' | 'calendars' | 'contacts'>('files');
  const [calendars, setCalendars] = useState<CloudFile[]>([]);
  const [contacts, setContacts] = useState<CloudFile[]>([]);
  const [loadingCalendars, setLoadingCalendars] = useState(false);
  const [loadingContacts, setLoadingContacts] = useState(false);
  const [selectedCalendars, setSelectedCalendars] = useState<Record<string, boolean>>({});
  const [selectedContacts, setSelectedContacts] = useState<Record<string, boolean>>({});

  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>({});
  const [directoryContents, setDirectoryContents] = useState<Record<string, CloudFile[]>>({
    '/': initialFiles,
  });
  const [selectedPaths, setSelectedPaths] = useState<Record<string, boolean>>({});
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [conflictStrategy, setConflictStrategy] = useState('SKIP');
  const [threads, setThreads] = useState(4);
  const [targetDir, setTargetDir] = useState('/');
  const [isTargetBrowserOpen, setIsTargetBrowserOpen] = useState(false);
  const [targetExpandedPaths, setTargetExpandedPaths] = useState<Record<string, boolean>>({});
  const [targetDirectoryContents, setTargetDirectoryContents] = useState<Record<string, CloudFile[]>>({});
  const [targetLoadingPaths, setTargetLoadingPaths] = useState<Record<string, boolean>>({});
  const [targetError, setTargetError] = useState<string | null>(null);
  const [isCreatingFolder, setIsCreatingFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState('');
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchTargetChildren = async (folderPath: string) => {
    if (targetDirectoryContents[folderPath] || targetLoadingPaths[folderPath]) return;

    setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
    setTargetError(null);
    try {
      const response = await fetch(`${apiUrl}/api/migration/target/browse`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          target_url: credentials.target_url,
          target_username: credentials.target_username,
          target_password: credentials.target_password,
          target_provider: credentials.target_provider,
          path: folderPath,
        }),
      });

      if (!response.ok) throw new Error('Fehler beim Laden des Ziel-Verzeichnisses');

      const data = await response.json();
      if (data.success) {
        const foldersOnly = (data.files || []).filter((f: CloudFile) => f.is_dir);
        setTargetDirectoryContents((prev) => ({ ...prev, [folderPath]: foldersOnly }));
      } else {
        setTargetError(data.error || 'Fehler beim Laden des Ziel-Verzeichnisses');
      }
    } catch (err) {
      console.error(err);
      setTargetError(err instanceof Error ? err.message : 'Fehler beim Laden des Ziel-Verzeichnisses');
    } finally {
      setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
    }
  };

  const handleCreateTargetFolder = async (parentPath: string) => {
    if (!newFolderName.trim()) return;

    const fullNewPath = parentPath === '/' 
      ? `/${newFolderName.trim()}` 
      : `${parentPath}/${newFolderName.trim()}`;

    setTargetLoadingPaths((prev) => ({ ...prev, [parentPath]: true }));
    setTargetError(null);
    try {
      const response = await fetch(`${apiUrl}/api/migration/target/mkdir`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          target_url: credentials.target_url,
          target_username: credentials.target_username,
          target_password: credentials.target_password,
          target_provider: credentials.target_provider,
          path: fullNewPath,
        }),
      });

      if (!response.ok) throw new Error('Fehler beim Erstellen des Ordners');

      const data = await response.json();
      if (data.success) {
        setNewFolderName('');
        setIsCreatingFolder(false);
        
        setTargetDirectoryContents((prev) => {
          const next = { ...prev };
          delete next[parentPath];
          return next;
        });
        await fetchTargetChildren(parentPath);
      } else {
        setTargetError(data.error || 'Fehler beim Erstellen des Ordners');
      }
    } catch (err) {
      console.error(err);
      setTargetError(err instanceof Error ? err.message : 'Fehler beim Erstellen des Ordners');
    } finally {
      setTargetLoadingPaths((prev) => ({ ...prev, [parentPath]: false }));
    }
  };

  const openTargetBrowser = () => {
    setIsTargetBrowserOpen(true);
    if (!targetDirectoryContents['/']) {
      fetchTargetChildren('/');
    }
  };

  const fetchCalendars = async (force?: boolean) => {
    if (!force && (calendars.length > 0 || loadingCalendars)) return;
    setLoadingCalendars(true);
    try {
      const response = await fetch(`${apiUrl}/api/migration/browse`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          source_url: credentials.source_url,
          source_username: credentials.source_username,
          source_password: credentials.source_password,
          source_provider: credentials.source_provider,
          resource_type: 'calendars',
        }),
      });
      if (!response.ok) throw new Error('Fehler beim Laden der Kalender');
      const data = await response.json();
      if (data.success) {
        setCalendars(data.items || []);
      }
    } catch (err) {
      console.error(err);
    } finally {
      setLoadingCalendars(false);
    }
  };

  const fetchContacts = async (force?: boolean) => {
    if (!force && (contacts.length > 0 || loadingContacts)) return;
    setLoadingContacts(true);
    try {
      const response = await fetch(`${apiUrl}/api/migration/browse`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          source_url: credentials.source_url,
          source_username: credentials.source_username,
          source_password: credentials.source_password,
          source_provider: credentials.source_provider,
          resource_type: 'contacts',
        }),
      });
      if (!response.ok) throw new Error('Fehler beim Laden der Kontakte');
      const data = await response.json();
      if (data.success) {
        setContacts(data.items || []);
      }
    } catch (err) {
      console.error(err);
    } finally {
      setLoadingContacts(false);
    }
  };

  const handleTabChange = (tab: 'files' | 'calendars' | 'contacts') => {
    setActiveTab(tab);
    if (tab === 'calendars') fetchCalendars();
    if (tab === 'contacts') fetchContacts();
  };

  const fetchChildren = async (folderPath: string) => {
    if (directoryContents[folderPath] || loadingPaths[folderPath]) return;

    setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
    try {
      const response = await fetch(`${apiUrl}/api/migration/browse`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          source_url: credentials.source_url,
          source_username: credentials.source_username,
          source_password: credentials.source_password,
          source_provider: credentials.source_provider,
          resource_type: 'files',
          path: folderPath,
        }),
      });

      if (!response.ok) throw new Error('Fehler beim Laden des Verzeichnisses');

      const data = await response.json();
      if (data.success) {
        setDirectoryContents((prev) => ({ ...prev, [folderPath]: data.items || data.files || [] }));
      }
    } catch (err) {
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
    const calendarsToMigrate = Object.keys(selectedCalendars).filter((p) => selectedCalendars[p]);
    const contactsToMigrate = Object.keys(selectedContacts).filter((p) => selectedContacts[p]);

    if (pathsToMigrate.length === 0 && calendarsToMigrate.length === 0 && contactsToMigrate.length === 0) {
      setError('Bitte wähle mindestens ein Verzeichnis, einen Kalender oder ein Adressbuch aus.');
      return;
    }

    setStarting(true);
    setError(null);

    try {
      const response = await fetch(`${apiUrl}/api/migration/start`, {
        method: 'POST',
        headers: { 
          'Content-Type': 'application/json',
          'Authorization': `Bearer ${token}`
        },
        body: JSON.stringify({
          ...credentials,
          conflict_strategy: conflictStrategy,
          paths: pathsToMigrate,
          calendars: calendarsToMigrate,
          contacts: contactsToMigrate,
          target_dir: targetDir,
          threads: threads,
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
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Verbindungsfehler beim Starten der Übertragung.');
    } finally {
      setStarting(false);
    }
  };


  const renderNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!expandedPaths[file.path];
    const isSelected = !!selectedPaths[file.path];
    const isLoading = !!loadingPaths[file.path];
    const children = directoryContents[file.path] || [];

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        {/* Row */}
        <div
          className={`flex items-center gap-3 py-3.5 px-4 border-b border-slate-100 hover:bg-slate-50 cursor-pointer transition-colors duration-150 ${
            isSelected ? 'bg-slate-50 font-semibold' : ''
          }`}
          style={{ paddingLeft: `${depth * 20 + 16}px` }}
          onClick={() => (file.is_dir ? toggleExpand(file.path) : toggleSelect(file.path))}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <span className="w-5 h-5 flex items-center justify-center text-slate-500 hover:text-portal-navy transition-colors">
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-portal-navy" />
              ) : isExpanded ? (
                <ChevronDown className="w-4.5 h-4.5 stroke-[2]" />
              ) : (
                <ChevronRight className="w-4.5 h-4.5 stroke-[2]" />
              )}
            </span>
          ) : (
            <span className="w-5" />
          )}

          {/* Rounded-md Checkbox */}
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              toggleSelect(file.path);
            }}
            className="focus:outline-none flex items-center justify-center"
          >
            <div className={`w-5.5 h-5.5 border rounded flex items-center justify-center transition-all duration-200 ${
              isSelected 
                ? 'bg-portal-orange text-white border-transparent scale-102 shadow-sm' 
                : 'bg-white border-slate-300 hover:border-slate-400'
            }`}>
              {isSelected && <Check className="w-3.5 h-3.5 text-white stroke-[3.5] animate-pulse" />}
            </div>
          </button>

          {/* Outline-style Icon */}
          <span className="text-portal-navy">
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-5 h-5 text-portal-navy/80" />
              ) : (
                <Folder className="w-5 h-5 text-portal-navy/80" />
              )
            ) : (
              <File className="w-5 h-5 text-slate-400" />
            )}
          </span>

          {/* Name & Size */}
          <span className={`text-[12px] truncate flex-grow leading-none ${
            isSelected ? 'text-portal-navy font-bold' : 'text-slate-800'
          }`}>
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="text-[10px] font-bold text-slate-500 border border-portal-border px-2 py-0.5 bg-slate-50 rounded">
              {formatSize(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="relative">
            {/* Visual connector left track */}
            <div className="absolute left-6.5 top-0 bottom-4.5 border-l border-slate-200"></div>
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

  // Render target directory tree node recursively
  const renderTargetNode = (file: CloudFile, depth: number = 0) => {
    const isExpanded = !!targetExpandedPaths[file.path];
    const isSelected = targetDir === file.path;
    const isLoading = !!targetLoadingPaths[file.path];
    const children = targetDirectoryContents[file.path] || [];

    const toggleTargetExpand = (folderPath: string) => {
      const nextExpanded = !targetExpandedPaths[folderPath];
      setTargetExpandedPaths((prev) => ({ ...prev, [folderPath]: nextExpanded }));
      if (nextExpanded) {
        fetchTargetChildren(folderPath);
      }
    };

    return (
      <div key={file.path} className="select-none font-sans text-xs">
        {/* Row */}
        <div
          className={`flex items-center gap-2.5 py-2 px-3 border-b border-slate-100 hover:bg-slate-50 cursor-pointer transition-colors duration-150 rounded-md ${
            isSelected ? 'bg-white font-bold border border-portal-border text-portal-navy shadow-sm' : ''
          }`}
          style={{ paddingLeft: `${depth * 16 + 12}px` }}
          onClick={() => setTargetDir(file.path)}
        >
          {/* Collapse/Expand Arrow */}
          <span 
            className="w-4 h-4 flex items-center justify-center text-slate-500 hover:text-portal-navy transition-colors cursor-pointer"
            onClick={(e) => {
              e.stopPropagation();
              toggleTargetExpand(file.path);
            }}
          >
            {isLoading ? (
              <RefreshCw className="w-3 h-3 animate-spin text-portal-navy" />
            ) : isExpanded ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
          </span>

          {/* Icon */}
          <span className="text-portal-navy">
            {isExpanded ? (
              <FolderOpen className="w-4 h-4 text-portal-navy/80" />
            ) : (
              <Folder className="w-4 h-4 text-portal-navy/80" />
            )}
          </span>

          {/* Name */}
          <span className={`text-[11.5px] truncate flex-grow ${
            isSelected ? 'text-portal-navy' : 'text-slate-700'
          }`}>
            {file.name}
          </span>

          {/* Select Indicator */}
          {isSelected && (
            <Check className="w-3.5 h-3.5 text-portal-orange stroke-[3]" />
          )}
        </div>

        {/* Children (Recursion) */}
        {isExpanded && (
          <div className="relative">
            <div className="absolute left-[20px] top-0 bottom-3 border-l border-slate-200"></div>
            {children.length > 0 ? (
              children.map((child) => renderTargetNode(child, depth + 1))
            ) : isLoading ? null : (
              <div className="text-[10px] text-slate-400 italic py-2 pl-[42px]">
                Keine Unterverzeichnisse
              </div>
            )}
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="w-full max-w-4xl mx-auto py-2">
      
      {/* Header */}
      <div className="flex items-center gap-4 mb-6">
        <button
          onClick={onBack}
          className="px-4 py-2 border border-portal-border bg-white text-slate-700 text-xs font-semibold rounded-lg shadow-sm hover:bg-slate-50 hover:border-slate-350 hover:text-slate-900 transition-all flex items-center gap-1.5 cursor-pointer"
        >
          <ArrowLeft className="w-4 h-4 stroke-[2.5]" />
          <span>Zurück</span>
        </button>
        <div>
          <h1 className="font-display font-extrabold text-2xl md:text-3xl text-portal-navy tracking-tight">
            Dateien auswählen
          </h1>
          <p className="text-xs font-semibold text-slate-450 mt-1">
            Wähle die Dateibestände für den Übertragungslauf aus.
          </p>
        </div>
      </div>

      <div className="grid md:grid-cols-3 gap-8">
        
        {/* Ledger Browser Tree Card */}
        <div className="md:col-span-2 border border-portal-border bg-white shadow-portal rounded-lg min-h-[400px] max-h-[600px] overflow-y-auto scrollbar-portal p-0">
          {/* Tab Switcher */}
          <div className="flex items-center justify-between border-b border-portal-border bg-slate-50 rounded-t-lg pr-3">
            <div className="flex flex-1">
              <button
                onClick={() => handleTabChange('files')}
                className={`flex-1 py-3.5 text-center font-display text-xs font-bold uppercase tracking-wider transition-colors border-r border-portal-border cursor-pointer focus:outline-none ${
                  activeTab === 'files'
                    ? 'bg-white text-portal-navy border-b-2 border-b-portal-orange font-bold'
                    : 'text-slate-500 hover:bg-slate-100'
                }`}
              >
                Dateien
              </button>
              {(credentials.source_provider === 'nextcloud' || credentials.source_provider === 'google') && (
                <>
                  <button
                    onClick={() => handleTabChange('calendars')}
                    className={`flex-1 py-3.5 text-center font-display text-xs font-bold uppercase tracking-wider transition-colors border-r border-portal-border cursor-pointer focus:outline-none ${
                      activeTab === 'calendars'
                        ? 'bg-white text-portal-navy border-b-2 border-b-portal-orange font-bold'
                        : 'text-slate-500 hover:bg-slate-100'
                    }`}
                  >
                    Kalender ({Object.values(selectedCalendars).filter(Boolean).length})
                  </button>
                  <button
                    onClick={() => handleTabChange('contacts')}
                    className={`flex-1 py-3.5 text-center font-display text-xs font-bold uppercase tracking-wider transition-colors border-r border-portal-border cursor-pointer focus:outline-none ${
                      activeTab === 'contacts'
                        ? 'bg-white text-portal-navy border-b-2 border-b-portal-orange font-bold'
                        : 'text-slate-500 hover:bg-slate-100'
                    }`}
                  >
                    Kontakte ({Object.values(selectedContacts).filter(Boolean).length})
                  </button>
                </>
              )}
            </div>

            {activeTab !== 'files' && (
              <button
                onClick={() => activeTab === 'calendars' ? fetchCalendars(true) : fetchContacts(true)}
                disabled={loadingCalendars || loadingContacts}
                className="p-2 text-slate-500 hover:text-portal-navy hover:bg-slate-100 rounded-full transition-colors duration-150 cursor-pointer disabled:opacity-50"
                title="Aktualisieren"
              >
                <RefreshCw className={`w-4 h-4 ${(loadingCalendars || loadingContacts) ? 'animate-spin' : ''}`} />
              </button>
            )}
          </div>

          <div className="p-1">
            {activeTab === 'files' && (
              directoryContents['/']?.length > 0 ? (
                directoryContents['/'].map((file) => renderNode(file, 0))
              ) : (
                <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-2">
                  <Folder className="w-10 h-10 text-slate-300" />
                  <p className="font-sans text-xs italic text-slate-400">Keine Dateien im Stammverzeichnis gefunden.</p>
                </div>
              )
            )}

            {activeTab === 'calendars' && (
              loadingCalendars ? (
                <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-3">
                  <RefreshCw className="w-8 h-8 text-portal-navy animate-spin" />
                  <p className="font-sans text-xs italic">Lade Kalender...</p>
                </div>
              ) : calendars.length > 0 ? (
                calendars.map((cal) => (
                  <div
                    key={cal.path}
                    className={`flex items-center gap-3 py-3.5 px-4 border-b border-slate-100 hover:bg-slate-50 cursor-pointer transition-colors duration-150 ${
                      selectedCalendars[cal.path] ? 'bg-slate-50 font-semibold' : ''
                    }`}
                    onClick={() => setSelectedCalendars(prev => ({ ...prev, [cal.path]: !prev[cal.path] }))}
                  >
                    <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                      <div className={`w-5.5 h-5.5 border rounded flex items-center justify-center transition-all duration-200 ${
                        selectedCalendars[cal.path] 
                          ? 'bg-portal-orange text-white border-transparent' 
                          : 'bg-white border-slate-300'
                      }`}>
                        {selectedCalendars[cal.path] && <Check className="w-3.5 h-3.5 text-white stroke-[3.5]" />}
                      </div>
                    </button>
                    <Calendar className="w-5 h-5 text-portal-navy" />
                    <span className="text-[12px] text-slate-850 flex-grow">{cal.name}</span>
                  </div>
                ))
              ) : (
                <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-2">
                  <Calendar className="w-10 h-10 text-slate-300" />
                  <p className="font-sans text-xs italic">Keine Kalender gefunden.</p>
                </div>
              )
            )}

            {activeTab === 'contacts' && (
              loadingContacts ? (
                <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-3">
                  <RefreshCw className="w-8 h-8 text-portal-navy animate-spin" />
                  <p className="font-sans text-xs italic">Lade Adressbücher...</p>
                </div>
              ) : contacts.length > 0 ? (
                contacts.map((addr) => (
                  <div
                    key={addr.path}
                    className={`flex items-center gap-3 py-3.5 px-4 border-b border-slate-100 hover:bg-slate-50 cursor-pointer transition-colors duration-150 ${
                      selectedContacts[addr.path] ? 'bg-slate-50 font-semibold' : ''
                    }`}
                    onClick={() => setSelectedContacts(prev => ({ ...prev, [addr.path]: !prev[addr.path] }))}
                  >
                    <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                      <div className={`w-5.5 h-5.5 border rounded flex items-center justify-center transition-all duration-200 ${
                        selectedContacts[addr.path] 
                          ? 'bg-portal-orange text-white border-transparent' 
                          : 'bg-white border-slate-300'
                      }`}>
                        {selectedContacts[addr.path] && <Check className="w-3.5 h-3.5 text-white stroke-[3.5]" />}
                      </div>
                    </button>
                    <BookOpen className="w-5 h-5 text-portal-navy" />
                    <span className="text-[12px] text-slate-850 flex-grow">{addr.name}</span>
                  </div>
                ))
              ) : (
                <div className="flex flex-col items-center justify-center py-20 text-slate-400 gap-2">
                  <BookOpen className="w-10 h-10 text-slate-300" />
                  <p className="font-sans text-xs italic">Keine Adressbücher gefunden.</p>
                </div>
              )
            )}
          </div>
        </div>

        {/* Configurations Sidebar */}
        <div className="space-y-6">
          <div className="bg-white border border-portal-border p-6 shadow-portal rounded-lg space-y-6">
            
            <div className="flex items-center gap-2 border-b border-portal-border pb-3 mb-1">
              <h3 className="font-display font-bold text-lg text-portal-navy tracking-tight">Konfiguration</h3>
            </div>

            {/* Target Path */}
            <div className="space-y-2 text-xs">
              <label className="block font-display font-bold text-slate-500 uppercase tracking-wider">Ziel-Stammverzeichnis</label>
              <div className="flex gap-2">
                <div className="relative flex-grow">
                  <input
                    type="text"
                    value={targetDir}
                    className="w-full bg-slate-50 border border-portal-border rounded-lg py-2.5 px-3.5 text-slate-750 font-mono text-xs cursor-default"
                    readOnly
                  />
                </div>
                <button
                  type="button"
                  onClick={openTargetBrowser}
                  className="px-3 py-2 bg-portal-navy text-white text-xs font-semibold rounded-lg shadow-sm hover:bg-portal-navy/90 transition-all flex items-center gap-1.5 cursor-pointer"
                >
                  <FolderOpen className="w-4 h-4" />
                  <span>Durchsuchen</span>
                </button>
              </div>
              <p className="text-[10px] text-slate-550 leading-relaxed">
                Kopiert Bestände in den ausgewählten Ordner auf der Zielinstanz.
              </p>
            </div>

            {/* Conflict Strategy block selector */}
            <div className="space-y-3 text-xs">
              <label className="block font-display font-bold text-slate-500 uppercase tracking-wider">Kollisions-Behandlung</label>
              <div className="space-y-2.5">
                {/* SKIP card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('SKIP')}
                  className={`w-full text-left p-3.5 rounded-lg border transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'SKIP'
                      ? 'bg-slate-50 border-portal-navy text-portal-navy font-bold'
                      : 'bg-white text-slate-700 border-portal-border hover:bg-slate-50'
                  }`}
                >
                  <div className="flex items-center justify-between text-xs">
                    <span className="font-display font-bold">Überspringen</span>
                    {conflictStrategy === 'SKIP' && <Check className="w-4 h-4 text-portal-orange stroke-[3]" />}
                  </div>
                  <p className={`text-[10.5px] mt-1.5 leading-normal ${conflictStrategy === 'SKIP' ? 'text-slate-600' : 'text-slate-450'}`}>
                    Bereits existierende Dateien werden im Ziel nicht überschrieben.
                  </p>
                </button>

                {/* OVERWRITE card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('OVERWRITE')}
                  className={`w-full text-left p-3.5 rounded-lg border transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'OVERWRITE'
                      ? 'bg-slate-50 border-portal-navy text-portal-navy font-bold'
                      : 'bg-white text-slate-700 border-portal-border hover:bg-slate-50'
                  }`}
                >
                  <div className="flex items-center justify-between text-xs">
                    <span className="font-display font-bold">Überschreiben</span>
                    {conflictStrategy === 'OVERWRITE' && <Check className="w-4 h-4 text-portal-orange stroke-[3]" />}
                  </div>
                  <p className={`text-[10.5px] mt-1.5 leading-normal ${conflictStrategy === 'OVERWRITE' ? 'text-slate-600' : 'text-slate-450'}`}>
                    Ersetzt doppelte Dateinamen im Ziel bedingungslos.
                  </p>
                </button>

                {/* RENAME card */}
                <button
                  type="button"
                  onClick={() => setConflictStrategy('RENAME')}
                  className={`w-full text-left p-3.5 rounded-lg border transition-all duration-150 cursor-pointer ${
                    conflictStrategy === 'RENAME'
                      ? 'bg-slate-50 border-portal-navy text-portal-navy font-bold'
                      : 'bg-white text-slate-700 border-portal-border hover:bg-slate-50'
                  }`}
                >
                  <div className="flex items-center justify-between text-xs">
                    <span className="font-display font-bold">Automatisch umbenennen</span>
                    {conflictStrategy === 'RENAME' && <Check className="w-4 h-4 text-portal-orange stroke-[3]" />}
                  </div>
                  <p className={`text-[10.5px] mt-1.5 leading-normal ${conflictStrategy === 'RENAME' ? 'text-slate-600' : 'text-slate-450'}`}>
                    Fügt Suffixe hinzu (z. B. datei(1).pdf).
                  </p>
                </button>
              </div>
            </div>

            {/* Thread count selector */}
            <div className="space-y-3 text-xs pt-4 border-t border-portal-border">
              <label className="block font-display font-bold text-slate-500 uppercase tracking-wider">Parallele Übertragungen (Threads)</label>
              <div className="flex items-center gap-4">
                <input
                  type="range"
                  min="1"
                  max="16"
                  value={threads}
                  onChange={(e) => setThreads(parseInt(e.target.value, 10))}
                  className="flex-grow accent-portal-navy cursor-pointer"
                />
                <span className="font-mono text-sm font-bold text-portal-navy bg-slate-100 px-2.5 py-1 rounded-md min-w-[32px] text-center">
                  {threads}
                </span>
              </div>
              <p className="text-[10px] text-slate-550 leading-relaxed">
                Höhere Werte beschleunigen die Migration, belasten aber Quell- und Zielserver.
              </p>
            </div>
          </div>

          {error && (
            <div className="p-4 bg-rose-50 border border-rose-200 rounded-lg text-xs font-semibold text-rose-700 leading-normal flex gap-2">
              <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
              <span>{error}</span>
            </div>
          )}

          {/* Action submit button */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="w-full flex items-center justify-center gap-2 py-4 bg-portal-orange text-white rounded-lg font-display text-base font-bold shadow-sm hover:bg-portal-orange-hover hover:scale-101 transition-all duration-200 cursor-pointer disabled:opacity-50"
          >
            {starting ? (
              <>
                <RefreshCw className="w-5 h-5 animate-spin" />
                <span>Indexierung läuft...</span>
              </>
            ) : (
              <>
                <Play className="w-5 h-5 fill-current stroke-[2.5]" />
                <span>Transfer starten</span>
              </>
            )}
          </button>
        </div>
      </div>

      {/* Target Directory Browser Modal */}
      {isTargetBrowserOpen && (
        <div className="fixed inset-0 bg-slate-900/60 backdrop-blur-sm z-50 flex items-center justify-center p-4">
          <div className="bg-white border border-portal-border rounded-xl shadow-2xl max-w-lg w-full max-h-[85vh] flex flex-col overflow-hidden animate-in fade-in zoom-in duration-200">
            {/* Modal Header */}
            <div className="p-5 border-b border-portal-border flex items-center justify-between bg-slate-50">
              <div>
                <h3 className="font-display font-bold text-lg text-portal-navy tracking-tight">
                  Ziel-Verzeichnis auswählen
                </h3>
                <p className="text-[11px] text-slate-500 mt-0.5">
                  Wähle ein Verzeichnis auf der Zielinstanz aus.
                </p>
              </div>
              <button
                type="button"
                onClick={() => {
                  setIsTargetBrowserOpen(false);
                  setIsCreatingFolder(false);
                  setNewFolderName('');
                }}
                className="p-1.5 text-slate-400 hover:text-slate-700 hover:bg-slate-200 rounded-lg transition-colors cursor-pointer"
              >
                <X className="w-5 h-5" />
              </button>
            </div>

            {/* Modal Content - Directory Tree */}
            <div className="p-4 flex-grow overflow-y-auto min-h-[300px]">
              {targetError && (
                <div className="mb-4 p-3 bg-rose-50 border border-rose-200 rounded-lg text-xs text-rose-700 flex gap-2">
                  <AlertTriangle className="w-4 h-4 shrink-0 text-rose-600" />
                  <span>{targetError}</span>
                </div>
              )}

              <div className="border border-portal-border rounded-lg bg-slate-50/50 p-2 overflow-x-auto max-h-[400px]">
                {/* Root Directory Node */}
                <div className="select-none font-sans text-xs">
                  <div
                    className={`flex items-center gap-2.5 py-2 px-3 border-b border-slate-100 hover:bg-slate-50 cursor-pointer transition-colors duration-150 rounded-md ${
                      targetDir === '/' ? 'bg-white font-bold border border-portal-border text-portal-navy shadow-sm' : ''
                    }`}
                    onClick={() => setTargetDir('/')}
                  >
                    <span
                      className="w-4 h-4 flex items-center justify-center text-slate-500 hover:text-portal-navy transition-colors cursor-pointer"
                      onClick={(e) => {
                        e.stopPropagation();
                        const isExpanded = !!targetExpandedPaths['/'];
                        setTargetExpandedPaths((prev) => ({ ...prev, '/': !isExpanded }));
                        if (!isExpanded) fetchTargetChildren('/');
                      }}
                    >
                      {targetLoadingPaths['/'] ? (
                        <RefreshCw className="w-3 h-3 animate-spin text-portal-navy" />
                      ) : targetExpandedPaths['/'] ? (
                        <ChevronDown className="w-3.5 h-3.5" />
                      ) : (
                        <ChevronRight className="w-3.5 h-3.5" />
                      )}
                    </span>
                    <span className="text-portal-navy">
                      {targetExpandedPaths['/'] ? (
                        <FolderOpen className="w-4 h-4 text-portal-navy/80" />
                      ) : (
                        <Folder className="w-4 h-4 text-portal-navy/80" />
                      )}
                    </span>
                    <span className={`text-[11.5px] truncate flex-grow ${
                      targetDir === '/' ? 'text-portal-navy' : 'text-slate-700'
                    }`}>
                      Hauptverzeichnis (/)
                    </span>
                    {targetDir === '/' && (
                      <Check className="w-3.5 h-3.5 text-portal-orange stroke-[3]" />
                    )}
                  </div>

                  {/* Root Children */}
                  {targetExpandedPaths['/'] && (
                    <div className="relative">
                      {/* Tree visual line */}
                      <div className="absolute left-[20px] top-0 bottom-3 border-l border-slate-200"></div>
                      
                      {targetDirectoryContents['/'] && targetDirectoryContents['/'].length > 0 ? (
                        targetDirectoryContents['/'].map((child) => renderTargetNode(child, 1))
                      ) : targetLoadingPaths['/'] ? null : (
                        <div className="text-[10px] text-slate-400 italic py-2 pl-[42px]">
                          Keine Unterverzeichnisse
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </div>
            </div>

            {/* Folder creation form */}
            {isCreatingFolder && (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  handleCreateTargetFolder(targetDir);
                }}
                className="p-4 border-t border-portal-border bg-slate-50 flex items-center gap-3"
              >
                <div className="flex-grow">
                  <label className="block text-[9.5px] text-slate-500 uppercase font-bold tracking-wider mb-1">
                    Neuer Ordnername in {targetDir}
                  </label>
                  <input
                    type="text"
                    value={newFolderName}
                    onChange={(e) => setNewFolderName(e.target.value)}
                    placeholder="z.B. Archiv"
                    className="w-full bg-white border border-portal-border rounded-lg py-2 px-3 text-xs text-slate-800 focus:outline-none focus:border-portal-navy"
                    autoFocus
                  />
                </div>
                <div className="flex items-end gap-1.5 pt-5">
                  <button
                    type="submit"
                    disabled={!newFolderName.trim()}
                    className="px-3.5 py-2 bg-portal-orange text-white text-xs font-semibold rounded-lg shadow-sm hover:bg-portal-orange-hover hover:scale-101 transition-all disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
                  >
                    Erstellen
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setIsCreatingFolder(false);
                      setNewFolderName('');
                    }}
                    className="px-3.5 py-2 border border-portal-border bg-white text-slate-700 text-xs font-semibold rounded-lg shadow-sm hover:bg-slate-50 transition-all cursor-pointer"
                  >
                    Abbrechen
                  </button>
                </div>
              </form>
            )}

            {/* Modal Footer */}
            <div className="p-4 border-t border-portal-border flex items-center justify-between bg-slate-50">
              <div className="text-left max-w-[200px] md:max-w-[240px]">
                <p className="text-[10px] text-slate-400 uppercase font-bold tracking-wider">Auswahl:</p>
                <p className="font-mono text-[11px] text-slate-800 truncate font-semibold">{targetDir}</p>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setIsCreatingFolder(true)}
                  className="px-3 py-2 border border-portal-border bg-white text-slate-700 text-xs font-semibold rounded-lg shadow-sm hover:bg-slate-50 hover:text-portal-navy transition-all flex items-center gap-1.5 cursor-pointer"
                  title="Neuen Ordner in diesem Verzeichnis erstellen"
                >
                  <FolderPlus className="w-4 h-4 text-portal-navy" />
                  <span>Neuer Ordner</span>
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setIsTargetBrowserOpen(false);
                    setIsCreatingFolder(false);
                    setNewFolderName('');
                  }}
                  className="px-4 py-2 bg-portal-orange text-white text-xs font-bold rounded-lg shadow-sm hover:bg-portal-orange-hover hover:scale-101 transition-all cursor-pointer"
                >
                  Auswählen
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
