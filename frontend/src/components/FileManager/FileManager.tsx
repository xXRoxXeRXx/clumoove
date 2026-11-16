import { useCallback, useEffect, useMemo, useRef, useState, lazy, Suspense } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowDownTrayIcon,
  ArrowLeftIcon,
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowsRightLeftIcon,
  ArrowUpIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  ChevronUpDownIcon,
	ClipboardDocumentIcon,
  EllipsisHorizontalIcon,
	EllipsisVerticalIcon,
  FolderIcon,
  FolderPlusIcon,
  ListBulletIcon,
  PencilIcon,
  ProviderIcon,
  Squares2X2Icon,
  TrashIcon,
  WrenchScrewdriverIcon,
  XMarkIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { getFileCapabilities, listFileEntries, type FileBreadcrumb, type FileCapabilities, type FileEntry, type FileTransferSummary } from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { LoadingIndicator } from '../LoadingIndicator';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { FileUploadControl, type InternalTransfer } from './FileUploadControl';
import { FileThumbnail } from './FileThumbnail';
import { useFileManagerActions, type FileActionTransfer, type FileManagerBreadcrumb } from './useFileManagerActions';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

export type SortField = 'name' | 'size' | 'modified';
export type SortDirection = 'asc' | 'desc';

type FileManagerProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  initialBreadcrumbs?: FileBreadcrumb[];
  initialPathFallback?: boolean;
  onProfileChange: (profileId: string) => void;
  onOpenManager?: () => void;
  onBack?: () => void;
};

const unavailableCapabilities: FileCapabilities = {
  browse: false,
  native_pagination: false,
  download: false,
  archive: false,
  upload: false,
  mkdir: false,
  rename: false,
  move: false,
  copy: false,
  delete_file: false,
  delete_empty_directory: false,
  delete_recursive_directory: false,
  conflict_skip: false,
  conflict_overwrite: false,
  conflict_overwrite_atomic: false,
  conflict_rename: false,
  native_copy: false,
  range_download: false,
  thumbnails: false,
};

function toInternalTransfer(transfer: FileActionTransfer): InternalTransfer {
  const status: InternalTransfer['status'] =
    transfer.status === 'completed'
      ? transfer.operation === 'copy'
        ? 'copied'
        : 'moved'
      : transfer.status === 'running'
        ? transfer.operation === 'copy'
          ? 'copying'
          : 'moving'
        : transfer.status;
  return {
    id: transfer.id,
    operation: transfer.operation,
    sourceName: transfer.sourceLabel,
    destinationName: transfer.destinationLabel,
    itemCount: transfer.itemCount,
    status,
    error: transfer.error,
  };
}

const BACKGROUND_STATUS_BY_ACTION_STATUS: Record<FileActionTransfer['status'], string> = {
  queued: 'PENDING',
  running: 'RUNNING',
  completed: 'COMPLETED',
  failed: 'FAILED',
  cancelled: 'CANCELLED',
};

function toBackgroundTransfer(transfer: FileActionTransfer): FileTransferSummary {
  return {
    id: transfer.id,
    operation: transfer.operation,
    status: transfer.serverStatus ?? BACKGROUND_STATUS_BY_ACTION_STATUS[transfer.status],
    source_profile_name: transfer.sourceLabel,
    target_profile_name: transfer.destinationLabel,
    total_files: transfer.itemCount,
    processed_files: transfer.processedCount,
    failed_files: transfer.failedCount,
    skipped_files: transfer.skippedCount,
  };
}

export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const [profiles, setProfiles] = useState<ConnectionProfilePublic[]>([]);
  const [profilesLoading, setProfilesLoading] = useState(true);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [entriesLoading, setEntriesLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [capabilities, setCapabilities] = useState<FileCapabilities>(unavailableCapabilities);
  const [breadcrumbs, setBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [selectedRefs, setSelectedRefs] = useState<Set<string>>(() => new Set());
  const [previewEntry, setPreviewEntry] = useState<FileEntry | null>(null);
  const [menuState, setMenuState] = useState<{
    entry: FileEntry;
    x: number;
    y: number;
    align?: 'left' | 'right';
  } | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [isBreadcrumbMenuOpen, setIsBreadcrumbMenuOpen] = useState(false);
  const breadcrumbMenuRef = useRef<HTMLDivElement>(null);
  const [viewMode, setViewMode] = useState<'list' | 'grid'>(() => {
    try {
      const stored = localStorage.getItem('clumoove_file_manager_view_mode');
      return stored === 'grid' ? 'grid' : 'list';
    } catch {
      return 'list';
    }
  });

  const handleViewModeChange = (mode: 'list' | 'grid') => {
    setViewMode(mode);
    try {
      localStorage.setItem('clumoove_file_manager_view_mode', mode);
    } catch {
      // ignore storage errors
    }
  };

  const [sortField, setSortField] = useState<SortField>(() => {
    try {
      const stored = localStorage.getItem('clumoove_file_manager_sort_field');
      if (stored === 'name' || stored === 'size' || stored === 'modified') return stored;
    } catch {
      // ignore storage errors
    }
    return 'name';
  });

  const [sortDirection, setSortDirection] = useState<SortDirection>(() => {
    try {
      const stored = localStorage.getItem('clumoove_file_manager_sort_dir');
      if (stored === 'asc' || stored === 'desc') return stored;
    } catch {
      // ignore storage errors
    }
    return 'asc';
  });

  const handleSort = (field: SortField) => {
    let nextField = field;
    let nextDir: SortDirection;
    if (sortField === field) {
      nextDir = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
      nextField = field;
      // Start size/modified columns descending (largest/newest first)
      nextDir = field === 'name' ? 'asc' : 'desc';
    }
    setSortField(nextField);
    setSortDirection(nextDir);
    try {
      localStorage.setItem('clumoove_file_manager_sort_field', nextField);
      localStorage.setItem('clumoove_file_manager_sort_dir', nextDir);
    } catch {
      // ignore storage errors
    }
  };

  const renderSortHeader = (field: SortField, label: string, sortLabel: string) => {
    const isActive = sortField === field;
    return (
      <button
        type="button"
        onClick={() => handleSort(field)}
        className="group -mx-1 -my-1 px-1 py-1 inline-flex items-center gap-1.5 font-medium hover:text-[var(--color-text-primary)] transition-colors select-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)] rounded text-left"
        title={isActive ? (sortDirection === 'asc' ? t('files.sortDescending') : t('files.sortAscending')) : sortLabel}
        aria-label={`${label}: ${isActive ? (sortDirection === 'asc' ? t('files.sortAscending') : t('files.sortDescending')) : sortLabel}`}
      >
        <span className={isActive ? 'text-[var(--color-text-primary)] font-semibold' : 'text-[var(--color-text-secondary)] group-hover:text-[var(--color-text-primary)] transition-colors'}>
          {label}
        </span>
        {isActive ? (
          <span className="text-[var(--color-text-primary)] flex items-center shrink-0">
            {sortDirection === 'asc' ? (
              <ChevronUpIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
            ) : (
              <ChevronDownIcon className="h-3.5 w-3.5 stroke-[2.5]" aria-hidden="true" />
            )}
          </span>
        ) : (
          <span className="opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100 transition-opacity text-[var(--color-text-secondary)] flex items-center shrink-0">
            <ChevronUpDownIcon className="h-3.5 w-3.5" aria-hidden="true" />
          </span>
        )}
      </button>
    );
  };

  const createDirDialogRef = useRef<HTMLDivElement>(null);
  const createDirCancelRef = useRef<HTMLButtonElement>(null);
  const deleteDialogRef = useRef<HTMLDivElement>(null);
  const deleteCancelRef = useRef<HTMLButtonElement>(null);
  const batchDeleteDialogRef = useRef<HTMLDivElement>(null);
  const batchDeleteCancelRef = useRef<HTMLButtonElement>(null);
  const selectAllRef = useRef<HTMLInputElement>(null);
  const profileRequestRef = useRef<AbortController | null>(null);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const renameDialogRef = useRef<HTMLDivElement>(null);
  const renameCancelRef = useRef<HTMLButtonElement>(null);
  const pickerDialogRef = useRef<HTMLDivElement>(null);
  const pickerCancelRef = useRef<HTMLButtonElement>(null);
  const conflictDialogRef = useRef<HTMLDivElement>(null);
  const conflictCancelRef = useRef<HTMLButtonElement>(null);
  const latestEntriesRequestRef = useRef(0);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const uploadRefreshTimeoutRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (uploadRefreshTimeoutRef.current !== null) {
        window.clearTimeout(uploadRefreshTimeoutRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!menuState) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setMenuState(null);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setMenuState(null);
      }
    };
    const handleScroll = () => {
      setMenuState(null);
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener('scroll', handleScroll, true);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('scroll', handleScroll, true);
    };
  }, [menuState]);

  useEffect(() => {
    if (!isBreadcrumbMenuOpen) return;
    const handlePointerDown = (event: PointerEvent | MouseEvent) => {
      if (breadcrumbMenuRef.current && !breadcrumbMenuRef.current.contains(event.target as Node)) {
        setIsBreadcrumbMenuOpen(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setIsBreadcrumbMenuOpen(false);
      }
    };
    window.addEventListener('pointerdown', handlePointerDown);
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [isBreadcrumbMenuOpen]);

  const selectedProfile = profiles.find((profile) => profile.id === profileId) ?? null;
  const currentBreadcrumb = breadcrumbs[breadcrumbs.length - 1];
  const currentRef = currentBreadcrumb?.ref ?? null;
  const selectedEntries = entries.filter((entry) => selectedRefs.has(entry.ref));
  const allLoadedSelected = entries.length > 0 && entries.every((entry) => selectedRefs.has(entry.ref));
  const someLoadedSelected = entries.some((entry) => selectedRefs.has(entry.ref));
  const isSelectionMode = selectedRefs.size > 0;

  const sortedEntries = useMemo(() => {
    return [...entries].sort((a, b) => {
      // Directories always appear first
      if (a.kind !== b.kind) {
        return a.kind === 'directory' ? -1 : 1;
      }

      if (sortField === 'name') {
        const cmp = a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
        return sortDirection === 'asc' ? cmp : -cmp;
      }

      if (sortField === 'size') {
        if (a.kind === 'directory') {
          return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
        }
        const sizeA = a.size ?? 0;
        const sizeB = b.size ?? 0;
        if (sizeA !== sizeB) {
          return sortDirection === 'asc' ? sizeA - sizeB : sizeB - sizeA;
        }
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
      }

      if (sortField === 'modified') {
        const hasA = Boolean(a.modified_at);
        const hasB = Boolean(b.modified_at);
        if (!hasA && !hasB) {
          const cmp = a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
          return sortDirection === 'asc' ? cmp : -cmp;
        }
        if (!hasA) return 1;
        if (!hasB) return -1;

        const timeA = new Date(a.modified_at!).getTime();
        const timeB = new Date(b.modified_at!).getTime();
        if (timeA !== timeB) {
          return sortDirection === 'asc' ? timeA - timeB : timeB - timeA;
        }
        return a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
      }

      return 0;
    });
  }, [entries, sortField, sortDirection]);

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = someLoadedSelected && !allLoadedSelected;
  }, [allLoadedSelected, someLoadedSelected]);

  const loadEntries = useCallback(async (parentRef: string | null) => {
    if (!profileId || !capabilities.browse) return;
    entriesRequestRef.current?.abort();
    const controller = new AbortController();
    entriesRequestRef.current = controller;
    const request = ++latestEntriesRequestRef.current;
    setEntriesLoading(true);
    setError('');
    const result = await listFileEntries(apiUrl, token, profileId, parentRef, undefined, controller.signal);
    if (controller.signal.aborted || latestEntriesRequestRef.current !== request) return;
    if (result.ok === false) {
      setEntries([]);
      setNextCursor(null);
      setError(translateApiError(result.errorCode));
    } else {
      setEntries(result.data.entries ?? []);
      setNextCursor(result.data.next_cursor ?? null);
    }
    setEntriesLoading(false);
  }, [apiUrl, capabilities.browse, profileId, token, translateApiError]);

  const loadMore = useCallback(async () => {
    if (!profileId || !capabilities.browse || !nextCursor || entriesLoading || loadingMore) return;
    setLoadingMore(true);
    const controller = new AbortController();
    const result = await listFileEntries(apiUrl, token, profileId, currentRef, nextCursor, controller.signal);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      setEntries((current) => [...current, ...(result.data.entries ?? [])]);
      setNextCursor(result.data.next_cursor ?? null);
    }
    setLoadingMore(false);
  }, [apiUrl, capabilities.browse, currentRef, entriesLoading, loadingMore, nextCursor, profileId, token, translateApiError]);

  const loadProfiles = useCallback(async () => {
    profileRequestRef.current?.abort();
    const controller = new AbortController();
    profileRequestRef.current = controller;
    setProfilesLoading(true);
    const result = await listConnectionProfiles(apiUrl, token, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok === true) {
      setProfiles(result.data.profiles ?? []);
    } else {
      setProfiles([]);
      setError(translateApiError(result.errorCode));
    }
    setProfilesLoading(false);
  }, [apiUrl, token, translateApiError]);

  useEffect(() => {
    const timeoutId = window.setTimeout(() => void loadProfiles(), 0);
    return () => {
      window.clearTimeout(timeoutId);
      profileRequestRef.current?.abort();
      entriesRequestRef.current?.abort();
    };
  }, [loadProfiles]);

  useEffect(() => {
    let controller: AbortController | null = null;
    const timeoutId = window.setTimeout(() => {
      if (!selectedProfile) {
        setCapabilities(unavailableCapabilities);
        setBreadcrumbs([]);
        setEntries([]);
        setNextCursor(null);
        setEntriesLoading(false);
        return;
      }

      controller = new AbortController();
      entriesRequestRef.current?.abort();
      entriesRequestRef.current = controller;
      const request = ++latestEntriesRequestRef.current;

      const resolvedBreadcrumbs = initialBreadcrumbs?.length
        ? initialBreadcrumbs.map((breadcrumb) => ({ ref: breadcrumb.ref || null, name: breadcrumb.name }))
        : [{ ref: null, name: selectedProfile.name }];
      setBreadcrumbs(resolvedBreadcrumbs);
      setSelectedRefs(new Set());
      setEntries([]);
      setNextCursor(null);
      setError('');
      setEntriesLoading(true);

      const targetRef = resolvedBreadcrumbs[resolvedBreadcrumbs.length - 1]?.ref ?? null;

      async function initProfile() {
        const capResult = await getFileCapabilities(apiUrl, token, selectedProfile!.id, controller!.signal);
        if (controller!.signal.aborted || latestEntriesRequestRef.current !== request) return;

        if (capResult.ok === false) {
          setCapabilities(unavailableCapabilities);
          setEntries([]);
          setNextCursor(null);
          setError(translateApiError(capResult.errorCode));
          setEntriesLoading(false);
          return;
        }

        setCapabilities(capResult.data.capabilities);

        if (!capResult.data.capabilities.browse) {
          setEntries([]);
          setNextCursor(null);
          setEntriesLoading(false);
          return;
        }

        const listResult = await listFileEntries(apiUrl, token, selectedProfile!.id, targetRef, undefined, controller!.signal);
        if (controller!.signal.aborted || latestEntriesRequestRef.current !== request) return;

        if (listResult.ok === false) {
          setEntries([]);
          setNextCursor(null);
          setError(translateApiError(listResult.errorCode));
        } else {
          setEntries(listResult.data.entries ?? []);
          setNextCursor(listResult.data.next_cursor ?? null);
        }
        setEntriesLoading(false);
      }

      void initProfile();
    }, 0);

    return () => {
      window.clearTimeout(timeoutId);
      controller?.abort();
    };
  }, [apiUrl, initialBreadcrumbs, selectedProfile, token, translateApiError]);

  const refresh = useCallback(() => {
    if (!capabilities.browse) return;
    void loadEntries(currentRef);
  }, [capabilities.browse, currentRef, loadEntries]);

  const clearSelection = useCallback(() => {
    setSelectedRefs(new Set());
  }, []);

  const clearPreview = useCallback(() => {
    setPreviewEntry(null);
  }, []);

  const actions = useFileManagerActions({
    apiUrl,
    token,
    profileId,
    profileName: selectedProfile?.name ?? '',
    profiles,
    currentRef,
    capabilities,
    selectedEntries,
    previewEntryRef: previewEntry?.ref ?? null,
    refreshListing: refresh,
    clearSelection,
    clearPreview,
  });

  const clearActionError = actions.clearError;
  useEffect(() => {
    // A new listing request clears stale action errors, as it does for listing errors.
    if (entriesLoading) clearActionError();
  }, [clearActionError, entriesLoading]);

  useFocusTrap(createDirDialogRef, createDirCancelRef, actions.createFolder.close, actions.createFolder.open);
  useFocusTrap(deleteDialogRef, deleteCancelRef, actions.deleteEntry.close, actions.deleteEntry.open);
  useFocusTrap(batchDeleteDialogRef, batchDeleteCancelRef, actions.batch.closeDeleteDialog, actions.batch.deleteDialogOpen);
  useFocusTrap(renameDialogRef, renameCancelRef, actions.rename.close, actions.rename.open);
  useFocusTrap(pickerDialogRef, pickerCancelRef, actions.destinationPicker.close, actions.destinationPicker.open);
  useFocusTrap(conflictDialogRef, conflictCancelRef, actions.conflict.cancel, actions.conflict.open);

  const internalTransfers = useMemo(
    () => actions.transfers.filter((transfer) => transfer.origin === 'same-profile').map(toInternalTransfer),
    [actions.transfers],
  );
  const backgroundTransfers = useMemo(
    () => actions.transfers.filter((transfer) => transfer.origin === 'cross-profile').map(toBackgroundTransfer),
    [actions.transfers],
  );

  useEffect(() => {
    if (!nextCursor || entriesLoading || loadingMore || !capabilities.browse) return;
    const target = sentinelRef.current;
    if (!target || typeof IntersectionObserver === 'undefined') return;

    const observer = new IntersectionObserver(
      (observedEntries) => {
        if (observedEntries[0]?.isIntersecting) {
          void loadMore();
        }
      },
      { rootMargin: '250px' }
    );

    observer.observe(target);
    return () => observer.disconnect();
  }, [capabilities.browse, entriesLoading, loadMore, loadingMore, nextCursor]);

  const selectProfile = (id: string) => {
    if (id === profileId) return;
    setIsBreadcrumbMenuOpen(false);
    onProfileChange(id);
  };

  const openDirectory = (entry: FileEntry) => {
    if (entry.kind !== 'directory' || !capabilities.browse) return;
    setIsBreadcrumbMenuOpen(false);
    setBreadcrumbs((current) => [...current, { ref: entry.ref, name: entry.name }]);
    setSelectedRefs(new Set());
    void loadEntries(entry.ref);
  };

  const openEntry = (entry: FileEntry) => {
    if (entry.kind === 'directory') {
      openDirectory(entry);
      return;
    }
    setPreviewEntry(entry);
  };

  const goUp = () => {
    if (breadcrumbs.length <= 1) return;
    setIsBreadcrumbMenuOpen(false);
    const parentBreadcrumbs = breadcrumbs.slice(0, -1);
    const parentRef = parentBreadcrumbs[parentBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(parentBreadcrumbs);
    setSelectedRefs(new Set());
    void loadEntries(parentRef);
  };

  const goToBreadcrumb = (index: number) => {
    if (index === breadcrumbs.length - 1) return;
    setIsBreadcrumbMenuOpen(false);
    const targetBreadcrumbs = breadcrumbs.slice(0, index + 1);
    const targetRef = targetBreadcrumbs[targetBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(targetBreadcrumbs);
    setSelectedRefs(new Set());
    void loadEntries(targetRef);
  };

  const toggleSelection = (ref: string) => {
    setSelectedRefs((current) => {
      const next = new Set(current);
      if (next.has(ref)) next.delete(ref); else next.add(ref);
      return next;
    });
  };

  const handleEntryClick = (entry: FileEntry, event?: React.MouseEvent | React.KeyboardEvent) => {
    if (isSelectionMode || event?.ctrlKey || event?.metaKey) {
      toggleSelection(entry.ref);
      return;
    }
    const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || entry.kind === 'file';
    if (isInteractive) {
      openEntry(entry);
    }
  };

  const toggleSelectAll = () => {
    setSelectedRefs((current) => {
      const next = new Set(current);
      if (allLoadedSelected) entries.forEach((entry) => next.delete(entry.ref));
      else entries.forEach((entry) => next.add(entry.ref));
      return next;
    });
  };

  const uploadCompleted = useCallback((completedProfileID: string) => {
    if (completedProfileID !== profileId) return;
    if (uploadRefreshTimeoutRef.current !== null) {
      window.clearTimeout(uploadRefreshTimeoutRef.current);
    }
    uploadRefreshTimeoutRef.current = window.setTimeout(() => {
      uploadRefreshTimeoutRef.current = null;
      void loadEntries(currentRef);
    }, 500);
  }, [currentRef, loadEntries, profileId]);

  const toggleMenu = (event: React.MouseEvent, entry: FileEntry) => {
    event.stopPropagation();
    if (menuState?.entry.ref === entry.ref) {
      setMenuState(null);
      return;
    }
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    setMenuState({
      entry,
      x: rect.right,
      y: rect.bottom + 4,
      align: 'right',
    });
  };

  const handleContextMenu = (event: React.MouseEvent, entry: FileEntry) => {
    if (!actions.permissions.hasEntryActions(entry)) return;
    event.preventDefault();
    event.stopPropagation();
    setMenuState({
      entry,
      x: event.clientX,
      y: event.clientY,
      align: 'left',
    });
  };

  const renderActionMenuPortal = () => {
    if (!menuState) return null;
    const entry = menuState.entry;
    const menuWidth = 180;
    const menuHeight = 220;
    let left = menuState.align === 'right' ? menuState.x - menuWidth : menuState.x;
    let top = menuState.y;

    if (left + menuWidth > window.innerWidth - 8) {
      left = window.innerWidth - menuWidth - 8;
    }
    if (left < 8) {
      left = 8;
    }
    if (top + menuHeight > window.innerHeight - 8) {
      top = Math.max(8, top - menuHeight - 8);
    }

    return createPortal(
      <div
        ref={menuRef}
        role="menu"
        aria-label={t('files.actionsFor', { name: entry.name })}
        style={{ position: 'fixed', top: `${top}px`, left: `${left}px`, zIndex: 50 }}
        className="w-44 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1.5 shadow-xl ring-1 ring-black/5 dark:ring-white/10 animate-in fade-in zoom-in-95 duration-100"
        onClick={(event) => event.stopPropagation()}
      >
        {actions.permissions.canRenameEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.rename.openDialog(entry);
            }}
          >
            <PencilIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.rename')}</span>
          </button>
        )}
        {actions.permissions.canCopyEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.destinationPicker.openSingle(entry, 'copy');
            }}
          >
            <ClipboardDocumentIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.copy')}</span>
          </button>
        )}
        {actions.permissions.canMoveEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.destinationPicker.openSingle(entry, 'move');
            }}
          >
            <ArrowRightIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.move')}</span>
          </button>
        )}
        {actions.permissions.canDownloadEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              void actions.download(entry);
            }}
          >
            <ArrowDownTrayIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.downloadAction')}</span>
          </button>
        )}
        {actions.permissions.canDeleteEntry(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-danger)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              actions.deleteEntry.openDialog(entry);
            }}
          >
            <TrashIcon className="h-4 w-4 text-[var(--color-danger)] shrink-0" aria-hidden="true" />
            <span>{t('files.deleteAction')}</span>
          </button>
        )}
      </div>,
      document.body
    );
  };

  const shouldCollapseBreadcrumbs = breadcrumbs.length > 3;
  const rootBreadcrumb = breadcrumbs[0];
  const collapsedBreadcrumbs = shouldCollapseBreadcrumbs ? breadcrumbs.slice(1, -2) : [];
  const parentBreadcrumb = shouldCollapseBreadcrumbs ? breadcrumbs[breadcrumbs.length - 2] : null;
  const currentBreadcrumbItem = shouldCollapseBreadcrumbs ? breadcrumbs[breadcrumbs.length - 1] : null;

  return (
    <section className="w-full space-y-5" aria-labelledby="file-manager-title">
      {/* Back Header */}
      <div className="flex items-center justify-between pb-4 border-b border-[var(--color-border)]/50">
        {onBack ? (
          <button
            type="button"
            onClick={onBack}
            className="ui-button-secondary flex items-center gap-2 px-3 py-2 text-sm font-medium hover:bg-[var(--color-bg-tertiary)]"
          >
            <ArrowLeftIcon className="w-4 h-4" aria-hidden="true" />
            {t('common.back')}
          </button>
        ) : <div />}
        <div className="flex items-center gap-2">
          <FolderIcon className="w-5 h-5 text-[var(--color-text-primary)]" aria-hidden="true" />
          <h1 id="file-manager-title" className="font-display font-semibold text-xl text-[var(--color-text-primary)] leading-none">{t('files.title')}</h1>
        </div>
      </div>

      {(error || actions.error) && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{error || actions.error}</p>}
      {initialPathFallback && <p className="ui-alert px-3 py-2 text-sm" role="status">{t('files.pathFallback')}</p>}

      <div className="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)] items-start">
        <aside className="ui-card flex flex-col justify-between p-3 min-h-[160px] self-start lg:sticky lg:top-20 z-10" aria-label={t('files.profiles')}>
          <div>
            <div className="mb-2 px-2">
              <h2 className="text-sm font-semibold">{t('files.profiles')}</h2>
            </div>
            {profilesLoading ? (
              <div className="px-2 py-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div>
            ) : profiles.length === 0 ? (
              <p className="ui-empty px-2 py-4 text-sm">{t('files.noProfiles')}</p>
            ) : (
              <ul className="space-y-1" aria-label={t('files.profiles')}>
                {profiles.map((profile) => (
                  <li key={profile.id}>
                    <button
                      type="button"
                      onClick={() => selectProfile(profile.id)}
                      aria-current={profile.id === profileId ? 'page' : undefined}
                      className={`flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm transition-colors ${profile.id === profileId ? 'bg-[var(--color-selection-bg)] text-[var(--color-selection-text)]' : 'text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)]'}`}
                    >
                      <ProviderIcon provider={profile.provider} className="h-4 w-4 shrink-0" />
                      <span className="truncate font-medium">{profile.name}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>

          {onOpenManager && (
            <div className="mt-3 border-t border-[var(--color-border)] pt-2">
              <button
                type="button"
                onClick={onOpenManager}
                className="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)]"
              >
                <WrenchScrewdriverIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                <span className="truncate">{t('files.manageProfiles')}</span>
              </button>
            </div>
          )}
        </aside>

        <div className="ui-card min-w-0 min-h-[560px] flex flex-col">
          {!selectedProfile ? (
            <div className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.selectProfile')}</div>
          ) : (
            <>
              {selectedEntries.length > 0 ? (
                <div
                  className="sticky top-16 lg:top-20 z-10 flex items-center justify-between gap-2 sm:gap-3 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2.5 sm:px-3 py-2 sm:py-2.5 rounded-t-[var(--radius-lg)] -mt-[1px] -mx-[1px] shadow-xs relative before:content-[''] before:hidden lg:before:block before:absolute before:bottom-full before:mb-[1px] before:left-0 before:right-0 before:h-5 before:bg-[var(--color-bg-primary)] before:pointer-events-none"
                  role="toolbar"
                  aria-label={t('files.selectionActions')}
                >
                  <div className="absolute inset-0 bg-[var(--color-selection-bg)]/15 pointer-events-none rounded-t-[inherit]" />
                  <div className="flex items-center gap-1.5 sm:gap-2 min-w-0 relative z-10">
                    <button
                      type="button"
                      onClick={() => setSelectedRefs(new Set())}
                      className="ui-icon-button p-1.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
                      title={t('files.clearSelection')}
                      aria-label={t('files.clearSelection')}
                    >
                      <XMarkIcon className="h-5 w-5" aria-hidden="true" />
                    </button>
                    <span className="text-sm font-semibold text-[var(--color-text-primary)] truncate" title={t('files.selectedCount', { count: selectedEntries.length })}>
                      <span className="hidden sm:inline">
                        {t('files.selectedCount', { count: selectedEntries.length })}
                      </span>
                      <span className="sm:hidden">
                        {t('files.selectedCountShort', { count: selectedEntries.length })}
                      </span>
                    </span>
                  </div>

                  <div className="flex items-center gap-1 sm:gap-2 shrink-0 relative z-10">
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchDownload || actions.batch.busy}
                      onClick={() => void actions.batch.downloadArchive()}
                      title={t('files.downloadArchive')}
                      aria-label={t('files.downloadArchive')}
                    >
                      <ArrowDownTrayIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.downloadArchive')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchCopy || actions.batch.busy}
                      onClick={() => actions.destinationPicker.openBatch('copy')}
                      title={t('files.copy')}
                      aria-label={t('files.copy')}
                    >
                      <ClipboardDocumentIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.copy')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchMove || actions.batch.busy}
                      onClick={() => actions.destinationPicker.openBatch('move')}
                      title={t('files.move')}
                      aria-label={t('files.move')}
                    >
                      <ArrowsRightLeftIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.move')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-danger inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!actions.permissions.canBatchDelete || actions.batch.busy}
                      onClick={actions.batch.openDeleteDialog}
                      title={t('files.deleteAction')}
                      aria-label={t('files.deleteAction')}
                    >
                      <TrashIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.deleteAction')}</span>
                    </button>
                  </div>
                </div>
              ) : (
                <div className="sticky top-16 lg:top-20 z-10 flex items-center justify-between gap-3 border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-3 rounded-t-[var(--radius-lg)] -mt-[1px] -mx-[1px] shadow-xs relative before:content-[''] before:hidden lg:before:block before:absolute before:bottom-full before:mb-[1px] before:left-0 before:right-0 before:h-5 before:bg-[var(--color-bg-primary)] before:pointer-events-none">
                  <div className="flex min-w-0 items-center gap-1.5 flex-1">
                    <button
                      type="button"
                      onClick={goUp}
                      disabled={!capabilities.browse || breadcrumbs.length <= 1 || entriesLoading}
                      className="ui-icon-button p-2 hover:bg-[var(--color-hover)] shrink-0 disabled:opacity-40 disabled:cursor-not-allowed"
                      aria-label={t('files.up')}
                      title={t('files.up')}
                    >
                      <ArrowUpIcon className="h-4 w-4" aria-hidden="true" />
                    </button>
                    <nav className="flex min-w-0 flex-nowrap items-center gap-1 text-sm overflow-hidden" aria-label={t('files.breadcrumb')}>
                      {shouldCollapseBreadcrumbs ? (
                        <>
                          {rootBreadcrumb && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                onClick={() => goToBreadcrumb(0)}
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 hover:bg-[var(--color-hover)]"
                                title={rootBreadcrumb.name}
                              >
                                {rootBreadcrumb.name}
                              </button>
                            </span>
                          )}
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          <div className="relative inline-flex items-center shrink-0" ref={breadcrumbMenuRef}>
                            <button
                              type="button"
                              onClick={() => setIsBreadcrumbMenuOpen((open) => !open)}
                              aria-expanded={isBreadcrumbMenuOpen}
                              aria-haspopup="menu"
                              aria-label={t('files.moreFolders')}
                              title={t('files.moreFolders')}
                              className="rounded px-1.5 py-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] transition-colors"
                            >
                              <EllipsisHorizontalIcon className="h-4 w-4" aria-hidden="true" />
                            </button>
                            {isBreadcrumbMenuOpen && (
                              <div
                                role="menu"
                                aria-label={t('files.moreFolders')}
                                className="absolute left-0 top-full mt-1 z-30 min-w-48 max-w-64 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-primary)] p-1 shadow-lg backdrop-blur-xs"
                              >
                                {collapsedBreadcrumbs.map((crumb, idx) => {
                                  const targetIndex = 1 + idx;
                                  return (
                                    <button
                                      key={crumb.ref ?? targetIndex}
                                      type="button"
                                      role="menuitem"
                                      onClick={() => {
                                        goToBreadcrumb(targetIndex);
                                        setIsBreadcrumbMenuOpen(false);
                                      }}
                                      className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm text-[var(--color-text-secondary)] hover:bg-[var(--color-hover)] hover:text-[var(--color-text-primary)] transition-colors"
                                      title={crumb.name}
                                    >
                                      <FolderIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                                      <span className="truncate">{crumb.name}</span>
                                    </button>
                                  );
                                })}
                              </div>
                            )}
                          </div>
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          {parentBreadcrumb && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                onClick={() => goToBreadcrumb(breadcrumbs.length - 2)}
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 hover:bg-[var(--color-hover)]"
                                title={parentBreadcrumb.name}
                              >
                                {parentBreadcrumb.name}
                              </button>
                            </span>
                          )}
                          <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />
                          {currentBreadcrumbItem && (
                            <span className="inline-flex min-w-0 items-center gap-1 shrink-0">
                              <button
                                type="button"
                                disabled
                                className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 font-semibold text-[var(--color-text-primary)]"
                                title={currentBreadcrumbItem.name}
                              >
                                {currentBreadcrumbItem.name}
                              </button>
                            </span>
                          )}
                        </>
                      ) : (
                        breadcrumbs.map((breadcrumb, index) => (
                          <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1 shrink-0">
                            {index > 0 && <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />}
                            <button
                              type="button"
                              onClick={() => goToBreadcrumb(index)}
                              disabled={index === breadcrumbs.length - 1}
                              className="max-w-28 sm:max-w-36 md:max-w-44 truncate rounded px-1 py-0.5 disabled:text-[var(--color-text-primary)] disabled:font-semibold hover:bg-[var(--color-hover)]"
                              title={breadcrumb.name}
                            >
                              {breadcrumb.name}
                            </button>
                          </span>
                        ))
                      )}
                    </nav>
                  </div>

                  <div className="flex items-center gap-2 shrink-0">
                    <FileUploadControl
                      apiUrl={apiUrl}
                      token={token}
                      profileId={profileId}
                      parentRef={currentRef}
                      capabilities={capabilities}
                      disabled={entriesLoading}
                      onCompleted={uploadCompleted}
                      backgroundTransfers={backgroundTransfers}
                      onCancelBackgroundTransfer={(transferId) => void actions.cancelTransfer(transferId)}
                      onDismissBackgroundTransfer={actions.dismissTransfer}
                      internalTransfers={internalTransfers}
                      onCancelInternalTransfer={(transferId) => void actions.cancelTransfer(transferId)}
                      onDismissInternalTransfer={actions.dismissTransfer}
                    />
                    <button
                      type="button"
                      onClick={actions.createFolder.openDialog}
                      disabled={entriesLoading || !actions.permissions.canCreateFolder}
                      className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
                      title={!actions.permissions.canCreateFolder ? t('files.mkdirUnavailable') : t('files.newFolder')}
                      aria-label={t('files.newFolder')}
                    >
                      <FolderPlusIcon className="h-4 w-4" aria-hidden="true" />
                      <span className="hidden sm:inline">{t('files.newFolder')}</span>
                    </button>
                    <div className="flex items-center rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-0.5" role="group" aria-label={t('files.viewMode')}>
                      <button
                        type="button"
                        onClick={() => handleViewModeChange('list')}
                        aria-pressed={viewMode === 'list'}
                        aria-label={t('files.viewList')}
                        title={t('files.viewList')}
                        className={`p-1.5 rounded-md transition-colors ${
                          viewMode === 'list'
                            ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] shadow-xs'
                            : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                        }`}
                      >
                        <ListBulletIcon className="h-4 w-4" aria-hidden="true" />
                      </button>
                      <button
                        type="button"
                        onClick={() => handleViewModeChange('grid')}
                        aria-pressed={viewMode === 'grid'}
                        aria-label={t('files.viewGrid')}
                        title={t('files.viewGrid')}
                        className={`p-1.5 rounded-md transition-colors ${
                          viewMode === 'grid'
                            ? 'bg-[var(--color-bg-tertiary)] text-[var(--color-text-primary)] shadow-xs'
                            : 'text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]'
                        }`}
                      >
                        <Squares2X2Icon className="h-4 w-4" aria-hidden="true" />
                      </button>
                    </div>
                    <button
                      type="button"
                      onClick={refresh}
                      disabled={!capabilities.browse}
                      className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                      aria-label={t('files.refresh')}
                      title={t('files.refresh')}
                    >
                      <ArrowPathIcon className="h-4 w-4" aria-hidden="true" />
                    </button>
                  </div>
                </div>
              )}
              {actions.batch.results.length > 0 && (
                <div className="mx-3 mt-3 flex items-center justify-between gap-2 ui-alert px-3 py-2 text-sm" role="status">
                  <span>
                    {t('files.batchResultSummary', {
                      success: actions.batch.results.filter((item) => ['deleted', 'copied', 'moved'].includes(item.status)).length,
                      failed: actions.batch.results.filter((item) => item.status === 'failed').length,
                    })}
                  </span>
                  <button
                    type="button"
                    onClick={actions.batch.dismissResults}
                    className="ui-icon-button p-0.5 hover:bg-[var(--color-hover)] text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)] shrink-0"
                    aria-label={t('common.cancel')}
                    title={t('common.cancel')}
                  >
                    <XMarkIcon className="h-4 w-4" aria-hidden="true" />
                  </button>
                </div>
              )}

              {!capabilities.browse && !entriesLoading ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.listUnavailable')}</p>
              ) : entriesLoading ? (
                <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>
              ) : entries.length === 0 ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.emptyDirectory')}</p>
              ) : viewMode === 'list' ? (
                <div className="overflow-x-auto flex-1">
                  <table className="ui-table w-full table-fixed text-sm">
                    <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
                      <tr>
                        <th scope="col" className="w-10 px-3 py-2 font-medium"><input ref={selectAllRef} type="checkbox" checked={allLoadedSelected} onChange={toggleSelectAll} aria-label={t('files.selectAll')} /></th>
                        <th
                          scope="col"
                          aria-sort={sortField === 'name' ? (sortDirection === 'asc' ? 'ascending' : 'descending') : 'none'}
                          className="px-3 py-2 font-medium"
                        >
                          {renderSortHeader('name', t('files.name'), t('files.sortByName'))}
                        </th>
                        <th
                          scope="col"
                          aria-sort={sortField === 'size' ? (sortDirection === 'asc' ? 'ascending' : 'descending') : 'none'}
                          className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 font-medium whitespace-nowrap shrink-0"
                        >
                          {renderSortHeader('size', t('files.size'), t('files.sortBySize'))}
                        </th>
                        <th
                          scope="col"
                          aria-sort={sortField === 'modified' ? (sortDirection === 'asc' ? 'ascending' : 'descending') : 'none'}
                          className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 font-medium whitespace-nowrap shrink-0"
                        >
                          {renderSortHeader('modified', t('files.modified'), t('files.sortByModified'))}
                        </th>
                        <th scope="col" className="w-12 sm:w-16 px-2 sm:px-3 py-2 font-medium text-right shrink-0"><span className="sr-only">{t('files.actions')}</span></th>
                      </tr>
                    </thead>
                    <tbody>
                      {sortedEntries.map((entry) => {
                        const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || entry.kind === 'file';
                        const isSelected = selectedRefs.has(entry.ref);
                        return (
                          <tr
                            key={entry.ref}
                            aria-selected={isSelectionMode ? isSelected : undefined}
                            onClick={(event) => handleEntryClick(entry, event)}
                            onContextMenu={(event) => handleContextMenu(event, entry)}
                            className={`border-t border-[var(--color-border)] transition-colors ${
                              isSelected
                                ? 'bg-[var(--color-selection-bg)]/20 hover:bg-[var(--color-selection-bg)]/30'
                                : 'hover:bg-[var(--color-hover)]'
                            } ${isInteractive || isSelectionMode ? 'cursor-pointer' : ''}`}
                          >
                            <td className="w-10 px-3 py-2"><input type="checkbox" checked={isSelected} onClick={(event) => event.stopPropagation()} onChange={() => toggleSelection(entry.ref)} aria-label={t('files.selectEntry', { name: entry.name })} /></td>
                            <td data-label={t('files.name')} className="px-3 py-2 min-w-0 max-w-0">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  handleEntryClick(entry, e);
                                }}
                                disabled={!isSelectionMode && entry.kind === 'directory' && (!capabilities.browse || entriesLoading)}
                                className="inline-flex w-full items-center gap-2.5 sm:gap-3 min-w-0 text-left ui-link disabled:cursor-not-allowed disabled:opacity-55"
                                title={entry.name}
                              >
                                <div className="relative w-9 h-9 sm:w-10 sm:h-10 shrink-0 rounded-lg overflow-hidden bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)]/60 flex items-center justify-center">
                                  <FileThumbnail
                                    apiUrl={apiUrl}
                                    token={token}
                                    profileId={profileId}
                                    entry={entry}
                                    thumbnailsEnabled={capabilities.thumbnails}
                                    size="sm"
                                    className="w-full h-full flex items-center justify-center"
                                    imageClassName="w-full h-full object-cover"
                                    fallbackIconClassName="h-5 w-5"
                                  />
                                </div>
                                <div className="min-w-0 flex-1">
                                  <span className="truncate block font-medium text-sm text-[var(--color-text-primary)]">{entry.name}</span>
                                  <div className="flex items-center gap-1.5 text-[11px] text-[var(--color-text-secondary)] sm:hidden mt-0.5 truncate font-normal">
                                    <span>{entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}</span>
                                    {entry.modified_at && (
                                      <>
                                        <span aria-hidden="true">•</span>
                                        <span className="truncate">{formatDateTime(entry.modified_at)}</span>
                                      </>
                                    )}
                                  </div>
                                </div>
                              </button>
                            </td>
                            <td data-label={t('files.size')} className="hidden sm:table-cell w-28 sm:w-32 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                            </td>
                            <td data-label={t('files.modified')} className="hidden sm:table-cell w-36 sm:w-44 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.modified_at ? formatDateTime(entry.modified_at) : t('common.unspecified')}
                            </td>
                            <td data-label={t('files.actions')} className="w-12 sm:w-16 px-2 sm:px-3 py-2 text-right whitespace-nowrap shrink-0">
                              {actions.permissions.hasEntryActions(entry) && (
                                <button
                                  type="button"
                                  className="ui-icon-button p-2 hover:bg-[var(--color-hover)]"
                                  aria-label={t('files.actionsFor', { name: entry.name })}
                                  aria-haspopup="menu"
                                  aria-expanded={menuState?.entry.ref === entry.ref}
                                  onClick={(event) => toggleMenu(event, entry)}
                                >
                                  <EllipsisVerticalIcon className="h-5 w-5" aria-hidden="true" />
                                </button>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              ) : (
                <div className="p-4 grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-3 flex-1 auto-rows-max" role="grid" aria-label={t('files.title')}>
                  {sortedEntries.map((entry) => {
                    const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || entry.kind === 'file';
                    const isSelected = selectedRefs.has(entry.ref);

                    return (
                      <div
                        key={entry.ref}
                        role="gridcell"
                        aria-selected={isSelectionMode ? isSelected : undefined}
                        tabIndex={isInteractive || isSelectionMode ? 0 : undefined}
                        onClick={(event) => handleEntryClick(entry, event)}
                        onContextMenu={(event) => handleContextMenu(event, entry)}
                        onKeyDown={(e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault();
                            handleEntryClick(entry, e);
                          }
                        }}
                        className={`group isolate relative flex flex-col justify-between rounded-xl border transition-all ${
                          isSelected
                            ? 'border-[var(--color-text-primary)] ring-2 ring-[var(--color-text-primary)]/20 bg-[var(--color-selection-bg)]/20 shadow-xs'
                            : 'border-[var(--color-border)] bg-[var(--color-bg-secondary)] hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))] hover:shadow-xs'
                        } ${
                          isInteractive || isSelectionMode ? 'cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]' : 'opacity-70'
                        }`}
                        title={entry.name}
                      >
                        <input type="checkbox" className="absolute left-2 top-2 z-10" checked={isSelected} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()} onChange={() => toggleSelection(entry.ref)} aria-label={t('files.selectEntry', { name: entry.name })} />
                        {actions.permissions.hasEntryActions(entry) && (
                          <div className="absolute right-2 top-2 z-10 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
                            <button
                              type="button"
                              className="ui-icon-button p-1.5 rounded-lg bg-[var(--color-bg-primary)]/80 backdrop-blur-xs border border-[var(--color-border)]/60 shadow-xs hover:bg-[var(--color-hover)]"
                              aria-label={t('files.actionsFor', { name: entry.name })}
                              aria-haspopup="menu"
                              aria-expanded={menuState?.entry.ref === entry.ref}
                              onClick={(event) => toggleMenu(event, entry)}
                            >
                              <EllipsisVerticalIcon className="h-4 w-4" aria-hidden="true" />
                            </button>
                          </div>
                        )}
                        <div className="relative w-full aspect-[4/3] flex items-center justify-center bg-[var(--color-bg-tertiary)]/40 overflow-hidden">
                          <FileThumbnail
                            apiUrl={apiUrl}
                            token={token}
                            profileId={profileId}
                            entry={entry}
                            thumbnailsEnabled={capabilities.thumbnails}
                            size="lg"
                            className="w-full h-full flex items-center justify-center"
                            imageClassName="w-full h-full object-cover"
                            fallbackIconClassName="h-12 w-12 drop-shadow-xs"
                          />
                        </div>
                        <div className="w-full min-w-0 p-2.5 flex flex-col text-left border-t border-[var(--color-border)]/60 bg-[var(--color-bg-secondary)]">
                          <span className="w-full truncate text-xs font-medium text-[var(--color-text-primary)]">
                            {entry.name}
                          </span>
                          <span className="text-[11px] text-[var(--color-text-secondary)] mt-0.5 truncate">
                            {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                          </span>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}

              {nextCursor && (
                <div ref={sentinelRef} className="flex justify-center p-4 border-t border-[var(--color-border)]">
                  {loadingMore ? (
                    <LoadingIndicator label={t('common.loading')} size="sm" />
                  ) : (
                    <button
                      type="button"
                      onClick={() => void loadMore()}
                      className="ui-button-secondary inline-flex items-center gap-1.5 px-3 py-1.5 text-xs text-[var(--color-text-secondary)] hover:text-[var(--color-text-primary)]"
                    >
                      <ArrowPathIcon className="h-3.5 w-3.5" aria-hidden="true" />
                      {t('files.loadMore')}
                    </button>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
      {actions.createFolder.open &&
        createPortal(
          <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
            <div
              ref={createDirDialogRef}
              role="dialog"
              aria-modal="true"
              aria-labelledby="create-dir-title"
              tabIndex={-1}
              className="ui-card w-full max-w-md p-5"
            >
              <h2 id="create-dir-title" className="text-lg font-semibold text-[var(--color-text-primary)]">
                {t('files.newFolder')}
              </h2>
              {actions.createFolder.error && (
                <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">
                  {actions.createFolder.error}
                </p>
              )}
              <form onSubmit={(event) => { event.preventDefault(); void actions.createFolder.submit(); }} className="mt-4 space-y-4">
                <div>
                  <label htmlFor="new-folder-name-input" className="block text-xs font-semibold text-[var(--color-text-secondary)] uppercase tracking-wider mb-1">
                    {t('files.folderName')}
                  </label>
                  <input
                    id="new-folder-name-input"
                    type="text"
                    value={actions.createFolder.name}
                    onChange={(e) => actions.createFolder.setName(e.target.value)}
                    placeholder={t('files.folderNamePlaceholder')}
                    disabled={actions.createFolder.busy}
                    className="ui-input w-full py-2 px-3 text-sm"
                    autoFocus
                  />
                </div>
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    ref={createDirCancelRef}
                    type="button"
                    onClick={actions.createFolder.close}
                    disabled={actions.createFolder.busy}
                    className="ui-button-secondary px-3 py-2 text-sm"
                  >
                    {t('common.cancel')}
                  </button>
                  <button
                    type="submit"
                    disabled={!actions.createFolder.name.trim() || actions.createFolder.busy}
                    className="ui-button-primary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {actions.createFolder.busy ? (
                      <>
                        <LoadingIndicator label={t('common.loading')} size="sm" />
                        <span>{t('common.loading')}</span>
                      </>
                    ) : (
                      <span>{t('common.create')}</span>
                    )}
                  </button>
                </div>
              </form>
            </div>
          </div>,
          document.body
        )}
      {actions.deleteEntry.entry && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={deleteDialogRef} role="dialog" aria-modal="true" aria-labelledby="delete-entry-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="delete-entry-title" className="text-lg font-semibold text-[var(--color-text-primary)]">{t('files.deleteTitle')}</h2>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">
              {actions.deleteEntry.entry.kind === 'file' ? t('files.deleteFileWarning', { name: actions.deleteEntry.entry.name }) : actions.deleteEntry.recursive ? t('files.deleteRecursiveWarning', { name: actions.deleteEntry.entry.name }) : t('files.deleteDirectoryWarning', { name: actions.deleteEntry.entry.name })}
            </p>
            {actions.deleteEntry.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{actions.deleteEntry.error}</p>}
            {actions.deleteEntry.entry.kind === 'directory' && actions.deleteEntry.recursive && !capabilities.delete_empty_directory && capabilities.delete_recursive_directory && <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.deleteOnlyRecursiveSupported')}</p>}
            <div className="mt-5 flex justify-end gap-2">
              <button ref={deleteCancelRef} type="button" onClick={actions.deleteEntry.close} disabled={actions.deleteEntry.busy} className="ui-button-secondary px-3 py-2 text-sm">{t('common.cancel')}</button>
              <button type="button" onClick={() => void actions.deleteEntry.confirm()} disabled={actions.deleteEntry.busy || (actions.deleteEntry.entry.kind === 'directory' && !actions.deleteEntry.recursive && !capabilities.delete_empty_directory)} className="ui-button-danger px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50">
                {actions.deleteEntry.busy ? t('common.loading') : actions.deleteEntry.recursive ? t('files.deleteRecursively') : t('files.deleteConfirm')}
              </button>
            </div>
          </div>
        </div>, document.body)}
      {actions.batch.deleteDialogOpen && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={batchDeleteDialogRef} role="dialog" aria-modal="true" aria-labelledby="batch-delete-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="batch-delete-title" className="text-lg font-semibold">{t('files.batchDeleteTitle')}</h2>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteWarning', { count: actions.batch.itemCount })}</p>
            {actions.batch.hasDirectories && <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteRecursive')}</p>}
            <div className="mt-5 flex justify-end gap-2"><button ref={batchDeleteCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={actions.batch.deleting} onClick={actions.batch.closeDeleteDialog}>{t('common.cancel')}</button><button type="button" className="ui-button-danger px-3 py-2 text-sm" disabled={actions.batch.deleting} onClick={() => void actions.batch.confirmDelete()}>{actions.batch.deleting ? t('common.loading') : t('files.deleteRecursively')}</button></div>
          </div>
        </div>, document.body)}
      {actions.rename.open && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={renameDialogRef} role="dialog" aria-modal="true" aria-labelledby="rename-entry-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="rename-entry-title" className="text-lg font-semibold">{t('files.renameTitle')}</h2>
            {actions.rename.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{actions.rename.error}</p>}
            <form className="mt-4 space-y-4" onSubmit={(event) => { event.preventDefault(); void actions.rename.submit(); }}>
              <label className="block text-sm font-medium" htmlFor="rename-entry-name">{t('files.name')}</label>
              <input id="rename-entry-name" className="ui-input w-full px-3 py-2" value={actions.rename.name} disabled={actions.rename.busy} onChange={(event) => actions.rename.setName(event.target.value)} autoFocus />
              <div className="flex justify-end gap-2"><button ref={renameCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={actions.rename.busy} onClick={actions.rename.close}>{t('common.cancel')}</button><button type="submit" className="ui-button-primary px-3 py-2 text-sm" disabled={!actions.rename.name.trim() || actions.rename.busy}>{actions.rename.busy ? t('files.mutating') : t('files.rename')}</button></div>
            </form>
          </div>
        </div>, document.body)}
      {actions.destinationPicker.open && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={pickerDialogRef} role="dialog" aria-modal="true" aria-labelledby="destination-picker-title" tabIndex={-1} className="ui-card flex max-h-[85vh] w-full max-w-lg flex-col p-5">
            <h2 id="destination-picker-title" className="text-lg font-semibold">{actions.destinationPicker.operation === 'copy' ? t('files.copyTitle') : t('files.moveTitle')}</h2>
            <p className="mt-1 text-sm text-[var(--color-text-secondary)]">{actions.destinationPicker.entry ? t('files.destinationPickerDescription', { name: actions.destinationPicker.entry.name }) : t('files.batchDestinationPickerDescription', { count: actions.destinationPicker.itemCount })}</p>
            {actions.destinationPicker.error && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{actions.destinationPicker.error}</p>}
            <label className="mt-3 block text-sm font-medium" htmlFor="destination-profile">{t('files.destinationProfile')}</label>
            <select id="destination-profile" className="ui-input mt-1 w-full px-3 py-2" value={actions.destinationPicker.profileId} disabled={actions.destinationPicker.loading || actions.destinationPicker.busy} onChange={(event) => actions.destinationPicker.selectProfile(event.target.value)}>
              {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
            </select>
            <nav className="mt-3 flex flex-wrap gap-1 text-sm" aria-label={t('files.destinationBreadcrumb')}>
              {actions.destinationPicker.breadcrumbs.map((breadcrumb, index) => <button key={breadcrumb.ref ?? 'root'} type="button" className="rounded px-1 py-0.5 hover:bg-[var(--color-hover)]" disabled={actions.destinationPicker.loading || index === actions.destinationPicker.breadcrumbs.length - 1} onClick={() => actions.destinationPicker.goToBreadcrumb(index)}>{breadcrumb.name}{index < actions.destinationPicker.breadcrumbs.length - 1 ? ' /' : ''}</button>)}
            </nav>
            <div className="mt-3 min-h-36 overflow-y-auto rounded border border-[var(--color-border)]">
               {actions.destinationPicker.loading ? <div className="p-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div> : actions.destinationPicker.hasInvalidDestination ? <p className="p-3 text-sm text-[var(--color-text-secondary)]">{t('files.noValidDestination')}</p> : actions.destinationPicker.availableEntries.map((entry) => <button key={entry.ref} type="button" className="flex w-full items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-left text-sm hover:bg-[var(--color-hover)]" onClick={() => actions.destinationPicker.openFolder(entry)}><FolderIcon className="h-4 w-4 ui-file-folder" aria-hidden="true" />{entry.name}</button>)}
            </div>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.destinationSelected', { name: actions.destinationPicker.breadcrumbs[actions.destinationPicker.breadcrumbs.length - 1]?.name })}</p>
            <div className="mt-5 flex justify-end gap-2"><button ref={pickerCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={actions.destinationPicker.busy} onClick={actions.destinationPicker.close}>{t('common.cancel')}</button><button type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={actions.destinationPicker.busy} onClick={() => void actions.destinationPicker.submit()}>{actions.destinationPicker.busy ? t('files.mutating') : actions.destinationPicker.operation === 'copy' ? t('files.copy') : t('files.move')}</button></div>
          </div>
        </div>, document.body)}
      {actions.conflict.open && actions.conflict.strategies && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={conflictDialogRef} role="dialog" aria-modal="true" aria-labelledby="mutation-conflict-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="mutation-conflict-title" className="text-lg font-semibold">{t('files.conflictTitle')}</h2><p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.conflictDescription')}</p>
            <div className="mt-4 flex flex-wrap justify-end gap-2"><button ref={conflictCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={actions.conflict.busy} onClick={actions.conflict.cancel}>{t('common.cancel')}</button>{actions.conflict.strategies.map((strategy) => <button key={strategy} type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={actions.conflict.busy} onClick={() => void actions.conflict.resolve(strategy)}>{t(`files.conflict${strategy[0]}${strategy.slice(1).toLowerCase()}`)}</button>)}</div>
          </div>
        </div>, document.body)}
      {previewEntry && (
        <Suspense fallback={null}>
          <FilePreviewDialog
            apiUrl={apiUrl}
            token={token}
            profileId={profileId}
            entry={previewEntry}
            entries={sortedEntries}
            supportsThumbnails={capabilities.thumbnails}
            onNavigate={(entry) => setPreviewEntry(entry)}
            onClose={() => setPreviewEntry(null)}
            onDownload={(entry) => void actions.download(entry)}
          />
        </Suspense>
      )}
      {renderActionMenuPortal()}
    </section>
  );
}
