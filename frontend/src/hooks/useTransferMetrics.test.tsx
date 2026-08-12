import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { useTransferMetrics } from './useTransferMetrics';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('useTransferMetrics', () => {
  let container: HTMLDivElement;
  let root: Root;
  let metrics: ReturnType<typeof useTransferMetrics>;

  function MetricsHarness() {
    metrics = useTransferMetrics();
    return <output data-eta={metrics.eta} data-speed={metrics.speed} />;
  }

  beforeEach(async () => {
    await i18n.changeLanguage('en');
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<MetricsHarness />));
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.restoreAllMocks();
  });

  it('shows a localized verification label instead of a stale transfer speed', async () => {
    act(() => metrics.updateMetrics({ status: 'VERIFYING' }));

    const output = container.querySelector('output');
    expect(output?.dataset).toMatchObject({ eta: 'Verifying integrity...', speed: '0' });

    await act(async () => {
      await i18n.changeLanguage('de');
    });
    expect(output?.dataset).toMatchObject({ eta: 'Integrität wird geprüft...', speed: '0' });
  });

  it('resets the speed window when byte progress moves backwards', () => {
    let now = 0;
    vi.spyOn(Date, 'now').mockImplementation(() => now);

    act(() => metrics.updateMetrics({ status: 'RUNNING', live_bytes: 100, total_bytes: 1000 }));
    now = 1000;
    act(() => metrics.updateMetrics({ status: 'RUNNING', live_bytes: 200, total_bytes: 1000 }));
    now = 2000;
    act(() => metrics.updateMetrics({ status: 'RUNNING', live_bytes: 0, total_bytes: 1000 }));

    const output = container.querySelector('output');
    expect(output?.dataset).toMatchObject({ eta: 'Calculating...', speed: '0' });
  });
});
