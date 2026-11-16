import type { FileEntry } from '../../../api/files';
import type { MenuAnchor } from './entryRows';

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

export type EntryMenuState = {
  ref: string;
  position: { x: number; y: number; align: 'left' | 'right' };
};

/** The menu state after the user asks for an entry's menu from `anchor`; a repeated button press closes it. */
export function nextMenuState(current: EntryMenuState | null, ref: string, anchor: MenuAnchor): EntryMenuState | null {
  if (anchor.type === 'point') return { ref, position: { x: anchor.x, y: anchor.y, align: 'left' } };
  if (current?.ref === ref) return null;
  return { ref, position: { x: anchor.right, y: anchor.bottom + 4, align: 'right' } };
}
