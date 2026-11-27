import type { JobAction } from '../types';

/** Anything the API returns for a job: it lists the actions its current status allows. */
export interface HasAllowedActions {
  allowed_actions?: readonly JobAction[];
}

/**
 * Whether the backend allows `action` for this job right now. The backend owns the
 * lifecycle rules, so a missing list means "nothing is allowed" rather than a guess
 * derived from the status.
 */
export function canPerform(job: HasAllowedActions | null | undefined, action: JobAction): boolean {
  return job?.allowed_actions?.includes(action) ?? false;
}

/**
 * Optimistic status change after a successful control call. The allowed actions
 * belonged to the previous status, so they are cleared until the next server frame
 * delivers the new ones.
 */
export function withStatus<T extends { status: string } & HasAllowedActions>(job: T, status: string): T {
  return { ...job, status, allowed_actions: [] };
}

/**
 * Resolves the pause/resume toggle action for a job from its `allowed_actions`.
 * Returns `'resume'` when resumable, `'pause'` when pausable, or `null` when neither is allowed.
 */
export function pauseResumeAction(job: HasAllowedActions | null | undefined): 'resume' | 'pause' | null {
  if (canPerform(job, 'resume')) return 'resume';
  if (canPerform(job, 'pause')) return 'pause';
  return null;
}
