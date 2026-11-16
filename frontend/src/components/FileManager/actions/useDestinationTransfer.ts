import { useCallback, useState } from 'react';
import type { FileEntry } from '../../../api/files';
import type { ConnectionProfilePublic } from '../../../api/profiles';
import { useRequestSlot, type ActionEnvironment } from './actionEnvironment';
import type { DestinationPickerDialogModel } from './dialogModels';
import type { BatchStatus } from './useBatchStatus';
import { useDestinationBrowser } from './useDestinationBrowser';
import type { FileTransferOperation, TransferQueue } from './useTransferQueue';

type Target =
  | { kind: 'single'; entry: FileEntry; operation: FileTransferOperation }
  | { kind: 'batch'; operation: FileTransferOperation };

type Options = {
  profiles: ConnectionProfilePublic[];
  profileName: string;
  selectedEntries: FileEntry[];
  previewEntryRef: string | null;
  canBatchTransfer: Record<FileTransferOperation, boolean>;
  batch: BatchStatus;
  queue: TransferQueue;
};

/**
 * Copying or moving one entry or the selection to a destination folder, in this profile or
 * another one. Same-profile transfers close the picker and continue in the transfer queue;
 * cross-profile transfers keep the picker open until the server accepts them.
 */
export function useDestinationTransfer(
  env: ActionEnvironment,
  { profiles, profileName, selectedEntries, previewEntryRef, canBatchTransfer, batch, queue }: Options,
) {
  const { requests, request, t, translateApiError, translateMutationError, clearSelection, clearPreview, refreshListing, beginSession } = env;
  const browser = useDestinationBrowser(env, { profiles, profileName });
  const [target, setTarget] = useState<Target | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState('');
  const { begin: beginRequest, abort: abortRequest } = useRequestSlot(request.profileId);

  const {
    reset: resetBrowser,
    abort: abortBrowser,
    clearError: clearBrowserError,
    selectProfile: selectBrowserProfile,
    destination,
    profileId: destinationProfileId,
  } = browser;
  const open = useCallback(
    (next: Target) => {
      setTarget(next);
      setSubmitError('');
      resetBrowser();
    },
    [resetBrowser],
  );

  /** Omit `entry` to transfer the current selection. */
  const start = useCallback(
    (operation: FileTransferOperation, entry?: FileEntry) => {
      if (entry) {
        open({ kind: 'single', entry, operation });
        return;
      }
      if (!canBatchTransfer[operation] || batch.busy) return;
      batch.dismissSummary();
      open({ kind: 'batch', operation });
    },
    [batch, canBatchTransfer, open],
  );

  const closeDialog = useCallback(() => {
    setTarget(null);
    setSubmitError('');
  }, []);

  const close = useCallback(() => {
    if (submitting) return;
    abortRequest();
    abortBrowser();
    closeDialog();
  }, [abortBrowser, abortRequest, closeDialog, submitting]);

  const startCrossProfile = useCallback(
    async (params: {
      refs: string[];
      operation: FileTransferOperation;
      destinationRef: string | null;
      targetProfileId: string;
      signal?: AbortSignal;
      markBatchBusy: boolean;
      onSuccess: () => void;
    }) => {
      const isCurrent = beginSession();
      setSubmitting(true);
      if (params.markBatchBusy) batch.setBusy(true);
      const result = await requests.startCrossProfileTransfer({
        refs: params.refs,
        targetProfileId: params.targetProfileId,
        targetParentRef: params.destinationRef,
        operation: params.operation,
        strategy: undefined,
        signal: params.signal,
      });
      if (params.signal?.aborted || !isCurrent()) return;
      setSubmitting(false);
      if (params.markBatchBusy) batch.setBusy(false);
      if (result.ok === false) {
        setSubmitError(translateApiError(result.errorCode));
      } else {
        params.onSuccess();
      }
      void queue.reload();
    },
    [batch, beginSession, queue, requests, translateApiError],
  );

  const submitSingle = useCallback(
    async (single: Extract<Target, { kind: 'single' }>) => {
      const { entry, operation } = single;
      const { ref: destinationRef, name: destinationName } = destination;
      const controller = beginRequest();
      setSubmitError('');
      clearBrowserError();

      if (destinationProfileId !== request.profileId) {
        await startCrossProfile({
          refs: [entry.ref],
          operation,
          destinationRef,
          targetProfileId: destinationProfileId,
          signal: controller.signal,
          markBatchBusy: false,
          onSuccess: closeDialog,
        });
        return;
      }

      // The transfer outlives the dialog request, so it gets its own controller.
      const transferController = new AbortController();
      closeDialog();
      queue.enqueue({
        operation,
        sourceLabel: entry.name,
        destinationLabel: destinationName,
        itemCount: 1,
        controller: transferController,
        run: async () => {
          const result =
            operation === 'copy'
              ? await requests.copyEntry(entry.ref, destinationRef, undefined, transferController.signal)
              : await requests.moveEntry(entry.ref, destinationRef, undefined, transferController.signal);
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
    [beginRequest, clearBrowserError, destination, destinationProfileId, clearPreview, closeDialog, previewEntryRef, queue, request.profileId, requests, startCrossProfile, translateMutationError],
  );

  const submitBatch = useCallback(
    async (batchTarget: Extract<Target, { kind: 'batch' }>) => {
      if (batch.busy) return;
      const refs = selectedEntries.map((entry) => entry.ref);
      if (refs.length === 0) return;

      const { operation } = batchTarget;
      const { ref: destinationRef, name: destinationName } = destination;
      setSubmitError('');
      clearBrowserError();

      if (destinationProfileId !== request.profileId) {
        await startCrossProfile({
          refs,
          operation,
          destinationRef,
          targetProfileId: destinationProfileId,
          markBatchBusy: true,
          onSuccess: () => {
            closeDialog();
            clearSelection();
            void refreshListing();
          },
        });
        return;
      }

      const controller = new AbortController();
      closeDialog();
      clearSelection();
      queue.enqueue({
        operation,
        sourceLabel: t('files.selectedCount', { count: refs.length }),
        destinationLabel: destinationName,
        itemCount: refs.length,
        controller,
        run: async () => {
          const result = await requests.batchMutate(operation, refs, destinationRef, undefined, controller.signal);
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
    [batch, clearBrowserError, destination, destinationProfileId, clearSelection, closeDialog, queue, refreshListing, request.profileId, requests, selectedEntries, startCrossProfile, t, translateApiError],
  );

  const submit = useCallback(async () => {
    if (!target || submitting) return;
    if (target.kind === 'single') await submitSingle(target);
    else await submitBatch(target);
  }, [submitBatch, submitSingle, submitting, target]);

  const selectProfile = useCallback(
    (profileId: string) => {
      setSubmitError('');
      selectBrowserProfile(profileId);
    },
    [selectBrowserProfile],
  );

  const targetEntries = target?.kind === 'batch' ? selectedEntries : target?.kind === 'single' ? [target.entry] : [];
  const dialog: DestinationPickerDialogModel | null = target
    ? {
        operation: target.operation,
        entryName: target.kind === 'single' ? target.entry.name : null,
        itemCount: selectedEntries.length,
        profiles,
        profileId: destinationProfileId,
        breadcrumbs: browser.breadcrumbs,
        folders: browser.folders.filter((folder) => !targetEntries.some((entry) => entry.ref === folder.ref)),
        hasInvalidDestination: browser.breadcrumbs.some((breadcrumb) =>
          targetEntries.some((entry) => entry.kind === 'directory' && breadcrumb.ref === entry.ref),
        ),
        loading: browser.loading,
        busy: submitting,
        error: submitError || browser.error,
        selectProfile,
        openFolder: browser.openFolder,
        goToBreadcrumb: browser.goToBreadcrumb,
        submit,
        close,
      }
    : null;
  return { start, dialog };
}
