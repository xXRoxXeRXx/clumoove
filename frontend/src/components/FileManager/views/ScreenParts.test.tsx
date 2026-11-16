import { act } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BatchSummaryBanner } from './BatchSummaryBanner';
import { LoadMoreFooter } from './LoadMoreFooter';
import { ProfileSidebar } from './ProfileSidebar';
import { useRenderHarness } from './renderHarness';
import { useViewMode } from './useViewMode';

describe('ProfileSidebar', () => {
  const view = useRenderHarness();
  const profiles = [
    { id: 'p1', name: 'Home', provider: 'webdav' },
    { id: 'p2', name: 'Work', provider: 'dropbox' },
  ];

  it('lists profiles, marks the active one and reports selection', async () => {
    const onSelect = vi.fn();
    await view.render(<ProfileSidebar profiles={profiles} activeId="p1" loading={false} onSelect={onSelect} />);
    const buttons = view.container.querySelectorAll('ul[aria-label="Connections"] button');
    expect(buttons[0].getAttribute('aria-current')).toBe('page');
    expect(buttons[1].hasAttribute('aria-current')).toBe(false);
    await view.click(buttons[1]);
    expect(onSelect).toHaveBeenCalledWith('p2');
  });

  it('shows loading and empty states, and the manage link only when provided', async () => {
    await view.render(<ProfileSidebar profiles={[]} activeId="" loading onSelect={vi.fn()} />);
    expect(view.container.textContent).not.toContain('No saved connections');
    await view.render(<ProfileSidebar profiles={[]} activeId="" loading={false} onSelect={vi.fn()} />);
    expect(view.container.textContent).toContain('No saved connections are available.');
    expect(view.container.textContent).not.toContain('Manage connections');
    const onOpenManager = vi.fn();
    await view.render(<ProfileSidebar profiles={profiles} activeId="p1" loading={false} onSelect={vi.fn()} onOpenManager={onOpenManager} />);
    await view.click([...view.container.querySelectorAll('button')].find((b) => b.textContent === 'Manage connections') ?? null);
    expect(onOpenManager).toHaveBeenCalled();
  });
});

describe('BatchSummaryBanner', () => {
  const view = useRenderHarness();

  it('announces the outcome as status and can be dismissed', async () => {
    const onDismiss = vi.fn();
    await view.render(<BatchSummaryBanner summary={{ success: 2, failed: 1 }} onDismiss={onDismiss} />);
    expect(view.container.querySelector('[role="status"]')!.textContent).toContain('2 items completed; 1 failed.');
    await view.click(view.container.querySelector('button[aria-label="Cancel"]'));
    expect(onDismiss).toHaveBeenCalled();
  });
});

describe('LoadMoreFooter', () => {
  const view = useRenderHarness();
  let observers: Array<{ callback: IntersectionObserverCallback; disconnect: ReturnType<typeof vi.fn> }>;

  beforeEach(() => {
    observers = [];
    vi.stubGlobal(
      'IntersectionObserver',
      class {
        disconnect = vi.fn();
        constructor(callback: IntersectionObserverCallback) {
          observers.push({ callback, disconnect: this.disconnect });
        }
        observe() {}
      },
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it('loads more from the button, or a spinner while loading', async () => {
    const onLoadMore = vi.fn();
    await view.render(<LoadMoreFooter loading={false} autoLoad={false} onLoadMore={onLoadMore} />);
    await view.click(view.container.querySelector('button'));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
    await view.render(<LoadMoreFooter loading autoLoad={false} onLoadMore={onLoadMore} />);
    expect(view.container.querySelector('button')).toBeNull();
  });

  it('loads automatically when the sentinel scrolls into view, only while autoLoad is on', async () => {
    const onLoadMore = vi.fn();
    await view.render(<LoadMoreFooter loading={false} autoLoad={false} onLoadMore={onLoadMore} />);
    expect(observers).toHaveLength(0);
    await view.render(<LoadMoreFooter loading={false} autoLoad onLoadMore={onLoadMore} />);
    expect(observers).toHaveLength(1);
    act(() => observers[0].callback([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver));
    expect(onLoadMore).toHaveBeenCalledTimes(1);
    await view.render(null);
    expect(observers[0].disconnect).toHaveBeenCalled();
  });
});

describe('useViewMode', () => {
  const view = useRenderHarness();
  const KEY = 'clumoove_file_manager_view_mode';

  function Probe() {
    const [mode, setMode] = useViewMode();
    return <button onClick={() => setMode(mode === 'list' ? 'grid' : 'list')}>{mode}</button>;
  }

  afterEach(() => localStorage.clear());

  it('defaults to list, restores a stored grid and persists changes', async () => {
    await view.render(<Probe />);
    expect(view.container.textContent).toBe('list');
    await view.click(view.container.querySelector('button'));
    expect(view.container.textContent).toBe('grid');
    expect(localStorage.getItem(KEY)).toBe('grid');
    await view.render(null);
    await view.render(<Probe />);
    expect(view.container.textContent).toBe('grid');
  });

  it('survives unavailable storage', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    await view.render(<Probe />);
    await view.click(view.container.querySelector('button'));
    expect(view.container.textContent).toBe('grid');
    vi.restoreAllMocks();
  });
});
