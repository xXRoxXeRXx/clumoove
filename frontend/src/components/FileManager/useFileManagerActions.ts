import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  batchDeleteFileEntries,
  batchMutateFileEntries,
  cancelCrossProfileFileTransfer,
  copyFileEntry,
  createArchiveTicket,
  createDirectory,
  createDownloadTicket,
  deleteFileEntry,
  listCrossProfileFileTransfers,
  listFileEntries,
  moveFileEntry,
  renameFileEntry,
  startCrossProfileFileTransfer,
  type BatchItemResult,
  type FileCapabilities,
  type FileEntry,
  type FileMutationConflictStrategy,
  type FileTransferSummary,
} from '../../api/files';
import type { ConnectionProfilePublic } from '../../api/profiles';
import { useApiError } from '../../utils/apiError';

export type FileManagerBreadcrumb = {
  ref: string | null;
  name: string;
};

export type FileActionTransferOrigin = 'same-profile' | 'cross-profile';

export type FileActionTransferStatus =
  | 'queued'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type FileActionTransfer = {
  id: string;
  origin: FileActionTransferOrigin;
  operation: 'copy' | 'move';
  status: FileActionTransferStatus;
  sourceLabel: string;
  destinationLabel: string;
  itemCount: number;
  processedCount: number;
  failedCount: number;
  skippedCount: number;
  error?: string;
};

export type FileActionPermissions = {
  canCreateFolder: boolean;
  canDownloadEntry: (entry: FileEntry) => boolean;
  canRenameEntry: (entry: FileEntry) => boolean;
  canCopyEntry: (entry: FileEntry) => boolean;
  canMoveEntry: (entry: FileEntry) => boolean;
  canDeleteEntry: (entry: FileEntry) => boolean;
  hasEntryActions: (entry: FileEntry) => boolean;
  canBatchDownload: boolean;
  canBatchCopy: boolean;
  canBatchMove: boolean;
  canBatchDelete: boolean;
};

export type UseFileManagerActionsOptions = {
  apiUrl: string;
  token: string;
  profileId: string;
  profileName: string;
  profiles: ConnectionProfilePublic[];
  currentRef: string | null;
  capabilities: FileCapabilities;
  selectedEntries: FileEntry[];
  previewEntryRef: string | null;
  refreshListing: () => void | Promise<void>;
  clearSelection: () => void;
  clearPreview: () => void;
  navigateToDownload?: (url: string) => void;
};

export type FileManagerActions = {
  error: string;
  clearError: () => void;
  permissions: FileActionPermissions;
  download: (entry: FileEntry) => Promise<void>;
  createFolder: {
    open: boolean;
    name: string;
    busy: boolean;
    error: string;
    openDialog: () => void;
    setName: (name: string) => void;
    submit: () => Promise<void>;
    close: () => void;
  };
  rename: {
    open: boolean;
    entry: FileEntry | null;
    name: string;
    busy: boolean;
    error: string;
    openDialog: (entry: FileEntry) => void;
    setName: (name: string) => void;
    submit: () => Promise<void>;
    close: () => void;
  };
  deleteEntry: {
    open: boolean;
    entry: FileEntry | null;
    recursive: boolean;
    busy: boolean;
    error: string;
    openDialog: (entry: FileEntry) => void;
    confirm: () => Promise<void>;
    close: () => void;
  };
  destinationPicker: {
    open: boolean;
    mode: 'single' | 'batch' | null;
    operation: 'copy' | 'move' | null;
    entry: FileEntry | null;
    itemCount: number;
    profileId: string;
    breadcrumbs: FileManagerBreadcrumb[];
    availableEntries: FileEntry[];
    hasInvalidDestination: boolean;
    loading: boolean;
    busy: boolean;
    error: string;
    openSingle: (entry: FileEntry, operation: 'copy' | 'move') => void;
    openBatch: (operation: 'copy' | 'move') => void;
    selectProfile: (targetProfileId: string) => void;
    openFolder: (entry: FileEntry) => void;
    goToBreadcrumb: (index: number) => void;
    submit: () => Promise<void>;
    close: () => void;
  };
  conflict: {
    open: boolean;
    strategies: FileMutationConflictStrategy[] | null;
    busy: boolean;
    resolve: (strategy: FileMutationConflictStrategy) => Promise<void>;
    cancel: () => void;
  };
  batch: {
    busy: boolean;
    deleteDialogOpen: boolean;
    deleting: boolean;
    itemCount: number;
    hasDirectories: boolean;
    results: BatchItemResult[];
    downloadArchive: () => Promise<void>;
    openDeleteDialog: () => void;
    confirmDelete: () => Promise<void>;
    closeDeleteDialog: () => void;
    dismissResults: () => void;
  };
  transfers: FileActionTransfer[];
  cancelTransfer: (transferId: string) => Promise<void>;
  dismissTransfer: (transferId: string) => void;
};

function nextTransferID(): string {
  return crypto.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
}

function mapCrossProfileStatus(transfer: FileTransferSummary): FileActionTransferStatus {
  if (transfer.status === 'COMPLETED' || (transfer.total_files > 0 && transfer.processed_files >= transfer.total_files)) {
    return 'completed';
  }
  if (transfer.status === 'CANCELLED' || transfer.status === 'CANCELED') {
    return 'cancelled';
  }
  if (transfer.status === 'FAILED') {
    return 'failed';
  }
  if (transfer.status === 'PENDING') {
    return 'queued';
  }
  return 'running';
}

function toCrossProfileActionTransfer(transfer: FileTransferSummary): FileActionTransfer {
  return {
    id: transfer.id,
    origin: 'cross-profile',
    operation: transfer.operation,
    status: mapCrossProfileStatus(transfer),
    sourceLabel: transfer.source_profile_name,
    destinationLabel: transfer.target_profile_name,
    itemCount: transfer.total_files,
    processedCount: transfer.processed_files,
    failedCount: transfer.failed_files,
    skippedCount: transfer.skipped_files,
  };
}

/**
 * Encapsulates file-manager permissions, single and batch mutations, destination
 * picking, conflict resolution, and unified same/cross-profile transfer state.
 */
export function useFileManagerActions({
  apiUrl,
  token,
  profileId,
  profileName,
  profiles,
  currentRef,
  capabilities,
  selectedEntries,
  previewEntryRef,
  refreshListing,
  clearSelection,
  clearPreview,
  navigateToDownload,
}: UseFileManagerActionsOptions): FileManagerActions {
  const { t } = useTranslation();
  const translateApiError = useApiError();

  const [error, setError] = useState('');
  const [batchBusy, setBatchBusy] = useState(false);
  const [batchOperation, setBatchOperation] = useState<'copy' | 'move' | null>(null);
  const [batchDeleting, setBatchDeleting] = useState(false);
  const [batchDeleteOpen, setBatchDeleteOpen] = useState(false);
  const [batchResults, setBatchResults] = useState<BatchItemResult[]>([]);
  const [isCreateDirOpen, setIsCreateDirOpen] = useState(false);
  const [newDirName, setNewDirName] = useState('');
  const [creatingDir, setCreatingDir] = useState(false);
  const [createDirError, setCreateDirError] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<FileEntry | null>(null);
  const [deletingEntry, setDeletingEntry] = useState(false);
  const [deleteRecursive, setDeleteRecursive] = useState(false);
  const [deleteError, setDeleteError] = useState('');
  const [renameTarget, setRenameTarget] = useState<FileEntry | null>(null);
  const [renameName, setRenameName] = useState('');
  const [mutationEntry, setMutationEntry] = useState<FileEntry | null>(null);
  const [mutationOperation, setMutationOperation] = useState<'copy' | 'move' | null>(null);
  const [pickerBreadcrumbs, setPickerBreadcrumbs] = useState<FileManagerBreadcrumb[]>([]);
  const [pickerProfileId, setPickerProfileId] = useState(profileId);
  const [pickerEntries, setPickerEntries] = useState<FileEntry[]>([]);
  const [pickerLoading, setPickerLoading] = useState(false);
  const [mutationBusy, setMutationBusy] = useState(false);
  const [mutationError, setMutationError] = useState('');
  const [conflictStrategies, setConflictStrategies] = useState<FileMutationConflictStrategy[] | null>(null);
  const [sameProfileTransfers, setSameProfileTransfers] = useState<FileActionTransfer[]>([]);
  const [crossProfileTransfers, setCrossProfileTransfers] = useState<FileTransferSummary[]>([]);
  const [dismissedCrossProfileIds, setDismissedCrossProfileIds] = useState<Set<string>>(() => new Set());

  const activeGenerationRef = useRef(0);
  const downloadingEntryRef = useRef<string | null>(null);
  const createDirRequestRef = useRef<AbortController | null>(null);
  const downloadRequestRef = useRef<AbortController | null>(null);
  const deleteRequestRef = useRef<AbortController | null>(null);
  const mutationRequestRef = useRef<AbortController | null>(null);
  const pickerRequestRef = useRef<AbortController | null>(null);
  const transfersRequestRef = useRef<AbortController | null>(null);
  const transferControllersRef = useRef(new Map<string, AbortController>());
  const sameProfileIdsRef = useRef(new Set<string>());

  useEffect(() => {
    const transferControllers = transferControllersRef.current;
    return () => {
      activeGenerationRef.current += 1;
      createDirRequestRef.current?.abort();
      downloadRequestRef.current?.abort();
      deleteRequestRef.current?.abort();
      mutationRequestRef.current?.abort();
      pickerRequestRef.current?.abort();
      transfersRequestRef.current?.abort();
      transferControllers.forEach((controller) => controller.abort());
      transferControllers.clear();
      downloadingEntryRef.current = null;
    };
  }, [profileId]);

  const loadTransfers = useCallback(async () => {
    if (!profileId) return;
    transfersRequestRef.current?.abort();
    const controller = new AbortController();
    transfersRequestRef.current = controller;
    const result = await listCrossProfileFileTransfers(apiUrl, token, profileId, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok) {
      setCrossProfileTransfers(result.data.transfers ?? []);
    }
  }, [apiUrl, profileId, token]);

  useEffect(() => {
    const timer = window.setInterval(() => void loadTransfers(), 5000);
    const initialLoad = window.setTimeout(() => void loadTransfers(), 0);
    return () => {
      window.clearInterval(timer);
      window.clearTimeout(initialLoad);
    };
  }, [loadTransfers]);

  useEffect(() => {
    if (batchResults.length === 0) return;
    const timer = window.setTimeout(() => {
      setBatchResults([]);
    }, 4000);
    return () => window.clearTimeout(timer);
  }, [batchResults]);

  const triggerDownloadUrl = useCallback(
    (downloadUrl: string) => {
      const resolvedUrl = new URL(downloadUrl, apiUrl).toString();
      if (navigateToDownload) {
        navigateToDownload(resolvedUrl);
      } else {
        window.location.assign(resolvedUrl);
      }
    },
    [apiUrl, navigateToDownload],
  );

  const canDownloadEntry = useCallback(
    (entry: FileEntry) => entry.kind === 'file' && capabilities.download && entry.allowed_actions.includes('download'),
    [capabilities.download],
  );
  const canRenameEntry = useCallback(
    (entry: FileEntry) => Boolean(capabilities.rename && entry.allowed_actions.includes('rename')),
    [capabilities.rename],
  );
  const canCopyEntry = useCallback(
    (entry: FileEntry) => Boolean(capabilities.copy && entry.allowed_actions.includes('copy')),
    [capabilities.copy],
  );
  const canMoveEntry = useCallback(
    (entry: FileEntry) => Boolean(capabilities.move && entry.allowed_actions.includes('move')),
    [capabilities.move],
  );
  const canDeleteEntry = useCallback(
    (entry: FileEntry) => entry.allowed_actions.includes('delete'),
    [],
  );
  const hasEntryActions = useCallback(
    (entry: FileEntry) =>
      canRenameEntry(entry) ||
      canCopyEntry(entry) ||
      canMoveEntry(entry) ||
      canDownloadEntry(entry) ||
      canDeleteEntry(entry),
    [canCopyEntry, canDeleteEntry, canDownloadEntry, canMoveEntry, canRenameEntry],
  );

  const selectedCan = useCallback(
    (action: 'download' | 'delete' | 'copy' | 'move') =>
      selectedEntries.length > 0 &&
      selectedEntries.every((entry) =>
        action === 'download'
          ? capabilities.archive === true
          : entry.allowed_actions.includes(action),
      ),
    [capabilities.archive, selectedEntries],
  );

  const permissions = useMemo<FileActionPermissions>(
    () => ({
      canCreateFolder: Boolean(capabilities.mkdir),
      canDownloadEntry,
      canRenameEntry,
      canCopyEntry,
      canMoveEntry,
      canDeleteEntry,
      hasEntryActions,
      canBatchDownload: selectedCan('download'),
      canBatchCopy: selectedCan('copy'),
      canBatchMove: selectedCan('move'),
      canBatchDelete: selectedCan('delete'),
    }),
    [
      capabilities.mkdir,
      canCopyEntry,
      canDeleteEntry,
      canDownloadEntry,
      canMoveEntry,
      canRenameEntry,
      hasEntryActions,
      selectedCan,
    ],
  );

  const openCreateFolder = useCallback(() => {
    setIsCreateDirOpen(true);
    setNewDirName('');
    setCreateDirError('');
  }, []);

  const closeCreateFolder = useCallback(() => {
    if (creatingDir) return;
    createDirRequestRef.current?.abort();
    setIsCreateDirOpen(false);
    setNewDirName('');
    setCreateDirError('');
  }, [creatingDir]);

  const submitCreateFolder = useCallback(async () => {
    const trimmed = newDirName.trim();
    if (!trimmed || creatingDir || !capabilities.mkdir) return;
    createDirRequestRef.current?.abort();
    const controller = new AbortController();
    createDirRequestRef.current = controller;
    setCreatingDir(true);
    setCreateDirError('');

    const result = await createDirectory(apiUrl, token, profileId, trimmed, currentRef, controller.signal);
    if (controller.signal.aborted) return;
    if (result.ok === false) {
      setCreateDirError(translateApiError(result.errorCode));
      setCreatingDir(false);
      return;
    }

    setCreatingDir(false);
    setIsCreateDirOpen(false);
    setNewDirName('');
    setCreateDirError('');
    void refreshListing();
  }, [apiUrl, capabilities.mkdir, creatingDir, currentRef, newDirName, profileId, refreshListing, token, translateApiError]);

  const download = useCallback(
    async (entry: FileEntry) => {
      if (entry.kind === 'directory' || !capabilities.download || downloadingEntryRef.current) return;
      downloadRequestRef.current?.abort();
      const controller = new AbortController();
      downloadRequestRef.current = controller;
      downloadingEntryRef.current = entry.ref;
      setError('');
      const result = await createDownloadTicket(apiUrl, token, profileId, entry.ref, controller.signal);
      if (downloadingEntryRef.current === entry.ref) {
        downloadingEntryRef.current = null;
      }
      if (controller.signal.aborted) return;
      if (result.ok === false) {
        setError(translateApiError(result.errorCode));
      } else {
        triggerDownloadUrl(result.data.download_url);
      }
    },
    [apiUrl, capabilities.download, profileId, token, translateApiError, triggerDownloadUrl],
  );

  const downloadArchive = useCallback(async () => {
    if (!selectedCan('download') || batchBusy) return;
    const generation = activeGenerationRef.current;
    setBatchBusy(true);
    setError('');
    setBatchResults([]);
    const result = await createArchiveTicket(
      apiUrl,
      token,
      profileId,
      selectedEntries.map((entry) => entry.ref),
    );
    if (activeGenerationRef.current !== generation) return;
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
    } else {
      triggerDownloadUrl(result.data.download_url);
    }
    setBatchBusy(false);
  }, [apiUrl, batchBusy, profileId, selectedCan, selectedEntries, token, translateApiError, triggerDownloadUrl]);

  const openDeleteDialog = useCallback(
    (entry: FileEntry) => {
      if (!entry.allowed_actions.includes('delete') || deletingEntry) return;
      setDeleteTarget(entry);
      setDeleteRecursive(
        entry.kind === 'directory' &&
          !capabilities.delete_empty_directory &&
          capabilities.delete_recursive_directory,
      );
      setDeleteError('');
    },
    [capabilities.delete_empty_directory, capabilities.delete_recursive_directory, deletingEntry],
  );

  const closeDeleteDialog = useCallback(() => {
    if (deletingEntry) return;
    deleteRequestRef.current?.abort();
    setDeleteTarget(null);
    setDeleteError('');
    setDeleteRecursive(false);
  }, [deletingEntry]);

  const confirmDelete = useCallback(async () => {
    if (!deleteTarget || deletingEntry) return;
    setDeletingEntry(true);
    setDeleteError('');
    deleteRequestRef.current?.abort();
    const controller = new AbortController();
    deleteRequestRef.current = controller;

    const result = await deleteFileEntry(
      apiUrl,
      token,
      profileId,
      deleteTarget.ref,
      deleteRecursive,
      controller.signal,
    );
    if (controller.signal.aborted) return;
    if (result.ok === false) {
      if (
        result.errorCode === 'FILES_DIRECTORY_NOT_EMPTY' &&
        deleteTarget.kind === 'directory' &&
        capabilities.delete_recursive_directory
      ) {
        setDeleteRecursive(true);
        setDeleteError(t('files.deleteDirectoryNotEmpty'));
      } else {
        setDeleteError(translateApiError(result.errorCode));
      }
      setDeletingEntry(false);
      return;
    }

    if (previewEntryRef === deleteTarget.ref) clearPreview();
    setDeleteTarget(null);
    setDeleteRecursive(false);
    setDeletingEntry(false);
    void refreshListing();
  }, [
    apiUrl,
    capabilities.delete_recursive_directory,
    clearPreview,
    deleteRecursive,
    deleteTarget,
    deletingEntry,
    previewEntryRef,
    profileId,
    refreshListing,
    t,
    token,
    translateApiError,
  ]);

  const openBatchDeleteDialog = useCallback(() => {
    if (!selectedCan('delete') || batchBusy) return;
    setBatchDeleteOpen(true);
  }, [batchBusy, selectedCan]);

  const closeBatchDeleteDialog = useCallback(() => {
    if (!batchDeleting) setBatchDeleteOpen(false);
  }, [batchDeleting]);

  const confirmBatchDelete = useCallback(async () => {
    if (!selectedCan('delete') || batchDeleting) return;
    const generation = activeGenerationRef.current;
    setBatchDeleting(true);
    setBatchResults([]);
    const result = await batchDeleteFileEntries(
      apiUrl,
      token,
      profileId,
      selectedEntries.map((entry) => ({
        ref: entry.ref,
        recursive: entry.kind === 'directory',
      })),
    );
    if (activeGenerationRef.current !== generation) return;
    setBatchDeleting(false);
    setBatchDeleteOpen(false);
    if (result.ok === false) {
      setError(translateApiError(result.errorCode));
      return;
    }
    setBatchResults(result.data.results);
    clearSelection();
    void refreshListing();
  }, [
    apiUrl,
    batchDeleting,
    clearSelection,
    profileId,
    refreshListing,
    selectedCan,
    selectedEntries,
    token,
    translateApiError,
  ]);

  const openRenameDialog = useCallback((entry: FileEntry) => {
    setRenameTarget(entry);
    setRenameName(entry.name);
    setMutationError('');
    setConflictStrategies(null);
  }, []);

  const closeMutationDialogs = useCallback(() => {
    if (mutationBusy || batchBusy) return;
    mutationRequestRef.current?.abort();
    pickerRequestRef.current?.abort();
    setRenameTarget(null);
    setMutationEntry(null);
    setMutationOperation(null);
    setBatchOperation(null);
    setMutationError('');
    setConflictStrategies(null);
  }, [batchBusy, mutationBusy]);

  const loadPickerEntries = useCallback(
    async (parentRef: string | null, targetProfileId: string) => {
      pickerRequestRef.current?.abort();
      const controller = new AbortController();
      pickerRequestRef.current = controller;
      setPickerLoading(true);
      const result = await listFileEntries(apiUrl, token, targetProfileId, parentRef, undefined, controller.signal);
      if (!controller.signal.aborted) {
        setPickerEntries(result.ok ? result.data.entries.filter((entry) => entry.kind === 'directory') : []);
        if (result.ok === false) setMutationError(translateApiError(result.errorCode));
        setPickerLoading(false);
      }
    },
    [apiUrl, token, translateApiError],
  );

  const initDestinationPicker = useCallback(() => {
    setMutationError('');
    setConflictStrategies(null);
    setPickerProfileId(profileId);
    setPickerBreadcrumbs([{ ref: null, name: profileName || t('files.title') }]);
    setPickerEntries([]);
    void loadPickerEntries(null, profileId);
  }, [loadPickerEntries, profileId, profileName, t]);

  const openSingleDestinationPicker = useCallback(
    (entry: FileEntry, operation: 'copy' | 'move') => {
      setBatchOperation(null);
      setMutationEntry(entry);
      setMutationOperation(operation);
      initDestinationPicker();
    },
    [initDestinationPicker],
  );

  const openBatchDestinationPicker = useCallback(
    (operation: 'copy' | 'move') => {
      if (!selectedCan(operation) || batchBusy) return;
      setMutationEntry(null);
      setMutationOperation(null);
      setBatchOperation(operation);
      setBatchResults([]);
      initDestinationPicker();
    },
    [batchBusy, initDestinationPicker, selectedCan],
  );

  const selectPickerProfile = useCallback(
    (targetProfileId: string) => {
      const target = profiles.find((profile) => profile.id === targetProfileId);
      if (!target) return;
      setPickerProfileId(targetProfileId);
      setPickerBreadcrumbs([{ ref: null, name: target.name }]);
      setPickerEntries([]);
      setMutationError('');
      void loadPickerEntries(null, targetProfileId);
    },
    [loadPickerEntries, profiles],
  );

  const openPickerFolder = useCallback(
    (entry: FileEntry) => {
      const next = [...pickerBreadcrumbs, { ref: entry.ref, name: entry.name }];
      setPickerBreadcrumbs(next);
      void loadPickerEntries(entry.ref, pickerProfileId);
    },
    [loadPickerEntries, pickerBreadcrumbs, pickerProfileId],
  );

  const goToPickerBreadcrumb = useCallback(
    (index: number) => {
      const next = pickerBreadcrumbs.slice(0, index + 1);
      setPickerBreadcrumbs(next);
      void loadPickerEntries(next[next.length - 1]?.ref ?? null, pickerProfileId);
    },
    [loadPickerEntries, pickerBreadcrumbs, pickerProfileId],
  );

  const updateSameProfileTransfer = useCallback(
    (transferId: string, patch: Partial<FileActionTransfer>) => {
      setSameProfileTransfers((prev) =>
        prev.map((item) => (item.id === transferId ? { ...item, ...patch } : item)),
      );
    },
    [],
  );

  const translateMutationError = useCallback(
    (errorCode?: string | null) =>
      errorCode === 'FILES_PARTIAL_OPERATION' ? t('files.partialWarning') : translateApiError(errorCode),
    [t, translateApiError],
  );

  const clearDestinationDialog = useCallback(() => {
    setMutationEntry(null);
    setMutationOperation(null);
    setBatchOperation(null);
    setConflictStrategies(null);
    setMutationError('');
  }, []);

  const enqueueSameProfileTransfer = useCallback(
    (params: {
      operation: 'copy' | 'move';
      sourceLabel: string;
      destinationLabel: string;
      itemCount: number;
      controller: AbortController;
      run: () => Promise<
        | { ok: true }
        | {
            ok: false;
            processedCount: number;
            failedCount: number;
            error: string;
            refreshOnPartial?: boolean;
          }
      >;
      onSuccess?: () => void;
    }) => {
      const transferId = nextTransferID();
      sameProfileIdsRef.current.add(transferId);
      transferControllersRef.current.set(transferId, params.controller);
      setSameProfileTransfers((prev) => [
        ...prev,
        {
          id: transferId,
          origin: 'same-profile',
          operation: params.operation,
          status: 'running',
          sourceLabel: params.sourceLabel,
          destinationLabel: params.destinationLabel,
          itemCount: params.itemCount,
          processedCount: 0,
          failedCount: 0,
          skippedCount: 0,
        },
      ]);

      params
        .run()
        .then((outcome) => {
          transferControllersRef.current.delete(transferId);
          if (params.controller.signal.aborted) return;
          if (outcome.ok === false) {
            updateSameProfileTransfer(transferId, {
              status: 'failed',
              processedCount: outcome.processedCount,
              failedCount: outcome.failedCount,
              error: outcome.error,
            });
            if (outcome.refreshOnPartial) {
              void refreshListing();
            }
            return;
          }
          updateSameProfileTransfer(transferId, {
            status: 'completed',
            processedCount: params.itemCount,
          });
          params.onSuccess?.();
          void refreshListing();
        })
        .catch(() => {
          transferControllersRef.current.delete(transferId);
          if (params.controller.signal.aborted) return;
          updateSameProfileTransfer(transferId, {
            status: 'failed',
            failedCount: params.itemCount,
            error: translateApiError(null),
          });
        });
    },
    [refreshListing, translateApiError, updateSameProfileTransfer],
  );

  const submitCrossProfileTransfer = useCallback(
    async (params: {
      refs: string[];
      operation: 'copy' | 'move';
      destinationRef: string | null;
      strategy?: FileMutationConflictStrategy;
      signal?: AbortSignal;
      onSettledBusy: () => void;
      onSuccess: () => void;
    }) => {
      const generation = activeGenerationRef.current;
      const result = params.signal
        ? await startCrossProfileFileTransfer(
            apiUrl,
            token,
            profileId,
            params.refs,
            pickerProfileId,
            params.destinationRef,
            params.operation,
            params.strategy,
            params.signal,
          )
        : await startCrossProfileFileTransfer(
            apiUrl,
            token,
            profileId,
            params.refs,
            pickerProfileId,
            params.destinationRef,
            params.operation,
            params.strategy,
          );
      if (params.signal?.aborted || activeGenerationRef.current !== generation) return;
      params.onSettledBusy();
      if (result.ok === false) {
        setMutationError(translateApiError(result.errorCode));
      } else {
        params.onSuccess();
      }
      void loadTransfers();
    },
    [apiUrl, loadTransfers, pickerProfileId, profileId, token, translateApiError],
  );

  const submitRename = useCallback(
    async (strategy?: FileMutationConflictStrategy) => {
      if (!renameTarget || mutationBusy) return;
      const newName = renameName.trim();
      if (!newName) {
        setMutationError(t('files.nameRequired'));
        return;
      }

      mutationRequestRef.current?.abort();
      const controller = new AbortController();
      mutationRequestRef.current = controller;
      setMutationBusy(true);
      setMutationError('');

      const result = await renameFileEntry(
        apiUrl,
        token,
        profileId,
        renameTarget.ref,
        newName,
        strategy,
        controller.signal,
      );
      if (controller.signal.aborted) return;
      setMutationBusy(false);
      if (result.ok === false) {
        const options = (result.data as { conflict_strategies?: FileMutationConflictStrategy[] } | undefined)
          ?.conflict_strategies;
        if (result.errorCode === 'FILES_CONFLICT' && options?.length) {
          setConflictStrategies(options);
        } else {
          setMutationError(translateMutationError(result.errorCode));
        }
        if (result.errorCode === 'FILES_PARTIAL_OPERATION') {
          void refreshListing();
        }
        return;
      }

      setRenameTarget(null);
      setConflictStrategies(null);
      setMutationError('');
      void refreshListing();
    },
    [
      apiUrl,
      mutationBusy,
      profileId,
      refreshListing,
      renameName,
      renameTarget,
      t,
      token,
      translateMutationError,
    ],
  );

  const submitSingleDestinationMutation = useCallback(
    async (strategy?: FileMutationConflictStrategy) => {
      if (!mutationEntry || !mutationOperation || mutationBusy) return;
      const entry = mutationEntry;
      const operation = mutationOperation;
      const destinationRef = pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.ref ?? null;
      const destinationName = pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.name ?? t('files.title');

      mutationRequestRef.current?.abort();
      const controller = new AbortController();
      mutationRequestRef.current = controller;
      setMutationError('');

      if (pickerProfileId !== profileId) {
        setMutationBusy(true);
        await submitCrossProfileTransfer({
          refs: [entry.ref],
          operation,
          destinationRef,
          strategy,
          signal: controller.signal,
          onSettledBusy: () => setMutationBusy(false),
          onSuccess: clearDestinationDialog,
        });
        return;
      }

      clearDestinationDialog();
      setMutationBusy(false);

      enqueueSameProfileTransfer({
        operation,
        sourceLabel: entry.name,
        destinationLabel: destinationName,
        itemCount: 1,
        controller,
        run: async () => {
          const result =
            operation === 'copy'
              ? await copyFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal)
              : await moveFileEntry(apiUrl, token, profileId, entry.ref, destinationRef, strategy, controller.signal);
          if (result.ok === false) {
            return {
              ok: false,
              processedCount: 0,
              failedCount: 1,
              error: translateMutationError(result.errorCode),
              refreshOnPartial: result.errorCode === 'FILES_PARTIAL_OPERATION',
            };
          }
          return { ok: true };
        },
        onSuccess: () => {
          if (previewEntryRef === entry.ref && operation === 'move') {
            clearPreview();
          }
        },
      });
    },
    [
      apiUrl,
      clearDestinationDialog,
      clearPreview,
      enqueueSameProfileTransfer,
      mutationBusy,
      mutationEntry,
      mutationOperation,
      pickerBreadcrumbs,
      pickerProfileId,
      previewEntryRef,
      profileId,
      submitCrossProfileTransfer,
      t,
      token,
      translateMutationError,
    ],
  );

  const submitBatchDestinationMutation = useCallback(
    async (strategy?: FileMutationConflictStrategy) => {
      if (!batchOperation || batchBusy) return;
      const refs = selectedEntries.map((entry) => entry.ref);
      if (refs.length === 0) return;

      const operation = batchOperation;
      const destinationRef = pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.ref ?? null;
      const destinationName = pickerBreadcrumbs[pickerBreadcrumbs.length - 1]?.name ?? t('files.title');
      setMutationError('');

      if (pickerProfileId !== profileId) {
        setBatchBusy(true);
        await submitCrossProfileTransfer({
          refs,
          operation,
          destinationRef,
          strategy,
          onSettledBusy: () => setBatchBusy(false),
          onSuccess: () => {
            clearDestinationDialog();
            clearSelection();
            void refreshListing();
          },
        });
        return;
      }

      const controller = new AbortController();
      clearDestinationDialog();
      clearSelection();
      setBatchBusy(false);

      enqueueSameProfileTransfer({
        operation,
        sourceLabel: t('files.selectedCount', { count: refs.length }),
        destinationLabel: destinationName,
        itemCount: refs.length,
        controller,
        run: async () => {
          const result = await batchMutateFileEntries(
            operation,
            apiUrl,
            token,
            profileId,
            refs,
            destinationRef,
            strategy,
            controller.signal,
          );
          if (result.ok === false) {
            return {
              ok: false,
              processedCount: 0,
              failedCount: refs.length,
              error: translateApiError(result.errorCode),
            };
          }
          const failures = result.data.results.filter((item) => item.status === 'failed' || item.status === 'conflict');
          const succeeded = result.data.results.length - failures.length;
          if (failures.length > 0) {
            return {
              ok: false,
              processedCount: succeeded,
              failedCount: failures.length,
              error: t('files.batchResultSummary', { success: succeeded, failed: failures.length }),
              refreshOnPartial: true,
            };
          }
          return { ok: true };
        },
      });
    },
    [
      apiUrl,
      batchBusy,
      batchOperation,
      clearDestinationDialog,
      clearSelection,
      enqueueSameProfileTransfer,
      pickerBreadcrumbs,
      pickerProfileId,
      profileId,
      refreshListing,
      selectedEntries,
      submitCrossProfileTransfer,
      t,
      token,
      translateApiError,
    ],
  );

  const cancelTransfer = useCallback(
    async (transferId: string) => {
      const controller = transferControllersRef.current.get(transferId);
      if (controller) {
        controller.abort();
        transferControllersRef.current.delete(transferId);
        updateSameProfileTransfer(transferId, { status: 'cancelled' });
        return;
      }
      if (sameProfileIdsRef.current.has(transferId)) {
        return;
      }
      const generation = activeGenerationRef.current;
      const result = await cancelCrossProfileFileTransfer(apiUrl, token, transferId);
      if (activeGenerationRef.current !== generation) return;
      if (result.ok === false) {
        setError(translateApiError(result.errorCode));
      } else {
        void loadTransfers();
      }
    },
    [apiUrl, loadTransfers, token, translateApiError, updateSameProfileTransfer],
  );

  const dismissTransfer = useCallback((transferId: string) => {
    sameProfileIdsRef.current.delete(transferId);
    setSameProfileTransfers((prev) => prev.filter((item) => item.id !== transferId));
    setDismissedCrossProfileIds((prev) => {
      if (prev.has(transferId)) return prev;
      const next = new Set(prev);
      next.add(transferId);
      return next;
    });
    setCrossProfileTransfers((prev) => prev.filter((item) => item.id !== transferId));
  }, []);

  const transfers = useMemo<FileActionTransfer[]>(
    () => [
      ...sameProfileTransfers,
      ...crossProfileTransfers
        .filter((transfer) => !dismissedCrossProfileIds.has(transfer.id))
        .map(toCrossProfileActionTransfer),
    ],
    [crossProfileTransfers, dismissedCrossProfileIds, sameProfileTransfers],
  );

  const targetPickerEntries = useMemo(
    () => (batchOperation !== null ? selectedEntries : mutationEntry ? [mutationEntry] : []),
    [batchOperation, mutationEntry, selectedEntries],
  );

  const availablePickerEntries = useMemo(
    () => pickerEntries.filter((entry) => !targetPickerEntries.some((target) => target.ref === entry.ref)),
    [pickerEntries, targetPickerEntries],
  );

  const hasInvalidDestination = useMemo(
    () =>
      pickerBreadcrumbs.some((breadcrumb) =>
        targetPickerEntries.some((entry) => entry.kind === 'directory' && breadcrumb.ref === entry.ref),
      ),
    [pickerBreadcrumbs, targetPickerEntries],
  );

  const clearError = useCallback(() => setError(''), []);
  const dismissBatchResults = useCallback(() => setBatchResults([]), []);

  return {
    error,
    clearError,
    permissions,
    download,
    createFolder: {
      open: isCreateDirOpen,
      name: newDirName,
      busy: creatingDir,
      error: createDirError,
      openDialog: openCreateFolder,
      setName: setNewDirName,
      submit: submitCreateFolder,
      close: closeCreateFolder,
    },
    rename: {
      open: renameTarget !== null,
      entry: renameTarget,
      name: renameName,
      busy: mutationBusy,
      error: mutationError,
      openDialog: openRenameDialog,
      setName: setRenameName,
      submit: () => submitRename(),
      close: closeMutationDialogs,
    },
    deleteEntry: {
      open: deleteTarget !== null,
      entry: deleteTarget,
      recursive: deleteRecursive,
      busy: deletingEntry,
      error: deleteError,
      openDialog: openDeleteDialog,
      confirm: confirmDelete,
      close: closeDeleteDialog,
    },
    destinationPicker: {
      open: ((mutationEntry !== null && mutationOperation !== null) || batchOperation !== null) && conflictStrategies === null,
      mode: batchOperation !== null ? 'batch' : mutationEntry !== null ? 'single' : null,
      operation: mutationOperation ?? batchOperation,
      entry: mutationEntry,
      itemCount: selectedEntries.length,
      profileId: pickerProfileId,
      breadcrumbs: pickerBreadcrumbs,
      availableEntries: availablePickerEntries,
      hasInvalidDestination,
      loading: pickerLoading,
      busy: mutationBusy || batchBusy,
      error: mutationError,
      openSingle: openSingleDestinationPicker,
      openBatch: openBatchDestinationPicker,
      selectProfile: selectPickerProfile,
      openFolder: openPickerFolder,
      goToBreadcrumb: goToPickerBreadcrumb,
      submit: () => (batchOperation ? submitBatchDestinationMutation() : submitSingleDestinationMutation()),
      close: closeMutationDialogs,
    },
    conflict: {
      open: conflictStrategies !== null && renameTarget !== null,
      strategies: conflictStrategies,
      busy: mutationBusy || batchBusy,
      resolve: (strategy) => submitRename(strategy),
      cancel: closeMutationDialogs,
    },
    batch: {
      busy: batchBusy,
      deleteDialogOpen: batchDeleteOpen,
      deleting: batchDeleting,
      itemCount: selectedEntries.length,
      hasDirectories: selectedEntries.some((entry) => entry.kind === 'directory'),
      results: batchResults,
      downloadArchive,
      openDeleteDialog: openBatchDeleteDialog,
      confirmDelete: confirmBatchDelete,
      closeDeleteDialog: closeBatchDeleteDialog,
      dismissResults: dismissBatchResults,
    },
    transfers,
    cancelTransfer,
    dismissTransfer,
  };
}
