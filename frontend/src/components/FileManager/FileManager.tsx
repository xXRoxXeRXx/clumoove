import { useCallback, useEffect, useRef, useState, lazy, Suspense } from 'react';
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
import { batchDeleteFileEntries, batchMutateFileEntries, copyFileEntry, createArchiveTicket, createDirectory, deleteFileEntry, createDownloadTicket, moveFileEntry, renameFileEntry, startCrossProfileFileTransfer, type BatchItemResult, type FileBreadcrumb, type FileEntry, type FileMutationConflictStrategy } from '../../api/files';
import { LoadingIndicator } from '../LoadingIndicator';
import { useApiError } from '../../utils/apiError';
import { useFormat } from '../../utils/format';
import { useFocusTrap } from '../../hooks/useFocusTrap';
import { FileUploadControl, type InternalTransfer } from './FileUploadControl';
import { FileThumbnail } from './FileThumbnail';
import { FileManagerMutationDialogs } from './FileManagerMutationDialogs';
import type { SortField } from './FileManagerTypes';
import { useDestinationPicker } from './useDestinationPicker';
import { useCrossProfileTransfers } from './useCrossProfileTransfers';
import { useFileManagerNavigation } from './useFileManagerNavigation';
import { useFileManagerEntryPresentation } from './useFileManagerEntryPresentation';
import { useFileManagerSelection } from './useFileManagerSelection';

const FilePreviewDialog = lazy(() => import('./FilePreviewDialog').then((m) => ({ default: m.FilePreviewDialog })));

/**
 * Authenticated, profile-bound interactive cloud file management.
 *
 * Callers provide the active profile and navigation callbacks; FileManager keeps
 * provider paths opaque and uses the supplied session only for file operations.
 * This remains feature-local rather than sharing FileBrowser's setup contract:
 * its authenticated protocol owns sealed refs, breadcrumbs, capabilities, and API errors.
 */
export type FileManagerProps = {
  apiUrl: string;
  token: string;
  profileId: string;
  initialBreadcrumbs?: FileBreadcrumb[];
  initialPathFallback?: boolean;
  onProfileChange: (profileId: string) => void;
  onOpenManager?: () => void;
  onBack?: () => void;
};

function nextTransferID(): string {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

export function FileManager({ apiUrl, token, profileId, initialBreadcrumbs, initialPathFallback = false, onProfileChange, onOpenManager, onBack }: FileManagerProps) {
  const { t } = useTranslation();
  const { formatBytes, formatDateTime } = useFormat();
  const translateApiError = useApiError();
  const [error, setError] = useState('');
  const [downloadingRef, setDownloadingRef] = useState<string | null>(null);
  const resetSelectionRef = useRef<() => void>(() => {});
  const resetSelection = useCallback(() => resetSelectionRef.current(), []);
  const navigation = useFileManagerNavigation({ apiUrl, token, profileId, initialBreadcrumbs, translateApiError, onError: setError, onResetSelection: resetSelection });
  const { profiles, profilesLoading, entries, entriesLoading, loadingMore, capabilities, breadcrumbs, nextCursor, selectedProfile, currentRef, setBreadcrumbs, loadEntries, loadMore, abort: abortNavigation } = navigation;
  const selection = useFileManagerSelection(entries);
  resetSelectionRef.current = selection.resetSelection;
  const { selectedRefs, selectedEntries, allLoadedSelected, someLoadedSelected, isSelectionMode, toggleSelection, toggleAll: toggleSelectAll } = selection;
  const [batchOperation, setBatchOperation] = useState<'copy' | 'move' | null>(null);
  const [batchDeleting, setBatchDeleting] = useState(false);
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [batchBusy, setBatchBusy] = useState(false);
  const [batchConflictRefs, setBatchConflictRefs] = useState<string[]>([]);
  const [batchResults, setBatchResults] = useState<BatchItemResult[]>([]);
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
  const [internalTransfers, setInternalTransfers] = useState<InternalTransfer[]>([]);
  const internalControllers = useRef(new Map<string, AbortController>());
  const [mutationBusy, setMutationBusy] = useState(false);
  const [mutationError, setMutationError] = useState('');
  const [conflictStrategies, setConflictStrategies] = useState<FileMutationConflictStrategy[] | null>(null);
  const { picker: destinationPicker, open: openDestinationPicker, abort: abortDestinationPicker } = useDestinationPicker({
    apiUrl, token, profiles, translateApiError, onError: setMutationError,
  });
  const { transfers, loadTransfers, cancelTransfer, dismissTransfer } = useCrossProfileTransfers({
    apiUrl, token, profileId, onError: (errorCode) => setError(translateApiError(errorCode)),
  });
  const [isBreadcrumbMenuOpen, setIsBreadcrumbMenuOpen] = useState(false);
  const breadcrumbMenuRef = useRef<HTMLDivElement>(null);
  const renderSortHeader = (field: SortField, label: string, sortLabel: string) => {
    const isActive = sortField === field;
    return (
      <button
        type="button"
        onClick={() => sortBy(field)}
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
  const deleteRequestRef = useRef<AbortController | null>(null);
  const mutationRequestRef = useRef<AbortController | null>(null);
  const renameDialogRef = useRef<HTMLDivElement>(null);
  const renameCancelRef = useRef<HTMLButtonElement>(null);
  const pickerDialogRef = useRef<HTMLDivElement>(null);
  const pickerCancelRef = useRef<HTMLButtonElement>(null);
  const conflictDialogRef = useRef<HTMLDivElement>(null);
  const conflictCancelRef = useRef<HTMLButtonElement>(null);
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
  useFocusTrap(batchDeleteDialogRef, batchDeleteCancelRef, () => {
    if (!batchDeleting) setBatchDeleteOpen(false);
  }, batchDeleteOpen);
  const closeMutationDialogs = useCallback(() => {
    if (mutationBusy) return;
    mutationRequestRef.current?.abort();
    abortDestinationPicker();
    setRenameEntry(null); setMutationEntry(null); setMutationOperation(null); setBatchOperation(null); setBatchConflictRefs([]); setMutationError(''); setConflictStrategies(null);
  }, [abortDestinationPicker, mutationBusy]);
  useFocusTrap(renameDialogRef, renameCancelRef, closeMutationDialogs, renameEntry !== null);
  useFocusTrap(pickerDialogRef, pickerCancelRef, closeMutationDialogs, ((mutationEntry !== null && mutationOperation !== null) || batchOperation !== null) && conflictStrategies === null);
  useFocusTrap(conflictDialogRef, conflictCancelRef, closeMutationDialogs, conflictStrategies !== null);

  useEffect(() => () => {
    abortNavigation();
    deleteRequestRef.current?.abort();
    mutationRequestRef.current?.abort();
    abortDestinationPicker();
  }, [abortDestinationPicker, abortNavigation]);

  useEffect(() => {
    const controllers = internalControllers.current;
    return () => {
      controllers.forEach((controller) => controller.abort());
      controllers.clear();
      if (uploadRefreshTimeoutRef.current !== null) {
        window.clearTimeout(uploadRefreshTimeoutRef.current);
      }
    };
  }, []);

  const cancelInternalTransfer = useCallback((transferId: string) => {
    const controller = internalControllers.current.get(transferId);
    if (controller) {
      controller.abort();
      internalControllers.current.delete(transferId);
    }
    setInternalTransfers((prev) =>
      prev.map((t) => (t.id === transferId ? { ...t, status: 'cancelled' } : t))
    );
  }, []);

  const dismissInternalTransfer = useCallback((transferId: string) => {
    setInternalTransfers((prev) => prev.filter((t) => t.id !== transferId));
  }, []);

  useEffect(() => {
    if (batchResults.length === 0) return;
    const timer = window.setTimeout(() => {
      setBatchResults([]);
    }, 4000);
    return () => window.clearTimeout(timer);
  }, [batchResults]);

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

  const { viewMode, setView: handleViewModeChange, sortField, sortDirection, sortBy, sortedEntries } = useFileManagerEntryPresentation(entries);

  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = someLoadedSelected && !allLoadedSelected;
  }, [allLoadedSelected, someLoadedSelected]);

  useEffect(() => {
    if (!nextCursor || entriesLoading || loadingMore || !capabilities.browse) return;
    const target = sentinelRef.current;
    if (!target || typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver((observed) => {
      if (observed[0]?.isIntersecting) void loadMore();
    }, { rootMargin: '250px' });
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
    resetSelection();
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
    resetSelection();
    void loadEntries(parentRef);
  };

  const refresh = () => {
    if (!capabilities.browse) return;
    void loadEntries(currentRef);
  };

  const goToBreadcrumb = (index: number) => {
    if (index === breadcrumbs.length - 1) return;
    setIsBreadcrumbMenuOpen(false);
    const targetBreadcrumbs = breadcrumbs.slice(0, index + 1);
    const targetRef = targetBreadcrumbs[targetBreadcrumbs.length - 1]?.ref ?? null;
    setBreadcrumbs(targetBreadcrumbs);
    resetSelection();
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

  const selectedCan = (action: 'download' | 'delete' | 'copy' | 'move') => selectedEntries.length > 0 && selectedEntries.every((entry) => action === 'download'
    ? capabilities.archive === true
    : entry.allowed_actions.includes(action));

  const downloadArchive = async () => {
    if (!selectedCan('download') || batchBusy) return;
    setBatchBusy(true); setError(''); setBatchResults([]);
    const result = await createArchiveTicket(apiUrl, token, profileId, selectedEntries.map((entry) => entry.ref));
    if (result.ok === false) setError(translateApiError(result.errorCode));
    else window.location.assign(new URL(result.data.download_url, apiUrl).toString());
    setBatchBusy(false);
  };

  const deleteSelected = async () => {
    if (!selectedCan('delete') || batchDeleting) return;
    setBatchDeleting(true); setBatchResults([]);
    const result = await batchDeleteFileEntries(apiUrl, token, profileId, selectedEntries.map((entry) => ({ ref: entry.ref, recursive: entry.kind === 'directory' })));
    setBatchDeleting(false); setBatchDeleteOpen(false);
    if (result.ok === false) setError(translateApiError(result.errorCode));
    else {
      setBatchResults(result.data.results);
      resetSelection();
      void loadEntries(currentRef);
    }
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

  const startRename = (entry: FileEntry) => {
    setMenuState(null); setRenameEntry(entry); setRenameName(entry.name); setMutationError(''); setConflictStrategies(null);
  };

  const startDestinationPicker = (entry: FileEntry, operation: 'copy' | 'move') => {
    setMenuState(null); setMutationEntry(entry); setMutationOperation(operation); setMutationError(''); setConflictStrategies(null);
    openDestinationPicker(profileId, selectedProfile?.name ?? t('files.title'));
  };

  const startBatchDestinationPicker = (operation: 'copy' | 'move') => {
    if (!selectedCan(operation) || batchBusy) return;
    setBatchOperation(operation); setBatchConflictRefs([]); setBatchResults([]); setMutationError(''); setConflictStrategies(null);
    openDestinationPicker(profileId, selectedProfile?.name ?? t('files.title'));
  };

  const executeBatchMutation = async (strategy?: FileMutationConflictStrategy) => {
    if (!batchOperation || batchBusy) return;
    const refs = strategy ? batchConflictRefs : selectedEntries.map((entry) => entry.ref);
    if (refs.length === 0) return;
    setBatchBusy(true); setMutationError('');
    const destinationRef = destinationPicker.breadcrumbs[destinationPicker.breadcrumbs.length - 1]?.ref ?? null;
    const destinationName = destinationPicker.breadcrumbs[destinationPicker.breadcrumbs.length - 1]?.name ?? t('files.title');

    if (destinationPicker.profileId !== profileId) {
      const result = await startCrossProfileFileTransfer(apiUrl, token, profileId, refs, destinationPicker.profileId, destinationRef, batchOperation, strategy);
      setBatchBusy(false);
      if (result.ok === false) setMutationError(translateApiError(result.errorCode));
      else { setBatchOperation(null); setBatchConflictRefs([]); setConflictStrategies(null); resetSelection(); void loadEntries(currentRef); }
      void loadTransfers();
      return;
    }

    // Same-profile batch copy or move: non-blocking in background transfer queue
    const op = batchOperation;
    const count = refs.length;
    const transferId = nextTransferID();
    const controller = new AbortController();
    internalControllers.current.set(transferId, controller);

    const transfer: InternalTransfer = {
      id: transferId,
      operation: op,
      sourceName: t('files.selectedCount', { count }),
      destinationName,
      itemCount: count,
      status: op === 'copy' ? 'copying' : 'moving',
    };
    setInternalTransfers((prev) => [...prev, transfer]);

    setBatchOperation(null);
    setBatchConflictRefs([]);
    setConflictStrategies(null);
    resetSelection();
    setBatchBusy(false);
    setMutationError('');

    batchMutateFileEntries(op, apiUrl, token, profileId, refs, destinationRef, strategy, controller.signal)
      .then((result) => {
        internalControllers.current.delete(transferId);
        if (controller.signal.aborted) return;
        if (result.ok === false) {
          setInternalTransfers((prev) =>
            prev.map((t) => (t.id === transferId ? { ...t, status: 'failed', error: translateApiError(result.errorCode) } : t))
          );
          return;
        }
        const failures = result.data.results.filter((item) => item.status === 'failed' || item.status === 'conflict');
        if (failures.length > 0) {
          const summary = t('files.batchResultSummary', {
            success: result.data.results.length - failures.length,
            failed: failures.length,
          });
          setInternalTransfers((prev) =>
            prev.map((t) => (t.id === transferId ? { ...t, status: 'failed', error: summary } : t))
          );
        } else {
          setInternalTransfers((prev) =>
            prev.map((t) => (t.id === transferId ? { ...t, status: op === 'copy' ? 'copied' : 'moved' } : t))
          );
        }
        void loadEntries(currentRef);
      })
      .catch((err) => {
        internalControllers.current.delete(transferId);
        if (controller.signal.aborted) return;
        setInternalTransfers((prev) =>
          prev.map((t) => (t.id === transferId ? { ...t, status: 'failed', error: String(err) } : t))
        );
      });
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
    const destinationRef = destinationPicker.breadcrumbs[destinationPicker.breadcrumbs.length - 1]?.ref ?? null;
    const destinationName = destinationPicker.breadcrumbs[destinationPicker.breadcrumbs.length - 1]?.name ?? t('files.title');

    if (operation !== 'rename' && destinationPicker.profileId !== profileId) {
      const result = await startCrossProfileFileTransfer(apiUrl, token, profileId, [entry.ref], destinationPicker.profileId, destinationRef, operation, strategy, controller.signal);
      if (controller.signal.aborted) return;
      setMutationBusy(false);
      if (result.ok === false) { setMutationError(translateApiError(result.errorCode)); return; }
      setMutationEntry(null); setMutationOperation(null); setConflictStrategies(null); setMutationError('');
      void loadTransfers();
      return;
    }

    if (operation === 'rename') {
      const result = await renameFileEntry(apiUrl, token, profileId, entry.ref, newName, strategy, controller.signal);
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
      setRenameEntry(null); setMutationEntry(null); setMutationOperation(null); setConflictStrategies(null); setMutationError('');
      void loadEntries(currentRef);
      return;
    }

    // Same-profile copy or move: non-blocking in background transfer queue
    const transferId = nextTransferID();
    const transfer: InternalTransfer = {
      id: transferId,
      operation,
      sourceName: entry.name,
      destinationName,
      itemCount: 1,
      status: operation === 'copy' ? 'copying' : 'moving',
    };
    internalControllers.current.set(transferId, controller);
    setInternalTransfers((prev) => [...prev, transfer]);

    // Close destination picker modal immediately so UI is not blocked
    setMutationEntry(null);
    setMutationOperation(null);
    setConflictStrategies(null);
    setMutationError('');
    setMutationBusy(false);

    const mutationPromise = operation === 'copy'
      ? copyFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal)
      : moveFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal);

    mutationPromise.then((result) => {
      internalControllers.current.delete(transferId);
      if (controller.signal.aborted) return;
      if (result.ok === false) {
        const err = result.errorCode === 'FILES_PARTIAL_OPERATION' ? t('files.partialWarning') : translateApiError(result.errorCode);
        setInternalTransfers((prev) =>
          prev.map((t) => (t.id === transferId ? { ...t, status: 'failed', error: err } : t))
        );
        if (result.errorCode === 'FILES_PARTIAL_OPERATION') void loadEntries(currentRef);
        return;
      }
      setInternalTransfers((prev) =>
        prev.map((t) => (t.id === transferId ? { ...t, status: operation === 'copy' ? 'copied' : 'moved' } : t))
      );
      if (previewEntry?.ref === entry.ref && operation === 'move') setPreviewEntry(null);
      void loadEntries(currentRef);
    }).catch((err) => {
      internalControllers.current.delete(transferId);
      if (controller.signal.aborted) return;
      setInternalTransfers((prev) =>
        prev.map((t) => (t.id === transferId ? { ...t, status: 'failed', error: String(err) } : t))
      );
    });
  };

  const canRename = (entry: FileEntry) => capabilities.rename && entry.allowed_actions.includes('rename');
  const canCopy = (entry: FileEntry) => capabilities.copy && entry.allowed_actions.includes('copy');
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

      {error && <p className="ui-alert ui-alert-error px-3 py-2 text-sm" role="alert">{error}</p>}
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
                      onClick={resetSelection}
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
                      disabled={!selectedCan('download') || batchBusy}
                      onClick={() => void downloadArchive()}
                      title={t('files.downloadArchive')}
                      aria-label={t('files.downloadArchive')}
                    >
                      <ArrowDownTrayIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.downloadArchive')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!selectedCan('copy') || batchBusy}
                      onClick={() => startBatchDestinationPicker('copy')}
                      title={t('files.copy')}
                      aria-label={t('files.copy')}
                    >
                      <ClipboardDocumentIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.copy')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-secondary inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!selectedCan('move') || batchBusy}
                      onClick={() => startBatchDestinationPicker('move')}
                      title={t('files.move')}
                      aria-label={t('files.move')}
                    >
                      <ArrowsRightLeftIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
                      <span className="hidden md:inline">{t('files.move')}</span>
                    </button>
                    <button
                      type="button"
                      className="ui-button-danger inline-flex items-center justify-center gap-1.5 p-2 md:px-3 md:py-1.5 text-xs font-medium disabled:opacity-50 shrink-0"
                      disabled={!selectedCan('delete') || batchBusy}
                      onClick={() => setBatchDeleteOpen(true)}
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
                      backgroundTransfers={transfers}
                      onCancelBackgroundTransfer={(transferId) => void cancelTransfer(transferId)}
                      onDismissBackgroundTransfer={dismissTransfer}
                      internalTransfers={internalTransfers}
                      onCancelInternalTransfer={cancelInternalTransfer}
                      onDismissInternalTransfer={dismissInternalTransfer}
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
              )}
              {batchResults.length > 0 && (
                <div className="mx-3 mt-3 flex items-center justify-between gap-2 ui-alert px-3 py-2 text-sm" role="status">
                  <span>
                    {t('files.batchResultSummary', {
                      success: batchResults.filter((item) => ['deleted', 'copied', 'moved'].includes(item.status)).length,
                      failed: batchResults.filter((item) => item.status === 'failed').length,
                    })}
                  </span>
                  <button
                    type="button"
                    onClick={() => setBatchResults([])}
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
      {batchDeleteOpen && createPortal(
        <div className="fixed inset-0 z-[var(--layer-dialog)] flex items-center justify-center bg-[var(--color-overlay)] p-4">
          <div ref={batchDeleteDialogRef} role="dialog" aria-modal="true" aria-labelledby="batch-delete-title" tabIndex={-1} className="ui-card w-full max-w-md p-5">
            <h2 id="batch-delete-title" className="text-lg font-semibold">{t('files.batchDeleteTitle')}</h2>
            <p className="mt-3 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteWarning', { count: selectedEntries.length })}</p>
            {selectedEntries.some((entry) => entry.kind === 'directory') && <p className="mt-2 text-sm text-[var(--color-text-secondary)]">{t('files.batchDeleteRecursive')}</p>}
            <div className="mt-5 flex justify-end gap-2"><button ref={batchDeleteCancelRef} type="button" className="ui-button-secondary px-3 py-2 text-sm" disabled={batchDeleting} onClick={() => setBatchDeleteOpen(false)}>{t('common.cancel')}</button><button type="button" className="ui-button-danger px-3 py-2 text-sm" disabled={batchDeleting} onClick={() => void deleteSelected()}>{batchDeleting ? t('common.loading') : t('files.deleteRecursively')}</button></div>
          </div>
        </div>, document.body)}
      <FileManagerMutationDialogs
        profiles={profiles}
        selectedEntries={selectedEntries}
        renameEntry={renameEntry}
        renameName={renameName}
        mutationEntry={mutationEntry}
        mutationOperation={mutationOperation}
        batchOperation={batchOperation}
        batchConflictRefs={batchConflictRefs}
        conflictStrategies={conflictStrategies}
        mutationError={mutationError}
        mutationBusy={mutationBusy}
        batchBusy={batchBusy}
        destinationPicker={destinationPicker}
        dialogRefs={{
          rename: renameDialogRef,
          renameCancel: renameCancelRef,
          picker: pickerDialogRef,
          pickerCancel: pickerCancelRef,
          conflict: conflictDialogRef,
          conflictCancel: conflictCancelRef,
        }}
        onRenameNameChange={setRenameName}
        onClose={closeMutationDialogs}
        onExecuteMutation={(strategy) => void executeMutation(strategy)}
        onExecuteBatchMutation={(strategy) => void executeBatchMutation(strategy)}
      />
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
            onDownload={(entry) => void download(entry)}
          />
        </Suspense>
      )}
      {renderActionMenuPortal()}
    </section>
  );
}
