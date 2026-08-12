import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { SyncJob } from '../types';
import { apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';
import { SyncDashboard } from './SyncDashboard';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const toast = vi.fn();

vi.mock('../contexts/useToast', () => ({ useToast: () => toast }));
vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', () => ({
  ApiDisplayError: class ApiDisplayError extends Error {},
  apiFetch: vi.fn(),
  apiJson: vi.fn(),
  apiErrorMessage: (result: { errorCode?: string; networkError: boolean }, translate: (code?: string) => string, fallback: string) =>
    result.networkError ? fallback : translate(result.errorCode),
  apiResponseError: vi.fn(() => Promise.resolve(null)),
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

function jsonResult<T>(data: T): { ok: true; status: number; data: T } {
  return { ok: true as const, status: 200, data };
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
    toast.mockReset();
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockResolvedValue(jsonResponse({ errors: [], total: 0 }));
    vi.mocked(apiJson).mockReset();
    vi.mocked(connectSseLoop).mockReset();
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('keeps newer stream data when the initial sync detail snapshot resolves late', async () => {
    const snapshot = deferred<ReturnType<typeof jsonResult<SyncJob>>>();
    const streams = new Map<string, SseHandlers>();
    let snapshotSignal: AbortSignal | undefined;
    vi.mocked(apiJson).mockImplementation((_url, options) => {
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
      snapshot.resolve(jsonResult(createSyncJob('https://stale-detail.example.test')));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-detail.example.test');
    expect(container.textContent).not.toContain('https://stale-detail.example.test');
    expect(snapshotSignal?.aborted).toBe(true);
  });

  it('treats a stream frame without the selected job as a deletion', async () => {
    const snapshot = deferred<ReturnType<typeof jsonResult<SyncJob>>>();
    const streams = new Map<string, SseHandlers>();
    vi.mocked(apiJson).mockImplementation(() => snapshot.promise);
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
      snapshot.resolve(jsonResult(createSyncJob('https://stale-detail.example.test')));
      await Promise.resolve();
    });

    expect(container.textContent).toContain(i18n.t('sync.notFound'));
    expect(container.textContent).not.toContain('https://stale-detail.example.test');
  });

  it.each([
    [{ ok: false as const, status: 403, errorCode: 'FORBIDDEN', networkError: false }, 'Access forbidden.'],
    [{ ok: false as const, status: 403, errorCode: 'UNKNOWN', networkError: false }, 'An unexpected error occurred.'],
    [{ ok: false as const, status: 0, networkError: true }, 'Failed to load sync job details.'],
  ])('displays the mapped error or network fallback for a failed detail snapshot', async (result, expectedMessage) => {
    vi.mocked(apiJson).mockResolvedValue(result);

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SyncDashboard syncId="sync-1" apiUrl="https://api.example.test" token="token" onBack={vi.fn()} />);
      await Promise.resolve();
    });

    expect(container.textContent).toContain(expectedMessage);
  });

  it('keeps the localized download fallback for network failures', async () => {
    const job = { ...createSyncJob('https://source.example.test'), failed_files: 1 };
    vi.mocked(apiJson).mockResolvedValue(jsonResult(job));
    vi.mocked(apiFetch).mockImplementation((url) => String(url).includes('/errors?')
      ? Promise.resolve(jsonResponse({
        errors: [{ id: 'error-1', kind: 'transfer', resource_type: 'files', path: '/file', status: 'FAILED', attempts: 0, error_message: 'failed', occurred_at: '2026-01-01T00:00:00Z' }],
        total: 1,
      }))
      : Promise.reject(new TypeError('Failed to fetch')));

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<SyncDashboard syncId="sync-1" apiUrl="https://api.example.test" token="token" onBack={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const downloadButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent === i18n.t('sync.downloadReport'));
    expect(downloadButton).toBeDefined();
    await act(async () => {
      downloadButton?.click();
      await Promise.resolve();
    });

    expect(toast).toHaveBeenCalledWith(i18n.t('dashboard.downloadFailed'), 'error');
  });
});
