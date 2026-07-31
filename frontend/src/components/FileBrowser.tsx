import React, { useState, useMemo, useEffect, useCallback, useId, useRef } from 'react';
import {
  ArchiveBoxIcon as Archive,
  ArrowLeftIcon as ArrowLeft,
  ArrowPathIcon as RefreshCw,
  BookOpenIcon as BookOpen,
  CalendarDaysIcon as Calendar,
  CheckIcon as Check,
  ChevronDownIcon as ChevronDown,
  ChevronRightIcon as ChevronRight,
  CodeBracketIcon as FileCode,
  DocumentIcon as File,
  DocumentTextIcon as FileText,
  ExclamationTriangleIcon as AlertTriangle,
  FilmIcon as Film,
  FolderIcon as Folder,
  FolderOpenIcon as FolderOpen,
  FolderPlusIcon as FolderPlus,
  InformationCircleIcon as Info,
  MusicalNoteIcon as Music,
  PhotoIcon as ImageIcon,
  PlayIcon as Play,
  XMarkIcon as X,
} from '@heroicons/react/24/outline';
import type { CloudFile, MigrationConfig } from '../types';
import { useTranslation } from 'react-i18next';
import { useFormat } from '../utils/format';
import { useApiError } from '../utils/apiError';
import { apiFetch } from '../utils/apiClient';
import { SelectedPathsViewer } from './SelectedPathsViewer';
import { Button } from './Button';
import { useFocusTrap } from '../hooks/useFocusTrap';
import { BANDWIDTH_OPTIONS, valueToBandwidthIndex, bandwidthIndexToValue, getBandwidthLabel } from '../utils/bandwidth';


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
    return a.name.localeCompare(b.name, undefined, { sensitivity: 'base' });
  });
};

const getFileIcon = (fileName: string, className = "w-5 h-5 shrink-0") => {
  if (!fileName) return <File className={`${className} ui-file-default`} />;
  if (fileName.endsWith('/')) return <Folder className={`${className} ui-file-folder`} />;

  const lastSegment = fileName.split('/').pop() || '';
  if (!lastSegment.includes('.')) return <File className={`${className} ui-file-default`} />;

  const ext = lastSegment.split('.').pop()?.toLowerCase() || '';

  if (['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'ico', 'tiff', 'heic', 'raw', 'psd', 'ai'].includes(ext)) {
    return <ImageIcon className={`${className} ui-file-image`} />;
  }
  if (['mp4', 'mkv', 'avi', 'mov', 'webm', 'm4v', 'flv', 'wmv', 'mpeg', '3gp'].includes(ext)) {
    return <Film className={`${className} ui-file-video`} />;
  }
  if (['mp3', 'wav', 'flac', 'aac', 'ogg', 'm4a', 'wma', 'alac'].includes(ext)) {
    return <Music className={`${className} ui-file-audio`} />;
  }
  if (['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp', 'rtf', 'txt', 'csv', 'md'].includes(ext)) {
    return <FileText className={`${className} ui-file-document`} />;
  }
  if (['js', 'ts', 'jsx', 'tsx', 'json', 'xml', 'html', 'css', 'scss', 'py', 'go', 'rs', 'java', 'c', 'cpp', 'h', 'sh', 'yaml', 'yml', 'sql', 'env'].includes(ext)) {
    return <FileCode className={`${className} ui-file-folder`} />;
  }
  if (['zip', 'tar', 'gz', '7z', 'rar', 'bz2', 'xz', 'iso', 'dmg'].includes(ext)) {
    return <Archive className={`${className} ui-file-archive`} />;
  }

  return <File className={`${className} ui-file-default`} />;
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
  const isImmichSource = credentials.source_provider === 'immich';
  const isImmichTarget = credentials.target_provider === 'immich';
  const hasImmichEndpoint = isImmichSource || isImmichTarget;

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
      acc[f.path] = !isImmichSource || f.path === '/All Assets';
      return acc;
    }, {} as Record<string, boolean>)
  );
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [conflictStrategy, setConflictStrategy] = useState('SKIP');
  const [threads, setThreads] = useState<number>(8);
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
  const targetDialogRef = useRef<HTMLDivElement>(null);
  const targetCloseButtonRef = useRef<HTMLButtonElement>(null);
  const targetDialogTitleId = useId();

  // Job type: a third mode (e.g. 'backup') can be added later as a third
  // segmented-control column without restructuring the settings strip.
  const [jobType, setJobType] = useState<'migration' | 'sync'>('migration');
  const [direction, setDirection] = useState<'one_way' | 'two_way'>('one_way');
  const [intervalMinutes, setIntervalMinutes] = useState<number>(15);
  const [deletePropagation, setDeletePropagation] = useState<boolean>(false);
  // A profile change can introduce Immich after sync was selected. Deriving the
  // active mode keeps the UI and request path migration-only without a stateful
  // effect that would cause an unnecessary render.
  const effectiveJobType = hasImmichEndpoint ? 'migration' : jobType;

  // Scheduling state
  const [enableScheduling, setEnableScheduling] = useState(false);
  const [scheduledTime, setScheduledTime] = useState('');
  const [bandwidthLimit, setBandwidthLimit] = useState(0);

  const closeTargetBrowser = useCallback(() => {
    setIsTargetBrowserOpen(false);
    setIsCreatingFolder(false);
    setNewFolderName('');
  }, []);

  useFocusTrap(targetDialogRef, targetCloseButtonRef, closeTargetBrowser, isTargetBrowserOpen);

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
      const response = await apiFetch(`${apiUrl}/api/migration/target/browse`, {
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
      const response = await apiFetch(`${apiUrl}/api/migration/target/mkdir`, {
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
      const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
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
      const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
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

  const fetchChildren = async (folderPath: string, force?: boolean) => {
    if (loadingPaths[folderPath]) return;
    if (!force && directoryContents[folderPath]) return;

    setLoadingPaths((prev) => ({ ...prev, [folderPath]: true }));
    try {
      const response = await apiFetch(`${apiUrl}/api/migration/browse`, {
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

  const refreshFiles = async () => {
    setDirectoryContents({});
    setExpandedPaths({});
    await fetchChildren('/', true);
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

    if (effectiveJobType === 'sync') {
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
      if (effectiveJobType === 'sync') {
        const response = await apiFetch(`${apiUrl}/api/sync`, {
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
            bandwidth_limit_mbps: bandwidthLimit,
            target_dir: targetDir,
            selected_paths: pathsToMigrate,
          }),
        });

        if (!response.ok) {
          const b = await response.json().catch(() => ({} as { error_code?: string }));
          throw new Error(b.error_code ? translateApiError(b.error_code) : t('sync.createFailed'));
        }

        const data = await response.json() as { id?: string };
        if (data.id) {
          // Trigger first pass immediately
          const startResponse = await apiFetch(`${apiUrl}/api/sync/${data.id}/start`, {
            method: 'POST',
            headers: { 'Authorization': `Bearer ${token}` },
          });
          if (!startResponse.ok) {
            const body = await startResponse.json().catch(() => ({} as { error_code?: string }));
            throw new Error(body.error_code ? translateApiError(body.error_code) : t('sync.startFailed'));
          }
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

        const response = await apiFetch(`${apiUrl}/api/migration/start`, {
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
          className={`flex items-center gap-3 py-3.5 px-4 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] transition-colors duration-150 ${
            isSelected ? 'bg-[var(--color-bg-tertiary)] font-semibold' : ''
          }`}
          style={{ paddingLeft: `${depth * 20 + 16}px` }}
        >
          {/* Collapse/Expand Arrow */}
          {file.is_dir ? (
            <button
              type="button"
              className="w-5 h-5 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
              onClick={() => toggleExpand(file.path)}
              aria-label={isExpanded ? t('common.collapse', { name: file.name }) : t('common.expand', { name: file.name })}
            >
              {isLoading ? (
                <RefreshCw className="w-3.5 h-3.5 animate-spin text-[var(--color-text-primary)]" />
              ) : isExpanded ? (
                <ChevronDown className="w-4.5 h-4.5 stroke-[2]" />
              ) : (
                <ChevronRight className="w-4.5 h-4.5 stroke-[2]" />
              )}
            </button>
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
            className="flex items-center justify-center"
            aria-label={`${t('common.select')} ${file.name}`}
          >
            <div className={`w-4.5 h-4.5 border rounded flex items-center justify-center transition-all duration-200 ${
              isSelected 
                ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent shadow-xs'
                : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] hover:border-[var(--color-border)]'
            }`}>
              {isSelected && <Check className="w-3 h-3 text-[var(--color-text-inverse)] stroke-[3.5]" />}
            </div>
          </button>

          {/* Outline-style Icon */}
          <span className="shrink-0">
            {file.is_dir ? (
              isExpanded ? (
                <FolderOpen className="w-5 h-5 text-[var(--color-text-secondary)]" />
              ) : (
                <Folder className="w-5 h-5 text-[var(--color-text-secondary)]" />
              )
            ) : (
              getFileIcon(file.name, "w-5 h-5")
            )}
          </span>

          {/* Name & Size */}
          <span className={`text-[12px] truncate flex-grow leading-normal py-0.5 ${
            isSelected ? 'text-[var(--color-text-primary)] font-bold' : 'text-[var(--color-text-primary)]'
          }`}>
            {file.name}
          </span>
          
          {!file.is_dir && (
            <span className="ui-badge ui-badge-muted text-[10px] font-bold px-2 py-0.5 rounded">
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
          className={`flex items-center gap-2.5 py-2 px-3 border-b border-[var(--color-border-light)] hover:bg-[var(--color-bg-tertiary)] transition-colors duration-150 rounded-md ${
            isSelected ? 'bg-[var(--color-bg-secondary)] font-bold border border-[var(--color-border)] text-[var(--color-text-primary)] shadow-sm' : ''
          }`}
          style={{ paddingLeft: `${depth * 16 + 12}px` }}
        >
          {/* Collapse/Expand Arrow */}
          <button
            type="button"
            className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors"
            onClick={(e) => {
              e.stopPropagation();
              toggleTargetExpand(file.path);
            }}
            aria-label={isExpanded ? t('common.collapse', { name: file.name }) : t('common.expand', { name: file.name })}
          >
            {isLoading ? (
              <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-text-primary)]" />
            ) : isExpanded ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
          </button>

          {/* Icon */}
          <span className="text-[var(--color-text-primary)]">
            {isExpanded ? (
              <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" />
            ) : (
              <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
            )}
          </span>

          {/* Name */}
          <button
            type="button"
            className={`text-[11.5px] truncate flex-grow leading-normal py-0.5 text-left ${
            isSelected ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'
          }`}
            onClick={() => setTargetDir(file.path)}
            aria-pressed={isSelected}
          >
            {file.name}
          </button>

          {/* Select Indicator */}
          {isSelected && (
            <Check className="w-3.5 h-3.5 text-[var(--color-text-primary)] stroke-[3]" />
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
    <div className="w-full max-w-5xl mx-auto py-2 text-left space-y-6">
      
      {/* Wizard header */}
      <div className="flex items-center justify-between border-b border-[var(--color-border)]/50 pb-4">
        {onBack ? (
          <Button
            type="button"
            onClick={onBack}
          >
            <ArrowLeft className="w-4 h-4" />
            <span>{t('common.back')}</span>
          </Button>
        ) : <span />}
        <h1 className="font-display text-xl font-semibold leading-none text-[var(--color-text-primary)]">
          {t('fileBrowser.wizardStep')}
        </h1>
      </div>

      {/* Source & Target Connection Cards Grid */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        {/* Source Card */}
        <div className="ui-card space-y-4 p-5">
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
            <div className="flex items-center gap-2">
              <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                {t('migrations.source')}
              </h3>
            </div>
            <span className="ui-badge ui-badge-muted text-[10px] font-mono font-bold px-2.5 py-0.5">
              {t('fileBrowser.itemCount', { count: pathsToMigrate.length })}
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
        <div className="ui-card space-y-4 p-5">
          <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-2.5">
            <div className="flex items-center gap-2">
              <Folder className="w-4 h-4 text-[var(--color-text-secondary)] shrink-0" />
              <h3 className="font-display font-bold text-xs text-[var(--color-text-primary)] uppercase tracking-wider font-mono">
                {t('migrations.target')}
              </h3>
            </div>
            <button
              type="button"
              onClick={openTargetBrowser}
              className="ui-link text-[10px] font-mono font-bold hover:text-[var(--color-text-secondary)] transition-colors cursor-pointer underline flex items-center gap-1"
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
              <span className="ui-card inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-mono text-[var(--color-text-primary)] shadow-2xs font-bold">
                <Folder className="w-3.5 h-3.5 text-[var(--color-text-secondary)] shrink-0" />
                <span>{targetDir || '/'}</span>
              </span>
            </div>
          </div>
        </div>
      </div>

      {/* Settings Strip — full width, backup-ready 3-mode layout */}
        <div className="ui-card">
        {/* Mode selector (left) + start button (right) */}
        <div className="flex flex-col justify-between gap-3 border-b border-[var(--color-border-light)] px-5 py-3 sm:flex-row sm:items-center sm:px-6">
          {/* Job Mode Selector (segmented control; a third column for Backup is added later) */}
          <div className="w-full text-xs sm:w-auto">
            <div className="flex border-b border-[var(--color-border-light)]">
              <button
                type="button"
                onClick={() => setJobType('migration')}
                className={`px-3 py-2 text-sm ${
                  effectiveJobType === 'migration'
                    ? 'border-b-2 border-[var(--color-text-primary)] font-medium text-[var(--color-text-primary)]'
                    : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                }`}
              >
                {t('sync.modeMigration')}
              </button>
              {!hasImmichEndpoint && (
                <button
                  type="button"
                  onClick={() => setJobType('sync')}
                  className={`px-3 py-2 text-sm ${
                    effectiveJobType === 'sync'
                      ? 'border-b-2 border-[var(--color-text-primary)] font-medium text-[var(--color-text-primary)]'
                      : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                  }`}
                >
                  {t('sync.modeSync')}
                </button>
              )}
            </div>
          </div>

          {/* Sticky start button */}
          <button
            onClick={handleStartMigration}
            disabled={starting}
            className="ui-button-primary inline-flex w-full shrink-0 items-center justify-center gap-2 whitespace-nowrap px-4 py-2 text-sm font-medium hover:opacity-90 disabled:opacity-50 sm:w-auto"
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
        </div>

        {/* Settings body */}
        <div className="p-5 sm:p-6 space-y-6">
          {/* Sync-only options */}
          {effectiveJobType === 'sync' && (
            <div className="ui-alert ui-alert-info space-y-4 p-4 text-xs">
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                {/* Direction */}
                <div className="space-y-2">
                  <label className="block text-[10px] font-bold text-[var(--color-text-primary)] uppercase tracking-widest font-mono">{t('sync.direction')}</label>
                  <div className="grid grid-cols-2 gap-2">
                    <button
                      type="button"
                      onClick={() => setDirection('one_way')}
                        className={`py-2 px-2.5 text-[11px] font-bold font-mono transition-all cursor-pointer ${
                          direction === 'one_way'
                          ? 'ui-button-primary'
                          : 'ui-button-secondary'
                      }`}
                    >
                      {t('sync.oneWay')} (→)
                    </button>
                    <button
                      type="button"
                      onClick={() => setDirection('two_way')}
                        className={`py-2 px-2.5 text-[11px] font-bold font-mono transition-all cursor-pointer ${
                          direction === 'two_way'
                          ? 'ui-button-primary'
                          : 'ui-button-secondary'
                      }`}
                    >
                      {t('sync.twoWay')} (↔)
                    </button>
                  </div>
                </div>

                {/* Interval */}
                <div className="space-y-1">
                  <label className="block text-[10px] font-bold text-[var(--color-text-primary)] uppercase tracking-widest font-mono">{t('sync.interval')}</label>
                  <select
                    value={intervalMinutes}
                    onChange={(e) => setIntervalMinutes(parseInt(e.target.value, 10))}
                    className="ui-select w-full py-2 px-3 text-xs font-mono"
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
                <div className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    id="deletePropagation"
                    checked={deletePropagation}
                    onChange={(e) => setDeletePropagation(e.target.checked)}
                    className="rounded accent-[var(--color-text-primary)] cursor-pointer"
                  />
                  <div className="flex flex-col">
                    <label htmlFor="deletePropagation" className="text-[11px] font-bold text-[var(--color-text-primary)] cursor-pointer">
                      {t('sync.deletePropagation')}
                    </label>
                    <span className="text-[10px] text-[var(--color-text-secondary)]">
                      {t('sync.deletePropagationHelp')}
                    </span>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* Common + mode-specific settings grid (full width) */}
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-6 items-start">
            {/* Target folder is configured from the target summary above. */}
            <div className="space-y-2 text-xs md:col-span-2 xl:col-span-1">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{isImmichTarget ? t('fileBrowser.targetAlbum') : t('fileBrowser.targetDir')}</label>
              <div className="flex items-center gap-2">
                <span className="flex-grow flex items-center gap-2 px-3 py-2.5 bg-[var(--color-bg-tertiary)] border border-[var(--color-border)] rounded-xl font-mono text-xs text-[var(--color-text-secondary)] truncate">
                  <Folder className="w-3.5 h-3.5 text-[var(--color-text-secondary)] shrink-0" />
                  <span className="truncate">{targetDir || '/'}</span>
                </span>
              </div>
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-sans">
                {isImmichTarget ? t('fileBrowser.targetAlbumCopied') : t('fileBrowser.targetCopied')}
              </p>
            </div>

            {/* Conflict Strategy */}
            {isImmichTarget ? (
              <div className="ui-alert ui-alert-info p-3.5 text-xs font-mono flex items-center gap-2 xl:col-span-2">
                <Info className="w-4 h-4 shrink-0" />
                <span>{t('fileBrowser.immichDuplicateDetection')}</span>
              </div>
            ) : effectiveJobType === 'sync' && direction === 'one_way' ? (
              <div className="ui-alert ui-alert-info self-center p-3.5 text-xs font-mono flex items-center gap-2 xl:col-span-2">
                <Info className="w-4 h-4 shrink-0" />
                <span>{t('sync.oneWayConflictNote')}</span>
              </div>
            ) : (
              <div className="space-y-3 text-xs xl:col-span-2">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('fileBrowser.conflictHandling')}</label>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  {/* OVERWRITE card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('OVERWRITE')}
                    aria-pressed={conflictStrategy === 'OVERWRITE'}
                    className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'OVERWRITE'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {effectiveJobType === 'sync' ? t('sync.conflictSourceWins') : t('fileBrowser.overwrite')}
                      </span>
                      {conflictStrategy === 'OVERWRITE' && <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />}
                    </div>
                    <p className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === 'OVERWRITE' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.overwriteDesc')}
                    </p>
                  </button>

                  {/* RENAME card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('RENAME')}
                    aria-pressed={conflictStrategy === 'RENAME'}
                    className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'RENAME'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {effectiveJobType === 'sync' ? t('sync.conflictKeepBoth') : t('fileBrowser.rename')}
                      </span>
                      {conflictStrategy === 'RENAME' && <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />}
                    </div>
                    <p className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === 'RENAME' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.renameDesc')}
                    </p>
                  </button>

                  {/* SKIP card */}
                  <button
                    type="button"
                    onClick={() => setConflictStrategy('SKIP')}
                    aria-pressed={conflictStrategy === 'SKIP'}
                    className={`w-full text-left p-3.5 rounded-lg border transition-all duration-200 cursor-pointer ${
                      conflictStrategy === 'SKIP'
                        ? 'bg-[var(--color-bg-tertiary)]/50 border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold shadow-xs'
                        : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]/30'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span className="font-display">
                        {effectiveJobType === 'sync' ? t('sync.conflictSkip') : t('fileBrowser.skip')}
                      </span>
                      {conflictStrategy === 'SKIP' && <Check className="w-4 h-4 text-[var(--color-text-primary)] stroke-[3]" />}
                    </div>
                    <p className={`text-xs mt-1 leading-normal font-normal ${conflictStrategy === 'SKIP' ? 'text-[var(--color-text-secondary)]' : 'text-[var(--color-text-muted)]'}`}>
                      {t('fileBrowser.skipDesc')}
                    </p>
                  </button>
                </div>
              </div>
            )}

            {/* Thread count selector */}
            <div className="space-y-3 text-xs">
              <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono">{t('fileBrowser.threads')}</label>
              <div className="flex items-center gap-4">
                <input
                  type="range"
                  min="1"
                  max={16}
                  value={threads}
                  onChange={(e) => setThreads(parseInt(e.target.value, 10))}
                  className="flex-grow accent-[var(--color-text-primary)] cursor-pointer"
                />
                <span className={`font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[32px] text-center transition-colors ${
                  threads > 8 ? 'bg-[var(--color-warning-bg)] text-[var(--color-text-primary)]' : 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]'
                }`}>
                  {threads}
                </span>
              </div>
              <p className="text-xs text-[var(--color-text-muted)] leading-relaxed font-sans">
                {threads > 8 ? (
                  <span className="text-[var(--color-text-primary)] font-semibold">{t('fileBrowser.threadsHighWarn')}</span>
                ) : (
                  t('fileBrowser.threadsHint')
                )}
              </p>
            </div>

            {/* Bandwidth limit */}
            <div className="space-y-3 text-xs">
                <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-3">
                  {t('fileBrowser.bandwidth')}
                </label>
                <div className="flex items-center gap-4">
                  <input
                    type="range"
                    min="0"
                    max={BANDWIDTH_OPTIONS.length - 1}
                    step="1"
                    value={valueToBandwidthIndex(bandwidthLimit)}
                    onChange={(e) => {
                      const idx = parseInt(e.target.value, 10);
                      setBandwidthLimit(bandwidthIndexToValue(idx));
                    }}
                    className="flex-grow accent-[var(--color-text-primary)] cursor-pointer"
                  />
                  <span className="font-mono text-xs font-bold px-2.5 py-1 rounded-lg min-w-[70px] text-center bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)]">
                    {getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited'))}
                  </span>
                </div>
                <p className="text-xs text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
                  {bandwidthLimit === 0 ? (
                    t('fileBrowser.bandwidthUnlimited')
                  ) : (
                    t('fileBrowser.bandwidthHint', { limit: getBandwidthLabel(bandwidthLimit, t('dashboard.unlimited')) })
                  )}
                </p>
            </div>
          </div>

          {/* Migration-only scheduling */}
          {effectiveJobType === 'migration' && (
            <div className="space-y-3 text-xs pt-5 border-t border-[var(--color-border-light)]">
              <label className="flex items-center gap-3 cursor-pointer group">
                <input
                  type="checkbox"
                  checked={enableScheduling}
                  onChange={(e) => setEnableScheduling(e.target.checked)}
                  className="w-4 h-4 rounded border-[var(--color-border)] accent-[var(--color-text-primary)] cursor-pointer"
                />
                <div className="flex items-center gap-2">
                  <Calendar className="w-4 h-4 text-[var(--color-text-muted)] group-hover:text-[var(--color-text-primary)] transition-colors" />
                  <span className="text-xs font-semibold text-[var(--color-text-primary)]">
                    {t('fileBrowser.schedule')}
                  </span>
                </div>
              </label>

              {enableScheduling && (
                <div className="mt-3 sm:max-w-sm">
                  <label className="block text-[10px] font-bold text-[var(--color-text-muted)] uppercase tracking-widest font-mono mb-2">
                    {t('fileBrowser.scheduleTime')}
                  </label>
                  <input
                    type="datetime-local"
                    value={scheduledTime}
                    onChange={(e) => setScheduledTime(e.target.value)}
                    min={minScheduledTime}
                    className="ui-input w-full py-2.5 px-4 text-sm transition-all font-sans"
                  />
                  <p className="text-xs text-[var(--color-text-muted)] mt-2 leading-relaxed font-sans">
                    {t('fileBrowser.scheduleHint')}
                  </p>
                </div>
              )}
            </div>
          )}

          {error && (
            <div className="ui-alert ui-alert-error p-4 text-[11px] font-semibold leading-normal flex gap-2 text-left">
              <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
              <span>{error}</span>
            </div>
          )}
        </div>
      </div>

      {/* Ledger Browser Tree Card — full width */}
        <div className="ui-card flex flex-col p-5">
        {/* Tab Switcher */}
        <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-4 mb-4 gap-4">
          <div className="flex bg-[var(--color-bg-tertiary)]/80 border border-[var(--color-border)]/20 p-1 rounded-lg flex-grow max-w-md">
            <button
              onClick={() => handleTabChange('files')}
              className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                activeTab === 'files'
                  ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]'
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
                      ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]'
                      : 'text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                  }`}
                >
                  {t('fileBrowser.calendars')} ({Object.values(selectedCalendars).filter(Boolean).length})
                </button>
                <button
                  onClick={() => handleTabChange('contacts')}
                  className={`flex-1 py-2 px-3 rounded-xl text-center font-mono text-[11px] font-bold uppercase tracking-wider transition-all duration-300 cursor-pointer focus:outline-none ${
                    activeTab === 'contacts'
                      ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)]'
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
              className="ui-button-secondary p-2.5 hover:bg-[var(--color-bg-tertiary)] transition-all cursor-pointer flex items-center gap-1.5"
              title={t('common.deselectAll')}
            >
              <X className="w-4 h-4" />
              <span className="text-[11px] font-mono font-bold uppercase tracking-wider">{t('common.deselectAll')}</span>
            </button>

            {(() => {
              const isRefreshing = activeTab === 'files' ? !!loadingPaths['/']
                : activeTab === 'calendars' ? loadingCalendars
                : loadingContacts;

              const handleRefresh = () => {
                if (activeTab === 'files') {
                  void refreshFiles();
                } else if (activeTab === 'calendars') {
                  void fetchCalendars(true);
                } else {
                  void fetchContacts(true);
                }
              };

              return (
                <button
                  onClick={handleRefresh}
                  disabled={isRefreshing}
                  className="ui-button-secondary p-2.5 hover:bg-[var(--color-bg-tertiary)] disabled:opacity-50"
                  title={t('common.refresh')}
                  aria-label={t('common.refresh')}
                >
                  <RefreshCw className={`w-4 h-4 ${isRefreshing ? 'animate-spin' : ''}`} />
                </button>
              );
            })()}
          </div>
        </div>

        <div className="flex-grow overflow-y-auto rounded-lg">
          {activeTab === 'files' && (
            directoryContents['/']?.length > 0 ? (
              directoryContents['/'].map((file) => renderNode(file, 0))
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <Folder className="w-10 h-10 text-[var(--color-text-muted)]" />
                <p className="font-mono text-xs italic text-[var(--color-text-muted)]">{t('fileBrowser.noFiles')}</p>
              </div>
            )
          )}

          {activeTab === 'calendars' && (
            loadingCalendars ? (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                <RefreshCw className="w-8 h-8 text-[var(--color-text-primary)] animate-spin" />
                  <p className="font-mono text-xs italic">{t('fileBrowser.loadingCalendars')}</p>
              </div>
            ) : calendars.length > 0 ? (
              <div className="space-y-2">
                {calendars.map((cal) => (
                  <div
                    key={cal.path}
                    className={`flex items-center gap-3.5 py-3 px-4 border rounded-lg cursor-pointer transition-all duration-250 ${
                      selectedCalendars[cal.path] 
                        ? 'bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] shadow-xs font-semibold'
                        : 'bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]'
                    }`}
                    onClick={() => setSelectedCalendars(prev => ({ ...prev, [cal.path]: !prev[cal.path] }))}
                  >
                    <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                      <div className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                        selectedCalendars[cal.path] 
                          ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent'
                          : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)]'
                      }`}>
                        {selectedCalendars[cal.path] && <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />}
                      </div>
                    </button>
                    <Calendar className="w-5 h-5 text-[var(--color-text-primary)]" />
                    <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">{cal.name}</span>
                  </div>
                ))}
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <Calendar className="w-10 h-10 text-[var(--color-text-muted)]" />
                  <p className="font-mono text-xs italic">{t('fileBrowser.noCalendars')}</p>
              </div>
            )
          )}

          {activeTab === 'contacts' && (
            loadingContacts ? (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-3">
                <RefreshCw className="w-8 h-8 text-[var(--color-text-primary)] animate-spin" />
                 <p className="font-mono text-[10px] italic">{t('fileBrowser.loadingContacts')}</p>
              </div>
            ) : contacts.length > 0 ? (
              <div className="space-y-2">
                {contacts.map((addr) => (
                  <div
                    key={addr.path}
                    className={`flex items-center gap-3.5 py-3 px-4 border rounded-lg cursor-pointer transition-all duration-250 ${
                      selectedContacts[addr.path] 
                        ? 'bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] shadow-xs font-semibold'
                        : 'bg-[var(--color-bg-secondary)]/50 border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)]/50 hover:border-[var(--color-border)]'
                    }`}
                    onClick={() => setSelectedContacts(prev => ({ ...prev, [addr.path]: !prev[addr.path] }))}
                  >
                    <button type="button" className="focus:outline-none flex items-center justify-center cursor-pointer">
                      <div className={`w-5 h-5 border rounded-lg flex items-center justify-center transition-all duration-200 ${
                        selectedContacts[addr.path] 
                          ? 'bg-[var(--color-bg-inverse)] text-[var(--color-text-inverse)] border-transparent'
                          : 'bg-[var(--color-bg-secondary)] border-[var(--color-border)]'
                      }`}>
                        {selectedContacts[addr.path] && <Check className="w-3.5 h-3.5 text-[var(--color-text-inverse)] stroke-[3.5]" />}
                      </div>
                    </button>
                    <BookOpen className="w-5 h-5 text-[var(--color-text-primary)]" />
                    <span className="text-[12px] text-[var(--color-text-secondary)] flex-grow text-left">{addr.name}</span>
                  </div>
                ))}
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center py-24 text-[var(--color-text-muted)] gap-2">
                <BookOpen className="w-10 h-10 text-[var(--color-text-muted)]" />
                  <p className="font-mono text-xs italic">{t('fileBrowser.noContacts')}</p>
              </div>
            )
          )}
        </div>
      </div>

      {/* Target Directory Browser Modal */}
      {isTargetBrowserOpen && (
          <div className="fixed inset-0 bg-[var(--color-overlay)] z-[var(--layer-dialog)] flex items-center justify-center p-4">
          <div ref={targetDialogRef} role="dialog" aria-modal="true" aria-labelledby={targetDialogTitleId} className="ui-card max-w-lg w-full max-h-[85vh] flex flex-col overflow-hidden text-left">
            
            {/* Modal Header */}
            <div className="p-5 border-b border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
              <div>
                <h3 id={targetDialogTitleId} className="font-display font-extrabold text-lg text-[var(--color-text-primary)] tracking-tight">
                  {isImmichTarget ? t('fileBrowser.targetAlbumSelectTitle') : t('fileBrowser.targetSelectTitle')}
                </h3>
                <p className="text-xs text-[var(--color-text-muted)] mt-0.5 uppercase tracking-wider font-mono">
                  {isImmichTarget ? t('fileBrowser.targetAlbumSelectSubtitle') : t('fileBrowser.targetSelectSubtitle')}
                </p>
              </div>
              <button
                type="button"
                ref={targetCloseButtonRef}
                onClick={closeTargetBrowser}
                className="ui-button-secondary p-1.5 hover:bg-[var(--color-bg-tertiary)]"
                aria-label={t('paths.close')}
              >
                <X className="w-5 h-5" />
              </button>
            </div>

            {/* Modal Content - Directory Tree */}
            <div className="p-5 flex-grow overflow-y-auto min-h-[300px]">
              {targetError && (
                <div className="ui-alert ui-alert-error mb-4 p-3 text-xs flex gap-2">
                  <AlertTriangle className="w-4 h-4 shrink-0" />
                  <span>{targetError}</span>
                </div>
              )}

              <div className="border border-[var(--color-border)]/60 rounded-lg bg-[var(--color-bg-tertiary)]/30 p-2 overflow-x-auto max-h-[350px]">
                {/* Root Directory Node */}
                <div className="select-none font-sans text-xs">
                  <div
                    className={`flex items-center gap-2.5 py-2 px-3 border border-transparent hover:bg-[var(--color-bg-tertiary)]/50 transition-colors duration-150 rounded-xl ${
                      targetDir === '/' ? 'bg-[var(--color-bg-secondary)] font-bold border-[var(--color-border)] text-[var(--color-text-primary)] shadow-xs' : ''
                    }`}
                  >
                    <button
                      type="button"
                      className="w-4 h-4 flex items-center justify-center text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)] transition-colors cursor-pointer"
                      onClick={() => {
                        const isExpanded = !!targetExpandedPaths['/'];
                        setTargetExpandedPaths((prev) => ({ ...prev, '/': !isExpanded }));
                        if (!isExpanded) fetchTargetChildren('/');
                      }}
                      aria-label={targetExpandedPaths['/'] ? t('common.collapse', { name: isImmichTarget ? t('fileBrowser.immichLibrary') : t('fileBrowser.mainDir') }) : t('common.expand', { name: isImmichTarget ? t('fileBrowser.immichLibrary') : t('fileBrowser.mainDir') })}
                    >
                      {targetLoadingPaths['/'] ? (
                        <RefreshCw className="w-3 h-3 animate-spin text-[var(--color-text-primary)]" />
                      ) : targetExpandedPaths['/'] ? (
                        <ChevronDown className="w-3.5 h-3.5" />
                      ) : (
                        <ChevronRight className="w-3.5 h-3.5" />
                      )}
                    </button>
                    <span className="text-[var(--color-text-primary)]">
                      {/* Icon */}
                      <span className="shrink-0">
                        {targetExpandedPaths['/'] ? (
                          <FolderOpen className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        ) : (
                          <Folder className="w-4 h-4 text-[var(--color-text-secondary)]" />
                        )}
                      </span>
                    </span>
                    <button
                      type="button"
                      className={`text-[11.5px] truncate flex-grow text-left leading-normal py-0.5 ${
                      targetDir === '/' ? 'text-[var(--color-text-primary)]' : 'text-[var(--color-text-secondary)]'
                    }`}
                      onClick={() => setTargetDir('/')}
                      aria-pressed={targetDir === '/'}
                    >
                      {isImmichTarget ? t('fileBrowser.immichLibrary') : t('fileBrowser.mainDir')}
                    </button>
                    {targetDir === '/' && (
                      <Check className="w-3.5 h-3.5 text-[var(--color-text-primary)] stroke-[3]" />
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
                          {t('fileBrowser.noSubdirs')}
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
                className="p-4 border-t border-[var(--color-border-light)] bg-[var(--color-bg-tertiary)]/50 flex items-center gap-3 text-left"
              >
                <div className="flex-grow space-y-1">
                  <label className="block text-xs font-bold font-mono text-[var(--color-text-muted)] uppercase tracking-wider">
                    {isImmichTarget ? t('fileBrowser.createAlbumIn', { path: targetDir }) : t('fileBrowser.mkdirIn', { path: targetDir })}
                  </label>
                  <input
                    type="text"
                    value={newFolderName}
                    onChange={(e) => setNewFolderName(e.target.value)}
                    placeholder={t('fileBrowser.mkdirPlaceholder')}
                    className="ui-input w-full py-2 px-3 text-xs text-[var(--color-text-primary)]"
                    autoFocus
                  />
                </div>
                <div className="flex items-end gap-1.5 pt-5">
                  <button
                    type="submit"
                    disabled={!newFolderName.trim()}
                    className="ui-button-primary px-3.5 py-2 text-xs font-mono font-bold uppercase hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {t('common.create')}
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setIsCreatingFolder(false);
                      setNewFolderName('');
                    }}
                    className="ui-button-secondary px-3.5 py-2 text-xs font-mono font-bold uppercase hover:bg-[var(--color-bg-tertiary)]"
                  >
                    {t('common.cancel')}
                  </button>
                </div>
              </form>
            )}

            {/* Modal Footer */}
            <div className="p-4 border-t border-[var(--color-border-light)] flex items-center justify-between bg-[var(--color-bg-tertiary)]/50">
              <div className="text-left max-w-[200px] md:max-w-[240px] space-y-0.5">
                <p className="text-xs text-[var(--color-text-muted)] font-bold font-mono uppercase tracking-wider">{t('fileBrowser.selectionLabel')}</p>
                <p className="font-mono text-[11px] text-[var(--color-text-primary)] truncate font-semibold">{targetDir}</p>
              </div>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => setIsCreatingFolder(true)}
                  className="ui-button-secondary px-3.5 py-2 text-[11px] font-mono font-bold uppercase hover:bg-[var(--color-bg-tertiary)] flex items-center gap-1.5"
                  title={t('fileBrowser.newFolderHint')}
                >
                  <FolderPlus className="w-4 h-4 text-[var(--color-text-primary)]" />
                  <span>{isImmichTarget ? t('fileBrowser.newAlbum') : t('fileBrowser.newFolder')}</span>
                </button>
                <button
                  type="button"
                  onClick={closeTargetBrowser}
                  className="ui-button-primary px-4 py-2 text-[11px] font-mono font-bold uppercase hover:opacity-90"
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
