import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useTheme } from './useTheme';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

function ThemeHarness() {
  const { effectiveTheme, preference, setPreference } = useTheme();

  return (
    <button
      type="button"
      data-preference={preference}
      data-theme={effectiveTheme}
      onClick={() => setPreference('dark')}
    >
      Set dark theme
    </button>
  );
}

describe('useTheme', () => {
  let container: HTMLDivElement;
  let root: Root;
  const previousTheme = document.documentElement.getAttribute('data-theme');

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    document.documentElement.toggleAttribute('data-theme', previousTheme !== null);
    if (previousTheme !== null) document.documentElement.setAttribute('data-theme', previousTheme);
    vi.restoreAllMocks();
  });

  it('keeps the in-memory preference usable when local storage is unavailable', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('Storage unavailable');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('Storage unavailable');
    });
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<ThemeHarness />));

    const button = container.querySelector('button');
    expect(button?.dataset).toMatchObject({ preference: 'auto' });
    act(() => button?.click());

    expect(button?.dataset).toMatchObject({ preference: 'dark', theme: 'dark' });
    expect(document.documentElement.dataset.theme).toBe('dark');
  });
});
