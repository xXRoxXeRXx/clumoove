import { describe, expect, it } from 'vitest';
import {
  backupBadgeStatus,
  canPerform,
  isActiveBackupStatus,
  pauseResumeAction,
  withStatus,
} from './jobActions';

describe('canPerform', () => {
  it('is true only for actions the backend listed', () => {
    const job = { allowed_actions: ['pause', 'cancel'] as const };
    expect(canPerform(job, 'pause')).toBe(true);
    expect(canPerform(job, 'cancel')).toBe(true);
    expect(canPerform(job, 'resume')).toBe(false);
    expect(canPerform(job, 'start')).toBe(false);
  });

  it('allows nothing when the backend sent no actions', () => {
    expect(canPerform({}, 'pause')).toBe(false);
    expect(canPerform({ allowed_actions: [] }, 'resume')).toBe(false);
    expect(canPerform(null, 'resume')).toBe(false);
    expect(canPerform(undefined, 'resume')).toBe(false);
  });
});

describe('withStatus', () => {
  it('drops the now stale actions until the next server frame', () => {
    const job = { id: 'a', status: 'RUNNING', allowed_actions: ['pause' as const] };
    expect(withStatus(job, 'PAUSED')).toEqual({ id: 'a', status: 'PAUSED', allowed_actions: [] });
  });
});

describe('pauseResumeAction', () => {
  it('returns resume when resume is allowed, even if pause were also listed', () => {
    expect(pauseResumeAction({ allowed_actions: ['resume'] })).toBe('resume');
    expect(pauseResumeAction({ allowed_actions: ['pause', 'resume'] })).toBe('resume');
  });

  it('returns pause when pause is allowed and resume is not', () => {
    expect(pauseResumeAction({ allowed_actions: ['pause', 'cancel'] })).toBe('pause');
  });

  it('returns null when neither resume nor pause is allowed', () => {
    expect(pauseResumeAction({ allowed_actions: ['start', 'cancel'] })).toBeNull();
    expect(pauseResumeAction({ allowed_actions: [] })).toBeNull();
    expect(pauseResumeAction({})).toBeNull();
    expect(pauseResumeAction(null)).toBeNull();
    expect(pauseResumeAction(undefined)).toBeNull();
  });
});

describe('isActiveBackupStatus', () => {
  it('identifies active backup statuses for presentation', () => {
    for (const status of ['QUEUED', 'SCANNING', 'RUNNING', 'VERIFYING']) {
      expect(isActiveBackupStatus(status)).toBe(true);
    }
    for (const status of ['IDLE', 'PAUSED', 'COMPLETED', 'FAILED', 'CANCELLED', 'DELETING']) {
      expect(isActiveBackupStatus(status)).toBe(false);
    }
  });
});

describe('backupBadgeStatus', () => {
  it('maps backup-specific statuses to StatusBadge statuses and passes others through', () => {
    expect(backupBadgeStatus('QUEUED')).toBe('PENDING');
    expect(backupBadgeStatus('SCANNING')).toBe('INDEXING');
    expect(backupBadgeStatus('RUNNING')).toBe('RUNNING');
    expect(backupBadgeStatus('PAUSED')).toBe('PAUSED');
    expect(backupBadgeStatus('COMPLETED')).toBe('COMPLETED');
  });
});
