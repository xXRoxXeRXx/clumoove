import React, { useState, useMemo, useEffect, useCallback } from 'react';
import { Folder, FolderOpen, File, ChevronRight, ChevronDown, Check, Play, ArrowLeft, RefreshCw, AlertTriangle, Calendar, BookOpen, FolderPlus, X, Info } from 'lucide-react';
import type { CloudFile, MigrationConfig } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { SelectedPathsViewer } from './SelectedPathsViewer';


interface FileBrowserProps {
  initialFiles: CloudFile[];
  credentials: MigrationConfig;
  apiUrl: string;
  onBack: () => void;
  onStartSuccess: (id: string, isSync?: boolean) => void;
  token: string;
}

// toLocalInputValue formats a Date as a local-time datetime-local string
// (YYYY-MM-DDTHH:MM) without UTC conversion. datetime-local inputs expect the
// value in the user's local timezone, so using toISOString() (which is UTC)
// would shift the minimum by the timezone offset.
const toLocalInputValue = (date: Date): string => {
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    `T${pad(date.getHours())}:${pad(date.getMinutes())}`
  );
};

// sortEntries returns a new array with folders first, then files, each group
// sorted alphabetically (case-insensitive). Used for files, calendars and
// contacts so the selection lists are consistently ordered.
const sortEntries = (entries: CloudFile[]): CloudFile[] => {
  return [...entries].sort((a, b) => {
    if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
    return a.name.localeCompare(b.name, 'de', { sensitivity: 'base' });
  });
};

export const FileBrowser: React.FC<FileBrowserProps> = ({
  initialFiles,
  credentials,
  apiUrl,
  onBack,
  onStartSuccess,
  token,
}) => {
  const { t } = useTranslation();
  const { formatBytes } = useFormat();
  const translateApiError = useApiError();

  const [activeTab, setActiveTab] = useState<'files' | 'calendars' | 'contacts'>('files');
  const [calendars, setCalendars] = useState<CloudFile[]>([]);
  const [contacts, setContacts] = useState<CloudFile[]>([]);
  const [loadingCalendars, setLoadingCalendars] = useState(false);
  const [loadingContacts, setLoadingContacts] = useState(false);
  const [selectedCalendars, setSelectedCalendars] = useState<Record<string, boolean>>({});
  const [selectedContacts, setSelectedContacts] = useState<Record<string, boolean>>({});

  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>({});
  const [directoryContents, setDirectoryContents] = useState<Record<string, CloudFile[]>>(() => ({
    '/': sortEntries(initialFiles),
  }));
  // All files/folders are selected by default. Pre-populate the top-level
  // entries so the selection checkboxes render checked on first paint.
  const [selectedPaths, setSelectedPaths] = useState<Record<string, boolean>>(() =>
    initialFiles.reduce((acc, f) => {
      acc[f.path] = true;
      return acc;
    }, {} as Record<string, boolean>)
  );
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [conflictStrategy, setConflictStrategy] = useState('SKIP');
  const [threads, setThreads] = useState<number>(4);
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

  // Sync Job options
  const [jobType, setJobType] = useState<'migration' | 'sync'>('migration');
  const [direction, setDirection] = useState<'one_way' | 'two_way'>('one_way');
  const [intervalMinutes, setIntervalMinutes] = useState<number>(15);
  const [deletePropagation, setDeletePropagation] = useState<boolean>(false);



  
  // Scheduling state
  const [enableScheduling, setEnableScheduling] = useState(false);
  const [scheduledTime, setScheduledTime] = useState('');
  const [bandwidthLimit, setBandwidthLimit] = useState(0);

  const pathsToMigrate = useMemo(
    () => Object.keys(selectedPaths).filter((p) => selectedPaths[p]),
    [selectedPaths]
  );



  // Minimum selectable start time: now + 1 minute, formatted in the user's
  // local timezone (datetime-local inputs expect local time, not UTC).
  // Computed once via a useState lazy initializer to keep render pure
  // (no Date.now() called during render).
  const [minScheduledTime] = useState(() =>
    toLocalInputValue(new Date(Date.now() + 60000))
  );

  const fetchTargetChildren = async (folderPath: string, depth: number = 0) => {
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
          target_profile_id: credentials.target_profile_id,
          path: folderPath,
        }),
      });

      if (!response.ok) {
        const b = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.loadTarget'));
      }

      const data = await response.json();
      if (data.success) {
        const foldersOnly = sortEntries((data.files || []).filter((f: CloudFile) => f.is_dir));
        setTargetDirectoryContents((prev) => ({ ...prev, [folderPath]: foldersOnly }));
        // Only the first folder level is loaded directly. Deeper levels are
        // loaded on demand when the user expands a folder.
        if (depth < 1) {
          setTargetExpandedPaths((prev) => ({ ...prev, [folderPath]: true }));
        }
      } else {
        setTargetError(data.error_code ? translateApiError(data.error_code) : t('fileBrowser.errors.loadTarget'));
      }
    } catch (err) {
      console.error(err);
      setTargetError(err instanceof Error ? err.message : t('fileBrowser.errors.loadTarget'));
    } finally {
      setTargetLoadingPaths((prev) => ({ ...prev, [folderPath]: false }));
    }
  };

  const handleCreateTargetFolder = async (parentPath: string) => {
    const trimmedName = newFolderName.trim();
    if (!trimmedName) return;

    // Client-side defense-in-depth against path traversal. Strip path
    // separators and any ".." segments; the backend remains authoritative.
    const safeName = trimmedName
      .split('/').join('')
      .split('\\').join('')
      .split('..').join('')
      .trim();
    if (!safeName) {
      setTargetError(t('fileBrowser.errors.invalidFolderName'));
      return;
    }

    const fullNewPath = parentPath === '/'
      ? `/${safeName}`
      : `${parentPath}/${safeName}`;

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
          target_profile_id: credentials.target_profile_id,
          path: fullNewPath,
        }),
      });

      if (!response.ok) {
        const b = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.createFolder'));
      }

      const data = await response.json();
      if (data.success) {
        setNewFolderName('');
        setIsCreatingFolder(false);

        setTargetDir(fullNewPath);
        setTargetExpandedPaths((prev) => ({ ...prev, [parentPath]: true }));

        setTargetDirectoryContents((prev) => {
          const next = { ...prev };
          delete next[parentPath];
          return next;
        });
        await fetchTargetChildren(parentPath);
      } else {
        setTargetError(data.error_code ? translateApiError(data.error_code) : t('fileBrowser.errors.createFolder'));
      }
    } catch (err) {
      console.error(err);
      setTargetError(err instanceof Error ? err.message : t('fileBrowser.errors.createFolder'));
    } finally {
      setTargetLoadingPaths((prev) => ({ ...prev, [parentPath]: false }));
    }
  };

  const openTargetBrowser = () => {
    setIsTargetBrowserOpen(true);
    setTargetExpandedPaths((prev) => ({ ...prev, '/': true }));
    fetchTargetChildren('/');
  };

  const fetchCalendars = useCallback(async (force?: boolean) => {
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
          source_profile_id: credentials.source_profile_id,
          resource_type: 'calendars',
        }),
      });
      if (!response.ok) {
        const b = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.loadCalendars'));
      }
      const data = await response.json();
      if (data.success) {
        const items = sortEntries(data.items || []);
        setCalendars(items);
        setSelectedCalendars((prev) => {
          const next = { ...prev };
          for (const c of items) {
            if (next[c.path] === undefined) next[c.path] = true;
          }
          return next;
        });
      }
    } catch (err) {
      console.error(err);
    } finally {
      setLoadingCalendars(false);
    }
  }, [apiUrl, credentials, calendars.length, loadingCalendars, t, token, translateApiError]);

  const fetchContacts = useCallback(async (force?: boolean) => {
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
          source_profile_id: credentials.source_profile_id,
          resource_type: 'contacts',
        }),
      });
      if (!response.ok) {
        const b = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.loadContacts'));
      }
      const data = await response.json();
      if (data.success) {
        const items = sortEntries(data.items || []);
        setContacts(items);
        setSelectedContacts((prev) => {
          const next = { ...prev };
          for (const c of items) {
            if (next[c.path] === undefined) next[c.path] = true;
          }
          return next;
        });
      }
    } catch (err) {
      console.error(err);
    } finally {
      setLoadingContacts(false);
    }
  }, [apiUrl, credentials, contacts.length, loadingContacts, t, token, translateApiError]);

  useEffect(() => {
    if (credentials.source_provider === 'nextcloud' || credentials.source_provider === 'google') {
      const timer = setTimeout(() => {
        void fetchCalendars();
        void fetchContacts();
      }, 0);
      return () => clearTimeout(timer);
    }
  }, [credentials.source_provider, fetchCalendars, fetchContacts]);

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
          source_profile_id: credentials.source_profile_id,
          resource_type: 'files',
          path: folderPath,
        }),
      });

      if (!response.ok) {
        const b = await response.json().catch(() => ({} as { error_code?: string }));
        throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.loadDir'));
      }

      const data = await response.json();
      if (data.success) {
        const items = sortEntries(data.items || data.files || []);
        setDirectoryContents((prev) => ({ ...prev, [folderPath]: items }));
        // Newly loaded children are selected by default.
        // Newly loaded children are selected by default, but only if the
        // user has not explicitly interacted with them yet (so a "Deselect
        // all" followed by expanding a folder keeps the children
        // unselected).
        setSelectedPaths((prev) => {
          const next = { ...prev };
          for (const child of items) {
            if (next[child.path] === undefined) next[child.path] = true;
          }
          return next;
        });
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

  const deselectAll = () => {
    setSelectedPaths({});
    setSelectedCalendars({});
    setSelectedContacts({});
  };

  const handleStartMigration = async () => {
    const pathsToMigrate = Object.keys(selectedPaths).filter((p) => selectedPaths[p]);
    const calendarsToMigrate = Object.keys(selectedCalendars).filter((p) => selectedCalendars[p]);
    const contactsToMigrate = Object.keys(selectedContacts).filter((p) => selectedContacts[p]);

    if (pathsToMigrate.length === 0 && calendarsToMigrate.length === 0 && contactsToMigrate.length === 0) {
      setError(t('fileBrowser.errors.selectOne'));
      return;
    }

    if (jobType === 'sync') {
      if (pathsToMigrate.length === 0) {
        setError(t('fileBrowser.errors.selectOne'));
        return;
      }
    } else {
      // Validate scheduled time if scheduling is enabled
      if (enableScheduling && scheduledTime) {
        const scheduledDate = new Date(scheduledTime);
        if (scheduledDate <= new Date()) {
          setError(t('fileBrowser.errors.futureTime'));
          return;
        }
      }
    }

    setStarting(true);
    setError(null);

    try {
      if (jobType === 'sync') {
        const response = await fetch(`${apiUrl}/api/sync`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
          },
          body: JSON.stringify({
            source_profile_id: credentials.source_profile_id,
            target_profile_id: credentials.target_profile_id,
            source_url: credentials.source_url,
            source_username: credentials.source_username,
            source_password: credentials.source_password,
            source_refresh_token: credentials.source_refresh_token,
            target_url: credentials.target_url,
            target_username: credentials.target_username,
            target_password: credentials.target_password,
            target_refresh_token: credentials.target_refresh_token,
            source_provider: credentials.source_provider,
            target_provider: credentials.target_provider,
            direction: direction,
            conflict_strategy: conflictStrategy,
            delete_propagation: deletePropagation,
            interval_minutes: intervalMinutes,
            threads: threads,
            target_dir: targetDir,
            selected_paths: pathsToMigrate,
          }),
        });

        if (!response.ok) {
          const b = await response.json().catch(() => ({} as { error_code?: string }));
          throw new Error(b.error_code ? translateApiError(b.error_code) : t('sync.createFailed'));
        }

        const data = await response.json();
        if (data.id) {
          // Trigger first pass immediately
          await fetch(`${apiUrl}/api/sync/${data.id}/start`, {
            method: 'POST',
            headers: { 'Authorization': `Bearer ${token}` },
          });
          onStartSuccess(data.id, true);
        } else {
          setError(t('sync.createFailed'));
        }
      } else {
        const requestBody: Record<string, unknown> = {
          ...credentials,
          conflict_strategy: conflictStrategy,
          paths: pathsToMigrate,
          calendars: calendarsToMigrate,
          contacts: contactsToMigrate,
          target_dir: targetDir,
          threads: threads,
          bandwidth_limit_mbps: bandwidthLimit,
        };

        // Add scheduled_time if scheduling is enabled
        if (enableScheduling && scheduledTime) {
          requestBody.scheduled_time = new Date(scheduledTime).toISOString();
        }

        const response = await fetch(`${apiUrl}/api/migration/start`, {
          method: 'POST',
          headers: { 
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${token}`
          },
          body: JSON.stringify(requestBody),
        });

        if (!response.ok) {
          const b = await response.json().catch(() => ({} as { error_code?: string }));
          throw new Error(b.error_code ? translateApiError(b.error_code) : t('fileBrowser.errors.startFailed'));
        }

        const data = await response.json();
        if (data.success && data.migration_id) {
          onStartSuccess(data.migration_id, false);
        } else {
          setError(data.error_code ? translateApiError(data.error_code) : t('fileBrowser.errors.startError'));
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('fileBrowser.errors.networkError'));
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
          className={`flex items-center gap-3 py-3.5 px-4 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] cursor-pointer transition-colors duration-150 ${
            isSelected ? 'bg-[var(--color-bg-tertiary)] font-semibold' : ''
          }`}
          style={{ paddingLeft: `${depth * 20 + 16}px` }}
          onClick={() => (file.is_dir ? toggleExpand(file.path) : toggleSelect(file.path))}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <span className="w-5 h-5 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors">
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-[var(--color-portal-navy-themed)]" />
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
                ? 'bg-portal-orange text-[var(--color-text-inverse)] border-transparent scale-102 shadow-sm' 
                : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:border-[var(--color-border)]'
            }`}>
              {isSelected && <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5] animate-pulse" />}
            </div>
          </button>

          {/* Outline-style Icon */}
          <span className="text-[var(--color-portal-navy-themed)]">
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-5 h-5 text-[var(--color-portal-navy-themed)]/80" />
              ) : (
                <Folder className="w-5 h-5 text-[var(--color-portal-navy-themed)]/80" />
              )
            ) : (
              <File className="w-5 h-5 text-[var(--color-text-muted)]" />
            )}
          </span>

          {/* Name & Size */}
          <span className={`text-[12px] truncate flex-grow leading-none ${
            isSelected ? 'text-[var(--color-portal-navy-themed)] font-bold' : 'text-[var(--color-text-primary)]'
          }`}>
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="text-[10px] font-bold text-[var(--color-text-muted)] border border-portal-border px-2 py-0.5 bg-[var(--color-bg-tertiary)] rounded">
              {formatBytes(file.size)}
            </span>
          )}
        </div>

        {/* Children (Recursion) */}
        {file.is_dir && isExpanded && children.length > 0 && (
          <div className="relative">
            {/* Visual connector left track */}
            <div className="absolute left-6.5 top-0 bottom-4.5 border-l border-[var(--color-border)]"></div>
            {children.map((child) => renderNode(child, depth + 1))}
          </div>
        )}

        {file.is_dir && isExpanded && children.length === 0 && !isLoading && (
          <div
            className="text-[10px] text-[var(--color-text-muted)] italic py-2.5 pl-14"
          >
            {t('fileBrowser.emptyDir')}
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
          className={`flex items-center gap-2.5 py-2 px-3 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] cursor-pointer transition-colors duration-150 rounded-md ${
            isSelected ? 'bg-[var(--color-bg-secondary)] font-bold border border-portal-border text-[var(--color-portal-navy-themed)] shadow-sm' : ''
          }`}
          style={{ paddingLeft: `${depth * 16 + 12}px` }}
          onClick={() => setTargetDir(file.path)}
        >
          {/* Collapse/Expand Arrow */}
          <span 
            className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer"
            onClick={(e) => {
              e.stopPropagation();
              toggleTargetExpand(file.path);
            }}
          >
            {isLoading ? (
              <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-portal-navy-themed)]" />
            ) : isExpanded ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
          </span>

          {/* Icon */}
          <span className="text-[var(--color-portal-navy-themed)]">
            {isExpanded ? (
              <FolderOpen className="w-4 h-4 text-[var(--color-portal-navy-themed)]/80" />
            ) : (
              <Folder className="w-4 h-4 text-[var(--color-portal-navy-themed)]/80" />
            )}
          </span>

          {/* Name */}
          <span className={`text-[11.5px] truncate flex-grow ${
            isSelected ? 'text-[var(--color-portal-navy-themed)]' : 'text-[var(--color-text-secondary)]'
          }`}>
            {file.name}
          </span>

          {/* Select Indicator */}
          {isSelected && (
            <Check className="w-3.5 h-3.5 text-[var(--color-portal-orange-themed)] stroke-[3]" />
          )}
        </div>

        {/* Children (Recursion) */}
        {isExpanded && (
          <div className="relative">
            <div className="absolute left-[20px] top-0 bottom-3 border-l border-[var(--color-border)]"></div>
            {children.length > 0 ? (
              children.map((child) => renderTargetNode(child, depth + 1))
            ) : isLoading ? null : (
              <div className="text-[10px] text-[var(--color-text-muted)] italic py-2 pl-[42px]">
                {t('fileBrowser.noSubdirs')}
              </div>
            )}
          </div>
        )}
      </div>
    );
  };
  return (
    <div className="w-full max-w-5xl mx-auto py-2 animate-fade-in text-left space-y-6">
      
      {/* Wizard Step Progress Banner */}
      <div className="flex items-center justify-between p-4 rounded-2xl bg-[var(--color-bg-secondary)] border border-[var(--color-border)] shadow-xs">
        <div className="flex items-center gap-3">
          <span className="flex items-center justify-center w-8 h-8 rounded-full bg-portal-orange text-white font-mono font-bold text-xs shadow-xs">
            3
          </span>
          <div className="flex flex-col text-left">
            <span className="font-display font-extrabold text-sm text-[var(--color-portal-navy-themed)]">
              {t('fileBrowser.title')}
            </span>
            <span className="text-[10px] font-mono text-[var(--color-text-muted)]">
              Schritt 3 von 3: Daten auswählen & Einstellungen festlegen
            </span>
          </div>
        </div>

        <div className="flex items-center gap-1.5 text-xs font-mono font-bold text-portal-orange bg-portal-orange/10 px-3 py-1 rounded-full border border-portal-orange/20">
          <Folder className="w-3.5 h-3.5" />
          <span>Datenauswahl</span>
        </div>
      </div>

      {/* Top Header */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 border-b border-[var(--color-border-light)] pb-5">
        <div className="flex items-center gap-4">
          <button
            onClick={onBack}
            className="px-4 py-2 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-full hover:border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] transition-all font-mono font-bold text-[11px] cursor-pointer text-[var(--color-text-secondary)] hover:text-[var(--color-portal-navy-themed)] shadow-xs hover:shadow-sm shrink-0"
          >
            <ArrowLeft className="w-3.5 h-3.5 stroke-[2.5] inline-block mr-1.5 align-text-bottom" />
            <span>{t('common.back')}</span>
          </button>
          <div>
            <div className="flex items-center gap-3 flex-wrap">
              <h1 className="font-display font-extrabold text-2xl md:text-3xl text-[var(--color-portal-navy-themed)] tracking-tight">
                {t('fileBrowser.title')}
              </h1>
              <span className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-[10px] font-mono font-bold uppercase tracking-wider bg-portal-orange/10 text-portal-orange border border-portal-orange/30">
                {jobType === 'sync' ? t('sync.modeSync') : t('sync.modeMigration')}
              </span>
            </div>
            <p className="text-[10px] font-mono text-[var(--color-text-muted)] mt-1 uppercase tracking-wider">
              {t('fileBrowser.subtitle')}
            </p>
          </div>
        </div>
      </div>

      {/* Source & Target Connection Cards Grid */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {/* Source Card */}
        <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4 shadow-2xs">
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
            <div className="flex items-center gap-2">
              <Folder className="w-4 h-4 text-portal-orange shrink-0" />
              <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                {t('migrations.source')}
              </h3>
            </div>
            <span className="text-[10px] font-mono font-bold px-2.5 py-0.5 rounded-md bg-[var(--color-bg-tertiary)] text-[var(--color-portal-navy-themed)]">
              {pathsToMigrate.length} {pathsToMigrate.length === 1 ? 'Element' : 'Elemente'}
            </span>
          </div>
          
          <div className="space-y-2">
            <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize flex items-center gap-2">
              <span>{credentials.source_provider || 'nextcloud'}</span>
            </div>
            <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
              {credentials.source_url || t('migrations.oauth')}
            </div>
            <SelectedPathsViewer paths={pathsToMigrate} maxVisible={3} />
          </div>
        </div>

        {/* Target Card */}
        <div className="p-5 rounded-2xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] space-y-4 shadow-2xs">
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
            <div className="flex items-center gap-2">
              <Folder className="w-4 h-4 text-emerald-600 shrink-0" />
              <h3 className="font-display font-bold text-xs text-[var(--color-portal-navy-themed)] uppercase tracking-wider font-mono">
                {t('migrations.target')}
              </h3>
            </div>
            <button
              type="button"
              onClick={openTargetBrowser}
              className="text-[10px] font-mono font-bold text-portal-navy hover:text-portal-orange transition-colors cursor-pointer underline flex items-center gap-1"
            >
              <FolderOpen className="w-3.5 h-3.5" />
              <span>{t('fileBrowser.selectFolder')}</span>
            </button>
          </div>

          <div className="space-y-2">
            <div className="font-extrabold text-sm text-[var(--color-text-primary)] capitalize">
              {credentials.target_provider || 'nextcloud'}
            </div>
            <div className="text-xs text-[var(--color-text-muted)] font-mono break-all leading-normal">
              {credentials.target_url || t('migrations.oauth')}
            </div>
            <div className="flex flex-wrap gap-1.5 pt-1">
              <span className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-xl bg-white dark:bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] text-xs font-mono text-[var(--color-portal-navy-themed)] shadow-2xs font-bold">
                <Folder className="w-3.5 h-3.5 text-emerald-500 shrink-0" />
                <span>{targetDir || '/'}</span>
              </span>
            </div>
          </div>
        </div>
      </div>

      <div className="grid md:grid-cols-3 gap-8 items-stretch">
        
        {/* Ledger Browser Tree Card */}
        <div className="md:col-span-2 glass-panel border border-[var(--color-glass-border)] shadow-portal rounded-3xl flex flex-col p-5 h-full">
          {/* Tab Switcher */}
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-4 mb-4 gap-4">
            <div className="flex bg-[var(--color-bg-tertiary)]/80 border border-[var(--color-border)]/20 p-1 rounded-2xl flex-grow max-w-md">
              <button
                onClick={() => handleTabChange('files')}
                className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                  activeTab === 'files'
                    ? 'bg-gradient-to-tr from-portal-navy to-portal-navy-light text-[var(--color-text-inverse)] shadow-xs'
                    : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                }`}
              >
                {t('fileBrowser.files')} ({pathsToMigrate.length})
              </button>
              {(credentials.source_provider === 'nextcloud' || credentials.source_provider === 'google') && (
                <>
                  <button
                    onClick={() => handleTabChange('calendars')}
                    className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                      activeTab === 'calendars'
                        ? 'bg-gradient-to-tr from-portal-navy to-portal-navy-light text-[var(--color-text-inverse)] shadow-xs'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                  >
                    {t('fileBrowser.calendars')} ({Object.values(selectedCalendars).filter(Boolean).length})
                  </button>
                  <button
                    onClick={() => handleTabChange('contacts')}
                    className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                      activeTab === 'contacts'
                        ? 'bg-gradient-to-tr from-portal-navy to-portal-navy-light text-[var(--color-text-inverse)] shadow-xs'
                        : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                    }`}
                  >
                    {t('fileBrowser.contacts')} ({Object.values(selectedContacts).filter(Boolean).length})
                  </button>
                </>
              )}
            </div>

            <div className="flex items-center gap-2 shrink-0">
              <button
                onClick={deselectAll}
                className="p-2.5 text-[var(--color-text-muted)] hover:text-[var(--color-portal-orange-themed)] hover:bg-[var(--color-bg-tertiary)] rounded-xl transition-all cursor-pointer border border-[var(--color-border)] flex items-center gap-1.5"
                title={t('common.deselectAll')}
              >
                <X className="w-4 h-4" />
                <span className="text-[11px] font-mono font-bold uppercase tracking-wider">{t('common.deselectAll')}</span>
              </button>

              {activeTab !== 'files' && (
                <button
                  onClick={() => activeTab === 'calendars' ? fetchCalendars(true) : fetchContacts(true)}
                  disabled={loadingCalendars || loadingContacts}
                  className="p-2.5 text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] hover:bg-[var(--color-bg-tertiary)] rounded-xl transition-all cursor-pointer border border-[var(--color-border)] disabled:opacity-50"
                  title={t('common.refresh')}
                >
                  <RefreshCw className={`w-4 h-4 ${(loadingCalendars || loadingContacts) ? 'animate-spin' : ''}`} />
                </button>
              )}
            </div>
          </div>

          <div className="flex-grow overflow-y-auto scrollbar-portal">
            {activeTab === 'files' && (
              directoryContents['/']?.length > 0 ? (
                directoryContents['/'].map((file) => renderNode(file, 0))
              ) : (
                <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                  <Folder className="w-10 h-10 text-[var(--color-text-muted)] animate-float" />
                  <p className="font-mono text-[10px] italic text-[var(--color-text-muted)]">{t('fileBrowser.noFiles')}</p>
                </div>
              )
            )}

            {activeTab === 'calendars' && (
              loadingCalendars ? (
                <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                  <RefreshCw className="w-8 h-8 text-[var(--color-portal-orange-themed)] animate-spin" />
                   <p className="font-mono text-[10px] italic">{t('fileBrowser.loadingCalendars')}</p>
                </div>
              ) : calendars.length > 0 ? (
                <div className="space-y-2">
                  {calendars.map((cal) => (
                    <div
                      key={cal.path}
                      className={`flex items-center gap-3.5 py-3 px-4 border rounded-2xl cursor-pointer transition-all duration-250 ${
                        selectedCalendars[cal.path] 
                          ? 'bg-[var(--color-bg-tertiary)] border-portal-navy shadow-xs font-semibold' 
                          : 'bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]'
                      }`}
                      onClick={() => setSelectedCalendars(prev => ({ ...prev, [cal.path]: !prev[cal.path] }))}
                    >
                      <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                        <div className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                          selectedCalendars[cal.path] 
                            ? 'bg-gradient-to-tr from-portal-orange to-orange-500 text-[var(--color-text-inverse)] border-transparent' 
                            : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)]'
                        }`}>
                          {selectedCalendars[cal.path] && <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />}
                        </div>
                      </button>
                      <Calendar className="w-5 h-5 text-[var(--color-portal-navy-themed)]" />
                      <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">{cal.name}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                  <Calendar className="w-10 h-10 text-[var(--color-text-muted)] animate-float" />
                   <p className="font-mono text-[10px] italic">{t('fileBrowser.noCalendars')}</p>
                </div>
              )
            )}

            {activeTab === 'contacts' && (
              loadingContacts ? (
                <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                  <RefreshCw className="w-8 h-8 text-[var(--color-portal-orange-themed)] animate-spin" />
                   <p className="font-mono text-[10px] italic">{t('fileBrowser.loadingContacts')}</p>
                </div>
              ) : contacts.length > 0 ? (
                <div className="space-y-2">
                  {contacts.map((addr) => (
                    <div
                      key={addr.path}
                      className={`flex items-center gap-3.5 py-3 px-4 border rounded-2xl cursor-pointer transition-all duration-250 ${
                        selectedContacts[addr.path] 
                          ? 'bg-[var(--color-bg-tertiary)] border-portal-navy shadow-xs font-semibold' 
                          : 'bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]'
                      }`}
                      onClick={() => setSelectedContacts(prev => ({ ...prev, [addr.path]: !prev[addr.path] }))}
                    >
                      <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                        <div className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                          selectedContacts[addr.path] 
                            ? 'bg-gradient-to-tr from-portal-orange to-orange-500 text-[var(--color-text-inverse)] border-transparent' 
                            : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)]'
                        }`}>
                          {selectedContacts[addr.path] && <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />}
                        </div>
                      </button>
                      <BookOpen className="w-5 h-5 text-[var(--color-portal-navy-themed)]" />
                      <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">{addr.name}</span>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                  <BookOpen className="w-10 h-10 text-[var(--color-text-muted)] animate-float" />
                   <p className="font-mono text-[10px] italic">{t('fileBrowser.noContacts')}</p>
                </div>
              )
            )}
          </div>
        </div>

        {/* Configurations Sidebar */}
        <div className="space-y-6 flex flex-col">
          {/* Action submit button - moved to top */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="w-full flex items-center justify-center gap-2.5 py-4 bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] hover:shadow-md hover:scale-[1.01] active:scale-[0.99] transition-all rounded-2xl font-mono text-xs font-bold uppercase tracking-wider cursor-pointer duration-300 disabled:opacity-50 disabled:cursor-not-allowed shrink-0"
          >
            {starting ? (
              <>
                <RefreshCw className="w-4 h-4 animate-spin" />
                <span>{t('fileBrowser.indexing')}</span>
              </>
            ) : (
              <>
                <Play className="w-4 h-4 fill-current stroke-[2.5]" />
                <span>{t('fileBrowser.startTransfer')}</span>
              </>
            )}
          </button>

          <div className="glass-panel border border-[var(--color-glass-border)] rounded-3xl p-6 shadow-portal space-y-6 flex-grow text-left">
            
            <div className="flex items-center gap-2 border-b border-[var(--color-border-light)] pb-3 mb-1">
              <h3 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)] tracking-tight">{t('fileBrowser.config')}</h3>
            </div>

            {/* Job Mode Selector: Migration vs Sync */}
            <div className="space-y-2 text-xs">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('sync.mode')}</label>
              <div className="grid grid-cols-2 gap-2 p-1 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-2xl">
                <button
                  type="button"
                  onClick={() => setJobType('migration')}
                  className={`py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold transition-all cursor-pointer ${
                    jobType === 'migration'
                      ? 'bg-portal-navy text-white shadow-xs'
                      : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                  }`}
                >
                  {t('sync.modeMigration')}
                </button>
                <button
                  type="button"
                  onClick={() => setJobType('sync')}
                  className={`py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold transition-all cursor-pointer ${
                    jobType === 'sync'
                      ? 'bg-portal-navy text-white shadow-xs'
                      : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                  }`}
                >
                  {t('sync.modeSync')}
                </button>
              </div>
            </div>

            {jobType === 'sync' && (
              <div className="space-y-4 p-4 rounded-2xl bg-amber-50/60 border border-amber-200 text-xs animate-fade-in">
                {/* Direction */}
                <div className="space-y-2">
                  <label className="block text-[10px] font-bold text-amber-900 uppercase tracking-widest font-mono">{t('sync.direction')}</label>
                  <div className="grid grid-cols-2 gap-2">
                    <button
                      type="button"
                      onClick={() => setDirection('one_way')}
                      className={`py-2 px-2.5 rounded-xl text-[11px] font-bold font-mono transition-all border cursor-pointer ${
                        direction === 'one_way'
                          ? 'bg-amber-600 text-white border-amber-600'
                          : 'bg-white text-amber-900 border-amber-200'
                      }`}
                    >
                      {t('sync.oneWay')} (→)
                    </button>
                    <button
                      type="button"
                      onClick={() => setDirection('two_way')}
                      className={`py-2 px-2.5 rounded-xl text-[11px] font-bold font-mono transition-all border cursor-pointer ${
                        direction === 'two_way'
                          ? 'bg-amber-600 text-white border-amber-600'
                          : 'bg-white text-amber-900 border-amber-200'
                      }`}
                    >
                      {t('sync.twoWay')} (↔)
                    </button>
                  </div>
                </div>

                {/* Interval */}
                <div className="space-y-1">
                  <label className="block text-[10px] font-bold text-amber-900 uppercase tracking-widest font-mono">{t('sync.interval')}</label>
                  <select
                    value={intervalMinutes}
                    onChange={(e) => setIntervalMinutes(parseInt(e.target.value, 10))}
                    className="w-full bg-white border border-amber-200 rounded-xl py-2 px-3 text-xs font-mono text-amber-900 focus:outline-none"
                  >
                    <option value={5}>5 {t('sync.minutes')}</option>
                    <option value={15}>15 {t('sync.minutes')}</option>
                    <option value={30}>30 {t('sync.minutes')}</option>
                    <option value={60}>1 {t('sync.hour')}</option>
                    <option value={360}>6 {t('sync.hours')}</option>
                    <option value={1440}>24 {t('sync.hours')}</option>
                  </select>
                </div>

                {/* Delete propagation */}
                <div className="flex items-center gap-2 pt-1">
                  <input
                    type="checkbox"
                    id="deletePropagation"
                    checked={deletePropagation}
                    onChange={(e) => setDeletePropagation(e.target.checked)}
                    className="rounded text-amber-600 focus:ring-amber-500 cursor-pointer"
                  />
                  <label htmlFor="deletePropagation" className="text-[11px] font-bold text-amber-950 cursor-pointer">
                    {t('sync.deletePropagation')}
                  </label>
                </div>
              </div>
            )}

            {/* Target Path */}
            <div className="space-y-2 text-xs">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('fileBrowser.targetDir')}</label>
              <input
                type="text"
                value={targetDir}
                className="w-full bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-xl py-2.5 px-3.5 text-[var(--color-text-secondary)] font-mono text-[11px] cursor-default focus:outline-none"
                readOnly
              />
              <button
                type="button"
                onClick={openTargetBrowser}
                className="w-full py-2.5 bg-portal-navy hover:bg-portal-navy-light text-[var(--color-text-inverse)] text-[11px] font-bold font-mono uppercase tracking-wider rounded-xl shadow-xs transition-all flex items-center justify-center gap-1.5 cursor-pointer"
              >
                <FolderOpen className="w-4 h-4" />
                <span>{t('fileBrowser.selectFolder')}</span>
              </button>
              <p className="text-[10px] text-[var(--color-text-muted)] leading-relaxed font-sans">
                {t('fileBrowser.targetCopied')}
              </p>
            </div>

            {/* Conflict Strategy block selector */}
            {jobType === 'sync' && direction === 'one_way' ? (
              <div className="p-3.5 rounded-2xl bg-amber-50/70 border border-amber-200 text-amber-900 text-xs font-mono flex items-center gap-2">
                <Info className="w-4 h-4 text-amber-600 shrink-0" />
                <span>{t('sync.oneWayConflictNote')}</span>
              </div>
            ) : (
              <div className="space-y-3 text-xs">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('fileBrowser.conflictHandling')}</label>
                <div className="space-y-2">
                  {/* OVERWRITE card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('OVERWRITE')}
                    className={`w-full text-left p-3.5 rounded-2xl border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'OVERWRITE'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-portal-navy text-[var(--color-portal-navy-themed)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {jobType === 'sync' ? t('sync.conflictSourceWins') : t('fileBrowser.overwrite')}
                      </span>
                      {conflictStrategy === 'OVERWRITE' && <Check className="w-4 h-4 text-[var(--color-portal-orange-themed)] stroke-[3]" />}
                    </div>
                    <p className={`text-[10px] mt-1 leading-normal font-normal ${conflictStrategy === 'OVERWRITE' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.overwriteDesc')}
                    </p>
                  </button>

                  {/* RENAME card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('RENAME')}
                    className={`w-full text-left p-3.5 rounded-2xl border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'RENAME'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-portal-navy text-[var(--color-portal-navy-themed)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {jobType === 'sync' ? t('sync.conflictKeepBoth') : t('fileBrowser.rename')}
                      </span>
                      {conflictStrategy === 'RENAME' && <Check className="w-4 h-4 text-[var(--color-portal-orange-themed)] stroke-[3]" />}
                    </div>
                    <p className={`text-[10px] mt-1 leading-normal font-normal ${conflictStrategy === 'RENAME' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.renameDesc')}
                    </p>
                  </button>

                  {/* SKIP card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('SKIP')}
                    className={`w-full text-left p-3.5 rounded-2xl border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'SKIP'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-portal-navy text-[var(--color-portal-navy-themed)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {jobType === 'sync' ? t('sync.conflictSkip') : t('fileBrowser.skip')}
                      </span>
                      {conflictStrategy === 'SKIP' && <Check className="w-4 h-4 text-[var(--color-portal-orange-themed)] stroke-[3]" />}
                    </div>
                    <p className={`text-[10px] mt-1 leading-normal font-normal ${conflictStrategy === 'SKIP' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.skipDesc')}
                    </p>
                  </button>
                </div>
              </div>
            )}

            {/* Thread count selector */}
            <div className="space-y-3 text-xs pt-4 border-t border-[var(--color-border-light)]">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('fileBrowser.threads')}</label>
              <div className="flex items-center gap-4">
                <input
                  type="range"
                  min="1"
                  max={16}
                  value={threads}
                  onChange={(e) => setThreads(parseInt(e.target.value, 10))}
                  className="flex-grow accent-portal-navy cursor-pointer"
                />
                <span className={`font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[32px] text-center transition-colors ${
                  threads > 8 ? 'bg-[var(--color-warning-bg)] text-[var(--color-portal-orange-themed)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-portal-navy-themed)]'
                }`}>
                  {threads}
                </span>
              </div>
              <p className="text-[9.5px] text-[var(--color-text-muted)] leading-relaxed font-sans">
                {threads > 8 ? (
                  <span className="text-[var(--color-portal-orange-themed)] font-semibold">{t('fileBrowser.threadsHighWarn')}</span>
                ) : (
                  t('fileBrowser.threadsHint')
                )}
              </p>
            </div>

            {/* Scheduling Option */}
            <div className="space-y-3 text-xs pt-4 border-t border-[var(--color-border-light)]">
              <label className="flex items-center gap-3 cursor-pointer group">
                <input
                  type="checkbox"
                  checked={enableScheduling}
                  onChange={(e) => setEnableScheduling(e.target.checked)}
                  className="w-4 h-4 rounded border-[var(--color-border)] text-portal-orange focus:ring-portal-orange/30 cursor-pointer"
                />
                <div className="flex items-center gap-2">
                  <Calendar className="w-4 h-4 text-[var(--color-text-muted)] group-hover:text-portal-orange transition-colors" />
                  <span className="text-xs font-semibold text-[var(--color-text-primary)]">
                    {t('fileBrowser.schedule')}
                  </span>
                </div>
              </label>
              
              {enableScheduling && (
                <div className="mt-3 pl-7">
                    <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                      {t('fileBrowser.scheduleTime')}
                    </label>
                  <input
                    type="datetime-local"
                    value={scheduledTime}
                    onChange={(e) => setScheduledTime(e.target.value)}
                    min={minScheduledTime}
                    className="w-full bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-xl py-2.5 px-4 text-sm focus:outline-none focus:ring-2 focus:ring-portal-orange/30 focus:border-portal-orange transition-all font-sans"
                  />
                  <p className="text-[9.5px] text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
                    {t('fileBrowser.scheduleHint')}
                  </p>
                </div>
              )}
            </div>

            {/* Bandwidth Limit */}
            <div className="space-y-3 text-xs pt-4 border-t border-[var(--color-border-light)]">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-3">
                {t('fileBrowser.bandwidth')}
              </label>
              <div className="flex items-center gap-4">
                <input
                  type="range"
                  min="0"
                  max="1000"
                  step="1"
                  value={bandwidthLimit}
                  onChange={(e) => setBandwidthLimit(parseInt(e.target.value, 10))}
                  className="flex-grow accent-portal-navy cursor-pointer"
                />
                <span className="font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[48px] text-center bg-[var(--color-bg-tertiary)] text-[var(--color-portal-navy-themed)]">
                  {bandwidthLimit === 0 ? '∞' : `${bandwidthLimit}`}
                </span>
              </div>
              <p className="text-[9.5px] text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
                {bandwidthLimit === 0 ? (
                  t('fileBrowser.bandwidthUnlimited')
                ) : (
                  t('fileBrowser.bandwidthHint', { limit: bandwidthLimit })
                )}
              </p>
            </div>
          </div>

          {error && (
            <div className="p-4 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] rounded-2xl text-[11px] font-semibold text-[var(--color-error-text)] leading-normal flex gap-2 text-left mt-4">
              <AlertTriangle className="w-4 h-4 shrink-0 text-[var(--color-error-text)] mt-0.5" />
              <span>{error}</span>
            </div>
          )}
        </div>
      </div>

      {/* Target Directory Browser Modal */}
      {isTargetBrowserOpen && (
        <div className="fixed inset-0 bg-[var(--color-bg-inverse)]/60 backdrop-blur-md z-50 flex items-center justify-center p-4 animate-fade-in">
          <div className="bg-[var(--color-bg-secondary)]/95 border border-[var(--color-glass-border)] rounded-3xl shadow-2xl max-w-lg w-full max-h-[85vh] flex flex-col overflow-hidden animate-slide-up text-left">
            
            {/* Modal Header */}
            <div className="p-5 border-b border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
              <div>
                <h3 className="font-display font-extrabold text-lg text-[var(--color-portal-navy-themed)] tracking-tight">
                  {t('fileBrowser.targetSelectTitle')}
                </h3>
                <p className="text-[10px] text-[var(--color-text-muted)] mt-0.5 uppercase tracking-wider font-mono">
                  {t('fileBrowser.targetSelectSubtitle')}
                </p>
              </div>
              <button
                type="button"
                onClick={() => {
                  setIsTargetBrowserOpen(false);
                  setIsCreatingFolder(false);
                  setNewFolderName('');
                }}
                className="p-1.5 text-[var(--color-text-muted)] hover:text-[var(--color-text-secondary)] hover:bg-[var(--color-border)]/50 rounded-xl transition-colors cursor-pointer"
              >
                <X className="w-5 h-5" />
              </button>
            </div>

            {/* Modal Content - Directory Tree */}
            <div className="p-5 flex-grow overflow-y-auto min-h-[300px] scrollbar-portal">
              {targetError && (
                <div className="mb-4 p-3 bg-[var(--color-error-bg)] border border-[var(--color-error-border)] rounded-2xl text-xs text-[var(--color-error-text)] flex gap-2">
                  <AlertTriangle className="w-4 h-4 shrink-0 text-[var(--color-error-text)]" />
                  <span>{targetError}</span>
                </div>
              )}

              <div className="border border-[var(--color-border)]/60 rounded-2xl bg-[var(--color-bg-tertiary)]/30 p-2 overflow-x-auto max-h-[350px] scrollbar-portal">
                {/* Root Directory Node */}
                <div className="select-none font-sans text-xs">
                  <div
                    className={`flex items-center gap-2.5 py-2 px-3 border border-transparent hover:bg-[var(--color-bg-tertiary)]/50 cursor-pointer transition-colors duration-150 rounded-xl ${
                      targetDir === '/' ? 'bg-[var(--color-bg-secondary)] font-bold border-[var(--color-border)] text-[var(--color-portal-navy-themed)] shadow-xs' : ''
                    }`}
                    onClick={() => setTargetDir('/')}
                  >
                    <span
                      className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-portal-navy-themed)] transition-colors cursor-pointer"
                      onClick={(e) => {
                        e.stopPropagation();
                        const isExpanded = !!targetExpandedPaths['/'];
                        setTargetExpandedPaths((prev) => ({ ...prev, '/': !isExpanded }));
                        if (!isExpanded) fetchTargetChildren('/');
                      }}
                    >
                      {targetLoadingPaths['/'] ? (
                        <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-portal-navy-themed)]" />
                      ) : targetExpandedPaths['/'] ? (
                        <ChevronDown className="w-3.5 h-3.5" />
                      ) : (
                        <ChevronRight className="w-3.5 h-3.5" />
                      )}
                    </span>
                    <span className="text-[var(--color-portal-navy-themed)]">
                      {targetExpandedPaths['/'] ? (
                        <FolderOpen className="w-4 h-4 text-[var(--color-portal-navy-themed)]/80" />
                      ) : (
                        <Folder className="w-4 h-4 text-[var(--color-portal-navy-themed)]/80" />
                      )}
                    </span>
                    <span className={`text-[11.5px] truncate flex-grow text-left ${
                      targetDir === '/' ? 'text-[var(--color-portal-navy-themed)]' : 'text-[var(--color-text-secondary)]'
                    }`}>
                      {t('fileBrowser.mainDir')}
                    </span>
                    {targetDir === '/' && (
                      <Check className="w-3.5 h-3.5 text-[var(--color-portal-orange-themed)] stroke-[3]" />
                    )}
                  </div>

                  {/* Root Children */}
                  {targetExpandedPaths['/'] && (
                    <div className="relative">
                      {/* Tree visual line */}
                      <div className="absolute left-[20px] top-0 bottom-3 border-l border-[var(--color-border)]"></div>
                      
                      {targetDirectoryContents['/'] && targetDirectoryContents['/'].length > 0 ? (
                        targetDirectoryContents['/'].map((child) => renderTargetNode(child, 1))
                      ) : targetLoadingPaths['/'] ? null : (
                        <div className="text-[10px] text-[var(--color-text-muted)] italic py-2 pl-[42px] text-left">
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
                className="p-4 border-t border-[var(--color-border-light)] bg-[var(--color-bg-tertiary)]/50 flex items-center gap-3 text-left animate-slide-up"
              >
                <div className="flex-grow space-y-1">
                  <label className="block text-[9px] font-bold font-mono text-[var(--color-text-muted)] uppercase tracking-widest">
                    Neuer Ordnername in {targetDir}
                  </label>
                  <input
                    type="text"
                    value={newFolderName}
                    onChange={(e) => setNewFolderName(e.target.value)}
                    placeholder="z.B. Archiv"
                    className="w-full bg-[var(--color-bg-secondary)] border border-[var(--color-border)] rounded-xl py-2 px-3 text-xs text-[var(--color-text-primary)] focus:outline-none focus:border-[var(--color-portal-navy-themed)]"
                    autoFocus
                  />
                </div>
                <div className="flex items-end gap-1.5 pt-5">
                  <button
                    type="submit"
                    disabled={!newFolderName.trim()}
                    className="px-3.5 py-2 bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] text-xs font-mono font-bold uppercase rounded-xl shadow-xs hover:shadow-sm active:scale-97 transition-all disabled:opacity-50 disabled:cursor-not-allowed cursor-pointer"
                  >
                    Erstellen
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setIsCreatingFolder(false);
                      setNewFolderName('');
                    }}
                    className="px-3.5 py-2 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] text-[var(--color-text-secondary)] text-xs font-mono font-bold uppercase rounded-xl hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer"
                  >
                    {t('common.cancel')}
                  </button>
                </div>
              </form>
            )}

            {/* Modal Footer */}
            <div className="p-4 border-t border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
              <div className="text-left max-w-[200px] md:max-w-[240px] space-y-0.5">
                <p className="text-[9px] text-[var(--color-text-muted)] font-bold font-mono uppercase tracking-wider">Auswahl:</p>
                <p className="font-mono text-[11px] text-[var(--color-text-primary)] truncate font-semibold">{targetDir}</p>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setIsCreatingFolder(true)}
                  className="px-3.5 py-2 bg-[var(--color-bg-secondary)] border border-[var(--color-border)] text-[var(--color-text-secondary)] text-[11px] font-mono font-bold uppercase rounded-xl shadow-xs hover:bg-[var(--color-bg-tertiary)] hover:text-[var(--color-portal-navy-themed)] transition-all flex items-center gap-1.5 cursor-pointer"
                  title="Neuen Ordner in diesem Verzeichnis erstellen"
                >
                  <FolderPlus className="w-4 h-4 text-[var(--color-portal-navy-themed)]" />
                  <span>Neuer Ordner</span>
                </button>
                <button
                  type="button"
                  onClick={() => {
                    setIsTargetBrowserOpen(false);
                    setIsCreatingFolder(false);
                    setNewFolderName('');
                  }}
                  className="px-4 py-2 bg-gradient-to-r from-portal-orange to-orange-500 text-[var(--color-text-inverse)] text-[11px] font-mono font-bold uppercase rounded-xl shadow-xs hover:shadow-sm active:scale-97 transition-all cursor-pointer"
                >
                  {t('common.select')}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};
