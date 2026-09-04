import { useCallback, useEffect, useRef, useState, lazy, Suspense } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowDownTrayIcon,
  ArrowLeftIcon,
  ArrowPathIcon,
  ArrowRightIcon,
  ArrowUpIcon,
  ChevronRightIcon,
	ClipboardDocumentIcon,
	EllipsisVerticalIcon,
  FolderIcon,
  FolderPlusIcon,
  ListBulletIcon,
  PencilIcon,
  ProviderIcon,
  Squares2X2Icon,
  TrashIcon,
  WrenchScrewdriverIcon,
} from '../icons';
import { useTranslation } from 'react-i18next';
import { copyFileEntry, createDirectory, deleteFileEntry, getFileCapabilities, listFileEntries, createDownloadTicket, moveFileEntry, renameFileEntry, type FileBreadcrumb, type FileCapabilities, type FileEntry, type FileMutationConflictStrategy } from '../../api/files';
import { listConnectionProfiles, type ConnectionProfilePublic } from '../../api/profiles';
import { LoadingIndicator } from '../LoadingIndicator';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { FileUploadControl } from './FileUploadControl';
import { FileThumbnail } from './FileThumbnail';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

type Breadcrumb = {
  ref: string | null;
  name: string;
};

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
  upload: false,
  mkdir: false,
  rename: false,
  move: false,
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
  const [breadcrumbs, setBreadcrumbs] = useState<Breadcrumb[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState('');
  const [downloadingRef, setDownloadingRef] = useState<string | null>(null);
  const [previewEntry, setPreviewEntry] = useState<FileEntry | null>(null);
  const [isCreateDirOpen, setIsCreateDirOpen] = useState(false);
  const [newDirName, setNewDirName] = useState('');
  const [creatingDir, setCreatingDir] = useState(false);
  const [createDirError, setCreateDirError] = useState('');
  const [deleteEntry, setDeleteEntry] = useState<FileEntry | null>(null);
  const [deletingEntry, setDeletingEntry] = useState(false);
  const [deleteRecursive, setDeleteRecursive] = useState(false);
  const [deleteError, setDeleteError] = useState('');
  const [menuState, setMenuState] = useState<{
    entry: FileEntry;
    x: number;
    y: number;
    align?: 'left' | 'right';
  } | null>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [renameEntry, setRenameEntry] = useState<FileEntry | null>(null);
  const [renameName, setRenameName] = useState('');
  const [mutationEntry, setMutationEntry] = useState<FileEntry | null>(null);
  const [mutationOperation, setMutationOperation] = useState<'copy' | 'move' | null>(null);
  const [pickerBreadcrumbs, setPickerBreadcrumbs] = useState<Breadcrumb[]>([]);
  const [pickerEntries, setPickerEntries] = useState<FileEntry[]>([]);
  const [pickerLoading, setPickerLoading] = useState(false);
  const [mutationBusy, setMutationBusy] = useState(false);
  const [mutationError, setMutationError] = useState('');
  const [conflictStrategies, setConflictStrategies] = useState<FileMutationConflictStrategy[] | null>(null);
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
  const createDirDialogRef = useRef<HTMLDivElement>(null);
  const createDirCancelRef = useRef<HTMLButtonElement>(null);
  const deleteDialogRef = useRef<HTMLDivElement>(null);
  const deleteCancelRef = useRef<HTMLButtonElement>(null);
  const profileRequestRef = useRef<AbortController | null>(null);
  const entriesRequestRef = useRef<AbortController | null>(null);
  const deleteRequestRef = useRef<AbortController | null>(null);
  const mutationRequestRef = useRef<AbortController | null>(null);
  const pickerRequestRef = useRef<AbortController | null>(null);
  const renameDialogRef = useRef<HTMLDivElement>(null);
  const renameCancelRef = useRef<HTMLButtonElement>(null);
  const pickerDialogRef = useRef<HTMLDivElement>(null);
  const pickerCancelRef = useRef<HTMLButtonElement>(null);
  const conflictDialogRef = useRef<HTMLDivElement>(null);
  const conflictCancelRef = useRef<HTMLButtonElement>(null);
  const latestEntriesRequestRef = useRef(0);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const uploadRefreshTimeoutRef = useRef<number | null>(null);

  useFocusTrap(createDirDialogRef, createDirCancelRef, () => {
    if (!creatingDir) {
      setIsCreateDirOpen(false);
      setNewDirName('');
      setCreateDirError('');
    }
  }, isCreateDirOpen);
  useFocusTrap(deleteDialogRef, deleteCancelRef, () => {
    if (!deletingEntry) {
      setDeleteEntry(null);
      setDeleteError('');
      setDeleteRecursive(false);
    }
  }, deleteEntry !== null);
  const closeMutationDialogs = useCallback(() => {
    if (mutationBusy) return;
    mutationRequestRef.current?.abort();
    pickerRequestRef.current?.abort();
    setRenameEntry(null); setMutationEntry(null); setMutationOperation(null); setMutationError(''); setConflictStrategies(null);
  }, [mutationBusy]);
  useFocusTrap(renameDialogRef, renameCancelRef, closeMutationDialogs, renameEntry !== null);
  useFocusTrap(pickerDialogRef, pickerCancelRef, closeMutationDialogs, mutationEntry !== null && conflictStrategies === null);
  useFocusTrap(conflictDialogRef, conflictCancelRef, closeMutationDialogs, conflictStrategies !== null);

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

  const selectedProfile = profiles.find((profile) => profile.id === profileId) ?? null;
  const currentBreadcrumb = breadcrumbs[breadcrumbs.length - 1];
  const currentRef = currentBreadcrumb?.ref ?? null;

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
      deleteRequestRef.current?.abort();
      mutationRequestRef.current?.abort();
      pickerRequestRef.current?.abort();
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
    onProfileChange(id);
  };

  const openDirectory = (entry: FileEntry) => {
    if (entry.kind !== 'directory' || !capabilities.browse) return;
    setBreadcrumbs((current) => [...current, { ref: entry.ref, name: entry.name }]);
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
    const parentBreadcrumbs = breadcrumbs.slice(0, -1);
    const parentRef = parentBreadcrumbs[parentBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(parentBreadcrumbs);
    void loadEntries(parentRef);
  };

  const refresh = () => {
    if (!capabilities.browse) return;
    void loadEntries(currentRef);
  };

  const goToBreadcrumb = (index: number) => {
    if (index === breadcrumbs.length - 1) return;
    const targetBreadcrumbs = breadcrumbs.slice(0, index + 1);
    const targetRef = targetBreadcrumbs[targetBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(targetBreadcrumbs);
    void loadEntries(targetRef);
  };

  const download = async (entry: FileEntry) => {
    if (entry.kind === 'directory' || !capabilities.download || downloadingRef) return;
    const controller = new AbortController();
    setDownloadingRef(entry.ref);
    setError('');
    const result = await createDownloadTicket(apiUrl, token, profileId, entry.ref, controller.signal);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      window.location.assign(new URL(result.data.download_url, apiUrl).toString());
    }
    setDownloadingRef(null);
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

  const handleCreateDirectory = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = newDirName.trim();
    if (!trimmed || creatingDir || !capabilities.mkdir) return;
    setCreatingDir(true);
    setCreateDirError('');
    const result = await createDirectory(apiUrl, token, profileId, trimmed, currentRef);
    if (result.ok === false) {
      setCreateDirError(translateApiError(result.errorCode));
      setCreatingDir(false);
    } else {
      setCreatingDir(false);
      setIsCreateDirOpen(false);
      setNewDirName('');
      setCreateDirError('');
      void loadEntries(currentRef);
    }
  };

  const requestDelete = (entry: FileEntry) => {
    if (!entry.allowed_actions.includes('delete') || deletingEntry) return;
    setDeleteEntry(entry);
    setDeleteRecursive(entry.kind === 'directory' && !capabilities.delete_empty_directory && capabilities.delete_recursive_directory);
    setDeleteError('');
  };

  const confirmDelete = async () => {
    if (!deleteEntry || deletingEntry) return;
    setDeletingEntry(true);
    setDeleteError('');
    deleteRequestRef.current?.abort();
    const controller = new AbortController();
    deleteRequestRef.current = controller;
    const result = await deleteFileEntry(apiUrl, token, profileId, deleteEntry.ref, deleteRecursive, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok === false) {
      if (result.errorCode === 'FILES_DIRECTORY_NOT_EMPTY' && deleteEntry.kind === 'directory' && capabilities.delete_recursive_directory) {
        setDeleteRecursive(true);
        setDeleteError(t('files.deleteDirectoryNotEmpty'));
      } else {
        setDeleteError(translateApiError(result.errorCode));
      }
      setDeletingEntry(false);
      return;
    }
    if (previewEntry?.ref === deleteEntry.ref) setPreviewEntry(null);
    setDeleteEntry(null);
    setDeleteRecursive(false);
    setDeletingEntry(false);
    void loadEntries(currentRef);
  };

  const loadPickerEntries = useCallback(async (parentRef: string | null) => {
    pickerRequestRef.current?.abort();
    const controller = new AbortController();
    pickerRequestRef.current = controller;
    setPickerLoading(true);
    const result = await listFileEntries(apiUrl, token, profileId, parentRef, undefined, controller.signal);
    if (!controller.signal.aborted) {
      setPickerEntries(result.ok ? result.data.entries.filter((entry) => entry.kind === 'directory') : []);
      if (result.ok === false) setMutationError(translateApiError(result.errorCode));
      setPickerLoading(false);
    }
  }, [apiUrl, profileId, token, translateApiError]);

  const startRename = (entry: FileEntry) => {
    setMenuState(null); setRenameEntry(entry); setRenameName(entry.name); setMutationError(''); setConflictStrategies(null);
  };

  const startDestinationPicker = (entry: FileEntry, operation: 'copy' | 'move') => {
    setMenuState(null); setMutationEntry(entry); setMutationOperation(operation); setMutationError(''); setConflictStrategies(null);
    const initial = [{ ref: null, name: selectedProfile?.name ?? t('files.title') }];
    setPickerBreadcrumbs(initial); setPickerEntries([]); void loadPickerEntries(null);
  };

  const executeMutation = async (strategy?: FileMutationConflictStrategy) => {
    const entry = renameEntry ?? mutationEntry;
    if (!entry || mutationBusy) return;
    const operation = renameEntry ? 'rename' : mutationOperation;
    if (!operation) return;
    const newName = renameName.trim();
    if (operation === 'rename' && !newName) {
      setMutationError(t('files.nameRequired'));
      return;
    }
    mutationRequestRef.current?.abort();
    const controller = new AbortController();
    mutationRequestRef.current = controller;
    setMutationBusy(true); setMutationError('');
    const destinationRef = pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.ref ?? null;
    const result = operation === 'rename'
      ? await renameFileEntry(apiUrl, token, profileId, entry.ref, newName, strategy, controller.signal)
      : operation === 'copy'
        ? await copyFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal)
        : await moveFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal);
    if (controller.signal.aborted) return;
    setMutationBusy(false);
    if (result.ok === false) {
      const options = (result.data as { conflict_strategies?: FileMutationConflictStrategy[] } | undefined)?.conflict_strategies;
      if (result.errorCode === 'FILES_CONFLICT' && options?.length) {
        setConflictStrategies(options);
	      } else {
	        setMutationError(result.errorCode === 'FILES_PARTIAL_OPERATION' ? t('files.partialWarning') : translateApiError(result.errorCode));
      }
      if (result.errorCode === 'FILES_PARTIAL_OPERATION') void loadEntries(currentRef);
      return;
    }
    if (previewEntry?.ref === entry.ref && operation === 'move') setPreviewEntry(null);
    setRenameEntry(null); setMutationEntry(null); setMutationOperation(null); setConflictStrategies(null); setMutationError('');
    void loadEntries(currentRef);
  };

  const canRename = (entry: FileEntry) => capabilities.rename && entry.allowed_actions.includes('rename');
  const canCopy = (entry: FileEntry) => capabilities.move && entry.allowed_actions.includes('copy');
  const canMove = (entry: FileEntry) => capabilities.move && entry.allowed_actions.includes('move');
  const hasEntryActions = (entry: FileEntry) => canRename(entry) || canCopy(entry) || canMove(entry) || (entry.kind === 'file' && capabilities.download && entry.allowed_actions.includes('download')) || entry.allowed_actions.includes('delete');

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
    if (!hasEntryActions(entry)) return;
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
        {canRename(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => startRename(entry)}
          >
            <PencilIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.rename')}</span>
          </button>
        )}
        {canCopy(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => startDestinationPicker(entry, 'copy')}
          >
            <ClipboardDocumentIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.copy')}</span>
          </button>
        )}
        {canMove(entry) && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => startDestinationPicker(entry, 'move')}
          >
            <ArrowRightIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.move')}</span>
          </button>
        )}
        {entry.kind === 'file' && capabilities.download && entry.allowed_actions.includes('download') && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-text-primary)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              void download(entry);
            }}
          >
            <ArrowDownTrayIcon className="h-4 w-4 text-[var(--color-text-secondary)] shrink-0" aria-hidden="true" />
            <span>{t('files.downloadAction')}</span>
          </button>
        )}
        {entry.allowed_actions.includes('delete') && (
          <button
            type="button"
            role="menuitem"
            className="flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm text-[var(--color-danger)] hover:bg-[var(--color-hover)] transition-colors"
            onClick={() => {
              setMenuState(null);
              requestDelete(entry);
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

      {error && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{error}</p>}
      {initialPathFallback && <p className="ui-alert px-3 py-2 text-sm" role="status">{t('files.pathFallback')}</p>}

      <div className="grid gap-5 lg:grid-cols-[15rem_minmax(0,1fr)] items-start">
        <aside className="ui-card flex flex-col justify-between p-3 min-h-[160px] self-start" aria-label={t('files.profiles')}>
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

        <div className="ui-card min-w-0 overflow-hidden min-h-[560px] flex flex-col">
          {!selectedProfile ? (
            <div className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.selectProfile')}</div>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--color-border)] p-3">
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
                  <nav className="flex min-w-0 flex-wrap items-center gap-1 text-sm" aria-label={t('files.breadcrumb')}>
                    {breadcrumbs.map((breadcrumb, index) => (
                      <span key={breadcrumb.ref ?? 'root'} className="inline-flex min-w-0 items-center gap-1">
                        {index > 0 && <ChevronRightIcon className="h-4 w-4 shrink-0 text-[var(--color-text-muted)]" aria-hidden="true" />}
                        <button
                          type="button"
                          onClick={() => goToBreadcrumb(index)}
                          disabled={index === breadcrumbs.length - 1}
                          className="max-w-44 truncate rounded px-1 py-0.5 disabled:text-[var(--color-text-primary)] hover:bg-[var(--color-hover)]"
                        >
                          {breadcrumb.name}
                        </button>
                      </span>
                    ))}
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
                  />
                  <button
                    type="button"
                    onClick={() => {
                      setIsCreateDirOpen(true);
                      setNewDirName('');
                      setCreateDirError('');
                    }}
                    disabled={entriesLoading || !capabilities.mkdir}
                    className="ui-button-secondary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
                    title={!capabilities.mkdir ? t('files.mkdirUnavailable') : t('files.newFolder')}
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

              {!capabilities.browse && !entriesLoading ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.listUnavailable')}</p>
              ) : entriesLoading ? (
                <div className="p-8 flex-1 flex items-center justify-center"><LoadingIndicator label={t('common.loading')} /></div>
              ) : entries.length === 0 ? (
                <p className="ui-empty p-8 text-sm flex-1 flex items-center justify-center">{t('files.emptyDirectory')}</p>
              ) : viewMode === 'list' ? (
                <div className="overflow-x-auto flex-1">
                  <table className="ui-table ui-responsive-table w-full table-fixed text-sm">
                    <thead className="bg-[var(--color-bg-tertiary)] text-left text-xs text-[var(--color-text-secondary)]">
                      <tr>
                        <th scope="col" className="px-3 py-2 font-medium">{t('files.name')}</th>
                        <th scope="col" className="w-28 sm:w-32 px-3 py-2 font-medium whitespace-nowrap shrink-0">{t('files.size')}</th>
                        <th scope="col" className="w-36 sm:w-44 px-3 py-2 font-medium whitespace-nowrap shrink-0">{t('files.modified')}</th>
                        <th scope="col" className="w-14 sm:w-16 px-3 py-2 font-medium text-right shrink-0"><span className="sr-only">{t('files.actions')}</span></th>
                      </tr>
                    </thead>
                    <tbody>
                      {entries.map((entry) => {
                        const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || entry.kind === 'file';
                        return (
                          <tr
                            key={entry.ref}
                            onClick={() => isInteractive && openEntry(entry)}
                            onContextMenu={(event) => handleContextMenu(event, entry)}
                            className={`border-t border-[var(--color-border)] hover:bg-[var(--color-hover)] transition-colors ${isInteractive ? 'cursor-pointer' : ''}`}
                          >
                            <td data-label={t('files.name')} className="px-3 py-2 min-w-0 max-w-0">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation();
                                  openEntry(entry);
                                }}
                                disabled={entry.kind === 'directory' && (!capabilities.browse || entriesLoading)}
                                className="inline-flex max-w-full items-center gap-3 min-w-0 text-left ui-link disabled:cursor-not-allowed disabled:opacity-55"
                                title={entry.name}
                              >
                                <div className="relative w-10 h-10 shrink-0 rounded-lg overflow-hidden bg-[var(--color-bg-tertiary)]/40 border border-[var(--color-border)]/60 flex items-center justify-center">
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
                                <span className="truncate font-medium">{entry.name}</span>
                              </button>
                            </td>
                            <td data-label={t('files.size')} className="w-28 sm:w-32 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.kind === 'directory' ? t('files.directory') : formatBytes(entry.size)}
                            </td>
                            <td data-label={t('files.modified')} className="w-36 sm:w-44 px-3 py-2 text-[var(--color-text-secondary)] whitespace-nowrap shrink-0">
                              {entry.modified_at ? formatDateTime(entry.modified_at) : t('common.unspecified')}
                            </td>
                            <td data-label={t('files.actions')} className="w-14 sm:w-16 px-3 py-2 text-right whitespace-nowrap shrink-0">
                              {hasEntryActions(entry) && (
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
                  {entries.map((entry) => {
                    const isInteractive = (entry.kind === 'directory' && capabilities.browse && !entriesLoading) || entry.kind === 'file';

                    return (
                      <div
                        key={entry.ref}
                        role="gridcell"
                        tabIndex={isInteractive ? 0 : undefined}
                        onClick={() => isInteractive && openEntry(entry)}
                        onContextMenu={(event) => handleContextMenu(event, entry)}
                        onKeyDown={(e) => {
                          if ((e.key === 'Enter' || e.key === ' ') && isInteractive) {
                            e.preventDefault();
                            openEntry(entry);
                          }
                        }}
                        className={`group relative flex flex-col justify-between rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] overflow-hidden transition-all hover:bg-[var(--color-hover)] hover:border-[var(--color-border-hover,var(--color-border))] hover:shadow-xs ${
                          isInteractive ? 'cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-focus)]' : 'opacity-70'
                        }`}
                        title={entry.name}
                      >
                        {hasEntryActions(entry) && (
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
      {isCreateDirOpen &&
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
              {createDirError && (
                <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">
                  {createDirError}
                </p>
              )}
              <form onSubmit={handleCreateDirectory} className="mt-4 space-y-4">
                <div>
                  <label htmlFor="new-folder-name-input" className="block text-xs font-semibold text-[var(--color-text-secondary)] uppercase tracking-wider mb-1">
                    {t('files.folderName')}
                  </label>
                  <input
                    id="new-folder-name-input"
                    type="text"
                    value={newDirName}
                    onChange={(e) => setNewDirName(e.target.value)}
                    placeholder={t('files.folderNamePlaceholder')}
                    disabled={creatingDir}
                    className="ui-input w-full py-2 px-3 text-sm"
                    autoFocus
                  />
                </div>
                <div className="flex justify-end gap-2 pt-2">
                  <button
                    ref={createDirCancelRef}
                    type="button"
                    onClick={() => {
                      setIsCreateDirOpen(false);
                      setNewDirName('');
                      setCreateDirError('');
                    }}
                    disabled={creatingDir}
                    className="ui-button-secondary px-3 py-2 text-sm"
                  >
                    {t('common.cancel')}
                  </button>
                  <button
                    type="submit"
                    disabled={!newDirName.trim() || creatingDir}
                    className="ui-button-primary inline-flex items-center gap-2 px-3 py-2 text-sm disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {creatingDir ? (
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
      {deleteEntry && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={deleteDialogRef} role="dialog" aria-modal="true" aria-labelledby="delete-entry-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="delete-entry-title" className="text-lg font-semibold text-[var(--color-text-primary)]">{t('files.deleteTitle')}</h2>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">
              {deleteEntry.kind === 'file' ? t('files.deleteFileWarning', { name: deleteEntry.name }) : deleteRecursive ? t('files.deleteRecursiveWarning', { name: deleteEntry.name }) : t('files.deleteDirectoryWarning', { name: deleteEntry.name })}
            </p>
            {deleteError && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{deleteError}</p>}
            {deleteEntry.kind === 'directory' && deleteRecursive && !capabilities.delete_empty_directory && capabilities.delete_recursive_directory && <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.deleteOnlyRecursiveSupported')}</p>}
            <div className="mt-5 flex justify-end gap-2">
              <button ref={deleteCancelRef} type="button" onClick={() => { setDeleteEntry(null); setDeleteError(''); setDeleteRecursive(false); }} disabled={deletingEntry} className="ui-button-secondary px-3 py-2 text-sm">{t('common.cancel')}</button>
              <button type="button" onClick={() => void confirmDelete()} disabled={deletingEntry || (deleteEntry.kind === 'directory' && !deleteRecursive && !capabilities.delete_empty_directory)} className="ui-button-danger px-3 py-2 text-sm disabled:cursor-not-allowed disabled:opacity-50">
                {deletingEntry ? t('common.loading') : deleteRecursive ? t('files.deleteRecursively') : t('files.deleteConfirm')}
              </button>
            </div>
          </div>
        </div>, document.body)}
      {renameEntry && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={renameDialogRef} role="dialog" aria-modal="true" aria-labelledby="rename-entry-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="rename-entry-title" className="text-lg font-semibold">{t('files.renameTitle')}</h2>
            {mutationError && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{mutationError}</p>}
            <form className="mt-4 space-y-4" onSubmit={(event) => { event.preventDefault(); void executeMutation(); }}>
              <label className="block text-sm font-medium" htmlFor="rename-entry-name">{t('files.name')}</label>
              <input id="rename-entry-name" className="ui-input w-full px-3 py-2" value={renameName} disabled={mutationBusy} onChange={(event) => setRenameName(event.target.value)} autoFocus />
              <div className="flex justify-end gap-2"><button ref={renameCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={mutationBusy} onClick={closeMutationDialogs}>{t('common.cancel')}</button><button type="submit" className="ui-button-primary px-3 py-2 text-sm" disabled={!renameName.trim() || mutationBusy}>{mutationBusy ? t('files.mutating') : t('files.rename')}</button></div>
            </form>
          </div>
        </div>, document.body)}
      {mutationEntry && mutationOperation && conflictStrategies === null && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={pickerDialogRef} role="dialog" aria-modal="true" aria-labelledby="destination-picker-title" tabIndex={-1} className="ui-card flex max-h-[85vh] w-full max-w-lg flex-col p-5">
            <h2 id="destination-picker-title" className="text-lg font-semibold">{mutationOperation === 'copy' ? t('files.copyTitle') : t('files.moveTitle')}</h2>
            <p className="mt-1 text-sm text-[var(--color-text-secondary)]">{t('files.destinationPickerDescription', { name: mutationEntry.name })}</p>
            {mutationError && <p className="ui-alert ui-alert-error mt-3 px-3 py-2 text-sm" role="alert">{mutationError}</p>}
            <nav className="mt-3 flex flex-wrap gap-1 text-sm" aria-label={t('files.destinationBreadcrumb')}>
              {pickerBreadcrumbs.map((breadcrumb, index) => <button key={breadcrumb.ref ?? 'root'} type="button" className="rounded px-1 py-0.5 hover:bg-[var(--color-hover)]" disabled={pickerLoading || index === pickerBreadcrumbs.length - 1} onClick={() => { const next = pickerBreadcrumbs.slice(0, index + 1); setPickerBreadcrumbs(next); void loadPickerEntries(next[next.length - 1]?.ref ?? null); }}>{breadcrumb.name}{index < pickerBreadcrumbs.length - 1 ? ' /' : ''}</button>)}
            </nav>
            <div className="mt-3 min-h-36 overflow-y-auto rounded border border-[var(--color-border)]">
              {pickerLoading ? <div className="p-4"><LoadingIndicator label={t('common.loading')} size="sm" /></div> : pickerBreadcrumbs.some((breadcrumb) => mutationEntry.kind === 'directory' && breadcrumb.ref === mutationEntry.ref) ? <p className="p-3 text-sm text-[var(--color-text-secondary)]">{t('files.noValidDestination')}</p> : pickerEntries.filter((entry) => entry.ref !== mutationEntry.ref).map((entry) => <button key={entry.ref} type="button" className="flex w-full items-center gap-2 border-b border-[var(--color-border)] px-3 py-2 text-left text-sm hover:bg-[var(--color-hover)]" onClick={() => { const next = [...pickerBreadcrumbs, { ref: entry.ref, name: entry.name }]; setPickerBreadcrumbs(next); void loadPickerEntries(entry.ref); }}><FolderIcon className="h-4 w-4" aria-hidden="true" />{entry.name}</button>)}
            </div>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.destinationSelected', { name: pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.name })}</p>
            <div className="mt-5 flex justify-end gap-2"><button ref={pickerCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={mutationBusy} onClick={closeMutationDialogs}>{t('common.cancel')}</button><button type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={mutationBusy || (mutationOperation === 'move' && (mutationEntry.parent_ref ?? currentRef) === (pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.ref ?? null))} onClick={() => void executeMutation()}>{mutationBusy ? t('files.mutating') : mutationOperation === 'copy' ? t('files.copy') : t('files.move')}</button></div>
          </div>
        </div>, document.body)}
      {conflictStrategies && (renameEntry || mutationEntry) && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={conflictDialogRef} role="dialog" aria-modal="true" aria-labelledby="mutation-conflict-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="mutation-conflict-title" className="text-lg font-semibold">{t('files.conflictTitle')}</h2><p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.conflictDescription')}</p>
            <div className="mt-4 flex flex-wrap justify-end gap-2"><button ref={conflictCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={mutationBusy} onClick={closeMutationDialogs}>{t('common.cancel')}</button>{conflictStrategies.map((strategy) => <button key={strategy} type="button" className="ui-button-primary px-3 py-2 text-sm" disabled={mutationBusy} onClick={() => void executeMutation(strategy)}>{t(`files.conflict${strategy[0]}${strategy.slice(1).toLowerCase()}`)}</button>)}</div>
          </div>
        </div>, document.body)}
      {previewEntry && (
        <Suspense fallback={null}>
          <FilePreviewDialog
            apiUrl={apiUrl}
            token={token}
            profileId={profileId}
            entry={previewEntry}
            entries={entries}
            onNavigate={(entry) => setPreviewEntry(entry)}
            onClose={() => setPreviewEntry(null)}
            onDownload={(entry) => void download(entry)}
          />
        </Suspense>
      )}
      {renderActionMenuPortal()}
    </section>
  );
}
