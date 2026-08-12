import { useTranslation } from 'react-i18next';

const intlLocale = (lng?: string): string => {
  if (lng === 'de') return 'de-DE';
  if (lng === 'en') return 'en-US';
  return lng || 'en-US';
};

export const formatBytes = (bytes: number, lng?: string): string => {
  if (!bytes || bytes <= 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(k)), sizes.length - 1);
  const value = bytes / Math.pow(k, i);
  const formatted = new Intl.NumberFormat(intlLocale(lng), {
    maximumFractionDigits: 1,
  }).format(value);
  return `${formatted} ${sizes[i]}`;
};

export const formatDate = (iso: string, lng?: string): string => {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  return new Intl.DateTimeFormat(intlLocale(lng), {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
  }).format(d);
};

export const formatDateTime = (iso: string, lng?: string): string => {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  return new Intl.DateTimeFormat(intlLocale(lng), {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(d);
};

export type TFunc = (key: string, options?: Record<string, unknown>) => string;

export const formatDuration = (seconds: number, t: TFunc): string => {
  if (seconds === Infinity || isNaN(seconds)) return t('dashboard.eta.computing');
  if (seconds < 60) return t('format.duration.seconds', { count: Math.round(seconds) });
  const mins = Math.floor(seconds / 60);
  const secs = Math.round(seconds % 60);
  if (mins < 60) {
    return secs === 0
      ? t('format.duration.minutes', { count: mins })
      : `${t('format.duration.minutes', { count: mins })} ${t('format.duration.seconds', { count: secs })}`;
  }
  const hrs = Math.floor(mins / 60);
  const remMins = mins % 60;
  return remMins === 0
    ? t('format.duration.hours', { count: hrs })
    : `${t('format.duration.hours', { count: hrs })} ${t('format.duration.minutes', { count: remMins })}`;
};

export const formatNumber = (value: number, lng?: string): string =>
  new Intl.NumberFormat(intlLocale(lng)).format(value);

export const formatPercent = (value: number, lng?: string): string =>
  new Intl.NumberFormat(intlLocale(lng), { style: 'percent', maximumFractionDigits: 0 }).format(value / 100);

export const useFormat = () => {
  const { i18n } = useTranslation();
  const lng = i18n.language;
  return {
    lng,
    formatBytes: (bytes: number) => formatBytes(bytes, lng),
    formatNumber: (value: number) => formatNumber(value, lng),
    formatPercent: (value: number) => formatPercent(value, lng),
    formatDate: (iso: string) => formatDate(iso, lng),
    formatDateTime: (iso: string) => formatDateTime(iso, lng),
  };
};
