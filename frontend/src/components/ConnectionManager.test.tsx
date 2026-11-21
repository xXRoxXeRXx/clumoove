import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import type { ConnectionProfilePublic } from '../api/profiles';
import { ConfirmationProvider } from '../contexts/ConfirmationContext';
import { ConnectionManager, type ConnectionManagerProps } from './ConnectionManager';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const VALID_SFTP_KEY = 'SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU';

function makeProfile(overrides: Partial<ConnectionProfilePublic> = {}): ConnectionProfilePublic {
  return {
    id: 'prof-1',
    name: 'My Nextcloud',
    provider: 'nextcloud',
    url: 'https://cloud.example.com',
    username: 'alice',
    has_password: true,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

function setInputValue(input: HTMLInputElement | HTMLTextAreaElement, value: string): void {
  const prototype = Object.getPrototypeOf(input);
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(input, value);
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
}

function setSelectValue(select: HTMLSelectElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set?.call(select, value);
  select.dispatchEvent(new Event('change', { bubbles: true }));
}

async function flushAsync(): Promise<void> {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

function findButton(rootEl: ParentNode, text: string): HTMLButtonElement | undefined {
  return Array.from(rootEl.querySelectorAll<HTMLButtonElement>('button')).find(
    (btn) => btn.textContent?.trim() === text || btn.textContent?.includes(text)
  );
}

function selectProvider(container: ParentNode, providerLabel: string): void {
  const radio = Array.from(container.querySelectorAll<HTMLButtonElement>('[role="radio"]')).find(
    (btn) => btn.textContent?.trim() === providerLabel
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
): void {
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

describe('ConnectionManager workflows', () => {
  let container: HTMLDivElement;
  let root: Root;
  let fetchMock: ReturnType<typeof vi.spyOn>;
  let profilesState: ConnectionProfilePublic[];

  function installDefaultFetchMock(
    customHandler?: (url: string, method: string, init?: RequestInit) => Promise<Response> | null,
  ) {
    fetchMock.mockImplementation((input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      const custom = customHandler?.(url, method, init);
      if (custom) return custom;

      if (url.endsWith('/api/profiles') && method === 'GET') {
        return Promise.resolve(new Response(JSON.stringify({ profiles: profilesState }), { status: 200 }));
      }
      if (url.endsWith('/api/profiles') && method === 'POST') {
        const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
        const created = makeProfile({
          id: `prof-${profilesState.length + 1}`,
          name: String(body.name ?? ''),
          provider: String(body.provider ?? 'nextcloud'),
          url: String(body.url ?? ''),
          username: String(body.username ?? ''),
          oauth_user: typeof body.oauth_user === 'string' ? body.oauth_user : undefined,
        });
        profilesState = [...profilesState, created];
        return Promise.resolve(new Response(JSON.stringify(created), { status: 201 }));
      }
      if (/\/api\/profiles\/[^/?]+(\?.*)?$/.test(url) && method === 'PUT') {
        return Promise.resolve(new Response(JSON.stringify({ success: true }), { status: 200 }));
      }
      if (/\/api\/profiles\/[^/]+\/test$/.test(url) && method === 'POST') {
        return Promise.resolve(new Response(JSON.stringify({ success: true }), { status: 200 }));
      }
      if (/\/api\/profiles\/[^/]+$/.test(url) && method === 'DELETE') {
        const id = decodeURIComponent(url.split('/').pop()!);
        profilesState = profilesState.filter((p) => p.id !== id);
        return Promise.resolve(new Response(null, { status: 204 }));
      }

      throw new Error(`Unexpected fetch: ${method} ${url}`);
    });
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    localStorage.clear();
    sessionStorage.clear();
    profilesState = [];

    fetchMock = vi.spyOn(globalThis, 'fetch');
    installDefaultFetchMock();

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

  async function renderManager(props: Partial<ConnectionManagerProps> = {}) {
    await act(async () => {
      root.render(
        <ConfirmationProvider>
          <ConnectionManager
            apiUrl="https://api.example.test"
            token="jwt-token"
            localStorageEnabled={false}
            oauthProviders={{}}
            {...props}
          />
        </ConfirmationProvider>
      );
      await Promise.resolve();
    });
    await flushAsync();
  }

  async function openNewProfileModal() {
    const newBtn = findButton(container, 'New profile');
    expect(newBtn).toBeDefined();
    await act(async () => {
      newBtn!.click();
    });
  }

  async function submitProfileForm() {
    const form = container.querySelector<HTMLFormElement>('[role="dialog"] form');
    expect(form).not.toBeNull();
    await act(async () => {
      form!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
      await Promise.resolve();
    });
    await flushAsync();
  }

  describe('listing and credential protection', () => {
    it('renders empty state when no profiles exist and sends bearer authorization', async () => {
      await renderManager();
      expect(container.textContent).toContain('No saved connections yet.');
      expect(getBearerHeader(fetchMock.mock.calls[0][1])).toBe('Bearer jwt-token');
    });

    it('displays saved profiles without rendering secrets in the DOM', async () => {
      const future = new Date(Date.now() + 5 * 24 * 60 * 60 * 1000).toISOString();
      const past = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString();
      profilesState = [
        makeProfile({ id: 'p-nc', name: 'Work Nextcloud', provider: 'nextcloud', username: 'alice', has_password: true }),
        makeProfile({ id: 'p-gd', name: 'Personal Drive', provider: 'google', oauth_user: 'alice@gmail.com', token_expires_at: future }),
        makeProfile({ id: 'p-db', name: 'Old Dropbox', provider: 'dropbox', oauth_user: 'alice@db.test', token_expires_at: past }),
      ];

      await renderManager({ oauthProviders: { google: true, dropbox: true } });

      expect(container.textContent).toContain('Work Nextcloud');
      expect(container.textContent).toContain('alice');
      expect(container.textContent).toContain('Connected as alice@gmail.com');
      expect(container.textContent).toContain('Token expires in 5 days');
      expect(container.textContent).toContain('Connected as alice@db.test');
      expect(container.textContent).toContain('Token expired');
      expect(container.querySelectorAll('input[type="password"]')).toHaveLength(0);
    });
  });

  describe('creating and validating profiles', () => {
    it('rejects a blank profile name without issuing a create request', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, '   ');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-url')!, 'https://cloud.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'app-secret');
      });

      await submitProfileForm();

      expect(container.querySelector('[role="alert"]')?.textContent).toContain('A profile name is required');
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
    });

    it('creates a Nextcloud profile, refreshes the list, and never renders the saved secret in DOM', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, '  Home Nextcloud  ');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-url')!, 'https://cloud.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'app-secret-123');
      });

      await submitProfileForm();

      const postCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/api/profiles') && init?.method === 'POST');
      expect(postCall).toBeDefined();
      expect(getBearerHeader(postCall![1])).toBe('Bearer jwt-token');
      expect(JSON.parse(String(postCall![1]?.body))).toEqual({
        name: 'Home Nextcloud',
        provider: 'nextcloud',
        url: 'https://cloud.example.com',
        username: 'alice',
        password: 'app-secret-123',
      });
      expect(container.querySelector('[role="dialog"]')).toBeNull();
      expect(container.textContent).toContain('Connection saved!');
      expect(container.textContent).toContain('Home Nextcloud');
      expect(container.innerHTML).not.toContain('app-secret-123');
    });

    it('creates OpenCloud, Seafile (optional username), WebDAV, and MagentaCLOUD profiles', async () => {
      await renderManager();

      for (const spec of [
        { label: 'OpenCloud', provider: 'opencloud', name: 'OC', url: 'https://oc.example.com', username: 'oc-user', password: 'oc-pw' },
        { label: 'Seafile', provider: 'seafile', name: 'SF', url: 'https://sf.example.com', username: '', password: 'sf-repo-password' },
        { label: 'WebDAV', provider: 'webdav', name: 'DAV', url: 'https://dav.example.com/files', username: 'dav-user', password: 'dav-pw' },
        { label: 'MagentaCLOUD', provider: 'magentacloud', name: 'MC', url: null, username: 'mc@t-online.de', password: 'mc-webdav-pw' },
      ]) {
        await openNewProfileModal();
        await act(async () => { selectProvider(container, spec.label); });
        await act(async () => {
          setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, spec.name);
          if (spec.url !== null) {
            setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-url')!, spec.url);
          }
          if (spec.username) {
            setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, spec.username);
          }
          setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, spec.password);
        });
        await submitProfileForm();
      }

      const payloads = fetchMock.mock.calls
        .filter(([, init]) => init?.method === 'POST')
        .map(([, init]) => JSON.parse(String(init?.body)));

      expect(payloads).toEqual([
        { name: 'OC', provider: 'opencloud', url: 'https://oc.example.com', username: 'oc-user', password: 'oc-pw' },
        { name: 'SF', provider: 'seafile', url: 'https://sf.example.com', username: '', password: 'sf-repo-password' },
        { name: 'DAV', provider: 'webdav', url: 'https://dav.example.com/files', username: 'dav-user', password: 'dav-pw' },
        { name: 'MC', provider: 'magentacloud', url: '', username: 'mc@t-online.de', password: 'mc-webdav-pw' },
      ]);
    });

    it('validates and creates an SMB profile with built SMB URL', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        selectProvider(container, 'SMB/CIFS');
      });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'NAS Share');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-host')!, '192.168.1.50');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-port')!, '445');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-share')!, '../escape');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'smbuser');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'smbpass');
      });

      await submitProfileForm();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Please enter a valid secure server address.');
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-share')!, '/backups');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-domain')!, 'WORKGROUP');
      });

      await submitProfileForm();

      const postCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!;
      expect(JSON.parse(String(postCall[1]?.body))).toEqual({
        name: 'NAS Share',
        provider: 'smb',
        url: 'smb://192.168.1.50:445/backups?domain=WORKGROUP',
        username: 'smbuser',
        password: 'smbpass',
      });
    });

    it('blocks insecure or SSRF S3 endpoints and accepts valid HTTPS custom endpoints', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        selectProvider(container, 'S3');
      });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'Archive Bucket');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-s3-bucket')!, 'my-bucket');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-s3-region')!, 'eu-central-1');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'AKIAEXAMPLE');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'secret-key');
      });

      for (const badEndpoint of ['http://s3.example.com', 'https://169.254.169.254', 'https://[::1]']) {
        await act(async () => {
          setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-s3-endpoint')!, badEndpoint);
        });
        await submitProfileForm();
        expect(container.querySelector('[role="alert"]')?.textContent).toContain('Please enter a valid secure server address.');
      }
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-s3-endpoint')!, 'https://s3.wasabisys.com');
      });
      await submitProfileForm();

      const postCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!;
      expect(JSON.parse(String(postCall[1]?.body))).toEqual({
        name: 'Archive Bucket',
        provider: 's3',
        url: 's3://my-bucket?region=eu-central-1&endpoint=https%3A%2F%2Fs3.wasabisys.com',
        username: 'AKIAEXAMPLE',
        password: 'secret-key',
      });
    });

    it('requires a valid SHA256 host key fingerprint for SFTP and supports SSH private key auth', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        selectProvider(container, 'SFTP');
      });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'SSH Backup');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-sftp-host')!, 'sftp.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-sftp-port')!, '2222');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-sftp-hostkey')!, 'MD5:invalid');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'deploy');
      });

      await submitProfileForm();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('SHA256 fingerprint');
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-sftp-hostkey')!, VALID_SFTP_KEY);
        findButton(container.querySelector('[role="dialog"]')!, 'Security key')!.click();
      });
      await act(async () => {
        setInputValue(
          container.querySelector<HTMLTextAreaElement>('#profile-editor-sftp-privatekey')!,
          '-----BEGIN OPENSSH PRIVATE KEY-----\nkey-body\n-----END OPENSSH PRIVATE KEY-----'
        );
      });

      await submitProfileForm();

      const postCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!;
      expect(JSON.parse(String(postCall[1]?.body))).toEqual({
        name: 'SSH Backup',
        provider: 'sftp',
        url: `sftp://sftp.example.com:2222?host_key=${encodeURIComponent(VALID_SFTP_KEY)}`,
        username: 'deploy',
        password: '-----BEGIN OPENSSH PRIVATE KEY-----\nkey-body\n-----END OPENSSH PRIVATE KEY-----',
      });
    });

    it('validates FTPS host/port and updates default port when switching TLS mode', async () => {
      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        selectProvider(container, 'FTPS');
      });

      const portInput = container.querySelector<HTMLInputElement>('#profile-editor-ftp-port')!;
      const tlsSelect = container.querySelector<HTMLSelectElement>('#profile-editor-ftp-tls-mode')!;
      expect(portInput.value).toBe('21');

      await act(async () => {
        setSelectValue(tlsSelect, 'implicit');
      });
      expect(portInput.value).toBe('990');

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'FTPS Vault');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-ftp-host')!, 'ftp.example.com');
        setInputValue(portInput, '0');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'ftpuser');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'ftppass');
      });

      await submitProfileForm();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('valid FTPS port between 1 and 65535');
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);

      await act(async () => {
        setInputValue(portInput, '990');
      });
      await submitProfileForm();

      const postCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!;
      expect(JSON.parse(String(postCall[1]?.body))).toEqual({
        name: 'FTPS Vault',
        provider: 'ftp',
        url: 'ftps://ftp.example.com:990',
        username: 'ftpuser',
        password: 'ftppass',
      });
    });

    it('creates Immich, Koofr, MEGA, and Local profiles with provider-specific credential shapes', async () => {
      await renderManager({ localStorageEnabled: true });

      // Immich: URL + API key, empty username
      await openNewProfileModal();
      await act(async () => { selectProvider(container, 'Immich'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'Photos');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-url')!, 'https://immich.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'immich-api-key');
      });
      await submitProfileForm();

      // Koofr: empty URL + username + password
      await openNewProfileModal();
      await act(async () => { selectProvider(container, 'Koofr'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'Koofr Vault');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'koofr@example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'koofr-app-pw');
      });
      await submitProfileForm();

      // MEGA: empty URL + email + password
      await openNewProfileModal();
      await act(async () => { selectProvider(container, 'MEGA'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'MEGA Drive');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'mega@example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'mega-pw');
      });
      await submitProfileForm();

      // Local: empty URL + empty username, no password field sent
      await openNewProfileModal();
      await act(async () => { selectProvider(container, 'Local'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'Server Local');
      });
      await submitProfileForm();

      const postPayloads = fetchMock.mock.calls
        .filter(([, init]) => init?.method === 'POST')
        .map(([, init]) => JSON.parse(String(init?.body)));

      expect(postPayloads).toEqual([
        { name: 'Photos', provider: 'immich', url: 'https://immich.example.com', username: '', password: 'immich-api-key' },
        { name: 'Koofr Vault', provider: 'koofr', url: '', username: 'koofr@example.com', password: 'koofr-app-pw' },
        { name: 'MEGA Drive', provider: 'mega', url: '', username: 'mega@example.com', password: 'mega-pw' },
        { name: 'Server Local', provider: 'local', url: '', username: '' },
      ]);
    });

    it('requires OAuth completion, rejects untrusted postMessage origins, and stores OAuth tokens in memory only', async () => {
      const popup = { closed: false, close: vi.fn() } as unknown as Window;
      vi.spyOn(window, 'open').mockReturnValue(popup);

      await renderManager({ oauthProviders: { google: true, dropbox: true, hidrive: true, box: true } });
      await openNewProfileModal();

      await act(async () => { selectProvider(container, 'Google'); });
      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'My Google Drive');
      });

      // Attempt save before OAuth popup completes
      await submitProfileForm();
      expect(container.querySelector('[role="alert"]')?.textContent).toContain('Connect the OAuth provider before saving this profile.');
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);

      // Start OAuth popup
      await act(async () => {
        findButton(container.querySelector('[role="dialog"]')!, 'Connect with Google')!.click();
      });

      // Spoofed origin message must be ignored
      await act(async () => {
        dispatchOAuthSuccess(popup, 'google', {
          origin: 'https://evil.example.test',
          token: 'spoofed-access',
          refreshToken: 'spoofed-refresh',
          username: 'attacker@evil.test',
        });
      });
      expect(container.textContent).not.toContain('attacker@evil.test');

      // Legitimate OAuth callback from API origin + popup source
      await act(async () => {
        dispatchOAuthSuccess(popup, 'google', {
          token: 'google-access-token',
          refreshToken: 'google-refresh-token',
          username: 'user@gmail.com',
        });
      });

      expect(container.querySelector('[role="dialog"]')?.textContent).toContain('Connected as user@gmail.com');
      await submitProfileForm();

      // Dropbox, HiDrive & Box OAuth creation
      for (const spec of [
        { label: 'Dropbox', provider: 'dropbox', name: 'My Dropbox', user: 'user@dropbox.test' },
        { label: 'HiDrive', provider: 'hidrive', name: 'My HiDrive', user: 'user@hidrive.test' },
        { label: 'Box', provider: 'box', name: 'My Box', user: 'user@box.test' },
      ]) {
        await openNewProfileModal();
        await act(async () => { selectProvider(container, spec.label); });
        await act(async () => {
          setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, spec.name);
          findButton(container.querySelector('[role="dialog"]')!, `Connect with ${spec.label}`)!.click();
        });
        await act(async () => {
          dispatchOAuthSuccess(popup, spec.provider, {
            token: `${spec.provider}-access`,
            refreshToken: `${spec.provider}-refresh`,
            username: spec.user,
          });
        });
        await submitProfileForm();
      }

      const postPayloads = fetchMock.mock.calls
        .filter(([, init]) => init?.method === 'POST')
        .map(([, init]) => JSON.parse(String(init?.body)));

      expect(postPayloads).toEqual([
        {
          name: 'My Google Drive',
          provider: 'google',
          url: 'oauth://google',
          username: 'user@gmail.com',
          password: 'google-access-token',
          refresh_token: 'google-refresh-token',
          refresh_token_expires_in: 3600,
          oauth_user: 'user@gmail.com',
        },
        {
          name: 'My Dropbox',
          provider: 'dropbox',
          url: 'oauth://dropbox',
          username: 'user@dropbox.test',
          password: 'dropbox-access',
          refresh_token: 'dropbox-refresh',
          refresh_token_expires_in: 3600,
          oauth_user: 'user@dropbox.test',
        },
        {
          name: 'My HiDrive',
          provider: 'hidrive',
          url: 'oauth://hidrive',
          username: 'user@hidrive.test',
          password: 'hidrive-access',
          refresh_token: 'hidrive-refresh',
          refresh_token_expires_in: 3600,
          oauth_user: 'user@hidrive.test',
        },
        {
          name: 'My Box',
          provider: 'box',
          url: 'oauth://box',
          username: 'user@box.test',
          password: 'box-access',
          refresh_token: 'box-refresh',
          refresh_token_expires_in: 3600,
          oauth_user: 'user@box.test',
        },
      ]);
      expect(container.innerHTML).not.toContain('google-access-token');
      expect(container.innerHTML).not.toContain('google-refresh-token');
      expect(localStorage.length).toBe(0);
      expect(sessionStorage.length).toBe(0);
    });

    it('keeps the modal open and displays translated API errors when saving fails', async () => {
      installDefaultFetchMock((url, method) =>
        url.endsWith('/api/profiles') && method === 'POST'
          ? Promise.resolve(new Response(JSON.stringify({ error_code: 'FORBIDDEN' }), { status: 403 }))
          : null
      );

      await renderManager();
      await openNewProfileModal();

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('[role="dialog"] input[type="text"]')!, 'Blocked');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-url')!, 'https://cloud.example.com');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-username')!, 'alice');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'secret');
      });

      await submitProfileForm();

      expect(container.querySelector('[role="dialog"]')).not.toBeNull();
      expect(container.querySelector('[role="alert"]')?.textContent).toBe('Access forbidden.');
    });
  });

  describe('editing profiles and preserving credentials', () => {
    it('omits password from PUT payload when left blank while editing an existing profile', async () => {
      profilesState = [
        makeProfile({
          id: 'smb-1',
          name: 'Old SMB',
          provider: 'smb',
          url: 'smb://10.0.0.5:445/files?domain=CORP',
          username: 'olduser',
          has_password: true,
        }),
      ];

      await renderManager();

      await act(async () => {
        findButton(container, 'Edit')!.click();
      });

      const dialog = container.querySelector('[role="dialog"]')!;
      expect(container.querySelector<HTMLInputElement>('#profile-editor-smb-host')?.value).toBe('10.0.0.5');
      expect(container.querySelector<HTMLInputElement>('#profile-editor-smb-share')?.value).toBe('files');
      expect(container.querySelector<HTMLInputElement>('#profile-editor-smb-domain')?.value).toBe('CORP');
      expect(container.querySelector<HTMLInputElement>('#profile-editor-password')?.value).toBe('');

      // Provider cannot be switched while editing
      await act(async () => {
        selectProvider(dialog, 'Nextcloud');
      });
      expect(container.querySelector('#profile-editor-smb-host')).not.toBeNull();

      await act(async () => {
        setInputValue(dialog.querySelector<HTMLInputElement>('input[type="text"]')!, 'Renamed SMB');
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-smb-share')!, 'archive');
      });

      await submitProfileForm();

      const putCall = fetchMock.mock.calls.find(([url, init]) => String(url).includes('/api/profiles/smb-1') && init?.method === 'PUT')!;
      expect(String(putCall[0])).toBe('https://api.example.test/api/profiles/smb-1?url=1&username=1');
      expect(getBearerHeader(putCall[1])).toBe('Bearer jwt-token');
      const payload = JSON.parse(String(putCall[1]?.body));
      expect(payload).toEqual({
        name: 'Renamed SMB',
        provider: 'smb',
        url: 'smb://10.0.0.5:445/archive?domain=CORP',
        username: 'olduser',
      });
      expect('password' in payload).toBe(false);
      expect(container.textContent).toContain('Connection updated!');
    });

    it('includes password in PUT payload when explicitly updated during edit without leaking it to DOM', async () => {
      profilesState = [
        makeProfile({
          id: 'nc-1',
          name: 'Cloud',
          provider: 'nextcloud',
          url: 'https://nc.example.com',
          username: 'alice',
        }),
      ];

      await renderManager();
      await act(async () => { findButton(container, 'Edit')!.click(); });

      await act(async () => {
        setInputValue(container.querySelector<HTMLInputElement>('#profile-editor-password')!, 'rotated-secret');
      });

      await submitProfileForm();

      const putCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')!;
      expect(JSON.parse(String(putCall[1]?.body))).toEqual({
        name: 'Cloud',
        provider: 'nextcloud',
        url: 'https://nc.example.com',
        username: 'alice',
        password: 'rotated-secret',
      });
      expect(container.innerHTML).not.toContain('rotated-secret');
    });
  });

  describe('testing, reauthorizing, and deleting profiles', () => {
    it('tests a saved profile server-side and shows translated success or failure feedback', async () => {
      profilesState = [makeProfile({ id: 'prof-test', name: 'Test Target' })];
      await renderManager();

      await act(async () => {
        findButton(container, 'Test')!.click();
        await Promise.resolve();
      });
      await flushAsync();

      const testCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/api/profiles/prof-test/test'));
      expect(testCall).toBeDefined();
      expect(getBearerHeader(testCall![1])).toBe('Bearer jwt-token');
      expect(testCall![1]?.body).toBeUndefined();
      expect(container.querySelector('[role="status"]')?.textContent).toContain('Connection successful!');

      fetchMock.mockImplementationOnce(() =>
        Promise.resolve(new Response(JSON.stringify({ success: false, error_code: 'SOURCE_CONNECTION_FAILED' }), { status: 200 }))
      );
      await act(async () => {
        findButton(container, 'Test')!.click();
        await Promise.resolve();
      });
      await flushAsync();

      expect(container.querySelector('[role="alert"]')?.textContent).toBe(
        'Could not connect to the source. Please check the address and credentials.'
      );
    });

    it('re-authorizes an OAuth profile via popup and writes refreshed tokens without url/username query flags', async () => {
      const popup = { closed: false, close: vi.fn() } as unknown as Window;
      vi.spyOn(window, 'open').mockReturnValue(popup);
      profilesState = [
        makeProfile({ id: 'oauth-1', name: 'OneDrive', provider: 'onedrive', oauth_user: 'old@live.com' }),
      ];

      await renderManager({ oauthProviders: { onedrive: true } });

      await act(async () => {
        findButton(container, 'Re-authorize')!.click();
      });

      await act(async () => {
        dispatchOAuthSuccess(popup, 'onedrive', {
          token: 'fresh-access',
          refreshToken: 'fresh-refresh',
          expiresIn: 7200,
          username: 'new@live.com',
        });
        await Promise.resolve();
      });
      await flushAsync();

      const putCall = fetchMock.mock.calls.find(([url, init]) => String(url).includes('/api/profiles/oauth-1') && init?.method === 'PUT')!;
      expect(String(putCall[0])).toBe('https://api.example.test/api/profiles/oauth-1');
      expect(JSON.parse(String(putCall[1]?.body))).toEqual({
        password: 'fresh-access',
        refresh_token: 'fresh-refresh',
        refresh_token_expires_in: 7200,
        oauth_user: 'new@live.com',
      });
      expect(container.textContent).toContain('Connection updated!');
    });

    it('confirms before deleting a profile and refreshes the list after deletion', async () => {
      profilesState = [makeProfile({ id: 'del-1', name: 'Disposable' })];
      await renderManager();

      // Open confirm dialog and cancel
      await act(async () => {
        findButton(container, 'Delete')!.click();
      });
      expect(container.textContent).toContain('Delete this saved connection?');
      await act(async () => {
        findButton(container, 'Cancel')!.click();
        await Promise.resolve();
      });
      await flushAsync();
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(0);
      expect(container.textContent).toContain('Disposable');

      // Open confirm dialog and confirm
      await act(async () => {
        findButton(container, 'Delete')!.click();
      });
      await act(async () => {
        const dialog = container.querySelector('[role="alertdialog"]')!;
        findButton(dialog, 'Confirm')!.click();
        await Promise.resolve();
      });
      await flushAsync();

      const deleteCall = fetchMock.mock.calls.find(([, init]) => init?.method === 'DELETE');
      expect(deleteCall).toBeDefined();
      expect(getBearerHeader(deleteCall![1])).toBe('Bearer jwt-token');
      expect(container.textContent).toContain('Connection deleted!');
      expect(container.textContent).toContain('No saved connections yet.');
    });
  });
});
