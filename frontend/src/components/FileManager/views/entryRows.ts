import type { KeyboardEvent, MouseEvent, ReactNode } from 'react';
import type { FileEntry } from '../../../api/files';

/** Everything a list row or grid card needs to render one entry; no behaviour, no credentials. */
export type EntryRowModel = {
  ref: string;
  name: string;
  isDirectory: boolean;
  /** Formatted size, or null for folders. */
  sizeLabel: string | null;
  /** Formatted modification time, or null when the provider reports none. */
  modifiedLabel: string | null;
  selected: boolean;
  /** The row reacts to a click: it opens, previews, or toggles selection. */
  clickable: boolean;
  /** The name control cannot be used right now (browsing unavailable or loading). */
  nameDisabled: boolean;
  /** The entry offers at least one action, so a menu can be opened. */
  hasActions: boolean;
  menuOpen: boolean;
  thumbnail: ReactNode;
};

export type EntryRowContext = {
  selectionActive: boolean;
  isSelected: (ref: string) => boolean;
  isInteractive: (entry: FileEntry) => boolean;
  canBrowse: boolean;
  loading: boolean;
  hasEntryActions: (entry: FileEntry) => boolean;
  menuRef: string | null;
  formatBytes: (size: number) => string;
  formatDateTime: (iso: string) => string;
  renderThumbnail: (entry: FileEntry) => ReactNode;
};

export function toEntryRow(entry: FileEntry, ctx: EntryRowContext): EntryRowModel {
  const isDirectory = entry.kind === 'directory';
  return {
    ref: entry.ref,
    name: entry.name,
    isDirectory,
    sizeLabel: isDirectory ? null : ctx.formatBytes(entry.size),
    modifiedLabel: entry.modified_at ? ctx.formatDateTime(entry.modified_at) : null,
    selected: ctx.isSelected(entry.ref),
    clickable: ctx.isInteractive(entry) || ctx.selectionActive,
    nameDisabled: !ctx.selectionActive && isDirectory && (!ctx.canBrowse || ctx.loading),
    hasActions: ctx.hasEntryActions(entry),
    menuOpen: ctx.menuRef === entry.ref,
    thumbnail: ctx.renderThumbnail(entry),
  };
}

/** What the browsing area shows instead of entries, or `ready` when it has entries to render. */
export type ListingState = 'no-profile' | 'unavailable' | 'loading' | 'ready';

/** The listing state of the browsing area, from what is selected and what the connection supports. */
export function listingState({ hasProfile, canBrowse, loading }: { hasProfile: boolean; canBrowse: boolean; loading: boolean }): ListingState {
  if (!hasProfile) return 'no-profile';
  if (!canBrowse && !loading) return 'unavailable';
  return loading ? 'loading' : 'ready';
}

/** Where an entry menu opens from: a button's bottom-right corner or the pointer. */
export type MenuAnchor = { type: 'button'; right: number; bottom: number } | { type: 'point'; x: number; y: number };

/** What the user can do with an entry in a list or grid, expressed as intents on entry refs. */
export type EntryCollectionIntents = {
  onActivate: (ref: string, options: { toggleSelection: boolean }) => void;
  onToggleSelect: (ref: string) => void;
  onOpenMenu: (ref: string, anchor: MenuAnchor) => void;
};

/** Shared activation and menu-trigger handlers for list and grid entry collections. */
export function entryCollectionHandlers({ onActivate, onOpenMenu }: Pick<EntryCollectionIntents, 'onActivate' | 'onOpenMenu'>) {
  const activate = (ref: string, event: MouseEvent | KeyboardEvent) =>
    onActivate(ref, { toggleSelection: event.ctrlKey || event.metaKey });

  const openMenuFromButton = (ref: string, event: MouseEvent) => {
    event.stopPropagation();
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect();
    onOpenMenu(ref, { type: 'button', right: rect.right, bottom: rect.bottom });
  };

  const openContextMenu = (row: EntryRowModel, event: MouseEvent) => {
    if (!row.hasActions) return;
    event.preventDefault();
    event.stopPropagation();
    onOpenMenu(row.ref, { type: 'point', x: event.clientX, y: event.clientY });
  };

  return { activate, openMenuFromButton, openContextMenu };
}
