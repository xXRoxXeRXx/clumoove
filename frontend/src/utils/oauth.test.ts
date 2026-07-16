import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { listenForOAuthMessage } from './oauth';
import type { OAuthSuccessMessage } from './oauth';

const API_ORIGIN = 'https://api.example.com';

function makeEvent(origin: string, data: unknown, source?: Window): MessageEvent {
  return new MessageEvent('message', { origin, data, source: source as Window });
}

describe('listenForOAuthMessage', () => {
  let addSpy: ReturnType<typeof vi.spyOn>;
  let removeSpy: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    addSpy = vi.spyOn(window, 'addEventListener');
    removeSpy = vi.spyOn(window, 'removeEventListener');
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('registers and returns a working cleanup that removes the listener', () => {
    const cleanup = listenForOAuthMessage(API_ORIGIN, {
      onSuccess: () => {},
      onError: () => {},
    });
    expect(addSpy).toHaveBeenCalledWith('message', expect.any(Function));
    cleanup();
    expect(removeSpy).toHaveBeenCalledWith('message', expect.any(Function));
  });

  it('ignores messages from a non-allowlisted origin (confused-deputy protection)', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const fakeSource = {} as Window;
    listener(makeEvent('https://evil.example.com', { type: 'oauth-success', token: 'x' }, fakeSource));

    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });

  it('ignores messages with no source window', () => {
    const onSuccess = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError: () => {} });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    listener(makeEvent(API_ORIGIN, { type: 'oauth-success', token: 'x' }, undefined));

    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('ignores malformed message payloads', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const src = {} as Window;
    listener(makeEvent(API_ORIGIN, { type: 'unknown' }, src));
    listener(makeEvent(API_ORIGIN, null, src));

    expect(onSuccess).not.toHaveBeenCalled();
    expect(onError).not.toHaveBeenCalled();
  });

  it('dispatches oauth-success to onSuccess', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const msg: OAuthSuccessMessage = {
      type: 'oauth-success',
      provider: 'dropbox',
      purpose: 'connect',
      token: 'access',
      refreshToken: 'refresh',
      expiresIn: 3600,
      username: 'u',
    };
    const src = { close: vi.fn() } as unknown as Window;
    listener(makeEvent(API_ORIGIN, msg, src));

    expect(onSuccess).toHaveBeenCalledTimes(1);
    expect(onSuccess).toHaveBeenCalledWith(msg);
    expect(onError).not.toHaveBeenCalled();
  });

  it('dispatches oauth-error to onError', () => {
    const onSuccess = vi.fn();
    const onError = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const src = { close: vi.fn() } as unknown as Window;
    listener(makeEvent(API_ORIGIN, { type: 'oauth-error', error: 'denied' }, src));

    expect(onError).toHaveBeenCalledTimes(1);
    expect(onError).toHaveBeenCalledWith({ type: 'oauth-error', error: 'denied' });
  });

  it('rejects success messages whose purpose does not match expectedPurpose', () => {
    const onSuccess = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError: () => {}, expectedPurpose: 'connect' });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const src = { close: vi.fn() } as unknown as Window;
    listener(
      makeEvent(
        API_ORIGIN,
        { type: 'oauth-success', provider: 'dropbox', purpose: 'login', token: 'x', refreshToken: 'r', expiresIn: 1, username: 'u' },
        src,
      ),
    );

    expect(onSuccess).not.toHaveBeenCalled();
  });

  it('closes the source popup on a valid message', () => {
    const onSuccess = vi.fn();
    listenForOAuthMessage(API_ORIGIN, { onSuccess, onError: () => {} });

    const listener = addSpy.mock.calls[0][1] as EventListener;
    const src = { close: vi.fn() } as unknown as Window;
    listener(
      makeEvent(
        API_ORIGIN,
        { type: 'oauth-success', provider: 'dropbox', purpose: 'connect', token: 'x', refreshToken: 'r', expiresIn: 1, username: 'u' },
        src,
      ),
    );

    expect(src.close).toHaveBeenCalled();
  });
});