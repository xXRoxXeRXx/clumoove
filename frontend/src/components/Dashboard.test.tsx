import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { JobAction } from '../types';
import { apiFetch, apiJson } from '../utils/apiClient';
import { connectSseLoop, type SseHandlers } from '../utils/sse';
import { Dashboard } from './Dashboard';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../contexts/useConfirm', () => ({ useConfirm: () => vi.fn() }));
vi.mock('../contexts/useToast', () => ({ useToast: () => vi.fn() }));
vi.mock('../hooks/useOAuthPopup', () => ({ useOAuthPopup: () => ({ openOAuthPopup: vi.fn() }) }));
vi.mock('../utils/sse', () => ({ connectSseLoop: vi.fn(() => new Promise<void>(() => {})) }));
vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return { ...actual, apiFetch: vi.fn(), apiJson: vi.fn(), apiResponseError: vi.fn(() => Promise.resolve(null)) };
});

function migrationPayload(status: string, allowed_actions?: JobAction[]) {
  return {
    id: 'mig-1', status, allowed_actions, error_message: '', active_file: '',
    source_provider: 'nextcloud', source_url: 'https://source.example.test',
    target_provider: 'nextcloud', target_url: 'https://target.example.test',
    total_files: 10, total_bytes: 10, processed_files: 2, processed_bytes: 2, skipped_files: 0, failed_files: 0,
  };
}

describe('Dashboard migration controls', () => {
  let container: HTMLDivElement;
  let root: Root;
  let streams: Map<string, SseHandlers>;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    streams = new Map();
    vi.mocked(connectSseLoop).mockReset();
    vi.mocked(connectSseLoop).mockImplementation((options) => {
      streams.set(options.url, options.handlers);
      return new Promise<void>(() => {});
    });
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockResolvedValue({ ok: true, json: () => Promise.resolve({ errors: [], total: 0 }) } as Response);
    vi.mocked(apiJson).mockReset();
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  async function renderDashboard(payload: ReturnType<typeof migrationPayload>): Promise<void> {
    vi.mocked(apiJson).mockResolvedValue({ ok: true, status: 200, data: payload });
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<Dashboard migrationId="mig-1" apiUrl="https://api.example.test" token="token" onReset={vi.fn()} />);
      await Promise.resolve();
    });
  }

  function control(label: string): HTMLButtonElement | undefined {
    return Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.trim() === label);
  }

  it('enables pause when the backend lists pause', async () => {
    await renderDashboard(migrationPayload('RUNNING', ['pause', 'cancel']));
    expect(control(i18n.t('dashboard.pause'))?.disabled).toBe(false);
    expect(control(i18n.t('dashboard.resume'))).toBeUndefined();
  });

  it('shows resume for a connection-loss pause the backend allows resuming', async () => {
    await renderDashboard(migrationPayload('PAUSED_CONNECTION_LOSS', ['resume', 'cancel']));
    expect(control(i18n.t('dashboard.resume'))?.disabled).toBe(false);
    expect(control(i18n.t('dashboard.pause'))).toBeUndefined();
  });

  it('disables pause when the backend lists no actions, whatever the status says', async () => {
    await renderDashboard(migrationPayload('RUNNING', []));
    expect(control(i18n.t('dashboard.pause'))?.disabled).toBe(true);
  });

  it('follows the actions of the next stream frame', async () => {
    await renderDashboard(migrationPayload('RUNNING', ['pause', 'cancel']));
    await act(async () => {
      streams.get('https://api.example.test/api/migration/mig-1/stream')?.onEvent('migration', JSON.stringify(migrationPayload('PAUSED', ['resume', 'cancel'])));
    });
    expect(control(i18n.t('dashboard.resume'))?.disabled).toBe(false);
    expect(control(i18n.t('dashboard.pause'))).toBeUndefined();
  });
});
