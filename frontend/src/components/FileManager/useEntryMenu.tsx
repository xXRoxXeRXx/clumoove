import { useMemo, useState, type ReactNode } from 'react';
import type { FileEntry } from '../../api/files';
import type { FileManagerActions } from './useFileManagerActions';
import { EntryActionMenu } from './views/EntryActionMenu';
import { entryMenuActions, nextMenuState, type EntryMenuAction, type EntryMenuState } from './views/entryMenu';
import type { MenuAnchor } from './views/entryRows';

export type EntryMenu = {
  /** Ref of the entry whose menu is open, if any. */
  openRef: string | null;
  openMenu: (ref: string, anchor: MenuAnchor) => void;
  /** The open menu, to render once anywhere in the screen. */
  element: ReactNode;
};

/** Connects the per-entry action menu to the file actions: which items an entry offers and what choosing one does. */
export function useEntryMenu(entries: FileEntry[], actions: FileManagerActions): EntryMenu {
  const [menu, setMenu] = useState<EntryMenuState | null>(null);
  const entriesByRef = useMemo(() => new Map(entries.map((entry) => [entry.ref, entry])), [entries]);
  const entry = menu ? entriesByRef.get(menu.ref) : undefined;

  const run = (target: FileEntry, action: EntryMenuAction) => {
    setMenu(null);
    if (action === 'rename') actions.rename(target);
    else if (action === 'copy') actions.copy(target);
    else if (action === 'move') actions.move(target);
    else if (action === 'download') void actions.download(target);
    else actions.delete(target);
  };

  return {
    openRef: menu?.ref ?? null,
    openMenu: (ref, anchor) => setMenu((current) => nextMenuState(current, ref, anchor)),
    element:
      menu && entry ? (
        <EntryActionMenu
          name={entry.name}
          position={menu.position}
          actions={entryMenuActions(entry, actions.permissions)}
          onSelect={(action) => run(entry, action)}
          onDismiss={() => setMenu(null)}
        />
      ) : null,
  };
}
