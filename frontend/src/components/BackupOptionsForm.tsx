import { useId } from 'react';
import { useTranslation } from 'react-i18next';

interface BackupOptionsFormProps {
  cronExpression: string;
  setCronExpression: (value: string) => void;
  timezone: string;
  setTimezone: (value: string) => void;
  retentionCount: number;
  setRetentionCount: (value: number) => void;
  threads: number;
  setThreads: (value: number) => void;
  error?: string | null;
}

export function BackupOptionsForm({
  cronExpression,
  setCronExpression,
  timezone,
  setTimezone,
  retentionCount,
  setRetentionCount,
  threads,
  setThreads,
  error,
}: BackupOptionsFormProps) {
  const { t } = useTranslation();
  const cronId = useId();
  const timezoneId = useId();
  const retentionId = useId();
  const threadsId = useId();

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
        <div className="space-y-2">
          <label htmlFor={cronId} className="block text-xs font-medium text-[var(--color-text-primary)]">
            {t('backup.cron')}
          </label>
          <input
            id={cronId}
            value={cronExpression}
            onChange={(event) => setCronExpression(event.target.value)}
            className="ui-input w-full px-3 py-2 text-sm"
            aria-describedby={`${cronId}-hint`}
          />
          <p id={`${cronId}-hint`} className="text-xs text-[var(--color-text-muted)]">
            {t('backup.cronHint')}
          </p>
        </div>
        <div className="space-y-2">
          <label htmlFor={timezoneId} className="block text-xs font-medium text-[var(--color-text-primary)]">
            {t('backup.timezone')}
          </label>
          <input
            id={timezoneId}
            value={timezone}
            onChange={(event) => setTimezone(event.target.value)}
            className="ui-input w-full px-3 py-2 text-sm"
            placeholder="Europe/Berlin"
            aria-describedby={`${timezoneId}-hint`}
          />
          <p id={`${timezoneId}-hint`} className="text-xs text-[var(--color-text-muted)]">
            {t('backup.timezoneHint')}
          </p>
        </div>
        <div className="space-y-2">
          <label htmlFor={retentionId} className="block text-xs font-medium text-[var(--color-text-primary)]">
            {t('backup.retention')}
          </label>
          <input
            id={retentionId}
            type="number"
            min="1"
            max="365"
            value={retentionCount}
            onChange={(event) => setRetentionCount(Number(event.target.value))}
            className="ui-input w-full px-3 py-2 text-sm"
          />
          <p className="text-xs text-[var(--color-text-muted)]">{t('backup.retentionHint')}</p>
        </div>
        <div className="space-y-2">
          <label htmlFor={threadsId} className="block text-xs font-medium text-[var(--color-text-primary)]">
            {t('backup.threads')}
          </label>
          <input
            id={threadsId}
            type="number"
            min="1"
            max="16"
            value={threads}
            onChange={(event) => setThreads(Number(event.target.value))}
            className="ui-input w-full px-3 py-2 text-sm"
          />
          <p className="text-xs text-[var(--color-text-muted)]">{t('backup.threadsHint')}</p>
        </div>
      </div>
      {error && <p role="alert" className="ui-alert ui-alert-error p-3 text-sm">{error}</p>}
    </div>
  );
}
