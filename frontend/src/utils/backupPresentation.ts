export type ActiveBackupStatus = 'QUEUED' | 'SCANNING' | 'RUNNING' | 'VERIFYING';

const ACTIVE_BACKUP_STATUSES: ReadonlySet<string> = new Set<ActiveBackupStatus>([
  'QUEUED',
  'SCANNING',
  'RUNNING',
  'VERIFYING',
]);

/**
 * Whether a backup job status represents an in-flight run for presentation (active counts, live polling).
 */
export function isActiveBackupStatus(status: string): status is ActiveBackupStatus {
  return ACTIVE_BACKUP_STATUSES.has(status);
}

const BACKUP_BADGE_STATUS: Record<string, string> = {
  QUEUED: 'PENDING',
  SCANNING: 'INDEXING',
};

/**
 * Maps backup-specific statuses (`QUEUED`, `SCANNING`) to the shared `StatusBadge` status names.
 */
export function backupBadgeStatus(status: string): string {
  return BACKUP_BADGE_STATUS[status] ?? status;
}
