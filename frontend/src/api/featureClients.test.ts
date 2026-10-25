import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiJson } from '../utils/apiClient';
import { updateMigrationBandwidth } from './migrations';
import { startSync } from './sync';
import { getBackup } from './backups';
import { requestPasswordReset, submitTotp } from './auth';
import { updateNotificationSetting } from './settings';

vi.mock('../utils/apiClient', () => ({ apiJson: vi.fn() }));

describe('feature API clients', () => {
  beforeEach(() => {
    vi.mocked(apiJson).mockReset();
  });

  it('sends typed migration updates to their feature endpoint', async () => {
    await updateMigrationBandwidth({ apiUrl: 'https://api.example.test', token: 'token', migrationId: 'migration 1', limitMbps: 25 });

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/migration/migration%201/bandwidth',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify({ limit_mbps: 25 }) }),
    );
  });

  it('sends sync actions through the sync client', async () => {
    await startSync({ apiUrl: 'https://api.example.test', token: 'token', syncId: 'sync 1' });

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/sync/sync%201/start',
      expect.objectContaining({ method: 'POST' }),
    );
  });

  it('preserves normalized backup failures for the view layer', async () => {
    const failure = { ok: false as const, status: 403, errorCode: 'FORBIDDEN', networkError: false };
    vi.mocked(apiJson).mockResolvedValueOnce(failure);

    await expect(getBackup({ apiUrl: 'https://api.example.test', token: 'token', backupId: 'backup-1' })).resolves.toEqual(failure);
  });

  it('uses the existing TOTP session contract and cookie credentials', async () => {
    await submitTotp({ apiUrl: 'https://api.example.test', tempSession: 'session', code: '123456' });

    expect(apiJson).toHaveBeenCalledWith('https://api.example.test/api/auth/totp', expect.objectContaining({
      method: 'POST', credentials: 'include', body: JSON.stringify({ temp_session: 'session', code: '123456' }),
    }));
  });

  it('keeps auth and notification payloads within their feature clients', async () => {
    await requestPasswordReset({ apiUrl: 'https://api.example.test', email: 'user@example.test' });
    await updateNotificationSetting({ apiUrl: 'https://api.example.test', token: 'token', type: 'email', enabled: true });

    expect(apiJson).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/auth/forgot-password', expect.objectContaining({ method: 'POST', body: JSON.stringify({ email: 'user@example.test' }) }));
    expect(apiJson).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/settings/notifications', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ type: 'email', enabled: true }) }));
  });
});
