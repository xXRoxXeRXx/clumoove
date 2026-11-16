import { act } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { BrowseToolbar } from './BrowseToolbar';
import { SelectionToolbar } from './SelectionToolbar';
import { useRenderHarness } from './renderHarness';

const crumbs = (n: number) => Array.from({ length: n }, (_, i) => ({ ref: i === 0 ? null : `ref-${i}`, name: i === 0 ? 'Root' : `Dir${i}` }));

describe('BrowseToolbar', () => {
  const view = useRenderHarness();
  const base = () => ({
    breadcrumbs: crumbs(2),
    canGoUp: true,
    onUp: vi.fn(),
    onNavigate: vi.fn(),
    uploadSlot: <span data-testid="upload-slot" />,
    newFolder: { disabled: false, unavailable: false, onClick: vi.fn() },
    viewMode: 'list' as const,
    onViewModeChange: vi.fn(),
    refresh: { disabled: false, onClick: vi.fn() },
  });
  const byLabel = (label: string) => view.container.querySelector(`[aria-label="${label}"]`) as HTMLButtonElement | null;

  it('renders the controls, the upload slot and reports each intent', async () => {
    const props = base();
    await view.render(<BrowseToolbar {...props} />);
    expect(view.container.querySelector('[data-testid="upload-slot"]')).not.toBeNull();
    await view.click(byLabel('Go to parent folder'));
    await view.click(byLabel('New folder'));
    await view.click(byLabel('Refresh files'));
    await view.click(byLabel('Grid view'));
    expect(props.onUp).toHaveBeenCalledTimes(1);
    expect(props.newFolder.onClick).toHaveBeenCalledTimes(1);
    expect(props.refresh.onClick).toHaveBeenCalledTimes(1);
    expect(props.onViewModeChange).toHaveBeenCalledWith('grid');
    expect(byLabel('List view')!.getAttribute('aria-pressed')).toBe('true');
    expect(byLabel('Grid view')!.getAttribute('aria-pressed')).toBe('false');
  });

  it('reflects disabled state and explains why folder creation is unavailable', async () => {
    const props = { ...base(), canGoUp: false, newFolder: { disabled: true, unavailable: true, onClick: vi.fn() }, refresh: { disabled: true, onClick: vi.fn() } };
    await view.render(<BrowseToolbar {...props} />);
    expect(byLabel('Go to parent folder')!.disabled).toBe(true);
    expect(byLabel('New folder')!.disabled).toBe(true);
    expect(byLabel('New folder')!.title).toBe('Folder creation is not supported for this connection.');
    expect(byLabel('Refresh files')!.disabled).toBe(true);
  });

  it('navigates by breadcrumb index and disables the current folder', async () => {
    const props = { ...base(), breadcrumbs: crumbs(3) };
    await view.render(<BrowseToolbar {...props} />);
    const nav = view.container.querySelector('nav[aria-label="Current folder"]')!;
    const buttons = nav.querySelectorAll('button');
    expect(Array.from(buttons).map((b) => b.disabled)).toEqual([false, false, true]);
    await view.click(buttons[1]);
    expect(props.onNavigate).toHaveBeenCalledWith(1);
  });

  it('collapses deep paths into a menu that closes on Escape, outside press and selection', async () => {
    const props = { ...base(), breadcrumbs: crumbs(6) };
    await view.render(<BrowseToolbar {...props} />);
    const more = byLabel('More folders')!;
    expect(more.getAttribute('aria-expanded')).toBe('false');
    expect(view.container.querySelector('[role="menu"]')).toBeNull();

    await view.click(more);
    expect(more.getAttribute('aria-expanded')).toBe('true');
    expect(Array.from(view.container.querySelectorAll('[role="menuitem"]')).map((i) => i.textContent)).toEqual(['Dir1', 'Dir2', 'Dir3']);

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(view.container.querySelector('[role="menu"]')).toBeNull();

    await view.click(more);
    act(() => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    });
    expect(view.container.querySelector('[role="menu"]')).toBeNull();

    await view.click(more);
    await view.click(view.container.querySelectorAll('[role="menuitem"]')[1]);
    expect(props.onNavigate).toHaveBeenCalledWith(2);
    expect(view.container.querySelector('[role="menu"]')).toBeNull();
  });

  it('goes up from the parent crumb of a collapsed path', async () => {
    const props = { ...base(), breadcrumbs: crumbs(6) };
    await view.render(<BrowseToolbar {...props} />);
    await view.click(view.container.querySelector('nav button[title="Dir4"]'));
    expect(props.onUp).toHaveBeenCalledTimes(1);
    expect((view.container.querySelector('nav button[title="Dir5"]') as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('SelectionToolbar', () => {
  const view = useRenderHarness();
  const base = () => ({
    count: 3,
    busy: false,
    canDownloadArchive: true,
    canCopy: true,
    canMove: true,
    canDelete: true,
    onClear: vi.fn(),
    onDownloadArchive: vi.fn(),
    onCopy: vi.fn(),
    onMove: vi.fn(),
    onDelete: vi.fn(),
  });
  const byLabel = (label: string) => view.container.querySelector(`[aria-label="${label}"]`) as HTMLButtonElement;

  it('is a labelled toolbar that reports every batch intent', async () => {
    const props = base();
    await view.render(<SelectionToolbar {...props} />);
    expect(view.container.querySelector('[role="toolbar"]')!.getAttribute('aria-label')).toBe('Selected file actions');
    await view.click(byLabel('Clear selection'));
    await view.click(byLabel('Download ZIP'));
    await view.click(byLabel('Copy'));
    await view.click(byLabel('Move'));
    await view.click(byLabel('Delete'));
    expect(props.onClear).toHaveBeenCalled();
    expect(props.onDownloadArchive).toHaveBeenCalled();
    expect(props.onCopy).toHaveBeenCalled();
    expect(props.onMove).toHaveBeenCalled();
    expect(props.onDelete).toHaveBeenCalled();
  });

  it('shows the selected count', async () => {
    await view.render(<SelectionToolbar {...base()} />);
    expect(view.container.textContent).toContain('3');
  });

  it('disables batch buttons when not permitted or while a batch runs, but keeps clear available', async () => {
    await view.render(<SelectionToolbar {...base()} canMove={false} canCopy={false} />);
    expect(byLabel('Move').disabled).toBe(true);
    expect(byLabel('Copy').disabled).toBe(true);
    expect(byLabel('Delete').disabled).toBe(false);
    await view.render(<SelectionToolbar {...base()} busy />);
    for (const label of ['Download ZIP', 'Copy', 'Move', 'Delete']) expect(byLabel(label).disabled).toBe(true);
    expect(byLabel('Clear selection').disabled).toBe(false);
  });
});
