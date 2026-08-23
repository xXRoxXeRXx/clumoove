import { describe, it, expect } from 'vitest';
import {
  isValidCron,
  parseCronToScheduleConfig,
  buildCronFromScheduleConfig,
  formatCronHuman,
  type ScheduleConfig,
} from './cronFormatter';

// Mock translation function
const mockT = (key: string, options?: Record<string, unknown>): string => {
  const translations: Record<string, string> = {
    'backup.descHourly': 'Every hour',
    'backup.descIntervalHours': `Every ${options?.count} hours`,
    'backup.descIntervalMinutes': `Every ${options?.count} minutes`,
    'backup.descDaily': `Daily at ${options?.time}`,
    'backup.descWeekly': `Every ${options?.day} at ${options?.time}`,
    'backup.descMonthly': `Monthly on day ${options?.day} at ${options?.time}`,
    'backup.descCustom': `Custom (${options?.cron})`,
    'backup.weekdays.0': 'Sunday',
    'backup.weekdays.1': 'Monday',
    'backup.weekdays.2': 'Tuesday',
    'backup.weekdays.3': 'Wednesday',
    'backup.weekdays.4': 'Thursday',
    'backup.weekdays.5': 'Friday',
    'backup.weekdays.6': 'Saturday',
  };
  return translations[key] || key;
};

describe('cronFormatter', () => {
  describe('isValidCron', () => {
    it('validates 5-part cron expressions correctly', () => {
      expect(isValidCron('0 2 * * *')).toBe(true);
      expect(isValidCron('30 14 * * *')).toBe(true);
      expect(isValidCron('0 3 * * 0')).toBe(true);
      expect(isValidCron('0 3 * * 7')).toBe(true);
      expect(isValidCron('0 2 1 * *')).toBe(true);
      expect(isValidCron('0 */6 * * *')).toBe(true);
      expect(isValidCron('0 * * * *')).toBe(true);
      expect(isValidCron('*/15 * * * *')).toBe(true);
      expect(isValidCron('0 0 1,15 * *')).toBe(true);
    });

    it('rejects invalid cron expressions', () => {
      expect(isValidCron('')).toBe(false);
      expect(isValidCron('invalid')).toBe(false);
      expect(isValidCron('0 2 * *')).toBe(false); // 4 parts
      expect(isValidCron('0 2 * * * *')).toBe(false); // 6 parts
      expect(isValidCron('60 2 * * *')).toBe(false); // minute > 59
      expect(isValidCron('0 24 * * *')).toBe(false); // hour > 23
      expect(isValidCron('0 2 32 * *')).toBe(false); // dom > 31
      expect(isValidCron('0 2 0 * *')).toBe(false); // dom < 1
    });
  });

  describe('parseCronToScheduleConfig', () => {
    it('parses daily expressions', () => {
      const config = parseCronToScheduleConfig('0 2 * * *');
      expect(config.mode).toBe('daily');
      expect(config.time).toBe('02:00');

      const config2 = parseCronToScheduleConfig('30 14 * * *');
      expect(config2.mode).toBe('daily');
      expect(config2.time).toBe('14:30');
    });

    it('parses weekly expressions', () => {
      const configSun = parseCronToScheduleConfig('0 3 * * 0');
      expect(configSun.mode).toBe('weekly');
      expect(configSun.dayOfWeek).toBe(0);
      expect(configSun.time).toBe('03:00');

      const configSun7 = parseCronToScheduleConfig('0 3 * * 7');
      expect(configSun7.mode).toBe('weekly');
      expect(configSun7.dayOfWeek).toBe(0);

      const configFri = parseCronToScheduleConfig('15 22 * * 5');
      expect(configFri.mode).toBe('weekly');
      expect(configFri.dayOfWeek).toBe(5);
      expect(configFri.time).toBe('22:15');
    });

    it('parses monthly expressions', () => {
      const config1 = parseCronToScheduleConfig('0 2 1 * *');
      expect(config1.mode).toBe('monthly');
      expect(config1.dayOfMonth).toBe(1);
      expect(config1.time).toBe('02:00');

      const config15 = parseCronToScheduleConfig('45 4 15 * *');
      expect(config15.mode).toBe('monthly');
      expect(config15.dayOfMonth).toBe(15);
      expect(config15.time).toBe('04:45');
    });

    it('parses hourly intervals', () => {
      const config1h = parseCronToScheduleConfig('0 * * * *');
      expect(config1h.mode).toBe('hourly');
      expect(config1h.intervalHours).toBe(1);

      const config6h = parseCronToScheduleConfig('0 */6 * * *');
      expect(config6h.mode).toBe('hourly');
      expect(config6h.intervalHours).toBe(6);
    });

    it('falls back to custom mode for complex or non-standard expressions', () => {
      const custom = parseCronToScheduleConfig('0 0 1,15 * *');
      expect(custom.mode).toBe('custom');
      expect(custom.customCron).toBe('0 0 1,15 * *');
    });
  });

  describe('buildCronFromScheduleConfig', () => {
    it('builds daily cron', () => {
      const config: ScheduleConfig = {
        mode: 'daily',
        time: '02:00',
        dayOfWeek: 0,
        dayOfMonth: 1,
        intervalHours: 6,
        customCron: '',
      };
      expect(buildCronFromScheduleConfig(config)).toBe('0 2 * * *');
    });

    it('builds weekly cron', () => {
      const config: ScheduleConfig = {
        mode: 'weekly',
        time: '03:15',
        dayOfWeek: 5, // Friday
        dayOfMonth: 1,
        intervalHours: 6,
        customCron: '',
      };
      expect(buildCronFromScheduleConfig(config)).toBe('15 3 * * 5');
    });

    it('builds monthly cron', () => {
      const config: ScheduleConfig = {
        mode: 'monthly',
        time: '04:30',
        dayOfWeek: 0,
        dayOfMonth: 15,
        intervalHours: 6,
        customCron: '',
      };
      expect(buildCronFromScheduleConfig(config)).toBe('30 4 15 * *');
    });

    it('builds hourly interval cron', () => {
      const config1: ScheduleConfig = {
        mode: 'hourly',
        time: '00:00',
        dayOfWeek: 0,
        dayOfMonth: 1,
        intervalHours: 1,
        customCron: '',
      };
      expect(buildCronFromScheduleConfig(config1)).toBe('0 * * * *');

      const config6: ScheduleConfig = {
        mode: 'hourly',
        time: '00:00',
        dayOfWeek: 0,
        dayOfMonth: 1,
        intervalHours: 6,
        customCron: '',
      };
      expect(buildCronFromScheduleConfig(config6)).toBe('0 */6 * * *');
    });

    it('builds custom cron', () => {
      const config: ScheduleConfig = {
        mode: 'custom',
        time: '00:00',
        dayOfWeek: 0,
        dayOfMonth: 1,
        intervalHours: 6,
        customCron: '*/10 * * * *',
      };
      expect(buildCronFromScheduleConfig(config)).toBe('*/10 * * * *');
    });
  });

  describe('formatCronHuman', () => {
    it('formats human readable descriptions correctly', () => {
      expect(formatCronHuman('0 2 * * *', mockT)).toBe('Daily at 02:00');
      expect(formatCronHuman('0 3 * * 0', mockT)).toBe('Every Sunday at 03:00');
      expect(formatCronHuman('15 22 * * 1', mockT)).toBe('Every Monday at 22:15');
      expect(formatCronHuman('0 2 1 * *', mockT)).toBe('Monthly on day 1 at 02:00');
      expect(formatCronHuman('0 * * * *', mockT)).toBe('Every hour');
      expect(formatCronHuman('0 */6 * * *', mockT)).toBe('Every 6 hours');
      expect(formatCronHuman('*/15 * * * *', mockT)).toBe('Every 15 minutes');
      expect(formatCronHuman('0 0 1,15 * *', mockT)).toBe('Custom (0 0 1,15 * *)');
    });
  });
});
