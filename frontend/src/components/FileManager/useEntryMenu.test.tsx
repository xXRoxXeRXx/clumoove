import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../../i18n';
import type { FileEntry } from '../../api/files';
import { useEntryMenu, type EntryMenu } from './useEntryMenu';
import type { FileManagerActions } from './useFileManagerActions';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const entry = { ref: 'f1', name: 'report.txt', kind: 'file', size: 1, allowed_actions: ['download'] } as unknown as FileEntry;

function makeActions(overrides: Partial<Record<'canRenameEntry' | 'canDownloadEntry' | 'canDeleteEntry', boolean>> = {}) {
  return {
    permissions: {
      canRenameEntry: () => overrides.canRenameEntry ?? true,
      canCopyEntry: () => false,
      canMoveEntry: () => false,
      canDownloadEntry: () => overrides.canDownloadEntry ?? true,
      canDeleteEntry: () => overrides.canDeleteEntry ?? true,
    },
    rename: vi.fn(),
    copy: vi.fn(),
    move: vi.fn(),
    delete: vi.fn(),
    download: vi.fn(async () => undefined),
  } as unknown as FileManagerActions;
}

describe('useEntryMenu', () => {
  let container: HTMLDivElement;
  let root: Root;
  let menu: EntryMenu;

  function Probe({ actions }: { actions: FileManagerActions }) {
    menu = useEntryMenu([entry], actions);
    return <>{menu.element}</>;
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  const items = () => Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'));

  it('offers the permitted actions of the entry and closes after one is chosen', async () => {
    const actions = makeActions({ canDeleteEntry: false });
    await act(async () => root.render(<Probe actions={actions} />));
    expect(menu.openRef).toBeNull();
    expect(items()).toHaveLength(0);

    act(() => menu.openMenu('f1', { type: 'point', x: 1, y: 2 }));
    expect(menu.openRef).toBe('f1');
    expect(items().map((item) => item.textContent)).toEqual(['Rename', 'Download']);

    await act(async () => items()[1].click());
    expect(actions.download).toHaveBeenCalledWith(entry);
    expect(menu.openRef).toBeNull();
    expect(items()).toHaveLength(0);
  });

  it('routes each action to its intent and toggles closed on a repeated button press', async () => {
    const actions = makeActions();
    await act(async () => root.render(<Probe actions={actions} />));
    act(() => menu.openMenu('f1', { type: 'button', right: 10, bottom: 10 }));
    act(() => menu.openMenu('f1', { type: 'button', right: 10, bottom: 10 }));
    expect(menu.openRef).toBeNull();

    act(() => menu.openMenu('f1', { type: 'point', x: 1, y: 2 }));
    await act(async () => items()[0].click());
    expect(actions.rename).toHaveBeenCalledWith(entry);

    act(() => menu.openMenu('f1', { type: 'point', x: 1, y: 2 }));
    await act(async () => items()[2].click());
    expect(actions.delete).toHaveBeenCalledWith(entry);
  });

  it('ignores a menu for an entry that is no longer listed', async () => {
    await act(async () => root.render(<Probe actions={makeActions()} />));
    act(() => menu.openMenu('gone', { type: 'point', x: 1, y: 2 }));
    expect(items()).toHaveLength(0);
  });
});
