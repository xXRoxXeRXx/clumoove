import { useEffect, useRef, type FocusEvent, type KeyboardEvent, type PointerEvent, type RefObject } from 'react';

const ADJUSTING_KEYS = new Set(['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown']);

/**
 * Commit-on-release for a range input whose value is also pushed by the server.
 *
 * An interaction starts on pointer-down or the first adjusting key and ends on whatever
 * finishes it: pointer-up, a cancelled gesture, lost pointer capture, blur, or key-up. The
 * value is committed once, and only if it differs from the value at the start, so tabbing
 * through a slider or tapping without moving never sends a request. While an interaction is
 * active, `interactingRef` (owned by the caller, who reads it to hold off server updates) is true
 * so incoming server values do not overwrite the thumb.
 */
export function useSliderCommit(interactingRef: RefObject<boolean>, onCommit: (value: number) => void) {
  const startValueRef = useRef<number | null>(null);
  const onCommitRef = useRef(onCommit);

  useEffect(() => {
    onCommitRef.current = onCommit;
  });

  const begin = (input: HTMLInputElement) => {
    if (interactingRef.current) return;
    interactingRef.current = true;
    startValueRef.current = Number(input.value);
  };

  const end = (input: HTMLInputElement) => {
    if (!interactingRef.current) return;
    interactingRef.current = false;
    const value = Number(input.value);
    const changed = value !== startValueRef.current;
    startValueRef.current = null;
    if (changed) onCommitRef.current(value);
  };

  return {
    onPointerDown: (event: PointerEvent<HTMLInputElement>) => begin(event.currentTarget),
    onPointerUp: (event: PointerEvent<HTMLInputElement>) => end(event.currentTarget),
    onPointerCancel: (event: PointerEvent<HTMLInputElement>) => end(event.currentTarget),
    onLostPointerCapture: (event: PointerEvent<HTMLInputElement>) => end(event.currentTarget),
    onBlur: (event: FocusEvent<HTMLInputElement>) => end(event.currentTarget),
    onKeyDown: (event: KeyboardEvent<HTMLInputElement>) => {
      if (ADJUSTING_KEYS.has(event.key)) begin(event.currentTarget);
    },
    onKeyUp: (event: KeyboardEvent<HTMLInputElement>) => {
      if (ADJUSTING_KEYS.has(event.key)) end(event.currentTarget);
    },
  };
}
