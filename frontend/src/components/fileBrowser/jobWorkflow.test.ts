import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MigrationConfig } from '../../types';
import { apiFetch } from '../../utils/apiClient';
import { buildJobRequest, submitJob, type JobOptions, type JobSelection } from './jobWorkflow';

vi.mock('../../utils/apiClient', async () => {
  const actual = await vi.importActual<typeof import('../../utils/apiClient')>('../../utils/apiClient');
  return { ...actual, apiFetch: vi.fn() };
});

const baseCredentials: MigrationConfig = {
  source_url: 'https://source.example.test',
  source_username: 'alice',
  source_password: 'pw1',
  source_refresh_token: '',
  source_token_expires_in: 0,
  target_url: 'https://target.example.test',
  target_username: 'bob',
  target_password: 'pw2',
  target_refresh_token: '',
  target_token_expires_in: 0,
  source_provider: 'nextcloud',
  target_provider: 'nextcloud',
  source_profile_id: 'prof-src',
  target_profile_id: 'prof-tgt',
};

function makeSelection(overrides: Partial<JobSelection> = {}): JobSelection {
  return {
    paths: ['/Docs'],
    syncPaths: ['/Docs'],
    backupPaths: ['/Docs'],
    calendars: [],
    contacts: [],
    ...overrides,
  };
}

function makeOptions(overrides: Partial<JobOptions> = {}): JobOptions {
  return {
    credentials: baseCredentials,
    targetDir: '/Target',
    conflictStrategy: 'SKIP',
    threads: 4,
    bandwidthLimit: 0,
    enableScheduling: false,
    scheduledTime: '',
    direction: 'one_way',
    intervalMinutes: 15,
    deletePropagation: false,
    existingSyncId: null,
    backupAvailable: true,
    backupCronExpression: '0 2 * * *',
    backupTimezone: 'UTC',
    backupRetentionCount: 7,
    ...overrides,
  };
}

describe('buildJobRequest', () => {
  it('rejects empty selection across all job types', () => {
    const empty = makeSelection({ paths: [], syncPaths: [], backupPaths: [], calendars: [], contacts: [] });
    expect(buildJobRequest('migration', empty, makeOptions())).toEqual({
      ok: false,
      messageKey: 'fileBrowser.errors.selectOne',
    });
    expect(buildJobRequest('sync', empty, makeOptions())).toEqual({
      ok: false,
      messageKey: 'fileBrowser.errors.selectOne',
    });
    expect(buildJobRequest('backup', empty, makeOptions())).toEqual({
      ok: false,
      messageKey: 'fileBrowser.errors.selectOne',
    });
  });

  it('builds a migration request with files, calendars, and contacts', () => {
    const result = buildJobRequest(
      'migration',
      makeSelection({ paths: ['/Docs'], calendars: ['/cal/personal'], contacts: ['/contacts/default'] }),
      makeOptions({ threads: 8, bandwidthLimit: 50, conflictStrategy: 'OVERWRITE', targetDir: '/Dest' }),
    );
    expect(result).toEqual({
      ok: true,
      request: {
        jobType: 'migration',
        payload: {
          ...baseCredentials,
          conflict_strategy: 'OVERWRITE',
          paths: ['/Docs'],
          calendars: ['/cal/personal'],
          contacts: ['/contacts/default'],
          target_dir: '/Dest',
          threads: 8,
          bandwidth_limit_mbps: 50,
        },
      },
    });
  });

  it('validates migration scheduling time and includes ISO scheduled_time when valid', () => {
    const now = new Date('2026-11-27T12:00:00.000Z');
    expect(buildJobRequest('migration', makeSelection(), makeOptions({ enableScheduling: true, scheduledTime: '' }), now))
      .toEqual({ ok: false, messageKey: 'fileBrowser.errors.scheduleTimeRequired' });
    expect(buildJobRequest('migration', makeSelection(), makeOptions({ enableScheduling: true, scheduledTime: 'not-a-date' }), now))
      .toEqual({ ok: false, messageKey: 'fileBrowser.errors.invalidScheduledTime' });
    expect(buildJobRequest('migration', makeSelection(), makeOptions({ enableScheduling: true, scheduledTime: '2026-11-27T12:00:00.000Z' }), now))
      .toEqual({ ok: false, messageKey: 'fileBrowser.errors.futureTime' });

    const valid = buildJobRequest(
      'migration',
      makeSelection(),
      makeOptions({ enableScheduling: true, scheduledTime: '2026-11-27T13:00:00.000Z' }),
      now,
    );
    expect(valid.ok).toBe(true);
    if (valid.ok) {
      expect(valid.request.payload.scheduled_time).toBe('2026-11-27T13:00:00.000Z');
    }
  });

  it('rejects sync when only calendars or contacts are selected and shapes valid sync payloads', () => {
    expect(buildJobRequest(
      'sync',
      makeSelection({ paths: [], syncPaths: [], calendars: ['/cal/personal'] }),
      makeOptions(),
    )).toEqual({ ok: false, messageKey: 'fileBrowser.errors.selectOne' });

    const valid = buildJobRequest(
      'sync',
      makeSelection({ paths: ['/Docs', '/Photos'], syncPaths: [] }),
      makeOptions({
        direction: 'two_way',
        conflictStrategy: 'RENAME',
        deletePropagation: true,
        intervalMinutes: 30,
        threads: 2,
        bandwidthLimit: 25,
        targetDir: '/SyncDest',
        existingSyncId: 'sync-99',
      }),
    );
    expect(valid).toEqual({
      ok: true,
      request: {
        jobType: 'sync',
        existingSyncId: 'sync-99',
        payload: {
          source_profile_id: 'prof-src',
          target_profile_id: 'prof-tgt',
          source_url: 'https://source.example.test',
          source_username: 'alice',
          source_password: 'pw1',
          source_refresh_token: '',
          target_url: 'https://target.example.test',
          target_username: 'bob',
          target_password: 'pw2',
          target_refresh_token: '',
          source_provider: 'nextcloud',
          target_provider: 'nextcloud',
          direction: 'two_way',
          conflict_strategy: 'RENAME',
          delete_propagation: true,
          interval_minutes: 30,
          threads: 2,
          bandwidth_limit_mbps: 25,
          target_dir: '/SyncDest',
          selected_paths: [],
        },
      },
    });
  });

  it('validates backup availability, retention count, and thread bounds and shapes backup payloads', () => {
    expect(buildJobRequest('backup', makeSelection(), makeOptions({ backupAvailable: false })))
      .toEqual({ ok: false, messageKey: 'backup.invalidOptions' });
    expect(buildJobRequest('backup', makeSelection(), makeOptions({ backupRetentionCount: 0 })))
      .toEqual({ ok: false, messageKey: 'backup.invalidOptions' });
    expect(buildJobRequest('backup', makeSelection(), makeOptions({ backupRetentionCount: 366 })))
      .toEqual({ ok: false, messageKey: 'backup.invalidOptions' });
    expect(buildJobRequest('backup', makeSelection(), makeOptions({ threads: 0 })))
      .toEqual({ ok: false, messageKey: 'backup.invalidOptions' });
    expect(buildJobRequest('backup', makeSelection(), makeOptions({ threads: 17 })))
      .toEqual({ ok: false, messageKey: 'backup.invalidOptions' });

    const valid = buildJobRequest(
      'backup',
      makeSelection({ backupPaths: ['/Archive'] }),
      makeOptions({
        targetDir: '/Backups',
        backupCronExpression: '0 3 * * 1',
        backupTimezone: 'Europe/Berlin',
        backupRetentionCount: 14,
        threads: 6,
      }),
    );
    expect(valid).toEqual({
      ok: true,
      request: {
        jobType: 'backup',
        payload: {
          source_profile_id: 'prof-src',
          target_profile_id: 'prof-tgt',
          selected_paths: ['/Archive'],
          target_dir: '/Backups',
          cron_expression: '0 3 * * 1',
          timezone: 'Europe/Berlin',
          retention_count: 14,
          threads: 6,
        },
      },
    });
  });
});

describe('submitJob', () => {
  const ctx = { apiUrl: 'https://api.example.test', token: 'jwt' };

  beforeEach(() => {
    vi.mocked(apiFetch).mockReset();
  });

  function respond(body: unknown, init?: ResponseInit) {
    vi.mocked(apiFetch).mockResolvedValueOnce(new Response(JSON.stringify(body), init));
  }

  it('submits a migration request and returns the migration id', async () => {
    const built = buildJobRequest('migration', makeSelection(), makeOptions());
    expect(built.ok).toBe(true);
    if (!built.ok) return;

    respond({ success: true, migration_id: 'mig-1' });
    const result = await submitJob(built.request, ctx);
    expect(result).toEqual({ ok: true, id: 'mig-1', jobType: 'migration' });
    const [url] = vi.mocked(apiFetch).mock.calls[0];
    expect(String(url)).toBe('https://api.example.test/api/migration/start');
  });

  it('creates and starts a sync job, and retains pendingSyncId if start fails', async () => {
    const built = buildJobRequest('sync', makeSelection(), makeOptions());
    expect(built.ok).toBe(true);
    if (!built.ok) return;

    respond({ id: 'sync-1', success: true });
    respond({ error_code: 'SYNC_BUSY' }, { status: 503 });

    const failedStart = await submitJob(built.request, ctx);
    expect(failedStart).toEqual({
      ok: false,
      pendingSyncId: 'sync-1',
      failure: { kind: 'api', errorCode: 'SYNC_BUSY', fallbackKey: 'sync.startFailed' },
    });

    const retryBuilt = buildJobRequest('sync', makeSelection(), makeOptions({ existingSyncId: 'sync-1' }));
    expect(retryBuilt.ok).toBe(true);
    if (!retryBuilt.ok) return;

    vi.mocked(apiFetch).mockReset();
    respond({ success: true });
    const retryResult = await submitJob(retryBuilt.request, ctx);
    expect(retryResult).toEqual({ ok: true, id: 'sync-1', jobType: 'sync' });
    expect(vi.mocked(apiFetch)).toHaveBeenCalledTimes(1);
    expect(String(vi.mocked(apiFetch).mock.calls[0][0])).toBe('https://api.example.test/api/sync/sync-1/start');
  });

  it('submits a backup request and maps API, network, and abort failures', async () => {
    const built = buildJobRequest('backup', makeSelection(), makeOptions());
    expect(built.ok).toBe(true);
    if (!built.ok) return;

    respond({ id: 'bak-1' });
    expect(await submitJob(built.request, ctx)).toEqual({ ok: true, id: 'bak-1', jobType: 'backup' });

    respond({ error_code: 'BACKUP_QUOTA' }, { status: 400 });
    expect(await submitJob(built.request, ctx)).toEqual({
      ok: false,
      failure: { kind: 'api', errorCode: 'BACKUP_QUOTA', fallbackKey: 'backup.createFailed' },
    });

    vi.mocked(apiFetch).mockRejectedValueOnce(new Error('offline'));
    expect(await submitJob(built.request, ctx)).toEqual({
      ok: false,
      failure: { kind: 'network' },
    });

    const controller = new AbortController();
    controller.abort();
    vi.mocked(apiFetch).mockRejectedValueOnce(new DOMException('Aborted', 'AbortError'));
    expect(await submitJob(built.request, { ...ctx, signal: controller.signal })).toEqual({
      ok: false,
      failure: { kind: 'aborted' },
    });
  });
});
