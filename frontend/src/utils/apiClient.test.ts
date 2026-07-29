import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { configureApiClient, apiFetch } from './apiClient';

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
});
