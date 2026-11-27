import { beforeEach, describe, expect, it, vi } from 'vitest';
import { apiFetch, apiJson } from '../utils/apiClient';
import { connectMigration, testMigrationConnection, updateMigrationBandwidth } from './migrations';
import { listConnectionProfilesResponse } from './profiles';
import { startSync } from './sync';
import {
  cancelBackupVerify,
  cancelRestorePreview,
  cancelRestoreRun,
  consumeRestorePreview,
  createRestorePreview,
  downloadRestoreReport,
  getBackup,
  getRestorePreview,
  getRestoreRun,
  listBackupSnapshots,
  listBackupVerifies,
  listRestoreRunItems,
  listRestoreRuns,
  listSnapshotItems,
  startBackupVerify,
  updateBackup,
} from './backups';
import {
  changePassword,
  confirmEmailChange,
  deleteAvatar,
  disableTwoFactor,
  enableTwoFactor,
  getEmailChangeAvailability,
  getTwoFactorStatus,
  requestEmailChange,
  requestPasswordReset,
  setupTwoFactor,
  submitTotp,
  updateCurrentUserProfile,
  uploadAvatar,
} from './auth';
import { getInstanceSettings, getPublicSettings, updateInstanceSetting, updateNotificationSetting } from './settings';
import { listErrors } from './errors';

vi.mock('../utils/apiClient', () => ({ apiFetch: vi.fn(), apiJson: vi.fn() }));

describe('feature API clients', () => {
  beforeEach(() => {
    vi.mocked(apiFetch).mockReset();
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

  it('reads and updates instance and public settings through the settings client', async () => {
    await getPublicSettings({ apiUrl: 'https://api.example.test' });
    await getInstanceSettings({ apiUrl: 'https://api.example.test', token: 'token' });
    await updateInstanceSetting({ apiUrl: 'https://api.example.test', token: 'token', key: 'registrations_enabled', value: 'true' });

    expect(apiJson).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/settings');
    expect(apiJson).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/settings', {
      headers: { Authorization: 'Bearer token' },
    });
    expect(apiJson).toHaveBeenNthCalledWith(3, 'https://api.example.test/api/settings', {
      method: 'PUT',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ key: 'registrations_enabled', value: 'true' }),
    });
  });

  it('fetches paginated errors through the errors client', async () => {
    await listErrors({ endpoint: 'https://api.example.test/api/sync/sync-1/errors', token: 'token', limit: 20, offset: 40 });

    expect(apiJson).toHaveBeenCalledWith(
      'https://api.example.test/api/sync/sync-1/errors?limit=20&offset=40',
      { headers: { Authorization: 'Bearer token' } },
    );
  });

  it('routes 2FA, profile, avatar, password, and email-change calls through the auth client', async () => {
    const auth = { apiUrl: 'https://api.example.test', token: 'token' };

    await getTwoFactorStatus(auth);
    await setupTwoFactor(auth);
    await enableTwoFactor({ ...auth, code: '123456' });
    await disableTwoFactor({ ...auth, code: '654321' });
    await getEmailChangeAvailability({ apiUrl: 'https://api.example.test' });
    await updateCurrentUserProfile({ ...auth, displayName: 'Alice' });
    await uploadAvatar({ ...auth, avatar: 'data:image/png;base64,abc' });
    await deleteAvatar(auth);
    await changePassword({ ...auth, currentPassword: 'old', newPassword: 'new-password-123', confirmPassword: 'new-password-123' });
    await requestEmailChange({ ...auth, newEmail: 'new@example.test' });
    await confirmEmailChange({ apiUrl: 'https://api.example.test', token: 'email-token' });

    expect(apiJson).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/auth/2fa/status', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/auth/2fa/setup', { method: 'GET', headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(3, 'https://api.example.test/api/auth/2fa/enable', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: '123456' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(4, 'https://api.example.test/api/auth/2fa/disable', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: '654321' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(5, 'https://api.example.test/api/auth/email-change-available');
    expect(apiJson).toHaveBeenNthCalledWith(6, 'https://api.example.test/api/auth/me', {
      method: 'PUT',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ display_name: 'Alice' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(7, 'https://api.example.test/api/user/avatar', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ avatar: 'data:image/png;base64,abc' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(8, 'https://api.example.test/api/user/avatar', {
      method: 'DELETE',
      headers: { Authorization: 'Bearer token' },
    });
    expect(apiJson).toHaveBeenNthCalledWith(9, 'https://api.example.test/api/auth/change-password', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: 'old', new_password: 'new-password-123', confirm_password: 'new-password-123' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(10, 'https://api.example.test/api/auth/change-email', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ new_email: 'new@example.test' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(11, 'https://api.example.test/api/auth/confirm-email-change', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: 'email-token' }),
    });
  });

  it('routes backup update, snapshot, verify, and restore calls through the backups client', async () => {
    const auth = { apiUrl: 'https://api.example.test', token: 'token' };
    const job = { ...auth, backupId: 'job 1' };

    await updateBackup({ ...job, payload: { cron_expression: '0 * * * *', timezone: 'UTC', retention_count: 5, threads: 2 } });
    await listBackupSnapshots(job);
    await listSnapshotItems({ ...job, snapshotId: 'snap 1', path: '/docs' });
    await listBackupVerifies(job);
    await startBackupVerify({ ...job, payload: { mode: 'full' } });
    await cancelBackupVerify({ ...job, verifyId: 'ver 1' });
    const previewPayload = {
      selected_paths: ['/docs'],
      target_root: '/restore',
      conflict_strategy: 'RENAME',
      threads: 4,
      bandwidth_mbps: 0,
    };
    await createRestorePreview({ ...job, snapshotId: 'snap 1', payload: previewPayload });
    await listRestoreRuns(auth);
    await getRestorePreview({ ...auth, previewId: 'prev 1' });
    await consumeRestorePreview({ ...auth, previewId: 'prev 1' });
    await cancelRestorePreview({ ...auth, previewId: 'prev 1' });
    await getRestoreRun({ ...auth, runId: 'run 1' });
    await listRestoreRunItems({ ...auth, runId: 'run 1' });
    await cancelRestoreRun({ ...auth, runId: 'run 1' });
    await downloadRestoreReport({ ...auth, runId: 'run 1' });

    expect(apiJson).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/backup/job%201', {
      method: 'PUT',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ cron_expression: '0 * * * *', timezone: 'UTC', retention_count: 5, threads: 2 }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/backup/job%201/snapshots', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(3, 'https://api.example.test/api/backup/job%201/snapshots/snap%201/items?path=%2Fdocs', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(4, 'https://api.example.test/api/backup/job%201/verify', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(5, 'https://api.example.test/api/backup/job%201/verify', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: 'full' }),
    });
    expect(apiJson).toHaveBeenNthCalledWith(6, 'https://api.example.test/api/backup/job%201/verify/ver%201/cancel', {
      method: 'POST',
      headers: { Authorization: 'Bearer token' },
    });
    expect(apiJson).toHaveBeenNthCalledWith(7, 'https://api.example.test/api/backup/job%201/snapshots/snap%201/restore/previews', {
      method: 'POST',
      headers: { Authorization: 'Bearer token', 'Content-Type': 'application/json' },
      body: JSON.stringify(previewPayload),
    });
    expect(apiJson).toHaveBeenNthCalledWith(8, 'https://api.example.test/api/restore', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(9, 'https://api.example.test/api/restore/previews/prev%201', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(10, 'https://api.example.test/api/restore/previews/prev%201/consume', { method: 'POST', headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(11, 'https://api.example.test/api/restore/previews/prev%201/cancel', { method: 'POST', headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(12, 'https://api.example.test/api/restore/runs/run%201', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(13, 'https://api.example.test/api/restore/runs/run%201/items', { headers: { Authorization: 'Bearer token' } });
    expect(apiJson).toHaveBeenNthCalledWith(14, 'https://api.example.test/api/restore/runs/run%201/cancel', { method: 'POST', headers: { Authorization: 'Bearer token' } });
    expect(apiFetch).toHaveBeenCalledWith('https://api.example.test/api/restore/runs/run%201/report', { headers: { Authorization: 'Bearer token' } });
  });

  it('routes migration connect helpers and profile list responses through their feature clients', async () => {
    const auth = { apiUrl: 'https://api.example.test', token: 'token' };

    await testMigrationConnection({ ...auth, payload: { role: 'source' } });
    await connectMigration({ ...auth, payload: { source_provider: 'nextcloud' } });
    await listConnectionProfilesResponse(auth.apiUrl, auth.token);

    expect(apiFetch).toHaveBeenNthCalledWith(1, 'https://api.example.test/api/migration/connect/test', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ role: 'source' }),
    }));
    expect(apiFetch).toHaveBeenNthCalledWith(2, 'https://api.example.test/api/migration/connect', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ source_provider: 'nextcloud' }),
    }));
    expect(apiFetch).toHaveBeenNthCalledWith(3, 'https://api.example.test/api/profiles', {
      headers: { Authorization: 'Bearer token' },
      signal: undefined,
    });
  });
});
