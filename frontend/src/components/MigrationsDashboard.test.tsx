import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { Migration, SyncJob } from '../types';
import { MigrationsDashboard } from './MigrationsDashboard';
import { apiFetch } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';

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

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

function jsonResponse(data: unknown): Response {
  return { ok: true, json: () => Promise.resolve(data) } as Response;
}

function createMigration(sourceUrl: string): Migration {
  return {
    id: 'migration-1', status: 'RUNNING', source_provider: 'nextcloud', source_url: sourceUrl,
    target_provider: 'nextcloud', target_url: 'https://target.example.test', processed_files: 2,
    total_files: 10, processed_bytes: 2, total_bytes: 10, created_at: '2026-01-01T00:00:00Z',
  };
}

function createSyncJob(sourceUrl: string): SyncJob {
  return {
    id: 'sync-1', status: 'RUNNING', direction: 'one_way', interval_minutes: 60,
    delete_propagation: false, conflict_strategy: 'OVERWRITE', source_provider: 'nextcloud',
    source_url: sourceUrl, target_provider: 'nextcloud', target_url: 'https://target.example.test',
    total_files: 10, processed_files: 2, processed_bytes: 2, total_bytes: 10, changed_files: 0,
    deleted_files: 0, failed_files: 0, last_run_at: null, last_run_status: null,
    error_message: null, created_at: '2026-01-01T00:00:00Z',
  };
}

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
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse([]));
    vi.mocked(connectSseLoop).mockReset();
    vi.mocked(connectSseLoop).mockImplementation(() => new Promise<void>(() => {}));
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

  it('keeps newer migration stream data when the initial migration snapshot resolves late', async () => {
    const migrationSnapshot = deferred<Response>();
    const syncSnapshot = deferred<Response>();
    const streams = new Map<string, SseHandlers>();
    let migrationSignal: AbortSignal | undefined;
    vi.mocked(apiFetch).mockImplementation((url, options) => {
      if (String(url).endsWith('/api/migration')) {
        migrationSignal = options?.signal;
        return migrationSnapshot.promise;
      }
      return syncSnapshot.promise;
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<MigrationsDashboard apiUrl="https://api.example.test" token="token" user={null} onStartNewMigration={vi.fn()} onSelectActiveMigration={vi.fn()} />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    await act(async () => {
      streams.get('https://api.example.test/api/migration/stream')?.onEvent('migrations', JSON.stringify([createMigration('https://live-migration.example.test')]));
      migrationSnapshot.resolve(jsonResponse([createMigration('https://stale-migration.example.test')]));
      syncSnapshot.resolve(jsonResponse([]));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-migration.example.test');
    expect(container.textContent).not.toContain('https://stale-migration.example.test');
    expect(migrationSignal?.aborted).toBe(true);
  });

  it('keeps newer sync stream data when the initial sync snapshot resolves late', async () => {
    const migrationSnapshot = deferred<Response>();
    const syncSnapshot = deferred<Response>();
    const streams = new Map<string, SseHandlers>();
    let syncSignal: AbortSignal | undefined;
    vi.mocked(apiFetch).mockImplementation((url, options) => {
      if (String(url).endsWith('/api/migration')) return migrationSnapshot.promise;
      syncSignal = options?.signal;
      return syncSnapshot.promise;
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<MigrationsDashboard apiUrl="https://api.example.test" token="token" user={null} onStartNewMigration={vi.fn()} onSelectActiveMigration={vi.fn()} />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });

    await act(async () => {
      streams.get('https://api.example.test/api/sync/stream')?.onEvent('sync_jobs', JSON.stringify([createSyncJob('https://live-sync.example.test')]));
      migrationSnapshot.resolve(jsonResponse([]));
      syncSnapshot.resolve(jsonResponse([createSyncJob('https://stale-sync.example.test')]));
      await Promise.resolve();
    });
    await act(async () => {
      getTab('sync')?.click();
    });

    expect(container.textContent).toContain('https://live-sync.example.test');
    expect(container.textContent).not.toContain('https://stale-sync.example.test');
    expect(syncSignal?.aborted).toBe(true);
  });
});
