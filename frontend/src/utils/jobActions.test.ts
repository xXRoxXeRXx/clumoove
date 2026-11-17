import { describe, expect, it } from 'vitest';
import { canPerform, withStatus } from './jobActions';

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
