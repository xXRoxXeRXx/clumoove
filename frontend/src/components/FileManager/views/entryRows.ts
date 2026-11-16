import type { ReactNode } from 'react';
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

/** Where an entry menu opens from: a button's bottom-right corner or the pointer. */
export type MenuAnchor = { type: 'button'; right: number; bottom: number } | { type: 'point'; x: number; y: number };

/** What the user can do with an entry in a list or grid, expressed as intents on entry refs. */
export type EntryCollectionIntents = {
  onActivate: (ref: string, options: { toggleSelection: boolean }) => void;
  onToggleSelect: (ref: string) => void;
  onOpenMenu: (ref: string, anchor: MenuAnchor) => void;
};
