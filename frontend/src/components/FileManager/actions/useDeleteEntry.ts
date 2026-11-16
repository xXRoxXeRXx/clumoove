import { useCallback, useState } from 'react';
import type { FileCapabilities, FileEntry } from '../../../api/files';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';
import type { BatchDeleteDialogModel, DeleteDialogModel } from './dialogModels';
import type { BatchStatus } from './useBatchStatus';

type SingleOptions = {
  capabilities: FileCapabilities;
  previewEntryRef: string | null;
};

/**
 * Deleting one entry. A directory the connection cannot delete while non-empty falls back
 * to an explicit recursive confirmation.
 */
export function useDeleteEntry(env: ActionEnvironment, { capabilities, previewEntryRef }: SingleOptions) {
  const { requests, request, t, translateApiError, refreshListing, clearPreview } = env;
  const [target, setTarget] = useState<FileEntry | null>(null);
  const [busy, setBusy] = useState(false);
  const [recursive, setRecursive] = useState(false);
  const [error, setError] = useState('');
  const { begin: beginRequest, abort: abortRequest } = useRequestSlot(request.profileId);

  const start = useCallback(
    (entry: FileEntry) => {
      if (!entry.allowed_actions.includes('delete') || busy) return;
      setTarget(entry);
      setRecursive(
        entry.kind === 'directory' && !capabilities.delete_empty_directory && capabilities.delete_recursive_directory,
      );
      setError('');
    },
    [busy, capabilities.delete_empty_directory, capabilities.delete_recursive_directory],
  );

  const close = useCallback(() => {
    if (busy) return;
    abortRequest();
    setTarget(null);
    setError('');
    setRecursive(false);
  }, [abortRequest, busy]);

  const confirm = useCallback(async () => {
    if (!target || busy) return;
    setBusy(true);
    setError('');
    const controller = beginRequest();

    const result = await requests.deleteEntry(target.ref, recursive, controller.signal);
    if (controller.signal.aborted) return;
    setBusy(false);
    if (result.ok === false) {
      if (
        result.errorCode === 'FILES_DIRECTORY_NOT_EMPTY' &&
        target.kind === 'directory' &&
        capabilities.delete_recursive_directory
      ) {
        setRecursive(true);
        setError(t('files.deleteDirectoryNotEmpty'));
      } else {
        setError(translateApiError(result.errorCode));
      }
      return;
    }

    if (previewEntryRef === target.ref) clearPreview();
    setTarget(null);
    setRecursive(false);
    void refreshListing();
  }, [
    beginRequest,
    busy,
    capabilities.delete_recursive_directory,
    clearPreview,
    previewEntryRef,
    recursive,
    refreshListing,
    requests,
    t,
    target,
    translateApiError,
  ]);

  const dialog: DeleteDialogModel | null = target
    ? {
        entry: target,
        recursive,
        busy,
        error,
        canConfirm: !(target.kind === 'directory' && !recursive && !capabilities.delete_empty_directory),
        onlyRecursiveSupported:
          target.kind === 'directory' &&
          recursive &&
          !capabilities.delete_empty_directory &&
          capabilities.delete_recursive_directory,
        confirm,
        close,
      }
    : null;
  return { start, dialog };
}

type BatchOptions = {
  canDelete: boolean;
  selectedEntries: FileEntry[];
  batch: BatchStatus;
};

/** Deleting the whole selection; directories are always deleted recursively. */
export function useBatchDelete(env: ActionEnvironment, { canDelete, selectedEntries, batch }: BatchOptions) {
  const { requests, translateApiError, reportError, clearSelection, refreshListing, beginSession } = env;
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  const start = useCallback(() => {
    if (!canDelete || batch.busy) return;
    setOpen(true);
  }, [batch.busy, canDelete]);

  const close = useCallback(() => {
    if (!busy) setOpen(false);
  }, [busy]);

  const confirm = useCallback(async () => {
    if (!canDelete || busy) return;
    const isCurrent = beginSession();
    setBusy(true);
    batch.dismissSummary();
    const result = await requests.batchDelete(
      selectedEntries.map((entry) => ({ ref: entry.ref, recursive: entry.kind === 'directory' })),
    );
    if (!isCurrent()) return;
    setBusy(false);
    setOpen(false);
    if (result.ok === false) {
      reportError(translateApiError(result.errorCode));
      return;
    }
    batch.showResults(result.data.results);
    clearSelection();
    void refreshListing();
  }, [batch, beginSession, busy, canDelete, clearSelection, refreshListing, reportError, requests, selectedEntries, translateApiError]);

  const dialog: BatchDeleteDialogModel | null = open
    ? {
        itemCount: selectedEntries.length,
        hasDirectories: selectedEntries.some((entry) => entry.kind === 'directory'),
        busy,
        confirm,
        close,
      }
    : null;
  return { start, dialog };
}
