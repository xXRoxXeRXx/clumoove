import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useOAuthPopup } from './useOAuthPopup';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function OAuthPopupHarness({ onSuccess, onError }: { onSuccess: () => void; onError: (code: string) => void }) {
  const { openOAuthPopup } = useOAuthPopup('https://api.example.test/');

  return (
    <button
      type="button"
      onClick={() => openOAuthPopup('google', 'connect', {
        onSuccess,
        onError,
      })}
    >
      Connect
    </button>
  );
}

describe('useOAuthPopup', () => {
  let container: HTMLDivElement;
  let root: Root;

  const render = (onSuccess: () => void, onError: (code: string) => void) => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<OAuthPopupHarness onSuccess={onSuccess} onError={onError} />));
  };

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('removes its message listener after a successful authorization', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const popup = { closed: false, close: vi.fn() } as unknown as Window;
    const open = vi.spyOn(window, 'open').mockReturnValue(popup);
    const removeEventListener = vi.spyOn(window, 'removeEventListener');
    render(onSuccess, onError);

    act(() => container.querySelector('button')?.click());

    const authorizationUrl = new URL(String(open.mock.calls[0][0]));
    expect(authorizationUrl.pathname).toBe('/api/oauth/auth');
    expect(authorizationUrl.searchParams.get('provider')).toBe('google');
    expect(authorizationUrl.searchParams.get('purpose')).toBe('connect');

    const message = new MessageEvent('message', {
      origin: 'https://api.example.test',
      source: popup,
      data: {
        type: 'oauth-success',
        provider: 'google',
        purpose: 'connect',
        token: 'access-token',
        refreshToken: 'refresh-token',
        expiresIn: 3600,
        username: 'user@example.test',
      },
    });
    act(() => window.dispatchEvent(message));
    act(() => window.dispatchEvent(message));

    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(onError).not.toHaveBeenCalled();
    expect(removeEventListener).toHaveBeenCalledWith('message', expect.any(Function));
  });

  it('reports a blocked popup immediately', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    vi.spyOn(window, 'open').mockReturnValue(null);
    render(onSuccess, onError);

    act(() => container.querySelector('button')?.click());

    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith('OAUTH_POPUP_BLOCKED');
  });

  it('reports cancellation when the user closes the popup', () => {
    vi.useFakeTimers();
    const onSuccess = vi.fn();
    const onError = vi.fn();
    const popup = { closed: false, close: vi.fn() };
    vi.spyOn(window, 'open').mockReturnValue(popup as unknown as Window);
    render(onSuccess, onError);

    act(() => container.querySelector('button')?.click());
    popup.closed = true;
    act(() => vi.advanceTimersByTime(1000));

    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith('OAUTH_CANCELLED');
  });
});
