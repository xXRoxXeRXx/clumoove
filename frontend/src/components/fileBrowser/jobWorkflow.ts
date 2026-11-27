import type { MigrationConfig } from '../../types';
import { createBackup } from '../../api/backups';
import { startMigration } from '../../api/migrations';
import { createSync, startSyncResponse } from '../../api/sync';

export type JobType = 'migration' | 'sync' | 'backup';

export interface JobSelection {
  paths: string[];
  syncPaths: string[];
  backupPaths: string[];
  calendars: string[];
  contacts: string[];
}

export interface JobOptions {
  credentials: MigrationConfig;
  targetDir: string;
  conflictStrategy: string;
  threads: number;
  bandwidthLimit: number;
  enableScheduling: boolean;
  scheduledTime: string;
  direction: 'one_way' | 'two_way';
  intervalMinutes: number;
  deletePropagation: boolean;
  existingSyncId: string | null;
  backupAvailable: boolean;
  backupCronExpression: string;
  backupTimezone: string;
  backupRetentionCount: number;
}

export type JobRequest =
  | { jobType: 'migration'; payload: Record<string, unknown> }
  | { jobType: 'sync'; existingSyncId: string | null; payload: Record<string, unknown> }
  | { jobType: 'backup'; payload: Record<string, unknown> };

export type BuildJobRequestResult =
  | { ok: true; request: JobRequest }
  | { ok: false; messageKey: string };

export function buildJobRequest(
  jobType: JobType,
  selection: JobSelection,
  options: JobOptions,
  now: Date = new Date(),
): BuildJobRequestResult {
  const calendars = jobType === 'migration' ? selection.calendars : [];
  const contacts = jobType === 'migration' ? selection.contacts : [];
  const primaryPaths = jobType === 'backup' ? selection.backupPaths : selection.paths;

  if (primaryPaths.length === 0 && calendars.length === 0 && contacts.length === 0) {
    return { ok: false, messageKey: 'fileBrowser.errors.selectOne' };
  }

  if (jobType === 'backup') {
    if (
      !options.backupAvailable ||
      options.backupRetentionCount < 1 ||
      options.backupRetentionCount > 365 ||
      options.threads < 1 ||
      options.threads > 16
    ) {
      return { ok: false, messageKey: 'backup.invalidOptions' };
    }
    return {
      ok: true,
      request: {
        jobType: 'backup',
        payload: {
          source_profile_id: options.credentials.source_profile_id,
          target_profile_id: options.credentials.target_profile_id,
          selected_paths: selection.backupPaths,
          target_dir: options.targetDir,
          cron_expression: options.backupCronExpression,
          timezone: options.backupTimezone,
          retention_count: options.backupRetentionCount,
          threads: options.threads,
        },
      },
    };
  }

  if (jobType === 'sync') {
    if (selection.paths.length === 0) {
      return { ok: false, messageKey: 'fileBrowser.errors.selectOne' };
    }
    return {
      ok: true,
      request: {
        jobType: 'sync',
        existingSyncId: options.existingSyncId,
        payload: {
          source_profile_id: options.credentials.source_profile_id,
          target_profile_id: options.credentials.target_profile_id,
          source_url: options.credentials.source_url,
          source_username: options.credentials.source_username,
          source_password: options.credentials.source_password,
          source_refresh_token: options.credentials.source_refresh_token,
          target_url: options.credentials.target_url,
          target_username: options.credentials.target_username,
          target_password: options.credentials.target_password,
          target_refresh_token: options.credentials.target_refresh_token,
          source_provider: options.credentials.source_provider,
          target_provider: options.credentials.target_provider,
          direction: options.direction,
          conflict_strategy: options.conflictStrategy,
          delete_propagation: options.deletePropagation,
          interval_minutes: options.intervalMinutes,
          threads: options.threads,
          bandwidth_limit_mbps: options.bandwidthLimit,
          target_dir: options.targetDir,
          selected_paths: selection.syncPaths,
        },
      },
    };
  }

  let scheduledTimeISO: string | undefined;
  if (options.enableScheduling) {
    if (!options.scheduledTime) {
      return { ok: false, messageKey: 'fileBrowser.errors.scheduleTimeRequired' };
    }
    const scheduledDate = new Date(options.scheduledTime);
    if (Number.isNaN(scheduledDate.getTime())) {
      return { ok: false, messageKey: 'fileBrowser.errors.invalidScheduledTime' };
    }
    if (scheduledDate <= now) {
      return { ok: false, messageKey: 'fileBrowser.errors.futureTime' };
    }
    scheduledTimeISO = scheduledDate.toISOString();
  }

  const payload: Record<string, unknown> = {
    ...options.credentials,
    conflict_strategy: options.conflictStrategy,
    paths: selection.paths,
    calendars,
    contacts,
    target_dir: options.targetDir,
    threads: options.threads,
    bandwidth_limit_mbps: options.bandwidthLimit,
  };
  if (scheduledTimeISO) {
    payload.scheduled_time = scheduledTimeISO;
  }

  return {
    ok: true,
    request: {
      jobType: 'migration',
      payload,
    },
  };
}

export interface SubmitJobContext {
  apiUrl: string;
  token: string;
  signal?: AbortSignal;
}

export type JobSubmitFailure =
  | { kind: 'aborted' }
  | { kind: 'api'; errorCode?: string; fallbackKey: string }
  | { kind: 'network' };

export type SubmitJobResult =
  | { ok: true; id: string; jobType: JobType }
  | { ok: false; failure: JobSubmitFailure; pendingSyncId?: string };

const isAbortError = (error: unknown): boolean =>
  error instanceof DOMException
    ? error.name === 'AbortError'
    : error instanceof Error && error.name === 'AbortError';

async function readErrorCode(response: Response): Promise<string | undefined> {
  const body = await response.json().catch(() => ({} as { error_code?: string }));
  return body.error_code;
}

function apiFailure(errorCode: string | undefined, fallbackKey: string, pendingSyncId?: string): SubmitJobResult {
  return {
    ok: false,
    failure: { kind: 'api', errorCode, fallbackKey },
    ...(pendingSyncId ? { pendingSyncId } : {}),
  };
}

async function readJsonOrFail<T>(
  response: Response,
  signal: AbortSignal | undefined,
  fallbackKey: string,
): Promise<{ data: T } | { fail: SubmitJobResult }> {
  if (!response.ok) {
    return { fail: apiFailure(await readErrorCode(response), fallbackKey) };
  }
  const data = await response.json() as T;
  if (signal?.aborted) {
    return { fail: { ok: false, failure: { kind: 'aborted' } } };
  }
  return { data };
}

export async function submitJob(request: JobRequest, context: SubmitJobContext): Promise<SubmitJobResult> {
  const { apiUrl, token, signal } = context;
  try {
    if (request.jobType === 'backup') {
      const response = await createBackup({ apiUrl, token, signal, payload: request.payload });
      const parsed = await readJsonOrFail<{ id?: string }>(response, signal, 'backup.createFailed');
      if ('fail' in parsed) return parsed.fail;
      return parsed.data.id
        ? { ok: true, id: parsed.data.id, jobType: 'backup' }
        : apiFailure(undefined, 'backup.createFailed');
    }

    if (request.jobType === 'sync') {
      let syncId = request.existingSyncId;
      if (!syncId) {
        const response = await createSync({ apiUrl, token, signal, payload: request.payload });
        const parsed = await readJsonOrFail<{ id?: string; success?: boolean; error_code?: string }>(
          response,
          signal,
          'sync.createFailed',
        );
        if ('fail' in parsed) return parsed.fail;
        if (parsed.data.success === false) return apiFailure(parsed.data.error_code, 'sync.createFailed');
        if (!parsed.data.id) return apiFailure(undefined, 'sync.createFailed');
        syncId = parsed.data.id;
      }

      const startResponse = await startSyncResponse({ apiUrl, token, syncId, signal });
      if (!startResponse.ok) {
        return apiFailure(await readErrorCode(startResponse), 'sync.startFailed', syncId);
      }
      return { ok: true, id: syncId, jobType: 'sync' };
    }

    const response = await startMigration({ apiUrl, token, payload: request.payload, signal });
    const parsed = await readJsonOrFail<{ success?: boolean; migration_id?: string; error_code?: string }>(
      response,
      signal,
      'fileBrowser.errors.startFailed',
    );
    if ('fail' in parsed) return parsed.fail;
    if (parsed.data.success && parsed.data.migration_id) {
      return { ok: true, id: parsed.data.migration_id, jobType: 'migration' };
    }
    return apiFailure(parsed.data.error_code, 'fileBrowser.errors.startError');
  } catch (err) {
    if (signal?.aborted || isAbortError(err)) {
      return { ok: false, failure: { kind: 'aborted' } };
    }
    return { ok: false, failure: { kind: 'network' } };
  }
}
