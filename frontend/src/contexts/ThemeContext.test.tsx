import { act, memo, useEffect, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import { ThemeProvider } from './ThemeContext';
import { useThemeContext } from './useThemeContext';

globalThis.IS_REACT_ACT_ENVIRONMENT = true;

const renderStats = { consumerCount: 0 };

const ThemeConsumer = memo(function ThemeConsumer() {
  useThemeContext();
  useEffect(() => {
    renderStats.consumerCount += 1;
  });
  return null;
});

function RerenderingThemeProvider() {
  const [, setRender] = useState(0);

  return (
    <>
      <button type="button" onClick={() => setRender((count) => count + 1)}>Rerender</button>
      <ThemeProvider><ThemeConsumer /></ThemeProvider>
    </>
  );
}

describe('ThemeProvider', () => {
  let container: HTMLDivElement;
  let root: Root;

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    window.localStorage.removeItem('clumoove-theme-preference');
  });

  it('does not update consumers when its parent rerenders without a theme change', () => {
    renderStats.consumerCount = 0;
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
    act(() => root.render(<RerenderingThemeProvider />));

    act(() => container.querySelector('button')?.click());

    expect(renderStats.consumerCount).toBe(1);
  });
});
