import { act, useEffect } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ToastProvider } from './ToastContext';
import { useToast } from './useToast';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const captured = { toast: undefined as ReturnType<typeof useToast> | undefined };

function ToastCapture() {
  const toast = useToast();
  useEffect(() => {
    captured.toast = toast;
  }, [toast]);
  return null;
}

describe('ToastProvider', () => {
  let container: HTMLDivElement;
  let root: Root;

  const renderProvider = () => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<ToastProvider><ToastCapture /></ToastProvider>));
  };

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.useRealTimers();
  });

  it('uses an informational default and retains at most five current toasts', () => {
    vi.useFakeTimers();
    renderProvider();

    act(() => {
      for (let index = 1; index <= 6; index += 1) {
        captured.toast!(`toast ${index}`);
      }
    });

    const toasts = container.querySelectorAll('.ui-alert');
    expect(toasts).toHaveLength(5);
    expect(container.textContent).not.toContain('toast 1');
    expect(container.textContent).toContain('toast 6');
    expect(toasts[0]?.classList.contains('ui-alert-info')).toBe(true);
    expect(toasts[0]?.getAttribute('role')).toBe('status');
    expect(container.querySelector('[aria-live]')).toBeNull();
    expect(vi.getTimerCount()).toBe(5);
  });

  it('automatically dismisses toasts and clears their timer', () => {
    vi.useFakeTimers();
    renderProvider();

    act(() => captured.toast!('expires', 'error'));
    expect(container.textContent).toContain('expires');
    expect(vi.getTimerCount()).toBe(1);

    act(() => vi.advanceTimersByTime(4500));

    expect(container.textContent).not.toContain('expires');
    expect(vi.getTimerCount()).toBe(0);
  });

  it('clears pending timers when the provider unmounts', () => {
    vi.useFakeTimers();
    renderProvider();

    act(() => captured.toast!('pending'));
    expect(vi.getTimerCount()).toBe(1);

    act(() => root.unmount());

    expect(vi.getTimerCount()).toBe(0);
  });
});
