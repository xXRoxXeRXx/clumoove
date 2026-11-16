import type { ComponentType, SVGProps } from 'react';
import type { FileEntry } from '../../../api/files';
import { ArrowDownTrayIcon, ArrowRightIcon, ClipboardDocumentIcon, PencilIcon, TrashIcon } from '../../icons';
import type { MenuAnchor } from './entryRows';

type EntryMenuPermissions = {
  canRenameEntry: (entry: FileEntry) => boolean;
  canCopyEntry: (entry: FileEntry) => boolean;
  canMoveEntry: (entry: FileEntry) => boolean;
  canDownloadEntry: (entry: FileEntry) => boolean;
  canDeleteEntry: (entry: FileEntry) => boolean;
};

type EntryMenuItem = {
  action: string;
  /** The permission that gates the item. */
  permission: keyof EntryMenuPermissions;
  labelKey: string;
  Icon: ComponentType<SVGProps<SVGSVGElement>>;
  danger?: boolean;
};

/** The one definition of the entry menu: its actions, their order, gating permission, label and icon. */
export const ENTRY_MENU_ITEMS = [
  { action: 'rename', permission: 'canRenameEntry', labelKey: 'files.rename', Icon: PencilIcon },
  { action: 'copy', permission: 'canCopyEntry', labelKey: 'files.copy', Icon: ClipboardDocumentIcon },
  { action: 'move', permission: 'canMoveEntry', labelKey: 'files.move', Icon: ArrowRightIcon },
  { action: 'download', permission: 'canDownloadEntry', labelKey: 'files.downloadAction', Icon: ArrowDownTrayIcon },
  { action: 'delete', permission: 'canDeleteEntry', labelKey: 'files.deleteAction', Icon: TrashIcon, danger: true },
] as const satisfies readonly EntryMenuItem[];

export type EntryMenuAction = (typeof ENTRY_MENU_ITEMS)[number]['action'];

/** How the menu presents one action. */
export function entryMenuItem(action: EntryMenuAction): EntryMenuItem {
  return ENTRY_MENU_ITEMS.find((item) => item.action === action) ?? ENTRY_MENU_ITEMS[0];
}

/** The menu items an entry offers, in display order, derived from what its permissions allow. */
export function entryMenuActions(entry: FileEntry, permissions: EntryMenuPermissions): EntryMenuAction[] {
  return ENTRY_MENU_ITEMS.filter((item) => permissions[item.permission](entry)).map((item) => item.action);
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
