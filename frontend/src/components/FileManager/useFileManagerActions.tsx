import { useCallback, type ReactNode } from 'react';
import type { FileCapabilities, FileEntry } from '../../api/files';
import type { ConnectionProfilePublic } from '../../api/profiles';
import { useActionEnvironment } from './actions/actionEnvironment';
import { FileActionDialogs } from './actions/FileActionDialogs';
import type { FileRequestContext } from './actions/fileRequests';
import { useBatchStatus, type BatchSummary } from './actions/useBatchStatus';
import { useCreateFolder } from './actions/useCreateFolder';
import { useBatchDelete, useDeleteEntry } from './actions/useDeleteEntry';
import { useDestinationTransfer } from './actions/useDestinationTransfer';
import { useDownloads } from './actions/useDownloads';
import { useFilePermissions, type FileActionPermissions } from './actions/useFilePermissions';
import { useRenameEntry } from './actions/useRenameEntry';
import { useTransferQueue, type FileActionTransfer } from './actions/useTransferQueue';

export type {
  FileActionTransfer,
  FileActionTransferOrigin,
  FileActionTransferStatus,
  FileTransferOperation,
} from './actions/useTransferQueue';
export type { BatchSummary } from './actions/useBatchStatus';
export type { FileActionPermissions } from './actions/useFilePermissions';
export type { FileRequestContext } from './actions/fileRequests';

export type UseFileManagerActionsOptions = {
  /** Where requests go and who they act as. */
  request: FileRequestContext;
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

/**
 * The file manager's user intents and their results. Everything a dialog needs (names being
 * typed, busy flags, destination browsing, conflict choices) stays inside the module and is
 * rendered through `dialogs`; callers start an action and show its outcome.
 */
export type FileManagerActions = {
  /** Latest action error to show to the user; cleared by `clearError` or the next action. */
  error: string;
  clearError: () => void;
  permissions: FileActionPermissions;

  /** Starts a download of a single file. */
  download: (entry: FileEntry) => Promise<void>;
  /** Downloads the selection as a ZIP archive. */
  downloadArchive: () => Promise<void>;
  /** Opens the new-folder dialog for the current directory. */
  createFolder: () => void;
  rename: (entry: FileEntry) => void;
  /** Deletes `entry`, or the current selection when omitted. */
  delete: (entry?: FileEntry) => void;
  /** Copies `entry`, or the current selection when omitted, to a destination the user picks. */
  copy: (entry?: FileEntry) => void;
  /** Moves `entry`, or the current selection when omitted, to a destination the user picks. */
  move: (entry?: FileEntry) => void;

  /** A batch operation on the selection is in flight. */
  batchBusy: boolean;
  /** Outcome of the last batch operation; dismisses itself after a few seconds. */
  batchSummary: BatchSummary | null;
  dismissBatchSummary: () => void;

  /** Background copies and moves, within this profile and across profiles. */
  transfers: FileActionTransfer[];
  cancelTransfer: (transferId: string) => Promise<void>;
  dismissTransfer: (transferId: string) => void;

  /** The action dialogs that are currently open. Render once, anywhere in the screen. */
  dialogs: ReactNode;
};

/**
 * Composes the independent action modules (permissions, downloads, single-entry mutations,
 * batch delete, destination transfers, transfer queue) behind one small interface.
 */
export function useFileManagerActions({
  request,
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
  const { environment, error } = useActionEnvironment({ request, refreshListing, clearSelection, clearPreview });
  const permissions = useFilePermissions(capabilities, selectedEntries);
  const batch = useBatchStatus();
  const queue = useTransferQueue(environment);

  const downloads = useDownloads(environment, {
    capabilities,
    canDownloadArchive: permissions.canBatchDownload,
    selectedEntries,
    batch,
    navigateToDownload,
  });
  const createFolder = useCreateFolder(environment, { enabled: permissions.canCreateFolder, currentRef });
  const rename = useRenameEntry(environment);
  const deleteEntry = useDeleteEntry(environment, { capabilities, previewEntryRef });
  const deleteSelection = useBatchDelete(environment, {
    canDelete: permissions.canBatchDelete,
    selectedEntries,
    batch,
  });
  const transfer = useDestinationTransfer(environment, {
    profiles,
    profileName,
    selectedEntries,
    previewEntryRef,
    canBatchTransfer: { copy: permissions.canBatchCopy, move: permissions.canBatchMove },
    batch,
    queue,
  });

  const { start: startDeleteEntry } = deleteEntry;
  const { start: startDeleteSelection } = deleteSelection;
  const { start: startTransfer } = transfer;
  const startDelete = useCallback(
    (entry?: FileEntry) => (entry ? startDeleteEntry(entry) : startDeleteSelection()),
    [startDeleteEntry, startDeleteSelection],
  );
  const startCopy = useCallback((entry?: FileEntry) => startTransfer('copy', entry), [startTransfer]);
  const startMove = useCallback((entry?: FileEntry) => startTransfer('move', entry), [startTransfer]);

  return {
    error,
    clearError: environment.clearError,
    permissions,
    download: downloads.download,
    downloadArchive: downloads.downloadArchive,
    createFolder: createFolder.start,
    rename: rename.start,
    delete: startDelete,
    copy: startCopy,
    move: startMove,
    batchBusy: batch.busy,
    batchSummary: batch.summary,
    dismissBatchSummary: batch.dismissSummary,
    transfers: queue.transfers,
    cancelTransfer: queue.cancel,
    dismissTransfer: queue.dismiss,
    dialogs: (
      <FileActionDialogs
        models={{
          createFolder: createFolder.dialog,
          rename: rename.dialog,
          delete: deleteEntry.dialog,
          batchDelete: deleteSelection.dialog,
          destinationPicker: transfer.dialog,
        }}
      />
    ),
  };
}
