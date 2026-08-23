import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { BackupJob, BackupRun } from '../types';
import { apiJson } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';
import { BackupDashboard } from './BackupDashboard';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const toast = vi.fn();
const confirmMock = vi.fn().mockResolvedValue(true);

vi.mock('../contexts/useToast', () => ({ useToast: () => toast }));
vi.mock('../contexts/useConfirm', () => ({ useConfirm: () => confirmMock }));
vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return {
    ...actual,
    apiFetch: vi.fn(),
    apiJson: vi.fn(),
    apiResponseError: vi.fn(() => Promise.resolve(null)),
  };
});

function jsonResult<T>(data: T): { ok: true; status: number; data: T } {
  return { ok: true as const, status: 200, data };
}

function createBackupJob(status: BackupJob['status'] = 'IDLE'): BackupJob {
  return {
    id: 'backup-123',
    status,
    source_provider: 'nextcloud',
    source_url: 'https://source.example.test',
    selected_paths: ['/photos', '/documents'],
    target_provider: 's3',
    target_url: 'https://s3.example.test',
    target_dir: '/repo',
    total_files: 42,
    processed_files: 42,
    processed_bytes: 104857600, // 100 MB
    total_bytes: 104857600,
    deduplicated_bytes: 52428800, // 50 MB
    failed_files: 0,
    retention_count: 30,
    threads: 4,
    cron_expression: '0 2 * * *',
    timezone: 'Europe/Berlin',
    last_run_at: '2026-08-20T02:00:00Z',
    last_run_status: 'COMPLETED',
    error_code: null,
    created_at: '2026-08-01T00:00:00Z',
  };
}

function createBackupRuns(): BackupRun[] {
  return [
    {
      id: 'run-1',
      backup_job_id: 'backup-123',
      generation: 1,
      trigger: 'schedule',
      scheduled_local_key: '2026-08-20_02:00',
      state: 'COMPLETED',
      total_files: 42,
      processed_files: 42,
      processed_bytes: 104857600,
      total_bytes: 104857600,
      deduplicated_bytes: 52428800,
      failed_files: 0,
      started_at: '2026-08-20T02:00:00Z',
      finished_at: '2026-08-20T02:05:30Z',
      error_code: null,
      created_at: '2026-08-20T02:00:00Z',
    },
  ];
}

describe('BackupDashboard', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('de');
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    vi.clearAllMocks();
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('renders overview with schedule, endpoints, and deduplication statistics', async () => {
    const job = createBackupJob('IDLE');
    const runs = createBackupRuns();

    vi.mocked(apiJson).mockImplementation(async (url: string) => {
      if (url.endsWith('/runs')) {
        return jsonResult(runs);
      }
      return jsonResult(job);
    });

    await act(async () => {
      root.render(
        <BackupDashboard
          backupId="backup-123"
          apiUrl="https://api.example.test"
          token="test-token"
          onBack={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('backup-123');
    expect(container.textContent).toContain('nextcloud');
    expect(container.textContent).toContain('s3');
    expect(container.textContent).toContain('/photos');
    expect(container.textContent).toContain('Europe/Berlin');
    expect(container.textContent).toContain('100 MB');
    expect(container.textContent).toContain('50 MB');
    expect(container.textContent).toContain('30');
  });

  it('switches to runs tab and displays runs table', async () => {
    const job = createBackupJob('IDLE');
    const runs = createBackupRuns();

    vi.mocked(apiJson).mockImplementation(async (url: string) => {
      if (url.endsWith('/runs')) {
        return jsonResult(runs);
      }
      return jsonResult(job);
    });

    await act(async () => {
      root.render(
        <BackupDashboard
          backupId="backup-123"
          apiUrl="https://api.example.test"
          token="test-token"
          onBack={vi.fn()}
        />
      );
    });

    // Find and click the Runs tab
    const tabs = container.querySelectorAll<HTMLButtonElement>('[role="tab"]');
    expect(tabs.length).toBe(3);

    const runsTab = Array.from(tabs).find((t) => t.textContent?.includes('1') || t.id.includes('runs'));
    expect(runsTab).toBeDefined();

    await act(async () => {
      runsTab?.click();
    });

    expect(container.textContent).toContain('#1');
    expect(container.textContent).toContain('42 / 42');
  });

  it('handles run now action triggering POST /api/backup/{id}/run', async () => {
    const job = createBackupJob('IDLE');
    const runs = createBackupRuns();

    vi.mocked(apiJson).mockImplementation(async (url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url.endsWith('/run')) {
        return jsonResult({ ok: true });
      }
      if (url.endsWith('/runs')) {
        return jsonResult(runs);
      }
      return jsonResult(job);
    });

    await act(async () => {
      root.render(
        <BackupDashboard
          backupId="backup-123"
          apiUrl="https://api.example.test"
          token="test-token"
          onBack={vi.fn()}
        />
      );
    });

    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const runButton = buttons.find((b) => b.textContent?.includes('Ausführen') || b.textContent?.includes('Jetzt'));
    expect(runButton).toBeDefined();

    await act(async () => {
      runButton?.click();
    });

    expect(vi.mocked(apiJson)).toHaveBeenCalledWith(
      'https://api.example.test/api/backup/backup-123/run',
      expect.objectContaining({ method: 'POST' })
    );
  });

  it('receives live stream update via SSE', async () => {
    const job = createBackupJob('IDLE');
    let sseHandlers: SseHandlers | undefined;

    vi.mocked(connectSseLoop).mockImplementation(async (options) => {
      sseHandlers = options.handlers;
      return new Promise<void>(() => {});
    });

    vi.mocked(apiJson).mockImplementation(async (url: string) => {
      if (url.endsWith('/runs')) return jsonResult([]);
      return jsonResult(job);
    });

    await act(async () => {
      root.render(
        <BackupDashboard
          backupId="backup-123"
          apiUrl="https://api.example.test"
          token="test-token"
          onBack={vi.fn()}
        />
      );
    });

    // Simulate stream update with RUNNING state
    const runningJob: BackupJob = {
      ...job,
      status: 'RUNNING',
      processed_files: 20,
      total_files: 42,
    };

    await act(async () => {
      sseHandlers?.onEvent?.('backup_jobs', JSON.stringify([runningJob]));
    });

    expect(container.textContent).toContain('Blöcke werden verarbeitet');
  });
});
