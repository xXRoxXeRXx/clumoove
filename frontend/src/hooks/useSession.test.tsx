import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { useSession } from './useSession';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const replaceNav = vi.fn();
const initialNavigation = {};
const onLogout = vi.fn();

function SessionHarness({
  resetToken = null,
  emailChangeToken = null,
  navigation = initialNavigation,
}: {
  resetToken?: string | null;
  emailChangeToken?: string | null;
  navigation?: Record<string, string>;
}) {
  const session = useSession({
    apiUrl: 'https://api.example.test',
    resetToken,
    emailChangeToken,
    initialNavigation: navigation,
    replaceNav,
    onLogout,
  });
  return <>
    <output data-token={session.token} data-user={session.user?.email ?? ''} data-validating={String(session.isValidating)} />
    <button type="button" onClick={() => session.authenticate('manual-token', { email: 'manual@example.test' })}>login</button>
    <button type="button" onClick={() => void session.logout()}>logout</button>
  </>;
}

describe('useSession', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    localStorage.setItem('has_session', 'true');
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(() => {
    vi.useRealTimers();
    act(() => root.unmount());
    container.remove();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    replaceNav.mockReset();
    onLogout.mockReset();
  });

  it('clears a failed restore and returns to login', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('', { status: 401 }))));
    await act(async () => { root.render(<SessionHarness />); await Promise.resolve(); await Promise.resolve(); });
    expect(localStorage.getItem('has_session')).toBeNull();
    expect(replaceNav).toHaveBeenCalledWith('login', '');
  });

  it.each([
    ['reset-token', null],
    [null, 'email-token'],
  ])('skips validation for special links', async (resetToken, emailChangeToken) => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    await act(async () => { root.render(<SessionHarness resetToken={resetToken} emailChangeToken={emailChangeToken} />); await Promise.resolve(); });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    ['initialMigrationId', 'migration-1', '/api/migration/migration-1', 'dashboard'],
    ['initialSyncId', 'sync-1', '/api/sync/sync-1', 'syncdetail'],
    ['initialBackupId', 'backup-1', '/api/backup/backup-1', 'backupdetail'],
    ['initialProfileId', 'profile-1', '/api/profiles/profile-1', 'files'],
  ])('validates %s deep links and falls back when unavailable', async (key, id, path, step) => {
    vi.stubGlobal('fetch', vi.fn((url) => {
      const value = String(url);
      if (value.endsWith('/refresh')) return Promise.resolve(new Response(JSON.stringify({ access_token: 'access-token' })));
      if (value.endsWith('/me')) return Promise.resolve(new Response(JSON.stringify({ email: 'user@example.test' })));
      return Promise.resolve(new Response('', { status: 404 }));
    }));
    await act(async () => { root.render(<SessionHarness navigation={{ [key]: id }} />); await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
    expect(replaceNav).toHaveBeenCalledWith('history', '');
    expect(globalThis.fetch).toHaveBeenCalledWith(`https://api.example.test${path}`, expect.anything());

    replaceNav.mockReset();
    root.unmount(); container.replaceChildren(); root = createRoot(container);
    vi.stubGlobal('fetch', vi.fn((url) => {
      const value = String(url);
      if (value.endsWith('/refresh')) return Promise.resolve(new Response(JSON.stringify({ access_token: 'access-token' })));
      if (value.endsWith('/me')) return Promise.resolve(new Response(JSON.stringify({ email: 'user@example.test' })));
      return Promise.resolve(new Response('{}'));
    }));
    await act(async () => { root.render(<SessionHarness navigation={{ [key]: id }} />); await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); });
    expect(replaceNav).toHaveBeenCalledWith(step, id);
  });

  it('exposes login and logout without leaking session state', async () => {
    localStorage.removeItem('has_session');
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response('{}'))));
    await act(async () => { root.render(<SessionHarness />); });
    await act(async () => { container.querySelector<HTMLButtonElement>('button')?.click(); });
    expect(replaceNav).toHaveBeenCalledWith('history', '');
    await act(async () => { container.querySelectorAll<HTMLButtonElement>('button')[1]?.click(); await Promise.resolve(); });
    expect(localStorage.getItem('has_session')).toBeNull();
    expect(onLogout).toHaveBeenCalledOnce();
    expect(replaceNav).toHaveBeenCalledWith('login', '');
  });

  it('logs out when a scheduled refresh has no access token', async () => {
    vi.useFakeTimers();
    let refreshes = 0;
    vi.stubGlobal('fetch', vi.fn((url) => {
      if (String(url).endsWith('/refresh')) {
        refreshes += 1;
        return Promise.resolve(new Response(JSON.stringify(refreshes === 1 ? { access_token: 'access-token' } : {})));
      }
      return Promise.resolve(new Response(JSON.stringify({ email: 'user@example.test' })));
    }));
    await act(async () => { root.render(<SessionHarness />); await Promise.resolve(); await Promise.resolve(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(14 * 60 * 1000); });
    expect(localStorage.getItem('has_session')).toBeNull();
    expect(replaceNav).toHaveBeenCalledWith('login', '');
    vi.useRealTimers();
  });

  it('does not update navigation after an in-flight restore is unmounted', async () => {
    let resolveRefresh!: (response: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { resolveRefresh = resolve; })));
    await act(async () => { root.render(<SessionHarness />); await Promise.resolve(); });
    await act(async () => { root.unmount(); resolveRefresh(new Response(JSON.stringify({ access_token: 'access-token' }))); await Promise.resolve(); });
    expect(replaceNav).not.toHaveBeenCalled();
  });

  it('restores a saved session and sends authenticated users to history', async () => {
    vi.stubGlobal('fetch', vi.fn((url) => {
      if (String(url).endsWith('/api/auth/refresh')) {
        return Promise.resolve(new Response(JSON.stringify({ access_token: 'access-token' })));
      }
      return Promise.resolve(new Response(JSON.stringify({ id: 'user-1', email: 'user@example.test' })));
    }));

    await act(async () => {
      root.render(<SessionHarness />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('output')?.dataset).toMatchObject({
      token: 'access-token', user: 'user@example.test', validating: 'false',
    });
    expect(replaceNav).toHaveBeenCalledWith('history', '');
  });
});
