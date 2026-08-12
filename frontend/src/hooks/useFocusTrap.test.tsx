import { act, useRef } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useFocusTrap } from './useFocusTrap';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function FocusTrapHarness({ onEscape }: { onEscape: () => void }) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const initialFocusRef = useRef<HTMLButtonElement>(null);
  useFocusTrap(dialogRef, initialFocusRef, onEscape);

  return (
    <div ref={dialogRef} role="dialog" tabIndex={-1}>
      <button ref={initialFocusRef} type="button">Close</button>
      <input aria-label="Name" />
    </div>
  );
}

function EmptyFocusTrapHarness() {
  const dialogRef = useRef<HTMLDivElement>(null);
  const initialFocusRef = useRef<HTMLElement>(null);
  useFocusTrap(dialogRef, initialFocusRef, () => {});

  return <div ref={dialogRef} role="dialog" tabIndex={-1} />;
}

describe('useFocusTrap', () => {
  let container: HTMLDivElement;
  let root: Root | null;

  const render = (onEscape: () => void) => {
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root?.render(<FocusTrapHarness onEscape={onEscape} />));
  };

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    vi.useRealTimers();
  });

  it('keeps focus in the active field when its escape callback changes', () => {
    vi.useFakeTimers();
    const firstEscape = vi.fn();
    const latestEscape = vi.fn();
    render(firstEscape);
    act(() => vi.runAllTimers());

    const input = container.querySelector('input') as HTMLInputElement;
    input.focus();
    act(() => root?.render(<FocusTrapHarness onEscape={latestEscape} />));
    act(() => vi.runAllTimers());

    expect(document.activeElement).toBe(input);

    act(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })));
    expect(firstEscape).not.toHaveBeenCalled();
    expect(latestEscape).toHaveBeenCalledTimes(1);
  });

  it('focuses an empty dialog on Tab and restores its trigger on unmount', () => {
    vi.useFakeTimers();
    const trigger = document.createElement('button');
    document.body.append(trigger);
    trigger.focus();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<EmptyFocusTrapHarness />));
    act(() => vi.runAllTimers());

    const tab = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    act(() => document.dispatchEvent(tab));
    const dialog = container.querySelector('[role="dialog"]');
    expect(tab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(dialog);

    act(() => root?.unmount());
    root = null;
    expect(document.activeElement).toBe(trigger);
    trigger.remove();
  });
});
