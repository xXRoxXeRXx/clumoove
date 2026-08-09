import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { MigrationsDashboard } from './MigrationsDashboard';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

type DashboardTab = 'migrations' | 'sync';

interface TabNavigationCase {
  from: DashboardTab;
  key: string;
  selected: DashboardTab;
  unselected: DashboardTab;
}

vi.mock('../contexts/useConfirm', () => ({ useConfirm: () => vi.fn() }));
vi.mock('../contexts/useToast', () => ({ useToast: () => vi.fn() }));
vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', () => ({
  apiFetch: vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve([]) })),
}));

describe('MigrationsDashboard tabs', () => {
  let container: HTMLDivElement;
  let root: Root;

  function getTab(tab: DashboardTab): HTMLButtonElement | null {
    return container.querySelector<HTMLButtonElement>(`#${tab}-tab`);
  }

  function expectSelection(selectedTab: DashboardTab, unselectedTab: DashboardTab): void {
    const selected = getTab(selectedTab)!;
    const unselected = getTab(unselectedTab)!;
    const panel = container.querySelector<HTMLElement>('[role="tabpanel"]')!;
    expect(selected.getAttribute('aria-selected')).toBe('true');
    expect(selected.tabIndex).toBe(0);
    expect(unselected.getAttribute('aria-selected')).toBe('false');
    expect(unselected.tabIndex).toBe(-1);
    expect(document.activeElement).toBe(selected);
    expect(panel.getAttribute('aria-labelledby')).toBe(selected.id);
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('moves selection, focus, the roving tab stop, and panel for Arrow, Home, and End keys', async () => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    await act(async () => {
      root.render(
        <MigrationsDashboard
          apiUrl="https://api.example.test"
          token="token"
          user={null}
          onStartNewMigration={vi.fn()}
          onSelectActiveMigration={vi.fn()}
        />,
      );
    });
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    const cases: TabNavigationCase[] = [
      { from: 'migrations', key: 'ArrowRight', selected: 'sync', unselected: 'migrations' },
      { from: 'sync', key: 'ArrowLeft', selected: 'migrations', unselected: 'sync' },
      { from: 'sync', key: 'Home', selected: 'migrations', unselected: 'sync' },
      { from: 'migrations', key: 'End', selected: 'sync', unselected: 'migrations' },
    ];

    for (const testCase of cases) {
      const from = getTab(testCase.from)!;
      await act(async () => {
        from.focus();
        from.dispatchEvent(new KeyboardEvent('keydown', { key: testCase.key, bubbles: true }));
      });
      expectSelection(testCase.selected, testCase.unselected);
    }

    expect(container.querySelector('input[aria-label="Search transfers"]')).not.toBeNull();
    expect(container.querySelector('select[aria-label="Filter transfers by status"]')).not.toBeNull();
  });
});
