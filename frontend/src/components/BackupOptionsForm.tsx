import { useId, useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import {
  CalendarDaysIcon,
  ClockIcon,
  InformationCircleIcon,
  ExclamationTriangleIcon,
} from './icons';
import {
  parseCronToScheduleConfig,
  buildCronFromScheduleConfig,
  formatCronHuman,
  isValidCron,
  type ScheduleMode,
  type ScheduleConfig,
} from '../utils/cronFormatter';

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

const COMMON_TIMEZONES = [
  'Europe/Berlin',
  'Europe/Vienna',
  'Europe/Zurich',
  'Europe/London',
  'Europe/Paris',
  'UTC',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'Asia/Tokyo',
  'Asia/Singapore',
  'Australia/Sydney',
];

const WEEKDAYS = [
  { value: 1, key: '1' },
  { value: 2, key: '2' },
  { value: 3, key: '3' },
  { value: 4, key: '4' },
  { value: 5, key: '5' },
  { value: 6, key: '6' },
  { value: 0, key: '0' },
];

const INTERVAL_HOURS_OPTIONS = [1, 2, 4, 6, 8, 12];

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
  const timezoneId = useId();
  const timezoneListId = useId();
  const retentionId = useId();
  const threadsId = useId();
  const dailyTimeInputId = useId();
  const weeklyTimeInputId = useId();
  const monthlyTimeInputId = useId();
  const customCronId = useId();

  const [prevCron, setPrevCron] = useState(cronExpression);
  const [scheduleConfig, setScheduleConfig] = useState<ScheduleConfig>(() =>
    parseCronToScheduleConfig(cronExpression)
  );

  if (cronExpression !== prevCron) {
    setPrevCron(cronExpression);
    setScheduleConfig(parseCronToScheduleConfig(cronExpression));
  }

  const handleConfigChange = (updater: (prev: ScheduleConfig) => ScheduleConfig) => {
    setScheduleConfig((prev) => {
      const next = updater(prev);
      const newCron = buildCronFromScheduleConfig(next);
      setCronExpression(newCron);
      return next;
    });
  };

  const setMode = (mode: ScheduleMode) => {
    handleConfigChange((prev) => {
      let customCron = prev.customCron;
      if (mode === 'custom' && !customCron) {
        customCron = buildCronFromScheduleConfig(prev);
      }
      return { ...prev, mode, customCron };
    });
  };

  const setTime = (time: string) => {
    handleConfigChange((prev) => ({ ...prev, time }));
  };

  const setDayOfWeek = (dayOfWeek: number) => {
    handleConfigChange((prev) => ({ ...prev, dayOfWeek }));
  };

  const setDayOfMonth = (dayOfMonth: number) => {
    handleConfigChange((prev) => ({ ...prev, dayOfMonth }));
  };

  const setIntervalHours = (intervalHours: number) => {
    handleConfigChange((prev) => ({ ...prev, intervalHours }));
  };

  const setCustomCron = (customCron: string) => {
    setScheduleConfig((prev) => ({ ...prev, customCron }));
    setCronExpression(customCron);
  };

  const isCustom = scheduleConfig.mode === 'custom';
  const customValid = useMemo(() => !isCustom || isValidCron(cronExpression), [isCustom, cronExpression]);
  const humanReadableDescription = useMemo(
    () => formatCronHuman(cronExpression, t),
    [cronExpression, t]
  );

  return (
    <div className="space-y-6">
      {/* Target hint alert */}
      <div className="ui-alert ui-alert-info p-3.5 text-xs flex gap-2.5 items-start" role="note">
        <InformationCircleIcon className="size-4 shrink-0 mt-0.5" />
        <span>{t('backup.targetHint')}</span>
      </div>

      {/* Schedule Configuration Card */}
      <div className="space-y-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 sm:p-5">
        <div className="flex items-center justify-between border-b border-[var(--color-border-light)] pb-3">
          <div className="flex items-center gap-2">
            <CalendarDaysIcon className="size-4 text-[var(--color-text-primary)]" />
            <h4 className="text-xs font-bold uppercase tracking-wider text-[var(--color-text-primary)] font-mono">
              {t('backup.schedule')}
            </h4>
          </div>
          <span className="text-[11px] font-mono text-[var(--color-text-muted)]">
            {cronExpression}
          </span>
        </div>

        {/* Schedule Mode Selector Pills */}
        <div className="space-y-2">
          <label className="block text-[10px] font-bold uppercase tracking-widest text-[var(--color-text-muted)] font-mono">
            {t('backup.scheduleType')}
          </label>
          <div className="grid grid-cols-2 sm:grid-cols-5 gap-2">
            {(
              [
                { mode: 'daily', label: t('backup.scheduleDaily') },
                { mode: 'weekly', label: t('backup.scheduleWeekly') },
                { mode: 'monthly', label: t('backup.scheduleMonthly') },
                { mode: 'hourly', label: t('backup.scheduleHourly') },
                { mode: 'custom', label: t('backup.scheduleCustom') },
              ] as const
            ).map((item) => {
              const active = scheduleConfig.mode === item.mode;
              return (
                <button
                  key={item.mode}
                  type="button"
                  onClick={() => setMode(item.mode)}
                  aria-pressed={active}
                  className={`py-2 px-3 text-xs font-semibold rounded-lg border transition-all cursor-pointer text-center ${
                    active
                      ? 'ui-button-primary border-transparent shadow-sm'
                      : 'ui-button-secondary border-[var(--color-border)] hover:bg-[var(--color-bg-tertiary)] text-[var(--color-text-secondary)]'
                  }`}
                >
                  {item.label}
                </button>
              );
            })}
          </div>
        </div>

        {/* Dynamic Controls based on selected mode */}
        <div className="pt-2">
          {/* DAILY MODE */}
          {scheduleConfig.mode === 'daily' && (
            <div className="space-y-3">
              <div className="flex flex-col sm:flex-row sm:items-center gap-3">
                <div className="w-full sm:w-48">
                  <label htmlFor={dailyTimeInputId} className="block text-xs font-medium text-[var(--color-text-primary)] mb-1.5">
                    {t('backup.scheduleTime')}
                  </label>
                  <input
                    id={dailyTimeInputId}
                    type="time"
                    value={scheduleConfig.time}
                    onChange={(e) => setTime(e.target.value)}
                    className="ui-input w-full px-3 py-2 text-sm font-mono"
                  />
                </div>
                <div className="flex flex-wrap items-center gap-1.5 sm:mt-5">
                  <span className="text-[11px] text-[var(--color-text-muted)] mr-1">{t('backup.presets')}</span>
                  {[
                    { label: t('backup.presetNight'), val: '02:00' },
                    { label: t('backup.presetMorning'), val: '06:00' },
                    { label: t('backup.presetEvening'), val: '20:00' },
                  ].map((p) => (
                    <button
                      key={p.val}
                      type="button"
                      onClick={() => setTime(p.val)}
                      className={`px-2.5 py-1 text-xs rounded-md border cursor-pointer font-mono transition-colors ${
                        scheduleConfig.time === p.val
                          ? 'bg-[var(--color-bg-tertiary)] border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold'
                          : 'border-[var(--color-border)] text-[var(--color-text-muted)] hover:text-[var(--color-text-primary)]'
                      }`}
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* WEEKLY MODE */}
          {scheduleConfig.mode === 'weekly' && (
            <div className="space-y-4">
              <div className="space-y-1.5">
                <label className="block text-xs font-medium text-[var(--color-text-primary)]">
                  {t('backup.scheduleDayOfWeek')}
                </label>
                <div className="flex flex-wrap gap-1.5">
                  {WEEKDAYS.map((wd) => {
                    const selected = scheduleConfig.dayOfWeek === wd.value;
                    return (
                      <button
                        key={wd.value}
                        type="button"
                        onClick={() => setDayOfWeek(wd.value)}
                        className={`px-3 py-1.5 text-xs rounded-lg font-medium border transition-colors cursor-pointer ${
                          selected
                            ? 'bg-[var(--color-bg-primary)] border-[var(--color-text-primary)] text-[var(--color-text-primary)] font-bold shadow-xs'
                            : 'border-[var(--color-border)] text-[var(--color-text-secondary)] hover:bg-[var(--color-bg-tertiary)]'
                        }`}
                      >
                        {t(`backup.weekdays.${wd.key}`)}
                      </button>
                    );
                  })}
                </div>
              </div>
              <div className="w-full sm:w-48">
                <label htmlFor={weeklyTimeInputId} className="block text-xs font-medium text-[var(--color-text-primary)] mb-1.5">
                  {t('backup.scheduleTime')}
                </label>
                <input
                  id={weeklyTimeInputId}
                  type="time"
                  value={scheduleConfig.time}
                  onChange={(e) => setTime(e.target.value)}
                  className="ui-input w-full px-3 py-2 text-sm font-mono"
                />
              </div>
            </div>
          )}

          {/* MONTHLY MODE */}
          {scheduleConfig.mode === 'monthly' && (
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 items-start">
              <div className="space-y-1.5">
                <label className="block text-xs font-medium text-[var(--color-text-primary)]">
                  {t('backup.scheduleDayOfMonth')}
                </label>
                <select
                  value={scheduleConfig.dayOfMonth}
                  onChange={(e) => setDayOfMonth(parseInt(e.target.value, 10))}
                  className="ui-select w-full px-3 py-2 text-sm"
                >
                  {Array.from({ length: 31 }, (_, i) => i + 1).map((day) => (
                    <option key={day} value={day}>
                      {day}.
                    </option>
                  ))}
                </select>
              </div>
              <div className="space-y-1.5">
                <label htmlFor={monthlyTimeInputId} className="block text-xs font-medium text-[var(--color-text-primary)]">
                  {t('backup.scheduleTime')}
                </label>
                <input
                  id={monthlyTimeInputId}
                  type="time"
                  value={scheduleConfig.time}
                  onChange={(e) => setTime(e.target.value)}
                  className="ui-input w-full px-3 py-2 text-sm font-mono"
                />
              </div>
            </div>
          )}

          {/* HOURLY / INTERVAL MODE */}
          {scheduleConfig.mode === 'hourly' && (
            <div className="space-y-2 max-w-sm">
              <label className="block text-xs font-medium text-[var(--color-text-primary)]">
                {t('backup.scheduleInterval')}
              </label>
              <select
                value={scheduleConfig.intervalHours}
                onChange={(e) => setIntervalHours(parseInt(e.target.value, 10))}
                className="ui-select w-full px-3 py-2 text-sm"
              >
                {INTERVAL_HOURS_OPTIONS.map((hours) => (
                  <option key={hours} value={hours}>
                    {t(`backup.hours${hours as 1 | 2 | 4 | 6 | 8 | 12}`)}
                  </option>
                ))}
              </select>
            </div>
          )}

          {/* CUSTOM / CRON EXPERT MODE */}
          {scheduleConfig.mode === 'custom' && (
            <div className="space-y-2">
              <label htmlFor={customCronId} className="block text-xs font-medium text-[var(--color-text-primary)]">
                {t('backup.cron')}
              </label>
              <input
                id={customCronId}
                value={cronExpression}
                onChange={(e) => setCustomCron(e.target.value)}
                placeholder="0 2 * * *"
                className={`ui-input w-full px-3 py-2 text-sm font-mono ${
                  !customValid ? 'border-[var(--color-error-text)] ring-1 ring-[var(--color-error-text)]' : ''
                }`}
                aria-describedby={`${customCronId}-hint`}
              />
              <p id={`${customCronId}-hint`} className="text-xs text-[var(--color-text-muted)]">
                {t('backup.cronHint')}
              </p>
            </div>
          )}
        </div>

        {/* Live Human-Readable Summary Banner */}
        <div className="mt-4 pt-3 border-t border-[var(--color-border-light)] flex items-center justify-between gap-3 text-xs bg-[var(--color-bg-tertiary)] p-3 rounded-lg">
          <div className="flex items-center gap-2 text-[var(--color-text-primary)]">
            <ClockIcon className="size-4 text-[var(--color-text-muted)] shrink-0" />
            <span>
              <strong className="font-semibold">{t('backup.scheduleSummary')}:</strong>{' '}
              {customValid ? humanReadableDescription : (
                <span className="text-[var(--color-error-text)] font-semibold">
                  {t('backup.invalidCron')}
                </span>
              )}
            </span>
          </div>
          <span className="shrink-0 text-[11px] font-mono px-2 py-0.5 rounded bg-[var(--color-bg-primary)] text-[var(--color-text-secondary)] border border-[var(--color-border)]">
            {timezone}
          </span>
        </div>
      </div>

      {/* Timezone, Retention & Threads */}
      <div className="grid grid-cols-1 gap-5 md:grid-cols-3">
        {/* Timezone */}
        <div className="space-y-2">
          <label htmlFor={timezoneId} className="block text-xs font-medium text-[var(--color-text-primary)]">
            {t('backup.timezone')}
          </label>
          <input
            id={timezoneId}
            list={timezoneListId}
            value={timezone}
            onChange={(event) => setTimezone(event.target.value)}
            className="ui-input w-full px-3 py-2 text-sm"
            placeholder="Europe/Berlin"
            aria-describedby={`${timezoneId}-hint`}
          />
          <datalist id={timezoneListId}>
            {COMMON_TIMEZONES.map((tz) => (
              <option key={tz} value={tz} />
            ))}
          </datalist>
          <p id={`${timezoneId}-hint`} className="text-xs text-[var(--color-text-muted)]">
            {t('backup.timezoneHint')}
          </p>
        </div>

        {/* Retention count */}
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
            className="ui-input w-full px-3 py-2 text-sm font-mono"
          />
          <p className="text-xs text-[var(--color-text-muted)]">{t('backup.retentionHint')}</p>
        </div>

        {/* Threads */}
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
            className="ui-input w-full px-3 py-2 text-sm font-mono"
          />
          <p className="text-xs text-[var(--color-text-muted)]">{t('backup.threadsHint')}</p>
        </div>
      </div>

      {error && <p role="alert" className="ui-alert ui-alert-error p-3 text-sm flex items-center gap-2"><ExclamationTriangleIcon className="size-4 shrink-0" />{error}</p>}
    </div>
  );
}
