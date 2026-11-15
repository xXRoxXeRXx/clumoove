import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { CloudFile, MigrationConfig } from '../types';
import { apiFetch } from '../utils/apiClient';
import { ConnectForm } from './ConnectForm';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../utils/apiClient')>('../utils/apiClient');
  return { ...actual, apiFetch: vi.fn() };
});

const VALID_SFTP_KEY = 'SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU';

function setInputValue(input: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const prototype = Object.getPrototypeOf(input);
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

function setSelectValue(select: HTMLSelectElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set?.call(select, value);
  select.dispatchEvent(new Event('change', { bubbles: true }));
}

async function flushAsync() {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

function findButton(container: ParentNode, text: string): HTMLButtonElement | undefined {
  return Array.from(container.querySelectorAll<HTMLButtonElement>('button')).find(
    (button) => button.textContent?.includes(text)
  );
}

function selectProviderRadio(container: ParentNode, label: string) {
  const radio = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find(
    (button) => button.textContent?.trim() === label
  );
  expect(radio).toBeDefined();
  radio!.click();
}

function dispatchOAuthSuccess(
  popup: Window,
  provider: string,
  overrides: Partial<{
    origin: string;
    token: string;
    refreshToken: string;
    expiresIn: number;
    username: string;
  }> = {},
) {
  window.dispatchEvent(new MessageEvent('message', {
    origin: overrides.origin ?? 'https://api.example.test',
    source: popup,
    data: {
      type: 'oauth-success',
      provider,
      purpose: 'connect',
      token: overrides.token ?? `${provider}-access-token`,
      refreshToken: overrides.refreshToken ?? `${provider}-refresh-token`,
      expiresIn: overrides.expiresIn ?? 3600,
      username: overrides.username ?? `user@${provider}.test`,
    },
  }));
}

function getBearerHeader(init?: RequestInit): string | null {
  return new Headers(init?.headers).get('Authorization');
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

describe('ConnectForm workflows', () => {
  let container: HTMLDivElement;
  let root: Root;
  let savedProfiles: { id: string; name: string; provider: string }[];
  let onConnectSuccess: ReturnType<typeof vi.fn<(config: MigrationConfig, initialFiles: CloudFile[]) => void>>;
  let onBack: ReturnType<typeof vi.fn<() => void>>;

  function installDefaultApiMock(
    customHandler?: (url: string, init?: RequestInit) => Promise<Response> | null,
  ) {
    vi.mocked(apiFetch).mockImplementation((url, init) => {
      const targetUrl = String(url);
      const custom = customHandler?.(targetUrl, init);
      if (custom) return custom;

      if (targetUrl.endsWith('/api/profiles') && (!init?.method || init.method === 'GET')) {
        return Promise.resolve(new Response(JSON.stringify({ profiles: savedProfiles })));
      }
      if (targetUrl.endsWith('/api/migration/connect/test')) {
        return Promise.resolve(new Response(JSON.stringify({ success: true })));
      }
      if (targetUrl.endsWith('/api/migration/connect')) {
        const files: CloudFile[] = [
          { path: '/docs', name: 'docs', size: 0, is_dir: true, hash: '', last_modified: '2026-01-01T00:00:00Z' },
        ];
        return Promise.resolve(new Response(JSON.stringify({ success: true, files })));
      }
      if (targetUrl.endsWith('/api/profiles') && init?.method === 'POST') {
        return Promise.resolve(new Response(JSON.stringify({ id: 'new-prof' }), { status: 201 }));
      }
      throw new Error(`unexpected request ${targetUrl}`);
    });
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    localStorage.clear();
    sessionStorage.clear();
    savedProfiles = [];
    onConnectSuccess = vi.fn<(config: MigrationConfig, initialFiles: CloudFile[]) => void>();
    onBack = vi.fn<() => void>();

    vi.mocked(apiFetch).mockReset();
    installDefaultApiMock();

    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  async function renderForm(props: Partial<React.ComponentProps<typeof ConnectForm>> = {}) {
    await act(async () => {
      root.render(
        <ConnectForm
          apiUrl="https://api.example.test"
          token="jwt-token"
          onConnectSuccess={onConnectSuccess}
          onBack={onBack}
          {...props}
        />
      );
      await Promise.resolve();
    });
    await flushAsync();
  }

  async function clickVerifyAndAdvance() {
    const button = findButton(container, 'Test connection & continue');
    expect(button).toBeDefined();
    await act(async () => {
      button!.click();
      await Promise.resolve();
    });
    await flushAsync();
  }

  async function submitConnectForm() {
    const form = container.querySelector('form');
    expect(form).not.toBeNull();
    await act(async () => {
      form!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    await flushAsync();
  }

  describe('wizard navigation and provider availability', () => {
    it('gates Immich, Local, and OAuth providers on configuration flags', async () => {
      await renderForm({ localStorageEnabled: false, oauthProviders: {} });
      const defaultLabels = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).map((el) => el.textContent?.trim());
      expect(defaultLabels).not.toContain('Immich');
      expect(defaultLabels).not.toContain('Local');
      expect(defaultLabels).not.toContain('Google');
      expect(defaultLabels).not.toContain('Dropbox');

      await renderForm({ localStorageEnabled: true, oauthProviders: { google: true, onedrive: true } });
      const enabledLabels = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).map((el) => el.textContent?.trim());
      expect(enabledLabels).toContain('Immich');
      expect(enabledLabels).toContain('Local');
      expect(enabledLabels).toContain('Google');
      expect(enabledLabels).toContain('OneDrive');
      expect(enabledLabels).not.toContain('Dropbox');
    });

    it('navigates between step 1, step 2, and onBack, and toggles help guide', async () => {
      await renderForm();

      expect(container.querySelector('h1')?.textContent).toBe('Step 1 of 3: Set source');
      await act(async () => { findButton(container, 'Back')!.click(); });
      expect(onBack).toHaveBeenCalledTimes(1);

      await act(async () => { findButton(container, 'Help guide')!.click(); });
      expect(container.textContent).toContain('Instructions for creating an application password');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://source.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'src-pass');
      });
      await clickVerifyAndAdvance();

      expect(container.querySelector('h1')?.textContent).toBe('Step 2 of 3: Set target');

      // Back from step 2 returns to step 1 without invoking onBack
      await act(async () => { findButton(container, 'Back')!.click(); });
      expect(container.querySelector('h1')?.textContent).toBe('Step 1 of 3: Set source');
      expect(onBack).toHaveBeenCalledTimes(1);
    });
  });

  describe('provider validation, testing, and connection submission', () => {
    it('validates missing Nextcloud fields on step 1 and connects step 2 to onConnectSuccess', async () => {
      await renderForm();

      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Please fill in all fields');
      expect(vi.mocked(apiFetch).mock.calls.some(([url]) => String(url).endsWith('/api/migration/connect/test'))).toBe(false);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://src.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'src-user');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'src-pass');
      });
      await clickVerifyAndAdvance();

      // Target missing fields must block submission
      await submitConnectForm();
      expect(vi.mocked(apiFetch).mock.calls.some(([url]) => String(url).endsWith('/api/migration/connect'))).toBe(false);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-url')!, 'https://dst.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-username')!, 'dst-user');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-password')!, 'dst-pass');
      });
      await submitConnectForm();

      const testCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect/test'))!;
      expect(getBearerHeader(testCall[1])).toBe('Bearer jwt-token');
      expect(JSON.parse(String(testCall[1]?.body))).toEqual({
        source_url: 'https://src.example.com',
        source_username: 'src-user',
        source_password: 'src-pass',
        source_refresh_token: '',
        source_token_expires_in: 0,
        source_provider: 'nextcloud',
        source_profile_id: '',
        role: 'source',
      });

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(getBearerHeader(connectCall[1])).toBe('Bearer jwt-token');
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: 'https://src.example.com',
        source_username: 'src-user',
        source_password: 'src-pass',
        target_url: 'https://dst.example.com',
        target_username: 'dst-user',
        target_password: 'dst-pass',
        source_provider: 'nextcloud',
        target_provider: 'nextcloud',
      });
      expect(onConnectSuccess).toHaveBeenCalledTimes(1);
      expect(onConnectSuccess.mock.calls[0][1]).toHaveLength(1);
    });

    it('connects OpenCloud, Seafile, WebDAV, and MagentaCLOUD across source and target', async () => {
      await renderForm();

      await act(async () => { selectProviderRadio(container, 'OpenCloud'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://oc.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'oc-user');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'oc-pass');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'Seafile'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-url')!, 'https://sf.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-username')!, 'sf-user');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-password')!, 'sf-pass');
      });
      await submitConnectForm();

      let connectCalls = vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith('/api/migration/connect'));
      expect(JSON.parse(String(connectCalls[0][1]?.body))).toMatchObject({
        source_provider: 'opencloud',
        source_url: 'https://oc.example.com',
        target_provider: 'seafile',
        target_url: 'https://sf.example.com',
      });

      await act(async () => { findButton(container, 'Back')!.click(); });
      await act(async () => { selectProviderRadio(container, 'WebDAV'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://dav.example.com/dav');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'dav-user');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'dav-pass');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'MagentaCLOUD'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-magentacloud-username')!, 'mc@t-online.de');
        setInputValue(container.querySelector<HTMLInputElement>('#target-magentacloud-password')!, 'mc-pass');
      });
      await submitConnectForm();

      connectCalls = vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith('/api/migration/connect'));
      expect(JSON.parse(String(connectCalls[1][1]?.body))).toMatchObject({
        source_provider: 'webdav',
        source_url: 'https://dav.example.com/dav',
        target_provider: 'magentacloud',
        target_url: '',
        target_username: 'mc@t-online.de',
        target_password: 'mc-pass',
      });
    });

    it('validates and builds SMB URLs on source and target steps', async () => {
      await renderForm();
      await act(async () => { selectProviderRadio(container, 'SMB/CIFS'); });

      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('server address and the name of the share for the source');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-smb-host')!, '10.0.0.10');
        setInputValue(container.querySelector<HTMLInputElement>('#source-smb-share')!, '/media');
        setInputValue(container.querySelector<HTMLInputElement>('#source-smb-domain')!, 'HOME');
        setInputValue(container.querySelector<HTMLInputElement>('#source-smb-username')!, 'smb-u');
        setInputValue(container.querySelector<HTMLInputElement>('#source-smb-password')!, 'smb-p');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'SMB/CIFS'); });
      await submitConnectForm();
      expect(vi.mocked(apiFetch).mock.calls.some(([url]) => String(url).endsWith('/api/migration/connect'))).toBe(false);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-smb-host')!, '10.0.0.20');
        setInputValue(container.querySelector<HTMLInputElement>('#target-smb-port')!, '1445');
        setInputValue(container.querySelector<HTMLInputElement>('#target-smb-share')!, 'backups');
        setInputValue(container.querySelector<HTMLInputElement>('#target-smb-username')!, 'smb-u2');
        setInputValue(container.querySelector<HTMLInputElement>('#target-smb-password')!, 'smb-p2');
      });
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: 'smb://10.0.0.10:445/media?domain=HOME',
        target_url: 'smb://10.0.0.20:1445/backups',
        source_provider: 'smb',
        target_provider: 'smb',
      });
    });

    it('validates and builds S3 URLs with custom endpoints', async () => {
      await renderForm();
      await act(async () => { selectProviderRadio(container, 'S3'); });

      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('storage folder and the region for the source');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-s3-bucket')!, 'src-bucket');
        setInputValue(container.querySelector<HTMLInputElement>('#source-s3-region')!, 'eu-west-1');
        setInputValue(container.querySelector<HTMLInputElement>('#source-s3-endpoint')!, 'https://s3.wasabisys.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-s3-access-key')!, 'AKIA1');
        setInputValue(container.querySelector<HTMLInputElement>('#source-s3-secret-key')!, 'SECRET1');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'S3'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-s3-bucket')!, 'dst-bucket');
        setInputValue(container.querySelector<HTMLInputElement>('#target-s3-access-key')!, 'AKIA2');
        setInputValue(container.querySelector<HTMLInputElement>('#target-s3-secret-key')!, 'SECRET2');
      });
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: 's3://src-bucket?region=eu-west-1&endpoint=https%3A%2F%2Fs3.wasabisys.com',
        target_url: 's3://dst-bucket?region=us-east-1',
        source_provider: 's3',
        target_provider: 's3',
      });
    });

    it('enforces SFTP host key fingerprint and supports password and SSH key auth', async () => {
      await renderForm();
      await act(async () => { selectProviderRadio(container, 'SFTP'); });

      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('server address for the source');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-sftp-host')!, 'sftp.src.test');
        setInputValue(container.querySelector<HTMLInputElement>('#source-sftp-host-key')!, 'SHA256:bad');
      });
      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('host-key fingerprint');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-sftp-host-key')!, VALID_SFTP_KEY);
        setInputValue(container.querySelector<HTMLInputElement>('#source-sftp-username')!, 'sftp-u');
        findButton(container, 'Security key')!.click();
      });
      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('security key for the source');

      await act(async () => {
        setInputValue(container.querySelector<HTMLTextAreaElement>('#source-sftp-private-key')!, 'PRIVATE-KEY-PEM');
        container.querySelector<HTMLInputElement>('#saveProfile-source')!.click();
      });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profileName-source')!, 'Source SFTP Key');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'SFTP'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-sftp-host')!, 'sftp.dst.test');
        setInputValue(container.querySelector<HTMLInputElement>('#target-sftp-port')!, '2222');
        setInputValue(container.querySelector<HTMLInputElement>('#target-sftp-host-key')!, VALID_SFTP_KEY);
        setInputValue(container.querySelector<HTMLInputElement>('#target-sftp-username')!, 'sftp-u2');
        setInputValue(container.querySelector<HTMLInputElement>('#target-sftp-password')!, 'sftp-pw2');
      });
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: `sftp://sftp.src.test:22?host_key=${encodeURIComponent(VALID_SFTP_KEY)}`,
        source_password: 'PRIVATE-KEY-PEM',
        target_url: `sftp://sftp.dst.test:2222?host_key=${encodeURIComponent(VALID_SFTP_KEY)}`,
        target_password: 'sftp-pw2',
      });

      // Characterize current saveProfile behavior for SFTP key mode (reads sourcePass, omitting private key)
      const profilePost = vi.mocked(apiFetch).mock.calls.find(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST')!;
      expect(JSON.parse(String(profilePost[1]?.body))).toEqual({
        name: 'Source SFTP Key',
        provider: 'sftp',
        url: `sftp://sftp.src.test:22?host_key=${encodeURIComponent(VALID_SFTP_KEY)}`,
        username: 'sftp-u',
      });
    });

    it('validates FTPS host/port and builds explicit and implicit FTPS URLs', async () => {
      await renderForm();
      await act(async () => { selectProviderRadio(container, 'FTPS'); });

      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('FTPS server address for the source');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-ftp-host')!, 'ftp.src.test');
        setInputValue(container.querySelector<HTMLInputElement>('#source-ftp-port')!, '99999');
      });
      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('valid FTPS port');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-ftp-port')!, '21');
        setInputValue(container.querySelector<HTMLInputElement>('#source-ftp-username')!, 'ftp-u1');
        setInputValue(container.querySelector<HTMLInputElement>('#source-ftp-password')!, 'ftp-p1');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'FTPS'); });
      const targetTlsSelect = container.querySelector<HTMLSelectElement>('#target-ftp-tls-mode')!;
      await act(async () => {
        setSelectValue(targetTlsSelect, 'implicit');
      });
      expect(container.querySelector<HTMLInputElement>('#target-ftp-port')?.value).toBe('990');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-ftp-host')!, 'ftp.dst.test');
        setInputValue(container.querySelector<HTMLInputElement>('#target-ftp-username')!, 'ftp-u2');
        setInputValue(container.querySelector<HTMLInputElement>('#target-ftp-password')!, 'ftp-p2');
      });
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: 'ftp://ftp.src.test:21?tls=explicit',
        target_url: 'ftps://ftp.dst.test:990',
      });
    });

    it('supports Immich (API key without username) and Local (no credentials, no save-profile option)', async () => {
      await renderForm({ localStorageEnabled: true });
      await act(async () => { selectProviderRadio(container, 'Immich'); });

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-immich-url')!, 'https://immich.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-immich-api-key')!, 'immich-secret');
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'Local'); });
      expect(container.querySelector('#saveProfile-target')).toBeNull();
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_provider: 'immich',
        source_url: 'https://immich.example.com',
        source_username: '',
        source_password: 'immich-secret',
        target_provider: 'local',
        target_url: '',
        target_username: '',
        target_password: '',
      });
    });

    it('handles OAuth popup completion, rejects untrusted origins, supports disconnect, and surfaces blocked popups', async () => {
      const popup = { closed: false, close: vi.fn() } as unknown as Window;
      const openSpy = vi.spyOn(window, 'open').mockReturnValueOnce(null).mockReturnValue(popup);

      await renderForm({ oauthProviders: { google: true, onedrive: true, dropbox: true, hidrive: true } });
      await act(async () => { selectProviderRadio(container, 'Google'); });

      // Popup blocked surfaces localized error
      await act(async () => { findButton(container, 'Connect with Google')!.click(); });
      expect(container.querySelector('[role="alert"]')?.textContent).toBe(
        'Your browser blocked the authorization popup. Allow popups for this site and try again.'
      );

      // Start OAuth popup and verify untrusted origin is ignored
      await act(async () => { findButton(container, 'Connect with Google')!.click(); });
      expect(openSpy).toHaveBeenCalledTimes(2);
      await act(async () => {
        dispatchOAuthSuccess(popup, 'google', {
          origin: 'https://evil.example.test',
          token: 'evil-token',
          username: 'evil@attacker.test',
        });
      });
      expect(container.textContent).not.toContain('evil@attacker.test');

      // Legitimate OAuth callback
      await act(async () => {
        dispatchOAuthSuccess(popup, 'google', {
          token: 'src-oauth-access',
          refreshToken: 'src-oauth-refresh',
          username: 'alice@gmail.com',
        });
      });
      expect(container.textContent).toContain('alice@gmail.com');

      // Disconnecting clears token and blocks advancing
      await act(async () => { findButton(container, 'Disconnect')!.click(); });
      await clickVerifyAndAdvance();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Please fill in all fields');

      // Reconnect Google source and connect OneDrive target
      await act(async () => { findButton(container, 'Connect with Google')!.click(); });
      await act(async () => {
        dispatchOAuthSuccess(popup, 'google', {
          token: 'src-oauth-access',
          refreshToken: 'src-oauth-refresh',
          username: 'alice@gmail.com',
        });
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'OneDrive'); });
      await act(async () => { findButton(container, 'Connect with OneDrive')!.click(); });
      await act(async () => {
        dispatchOAuthSuccess(popup, 'onedrive', {
          token: 'dst-oauth-access',
          refreshToken: 'dst-oauth-refresh',
          expiresIn: 7200,
          username: 'alice@outlook.com',
        });
      });

      await act(async () => {
        container.querySelector<HTMLInputElement>('#saveProfile-target')!.click();
      });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profileName-target')!, 'My OneDrive');
      });
      await submitConnectForm();

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_url: 'https://api.google.com',
        source_username: 'alice@gmail.com',
        source_password: 'src-oauth-access',
        source_refresh_token: 'src-oauth-refresh',
        target_url: 'oauth://onedrive',
        target_username: 'alice@outlook.com',
        target_password: 'dst-oauth-access',
        target_refresh_token: 'dst-oauth-refresh',
      });

      // OAuth profile save sends refresh_token + oauth_user and omits access token password
      const profilePost = vi.mocked(apiFetch).mock.calls.find(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST')!;
      expect(getBearerHeader(profilePost[1])).toBe('Bearer jwt-token');
      const savedBody = JSON.parse(String(profilePost[1]?.body));
      expect(savedBody).toEqual({
        name: 'My OneDrive',
        provider: 'onedrive',
        url: 'oauth://onedrive',
        username: 'alice@outlook.com',
        refresh_token: 'dst-oauth-refresh',
        oauth_user: 'alice@outlook.com',
      });
      expect('password' in savedBody).toBe(false);
      expect(localStorage.length).toBe(0);
      expect(sessionStorage.length).toBe(0);

      // Dropbox -> HiDrive OAuth pairing
      await act(async () => { findButton(container, 'Back')!.click(); });
      await act(async () => { selectProviderRadio(container, 'Dropbox'); });
      await act(async () => { findButton(container, 'Connect with Dropbox')!.click(); });
      await act(async () => {
        dispatchOAuthSuccess(popup, 'dropbox', { username: 'alice@dropbox.test' });
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'HiDrive'); });
      await act(async () => { findButton(container, 'Connect with HiDrive')!.click(); });
      await act(async () => {
        dispatchOAuthSuccess(popup, 'hidrive', { username: 'alice@hidrive.test' });
      });
      await submitConnectForm();

      const oauthConnectCalls = vi.mocked(apiFetch).mock.calls.filter(([url]) => String(url).endsWith('/api/migration/connect'));
      expect(JSON.parse(String(oauthConnectCalls[1][1]?.body))).toMatchObject({
        source_provider: 'dropbox',
        source_url: 'https://api.dropbox.com',
        target_provider: 'hidrive',
        target_url: 'https://api.hidrive.com',
      });
    });
  });

  describe('saved profiles, credential isolation, and save-as-profile', () => {
    it('does not leak ad-hoc credentials when a saved profile is selected and clears stale fields on provider switch', async () => {
      savedProfiles = [
        { id: 'prof-src', name: 'Saved Nextcloud', provider: 'nextcloud' },
        { id: 'prof-dst', name: 'Saved S3', provider: 's3' },
      ];

      await renderForm();

      // Type ad-hoc credentials first, then switch provider -> credentials must reset
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://leaked.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'leaked-user');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'leaked-secret');
        selectProviderRadio(container, 'WebDAV');
      });
      expect(container.querySelector<HTMLInputElement>('#source-provider-url')?.value).toBe('');
      expect(container.querySelector<HTMLInputElement>('#source-provider-username')?.value).toBe('');
      expect(container.querySelector<HTMLInputElement>('#source-provider-password')?.value).toBe('');

      // Type credentials, then select a saved profile -> credential inputs vanish and payload sends profile_id with empty credentials
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://typed.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'typed-user');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'typed-secret');
        setSelectValue(container.querySelector<HTMLSelectElement>('#connection-profile-source')!, 'prof-src');
      });
      expect(container.querySelector('#source-provider-password')).toBeNull();
      expect(container.querySelector('#saveProfile-source')).toBeNull();
      expect(container.textContent).toContain('Using the saved profile "Saved Nextcloud"');

      // Clearing profile restores manual inputs
      await act(async () => { findButton(container, 'Clear')!.click(); });
      expect(container.querySelector('#source-provider-password')).not.toBeNull();

      // Re-select saved profile and verify step 1 + step 2
      await act(async () => {
        setSelectValue(container.querySelector<HTMLSelectElement>('#connection-profile-source')!, 'prof-src');
      });
      await clickVerifyAndAdvance();

      await act(async () => {
        setSelectValue(container.querySelector<HTMLSelectElement>('#connection-profile-target')!, 'prof-dst');
      });
      await submitConnectForm();

      const testCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect/test'))!;
      expect(JSON.parse(String(testCall[1]?.body))).toMatchObject({
        source_profile_id: 'prof-src',
        source_url: '',
        source_username: '',
        source_password: '',
      });

      const connectCall = vi.mocked(apiFetch).mock.calls.find(([url]) => String(url).endsWith('/api/migration/connect'))!;
      expect(JSON.parse(String(connectCall[1]?.body))).toMatchObject({
        source_profile_id: 'prof-src',
        source_url: '',
        source_username: '',
        source_password: '',
        target_profile_id: 'prof-dst',
        target_url: '',
        target_username: '',
        target_password: '',
      });
    });

    it('defaults blank save-profile names to URL or provider and survives profile-save failure', async () => {
      installDefaultApiMock((url, init) =>
        url.endsWith('/api/profiles') && init?.method === 'POST'
          ? Promise.resolve(new Response(JSON.stringify({ error_code: 'INTERNAL_ERROR' }), { status: 500 }))
          : null
      );

      await renderForm();
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://src.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'src-pw');
        container.querySelector<HTMLInputElement>('#saveProfile-source')!.click();
      });
      await clickVerifyAndAdvance();

      await act(async () => { selectProviderRadio(container, 'MEGA'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-mega-email')!, 'bob@mega.test');
        setInputValue(container.querySelector<HTMLInputElement>('#target-mega-password')!, 'mega-pw');
        container.querySelector<HTMLInputElement>('#saveProfile-target')!.click();
      });
      await submitConnectForm();

      const profilePosts = vi.mocked(apiFetch).mock.calls
        .filter(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST')
        .map(([, init]) => JSON.parse(String(init?.body)));

      expect(profilePosts).toEqual([
        { name: 'https://src.example.com', provider: 'nextcloud', url: 'https://src.example.com', username: 'alice', password: 'src-pw' },
        { name: 'mega', provider: 'mega', url: '', username: 'bob@mega.test', password: 'mega-pw' },
      ]);
      expect(onConnectSuccess).toHaveBeenCalledTimes(1);
    });
  });

  describe('error handling on test and connect', () => {
    it('stays on step 1 and shows translated error when connection test fails', async () => {
      installDefaultApiMock((url) =>
        url.endsWith('/api/migration/connect/test')
          ? Promise.resolve(new Response(JSON.stringify({ success: false, error_code: 'SOURCE_CONNECTION_FAILED' })))
          : null
      );

      await renderForm();
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://src.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'wrong');
      });
      await clickVerifyAndAdvance();

      expect(container.querySelector('h1')?.textContent).toBe('Step 1 of 3: Set source');
      expect(container.querySelector('[role="alert"]')?.textContent).toBe(
        'Could not connect to the source. Please check the address and credentials.'
      );
    });

    it('does not invoke onConnectSuccess or save profiles when final connect fails', async () => {
      installDefaultApiMock((url) =>
        url.endsWith('/api/migration/connect')
          ? Promise.resolve(new Response(JSON.stringify({ error_code: 'TARGET_CONNECTION_FAILED' }), { status: 400 }))
          : null
      );

      await renderForm();
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-url')!, 'https://src.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#source-provider-password')!, 'src-pw');
      });
      await clickVerifyAndAdvance();

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-url')!, 'https://dst.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-username')!, 'bob');
        setInputValue(container.querySelector<HTMLInputElement>('#target-provider-password')!, 'dst-pw');
        container.querySelector<HTMLInputElement>('#saveProfile-target')!.click();
      });
      await submitConnectForm();

      expect(onConnectSuccess).not.toHaveBeenCalled();
      expect(vi.mocked(apiFetch).mock.calls.filter(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST')).toHaveLength(0);
    });
  });
});
