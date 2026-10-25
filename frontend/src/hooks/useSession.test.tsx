import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { useSession } from './useSession';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const replaceNav = vi.fn();
const initialNavigation = {};

function SessionHarness() {
  const session = useSession({
    apiUrl: 'https://api.example.test',
    resetToken: null,
    emailChangeToken: null,
    initialNavigation,
    replaceNav,
  });
  return <output data-token={session.token} data-user={session.user?.email ?? ''} data-validating={String(session.isValidating)} />;
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
    act(() => root.unmount());
    container.remove();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    replaceNav.mockReset();
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
