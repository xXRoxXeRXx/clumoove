import { act } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { EntryActionMenu, type EntryMenuPosition } from './EntryActionMenu';
import { entryMenuActions } from './entryMenu';
import { useRenderHarness } from './renderHarness';
import type { FileEntry } from '../../../api/files';

const position: EntryMenuPosition = { x: 100, y: 100, align: 'left' };

describe('EntryActionMenu', () => {
  const view = useRenderHarness();
  const onSelect = vi.fn();
  const onDismiss = vi.fn();

  const renderMenu = (actions = ['rename', 'copy', 'move', 'download', 'delete'] as const) =>
    view.render(<EntryActionMenu name="a.txt" position={position} actions={[...actions]} onSelect={onSelect} onDismiss={onDismiss} />);

  const menu = () => document.body.querySelector('[role="menu"]');

  it('renders a labelled menu in a body portal with one menuitem per action', async () => {
    onSelect.mockReset();
    await renderMenu();
    expect(menu()?.getAttribute('aria-label')).toBe('Actions for a.txt');
    expect(view.container.querySelector('[role="menu"]')).toBeNull();
    expect([...document.body.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent)).toEqual([
      'Rename', 'Copy', 'Move', 'Download', 'Delete',
    ]);
  });

  it('reports the chosen action as an intent', async () => {
    onSelect.mockReset();
    await renderMenu();
    await view.click(document.body.querySelectorAll('[role="menuitem"]')[1]);
    expect(onSelect).toHaveBeenCalledWith('copy');
  });

  it('dismisses on Escape, outside pointerdown and scroll, but not on inside pointerdown', async () => {
    onDismiss.mockReset();
    await renderMenu();
    act(() => {
      menu()!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    });
    expect(onDismiss).not.toHaveBeenCalled();
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(onDismiss).toHaveBeenCalledTimes(1);
    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    });
    expect(onDismiss).toHaveBeenCalledTimes(2);
    act(() => {
      window.dispatchEvent(new Event('scroll'));
    });
    expect(onDismiss).toHaveBeenCalledTimes(3);
  });

  it('removes its window listeners when unmounted', async () => {
    onDismiss.mockReset();
    await renderMenu();
    await view.render(null);
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(onDismiss).not.toHaveBeenCalled();
  });
});

describe('entryMenuActions', () => {
  const entry = { ref: 'r', name: 'a', kind: 'file', allowed_actions: [] } as unknown as FileEntry;
  const permissions = {
    canRenameEntry: () => true,
    canCopyEntry: () => false,
    canMoveEntry: () => true,
    canDownloadEntry: () => false,
    canDeleteEntry: () => true,
  };

  it('lists only the actions the permissions allow, in menu order', () => {
    expect(entryMenuActions(entry, permissions)).toEqual(['rename', 'move', 'delete']);
  });
});
