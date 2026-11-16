import { act } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { EntryList } from './EntryList';
import { EntryGrid } from './EntryGrid';
import type { EntryRowModel } from './entryRows';
import { useRenderHarness } from './renderHarness';

const row = (overrides: Partial<EntryRowModel> = {}): EntryRowModel => ({
  ref: 'r1',
  name: 'report.txt',
  isDirectory: false,
  sizeLabel: '2 KB',
  modifiedLabel: 'Jan 1',
  selected: false,
  clickable: true,
  nameDisabled: false,
  hasActions: true,
  menuOpen: false,
  thumbnail: <i data-testid="thumb" />,
  ...overrides,
});

const intents = () => ({
  onActivate: vi.fn(),
  onToggleSelect: vi.fn(),
  onOpenMenu: vi.fn(),
});

describe('EntryList', () => {
  const view = useRenderHarness();
  const sort = { field: 'name' as const, direction: 'asc' as const, onSort: vi.fn() };
  const selection = { active: false, allSelected: false, partiallySelected: false, onToggleAll: vi.fn() };

  const renderList = (rows: EntryRowModel[], i = intents(), sel = selection) =>
    view.render(<EntryList rows={rows} selection={sel} sort={sort} {...i} />);

  it('renders one row per model with labelled controls', async () => {
    await renderList([row(), row({ ref: 'r2', name: 'docs', isDirectory: true, sizeLabel: null, modifiedLabel: null })]);
    const rows = view.container.querySelectorAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain('report.txt');
    expect(rows[0].textContent).toContain('2 KB');
    expect(rows[1].textContent).toContain('Folder');
    expect(view.container.querySelector('[aria-label="Select report.txt"]')).not.toBeNull();
    const actionsBtn = view.container.querySelector('[aria-label="Actions for report.txt"]');
    expect(actionsBtn).not.toBeNull();
    expect(actionsBtn?.getAttribute('title')).toBe('Actions for report.txt');
    expect(view.container.querySelectorAll('[data-testid="thumb"]')).toHaveLength(2);
  });

  it('reports row activation with the ctrl/meta modifier and keeps checkbox clicks out of it', async () => {
    const i = intents();
    await renderList([row()], i);
    await view.click(view.container.querySelector('tbody tr'));
    expect(i.onActivate).toHaveBeenLastCalledWith('r1', { toggleSelection: false });
    await act(async () => {
      view.container.querySelector('tbody tr')!.dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }));
    });
    expect(i.onActivate).toHaveBeenLastCalledWith('r1', { toggleSelection: true });
    i.onActivate.mockClear();
    await view.click(view.container.querySelector('[aria-label="Select report.txt"]'));
    expect(i.onToggleSelect).toHaveBeenCalledWith('r1');
    expect(i.onActivate).not.toHaveBeenCalled();
  });

  it('activates once when the name button is clicked', async () => {
    const i = intents();
    await renderList([row()], i);
    await view.click(view.container.querySelector('tbody button[title="report.txt"]'));
    expect(i.onActivate).toHaveBeenCalledTimes(1);
  });

  it('disables the name button when the model says so', async () => {
    await renderList([row({ nameDisabled: true })]);
    expect((view.container.querySelector('tbody button[title="report.txt"]') as HTMLButtonElement).disabled).toBe(true);
  });

  it('opens the menu from the actions button, anchored to the button, and via context menu at the pointer', async () => {
    const i = intents();
    await renderList([row()], i);
    const button = view.container.querySelector('[aria-label="Actions for report.txt"]')!;
    expect(button.getAttribute('aria-haspopup')).toBe('menu');
    expect(button.getAttribute('aria-expanded')).toBe('false');
    await view.click(button);
    expect(i.onOpenMenu).toHaveBeenCalledWith('r1', expect.objectContaining({ type: 'button' }));
    expect(i.onActivate).not.toHaveBeenCalled();
    await act(async () => {
      view.container.querySelector('tbody tr')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 7, clientY: 9 }));
    });
    expect(i.onOpenMenu).toHaveBeenLastCalledWith('r1', { type: 'point', x: 7, y: 9 });
  });

  it('shows no actions button and ignores context menu for rows without actions', async () => {
    const i = intents();
    await renderList([row({ hasActions: false })], i);
    expect(view.container.querySelector('[aria-label="Actions for report.txt"]')).toBeNull();
    const event = new MouseEvent('contextmenu', { bubbles: true, cancelable: true });
    await act(async () => {
      view.container.querySelector('tbody tr')!.dispatchEvent(event);
    });
    expect(i.onOpenMenu).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it('exposes aria-selected only while a selection is active, and the expanded state of the open menu', async () => {
    await renderList([row({ selected: true, menuOpen: true })]);
    expect(view.container.querySelector('tbody tr')!.hasAttribute('aria-selected')).toBe(false);
    expect(view.container.querySelector('[aria-label="Actions for report.txt"]')!.getAttribute('aria-expanded')).toBe('true');
    await renderList([row({ selected: true })], intents(), { ...selection, active: true });
    expect(view.container.querySelector('tbody tr')!.getAttribute('aria-selected')).toBe('true');
  });

  it('marks the sorted column and sorts through the header buttons', async () => {
    await renderList([row()]);
    const headers = view.container.querySelectorAll('th[aria-sort]');
    expect(Array.from(headers).map((h) => h.getAttribute('aria-sort'))).toEqual(['ascending', 'none', 'none']);
    await view.click(view.container.querySelector('th[aria-sort="none"] button'));
    expect(sort.onSort).toHaveBeenCalledWith('size');
  });

  it('wires select-all with an indeterminate state', async () => {
    await renderList([row()], intents(), { ...selection, partiallySelected: true });
    const box = view.container.querySelector('[aria-label="Select all loaded items"]') as HTMLInputElement;
    expect(box.indeterminate).toBe(true);
    await view.click(box);
    expect(selection.onToggleAll).toHaveBeenCalled();
  });
});

describe('EntryGrid', () => {
  const view = useRenderHarness();
  const renderGrid = (rows: EntryRowModel[], i = intents(), selectionActive = false) =>
    view.render(<EntryGrid rows={rows} selectionActive={selectionActive} {...i} />);

  it('structures the grid as grid > row > gridcell so the ARIA roles are valid', async () => {
    await renderGrid([row(), row({ ref: 'r2', name: 'docs' })]);
    const grid = view.container.querySelector('[role="grid"]')!;
    const rowElements = Array.from(grid.children);
    expect(rowElements).toHaveLength(2);
    for (const rowElement of rowElements) {
      expect(rowElement.getAttribute('role')).toBe('row');
      expect(rowElement.children).toHaveLength(1);
      expect(rowElement.children[0].getAttribute('role')).toBe('gridcell');
    }
    expect(grid.querySelectorAll('[role="gridcell"]')).toHaveLength(2);
  });

  it('renders a labelled grid with a gridcell per entry', async () => {
    await renderGrid([row(), row({ ref: 'r2', name: 'docs', isDirectory: true, sizeLabel: null })]);
    expect(view.container.querySelector('[role="grid"]')!.getAttribute('aria-label')).toBe('File Manager');
    const cells = view.container.querySelectorAll('[role="gridcell"]');
    expect(cells).toHaveLength(2);
    expect(cells[1].textContent).toContain('Folder');
  });

  it('activates via native card buttons and preserves direct gridcell click/key handling', async () => {
    const i = intents();
    await renderGrid([row(), row({ ref: 'r2', name: 'locked.txt', clickable: false })], i);
    const [first, second] = Array.from(view.container.querySelectorAll('[role="gridcell"]'));
    expect(first.hasAttribute('tabindex')).toBe(false);
    expect(second.hasAttribute('tabindex')).toBe(false);

    const cardButton = (cell: Element) =>
      Array.from(cell.querySelectorAll<HTMLButtonElement>('button')).find((b) => !b.hasAttribute('aria-haspopup'))!;
    expect(cardButton(first).disabled).toBe(false);
    expect(cardButton(second).disabled).toBe(true);

    await view.click(cardButton(first));
    expect(i.onActivate).toHaveBeenLastCalledWith('r1', { toggleSelection: false });
    await view.click(first);
    expect(i.onActivate).toHaveBeenCalledTimes(2);
    for (const key of ['Enter', ' ']) {
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
      await act(async () => {
        first.dispatchEvent(event);
      });
      expect(event.defaultPrevented).toBe(true);
    }
    expect(i.onActivate).toHaveBeenCalledTimes(4);
    expect(first.querySelector('[aria-label="Actions for report.txt"]')?.getAttribute('title')).toBe('Actions for report.txt');
  });

  it('keeps checkbox clicks and keys from activating the card, and toggles selection', async () => {
    const i = intents();
    await renderGrid([row()], i);
    const box = view.container.querySelector('[aria-label="Select report.txt"]')!;
    await act(async () => {
      box.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true }));
    });
    await view.click(box);
    expect(i.onActivate).not.toHaveBeenCalled();
    expect(i.onToggleSelect).toHaveBeenCalledWith('r1');
  });

  it('opens the menu from the actions button and context menu', async () => {
    const i = intents();
    await renderGrid([row()], i);
    await view.click(view.container.querySelector('[aria-label="Actions for report.txt"]'));
    expect(i.onOpenMenu).toHaveBeenCalledWith('r1', expect.objectContaining({ type: 'button' }));
    await act(async () => {
      view.container.querySelector('[role="gridcell"]')!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 1, clientY: 2 }));
    });
    expect(i.onOpenMenu).toHaveBeenLastCalledWith('r1', { type: 'point', x: 1, y: 2 });
  });
});
