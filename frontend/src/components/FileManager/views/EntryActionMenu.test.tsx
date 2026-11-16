import { act } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { EntryActionMenu, type EntryMenuPosition } from './EntryActionMenu';
import { entryMenuActions, nextMenuState } from './entryMenu';
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
    expect(Array.from(document.body.querySelectorAll('[role="menuitem"]')).map((i) => i.textContent)).toEqual([
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

  describe('keyboard navigation', () => {
    const items = () => Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]'));
    const press = (key: string) =>
      act(() => {
        (document.activeElement ?? document.body).dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
      });

    it('moves focus to the first item when it opens', async () => {
      await renderMenu();
      expect(document.activeElement).toBe(items()[0]);
    });

    it('moves focus with the arrow keys, wrapping at both ends', async () => {
      await renderMenu();
      press('ArrowDown');
      expect(document.activeElement).toBe(items()[1]);
      press('ArrowUp');
      press('ArrowUp');
      expect(document.activeElement).toBe(items()[4]);
      press('ArrowDown');
      expect(document.activeElement).toBe(items()[0]);
    });

    it('jumps to the first and last item with Home and End', async () => {
      await renderMenu();
      press('End');
      expect(document.activeElement).toBe(items()[4]);
      press('Home');
      expect(document.activeElement).toBe(items()[0]);
    });

    it('activates the focused item', async () => {
      onSelect.mockReset();
      await renderMenu();
      press('ArrowDown');
      await view.click(document.activeElement);
      expect(onSelect).toHaveBeenCalledWith('copy');
    });

    it('keeps only the focused item in the tab sequence', async () => {
      await renderMenu();
      expect(items().map((item) => item.tabIndex)).toEqual([0, -1, -1, -1, -1]);
      press('ArrowDown');
      expect(items().map((item) => item.tabIndex)).toEqual([-1, 0, -1, -1, -1]);
    });

    it('dismisses on Tab instead of leaving a focus trap behind', async () => {
      onDismiss.mockReset();
      await renderMenu();
      press('Tab');
      expect(onDismiss).toHaveBeenCalledTimes(1);
    });

    it('returns focus to the element that opened it when it closes', async () => {
      const trigger = document.createElement('button');
      document.body.append(trigger);
      trigger.focus();
      await renderMenu();
      expect(document.activeElement).not.toBe(trigger);
      await view.render(null);
      expect(document.activeElement).toBe(trigger);
      trigger.remove();
    });

    it('does not steal focus back when the opener is gone', async () => {
      const trigger = document.createElement('button');
      document.body.append(trigger);
      trigger.focus();
      await renderMenu();
      trigger.remove();
      await view.render(null);
      expect(document.activeElement).toBe(document.body);
    });
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

describe('nextMenuState', () => {
  it('opens at a button, right-aligned just below it', () => {
    expect(nextMenuState(null, 'a', { type: 'button', right: 300, bottom: 50 })).toEqual({ ref: 'a', position: { x: 300, y: 54, align: 'right' } });
  });

  it('opens at the pointer, left-aligned', () => {
    expect(nextMenuState(null, 'a', { type: 'point', x: 7, y: 9 })).toEqual({ ref: 'a', position: { x: 7, y: 9, align: 'left' } });
  });

  it('closes when the same entry button is pressed again, but a context menu always reopens', () => {
    const open = nextMenuState(null, 'a', { type: 'button', right: 1, bottom: 1 });
    expect(nextMenuState(open, 'a', { type: 'button', right: 1, bottom: 1 })).toBeNull();
    expect(nextMenuState(open, 'a', { type: 'point', x: 3, y: 4 })).toEqual({ ref: 'a', position: { x: 3, y: 4, align: 'left' } });
    expect(nextMenuState(open, 'b', { type: 'button', right: 1, bottom: 1 })?.ref).toBe('b');
  });
});
