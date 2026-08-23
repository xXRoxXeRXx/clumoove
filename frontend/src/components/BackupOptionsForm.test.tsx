import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { BackupOptionsForm } from './BackupOptionsForm';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('BackupOptionsForm', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(async () => {
    await i18n.changeLanguage('de');
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
  });

  interface RenderProps {
    cronExpression?: string;
    setCronExpression?: (cron: string) => void;
    timezone?: string;
    setTimezone?: (tz: string) => void;
    retentionCount?: number;
    setRetentionCount?: (count: number) => void;
    threads?: number;
    setThreads?: (threads: number) => void;
    error?: string | null;
  }

  async function renderForm(props: RenderProps = {}) {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);

    const defaultProps = {
      cronExpression: '0 2 * * *',
      setCronExpression: vi.fn(),
      timezone: 'Europe/Berlin',
      setTimezone: vi.fn(),
      retentionCount: 7,
      setRetentionCount: vi.fn(),
      threads: 4,
      setThreads: vi.fn(),
      error: null,
      ...props,
    };

    await act(async () => {
      root.render(<BackupOptionsForm {...defaultProps} />);
    });

    return defaultProps;
  }

  function setInputValue(input: HTMLInputElement | HTMLSelectElement, value: string): void {
    const proto = input instanceof HTMLInputElement ? HTMLInputElement.prototype : HTMLSelectElement.prototype;
    const valueSetter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
    valueSetter?.call(input, value);
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.dispatchEvent(new Event('change', { bubbles: true }));
  }

  it('renders mode pills and defaults to daily mode for 0 2 * * *', async () => {
    await renderForm({ cronExpression: '0 2 * * *' });

    // Mode buttons
    const dailyBtn = container.querySelector<HTMLButtonElement>('button[aria-pressed="true"]');
    expect(dailyBtn).not.toBeNull();
    expect(dailyBtn?.textContent).toContain('Täglich');

    // Live summary shows readable text
    expect(container.textContent).toContain('Täglich um 02:00 Uhr');
    expect(container.textContent).toContain('Europe/Berlin');
  });

  it('switches to weekly mode and updates cronExpression when weekday is selected', async () => {
    const setCronExpression = vi.fn();
    await renderForm({ cronExpression: '0 2 * * *', setCronExpression });

    // Click "Wöchentlich"
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const weeklyBtn = buttons.find((b) => b.textContent?.includes('Wöchentlich'));
    expect(weeklyBtn).toBeDefined();

    await act(async () => {
      weeklyBtn?.click();
    });

    // Cron expression updated to weekly (Sunday default = 0)
    expect(setCronExpression).toHaveBeenCalledWith('0 2 * * 0');

    // Click "Montag"
    const weekdayButtons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const mondayBtn = weekdayButtons.find((b) => b.textContent?.trim() === 'Montag');
    expect(mondayBtn).toBeDefined();

    await act(async () => {
      mondayBtn?.click();
    });

    expect(setCronExpression).toHaveBeenCalledWith('0 2 * * 1');
  });

  it('switches to monthly mode and updates day of month', async () => {
    const setCronExpression = vi.fn();
    await renderForm({ cronExpression: '0 2 * * *', setCronExpression });

    // Click "Monatlich"
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const monthlyBtn = buttons.find((b) => b.textContent?.includes('Monatlich'));
    expect(monthlyBtn).toBeDefined();

    await act(async () => {
      monthlyBtn?.click();
    });

    expect(setCronExpression).toHaveBeenCalledWith('0 2 1 * *');

    // Change day of month
    const select = container.querySelector<HTMLSelectElement>('select');
    expect(select).not.toBeNull();

    await act(async () => {
      setInputValue(select!, '15');
    });

    expect(setCronExpression).toHaveBeenCalledWith('0 2 15 * *');
  });

  it('switches to hourly interval mode and updates hours', async () => {
    const setCronExpression = vi.fn();
    await renderForm({ cronExpression: '0 2 * * *', setCronExpression });

    // Click "Intervall"
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const hourlyBtn = buttons.find((b) => b.textContent?.includes('Intervall'));
    expect(hourlyBtn).toBeDefined();

    await act(async () => {
      hourlyBtn?.click();
    });

    expect(setCronExpression).toHaveBeenCalledWith('0 */6 * * *');

    const select = container.querySelector<HTMLSelectElement>('select');
    expect(select).not.toBeNull();

    await act(async () => {
      setInputValue(select!, '1');
    });

    expect(setCronExpression).toHaveBeenCalledWith('0 * * * *');
  });

  it('switches to custom expert mode and handles raw input', async () => {
    const setCronExpression = vi.fn();
    await renderForm({ cronExpression: '0 2 * * *', setCronExpression });

    // Click "Experte (Cron)"
    const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button'));
    const customBtn = buttons.find((b) => b.textContent?.includes('Experte'));
    expect(customBtn).toBeDefined();

    await act(async () => {
      customBtn?.click();
    });

    const customInput = container.querySelector<HTMLInputElement>('input[placeholder="0 2 * * *"]');
    expect(customInput).not.toBeNull();

    await act(async () => {
      setInputValue(customInput!, '*/15 * * * *');
    });

    expect(setCronExpression).toHaveBeenCalledWith('*/15 * * * *');
  });

  it('updates timezone, retention and threads inputs', async () => {
    const setTimezone = vi.fn();
    const setRetentionCount = vi.fn();
    const setThreads = vi.fn();

    await renderForm({
      setTimezone,
      setRetentionCount,
      setThreads,
    });

    const timezoneInput = container.querySelector<HTMLInputElement>('input[placeholder="Europe/Berlin"]');
    expect(timezoneInput).not.toBeNull();

    await act(async () => {
      setInputValue(timezoneInput!, 'UTC');
    });
    expect(setTimezone).toHaveBeenCalledWith('UTC');

    const numberInputs = container.querySelectorAll<HTMLInputElement>('input[type="number"]');
    const retentionInput = numberInputs[0];
    const threadsInput = numberInputs[1];

    await act(async () => {
      setInputValue(retentionInput, '14');
      setInputValue(threadsInput, '8');
    });

    expect(setRetentionCount).toHaveBeenCalledWith(14);
    expect(setThreads).toHaveBeenCalledWith(8);
  });
});
