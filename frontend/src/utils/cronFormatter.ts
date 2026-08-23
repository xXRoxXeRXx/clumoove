export type ScheduleMode = 'daily' | 'weekly' | 'monthly' | 'hourly' | 'custom';

export interface ScheduleConfig {
  mode: ScheduleMode;
  time: string; // "HH:MM", e.g. "02:00"
  dayOfWeek: number; // 0=Sun, 1=Mon, 2=Tue, 3=Wed, 4=Thu, 5=Fri, 6=Sat
  dayOfMonth: number; // 1-31
  intervalHours: number; // 1, 2, 4, 6, 8, 12
  customCron: string;
}

export type TFunc = (key: string, options?: Record<string, unknown>) => string;

const pad2 = (num: number): string => num.toString().padStart(2, '0');

const parseTimeStr = (timeStr: string): { hour: number; minute: number } => {
  const parts = (timeStr || '02:00').split(':');
  const hour = Math.max(0, Math.min(23, parseInt(parts[0] || '2', 10) || 0));
  const minute = Math.max(0, Math.min(59, parseInt(parts[1] || '0', 10) || 0));
  return { hour, minute };
};

export const isValidCron = (expr: string): boolean => {
  if (!expr || typeof expr !== 'string') return false;
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return false;

  const [min, hour, dom, month, dow] = parts;

  const isValidPart = (part: string, minVal: number, maxVal: number): boolean => {
    if (part === '*') return true;
    if (/^\*\/\d+$/.test(part)) {
      const step = parseInt(part.slice(2), 10);
      return step > 0 && step <= maxVal;
    }
    if (/^\d+$/.test(part)) {
      const val = parseInt(part, 10);
      return val >= minVal && val <= maxVal;
    }
    if (/^\d+-\d+$/.test(part)) {
      const [start, end] = part.split('-').map((v) => parseInt(v, 10));
      return start >= minVal && end <= maxVal && start <= end;
    }
    if (/^\d+(,\d+)+$/.test(part)) {
      return part.split(',').every((v) => {
        const val = parseInt(v, 10);
        return !isNaN(val) && val >= minVal && val <= maxVal;
      });
    }
    return false;
  };

  return (
    isValidPart(min, 0, 59) &&
    isValidPart(hour, 0, 23) &&
    isValidPart(dom, 1, 31) &&
    isValidPart(month, 1, 12) &&
    isValidPart(dow, 0, 7)
  );
};

export const parseCronToScheduleConfig = (expr: string): ScheduleConfig => {
  const fallback: ScheduleConfig = {
    mode: 'daily',
    time: '02:00',
    dayOfWeek: 0,
    dayOfMonth: 1,
    intervalHours: 6,
    customCron: expr || '0 2 * * *',
  };

  if (!expr || typeof expr !== 'string') return fallback;
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) {
    return { ...fallback, mode: 'custom', customCron: expr };
  }

  const [min, hour, dom, month, dow] = parts;

  // Check hourly pattern: 0 * * * * or 0 */X * * *
  if (min === '0' && dom === '*' && month === '*' && dow === '*') {
    if (hour === '*') {
      return { ...fallback, mode: 'hourly', intervalHours: 1, customCron: expr };
    }
    if (/^\*\/\d+$/.test(hour)) {
      const step = parseInt(hour.slice(2), 10);
      if (step > 0 && step <= 24) {
        return { ...fallback, mode: 'hourly', intervalHours: step, customCron: expr };
      }
    }
  }

  const isNumeric = (s: string) => /^\d+$/.test(s);

  // Check daily pattern: M H * * *
  if (isNumeric(min) && isNumeric(hour) && dom === '*' && month === '*' && dow === '*') {
    const m = parseInt(min, 10);
    const h = parseInt(hour, 10);
    if (m >= 0 && m <= 59 && h >= 0 && h <= 23) {
      return {
        ...fallback,
        mode: 'daily',
        time: `${pad2(h)}:${pad2(m)}`,
        customCron: expr,
      };
    }
  }

  // Check weekly pattern: M H * * DOW
  if (isNumeric(min) && isNumeric(hour) && dom === '*' && month === '*' && isNumeric(dow)) {
    const m = parseInt(min, 10);
    const h = parseInt(hour, 10);
    let d = parseInt(dow, 10);
    if (d === 7) d = 0; // 7 is Sunday in cron
    if (m >= 0 && m <= 59 && h >= 0 && h <= 23 && d >= 0 && d <= 6) {
      return {
        ...fallback,
        mode: 'weekly',
        dayOfWeek: d,
        time: `${pad2(h)}:${pad2(m)}`,
        customCron: expr,
      };
    }
  }

  // Check monthly pattern: M H DOM * *
  if (isNumeric(min) && isNumeric(hour) && isNumeric(dom) && month === '*' && dow === '*') {
    const m = parseInt(min, 10);
    const h = parseInt(hour, 10);
    const day = parseInt(dom, 10);
    if (m >= 0 && m <= 59 && h >= 0 && h <= 23 && day >= 1 && day <= 31) {
      return {
        ...fallback,
        mode: 'monthly',
        dayOfMonth: day,
        time: `${pad2(h)}:${pad2(m)}`,
        customCron: expr,
      };
    }
  }

  return { ...fallback, mode: 'custom', customCron: expr };
};

export const buildCronFromScheduleConfig = (config: ScheduleConfig): string => {
  const { hour, minute } = parseTimeStr(config.time);

  switch (config.mode) {
    case 'daily':
      return `${minute} ${hour} * * *`;
    case 'weekly': {
      const dow = typeof config.dayOfWeek === 'number' && config.dayOfWeek >= 0 && config.dayOfWeek <= 6
        ? config.dayOfWeek
        : 0;
      return `${minute} ${hour} * * ${dow}`;
    }
    case 'monthly': {
      const dom = typeof config.dayOfMonth === 'number' && config.dayOfMonth >= 1 && config.dayOfMonth <= 31
        ? config.dayOfMonth
        : 1;
      return `${minute} ${hour} ${dom} * *`;
    }
    case 'hourly': {
      const interval = Math.max(1, Math.min(24, config.intervalHours || 6));
      return interval === 1 ? '0 * * * *' : `0 */${interval} * * *`;
    }
    case 'custom':
    default:
      return config.customCron?.trim() || '0 2 * * *';
  }
};

export const formatCronHuman = (expr: string, t: TFunc): string => {
  if (!expr || typeof expr !== 'string') return '';
  const trimmed = expr.trim();
  if (!isValidCron(trimmed)) {
    return t('backup.descCustom', { cron: trimmed });
  }

  const parts = trimmed.split(/\s+/);
  const [min, hour, dom, month, dow] = parts;
  const isNumeric = (s: string) => /^\d+$/.test(s);

  // Hourly / interval
  if (min === '0' && dom === '*' && month === '*' && dow === '*') {
    if (hour === '*') {
      return t('backup.descHourly');
    }
    if (/^\*\/\d+$/.test(hour)) {
      const interval = parseInt(hour.slice(2), 10);
      return t('backup.descIntervalHours', { count: interval });
    }
  }

  // Every X minutes
  if (/^\*\/\d+$/.test(min) && hour === '*' && dom === '*' && month === '*' && dow === '*') {
    const interval = parseInt(min.slice(2), 10);
    return t('backup.descIntervalMinutes', { count: interval });
  }

  // Daily
  if (isNumeric(min) && isNumeric(hour) && dom === '*' && month === '*' && dow === '*') {
    const time = `${pad2(parseInt(hour, 10))}:${pad2(parseInt(min, 10))}`;
    return t('backup.descDaily', { time });
  }

  // Weekly
  if (isNumeric(min) && isNumeric(hour) && dom === '*' && month === '*' && isNumeric(dow)) {
    let d = parseInt(dow, 10);
    if (d === 7) d = 0;
    const time = `${pad2(parseInt(hour, 10))}:${pad2(parseInt(min, 10))}`;
    const dayName = t(`backup.weekdays.${d}`);
    return t('backup.descWeekly', { day: dayName, time });
  }

  // Monthly
  if (isNumeric(min) && isNumeric(hour) && isNumeric(dom) && month === '*' && dow === '*') {
    const time = `${pad2(parseInt(hour, 10))}:${pad2(parseInt(min, 10))}`;
    return t('backup.descMonthly', { day: dom, time });
  }

  return t('backup.descCustom', { cron: trimmed });
};
