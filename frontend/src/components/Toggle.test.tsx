import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Toggle } from './Toggle';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

describe('Toggle component', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => {
      root.unmount();
    });
    container.remove();
  });

  it('renders switch with proper accessibility attributes and unchecked state', () => {
    act(() => {
      root.render(<Toggle checked={false} onChange={vi.fn()} label="Enable feature" />);
    });

    const input = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.checked).toBe(false);
    expect(input.getAttribute('role')).toBe('switch');
    expect(input.getAttribute('aria-checked')).toBe('false');
    expect(container.textContent).toContain('Enable feature');

    const thumb = container.querySelector('span[aria-hidden="true"] + span[aria-hidden="true"]');
    expect(thumb?.classList.contains('translate-x-0')).toBe(true);
  });

  it('reflects checked state properly', () => {
    act(() => {
      root.render(<Toggle checked={true} onChange={vi.fn()} label="Enable feature" />);
    });

    const input = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(input).toBeTruthy();
    expect(input.checked).toBe(true);
    expect(input.getAttribute('aria-checked')).toBe('true');

    const thumb = container.querySelector('span[aria-hidden="true"] + span[aria-hidden="true"]');
    expect(thumb?.classList.contains('translate-x-4')).toBe(true);
  });

  it('calls onChange when clicked', () => {
    const onChange = vi.fn();
    act(() => {
      root.render(<Toggle checked={false} onChange={onChange} label="Enable feature" />);
    });

    const input = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    act(() => {
      input.click();
    });

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it('supports visually hiding the label for screen readers only', () => {
    act(() => {
      root.render(<Toggle checked={false} onChange={vi.fn()} label="Enable feature" hideLabel />);
    });

    const labelSpan = container.querySelector('span.sr-only');
    expect(labelSpan).toBeTruthy();
    expect(labelSpan?.textContent).toBe('Enable feature');
  });

  it('disables input when disabled prop is true', () => {
    const onChange = vi.fn();
    act(() => {
      root.render(<Toggle checked={false} disabled={true} onChange={onChange} label="Enable feature" />);
    });

    const input = container.querySelector('input[type="checkbox"]') as HTMLInputElement;
    expect(input.disabled).toBe(true);
  });
});
