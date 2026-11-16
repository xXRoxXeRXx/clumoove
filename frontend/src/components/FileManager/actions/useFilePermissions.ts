import { useCallback, useMemo } from 'react';
import type { FileCapabilities, FileEntry } from '../../../api/files';

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

/** What the connection and each entry's `allowed_actions` permit, for single entries and the selection. */
export function useFilePermissions(capabilities: FileCapabilities, selectedEntries: FileEntry[]): FileActionPermissions {
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
  const canDeleteEntry = useCallback((entry: FileEntry) => entry.allowed_actions.includes('delete'), []);
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
    (action: 'archive' | 'delete' | 'copy' | 'move') =>
      selectedEntries.length > 0 &&
      selectedEntries.every((entry) =>
        action === 'archive' ? capabilities.archive === true : entry.allowed_actions.includes(action),
      ),
    [capabilities.archive, selectedEntries],
  );

  return useMemo<FileActionPermissions>(
    () => ({
      canCreateFolder: Boolean(capabilities.mkdir),
      canDownloadEntry,
      canRenameEntry,
      canCopyEntry,
      canMoveEntry,
      canDeleteEntry,
      hasEntryActions,
      canBatchDownload: selectedCan('archive'),
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
}
