import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { apiFetch } from '../utils/apiClient';
import { ConnectForm } from './ConnectForm';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return { ...actual, apiFetch: vi.fn() };
});

function setInputValue(input: HTMLInputElement, value: string) {
  const prototype = Object.getPrototypeOf(input);
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

describe('ConnectForm Koofr', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    vi.mocked(apiFetch).mockReset();
    vi.mocked(apiFetch).mockImplementation((url, init) => {
      if (String(url).endsWith('/api/profiles') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: [] })));
      }
      if (String(url).endsWith('/api/migration/connect/test')) {
        return Promise.resolve(new Response(JSON.stringify({ success: true })));
      }
      if (String(url).endsWith('/api/migration/connect')) {
        return Promise.resolve(new Response(JSON.stringify({ success: true, files: [] })));
      }
      if (String(url).endsWith('/api/profiles') && init?.method === 'POST') {
        return Promise.resolve(new Response('{}'));
      }
      throw new Error(`unexpected request ${url}`);
    });

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<ConnectForm apiUrl="https://api.example.test" token="token" onConnectSuccess={vi.fn()} />);
      await Promise.resolve();
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('connects and saves a Koofr profile without a server URL', async () => {
    const sourceKoofr = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find((button) => button.textContent?.includes('Koofr'))!;
    await act(async () => {
      sourceKoofr.click();
    });

    const link = container.querySelector<HTMLAnchorElement>('a[href="https://app.koofr.net/app/admin/preferences/password"]');
    expect(link?.target).toBe('_blank');
    expect(link?.rel).toBe('noopener noreferrer');
    expect(container.querySelector('#source-koofr-username')).not.toBeNull();
    expect(container.querySelector('#source-provider-url')).toBeNull();

    await act(async () => {
      setInputValue(container.querySelector<HTMLInputElement>('#source-koofr-username')!, 'koofr-user');
      setInputValue(container.querySelector<HTMLInputElement>('#source-koofr-password')!, 'source-app-password');
      Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.includes('Test connection & continue'))!.click();
      await Promise.resolve();
    });

    const targetKoofr = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find((button) => button.textContent?.includes('Koofr'))!;
    await act(async () => {
      targetKoofr.click();
    });
    await act(async () => {
      setInputValue(container.querySelector<HTMLInputElement>('#target-koofr-username')!, 'target-user');
      setInputValue(container.querySelector<HTMLInputElement>('#target-koofr-password')!, 'target-app-password');
      container.querySelector<HTMLInputElement>('#saveProfile-target')!.click();
    });
    await act(async () => {
      setInputValue(container.querySelector<HTMLInputElement>('#profileName-target')!, 'Koofr target');
      container.querySelector<HTMLButtonElement>('button[type="submit"]')!.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
    const connectPayload = JSON.parse(String(connectCall[1]?.body));
    expect(connectPayload).toMatchObject({
      source_url: '', source_username: 'koofr-user', source_password: 'source-app-password',
      target_url: '', target_username: 'target-user', target_password: 'target-app-password',
    });

    const profileCall = vi.mocked(apiFetch).mock.calls.find(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST')!;
    const profilePayload = JSON.parse(String(profileCall[1]?.body));
    expect(profilePayload).toMatchObject({ provider: 'koofr', url: '', username: 'target-user', password: 'target-app-password' });
  });
});
