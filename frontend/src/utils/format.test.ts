import { describe, it, expect } from 'vitest';
import { formatBytes, formatDate, formatDateTime } from './format';

describe('formatBytes', () => {
  it('returns 0 B for zero/negative/invalid', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(-5)).toBe('0 B');
    expect(formatBytes(NaN)).toBe('0 B');
  });

  it('formats bytes', () => {
    expect(formatBytes(512)).toMatch(/512 B/);
  });

  it('formats kilobytes', () => {
    const out = formatBytes(1024);
    expect(out).toContain('KB');
    expect(out).toContain('1');
  });

  it('formats megabytes with one fraction digit', () => {
    const out = formatBytes(1024 * 1024 * 1.5);
    expect(out).toContain('MB');
    expect(out).toMatch(/1\.5/);
  });

  it('formats gigabytes', () => {
    const out = formatBytes(1024 * 1024 * 1024);
    expect(out).toContain('GB');
  });

  it('respects locale for decimal separator', () => {
    const de = formatBytes(1024 * 1024 * 1.5, 'de');
    const en = formatBytes(1024 * 1024 * 1.5, 'en');
    expect(en).toContain('.');
    expect(de).toContain(',');
  });
});

describe('formatDate', () => {
  it('returns empty string for invalid input', () => {
    expect(formatDate('not-a-date')).toBe('');
    expect(formatDate('')).toBe('');
  });

  it('formats a valid ISO date in de', () => {
    const out = formatDate('2024-03-05T12:00:00Z', 'de');
    expect(out).toContain('03');
    expect(out).toContain('2024');
  });

  it('formats a valid ISO date in en', () => {
    const out = formatDate('2024-03-05T12:00:00Z', 'en');
    expect(out).toContain('2024');
  });
});

describe('formatDateTime', () => {
  it('returns empty string for invalid input', () => {
    expect(formatDateTime('garbage')).toBe('');
  });

  it('includes date and time components', () => {
    const out = formatDateTime('2024-03-05T14:30:00Z', 'en');
    expect(out).toContain('2024');
    expect(out).toMatch(/14|02/); // hour present (locale-dependent 12/24h)
  });
});
