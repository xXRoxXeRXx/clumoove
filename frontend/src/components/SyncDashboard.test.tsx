import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { SyncJob } from '../types';
import { apiFetch } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';
import { SyncDashboard } from './SyncDashboard';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../contexts/useToast', () => ({ useToast: () => vi.fn() }));
vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', () => ({ apiFetch: vi.fn() }));

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

describe('SyncDashboard initial snapshot ordering', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(apiFetch).mockReset();
    vi.mocked(connectSseLoop).mockReset();
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('keeps newer stream data when the initial sync detail snapshot resolves late', async () => {
    const snapshot = deferred<Response>();
    const streams = new Map<string, SseHandlers>();
    let snapshotSignal: AbortSignal | undefined;
    vi.mocked(apiFetch).mockImplementation((url, options) => {
      if (String(url).includes('/errors?')) {
        return Promise.resolve(jsonResponse({ errors: [], total: 0 }));
      }
      snapshotSignal = options?.signal;
      return snapshot.promise;
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SyncDashboard syncId="sync-1" apiUrl="https://api.example.test" token="token" onBack={vi.fn()} />);
    });

    await act(async () => {
      streams.get('https://api.example.test/api/sync/stream')?.onEvent('sync_jobs', JSON.stringify([createSyncJob('https://live-detail.example.test')]));
      snapshot.resolve(jsonResponse(createSyncJob('https://stale-detail.example.test')));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-detail.example.test');
    expect(container.textContent).not.toContain('https://stale-detail.example.test');
    expect(snapshotSignal?.aborted).toBe(true);
  });

  it('treats a stream frame without the selected job as a deletion', async () => {
    const snapshot = deferred<Response>();
    const streams = new Map<string, SseHandlers>();
    vi.mocked(apiFetch).mockImplementation((url) => String(url).includes('/errors?')
      ? Promise.resolve(jsonResponse({ errors: [], total: 0 }))
      : snapshot.promise);
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SyncDashboard syncId="sync-1" apiUrl="https://api.example.test" token="token" onBack={vi.fn()} />);
    });

    await act(async () => {
      streams.get('https://api.example.test/api/sync/stream')?.onEvent('sync_jobs', '[]');
      snapshot.resolve(jsonResponse(createSyncJob('https://stale-detail.example.test')));
      await Promise.resolve();
    });

    expect(container.textContent).toContain(i18n.t('sync.notFound'));
    expect(container.textContent).not.toContain('https://stale-detail.example.test');
  });
});
