import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ApiDisplayError, apiErrorMessage, apiFetch, apiJson, apiResponseError, configureApiClient } from './apiClient';

describe('apiFetch', () => {
  const apiUrl = 'https://api.example.com';
  let accessToken = 'tok-1';
  const onAuthFailure = vi.fn();
  const setAccessToken = vi.fn((t: string) => {
    accessToken = t;
  });

  beforeEach(() => {
    accessToken = 'tok-1';
    onAuthFailure.mockReset();
    setAccessToken.mockClear();
    configureApiClient({
      apiUrl,
      getAccessToken: () => accessToken,
      setAccessToken,
      onAuthFailure,
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('retries once after 401 with refreshed token', async () => {
    const fetchMock = vi
      .fn()
      // initial request
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      // refresh
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ access_token: 'tok-2' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      // retry
      .mockResolvedValueOnce(new Response(JSON.stringify({ ok: true }), { status: 200 }));

    vi.stubGlobal('fetch', fetchMock);

    const res = await apiFetch(`${apiUrl}/api/migration`, {
      headers: { Authorization: 'Bearer tok-1' },
    });

    expect(res.status).toBe(200);
    expect(setAccessToken).toHaveBeenCalledWith('tok-2');
    expect(fetchMock).toHaveBeenCalledTimes(3);
    const retryInit = fetchMock.mock.calls[2][1] as RequestInit;
    const headers = new Headers(retryInit.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-2');
    expect(onAuthFailure).not.toHaveBeenCalled();
  });

  it('does not refresh on auth login 401', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(null, { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);

    const res = await apiFetch(`${apiUrl}/api/auth/login`, { method: 'POST' });
    expect(res.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(onAuthFailure).not.toHaveBeenCalled();
  });

  it('ignores 401 from foreign origins', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(null, { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);

    const res = await apiFetch('https://evil.example.com/x');
    expect(res.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(onAuthFailure).not.toHaveBeenCalled();
  });

  it('does not log out when another request has refreshed the token', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockRejectedValueOnce(new Error('network unavailable'));
    vi.stubGlobal('fetch', fetchMock);

    const request = apiFetch(`${apiUrl}/api/migration`);
    accessToken = 'tok-2';

    const res = await request;
    expect(res.status).toBe(401);
    expect(onAuthFailure).not.toHaveBeenCalled();
  });

  it('backs off repeated refresh attempts after a refresh failure', async () => {
    const fetchMock = vi
      .fn()
      // First API request and its failed refresh.
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      .mockResolvedValueOnce(new Response(null, { status: 401 }))
      // Second API request sees the refresh cooldown.
      .mockResolvedValueOnce(new Response(null, { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);

    await apiFetch(`${apiUrl}/api/migration`);
    await apiFetch(`${apiUrl}/api/migration`);

    expect(fetchMock).toHaveBeenCalledTimes(3);
  });
});

describe('apiJson', () => {
  beforeEach(() => {
    configureApiClient({
      apiUrl: 'https://api.example.com',
      getAccessToken: () => 'token',
      setAccessToken: vi.fn(),
      onAuthFailure: vi.fn(),
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    ['a JSON error code', new Response(JSON.stringify({ error_code: 'FORBIDDEN' }), { status: 403 }), 'FORBIDDEN'],
    ['a malformed error body', new Response('not json', { status: 403 }), 'UNKNOWN'],
  ])('preserves %s on a non-2xx response', async (_name, response, errorCode) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response));

    const result = await apiJson('/api/sync');

    expect(result).toMatchObject({ ok: false, status: 403, errorCode, networkError: false });
    if (result.ok === false) {
      expect(apiErrorMessage(result, (code) => `translated:${code}`, 'network fallback')).toBe(`translated:${errorCode}`);
    }
  });

  it('returns a contextual fallback for network failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));

    const result = await apiJson('/api/sync');

    expect(result).toMatchObject({ ok: false, status: 0, networkError: true });
    if (result.ok === false) {
      expect(apiErrorMessage(result, (code) => `translated:${code}`, 'network fallback')).toBe('network fallback');
    }
  });

  it('normalizes failed binary-endpoint responses without reading a success body', async () => {
    const result = await apiResponseError(new Response(JSON.stringify({ error_code: 'FORBIDDEN' }), { status: 403 }));

    expect(result).toMatchObject({ ok: false, status: 403, errorCode: 'FORBIDDEN', networkError: false });
  });

  it('marks locally translated API messages as safe for display', () => {
    const error = new ApiDisplayError('Access forbidden.');

    expect(error).toBeInstanceOf(Error);
    expect(error.name).toBe('ApiDisplayError');
  });
});
