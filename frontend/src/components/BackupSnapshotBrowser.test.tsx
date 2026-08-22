import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { apiFetch, apiJson } from '../utils/apiClient';
import { BackupSnapshotBrowser } from './BackupSnapshotBrowser';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return {
    ...actual,
    apiFetch: vi.fn(),
    apiJson: vi.fn(),
  };
});

function jsonResult<T>(data: T): { ok: true; status: number; data: T } {
  return { ok: true as const, status: 200, data };
}

describe('BackupSnapshotBrowser', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    vi.clearAllMocks();
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  const mockSnapshots = [
    {
      id: 'snapshot-1',
      state: 'READY',
      created_at: '2026-08-20T10:00:00Z',
      total_files: 5,
      total_dirs: 2,
      total_bytes: 1048576,
      omitted_unstable_count: 0,
      omitted_error_count: 0,
      integrity_state: 'VERIFIED',
    },
  ];

  const mockSnapshotItems = [
    {
      relative_path: 'Documents',
      name: 'Documents',
      is_dir: true,
      size_bytes: 0,
      mtime: null,
      state: 'AVAILABLE',
      error_code: null,
    },
    {
      relative_path: 'file.txt',
      name: 'file.txt',
      is_dir: false,
      size_bytes: 1024,
      mtime: '2026-08-20T09:00:00Z',
      state: 'AVAILABLE',
      error_code: null,
    },
  ];

  it('renders snapshots and navigates into snapshot contents', async () => {
    vi.mocked(apiJson).mockImplementation(async (url: string) => {
      if (url.endsWith('/snapshots')) {
        return jsonResult(mockSnapshots);
      }
      if (url.endsWith('/verify')) {
        return jsonResult([]);
      }
      if (url.endsWith('/restore')) {
        return jsonResult([]);
      }
      if (url.includes('/items')) {
        return jsonResult(mockSnapshotItems);
      }
      return jsonResult(null);
    });

    await act(async () => {
      root.render(
        <BackupSnapshotBrowser
          apiUrl="http://localhost:8080"
          token="test-token"
          jobID="job-1"
          onBack={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('Browse snapshots');
    expect(container.textContent).toContain('READY');

    const snapshotButton = container.querySelector('button.ui-button-secondary span strong') as HTMLElement;
    expect(snapshotButton).not.toBeNull();

    await act(async () => {
      snapshotButton.closest('button')?.click();
    });

    expect(container.textContent).toContain('Snapshot contents');
    expect(container.textContent).toContain('Documents');
    expect(container.textContent).toContain('file.txt');
  });

  it('selects paths, opens restore dialog, generates preview and consumes preview', async () => {
    vi.mocked(apiJson).mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.endsWith('/snapshots')) {
        return jsonResult(mockSnapshots);
      }
      if (url.endsWith('/verify')) {
        return jsonResult([]);
      }
      if (url.endsWith('/restore')) {
        return jsonResult([]);
      }
      if (url.includes('/items')) {
        return jsonResult(mockSnapshotItems);
      }
      if (url.endsWith('/profiles')) {
        return jsonResult({
          profiles: [
            { id: 'prof-1', name: 'My Target', provider: 'nextcloud' },
          ],
        });
      }
      if (url.endsWith('/consume') && options?.method === 'POST') {
        return jsonResult({ restore_run_id: 'run-1' });
      }
      if (url.includes('/restore/previews') && options?.method === 'POST') {
        return jsonResult({ id: 'prev-1' });
      }
      if (url.endsWith('/restore/previews/prev-1')) {
        return jsonResult({
          id: 'prev-1',
          status: 'READY',
          total_files: 1,
          total_directories: 0,
          total_bytes: 1024,
          existing_file_conflicts: 0,
          mergeable_directories: 0,
          type_conflicts: 0,
          unavailable_items: 0,
          expected_skips: 0,
          expected_renames: 0,
          metadata_warnings: 0,
          conflict_examples: [],
        });
      }
      return jsonResult(null);
    });

    await act(async () => {
      root.render(
        <BackupSnapshotBrowser
          apiUrl="http://localhost:8080"
          token="test-token"
          jobID="job-1"
          onBack={vi.fn()}
        />
      );
    });

    // Enter snapshot
    const snapshotBtn = container.querySelector('button.ui-button-secondary span strong')?.closest('button') as HTMLButtonElement;
    await act(async () => {
      snapshotBtn.click();
    });

    // Select file.txt
    const checkbox = container.querySelector('input[type="checkbox"][aria-label="file.txt"]') as HTMLInputElement;
    expect(checkbox).not.toBeNull();
    await act(async () => {
      checkbox.click();
    });

    // Open restore modal
    const restoreBtn = container.querySelector('button.ui-button-primary') as HTMLButtonElement;
    expect(restoreBtn.textContent).toContain('Restore selected (1)');
    await act(async () => {
      restoreBtn.click();
    });

    expect(container.textContent).toContain('Restore snapshot');

    // Select profile
    const profileSelect = container.querySelector('select') as HTMLSelectElement;
    await act(async () => {
      profileSelect.value = 'prof-1';
      profileSelect.dispatchEvent(new Event('change', { bubbles: true }));
    });

    // Click Preview
    const previewBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Create preview'));
    expect(previewBtn).not.toBeUndefined();
    await act(async () => {
      previewBtn?.click();
    });

    expect(container.textContent).toContain('Preview is ready');

    // Click Start Restore
    const startBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Start restore'));
    expect(startBtn).not.toBeUndefined();
    await act(async () => {
      startBtn?.click();
    });

    expect(container.textContent).toContain('Restore progress');
  });

  it('triggers a repository check with selected mode', async () => {
    let checkStarted = false;
    vi.mocked(apiJson).mockImplementation(async (url: string, options?: RequestInit) => {
      if (url.endsWith('/snapshots')) {
        return jsonResult(mockSnapshots);
      }
      if (url.endsWith('/verify') && options?.method === 'POST') {
        checkStarted = true;
        return jsonResult({ id: 'verify-1' });
      }
      if (url.endsWith('/verify')) {
        if (checkStarted) {
          return jsonResult([
            {
              id: 'verify-1',
              state: 'RUNNING',
              verify_mode: 'METADATA',
              byte_budget: null,
              processed_bytes: 1048576,
              total_packs: 10,
              checked_packs: 5,
              missing_packs: 0,
              damaged_packs: 0,
            },
          ]);
        }
        return jsonResult([]);
      }
      if (url.endsWith('/restore')) {
        return jsonResult([]);
      }
      return jsonResult(null);
    });

    await act(async () => {
      root.render(
        <BackupSnapshotBrowser
          apiUrl="http://localhost:8080"
          token="test-token"
          jobID="job-1"
          onBack={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('Repository check');

    const startCheckBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Start check'));
    expect(startCheckBtn).not.toBeUndefined();

    await act(async () => {
      startCheckBtn?.click();
    });

    expect(checkStarted).toBe(true);
    expect(container.textContent).toContain('METADATA');
    expect(container.textContent).toContain('RUNNING');
  });

  it('downloads restore report when available in history', async () => {
    vi.mocked(apiJson).mockImplementation(async (url: string) => {
      if (url.endsWith('/snapshots')) {
        return jsonResult(mockSnapshots);
      }
      if (url.endsWith('/verify')) {
        return jsonResult([]);
      }
      if (url.endsWith('/restore')) {
        return jsonResult([
          {
            id: 'run-done',
            status: 'COMPLETED',
            total_files: 5,
            processed_files: 5,
            total_bytes: 1048576,
            processed_bytes: 1048576,
            failed_files: 0,
          },
        ]);
      }
      return jsonResult(null);
    });

    vi.mocked(apiFetch).mockResolvedValueOnce({
      ok: true,
      blob: () => Promise.resolve(new Blob(['source,target,status\nfile.txt,/file.txt,COMPLETED'], { type: 'text/csv' })),
    } as Response);

    await act(async () => {
      root.render(
        <BackupSnapshotBrowser
          apiUrl="http://localhost:8080"
          token="test-token"
          jobID="job-1"
          onBack={vi.fn()}
        />
      );
    });

    expect(container.textContent).toContain('Restore history');
    expect(container.textContent).toContain('COMPLETED');

    const reportBtn = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes('Download report'));
    expect(reportBtn).not.toBeUndefined();

    await act(async () => {
      reportBtn?.click();
    });

    expect(apiFetch).toHaveBeenCalledWith('http://localhost:8080/api/restore/run-done/report', expect.anything());
  });
});
