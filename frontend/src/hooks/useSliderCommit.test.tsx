import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useSliderCommit } from './useSliderCommit';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function Harness({ onCommit, interactingRef }: { onCommit: (value: number) => void; interactingRef: { current: boolean } }) {
  const handlers = useSliderCommit(interactingRef, onCommit);
  return <input type="range" min={0} max={16} defaultValue={4} {...handlers} />;
}

describe('useSliderCommit', () => {
  let container: HTMLDivElement;
  let root: Root;
  let input: HTMLInputElement;
  let onCommit: ReturnType<typeof vi.fn<(value: number) => void>>;
  let interacting: { current: boolean };

  const fire = (type: string, init: KeyboardEventInit = {}) => {
    act(() => {
      const EventCtor = type.startsWith('key') ? KeyboardEvent : Event;
      input.dispatchEvent(new EventCtor(type, { bubbles: type !== 'blur' && type !== 'lostpointercapture', ...init }));
    });
  };
  const focusOut = () => {
    act(() => {
      input.dispatchEvent(new FocusEvent('focusout', { bubbles: true }));
    });
  };
  const setValue = (value: number) => {
    input.value = String(value);
  };

  beforeEach(() => {
    onCommit = vi.fn<(value: number) => void>();
    interacting = { current: false };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root.render(<Harness onCommit={onCommit} interactingRef={interacting} />);
    });
    input = container.querySelector('input')!;
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('commits a changed value on pointer-up', () => {
    fire('pointerdown');
    expect(interacting.current).toBe(true);
    setValue(9);
    fire('pointerup');
    expect(onCommit).toHaveBeenCalledExactlyOnceWith(9);
    expect(interacting.current).toBe(false);
  });

  it('commits when the gesture is cancelled instead of released', () => {
    fire('pointerdown');
    setValue(7);
    fire('pointercancel');
    expect(onCommit).toHaveBeenCalledExactlyOnceWith(7);
    expect(interacting.current).toBe(false);
  });

  it('commits once when capture is lost after the pointer is released', () => {
    fire('pointerdown');
    setValue(12);
    fire('pointerup');
    fire('lostpointercapture');
    expect(onCommit).toHaveBeenCalledOnce();
  });

  it('commits and releases the server-sync guard when focus leaves mid-interaction', () => {
    fire('keydown', { key: 'ArrowRight' });
    setValue(5);
    focusOut();
    expect(onCommit).toHaveBeenCalledExactlyOnceWith(5);
    expect(interacting.current).toBe(false);
  });

  it('does not send a request when the value did not change', () => {
    fire('pointerdown');
    fire('pointerup');
    fire('keydown', { key: 'ArrowRight' });
    fire('keyup', { key: 'ArrowRight' });
    expect(onCommit).not.toHaveBeenCalled();
  });

  it('ignores non-adjusting keys such as Tab', () => {
    fire('keyup', { key: 'Tab' });
    fire('keydown', { key: 'Shift' });
    expect(interacting.current).toBe(false);
    expect(onCommit).not.toHaveBeenCalled();
  });

  it('commits a keyboard change once, even while the key repeats', () => {
    fire('keydown', { key: 'ArrowRight' });
    setValue(5);
    fire('keydown', { key: 'ArrowRight', repeat: true });
    setValue(6);
    fire('keyup', { key: 'ArrowRight' });
    expect(onCommit).toHaveBeenCalledExactlyOnceWith(6);
  });
});
