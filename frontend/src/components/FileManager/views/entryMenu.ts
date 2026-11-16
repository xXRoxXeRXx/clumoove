import type { FileEntry } from '../../../api/files';

export type EntryMenuAction = 'rename' | 'copy' | 'move' | 'download' | 'delete';

type EntryMenuPermissions = {
  canRenameEntry: (entry: FileEntry) => boolean;
  canCopyEntry: (entry: FileEntry) => boolean;
  canMoveEntry: (entry: FileEntry) => boolean;
  canDownloadEntry: (entry: FileEntry) => boolean;
  canDeleteEntry: (entry: FileEntry) => boolean;
};

/** The menu items an entry offers, in display order, derived from what its permissions allow. */
export function entryMenuActions(entry: FileEntry, permissions: EntryMenuPermissions): EntryMenuAction[] {
  const actions: EntryMenuAction[] = [];
  if (permissions.canRenameEntry(entry)) actions.push('rename');
  if (permissions.canCopyEntry(entry)) actions.push('copy');
  if (permissions.canMoveEntry(entry)) actions.push('move');
  if (permissions.canDownloadEntry(entry)) actions.push('download');
  if (permissions.canDeleteEntry(entry)) actions.push('delete');
  return actions;
}
