import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '../i18n';
import type { BackupJob, Migration, SyncJob } from '../types';
import { apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';
import { useTransferOverview } from './useTransferOverview';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return {
    ...actual,
    apiFetch: vi.fn(() => Promise.resolve(new Response(JSON.stringify([])))),
    apiJson: vi.fn(() => Promise.resolve({ ok: true, status: 200, data: [] })),
  };
});

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void };

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((resolvePromise) => { resolve = resolvePromise; });
  return { promise, resolve };
}

function jsonResponse<T>(data: T): { ok: true; status: number; data: T } {
  return { ok: true, status: 200, data };
}

function migration(sourceUrl: string): Migration {
  return {
    id: sourceUrl, status: 'RUNNING', source_provider: 'nextcloud', source_url: sourceUrl,
    target_provider: 'nextcloud', target_url: 'https://target.example.test', processed_files: 2,
    total_files: 10, processed_bytes: 2, total_bytes: 10, created_at: '2026-01-01T00:00:00Z',
  };
}

function syncJob(sourceUrl: string): SyncJob {
  return {
    id: 'sync-1', status: 'RUNNING', direction: 'one_way', interval_minutes: 60,
    delete_propagation: false, conflict_strategy: 'OVERWRITE', source_provider: 'nextcloud',
    source_url: sourceUrl, target_provider: 'nextcloud', target_url: 'https://target.example.test',
    total_files: 10, processed_files: 2, processed_bytes: 2, total_bytes: 10, changed_files: 0,
    deleted_files: 0, failed_files: 0, last_run_at: null, last_run_status: null,
    error_message: null, created_at: '2026-01-01T00:00:00Z',
  };
}

function backupJob(sourceUrl: string): BackupJob {
  return {
    id: 'backup-1', status: 'IDLE', source_provider: 'nextcloud', source_url: sourceUrl,
    target_provider: 's3', target_url: 'https://target.example.test', selected_paths: ['/data'],
    target_dir: '/backup', cron_expression: '0 2 * * *', timezone: 'UTC', retention_count: 7,
    threads: 2, total_files: 10, total_bytes: 1024, processed_files: 10, processed_bytes: 1024,
    deduplicated_bytes: 128, failed_files: 0, last_run_at: null, last_run_status: null,
    error_code: null, created_at: '2026-01-01T00:00:00Z',
  };
}

function OverviewProbe({ apiUrl = 'https://api.example.test' }: { apiUrl?: string }) {
  const overview = useTransferOverview({ apiUrl, token: 'token' });
  return <output>{JSON.stringify({ migrations: overview.migrations, syncJobs: overview.syncJobs, backupJobs: overview.backupJobs })}</output>;
}

describe('useTransferOverview', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify([])));
    vi.mocked(apiJson).mockReset();
    vi.mocked(connectSseLoop).mockReset();
    vi.mocked(connectSseLoop).mockImplementation(() => new Promise<void>(() => {}));
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('suppresses a stale migration snapshot after a newer stream frame', async () => {
    const snapshot = deferred<ReturnType<typeof jsonResponse<Migration[]>>>();
    const streams = new Map<string, SseHandlers>();
    let signal: AbortSignal | undefined;
    vi.mocked(apiJson).mockImplementation((url, options) => {
      if (String(url).endsWith('/api/migration')) {
        signal = options?.signal;
        return snapshot.promise;
      }
      return Promise.resolve(jsonResponse([]));
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    await act(async () => {
      root.render(<OverviewProbe />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });
    await act(async () => {
      streams.get('https://api.example.test/api/migration/stream')?.onEvent('migrations', JSON.stringify([migration('https://live.example.test')]));
      snapshot.resolve(jsonResponse([migration('https://stale.example.test')]));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live.example.test');
    expect(container.textContent).not.toContain('https://stale.example.test');
    expect(signal?.aborted).toBe(true);
  });

  it('suppresses a stale sync snapshot after a newer stream frame', async () => {
    const snapshot = deferred<ReturnType<typeof jsonResponse<SyncJob[]>>>();
    const streams = new Map<string, SseHandlers>();
    let signal: AbortSignal | undefined;
    vi.mocked(apiJson).mockImplementation((url, options) => {
      if (String(url).endsWith('/api/sync')) {
        signal = options?.signal;
        return snapshot.promise;
      }
      return Promise.resolve(jsonResponse([]));
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    await act(async () => {
      root.render(<OverviewProbe />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });
    await act(async () => {
      streams.get('https://api.example.test/api/sync/stream')?.onEvent('sync_jobs', JSON.stringify([syncJob('https://live-sync.example.test')]));
      snapshot.resolve(jsonResponse([syncJob('https://stale-sync.example.test')]));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-sync.example.test');
    expect(container.textContent).not.toContain('https://stale-sync.example.test');
    expect(signal?.aborted).toBe(true);
  });

  it('suppresses a stale backup snapshot after a newer stream frame', async () => {
    const snapshot = deferred<ReturnType<typeof jsonResponse<BackupJob[]>>>();
    const streams = new Map<string, SseHandlers>();
    let signal: AbortSignal | undefined;
    vi.mocked(apiJson).mockImplementation((url, options) => {
      if (String(url).endsWith('/api/backup')) {
        signal = options?.signal;
        return snapshot.promise;
      }
      return Promise.resolve(jsonResponse([]));
    });
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    await act(async () => {
      root.render(<OverviewProbe />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });
    await act(async () => {
      streams.get('https://api.example.test/api/backup/stream')?.onEvent('backup_jobs', JSON.stringify([backupJob('https://live-backup.example.test')]));
      snapshot.resolve(jsonResponse([backupJob('https://stale-backup.example.test')]));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-backup.example.test');
    expect(container.textContent).not.toContain('https://stale-backup.example.test');
    expect(signal?.aborted).toBe(true);
  });

  it('exposes current sync and backup stream updates to its caller', async () => {
    const streams = new Map<string, SseHandlers>();
    vi.mocked(apiJson).mockResolvedValue(jsonResponse([]));
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    await act(async () => {
      root.render(<OverviewProbe />);
      await new Promise((resolve) => window.setTimeout(resolve, 0));
    });
    await act(async () => {
      streams.get('https://api.example.test/api/sync/stream')?.onEvent('sync_jobs', JSON.stringify([syncJob('https://live-sync.example.test')]));
      streams.get('https://api.example.test/api/backup/stream')?.onEvent('backup_jobs', JSON.stringify([backupJob('https://live-backup.example.test')]));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('https://live-sync.example.test');
    expect(container.textContent).toContain('https://live-backup.example.test');
  });

  it('ignores a stream frame delivered after its generation is cleaned up', async () => {
    const streams = new Map<string, SseHandlers>();
    vi.mocked(apiJson).mockResolvedValue(jsonResponse([]));
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });

    await act(async () => {
      root.render(<OverviewProbe apiUrl="https://first.example.test" />);
    });
    await act(async () => {
      root.render(<OverviewProbe apiUrl="https://second.example.test" />);
    });
    await act(async () => {
      streams.get('https://first.example.test/api/migration/stream')?.onEvent('migrations', JSON.stringify([migration('https://stale-stream.example.test')]));
      await Promise.resolve();
    });

    expect(container.textContent).not.toContain('https://stale-stream.example.test');
  });
});
