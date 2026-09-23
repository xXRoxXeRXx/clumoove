import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { SyncJob } from '../types';
import { apiFetch } from '../utils/apiClient';
import { EditSyncModal } from './EditSyncModal';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/apiClient', () => ({ apiFetch: vi.fn() }));

const job: SyncJob = {
  id: 'sync-1',
  status: 'IDLE',
  direction: 'one_way',
  interval_minutes: 15,
  delete_propagation: false,
  conflict_strategy: 'SKIP',
  source_provider: 'nextcloud',
  source_url: 'https://source.example.test',
  target_provider: 'nextcloud',
  target_url: 'https://target.example.test',
  selected_paths: [],
  target_dir: '/',
  total_files: 0,
  processed_files: 0,
  processed_bytes: 0,
  total_bytes: 0,
  changed_files: 0,
  deleted_files: 0,
  failed_files: 0,
  last_run_at: null,
  last_run_status: null,
  error_message: null,
  created_at: '2026-01-01T00:00:00Z',
};

const jsonResponse = (data: unknown): Response =>
  ({ ok: true, json: () => Promise.resolve(data) }) as Response;

describe('EditSyncModal root scope', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockImplementation((url) => {
      if (String(url).includes('/browse')) {
        return Promise.resolve(jsonResponse({ success: true, items: [{ name: 'Projects', path: '/Projects', is_dir: true, size: 0 }] }));
      }
      return Promise.resolve(jsonResponse({ success: true }));
    });
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  it('keeps an existing whole-root scope when only the interval changes', async () => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    await act(async () => {
      root.render(<EditSyncModal job={job} apiUrl="https://api.example.test" token="token" onClose={vi.fn()} onSuccess={vi.fn()} />);
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const rootScope = container.querySelector<HTMLButtonElement>('button[aria-label="Entire source root (/)"]');
    expect(rootScope?.getAttribute('aria-pressed')).toBe('true');

    const interval = container.querySelector<HTMLSelectElement>('select');
    expect(interval).not.toBeNull();
    const valueSetter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set;
    await act(async () => {
      valueSetter?.call(interval, '30');
      interval?.dispatchEvent(new Event('change', { bubbles: true }));
    });

    const save = Array.from(container.querySelectorAll('button')).find((button) => button.textContent?.includes('Save Changes'));
    expect(save).toBeDefined();
    await act(async () => {
      save?.click();
    });

    const scopeCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).includes('/scope'));
    expect(scopeCall).toBeDefined();
    expect(JSON.parse(String(scopeCall?.[1]?.body))).toMatchObject({ selected_paths: [] });

    const scheduleCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).includes('/schedule'));
    expect(scheduleCall).toBeDefined();
    expect(JSON.parse(String(scheduleCall?.[1]?.body))).toEqual({ interval_minutes: 30 });
  });
});
