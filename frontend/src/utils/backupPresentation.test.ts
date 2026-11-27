import { describe, expect, it } from 'vitest';
import { backupBadgeStatus, isActiveBackupStatus } from './backupPresentation';

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
