import { beforeEach, describe, expect, it, vi } from 'vitest';
import { adminApi } from './adminApi';
import { apiFetch } from './apiClient';

vi.mock('./apiClient', () => ({ apiFetch: vi.fn() }));

describe('adminApi', () => {
  beforeEach(() => {
    vi.mocked(apiFetch).mockReset();
  });

  it('returns a structured network failure instead of rejecting', async () => {
    vi.mocked(apiFetch).mockRejectedValueOnce(new TypeError('Failed to fetch'));

    await expect(adminApi.listUsers('https://api.example.test', 'token', {})).resolves.toEqual({
      ok: false,
      errorCode: 'NETWORK',
      status: 0,
      networkError: true,
    });
  });

  it('encodes user IDs before placing them in a path segment', async () => {
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(null, { status: 204 }));

    await adminApi.suspendUser('https://api.example.test', 'token', 'user/id?x=1');

    expect(apiFetch).toHaveBeenCalledWith(
      'https://api.example.test/api/admin/users/user%2Fid%3Fx%3D1/suspend',
      expect.any(Object),
    );
  });
});
